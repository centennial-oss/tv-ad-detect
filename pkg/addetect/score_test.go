// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"encoding/json"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// AT-12, AT-13, AT-14: scoring and ad-unit hypotheses (SCORE-01..08,
// BASE-01..03, BLOCK-02).

// AT-12 / SCORE-03: standard lengths on, just inside and just outside the
// strict (+-0.5 s) and loose (+-1.0 s) tolerances, to the microsecond. The
// match reports target, measured duration, deviation and tolerance.
func TestDurationTolerances(t *testing.T) {
	cases := []struct {
		us    int64
		rule  string
		score float64
	}{
		{30_000_000, "strict_standard_duration", 2},
		{30_500_000, "strict_standard_duration", 2}, // inclusive
		{30_500_001, "loose_standard_duration", 1.5},
		{31_000_000, "loose_standard_duration", 1.5}, // inclusive
		{31_000_001, "", 1},
		{29_500_000, "strict_standard_duration", 2},
		{29_499_999, "loose_standard_duration", 1.5},
		{29_000_000, "loose_standard_duration", 1.5},
		{28_999_999, "", 1},
	}
	for _, c := range cases {
		a := simulate(nil, []simBlock{plain(47 * sec), {us: c.us}, plain(53 * sec)})
		b := &a.blocks[1]
		if got := a.ruleNames(1); got != c.rule || b.score != c.score {
			t.Errorf("%d us: rules %q score %v, want %q %v", c.us, got, b.score, c.rule, c.score)
			continue
		}
		if c.rule == "" {
			continue
		}
		f := a.factorOut(&b.factors[0])
		tol := int64(500_000)
		if c.rule == "loose_standard_duration" {
			tol = 1_000_000
		}
		if f.Family != "duration" || *f.TargetUS != 30_000_000 || *f.MeasuredUS != c.us || *f.DeviationUS != c.us-30_000_000 || *f.ToleranceUS != tol {
			t.Errorf("%d us: factor = %s", c.us, mustJSON(t, f))
		}
	}

	// Every initial standard duration, and the nearest target is used.
	for _, std := range []int64{10, 15, 20, 30, 45, 60, 90, 120} {
		a := simulate(nil, []simBlock{plain(47 * sec), {us: std * 1_000_000}, plain(53 * sec)})
		if got := a.ruleNames(1); got != "strict_standard_duration" || a.blocks[1].factors[0].target != std*1_000_000 {
			t.Errorf("%d s: rules %q target %d", std, got, a.blocks[1].factors[0].target)
		}
	}
	for _, c := range []struct {
		us, target int64
		rule       string
	}{
		{14_200_000, 15_000_000, "loose_standard_duration"},
		{10_900_000, 10_000_000, "loose_standard_duration"},
		{12_500_000, 0, ""}, // 2.5 s from both 10 and 15
		{75_000_000, 0, ""},
		{121_000_000, 120_000_000, "loose_standard_duration"},
	} {
		a := simulate(nil, []simBlock{plain(47 * sec), {us: c.us}, plain(53 * sec)})
		if got := a.ruleNames(1); got != c.rule || (c.rule != "" && a.blocks[1].factors[0].target != c.target) {
			t.Errorf("%d us: rules %q", c.us, got)
		}
	}
}

// AT-12 / SCORE-03, TEST-01: the same limits end to end, with boundaries
// from black frames on a 10 fps grid.
func TestDurationTolerancesEndToEnd(t *testing.T) {
	durs := []time.Duration{30000 * ms, 30500 * ms, 30600 * ms, 31000 * ms, 31100 * ms, 29500 * ms, 29400 * ms, 29000 * ms, 28900 * ms}
	want := []string{"strict", "strict", "loose", "loose", "", "strict", "loose", "loose", ""}
	s := newScenario(blk{D: 47 * sec})
	for _, dd := range durs {
		s.add(blk{D: dd})
	}
	s.add(blk{D: 53 * sec})
	r := s.run(t, nil)
	if len(r.Diagnostics.Blocks) != len(durs)+2 {
		t.Fatalf("boundaries = %v", boundaryTimes(r))
	}
	for i, dd := range durs {
		b := r.Diagnostics.Blocks[i+1]
		if b.EndUS-b.StartUS != us(dd) {
			t.Fatalf("block %d is %d us, want %d", i+1, b.EndUS-b.StartUS, us(dd))
		}
		wantRule := ""
		if want[i] != "" {
			wantRule = want[i] + "_standard_duration"
		}
		if got := rules(b.Factors); got != wantRule {
			t.Errorf("%v block: factors %q, want %q", dd, got, wantRule)
			continue
		}
		if wantRule != "" {
			f := b.Factors[0]
			if *f.TargetUS != 30_000_000 || *f.MeasuredUS != us(dd) || *f.DeviationUS != us(dd)-30_000_000 {
				t.Errorf("%v block: factor %s", dd, mustJSON(t, f))
			}
		}
	}
}

// SCORE-03: the ordered set of standard durations and the tolerances are
// configurable for different markets.
func TestStandardDurationsConfigurable(t *testing.T) {
	cfg := simConfig()
	cfg.Scoring.StandardDurations = []time.Duration{25 * sec, 40 * sec}
	cfg.Scoring.StrictTolerance = 200 * ms
	cfg.Scoring.LooseTolerance = 2 * sec
	for _, c := range []struct {
		us   int64
		rule string
	}{
		{25_000_000, "strict_standard_duration"},
		{25_200_000, "strict_standard_duration"},
		{25_300_000, "loose_standard_duration"},
		{27_000_000, "loose_standard_duration"},
		{30_000_000, ""},
		{40_100_000, "strict_standard_duration"},
	} {
		a := simulate(cfg, []simBlock{plain(47 * sec), {us: c.us}, plain(53 * sec)})
		if got := a.ruleNames(1); got != c.rule {
			t.Errorf("%d us: rules %q, want %q", c.us, got, c.rule)
		}
	}
}

// SCORE-03: boundary uncertainty widens the tolerances only through the
// explicit bounded setting, which is off by default.
func TestUncertaintyToleranceExtension(t *testing.T) {
	unc := func(a *analysis) { a.bounds[1].unc, a.bounds[2].unc = 300_000, 300_000 }
	blocks := []simBlock{plain(47 * sec), {us: 31_400_000}, plain(53 * sec)}
	a := simulate(nil, blocks, unc)
	if got := a.ruleNames(1); got != "" {
		t.Errorf("default: rules %q (uncertainty must not widen tolerances)", got)
	}
	cfg := simConfig()
	cfg.Scoring.UncertaintyExtension = 400 * ms // bounded: less than the 600 ms of uncertainty
	a = simulate(cfg, blocks, unc)
	if got := a.ruleNames(1); got != "loose_standard_duration" || a.blocks[1].factors[0].tol != 1_400_000 {
		t.Errorf("extension 400 ms: rules %q tolerance %d", got, a.blocks[1].factors[0].tol)
	}
	a = simulate(cfg, []simBlock{plain(47 * sec), {us: 31_500_000}, plain(53 * sec)}, unc)
	if got := a.ruleNames(1); got != "" {
		t.Errorf("beyond the bound: rules %q", got)
	}
	// The extension never exceeds the actual uncertainty.
	cfg.Scoring.UncertaintyExtension = 5 * sec
	a = simulate(cfg, []simBlock{plain(47 * sec), {us: 31_700_000}, plain(53 * sec)}, unc)
	if got := a.ruleNames(1); got != "" {
		t.Errorf("beyond the uncertainty: rules %q", got)
	}
	a = simulate(cfg, []simBlock{plain(47 * sec), {us: 31_600_000}, plain(53 * sec)}, unc)
	if got := a.ruleNames(1); got != "loose_standard_duration" || a.blocks[1].factors[0].tol != 1_600_000 {
		t.Errorf("at the uncertainty: rules %q", got)
	}
}

