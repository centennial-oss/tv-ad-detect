// SPDX-License-Identifier: Apache-2.0

package addetect

import "math"

// aspectSpan is an interval of confirmed active-picture geometry. Time not
// covered by any span is unknown (ASPECT-02).
type aspectSpan struct {
	start, end int64
	ratio      float64
	l, t, w, h int // active picture in coded pixels
}

type aspectObs struct {
	l, t, r, b int // analysis pixels, half-open
	ratio      float64
}

// aspectTracker estimates the active picture by scanning inward from the
// frame edges for letterbox/pillarbox borders (ASPECT-01).
//
// A row or column is a border when its mean luminance is at most
// aspect.border_max_luma and no pixel exceeds the mean limit by more than
// aspect.border_max_deviation. Estimates on dark/uniform frames, crops below
// aspect.min_active_fraction and asymmetric borders are unknown. A ratio
// that differs from the confirmed one must be observed for
// aspect.persistence before it is confirmed; the change is then dated at its
// first supporting observation (ASPECT-03). Only time between consecutive
// supporting observations counts: time spent on unknown observations (black
// frames, for example) does not, so two isolated samples on either side of a
// dark stretch cannot confirm a ratio. Spans never overlap.
type aspectTracker struct {
	enabled   bool
	sampleUS  int64
	persistUS int64
	tol       float64
	maxLuma   int
	limit     int
	minActive float64
	symTol    float64

	ep         geomEpoch
	haveGeom   bool
	nextSample int64

	spans []aspectSpan

	cur      aspectObs
	curValid bool
	curStart int64

	cand      aspectObs
	candValid bool
	candFirst int64
	// candLast is the previous supporting observation while candContig is
	// set (nothing but support since then); candSupport is the observed
	// time accumulated between consecutive supporting observations.
	candLast    int64
	candContig  bool
	candSupport int64

	// spanEnd is the end of the last emitted span, once haveSpan is set.
	spanEnd  int64
	haveSpan bool

	inUnknown bool
	unkStart  int64
	lastPTS   int64
}

func (a *aspectTracker) init(cfg *Config) {
	a.enabled = cfg.Signals.Aspect
	a.sampleUS = us(cfg.Aspect.SampleInterval)
	a.persistUS = us(cfg.Aspect.Persistence)
	a.tol = cfg.Aspect.RatioTolerance
	a.maxLuma = int(cfg.Aspect.BorderMaxLuma)
	a.limit = int(cfg.Aspect.BorderMaxLuma + cfg.Aspect.BorderMaxDeviation)
	a.minActive = cfg.Aspect.MinActiveFraction
	a.symTol = cfg.Aspect.SymmetryTolerance
}

func (a *aspectTracker) closeCurrent(end int64) {
	if a.curValid && end > a.curStart {
		s := a.ep.scale
		a.spans = append(a.spans, aspectSpan{
			start: a.curStart, end: end, ratio: a.cur.ratio,
			l: a.cur.l * s, t: a.cur.t * s, w: (a.cur.r - a.cur.l) * s, h: (a.cur.b - a.cur.t) * s,
		})
		a.spanEnd, a.haveSpan = end, true
	}
	a.curValid = false
}

// newGeometry invalidates border state at a geometry change (SIZE-02).
func (a *aspectTracker) newGeometry(pts int64, ep *geomEpoch) {
	if a.haveGeom {
		a.closeCurrent(pts)
	}
	a.ep, a.haveGeom = *ep, true
	a.candValid, a.candContig, a.inUnknown = false, false, false
	a.nextSample = pts
}

// discontinuity drops an unconfirmed candidate so persistence is never
// accumulated across missing time.
func (a *aspectTracker) discontinuity(pts int64) {
	a.candValid, a.candContig = false, false
	a.inUnknown = false
	a.nextSample = pts
}

func (a *aspectTracker) match(x, y float64) bool {
	return math.Abs(x-y) <= a.tol*y
}

func (a *aspectTracker) frame(pts int64, luma []byte, stride int, flat bool) {
	a.lastPTS = pts
	if pts < a.nextSample {
		return
	}
	a.nextSample = pts + a.sampleUS
	obs, ok := aspectObs{}, false
	if !flat {
		obs, ok = a.measure(luma, stride)
	}
	if !ok {
		if !a.inUnknown {
			a.inUnknown, a.unkStart = true, pts
		}
		a.candContig = false
		if a.curValid && pts-a.unkStart >= a.persistUS {
			a.closeCurrent(a.unkStart)
		}
		return
	}
	a.accept(pts, obs)
}

// accept advances the confirmation state with a known observation.
func (a *aspectTracker) accept(pts int64, obs aspectObs) {
	a.inUnknown = false
	if a.curValid && a.match(obs.ratio, a.cur.ratio) {
		a.candValid = false
		return
	}
	if !a.candValid || !a.match(obs.ratio, a.cand.ratio) {
		a.cand, a.candFirst, a.candValid = obs, pts, true
		a.candSupport, a.candContig = 0, false
	}
	if a.candContig {
		a.candSupport += pts - a.candLast
	}
	a.candLast, a.candContig = pts, true
	if a.candSupport >= a.persistUS {
		// Dated at the first supporting observation, but never before the
		// end of the span already emitted.
		start := a.candFirst
		if a.haveSpan && start < a.spanEnd {
			start = a.spanEnd
		}
		a.closeCurrent(start)
		a.cur, a.curStart, a.curValid = a.cand, start, true
		a.candValid = false
	}
}

func (a *aspectTracker) finish(end int64) {
	if a.haveGeom {
		a.closeCurrent(end)
	}
}

func (a *aspectTracker) rowIsBorder(row []byte) bool {
	sum := 0
	for _, v := range row {
		if int(v) > a.limit {
			return false
		}
		sum += int(v)
	}
	return sum <= a.maxLuma*len(row)
}

func (a *aspectTracker) colIsBorder(luma []byte, stride, x, y0, y1 int) bool {
	sum := 0
	for y := y0; y < y1; y++ {
		v := int(luma[y*stride+x])
		if v > a.limit {
			return false
		}
		sum += v
	}
	return sum <= a.maxLuma*(y1-y0)
}

func (a *aspectTracker) measure(luma []byte, stride int) (aspectObs, bool) {
	w, h := a.ep.aw, a.ep.ah
	top := 0
	for top < h && a.rowIsBorder(luma[top*stride:top*stride+w]) {
		top++
	}
	if top == h {
		return aspectObs{}, false
	}
	bottom := h
	for bottom > top+1 && a.rowIsBorder(luma[(bottom-1)*stride:(bottom-1)*stride+w]) {
		bottom--
	}
	left := 0
	for left < w && a.colIsBorder(luma, stride, left, top, bottom) {
		left++
	}
	if left == w {
		return aspectObs{}, false
	}
	right := w
	for right > left+1 && a.colIsBorder(luma, stride, right-1, top, bottom) {
		right--
	}
	aw, ah := right-left, bottom-top
	if float64(aw) < a.minActive*float64(w) || float64(ah) < a.minActive*float64(h) {
		return aspectObs{}, false
	}
	if math.Abs(float64(top-(h-bottom))) > a.symTol*float64(h)+1 ||
		math.Abs(float64(left-(w-right))) > a.symTol*float64(w)+1 {
		return aspectObs{}, false
	}
	ratio := float64(aw) * float64(a.ep.sarN) / (float64(ah) * float64(a.ep.sarD))
	return aspectObs{l: left, t: top, r: right, b: bottom, ratio: ratio}, true
}
