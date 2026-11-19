// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"math"
	"math/bits"
	"sort"
)

// Logo presence (5.2).
//
// Every logo.sample_interval the Sobel edge map of the search regions is
// stored as one bit per analysis pixel (edge when (|gx|+|gy|)/8 >=
// logo.edge_threshold). Learning counts, per pixel, the observations in
// which it was an edge, using only observations at least
// logo.learn_min_separation apart whose luminance histogram moved at least
// logo.learn_min_change since the previous learning observation (static
// scenes add nothing) and never dark/uniform frames. After the pass, pixels
// that were edges in at least logo.min_persistence of the learning
// observations are grouped into connected components; components larger
// than the maximum candidate size are discarded (persistent picture borders
// span a whole search region), the rest are clustered and the best-supported
// cluster within the size limits becomes the logo mask. Each stored
// observation is then matched: match = share of mask pixels that are edges.
// Storing bit maps makes whole-recording learning possible in one decode
// pass (PIPE-03) at about 3 KiB per observation.

const (
	logoUnknown uint8 = iota
	logoPresent
	logoAbsent
)

var logoStateNames = [...]string{LogoStateUnknown, LogoStatePresent, LogoStateAbsent}

type logoSpan struct {
	start, end  int64
	state       uint8
	uncertainty int64
	meanMatch   float64
}

type logoSample struct {
	pts    int64
	usable bool
}

type logoModel struct {
	mask   []uint64
	w0, w1 int // word range containing mask bits
	pixels int
	// bg marks the non-mask pixels of the logo box; edges there are
	// background texture, not logo.
	bg       []uint64
	b0, b1   int
	bgPixels int
	// scaled marks a mask resampled from another geometry; its pixels are
	// only approximately placed, so the background discount is not used.
	scaled bool
	// reach is the distance (pixels) within which an observed edge counts
	// for a mask pixel of an enlarged asset; 0 = the pixel itself.
	reach   int
	box     [4]int // x0,y0,x1,y1 analysis pixels
	origin  string
	version string
}

type logoEpoch struct {
	geom       geomEpoch
	startPTS   int64
	endPTS     int64
	rects      [][4]int
	rectOff    []int
	nbits      int
	words      int
	samples    []logoSample
	data       []uint64
	counts     []uint32
	learnN     int
	lastLearn  int64
	haveLearn  bool
	learnHist  [logoHistBins]uint32
	learnHistN int
	model      *logoModel
	failed     string
}

type logoCollector struct {
	cfg        *Config
	asset      *LogoAsset
	sampleUS   int64
	sepUS      int64
	edgeThr    int
	epochs     []*logoEpoch
	nextSample int64
	foldBuf    [logoHistBins]uint32
}

func (l *logoCollector) init(cfg *Config, assets *Assets) {
	l.cfg = cfg
	if assets != nil {
		l.asset = assets.Logo
	}
	l.sampleUS = us(cfg.Logo.SampleInterval)
	l.sepUS = us(cfg.Logo.LearnMinSeparation)
	l.edgeThr = int(math.Round(cfg.Logo.EdgeThreshold * 8))
}

func (l *logoCollector) memoryUse() int64 {
	var n int64
	for _, e := range l.epochs {
		n += int64(cap(e.data))*logoWordBytes + int64(cap(e.samples))*logoSampleBytes + int64(cap(e.counts))*logoCountBytes
	}
	return n
}

