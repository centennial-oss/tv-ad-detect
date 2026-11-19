// SPDX-License-Identifier: Apache-2.0

package addetect

import "sort"

// Reason codes (OUT-02). The order here is the order reasons are listed.
const (
	rsnStandardDuration uint32 = 1 << iota
	rsnLogoAbsent
	rsnCaptionMismatch
	rsnAspectMismatch
	rsnRelBrightness
	rsnRelNonuniformity
	rsnRelAudio
	rsnRelSilence
	rsnRelScene
	rsnDarkBoundary
	rsnAVBoundary
	rsnBridgedGap
	rsnLogoPresent
	rsnStableInterval
	rsnMissingCoverage
	rsnBreakTooShort
	rsnBreakTooLong
	rsnConflicting
	rsnRejectedAd
	rsnInsufficient
	rsnCaptionAbsent
	rsnCaptionUnexpected
	rsnLongBlock
	rsnSliver
	rsnDarkTail
	rsnAdjacentProgram
	rsnSilentBlock
	rsnAdPhrase
	rsnProgramPhrase
)

var reasonNames = [...]string{
	ReasonStandardDuration, RuleLogoAbsent, RuleCaptionStyleMismatch, RuleAspectRatioMismatch,
	RuleRelativeBrightness, RuleRelativeNonuniformity, RuleRelativeAudioEnergy,
	RuleRelativeSilenceFraction, RuleRelativeSceneRate, EvidenceDarkUniformBoundary,
	EvidenceAVCorroboratedBoundary, ReasonBridgedGap, ReasonLogoPresent, ReasonStableLongInterval,
	ReasonMissingCoverage, ReasonBreakTooShort, ReasonBreakTooLong, ReasonConflictingEvidence,
	ReasonRejectedAdHypothesis, ReasonInsufficientEvidence,
	RuleCaptionAbsent, RuleCaptionUnexpected, ReasonLongBlock,
	ReasonContextSliver, ReasonDarkTail, ReasonAdjacentProgram, RuleSilentBlock,
	RuleCaptionAdPhrase, RuleCaptionProgramPhrase,
}

func reasonList(mask uint32) []string {
	out := make([]string, 0, 4)
	for i, n := range reasonNames {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, n)
		}
	}
	return out
}

var ruleReasons = [numRules]uint32{
	ruleStrict: rsnStandardDuration, ruleLoose: rsnStandardDuration, ruleCombined: rsnStandardDuration,
	ruleLogoPresent: 0, ruleLogoAbsent: rsnLogoAbsent, ruleCaption: rsnCaptionMismatch, ruleAspect: rsnAspectMismatch,
	ruleRelBrightness: rsnRelBrightness, ruleRelNonuniformity: rsnRelNonuniformity, ruleRelAudio: rsnRelAudio,
	ruleRelSilence: rsnRelSilence, ruleRelScene: rsnRelScene,
	ruleCaptionAbsent: rsnCaptionAbsent, ruleCaptionUnexpected: rsnCaptionUnexpected,
	ruleSilentBlock: rsnSilentBlock, ruleAdPhrase: rsnAdPhrase, ruleProgramPhrase: rsnProgramPhrase,
}

func (h *hypEval) reasons() uint32 {
	var r uint32
	for k := 0; k < int(h.nf); k++ {
		r |= ruleReasons[h.factors[k].rule]
	}
	if h.fam&famDarkBoundary != 0 {
		r |= rsnDarkBoundary
	}
	if h.fam&famAVBoundary != 0 {
		r |= rsnAVBoundary
	}
	return r
}

// adRun is a maximal run of adjacent selected units, possibly joined across
// one bridged gap (BREAK-02, BREAK-03).
type adRun struct {
	firstUnit, lastUnit int // inclusive unit indices
	firstBlock          int
	lastBlock           int
	start, end          int64
	accepted            bool
	reason              uint32
	truncStart          bool
	truncEnd            bool
	bridged             []TimeRange
	bridgeBlocks        [][2]int
}

