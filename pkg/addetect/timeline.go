// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"math"
	"sort"
	"unsafe"
)

// analysis is the post-extraction state of one recording. Everything here
// is on the normalized timeline: microseconds from the first presented
// video frame (TIME-02).
// Retained sizes of the classification structures (memory budget).
const (
	blockBytes   = int64(unsafe.Sizeof(block{}))
	prefixBytes  = int64(unsafe.Sizeof(prefix{}))
	hypEvalBytes = int64(unsafe.Sizeof(hypEval{}))
	// selectionBytesPerBlock covers the per-block arrays of the selection
	// pass: last end block, base id, value, unit count, choice and chosen id.
	selectionBytesPerBlock = 4 + 4 + 8 + 4 + 4 + 4
)

// checkClassificationBudget fails the analysis before scoring when the
// classification structures, added to the retained features, would exceed
// resources.memory_budget (QUAL-02): blocks, prefix sums, the selection
// arrays and the hypotheses that diagnostics retain.
func (a *analysis) checkClassificationBudget() {
	if a.stop == nil {
		// Extraction already failed (possibly over the budget itself): the
		// partial result is still assembled from what is held.
		return
	}
	n := int64(len(a.blocks))
	need := a.featureBytes + int64(cap(a.blocks))*blockBytes + int64(cap(a.pre))*prefixBytes + (n+1)*selectionBytesPerBlock
	if lvl := a.cfg.Outputs.Diagnostics; lvl == DiagnosticsFull || lvl == DiagnosticsDetailed {
		most := n * int64(a.cfg.Scoring.MaxUnitBlocks)
		need += min(most, int64(a.cfg.Outputs.MaxHypotheses)) * hypEvalBytes
	}
	if budget := int64(a.cfg.Resources.MemoryBudget); need > budget {
		a.err = Errorf(KindResourceLimit, "classification of %d blocks needs about %d bytes, over resources.memory_budget (%s)", n, need, a.cfg.Resources.MemoryBudget)
	}
}

// halted polls for cancellation every cancelCheckMask+1 steps of a
// classification loop and remembers a positive answer.
func (a *analysis) halted(step int) bool {
	if a.stopped {
		return true
	}
	if a.stop != nil && step&cancelCheckMask == 0 && a.stop() {
		a.stopped = true
	}
	return a.stopped
}

// measured is everything extraction produces for classification: the
// normalized timeline and every detector's compact measurements, plus the
// extraction counters the result reports. It is exactly what a feature file
// holds (PIPE-03); classification reads nothing else from extraction.
type measured struct {
	frames         []frameRec
	t0             int64 // normalization offset (media PTS of the first frame)
	dur            int64
	frameUS        int64 // median frame duration
	lastEnd        int64 // end of the last frame
	finalUS        int64
	finalEstimated bool
	gaps           []TimeRange

	scene      []SceneEvent
	resolution []ResolutionEvent
	aspect     []aspectSpan
	logoObs    []logoEpochObs // media time; decided in decideLogo
	caps       []capSpan
	phraseHits []PhraseHit // opt-in caption phrase matches
	bumpers    []BumperEvent
	audio      []AudioWindow
	audioKnown int64

	epochs        []geomEpoch
	regN          int       // region statistics per frame (0 = none)
	regStats      []regStat // frame-major, regN per frame
	rebases       []Rebase
	overlaps      int
	missingPTS    int
	decodeErrs    int
	corrupt       int
	interlaced    int
	haveAudio     bool
	firstAudio    int64
	bumpersActive bool
	featureBytes  int64 // retained extraction storage (memory budget)
	firstPTS      int64 // media time of the first frame, for progress
	lastPTS       int64
}