func (l *logoCollector) newGeometry(pts int64, ep *geomEpoch) {
	if !l.cfg.Signals.Logo {
		return
	}
	if n := len(l.epochs); n > 0 {
		l.epochs[n-1].endPTS = pts
	}
	e := &logoEpoch{geom: *ep, startPTS: pts, endPTS: pts}
	l.epochs = append(l.epochs, e)
	l.nextSample = pts
	if l.asset != nil {
		m, rect, why := l.asset.project(ep.aw, ep.ah)
		if m == nil {
			e.failed = why
			return
		}
		e.model = m
		e.rects = [][4]int{rect}
	} else {
		for _, r := range l.cfg.Logo.SearchRegions {
			x0, y0, x1, y1 := r.pixels(ep.aw, ep.ah)
			e.rects = append(e.rects, [4]int{x0, y0, x1, y1})
		}
	}
	e.rectOff = make([]int, len(e.rects))
	for i, r := range e.rects {
		e.rectOff[i] = e.nbits
		e.nbits += (r[2] - r[0]) * (r[3] - r[1])
	}
	e.words = bitWords(e.nbits)
	if e.model != nil {
		e.model.background(e.rects, e.rectOff)
	}
	if l.asset == nil {
		e.counts = make([]uint32, e.nbits)
	}
}

// foldHist folds the luminance histogram into logoHistBins equal bins.
func foldHist(dst *[logoHistBins]uint32, hist *[lumaLevels]uint32) {
	const per = lumaLevels / logoHistBins
	clear(dst[:])
	for v, c := range hist {
		dst[v/per] += c
	}
}

func (l *logoCollector) frame(pts int64, luma []byte, stride int, flat bool, hist *[lumaLevels]uint32, n int) {
	if pts < l.nextSample || len(l.epochs) == 0 {
		return
	}
	l.nextSample = pts + l.sampleUS
	e := l.epochs[len(l.epochs)-1]
	e.endPTS = pts
	if e.words == 0 {
		return
	}
	e.samples = append(e.samples, logoSample{pts: pts, usable: !flat})
	base := len(e.data)
	if cap(e.data)-base < e.words {
		grown := make([]uint64, base, max(2*cap(e.data), base+reserveSlack*e.words))
		copy(grown, e.data)
		e.data = grown
	}
	e.data = e.data[:base+e.words]
	dst := e.data[base:]
	clear(dst)
	if flat {
		return
	}
	learn := false
	if e.counts != nil && (!e.haveLearn || pts-e.lastLearn >= l.sepUS) {
		foldHist(&l.foldBuf, hist)
		if !e.haveLearn || histDistance(l.foldBuf[:], e.learnHist[:], n, e.learnHistN) >= l.cfg.Logo.LearnMinChange {
			learn = true
			e.haveLearn, e.lastLearn = true, pts
			e.learnHist, e.learnHistN = l.foldBuf, n
			e.learnN++
		}
	}
	w, h := e.geom.aw, e.geom.ah
	thr := l.edgeThr
	pos := 0
	for _, r := range e.rects {
		for y := r[1]; y < r[3]; y++ {
			if y == 0 || y == h-1 {
				pos += r[2] - r[0]
				continue
			}
			up := luma[(y-1)*stride : (y-1)*stride+w]
			mid := luma[y*stride : y*stride+w]
			dn := luma[(y+1)*stride : (y+1)*stride+w]
			for x := r[0]; x < r[2]; x++ {
				if x > 0 && x < w-1 {
					a, b, c := int(up[x-1]), int(up[x]), int(up[x+1])
					d, f := int(mid[x-1]), int(mid[x+1])
					g, hh, i := int(dn[x-1]), int(dn[x]), int(dn[x+1])
					gx := (c + 2*f + i) - (a + 2*d + g)
					gy := (g + 2*hh + i) - (a + 2*b + c)
					if gx < 0 {
						gx = -gx
					}
					if gy < 0 {
						gy = -gy
					}
					if gx+gy >= thr {
						setBit(dst, pos)
						if learn {
							e.counts[pos]++
						}
					}
				}
				pos++
			}
		}
	}
}

type logoComponent struct {
	x0, y0, x1, y1 int     // inclusive bounds in frame analysis pixels
	pixels         []int32 // bit positions
	used           bool
}

func boxGap(a0, a1, b0, b1 int) int {
	switch {
	case b0 > a1:
		return b0 - a1 - 1
	case a0 > b1:
		return a0 - b1 - 1
	}
	return 0
}

