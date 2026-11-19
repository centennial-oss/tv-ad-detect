// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"testing"
	"time"
)

// AT-15, AT-16, AT-17: break assembly and boundary policy (BREAK-01..05).

func runReasons(a *analysis) []string {
	var out []string
	for i := range a.runs {
		if a.runs[i].accepted {
			out = append(out, "accepted")
		} else {
			out = append(out, reasonList(a.runs[i].reason)[0])
		}
	}
	return out
}

// AT-15 / BREAK-01: the individual ad-unit limits (4 s and 120 s) are
// inclusive; just outside fails.
func TestUnitDurationLimits(t *testing.T) {
	// Minimum: a logo-free 4 s block (score 2.0 from validated absence) next
	// to a 30 s ad.
	a := simulate(nil, []simBlock{prog(60 * sec), {us: 4_000_000, logoKnown: 1}, noLogo(30 * sec), prog(60 * sec)})
	if h := a.hyp(t, 1, 1); !h.eligible || h.score != 2 {
		t.Errorf("4.000000 s unit: rejects %q", h.rejects())
	}
	if a.unitSpans() != "1-1 2-2" || a.labels() != "PAAP" {
		t.Errorf("4 s unit: units %s labels %s", a.unitSpans(), a.labels())
	}
	// (The boundary between the short block and the 30 s block is not a
	// dark one: blocks of non-standard total combine only across boundaries
	// that are not dark/uniform.)
	a = simulate(nil, []simBlock{prog(60 * sec), {us: 3_999_999, logoKnown: 1}, {us: d(30 * sec), logoKnown: 1, plainStart: true}, prog(60 * sec)})
	if h := a.hyp(t, 1, 1); h.eligible || h.rejects() != "unit_duration_limits" {
		t.Errorf("3.999999 s unit: rejects %q", h.rejects())
	}
	// The short block is not discarded (BLOCK-02): it joins its neighbour in
	// a group hypothesis, which here loses to the strict 30 s unit alone, so
	// the block stays unknown as a rejected ad hypothesis.
	if h := a.hyp(t, 1, 2); !h.eligible {
		t.Errorf("group with the short block: rejects %q", h.rejects())
	}
	if a.unitSpans() != "2-2" || a.labels() != "PUAP" || reasonList(a.blocks[1].reasons)[0] != "rejected_ad_hypothesis" {
		t.Errorf("3.999999 s: units %s labels %s reasons %v", a.unitSpans(), a.labels(), reasonList(a.blocks[1].reasons))
	}

	// Maximum: 120 s is a unit, and so is one that measures over it by no
	// more than the loose tolerance (1 s); one microsecond beyond that is
	// not, and the long interval is not split at invented timestamps.
	a = simulate(nil, []simBlock{plain(130 * sec), {us: 120_000_000}, plain(140 * sec)})
	if h := a.hyp(t, 1, 1); !h.eligible || a.labels() != "UAU" {
		t.Errorf("120.000000 s unit: rejects %q labels %s", h.rejects(), a.labels())
	}
	a = simulate(nil, []simBlock{plain(130 * sec), {us: 120_100_000}, plain(140 * sec)})
	if h := a.hyp(t, 1, 1); !h.eligible || h.score != 2 || a.labels() != "UAU" {
		t.Errorf("120.100000 s unit: rejects %q score %v labels %s", h.rejects(), h.score, a.labels())
	}
	a = simulate(nil, []simBlock{plain(130 * sec), {us: 121_000_000}, plain(140 * sec)})
	if h := a.hyp(t, 1, 1); !h.eligible || h.score != 1.5 || a.labels() != "UAU" {
		t.Errorf("121.000000 s unit: rejects %q score %v labels %s", h.rejects(), h.score, a.labels())
	}
	a = simulate(nil, []simBlock{plain(130 * sec), {us: 121_000_001}, plain(140 * sec)})
	if h := a.hyp(t, 1, 1); h.eligible || !containsReject(h, rejDurationLimits) {
		t.Errorf("121.000001 s unit: rejects %q score %v", h.rejects(), h.score)
	}
	if a.labels() != "UUU" || len(a.blocks) != 3 {
		t.Errorf("121.000001 s: labels %s", a.labels())
	}

	// The limits are configurable.
	cfg := simConfig()
	cfg.Breaks.MinUnit, cfg.Breaks.MaxUnit = 2*sec, 30*sec
	a = simulate(cfg, []simBlock{prog(60 * sec), {us: 2_000_000, logoKnown: 1}, noLogo(30 * sec), prog(60 * sec)})
	if !a.hyp(t, 1, 1).eligible || !a.hyp(t, 2, 2).eligible {
		t.Errorf("configured limits 2..30 s: 2 s %q, 30 s %q", a.hyp(t, 1, 1).rejects(), a.hyp(t, 2, 2).rejects())
	}
	a = simulate(cfg, []simBlock{prog(60 * sec), noLogo(32 * sec), prog(60 * sec)})
	if h := a.hyp(t, 1, 1); h.eligible || !containsReject(h, rejDurationLimits) {
		t.Errorf("32 s against max_unit 30 s plus tolerance: rejects %q", h.rejects())
	}
}

