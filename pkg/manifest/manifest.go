// SPDX-License-Identifier: Apache-2.0

// Package manifest resolves finite HLS (.m3u8) and static DASH (.mpd)
// presentations into an ordered resource model: initialization resources and
// media segments in manifest presentation order, grouped into continuity
// epochs (HLS discontinuity epochs, DASH Periods).
//
// The package only interprets manifests. It never opens or parses media
// resources; a media adapter consumes the returned [Presentation] and reads
// the listed byte ranges itself.
//
// Unsupported inputs (growing/live playlists, dynamic MPDs, encrypted media,
// SegmentBase/sidx addressing, remote resources without AllowRemote) fail
// with [ErrUnsupported] instead of yielding a silently incomplete result.
package manifest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
)

// Format identifies the manifest family of a presentation.
type Format string

// Supported manifest formats.
const (
	FormatHLS  Format = "hls"
	FormatDASH Format = "dash"
)

// Defaults applied by [Load] when the corresponding [Options] field is zero.
const (
	DefaultMaxManifestBytes int64 = 16 << 20
	DefaultMaxSegments            = 1_000_000
	DefaultMinHeight              = 360
)

// Resolver reads manifest and media resources.
type Resolver interface {
	// Open returns a reader over length bytes of the resource starting at offset.
	// length < 0 means "through the end of the resource". The caller closes it.
	Open(ctx context.Context, uri string, offset, length int64) (io.ReadCloser, error)
}

// Fingerprinter is an optional extension of a Resolver: it returns a cheap
// version token for a resource, one that changes when the resource is
// replaced (for local files, size and modification time). Package identity
// uses it so that a cache keyed on that identity notices replaced media
// without reading every byte twice.
type Fingerprinter interface {
	Fingerprint(ctx context.Context, uri string) (string, error)
}

// Resource addresses a whole resource or one byte range of it.
type Resource struct {
	URI    string // resolved URI or local path
	Offset int64  // 0 for a whole resource
	Length int64  // -1 = through end of resource
}

// Segment is one media segment in presentation order.
type Segment struct {
	Resource
	InitIndex  int   // index into Track.Inits; -1 when no initialization resource applies
	Epoch      int   // index into Presentation.Epochs
	Number     int64 // HLS media sequence number / DASH segment number
	StartUS    int64 // declared presentation start, microseconds from presentation start
	DurationUS int64 // declared duration in microseconds
	Gap        bool  // HLS EXT-X-GAP
}

// Epoch is a continuity epoch: an HLS discontinuity epoch or a DASH Period.
type Epoch struct {
	ID      string // DASH Period@id (or its index as a string); HLS discontinuity sequence number
	StartUS int64  // declared start on the presentation timeline (µs)
	EndUS   int64  // declared end (µs); -1 if unknown
}

// TrackEpoch is per-track timing/identity inside one Epoch (parallel to Presentation.Epochs).
type TrackEpoch struct {
	Available              bool   // false: the track has no media in this epoch
	RepresentationID       string // DASH Representation@id; HLS: ""
	Timescale              int64  // DASH timescale of PresentationTimeOffset; 0 for HLS
	PresentationTimeOffset int64  // DASH, in Timescale units; 0 for HLS
}

// Track is one selected logical media track.
type Track struct {
	ID          string // HLS: variant index as "v<N>" / rendition "GROUP-ID/NAME"; DASH: first Representation@id
	Name        string
	Language    string
	Codecs      string
	Bandwidth   int64
	Width       int
	Height      int
	FrameRate   string
	ManifestURI string     // media playlist URI (HLS) or MPD URI (DASH)
	Inits       []Resource // distinct initialization resources in first-use order
	Segments    []Segment  // presentation order
	Epochs      []TrackEpoch
}

// InitFor returns the initialization resource that applies to s, if any.
func (t *Track) InitFor(s *Segment) (Resource, bool) {
	if s.InitIndex < 0 || s.InitIndex >= len(t.Inits) {
		return Resource{}, false
	}
	return t.Inits[s.InitIndex], true
}

