// SPDX-License-Identifier: Apache-2.0

// Package ffadapter is the packaged media adapter (MEDIA-02): it implements
// addetect.Source over FFmpeg's libavformat/libavcodec through cgo, for
// local recordings and for local HLS/DASH presentations of CMAF resources.
//
// No ffmpeg process is started. The public detection API never exposes
// FFmpeg structures; everything crosses as addetect observations.
package ffadapter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/manifest"
)

// AdapterName and AdapterVersion identify this adapter in results and in
// the feature identity.
const (
	AdapterName    = "ffmpeg-libav"
	AdapterVersion = "1"
)

// Source is an addetect.Source for a recording path or a manifest. It may be
// opened concurrently. Apart from the list of local files it has read
// (LocalInputs) it holds no per-analysis state.
type Source struct {
	path     string
	resolver manifest.Resolver
	manifest bool

	mu     sync.Mutex
	inputs []string
}

// noteInput records a local file the adapter reads.
func (s *Source) noteInput(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.inputs {
		if p == path {
			return
		}
	}
	s.inputs = append(s.inputs, path)
}

// LocalInputs returns the local files this Source has read so far: the
// recording, or the manifests and media resources of a package read through
// the built-in file resolver. Callers use it to refuse writing an output
// over an input. Resources read through a caller-provided resolver are not
// listed.
func (s *Source) LocalInputs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.inputs...)
}

// recordingResolver is the built-in file resolver, noting each local file.
type recordingResolver struct {
	manifest.FileResolver
	src *Source
}

func (r recordingResolver) Open(ctx context.Context, uri string, offset, length int64) (io.ReadCloser, error) {
	if path, err := manifest.LocalPath(uri); err == nil {
		r.src.noteInput(path)
	}
	return r.FileResolver.Open(ctx, uri, offset, length)
}

// Path returns a Source for a local recording, or for a local HLS (.m3u8)
// or DASH (.mpd) manifest, detected by extension or content.
func Path(path string) *Source { return &Source{path: path} }

// Package returns a Source for an HLS/DASH manifest whose manifests and
// media resources are read through resolver (API-01). The resolver stays
// owned by the caller; every reader it returns is closed by the adapter,
// and its Open calls receive the analysis context for cancellation.
func Package(manifestURI string, resolver manifest.Resolver) *Source {
	return &Source{path: manifestURI, resolver: resolver, manifest: true}
}

func adapterInfo() addetect.AdapterInfo {
	return addetect.AdapterInfo{
		Name: AdapterName, Version: AdapterVersion,
		Libraries: map[string]string{libraryFFmpeg: libraryVersions()},
	}
}

// looksLikeManifest sniffs the head of a local file.
func looksLikeManifest(path string) bool {
	// Only regular files are sniffed: reading from a pipe would consume it.
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [sniffLen]byte
	n, _ := io.ReadFull(f, head[:])
	b := bytes.TrimLeft(bytes.TrimPrefix(head[:n], []byte("\xef\xbb\xbf")), " \t\r\n")
	return bytes.HasPrefix(b, []byte("#EXTM3U")) || (bytes.HasPrefix(b, []byte("<")) && bytes.Contains(b, []byte("<MPD")))
}

// Open implements addetect.Source.
func (s *Source) Open(ctx context.Context, req *addetect.OpenRequest) (addetect.Stream, error) {
	if s.path == "" {
		return nil, addetect.Errorf(addetect.KindInvalidConfig, "no input path")
	}
	isManifest := s.manifest || req.Package.ManifestType == addetect.ManifestHLS || req.Package.ManifestType == addetect.ManifestDASH
	if !isManifest && s.resolver == nil {
		if manifest.IsRemote(s.path) {
			return nil, addetect.Errorf(addetect.KindUnsupported, "remote inputs are not supported: %s", s.path)
		}
		isManifest = manifest.IsManifestPath(s.path) || looksLikeManifest(s.path)
	}
	if isManifest {
		return openPackage(ctx, s, req)
	}
	s.noteInput(s.path)
	return openFile(ctx, s.path, req)
}

// fileStream reads a single-file recording.
type fileStream struct {
	ctx  context.Context
	r    *reader
	info addetect.MediaInfo
	warn func(addetect.Warning)
	// Truncation check: the span of all media observed so far and of the
	// video alone, the video stream's own declared duration, and whether
	// every audio/video stream of the input is being read.
	first, end     int64
	seen           bool
	firstV, endV   int64
	seenV          bool
	videoDeclared  int64
	readsAllTracks bool
}