// AT-12 / BLOCK-02, SCORE-02: short blocks combine into one ad unit with the
// combined-duration modifier; internal cuts stay visible as blocks.
func TestCombinedShortBlocksFormOneUnit(t *testing.T) {
	// The neighbours are longer than the maximum unit so they cannot join a
	// group themselves.
	for _, parts := range [][]time.Duration{{12 * sec, 18 * sec}, {7 * sec, 17 * sec, 6 * sec}, {2 * sec, 26 * sec, 2 * sec}} {
		s := newScenario(blk{D: 130 * sec})
		for _, p := range parts {
			s.add(blk{D: p})
		}
		s.add(blk{D: 140 * sec})
		r := s.run(t, nil)
		wantTimeline(t, r, "U0-130 A130-160 U160-300")
		if len(r.AdUnits) != 1 {
			t.Fatalf("%v: units = %+v", parts, r.AdUnits)
		}
		u := r.AdUnits[0]
		if len(u.BlockIDs) != len(parts) || u.Score != 1.5 || rules(u.Factors) != "combined_standard_duration" {
			t.Errorf("%v: unit = %s", parts, mustJSON(t, u))
		}
		f := u.Factors[0]
		if f.Multiplier != 1.5 || *f.TargetUS != 30_000_000 || *f.MeasuredUS != 30_000_000 || *f.DeviationUS != 0 || *f.ToleranceUS != 1_000_000 {
			t.Errorf("%v: factor = %s", parts, mustJSON(t, f))
		}
		// The constituent blocks keep their own (neutral) scores: a group
		// hypothesis does not modify them (SCORE-04).
		for _, id := range u.BlockIDs {
			for _, b := range r.Diagnostics.Blocks {
				if b.ID == id && (b.Score != 1 || len(b.Factors) != 0 || b.Label != LabelAdvertisement) {
					t.Errorf("%v: block %s score %v factors %s label %s", parts, id, b.Score, rules(b.Factors), b.Label)
				}
			}
		}
		if seg := segmentAt(t, r, 145*sec); len(seg.BlockIDs) != len(parts) || seg.BreakID != "break0" {
			t.Errorf("%v: segment = %+v", parts, seg)
		}
	}

	// A combined duration within the loose tolerance also counts (1.5).
	a := simulate(nil, []simBlock{plain(130 * sec), plain(13 * sec), {us: 18_000_000}, plain(140 * sec)})
	if a.unitSpans() != "1-2" || a.units[0].h.rules() != "combined_standard_duration" || a.units[0].h.score != 1.5 {
		t.Errorf("loose combined: units %s", a.unitSpans())
	}
	a = simulate(nil, []simBlock{plain(130 * sec), plain(13 * sec), {us: 18_000_001}, plain(140 * sec)})
	if len(a.units) != 0 {
		t.Errorf("outside loose: units %s", a.unitSpans())
	}
}

// AT-12 / SCORE-02, SCORE-04: duration bonuses never stack. Strict, loose
// and combined are one evidence family; overlapping hypotheses do not add
// bonuses to a block.
func TestDurationBonusesDoNotStack(t *testing.T) {
	// Two 15 s blocks: each is strict (2.0); together they are a combined
	// 30 s (1.5). Each block must end at 2.0, not 2.0 x 1.5.
	s := newScenario(blk{D: 47 * sec}, blk{D: 15 * sec}, blk{D: 15 * sec}, blk{D: 53 * sec})
	r := s.run(t, nil)
	wantTimeline(t, r, "U0-47 A47-77 U77-130")
	if len(r.AdUnits) != 2 {
		t.Fatalf("units = %s", mustJSON(t, r.AdUnits))
	}
	for _, u := range r.AdUnits {
		if u.Score != 2 || rules(u.Factors) != "strict_standard_duration" {
			t.Errorf("unit = %s", mustJSON(t, u))
		}
	}
	for _, at := range []time.Duration{50 * sec, 65 * sec} {
		if b := blockAt(t, r, at); b.Score != 2 {
			t.Errorf("block %s score %v", b.ID, b.Score)
		}
	}
	// The overlapping combined hypothesis was evaluated once, was eligible,
	// lost the selection and left no trace on the blocks.
	h := hypothesis(t, r, 47*sec, 77*sec)
	if h.Score != 1.5 || rules(h.Factors) != "combined_standard_duration" || !h.Eligible || h.Selected || !hasString(h.Reasons, "not_selected") {
		t.Errorf("combined hypothesis = %s", mustJSON(t, h))
	}
	// SCORE-02: no evidence family applies twice to any hypothesis, and each
	// hypothesis is evaluated exactly once.
	seen := map[[2]int64]bool{}
	for _, h := range r.Diagnostics.Hypotheses {
		key := [2]int64{h.StartUS, h.EndUS}
		if seen[key] {
			t.Errorf("hypothesis [%d,%d) evaluated twice", h.StartUS, h.EndUS)
		}
		seen[key] = true
		fam := map[string]int{}
		prod := 1.0
		for _, f := range h.Factors {
			fam[f.Family]++
			prod *= f.Multiplier
		}
		for name, n := range fam {
			if n > 1 {
				t.Errorf("hypothesis %s applies family %s %d times", h.ID, name, n)
			}
		}
		// SCORE-01: the score is the product of the applied factors.
		if prod != h.Score {
			t.Errorf("hypothesis %s score %v != product of factors %v", h.ID, h.Score, prod)
		}
	}
	if r.Diagnostics.HypothesesEvaluated != len(r.Diagnostics.Hypotheses) {
		t.Errorf("evaluated %d, retained %d", r.Diagnostics.HypothesesEvaluated, len(r.Diagnostics.Hypotheses))
	}

	// A single block within strict tolerance gets the strict modifier only,
	// never strict x loose.
	a := simulate(nil, []simBlock{plain(47 * sec), {us: 30_200_000}, plain(53 * sec)})
	if a.blocks[1].nf != 1 || a.blocks[1].score != 2 {
		t.Errorf("strict block: %d factors, score %v", a.blocks[1].nf, a.blocks[1].score)
	}
}

// AT-13 / SCORE-01, SCORE-02: the controlled logo cases. Strict 30 s x logo
// absent = 4.0 exactly; strict 30 s x logo present = 0.2 exactly; disabled or
// missing logo evidence contributes 1.0.
func TestLogoFactorArithmetic(t *testing.T) {
	// End to end, with a learned and validated logo.
	r := logoScenario().run(t, nil)
	absent := blockSpan(t, r, 50*sec, 80*sec)
	if absent.Score != 4.0 || rules(absent.Factors) != "strict_standard_duration,logo_absent" {
		t.Errorf("logo-absent block: score %v factors %s", absent.Score, rules(absent.Factors))
	}
	if f := findFactor(absent.Factors, "logo_absent"); f.Multiplier != 2 || f.Family != "logo" || *f.Value > 0.2 || *f.Coverage != 1 {
		t.Errorf("logo-absent factor = %s", mustJSON(t, f))
	}
	present := blockSpan(t, r, 80*sec, 110*sec)
	if present.Score != 0.2 || rules(present.Factors) != "strict_standard_duration,logo_present" {
		t.Errorf("logo-present block: score %v factors %s", present.Score, rules(present.Factors))
	}
	if f := findFactor(present.Factors, "logo_present"); f.Multiplier != 0.1 || *f.Value < 0.8 {
		t.Errorf("logo-present factor = %s", mustJSON(t, f))
	}
	if h := hypothesis(t, r, 50*sec, 80*sec); h.Score != 4.0 || !h.Selected {
		t.Errorf("hypothesis = %s", mustJSON(t, h))
	}
	if h := hypothesis(t, r, 80*sec, 110*sec); h.Score != 0.2 || h.Eligible || !hasString(h.Reasons, "below_threshold") {
		t.Errorf("hypothesis = %s", mustJSON(t, h))
	}
	if u := r.AdUnits[0]; u.Score != 4.0 || u.HypothesisID == "" {
		t.Errorf("unit = %s", mustJSON(t, u))
	}

	// Disabled logo evidence: both 30 s blocks score 2.0 (logo factor 1.0).
	cfg := testConfig()
	cfg.Signals.Logo = false
	r = logoScenario().run(t, cfg)
	for _, at := range []time.Duration{60 * sec, 90 * sec} {
		if b := blockAt(t, r, at); b.Score != 2 || rules(b.Factors) != "strict_standard_duration" {
			t.Errorf("logo disabled: block %s score %v factors %s", b.ID, b.Score, rules(b.Factors))
		}
	}

	// White box: the same arithmetic with injected occupancy.
	a := simulate(nil, []simBlock{prog(50 * sec), noLogo(30 * sec), prog(30 * sec), prog(40 * sec)})
	if a.blocks[1].score != 4.0 || a.blocks[2].score != 0.2 || a.blocks[0].score != 0.1 {
		t.Errorf("scores = %v %v %v", a.blocks[0].score, a.blocks[1].score, a.blocks[2].score)
	}
	if a.labels() != "PAPP" {
		t.Errorf("labels = %s", a.labels())
	}
	// Missing logo evidence (no validated logo): 1.0.
	a = simulate(nil, []simBlock{plain(50 * sec), plain(30 * sec), plain(30 * sec), plain(40 * sec)})
	if a.blocks[1].score != 2 || a.blocks[2].score != 2 || a.blocks[0].score != 1 {
		t.Errorf("no logo: scores = %v %v %v", a.blocks[0].score, a.blocks[1].score, a.blocks[2].score)
	}
}