// AT-15 / BREAK-01, BREAK-02: the complete-break limits (20 s and 600 s)
// are inclusive; just outside fails and the run becomes unknown with a
// constraint reason.
func TestBreakDurationLimits(t *testing.T) {
	// Minimum.
	a := simulate(nil, []simBlock{plain(130 * sec), plain(10 * sec), plain(10 * sec), plain(140 * sec)})
	if a.labels() != "UAAU" || len(a.runs) != 1 || !a.runs[0].accepted {
		t.Errorf("20.000000 s break: labels %s runs %v", a.labels(), runReasons(a))
	}
	a = simulate(nil, []simBlock{plain(130 * sec), plain(10 * sec), {us: 9_999_999}, plain(140 * sec)})
	if a.labels() != "UUUU" || len(a.runs) != 1 || runReasons(a)[0] != "break_too_short" {
		t.Errorf("19.999999 s break: labels %s runs %v", a.labels(), runReasons(a))
	}
	if a.unitSpans() != "1-1 2-2" {
		t.Errorf("19.999999 s break: units %s (raw hypotheses must be retained)", a.unitSpans())
	}
	for _, i := range []int{1, 2} {
		if got := reasonList(a.blocks[i].reasons); len(got) != 1 || got[0] != "break_too_short" {
			t.Errorf("block %d reasons = %v", i, got)
		}
	}

	// Maximum: five 120 s units are exactly 600 s.
	u := plain(120 * sec)
	a = simulate(nil, []simBlock{plain(130 * sec), u, u, u, u, u, plain(140 * sec)})
	if a.labels() != "UAAAAAU" || len(a.runs) != 1 || a.runs[0].end-a.runs[0].start != 600_000_000 {
		t.Errorf("600.000000 s break: labels %s runs %v", a.labels(), runReasons(a))
	}
	a = simulate(nil, []simBlock{plain(130 * sec), u, u, u, u, plain(90 * sec), {us: 30_000_001}, plain(140 * sec)})
	if a.labels() != "UUUUUUUU" || len(a.runs) != 1 || runReasons(a)[0] != "break_too_long" {
		t.Errorf("600.000001 s run: labels %s runs %v", a.labels(), runReasons(a))
	}
	// AT-16: the over-long run is not sliced into plausible breaks.
	if len(a.units) != 6 || a.runs[0].firstUnit != 0 || a.runs[0].lastUnit != 5 {
		t.Errorf("over-long run: %d units, run %+v", len(a.units), a.runs[0])
	}

	// The limits are configurable.
	cfg := simConfig()
	cfg.Breaks.MinBreak, cfg.Breaks.MaxBreak = 10*sec, 45*sec
	a = simulate(cfg, []simBlock{plain(130 * sec), plain(10 * sec), plain(140 * sec)})
	if a.labels() != "UAU" {
		t.Errorf("min_break 10 s: labels %s", a.labels())
	}
	a = simulate(cfg, []simBlock{plain(130 * sec), plain(30 * sec), plain(20 * sec), plain(140 * sec)})
	if a.labels() != "UUUU" || runReasons(a)[0] != "break_too_long" {
		t.Errorf("max_break 45 s: labels %s runs %v", a.labels(), runReasons(a))
	}
}

// AT-15 / BREAK-01: a 10 s interior candidate, however well supported,
// cannot become a complete default break; the result says why.
func TestTenSecondInteriorCandidateIsNotABreak(t *testing.T) {
	s := newScenario(blk{D: 70 * sec, Logo: true}, blk{D: 10 * sec}, blk{D: 83 * sec, Logo: true})
	cfg := testConfig()
	cfg.Logo.MaxOccupancy = 0.97 // the logo is absent for only 10 of 163 s
	r := s.run(t, cfg)
	if len(r.AdBreaks) != 0 || len(r.AdUnits) != 0 || len(r.SkipIntervals) != 0 {
		t.Fatalf("10 s candidate accepted: %s", timeline(r))
	}
	wantTimeline(t, r, "P0-70 U70-80 P80-163")
	seg := segmentAt(t, r, 75*sec)
	if mustJSON(t, seg.Reasons) != `["break_too_short"]` || seg.BreakID != "" {
		t.Errorf("segment = %+v", seg)
	}
	// BREAK-02: the raw hypothesis is retained with its score and the
	// constraint that rejected it.
	h := hypothesis(t, r, 70*sec, 80*sec)
	if h.Score != 4 || !h.Eligible || !h.Selected || !hasString(h.Reasons, "break_constraint") {
		t.Errorf("hypothesis = %s", mustJSON(t, h))
	}
	rr := r.Diagnostics.RejectedRuns
	if len(rr) != 1 || rr[0].StartUS != 70_000_000 || rr[0].EndUS != 80_000_000 || rr[0].Reason != "break_too_short" || len(rr[0].HypothesisIDs) != 1 || rr[0].HypothesisIDs[0] != h.ID {
		t.Errorf("rejected runs = %s", mustJSON(t, rr))
	}
	// It is unknown, not program: the block keeps its ad evidence.
	if b := blockSpan(t, r, 70*sec, 80*sec); b.Label != LabelUnknown || b.Score != 4 {
		t.Errorf("block = %+v", b)
	}
}