// assembleBreaks groups selected units into breaks and applies the break
// duration rules, the optional gap bridge and the truncated-edge policy
// (section 8), then labels every block (RESULT-01).
func (a *analysis) assembleBreaks() {
	bc := &a.cfg.Breaks
	minBreak, maxBreak := us(bc.MinBreak), us(bc.MaxBreak)
	minUnit, maxUnit := us(bc.MinUnit), a.cfg.unitMaxUS()
	if bc.DarkTailMax > 0 {
		a.dropDarkTailUnits(us(bc.DarkTailMax))
	}

	var runs []adRun
	for k := 0; k < len(a.units); {
		m := k
		for m+1 < len(a.units) && a.units[m+1].h.i == a.units[m].h.j+1 {
			m++
		}
		fb, lb := int(a.units[k].h.i), int(a.units[m].h.j)
		runs = append(runs, adRun{firstUnit: k, lastUnit: m, firstBlock: fb, lastBlock: lb, start: a.blocks[fb].start, end: a.blocks[lb].end})
		k = m + 1
	}

	// Gap bridging (BREAK-03): consecutive runs merge across short gaps,
	// each gap at most max_bridge_gap, as long as the merged break stays
	// within max_break (which bounds the chain). A gap never holds missing
	// coverage, and holds positive program evidence only when that evidence
	// is the logo alone and bridge_logo_program allows it.
	if bc.BridgeGaps && len(runs) > 1 {
		maxGap := us(bc.MaxBridgeGap)
		out := runs[:0:0]
		for k := 0; k < len(runs); k++ {
			r := runs[k]
			for k+1 < len(runs) {
				nx := runs[k+1]
				gs, ge := r.lastBlock+1, nx.firstBlock-1
				gapDur := nx.start - r.end
				ok := gapDur > 0 && gapDur <= maxGap && gapDur <= maxUnit && nx.end-r.start <= maxBreak
				for b := gs; ok && b <= ge; b++ {
					blk := &a.blocks[b]
					if blk.gapUS != 0 || (blk.program != 0 && !(bc.BridgeLogoProgram && blk.program == programLogo)) {
						ok = false
					}
				}
				if !ok {
					break
				}
				r.bridged = append(r.bridged, TimeRange{StartUS: r.end, EndUS: nx.start, Reason: ReasonBridgedGap})
				r.bridgeBlocks = append(r.bridgeBlocks, [2]int{gs, ge})
				r.lastUnit, r.lastBlock, r.end = nx.lastUnit, nx.lastBlock, nx.end
				k++
			}
			out = append(out, r)
		}
		runs = out
	}

	for ri := range runs {
		r := &runs[ri]
		dur := r.end - r.start
		atStart, atEnd := r.start == 0, r.end == a.dur
		firstShort := a.blocks[int(a.units[r.firstUnit].h.j)].end-r.start < minUnit
		lastShort := r.end-a.blocks[int(a.units[r.lastUnit].h.i)].start < minUnit
		relaxed := (bc.AllowTruncatedEdges && (atStart || atEnd)) || a.inMargins(r.start, r.end)
		switch {
		case dur > maxBreak:
			r.reason = rsnBreakTooLong
		case dur < minBreak && !relaxed:
			r.reason = rsnBreakTooShort
		default:
			r.accepted = true
			if bc.AllowTruncatedEdges {
				// The relaxation was needed: report the truncation (BREAK-04).
				r.truncStart = atStart && (dur < minBreak || firstShort)
				r.truncEnd = atEnd && (dur < minBreak || lastShort)
			}
		}
		for u := r.firstUnit; u <= r.lastUnit; u++ {
			a.units[u].run = int32(ri)
		}
		for b := r.firstBlock; b <= r.lastBlock; b++ {
			blk := &a.blocks[b]
			blk.run = int32(ri)
			if r.accepted {
				blk.label = lblAd
				if blk.unit >= 0 {
					blk.reasons = a.units[blk.unit].h.reasons()
				} else {
					blk.reasons = rsnBridgedGap
				}
			} else {
				// A run violating the break limits is unknown, never program
				// and never sliced at invented timestamps (BREAK-02).
				blk.label, blk.reasons = lblUnknown, r.reason
			}
		}
	}
	a.runs = runs

	// Stable intervals longer than the maximum ad-unit duration whose
	// caption style and active ratio match the program baselines are
	// positive program evidence (SCORE-07). Evaluated on maximal runs of
	// consecutive covered blocks outside every selected unit.
	n := len(a.blocks)
	for i := 0; i < n; {
		if a.blocks[i].run >= 0 || a.blocks[i].gapUS != 0 {
			i++
			continue
		}
		j := i
		for j+1 < n && a.blocks[j+1].run < 0 && a.blocks[j+1].gapUS == 0 {
			j++
		}
		if a.blocks[j].end-a.blocks[i].start > maxUnit && a.stableProgram(i, j) {
			for b := i; b <= j; b++ {
				blk := &a.blocks[b]
				// An ad-raising factor only blocks the program label when
				// the block's net evidence still leans toward an ad.
				if !blk.conflict && blk.score < a.threshold && (!hasAdRaisingFactor(blk) || blk.score < 1) {
					blk.program |= programStable
				}
			}
		}
		i = j + 1
	}

	for i := range a.blocks {
		b := &a.blocks[i]
		if b.run >= 0 {
			continue
		}
		switch {
		case b.program != 0:
			b.label = lblProgram
			if b.program&programLogo != 0 {
				b.reasons |= rsnLogoPresent
			}
			if b.program&programStable != 0 {
				b.reasons |= rsnStableInterval
			}
			if b.program&programLong != 0 {
				b.reasons |= rsnLongBlock
			}
		case b.gapUS != 0:
			b.reasons = rsnMissingCoverage
		case b.conflict:
			b.reasons = rsnConflicting
		case b.score >= a.threshold:
			b.reasons = rsnRejectedAd
		default:
			b.reasons = rsnInsufficient
		}
		if b.darkTail {
			b.reasons |= rsnDarkTail
		}
	}
	if bc.SliverMax > 0 {
		a.labelSlivers(us(bc.SliverMax), maxUnit)
	}
	if bc.MarkAdjacentProgram {
		a.markAdjacentProgram()
	}
}