// LOGO-05, SCORE-02, PIPE-02: occupancy limits are inclusive, the range
// between them is neutral, coverage below the requirement is neutral, and
// every weight and limit is configurable.
func TestLogoOccupancyAndCoverageLimits(t *testing.T) {
	run := func(cfg *Config, b simBlock) (string, float64) {
		b.us = 30_000_000
		a := simulate(cfg, []simBlock{prog(50 * sec), b, prog(70 * sec)})
		return a.ruleNames(1), a.blocks[1].score
	}
	for _, c := range []struct {
		occ, known float64
		rules      string
		score      float64
	}{
		{1, 1, "strict_standard_duration,logo_present", 0.2},
		{0.8, 1, "strict_standard_duration,logo_present", 0.2}, // inclusive
		{0.79, 1, "strict_standard_duration", 2},
		{0.5, 1, "strict_standard_duration", 2},
		{0.21, 1, "strict_standard_duration", 2},
		{0.2, 1, "strict_standard_duration,logo_absent", 4}, // inclusive
		{0, 1, "strict_standard_duration,logo_absent", 4},
		// Occupancy is over known time; known time must cover 0.8 of the block.
		{0, 0.8, "strict_standard_duration,logo_absent", 4},
		{0, 0.79, "strict_standard_duration", 2},
		{1, 0.5, "strict_standard_duration", 2},
	} {
		if got, score := run(nil, simBlock{logo: c.occ, logoKnown: c.known}); got != c.rules || score != c.score {
			t.Errorf("occupancy %v known %v: rules %q score %v, want %q %v", c.occ, c.known, got, score, c.rules, c.score)
		}
	}

	cfg := simConfig()
	cfg.Scoring.LogoAbsentFactor, cfg.Scoring.LogoPresentFactor, cfg.Scoring.StrictFactor = 3, 0.25, 4
	if _, score := run(cfg, simBlock{logo: 0, logoKnown: 1}); score != 12 {
		t.Errorf("configured weights: absent score %v, want 12", score)
	}
	if _, score := run(cfg, simBlock{logo: 1, logoKnown: 1}); score != 1 {
		t.Errorf("configured weights: present score %v, want 1", score)
	}
	cfg = simConfig()
	cfg.Scoring.LogoPresentOccupancy, cfg.Scoring.LogoAbsentOccupancy, cfg.Scoring.MinCoverageLogo = 0.6, 0.4, 0.5
	if got, _ := run(cfg, simBlock{logo: 0.6, logoKnown: 1}); got != "strict_standard_duration,logo_present" {
		t.Errorf("present occupancy 0.6: %q", got)
	}
	if got, _ := run(cfg, simBlock{logo: 0.4, logoKnown: 0.5}); got != "strict_standard_duration,logo_absent" {
		t.Errorf("absent occupancy 0.4, coverage 0.5: %q", got)
	}

	// SCORE-02: a neutral modifier disables the rule's scoring effect, and
	// with it the evidence it would have supplied.
	cfg = simConfig()
	cfg.Scoring.LogoAbsentFactor = 1
	a := simulate(cfg, []simBlock{prog(50 * sec), {us: 37_000_000, logo: 0, logoKnown: 1, plainStart: true}, {us: 70_000_000, logo: 1, logoKnown: 1, plainStart: true}})
	if h := a.hyp(t, 1, 1); h.score != 1 || h.fam != 0 {
		t.Errorf("neutral logo-absent factor: score %v families %s", h.score, h.families())
	}
	cfg = simConfig()
	cfg.Scoring.StrictFactor = 1
	cfg.Scoring.LooseFactor = 1
	cfg.Scoring.CombinedFactor = 1
	a = simulate(cfg, []simBlock{plain(47 * sec), plain(30 * sec), plain(15 * sec), plain(15 * sec), plain(53 * sec)})
	for i := range a.blocks {
		if a.blocks[i].score != 1 || a.blocks[i].nf != 0 {
			t.Errorf("neutral duration factors: block %d score %v", i, a.blocks[i].score)
		}
	}
	if len(a.units) != 0 {
		t.Errorf("neutral duration factors: units %s", a.unitSpans())
	}
}

// AT-14 / SCORE-06: a duration-only hypothesis fails the evidence gate, and
// repeated duration matches do not bypass it. Boundary evidence must be at
// both edges of a unit (recording edges exempt) under the default policy.
func TestEvidenceGate(t *testing.T) {
	p := func(dur time.Duration) simBlock { return simBlock{us: d(dur), plainStart: true} }
	// Five consecutive 30 s blocks between evidence-free boundaries (scene
	// cuts, size changes, silence alone, bumpers alone).
	a := simulate(nil, []simBlock{plain(47 * sec), p(30 * sec), p(30 * sec), p(30 * sec), p(30 * sec), p(30 * sec), p(53 * sec)})
	if len(a.units) != 0 || a.labels() != "UUUUUUU" {
		t.Fatalf("duration-only blocks accepted: units %s labels %s", a.unitSpans(), a.labels())
	}
	for i := 1; i <= 5; i++ {
		h := a.hyp(t, i, i)
		if h.score != 2 || h.eligible || h.rejects() != "no_evidence_family" || h.fam != 0 {
			t.Errorf("block %d: score %v rejects %q families %q", i, h.score, h.rejects(), h.families())
		}
		// The rejected hypothesis is reported as such on the timeline.
		if got := reasonList(a.blocks[i].reasons); len(got) != 1 || got[0] != "rejected_ad_hypothesis" {
			t.Errorf("block %d reasons = %v", i, got)
		}
	}
	// The same blocks between dark boundaries are accepted.
	a = simulate(nil, []simBlock{plain(47 * sec), plain(30 * sec), plain(30 * sec), plain(53 * sec)})
	if a.labels() != "UAAU" || a.hyp(t, 1, 1).families() != "dark_uniform_boundary" {
		t.Errorf("dark boundaries: labels %s", a.labels())
	}
	// A corroborated audio/visual boundary is its own family.
	a = simulate(nil, []simBlock{plain(47 * sec), {us: d(30 * sec), avStart: true}, {us: d(53 * sec), avStart: true}})
	if a.labels() != "UAU" || a.hyp(t, 1, 1).families() != "av_corroborated_boundary" {
		t.Errorf("av boundaries: labels %s families %s", a.labels(), a.hyp(t, 1, 1).families())
	}

	// Evidence at one edge only.
	oneEdge := []simBlock{plain(47 * sec), plain(30 * sec), p(53 * sec)}
	a = simulate(nil, oneEdge)
	if h := a.hyp(t, 1, 1); h.eligible || h.rejects() != "no_evidence_family" {
		t.Errorf("one qualifying edge under the both-edges policy: rejects %q", h.rejects())
	}
	cfg := simConfig()
	cfg.Scoring.BoundaryEdges = "either"
	a = simulate(cfg, oneEdge)
	if h := a.hyp(t, 1, 1); !h.eligible || h.families() != "dark_uniform_boundary" {
		t.Errorf("either-edge policy: rejects %q", h.rejects())
	}

	// A recording edge is exempt from the both-edges requirement but is
	// never evidence itself (BREAK-04).
	a = simulate(nil, []simBlock{plain(30 * sec), p(53 * sec)})
	if h := a.hyp(t, 0, 0); h.eligible || h.fam != 0 {
		t.Errorf("edge block with no evidence: families %q", h.families())
	}
	a = simulate(cfg, []simBlock{plain(30 * sec), p(53 * sec)})
	if h := a.hyp(t, 0, 0); h.eligible || h.fam != 0 {
		t.Errorf("edge block with no evidence (either): families %q", h.families())
	}

	// Non-duration score evidence satisfies the gate without any boundary
	// evidence: validated logo absence.
	a = simulate(nil, []simBlock{prog(50 * sec), {us: d(30 * sec), logoKnown: 1, plainStart: true}, {us: d(70 * sec), logo: 1, logoKnown: 1, plainStart: true}})
	if h := a.hyp(t, 1, 1); !h.eligible || h.families() != "logo_absent" || a.labels() != "PAP" {
		t.Errorf("logo absence: families %q labels %s", h.families(), a.labels())
	}
}