// AT-15: the break limits end to end, on a real frame grid: two 10 s units
// are a complete 20 s break; 10 s + 9.9 s is not.
func TestBreakLimitsEndToEnd(t *testing.T) {
	r := newScenario(blk{D: 130 * sec}, blk{D: 10 * sec}, blk{D: 10 * sec}, blk{D: 140 * sec}).run(t, nil)
	wantTimeline(t, r, "U0-130 A130-150 U150-290")
	br := r.AdBreaks[0]
	if br.ID != "break0" || len(br.UnitIDs) != 2 || br.TruncatedStart || br.TruncatedEnd || len(br.BoundaryIDs) != 2 || len(br.BridgedGaps) != 0 {
		t.Errorf("break = %s", mustJSON(t, br))
	}
	if seg := segmentAt(t, r, 140*sec); seg.BreakID != "break0" || mustJSON(t, seg.UnitIDs) != mustJSON(t, br.UnitIDs) {
		t.Errorf("segment = %+v", seg)
	}
	if got := mustJSON(t, segmentAt(t, r, 140*sec).Reasons); got != `["standard_duration","dark_uniform_boundary","av_corroborated_boundary"]` {
		t.Errorf("ad reasons = %s", got)
	}

	r = newScenario(blk{D: 130 * sec}, blk{D: 10 * sec}, blk{D: 9900 * ms}, blk{D: 140 * sec}).run(t, nil)
	wantTimeline(t, r, "U0-289.9")
	if len(r.Diagnostics.RejectedRuns) != 1 || r.Diagnostics.RejectedRuns[0].Reason != "break_too_short" {
		t.Errorf("rejected runs = %s", mustJSON(t, r.Diagnostics.RejectedRuns))
	}

	// 600 s: five 120 s units between black frames.
	s := newScenario(blk{D: 130 * sec})
	for i := 0; i < 5; i++ {
		s.add(blk{D: 120 * sec})
	}
	s.add(blk{D: 140 * sec})
	r = s.run(t, nil)
	wantTimeline(t, r, "U0-130 A130-730 U730-870")
	if len(r.AdUnits) != 5 || len(r.AdBreaks) != 1 {
		t.Errorf("units %d breaks %d", len(r.AdUnits), len(r.AdBreaks))
	}
}

// AT-16 / BREAK-02: an excessive adjacent ad run is not split without
// observed separating evidence, is not reclassified as program, and keeps
// its raw hypotheses.
func TestExcessiveRunIsNotSplit(t *testing.T) {
	s := newScenario(
		blk{D: 50 * sec, Logo: true},
		blk{D: 30 * sec}, blk{D: 30 * sec}, blk{D: 30 * sec},
		blk{D: 90 * sec, Logo: true},
	)
	cfg := testConfig()
	cfg.Breaks.MaxBreak = 60 * sec
	r := s.run(t, cfg)
	if len(r.AdBreaks) != 0 || len(r.AdUnits) != 0 {
		t.Fatalf("over-long run produced breaks: %s", timeline(r))
	}
	wantTimeline(t, r, "P0-50 U50-140 P140-230")
	seg := segmentAt(t, r, 100*sec)
	if mustJSON(t, seg.Reasons) != `["break_too_long"]` || len(seg.BlockIDs) != 3 {
		t.Errorf("segment = %+v", seg)
	}
	rr := r.Diagnostics.RejectedRuns
	if len(rr) != 1 || rr[0].StartUS != 50_000_000 || rr[0].EndUS != 140_000_000 || rr[0].Reason != "break_too_long" || len(rr[0].HypothesisIDs) != 3 {
		t.Fatalf("rejected runs = %s", mustJSON(t, rr))
	}
	for _, at := range []time.Duration{60 * sec, 90 * sec, 120 * sec} {
		if b := blockAt(t, r, at); b.Score != 4 || b.Label != LabelUnknown {
			t.Errorf("block %s score %v label %s", b.ID, b.Score, b.Label)
		}
	}
	// With the default limit the same run is one accepted break.
	wantTimeline(t, s.run(t, nil), "P0-50 A50-140 P140-230")
}

