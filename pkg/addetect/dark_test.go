// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"testing"
)

// AT-02 and AT-03: black/uniform frames and their calibration (VIS-01..03).

// frameSeq builds a 10 fps recording showing the given pictures in order.
func frameSeq(pics ...[]byte) []Event {
	evs := make([]Event, len(pics))
	for i, p := range pics {
		evs[i] = videoFrame(int64(i)*100_000, 100_000, p)
	}
	return evs
}

func rep(p []byte, n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = p
	}
	return out
}

func join(parts ...[][]byte) [][]byte {
	var out [][]byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// darkWithObject is black except for a small object: 2% of the pixels at
// luminance 70. Mean ~1.4 and nonuniformity ~10 would both pass as dark; only
// the bright-pixel safeguard rejects it.
func darkWithObject() []byte {
	p := flatPicture(tW, tH, 0)
	for i := 0; i < tW*tH*2/100; i++ {
		p[10*tW+10+i%8+(i/8)*tW] = 70
	}
	return p
}

// darkNonuniform is half 0 and half 38: mean 19, no bright pixel, but a
// nonuniformity of 19 exceeds dark.dark_max_nonuniformity.
func darkNonuniform() []byte {
	p := flatPicture(tW, tH, 0)
	for y := 0; y < tH; y++ {
		for x := tW / 2; x < tW; x++ {
			p[y*tW+x] = 38
		}
	}
	return p
}

// AT-02 / VIS-01, VIS-03: dark, uniform, dark-with-bright-object and
// nonuniform dark frames are distinct measurements.
func TestDarkAndUniformKinds(t *testing.T) {
	c := rep(content, 30)
	pics := join(
		c, rep(flatPicture(tW, tH, 0), 5), // 3.0-3.5 dark
		c, rep(flatPicture(tW, tH, 128), 4), // 6.5-6.9 uniform
		c, rep(darkWithObject(), 5), // 9.9-10.4 nothing (bright-pixel safeguard)
		c, rep(darkNonuniform(), 5), // 13.4-13.9 nothing (uniformity safeguard)
		c, rep(flatPicture(tW, tH, 19), 3), // 16.9-17.2 dark: the threshold is inclusive
		c, rep(flatPicture(tW, tH, 20), 3), // 20.2-20.5 uniform, not dark
		c,
	)
	r := analyze(t, &SliceSource{Events: frameSeq(pics...)}, testConfig())
	type ev struct {
		s, e   int64
		kind   string
		frames int
		minB   float64
	}
	want := []ev{
		{3_000_000, 3_500_000, "dark", 5, 0},
		{6_500_000, 6_900_000, "uniform", 4, 128},
		{16_900_000, 17_200_000, "dark", 3, 19},
		{20_200_000, 20_500_000, "uniform", 3, 20},
	}
	got := r.Diagnostics.Events.DarkUniform
	if len(got) != len(want) {
		t.Fatalf("events = %+v", got)
	}
	for i, w := range want {
		g := got[i]
		if g.StartUS != w.s || g.EndUS != w.e || g.Kind != w.kind || g.Frames != w.frames || g.MinBrightness != w.minB || g.MeanBrightness != w.minB || g.MeanNonuniformity != 0 || g.Coverage != 1 {
			t.Errorf("event %d = %+v, want %+v", i, g, w)
		}
	}
	if d := detector(t, r, SignalDarkUniform); d.Status != StatusUsable || d.Events != 4 || !d.Boundary || *d.CoverageUS != r.DurationUS {
		t.Errorf("detector = %+v", d)
	}
	// Each event is one candidate boundary at its midpoint, on a frame time.
	wantBoundaries(t, r, 0, 3.2, 6.7, 17, 20.3, 23.5)
	for _, at := range []float64{3.2, 17} {
		if b := mustBoundary(t, r, us2d(int64(at*1e6))); !hasCause(b, SignalDarkUniform, "dark", "boundary") {
			t.Errorf("boundary at %v causes = %+v", at, b.Causes)
		}
	}
	if b := mustBoundary(t, r, us2d(6_700_000)); !hasCause(b, SignalDarkUniform, "uniform", "boundary") || b.UncertaintyUS != 200_000 {
		t.Errorf("uniform boundary = %+v", b)
	}

	// The measurements behind the two rejected sequences.
	blk := blockAt(t, r, us2d(10_000_000))
	if blk.Brightness == nil || blk.Nonuniformity == nil {
		t.Fatalf("block without measurements: %+v", blk)
	}
}

// VIS-01: the safeguards are configurable and the full-resolution
// bright-pixel measurement, when supplied, overrides the reduced image.
func TestDarkSafeguardsConfigurable(t *testing.T) {
	c := rep(content, 20)
	evs := frameSeq(join(c, rep(darkWithObject(), 5), c, rep(darkNonuniform(), 5), c)...)

	cfg := testConfig()
	if n := len(analyze(t, &SliceSource{Events: evs}, cfg).Diagnostics.Events.DarkUniform); n != 0 {
		t.Fatalf("defaults: %d events, want 0", n)
	}
	cfg = testConfig()
	cfg.Dark.MaxBrightFraction = 0.05
	got := analyze(t, &SliceSource{Events: evs}, cfg).Diagnostics.Events.DarkUniform
	if len(got) != 1 || got[0].StartUS != 2_000_000 || got[0].Kind != "dark" {
		t.Errorf("relaxed bright fraction: %+v", got)
	}
	cfg = testConfig()
	cfg.Dark.DarkMaxNonuniformity = 25
	got = analyze(t, &SliceSource{Events: evs}, cfg).Diagnostics.Events.DarkUniform
	if len(got) != 1 || got[0].StartUS != 4_500_000 || got[0].Kind != "dark" {
		t.Errorf("relaxed nonuniformity: %+v", got)
	}
	cfg = testConfig()
	cfg.Dark.BrightPixelLuma = 80 // the object (70) is no longer bright
	got = analyze(t, &SliceSource{Events: evs}, cfg).Diagnostics.Events.DarkUniform
	if len(got) != 1 || got[0].StartUS != 2_000_000 {
		t.Errorf("higher bright-pixel luma: %+v", got)
	}

	// A black analysis picture whose full-resolution measurement saw bright
	// detail (lost in the reduction) is not dark.
	evs = frameSeq(join(c, rep(flatPicture(tW, tH, 0), 5), c)...)
	for i := 20; i < 25; i++ {
		evs[i].Video.FullResBrightFraction = 0.05
	}
	if n := len(analyze(t, &SliceSource{Events: evs}, testConfig()).Diagnostics.Events.DarkUniform); n != 0 {
		t.Errorf("full-resolution bright detail ignored: %d events", n)
	}
	cfg = testConfig()
	cfg.Sampling.FullResBrightCheck = false
	if n := len(analyze(t, &SliceSource{Events: evs}, cfg).Diagnostics.Events.DarkUniform); n != 1 {
		t.Errorf("full_resolution_bright_check=false: %d events, want 1", n)
	}
}

// VIS-01: uniform detection can be switched off without affecting dark
// detection; the whole detector can be disabled.
func TestDarkUniformSwitches(t *testing.T) {
	c := rep(content, 20)
	evs := frameSeq(join(c, rep(flatPicture(tW, tH, 0), 4), c, rep(flatPicture(tW, tH, 128), 4), c)...)

	cfg := testConfig()
	cfg.Dark.DetectUniform = false
	r := analyze(t, &SliceSource{Events: evs}, cfg)
	if got := r.Diagnostics.Events.DarkUniform; len(got) != 1 || got[0].Kind != "dark" {
		t.Errorf("detect_uniform=false: %+v", got)
	}

	cfg = testConfig()
	cfg.Signals.DarkUniform = false
	r = analyze(t, &SliceSource{Events: evs}, cfg)
	if d := detector(t, r, SignalDarkUniform); d.Status != StatusDisabled || d.Boundary || d.CoverageUS != nil || d.Events != 0 {
		t.Errorf("disabled detector = %+v", d)
	}
	if len(r.Diagnostics.Events.DarkUniform) != 0 {
		t.Errorf("disabled detector produced events")
	}
	wantBoundaries(t, r, 0, 6.8)

	// Enabled detector that is not a boundary source: events, no boundaries.
	cfg = testConfig()
	cfg.Boundaries.FromDarkUniform = false
	r = analyze(t, &SliceSource{Events: evs}, cfg)
	if len(r.Diagnostics.Events.DarkUniform) != 2 {
		t.Errorf("events = %+v", r.Diagnostics.Events.DarkUniform)
	}
	wantBoundaries(t, r, 0, 6.8)
	if d := detector(t, r, SignalDarkUniform); d.Status != StatusUsable || d.Boundary {
		t.Errorf("detector = %+v", d)
	}
}

// AT-02 / VIS-03: a sustained black screen is one event and one candidate,
// not a cut per frame; a single dark frame is a valid event.
func TestDarkSustainedIsOneEvent(t *testing.T) {
	evs := frameSeq(join(rep(content, 100), rep(flatPicture(tW, tH, 0), 200), rep(content, 100), rep(flatPicture(tW, tH, 0), 1), rep(content, 100))...)
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	got := r.Diagnostics.Events.DarkUniform
	if len(got) != 2 {
		t.Fatalf("events = %+v", got)
	}
	if got[0].StartUS != 10_000_000 || got[0].EndUS != 30_000_000 || got[0].Frames != 200 {
		t.Errorf("sustained event = %+v", got[0])
	}
	if got[1].StartUS != 40_000_000 || got[1].EndUS != 40_100_000 || got[1].Frames != 1 {
		t.Errorf("single-frame event = %+v", got[1])
	}
	// One boundary for the 20 s of black, at its midpoint, with the span and
	// the resulting uncertainty kept in the diagnostics (BOUND-02).
	wantBoundaries(t, r, 0, 20, 40, 50.1)
	b := mustBoundary(t, r, 20*sec)
	if b.UncertaintyUS != 10_000_000 || len(b.Causes) != 1 || b.Causes[0].EventStartUS != 10_000_000 || b.Causes[0].EventEndUS != 30_000_000 || b.Causes[0].CandidateUS != 20_000_000 {
		t.Errorf("boundary = %+v", b)
	}
}

// AT-02 / VIS-03: a fade to black and back is one event whose extent is the
// run of frames that actually qualify; black followed by a solid colour is
// one continuous span and one candidate.
func TestDarkFadesAndAdjacentSpans(t *testing.T) {
	scale := func(f float64) []byte {
		p := make([]byte, len(content))
		for i, v := range content {
			p[i] = byte(float64(v) * f)
		}
		return p
	}
	var pics [][]byte
	pics = append(pics, rep(content, 30)...)
	for i := 9; i >= 1; i-- { // fade out over 0.9 s
		pics = append(pics, scale(float64(i)/10))
	}
	pics = append(pics, rep(flatPicture(tW, tH, 0), 3)...)
	for i := 1; i <= 9; i++ { // fade in
		pics = append(pics, scale(float64(i)/10))
	}
	pics = append(pics, rep(content, 30)...)
	r := analyze(t, &SliceSource{Events: frameSeq(pics...)}, testConfig())
	got := r.Diagnostics.Events.DarkUniform
	if len(got) != 1 {
		t.Fatalf("a fade must produce one event, got %+v", got)
	}
	e := got[0]
	// Content mean is ~110, so only the 10% frames (mean ~11) and the black
	// frames qualify: 3.8 s .. 4.3 s.
	if e.Kind != "dark" || e.StartUS != 3_800_000 || e.EndUS != 4_300_000 || e.Frames != 5 || e.MinBrightness != 0 || e.MeanBrightness <= 0 || e.MeanBrightness > 19 {
		t.Errorf("fade event = %+v", e)
	}
	wantBoundaries(t, r, 0, 4, 8.1)

	// Black then solid gray: two event kinds, one continuous span, one
	// candidate at the midpoint of the whole span.
	evs := frameSeq(join(rep(content, 30), rep(flatPicture(tW, tH, 0), 4), rep(flatPicture(tW, tH, 128), 4), rep(content, 30))...)
	r = analyze(t, &SliceSource{Events: evs}, testConfig())
	got = r.Diagnostics.Events.DarkUniform
	if len(got) != 2 || got[0].Kind != "dark" || got[1].Kind != "uniform" || got[0].EndUS != got[1].StartUS {
		t.Fatalf("events = %+v", got)
	}
	wantBoundaries(t, r, 0, 3.4, 6.8)
	b := mustBoundary(t, r, us2d(3_400_000))
	if len(b.Causes) != 1 || b.Causes[0].Kind != "dark_uniform_span" || b.Causes[0].EventStartUS != 3_000_000 || b.Causes[0].EventEndUS != 3_800_000 {
		t.Errorf("span boundary = %+v", b)
	}
}

// AT-02 / VIS-03: a long dark program scene, dark opening titles or credits
// are not automatically advertisements.
func TestDarkProgramSceneIsNotAnAd(t *testing.T) {
	s := newScenario(
		blk{D: 47 * sec, Sep: sepNone},
		blk{D: 30 * sec, Sep: sepNone, Pic: flatPicture(tW, tH, 6)}, // a 30 s night scene
		blk{D: 53 * sec, Sep: sepNone},
	)
	r := s.run(t, nil)
	if len(r.AdBreaks) != 0 {
		t.Fatalf("dark scene became an ad: %s", timeline(r))
	}
	wantTimeline(t, r, "U0-130")
	if got := r.Diagnostics.Events.DarkUniform; len(got) != 1 || got[0].StartUS != 47_000_000 || got[0].EndUS != 77_000_000 {
		t.Errorf("events = %+v", got)
	}
	// One candidate in the middle of the scene; darkness itself contributes
	// no score factor.
	wantBoundaries(t, r, 0, 62, 130)
	for _, b := range r.Diagnostics.Blocks {
		if b.Score != 1 || len(b.Factors) != 0 {
			t.Errorf("block %s score %v factors %s", b.ID, b.Score, rules(b.Factors))
		}
	}

	// Dark titles at the start and dark credits at the end.
	s = newScenario(
		blk{D: 20 * sec, Pic: flatPicture(tW, tH, 4)},
		blk{D: 77 * sec, Sep: sepNone},
		blk{D: 25 * sec, Sep: sepNone, Pic: flatPicture(tW, tH, 4)},
	)
	r = s.run(t, nil)
	if len(r.AdBreaks) != 0 {
		t.Fatalf("titles/credits became an ad: %s", timeline(r))
	}
}

// calibRecording is 110 s of content with flat frames of the given
// luminances spread through it.
func calibRecording(flats map[byte]int) []Event {
	pics := rep(content, 1100)
	at := 50
	for _, lv := range sortedKeys(flats) {
		for i := 0; i < flats[lv]; i++ {
			pics[at] = flatPicture(tW, tH, lv)
			at++
		}
		at += 20
	}
	return frameSeq(pics...)
}

func sortedKeys(m map[byte]int) []byte {
	var ks []byte
	for k := byte(0); ; k++ {
		if _, ok := m[k]; ok {
			ks = append(ks, k)
		}
		if k == 255 {
			return ks
		}
	}
}

// AT-03 / VIS-02: automatic calibration estimates thresholds from a suitable
// distribution and reports the estimate and its support.
func TestDarkAutoCalibrationEstimates(t *testing.T) {
	// This recording's black sits at 23: above the fixed threshold of 19.
	evs := calibRecording(map[byte]int{23: 12})

	fixed := analyze(t, &SliceSource{Events: evs}, testConfig())
	if c := fixed.Calibration.Dark; c.Mode != "fixed" || c.DarkThreshold != 19 || c.UniformThreshold != 3 || c.Estimated || c.FallbackReason != "" || c.UsableFrames != 1100 {
		t.Errorf("fixed calibration = %+v", c)
	}
	// AT-03: a fixed-mode run does not change its threshold, so 23 is only a
	// uniform frame.
	if got := fixed.Diagnostics.Events.DarkUniform; len(got) != 1 || got[0].Kind != "uniform" {
		t.Errorf("fixed mode events = %+v", got)
	}

	cfg := testConfig()
	cfg.Dark.Mode = "auto"
	auto := analyze(t, &SliceSource{Events: evs}, cfg)
	c := auto.Calibration.Dark
	// Darkest cluster top (23) + auto_margin (2); uniform bound from the
	// cluster's nonuniformity (0), raised to auto_uniform_min.
	if c.Mode != "auto" || !c.Estimated || c.DarkThreshold != 25 || c.UniformThreshold != 1 || c.SupportFrames != 12 || c.UsableFrames != 1100 || c.FallbackReason != "" {
		t.Errorf("auto calibration = %+v", c)
	}
	if got := auto.Diagnostics.Events.DarkUniform; len(got) != 1 || got[0].Kind != "dark" || got[0].Frames != 12 {
		t.Errorf("auto mode events = %+v", got)
	}
}

// AT-03 / VIS-02: the estimate is bounded by the configured limits.
func TestDarkAutoCalibrationBounds(t *testing.T) {
	cfg := testConfig()
	cfg.Dark.Mode = "auto"
	r := analyze(t, &SliceSource{Events: calibRecording(map[byte]int{39: 10})}, cfg)
	if c := r.Calibration.Dark; !c.Estimated || c.DarkThreshold != 40 {
		t.Errorf("upper bound: %+v", c)
	}
	r = analyze(t, &SliceSource{Events: calibRecording(map[byte]int{2: 10})}, cfg)
	if c := r.Calibration.Dark; !c.Estimated || c.DarkThreshold != 8 {
		t.Errorf("lower bound: %+v", c)
	}
	// A cluster of neighbouring levels is taken as a whole: top 14 + 2.
	r = analyze(t, &SliceSource{Events: calibRecording(map[byte]int{10: 5, 12: 5, 14: 5})}, cfg)
	if c := r.Calibration.Dark; !c.Estimated || c.DarkThreshold != 16 || c.SupportFrames != 15 {
		t.Errorf("cluster: %+v", c)
	}
	// A separate brighter flat population (solid colour cards) does not pull
	// the dark threshold up.
	r = analyze(t, &SliceSource{Events: calibRecording(map[byte]int{10: 9, 30: 40})}, cfg)
	if c := r.Calibration.Dark; !c.Estimated || c.DarkThreshold != 12 || c.SupportFrames != 9 {
		t.Errorf("two populations: %+v", c)
	}
}

// AT-03 / VIS-02: insufficient or ambiguous data falls back to the fixed
// thresholds and reports why.
func TestDarkAutoCalibrationFallback(t *testing.T) {
	auto := func() *Config {
		cfg := testConfig()
		cfg.Dark.Mode = "auto"
		return cfg
	}
	chain := map[byte]int{}
	for lv := byte(20); lv <= 46; lv += 2 {
		chain[lv] = 3
	}
	cases := []struct {
		name   string
		evs    []Event
		reason string
	}{
		// Fewer frames than dark.auto_min_frames (1000).
		{"insufficient data", frameSeq(join(rep(content, 300), rep(flatPicture(tW, tH, 23), 20), rep(content, 300))...), "insufficient_data"},
		// No flat frame at or below auto_max_luma.
		{"no dark frames", calibRecording(nil), "no_dark_frames"},
		{"only bright flat frames", calibRecording(map[byte]int{128: 30}), "no_dark_frames"},
		// The darkest cluster runs on past auto_max_luma: no separable black.
		{"ambiguous distribution", calibRecording(chain), "ambiguous_distribution"},
		// Fewer cluster frames than dark.auto_min_support (8).
		{"insufficient support", calibRecording(map[byte]int{23: 5}), "insufficient_support"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := analyze(t, &SliceSource{Events: c.evs}, auto())
			cal := r.Calibration.Dark
			if cal.Mode != "auto" || cal.Estimated || cal.FallbackReason != c.reason || cal.DarkThreshold != 19 || cal.UniformThreshold != 3 {
				t.Errorf("calibration = %+v, want fixed fallback with reason %q", cal, c.reason)
			}
		})
	}
}
