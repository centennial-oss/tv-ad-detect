// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"math"
	"time"
)

func us(d time.Duration) int64 { return int64(d / time.Microsecond) }

// unitMaxUS is the longest duration an ad unit may measure: breaks.max_unit
// widened by the loose duration tolerance (BREAK-01). Boundary placement is
// only exact to a frame or an audio window, so a unit of the longest
// standard length that measures a fraction over it must not be turned away
// by the limit itself.
func (c *Config) unitMaxUS() int64 {
	return us(c.Breaks.MaxUnit) + us(c.Scoring.LooseTolerance)
}

// frameRec is the compact per-frame measurement kept for the whole
// recording (16 bytes). Decoded pictures are never retained (QUAL-02).
type frameRec struct {
	pts    int64  // presentation time (media timeline during extraction, normalized afterwards)
	mean   uint16 // mean luminance x 256
	std    uint16 // luminance standard deviation x 256
	bright uint16 // bright-pixel ratio x 65535
	flags  uint8
}

const (
	fGapAfter uint8 = 1 << iota // a coverage gap follows this frame
	fCorrupt
)

func (f *frameRec) meanLuma() float64   { return float64(f.mean) / lumaFixedScale }
func (f *frameRec) stdLuma() float64    { return float64(f.std) / lumaFixedScale }
func (f *frameRec) brightFrac() float64 { return float64(f.bright) / ratioFixedScale }

// regStat is one region's luminance statistics in one frame, in the fixed
// point of frameRec. Frames carry one per logo search region plus one for
// the rest of the measurement region, so the dark test can leave the logo's
// region out once the logo is known (C5).
type regStat struct{ mean, std, bright uint16 }

// regStatBytes is the retained size of one regStat (memory budget).
const regStatBytes = 6

// regionsDisjoint reports whether no two regions overlap, which the region
// statistics need: the rest of the frame is the total minus every region.
func regionsDisjoint(rs []Region) bool {
	for i := range rs {
		for j := i + 1; j < len(rs); j++ {
			a, b := rs[i], rs[j]
			if a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
				return false
			}
		}
	}
	return true
}

// clipRect intersects two pixel rectangles; an empty result has zero area.
func clipRect(a, b [4]int) [4]int {
	r := [4]int{max(a[0], b[0]), max(a[1], b[1]), min(a[2], b[2]), min(a[3], b[3])}
	if r[2] < r[0] {
		r[2] = r[0]
	}
	if r[3] < r[1] {
		r[3] = r[1]
	}
	return r
}

// searchRects returns the logo search regions of a geometry, clipped to the
// measurement region.
func searchRects(cfg *Config, aw, ah int) (rects [][4]int, meas [4]int) {
	x0, y0, x1, y1 := cfg.Sampling.Region.pixels(aw, ah)
	meas = [4]int{x0, y0, x1, y1}
	for _, r := range cfg.Logo.SearchRegions {
		rx0, ry0, rx1, ry1 := r.pixels(aw, ah)
		rects = append(rects, clipRect([4]int{rx0, ry0, rx1, ry1}, meas))
	}
	return rects, meas
}

func makeRegStat(n, sum, sumsq, bright uint64) regStat {
	if n == 0 {
		return regStat{}
	}
	mean := float64(sum) / float64(n)
	v := float64(sumsq)/float64(n) - mean*mean
	if v < 0 {
		v = 0
	}
	return regStat{
		mean:   uint16(math.Round(mean * lumaFixedScale)),
		std:    uint16(math.Round(min(math.Sqrt(v), lumaMax) * lumaFixedScale)),
		bright: uint16(math.Round(float64(bright) / float64(n) * ratioFixedScale)),
	}
}

// geomEpoch is a run of frames with constant coded and analysis geometry.
type geomEpoch struct {
	firstFrame     int
	codedW, codedH int
	aw, ah, scale  int
	sarN, sarD     int
}

// span is a half-open interval.
type span struct{ start, end int64 }

