// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// AT-08: active-picture aspect ratio (ASPECT-01..03).

const fullRatio = float64(tW) / float64(tH)

func aspectSpans(r *Result) string {
	parts := make([]string, len(r.Diagnostics.Events.Aspect))
	for i, a := range r.Diagnostics.Events.Aspect {
		parts[i] = fmt.Sprintf("%.3f[%g,%g)", *a.Ratio, float64(a.StartUS)/1e6, float64(a.EndUS)/1e6)
	}
	return strings.Join(parts, " ")
}

func aspectBoundaries(r *Result) []float64 {
	var out []float64
	for _, b := range r.Diagnostics.Boundaries {
		if hasCause(&b, SignalAspect, "", "") {
			out = append(out, float64(b.TimeUS)/1e6)
		}
	}
	return out
}

// AT-08 / ASPECT-01, ASPECT-03: a sustained letterbox changes the active
// ratio and creates a boundary at the first supporting observation; a stable
// mismatch against the program baseline raises the ad score.
func TestAspectLetterboxChange(t *testing.T) {
	s := newScenario(
		blk{D: 130 * sec},
		blk{D: 30 * sec, LB: 4, Sep: sepNone},
		blk{D: 63 * sec, Sep: sepNone},
	)
	r := s.run(t, nil)
	if got, want := aspectSpans(r), "1.778[0,130) 2.286[130,160) 1.778[160,223)"; got != want {
		t.Fatalf("aspect spans = %s\n              want %s", got, want)
	}
	lb := r.Diagnostics.Events.Aspect[1]
	if lb.Left != 0 || lb.Top != 4 || lb.Width != 64 || lb.Height != 28 || *lb.Ratio != 64.0/28.0 {
		t.Errorf("letterbox span = %+v", lb)
	}
	if full := r.Diagnostics.Events.Aspect[0]; full.Left != 0 || full.Top != 0 || full.Width != 64 || full.Height != 36 || *full.Ratio != fullRatio {
		t.Errorf("full span = %+v", full)
	}
	// The change is dated at its first supporting observation, not at the
	// time persistence was confirmed (2 s later).
	wantBoundaries(t, r, 0, 130, 160, 223)
	for _, at := range []int64{130_000_000, 160_000_000} {
		b := mustBoundary(t, r, us2d(at))
		if len(b.Causes) != 1 || !hasCause(b, SignalAspect, "ratio_change", "boundary") || b.UncertaintyUS != 200_000 || len(b.Evidence) != 0 {
			t.Errorf("aspect boundary at %d = %+v", at, b)
		}
		// Distinguishable from a coded size change (SIZE-02).
		if hasCause(b, SignalResolution, "", "") {
			t.Errorf("aspect change reported as a resolution change")
		}
	}
	if len(r.Diagnostics.Events.Resolution) != 0 {
		t.Errorf("resolution events = %+v", r.Diagnostics.Events.Resolution)
	}
	d := detector(t, r, SignalAspect)
	if d.Status != StatusUsable || !d.Boundary || *d.CoverageUS != r.DurationUS {
		t.Errorf("detector = %+v", d)
	}

	bl := r.Baselines.AspectRatio
	if !bl.Reliable || bl.Origin != "long_stable_intervals" || *bl.Value != fullRatio || bl.SupportUS != 130_000_000 || *bl.Dominance != 1 {
		t.Fatalf("aspect baseline = %+v", bl)
	}
	ad := blockSpan(t, r, 130*sec, 160*sec)
	if *ad.ActiveRatio != 64.0/28.0 || *ad.AspectKnownUS != 30_000_000 {
		t.Errorf("ad block ratio %v known %v", *ad.ActiveRatio, *ad.AspectKnownUS)
	}
	f := findFactor(ad.Factors, "aspect_ratio_mismatch")
	if f == nil || f.Multiplier != 2 || f.Family != "aspect" || *f.Baseline != fullRatio || *f.Value != 1 || *f.Coverage != 1 {
		t.Fatalf("aspect factor = %+v (factors %s)", f, rules(ad.Factors))
	}
	if ad.Score != 4 {
		t.Errorf("ad score = %v", ad.Score)
	}
	// ASPECT-03: the mismatch is evidence, with the duration it is an ad.
	wantTimeline(t, r, "U0-130 A130-160 U160-223")
	u := r.AdUnits[0]
	if mustJSON(t, u.EvidenceFamilies) != `["aspect_ratio_mismatch"]` {
		t.Errorf("unit evidence = %v", u.EvidenceFamilies)
	}
	if got := mustJSON(t, segmentAt(t, r, 140*sec).Reasons); got != `["standard_duration","aspect_ratio_mismatch"]` {
		t.Errorf("ad reasons = %s", got)
	}
}

