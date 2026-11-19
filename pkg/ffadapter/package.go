// SPDX-License-Identifier: Apache-2.0

package ffadapter

/*
#include "reader.h"
*/
import "C"

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"slices"
	"sort"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/manifest"
)

// Packaged input (section 3.1).
//
// The manifest package resolves the presentation into ordered resources.
// Each track is then demuxed as a sequence of "runs": a run is a maximal
// series of consecutive media segments that share one initialization
// resource and one continuity epoch (HLS discontinuity epoch / DASH Period).
// A run is fed to one demuxer as the byte concatenation of its
// initialization data and its segments, read strictly in manifest order
// through the resolver, so no transcoding, temporary file or manual
// concatenation is involved and at most one resource per track is open.
//
// Timeline mapping (PACK-06), per track and epoch:
//
//	DASH: presentation = sample_pts - presentationTimeOffset + Period start;
//	      samples outside [Period start, Period end) are trimmed.
//	HLS:  the first epoch keeps its sample timestamps; each later epoch is
//	      placed so its first video frame follows the previous epoch's last
//	      video frame (plus any declared-missing duration). Audio shares
//	      its epoch's video offset.
//
// Sample timestamps, never EXTINF/duration tags, define time inside an
// epoch, so there is no rounding drift.

type segItem struct {
	res manifest.Resource
	seg int // index into Track.Segments, -1 for initialization data
}

// segSource is the concatenated byte stream of one run.
type segSource struct {
	ps    *pkgStream
	ts    *trackState
	items []segItem
	ends  []int64 // cumulative end offset of each completed item
	cur   int
	rc    io.ReadCloser
	pos   int64
	got   int64     // bytes read of the current item
	initH hash.Hash // digest of initialization data
	segH  hash.Hash // digest of the current media resource (package.hash_media)
	init  int       // index into PackageInfo.Inits, -1 when the run has none
	cut   bool      // stream ended early at an unreadable resource (degraded)
	walk  boxWalk   // top-level box structure of the current item
}

// boxWalk follows the top-level ISO BMFF box structure of one resource as
// its bytes stream past. A CMAF resource is a sequence of whole boxes, so
// one that ends inside a box is truncated; without this check the demuxer
// would silently take the following resources' bytes for the missing
// payload and lose its place (PACK-08). Resources that do not start with a
// known box (for example MPEG-TS segments) are not checked.
type boxWalk struct {
	hdr    [boxLargeHeaderLen]byte
	n      int     // header bytes collected
	need   int     // header length: boxHeaderLen, or boxLargeHeaderLen with a 64-bit size
	remain int64   // payload bytes left in the current box; -1: through the end
	last   [4]byte // type of the last box header
	boxes  int
	off    bool // not ISO BMFF: no verdict
	bad    bool // impossible size field
}

func (b *boxWalk) reset() { *b = boxWalk{need: boxHeaderLen} }

func (b *boxWalk) write(p []byte) {
	for len(p) > 0 && !b.off && !b.bad && b.remain >= 0 {
		if b.remain > 0 {
			k := min(int64(len(p)), b.remain)
			b.remain -= k
			p = p[k:]
			continue
		}
		k := copy(b.hdr[b.n:b.need], p)
		b.n += k
		p = p[k:]
		if b.n < b.need {
			return
		}
		size := int64(binary.BigEndian.Uint32(b.hdr[:boxTypeOffset]))
		if size == boxSizeIsLarge && b.need == boxHeaderLen {
			b.need = boxLargeHeaderLen
			continue
		}
		if b.need == boxLargeHeaderLen {
			size = int64(binary.BigEndian.Uint64(b.hdr[boxHeaderLen:boxLargeHeaderLen]))
		}
		copy(b.last[:], b.hdr[boxTypeOffset:boxHeaderLen])
		if b.boxes == 0 && !slices.Contains(topLevelBoxes[:], string(b.last[:])) {
			b.off = true
			return
		}
		b.boxes++
		switch {
		case size == boxSizeToEnd && b.need == boxHeaderLen:
			b.remain = -1
		case size < int64(b.need):
			b.bad = true
		default:
			b.remain = size - int64(b.need)
		}
		b.n, b.need = 0, boxHeaderLen
	}
}

// truncated reports whether a fully read resource was empty, ended inside
// a box, or, for a media resource, ended directly after a movie fragment
// header whose sample data is therefore missing.
func (b *boxWalk) truncated(media bool) bool {
	if b.off {
		return false
	}
	return b.bad || b.n != 0 || b.remain > 0 || b.boxes == 0 || (media && string(b.last[:]) == boxMovieFragment)
}

