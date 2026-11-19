// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"math"
	"sort"
)

const maxFactors = 9

// block is the interval between two consecutive boundaries with its
// duration-weighted aggregates (BLOCK-01). All *US fields are microseconds
// of known observation inside the block.
type block struct {
	start, end int64
	gapUS      int64 // missing video coverage

	vidUS           int64
	sumMean, sumStd float64 // luminance x microseconds
	scenes          int32
	logoPresent     int64
	logoKnown       int64
	aspKnown        int64
	aspMatch        int64   // known time matching the program baseline
	aspSum          float64 // ratio x microseconds
	cap             [numCaptionStyles]int64
	audKnown        int64
	audSilent       int64
	audEnergy       float64 // mean-square x microseconds
	codedW, codedH  int

	score    float64
	nf       uint8
	factors  [maxFactors]factorRec
	program  uint8 // programLogo | programStable | programLong
	conflict bool
	progOnly bool // scored alone, it carries program evidence and nothing else
	label    uint8
	reasons  uint32
	unit     int32 // index into analysis.units, -1 if none
	run      int32 // index into analysis.runs, -1 if none
	darkTail bool  // a selected unit here was dropped by the dark-tail rule
}

const (
	programLogo uint8 = 1 << iota
	programStable
	programLong
)

const (
	lblUnknown uint8 = iota
	lblProgram
	lblAd
)

var labelNames = [...]string{LabelUnknown, LabelProgram, LabelAdvertisement}

// prefix holds running sums over blocks [0,k) so any consecutive group of
// blocks is aggregated in constant time (SCORE-04).
type prefix struct {
	gapUS, vidUS           int64
	logoPresent, logoKnown int64
	aspKnown, aspMatch     int64
	audKnown, audSilent    int64
	cap                    [numCaptionStyles]int64
	sumMean, sumStd        float64
	audEnergy              float64
	scenes                 int32
	program                int32
}

// baselineSet holds the frozen baselines (BASE-03).
// sumRecognized adds the durations of the recognized caption styles.
func sumRecognized(d *[numCaptionStyles]int64) int64 {
	var t int64
	for st := CaptionRollUp; st < numCaptionStyles; st++ {
		t += d[st]
	}
	return t
}

type baselineSet struct {
	out       Baselines
	capStyle  CaptionStyle
	capOK     bool
	capNone   bool // the program is reliably uncaptioned
	aspect    float64
	aspOK     bool
	bright    float64
	brightOK  bool
	nonunif   float64
	nonunifOK bool
	audio     float64 // linear mean-square
	audioOK   bool
	silence   float64
	silenceOK bool
	scene     float64 // events per second
	sceneOK   bool
}

// presentCursor answers monotone "how much of [s,e) has the logo present"
// queries in amortized constant time.
type presentCursor struct {
	spans []logoSpan
	i     int
}

func (p *presentCursor) overlap(s, e int64) int64 {
	for p.i < len(p.spans) && p.spans[p.i].end <= s {
		p.i++
	}
	var d int64
	for k := p.i; k < len(p.spans) && p.spans[k].start < e; k++ {
		if p.spans[k].state == logoPresent {
			d += min(e, p.spans[k].end) - max(s, p.spans[k].start)
		}
	}
	return d
}

