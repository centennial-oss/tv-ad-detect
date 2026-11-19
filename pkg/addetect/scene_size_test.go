// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"math"
	"testing"
)

// AT-05 (scene changes, SCENE-01..02, SCORE-06) and AT-06 (coded size
// changes, SIZE-01..02).

// AT-05 / SCENE-01: hard cuts are detected at the first frame of the new
// shot with their distance and brightness change.
func TestSceneHardCuts(t *testing.T) {
	s := newScenario(blk{D: 20 * sec, HardCuts: true, Shot: 2 * sec})
	r := s.run(t, nil)
	ev := r.Diagnostics.Events.Scene
	if len(ev) != 9 {
		t.Fatalf("scene events = %+v", ev)
	}
	for i, e := range ev {
		want := int64(i+1) * 2_000_000
		if e.TimeUS != want || e.Kind != "cut" || e.Distance < 0.9 || e.Distance > 1 || math.Abs(math.Abs(e.BrightnessDelta)-60) > 1 {
			t.Errorf("event %d = %+v, want a cut at %d", i, e, want)
		}
	}
	if (ev[0].BrightnessDelta > 0) == (ev[1].BrightnessDelta > 0) {
		t.Errorf("brightness deltas should alternate: %v %v", ev[0].BrightnessDelta, ev[1].BrightnessDelta)
	}
	if d := detector(t, r, SignalScene); d.Status != StatusUsable || d.Events != 9 || d.Boundary {
		t.Errorf("detector = %+v", d)
	}
	// SCENE-02: count and rate per second over usable coverage.
	b := blockSpan(t, r, 0, 20*sec)
	if *b.SceneChanges != 9 || math.Abs(*b.SceneRate-0.45) > 1e-9 {
		t.Errorf("block scene changes %d rate %v", *b.SceneChanges, *b.SceneRate)
	}
}

// AT-05 / SCENE-01: events around the same transition are suppressed
// (scene.min_spacing).
func TestSceneDeduplication(t *testing.T) {
	a, b := picture(tW, tH, 90, 0, 0, false), picture(tW, tH, 150, 0, 0, false)
	white := flatPicture(tW, tH, 235)
	// A cut through a one-frame flash is two adjacent-frame changes.
	evs := frameSeq(join(rep(a, 20), rep(white, 1), rep(b, 20))...)
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	if ev := r.Diagnostics.Events.Scene; len(ev) != 1 || ev[0].TimeUS != 2_000_000 {
		t.Errorf("flash cut events = %+v", ev)
	}

	// A change on every other frame: at most one event per min_spacing.
	s := newScenario(blk{D: 10 * sec, HardCuts: true, Shot: 200 * ms})
	r = s.run(t, nil)
	ev := r.Diagnostics.Events.Scene
	if len(ev) != 25 {
		t.Fatalf("%d events, want 25 (one per 400 ms)", len(ev))
	}
	for i := 1; i < len(ev); i++ {
		if d := ev[i].TimeUS - ev[i-1].TimeUS; d < 400_000 {
			t.Errorf("events %d and %d are %d us apart", i-1, i, d)
		}
	}

	// With suppression off every change is reported.
	cfg := testConfig()
	cfg.Scene.MinSpacing = 0
	r = s.run(t, cfg)
	if n := len(r.Diagnostics.Events.Scene); n != 49 {
		t.Errorf("min_spacing=0: %d events, want 49", n)
	}
}

// AT-05 / SCENE-01: a gradual change (dissolve) is distinguished from a hard
// cut.
func TestSceneGradual(t *testing.T) {
	var pics [][]byte
	pics = append(pics, rep(picture(tW, tH, 90, 0, 0, false), 20)...)
	for _, lv := range []int{102, 114, 126, 138} { // 12 luma per frame: distance 0.2 each
		pics = append(pics, picture(tW, tH, lv, 0, 0, false))
	}
	pics = append(pics, rep(picture(tW, tH, 150, 0, 0, false), 20)...)
	evs := frameSeq(pics...)
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	ev := r.Diagnostics.Events.Scene
	if len(ev) != 1 || ev[0].Kind != "gradual" || ev[0].Distance < 0.55 {
		t.Fatalf("events = %+v", ev)
	}
	if ev[0].TimeUS < 2_000_000 || ev[0].TimeUS > 2_500_000 {
		t.Errorf("gradual event at %d, outside the dissolve", ev[0].TimeUS)
	}

	cfg := testConfig()
	cfg.Scene.GradualWindow = 0
	r = analyze(t, &SliceSource{Events: evs}, cfg)
	if n := len(r.Diagnostics.Events.Scene); n != 0 {
		t.Errorf("gradual_window=0: %d events", n)
	}

	// The slow drift of ordinary program content is neither.
	r = newScenario(blk{D: 60 * sec}).run(t, nil)
	if n := len(r.Diagnostics.Events.Scene); n != 0 {
		t.Errorf("drifting content produced %d scene events", n)
	}
}