func (s *segSource) closeCurrent() {
	if s.rc != nil {
		s.rc.Close()
		s.rc = nil
	}
}

func (s *segSource) read(p []byte) (int, error) {
	for {
		if s.cut || s.cur >= len(s.items) {
			return 0, nil
		}
		it := &s.items[s.cur]
		if s.rc == nil {
			if err := s.ps.ctx.Err(); err != nil {
				return 0, err
			}
			rc, err := s.ps.resolver.Open(s.ps.ctx, it.res.URI, it.res.Offset, it.res.Length)
			if err != nil {
				if e := s.ps.resourceFailed(s, it, err); e != nil {
					return 0, e
				}
				continue
			}
			s.rc, s.got = rc, 0
			s.walk.reset()
			if s.segH != nil {
				s.segH.Reset()
			}
		}
		n, err := s.rc.Read(p)
		s.pos += int64(n)
		s.got += int64(n)
		s.walk.write(p[:n])
		if n > 0 && it.seg < 0 {
			s.initH.Write(p[:n])
		} else if n > 0 && s.segH != nil {
			s.segH.Write(p[:n])
		}
		if err == io.EOF {
			s.closeCurrent()
			var short error
			switch {
			case it.res.Length >= 0 && s.got < it.res.Length:
				// The resource is shorter than its declared byte range.
				short = fmt.Errorf("short read: %d of %d bytes", s.got, it.res.Length)
			case s.walk.truncated(it.seg >= 0):
				short = fmt.Errorf("truncated: the resource ends inside its box structure after %d bytes", s.got)
			}
			if short != nil {
				if e := s.ps.resourceFailed(s, it, short); e != nil {
					return 0, e
				}
				if n > 0 {
					return n, nil
				}
				continue
			}
			s.ends = append(s.ends, s.pos)
			if it.seg >= 0 {
				s.ps.pk.Resources[s.ts.resBase+it.seg].Status = addetect.ResourceStatusOK
				if s.segH != nil {
					s.ps.mediaDigests[s.ts.resBase+it.seg] = hex.EncodeToString(s.segH.Sum(nil))
				}
			} else {
				s.ps.pk.Inits[s.init].Status = addetect.ResourceStatusOK
				s.ps.initDigests[s.init] = hex.EncodeToString(s.initH.Sum(nil))
			}
			s.cur++
			if n > 0 {
				return n, nil
			}
			continue
		}
		if err != nil {
			s.closeCurrent()
			if e := s.ps.resourceFailed(s, it, err); e != nil {
				return 0, e
			}
			if n > 0 {
				return n, nil
			}
			continue
		}
		if n > 0 {
			return n, nil
		}
	}
}

// itemAt maps a byte position of the concatenated stream to its item.
func (s *segSource) itemAt(pos int64) int {
	i := sort.Search(len(s.ends), func(i int) bool { return s.ends[i] > pos })
	if i >= len(s.items) {
		i = len(s.items) - 1
	}
	return i
}

type trackState struct {
	video    bool
	track    *manifest.Track
	resBase  int // index of the track's first media resource in PackageInfo.Resources
	initBase int
	next     int // next segment to schedule
	run      *reader
	src      *segSource
	epoch    int
	done     bool
	time     int64 // presentation time reached (interleaving)
	opt      C.tvad_options
	offsets  []int64 // DASH: per-epoch media->presentation offset
	lost     []lostSeg
}

// lostSeg is a media segment found unreadable while its run was still
// decoding; it is recorded once the run's last frame has been presented.
type lostSeg struct {
	seg    int
	reason string
}

type pkgStream struct {
	ctx      context.Context
	req      *addetect.OpenRequest
	resolver manifest.Resolver
	pres     *manifest.Presentation
	info     addetect.MediaInfo
	pk       *addetect.PackageInfo
	hls      bool

	video, audio *trackState
	// HLS epoch offsets, established by each epoch's first video frame.
	offsets []int64
	offOK   []bool

	haveVideo   bool
	videoEnd    int64 // presentation end of the last video frame
	lastDelta   int64
	pendingGap  int64 // declared-missing video duration since the last frame
	gaps        []addetect.GapObservation
	stash       []addetect.AudioWindow // muxed audio seen before its epoch offset
	stashEpoch  int
	stashSent   bool
	described   bool
	initDigests map[int]string
	// mediaDigests holds the digest of each media resource read in full,
	// by resource index (package.hash_media only).
	mediaDigests map[int]string
	lumaCap      int
	finished     bool
	tailChecked  bool
	closed       bool
	trimmed      int // video frames outside their Period bounds
}