// AT-16 / BREAK-03: gap bridging is on by default (ruling 2026-11-16); it
// merges ad runs across short unknown intervals and records the absorbed
// intervals. Switched off, the runs stay separate.
func TestGapBridging(t *testing.T) {
	blocks := []simBlock{plain(130 * sec), plain(30 * sec), plain(3 * sec), plain(30 * sec), plain(140 * sec)}
	off := simConfig()
	off.Breaks.BridgeGaps = false
	a := simulate(off, blocks)
	if a.labels() != "UAUAU" || len(a.runs) != 2 {
		t.Fatalf("bridging off: labels %s", a.labels())
	}
	if a = simulate(nil, blocks); a.labels() != "UAAAU" {
		t.Fatalf("default: labels %s (bridging must be on)", a.labels())
	}
	// The checks below use a 5 s limit and no logo-only program bridging.
	bridge := func() *Config {
		cfg := simConfig()
		cfg.Breaks.BridgeGaps = true
		cfg.Breaks.MaxBridgeGap = 5 * sec
		cfg.Breaks.BridgeLogoProgram = false
		return cfg
	}
	a = simulate(bridge(), blocks)
	if a.labels() != "UAAAU" || len(a.runs) != 1 {
		t.Fatalf("bridging: labels %s", a.labels())
	}
	run := a.runs[0]
	if len(run.bridged) != 1 || run.bridged[0] != (TimeRange{StartUS: 160_000_000, EndUS: 163_000_000, Reason: "bridged_gap"}) || run.end-run.start != 63_000_000 {
		t.Errorf("run = %+v", run)
	}
	if got := reasonList(a.blocks[2].reasons); len(got) != 1 || got[0] != "bridged_gap" {
		t.Errorf("bridged block reasons = %v", got)
	}
	// The absorbed block keeps its own (neutral) score.
	if a.blocks[2].score != 1 || a.blocks[2].unit != -1 {
		t.Errorf("bridged block score %v unit %d", a.blocks[2].score, a.blocks[2].unit)
	}

	// The maximum gap is inclusive.
	blocks[2].us = 5_000_000
	if a = simulate(bridge(), blocks); a.labels() != "UAAAU" {
		t.Errorf("5.000000 s gap: labels %s", a.labels())
	}
	blocks[2].us = 5_000_001
	if a = simulate(bridge(), blocks); a.labels() != "UAUAU" {
		t.Errorf("5.000001 s gap: labels %s", a.labels())
	}
	blocks[2].us = 3_000_000

	// The merged break must respect the break limits.
	cfg := bridge()
	cfg.Breaks.MaxBreak = 62 * sec
	if a = simulate(cfg, blocks); a.labels() != "UAUAU" || len(a.runs) != 2 {
		t.Errorf("merged break over max_break: labels %s", a.labels())
	}
	cfg.Breaks.MaxBreak = 63 * sec
	if a = simulate(cfg, blocks); a.labels() != "UAAAU" {
		t.Errorf("merged break at max_break: labels %s", a.labels())
	}

	// It refuses to bridge missing media coverage...
	gap := []simBlock{plain(130 * sec), plain(30 * sec), {us: 3_000_000, gap: true}, plain(30 * sec), plain(140 * sec)}
	if a = simulate(bridge(), gap); a.labels() != "UAUAU" || len(a.runs) != 2 {
		t.Errorf("coverage gap bridged: labels %s", a.labels())
	}
	// ...and positive program evidence.
	pg := []simBlock{prog(60 * sec), noLogo(30 * sec), prog(3 * sec), noLogo(30 * sec), prog(60 * sec)}
	if a = simulate(bridge(), pg); a.labels() != "PAPAP" || len(a.runs) != 2 {
		t.Errorf("program interval bridged: labels %s", a.labels())
	}
	// A gap whose only program evidence is the logo is bridged when
	// breaks.bridge_logo_program allows it (the default).
	logoOK := bridge()
	logoOK.Breaks.BridgeLogoProgram = true
	if a = simulate(logoOK, pg); a.labels() != "PAAAP" || len(a.runs) != 1 {
		t.Errorf("logo-only program gap: labels %s", a.labels())
	}
	// A gap made of several unknown blocks is one gap.
	multi := []simBlock{plain(130 * sec), plain(30 * sec), plain(2 * sec), {us: 1_000_000, plainStart: true}, plain(30 * sec), plain(140 * sec)}
	if a = simulate(bridge(), multi); a.labels() != "UAAAAU" {
		t.Errorf("two-block gap: labels %s", a.labels())
	}

	// A chain of gaps is bridged while the merged break stays within
	// max_break, which bounds the chain.
	chain := []simBlock{plain(130 * sec), plain(30 * sec), plain(3 * sec), plain(30 * sec), plain(3 * sec), plain(30 * sec), plain(3 * sec), plain(30 * sec), plain(140 * sec)}
	a = simulate(bridge(), chain)
	if a.labels() != "UAAAAAAAU" || len(a.runs) != 1 || len(a.runs[0].bridged) != 3 {
		t.Errorf("chain of gaps: labels %s", a.labels())
	}
	short := bridge()
	short.Breaks.MaxBreak = 70 * sec
	a = simulate(short, chain)
	if a.labels() != "UAAAUAAAU" || len(a.runs) != 2 {
		t.Errorf("chain bounded by max_break 70 s: labels %s", a.labels())
	}
}