// AT-05 / SCENE-02, SCORE-06: scene-only editing never creates an ad. With
// scene boundaries off (the default) cuts are only measured; switching them
// on changes the candidates but not the evidence gate.
func TestSceneOnlyNeverCreatesAnAd(t *testing.T) {
	// A cut exactly every 15 s: every shot has a standard ad duration.
	s := newScenario(blk{D: 120 * sec, HardCuts: true, Shot: 15 * sec})
	r := s.run(t, nil)
	if n := len(r.Diagnostics.Events.Scene); n != 7 {
		t.Fatalf("%d scene events", n)
	}
	wantBoundaries(t, r, 0, 120)
	wantTimeline(t, r, "U0-120")

	cfg := testConfig()
	cfg.Boundaries.FromScene = true
	r = s.run(t, cfg)
	wantBoundaries(t, r, 0, 15, 30, 45, 60, 75, 90, 105, 120)
	for _, b := range r.Diagnostics.Boundaries[1:8] {
		if len(b.Causes) != 1 || !hasCause(&b, SignalScene, "cut", "boundary") || len(b.Evidence) != 0 || b.UncertaintyUS != 0 {
			t.Errorf("scene boundary = %+v", b)
		}
	}
	if d := detector(t, r, SignalScene); !d.Boundary {
		t.Errorf("scene not reported as a boundary source")
	}
	// Every 15 s block reaches the score threshold on duration alone and is
	// still rejected: a scene-only cut is not an evidence family.
	if len(r.AdBreaks) != 0 || len(r.AdUnits) != 0 {
		t.Fatalf("scene-only editing created ads: %s", timeline(r))
	}
	wantTimeline(t, r, "U0-120")
	h := hypothesis(t, r, 15*sec, 30*sec)
	if h.Score != 2 || h.Eligible || !hasString(h.Reasons, "no_evidence_family") || hasString(h.Reasons, "below_threshold") || len(h.EvidenceFamilies) != 0 {
		t.Errorf("hypothesis = %+v", h)
	}
	if seg := r.Segments[0]; !hasString(seg.Reasons, "rejected_ad_hypothesis") {
		t.Errorf("segment reasons = %v", seg.Reasons)
	}
}

// SCENE-01, TIME-03: temporal context is not carried across a coverage gap,
// so the first picture after a gap is not a "cut"; a disabled detector
// measures nothing.
func TestSceneResetAndDisable(t *testing.T) {
	s := newScenario(
		blk{D: 10 * sec, Level: 90},
		blk{D: 2 * sec, Skip: true},
		blk{D: 10 * sec, Level: 150},
	)
	r := s.run(t, nil)
	if n := len(r.Diagnostics.Events.Scene); n != 0 {
		t.Errorf("scene event across a coverage gap: %+v", r.Diagnostics.Events.Scene)
	}

	cfg := testConfig()
	cfg.Signals.Scene = false
	cfg.Boundaries.FromScene = true // has no effect without the detector
	r = newScenario(blk{D: 70 * sec, HardCuts: true}).run(t, cfg)
	if d := detector(t, r, SignalScene); d.Status != StatusDisabled || d.Boundary || d.Events != 0 {
		t.Errorf("detector = %+v", d)
	}
	if b := r.Diagnostics.Blocks[0]; b.SceneChanges != nil || b.SceneRate != nil {
		t.Errorf("disabled scene detector reports a rate")
	}
	if bl := r.Baselines.SceneRate; bl.Reliable || bl.Value != nil || bl.Reason != "signal_unavailable" {
		t.Errorf("scene baseline = %+v", bl)
	}
	wantBoundaries(t, r, 0, 70)
}

func TestHistDistance(t *testing.T) {
	a := []uint32{10, 0, 0, 0}
	b := []uint32{0, 10, 0, 0}
	c := []uint32{5, 5, 0, 0}
	if d := histDistance(a, a, 10, 10); d != 0 {
		t.Errorf("identical = %v", d)
	}
	if d := histDistance(a, b, 10, 10); d != 1 {
		t.Errorf("disjoint = %v", d)
	}
	if d := histDistance(a, c, 10, 10); d != 0.5 {
		t.Errorf("half = %v", d)
	}
	// Different sample counts (geometry differs) are normalized.
	if d := histDistance(a, []uint32{20, 20, 0, 0}, 10, 40); math.Abs(d-0.5) > 1e-12 {
		t.Errorf("normalized = %v", d)
	}
	if d := histDistance([]uint32{3, 0}, []uint32{0, 7}, 3, 7); math.Abs(d-1) > 1e-12 {
		t.Errorf("normalized disjoint = %v", d)
	}
}

