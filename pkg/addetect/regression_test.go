// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Regression tests for the fixes that followed the first review.

// SCORE-03, VIS-03, BREAK-04: a standard-duration modifier is credited only
// when both unit boundaries are located within
// scoring.duration_max_boundary_uncertainty (default 1 s). The midpoint of a
// long dark or silent span says little about where content changed.
func TestDurationCreditNeedsLocatedBoundaries(t *testing.T) {
	if DefaultConfig().Scoring.DurationMaxUncertainty != time.Second {
		t.Fatalf("default = %v", DefaultConfig().Scoring.DurationMaxUncertainty)
	}
	raised := func(dd time.Duration) *Config {
		cfg := testConfig()
		if err := cfg.Set("scoring.duration_max_boundary_uncertainty", dd.String()); err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	// 40 s of dark titles at the recording start: the candidate sits at 20 s
	// with 20 s of uncertainty. It is no longer a "strict 20 s" ad.
	titles := newScenario(blk{D: 40 * sec, Pic: flatPicture(tW, tH, 4)}, blk{D: 77 * sec, Sep: sepNone})
	r := titles.run(t, nil)
	wantBoundaries(t, r, 0, 20, 117)
	wantTimeline(t, r, "U0-117")
	if b := mustBoundary(t, r, 20*sec); b.UncertaintyUS != 20_000_000 {
		t.Errorf("boundary uncertainty = %d", b.UncertaintyUS)
	}
	if b := blockSpan(t, r, 0, 20*sec); b.Score != 1 || len(b.Factors) != 0 {
		t.Errorf("first block: score %v factors %s", b.Score, rules(b.Factors))
	}
	if h := hypothesis(t, r, 0, 20*sec); h.Eligible || !hasString(h.Reasons, "below_threshold") {
		t.Errorf("hypothesis = %s", mustJSON(t, h))
	}
	// Raising the key restores the old behaviour (with C1 off: the titles'
	// audio continues, so their dark boundary is otherwise not evidence).
	cfgRaised := raised(30 * sec)
	cfgRaised.Boundaries.DarkRequiresQuiet = false
	r = titles.run(t, cfgRaised)
	wantTimeline(t, r, "A0-20 U20-117")
	if f := findFactor(blockSpan(t, r, 0, 20*sec).Factors, "strict_standard_duration"); f == nil {
		t.Errorf("raised limit: no duration factor")
	}

	// A 120 s all-black, silent recording: one candidate at 60 s.
	black := newScenario(blk{D: 120 * sec, Pic: flatPicture(tW, tH, 0), Silent: true})
	r = black.run(t, nil)
	wantBoundaries(t, r, 0, 60, 120)
	wantTimeline(t, r, "U0-120")
	for _, b := range r.Diagnostics.Blocks {
		if b.Score != 1 || len(b.Factors) != 0 {
			t.Errorf("block %s: score %v factors %s", b.ID, b.Score, rules(b.Factors))
		}
	}
	wantTimeline(t, black.run(t, raised(2*time.Minute)), "A0-120")

	// The midpoint of a long silence is no better located.
	r = newScenario(blk{D: 60 * sec, Silent: true}).run(t, nil)
	wantBoundaries(t, r, 0, 30, 60)
	if h := hypothesis(t, r, 0, 30*sec); h.Score != 1 || len(h.Factors) != 0 {
		t.Errorf("silent-track hypothesis = %s", mustJSON(t, h))
	}

	// Ordinary separators (0.6 s of black, 0.3 s of uncertainty) keep their
	// duration credit.
	sep := func(frames int) *scenario {
		return newScenario(blk{D: 130 * sec}, blk{D: 30 * sec, SepFrames: frames}, blk{D: 140 * sec, SepFrames: frames})
	}
	r = sep(6).run(t, nil)
	wantTimeline(t, r, "U0-130 A130-160 U160-300")
	if u := r.AdUnits[0]; u.Score != 2 || rules(u.Factors) != "strict_standard_duration" {
		t.Errorf("unit = %s", mustJSON(t, u))
	}
	if b := mustBoundary(t, r, 130*sec); b.UncertaintyUS != 300_000 {
		t.Errorf("separator uncertainty = %d", b.UncertaintyUS)
	}
	// The limit is inclusive: 2.0 s of black is exactly 1 s of uncertainty.
	wantTimeline(t, sep(20).run(t, nil), "U0-130 A130-160 U160-300")
	r = sep(22).run(t, nil)
	wantTimeline(t, r, "U0-300")
	if b := blockSpan(t, r, 130*sec, 160*sec); len(b.Factors) != 0 {
		t.Errorf("1.1 s uncertainty: factors %s", rules(b.Factors))
	}

	// White box, to the microsecond, for single and combined hypotheses.
	unc := func(i int, v int64) simOpt { return func(a *analysis) { a.bounds[i].unc = v } }
	blocks := []simBlock{plain(130 * sec), plain(30 * sec), plain(140 * sec)}
	if a := simulate(nil, blocks, unc(1, 1_000_000), unc(2, 1_000_000)); a.ruleNames(1) != "strict_standard_duration" || a.labels() != "UAU" {
		t.Errorf("uncertainty 1.000000 s: rules %q labels %s", a.ruleNames(1), a.labels())
	}
	for _, i := range []int{1, 2} {
		if a := simulate(nil, blocks, unc(i, 1_000_001)); a.ruleNames(1) != "" || a.labels() != "UUU" {
			t.Errorf("uncertainty 1.000001 s on boundary %d: rules %q labels %s", i, a.ruleNames(1), a.labels())
		}
	}
	comb := []simBlock{plain(130 * sec), plain(12 * sec), plain(18 * sec), plain(140 * sec)}
	if a := simulate(nil, comb); a.unitSpans() != "1-2" {
		t.Fatalf("combined units = %s", a.unitSpans())
	}
	if a := simulate(nil, comb, unc(3, 5_000_000)); len(a.units) != 0 || a.hyp(t, 1, 2).nf != 0 {
		t.Errorf("combined hypothesis with a poorly located end: units %s rules %q", a.unitSpans(), a.hyp(t, 1, 2).rules())
	}
	// An interior boundary of a group does not matter, only its two ends.
	if a := simulate(nil, comb, unc(2, 5_000_000)); a.unitSpans() != "1-2" {
		t.Errorf("poorly located interior boundary: units %s", a.unitSpans())
	}
	// Other evidence is unaffected: logo absence still scores.
	if a := simulate(nil, []simBlock{prog(60 * sec), noLogo(30 * sec), prog(60 * sec)}, unc(1, 9_000_000)); a.ruleNames(1) != "logo_absent" || a.labels() != "PAP" {
		t.Errorf("logo absence with a poorly located boundary: rules %q labels %s", a.ruleNames(1), a.labels())
	}
	// Zero means only exactly located boundaries qualify.
	cfg := simConfig()
	cfg.Scoring.DurationMaxUncertainty = 0
	if a := simulate(cfg, blocks, unc(1, 1)); a.ruleNames(1) != "" {
		t.Errorf("limit 0: rules %q", a.ruleNames(1))
	}
	if a := simulate(cfg, blocks); a.ruleNames(1) != "strict_standard_duration" {
		t.Errorf("limit 0, exact boundaries: rules %q", a.ruleNames(1))
	}
}

// SCORE-02: a neutral duration modifier disables its own rule and does not
// fall through to a weaker one.
func TestNeutralDurationFactorDoesNotFallThrough(t *testing.T) {
	run := func(cfg *Config, us int64) (string, float64) {
		a := simulate(cfg, []simBlock{plain(130 * sec), {us: us}, plain(140 * sec)})
		return a.ruleNames(1), a.blocks[1].score
	}
	cfg := simConfig()
	cfg.Scoring.StrictFactor = 1
	if got, score := run(cfg, 30_000_000); got != "" || score != 1 {
		t.Errorf("strict=1, exact 30 s: rules %q score %v (must not get the loose modifier)", got, score)
	}
	if got, score := run(cfg, 30_500_000); got != "" || score != 1 {
		t.Errorf("strict=1, 30.5 s: rules %q score %v", got, score)
	}
	// Outside strict tolerance the loose rule is still its own rule.
	if got, score := run(cfg, 30_800_000); got != "loose_standard_duration" || score != 1.5 {
		t.Errorf("strict=1, 30.8 s: rules %q score %v", got, score)
	}

	cfg = simConfig()
	cfg.Scoring.LooseFactor = 1
	if got, score := run(cfg, 30_800_000); got != "" || score != 1 {
		t.Errorf("loose=1, 30.8 s: rules %q score %v", got, score)
	}
	if got, score := run(cfg, 30_200_000); got != "strict_standard_duration" || score != 2 {
		t.Errorf("loose=1, 30.2 s: rules %q score %v", got, score)
	}

	cfg = simConfig()
	cfg.Scoring.CombinedFactor = 1
	a := simulate(cfg, []simBlock{plain(130 * sec), plain(12 * sec), plain(18 * sec), plain(140 * sec)})
	if h := a.hyp(t, 1, 2); h.nf != 0 || h.score != 1 || len(a.units) != 0 {
		t.Errorf("combined=1: rules %q score %v units %s", h.rules(), h.score, a.unitSpans())
	}
}

// BOUND-02, ASPECT-03: the picture bounds are unknown on dark frames, so an
// aspect change first observed just after a dark/uniform span joins that
// span's boundary: one boundary with both causes, no sliver block, and the
// unit keeps its dark-boundary evidence.
func TestAspectChangeJoinsPrecedingDarkSpan(t *testing.T) {
	s := newScenario(blk{D: 130 * sec}, blk{D: 30 * sec, LB: 4}, blk{D: 63 * sec})
	r := s.run(t, nil)
	wantBoundaries(t, r, 0, 130, 160, 223)
	for _, at := range []time.Duration{130 * sec, 160 * sec} {
		b := mustBoundary(t, r, at)
		if !hasCause(b, SignalDarkUniform, "dark", "boundary") || !hasCause(b, SignalAspect, "ratio_change", "boundary") || !hasCause(b, SignalAudio, "silence", "corroboration") {
			t.Errorf("boundary at %v causes = %s", at, mustJSON(t, b.Causes))
		}
		for _, c := range b.Causes {
			// The aspect cause keeps where the change was first observed and
			// reports the boundary it joined.
			if c.Signal == SignalAspect && (c.EventStartUS <= us(at) || c.EventStartUS > us(at)+300_000 || c.CandidateUS != us(at)) {
				t.Errorf("aspect cause = %+v", c)
			}
		}
		if mustJSON(t, b.Evidence) != `["dark_uniform_boundary","av_corroborated_boundary"]` {
			t.Errorf("evidence at %v = %v", at, b.Evidence)
		}
	}
	// No sliver blocks: the ad is exactly [130,160).
	wantTimeline(t, r, "U0-130 A130-160 U160-223")
	u := r.AdUnits[0]
	if len(u.BlockIDs) != 1 || u.Score != 4 || rules(u.Factors) != "strict_standard_duration,aspect_ratio_mismatch" {
		t.Errorf("unit = %s", mustJSON(t, u))
	}
	if !hasString(u.EvidenceFamilies, "dark_uniform_boundary") || !hasString(u.EvidenceFamilies, "aspect_ratio_mismatch") {
		t.Errorf("unit evidence = %v", u.EvidenceFamilies)
	}
	for _, b := range r.Diagnostics.Blocks {
		if b.EndUS-b.StartUS < 1_000_000 {
			t.Errorf("sliver block %s [%d,%d)", b.ID, b.StartUS, b.EndUS)
		}
	}

	// Uniform (non-dark) separators behave the same way.
	s = newScenario(blk{D: 130 * sec}, blk{D: 30 * sec, LB: 4, Sep: sepUniform}, blk{D: 63 * sec, Sep: sepUniform})
	r = s.run(t, nil)
	wantBoundaries(t, r, 0, 130, 160, 223)
	if b := mustBoundary(t, r, 130*sec); !hasCause(b, SignalDarkUniform, "uniform", "boundary") || !hasCause(b, SignalAspect, "ratio_change", "boundary") {
		t.Errorf("causes = %s", mustJSON(t, b.Causes))
	}

	// An aspect change with no dark span before it still gets its own exact
	// boundary at its first supporting observation.
	s = newScenario(blk{D: 130 * sec}, blk{D: 30 * sec, LB: 4, Sep: sepNone}, blk{D: 63 * sec, Sep: sepNone})
	r = s.run(t, nil)
	wantBoundaries(t, r, 0, 130, 160, 223)
	if b := mustBoundary(t, r, 130*sec); len(b.Causes) != 1 || !hasCause(b, SignalAspect, "ratio_change", "boundary") || b.UncertaintyUS != 200_000 {
		t.Errorf("standalone aspect boundary = %s", mustJSON(t, b))
	}

	// A dark span that ended a full second before the change is unrelated:
	// two boundaries.
	s = newScenario(blk{D: 129 * sec}, blk{D: 1 * sec}, blk{D: 30 * sec, LB: 4, Sep: sepNone}, blk{D: 63 * sec, Sep: sepNone})
	r = s.run(t, nil)
	wantBoundaries(t, r, 0, 129, 130, 160, 223)
	if b := mustBoundary(t, r, 129*sec); hasCause(b, SignalAspect, "", "") {
		t.Errorf("unrelated dark span took the aspect cause: %s", mustJSON(t, b.Causes))
	}
	if b := mustBoundary(t, r, 130*sec); len(b.Causes) != 1 || !hasCause(b, SignalAspect, "ratio_change", "boundary") {
		t.Errorf("aspect boundary = %s", mustJSON(t, b))
	}

	// With aspect boundaries off the dark boundary carries no aspect cause.
	cfg := testConfig()
	cfg.Boundaries.FromAspect = false
	r = newScenario(blk{D: 130 * sec}, blk{D: 30 * sec, LB: 4}, blk{D: 63 * sec}).run(t, cfg)
	if b := mustBoundary(t, r, 130*sec); hasCause(b, SignalAspect, "", "") {
		t.Errorf("from_aspect=false: causes %s", mustJSON(t, b.Causes))
	}
}

// shortLossEvents: 10 fps; the frames of [5.0 s, 5.3 s) are missing (less
// than input.timestamp_gap_threshold) unless covered.
func shortLossEvents(covered bool, gap GapObservation) []Event {
	var evs []Event
	for i := 0; i < 103; i++ {
		if !covered && i >= 50 && i < 53 {
			continue
		}
		evs = append(evs, videoFrame(int64(i)*100_000, 100_000, content))
	}
	return append(evs, Event{Kind: EventGap, Gap: gap})
}

// TIME-03, PACK-08: a loss the source declares is missing coverage even when
// it is shorter than the timestamp gap threshold, provided no frame was
// presented inside it.
func TestShortDeclaredGapIsMissingCoverage(t *testing.T) {
	loss := GapObservation{StartUS: 5_000_000, EndUS: 5_300_000, Reason: "resource_unreadable", Resource: 4}

	// Without the declaration the 300 ms hole is below the threshold and is
	// ordinary timing jitter.
	evs := shortLossEvents(false, loss)
	r := analyze(t, &SliceSource{Events: evs[:len(evs)-1]}, testConfig())
	if len(r.Coverage.Gaps) != 0 || !r.Complete {
		t.Fatalf("undeclared 300 ms hole: gaps %s complete %v", mustJSON(t, r.Coverage.Gaps), r.Complete)
	}

	r = analyze(t, &SliceSource{Events: evs}, testConfig())
	want := `[{"start_us":5000000,"end_us":5300000,"reason":"resource_unreadable"}]`
	if mustJSON(t, r.Coverage.Gaps) != want || mustJSON(t, r.Timing.Gaps) != want {
		t.Fatalf("gaps: coverage %s timing %s, want %s", mustJSON(t, r.Coverage.Gaps), mustJSON(t, r.Timing.Gaps), want)
	}
	if r.Complete {
		t.Errorf("a declared loss must make the result incomplete")
	}
	if r.DurationUS != 10_300_000 || r.Coverage.VideoUS != 10_000_000 {
		t.Errorf("duration %d video %d", r.DurationUS, r.Coverage.VideoUS)
	}
	wantBoundaries(t, r, 0, 5, 5.3, 10.3)
	g := blockSpan(t, r, 5*sec, 5300*ms)
	if !g.Missing || g.Coverage != 0 || g.Brightness != nil || g.Label != LabelUnknown || mustJSON(t, g.Reasons) != `["missing_coverage"]` {
		t.Errorf("gap block = %s", mustJSON(t, g))
	}
	if b := mustBoundary(t, r, 5*sec); !hasCause(b, "timeline", "coverage_gap_start", "boundary") {
		t.Errorf("gap start causes = %s", mustJSON(t, b.Causes))
	}
	if !hasWarning(r, "coverage_gaps") {
		t.Errorf("warnings = %s", mustJSON(t, r.Warnings))
	}
	// The frame before the loss is not stretched across it.
	if b := blockSpan(t, r, 0, 5*sec); b.Coverage != 1 || b.Missing {
		t.Errorf("block before the gap = %s", mustJSON(t, b))
	}

	// A presentation-declared gap (for example EXT-X-GAP) is unknown time
	// too, without making the result incomplete.
	decl := loss
	decl.Declared, decl.Reason = true, "declared_gap"
	r = analyze(t, &SliceSource{Events: shortLossEvents(false, decl)}, testConfig())
	if len(r.Coverage.Gaps) != 1 || r.Coverage.Gaps[0].Reason != "declared_gap" || !r.Complete {
		t.Errorf("declared gap: gaps %s complete %v", mustJSON(t, r.Coverage.Gaps), r.Complete)
	}

	// When frames do cover the interval, the declaration creates no gap
	// (the result is still marked incomplete: the source reported a loss).
	r = analyze(t, &SliceSource{Events: shortLossEvents(true, loss)}, testConfig())
	if len(r.Coverage.Gaps) != 0 || len(r.Timing.Gaps) != 0 || r.Coverage.VideoUS != r.DurationUS || len(r.Diagnostics.Blocks) != 1 {
		t.Errorf("covered declaration: gaps %s blocks %d", mustJSON(t, r.Coverage.Gaps), len(r.Diagnostics.Blocks))
	}
	if r.Complete {
		t.Errorf("covered declaration: complete = true")
	}

	// A declaration that overlaps a real timestamp gap only renames it.
	s := newScenario(blk{D: 5 * sec}, blk{D: 3 * sec, Skip: true}, blk{D: 5 * sec})
	e2 := append(s.events(), Event{Kind: EventGap, Gap: GapObservation{StartUS: 5_000_000, EndUS: 8_000_000, Reason: "resource_missing"}})
	r = analyze(t, &SliceSource{Events: e2}, testConfig())
	if got := mustJSON(t, r.Coverage.Gaps); got != `[{"start_us":5000000,"end_us":8000000,"reason":"resource_missing"}]` {
		t.Errorf("declared + timestamp gap: %s", got)
	}

	// The declaration is on the media timeline: a nonzero start normalizes.
	shifted := shortLossEvents(false, GapObservation{StartUS: 7_000_000, EndUS: 7_300_000, Reason: "resource_unreadable"})
	for i := range shifted {
		if shifted[i].Kind == EventVideo {
			shifted[i].Video.PTS += 2_000_000
		}
	}
	r = analyze(t, &SliceSource{Events: shifted}, testConfig())
	if mustJSON(t, r.Coverage.Gaps) != want {
		t.Errorf("shifted start: gaps %s", mustJSON(t, r.Coverage.Gaps))
	}
}

// TIME-01, TIME-03: at 2 fps or less without declared durations the first
// frame interval defines the frame spacing; it is not a gap, and neither is
// every later frame. A real later gap is still detected.
func TestLowFrameRateWithoutDurations(t *testing.T) {
	for _, step := range []int64{1_000_000, 666_667, 2_000_000} {
		var evs []Event
		for i := 0; i < 40; i++ {
			evs = append(evs, videoFrame(3_000_000+int64(i)*step, 0, content))
		}
		r := analyze(t, &SliceSource{Events: evs}, testConfig())
		if len(r.Coverage.Gaps) != 0 || len(r.Timing.Gaps) != 0 || r.Coverage.Fraction != 1 {
			t.Errorf("step %d: gaps %d fraction %v", step, len(r.Coverage.Gaps), r.Coverage.Fraction)
		}
		if r.DurationUS != 40*step || r.Timing.MedianFrameDurationUS != step || !r.Timing.FinalFrameDurationEstimated || r.Timing.Frames != 40 {
			t.Errorf("step %d: duration %d timing %+v", step, r.DurationUS, r.Timing)
		}
		if len(r.Diagnostics.Blocks) != 1 || r.Diagnostics.Blocks[0].Coverage != 1 {
			t.Errorf("step %d: blocks %s", step, mustJSON(t, boundaryTimes(r)))
		}
		if hasWarning(r, "coverage_gaps") {
			t.Errorf("step %d: coverage gap warning", step)
		}
	}

	// 1 fps, then 6 s without frames (several times the learned spacing).
	var evs []Event
	for i := 0; i < 20; i++ {
		evs = append(evs, videoFrame(int64(i)*1_000_000, 0, content))
	}
	for i := 0; i < 20; i++ {
		evs = append(evs, videoFrame(25_000_000+int64(i)*1_000_000, 0, content))
	}
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	if got := mustJSON(t, r.Coverage.Gaps); got != `[{"start_us":20000000,"end_us":25000000,"reason":"timestamp_gap"}]` {
		t.Errorf("real gap at 1 fps: %s", got)
	}
	wantBoundaries(t, r, 0, 20, 25, 45)
	if r.Coverage.VideoUS != 40_000_000 {
		t.Errorf("video coverage = %d", r.Coverage.VideoUS)
	}

	// With declared durations nothing changed: 1 fps is continuous.
	evs = evs[:0]
	for i := 0; i < 20; i++ {
		evs = append(evs, videoFrame(int64(i)*1_000_000, 1_000_000, content))
	}
	r = analyze(t, &SliceSource{Events: evs}, testConfig())
	if len(r.Coverage.Gaps) != 0 || r.DurationUS != 20_000_000 || r.Timing.FinalFrameDurationEstimated {
		t.Errorf("declared durations: gaps %d duration %d", len(r.Coverage.Gaps), r.DurationUS)
	}
}

// TIME-03: frames the decoder flags as corrupt are counted and reported.
func TestCorruptFramesAreReported(t *testing.T) {
	if WarnCorruptFrames != "corrupt_frames" {
		t.Fatalf("WarnCorruptFrames = %q", WarnCorruptFrames)
	}
	evs := frameSeq(rep(content, 50)...)
	for _, i := range []int{3, 4, 17, 40} {
		evs[i].Video.Corrupt = true
	}
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	if r.Timing.CorruptFrames != 4 || r.Timing.Frames != 50 || r.Timing.DecodeErrors != 0 {
		t.Errorf("timing = %+v", r.Timing)
	}
	var w *Warning
	for i := range r.Warnings {
		if r.Warnings[i].Code == WarnCorruptFrames {
			w = &r.Warnings[i]
		}
	}
	if w == nil || !strings.Contains(w.Message, "4 of 50") {
		t.Errorf("warnings = %s", mustJSON(t, r.Warnings))
	}
	if !strings.Contains(mustJSON(t, r.Timing), `"corrupt_frames":4`) {
		t.Errorf("timing JSON = %s", mustJSON(t, r.Timing))
	}
	// Corrupt frames are still measured; the analysis completes.
	if !r.Complete || r.DurationUS != 5_000_000 {
		t.Errorf("complete %v duration %d", r.Complete, r.DurationUS)
	}

	r = analyze(t, &SliceSource{Events: frameSeq(rep(content, 50)...)}, testConfig())
	if r.Timing.CorruptFrames != 0 || hasWarning(r, WarnCorruptFrames) || !strings.Contains(mustJSON(t, r.Timing), `"corrupt_frames":0`) {
		t.Errorf("clean input: timing %s warnings %s", mustJSON(t, r.Timing), mustJSON(t, r.Warnings))
	}
}

// CAP-01, MEDIA-04: caller-decoded intervals of style "unknown" carry no
// observation. Supplied alone they leave the captions detector unavailable.
func TestUnknownCaptionIntervalsAreIgnored(t *testing.T) {
	s := newScenario(blk{D: 130 * sec}, blk{D: 30 * sec}, blk{D: 63 * sec})
	unknown := Event{Kind: EventCaptionStyle, Caption: CaptionObservation{PTS: 0, EndPTS: 223_000_000, Style: CaptionUnknown}}
	r := analyze(t, &SliceSource{Events: append([]Event{unknown}, s.events()...)}, testConfig())
	d := detector(t, r, SignalCaptions)
	if d.Status != StatusUnavailable || d.Reason != "no_caption_data" || d.CoverageUS != nil || d.Coverage != nil || d.Events != 0 || d.Boundary {
		t.Errorf("detector = %s", mustJSON(t, d))
	}
	if len(r.Diagnostics.Events.Captions) != 0 {
		t.Errorf("caption spans = %s", captionSpans(r))
	}
	if bl := r.Baselines.CaptionStyle; bl.Reliable || bl.Reason != "signal_unavailable" {
		t.Errorf("caption baseline = %s", mustJSON(t, bl))
	}
	// The result equals that of a recording without any caption events.
	plain := s.run(t, nil)
	if mustJSON(t, r) != mustJSON(t, plain) {
		t.Errorf("unknown-only caption intervals changed the result")
	}

	// Mixed with recognized intervals, only the unknown ones are dropped:
	// their time stays unknown.
	evs := append([]Event{
		{Kind: EventCaptionStyle, Caption: CaptionObservation{PTS: 0, EndPTS: 130_000_000, Style: CaptionRollUp}},
		{Kind: EventCaptionStyle, Caption: CaptionObservation{PTS: 130_000_000, EndPTS: 160_000_000, Style: CaptionUnknown}},
		{Kind: EventCaptionStyle, Caption: CaptionObservation{PTS: 160_000_000, EndPTS: 223_000_000, Style: CaptionRollUp}},
	}, s.events()...)
	r = analyze(t, &SliceSource{Events: evs}, testConfig())
	if got := captionSpans(r); got != "roll_up[0,130) roll_up[160,223)" {
		t.Errorf("caption spans = %s", got)
	}
	if d := detector(t, r, SignalCaptions); d.Status != StatusUsable || *d.CoverageUS != 193_000_000 {
		t.Errorf("detector = %s", mustJSON(t, d))
	}
	if b := blockSpan(t, r, 130*sec, 160*sec); b.CaptionStyle != "unknown" || findFactor(b.Factors, "caption_style_mismatch") != nil {
		t.Errorf("unknown interval scored: style %s factors %s", b.CaptionStyle, rules(b.Factors))
	}
}

// stripes is dense texture: vertical stripes two pixels wide, so every
// pixel is a Sobel edge.
func stripes(w, h int) []byte {
	p := make([]byte, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p[y*w+x] = 40
			if x%4 >= 2 {
				p[y*w+x] = 220
			}
		}
	}
	return p
}