func mapManifestErr(err error) error {
	var ae *addetect.Error
	if errors.As(err, &ae) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return addetect.Errorf(addetect.KindCanceled, msgCanceled, err)
	}
	kind := addetect.KindIO
	switch {
	case errors.Is(err, manifest.ErrUnsupported), errors.Is(err, manifest.ErrInvalid):
		kind = addetect.KindUnsupported
	case errors.Is(err, manifest.ErrResourceLimit):
		kind = addetect.KindResourceLimit
	case errors.Is(err, manifest.ErrSelection):
		kind = addetect.KindInvalidConfig
	}
	return addetect.Errorf(kind, "%w", err)
}

func openPackage(ctx context.Context, s *Source, req *addetect.OpenRequest) (addetect.Stream, error) {
	resolver := s.resolver
	if resolver == nil {
		if req.Package.AllowRemote {
			return nil, addetect.Errorf(addetect.KindUnsupported, "package.allow_remote requires a caller-provided resolver; the built-in resolver reads local files only")
		}
		resolver = recordingResolver{src: s}
	}
	audioSel := req.Package.AudioSelection
	if audioSel == addetect.SelectionAuto {
		audioSel = ""
	}
	if !req.NeedAudio || req.AudioTrack == addetect.TrackNone {
		audioSel = manifest.AudioNone
	}
	pres, err := manifest.Load(ctx, s.path, resolver, manifest.Options{
		// manifest.Load reads MinHeight <= 0 as "use the default (360)";
		// an explicit package.min_height=0 means no minimum.
		Video: req.Package.VideoSelection, MinHeight: max(req.Package.MinHeight, 1), Audio: audioSel,
		AllowRemote: req.Package.AllowRemote, MaxManifestBytes: req.Package.MaxManifestBytes,
		MaxSegments: req.Package.MaxSegments,
	})
	if err != nil {
		return nil, mapManifestErr(err)
	}
	if want := req.Package.ManifestType; want != addetect.ManifestAuto && want != "" && want != string(pres.Format) {
		return nil, addetect.Errorf(addetect.KindInvalidConfig, "package.manifest_type=%s but the manifest is %s", want, pres.Format)
	}
	if pres.Video == nil || len(pres.Video.Segments) == 0 {
		return nil, addetect.Errorf(addetect.KindUnsupported, "the presentation has no video media")
	}
	for _, w := range pres.Warnings {
		req.Warn(addetect.Warning{Code: addetect.WarnManifest, Message: w})
	}

	ps := &pkgStream{
		ctx: ctx, req: req, resolver: resolver, pres: pres, hls: pres.Format == manifest.FormatHLS,
		offsets: make([]int64, len(pres.Epochs)), offOK: make([]bool, len(pres.Epochs)),
		initDigests: make(map[int]string), mediaDigests: make(map[int]string), lumaCap: req.AnalysisMaxWidth * req.AnalysisMaxHeight,
	}
	ps.info.Adapter = adapterInfo()
	ps.info.Input = addetect.InputInfo{Kind: string(pres.Format), Path: s.path, Container: containerCMAF}
	pk := &addetect.PackageInfo{
		Format: string(pres.Format), Notes: pres.Selection.Notes,
		Inits: []addetect.PackageResource{}, Resources: []addetect.PackageResource{},
	}
	ps.pk, ps.info.Package = pk, pk
	for _, m := range pres.Manifests {
		pk.Manifests = append(pk.Manifests, addetect.ManifestSnapshot{URI: m.URI, Size: m.Size, SHA256: m.SHA256})
	}
	trackOut := func(t *manifest.Track, rule string) addetect.PackageTrack {
		return addetect.PackageTrack{ID: t.ID, Name: t.Name, Language: t.Language, Codecs: t.Codecs,
			Bandwidth: t.Bandwidth, Width: t.Width, Height: t.Height, Rule: rule, Manifest: t.ManifestURI}
	}
	rule := pres.Selection.VideoRule
	if rule == "" {
		rule = addetect.SelectionHighest
	}
	pk.Video = trackOut(pres.Video, rule)
	if pres.Audio != nil {
		arule := pres.Selection.AudioRule
		if arule == "" {
			arule = audioRuleDefault
		}
		at := trackOut(pres.Audio, arule)
		pk.Audio = &at
	}
	for i, e := range pres.Epochs {
		pe := addetect.PackageEpoch{ID: e.ID, DeclaredStartUS: e.StartUS}
		if e.EndUS >= 0 {
			pe.DeclaredEndUS = &pres.Epochs[i].EndUS
		}
		if i < len(pres.Video.Epochs) {
			pe.VideoID = pres.Video.Epochs[i].RepresentationID
		}
		if pres.Audio != nil && i < len(pres.Audio.Epochs) {
			pe.AudioID, pe.AudioAvailable = pres.Audio.Epochs[i].RepresentationID, pres.Audio.Epochs[i].Available
		} else {
			pe.AudioAvailable = pres.AudioMuxed
		}
		pk.Epochs = append(pk.Epochs, pe)
	}
	for _, m := range pres.Markers {
		pm := addetect.PackageMarker{Kind: m.Kind, Detail: m.Detail}
		if m.TimeUS >= 0 {
			t := m.TimeUS
			pm.TimeUS = &t
		}
		pk.Markers = append(pk.Markers, pm)
	}
	addTrack := func(t *manifest.Track, name string, video bool) *trackState {
		ts := &trackState{video: video, track: t, resBase: len(pk.Resources), initBase: len(pk.Inits), opt: baseOptions(req)}
		for i, r := range t.Inits {
			pk.Inits = append(pk.Inits, addetect.PackageResource{Index: ts.initBase + i, Track: name, URI: r.URI, Offset: r.Offset, Length: r.Length, Init: -1, Epoch: -1, Status: addetect.ResourceStatusUnread})
		}
		for i := range t.Segments {
			sg := &t.Segments[i]
			init := -1
			if sg.InitIndex >= 0 {
				init = ts.initBase + sg.InitIndex
			}
			pk.Resources = append(pk.Resources, addetect.PackageResource{
				Index: ts.resBase + i, Track: name, URI: sg.URI, Offset: sg.Offset, Length: sg.Length, Init: init,
				Epoch: sg.Epoch, DeclaredStartUS: sg.StartUS, DeclaredDurationUS: sg.DurationUS, Status: addetect.ResourceStatusUnread,
			})
		}
		if !ps.hls {
			ts.offsets = make([]int64, len(pres.Epochs))
			for e := range pres.Epochs {
				var pto int64
				if e < len(t.Epochs) && t.Epochs[e].Timescale > 0 {
					// Round to nearest microsecond, as sample timestamps are.
					num := new(big.Int).Mul(big.NewInt(t.Epochs[e].PresentationTimeOffset), big.NewInt(2*usPerSecond))
					num.Add(num, big.NewInt(t.Epochs[e].Timescale))
					pto = num.Div(num, big.NewInt(2*t.Epochs[e].Timescale)).Int64()
				}
				ts.offsets[e] = pres.Epochs[e].StartUS - pto
			}
		}
		return ts
	}
	ps.video = addTrack(pres.Video, addetect.PackageTrackVideo, true)
	if pres.Audio != nil {
		ps.video.opt.audio_index = C.TVAD_TRACK_NONE
		ps.audio = addTrack(pres.Audio, addetect.PackageTrackAudio, false)
		ps.audio.opt.video_index = C.TVAD_TRACK_NONE
		ps.audio.opt.caption_index = C.TVAD_TRACK_NONE
		ps.audio.opt.audio_index = C.TVAD_TRACK_AUTO
	}
	if last := pres.Epochs[len(pres.Epochs)-1]; last.EndUS > pres.Epochs[0].StartUS {
		ps.info.DurationHintUS = last.EndUS - pres.Epochs[0].StartUS
	}
	// Open the first video run now so track metadata is available and
	// unsupported media fails before analysis starts.
	if err := ps.startRun(ps.video); err != nil {
		ps.Close()
		return nil, err
	}
	if !ps.described {
		ps.Close()
		return nil, addetect.Errorf(addetect.KindIO, "no video resource of the presentation could be read")
	}
	return ps, nil
}