// AT-08 / ASPECT-01: pillarbox.
func TestAspectPillarbox(t *testing.T) {
	s := newScenario(
		blk{D: 130 * sec},
		blk{D: 30 * sec, PB: 8, Sep: sepNone},
		blk{D: 63 * sec, Sep: sepNone},
	)
	r := s.run(t, nil)
	if got, want := aspectSpans(r), "1.778[0,130) 1.333[130,160) 1.778[160,223)"; got != want {
		t.Fatalf("aspect spans = %s, want %s", got, want)
	}
	pb := r.Diagnostics.Events.Aspect[1]
	if pb.Left != 8 || pb.Top != 0 || pb.Width != 48 || pb.Height != 36 {
		t.Errorf("pillarbox span = %+v", pb)
	}
	wantTimeline(t, r, "U0-130 A130-160 U160-223")
}

// AT-08 / ASPECT-02: dark frames do not establish a ratio. Brief darkness
// leaves the confirmed ratio in place; long darkness is unknown, and the
// same ratio afterwards is not a change.
func TestAspectDarkFramesDoNotChangeRatio(t *testing.T) {
	night := flatPicture(tW, tH, 6)
	s := newScenario(
		blk{D: 40 * sec},
		blk{D: 1 * sec, Pic: night, Sep: sepNone}, // brief
		blk{D: 40 * sec, Sep: sepNone},
		blk{D: 10 * sec, Pic: night, Sep: sepNone}, // sustained
		blk{D: 40 * sec, Sep: sepNone},
	)
	r := s.run(t, nil)
	if got, want := aspectSpans(r), "1.778[0,81) 1.778[91,131)"; got != want {
		t.Errorf("aspect spans = %s, want %s", got, want)
	}
	if got := aspectBoundaries(r); len(got) != 0 {
		t.Errorf("darkness produced aspect boundaries at %v", got)
	}
	// Unknown time is not counted as known aspect.
	if d := detector(t, r, SignalAspect); *d.CoverageUS != 121_000_000 {
		t.Errorf("aspect coverage = %d", *d.CoverageUS)
	}

	// A wholly dark or uniform recording has no determinable active picture.
	r = newScenario(blk{D: 30 * sec, Pic: flatPicture(tW, tH, 128)}).run(t, nil)
	if d := detector(t, r, SignalAspect); d.Status != StatusUnreliable || d.Reason != "no_stable_active_picture" || d.Boundary {
		t.Errorf("detector = %+v", d)
	}
	if bl := r.Baselines.AspectRatio; bl.Reliable {
		t.Errorf("aspect baseline = %+v", bl)
	}
}