// AT-16 / BREAK-03: bridging end to end. The canonical result reports the
// bridged interval and the segments still partition the timeline.
func TestGapBridgingEndToEnd(t *testing.T) {
	s := newScenario(blk{D: 130 * sec}, blk{D: 30 * sec}, blk{D: 3 * sec}, blk{D: 30 * sec}, blk{D: 140 * sec})
	off := testConfig()
	off.Breaks.BridgeGaps = false
	r := s.run(t, off)
	wantTimeline(t, r, "U0-130 A130-160 U160-163 A163-193 U193-333")
	if len(r.AdBreaks) != 2 {
		t.Fatalf("breaks = %s", mustJSON(t, r.AdBreaks))
	}

	cfg := testConfig()
	r = s.run(t, cfg)
	wantTimeline(t, r, "U0-130 A130-193 U193-333")
	if len(r.AdBreaks) != 1 || len(r.AdUnits) != 2 {
		t.Fatalf("breaks = %s", mustJSON(t, r.AdBreaks))
	}
	br := r.AdBreaks[0]
	if mustJSON(t, br.BridgedGaps) != `[{"start_us":160000000,"end_us":163000000,"reason":"bridged_gap"}]` || len(br.UnitIDs) != 2 {
		t.Errorf("break = %s", mustJSON(t, br))
	}
	seg := segmentAt(t, r, 161*sec)
	if seg.Label != LabelAdvertisement || !hasString(seg.Reasons, "bridged_gap") || len(seg.BlockIDs) != 3 {
		t.Errorf("segment = %+v", seg)
	}
	// The absorbed block keeps its identity and neutral score in the
	// diagnostics; a merged break does not imply one uniform score (OUT-02).
	if b := blockSpan(t, r, 160*sec, 163*sec); b.Score != 1 || b.Label != LabelAdvertisement || mustJSON(t, b.Reasons) != `["bridged_gap"]` {
		t.Errorf("bridged block = %+v", b)
	}

	// A timestamp gap between two ads is never bridged. (The ads rest on
	// validated logo absence; a coverage edge is not boundary evidence.)
	g := newScenario(blk{D: 70 * sec, Logo: true}, blk{D: 30 * sec}, blk{D: 3 * sec, Skip: true}, blk{D: 30 * sec}, blk{D: 67 * sec, Logo: true})
	r = g.run(t, cfg)
	wantTimeline(t, r, "P0-70 A70-100 U100-103 A103-133 P133-200")
	if seg := segmentAt(t, r, 101*sec); mustJSON(t, seg.Reasons) != `["missing_coverage"]` || seg.Coverage != 0 {
		t.Errorf("gap segment = %+v", seg)
	}
}

// AT-17 / BREAK-04: with the truncated-edge mode off, edge candidates follow
// the normal rules; the mode (on by default, ruling 2026-11-16) relaxes the
// minimums only at the actual recording edges and reports the truncation.
func TestTruncatedEdges(t *testing.T) {
	blocks := []simBlock{noLogo(12 * sec), prog(60 * sec), noLogo(12 * sec), prog(60 * sec), noLogo(12 * sec)}
	strictEdges := simConfig()
	strictEdges.Breaks.AllowTruncatedEdges = false
	if got := simulate(nil, blocks).labels(); got != "APUPA" {
		t.Errorf("default (truncated edges on): labels %s", got)
	}
	a := simulate(strictEdges, blocks)
	if a.labels() != "UPUPU" {
		t.Fatalf("default: labels %s", a.labels())
	}
	for _, r := range runReasons(a) {
		if r != "break_too_short" {
			t.Errorf("default run reasons = %v", runReasons(a))
		}
	}
	allow := func() *Config {
		cfg := simConfig()
		cfg.Breaks.AllowTruncatedEdges = true
		return cfg
	}
	a = simulate(allow(), blocks)
	if a.labels() != "APUPA" || len(a.runs) != 3 {
		t.Fatalf("truncated edges: labels %s", a.labels())
	}
	if r := a.runs[0]; !r.accepted || !r.truncStart || r.truncEnd {
		t.Errorf("start run = %+v", r)
	}
	if r := a.runs[1]; r.accepted || reasonList(r.reason)[0] != "break_too_short" {
		t.Errorf("interior run = %+v (the relaxation must not apply in the interior)", r)
	}
	if r := a.runs[2]; !r.accepted || r.truncStart || !r.truncEnd {
		t.Errorf("end run = %+v", r)
	}

	// The minimum unit duration is relaxed at the edges only.
	short := []simBlock{noLogo(2 * sec), prog(60 * sec), noLogo(2 * sec), prog(60 * sec), noLogo(2 * sec)}
	a = simulate(strictEdges, short)
	if a.labels() != "UPUPU" || len(a.units) != 0 {
		t.Errorf("2 s edge blocks, strict edges: labels %s units %s", a.labels(), a.unitSpans())
	}
	a = simulate(allow(), short)
	if a.labels() != "APUPA" {
		t.Errorf("2 s edge blocks, truncated edges: labels %s", a.labels())
	}
	if h := a.hyp(t, 2, 2); h.eligible || h.rejects() != "unit_duration_limits" {
		t.Errorf("interior 2 s block: rejects %q", h.rejects())
	}

	// Being at an edge never establishes an ad: score and evidence are still
	// required.
	a = simulate(allow(), []simBlock{plain(12 * sec), plain(130 * sec), plain(12 * sec)})
	if a.labels() != "UUU" {
		t.Errorf("edge blocks without evidence: labels %s", a.labels())
	}
	a = simulate(allow(), []simBlock{plain(10 * sec), {us: d(130 * sec), plainStart: true}})
	if a.labels() != "UU" || a.hyp(t, 0, 0).rejects() != "no_evidence_family" {
		t.Errorf("edge block with duration only: labels %s rejects %q", a.labels(), a.hyp(t, 0, 0).rejects())
	}
	// A program block at the edge stays program.
	a = simulate(allow(), []simBlock{prog(12 * sec), noLogo(30 * sec), prog(60 * sec)})
	if a.labels() != "PAP" {
		t.Errorf("program at the edge: labels %s", a.labels())
	}

	// The maximum limits are retained at the edges.
	a = simulate(allow(), []simBlock{{us: 121_000_001, logoKnown: 1}, prog(200 * sec)})
	if a.labels() != "UP" {
		t.Errorf("over-long edge unit: labels %s", a.labels())
	}
	cfg := allow()
	cfg.Breaks.MaxBreak = 20 * sec
	a = simulate(cfg, []simBlock{noLogo(12 * sec), noLogo(12 * sec), prog(60 * sec)})
	if a.labels() != "UUP" || runReasons(a)[0] != "break_too_long" {
		t.Errorf("over-long edge break: labels %s runs %v", a.labels(), runReasons(a))
	}

	// No truncation is reported when the relaxation was not needed.
	a = simulate(allow(), []simBlock{noLogo(30 * sec), prog(60 * sec), noLogo(30 * sec)})
	if a.labels() != "APA" || a.runs[0].truncStart || a.runs[1].truncEnd {
		t.Errorf("complete edge breaks flagged as truncated: %+v", a.runs)
	}
	// A whole-recording ad run touches both edges.
	a = simulate(allow(), []simBlock{noLogo(7 * sec), prog(200 * sec)}, func(a *analysis) {})
	if a.labels() != "AP" || !a.runs[0].truncStart || a.runs[0].truncEnd {
		t.Errorf("runs = %+v", a.runs)
	}
}