// decideLogo turns the logo measurements into presence spans and
// calibration records on the normalized timeline.
func (a *analysis) decideLogo(onLearned func(*LogoAsset)) {
	if !a.cfg.Signals.Logo {
		return
	}
	a.logo, a.logoCal, a.logoUsable = decideLogo(a.cfg, a.logoObs, onLearned)
	for i := range a.logo {
		a.logo[i].start -= a.t0
		a.logo[i].end = min(a.logo[i].end-a.t0, a.dur)
	}
	for i := range a.logoCal {
		a.logoCal[i].StartUS -= a.t0
		a.logoCal[i].EndUS = min(a.logoCal[i].EndUS-a.t0, a.dur)
	}
}

// takeExtraction copies the extractor state classification and the result
// need into the measurements, after which the extractor (with its raw logo
// observations) can be released.
func (a *analysis) takeExtraction(x *extractor, firstPTS int64) {
	m := &a.measured
	m.epochs, m.rebases = x.epochs, x.rebases
	m.regN, m.regStats = x.regN, x.regStats
	m.overlaps, m.missingPTS, m.decodeErrs, m.corrupt, m.interlaced = x.overlaps, x.missingPTS, x.decodeErrs, x.corrupt, x.interlaced
	m.haveAudio, m.firstAudio = x.haveAudio, x.firstAudio
	m.bumpersActive = x.bumpers.active()
	m.featureBytes = x.memoryUse()
	m.firstPTS, m.lastPTS = firstPTS, x.lastPTS
}

type analysis struct {
	cfg *Config
	x   *extractor

	// stop reports a canceled analysis during classification; nil when
	// classification must run to the end (a partial result after a failed
	// extraction). stopped records that a stage gave up because of it.
	stop    func() bool
	stopped bool
	// err ends classification early (a resource limit).
	err error
	// threshold is the score threshold in use: scoring.threshold, or the
	// percentile threshold when that is higher.
	threshold float64

	measured

	darkCal    DarkCalibration
	silenceCal SilenceCalibration
	silenceThr float32   // mean-square silence level in use
	notes      []Warning // classification warnings (C3)

	adPhraseT, progPhraseT []int64 // sorted phrase hit times per list
	dark                   []DarkEvent
	silence                []SilenceEvent
	logo                   []logoSpan
	logoCal                []LogoCalibration
	logoUsable             bool

	bounds []boundary
	blocks []block
	pre    []prefix
	base   baselineSet

	hyps      []hypEval // retained hypotheses
	evaluated int
	truncated bool
	units     []unit
	runs      []adRun
}

