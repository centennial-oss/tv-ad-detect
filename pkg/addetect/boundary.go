// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"fmt"
	"math"
	"sort"
)

// candKind orders candidate kinds; lower values win when a fused group
// chooses its timestamp (BOUND-03). Ties use the earlier candidate.
type candKind uint8

const (
	ckCoverage   candKind = iota // coverage edge: exact
	ckResolution                 // coded size change: exact
	ckAspect                     // confirmed format transition: exact
	ckDark                       // dark/uniform span
	ckBumper
	ckLogo
	ckCaption
	ckScene
	ckSilence
)

func (k candKind) exact() bool  { return k <= ckAspect }
func (k candKind) visual() bool { return k >= ckResolution && k <= ckScene && k != ckCaption }

type candidate struct {
	t              int64
	kind           candKind
	evStart, evEnd int64
	unc            int64
	hasSilence     bool
	av             bool  // silence corroborated by a visual event
	overlap        int64 // best visual/silence overlap seen (ckDark)
	moveTo         int64 // shared-interval midpoint, applied after alignment
	moved          bool
	causes         []BoundaryCause
}

// boundary is a fused candidate boundary.
type boundary struct {
	t      int64
	unc    int64
	causes []BoundaryCause
	sep    int64 // longest dark/uniform span among the fused candidates
	dark   bool  // supplied by a dark/uniform event
	av     bool  // audio/visual corroborated
	edge   bool  // recording start or end
}

func spanGap(s0, e0, s1, e1 int64) int64 {
	switch {
	case s1 > e0:
		return s1 - e0
	case s0 > e1:
		return s0 - e1
	}
	return 0
}