// programBaselines estimates the typical program caption style and active
// ratio from positively supported program observations (BASE-02): first
// time with a validated logo present, then stable intervals longer than the
// maximum ad-unit duration, then the duration-dominant recording value with
// an explicit fallback origin. A level without enough support defers to the
// next; a level with support but without dominance is ambiguous and
// disables the mismatch rule. Baselines never use ad classifications.
func (a *analysis) programBaselines(captionsUsable bool) {
	bc := &a.cfg.Baselines
	minSupport := us(bc.MinSupport)
	maxUnit := a.cfg.unitMaxUS()
	origins := [...]string{OriginLogoPresent, OriginLongStable, OriginRecordingDominant}

	// Caption style.
	capB := Baseline{Origin: OriginNone, Reason: BaselineSignalUnavailable}
	switch {
	case bc.CaptionStyle != "":
		st, _ := ParseCaptionStyle(bc.CaptionStyle)
		a.base.capStyle, a.base.capOK = st, true
		capB = Baseline{Style: st.String(), Origin: OriginChannelProfile, Reliable: true}
	case a.cfg.Signals.Captions && captionsUsable:
		capB = Baseline{Origin: OriginNone, Reason: BaselineInsufficientSupport}
		for level := 0; level < 3; level++ {
			if level == 0 && !a.logoUsable {
				continue
			}
			var d [numCaptionStyles]int64
			pc := presentCursor{spans: a.logo}
			// Observed absence counts only towards an uncaptioned
			// program baseline, which is established only when its rule
			// (caption_unexpected_factor) is enabled.
			noneRule := a.cfg.Scoring.CaptionUnexpectedFactor != 1
			for _, s := range a.caps {
				if !s.style.recognized() && !(noneRule && s.style == CaptionNone) {
					continue
				}
				switch level {
				case 0:
					d[s.style] += pc.overlap(s.start, s.end)
				case 1:
					if s.end-s.start > maxUnit {
						d[s.style] += s.end - s.start
					}
				default:
					d[s.style] += s.end - s.start
				}
			}
			if all := d[CaptionNone] + sumRecognized(&d); noneRule && all >= minSupport && all > 0 &&
				float64(d[CaptionNone]) >= bc.MinDominance*float64(all) {
				dom := float64(d[CaptionNone]) / float64(all)
				capB = Baseline{Style: CaptionNone.String(), SupportUS: all, Dominance: &dom, Origin: origins[level], Reliable: true}
				a.base.capNone = true
				break
			}
			var total int64
			top := CaptionRollUp
			for st := CaptionRollUp; st < numCaptionStyles; st++ {
				total += d[st]
				if d[st] > d[top] {
					top = st
				}
			}
			if total < minSupport || total == 0 {
				continue
			}
			dom := float64(d[top]) / float64(total)
			capB = Baseline{Style: top.String(), SupportUS: total, Dominance: &dom, Origin: origins[level]}
			if dom >= bc.MinDominance {
				capB.Reliable = true
				a.base.capStyle, a.base.capOK = top, true
			} else {
				capB.Reason = BaselineAmbiguous
			}
			break
		}
	}
	a.base.out.CaptionStyle = capB

	// Active-picture ratio.
	aspB := Baseline{Origin: OriginNone, Reason: BaselineSignalUnavailable, Unit: UnitRatio}
	tol := a.cfg.Aspect.RatioTolerance
	switch {
	case bc.AspectRatio > 0:
		a.base.aspect, a.base.aspOK = bc.AspectRatio, true
		aspB = Baseline{Value: ptr(bc.AspectRatio), Unit: UnitRatio, Origin: OriginChannelProfile, Reliable: true}
	case a.cfg.Signals.Aspect && len(a.aspect) > 0:
		aspB = Baseline{Origin: OriginNone, Reason: BaselineInsufficientSupport, Unit: UnitRatio}
		type rd struct {
			r float64
			d int64
		}
		items := make([]rd, 0, len(a.aspect))
		for level := 0; level < 3; level++ {
			if level == 0 && !a.logoUsable {
				continue
			}
			items = items[:0]
			pc := presentCursor{spans: a.logo}
			var total int64
			for _, s := range a.aspect {
				var d int64
				switch level {
				case 0:
					d = pc.overlap(s.start, s.end)
				case 1:
					if s.end-s.start > maxUnit {
						d = s.end - s.start
					}
				default:
					d = s.end - s.start
				}
				if d > 0 {
					items = append(items, rd{s.ratio, d})
					total += d
				}
			}
			if total < minSupport || total == 0 {
				continue
			}
			// Cluster ratios within the tolerance of the cluster's first
			// (smallest) ratio; ties keep the smaller ratio.
			sort.SliceStable(items, func(i, j int) bool { return items[i].r < items[j].r })
			var bestD int64
			var bestR float64
			for i := 0; i < len(items); {
				first := items[i].r
				var d int64
				var wsum float64
				j := i
				for ; j < len(items) && items[j].r-first <= tol*first; j++ {
					d += items[j].d
					wsum += items[j].r * float64(items[j].d)
				}
				if d > bestD {
					bestD, bestR = d, wsum/float64(d)
				}
				i = j
			}
			dom := float64(bestD) / float64(total)
			aspB = Baseline{Value: ptr(bestR), Unit: UnitRatio, SupportUS: total, Dominance: &dom, Origin: origins[level]}
			if dom >= bc.MinDominance {
				aspB.Reliable = true
				a.base.aspect, a.base.aspOK = bestR, true
			} else {
				aspB.Reason = BaselineAmbiguous
			}
			break
		}
	}
	a.base.out.AspectRatio = aspB
}