func openFile(ctx context.Context, path string, req *addetect.OpenRequest) (addetect.Stream, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, addetect.Errorf(addetect.KindIO, "cannot open input: %w", err)
	}
	if st.IsDir() {
		return nil, addetect.Errorf(addetect.KindIO, "input %s is a directory", path)
	}
	opt := baseOptions(req)
	r, err := openReader(ctx, path, nil, &opt, req.AnalysisMaxWidth*req.AnalysisMaxHeight)
	if err != nil {
		return nil, err
	}
	fs := &fileStream{ctx: ctx, r: r, warn: req.Warn, videoDeclared: int64(r.info.video_duration_us)}
	read := 0
	for _, idx := range [...]int{int(r.info.video_index), int(r.info.audio_index)} {
		if idx >= 0 {
			read++
		}
	}
	fs.readsAllTracks = read == int(r.info.n_av_streams)
	fs.info.Adapter = adapterInfo()
	container := cstr(&r.info.container[0])
	if i := strings.IndexByte(container, ','); i > 0 && strings.Contains(container, containerMP4) {
		container = containerMP4
	}
	fs.info.Input = addetect.InputInfo{
		Kind: addetect.InputKindFile, Path: path, Container: container,
		Identity: fmt.Sprintf("size=%d;mtime=%d", st.Size(), st.ModTime().UnixNano()),
	}
	r.describe(&fs.info, req)
	if err := r.checkCapabilities(req, true); err != nil {
		r.close()
		return nil, err
	}
	return fs, nil
}

func (s *fileStream) Info() *addetect.MediaInfo { return &s.info }

func (s *fileStream) Next(ev *addetect.Event) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if err := s.r.next(s.ctx); err != nil {
		return err
	}
	if !s.r.fill(ev) {
		s.checkTruncated()
		return io.EOF
	}
	switch ev.Kind {
	case addetect.EventVideo:
		if !ev.Video.NoPTS {
			start, end := ev.Video.PTS, ev.Video.PTS+ev.Video.Duration
			s.observe(start, end)
			if !s.seenV || start < s.firstV {
				s.firstV = start
			}
			if !s.seenV || end > s.endV {
				s.endV = end
			}
			s.seenV = true
		}
	case addetect.EventAudio:
		if n := len(ev.Audio); n > 0 {
			s.observe(ev.Audio[0].StartUS, ev.Audio[n-1].StartUS+int64(ev.Audio[n-1].DurationUS))
		}
	}
	return nil
}

func (s *fileStream) observe(start, end int64) {
	if !s.seen || start < s.first {
		s.first = start
	}
	if !s.seen || end > s.end {
		s.end = end
	}
	s.seen = true
}

// checkTruncated reports an input whose media ends well before the duration
// its container declares, so a cut-off recording is never presented as a
// complete analysis (QUAL-04).
//
// The video stream's own declared duration is compared with the video that
// was presented. Containers without per-stream durations are checked through
// the container duration, but only when every audio/video stream is read
// (an unread track may legitimately outlast the others). A declared duration
// may be a span or, in containers that count from timestamp zero, an end
// time; the input is only flagged when it falls short under both readings.
func (s *fileStream) checkTruncated() {
	var declared, first, end int64
	switch {
	case s.videoDeclared > 0 && s.seenV:
		declared, first, end = s.videoDeclared, s.firstV, s.endV
	case s.info.DurationHintUS > 0 && s.seen && s.readsAllTracks:
		declared, first, end = s.info.DurationHintUS, s.first, s.end
	default:
		return
	}
	missing := declared - (end - first)
	if first > 0 {
		missing = min(missing, declared-end)
	}
	if missing > max(truncationSlackUS, declared/truncationSlackDivisor) {
		s.info.Incomplete = true
		s.warn(addetect.Warning{Code: addetect.WarnTruncatedInput, Message: fmt.Sprintf(
			"the input ends %.1f s before the %.1f s its container declares; the result covers only the media that is present",
			float64(missing)/usPerSecond, float64(declared)/usPerSecond)})
	}
}

func (s *fileStream) Close() error {
	s.r.close()
	return nil
}