// inMargins reports whether an interval starts inside the declared lead-in
// or ends inside the declared lead-out (C7).
func (a *analysis) inMargins(start, end int64) bool {
	bc := &a.cfg.Breaks
	return (bc.MarginStart > 0 && start < us(bc.MarginStart)) || (bc.MarginEnd > 0 && end > a.dur-us(bc.MarginEnd))
}

// dropDarkTailUnits applies the fade-tail rule (C4c): a selected unit that
// directly follows program and ends in at least minDark of dark is a fade
// to black, not an ad.
func (a *analysis) dropDarkTailUnits(minDark int64) {
	keep := a.units[:0]
	for _, u := range a.units {
		fb, lb := int(u.h.i), int(u.h.j)
		end := a.blocks[lb].end
		drop := false
		if fb > 0 && a.blocks[fb-1].program != 0 {
			k := sort.Search(len(a.dark), func(k int) bool { return a.dark[k].EndUS >= end-a.frameUS })
			for ; k < len(a.dark) && a.dark[k].StartUS < end; k++ {
				if d := &a.dark[k]; d.Kind == KindDark && d.StartUS <= end-minDark {
					drop = true
					break
				}
			}
		}
		if drop {
			for b := fb; b <= lb; b++ {
				a.blocks[b].unit, a.blocks[b].darkTail = -1, true
			}
			continue
		}
		for b := fb; b <= lb; b++ {
			a.blocks[b].unit = int32(len(keep))
		}
		keep = append(keep, u)
	}
	a.units = keep
}

// labelSlivers applies the sliver rule (C4b): a short unknown block between
// an accepted break and a stretch of program longer than any ad unit is
// program.
func (a *analysis) labelSlivers(maxDur, longProgram int64) {
	n := len(a.blocks)
	progRun := func(from, step int) int64 {
		var d int64
		for k := from; k >= 0 && k < n && a.blocks[k].label == lblProgram; k += step {
			d += a.blocks[k].end - a.blocks[k].start
		}
		return d
	}
	for i := range a.blocks {
		b := &a.blocks[i]
		if b.label != lblUnknown || b.gapUS != 0 || b.end-b.start > maxDur {
			continue
		}
		leftAd := i > 0 && a.blocks[i-1].label == lblAd
		rightAd := i+1 < n && a.blocks[i+1].label == lblAd
		if (leftAd && progRun(i+1, 1) > longProgram) || (rightAd && progRun(i-1, -1) > longProgram) {
			b.label, b.reasons = lblProgram, rsnSliver
		}
	}
}