// buildTimeline establishes the normalized timeline and coverage (TIME-01
// to TIME-03).
func (a *analysis) buildTimeline(durationHint int64) error {
	x := a.x
	n := len(x.frames)
	if n == 0 {
		return Errorf(KindMedia, "no decodable video frames")
	}
	if float64(x.missingPTS) > a.cfg.Input.MaxMissingTimestamps*float64(n) {
		return Errorf(KindTiming, "%d of %d video frames carry no timestamp; a reliable timeline cannot be established", x.missingPTS, n)
	}
	a.frames = x.frames
	a.t0 = a.frames[0].pts

	// Median frame duration from up to 4096 evenly spaced continuous deltas.
	var sample []int64
	step := max(1, (n-1)/maxFrameDeltaSamples)
	for i := 1; i < n; i += step {
		if a.frames[i-1].flags&fGapAfter == 0 {
			sample = append(sample, a.frames[i].pts-a.frames[i-1].pts)
		}
	}
	if len(sample) > 0 {
		sort.Slice(sample, func(i, j int) bool { return sample[i] < sample[j] })
		a.frameUS = sample[len(sample)/2]
	}
	if a.frameUS <= 0 {
		a.frameUS = defaultFrameUS
		if x.lastDur > 0 {
			a.frameUS = x.lastDur
		}
	}
	// The final frame's duration comes from the media when declared;
	// otherwise the median frame duration is used and reported (TIME-02).
	a.finalUS, a.finalEstimated = x.lastDur, false
	if a.finalUS <= 0 {
		a.finalUS, a.finalEstimated = a.frameUS, true
	}
	end := a.frames[n-1].pts + a.finalUS
	a.lastEnd = end - a.t0

	gaps := append([]TimeRange(nil), x.gaps...)
	// A declared missing interval after the last frame keeps the timeline
	// from being silently shortened (PACK-08).
	for _, g := range x.declared {
		if g.EndUS > end && g.StartUS < g.EndUS {
			gaps = append(gaps, TimeRange{StartUS: max(g.StartUS, end), EndUS: g.EndUS, Reason: g.Reason})
			end = g.EndUS
		}
	}
	// A declared loss shorter than the timestamp gap threshold leaves no
	// timestamp gap. When no frame was presented inside it, it is still
	// missing coverage (TIME-03, PACK-08).
	for _, d := range x.declared {
		overlaps := false
		for _, g := range gaps {
			overlaps = overlaps || (d.StartUS < g.EndUS && d.EndUS > g.StartUS)
		}
		k := sort.Search(n, func(k int) bool { return a.frames[k].pts >= d.StartUS })
		if overlaps || k == 0 || k == n || a.frames[k].pts < d.EndUS {
			continue
		}
		start := max(d.StartUS, a.frames[k-1].pts+1)
		if stop := min(d.EndUS, a.frames[k].pts); stop > start {
			a.frames[k-1].flags |= fGapAfter
			gaps = append(gaps, TimeRange{StartUS: start, EndUS: stop, Reason: d.Reason})
		}
	}
	for i := range gaps {
		for _, d := range x.declared {
			if d.StartUS < gaps[i].EndUS && d.EndUS > gaps[i].StartUS && d.Reason != "" {
				gaps[i].Reason = d.Reason
			}
		}
		gaps[i].StartUS -= a.t0
		gaps[i].EndUS -= a.t0
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].StartUS < gaps[j].StartUS })
	a.gaps = gaps
	a.dur = end - a.t0
	if a.dur <= 0 {
		return Errorf(KindTiming, "the presentation has no positive duration")
	}

	// Normalize every stored time once (PACK-06: the whole presentation,
	// not each resource).
	t0 := a.t0
	for i := range a.frames {
		a.frames[i].pts -= t0
	}
	a.scene = x.scene.events
	sort.SliceStable(a.scene, func(i, j int) bool { return a.scene[i].TimeUS < a.scene[j].TimeUS })
	for i := range a.scene {
		a.scene[i].TimeUS -= t0
	}
	a.resolution = x.resolution
	for i := range a.resolution {
		a.resolution[i].TimeUS -= t0
	}
	x.aspect.finish(end)
	a.aspect = x.aspect.spans
	for i := range a.aspect {
		a.aspect[i].start -= t0
		a.aspect[i].end = min(a.aspect[i].end-t0, a.dur)
	}
	// A coverage gap is unobserved: no span vouches for the active picture
	// inside it (TIME-03).
	if len(a.gaps) > 0 {
		cut := make([]aspectSpan, 0, len(a.aspect)+len(a.gaps))
		for _, s := range a.aspect {
			k := sort.Search(len(a.gaps), func(k int) bool { return a.gaps[k].EndUS > s.start })
			for ; k < len(a.gaps) && a.gaps[k].StartUS < s.end; k++ {
				if g := a.gaps[k]; g.StartUS > s.start {
					head := s
					head.end = g.StartUS
					cut = append(cut, head)
				}
				s.start = min(a.gaps[k].EndUS, s.end)
			}
			if s.end > s.start {
				cut = append(cut, s)
			}
		}
		a.aspect = cut
	}
	x.bumpers.finish(end)
	a.bumpers = x.bumpers.events
	sort.SliceStable(a.bumpers, func(i, j int) bool { return a.bumpers[i].StartUS < a.bumpers[j].StartUS })
	for i := range a.bumpers {
		a.bumpers[i].StartUS -= t0
		a.bumpers[i].EndUS = min(a.bumpers[i].EndUS-t0, a.dur)
	}
	x.caps.shift(t0)
	a.caps = x.caps.spans(a.dur, a.frameUS)
	if x.caps.phrases != nil {
		for _, h := range x.caps.phrases.hits {
			if t := h.TimeUS - t0; t >= 0 && t < a.dur {
				h.TimeUS = t
				a.phraseHits = append(a.phraseHits, h)
			}
		}
	}

	// Audio windows: order, normalize, clip to the video timeline.
	aw := x.audio
	sorted := true
	for i := 1; i < len(aw); i++ {
		if aw[i].StartUS < aw[i-1].StartUS {
			sorted = false
			break
		}
	}
	if !sorted {
		sort.SliceStable(aw, func(i, j int) bool { return aw[i].StartUS < aw[j].StartUS })
	}
	k := 0
	var prevEnd int64 = math.MinInt64
	for _, w := range aw {
		w.StartUS -= t0
		e := w.StartUS + int64(w.DurationUS)
		if e <= 0 || w.StartUS >= a.dur || w.DurationUS <= 0 {
			continue
		}
		// Overlapping audio is never counted twice.
		if w.StartUS < prevEnd {
			if e <= prevEnd {
				continue
			}
			w.DurationUS -= int32(prevEnd - w.StartUS)
			w.StartUS = prevEnd
		}
		prevEnd = e
		aw[k] = w
		k++
		a.audioKnown += min(e, a.dur) - max(w.StartUS, 0)
	}
	a.audio = aw[:k]
	return nil
}