// AT-08 / ASPECT-02: short border transients do not change the ratio; a
// change must persist for aspect.persistence.
func TestAspectShortTransients(t *testing.T) {
	cases := []struct {
		d    float64
		want string
	}{
		{1.0, "1.778[0,81)"},
		{2.0, "1.778[0,82)"}, // last letterboxed sample is 1.8 s after the first
		{2.2, "1.778[0,40) 2.286[40,42.2) 1.778[42.2,82.2)"},
	}
	for _, c := range cases {
		s := newScenario(
			blk{D: 40 * sec},
			blk{D: us2d(int64(c.d * 1e6)), LB: 4, Sep: sepNone},
			blk{D: 40 * sec, Sep: sepNone},
		)
		r := s.run(t, nil)
		if got := aspectSpans(r); got != c.want {
			t.Errorf("letterbox for %v s: spans = %s, want %s", c.d, got, c.want)
		}
		if n := len(aspectBoundaries(r)); (n != 0) != (c.d > 2.1) {
			t.Errorf("letterbox for %v s: %d aspect boundaries", c.d, n)
		}
	}

	// Persistence is not accumulated across missing time.
	s := newScenario(
		blk{D: 40 * sec},
		blk{D: 1500 * ms, LB: 4, Sep: sepNone},
		blk{D: 3 * sec, Skip: true},
		blk{D: 1500 * ms, LB: 4},
		blk{D: 40 * sec, Sep: sepNone},
	)
	r := s.run(t, nil)
	if got := aspectBoundaries(r); len(got) != 0 {
		t.Errorf("letterbox split by a gap confirmed a change: boundaries %v spans %s", got, aspectSpans(r))
	}
}

// AT-08 / ASPECT-01: the sample (pixel) aspect ratio is part of the
// displayed active-picture ratio.
func TestAspectSampleAspectRatio(t *testing.T) {
	s := newScenario(blk{D: 30 * sec, SARNum: 4, SARDen: 3})
	r := s.run(t, nil)
	asp := r.Diagnostics.Events.Aspect
	want := 64.0 * 4 / (36.0 * 3)
	if len(asp) != 1 || *asp[0].Ratio != want {
		t.Fatalf("aspect spans = %s, want ratio %v", aspectSpans(r), want)
	}
	// The active picture itself (coded pixels) is unaffected.
	if asp[0].Width != 64 || asp[0].Height != 36 {
		t.Errorf("active picture = %+v", asp[0])
	}

	// A SAR change with the same coded size changes the displayed ratio: an
	// aspect change, not a resolution change.
	s = newScenario(
		blk{D: 30 * sec},
		blk{D: 30 * sec, SARNum: 4, SARDen: 3, Sep: sepNone},
	)
	r = s.run(t, nil)
	if got, wantS := aspectSpans(r), "1.778[0,30) 2.370[30,60)"; got != wantS {
		t.Errorf("aspect spans = %s, want %s", got, wantS)
	}
	if len(r.Diagnostics.Events.Resolution) != 0 {
		t.Errorf("SAR change reported as a coded size change")
	}
	wantBoundaries(t, r, 0, 30, 60)
	if b := mustBoundary(t, r, 30*sec); !hasCause(b, SignalAspect, "ratio_change", "boundary") {
		t.Errorf("boundary = %+v", b)
	}
	// Unknown SAR (0/1) is treated as square.
	s = newScenario(blk{D: 10 * sec, SARNum: 0, SARDen: 1})
	if got := aspectSpans(s.run(t, nil)); got != "1.778[0,10)" {
		t.Errorf("unknown SAR: %s", got)
	}
}

// ASPECT-02: invalid crops and asymmetric borders are unknown, not a ratio.
func TestAspectInvalidBoundsAreUnknown(t *testing.T) {
	topOnly := picture(tW, tH, 110, 0, 0, false)
	for i := 0; i < 8*tW; i++ {
		topOnly[i] = 0
	}
	tiny := picture(tW, tH, 110, 13, 0, false) // 10 of 36 rows: below aspect.min_active_fraction
	for name, pic := range map[string][]byte{"asymmetric": topOnly, "tiny": tiny} {
		s := newScenario(
			blk{D: 40 * sec},
			blk{D: 20 * sec, Pic: pic, Sep: sepNone},
			blk{D: 40 * sec, Sep: sepNone},
		)
		r := s.run(t, nil)
		if got, want := aspectSpans(r), "1.778[0,40) 1.778[60,100)"; got != want {
			t.Errorf("%s: aspect spans = %s, want %s", name, got, want)
		}
		if got := aspectBoundaries(r); len(got) != 0 {
			t.Errorf("%s: aspect boundaries %v", name, got)
		}
		if b := blockAt(t, r, 50*sec); findFactor(b.Factors, "aspect_ratio_mismatch") != nil {
			t.Errorf("%s: unknown bounds scored as a mismatch", name)
		}
	}

	// The limits are configurable.
	cfg := testConfig()
	cfg.Aspect.MinActiveFraction = 0.2
	s := newScenario(blk{D: 40 * sec}, blk{D: 20 * sec, Pic: tiny, Sep: sepNone}, blk{D: 40 * sec, Sep: sepNone})
	if got := aspectSpans(s.run(t, cfg)); got != "1.778[0,40) 6.400[40,60) 1.778[60,100)" {
		t.Errorf("min_active_fraction 0.2: %s", got)
	}
}