// buildBoundaries constructs, aligns and fuses candidate boundaries
// (BOUND-01 to BOUND-03).
func (a *analysis) buildBoundaries(captionsUsable bool) {
	cfg := a.cfg
	bc := &cfg.Boundaries
	var cands []candidate

	// Visual candidates. A continuous dark/uniform span collapses to one
	// candidate at its midpoint, quantized to a frame boundary (BOUND-02).
	if bc.FromDarkUniform {
		for i := 0; i < len(a.dark); {
			j := i
			for j+1 < len(a.dark) && a.dark[j+1].StartUS <= a.dark[j].EndUS {
				j++
			}
			s, e := a.dark[i].StartUS, a.dark[j].EndUS
			kind := a.dark[i].Kind
			if j > i {
				kind = KindDarkUniformSpan
			}
			t := a.nearestFrameTime(s + (e-s)/2)
			cands = append(cands, candidate{
				t: t, kind: ckDark, evStart: s, evEnd: e, unc: (e - s) / 2,
				causes: []BoundaryCause{{Signal: SignalDarkUniform, Kind: kind, Role: RoleBoundary, EventStartUS: s, EventEndUS: e, CandidateUS: t}},
			})
			i = j + 1
		}
	}
	point := func(t int64, kind candKind, unc int64, signal, name string) {
		cands = append(cands, candidate{
			t: t, kind: kind, evStart: t, evEnd: t, unc: unc,
			causes: []BoundaryCause{{Signal: signal, Kind: name, Role: RoleBoundary, EventStartUS: t, EventEndUS: t, CandidateUS: t}},
		})
	}
	if bc.FromResolution {
		for _, r := range a.resolution {
			point(r.TimeUS, ckResolution, 0, SignalResolution, KindCodedSizeChange)
		}
	}
	if bc.FromAspect {
		// The picture bounds are unknown on dark frames, so a ratio change
		// first observed just after a dark/uniform span happened somewhere
		// inside it: it joins that span's candidate instead of adding a
		// sliver boundary one sample later.
		slack := us(cfg.Aspect.SampleInterval) + a.frameUS
		nDark, di := len(cands), 0
		var prev float64
		for _, s := range a.aspect {
			if prev > 0 && math.Abs(s.ratio-prev) > cfg.Aspect.RatioTolerance*prev {
				for di < nDark && cands[di].evEnd < s.start-slack {
					di++
				}
				if di < nDark && cands[di].kind == ckDark && cands[di].evEnd <= s.start {
					c := &cands[di]
					c.causes = append(c.causes, BoundaryCause{Signal: SignalAspect, Kind: KindRatioChange, Role: RoleBoundary, EventStartUS: s.start, EventEndUS: s.start, CandidateUS: c.t})
				} else {
					point(s.start, ckAspect, us(cfg.Aspect.SampleInterval), SignalAspect, KindRatioChange)
				}
			}
			prev = s.ratio
		}
	}
	if bc.FromBumpers {
		for _, b := range a.bumpers {
			if cfg.Bumpers.Boundary != BumperBoundaryEnd {
				cands = append(cands, candidate{t: a.nearestFrameTime(b.StartUS), kind: ckBumper, evStart: b.StartUS, evEnd: b.StartUS, unc: b.UncertaintyUS,
					causes: []BoundaryCause{{Signal: SignalBumpers, Kind: KindMatchStart, Role: RoleBoundary, EventStartUS: b.StartUS, EventEndUS: b.EndUS, CandidateUS: b.StartUS, Detail: b.TemplateID}}})
			}
			if cfg.Bumpers.Boundary != BumperBoundaryStart {
				cands = append(cands, candidate{t: a.nearestFrameTime(b.EndUS), kind: ckBumper, evStart: b.EndUS, evEnd: b.EndUS, unc: b.UncertaintyUS,
					causes: []BoundaryCause{{Signal: SignalBumpers, Kind: KindMatchEnd, Role: RoleBoundary, EventStartUS: b.StartUS, EventEndUS: b.EndUS, CandidateUS: b.EndUS, Detail: b.TemplateID}}})
			}
		}
	}
	if bc.FromLogo && a.logoUsable {
		for i := 1; i < len(a.logo); i++ {
			p, s := a.logo[i-1], a.logo[i]
			if p.end != s.start || p.state == logoUnknown || s.state == logoUnknown {
				continue
			}
			name := KindLogoAppeared
			if s.state == logoAbsent {
				name = KindLogoDisappeared
			}
			point(a.nearestFrameTime(s.start), ckLogo, s.uncertainty, SignalLogo, name)
		}
	}
	if bc.FromCaptions && captionsUsable {
		for i := 1; i < len(a.caps); i++ {
			p, s := a.caps[i-1], a.caps[i]
			if p.end == s.start && p.style != s.style {
				point(a.nearestFrameTime(s.start), ckCaption, a.frameUS, SignalCaptions, KindStyleChange)
			}
		}
	}
	if bc.FromScene && cfg.Signals.Scene {
		for _, s := range a.scene {
			point(s.TimeUS, ckScene, 0, SignalScene, s.Kind)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].t < cands[j].t })

	// Silence: corroborate the nearest visual candidate inside the alignment
	// window without moving it, except that a silence overlapping a
	// dark/uniform span moves the candidate to the midpoint of the shared
	// interval. Unmatched silences become their own candidates (AUDIO-02).
	align := us(cfg.Audio.Alignment)
	var maxHalf int64
	for i := range cands {
		maxHalf = max(maxHalf, cands[i].evEnd-cands[i].evStart)
	}
	nVisual := len(cands)
	sceneIdx := 0
	for _, s := range a.silence {
		lo := s.StartUS - align - maxHalf
		hi := s.EndUS + align + maxHalf
		best, bestGap := -1, int64(math.MaxInt64)
		for k := sort.Search(nVisual, func(k int) bool { return cands[k].t >= lo }); k < nVisual && cands[k].t <= hi; k++ {
			c := &cands[k]
			if !c.kind.visual() {
				continue
			}
			if g := spanGap(s.StartUS, s.EndUS, c.evStart, c.evEnd); g <= align && g < bestGap {
				best, bestGap = k, g
			}
		}
		if best >= 0 {
			c := &cands[best]
			c.hasSilence, c.av = true, true
			c.causes = append(c.causes, BoundaryCause{Signal: SignalAudio, Kind: KindSilence, Role: RoleCorroboration, EventStartUS: s.StartUS, EventEndUS: s.EndUS, CandidateUS: c.t})
			if c.kind == ckDark {
				is, ie := max(s.StartUS, c.evStart), min(s.EndUS, c.evEnd)
				if ie-is > c.overlap {
					c.overlap = ie - is
					c.moveTo, c.moved = a.nearestFrameTime(is+(ie-is)/2), true
				}
			}
			continue
		}
		if !bc.FromSilence {
			continue
		}
		mid := s.StartUS + (s.EndUS-s.StartUS)/2
		c := candidate{
			t: mid, kind: ckSilence, evStart: s.StartUS, evEnd: s.EndUS, unc: (s.EndUS - s.StartUS) / 2, hasSilence: true,
			causes: []BoundaryCause{{Signal: SignalAudio, Kind: KindSilence, Role: RoleBoundary, EventStartUS: s.StartUS, EventEndUS: s.EndUS, CandidateUS: mid}},
		}
		if bc.SilenceSceneAlign && cfg.Signals.Scene {
			// Nearest scene event inside the alignment window of the silence.
			for sceneIdx < len(a.scene) && a.scene[sceneIdx].TimeUS < s.StartUS-align {
				sceneIdx++
			}
			bestT, bestD, found := int64(0), int64(math.MaxInt64), false
			var ev SceneEvent
			for k := sceneIdx; k < len(a.scene) && a.scene[k].TimeUS <= s.EndUS+align; k++ {
				d := a.scene[k].TimeUS - mid
				if d < 0 {
					d = -d
				}
				if d < bestD {
					bestT, bestD, found, ev = a.scene[k].TimeUS, d, true, a.scene[k]
				}
			}
			if found {
				c.t, c.av, c.unc = bestT, true, 0
				c.causes[0].CandidateUS = bestT
				c.causes = append(c.causes, BoundaryCause{Signal: SignalScene, Kind: ev.Kind, Role: RoleCorroboration, EventStartUS: bestT, EventEndUS: bestT, CandidateUS: bestT})
			}
		}
		cands = append(cands, c)
	}

	for i := range cands[:nVisual] {
		if c := &cands[i]; c.moved {
			c.t = c.moveTo
			for k := range c.causes {
				c.causes[k].CandidateUS = c.t
			}
		}
	}

	// Coverage edges keep missing-data intervals identifiable (BLOCK-01).
	for _, g := range a.gaps {
		for _, t := range [2]int64{g.StartUS, g.EndUS} {
			name := KindCoverageGapStart
			if t == g.EndUS {
				name = KindCoverageGapEnd
			}
			cands = append(cands, candidate{t: t, kind: ckCoverage, evStart: g.StartUS, evEnd: g.EndUS,
				causes: []BoundaryCause{{Signal: SignalTimeline, Kind: name, Role: RoleBoundary, EventStartUS: g.StartUS, EventEndUS: g.EndUS, CandidateUS: t, Detail: g.Reason}}})
		}
	}

	// Deterministic order: time, then kind priority, then event start.
	sort.SliceStable(cands, func(i, j int) bool {
		x, y := &cands[i], &cands[j]
		if x.t != y.t {
			return x.t < y.t
		}
		if x.kind != y.kind {
			return x.kind < y.kind
		}
		return x.evStart < y.evStart
	})

	// Fusion (BOUND-03): a group is anchored at its first candidate and only
	// accepts candidates within the tolerance of that anchor, so chains of
	// nearby candidates cannot merge an arbitrarily long interval. The group
	// time is that of its highest-priority member. Two exact candidates with
	// different timestamps never share a group, so resolution changes,
	// format transitions and coverage edges keep their timestamps.
	tol := us(bc.FusionTolerance)
	start := boundary{t: 0, edge: true, causes: []BoundaryCause{{Signal: SignalTimeline, Kind: KindRecordingStart, Role: RoleBoundary}}}
	end := boundary{t: a.dur, edge: true, causes: []BoundaryCause{{Signal: SignalTimeline, Kind: KindRecordingEnd, Role: RoleBoundary, EventStartUS: a.dur, EventEndUS: a.dur, CandidateUS: a.dur}}}
	absorb := func(b *boundary, c *candidate) {
		b.causes = append(b.causes, c.causes...)
		b.unc = max(b.unc, c.unc)
		b.dark = b.dark || c.kind == ckDark
		if c.kind == ckDark {
			b.sep = max(b.sep, c.evEnd-c.evStart)
		}
	}
	bounds := make([]boundary, 1, len(cands)+2)
	bounds[0] = start
	var (
		open      bool
		anchor    int64
		best      candKind
		exactT    int64
		hasExact  bool
		hasVisual bool
		hasSil    bool
	)
	closeGroup := func() {
		if open {
			b := &bounds[len(bounds)-1]
			b.av = b.av || (hasVisual && hasSil)
			open = false
		}
	}
	for i := range cands {
		c := &cands[i]
		switch {
		case c.kind.exact() && c.t > 0 && c.t < a.dur:
			// Exact kinds keep their own timestamp next to a recording edge
			// too; they are never absorbed by it.
		case c.t <= tol && !open:
			absorb(&bounds[0], c)
			continue
		case c.t >= a.dur-tol:
			absorb(&end, c)
			continue
		}
		if !open || c.t-anchor > tol || (c.kind.exact() && hasExact && c.t != exactT) {
			closeGroup()
			bounds = append(bounds, boundary{t: c.t})
			open, anchor, best = true, c.t, c.kind
			hasExact, hasVisual, hasSil = false, false, false
		}
		b := &bounds[len(bounds)-1]
		if c.kind < best {
			best, b.t = c.kind, c.t
		}
		if c.kind.exact() && !hasExact {
			hasExact, exactT = true, c.t
		}
		hasVisual = hasVisual || c.kind.visual()
		hasSil = hasSil || c.hasSilence
		b.av = b.av || c.av
		absorb(b, c)
	}
	closeGroup()
	a.bounds = append(bounds, end)
	if bc.DarkRequiresQuiet && cfg.Signals.Audio {
		a.vetoLoudDarkBoundaries()
	}
	if bc.ValidateSources {
		a.validateSources()
	}
}