func (ps *pkgStream) Info() *addetect.MediaInfo { return &ps.info }

// resourceFailed applies the package failure policy (PACK-08) to an
// unreadable resource. It returns an error under the default "fail" policy;
// in degraded mode it records the loss and lets the stream continue.
func (ps *pkgStream) resourceFailed(s *segSource, it *segItem, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	kind := resourceKindMedia
	if it.seg < 0 {
		kind = resourceKindInit
	}
	if !ps.req.Package.Degraded {
		return addetect.Errorf(addetect.KindIO, "package %s resource %s is unreadable (package.failure_policy=fail): %w", kind, it.res.URI, cause)
	}
	ps.info.Incomplete = true
	ps.req.Warn(addetect.Warning{Code: addetect.WarnResourceUnreadable, Message: fmt.Sprintf("%s resource %s is unreadable: %v", kind, it.res.URI, cause)})
	// Everything from the failed item to the end of the run is lost: an
	// unreadable initialization loses the whole run, a media resource ends
	// the run so the next one restarts cleanly at a fragment boundary.
	first := s.cur
	last := s.cur
	if it.seg < 0 {
		last = len(s.items) - 1
		ps.pk.Inits[s.init].Status, ps.pk.Inits[s.init].Reason = addetect.ResourceStatusMissing, cause.Error()
	}
	// The demuxer reads ahead of the decoder: frames of this run are still
	// to be presented, so the loss is placed on the timeline when the run
	// ends (flushLost), after its last frame.
	for k := first; k <= last; k++ {
		if seg := s.items[k].seg; seg >= 0 {
			s.ts.lost = append(s.ts.lost, lostSeg{seg, cause.Error()})
		}
	}
	s.ts.next = s.items[last].seg + 1
	s.cut = true
	return nil
}