// SCORE-01: the threshold is inclusive and configurable.
func TestScoreThresholdInclusive(t *testing.T) {
	blocks := []simBlock{plain(47 * sec), {us: 31_000_000}, plain(53 * sec)} // loose: exactly 1.5
	a := simulate(nil, blocks)
	if h := a.hyp(t, 1, 1); h.score != 1.5 || !h.eligible || a.labels() != "UAU" {
		t.Errorf("score 1.5 at threshold 1.5: eligible %v labels %s", h.eligible, a.labels())
	}
	cfg := simConfig()
	cfg.Scoring.Threshold = 1.5000001
	a = simulate(cfg, blocks)
	if h := a.hyp(t, 1, 1); h.eligible || h.rejects() != "below_threshold" || a.labels() != "UUU" {
		t.Errorf("score 1.5 below threshold: rejects %q labels %s", h.rejects(), a.labels())
	}
	// Below the threshold the block is not a "rejected ad": it simply has
	// insufficient evidence.
	if got := reasonList(a.blocks[1].reasons); len(got) != 1 || got[0] != "insufficient_evidence" {
		t.Errorf("reasons = %v", got)
	}
}

// relBlocks: four ordinary 53 s blocks and one 30 s block that is brighter,
// less uniform, louder, more often silent and cut faster.
func relBlocks() []simBlock {
	base := simBlock{us: d(53 * sec), mean: 100, std: 20, audioMS: 1e-2, silentFrac: 0.02, scenes: 5}
	hot := simBlock{us: d(30 * sec), mean: 200, std: 60, audioMS: 1e-1, silentFrac: 0.5, scenes: 20}
	return []simBlock{base, base, hot, base, base}
}

// AT-14 / SCORE-05: each optional relative-statistics rule is individually
// enabled, is off by default, and affects only its own factor.
func TestRelativeMetricsAreIndependent(t *testing.T) {
	a := simulate(nil, relBlocks())
	if got := a.ruleNames(2); got != "strict_standard_duration" {
		t.Fatalf("defaults: rules %q (relative rules must be off)", got)
	}
	type rule struct {
		name   string
		enable func(*ScoringConfig)
		value  float64
	}
	rs := []rule{
		{"relative_brightness", func(s *ScoringConfig) { s.RelBrightness = true }, 200},
		{"relative_nonuniformity", func(s *ScoringConfig) { s.RelNonuniformity = true }, 60},
		{"relative_audio_energy", func(s *ScoringConfig) { s.RelAudioEnergy = true }, 0.5*1e-6 + 0.5*1e-1},
		{"relative_silence_fraction", func(s *ScoringConfig) { s.RelSilence = true }, 0.5},
		{"relative_scene_rate", func(s *ScoringConfig) { s.RelSceneRate = true }, 20.0 / 30},
	}
	for _, r := range rs {
		cfg := simConfig()
		r.enable(&cfg.Scoring)
		a := simulate(cfg, relBlocks())
		if got := a.ruleNames(2); got != "strict_standard_duration,"+r.name || a.blocks[2].score != 4 {
			t.Errorf("%s only: rules %q score %v", r.name, got, a.blocks[2].score)
			continue
		}
		f := a.factorOut(&a.blocks[2].factors[1])
		if f.Family != r.name || f.Multiplier != 2 || math.Abs(*f.Value-r.value) > 1e-6*r.value+1e-9 || *f.Coverage != 1 {
			t.Errorf("%s factor = %s", r.name, mustJSON(t, f))
		}
		// The rule fired because the value is at least 1.3 x its baseline.
		if *f.Value < *f.Baseline*1.3 {
			t.Errorf("%s: value %v below 1.3 x baseline %v", r.name, *f.Value, *f.Baseline)
		}
		// It is its own evidence family and affects no other block.
		if h := a.hyp(t, 2, 2); h.families() != "dark_uniform_boundary,"+r.name {
			t.Errorf("%s: families %q", r.name, h.families())
		}
		for _, i := range []int{0, 1, 3, 4} {
			if a.blocks[i].nf != 0 || a.blocks[i].score != 1 {
				t.Errorf("%s: ordinary block %d has factors %q", r.name, i, a.ruleNames(i))
			}
		}
	}
	// All five: each applies once.
	cfg := simConfig()
	for _, r := range rs {
		r.enable(&cfg.Scoring)
	}
	a = simulate(cfg, relBlocks())
	if a.blocks[2].nf != 6 || a.blocks[2].score != 64 {
		t.Errorf("all rules: %d factors score %v (%s)", a.blocks[2].nf, a.blocks[2].score, a.ruleNames(2))
	}
	// A neutral relative factor disables them all.
	cfg.Scoring.RelativeFactor = 1
	a = simulate(cfg, relBlocks())
	if got := a.ruleNames(2); got != "strict_standard_duration" {
		t.Errorf("neutral relative factor: rules %q", got)
	}
	// The excess ratio is configurable.
	cfg = simConfig()
	cfg.Scoring.RelBrightness = true
	cfg.Scoring.RelExcessRatio = 2 // 200 against a baseline of ~112.4: below 2x
	a = simulate(cfg, relBlocks())
	if got := a.ruleNames(2); got != "strict_standard_duration" {
		t.Errorf("excess ratio 2: rules %q", got)
	}
}

// SCORE-05, PIPE-02: minimum support, minimum scene count and coverage for
// the relative rules.
func TestRelativeMetricSupport(t *testing.T) {
	all := func() *Config {
		cfg := simConfig()
		s := &cfg.Scoring
		s.RelBrightness, s.RelNonuniformity, s.RelAudioEnergy, s.RelSilence, s.RelSceneRate = true, true, true, true, true
		return cfg
	}
	// Fewer scene events than scoring.scene_min_events (3): the rate is high
	// (2 in 30 s against ~0.09/s) but not supported.
	bl := relBlocks()
	bl[2].scenes = 2
	a := simulate(all(), bl)
	if got := a.ruleNames(2); got != "strict_standard_duration,relative_brightness,relative_nonuniformity,relative_audio_energy,relative_silence_fraction" {
		t.Errorf("two scene events: rules %q", got)
	}
	cfg := all()
	cfg.Scoring.SceneMinEvents = 2
	bl[2].scenes = 4
	bl[0].scenes, bl[1].scenes, bl[3].scenes, bl[4].scenes = 1, 1, 1, 1
	a = simulate(cfg, bl)
	if got := a.ruleNames(2); !strings.Contains(got, "relative_scene_rate") {
		t.Errorf("scene_min_events 2: rules %q", got)
	}

	// A hypothesis shorter than scoring.relative_min_support (2 s).
	bl = relBlocks()
	bl[2].us = 1_900_000
	a = simulate(all(), bl)
	if got := a.ruleNames(2); got != "" {
		t.Errorf("1.9 s block: rules %q", got)
	}
	bl[2].us = 2_000_000
	bl[2].scenes = 0
	a = simulate(all(), bl)
	if a.blocks[2].nf != 4 {
		t.Errorf("2.0 s block: rules %q", a.ruleNames(2))
	}

	// No audio in the block: audio rules are neutral, video rules still
	// apply (unknown is not silence and not zero energy).
	bl = relBlocks()
	bl[2].audioMS = -1
	a = simulate(all(), bl)
	if got := a.ruleNames(2); got != "strict_standard_duration,relative_brightness,relative_nonuniformity,relative_scene_rate" {
		t.Errorf("no audio in block: rules %q", got)
	}
}

