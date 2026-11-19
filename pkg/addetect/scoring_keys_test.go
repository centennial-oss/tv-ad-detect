// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"math"
	"strings"
	"testing"
	"time"
)

// Phase 3: every new scoring key is neutral at its default, and each one
// changes exactly the rule it names when set.

// scoring.loose_standard_durations: a listed length earns the loose factor,
// never the strict one, even when measured exactly.
func TestLooseOnlyStandardDurations(t *testing.T) {
	blocks := []simBlock{prog(60 * sec), noLogo(25 * sec), prog(60 * sec)}
	a := simulate(nil, blocks)
	if h := a.hyp(t, 1, 1); h.score != 2 || h.nf != 1 || h.factors[0].rule != ruleLogoAbsent {
		t.Errorf("default: 25 s block score %v factors %v, want logo absence only", h.score, h.factors[:h.nf])
	}
	cfg := simConfig()
	cfg.Scoring.LooseDurations = []time.Duration{5 * sec, 25 * sec}
	a = simulate(cfg, blocks)
	h := a.hyp(t, 1, 1)
	if h.score != 3 || h.factors[0].rule != ruleLoose || h.factors[0].target != 25_000_000 {
		t.Errorf("loose 25 s: score %v first factor %+v, want loose 1.5 x absent 2", h.score, h.factors[0])
	}
	// A standard length nearer than the loose one still wins, strictly.
	a = simulate(cfg, []simBlock{prog(60 * sec), noLogo(30 * sec), prog(60 * sec)})
	if h := a.hyp(t, 1, 1); h.factors[0].rule != ruleStrict {
		t.Errorf("30 s: first factor %+v, want strict", h.factors[0])
	}

	for name, list := range map[string][]time.Duration{
		"duplicate of a standard length": {30 * sec},
		"not increasing":                 {25 * sec, 5 * sec},
		"not positive":                   {0},
	} {
		c := DefaultConfig()
		c.Scoring.LooseDurations = list
		if err := c.Validate(); KindOf(err) != KindInvalidConfig || !strings.Contains(err.Error(), "loose_standard_durations") {
			t.Errorf("%s: err %v", name, err)
		}
	}
}

// scoring.tolerance_per_separator: the extension follows the dark/uniform
// separators at the unit's edges, bounded by
// uncertainty_tolerance_extension.
func TestTolerancePerSeparator(t *testing.T) {
	// 31.6 s is outside the 1 s loose tolerance of 30 s.
	blocks := []simBlock{prog(60 * sec), {us: 31_600_000, logoKnown: 1}, prog(60 * sec)}
	seps := func(sep int64) simOpt {
		return func(a *analysis) {
			for k := range a.bounds {
				if a.bounds[k].dark {
					a.bounds[k].sep = sep
				}
			}
		}
	}
	durationRule := func(a *analysis) ruleID {
		h := a.hyp(t, 1, 1)
		if h.nf > 0 && h.factors[0].rule <= ruleCombined {
			return h.factors[0].rule
		}
		return numRules
	}
	cfg := simConfig()
	cfg.Scoring.UncertaintyExtension = 2 * sec
	cfg.Scoring.TolerancePerSeparator = 0.5
	// Two 0.8 s separators: 0.5 x 1.6 s = 0.8 s of extension, so the
	// loose tolerance reaches 1.8 s.
	if r := durationRule(simulate(cfg, blocks, seps(800_000))); r != ruleLoose {
		t.Errorf("0.8 s separators: duration rule %d, want loose", r)
	}
	// Two 0.4 s separators: 0.4 s of extension is not enough.
	if r := durationRule(simulate(cfg, blocks, seps(400_000))); r != numRules {
		t.Errorf("0.4 s separators: duration rule %d, want none", r)
	}
	// The extension bound still applies.
	cfg.Scoring.UncertaintyExtension = 500 * time.Millisecond
	if r := durationRule(simulate(cfg, blocks, seps(10_000_000))); r != numRules {
		t.Errorf("bounded extension: duration rule %d, want none", r)
	}
}