// extractor consumes observations and keeps compact features and events.
type extractor struct {
	cfg  *Config
	warn func(code, msg string, t int64, hasT bool)

	frames []frameRec
	epochs []geomEpoch

	// timeline state (media timeline)
	offset      int64 // re-basing offset added to media timestamps
	lastPTS     int64
	lastDur     int64 // declared duration of the last frame, 0 unknown
	lastDelta   int64
	gaps        []TimeRange // coverage gaps between frames
	declared    []TimeRange // gaps declared by the source
	rebases     []Rebase
	missingPTS  int
	overlaps    int
	decodeErrs  int
	interlaced  int
	corrupt     int
	firstAudio  int64
	haveAudio   bool
	gapUS       int64
	resetWarned bool

	// per-frame scratch
	hist    [lumaLevels]uint32
	scratch []byte
	region  [4]int // measurement region in analysis pixels for the current epoch

	resolution []ResolutionEvent
	scene      sceneDetector
	aspect     aspectTracker
	logo       logoCollector
	caps       captionTracker
	audio      []AudioWindow
	bumpers    bumperMatcher

	budget int64

	// regN is the number of region statistics per frame (search regions
	// plus the rest), 0 when they are not kept; regRects are the current
	// geometry's search regions clipped to the measurement region.
	regN     int
	regRects [][4]int
	regStats []regStat

	// frameCC and trackCC say which caption carriage the source selected;
	// bytes arriving the other way are ignored (CAP-01).
	frameCC, trackCC bool
}

// captionSource records the caption source the stream reports. A source
// that does not say (empty) may deliver captions either way.
func (x *extractor) captionSource(src string) {
	x.frameCC = src == "" || src == CaptionSourceEmbedded
	x.trackCC = src == "" || src == CaptionSourceTrack
}

func newExtractor(cfg *Config, assets *Assets, warn func(code, msg string, t int64, hasT bool)) *extractor {
	x := &extractor{cfg: cfg, warn: warn, gapUS: us(cfg.Input.TimestampGap), budget: int64(cfg.Resources.MemoryBudget)}
	if cfg.Signals.Logo && cfg.Signals.DarkUniform && len(cfg.Logo.SearchRegions) > 0 && regionsDisjoint(cfg.Logo.SearchRegions) {
		x.regN = len(cfg.Logo.SearchRegions) + 1
	}
	x.scene.init(cfg)
	x.aspect.init(cfg)
	x.logo.init(cfg, assets)
	x.caps.init(cfg)
	x.bumpers.init(cfg, assets)
	return x
}

// reserve pre-sizes compact storage from the source's hints.
func (x *extractor) reserve(durationUS int64, fps float64) {
	if durationUS <= 0 {
		return
	}
	if fps <= 0 || fps > maxPlausibleFPS {
		fps = fallbackFrameRate
	}
	sec := float64(durationUS) / usPerSecond
	n := int(sec*fps) + reserveSlack
	// Never reserve more than a quarter of the budget up front.
	if lim := int(x.budget / 4 / frameRecBytes); n > lim {
		n = lim
	}
	x.frames = make([]frameRec, 0, n)
	if x.cfg.Signals.Audio {
		w := int(sec*usPerSecond/float64(us(x.cfg.Audio.Window))) + reserveSlack
		if lim := int(x.budget / 4 / audioWindowBytes); w > lim {
			w = lim
		}
		x.audio = make([]AudioWindow, 0, w)
	}
}

// regionStats appends one frame's statistics per search region and for the
// rest of the measurement region (the histogram totals minus the regions).
func (x *extractor) regionStats(luma []byte, stride int, cnt, sum, sumsq, bright uint64) {
	bl := byte(x.cfg.Dark.BrightPixelLuma)
	for _, r := range x.regRects {
		var s, q, b uint64
		for y := r[1]; y < r[3]; y++ {
			for _, v := range luma[y*stride+r[0] : y*stride+r[2]] {
				s += uint64(v)
				q += uint64(v) * uint64(v)
				if v > bl {
					b++
				}
			}
		}
		n := uint64((r[2] - r[0]) * (r[3] - r[1]))
		x.regStats = append(x.regStats, makeRegStat(n, s, q, b))
		cnt, sum, sumsq, bright = cnt-n, sum-s, sumsq-q, bright-b
	}
	x.regStats = append(x.regStats, makeRegStat(cnt, sum, sumsq, bright))
}

func (x *extractor) memoryUse() int64 {
	return int64(cap(x.frames))*frameRecBytes + int64(cap(x.audio))*audioWindowBytes + int64(cap(x.regStats))*regStatBytes + x.logo.memoryUse()
}

// checkBudget fails when retained feature storage exceeds
// resources.memory_budget (QUAL-02). It is cheap (a few capacities) and runs
// after every growth point: each video frame and each audio delivery.
func (x *extractor) checkBudget() error {
	if use := x.memoryUse(); use > x.budget {
		return Errorf(KindResourceLimit, "compact feature storage (%d bytes) exceeds resources.memory_budget (%s) after %d frames and %d audio windows",
			use, x.cfg.Resources.MemoryBudget, len(x.frames), len(x.audio))
	}
	return nil
}