// textureScenario: program with the logo, a logo-free ad, 20 s of dense
// texture without the logo, program with the logo.
func textureScenario(w, h int) *scenario {
	return newScenario(
		blk{D: 50 * sec, Logo: true, W: w, H: h},
		blk{D: 30 * sec, W: w, H: h},
		blk{D: 20 * sec, Pic: stripes(w, h), W: w, H: h},
		blk{D: 50 * sec, Logo: true, W: w, H: h},
	)
}

// LOGO-03, LOGO-05: dense texture lights up the logo mask and its
// surroundings alike. When more than logo.max_background_density of the
// non-mask pixels in the logo box are edges the observation is unknown,
// neither present nor absent.
func TestLogoDenseBackgroundIsUnknown(t *testing.T) {
	if DefaultConfig().Logo.MaxBackgroundDensity != 0.85 {
		t.Fatalf("default = %v", DefaultConfig().Logo.MaxBackgroundDensity)
	}
	s := textureScenario(tW, tH)
	r := s.run(t, nil)
	if c := r.Calibration.Logo[0]; c.Status != StatusUsable {
		t.Fatalf("calibration = %s", mustJSON(t, c))
	}
	if got := logoStates(r); got != "present absent unknown present" {
		t.Fatalf("logo states = %s: %s", got, mustJSON(t, r.Diagnostics.Events.Logo))
	}
	iv := r.Diagnostics.Events.Logo[2]
	if iv.StartUS < 80_000_000 || iv.StartUS > 80_600_000 || iv.EndUS < 100_000_000 || iv.EndUS > 100_600_000 {
		t.Errorf("unknown interval = %s", mustJSON(t, iv))
	}
	// The texture does match the mask; it is the background that gives it
	// away.
	if iv.MeanMatch < 0.9 {
		t.Errorf("mean match over the texture = %v", iv.MeanMatch)
	}
	// Unknown is neutral: no logo factor, no program evidence, and the
	// interval is not counted as known logo time.
	tex := blockSpan(t, r, 80*sec, 100*sec)
	if findFactor(tex.Factors, "logo_present") != nil || findFactor(tex.Factors, "logo_absent") != nil || len(tex.ProgramEvidence) != 0 {
		t.Errorf("texture block: factors %s program evidence %v", rules(tex.Factors), tex.ProgramEvidence)
	}
	if *tex.LogoKnownUS > 1_000_000 {
		t.Errorf("texture block logo known = %d", *tex.LogoKnownUS)
	}
	if d := detector(t, r, SignalLogo); *d.CoverageUS > 131_000_000 || *d.CoverageUS < 129_000_000 {
		t.Errorf("logo coverage = %d", *d.CoverageUS)
	}

	// Setting the key to 1 disables the rule: the texture reads as the logo
	// (with the specificity check off too, which also catches texture).
	cfg := testConfig()
	if err := cfg.Set("logo.max_background_density", "1"); err != nil {
		t.Fatal(err)
	}
	cfg.Logo.MinSpecificity = 0
	r = s.run(t, cfg)
	if got := logoStates(r); got != "present absent present" {
		t.Errorf("rule disabled: logo states = %s", got)
	}
	if b := blockSpan(t, r, 80*sec, 100*sec); findFactor(b.Factors, "logo_present") == nil {
		t.Errorf("rule disabled: texture block factors %s", rules(b.Factors))
	}

	// The same rule applies to a supplied asset in its own geometry.
	asset := learnedAsset(t)
	cfg = testConfig()
	cfg.LogoAssetData = asset
	r = s.run(t, cfg)
	if c := r.Calibration.Logo[0]; c.Origin != "asset" || c.Status != StatusUsable {
		t.Fatalf("asset calibration = %s", mustJSON(t, c))
	}
	if got := logoStates(r); got != "present absent unknown present" {
		t.Errorf("asset, same geometry: logo states = %s", got)
	}

	// A scaled asset's mask pixels are only approximately placed, so the
	// rule is skipped for it: the texture reads as present.
	var big *LogoAsset
	if _, err := Analyze(context.Background(), textureScenario(128, 72).source(), testConfig(), &Options{OnLogoLearned: func(a *LogoAsset) { big = a }}); err != nil || big == nil {
		t.Fatalf("learning at 128x72: asset %v err %v", big, err)
	}
	cfg = testConfig()
	cfg.LogoAssetData = big
	r = s.run(t, cfg)
	if c := r.Calibration.Logo[0]; c.Origin != "asset" || c.Status != StatusUsable {
		t.Fatalf("scaled asset calibration = %s", mustJSON(t, c))
	}
	if got := logoStates(r); got != "present absent present" {
		t.Errorf("scaled asset: logo states = %s (the background rule must be skipped)", got)
	}

	// Ordinary program content around the logo is not "dense": the plain
	// logo scenario is unaffected by the rule.
	a, b := logoScenario().run(t, nil), logoScenario().run(t, cfg1(t, "logo.max_background_density", "1"))
	if mustJSON(t, a.Diagnostics.Events.Logo) != mustJSON(t, b.Diagnostics.Events.Logo) {
		t.Errorf("the rule changed an ordinary recording")
	}
}