// AT-08 / ASPECT-03, BASE-02: the mismatch rule needs a supported, dominant
// program baseline.
func TestAspectBaseline(t *testing.T) {
	noAspectFactor := func(t *testing.T, r *Result) {
		t.Helper()
		for _, b := range r.Diagnostics.Blocks {
			if findFactor(b.Factors, "aspect_ratio_mismatch") != nil {
				t.Errorf("block %s has an aspect factor without a reliable baseline", b.ID)
			}
		}
	}
	t.Run("insufficient support", func(t *testing.T) {
		s := newScenario(blk{D: 47 * sec}, blk{D: 30 * sec, LB: 4, Sep: sepNone}, blk{D: 33 * sec, Sep: sepNone})
		r := s.run(t, nil)
		if bl := r.Baselines.AspectRatio; bl.Reliable || bl.Reason != "insufficient_support" {
			t.Errorf("baseline = %+v", bl)
		}
		noAspectFactor(t, r)
		wantTimeline(t, r, "U0-110")
	})
	t.Run("ambiguous", func(t *testing.T) {
		// 70 s full frame against 60 s letterbox: dominance 0.54.
		s := newScenario(blk{D: 70 * sec}, blk{D: 30 * sec, LB: 4, Sep: sepNone}, blk{D: 30 * sec, LB: 4, Sep: sepBlack})
		r := s.run(t, nil)
		bl := r.Baselines.AspectRatio
		if bl.Reliable || bl.Reason != "ambiguous" || bl.Origin != "recording_dominant_fallback" || *bl.Dominance >= 0.6 || *bl.Value != fullRatio {
			t.Errorf("baseline = %+v", bl)
		}
		noAspectFactor(t, r)
	})
	t.Run("channel profile", func(t *testing.T) {
		s := newScenario(blk{D: 47 * sec}, blk{D: 30 * sec, LB: 4, Sep: sepNone}, blk{D: 33 * sec, Sep: sepNone})
		cfg := testConfig()
		cfg.Baselines.AspectRatio = 1.78
		r := s.run(t, cfg)
		bl := r.Baselines.AspectRatio
		if !bl.Reliable || bl.Origin != "channel_profile" || *bl.Value != 1.78 {
			t.Errorf("baseline = %+v", bl)
		}
		wantTimeline(t, r, "U0-47 A47-77 U77-110")
	})
	t.Run("letterboxed program with full-frame ads", func(t *testing.T) {
		s := newScenario(blk{D: 130 * sec, LB: 4}, blk{D: 30 * sec, Sep: sepNone}, blk{D: 63 * sec, LB: 4, Sep: sepNone})
		r := s.run(t, nil)
		bl := r.Baselines.AspectRatio
		if !bl.Reliable || *bl.Value != 64.0/28.0 {
			t.Errorf("baseline = %+v", bl)
		}
		wantTimeline(t, r, "U0-130 A130-160 U160-223")
	})
	t.Run("ratios within tolerance are one value", func(t *testing.T) {
		// 64x36 against 62x36 (PB 1): 3% apart, inside aspect.ratio_tolerance.
		s := newScenario(blk{D: 130 * sec}, blk{D: 30 * sec, PB: 1, Sep: sepNone}, blk{D: 63 * sec, Sep: sepNone})
		r := s.run(t, nil)
		if got := aspectBoundaries(r); len(got) != 0 {
			t.Errorf("3%% ratio difference produced boundaries %v", got)
		}
		noAspectFactor(t, r)
		wantTimeline(t, r, "U0-223")
	})
}