// Snapshot identifies one manifest read (for cache identity).
type Snapshot struct {
	URI    string
	Size   int64
	SHA256 string // lower-case hex
}

// CaptionService is an informational caption service declaration
// (e.g. HLS INSTREAM-ID "CC1").
type CaptionService struct {
	ID, Name, Language string
}

// Marker is a manifest ad marker kept as a diagnostic only. Markers never
// influence detection.
type Marker struct {
	Kind   string // e.g. "EXT-X-DATERANGE", "EXT-X-CUE-OUT", "EventStream"
	TimeUS int64  // declared presentation time, -1 if unknown
	Detail string
}

// Presentation is a resolved finite presentation.
type Presentation struct {
	Format     Format
	Manifests  []Snapshot
	Epochs     []Epoch
	Video      *Track
	Audio      *Track // nil when audio is muxed into the video resources or absent
	AudioMuxed bool   // HLS: audio is carried in the video variant's own segments (or unknown-but-possible); DASH: false
	Captions   []CaptionService
	Markers    []Marker
	Warnings   []string
	Selection  SelectionReport
}

// SelectionReport describes how the tracks were chosen.
type SelectionReport struct {
	VideoRule       string   // the rule applied, e.g. "highest", "id=720p"
	AudioRule       string   // "default", "none", "lang=en", ...
	VideoCandidates []string // human-readable candidate list in manifest order (for diagnostics)
	Notes           []string
}

// Options controls loading and selection.
type Options struct {
	// Video selects the variant/Representation:
	//
	//   - "" or "highest": highest resolution (pixel count), then highest
	//     bandwidth, then first in manifest order.
	//   - "lowest": lowest resolution with Height >= MinHeight, then lowest
	//     bandwidth, then first; falls back to "highest" if none qualifies.
	//   - "index=<n>": 0-based manifest order among full-video candidates.
	//   - "id=<x>": DASH Representation@id; HLS track ID ("v<N>"), NAME, or
	//     the variant URI (as written or resolved).
	//   - "bandwidth=<bps>": exact match, first in manifest order.
	//
	// In a multi-Period MPD the rule is applied in the first Period that has
	// video. Later Periods prefer the Representation with the same @id, then
	// one with the same width/height/codecs, and otherwise re-apply the rule
	// (falling back to "highest") with a warning.
	Video     string
	MinHeight int // used by "lowest"; default 360

	// Audio selects the rendition/AdaptationSet:
	//
	//   - "": HLS: the DEFAULT=YES rendition of the variant's AUDIO group,
	//     else the first rendition of that group. DASH: the first audio
	//     AdaptationSet in manifest order and its highest-bandwidth
	//     Representation (first on ties).
	//   - "none": no separate audio track is resolved.
	//   - "lang=<code>": first rendition/AdaptationSet whose language equals
	//     the code case-insensitively, else whose primary subtag matches.
	//   - "name=<NAME>": HLS NAME / DASH AdaptationSet Label.
	//   - "id=<id>": HLS "GROUP-ID/NAME" (or rendition URI as written);
	//     DASH Representation@id, else AdaptationSet@id.
	//
	// Across DASH Periods the same @id is preferred, then the same
	// language and codecs; otherwise the rule is re-applied with a warning,
	// and a Period with no matching audio is marked unavailable.
	Audio string

	AllowRemote      bool
	MaxManifestBytes int64 // default 16 MiB
	MaxSegments      int   // default 1_000_000; exceeding → resource-limit error (video + audio combined)
}

// Sentinel error kinds. Errors returned by [Load] wrap exactly one of these,
// except cancellation, which wraps only the context's error. I/O errors from
// the Resolver wrap ErrIO and keep the underlying error in the chain.
var (
	ErrUnsupported   = errors.New("manifest: unsupported")    // live/growing, encrypted, SegmentBase, remote without AllowRemote...
	ErrInvalid       = errors.New("manifest: invalid")        // malformed manifest
	ErrIO            = errors.New("manifest: i/o")            // resolver failure
	ErrResourceLimit = errors.New("manifest: resource limit") // manifest size or segment count exceeded
	ErrSelection     = errors.New("manifest: selection")      // an explicit selection matched nothing (or is malformed)
)