// AT-14 / BASE-01: near-zero baselines use their documented floor and can
// never produce a nonfinite or spurious result.
func TestNearZeroBaselines(t *testing.T) {
	all := func() *Config {
		cfg := simConfig()
		s := &cfg.Scoring
		s.RelBrightness, s.RelNonuniformity, s.RelAudioEnergy, s.RelSilence, s.RelSceneRate = true, true, true, true, true
		return cfg
	}
	// Black, perfectly uniform, digitally silent... and never "silent" by
	// fraction because there is no audio below the level other than all of
	// it; no scene events.
	zero := simBlock{us: d(30 * sec), mean: 1e-9, std: 1e-9, audioMS: 1e-30}
	blocks := []simBlock{zero, zero, zero, zero}
	a := simulate(all(), blocks)
	out := a.base.out
	js, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("baselines do not encode: %v", err)
	}
	for name, b := range map[string]Baseline{"brightness": out.Brightness, "nonuniformity": out.Nonuniformity, "audio": out.AudioEnergy, "scene": out.SceneRate} {
		if !b.Reliable || b.Origin != "recording_floored" || b.Value == nil || *b.Value <= 0 || math.IsInf(*b.Value, 0) || math.IsNaN(*b.Value) {
			t.Errorf("%s baseline = %s (all: %s)", name, mustJSON(t, b), js)
		}
	}
	if *out.Brightness.Value != 1 || *out.Nonuniformity.Value != 0.5 || *out.SceneRate.Value != 0.02 || math.Abs(*out.AudioEnergy.Value-1e-7) > 1e-12 {
		t.Errorf("floors not applied: %s", js)
	}
	for i := range a.blocks {
		b := &a.blocks[i]
		if math.IsNaN(b.score) || math.IsInf(b.score, 0) {
			t.Fatalf("block %d score %v", i, b.score)
		}
		// Zero against a floored baseline is not "unusually high".
		for k := 0; k < int(b.nf); k++ {
			switch b.factors[k].rule {
			case ruleRelBrightness, ruleRelNonuniformity, ruleRelAudio, ruleRelScene:
				t.Errorf("block %d: %s fired on a zero measurement", i, ruleNames[b.factors[k].rule])
			}
		}
	}

	// With the floors configured to zero the baseline itself is zero. The
	// affected rule must then be suppressed: a measurement of zero does not
	// exceed a baseline of zero by any ratio.
	cfg := all()
	cfg.Baselines.FloorBrightness, cfg.Baselines.FloorNonuniformity = 0, 0
	cfg.Baselines.FloorSilence, cfg.Baselines.FloorSceneRate = 0, 0
	cfg.Baselines.FloorAudioDBFS = -160
	quiet := simBlock{us: d(30 * sec), mean: 1e-9, std: 1e-9, audioMS: 1e-2} // no silence, no scenes
	a = simulate(cfg, []simBlock{quiet, quiet, quiet, quiet})
	for i := range a.blocks {
		b := &a.blocks[i]
		if math.IsNaN(b.score) || math.IsInf(b.score, 0) {
			t.Fatalf("block %d score %v", i, b.score)
		}
		for k := 0; k < int(b.nf); k++ {
			f := b.factors[k]
			if f.rule >= ruleRelBrightness && f.value == 0 {
				t.Errorf("block %d: %s fired with value 0 against baseline %v", i, ruleNames[f.rule], f.base)
			}
		}
	}
	if _, err := json.Marshal(a.base.out); err != nil {
		t.Errorf("baselines do not encode: %v", err)
	}
}

// BASE-01: recording baselines are over usable observation time only, so
// data gaps do not bias them; they need a minimum support.
func TestRecordingBaselinesIgnoreGaps(t *testing.T) {
	// 60 s at brightness 100 with 6 scene events, a 300 s gap, 60 s at 200.
	blocks := []simBlock{
		{us: d(60 * sec), mean: 100, scenes: 6},
		{us: d(300 * sec), gap: true},
		{us: d(60 * sec), mean: 200, scenes: 6},
	}
	a := simulate(nil, blocks)
	o := a.base.out
	if *o.Brightness.Value != 150 || o.Brightness.SupportUS != 120_000_000 || o.Brightness.Origin != "recording" {
		t.Errorf("brightness baseline = %s", mustJSON(t, o.Brightness))
	}
	if *o.SceneRate.Value != 0.1 { // 12 events in 120 s of video, not in 420 s
		t.Errorf("scene-rate baseline = %s", mustJSON(t, o.SceneRate))
	}
	if *o.AudioEnergy.Value < 0.0099 || *o.AudioEnergy.Value > 0.0101 || o.AudioEnergy.SupportUS != 120_000_000 || o.AudioEnergy.Unit != "mean_square" {
		t.Errorf("audio baseline = %s (linear energy over observed audio)", mustJSON(t, o.AudioEnergy))
	}

	// Below baselines.min_recording_support the baseline is unreliable and
	// its rule is disabled.
	cfg := simConfig()
	cfg.Baselines.MinRecording = 10 * time.Minute
	cfg.Scoring.RelBrightness = true
	a = simulate(cfg, relBlocks())
	if b := a.base.out.Brightness; b.Reliable || b.Value != nil || b.Reason != "insufficient_support" {
		t.Errorf("brightness baseline = %s", mustJSON(t, b))
	}
	if got := a.ruleNames(2); got != "strict_standard_duration" {
		t.Errorf("unsupported baseline: rules %q", got)
	}
}

// PIPE-02: caption and aspect factors need known observations over the
// configured share of the hypothesis.
func TestClassificationCoverageRequirement(t *testing.T) {
	mk := func(capFrac, aspFrac float64) []simBlock {
		p := simBlock{us: d(130 * sec), cap: CaptionRollUp, aspect: 1.78}
		ad := simBlock{us: d(30 * sec), cap: CaptionPopOn, capFrac: capFrac, aspect: 2.35, aspectFrac: aspFrac}
		return []simBlock{p, ad, p}
	}
	for _, c := range []struct {
		capFrac, aspFrac float64
		rules            string
	}{
		{1, 1, "strict_standard_duration,caption_style_mismatch,aspect_ratio_mismatch"},
		{0.8, 0.8, "strict_standard_duration,caption_style_mismatch,aspect_ratio_mismatch"},
		{0.79, 1, "strict_standard_duration,aspect_ratio_mismatch"},
		{1, 0.79, "strict_standard_duration,caption_style_mismatch"},
		{0.5, 0.5, "strict_standard_duration"},
	} {
		a := simulate(nil, mk(c.capFrac, c.aspFrac))
		if got := a.ruleNames(1); got != c.rules {
			t.Errorf("caption coverage %v aspect coverage %v: rules %q, want %q", c.capFrac, c.aspFrac, got, c.rules)
		}
	}
	cfg := simConfig()
	cfg.Scoring.MinCoverageCaptions, cfg.Scoring.MinCoverageAspect = 0.5, 0.5
	a := simulate(cfg, mk(0.5, 0.5))
	if got := a.ruleNames(1); got != "strict_standard_duration,caption_style_mismatch,aspect_ratio_mismatch" {
		t.Errorf("coverage 0.5 configured: rules %q", got)
	}
	// SCORE-07: both program blocks are long, stable and match both
	// baselines.
	a = simulate(nil, mk(1, 1))
	if a.labels() != "PAP" || a.blocks[0].program&programStable == 0 {
		t.Errorf("labels = %s", a.labels())
	}
	if a.blocks[1].score != 8 {
		t.Errorf("ad score = %v", a.blocks[1].score)
	}
}