// maxLogoCandidates bounds how many learned candidates are validated.
const maxLogoCandidates = 6

// learn derives logo candidates of an epoch from its edge persistence
// counts (LOGO-01), best first: candidates are ranked by mean persistence
// (how consistently their pixels were edges), then by size. A station logo
// outranks graphics that are on screen less of the time, such as score
// boxes. It returns a failure reason when no candidate is supported.
func (l *logoCollector) learn(e *logoEpoch) ([]*logoModel, string) {
	c := &l.cfg.Logo
	if e.learnN < c.MinLearnSamples {
		return nil, LogoFailInsufficientLearning
	}
	need := uint32(math.Ceil(c.MinPersistence * float64(e.learnN)))
	if need < 1 {
		need = 1
	}
	w, h := e.geom.aw, e.geom.ah
	maxW := int(math.Round(c.MaxWidth * float64(w)))
	maxH := int(math.Round(c.MaxHeight * float64(h)))
	minW := int(math.Ceil(c.MinWidth * float64(w)))
	minH := int(math.Ceil(c.MinHeight * float64(h)))
	gap := int(math.Round(c.ClusterGap * float64(w)))

	var comps []logoComponent
	var stack []int32
	for ri, r := range e.rects {
		rw, rh := r[2]-r[0], r[3]-r[1]
		off := e.rectOff[ri]
		seen := make([]bool, rw*rh)
		for start := 0; start < rw*rh; start++ {
			if seen[start] || e.counts[off+start] < need {
				continue
			}
			comp := logoComponent{x0: w, y0: h, x1: -1, y1: -1}
			stack = append(stack[:0], int32(start))
			seen[start] = true
			for len(stack) > 0 {
				p := int(stack[len(stack)-1])
				stack = stack[:len(stack)-1]
				px, py := p%rw, p/rw
				comp.pixels = append(comp.pixels, int32(off+p))
				fx, fy := r[0]+px, r[1]+py
				comp.x0, comp.x1 = min(comp.x0, fx), max(comp.x1, fx)
				comp.y0, comp.y1 = min(comp.y0, fy), max(comp.y1, fy)
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						nx, ny := px+dx, py+dy
						if nx < 0 || ny < 0 || nx >= rw || ny >= rh {
							continue
						}
						q := ny*rw + nx
						if !seen[q] && e.counts[off+q] >= need {
							seen[q] = true
							stack = append(stack, int32(q))
						}
					}
				}
			}
			// Oversized components are persistent borders or static sets,
			// not a logo.
			if comp.x1-comp.x0+1 > maxW || comp.y1-comp.y0+1 > maxH {
				continue
			}
			comps = append(comps, comp)
		}
	}
	if len(comps) == 0 {
		return nil, LogoFailNoPersistent
	}
	// Largest first; ties keep discovery (region, raster) order.
	sort.SliceStable(comps, func(i, j int) bool { return len(comps[i].pixels) > len(comps[j].pixels) })

	type cluster struct {
		members []int
		box     [4]int
		pixels  int
		mass    uint64 // sum of persistence counts
	}
	var clusters []cluster
	for seed := range comps {
		if comps[seed].used {
			continue
		}
		comps[seed].used = true
		members := []int{seed}
		x0, y0, x1, y1 := comps[seed].x0, comps[seed].y0, comps[seed].x1, comps[seed].y1
		for grew := true; grew; {
			grew = false
			for k := range comps {
				cmp := &comps[k]
				if cmp.used || boxGap(x0, x1, cmp.x0, cmp.x1) > gap || boxGap(y0, y1, cmp.y0, cmp.y1) > gap {
					continue
				}
				nx0, ny0, nx1, ny1 := min(x0, cmp.x0), min(y0, cmp.y0), max(x1, cmp.x1), max(y1, cmp.y1)
				if nx1-nx0+1 > maxW || ny1-ny0+1 > maxH {
					continue
				}
				cmp.used = true
				x0, y0, x1, y1 = nx0, ny0, nx1, ny1
				members = append(members, k)
				grew = true
			}
		}
		c2 := cluster{members: members, box: [4]int{x0, y0, x1 + 1, y1 + 1}}
		for _, k := range members {
			c2.pixels += len(comps[k].pixels)
			for _, p := range comps[k].pixels {
				c2.mass += uint64(e.counts[p])
			}
		}
		if x1-x0+1 < minW || y1-y0+1 < minH || c2.pixels < c.MinEdgePixels {
			continue
		}
		clusters = append(clusters, c2)
	}
	if len(clusters) == 0 {
		return nil, LogoFailNoCandidateInLimits
	}
	// Mean persistence a/b > c/d  <=>  a*d > c*b (exact integer compare).
	sort.SliceStable(clusters, func(i, j int) bool {
		x, y := &clusters[i], &clusters[j]
		l, r := x.mass*uint64(y.pixels), y.mass*uint64(x.pixels)
		if l != r {
			return l > r
		}
		return x.pixels > y.pixels
	})
	if len(clusters) > maxLogoCandidates {
		clusters = clusters[:maxLogoCandidates]
	}
	models := make([]*logoModel, len(clusters))
	for i := range clusters {
		m := &logoModel{mask: make([]uint64, e.words), pixels: clusters[i].pixels, box: clusters[i].box, origin: LogoOriginLearned}
		for _, k := range clusters[i].members {
			for _, p := range comps[k].pixels {
				setBit(m.mask, int(p))
			}
		}
		m.trim()
		m.background(e.rects, e.rectOff)
		models[i] = m
	}
	return models, ""
}