// scoring.long_block_factor: a block longer than any ad unit becomes
// program with its own reason only when the factor is set.
func TestLongBlockFactor(t *testing.T) {
	blocks := []simBlock{plain(200 * sec), noLogo(30 * sec), plain(200 * sec)}
	if got := simulate(nil, blocks).labels(); got != "UAU" {
		t.Errorf("default labels %s, want UAU", got)
	}
	cfg := simConfig()
	cfg.Scoring.LongBlockFactor = 0.5
	a := simulate(cfg, blocks)
	if got := a.labels(); got != "PAP" {
		t.Errorf("long_block_factor 0.5: labels %s, want PAP", got)
	}
	if a.blocks[0].reasons&rsnLongBlock == 0 || a.blocks[0].score != 0.5 {
		t.Errorf("long block: reasons %v score %v", reasonList(a.blocks[0].reasons), a.blocks[0].score)
	}
	// A long block with ad evidence is conflicting, not program.
	a = simulate(cfg, []simBlock{noLogo(200 * sec), noLogo(30 * sec), plain(200 * sec)})
	if a.labels()[0] == 'P' {
		t.Errorf("a long logo-absent block became program: %s", a.labels())
	}
}

// scoring.caption_unexpected_factor: in a reliably uncaptioned program a
// recognized style is ad evidence. The uncaptioned baseline is only
// established when the rule is enabled.
func TestCaptionUnexpected(t *testing.T) {
	sc := func() *scenario {
		return newScenario(blk{D: 130 * sec, CC: "null"}, blk{D: 30 * sec, CC: "ru"}, blk{D: 63 * sec, CC: "null"})
	}
	r := sc().run(t, nil)
	if b := r.Baselines.CaptionStyle; b.Reliable {
		t.Errorf("default: caption baseline %+v, want none established", b)
	}
	cfg := testConfig()
	cfg.Scoring.CaptionUnexpectedFactor = 2
	r = sc().run(t, cfg)
	if b := r.Baselines.CaptionStyle; !b.Reliable || b.Style != "none" {
		t.Fatalf("caption baseline %+v, want a reliable uncaptioned program", b)
	}
	ad := blockSpan(t, r, 130*sec, 160*sec)
	if f := findFactor(ad.Factors, "caption_unexpected"); f == nil || f.Multiplier != 2 || f.Family != "captions" {
		t.Errorf("ad block factors %s, want caption_unexpected x2", rules(ad.Factors))
	}
	for _, b := range []*Block{blockSpan(t, r, 0, 130*sec), blockSpan(t, r, 160*sec, 223*sec)} {
		if findFactor(b.Factors, "caption_unexpected") != nil || findFactor(b.Factors, "caption_style_match") != nil {
			t.Errorf("program block %s has caption factors %s", b.ID, rules(b.Factors))
		}
	}
}

// scoring.threshold_percentile: the threshold rises to the duration-weighted
// quantile of block scores, never below scoring.threshold, and the result
// reports both.
func TestThresholdPercentile(t *testing.T) {
	// 60 s of score-4 blocks (logo absent, strict 30 s) in 300 s, the rest
	// program (0.2 and below).
	blocks := []simBlock{prog(120 * sec), noLogo(30 * sec), noLogo(30 * sec), prog(120 * sec)}
	a := simulate(nil, blocks)
	if a.threshold != 1.5 || a.labels() != "PAAP" {
		t.Fatalf("default: threshold %v labels %s", a.threshold, a.labels())
	}
	cfg := simConfig()
	cfg.Scoring.ThresholdPercentile = 0.5
	if a = simulate(cfg, blocks); a.threshold != 1.5 {
		t.Errorf("median below the threshold: effective %v, want 1.5", a.threshold)
	}
	cfg.Scoring.ThresholdPercentile = 0.9
	a = simulate(cfg, blocks)
	if a.threshold != 4 || a.labels() != "PAAP" {
		t.Errorf("90th percentile: effective %v labels %s, want 4 (inclusive) and PAAP", a.threshold, a.labels())
	}
	r := newScenario(blk{D: 130 * sec}).run(t, nil)
	if c := r.Calibration.Score; c.Threshold != 1.5 || c.Percentile != 0 || c.Effective != 1.5 {
		t.Errorf("score calibration %+v", c)
	}
}