// lumaHistogram accumulates the histogram of a rectangle. Four interleaved
// tables avoid store-to-load stalls on runs of equal values.
func lumaHistogram(hist *[lumaLevels]uint32, luma []byte, stride, x0, y0, x1, y1 int) {
	var h [4][lumaLevels]uint32
	for y := y0; y < y1; y++ {
		row := luma[y*stride+x0 : y*stride+x1]
		for len(row) >= 4 {
			h[0][row[0]]++
			h[1][row[1]]++
			h[2][row[2]]++
			h[3][row[3]]++
			row = row[4:]
		}
		for _, v := range row {
			h[0][v]++
		}
	}
	for i := range hist {
		hist[i] = h[0][i] + h[1][i] + h[2][i] + h[3][i]
	}
}

// video ingests one frame.
func (x *extractor) video(f *VideoFrame) error {
	pts := f.PTS
	n := len(x.frames)
	if f.NoPTS {
		x.missingPTS++
		switch {
		case n == 0:
			pts = 0
		case x.lastDur > 0:
			pts = x.lastPTS + x.lastDur - x.offset
		default:
			pts = x.lastPTS + max(x.lastDelta, 1) - x.offset
		}
	}
	pts += x.offset
	discontinuity := false
	if n > 0 {
		expectedEnd := x.lastPTS + x.lastDur
		if x.lastDur <= 0 {
			expectedEnd = x.lastPTS + x.lastDelta
		}
		switch {
		case pts <= x.lastPTS:
			if x.lastPTS-pts <= x.gapUS {
				// Overlapping/duplicate presentation time: never counted twice.
				x.overlaps++
				return nil
			}
			if x.cfg.Input.TimestampResetPolicy == ResetPolicyFail {
				return Errorf(KindTiming, "video timestamps jump backwards by %d us after %d us", x.lastPTS-pts, x.lastPTS)
			}
			if expectedEnd <= x.lastPTS {
				expectedEnd = x.lastPTS + 1
			}
			x.offset += expectedEnd - pts
			x.rebases = append(x.rebases, Rebase{AtUS: expectedEnd, OffsetUS: x.offset})
			if !x.resetWarned {
				x.resetWarned = true
				x.warn(WarnTimestampReset, "video timestamps jumped backwards; later media was re-based to continue after the previous frame", expectedEnd, true)
			}
			pts = expectedEnd
			discontinuity = true
		case pts-expectedEnd > x.gapUS && (x.lastDur > 0 || x.lastDelta > 0):
			// (With no declared duration and no spacing learned yet, the
			// first interval defines the frame spacing instead.)
			if expectedEnd <= x.lastPTS {
				expectedEnd = x.lastPTS + 1
			}
			x.frames[n-1].flags |= fGapAfter
			x.gaps = append(x.gaps, TimeRange{StartUS: expectedEnd, EndUS: pts, Reason: GapReasonTimestamp})
			discontinuity = true
		}
		if !discontinuity {
			x.lastDelta = pts - x.lastPTS
		}
	}
	x.lastPTS, x.lastDur = pts, f.Duration

	luma, stride, aw, ah, scale := f.Luma, f.Stride, f.Width, f.Height, max(f.Scale, 1)
	if aw <= 0 || ah <= 0 || len(luma) < (ah-1)*stride+aw {
		return Errorf(KindMedia, "video frame at %d us has an invalid luminance plane", pts)
	}
	// Caller-provided frames larger than the analysis bound are reduced here.
	if k := max(ceilDiv(aw, x.cfg.Sampling.AnalysisMaxWidth), ceilDiv(ah, x.cfg.Sampling.AnalysisMaxHeight)); k > 1 {
		need := (aw / k) * (ah / k)
		if cap(x.scratch) < need {
			x.scratch = make([]byte, need)
		}
		x.scratch = x.scratch[:need]
		aw, ah = DownsampleLuma(x.scratch, luma, aw, ah, stride, k)
		luma, stride, scale = x.scratch, aw, scale*k
	}
	cw, ch := f.CodedWidth, f.CodedHeight
	if cw <= 0 || ch <= 0 {
		cw, ch = aw*scale, ah*scale
	}
	sn, sd := f.SARNum, f.SARDen
	if sn <= 0 || sd <= 0 {
		sn, sd = 1, 1
	}

	var ep *geomEpoch
	if len(x.epochs) > 0 {
		ep = &x.epochs[len(x.epochs)-1]
	}
	if ep == nil || ep.codedW != cw || ep.codedH != ch || ep.aw != aw || ep.ah != ah || ep.sarN != sn || ep.sarD != sd {
		if ep != nil && (ep.codedW != cw || ep.codedH != ch) && x.cfg.Signals.Resolution {
			x.resolution = append(x.resolution, ResolutionEvent{
				TimeUS: pts, OldWidth: ep.codedW, OldHeight: ep.codedH, NewWidth: cw, NewHeight: ch,
			})
		}
		x.epochs = append(x.epochs, geomEpoch{firstFrame: n, codedW: cw, codedH: ch, aw: aw, ah: ah, scale: scale, sarN: sn, sarD: sd})
		ep = &x.epochs[len(x.epochs)-1]
		x0, y0, x1, y1 := x.cfg.Sampling.Region.pixels(aw, ah)
		x.region = [4]int{x0, y0, x1, y1}
		if x.regN > 0 {
			x.regRects, _ = searchRects(x.cfg, aw, ah)
		}
		// Geometry-dependent state never survives a geometry change (SIZE-02).
		discontinuity = true
		x.aspect.newGeometry(pts, ep)
		x.logo.newGeometry(pts, ep)
		x.bumpers.newGeometry(ep, x.warn)
	}
	if discontinuity {
		x.scene.reset()
		x.aspect.discontinuity(pts)
		x.bumpers.discontinuity(pts)
	}

	// Canonical luminance statistics over the measurement region (VIS-01).
	lumaHistogram(&x.hist, luma, stride, x.region[0], x.region[1], x.region[2], x.region[3])
	var cnt, sum, sumsq, bright uint64
	bl := x.cfg.Dark.BrightPixelLuma
	for v, c := range x.hist {
		c64 := uint64(c)
		cnt += c64
		sum += c64 * uint64(v)
		sumsq += c64 * uint64(v*v)
		if v > bl {
			bright += c64
		}
	}
	mean := float64(sum) / float64(cnt)
	variance := float64(sumsq)/float64(cnt) - mean*mean
	if variance < 0 {
		variance = 0
	}
	std := math.Sqrt(variance)
	brightFrac := float64(bright) / float64(cnt)
	if f.FullResBrightFraction >= 0 && x.cfg.Sampling.FullResBrightCheck && x.region == [4]int{0, 0, aw, ah} {
		brightFrac = float64(f.FullResBrightFraction)
	}
	rec := frameRec{
		pts:    pts,
		mean:   uint16(math.Round(mean * lumaFixedScale)),
		std:    uint16(math.Round(min(std, lumaMax) * lumaFixedScale)),
		bright: uint16(math.Round(min(brightFrac, 1) * ratioFixedScale)),
	}
	if f.Corrupt {
		rec.flags |= fCorrupt
		x.corrupt++
	}
	if f.Interlaced {
		x.interlaced++
	}
	x.frames = append(x.frames, rec)
	if x.regN > 0 {
		x.regionStats(luma, stride, cnt, sum, sumsq, bright)
	}

	// Provisional flatness (fixed thresholds) for detectors whose content
	// bounds are indeterminable on dark or uniform pictures.
	flat := mean <= x.cfg.Dark.MaxMeanLuma || std <= x.cfg.Dark.UniformMaxNonunif

	if x.cfg.Signals.Scene {
		x.scene.frame(pts, &x.hist, int(cnt), mean)
	}
	if x.cfg.Signals.Aspect {
		x.aspect.frame(pts, luma, stride, flat)
	}
	if x.cfg.Signals.Logo {
		x.logo.frame(pts, luma, stride, flat, &x.hist, int(cnt))
	}
	if x.bumpers.active() {
		x.bumpers.frame(pts, luma, stride)
	}
	if len(f.CC) > 0 && x.frameCC && x.cfg.Signals.Captions {
		x.caps.feed(pts, f.CC)
	}
	if err := x.checkBudget(); err != nil {
		return err
	}
	return nil
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }

func (x *extractor) audioWindows(ws []AudioWindow) error {
	if !x.cfg.Signals.Audio || len(ws) == 0 {
		return nil
	}
	if !x.haveAudio {
		x.haveAudio, x.firstAudio = true, ws[0].StartUS
	}
	base := len(x.audio)
	x.audio = append(x.audio, ws...)
	if x.offset != 0 {
		for i := base; i < len(x.audio); i++ {
			x.audio[i].StartUS += x.offset
		}
	}
	return x.checkBudget()
}