// BOUND-01: aspect detector and boundary switches.
func TestAspectSwitches(t *testing.T) {
	s := newScenario(blk{D: 130 * sec}, blk{D: 30 * sec, LB: 4, Sep: sepNone}, blk{D: 63 * sec, Sep: sepNone})

	cfg := testConfig()
	cfg.Boundaries.FromAspect = false
	r := s.run(t, cfg)
	if len(r.Diagnostics.Events.Aspect) != 3 {
		t.Errorf("aspect spans = %s", aspectSpans(r))
	}
	wantBoundaries(t, r, 0, 223)
	if d := detector(t, r, SignalAspect); d.Status != StatusUsable || d.Boundary {
		t.Errorf("detector = %+v", d)
	}

	cfg = testConfig()
	cfg.Signals.Aspect = false
	r = s.run(t, cfg)
	if len(r.Diagnostics.Events.Aspect) != 0 {
		t.Errorf("disabled detector produced spans")
	}
	if d := detector(t, r, SignalAspect); d.Status != StatusDisabled || d.Boundary || d.CoverageUS != nil {
		t.Errorf("detector = %+v", d)
	}
	if bl := r.Baselines.AspectRatio; bl.Reliable || bl.Reason != "signal_unavailable" {
		t.Errorf("baseline = %+v", bl)
	}
	for _, b := range r.Diagnostics.Blocks {
		if b.ActiveRatio != nil || b.AspectKnownUS != nil {
			t.Errorf("block %s reports an active ratio", b.ID)
		}
	}
}

// TEST-01: with black separators the first letterboxed observation follows
// the black frames, so the aspect boundary and the dark boundary differ by
// at most one frame plus the aspect sampling interval. The ad is found
// within that documented uncertainty.
func TestAspectBoundaryAccuracyWithBlackSeparators(t *testing.T) {
	s := newScenario(blk{D: 130 * sec}, blk{D: 30 * sec, LB: 4}, blk{D: 63 * sec})
	r := s.run(t, nil)
	if len(r.AdBreaks) != 1 {
		t.Fatalf("timeline = %s", timeline(r))
	}
	const slack = 100_000 + 200_000
	br := r.AdBreaks[0]
	if math.Abs(float64(br.StartUS-130_000_000)) > slack || math.Abs(float64(br.EndUS-160_000_000)) > slack {
		t.Errorf("ad break [%d,%d), want [130 s,160 s) within %d us", br.StartUS, br.EndUS, slack)
	}
	if !hasString(r.AdUnits[0].EvidenceFamilies, "aspect_ratio_mismatch") {
		t.Errorf("unit evidence = %v", r.AdUnits[0].EvidenceFamilies)
	}
}

// TIME-03, MEDIA-04, BASE-02: a coverage gap is unobserved. No active
// picture is vouched for inside it, and its duration is not baseline
// support.
func TestAspectIsUnknownInCoverageGaps(t *testing.T) {
	s := newScenario(blk{D: 47 * sec}, blk{D: 30 * sec, Skip: true}, blk{D: 53 * sec})
	r := s.run(t, nil)
	if got, want := aspectSpans(r), "1.778[0,47) 1.778[77,130)"; got != want {
		t.Errorf("aspect spans = %s, want %s", got, want)
	}
	if d := detector(t, r, SignalAspect); *d.CoverageUS != 100_000_000 {
		t.Errorf("aspect coverage = %d us, the recording has 100 s of video", *d.CoverageUS)
	}
	if g := blockSpan(t, r, 47*sec, 77*sec); g.AspectKnownUS != nil || g.ActiveRatio != nil {
		t.Errorf("gap block reports an active ratio: %s", mustJSON(t, g))
	}
	// The same ratio on both sides of the gap is not a change.
	if got := aspectBoundaries(r); len(got) != 0 {
		t.Errorf("aspect boundaries at %v", got)
	}

	// 110 s of video around a 100 s gap is 110 s of baseline support, below
	// baselines.min_support (120 s): no aspect baseline.
	s = newScenario(blk{D: 55 * sec}, blk{D: 100 * sec, Skip: true}, blk{D: 55 * sec})
	r = s.run(t, nil)
	if bl := r.Baselines.AspectRatio; bl.Reliable || bl.Reason != "insufficient_support" {
		t.Errorf("aspect baseline = %s (gap time counted as support)", mustJSON(t, bl))
	}

	// A declared gap after the last frame is unknown as well.
	evs := append(newScenario(blk{D: 47 * sec}).events(), Event{Kind: EventGap, Gap: GapObservation{StartUS: 47_000_000, EndUS: 60_000_000, Reason: "resource_missing"}})
	r = analyze(t, &SliceSource{Events: evs}, testConfig())
	if got := aspectSpans(r); got != "1.778[0,47)" {
		t.Errorf("aspect spans with a tail gap = %s", got)
	}
}