func cfg1(t *testing.T, key, value string) *Config {
	t.Helper()
	cfg := testConfig()
	if err := cfg.Set(key, value); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The new keys are part of the configuration surface (CFG-04): present in
// the default YAML, range-checked, and part of the identity digest.
func TestNewConfigKeys(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDefaultYAML(&buf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"duration_max_boundary_uncertainty: 1s", "max_background_density: 0.85"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("default YAML lacks %q", want)
		}
	}
	base := DefaultConfig().identity(nil).Digest
	for key, v := range map[string]string{"scoring.duration_max_boundary_uncertainty": "3s", "logo.max_background_density": "0.5"} {
		c := DefaultConfig()
		if err := c.Set(key, v); err != nil {
			t.Fatalf("Set(%s): %v", key, err)
		}
		if c.identity(nil).Digest == base {
			t.Errorf("%s does not change the digest", key)
		}
	}
	wantInvalid(t, DefaultConfig().Set("scoring.duration_max_boundary_uncertainty", "-1s"), "range")
	wantInvalid(t, DefaultConfig().Set("scoring.duration_max_boundary_uncertainty", "5"), "unit")
	wantInvalid(t, DefaultConfig().Set("logo.max_background_density", "1.5"), "range")
	wantInvalid(t, DefaultConfig().Set("logo.max_background_density", "-0.1"), "range")
}