// flushLost records the segments a run lost, once that run has ended.
func (ps *pkgStream) flushLost(ts *trackState) {
	for _, l := range ts.lost {
		ps.markMissing(ts, l.seg, addetect.ResourceStatusMissing, l.reason, false)
	}
	ts.lost = ts.lost[:0]
}

// markMissing records a lost or declared-missing segment and, for video,
// queues the corresponding gap on the presentation timeline.
func (ps *pkgStream) markMissing(ts *trackState, seg int, status, reason string, declared bool) {
	sg := &ts.track.Segments[seg]
	r := &ps.pk.Resources[ts.resBase+seg]
	r.Status, r.Reason = status, reason
	if !ts.video {
		return
	}
	g := addetect.GapObservation{Reason: addetect.GapReasonResourcePrefix + status, Resource: int32(ts.resBase + seg), Declared: declared}
	if ps.hls {
		// Known interval: the declared duration after the last video frame,
		// less what was presented from the resource before it failed.
		g.StartUS = ps.videoEnd + ps.pendingGap
		dur := sg.DurationUS
		if r.StartUS != nil {
			dur = max(*r.StartUS+sg.DurationUS-g.StartUS, 0)
		}
		g.EndUS = g.StartUS + dur
		ps.pendingGap += dur
		if !ps.haveVideo {
			ps.req.Warn(addetect.Warning{Code: addetect.WarnLeadingGap, Message: "media is missing before the first video frame; the timeline starts at the first presented frame"})
			return
		}
	} else {
		g.StartUS, g.EndUS = sg.StartUS, sg.StartUS+sg.DurationUS
	}
	ps.gaps = append(ps.gaps, g)
}