// frameEnd returns the end of frame i's presentation interval.
func (a *analysis) frameEnd(i int) int64 {
	if i == len(a.frames)-1 {
		return a.lastEnd
	}
	if a.frames[i].flags&fGapAfter != 0 {
		pts := a.frames[i].pts
		k := sort.Search(len(a.gaps), func(k int) bool { return a.gaps[k].StartUS > pts })
		if k < len(a.gaps) {
			return a.gaps[k].StartUS
		}
	}
	return a.frames[i+1].pts
}

// nearestFrameTime quantizes t to the closest frame presentation boundary.
func (a *analysis) nearestFrameTime(t int64) int64 {
	n := len(a.frames)
	i := sort.Search(n, func(i int) bool { return a.frames[i].pts >= t })
	if i == n {
		if a.lastEnd-t <= t-a.frames[n-1].pts {
			return a.lastEnd
		}
		return a.frames[n-1].pts
	}
	if i > 0 && t-a.frames[i-1].pts <= a.frames[i].pts-t {
		return a.frames[i-1].pts
	}
	return a.frames[i].pts
}

// videoCovered returns the duration of [s,e) outside coverage gaps.
func (a *analysis) gapOverlap(s, e int64) int64 {
	var g int64
	k := sort.Search(len(a.gaps), func(k int) bool { return a.gaps[k].EndUS > s })
	for ; k < len(a.gaps) && a.gaps[k].StartUS < e; k++ {
		g += min(e, a.gaps[k].EndUS) - max(s, a.gaps[k].StartUS)
	}
	return g
}

