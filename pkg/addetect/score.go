// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"math"
	"math/bits"
	"sort"
)

type ruleID uint8

const (
	ruleStrict ruleID = iota
	ruleLoose
	ruleCombined
	ruleLogoPresent
	ruleLogoAbsent
	ruleCaption
	ruleAspect
	ruleRelBrightness
	ruleRelNonuniformity
	ruleRelAudio
	ruleRelSilence
	ruleRelScene
	ruleCaptionMatch
	ruleCaptionAbsent
	ruleCaptionUnexpected
	ruleLongBlock
	ruleSilentBlock
	ruleAdPhrase
	ruleProgramPhrase
	numRules
)

var ruleNames = [numRules]string{
	RuleStrictStandardDuration, RuleLooseStandardDuration, RuleCombinedStandardDuration,
	RuleLogoPresent, RuleLogoAbsent, RuleCaptionStyleMismatch, RuleAspectRatioMismatch,
	RuleRelativeBrightness, RuleRelativeNonuniformity, RuleRelativeAudioEnergy,
	RuleRelativeSilenceFraction, RuleRelativeSceneRate, RuleCaptionStyleMatch,
	RuleCaptionAbsent, RuleCaptionUnexpected, RuleLongBlock, RuleSilentBlock,
	RuleCaptionAdPhrase, RuleCaptionProgramPhrase,
}

var ruleFamilies = [numRules]string{
	FamilyDuration, FamilyDuration, FamilyDuration, SignalLogo, SignalLogo, SignalCaptions, SignalAspect,
	RuleRelativeBrightness, RuleRelativeNonuniformity, RuleRelativeAudioEnergy,
	RuleRelativeSilenceFraction, RuleRelativeSceneRate, SignalCaptions,
	SignalCaptions, SignalCaptions, FamilyDuration, SignalAudio,
	SignalCaptions, SignalCaptions,
}

// Evidence families for the SCORE-06 gate.
const (
	famDarkBoundary uint16 = 1 << iota
	famAVBoundary
	famLogoAbsent
	famCaption
	famAspect
	famRelBrightness
	famRelNonuniformity
	famRelAudio
	famRelSilence
	famRelScene
	famAdPhrase
)

var famNames = [...]string{
	EvidenceDarkUniformBoundary, EvidenceAVCorroboratedBoundary, RuleLogoAbsent,
	RuleCaptionStyleMismatch, RuleAspectRatioMismatch, RuleRelativeBrightness,
	RuleRelativeNonuniformity, RuleRelativeAudioEnergy, RuleRelativeSilenceFraction,
	RuleRelativeSceneRate, RuleCaptionAdPhrase,
}

// Reasons a hypothesis is not an accepted ad unit.
const (
	rejBelowThreshold uint16 = 1 << iota
	rejNoEvidence
	rejDurationLimits
	rejIncompleteCoverage
	rejContainsProgram
	rejNotSelected
	rejBreakConstraint
	rejSpansDarkBoundary
	rejTooFewSignals
	rejCombinedTooFewSignals
	rejCombinedProgramEdge
)

var rejNames = [...]string{
	RejectBelowThreshold, RejectNoEvidenceFamily, RejectUnitDurationLimits,
	RejectIncompleteCoverage, RejectContainsProgram, RejectNotSelected, RejectBreakConstraint, RejectSpansDarkBoundary,
	RejectTooFewSignals, RejectCombinedTooFewSignals, RejectCombinedProgramEdge,
}

func bitNames(mask uint16, names []string) []string {
	out := make([]string, 0, 4)
	for i, n := range names {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, n)
		}
	}
	return out
}

type factorRec struct {
	rule                  ruleID
	mult                  float64
	value, base, coverage float64
	target, measured, tol int64
}

// hypEval is one evaluated ad-unit hypothesis over blocks i..j inclusive.
type hypEval struct {
	i, j     int32
	id       int32
	score    float64
	nf       uint8
	fam      uint16
	reject   uint16
	eligible bool
	selected bool
	factors  [maxFactors]factorRec
}