// SCORE-07, RESULT-01: program needs positive evidence, a score below the
// threshold, sufficient coverage and no unresolved conflicting evidence.
func TestProgramEvidenceRules(t *testing.T) {
	// Logo present but the caption style contradicts the program baseline.
	conflict := simBlock{us: d(30 * sec), logo: 1, logoKnown: 1, cap: CaptionPopOn}
	p := simBlock{us: d(130 * sec), logo: 1, logoKnown: 1, cap: CaptionRollUp}
	a := simulate(nil, []simBlock{p, conflict, p})
	if a.labels() != "PUP" {
		t.Fatalf("labels = %s", a.labels())
	}
	if got := reasonList(a.blocks[1].reasons); len(got) != 1 || got[0] != "conflicting_evidence" {
		t.Errorf("conflicting block reasons = %v", got)
	}
	if a.blocks[1].score != 2*0.1*2 {
		t.Errorf("conflicting block score = %v", a.blocks[1].score)
	}
	if got := reasonList(a.blocks[0].reasons); mustJSON(t, got) != `["logo_present"]` {
		t.Errorf("program reasons = %v", got)
	}

	// A low ad score by itself is not program: no logo, no captions.
	a = simulate(nil, []simBlock{plain(200 * sec), plain(37 * sec)})
	if a.labels() != "UU" {
		t.Errorf("no positive evidence: labels %s", a.labels())
	}
	if got := reasonList(a.blocks[0].reasons); mustJSON(t, got) != `["insufficient_evidence"]` {
		t.Errorf("reasons = %v", got)
	}

	// A long stable interval needs both baselines to match; a caption
	// baseline alone is not enough.
	long := simBlock{us: d(130 * sec), cap: CaptionRollUp}
	a = simulate(nil, []simBlock{long, long})
	if a.labels() != "UU" {
		t.Errorf("captions without aspect: labels %s", a.labels())
	}
	// With both, intervals longer than the maximum unit are program; the
	// stable run may consist of several blocks.
	st := func(dur time.Duration) simBlock { return simBlock{us: d(dur), cap: CaptionRollUp, aspect: 1.78} }
	a = simulate(nil, []simBlock{st(130 * sec), st(47 * sec), st(53 * sec), st(41 * sec)})
	if a.labels() != "PPPP" {
		t.Errorf("stable run: labels %s", a.labels())
	}
	if got := reasonList(a.blocks[1].reasons); mustJSON(t, got) != `["stable_long_interval"]` {
		t.Errorf("stable run reasons = %v", got)
	}
	// A stable run no longer than the maximum unit duration is not program.
	a = simulate(nil, []simBlock{st(47 * sec), st(53 * sec)})
	if a.labels() != "UU" {
		t.Errorf("short stable run: labels %s", a.labels())
	}
	// Missing coverage is never program.
	a = simulate(nil, []simBlock{prog(60 * sec), {us: d(40 * sec), gap: true}, prog(60 * sec)})
	if a.labels() != "PUP" || mustJSON(t, reasonList(a.blocks[1].reasons)) != `["missing_coverage"]` {
		t.Errorf("gap: labels %s reasons %v", a.labels(), reasonList(a.blocks[1].reasons))
	}
}

// SCORE-04: a group does not absorb a constituent block that already meets
// the positive program conditions.
func TestGroupDoesNotAbsorbProgramBlock(t *testing.T) {
	blocks := []simBlock{prog(60 * sec), noLogo(12 * sec), prog(6 * sec), noLogo(12 * sec), prog(60 * sec)}
	a := simulate(nil, blocks)
	h := a.hyp(t, 1, 3)
	if h.eligible || !containsReject(h, rejContainsProgram) {
		t.Errorf("group over a program block: rejects %q", h.rejects())
	}
	// Each 12 s logo-free block is a valid unit, but a 12 s run is not a
	// complete break; without bridging the program block between them stays
	// program. With the default bridging of logo-only program gaps the two
	// runs form one 30 s break.
	if a.labels() != "PAAAP" {
		t.Errorf("labels = %s", a.labels())
	}
	noBridge := simConfig()
	noBridge.Breaks.BridgeGaps = false
	if got := simulate(noBridge, blocks).labels(); got != "PUPUP" {
		t.Errorf("without bridging: labels = %s", got)
	}
	// Without the program block in the middle the three blocks are one
	// 30 s combined unit.
	blocks[2] = noLogo(6 * sec)
	a = simulate(nil, blocks)
	if a.labels() != "PAAAP" || a.unitSpans() != "1-3" {
		t.Errorf("labels = %s units %s", a.labels(), a.unitSpans())
	}
}

func containsReject(h *hypEval, bit uint16) bool { return h.reject&bit != 0 }

// SCORE-04: selection maximizes the sum of unit_duration x log(unit_score);
// ties prefer fewer units, then earlier boundaries.
func TestSelectionWeightsAndTieBreaks(t *testing.T) {
	// Two strict 15 s blocks (2.0 each) beat one combined 30 s (1.5):
	// 30 ln 2 > 30 ln 1.5.
	a := simulate(nil, []simBlock{plain(47 * sec), plain(15 * sec), plain(15 * sec), plain(53 * sec)})
	if a.unitSpans() != "1-1 2-2" {
		t.Errorf("units = %s", a.unitSpans())
	}
	// With equal modifiers the sums tie exactly; fewer units wins.
	cfg := simConfig()
	cfg.Scoring.CombinedFactor = 2
	a = simulate(cfg, []simBlock{plain(47 * sec), plain(15 * sec), plain(15 * sec), plain(53 * sec)})
	if a.unitSpans() != "1-2" {
		t.Errorf("tie, fewer units: units = %s", a.unitSpans())
	}
	// The same exact tie in the default configuration: two adjacent 18 s
	// logo-free blocks score 2.0 each and 2.0 together, so 18 ln 2 + 18 ln 2
	// equals 36 ln 2. The totals differ only by floating-point rounding of
	// differently grouped sums and must still be recognized as a tie. (The
	// boundary between the two blocks is not dark: blocks of non-standard
	// total are combined only across non-dark boundaries.)
	a = simulate(nil, []simBlock{plain(25 * sec), noLogo(18 * sec), {us: d(18 * sec), logoKnown: 1, plainStart: true}, noLogo(45 * sec), prog(12 * sec)})
	if a.unitSpans() != "1-2 3-3" {
		t.Errorf("rounding-level tie, fewer units: units = %s", a.unitSpans())
	}
	// A stronger combined modifier wins outright.
	cfg.Scoring.CombinedFactor = 3
	a = simulate(cfg, []simBlock{plain(47 * sec), plain(15 * sec), plain(15 * sec), plain(53 * sec)})
	if a.unitSpans() != "1-2" {
		t.Errorf("combined 3.0: units = %s", a.unitSpans())
	}

	// Equal value and equal count: with a validated logo absent, {12, 3+12}
	// and {12+3, 12} both score 12 ln 2 + 15 ln 3 (the 3 s block is too
	// short to stand alone). The earlier internal boundary (12 s) wins.
	a = simulate(nil, []simBlock{prog(60 * sec), noLogo(12 * sec), noLogo(3 * sec), noLogo(12 * sec), prog(60 * sec)})
	if a.unitSpans() != "1-1 2-3" {
		t.Errorf("tie, earlier boundary: units = %s", a.unitSpans())
	}

	// A longer, weaker unit can beat a shorter, stronger one: weights are
	// duration x log(score), not score alone. 60 s combined (1.5) against a
	// strict 10 s inside it (2.0): 60 ln 1.5 = 24.3 > 10 ln 2 = 6.9.
	a = simulate(nil, []simBlock{plain(130 * sec), plain(27 * sec), plain(10 * sec), plain(23 * sec), plain(140 * sec)})
	if a.unitSpans() != "1-3" {
		t.Errorf("long combined: units = %s", a.unitSpans())
	}

	// Units never overlap and unselected time keeps its block evidence.
	a = simulate(nil, []simBlock{plain(130 * sec), plain(30 * sec), plain(37 * sec), plain(30 * sec), plain(140 * sec)})
	if a.unitSpans() != "1-1 3-3" || a.labels() != "UAUAU" || a.blocks[2].score != 1 {
		t.Errorf("units = %s labels %s", a.unitSpans(), a.labels())
	}
}