// errf builds an error wrapping kind.
func errf(kind error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", kind, fmt.Sprintf(format, args...))
}

// IsManifestPath reports whether a path looks like a manifest by extension
// (.m3u8, .m3u, .mpd), case-insensitive. Any query or fragment of a URI is
// ignored.
func IsManifestPath(path string) bool {
	if uriScheme(path) != "" {
		if i := strings.IndexAny(path, "?#"); i >= 0 {
			path = path[:i]
		}
	}
	ext := filepath.Ext(path)
	return strings.EqualFold(ext, ".m3u8") || strings.EqualFold(ext, ".m3u") || strings.EqualFold(ext, ".mpd")
}

// loader carries the state of one Load call.
type loader struct {
	ctx       context.Context
	r         Resolver
	opts      Options
	vrule     videoRule
	arule     audioRule
	p         *Presentation
	segBudget int // segments that may still be added before MaxSegments is exceeded
}

// Load detects the format (by content: "#EXTM3U" prefix vs XML root <MPD>;
// the extension is only a hint), reads all needed manifests through r,
// applies selection and returns the presentation. A nil r uses [FileResolver].
func Load(ctx context.Context, uri string, r Resolver, opts Options) (*Presentation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		r = FileResolver{}
	}
	if opts.MaxManifestBytes <= 0 {
		opts.MaxManifestBytes = DefaultMaxManifestBytes
	}
	if opts.MaxSegments <= 0 {
		opts.MaxSegments = DefaultMaxSegments
	}
	if opts.MinHeight <= 0 {
		opts.MinHeight = DefaultMinHeight
	}
	l := &loader{ctx: ctx, r: r, opts: opts, p: &Presentation{}, segBudget: opts.MaxSegments}
	var err error
	if l.vrule, err = parseVideoRule(opts.Video); err != nil {
		return nil, err
	}
	if l.arule, err = parseAudioRule(opts.Audio); err != nil {
		return nil, err
	}
	l.p.Selection.VideoRule = l.vrule.String()
	l.p.Selection.AudioRule = l.arule.String()

	top, err := l.normalizeTop(uri)
	if err != nil {
		return nil, err
	}
	format, body, err := l.readManifest(top)
	if err != nil {
		return nil, err
	}
	l.p.Format = format
	switch format {
	case FormatHLS:
		err = l.loadHLS(top, string(body))
	default:
		err = l.loadDASH(top, body)
	}
	if err != nil {
		return nil, err
	}
	return l.p, nil
}

// canceled wraps a context error.
func canceled(err error) error {
	return fmt.Errorf("manifest: load interrupted: %w", err)
}

// ioErr classifies a resolver failure.
func (l *loader) ioErr(op, uri string, err error) error {
	if cerr := l.ctx.Err(); cerr != nil {
		return canceled(cerr)
	}
	for _, kind := range [...]error{ErrUnsupported, ErrInvalid, ErrIO, ErrResourceLimit, ErrSelection} {
		if errors.Is(err, kind) {
			return err
		}
	}
	return fmt.Errorf("%w: %s %s: %w", ErrIO, op, uri, err)
}

// readManifest reads one manifest (bounded), records its snapshot and
// detects its format. The returned body has any byte-order mark and leading
// whitespace removed.
func (l *loader) readManifest(uri string) (Format, []byte, error) {
	if err := l.ctx.Err(); err != nil {
		return "", nil, canceled(err)
	}
	rc, err := l.r.Open(l.ctx, uri, 0, -1)
	if err != nil {
		return "", nil, l.ioErr("open", uri, err)
	}
	limit := l.opts.MaxManifestBytes
	var hint int64
	if st, ok := rc.(interface{ Stat() (fs.FileInfo, error) }); ok {
		if fi, serr := st.Stat(); serr == nil && fi.Mode().IsRegular() {
			hint = fi.Size()
		}
	}
	data, exceeded, err := readLimited(rc, hint, limit)
	_ = rc.Close()
	if err != nil {
		return "", nil, l.ioErr("read", uri, err)
	}
	if exceeded {
		return "", nil, errf(ErrResourceLimit, "manifest %s exceeds %d bytes", uri, limit)
	}
	sum := sha256.Sum256(data)
	l.p.Manifests = append(l.p.Manifests, Snapshot{URI: uri, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})

	format, body, ok := detectFormat(data)
	if !ok {
		return "", nil, errf(ErrInvalid, "%s: not an HLS playlist (#EXTM3U) or a DASH MPD", uri)
	}
	return format, body, nil
}