func (h *hypEval) apply(f factorRec) {
	h.factors[h.nf] = f
	h.nf++
	h.score *= f.mult
}

// evaluate scores blocks i..j as one hypothesis in constant time from the
// prefix sums (SCORE-01, SCORE-02). Each evidence family applies at most
// once; neutral (1.0) modifiers disable their rule.
func (a *analysis) evaluate(i, j int, h *hypEval) {
	h.i, h.j, h.score, h.nf, h.fam, h.reject, h.eligible, h.selected = int32(i), int32(j), 1, 0, 0, 0, false, false
	sc := &a.cfg.Scoring
	p0, p1 := &a.pre[i], &a.pre[j+1]
	dur := a.blocks[j].end - a.blocks[i].start
	fdur := float64(dur)

	// Duration (SCORE-03): nearest standard length by elapsed presentation
	// time; the strongest applicable duration modifier is used.
	// Loose-only durations never earn the strict factor.
	var target, dev int64 = 0, math.MaxInt64
	looseOnly := false
	for _, d := range sc.StandardDurations {
		if x := absInt(dur - us(d)); x < dev {
			target, dev = us(d), x
		}
	}
	for _, d := range sc.LooseDurations {
		if x := absInt(dur - us(d)); x < dev {
			target, dev, looseOnly = us(d), x, true
		}
	}
	strict, loose := us(sc.StrictTolerance), us(sc.LooseTolerance)
	if ext := us(sc.UncertaintyExtension); ext > 0 {
		bs, be := &a.bounds[i], &a.bounds[j+1]
		if sc.TolerancePerSeparator > 0 {
			// A share of the dark/uniform separators at the unit's edges:
			// a long fade leaves more room for the content to start.
			ext = min(ext, int64(math.Round(sc.TolerancePerSeparator*float64(bs.sep+be.sep))))
		} else {
			ext = min(ext, bs.unc+be.unc)
		}
		strict, loose = strict+ext, loose+ext
	}
	// A duration is only credited when both boundaries are located more
	// precisely than scoring.duration_max_boundary_uncertainty: the midpoint
	// of a long dark or silent span says little about where content changed.
	maxUnc := us(sc.DurationMaxUncertainty)
	located := a.bounds[i].unc <= maxUnc && a.bounds[j+1].unc <= maxUnc
	var rule ruleID
	var mult float64 = 1
	tol := loose
	switch {
	case !located:
	case i == j && dev <= strict && !looseOnly:
		rule, mult, tol = ruleStrict, sc.StrictFactor, strict
	case i == j && dev <= loose:
		rule, mult = ruleLoose, sc.LooseFactor
	case i != j && dev <= loose:
		rule, mult = ruleCombined, sc.CombinedFactor
	}
	if mult != 1 {
		h.apply(factorRec{rule: rule, mult: mult, target: target, measured: dur, tol: tol})
	}

	// Logo occupancy = present time / known time (LOGO-03, LOGO-05).
	logoEvidence := false
	if known := p1.logoKnown - p0.logoKnown; a.logoUsable && known > 0 && float64(known) >= sc.MinCoverageLogo*fdur {
		occ := float64(p1.logoPresent-p0.logoPresent) / float64(known)
		cov := float64(known) / fdur
		switch {
		case occ >= sc.LogoPresentOccupancy && sc.LogoPresentFactor != 1:
			h.apply(factorRec{rule: ruleLogoPresent, mult: sc.LogoPresentFactor, value: occ, coverage: cov})
			logoEvidence = true
		case occ <= sc.LogoAbsentOccupancy && sc.LogoAbsentFactor != 1:
			h.apply(factorRec{rule: ruleLogoAbsent, mult: sc.LogoAbsentFactor, value: occ, coverage: cov})
			h.fam |= famLogoAbsent
			logoEvidence = true
		}
	}

	// Caption style vs the program baseline (CAP-02). An uncaptioned
	// program has its own rule: any recognized style is unexpected there.
	capRules := a.base.capOK && (sc.CaptionFactor != 1 || sc.CaptionMatchFactor != 1 || sc.CaptionAbsentFactor != 1)
	if capRules || (a.base.capNone && sc.CaptionUnexpectedFactor != 1) {
		var known int64
		top := CaptionNone
		for s := CaptionNone; s < numCaptionStyles; s++ {
			d := p1.cap[s] - p0.cap[s]
			known += d
			if d > p1.cap[top]-p0.cap[top] {
				top = s
			}
		}
		if known > 0 && float64(known) >= sc.MinCoverageCaptions*fdur {
			cov := float64(known) / fdur
			switch {
			case a.base.capNone:
				if top.recognized() {
					h.apply(factorRec{rule: ruleCaptionUnexpected, mult: sc.CaptionUnexpectedFactor, value: float64(top), base: float64(CaptionNone), coverage: cov})
					h.fam |= famCaption
				}
			case top == CaptionNone:
				// Observed absence: neutral unless caption_absent_factor
				// is set.
				if sc.CaptionAbsentFactor != 1 {
					h.apply(factorRec{rule: ruleCaptionAbsent, mult: sc.CaptionAbsentFactor, value: float64(top), base: float64(a.base.capStyle), coverage: cov})
					h.fam |= famCaption
				}
			case top != a.base.capStyle:
				if sc.CaptionFactor != 1 {
					h.apply(factorRec{rule: ruleCaption, mult: sc.CaptionFactor, value: float64(top), base: float64(a.base.capStyle), coverage: cov})
					h.fam |= famCaption
				}
			case sc.CaptionMatchFactor != 1 && a.captionMatchApplies(logoEvidence):
				// Captions in the program's own style are evidence of program.
				h.apply(factorRec{rule: ruleCaptionMatch, mult: sc.CaptionMatchFactor, value: float64(top), base: float64(a.base.capStyle), coverage: cov})
			}
		}
	}

	// Active-picture ratio vs the program baseline (ASPECT-03): a mismatch
	// when most of the known time disagrees with the baseline.
	if known := p1.aspKnown - p0.aspKnown; a.base.aspOK && sc.AspectFactor != 1 && known > 0 && float64(known) >= sc.MinCoverageAspect*fdur {
		match := p1.aspMatch - p0.aspMatch
		if known-match > match {
			h.apply(factorRec{rule: ruleAspect, mult: sc.AspectFactor, value: float64(known-match) / float64(known), base: a.base.aspect, coverage: float64(known) / fdur})
			h.fam |= famAspect
		}
	}

	// Optional relative-statistics rules (SCORE-05).
	if sc.RelativeFactor != 1 {
		minSup := us(sc.RelMinSupport)
		vid := p1.vidUS - p0.vidUS
		vidOK := vid > 0 && vid >= minSup && float64(vid) >= sc.MinCoverageVideo*fdur
		rel := func(rule ruleID, fam uint16, value, base float64, cov float64) {
			// A zero baseline (possible only with a zero floor) suppresses
			// the rule: nothing exceeds it by a ratio (BASE-01).
			if base > 0 && value >= base*sc.RelExcessRatio {
				h.apply(factorRec{rule: rule, mult: sc.RelativeFactor, value: value, base: base, coverage: cov})
				h.fam |= fam
			}
		}
		if sc.RelBrightness && a.base.brightOK && vidOK {
			rel(ruleRelBrightness, famRelBrightness, (p1.sumMean-p0.sumMean)/float64(vid), a.base.bright, float64(vid)/fdur)
		}
		if sc.RelNonuniformity && a.base.nonunifOK && vidOK {
			rel(ruleRelNonuniformity, famRelNonuniformity, (p1.sumStd-p0.sumStd)/float64(vid), a.base.nonunif, float64(vid)/fdur)
		}
		if sc.RelSceneRate && a.base.sceneOK && vidOK && int(p1.scenes-p0.scenes) >= sc.SceneMinEvents {
			rel(ruleRelScene, famRelScene, float64(p1.scenes-p0.scenes)/(float64(vid)/usPerSecond), a.base.scene, float64(vid)/fdur)
		}
		aud := p1.audKnown - p0.audKnown
		audOK := aud > 0 && aud >= minSup && float64(aud) >= sc.MinCoverageAudio*fdur
		if sc.RelAudioEnergy && a.base.audioOK && audOK {
			// Linear energy, never a ratio of negative dBFS values (BASE-01).
			rel(ruleRelAudio, famRelAudio, (p1.audEnergy-p0.audEnergy)/float64(aud), a.base.audio, float64(aud)/fdur)
		}
		if sc.RelSilence && a.base.silenceOK && audOK {
			rel(ruleRelSilence, famRelSilence, float64(p1.audSilent-p0.audSilent)/float64(aud), a.base.silence, float64(aud)/fdur)
		}
	}

	// Opt-in caption phrases: once per hypothesis and list.
	if sc.AdPhraseFactor != 1 && countIn(a.adPhraseT, a.blocks[i].start, a.blocks[j].end) > 0 {
		h.apply(factorRec{rule: ruleAdPhrase, mult: sc.AdPhraseFactor, value: float64(countIn(a.adPhraseT, a.blocks[i].start, a.blocks[j].end))})
		h.fam |= famAdPhrase
	}
	if sc.ProgramPhraseFactor != 1 && countIn(a.progPhraseT, a.blocks[i].start, a.blocks[j].end) > 0 {
		h.apply(factorRec{rule: ruleProgramPhrase, mult: sc.ProgramPhraseFactor, value: float64(countIn(a.progPhraseT, a.blocks[i].start, a.blocks[j].end))})
	}

	// Long near-silent stretches (optional; no evidence family of its own).
	if sc.SilentBlockFactor != 1 && dur >= us(sc.SilentBlockMinDuration) {
		if aud := p1.audKnown - p0.audKnown; aud > 0 && float64(aud) >= sc.MinCoverageAudio*fdur {
			if frac := float64(p1.audSilent-p0.audSilent) / float64(aud); frac >= sc.SilentBlockMinFraction {
				h.apply(factorRec{rule: ruleSilentBlock, mult: sc.SilentBlockFactor, value: frac, base: sc.SilentBlockMinFraction, coverage: float64(aud) / fdur})
			}
		}
	}

	// Boundary evidence (SCORE-06): a dark/uniform or audio/visual
	// corroborated boundary at the unit's edges; recording edges are exempt
	// under the "both" policy but never count as evidence themselves.
	bs, be := &a.bounds[i], &a.bounds[j+1]
	qs, qe := bs.dark || bs.av, be.dark || be.av
	ok := qs || qe
	if sc.BoundaryEdges == EdgesBoth {
		ok = ok && (qs || bs.edge) && (qe || be.edge)
	}
	if ok {
		if (qs && bs.dark) || (qe && be.dark) {
			h.fam |= famDarkBoundary
		}
		if (qs && bs.av) || (qe && be.av) {
			h.fam |= famAVBoundary
		}
	}
}