// AT-06 / SIZE-01: a coded size change is exactly one boundary at the first
// frame with the new dimensions; the initial geometry is not an event;
// analysis continues.
func TestResolutionChange(t *testing.T) {
	s := newScenario(
		blk{D: 47 * sec},
		blk{D: 30 * sec, W: 128, H: 72, Sep: sepNone},
		blk{D: 53 * sec, Sep: sepNone},
	)
	r := s.run(t, nil)
	want := []ResolutionEvent{
		{TimeUS: 47_000_000, OldWidth: 64, OldHeight: 36, NewWidth: 128, NewHeight: 72},
		{TimeUS: 77_000_000, OldWidth: 128, OldHeight: 72, NewWidth: 64, NewHeight: 36},
	}
	if mustJSON(t, r.Diagnostics.Events.Resolution) != mustJSON(t, want) {
		t.Fatalf("resolution events = %+v", r.Diagnostics.Events.Resolution)
	}
	wantBoundaries(t, r, 0, 47, 77, 130)
	for _, at := range []int64{47_000_000, 77_000_000} {
		b := mustBoundary(t, r, us2d(at))
		// Same shape before and after: a pure coded-size change, clearly
		// distinguishable from an active-picture change (SIZE-02).
		if len(b.Causes) != 1 || !hasCause(b, SignalResolution, "coded_size_change", "boundary") || b.UncertaintyUS != 0 || b.Causes[0].CandidateUS != at {
			t.Errorf("boundary at %d = %+v", at, b)
		}
	}
	if d := detector(t, r, SignalResolution); d.Status != StatusUsable || d.Events != 2 || !d.Boundary {
		t.Errorf("detector = %+v", d)
	}
	if !r.Complete || r.Timing.Frames != 1300 || r.DurationUS != 130_000_000 {
		t.Errorf("analysis did not continue: frames %d duration %d", r.Timing.Frames, r.DurationUS)
	}
	for i, w := range []int{64, 128, 64} {
		if b := r.Diagnostics.Blocks[i]; b.CodedWidth != w {
			t.Errorf("block %d coded width %d, want %d", i, b.CodedWidth, w)
		}
	}
	// The reported track geometry is that of the first frame.
	if v := r.Tracks.Video; v.CodedWidth != 64 || v.CodedHeight != 36 || v.AnalysisWidth != 64 || v.AnalysisHeight != 36 || v.AnalysisScale != 1 {
		t.Errorf("video track = %+v", v)
	}

	// SIZE-02: a size change alone does not establish an ad, although the
	// 30 s block reaches the threshold on duration.
	if len(r.AdBreaks) != 0 {
		t.Fatalf("size change created an ad: %s", timeline(r))
	}
	h := hypothesis(t, r, 47*sec, 77*sec)
	if h.Score != 2 || h.Eligible || !hasString(h.Reasons, "no_evidence_family") {
		t.Errorf("hypothesis = %+v", h)
	}

	// SIZE-02: geometry-dependent state is invalidated: the logo is learned
	// and validated per geometry, the active picture is re-established.
	if len(r.Calibration.Logo) != 3 {
		t.Fatalf("logo calibration = %+v", r.Calibration.Logo)
	}
	for i, w := range [][2]int64{{0, 47_000_000}, {47_000_000, 77_000_000}, {77_000_000, 130_000_000}} {
		if c := r.Calibration.Logo[i]; c.StartUS != w[0] || c.EndUS != w[1] {
			t.Errorf("logo epoch %d = [%d,%d)", i, c.StartUS, c.EndUS)
		}
	}
	asp := r.Diagnostics.Events.Aspect
	if len(asp) != 3 || asp[0].EndUS != 47_000_000 || asp[1].StartUS != 47_000_000 || asp[1].EndUS != 77_000_000 || asp[2].StartUS != 77_000_000 {
		t.Fatalf("aspect spans = %+v", asp)
	}
	if asp[1].Width != 128 || asp[1].Height != 72 || asp[0].Width != 64 {
		t.Errorf("aspect spans = %+v", asp)
	}
	// The scene detector does not report the resize as a cut.
	if n := len(r.Diagnostics.Events.Scene); n != 0 {
		t.Errorf("scene events at the size change: %+v", r.Diagnostics.Events.Scene)
	}
}