// SCORE-04: the selection is optimal. Random block layouts are checked
// against an exhaustive search over every set of nonoverlapping eligible
// hypotheses.
func TestSelectionIsOptimal(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	durs := []int64{2, 4, 5, 6, 10, 12, 15, 18, 20, 25, 30, 31, 45, 47, 60}
	for iter := 0; iter < 300; iter++ {
		n := 3 + rng.Intn(9)
		blocks := make([]simBlock, n)
		for i := range blocks {
			b := simBlock{us: durs[rng.Intn(len(durs))] * 1_000_000, plainStart: rng.Intn(4) == 0, avStart: rng.Intn(5) == 0}
			switch rng.Intn(4) {
			case 0:
				b.logo, b.logoKnown = 1, 1
			case 1, 2:
				b.logo, b.logoKnown = 0, 1
			}
			if rng.Intn(12) == 0 {
				b = simBlock{us: b.us, gap: true}
			}
			blocks[i] = b
		}
		if blocks[0].gap {
			blocks[0].gap = false
		}
		cfg := simConfig()
		if iter%3 == 1 {
			cfg.Scoring.CombinedFactor = 2 // provoke exact ties
		}
		if iter%5 == 2 {
			cfg.Scoring.BoundaryEdges = "either"
		}
		a := simulate(cfg, blocks)

		// Index eligible hypotheses by start block.
		type cand struct {
			j int
			w float64
		}
		byStart := make([][]cand, n)
		elig := map[[2]int]bool{}
		for k := range a.hyps {
			h := &a.hyps[k]
			if !h.eligible {
				continue
			}
			dur := a.blocks[h.j].end - a.blocks[h.i].start
			byStart[h.i] = append(byStart[h.i], cand{int(h.j), float64(dur) * math.Log(h.score)})
			elig[[2]int{int(h.i), int(h.j)}] = true
			// Eligibility implies every SCORE-04 condition.
			atEdge := a.bounds[h.i].edge || a.bounds[h.j+1].edge
			if h.score < cfg.Scoring.Threshold || h.fam == 0 || dur > cfg.unitMaxUS() || (dur < us(cfg.Breaks.MinUnit) && !(cfg.Breaks.AllowTruncatedEdges && atEdge)) {
				t.Fatalf("iter %d: ineligible hypothesis %d..%d marked eligible", iter, h.i, h.j)
			}
			for b := h.i; b <= h.j; b++ {
				if a.blocks[b].gapUS != 0 {
					t.Fatalf("iter %d: eligible hypothesis %d..%d spans missing coverage", iter, h.i, h.j)
				}
			}
		}
		// Exhaustive search: best value, then fewest units.
		// Sums of duration x log(score) are compared with a relative
		// tolerance far below any real difference, so that differently
		// ordered additions of an exact tie compare equal.
		same := func(x, y float64) bool { return math.Abs(x-y) <= 1e-12*math.Max(math.Abs(x), math.Abs(y)) }
		bestV, bestC := -1.0, 0
		var rec func(i int, v float64, c int)
		rec = func(i int, v float64, c int) {
			if i == n {
				if same(v, bestV) {
					bestC = min(bestC, c)
				} else if v > bestV {
					bestV, bestC = v, c
				}
				return
			}
			rec(i+1, v, c)
			for _, cd := range byStart[i] {
				rec(cd.j+1, v+cd.w, c+1)
			}
		}
		rec(0, 0, 0)

		var gotV float64
		prevEnd := -1
		for _, u := range a.units {
			i, j := int(u.h.i), int(u.h.j)
			if i <= prevEnd {
				t.Fatalf("iter %d: units overlap: %s", iter, a.unitSpans())
			}
			prevEnd = j
			if !elig[[2]int{i, j}] {
				t.Fatalf("iter %d: selected unit %d..%d is not an eligible hypothesis", iter, i, j)
			}
			gotV += float64(a.blocks[j].end-a.blocks[i].start) * math.Log(u.h.score)
		}
		if !same(gotV, bestV) {
			t.Fatalf("iter %d: selected value %v, optimum %v (units %s)", iter, gotV, bestV, a.unitSpans())
		}
		if len(a.units) != bestC {
			t.Fatalf("iter %d: %d units selected, an equally good selection has %d (units %s)", iter, len(a.units), bestC, a.unitSpans())
		}
		// Label invariants on the same random layouts.
		for i := range a.blocks {
			b := &a.blocks[i]
			if b.gapUS != 0 && b.label != lblUnknown {
				t.Fatalf("iter %d: block %d with missing coverage labeled %s", iter, i, labelNames[b.label])
			}
			if b.label == lblAd && (b.run < 0 || !a.runs[b.run].accepted) {
				t.Fatalf("iter %d: block %d is an ad outside an accepted run", iter, i)
			}
			if b.label == lblProgram && (b.program == 0 || b.unit >= 0) {
				t.Fatalf("iter %d: block %d is program without program evidence", iter, i)
			}
			if math.IsNaN(b.score) || math.IsInf(b.score, 0) || b.score <= 0 {
				t.Fatalf("iter %d: block %d score %v", iter, i, b.score)
			}
		}
		for ri := range a.runs {
			r := &a.runs[ri]
			edge := r.start == 0 || r.end == a.dur
			if dur := r.end - r.start; r.accepted && ((dur < us(cfg.Breaks.MinBreak) && !(cfg.Breaks.AllowTruncatedEdges && edge)) || dur > us(cfg.Breaks.MaxBreak)) {
				t.Fatalf("iter %d: accepted break of %d us", iter, dur)
			}
		}
	}
}

// SCORE-04: scoring.max_unit_blocks bounds how many blocks one hypothesis
// combines.
func TestMaxUnitBlocks(t *testing.T) {
	blocks := []simBlock{plain(130 * sec), plain(7 * sec), plain(17 * sec), plain(6 * sec), plain(140 * sec)}
	a := simulate(nil, blocks)
	if a.unitSpans() != "1-3" {
		t.Fatalf("units = %s", a.unitSpans())
	}
	cfg := simConfig()
	cfg.Scoring.MaxUnitBlocks = 2
	a = simulate(cfg, blocks)
	if len(a.units) != 0 {
		t.Errorf("max_unit_blocks 2: units = %s", a.unitSpans())
	}
	for k := range a.hyps {
		if a.hyps[k].j-a.hyps[k].i >= 2 {
			t.Errorf("hypothesis %d..%d evaluated beyond max_unit_blocks", a.hyps[k].i, a.hyps[k].j)
		}
	}
}