// detectDark classifies frames with the fixed or calibrated thresholds and
// merges consecutive qualifying frames into events (VIS-01 to VIS-03).
// logoMaskedDark returns the C5 test: whether frame i is dark once the
// search regions holding the validated logo of its epoch are left out. It
// returns nil when the test does not apply (switched off, no region
// statistics, or no usable logo).
func (a *analysis) logoMaskedDark(darkThreshold float64) func(i int) bool {
	c := &a.cfg.Dark
	if !c.IgnoreLogoRegion || a.regN == 0 || !a.logoUsable || len(a.logoCal) != len(a.epochs) {
		return nil
	}
	nReg := a.regN - 1
	type epochMask struct {
		use    []bool  // region statistics that count (the rest always does)
		counts []int64 // pixels per region statistic
		any    bool
	}
	masks := make([]epochMask, len(a.epochs))
	for k, ep := range a.epochs {
		cal := &a.logoCal[k]
		if cal.Status != StatusUsable || cal.Box == nil {
			continue
		}
		rects, meas := searchRects(a.cfg, ep.aw, ep.ah)
		m := epochMask{use: make([]bool, a.regN), counts: make([]int64, a.regN)}
		rest := int64((meas[2] - meas[0]) * (meas[3] - meas[1]))
		box := *cal.Box
		for r, rc := range rects {
			n := int64((rc[2] - rc[0]) * (rc[3] - rc[1]))
			m.counts[r] = n
			rest -= n
			sr := a.cfg.Logo.SearchRegions[r]
			overlaps := sr.X < box.X+box.W && box.X < sr.X+sr.W && sr.Y < box.Y+box.H && box.Y < sr.Y+sr.H
			m.use[r] = !overlaps
			m.any = m.any || overlaps
		}
		m.counts[nReg], m.use[nReg] = rest, true
		masks[k] = m
	}
	maxBright := c.MaxBrightFraction
	maxStd := c.DarkMaxNonuniformity
	epoch := 0
	return func(i int) bool {
		for epoch+1 < len(a.epochs) && a.epochs[epoch+1].firstFrame <= i {
			epoch++
		}
		m := &masks[epoch]
		if !m.any {
			return false
		}
		var n, sum, sumsq, bright float64
		for r, st := range a.regStats[i*a.regN : (i+1)*a.regN] {
			if !m.use[r] || m.counts[r] == 0 {
				continue
			}
			w := float64(m.counts[r])
			mean, std := float64(st.mean)/lumaFixedScale, float64(st.std)/lumaFixedScale
			n += w
			sum += mean * w
			sumsq += (std*std + mean*mean) * w
			bright += float64(st.bright) / ratioFixedScale * w
		}
		if n == 0 {
			return false
		}
		mean := sum / n
		std := math.Sqrt(max(sumsq/n-mean*mean, 0))
		return mean <= darkThreshold && bright/n <= maxBright && std <= maxStd
	}
}

func (a *analysis) detectDark() {
	c := &a.cfg.Dark
	cal := DarkCalibration{Mode: c.Mode, DarkThreshold: c.MaxMeanLuma, UniformThreshold: c.UniformMaxNonunif, UsableFrames: len(a.frames)}
	if c.Mode == DarkModeAuto {
		a.calibrateDark(&cal)
	}
	a.darkCal = cal
	if !a.cfg.Signals.DarkUniform {
		return
	}
	darkT := uint16(math.Round(cal.DarkThreshold * lumaFixedScale))
	uniT := uint16(math.Round(cal.UniformThreshold * lumaFixedScale))
	darkStd := uint16(math.Round(c.DarkMaxNonuniformity * lumaFixedScale))
	brightT := uint16(math.Round(c.MaxBrightFraction * ratioFixedScale))
	const (
		none = iota
		dark
		uniform
	)
	names := [...]string{"", KindDark, KindUniform}
	cur, start := none, 0
	var minB, sumB, sumU float64
	flush := func(endIdx int) {
		if cur != none {
			nf := endIdx - start
			a.dark = append(a.dark, DarkEvent{
				StartUS: a.frames[start].pts, EndUS: a.frameEnd(endIdx - 1), Kind: names[cur], Frames: nf,
				MinBrightness: minB, MeanBrightness: sumB / float64(nf), MeanNonuniformity: sumU / float64(nf), Coverage: 1,
			})
		}
		cur = none
	}
	masked := a.logoMaskedDark(cal.DarkThreshold)
	for i := range a.frames {
		f := &a.frames[i]
		k := none
		switch {
		case f.mean <= darkT && f.bright <= brightT && f.std <= darkStd:
			k = dark
		case masked != nil && masked(i):
			k = dark
		case c.DetectUniform && f.std <= uniT && f.mean > darkT:
			k = uniform
		}
		if k != cur || (i > 0 && a.frames[i-1].flags&fGapAfter != 0) {
			flush(i)
			if k != none {
				cur, start, minB, sumB, sumU = k, i, lumaLevels, 0, 0
			}
		}
		if k != none {
			m := f.meanLuma()
			minB = min(minB, m)
			sumB += m
			sumU += f.stdLuma()
		}
	}
	flush(len(a.frames))
}