// startRun opens the next run of a track, or marks the track done.
func (ps *pkgStream) startRun(ts *trackState) error {
	segs := ts.track.Segments
	for ts.run == nil {
		// Declared gaps are missing coverage, not content (PACK-02).
		for ts.next < len(segs) && segs[ts.next].Gap {
			ps.markMissing(ts, ts.next, addetect.ResourceStatusGap, hlsGapReason, true)
			ts.next++
		}
		if ts.next >= len(segs) {
			ts.done = true
			return nil
		}
		first := ts.next
		end := first + 1
		for end < len(segs) && !segs[end].Gap && segs[end].InitIndex == segs[first].InitIndex && segs[end].Epoch == segs[first].Epoch {
			end++
		}
		src := &segSource{ps: ps, ts: ts, items: make([]segItem, 0, end-first+1), ends: make([]int64, 0, end-first+1), initH: sha256.New(), init: -1}
		if ps.req.Package.HashMedia {
			src.segH = sha256.New()
		}
		if init, ok := ts.track.InitFor(&segs[first]); ok {
			src.items = append(src.items, segItem{res: init, seg: -1})
			src.init = ts.initBase + segs[first].InitIndex
		}
		if ps.hls && ts.video && !ps.haveVideo {
			// Until a frame has been presented the timeline starts here.
			ps.offsets[segs[first].Epoch], ps.offOK[segs[first].Epoch] = 0, true
		}
		for i := first; i < end; i++ {
			src.items = append(src.items, segItem{res: segs[i].Resource, seg: i})
		}
		ts.next = end
		r, err := openReader(ps.ctx, "", src, &ts.opt, ps.lumaCap)
		if err != nil {
			src.closeCurrent()
			ps.flushLost(ts)
			if src.cut && ps.req.Package.Degraded {
				continue // loss already recorded; try the following run
			}
			if ps.req.Package.Degraded && addetect.KindOf(err) != addetect.KindCanceled && ps.described {
				// Undecodable (corrupt) run: keep its interval unknown.
				ps.info.Incomplete = true
				ps.req.Warn(addetect.Warning{Code: addetect.WarnResourceCorrupt, Message: fmt.Sprintf("resources %s .. could not be opened: %v", segs[first].URI, err)})
				for i := first; i < end; i++ {
					ps.markMissing(ts, i, addetect.ResourceStatusMissing, err.Error(), false)
				}
				continue
			}
			return err
		}
		if ts.video {
			if !ps.described {
				r.describe(&ps.info, ps.req)
				creq := ps.req
				if ps.audio != nil {
					// Audio comes from its own track: its absence from the
					// video resources is not missing evidence.
					c := *ps.req
					c.NeedAudio = false
					creq = &c
				}
				if err := r.checkCapabilities(creq, true); err != nil {
					src.closeCurrent()
					r.close()
					return err
				}
				ps.described = true
			} else if r.info.video_index < 0 {
				src.closeCurrent()
				r.close()
				return addetect.Errorf(addetect.KindUnsupported, "resource %s has no decodable video", segs[first].URI)
			}
		} else if r.info.audio_index >= 0 && ps.info.Tracks.Audio == nil {
			var ai addetect.MediaInfo
			r.describe(&ai, ps.req)
			ps.info.Tracks.Audio = ai.Tracks.Audio
		}
		ts.run, ts.src, ts.epoch = r, src, segs[first].Epoch
	}
	return nil
}

func (ps *pkgStream) endRun(ts *trackState) {
	if ts.run == nil {
		return
	}
	ts.src.closeCurrent()
	ts.run.close()
	ts.run, ts.src = nil, nil
	ps.flushLost(ts)
}

// offset returns the media->presentation offset for a track epoch.
func (ps *pkgStream) offset(ts *trackState, epoch int) (int64, bool) {
	if !ps.hls {
		return ts.offsets[epoch], true
	}
	return ps.offsets[epoch], ps.offOK[epoch]
}

// inEpoch applies DASH presentation trimming so samples of adjacent Periods
// are never counted twice (PACK-06).
func (ps *pkgStream) inEpoch(epoch int, t int64) bool {
	if ps.hls {
		return true
	}
	e := &ps.pres.Epochs[epoch]
	return t >= e.StartUS && (e.EndUS < 0 || t < e.EndUS)
}

// clipToEpoch intersects the sample interval [t, t+dur) with its DASH
// Period (PACK-06). It returns the part inside the Period and whether any of
// it is: a sample that starts before the Period keeps the part from the
// Period start, one that runs past the Period end is cut there. A sample of
// unknown duration (dur <= 0) is kept or dropped by its start alone. HLS
// epochs are not trimmed.
func (ps *pkgStream) clipToEpoch(epoch int, t, dur int64) (int64, int64, bool) {
	if ps.hls {
		return t, dur, true
	}
	if dur <= 0 {
		return t, dur, ps.inEpoch(epoch, t)
	}
	e := &ps.pres.Epochs[epoch]
	end := t + dur
	if t < e.StartUS {
		t = e.StartUS
	}
	if e.EndUS >= 0 && end > e.EndUS {
		end = e.EndUS
	}
	if end <= t {
		return t, 0, false
	}
	return t, end - t, true
}

// audioReady reports whether the separate audio track's next samples can be
// placed: in HLS their epoch offset is set by that epoch's first video frame.
func (ps *pkgStream) audioReady() bool {
	if !ps.hls {
		return true
	}
	a := ps.audio
	if a.run != nil {
		return ps.offOK[a.epoch]
	}
	for i := a.next; i < len(a.track.Segments); i++ {
		if !a.track.Segments[i].Gap {
			return ps.offOK[a.track.Segments[i].Epoch]
		}
	}
	return true
}

// mapAudio moves windows onto the presentation timeline in place and trims
// them to their epoch.
func (ps *pkgStream) mapAudio(ts *trackState, epoch int, ws []addetect.AudioWindow) []addetect.AudioWindow {
	off, _ := ps.offset(ts, epoch)
	k := 0
	for _, w := range ws {
		start, dur, ok := ps.clipToEpoch(epoch, w.StartUS+off, int64(w.DurationUS))
		if !ok {
			continue
		}
		// A clipped window keeps its measured levels: they describe the
		// whole window, of which only the part inside the Period is counted.
		w.StartUS, w.DurationUS = start, int32(dur)
		ws[k] = w
		k++
	}
	if k > 0 && !ts.video {
		ts.time = ws[k-1].StartUS + int64(ws[k-1].DurationUS)
	}
	return ws[:k]
}