// C1 / BOUND: a black separator with silence is evidence; black frames with
// the audio continuing loud keep their boundary but lose the evidence
// (loud_dark_boundary) unless boundaries.dark_requires_quiet is off.
func TestDarkBoundaryNeedsQuietAudio(t *testing.T) {
	sc := func(sep sepKind) *scenario {
		return newScenario(blk{D: 130 * sec}, blk{D: 30 * sec, Sep: sep}, blk{D: 63 * sec, Sep: sep})
	}
	quietR := sc(sepBlackSilence).run(t, nil)
	if b := mustBoundary(t, quietR, 130*sec); !hasString(b.Evidence, EvidenceDarkUniformBoundary) {
		t.Errorf("black + silence: evidence %v", b.Evidence)
	}
	loud := sc(sepBlack).run(t, nil)
	b := mustBoundary(t, loud, 130*sec)
	if hasString(b.Evidence, EvidenceDarkUniformBoundary) {
		t.Errorf("black with loud audio: still dark evidence %v", b.Evidence)
	}
	vetoed := false
	for _, c := range b.Causes {
		vetoed = vetoed || (c.Kind == KindLoudDarkBoundary && c.Role == RoleVeto)
	}
	if !vetoed {
		t.Errorf("no loud_dark_boundary veto among causes %s", mustJSON(t, b.Causes))
	}
	cfg := testConfig()
	cfg.Boundaries.DarkRequiresQuiet = false
	if b := mustBoundary(t, sc(sepBlack).run(t, cfg), 130*sec); !hasString(b.Evidence, EvidenceDarkUniformBoundary) {
		t.Errorf("C1 off: evidence %v", b.Evidence)
	}
	// Without audio the veto never applies (unknown audio is neutral).
	noAudio := sc(sepBlack)
	noAudio.noAudio = true
	if b := mustBoundary(t, noAudio.run(t, nil), 130*sec); !hasString(b.Evidence, EvidenceDarkUniformBoundary) {
		t.Errorf("no audio: evidence %v", b.Evidence)
	}
}

// C2: auto silence mode estimates the recording's floor (bounded, with a
// fallback reason), and silence_context drops silences without louder audio
// on both sides.
func TestAdaptiveSilenceLevel(t *testing.T) {
	cfg := simConfig()
	cfg.Audio.SilenceMode = SilenceModeAuto
	// 13 s of windows at -60 dBFS in 130 s, the rest speech.
	a := simulate(cfg, []simBlock{{us: 130_000_000, silentFrac: 0.1}})
	c := a.silenceCal
	if !c.Estimated || c.FloorDBFS == nil || math.Abs(*c.FloorDBFS-(-60)) > 0.5 || math.Abs(c.LevelDBFS-(-54)) > 0.5 {
		t.Errorf("auto calibration %+v floor %v", c, c.FloorDBFS)
	}
	cfg.Audio.AutoMaxDBFS = -58
	if a = simulate(cfg, []simBlock{{us: 130_000_000, silentFrac: 0.1}}); a.silenceCal.LevelDBFS != -58 {
		t.Errorf("bounded level %v, want -58", a.silenceCal.LevelDBFS)
	}
	if a = simulate(cfg, []simBlock{{us: 30_000_000, silentFrac: 0.1}}); a.silenceCal.Estimated || a.silenceCal.FallbackReason != SilenceFallbackInsufficient || a.silenceCal.LevelDBFS != -50 {
		t.Errorf("30 s of audio: %+v, want the fixed level with a reason", a.silenceCal)
	}
	if a = simulate(nil, []simBlock{{us: 130_000_000}}); a.silenceCal.Mode != SilenceModeFixed || a.silenceCal.Estimated {
		t.Errorf("default: %+v", a.silenceCal)
	}

	// Context: a silence between speech is kept; a silence followed by
	// murmur (-45 dBFS, not 10 dB above -50) is dropped when required.
	win := func(from, to int64, db float64) []AudioWindow {
		var out []AudioWindow
		for t := from; t < to; t += 20_000 {
			ms := float32(meanSquare(db))
			out = append(out, AudioWindow{StartUS: t, DurationUS: 20_000, PeakMS: ms, MeanMS: ms})
		}
		return out
	}
	mk := func(after float64, ctx time.Duration) *analysis {
		c := DefaultConfig()
		c.Audio.SilenceContext = ctx
		x := &analysis{cfg: c}
		x.dur = 10_000_000
		x.audio = append(append(win(0, 4_000_000, -20), win(4_000_000, 4_500_000, -70)...), win(4_500_000, 10_000_000, after)...)
		x.audioKnown = 10_000_000
		x.detectSilence()
		return x
	}
	if n := len(mk(-20, sec).silence); n != 1 {
		t.Errorf("speech on both sides: %d silences", n)
	}
	if n := len(mk(-45, sec).silence); n != 0 {
		t.Errorf("murmur after: %d silences, want the silence dropped", n)
	}
	if n := len(mk(-45, 0).silence); n != 1 {
		t.Errorf("context off: %d silences", n)
	}
}