// boundarySource classifies a boundary for the C3 check: silence-only
// (every cause is a silence or a corroborating scene event), uniform-only
// (dark/uniform evidence from uniform spans alone), or neither.
func boundarySource(b *boundary) string {
	silence, uniform, other := false, false, false
	for _, c := range b.causes {
		switch {
		case c.Signal == SignalAudio && c.Kind == KindSilence:
			silence = true
		case c.Signal == SignalScene && c.Role == RoleCorroboration:
		case c.Signal == SignalDarkUniform && c.Kind == KindUniform:
			uniform = true
		case c.Role == RoleVeto:
		default:
			other = true
		}
	}
	switch {
	case other || b.edge:
		return ""
	case silence && !uniform:
		return SourceSilenceOnly
	case uniform && !silence && b.dark:
		return SourceUniform
	}
	return ""
}

// validateSources applies C3: a source whose consecutive boundaries are
// rarely a standard ad length apart is not cutting at ads in this
// recording, so its boundaries stop counting as evidence.
func (a *analysis) validateSources() {
	bc := &a.cfg.Boundaries
	sc := &a.cfg.Scoring
	loose := us(sc.LooseTolerance)
	standard := func(d int64) bool {
		for _, s := range sc.StandardDurations {
			if absInt(d-us(s)) <= loose {
				return true
			}
		}
		for _, s := range sc.LooseDurations {
			if absInt(d-us(s)) <= loose {
				return true
			}
		}
		return false
	}
	for _, src := range [...]string{SourceSilenceOnly, SourceUniform} {
		var idx []int
		for k := range a.bounds {
			if boundarySource(&a.bounds[k]) == src {
				idx = append(idx, k)
			}
		}
		if len(idx) < bc.ValidateMinEvents {
			continue
		}
		hits := 0
		for n := 1; n < len(idx); n++ {
			if standard(a.bounds[idx[n]].t - a.bounds[idx[n-1]].t) {
				hits++
			}
		}
		share := float64(hits) / float64(len(idx)-1)
		if share >= bc.ValidateMinShare {
			continue
		}
		for _, k := range idx {
			b := &a.bounds[k]
			b.av, b.dark = false, false
			b.causes = append(b.causes, BoundaryCause{Signal: SignalTimeline, Kind: WarnSourceInvalidated, Role: RoleVeto, Detail: src, CandidateUS: b.t})
		}
		a.notes = append(a.notes, Warning{Code: WarnSourceInvalidated, Message: fmt.Sprintf(
			"%s boundaries are a standard ad length apart in only %.0f%% of %d spacings (minimum %.0f%%); they no longer count as boundary evidence",
			src, 100*share, len(idx)-1, 100*bc.ValidateMinShare)})
	}
}