// buildBlocks partitions the timeline at the fused boundaries and
// aggregates every signal per block with ordered sweeps (BLOCK-01).
func (a *analysis) buildBlocks() {
	nb := len(a.bounds) - 1
	a.blocks = make([]block, nb)
	for i := range a.blocks {
		b := &a.blocks[i]
		b.start, b.end = a.bounds[i].t, a.bounds[i+1].t
		b.gapUS = a.gapOverlap(b.start, b.end)
		b.unit, b.run = -1, -1
	}
	blocks := a.blocks

	// Frames: each frame's presentation interval is split across the blocks
	// it overlaps, so values are weighted by duration, not frame count.
	bi := 0
	epoch := 0
	for i := range a.frames {
		f := &a.frames[i]
		s, e := f.pts, a.frameEnd(i)
		for bi < nb-1 && blocks[bi].end <= s {
			bi++
		}
		for epoch+1 < len(a.epochs) && a.epochs[epoch+1].firstFrame <= i {
			epoch++
		}
		m, sd := f.meanLuma(), f.stdLuma()
		for k := bi; k < nb && blocks[k].start < e; k++ {
			b := &blocks[k]
			ov := min(e, b.end) - max(s, b.start)
			if ov <= 0 {
				continue
			}
			if b.vidUS == 0 {
				b.codedW, b.codedH = a.epochs[epoch].codedW, a.epochs[epoch].codedH
			}
			b.vidUS += ov
			b.sumMean += m * float64(ov)
			b.sumStd += sd * float64(ov)
		}
	}

	bi = 0
	for _, ev := range a.scene {
		for bi < nb-1 && blocks[bi].end <= ev.TimeUS {
			bi++
		}
		blocks[bi].scenes++
	}

	sweep := func(n int, at func(i int) (int64, int64), add func(b *block, i int, ov int64)) {
		bi := 0
		for i := 0; i < n; i++ {
			s, e := at(i)
			for bi < nb-1 && blocks[bi].end <= s {
				bi++
			}
			for k := bi; k < nb && blocks[k].start < e; k++ {
				if ov := min(e, blocks[k].end) - max(s, blocks[k].start); ov > 0 {
					add(&blocks[k], i, ov)
				}
			}
		}
	}
	if a.logoUsable {
		sweep(len(a.logo), func(i int) (int64, int64) { return a.logo[i].start, a.logo[i].end },
			func(b *block, i int, ov int64) {
				switch a.logo[i].state {
				case logoPresent:
					b.logoPresent += ov
					b.logoKnown += ov
				case logoAbsent:
					b.logoKnown += ov
				}
			})
	}
	tol := a.cfg.Aspect.RatioTolerance
	sweep(len(a.aspect), func(i int) (int64, int64) { return a.aspect[i].start, a.aspect[i].end },
		func(b *block, i int, ov int64) {
			r := a.aspect[i].ratio
			b.aspKnown += ov
			b.aspSum += r * float64(ov)
			if a.base.aspOK && math.Abs(r-a.base.aspect) <= tol*a.base.aspect {
				b.aspMatch += ov
			}
		})
	sweep(len(a.caps), func(i int) (int64, int64) { return a.caps[i].start, a.caps[i].end },
		func(b *block, i int, ov int64) { b.cap[a.caps[i].style] += ov })
	if a.silenceCal.Mode == "" {
		a.calibrateSilence() // classification stages called on their own (tests)
	}
	thr := a.silenceThr
	sweep(len(a.audio), func(i int) (int64, int64) {
		return a.audio[i].StartUS, a.audio[i].StartUS + int64(a.audio[i].DurationUS)
	}, func(b *block, i int, ov int64) {
		w := &a.audio[i]
		b.audKnown += ov
		b.audEnergy += float64(w.MeanMS) * float64(ov)
		if w.PeakMS <= thr {
			b.audSilent += ov
		}
	})

	a.pre = make([]prefix, nb+1)
	for i := range blocks {
		b := &blocks[i]
		p := a.pre[i]
		p.gapUS += b.gapUS
		p.vidUS += b.vidUS
		p.logoPresent += b.logoPresent
		p.logoKnown += b.logoKnown
		p.aspKnown += b.aspKnown
		p.aspMatch += b.aspMatch
		p.audKnown += b.audKnown
		p.audSilent += b.audSilent
		for s := range p.cap {
			p.cap[s] += b.cap[s]
		}
		p.sumMean += b.sumMean
		p.sumStd += b.sumStd
		p.audEnergy += b.audEnergy
		p.scenes += b.scenes
		a.pre[i+1] = p
	}
}