// AT-06 / SIZE-01: the change is detected on the coded dimensions, before
// any analysis reduction, even when the analysis picture keeps its size.
func TestResolutionChangeCodedOnly(t *testing.T) {
	evs := frameSeq(rep(content, 200)...)
	for i := range evs {
		v := &evs[i].Video
		v.CodedWidth, v.CodedHeight, v.Scale = 1280, 720, 20
		if i >= 120 {
			v.CodedWidth, v.CodedHeight, v.Scale = 1920, 1080, 30
		}
	}
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	want := []ResolutionEvent{{TimeUS: 12_000_000, OldWidth: 1280, OldHeight: 720, NewWidth: 1920, NewHeight: 1080}}
	if mustJSON(t, r.Diagnostics.Events.Resolution) != mustJSON(t, want) {
		t.Fatalf("resolution events = %+v", r.Diagnostics.Events.Resolution)
	}
	wantBoundaries(t, r, 0, 12, 20)
	if v := r.Tracks.Video; v.CodedWidth != 1280 || v.AnalysisWidth != 64 || v.AnalysisScale != 20 {
		t.Errorf("video track = %+v", v)
	}
	// Active picture is reported in coded pixels of its own geometry.
	asp := r.Diagnostics.Events.Aspect
	if len(asp) != 2 || asp[0].Width != 1280 || asp[0].Height != 720 || asp[1].Width != 1920 || asp[1].Height != 1080 {
		t.Errorf("aspect spans = %+v", asp)
	}
	if len(r.Calibration.Logo) != 2 {
		t.Errorf("logo epochs = %d, want 2", len(r.Calibration.Logo))
	}
}

// SIZE-01, BOUND-01: the detector and its boundary role are separate
// switches.
func TestResolutionSwitches(t *testing.T) {
	s := newScenario(blk{D: 12 * sec}, blk{D: 8 * sec, W: 128, H: 72, Sep: sepNone})

	cfg := testConfig()
	cfg.Boundaries.FromResolution = false
	r := s.run(t, cfg)
	if len(r.Diagnostics.Events.Resolution) != 1 {
		t.Errorf("events = %+v", r.Diagnostics.Events.Resolution)
	}
	wantBoundaries(t, r, 0, 20)
	if d := detector(t, r, SignalResolution); d.Status != StatusUsable || d.Boundary {
		t.Errorf("detector = %+v", d)
	}

	cfg = testConfig()
	cfg.Signals.Resolution = false
	r = s.run(t, cfg)
	if len(r.Diagnostics.Events.Resolution) != 0 {
		t.Errorf("disabled detector produced events")
	}
	wantBoundaries(t, r, 0, 20)
	if d := detector(t, r, SignalResolution); d.Status != StatusDisabled {
		t.Errorf("detector = %+v", d)
	}
	// Geometry-dependent state is still reset.
	if len(r.Calibration.Logo) != 2 {
		t.Errorf("logo epochs = %d", len(r.Calibration.Logo))
	}
}

// BOUND-03: a resolution change fused with a nearby dark candidate keeps
// its own transition timestamp and both causes.
func TestResolutionChangeKeepsTimestampWhenFused(t *testing.T) {
	s := newScenario(blk{D: 47 * sec}, blk{D: 53 * sec})
	evs := s.events()
	n := 0
	for i := range evs {
		if evs[i].Kind != EventVideo {
			continue
		}
		// The black separator covers 46.9 s and 47.0 s; the new size starts
		// with the first picture after it, at 47.1 s.
		if n >= 471 {
			evs[i].Video.CodedWidth, evs[i].Video.CodedHeight, evs[i].Video.Scale = 128, 72, 2
		}
		n++
	}
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	wantBoundaries(t, r, 0, 47.1, 100)
	b := mustBoundary(t, r, us2d(47_100_000))
	if !hasCause(b, SignalResolution, "coded_size_change", "boundary") || !hasCause(b, SignalDarkUniform, "dark", "boundary") || !hasCause(b, SignalAudio, "silence", "corroboration") {
		t.Errorf("causes lost in fusion: %+v", b.Causes)
	}
	// The dark candidate itself was at 47.0 s; its cause records that.
	for _, c := range b.Causes {
		if c.Signal == SignalDarkUniform && c.CandidateUS != 47_000_000 {
			t.Errorf("dark cause candidate = %d", c.CandidateUS)
		}
	}
	if !hasString(b.Evidence, "dark_uniform_boundary") || !hasString(b.Evidence, "av_corroborated_boundary") {
		t.Errorf("evidence = %v", b.Evidence)
	}
	if b.UncertaintyUS != 100_000 {
		t.Errorf("uncertainty = %d", b.UncertaintyUS)
	}
}