// vetoLoudDarkBoundaries applies C1: a dark/uniform boundary whose
// surrounding audio is known and never gets quiet is a picture event inside
// continuing content (a fade, a night scene), not a separator. It stays a
// boundary but loses its dark/uniform evidence. Unknown audio is neutral.
func (a *analysis) vetoLoudDarkBoundaries() {
	bc := &a.cfg.Boundaries
	half := us(bc.DarkQuietWindow) / 2
	quiet := math.Pow(10, bc.DarkQuietLevel/10) // mean-square
	for k := range a.bounds {
		b := &a.bounds[k]
		if !b.dark || b.edge {
			continue
		}
		lo, hi := b.t-b.sep/2-half, b.t+b.sep/2+half
		i := sort.Search(len(a.audio), func(i int) bool {
			return a.audio[i].StartUS+int64(a.audio[i].DurationUS) > lo
		})
		known, loud := false, true
		for ; i < len(a.audio) && a.audio[i].StartUS < hi; i++ {
			known = true
			if float64(a.audio[i].PeakMS) <= quiet {
				loud = false
				break
			}
		}
		if known && loud {
			b.dark = false
			b.causes = append(b.causes, BoundaryCause{Signal: SignalAudio, Kind: KindLoudDarkBoundary, Role: RoleVeto, EventStartUS: lo, EventEndUS: hi, CandidateUS: b.t})
		}
	}
}