// captionMatchApplies applies scoring.caption_match_condition (T3).
func (a *analysis) captionMatchApplies(logoEvidence bool) bool {
	notPopOn := a.base.capStyle != CaptionPopOn
	switch a.cfg.Scoring.CaptionMatchCondition {
	case CaptionMatchNoLogo:
		return !logoEvidence
	case CaptionMatchNotPopOn:
		return notPopOn
	case CaptionMatchNoLogoNotPopOn:
		return !logoEvidence && notPopOn
	}
	return true
}

func absInt(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// hypHeap retains, for diagnostics, the cap hypotheses with the smallest
// ids, whatever order they are offered in. It is a max-heap on id, so the
// largest retained id is evicted first.
type hypHeap struct {
	cap     int
	h       []hypEval
	dropped bool
}

func (k *hypHeap) offer(h *hypEval) {
	if k.cap <= 0 {
		k.dropped = true
		return
	}
	if len(k.h) < k.cap {
		k.h = append(k.h, *h)
		for c := len(k.h) - 1; c > 0; {
			p := (c - 1) / 2
			if k.h[p].id >= k.h[c].id {
				break
			}
			k.h[p], k.h[c] = k.h[c], k.h[p]
			c = p
		}
		return
	}
	k.dropped = true
	if h.id >= k.h[0].id {
		return
	}
	k.h[0] = *h
	for p := 0; ; {
		c := 2*p + 1
		if c >= len(k.h) {
			break
		}
		if c+1 < len(k.h) && k.h[c+1].id > k.h[c].id {
			c++
		}
		if k.h[p].id >= k.h[c].id {
			break
		}
		k.h[p], k.h[c] = k.h[c], k.h[p]
		p = c
	}
}

// sorted returns the retained hypotheses in id order, reusing dst, and
// whether any offered hypothesis was not retained.
func (k *hypHeap) sorted(dst []hypEval) ([]hypEval, bool) {
	sort.Slice(k.h, func(x, y int) bool { return k.h[x].id < k.h[y].id })
	return append(dst, k.h...), k.dropped
}

// effectiveThreshold is scoring.threshold, raised to the duration-weighted
// scoring.threshold_percentile quantile of the block scores when that is
// set: the score that the given share of the recording's block time does
// not exceed.
func (a *analysis) effectiveThreshold() float64 {
	sc := &a.cfg.Scoring
	if sc.ThresholdPercentile <= 0 || len(a.blocks) == 0 {
		return sc.Threshold
	}
	type sw struct {
		score float64
		w     int64
	}
	items := make([]sw, len(a.blocks))
	var total int64
	for i := range a.blocks {
		b := &a.blocks[i]
		items[i] = sw{b.score, b.end - b.start}
		total += items[i].w
	}
	sort.SliceStable(items, func(x, y int) bool { return items[x].score < items[y].score })
	need := sc.ThresholdPercentile * float64(total)
	var acc int64
	q := items[len(items)-1].score
	for _, it := range items {
		acc += it.w
		if float64(acc) >= need {
			q = it.score
			break
		}
	}
	return math.Max(sc.Threshold, q)
}

type unit struct {
	h       hypEval
	run     int32
	bridged bool // synthetic unit for a bridged gap
}

// scoreAndSelect scores every block and consecutive group of blocks once,
// in a fixed order, and selects the nonoverlapping eligible units that
// maximize sum(duration x log(score)) (SCORE-04).
func (a *analysis) scoreAndSelect() {
	cfg := a.cfg
	sc := &cfg.Scoring
	n := len(a.blocks)
	minUnit, maxUnit := us(cfg.Breaks.MinUnit), cfg.unitMaxUS()
	var h hypEval

	a.adPhraseT = phraseTimes(a.phraseHits, PhraseListAdvertisement)
	a.progPhraseT = phraseTimes(a.phraseHits, PhraseListProgram)

	// Pass 1: initial block scores and positive program evidence (SCORE-07).
	// A block longer than any ad unit can carry the optional long-block
	// program factor.
	for i := range a.blocks {
		b := &a.blocks[i]
		a.evaluate(i, i, &h)
		if dur := b.end - b.start; dur > maxUnit && sc.LongBlockFactor != 1 {
			h.apply(factorRec{rule: ruleLongBlock, mult: sc.LongBlockFactor, measured: dur, tol: maxUnit})
		}
		b.score, b.nf, b.factors = h.score, h.nf, h.factors
	}
	a.threshold = a.effectiveThreshold()
	for i := range a.blocks {
		b := &a.blocks[i]
		dur := b.end - b.start
		b.progOnly = b.nf > 0
		for k := 0; k < int(b.nf); k++ {
			switch b.factors[k].rule {
			case ruleCaptionMatch, ruleProgramPhrase, ruleLogoPresent:
			default:
				b.progOnly = false
			}
			switch b.factors[k].rule {
			case ruleLogoPresent:
				b.program |= programLogo
			case ruleLongBlock:
				b.program |= programLong
			case ruleLogoAbsent, ruleCaption, ruleCaptionAbsent, ruleCaptionUnexpected, ruleAspect, ruleRelBrightness, ruleRelNonuniformity, ruleRelAudio, ruleRelSilence, ruleRelScene, ruleSilentBlock, ruleAdPhrase:
				b.conflict = true
			}
		}
		if dur > maxUnit && a.stableProgram(i, i) {
			b.program |= programStable
		}
		covered := b.gapUS == 0 && float64(b.vidUS) >= sc.MinCoverageVideo*float64(dur)
		if b.program != 0 && (b.conflict || !covered || b.score >= a.threshold) {
			// Evidence exists but is conflicting or insufficient: unknown.
			b.conflict, b.program = true, 0
		} else {
			b.conflict = false
		}
		a.pre[i+1].program = a.pre[i].program
		if b.program != 0 {
			a.pre[i+1].program++
		}
	}

	// darkBefore[k] counts dark/uniform boundaries among boundaries 0..k-1.
	darkBefore := make([]int32, len(a.bounds)+1)
	for k := range a.bounds {
		darkBefore[k+1] = darkBefore[k]
		if a.bounds[k].dark {
			darkBefore[k+1]++
		}
	}

	// Pass 2: every hypothesis exactly once. Hypotheses are identified by
	// start block then length, but evaluated from the last start block to
	// the first, so that the weighted interval scheduling below runs in the
	// same pass and no list of eligible units is kept (memory stays linear
	// in the number of blocks, however many hypotheses there are).
	//
	// last[i] is the last end block considered from start block i; ids of
	// start block i begin at baseID[i].
	last := make([]int32, n)
	hi := 0
	for i := 0; i < n; i++ {
		hi = max(hi, i)
		for hi+1 < n && hi+1-i < sc.MaxUnitBlocks && a.blocks[hi+1].end-a.blocks[i].start <= maxUnit {
			hi++
		}
		// The first block alone is always evaluated, even when over-long.
		last[i] = int32(hi)
	}
	baseID := make([]int32, n+1)
	for i := 0; i < n; i++ {
		baseID[i+1] = baseID[i] + last[i] - int32(i) + 1
	}
	a.evaluated = int(baseID[n])

	level := cfg.Outputs.Diagnostics
	retain := level == DiagnosticsFull || level == DiagnosticsDetailed
	var kept hypHeap
	kept.cap = cfg.Outputs.MaxHypotheses

	// Weighted interval scheduling, right to left. Ties: fewer units, then
	// the earlier boundary (take a unit at i over skipping; among units from
	// i the one ending first).
	val := make([]float64, n+1)
	cnt := make([]int32, n+1)
	choice := make([]int32, n+1) // chosen end block, -1 = no unit starts here
	chosenID := make([]int32, n+1)
	for i := n - 1; i >= 0; i-- {
		if a.halted(n - 1 - i) {
			return
		}
		val[i], cnt[i], choice[i] = val[i+1], cnt[i+1], -1
		for j := i; j <= int(last[i]); j++ {
			dur := a.blocks[j].end - a.blocks[i].start
			a.evaluate(i, j, &h)
			h.id = baseID[i] + int32(j-i)
			atEdge := a.bounds[i].edge || a.bounds[j+1].edge
			relaxed := (cfg.Breaks.AllowTruncatedEdges && atEdge) || a.inMargins(a.blocks[i].start, a.blocks[j].end)
			if dur > maxUnit || (dur < minUnit && !relaxed) {
				h.reject |= rejDurationLimits
			}
			if a.pre[j+1].gapUS != a.pre[i].gapUS {
				h.reject |= rejIncompleteCoverage
			}
			if j > i && a.pre[j+1].program != a.pre[i].program {
				h.reject |= rejContainsProgram
			}
			// Blocks are combined across a dark/uniform boundary only when
			// their total is a standard length; otherwise that boundary
			// separates content and each side stands on its own.
			if j > i && darkBefore[j+1] != darkBefore[i+1] && !(h.nf > 0 && h.factors[0].rule <= ruleCombined) {
				h.reject |= rejSpansDarkBoundary
			}
			if h.score < a.threshold {
				h.reject |= rejBelowThreshold
			}
			if n := sc.UnmatchedMinFamilies; n > 1 && !(h.nf > 0 && h.factors[0].rule <= ruleCombined) &&
				bits.OnesCount16(h.fam&^(famDarkBoundary|famAVBoundary)) < n {
				h.reject |= rejTooFewSignals
			}
			// A length reached only by adding blocks up is weak: the sum can
			// land on a standard length by chance and take in program.
			if n := sc.CombinedMinFamilies; n > 0 && h.nf > 0 && h.factors[0].rule == ruleCombined &&
				bits.OnesCount16(h.fam&^(famDarkBoundary|famAVBoundary)) < n {
				h.reject |= rejCombinedTooFewSignals
			}
			// Short program-only blocks at either end that bring the sum to a
			// standard length are the neighboring program, not ad.
			if sc.CombinedProgramEdges && h.nf > 0 && h.factors[0].rule == ruleCombined &&
				(a.blocks[i].progOnly || a.blocks[j].progOnly) {
				h.reject |= rejCombinedProgramEdge
			}
			if h.fam == 0 {
				h.reject |= rejNoEvidence
			}
			if h.reject == 0 {
				h.eligible = true
				w := float64(dur) * math.Log(h.score)
				v, c := w+val[j+1], cnt[j+1]+1
				// The same total reached through a different grouping
				// differs only by floating-point rounding; such sums are
				// ties.
				tie := math.Abs(v-val[i]) <= selectionTieEpsilon*math.Max(v, val[i])
				if (v > val[i] && !tie) || (tie && (c < cnt[i] || (c == cnt[i] && choice[i] < 0))) {
					val[i], cnt[i], choice[i], chosenID[i] = v, c, int32(j), h.id
				}
			}
			if retain && (level == DiagnosticsFull || h.score >= a.threshold) {
				kept.offer(&h)
			}
		}
	}
	a.hyps, a.truncated = kept.sorted(a.hyps[:0])
	for i := 0; i < n; {
		j := choice[i]
		if j < 0 {
			i++
			continue
		}
		var u unit
		a.evaluate(i, int(j), &u.h)
		u.h.id, u.h.eligible, u.h.selected, u.run = chosenID[i], true, true, -1
		for b := i; b <= int(j); b++ {
			a.blocks[b].unit = int32(len(a.units))
		}
		a.units = append(a.units, u)
		i = int(j) + 1
	}
	// Mark retained hypotheses.
	for k := range a.hyps {
		hp := &a.hyps[k]
		ui := sort.Search(len(a.units), func(x int) bool { return a.units[x].h.i >= hp.i })
		if ui < len(a.units) && a.units[ui].h.i == hp.i && a.units[ui].h.j == hp.j {
			hp.selected = true
		} else if hp.eligible {
			hp.reject |= rejNotSelected
		}
	}
}

// stableProgram reports whether blocks i..j carry a known caption style and
// active ratio that both match reliable program baselines (SCORE-07).
func (a *analysis) stableProgram(i, j int) bool {
	if !a.base.capOK || !a.base.aspOK {
		return false
	}
	sc := &a.cfg.Scoring
	p0, p1 := &a.pre[i], &a.pre[j+1]
	fdur := float64(a.blocks[j].end - a.blocks[i].start)
	var known int64
	top := CaptionNone
	for s := CaptionNone; s < numCaptionStyles; s++ {
		d := p1.cap[s] - p0.cap[s]
		known += d
		if d > p1.cap[top]-p0.cap[top] {
			top = s
		}
	}
	if float64(known) < sc.MinCoverageCaptions*fdur || top != a.base.capStyle {
		return false
	}
	ak := p1.aspKnown - p0.aspKnown
	am := p1.aspMatch - p0.aspMatch
	return float64(ak) >= sc.MinCoverageAspect*fdur && am >= ak-am
}