// AT-17 / BREAK-04: truncated edges end to end, with a learned logo.
func TestTruncatedEdgesEndToEnd(t *testing.T) {
	s := newScenario(
		blk{D: 12 * sec},
		blk{D: 70 * sec, Logo: true},
		blk{D: 12 * sec},
		blk{D: 70 * sec, Logo: true},
		blk{D: 12 * sec},
	)
	strictEdges := testConfig()
	strictEdges.Breaks.AllowTruncatedEdges = false
	r := s.run(t, strictEdges)
	wantTimeline(t, r, "U0-12 P12-82 U82-94 P94-164 U164-176")
	if len(r.AdBreaks) != 0 {
		t.Fatalf("strict edge policy accepted a short break")
	}

	cfg := testConfig()
	cfg.Breaks.AllowTruncatedEdges = true
	r = s.run(t, cfg)
	wantTimeline(t, r, "A0-12 P12-82 U82-94 P94-164 A164-176")
	if len(r.AdBreaks) != 2 {
		t.Fatalf("breaks = %s", mustJSON(t, r.AdBreaks))
	}
	if b := r.AdBreaks[0]; !b.TruncatedStart || b.TruncatedEnd || b.StartUS != 0 {
		t.Errorf("first break = %s", mustJSON(t, b))
	}
	if b := r.AdBreaks[1]; b.TruncatedStart || !b.TruncatedEnd || b.EndUS != r.DurationUS {
		t.Errorf("last break = %s", mustJSON(t, b))
	}
	// The truncated breaks still rest on score and evidence.
	for _, u := range r.AdUnits {
		if u.Score != 2 || !hasString(u.EvidenceFamilies, "logo_absent") {
			t.Errorf("unit = %s", mustJSON(t, u))
		}
	}
}