// readLimited reads r until EOF or until more than limit bytes were seen.
func readLimited(r io.Reader, hint, limit int64) (data []byte, exceeded bool, err error) {
	capacity := int64(4096)
	if hint > 0 {
		capacity = hint + 1 // +1 so EOF is observed without growing
	}
	if capacity > limit+1 {
		capacity = limit + 1
	}
	data = make([]byte, 0, capacity)
	for {
		if len(data) == cap(data) {
			if int64(len(data)) > limit {
				return nil, true, nil
			}
			grown := int64(cap(data)) * 2
			if grown > limit+1 {
				grown = limit + 1
			}
			next := make([]byte, len(data), grown)
			copy(next, data)
			data = next
		}
		n, rerr := r.Read(data[len(data):cap(data)])
		data = data[:len(data)+n]
		if rerr == io.EOF {
			return data, int64(len(data)) > limit, nil
		}
		if rerr != nil {
			return nil, false, rerr
		}
	}
}

// detectFormat classifies manifest content and strips a UTF-8 byte-order
// mark and leading whitespace.
func detectFormat(data []byte) (Format, []byte, bool) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	i := 0
	for i < len(data) && (data[i] == ' ' || data[i] == '\t' || data[i] == '\r' || data[i] == '\n') {
		i++
	}
	data = data[i:]
	const magic = "#EXTM3U"
	if len(data) >= len(magic) && string(data[:len(magic)]) == magic {
		if len(data) == len(magic) || data[len(magic)] <= ' ' {
			return FormatHLS, data, true
		}
		return "", nil, false
	}
	if len(data) == 0 || data[0] != '<' {
		return "", nil, false
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.RawToken()
		if err != nil {
			return "", nil, false
		}
		if se, ok := tok.(xml.StartElement); ok {
			if se.Name.Local == "MPD" {
				return FormatDASH, data, true
			}
			return "", nil, false
		}
	}
}

// warnf records a warning.
func (l *loader) warnf(format string, args ...any) {
	l.p.Warnings = append(l.p.Warnings, fmt.Sprintf(format, args...))
}

// notef records a selection note.
func (l *loader) notef(format string, args ...any) {
	l.p.Selection.Notes = append(l.p.Selection.Notes, fmt.Sprintf(format, args...))
}

// takeSegment consumes one unit of the segment budget.
func (l *loader) takeSegment() error {
	if l.segBudget <= 0 {
		return l.overLimit()
	}
	l.segBudget--
	return nil
}

func (l *loader) overLimit() error {
	return errf(ErrResourceLimit, "presentation has more than %d segments", l.opts.MaxSegments)
}

// initSet interns initialization resources in first-use order.
type initSet struct {
	list []Resource
	m    map[Resource]int
}

func (s *initSet) intern(r Resource) int {
	if s.m == nil {
		for i := range s.list {
			if s.list[i] == r {
				return i
			}
		}
		if len(s.list) < smallInitSet {
			s.list = append(s.list, r)
			return len(s.list) - 1
		}
		s.m = make(map[Resource]int, 2*len(s.list))
		for i, v := range s.list {
			s.m[v] = i
		}
	}
	if i, ok := s.m[r]; ok {
		return i
	}
	s.list = append(s.list, r)
	s.m[r] = len(s.list) - 1
	return len(s.list) - 1
}