// OUT-01: Region is serialized with lowercase keys; timestamp resets are
// reported under "rebases".
func TestResultJSONKeyNames(t *testing.T) {
	js, err := json.Marshal(Region{X: 0.25, Y: 0.5, W: 0.125, H: 0.0625})
	if err != nil || string(js) != `{"x":0.25,"y":0.5,"w":0.125,"h":0.0625}` {
		t.Fatalf("Region JSON = %s (%v)", js, err)
	}
	var back Region
	if err := json.Unmarshal(js, &back); err != nil || back != (Region{0.25, 0.5, 0.125, 0.0625}) {
		t.Errorf("Region round trip = %+v (%v)", back, err)
	}
	r := logoScenario().run(t, nil)
	box := mustJSON(t, r.Calibration.Logo[0].Box)
	for _, k := range []string{`"x":`, `"y":`, `"w":`, `"h":`} {
		if !strings.Contains(box, k) {
			t.Errorf("logo box JSON %s lacks %s", box, k)
		}
	}
	if strings.ContainsAny(box, "XYWH") {
		t.Errorf("logo box JSON has upper-case keys: %s", box)
	}

	r = analyze(t, &SliceSource{Events: resetEvents()}, testConfig())
	if len(r.Timing.Rebases) != 1 || r.Timing.Rebases[0] != (Rebase{AtUS: 10_000_000, OffsetUS: 10_000_000}) {
		t.Fatalf("rebases = %+v", r.Timing.Rebases)
	}
	if tj := mustJSON(t, r.Timing); !strings.Contains(tj, `"rebases":[{"at_us":10000000,"offset_us":10000000}]`) || !strings.Contains(tj, `"timestamp_resets":1`) {
		t.Errorf("timing JSON = %s", tj)
	}
}