// AT-17 / BREAK-05: skip padding applies only to the derived skip list. It
// never changes canonical labels, is clamped to the timeline and never
// crosses positively identified program time.
func TestSkipPadding(t *testing.T) {
	pad := func(start, end time.Duration) *Config {
		cfg := testConfig()
		cfg.Outputs.SkipPaddingStart, cfg.Outputs.SkipPaddingEnd = start, end
		return cfg
	}
	canon := func(r *Result) string {
		return mustJSON(t, r.Segments) + mustJSON(t, r.AdBreaks) + mustJSON(t, r.AdUnits)
	}

	// Unknown time on both sides: the padding extends into it.
	u := newScenario(blk{D: 130 * sec}, blk{D: 30 * sec}, blk{D: 140 * sec})
	base := u.run(t, nil)
	wantTimeline(t, base, "U0-130 A130-160 U160-300")
	// Default: zero padding, the skip list mirrors the breaks.
	if got := mustJSON(t, base.SkipIntervals); got != `[{"start_us":130000000,"end_us":160000000,"break_ids":["break0"],"padding_start_us":0,"padding_end_us":0,"clamped":false,"merged":false}]` {
		t.Errorf("default skip list = %s", got)
	}
	r := u.run(t, pad(2*sec, 3*sec))
	if canon(r) != canon(base) {
		t.Errorf("padding changed the canonical result")
	}
	if got := mustJSON(t, r.SkipIntervals); got != `[{"start_us":128000000,"end_us":163000000,"break_ids":["break0"],"padding_start_us":2000000,"padding_end_us":3000000,"clamped":false,"merged":false}]` {
		t.Errorf("padded skip list = %s", got)
	}
	// Negative padding shrinks the skip interval (safety margin).
	r = u.run(t, pad(-1*sec, -2*sec))
	if got := mustJSON(t, r.SkipIntervals); got != `[{"start_us":131000000,"end_us":158000000,"break_ids":["break0"],"padding_start_us":-1000000,"padding_end_us":-2000000,"clamped":false,"merged":false}]` {
		t.Errorf("shrunk skip list = %s", got)
	}
	if canon(r) != canon(base) {
		t.Errorf("negative padding changed the canonical result")
	}
	// Padding that removes the whole interval drops it from the skip list
	// only.
	r = u.run(t, pad(-20*sec, -20*sec))
	if len(r.SkipIntervals) != 0 || len(r.AdBreaks) != 1 {
		t.Errorf("fully shrunk: skips %s breaks %d", mustJSON(t, r.SkipIntervals), len(r.AdBreaks))
	}

	// Program on both sides: the padding is clamped at the program edges.
	p := logoScenario()
	base = p.run(t, nil)
	wantTimeline(t, base, "P0-50 A50-80 P80-150")
	r = p.run(t, pad(5*sec, 5*sec))
	if canon(r) != canon(base) {
		t.Errorf("padding changed the canonical result")
	}
	if got := mustJSON(t, r.SkipIntervals); got != `[{"start_us":50000000,"end_us":80000000,"break_ids":["break0"],"padding_start_us":0,"padding_end_us":0,"clamped":true,"merged":false}]` {
		t.Errorf("clamped skip list = %s", got)
	}
	for _, s := range r.Segments {
		if s.Label != LabelProgram {
			continue
		}
		for _, k := range r.SkipIntervals {
			if k.StartUS < s.EndUS && k.EndUS > s.StartUS {
				t.Errorf("skip interval [%d,%d) consumes program time [%d,%d)", k.StartUS, k.EndUS, s.StartUS, s.EndUS)
			}
		}
	}

	// Program further away: the padding crosses unknown time and stops at
	// the program.
	m := newScenario(blk{D: 50 * sec, Logo: true}, blk{D: 3 * sec, Logo: true, Sep: sepNone, Level: 6}, blk{D: 30 * sec}, blk{D: 67 * sec, Logo: true})
	r = m.run(t, pad(10*sec, 0))
	if len(r.SkipIntervals) != 1 {
		t.Fatalf("skips = %s (timeline %s)", mustJSON(t, r.SkipIntervals), timeline(r))
	}
	for _, s := range r.Segments {
		if s.Label == LabelProgram && r.SkipIntervals[0].StartUS < s.EndUS && r.SkipIntervals[0].EndUS > s.StartUS {
			t.Errorf("skip interval %s consumes program segment [%d,%d)", mustJSON(t, r.SkipIntervals[0]), s.StartUS, s.EndUS)
		}
	}

	// Clamped to the available timeline at both recording edges.
	cfg := pad(5*sec, 5*sec)
	cfg.Breaks.AllowTruncatedEdges = true
	e := newScenario(blk{D: 30 * sec}, blk{D: 130 * sec}, blk{D: 30 * sec})
	r = e.run(t, cfg)
	wantTimeline(t, r, "A0-30 U30-160 A160-190")
	if got := mustJSON(t, r.SkipIntervals); got != `[{"start_us":0,"end_us":35000000,"break_ids":["break0"],"padding_start_us":0,"padding_end_us":5000000,"clamped":true,"merged":false},{"start_us":155000000,"end_us":190000000,"break_ids":["break1"],"padding_start_us":5000000,"padding_end_us":0,"clamped":true,"merged":false}]` {
		t.Errorf("edge skip list = %s", got)
	}

	// Padded intervals that meet are merged and reported as merged; the two
	// canonical breaks stay separate.
	g := newScenario(blk{D: 130 * sec}, blk{D: 30 * sec}, blk{D: 3 * sec}, blk{D: 30 * sec}, blk{D: 140 * sec})
	sep := pad(2*sec, 2*sec)
	sep.Breaks.BridgeGaps = false
	r = g.run(t, sep)
	if len(r.AdBreaks) != 2 {
		t.Fatalf("breaks = %d", len(r.AdBreaks))
	}
	if got := mustJSON(t, r.SkipIntervals); got != `[{"start_us":128000000,"end_us":195000000,"break_ids":["break0","break1"],"padding_start_us":2000000,"padding_end_us":2000000,"clamped":false,"merged":true}]` {
		t.Errorf("merged skip list = %s", got)
	}
}