// background derives the non-mask pixels of the model's box in the bit
// layout given by rects/rectOff.
func (m *logoModel) background(rects [][4]int, rectOff []int) {
	if m.scaled {
		return
	}
	m.bg, m.bgPixels = make([]uint64, len(m.mask)), 0
	for ri, r := range rects {
		rw := r[2] - r[0]
		for y := max(r[1], m.box[1]); y < min(r[3], m.box[3]); y++ {
			for x := max(r[0], m.box[0]); x < min(r[2], m.box[2]); x++ {
				if p := rectOff[ri] + (y-r[1])*rw + (x - r[0]); !hasBit(m.mask, p) {
					setBit(m.bg, p)
					m.bgPixels++
				}
			}
		}
	}
	m.b0, m.b1 = 0, len(m.bg)
	for m.b0 < m.b1 && m.bg[m.b0] == 0 {
		m.b0++
	}
	for m.b1 > m.b0 && m.bg[m.b1-1] == 0 {
		m.b1--
	}
}

func (m *logoModel) trim() {
	m.w0, m.w1 = 0, len(m.mask)
	for m.w0 < m.w1 && m.mask[m.w0] == 0 {
		m.w0++
	}
	for m.w1 > m.w0 && m.mask[m.w1-1] == 0 {
		m.w1--
	}
}

// logoTrial is one logo candidate of an epoch with what its presence
// decision needs: per sample, the logo edge pixels found (hits) and the
// edge pixels around the logo inside its box (noise). The decision keys
// (thresholds, smoothing, validation) only enter in decideLogo, so they can
// change when saved measurements are classified again (PIPE-03).
type logoTrial struct {
	box              [4]int // analysis pixels x0,y0,x1,y1
	pixels, bgPixels int
	// learned is the exportable asset of a learned candidate; nil for a
	// supplied asset.
	learned     *LogoAsset
	hits, noise []uint32
	// shift is, per sample, the most mask pixels found with the mask moved
	// logoShiftPixels in any of four directions: text or texture matches a
	// shifted mask about as well as the aligned one, a logo does not.
	// Zero for enlarged assets, which match within a distance.
	shift []uint32
	// selfShift is the most mask pixels the moved mask shares with the
	// mask itself: what shift reads when the logo alone is on screen.
	selfShift int
}