// calibrateDark estimates the dark and uniform thresholds from the
// recording (VIS-02).
//
// Among frames that are spatially flat (nonuniformity <= auto_uniform_max)
// and pass the bright-pixel safeguard, the darkest cluster is the run of
// populated integer mean-luminance bins starting at the darkest one with
// gaps of at most two empty bins. The dark threshold is the top of that
// cluster plus auto_margin, bounded by auto_min_luma..auto_max_luma. The
// uniform threshold is 1.5x the 95th percentile nonuniformity of the
// cluster, bounded by auto_uniform_min..auto_uniform_max. Any failure keeps
// the fixed thresholds and reports why.
func (a *analysis) calibrateDark(cal *DarkCalibration) {
	c := &a.cfg.Dark
	if len(a.frames) < c.AutoMinFrames {
		cal.FallbackReason = DarkFallbackInsufficientData
		return
	}
	flatT := uint16(math.Round(c.AutoUniformMax * lumaFixedScale))
	brightT := uint16(math.Round(c.MaxBrightFraction * ratioFixedScale))
	var hist [lumaLevels]int
	for i := range a.frames {
		f := &a.frames[i]
		if f.std <= flatT && f.bright <= brightT {
			hist[min((int(f.mean)+lumaFixedScale/2)/lumaFixedScale, lumaMax)]++
		}
	}
	lo := 0
	for lo < lumaLevels && hist[lo] == 0 {
		lo++
	}
	if lo == lumaLevels || float64(lo) > c.AutoMaxLuma {
		cal.FallbackReason = DarkFallbackNoDarkFrames
		return
	}
	top, count, empty := lo, 0, 0
	for b := lo; b < lumaLevels && empty <= darkClusterMaxEmptyBins; b++ {
		if hist[b] == 0 {
			empty++
			continue
		}
		empty, top = 0, b
		count += hist[b]
	}
	if float64(top) > c.AutoMaxLuma {
		cal.FallbackReason = DarkFallbackAmbiguous
		return
	}
	if count < c.AutoMinSupport {
		cal.FallbackReason = DarkFallbackInsufficientSupport
		return
	}
	// 95th percentile nonuniformity inside the cluster, 1/16 luma bins.
	var sh [stdBinCount]int
	topT := uint16((top + 1) * lumaFixedScale)
	for i := range a.frames {
		f := &a.frames[i]
		if f.std <= flatT && f.bright <= brightT && f.mean < topT {
			sh[min(int(f.std)*stdBinsPerLuma/lumaFixedScale, stdBinCount-1)]++
		}
	}
	need, acc, p95 := (count*uniformPercentile+99)/100, 0, 0
	for b, v := range sh {
		acc += v
		if acc >= need {
			p95 = b
			break
		}
	}
	cal.DarkThreshold = math.Max(c.AutoMinLuma, math.Min(float64(top)+c.AutoMargin, c.AutoMaxLuma))
	cal.UniformThreshold = math.Max(c.AutoUniformMin, math.Min(uniformHeadroom*float64(p95)/stdBinsPerLuma, c.AutoUniformMax))
	cal.Estimated, cal.SupportFrames = true, count
}

func dbfs(ms float64) float64 {
	if ms <= minMeanSquare {
		return minDBFS
	}
	return decibelBase * math.Log10(ms)
}

// meanSquare converts a dBFS level to a linear mean-square value.
func meanSquare(dbfsLevel float64) float64 {
	return math.Pow(decibelBase, dbfsLevel/decibelBase)
}