// recordingBaselines estimates the recording-relative baselines over usable
// observation time only, so data gaps and differing sampling rates do not
// bias them (BASE-01). Values below their configured floor are raised to it.
func (a *analysis) recordingBaselines() {
	bc := &a.cfg.Baselines
	minSupport := us(bc.MinRecording)
	tot := a.pre[len(a.pre)-1]
	mk := func(value float64, support int64, floor float64, unit string, enabled bool, okOut *bool, valOut *float64) Baseline {
		b := Baseline{Unit: unit, SupportUS: support, Origin: OriginRecording}
		switch {
		case !enabled:
			b.Origin, b.Reason = OriginNone, BaselineSignalUnavailable
		case support <= 0 || support < minSupport:
			b.Reason = BaselineInsufficientSupport
		default:
			if value < floor {
				value, b.Origin = floor, OriginRecordingFloored
			}
			b.Value, b.Reliable = ptr(value), true
			*okOut, *valOut = true, value
		}
		return b
	}
	vid := float64(max(tot.vidUS, 1))
	aud := float64(max(tot.audKnown, 1))
	o := &a.base.out
	o.Brightness = mk(tot.sumMean/vid, tot.vidUS, bc.FloorBrightness, UnitLuma, true, &a.base.brightOK, &a.base.bright)
	o.Nonuniformity = mk(tot.sumStd/vid, tot.vidUS, bc.FloorNonuniformity, UnitLuma, true, &a.base.nonunifOK, &a.base.nonunif)
	haveAudio := a.cfg.Signals.Audio && tot.audKnown > 0
	o.AudioEnergy = mk(tot.audEnergy/aud, tot.audKnown, meanSquare(bc.FloorAudioDBFS), UnitMeanSquare, haveAudio, &a.base.audioOK, &a.base.audio)
	o.SilenceFraction = mk(float64(tot.audSilent)/aud, tot.audKnown, bc.FloorSilence, UnitRatio, haveAudio, &a.base.silenceOK, &a.base.silence)
	o.SceneRate = mk(float64(tot.scenes)/(vid/usPerSecond), tot.vidUS, bc.FloorSceneRate, UnitEventsPerSecond, a.cfg.Signals.Scene, &a.base.sceneOK, &a.base.scene)
}