// logoEpochObs is the logo measurement of one geometry epoch.
type logoEpochObs struct {
	start, end   int64 // media time
	aw, ah       int
	samples      []logoSample
	learnN       int
	origin       string
	assetVersion string
	failed       string // why no candidate exists
	trials       []logoTrial
}

// measure learns each epoch's candidates (best first) and counts every
// stored observation against each of them. The raw edge observations are
// not needed afterwards.
func (l *logoCollector) measure(end int64) []logoEpochObs {
	if n := len(l.epochs); n > 0 {
		l.epochs[n-1].endPTS = end
	}
	out := make([]logoEpochObs, 0, len(l.epochs))
	for _, e := range l.epochs {
		ob := logoEpochObs{start: e.startPTS, end: e.endPTS, aw: e.geom.aw, ah: e.geom.ah, samples: e.samples, learnN: e.learnN, origin: LogoOriginLearned}
		if l.asset != nil {
			ob.origin, ob.assetVersion = LogoOriginAsset, l.asset.AssetVersion
		}
		var models []*logoModel
		if e.model != nil {
			models = []*logoModel{e.model}
		} else if e.failed == "" && l.asset == nil {
			models, e.failed = l.learn(e)
		}
		ob.failed = e.failed
		for _, m := range models {
			tr := logoTrial{box: m.box, pixels: m.pixels, bgPixels: m.bgPixels, hits: make([]uint32, len(e.samples)), noise: make([]uint32, len(e.samples)), shift: make([]uint32, len(e.samples))}
			var shifted [][]uint64
			if m.reach == 0 {
				shifted = m.shiftedMasks(e)
				for _, sm := range shifted {
					n := 0
					for k := range sm {
						n += bits.OnesCount64(m.mask[k] & sm[k])
					}
					tr.selfShift = max(tr.selfShift, n)
				}
			}
			if m.origin == LogoOriginLearned {
				tr.learned = m.export(e)
			}
			for i, s := range e.samples {
				if !s.usable {
					continue
				}
				row := e.data[i*e.words : (i+1)*e.words]
				hit, noise := 0, 0
				if m.reach > 0 {
					hit = m.reachHits(row, e)
				} else {
					for k := m.w0; k < m.w1; k++ {
						hit += bits.OnesCount64(row[k] & m.mask[k])
					}
				}
				for k := m.b0; k < m.b1; k++ {
					noise += bits.OnesCount64(row[k] & m.bg[k])
				}
				best := 0
				for _, sm := range shifted {
					n := 0
					for k := range sm {
						n += bits.OnesCount64(row[k] & sm[k])
					}
					best = max(best, n)
				}
				tr.hits[i], tr.noise[i], tr.shift[i] = uint32(hit), uint32(noise), uint32(best)
			}
			ob.trials = append(ob.trials, tr)
		}
		out = append(out, ob)
	}
	return out
}

// logoShiftPixels is how far (analysis pixels) the mask is moved for the
// specificity count.
const logoShiftPixels = 3

// shiftedMasks returns the mask moved logoShiftPixels right, left, down and
// up within the epoch's region layout.
func (m *logoModel) shiftedMasks(e *logoEpoch) [][]uint64 {
	moves := [...][2]int{{logoShiftPixels, 0}, {-logoShiftPixels, 0}, {0, logoShiftPixels}, {0, -logoShiftPixels}}
	out := make([][]uint64, len(moves))
	for k, mv := range moves {
		sm := make([]uint64, len(m.mask))
		for ri, r := range e.rects {
			rw, rh := r[2]-r[0], r[3]-r[1]
			off := e.rectOff[ri]
			for p := 0; p < rw*rh; p++ {
				if !hasBit(m.mask, off+p) {
					continue
				}
				x, y := p%rw+mv[0], p/rw+mv[1]
				if x >= 0 && y >= 0 && x < rw && y < rh {
					setBit(sm, off+y*rw+x)
				}
			}
		}
		out[k] = sm
	}
	return out
}