// Next implements addetect.Stream: it interleaves the video and audio
// tracks by presentation time, so differently segmented tracks stay aligned
// by time rather than by resource index (PACK-05).
func (ps *pkgStream) Next(ev *addetect.Event) error {
	if ps.closed {
		// Never open further resources once the stream was released.
		return addetect.Errorf(addetect.KindInternal, msgStreamClosed)
	}
	if ps.stashSent {
		ps.stash, ps.stashSent = ps.stash[:0], false
	}
	for {
		if err := ps.ctx.Err(); err != nil {
			return err
		}
		if len(ps.gaps) > 0 {
			ev.Kind, ev.Gap = addetect.EventGap, ps.gaps[0]
			ps.gaps = ps.gaps[1:]
			return nil
		}
		v, a := ps.video, ps.audio
		// Muxed audio that arrived before its epoch's offset was known.
		if len(ps.stash) > 0 && ps.offOK[ps.stashEpoch] {
			ps.stashSent = true
			if ws := ps.mapAudio(v, ps.stashEpoch, ps.stash); len(ws) > 0 {
				ev.Kind, ev.Audio = addetect.EventAudio, ws
				return nil
			}
			ps.stash, ps.stashSent = ps.stash[:0], false
		}
		var ts *trackState
		switch {
		case a != nil && !a.done && (v.done || a.time <= v.time) && ps.audioReady():
			ts = a
		case !v.done:
			ts = v
		case a != nil && !a.done:
			// Audio whose epoch never produced video cannot be placed.
			ps.req.Warn(addetect.Warning{Code: addetect.WarnAudioUnplaced, Message: "audio of an epoch without video could not be placed on the timeline and was skipped"})
			ps.endRun(a)
			a.done = true
			continue
		default:
			if !ps.haveVideo && ps.trimmed > 0 {
				return addetect.Errorf(addetect.KindTiming, "all %d decoded video frames lie outside their Period bounds: the manifest timing (Period start, presentationTimeOffset) does not match the media timestamps", ps.trimmed)
			}
			// Media that ends before the last Period's declared end is
			// missing coverage, not a shorter presentation (PACK-08).
			if last := ps.pres.Epochs[len(ps.pres.Epochs)-1]; !ps.hls && !ps.tailChecked && ps.haveVideo && last.EndUS-ps.videoEnd > periodTailSlackUS {
				ps.tailChecked = true
				ps.info.Incomplete = true
				ps.req.Warn(addetect.Warning{Code: addetect.WarnTruncatedInput, Message: fmt.Sprintf(
					"the media ends %.1f s before the declared end of the last Period; that interval is unknown",
					float64(last.EndUS-ps.videoEnd)/usPerSecond)})
				ps.gaps = append(ps.gaps, addetect.GapObservation{StartUS: ps.videoEnd, EndUS: last.EndUS, Reason: gapReasonShortPeriod, Resource: -1})
				continue
			}
			ps.finish()
			return io.EOF
		}
		if ts.run == nil {
			if err := ps.startRun(ts); err != nil {
				return err
			}
			if ts.done {
				continue
			}
		}
		if err := ts.run.next(ps.ctx); err != nil {
			if !ps.req.Package.Degraded || addetect.KindOf(err) == addetect.KindCanceled {
				return err
			}
			// Degraded mode: a run that fails mid-way ends here; the time it
			// would have covered stays unknown.
			ps.info.Incomplete = true
			ps.req.Warn(addetect.Warning{Code: addetect.WarnPackageRunFailed, Message: err.Error()})
			ps.endRun(ts)
			continue
		}
		e := &ts.run.ev
		switch e.kind {
		case C.TVAD_EOF:
			ps.endRun(ts)
		case C.TVAD_VIDEO:
			pts := int64(e.pts_us)
			if ps.hls && !ps.offOK[ts.epoch] {
				// Place this epoch directly after the previous one, plus any
				// declared-missing duration in between (PACK-06).
				ps.offsets[ts.epoch] = ps.videoEnd + ps.pendingGap - pts
				ps.offOK[ts.epoch] = true
			}
			off, _ := ps.offset(ts, ts.epoch)
			t := pts + off
			// The frame's interval, not only its start, is intersected with
			// the Period; an unknown duration is taken from the frame rate
			// seen so far.
			ts.run.fill(ev)
			f := &ev.Video
			if e.no_pts == 0 {
				fdur := f.Duration
				if fdur <= 0 {
					fdur = ps.lastDelta
				}
				ct, cdur, ok := ps.clipToEpoch(ts.epoch, t, fdur)
				if !ok {
					ps.trimmed++
					continue
				}
				if ct != t || cdur != fdur {
					t, f.Duration = ct, cdur
				}
			}
			f.PTS = t
			if ps.pk.Epochs[ts.epoch].MediaOffsetUS == nil {
				o := off
				ps.pk.Epochs[ts.epoch].MediaOffsetUS = &o
			}
			if ps.haveVideo && t > ts.time {
				ps.lastDelta = t - ts.time
			}
			dur := f.Duration
			if dur <= 0 {
				dur = ps.lastDelta
			}
			ts.time = t
			ps.haveVideo, ps.videoEnd, ps.pendingGap = true, t+dur, 0
			if e.pos >= 0 {
				if it := ts.src.items[ts.src.itemAt(int64(e.pos))]; it.seg >= 0 {
					f.Resource = int32(ts.resBase + it.seg)
					r := &ps.pk.Resources[f.Resource]
					end := t + dur
					if r.StartUS == nil {
						start := t
						r.StartUS, r.EndUS = &start, &end
					} else {
						*r.StartUS, *r.EndUS = min(*r.StartUS, t), max(*r.EndUS, end)
					}
				}
			}
			return nil
		case C.TVAD_AUDIO:
			ws := ts.run.aw[:int(e.n_audio)]
			if _, ok := ps.offset(ts, ts.epoch); !ok {
				if len(ps.stash) > 0 && ps.stashEpoch != ts.epoch {
					ps.stash = ps.stash[:0]
				}
				ps.stashEpoch = ts.epoch
				ps.stash = append(ps.stash, ws...)
				continue
			}
			if ws = ps.mapAudio(ts, ts.epoch, ws); len(ws) > 0 {
				ev.Kind, ev.Audio = addetect.EventAudio, ws
				return nil
			}
		case C.TVAD_CAPTION:
			if off, ok := ps.offset(ts, ts.epoch); ok {
				ts.run.fill(ev)
				ev.Caption.PTS += off
				return nil
			}
		default:
			if ts.run.fill(ev) {
				return nil
			}
		}
	}
}