// CAP-02, SCORE-07: captions in the program's own style are program
// evidence. scoring.caption_match_factor (0.4) applies when the dominant
// style is recognized, equals the reliable baseline and coverage suffices.
func TestCaptionMatchFactor(t *testing.T) {
	if DefaultConfig().Scoring.CaptionMatchFactor != 0.4 || RuleCaptionStyleMatch != "caption_style_match" {
		t.Fatalf("default %v rule %q", DefaultConfig().Scoring.CaptionMatchFactor, RuleCaptionStyleMatch)
	}
	p := simBlock{us: d(130 * sec), cap: CaptionRollUp}
	mk := func(frac float64, style CaptionStyle) []simBlock {
		return []simBlock{p, {us: d(30 * sec), cap: style, capFrac: frac}, p}
	}
	a := simulate(nil, mk(1, CaptionRollUp))
	if a.ruleNames(1) != "strict_standard_duration,caption_style_match" || a.blocks[1].score != 2*0.4 {
		t.Fatalf("rules %q score %v", a.ruleNames(1), a.blocks[1].score)
	}
	f := a.factorOut(&a.blocks[1].factors[1])
	if f.Family != "captions" || f.Rule != RuleCaptionStyleMatch || f.Multiplier != 0.4 || *f.Coverage != 1 || f.Detail != "roll_up vs program roll_up" {
		t.Errorf("factor = %s", mustJSON(t, f))
	}
	// It lowers the score: not an evidence family, not an ad.
	if h := a.hyp(t, 1, 1); h.fam&famCaption != 0 || h.eligible || h.rejects() != "below_threshold" || len(a.units) != 0 {
		t.Errorf("hypothesis: families %q rejects %q", h.families(), h.rejects())
	}
	if hasAdRaisingFactor(&a.blocks[1]) {
		t.Errorf("caption match counted as ad-raising")
	}
	// The long program blocks get it too.
	if a.ruleNames(0) != "caption_style_match" || a.blocks[0].score != 0.4 {
		t.Errorf("program block: rules %q score %v", a.ruleNames(0), a.blocks[0].score)
	}
	// Coverage requirement (scoring.min_coverage_captions, 0.8).
	if a := simulate(nil, mk(0.8, CaptionRollUp)); a.ruleNames(1) != "strict_standard_duration,caption_style_match" {
		t.Errorf("coverage 0.8: rules %q", a.ruleNames(1))
	}
	if a := simulate(nil, mk(0.79, CaptionRollUp)); a.ruleNames(1) != "strict_standard_duration" {
		t.Errorf("coverage 0.79: rules %q", a.ruleNames(1))
	}
	// Mismatch and match are one family: never both; none/unknown get
	// neither.
	if a := simulate(nil, mk(1, CaptionPopOn)); a.ruleNames(1) != "strict_standard_duration,caption_style_mismatch" {
		t.Errorf("mismatch: rules %q", a.ruleNames(1))
	}
	if a := simulate(nil, mk(1, CaptionNone)); a.ruleNames(1) != "strict_standard_duration" {
		t.Errorf("observed absence: rules %q", a.ruleNames(1))
	}
	if a := simulate(nil, mk(1, CaptionUnknown)); a.ruleNames(1) != "strict_standard_duration" {
		t.Errorf("no captions: rules %q", a.ruleNames(1))
	}
	// 1 disables; other values are applied as configured.
	cfg := simConfig()
	cfg.Scoring.CaptionMatchFactor = 1
	if a := simulate(cfg, mk(1, CaptionRollUp)); a.ruleNames(1) != "strict_standard_duration" || a.ruleNames(0) != "" || a.labels() != "UAU" {
		t.Errorf("factor 1: rules %q labels %s", a.ruleNames(1), a.labels())
	}
	cfg.Scoring.CaptionMatchFactor = 0.25
	if a := simulate(cfg, mk(1, CaptionRollUp)); a.blocks[1].score != 0.5 {
		t.Errorf("factor 0.25: score %v", a.blocks[1].score)
	}
	// No reliable baseline, no factor: 65 s roll-up against 65 s pop-on.
	amb := simConfig()
	a = simulate(amb, []simBlock{{us: d(65 * sec), cap: CaptionRollUp}, {us: d(30 * sec), cap: CaptionRollUp}, {us: d(95 * sec), cap: CaptionPopOn}})
	if a.base.capOK || a.ruleNames(1) != "strict_standard_duration" || a.ruleNames(0) != "" {
		t.Errorf("ambiguous baseline: reliable %v rules %q / %q", a.base.capOK, a.ruleNames(0), a.ruleNames(1))
	}

	// Configuration surface.
	var buf bytes.Buffer
	WriteDefaultYAML(&buf)
	if !strings.Contains(buf.String(), "caption_match_factor: 0.4") {
		t.Errorf("default YAML lacks caption_match_factor")
	}
	c := DefaultConfig()
	if err := c.Set("scoring.caption_match_factor", "0.5"); err != nil || c.identity(nil).Digest == DefaultConfig().identity(nil).Digest {
		t.Errorf("caption_match_factor: set %v, digest unchanged", err)
	}
	wantInvalid(t, DefaultConfig().Set("scoring.caption_match_factor", "0"), "range")
}