// RESULT-02: a successful analysis with no accepted breaks returns empty
// lists, not an error.
func TestNoBreaksIsEmptyNotError(t *testing.T) {
	r := newScenario(blk{D: 130 * sec}).run(t, nil)
	if r.AdBreaks == nil || len(r.AdBreaks) != 0 || r.AdUnits == nil || r.SkipIntervals == nil || !r.Complete || r.Error != nil {
		t.Errorf("breaks %v units %v skips %v complete %v error %v", r.AdBreaks, r.AdUnits, r.SkipIntervals, r.Complete, r.Error)
	}
	js := mustJSON(t, r)
	for _, want := range []string{`"ad_breaks":[]`, `"ad_units":[]`, `"skip_intervals":[]`, `"rejected_runs":[]`} {
		if !contains(js, want) {
			t.Errorf("JSON lacks %s", want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// C4b: a short unknown block between an accepted break and a long stretch of
// program becomes program (context_sliver), only when breaks.sliver_max is
// set.
func TestSliverRule(t *testing.T) {
	blocks := []simBlock{prog(130 * sec), noLogo(30 * sec), plain(3 * sec), prog(130 * sec)}
	off := simConfig()
	off.Breaks.BridgeGaps = false
	if got := simulate(off, blocks).labels(); got != "PAUP" {
		t.Fatalf("default: labels %s", got)
	}
	on := simConfig()
	on.Breaks.BridgeGaps = false
	on.Breaks.SliverMax = 5 * sec
	a := simulate(on, blocks)
	if a.labels() != "PAPP" || reasonList(a.blocks[2].reasons)[0] != ReasonContextSliver {
		t.Errorf("sliver: labels %s reasons %v", a.labels(), reasonList(a.blocks[2].reasons))
	}
	// Too long, or next to short program: unchanged.
	on.Breaks.SliverMax = 2 * sec
	if got := simulate(on, blocks).labels(); got != "PAUP" {
		t.Errorf("3 s block, 2 s limit: labels %s", got)
	}
	on.Breaks.SliverMax = 5 * sec
	short := []simBlock{prog(130 * sec), noLogo(30 * sec), plain(3 * sec), prog(60 * sec), plain(130 * sec)}
	if got := simulate(on, short).labels(); got[2] != 'U' {
		t.Errorf("next to 60 s of program: labels %s", got)
	}
}

// C4c: an ad unit that directly follows program and ends in a long dark
// span is a fade to black, not an ad (dark_tail), only when
// breaks.dark_tail_max is set.
func TestDarkTailRule(t *testing.T) {
	blocks := []simBlock{prog(130 * sec), noLogo(30 * sec), prog(130 * sec)}
	tail := func(a *analysis) {
		// 6 s of dark ending at the unit's end (160 s).
		a.dark = []DarkEvent{{StartUS: 154_000_000, EndUS: 160_000_000, Kind: KindDark}}
	}
	if got := simulate(nil, blocks, tail).labels(); got != "PAP" {
		t.Fatalf("default: labels %s", got)
	}
	cfg := simConfig()
	cfg.Breaks.DarkTailMax = 5 * sec
	a := simulate(cfg, blocks, tail)
	if a.labels() != "PUP" || !hasString(reasonList(a.blocks[1].reasons), ReasonDarkTail) {
		t.Errorf("dark tail: labels %s reasons %v", a.labels(), reasonList(a.blocks[1].reasons))
	}
	cfg.Breaks.DarkTailMax = 7 * sec
	if got := simulate(cfg, blocks, tail).labels(); got != "PAP" {
		t.Errorf("6 s of dark against a 7 s limit: labels %s", got)
	}
}

// C7: declared margins relax the minimums for breaks inside them and can
// mark the neighbouring show's time as adjacent_program.
func TestRecordingMargins(t *testing.T) {
	blocks := []simBlock{plain(40 * sec), noLogo(12 * sec), prog(300 * sec)}
	strict := simConfig()
	strict.Breaks.AllowTruncatedEdges = false
	if got := simulate(strict, blocks).labels(); got != "UUP" {
		t.Fatalf("no margins: labels %s", got)
	}
	cfg := simConfig()
	cfg.Breaks.AllowTruncatedEdges = false
	cfg.Breaks.MarginStart = 60 * sec
	a := simulate(cfg, blocks)
	if a.labels() != "UAP" {
		t.Errorf("60 s lead-in: labels %s", a.labels())
	}
	if hasString(reasonList(a.blocks[0].reasons), ReasonAdjacentProgram) {
		t.Errorf("adjacent_program without mark_adjacent_program")
	}
	cfg.Breaks.MarkAdjacentProgram = true
	a = simulate(cfg, blocks)
	if a.labels() != "UAP" || !hasString(reasonList(a.blocks[0].reasons), ReasonAdjacentProgram) {
		t.Errorf("mark_adjacent_program: labels %s reasons %v", a.labels(), reasonList(a.blocks[0].reasons))
	}
}