// reachHits counts the mask pixels with an observed edge within m.reach
// (an enlarged asset; the epoch then has the single rectangle of the
// projected box).
func (m *logoModel) reachHits(row []uint64, e *logoEpoch) int {
	r := e.rects[0]
	rw, rh := r[2]-r[0], r[3]-r[1]
	hit := 0
	for p := m.w0 * wordBits; p < m.w1*wordBits && p < rw*rh; p++ {
		if !hasBit(m.mask, p) {
			continue
		}
		x, y := p%rw, p/rw
	search:
		for dy := -m.reach; dy <= m.reach; dy++ {
			yy := y + dy
			if yy < 0 || yy >= rh {
				continue
			}
			for dx := -m.reach; dx <= m.reach; dx++ {
				xx := x + dx
				if xx >= 0 && xx < rw && hasBit(row, yy*rw+xx) {
					hit++
					break search
				}
			}
		}
	}
	return hit
}

// evaluate turns one candidate's counts into smoothed presence states and
// validates the result (LOGO-03, LOGO-04). Spans are appended to spans; rec
// receives the measurements. It returns the extended spans and the
// validation failure, if any.
func evaluateLogo(cfg *Config, ob *logoEpochObs, tr *logoTrial, spans []logoSpan, rec *LogoCalibration) ([]logoSpan, string) {
	c := &cfg.Logo
	sampleUS := us(c.SampleInterval)
	rec.EdgePixels = tr.pixels
	rec.Box = &Region{
		X: float64(tr.box[0]) / float64(ob.aw), Y: float64(tr.box[1]) / float64(ob.ah),
		W: float64(tr.box[2]-tr.box[0]) / float64(ob.aw), H: float64(tr.box[3]-tr.box[1]) / float64(ob.ah),
	}
	presentT, absentT := c.PresentThreshold, c.AbsentThreshold
	if c.ThresholdMode == LogoThresholdRelative {
		ref, ok := logoReference(cfg, tr, ob)
		if !ok {
			return spans, LogoFailInsufficientCoverage
		}
		presentT, absentT = c.RelativePresent*ref, c.RelativeAbsent*ref
		rec.Reference = ptr(ref)
	}
	rec.PresentThreshold, rec.AbsentThreshold = ptr(presentT), ptr(absentT)
	holdAmbiguous := c.AmbiguousState != LogoAmbiguousUnknown
	// Raw states with hysteresis between the absent and present thresholds.
	first := len(spans)
	prev := logoUnknown
	maxStep := logoMaxKnownSteps * sampleUS
	for i, s := range ob.samples {
		segEnd := ob.end
		if i+1 < len(ob.samples) {
			segEnd = ob.samples[i+1].pts
		}
		state, match := logoUnknown, float64(0)
		if s.usable {
			match = float64(tr.hits[i]) / float64(tr.pixels)
			// Dense texture lights up the mask and its surroundings alike,
			// so the logo cannot be told from the picture: unknown.
			switch {
			case tr.bgPixels > 0 && float64(tr.noise[i]) > c.MaxBackgroundDensity*float64(tr.bgPixels):
				state = logoUnknown
			case c.MinSpecificity > 0 && match >= presentT && tr.pixels > tr.selfShift &&
				float64(int64(tr.hits[i])-int64(tr.shift[i])) < c.MinSpecificity*float64(tr.pixels-tr.selfShift):
				// The mask matches almost as well when moved: text or
				// texture where the logo would be, not the logo.
				state = logoUnknown
			case match >= presentT:
				state = logoPresent
			case match <= absentT:
				state = logoAbsent
			case holdAmbiguous:
				state = prev
			default:
				// Between the thresholds: neither the logo nor its absence.
				state = logoUnknown
			}
			prev = state
		}
		// An observation never vouches for more than three sample
		// intervals; beyond that the state is unknown.
		known := min(segEnd, s.pts+maxStep)
		spans = appendLogoSpan(spans, first, logoSpan{start: s.pts, end: known, state: state, uncertainty: sampleUS, meanMatch: match})
		if known < segEnd {
			spans = appendLogoSpan(spans, first, logoSpan{start: known, end: segEnd, state: logoUnknown, uncertainty: sampleUS})
		}
	}
	// Time-based smoothing: a run shorter than min_state_duration is
	// absorbed by the run before it (the first run by the one after).
	spans = smoothLogoSpansBy(spans, first, logoMinDurations(c))

	var present, known int64
	for _, s := range spans[first:] {
		if s.state != logoUnknown {
			known += s.end - s.start
			if s.state == logoPresent {
				present += s.end - s.start
			}
		}
	}
	dur := ob.end - ob.start
	rec.Coverage, rec.Occupancy = nil, nil
	if dur > 0 {
		rec.Coverage = ptr(float64(known) / float64(dur))
	}
	if known > 0 {
		rec.Occupancy = ptr(float64(present) / float64(known))
	}
	switch {
	case dur <= 0 || known == 0 || float64(known) < c.MinCoverage*float64(dur):
		return spans, LogoFailInsufficientCoverage
	case float64(present) < c.MinOccupancy*float64(known):
		return spans, LogoFailOccupancyBelow
	case float64(present) > c.MaxOccupancy*float64(known):
		return spans, LogoFailOccupancyAbove
	}
	return spans, ""
}