// detectSilence coalesces adjoining silent windows into events (AUDIO-02).
// A window is silent only when its loudest included channel is at or below
// the silence level (AUDIO-01); a hole between windows ends an event because
// missing audio is unknown, never silence.
// calibrateSilence sets the silence level: the fixed key, or (auto mode,
// C2) the recording's floor, the duration-weighted auto_quantile of window
// levels, plus auto_margin_db and bounded, with a fallback reason.
func (a *analysis) calibrateSilence() {
	c := &a.cfg.Audio
	cal := SilenceCalibration{Mode: c.SilenceMode, LevelDBFS: c.SilenceLevel}
	if c.SilenceMode == SilenceModeAuto {
		if a.audioKnown < us(defaultAudioAutoMinSupport) || len(a.audio) == 0 {
			cal.FallbackReason = SilenceFallbackInsufficient
		} else {
			type lw struct {
				db float64
				w  int64
			}
			ws := make([]lw, len(a.audio))
			for i := range a.audio {
				ws[i] = lw{dbfs(float64(a.audio[i].PeakMS)), int64(a.audio[i].DurationUS)}
			}
			sort.Slice(ws, func(x, y int) bool { return ws[x].db < ws[y].db })
			need := c.AutoQuantile * float64(a.audioKnown)
			var acc int64
			floor := ws[len(ws)-1].db
			for _, w := range ws {
				acc += w.w
				if float64(acc) >= need {
					floor = w.db
					break
				}
			}
			cal.FloorDBFS = ptr(floor)
			cal.LevelDBFS = min(max(floor+c.AutoMarginDB, c.AutoMinDBFS), c.AutoMaxDBFS)
			cal.Estimated = true
		}
	}
	a.silenceCal = cal
	a.silenceThr = float32(meanSquare(cal.LevelDBFS))
}

func (a *analysis) detectSilence() {
	a.calibrateSilence()
	if !a.cfg.Signals.Audio || len(a.audio) == 0 {
		return
	}
	thr := a.silenceThr
	minDur := us(a.cfg.Audio.MinSilence)
	slack := us(a.cfg.Audio.Window) / 2
	var start, end int64
	var peak float32
	windows := 0
	flush := func() {
		if windows > 0 {
			s, e := max(start, 0), min(end, a.dur)
			if e-s >= minDur && e > s {
				a.silence = append(a.silence, SilenceEvent{StartUS: s, EndUS: e, PeakDBFS: dbfs(float64(peak)), Windows: windows})
			}
		}
		windows = 0
	}
	for i := range a.audio {
		w := &a.audio[i]
		if w.PeakMS > thr {
			flush()
			continue
		}
		if windows > 0 && w.StartUS-end > slack {
			flush()
		}
		if windows == 0 {
			start, peak = w.StartUS, 0
		}
		end = w.StartUS + int64(w.DurationUS)
		peak = max(peak, w.PeakMS)
		windows++
	}
	flush()
	if ctx := us(a.cfg.Audio.SilenceContext); ctx > 0 {
		a.requireLoudContext(ctx)
	}
}

// requireLoudContext keeps a silence only when the audio within ctx before
// and after it reaches silence_context_db above the silence level; a side
// without audio is neutral (C2).
func (a *analysis) requireLoudContext(ctx int64) {
	loud := float32(meanSquare(a.silenceCal.LevelDBFS + a.cfg.Audio.SilenceContextDB))
	side := func(lo, hi int64) (known, ok bool) {
		i := sort.Search(len(a.audio), func(i int) bool { return a.audio[i].StartUS+int64(a.audio[i].DurationUS) > lo })
		for ; i < len(a.audio) && a.audio[i].StartUS < hi; i++ {
			known = true
			if a.audio[i].PeakMS >= loud {
				return true, true
			}
		}
		return known, false
	}
	keep := a.silence[:0]
	for _, s := range a.silence {
		kb, ob := side(s.StartUS-ctx, s.StartUS)
		ka, oa := side(s.EndUS, s.EndUS+ctx)
		if (kb && !ob) || (ka && !oa) {
			continue
		}
		keep = append(keep, s)
	}
	a.silence = keep
}