// finish computes the package identity once every resource has been read
// (PACK-09): manifests, selected tracks, resource addresses, the content
// digests of the initialization resources and a version of every media
// resource. That version is the digest of its bytes with
// package.hash_media, otherwise the resolver's fingerprint (size and
// modification time for local files); a resolver without fingerprints, or
// a resource it cannot fingerprint, contributes its address only.
func (ps *pkgStream) finish() {
	if ps.finished {
		return
	}
	ps.finished = true
	h := sha256.New()
	pk := ps.pk
	for _, m := range pk.Manifests {
		fmt.Fprintf(h, "manifest %s %d %s\n", m.URI, m.Size, m.SHA256)
	}
	fmt.Fprintf(h, "video %s\n", pk.Video.ID)
	if pk.Audio != nil {
		fmt.Fprintf(h, "audio %s\n", pk.Audio.ID)
	}
	for i, r := range pk.Inits {
		fmt.Fprintf(h, "init %s %d %d %s\n", r.URI, r.Offset, r.Length, ps.initDigests[i])
	}
	fp, _ := ps.resolver.(manifest.Fingerprinter)
	prints := make(map[string]string)
	for i, r := range pk.Resources {
		version, ok := ps.mediaDigests[i]
		if !ok && fp != nil && !ps.req.Package.HashMedia {
			if version, ok = prints[r.URI]; !ok {
				if v, err := fp.Fingerprint(ps.ctx, r.URI); err == nil {
					version = v
				}
				prints[r.URI] = version
			}
		}
		fmt.Fprintf(h, "res %s %s %d %d %d %s\n", r.Track, r.URI, r.Offset, r.Length, r.Init, version)
	}
	pk.Identity = addetect.DigestPrefix + hex.EncodeToString(h.Sum(nil))
}

func (ps *pkgStream) Close() error {
	ps.closed = true
	if ps.video != nil {
		ps.endRun(ps.video)
	}
	if ps.audio != nil {
		ps.endRun(ps.audio)
	}
	return nil
}