// logoReference is the candidate's own logo level: the configured quantile
// of its match measures over usable samples that are not too textured to
// judge. It reports false when there are none.
func logoReference(cfg *Config, tr *logoTrial, ob *logoEpochObs) (float64, bool) {
	c := &cfg.Logo
	vals := make([]float64, 0, len(ob.samples))
	for i, s := range ob.samples {
		if !s.usable || (tr.bgPixels > 0 && float64(tr.noise[i]) > c.MaxBackgroundDensity*float64(tr.bgPixels)) {
			continue
		}
		vals = append(vals, float64(tr.hits[i])/float64(tr.pixels))
	}
	if len(vals) == 0 {
		return 0, false
	}
	sort.Float64s(vals)
	k := int(math.Ceil(c.ReferenceQuantile*float64(len(vals)))) - 1
	return vals[max(0, min(k, len(vals)-1))], true
}

// decideLogo validates each epoch's candidates best-first and returns
// smoothed presence spans plus the calibration records, in media time. The
// first candidate that passes is used; when none passes the epoch's logo
// state is unknown, never absent (LOGO-04). onLearned receives each
// accepted learned logo.
func decideLogo(cfg *Config, obs []logoEpochObs, onLearned func(*LogoAsset)) ([]logoSpan, []LogoCalibration, bool) {
	var spans []logoSpan
	var cal []LogoCalibration
	usable := false
	for k := range obs {
		ob := &obs[k]
		rec := LogoCalibration{StartUS: ob.start, EndUS: ob.end, Origin: ob.origin, AssetVersion: ob.assetVersion, Samples: len(ob.samples), LearnSamples: ob.learnN}
		if len(ob.trials) == 0 {
			rec.Status, rec.Failed = StatusUnavailable, ob.failed
			if rec.Failed == "" {
				rec.Failed = LogoFailNoObservations
			}
			cal = append(cal, rec)
			continue
		}
		first := len(spans)
		var firstRec LogoCalibration
		accepted := false
		for i := range ob.trials {
			tr := &ob.trials[i]
			try := rec
			var failed string
			spans, failed = evaluateLogo(cfg, ob, tr, spans[:first], &try)
			if failed == "" {
				rec, accepted = try, true
				rec.Status = StatusUsable
				if onLearned != nil && tr.learned != nil {
					cp := *tr.learned
					onLearned(&cp)
				}
				break
			}
			if i == 0 {
				firstRec = try
				firstRec.Failed = failed
			}
		}
		if accepted {
			usable = true
			if trim := us(cfg.Logo.PresentTrim); trim > 0 {
				spans = trimPresent(spans, first, trim)
			}
		} else {
			rec = firstRec
			rec.Status = StatusUnreliable
			spans = append(spans[:first], logoSpan{start: ob.start, end: ob.end, state: logoUnknown})
		}
		cal = append(cal, rec)
	}
	return spans, cal, usable
}