// SCORE-08, OUT-01: diagnostics levels and the retention bound.
func TestHypothesisDiagnosticsLevels(t *testing.T) {
	s := newScenario(blk{D: 47 * sec}, blk{D: 15 * sec}, blk{D: 15 * sec}, blk{D: 53 * sec})

	cfg := DefaultConfig() // summary
	r := s.run(t, cfg)
	if r.Diagnostics != nil {
		t.Errorf("summary level carries diagnostics")
	}
	if len(r.AdUnits) != 2 || len(r.AdBreaks) != 1 {
		t.Errorf("summary: units %d breaks %d", len(r.AdUnits), len(r.AdBreaks))
	}

	cfg.Outputs.Diagnostics = "detailed"
	r = s.run(t, cfg)
	d := r.Diagnostics
	if d == nil || d.Level != "detailed" || len(d.Blocks) != 4 || len(d.Boundaries) != 5 {
		t.Fatalf("detailed diagnostics = %+v", d)
	}
	// Only hypotheses that reached the threshold are kept.
	for _, h := range d.Hypotheses {
		if h.Score < 1.5 {
			t.Errorf("detailed level kept hypothesis %s with score %v", h.ID, h.Score)
		}
	}
	if len(d.Hypotheses) != 3 || d.HypothesesEvaluated <= 3 || d.HypothesesTruncated {
		t.Errorf("detailed: %d hypotheses kept of %d", len(d.Hypotheses), d.HypothesesEvaluated)
	}

	cfg.Outputs.Diagnostics = "full"
	r = s.run(t, cfg)
	if n := len(r.Diagnostics.Hypotheses); n != r.Diagnostics.HypothesesEvaluated || n < 8 {
		t.Errorf("full: %d hypotheses kept of %d", n, r.Diagnostics.HypothesesEvaluated)
	}
	// Stable IDs link units to hypotheses, blocks and boundaries (OUT-01).
	ids := map[string]bool{}
	for _, h := range r.Diagnostics.Hypotheses {
		ids[h.ID] = true
	}
	for _, b := range r.Diagnostics.Blocks {
		ids[b.ID] = true
	}
	for _, b := range r.Diagnostics.Boundaries {
		ids[b.ID] = true
	}
	for _, u := range r.AdUnits {
		if !ids[u.HypothesisID] || !ids[u.StartBoundaryID] || !ids[u.EndBoundaryID] || !ids[u.BlockIDs[0]] {
			t.Errorf("unit %s has dangling references: %s", u.ID, mustJSON(t, u))
		}
	}
	for _, br := range r.AdBreaks {
		for _, id := range br.BoundaryIDs {
			if !ids[id] {
				t.Errorf("break %s references unknown boundary %s", br.ID, id)
			}
		}
	}

	// The retention bound truncates with a warning, never silently, and the
	// selected units are always present.
	cfg.Outputs.MaxHypotheses = 1
	r = s.run(t, cfg)
	if !r.Diagnostics.HypothesesTruncated || !hasWarning(r, "hypotheses_truncated") {
		t.Errorf("truncation not reported")
	}
	have := map[string]bool{}
	for _, h := range r.Diagnostics.Hypotheses {
		have[h.ID] = true
	}
	for _, u := range r.AdUnits {
		if !have[u.HypothesisID] {
			t.Errorf("selected hypothesis %s missing after truncation", u.HypothesisID)
		}
	}
	if len(r.AdUnits) != 2 {
		t.Errorf("truncation changed the classification")
	}
}

// scoring.unmatched_length_min_families: a block of no standard length needs
// that many independent ad signals; boundary evidence does not count.
func TestUnmatchedLengthMinFamilies(t *testing.T) {
	blocks := func(aspect float64) []simBlock {
		return []simBlock{prog(60 * sec), {us: d(37 * sec), logoKnown: 1, aspect: aspect}, prog(60 * sec)}
	}
	a := simulate(nil, blocks(0))
	h := a.hyp(t, 1, 1)
	// Dark boundaries plus logo absence: one signal family besides the
	// boundaries.
	if !h.eligible || h.families() != "dark_uniform_boundary,logo_absent" {
		t.Fatalf("default: eligible %v families %q rejects %q", h.eligible, h.families(), h.rejects())
	}
	cfg := simConfig()
	cfg.Scoring.UnmatchedMinFamilies = 2
	a = simulate(cfg, blocks(0))
	if h := a.hyp(t, 1, 1); h.eligible || h.rejects() != RejectTooFewSignals {
		t.Errorf("one signal at minimum 2: eligible %v rejects %q", h.eligible, h.rejects())
	}
	// A standard length is exempt.
	a = simulate(cfg, []simBlock{prog(60 * sec), {us: d(30 * sec), logoKnown: 1}, prog(60 * sec)})
	if h := a.hyp(t, 1, 1); strings.Contains(h.rejects(), RejectTooFewSignals) {
		t.Errorf("standard length rejected for too few signals")
	}
}

// scoring.combined_length_min_families: a standard length reached only by
// adding blocks up needs that many independent ad signals.
func TestCombinedLengthMinFamilies(t *testing.T) {
	blocks := []simBlock{prog(60 * sec), plain(18 * sec), plain(12 * sec), prog(60 * sec)}
	a := simulate(nil, blocks)
	h := a.hyp(t, 1, 2)
	if h.nf == 0 || h.factors[0].rule != ruleCombined || !h.eligible {
		t.Fatalf("default: combined %v eligible %v rejects %q", h.nf > 0 && h.factors[0].rule == ruleCombined, h.eligible, h.rejects())
	}
	cfg := simConfig()
	cfg.Scoring.CombinedMinFamilies = 1
	a = simulate(cfg, blocks)
	if h := a.hyp(t, 1, 2); h.eligible || !strings.Contains(h.rejects(), RejectCombinedTooFewSignals) {
		t.Errorf("boundaries only at minimum 1: eligible %v rejects %q", h.eligible, h.rejects())
	}
	// With logo absence as a signal it stands.
	logo := []simBlock{prog(60 * sec), {us: d(18 * sec), logoKnown: 1}, {us: d(12 * sec), logoKnown: 1}, prog(60 * sec)}
	a = simulate(cfg, logo)
	if h := a.hyp(t, 1, 2); strings.Contains(h.rejects(), RejectCombinedTooFewSignals) {
		t.Errorf("logo absence did not count: rejects %q", h.rejects())
	}
}

// scoring.combined_length_program_edges: a standard length reached by adding
// a block that carries only program evidence (here captions in the program's
// style) to an ad is refused.
func TestCombinedLengthProgramEdges(t *testing.T) {
	rollUp := func(dur time.Duration) simBlock {
		b := prog(dur)
		b.cap = CaptionRollUp
		return b
	}
	blocks := []simBlock{
		rollUp(120 * sec),
		{us: d(22 * sec), cap: CaptionPopOn},
		{us: d(8 * sec), cap: CaptionRollUp, plainStart: true},
		rollUp(120 * sec),
	}
	a := simulate(nil, blocks)
	if !a.blocks[2].progOnly || a.blocks[1].progOnly {
		t.Fatalf("progOnly: edge %v ad %v", a.blocks[2].progOnly, a.blocks[1].progOnly)
	}
	if h := a.hyp(t, 1, 2); h.eligible || !strings.Contains(h.rejects(), RejectCombinedProgramEdge) {
		t.Errorf("default: program edge eligible %v rejects %q", h.eligible, h.rejects())
	}
	cfg := simConfig()
	cfg.Scoring.CombinedProgramEdges = false
	a = simulate(cfg, blocks)
	if h := a.hyp(t, 1, 2); h.nf == 0 || h.factors[0].rule != ruleCombined || strings.Contains(h.rejects(), RejectCombinedProgramEdge) {
		t.Fatalf("off: combined %v rejects %q", h.nf > 0 && h.factors[0].rule == ruleCombined, h.rejects())
	}
	cfg.Scoring.CombinedProgramEdges = true
	// Two ad blocks summed to a standard length still stand.
	ads := []simBlock{rollUp(120 * sec), {us: d(18 * sec), cap: CaptionPopOn}, {us: d(12 * sec), cap: CaptionPopOn, plainStart: true}, rollUp(120 * sec)}
	a = simulate(cfg, ads)
	if h := a.hyp(t, 1, 2); strings.Contains(h.rejects(), RejectCombinedProgramEdge) {
		t.Errorf("ad blocks refused: rejects %q", h.rejects())
	}
}