// C3: a source whose boundaries are rarely a standard length apart loses
// its evidence role, with a warning; a regular one keeps it.
func TestValidateSources(t *testing.T) {
	mk := func(spacings []int64) *analysis {
		c := DefaultConfig()
		c.Boundaries.ValidateSources = true
		a := &analysis{cfg: c}
		var t int64 = 100_000_000
		a.bounds = append(a.bounds, boundary{t: 0, edge: true})
		for _, s := range spacings {
			t += s
			a.bounds = append(a.bounds, boundary{t: t, av: true, causes: []BoundaryCause{
				{Signal: SignalAudio, Kind: KindSilence, Role: RoleBoundary},
				{Signal: SignalScene, Kind: "cut", Role: RoleCorroboration},
			}})
		}
		a.validateSources()
		return a
	}
	regular := mk([]int64{30e6, 30e6, 15e6, 60e6, 30e6, 30e6, 15e6, 15e6, 30e6})
	if !regular.bounds[3].av || len(regular.notes) != 0 {
		t.Errorf("regular source invalidated: notes %v", regular.notes)
	}
	irregular := mk([]int64{7e6, 23e6, 41e6, 13e6, 52e6, 9e6, 37e6, 26e6, 44e6})
	if irregular.bounds[3].av || len(irregular.notes) != 1 || irregular.notes[0].Code != WarnSourceInvalidated {
		t.Errorf("irregular source kept: av %v notes %v", irregular.bounds[3].av, irregular.notes)
	}
	if few := mk([]int64{7e6, 23e6, 41e6}); !few.bounds[2].av {
		t.Errorf("a source with too few boundaries was judged")
	}
}

// scoring.silent_block_factor: a long, mostly silent hypothesis gets the
// factor; short or partly silent ones do not. Off by default.
func TestSilentBlockFactor(t *testing.T) {
	blocks := []simBlock{prog(60 * sec), {us: 30_000_000, logoKnown: 1, logo: 0.5, silentFrac: 0.95}, prog(60 * sec)}
	if h := simulate(nil, blocks).hyp(t, 1, 1); h.nf != 1 || h.factors[0].rule != ruleStrict {
		t.Errorf("default: factors %v", h.factors[:h.nf])
	}
	cfg := simConfig()
	cfg.Scoring.SilentBlockFactor = 2
	h := simulate(cfg, blocks).hyp(t, 1, 1)
	if h.score != 4 || h.factors[h.nf-1].rule != ruleSilentBlock {
		t.Errorf("silent 30 s block: score %v factors %v", h.score, h.factors[:h.nf])
	}
	blocks[1].silentFrac = 0.5
	if h := simulate(cfg, blocks).hyp(t, 1, 1); h.score != 2 {
		t.Errorf("half silent: score %v", h.score)
	}
	blocks[1] = simBlock{us: 8_000_000, logoKnown: 1, logo: 0.5, silentFrac: 1}
	if h := simulate(cfg, blocks).hyp(t, 1, 1); h.score != 1 {
		t.Errorf("8 s silent block under the 10 s minimum: score %v", h.score)
	}
}

// outputs.keep_first / keep_last: the skip list never covers the kept head
// or tail; canonical breaks are unchanged.
func TestSkipKeepFirstAndLast(t *testing.T) {
	sc := newScenario(blk{D: 30 * sec}, blk{D: 130 * sec}, blk{D: 30 * sec})
	cfg := testConfig()
	cfg.Breaks.AllowTruncatedEdges = true
	base := sc.run(t, cfg)
	if len(base.AdBreaks) != 2 || base.SkipIntervals[0].StartUS != 0 {
		t.Fatalf("breaks %s skips %s", mustJSON(t, base.AdBreaks), mustJSON(t, base.SkipIntervals))
	}
	cfg.Outputs.KeepFirst, cfg.Outputs.KeepLast = 10*sec, 40*sec
	r := sc.run(t, cfg)
	if mustJSON(t, r.AdBreaks) != mustJSON(t, base.AdBreaks) {
		t.Errorf("keep options changed the canonical breaks")
	}
	if len(r.SkipIntervals) != 1 || r.SkipIntervals[0].StartUS != 10_000_000 || r.SkipIntervals[0].EndUS != 30_000_000 || !r.SkipIntervals[0].Clamped {
		t.Errorf("skips %s, want [10 s, 30 s) only (the tail break lies in the kept 40 s)", mustJSON(t, r.SkipIntervals))
	}
}