// trimPresent gives up trim at both ends of each present run (from index
// first on) to unknown (C6).
func trimPresent(spans []logoSpan, first int, trim int64) []logoSpan {
	out := make([]logoSpan, 0, len(spans)-first+8)
	for _, s := range spans[first:] {
		if s.state != logoPresent {
			out = appendLogoSpan(out, 0, s)
			continue
		}
		cut := min(trim, (s.end-s.start)/2)
		out = appendLogoSpan(out, 0, logoSpan{start: s.start, end: s.start + cut, state: logoUnknown, uncertainty: s.uncertainty})
		mid := s
		mid.start, mid.end = s.start+cut, s.end-cut
		out = appendLogoSpan(out, 0, mid)
		out = appendLogoSpan(out, 0, logoSpan{start: s.end - cut, end: s.end, state: logoUnknown, uncertainty: s.uncertainty})
	}
	return append(spans[:first], out...)
}

func appendLogoSpan(spans []logoSpan, first int, s logoSpan) []logoSpan {
	if s.end <= s.start {
		return spans
	}
	if n := len(spans); n > first && spans[n-1].state == s.state && spans[n-1].end == s.start {
		p := &spans[n-1]
		d0, d1 := float64(p.end-p.start), float64(s.end-s.start)
		p.meanMatch = (p.meanMatch*d0 + s.meanMatch*d1) / (d0 + d1)
		p.end = s.end
		return spans
	}
	return append(spans, s)
}

// logoMinDurations returns the smoothing length per presence state
// (C6: present and absent runs may differ).
func logoMinDurations(c *LogoConfig) [3]int64 {
	base := us(c.MinStateDuration)
	m := [3]int64{base, base, base}
	if c.MinPresentDuration > 0 {
		m[logoPresent] = us(c.MinPresentDuration)
	}
	if c.MinAbsentDuration > 0 {
		m[logoAbsent] = us(c.MinAbsentDuration)
	}
	return m
}

func smoothLogoSpans(spans []logoSpan, first int, minDur int64) []logoSpan {
	return smoothLogoSpansBy(spans, first, [3]int64{minDur, minDur, minDur})
}

// smoothLogoSpansBy absorbs runs shorter than their state's minimum.
func smoothLogoSpansBy(spans []logoSpan, first int, minDur [3]int64) []logoSpan {
	if (minDur[0] <= 0 && minDur[1] <= 0 && minDur[2] <= 0) || len(spans)-first < 2 {
		return spans
	}
	out := first
	for i := first; i < len(spans); i++ {
		s := spans[i]
		if out > first {
			p := &spans[out-1]
			if s.state == p.state || s.end-s.start < minDur[s.state] {
				p.end = s.end // absorbed
				continue
			}
		}
		spans[out] = s
		out++
	}
	spans = spans[:out]
	// A too-short first run joins its successor.
	if len(spans)-first >= 2 && spans[first].end-spans[first].start < minDur[spans[first].state] {
		spans[first+1].start = spans[first].start
		spans = append(spans[:first], spans[first+1:]...)
	}
	// Coalesce equal neighbors produced by absorption.
	out = first
	for i := first; i < len(spans); i++ {
		if out > first && spans[out-1].state == spans[i].state {
			spans[out-1].end = spans[i].end
			continue
		}
		spans[out] = spans[i]
		out++
	}
	return spans[:out]
}