// R3 / ASPECT-03: persistence counts observed time only. Two isolated
// letterbox samples on either side of a long unknown stretch (black frames)
// do not confirm a ratio, however far apart they are; a ratio observed
// continuously for the persistence time does, dated at its first sample.
func TestAspectPersistenceIsObservedTime(t *testing.T) {
	const (
		sample  = 500_000
		persist = 2_000_000
	)
	tr := &aspectTracker{sampleUS: sample, persistUS: persist, tol: 0.02}
	tr.newGeometry(0, &geomEpoch{aw: 64, ah: 36, scale: 1, sarN: 1, sarD: 1})
	wide := aspectObs{l: 0, t: 0, r: 64, b: 36, ratio: 64.0 / 36}
	box := aspectObs{l: 0, t: 6, r: 64, b: 30, ratio: 64.0 / 24}
	feed := func(pts int64, obs aspectObs, known bool) {
		// Drive the state machine as frame() does after measuring.
		tr.lastPTS, tr.nextSample = pts, pts
		if !known {
			if !tr.inUnknown {
				tr.inUnknown, tr.unkStart = true, pts
			}
			tr.candContig = false
			if tr.curValid && pts-tr.unkStart >= tr.persistUS {
				tr.closeCurrent(tr.unkStart)
			}
			return
		}
		tr.accept(pts, obs)
	}
	var pts int64
	for ; pts < 5_000_000; pts += sample {
		feed(pts, wide, true)
	}
	// One letterbox sample, 10 s of unknown, one more letterbox sample.
	feed(pts, box, true)
	for pts += sample; pts < 15_500_000; pts += sample {
		feed(pts, wide, false)
	}
	feed(pts, box, true)
	pts += sample
	feed(pts, wide, false)
	tr.finish(pts)
	for _, s := range tr.spans {
		if math.Abs(s.ratio-box.ratio) < 0.01 {
			t.Fatalf("isolated letterbox samples around unknown time confirmed %v: spans %+v", box.ratio, tr.spans)
		}
	}

	// Continuous letterbox for the persistence time is confirmed, dated at
	// its first sample, and spans do not overlap.
	tr = &aspectTracker{sampleUS: sample, persistUS: persist, tol: 0.02}
	tr.newGeometry(0, &geomEpoch{aw: 64, ah: 36, scale: 1, sarN: 1, sarD: 1})
	for pts = 0; pts < 5_000_000; pts += sample {
		feed(pts, wide, true)
	}
	for ; pts < 9_000_000; pts += sample {
		feed(pts, box, true)
	}
	tr.finish(pts)
	if len(tr.spans) != 2 || tr.spans[1].start != 5_000_000 || math.Abs(tr.spans[1].ratio-box.ratio) > 0.01 {
		t.Fatalf("spans %+v, want the letterbox confirmed from 5 s", tr.spans)
	}
	for i := 1; i < len(tr.spans); i++ {
		if tr.spans[i].start < tr.spans[i-1].end {
			t.Errorf("spans overlap: %+v", tr.spans)
		}
	}
}