// The reference-recording false positive in miniature: after an ad break,
// program resumes with the logo still off but with the program's captions.
// The break ends at the black separator and the logo-off, captioned stretch
// is program; a 5 s logo-free spot inside the break is still an ad unit.
func TestLogoOffCaptionedProgramAfterBreak(t *testing.T) {
	s := newScenario(
		blk{D: 70 * sec, Logo: true, Cap: CaptionRollUp},
		blk{D: 30 * sec},
		blk{D: 5 * sec},
		blk{D: 30 * sec},
		blk{D: 47 * sec, Cap: CaptionRollUp}, // program, logo not yet back
		blk{D: 80 * sec, Logo: true, Cap: CaptionRollUp},
	)
	r := s.run(t, nil)
	if c := r.Calibration.Logo[0]; c.Status != StatusUsable {
		t.Fatalf("logo calibration = %s", mustJSON(t, c))
	}
	if bl := r.Baselines.CaptionStyle; !bl.Reliable || bl.Style != "roll_up" || bl.Origin != "logo_present" {
		t.Fatalf("caption baseline = %s", mustJSON(t, bl))
	}
	wantBoundaries(t, r, 0, 70, 100, 105, 135, 182, 262)
	wantTimeline(t, r, "P0-70 A70-135 P135-262")
	if len(r.AdBreaks) != 1 || len(r.AdUnits) != 3 {
		t.Fatalf("breaks %d units %s", len(r.AdBreaks), mustJSON(t, r.AdUnits))
	}
	// The 5 s spot between two black separators is its own unit.
	spot := r.AdUnits[1]
	if spot.StartUS != 100_000_000 || spot.EndUS != 105_000_000 || spot.Score != 2 || rules(spot.Factors) != "logo_absent" || !hasString(spot.EvidenceFamilies, "logo_absent") || !hasString(spot.EvidenceFamilies, "dark_uniform_boundary") {
		t.Errorf("5 s spot = %s", mustJSON(t, spot))
	}
	// The stretch: logo absent (2.0) offset by matching captions (0.4).
	st := blockSpan(t, r, 135*sec, 182*sec)
	if st.Score != 0.8 || rules(st.Factors) != "logo_absent,caption_style_match" || st.Label != LabelProgram {
		t.Errorf("stretch = score %v factors %s label %s", st.Score, rules(st.Factors), st.Label)
	}
	if !hasString(st.ProgramEvidence, "stable_long_interval") || !hasString(st.Reasons, "stable_long_interval") {
		t.Errorf("stretch program evidence %v reasons %v", st.ProgramEvidence, st.Reasons)
	}
	// No hypothesis reaches across the separator into the stretch.
	for _, h := range r.Diagnostics.Hypotheses {
		if h.Eligible && h.EndUS > 135_000_000 && h.StartUS < 182_000_000 {
			t.Errorf("eligible hypothesis over the stretch: %s", mustJSON(t, h))
		}
	}
	if h := hypothesis(t, r, 105*sec, 182*sec); !hasString(h.Reasons, RejectSpansDarkBoundary) {
		t.Errorf("group across the separator: reasons %v", h.Reasons)
	}

	// With the match factor neutral the stretch is absorbed into the break
	// again (the old behaviour).
	r = s.run(t, cfg1(t, "scoring.caption_match_factor", "1"))
	wantTimeline(t, r, "P0-70 A70-182 P182-262")
	if b := blockSpan(t, r, 135*sec, 182*sec); b.Score != 2 || rules(b.Factors) != "logo_absent" {
		t.Errorf("match factor 1: stretch score %v factors %s", b.Score, rules(b.Factors))
	}
}