// markAdjacentProgram applies C7: unknown time before the first break of
// the lead-in, or after the last break of the lead-out, belongs to the
// neighbouring show. The label stays unknown; the reason says why.
func (a *analysis) markAdjacentProgram() {
	bc := &a.cfg.Breaks
	first, last := -1, -1
	for ri := range a.runs {
		if a.runs[ri].accepted {
			if first < 0 {
				first = ri
			}
			last = ri
		}
	}
	if first < 0 {
		return
	}
	if r := &a.runs[first]; bc.MarginStart > 0 && r.start < us(bc.MarginStart) {
		for b := 0; b < r.firstBlock; b++ {
			if a.blocks[b].label == lblUnknown {
				a.blocks[b].reasons |= rsnAdjacentProgram
			}
		}
	}
	if r := &a.runs[last]; bc.MarginEnd > 0 && r.end > a.dur-us(bc.MarginEnd) {
		for b := r.lastBlock + 1; b < len(a.blocks); b++ {
			if a.blocks[b].label == lblUnknown {
				a.blocks[b].reasons |= rsnAdjacentProgram
			}
		}
	}
}

func hasAdRaisingFactor(b *block) bool {
	for k := 0; k < int(b.nf); k++ {
		if r := b.factors[k].rule; r >= ruleLogoAbsent && r != ruleCaptionMatch && r != ruleLongBlock {
			return true
		}
	}
	return false
}

// skipIntervals derives the padded skip list (BREAK-05). Padding never
// changes canonical labels and never extends into program time.
func (a *analysis) skipIntervals(breaks []AdBreak) []SkipInterval {
	padS, padE := us(a.cfg.Outputs.SkipPaddingStart), us(a.cfg.Outputs.SkipPaddingEnd)
	out := make([]SkipInterval, 0, len(breaks))
	// Outward padding stops at the nearest program block. The nearest
	// program block before and after every block is found in two passes,
	// once, and only when a padding can move an edge outward; without it
	// the limits cannot bind.
	var progEndBefore, progStartAfter []int64
	if padS > 0 {
		progEndBefore = make([]int64, len(a.blocks))
		lo := int64(0)
		for b := range a.blocks {
			progEndBefore[b] = lo
			if a.blocks[b].label == lblProgram {
				lo = a.blocks[b].end
			}
		}
	}
	if padE > 0 {
		progStartAfter = make([]int64, len(a.blocks))
		hi := a.dur
		for b := len(a.blocks) - 1; b >= 0; b-- {
			progStartAfter[b] = hi
			if a.blocks[b].label == lblProgram {
				hi = a.blocks[b].start
			}
		}
	}
	ri := 0
	for bi := range breaks {
		br := &breaks[bi]
		for !a.runs[ri].accepted {
			ri++
		}
		r := &a.runs[ri]
		ri++
		s, e := br.StartUS-padS, br.EndUS+padE
		clamped := false
		// Never skip the kept head or tail of the recording.
		if kf := us(a.cfg.Outputs.KeepFirst); kf > 0 && s < kf {
			s, clamped = kf, true
		}
		if kl := us(a.cfg.Outputs.KeepLast); kl > 0 && e > a.dur-kl {
			e, clamped = a.dur-kl, true
		}
		// Outward padding stops at the nearest program block and the timeline.
		lo, hi := int64(0), a.dur
		if progEndBefore != nil {
			lo = progEndBefore[r.firstBlock]
		}
		if progStartAfter != nil {
			hi = progStartAfter[r.lastBlock]
		}
		if s < lo {
			s, clamped = lo, true
		}
		if e > hi {
			e, clamped = hi, true
		}
		if e <= s {
			continue // padding removed the whole interval
		}
		iv := SkipInterval{StartUS: s, EndUS: e, BreakIDs: []string{br.ID}, PaddingStartUS: br.StartUS - s, PaddingEndUS: e - br.EndUS, Clamped: clamped}
		if n := len(out); n > 0 && s <= out[n-1].EndUS {
			p := &out[n-1]
			p.EndUS = max(p.EndUS, e)
			p.BreakIDs = append(p.BreakIDs, br.ID)
			p.PaddingEndUS = iv.PaddingEndUS
			p.Clamped = p.Clamped || clamped
			p.Merged = true
			continue
		}
		out = append(out, iv)
	}
	return out
}