// SCORE-04: blocks are combined across a dark/uniform boundary only when
// their total is a standard length.
func TestGroupsDoNotSpanDarkBoundaries(t *testing.T) {
	if RejectSpansDarkBoundary != "spans_dark_boundary" {
		t.Fatalf("RejectSpansDarkBoundary = %q", RejectSpansDarkBoundary)
	}
	// 18 s + 17 s, logo absent, a dark boundary between them: two units.
	a := simulate(nil, []simBlock{prog(60 * sec), noLogo(18 * sec), noLogo(17 * sec), prog(60 * sec)})
	if a.unitSpans() != "1-1 2-2" || a.labels() != "PAAP" {
		t.Errorf("dark boundary: units %s labels %s", a.unitSpans(), a.labels())
	}
	if h := a.hyp(t, 1, 2); h.eligible || h.rejects() != "spans_dark_boundary" || h.score != 2 {
		t.Errorf("group across a dark boundary: rejects %q score %v", h.rejects(), h.score)
	}
	// Separated by a boundary without dark evidence (silence only, a scene
	// cut) they may combine: equal totals, fewer units.
	for name, mid := range map[string]simBlock{
		"plain": {us: d(17 * sec), logoKnown: 1, plainStart: true},
		"av":    {us: d(17 * sec), logoKnown: 1, avStart: true},
	} {
		a = simulate(nil, []simBlock{prog(60 * sec), noLogo(18 * sec), mid, prog(60 * sec)})
		if h := a.hyp(t, 1, 2); !h.eligible || a.unitSpans() != "1-2" {
			t.Errorf("%s boundary: units %s rejects %q", name, a.unitSpans(), h.rejects())
		}
	}
	// A standard total across the dark boundary does combine.
	a = simulate(nil, []simBlock{prog(60 * sec), noLogo(12 * sec), noLogo(18 * sec), prog(60 * sec)})
	if a.unitSpans() != "1-2" || a.units[0].h.rules() != "combined_standard_duration,logo_absent" || a.units[0].h.score != 3 {
		t.Errorf("standard total: units %s", a.unitSpans())
	}
	// ...but not when the duration credit is unavailable: neutral combined
	// factor, or a poorly located end boundary.
	cfg := simConfig()
	cfg.Scoring.CombinedFactor = 1
	a = simulate(cfg, []simBlock{prog(60 * sec), noLogo(12 * sec), noLogo(18 * sec), prog(60 * sec)})
	if a.unitSpans() != "1-1 2-2" || a.hyp(t, 1, 2).rejects() != "spans_dark_boundary" {
		t.Errorf("combined factor 1: units %s rejects %q", a.unitSpans(), a.hyp(t, 1, 2).rejects())
	}
	// Only interior boundaries count: the unit's own dark edges do not.
	a = simulate(nil, []simBlock{prog(60 * sec), noLogo(37 * sec), prog(60 * sec)})
	if a.unitSpans() != "1-1" {
		t.Errorf("single block between dark boundaries: units %s", a.unitSpans())
	}
	// Three blocks with one interior dark boundary among two.
	a = simulate(nil, []simBlock{prog(60 * sec), noLogo(11500 * ms), {us: d(13 * sec), logoKnown: 1, plainStart: true}, noLogo(22 * sec), prog(60 * sec)})
	if a.unitSpans() != "1-2 3-3" || a.hyp(t, 1, 3).rejects() != "spans_dark_boundary" {
		t.Errorf("three blocks: units %s rejects %q", a.unitSpans(), a.hyp(t, 1, 3).rejects())
	}
	// The reason is reported in the diagnostics end to end.
	r := newScenario(blk{D: 70 * sec, Logo: true}, blk{D: 18 * sec}, blk{D: 17 * sec}, blk{D: 73 * sec, Logo: true}).run(t, nil)
	wantTimeline(t, r, "P0-70 A70-105 P105-178")
	if len(r.AdUnits) != 2 {
		t.Errorf("units = %s", mustJSON(t, r.AdUnits))
	}
	if h := hypothesis(t, r, 70*sec, 105*sec); h.Eligible || !hasString(h.Reasons, "spans_dark_boundary") {
		t.Errorf("hypothesis = %s", mustJSON(t, h))
	}
}
