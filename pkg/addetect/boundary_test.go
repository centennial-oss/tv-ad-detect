// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"fmt"
	"testing"
	"time"
)

// AT-11: candidate boundary construction and fusion (BOUND-01..03,
// BLOCK-01).

// fusionAnalysis is a bare post-extraction state with a frame grid, for
// white-box tests that inject detector events at exact microsecond times.
func fusionAnalysis(cfg *Config, durUS, frameUS int64) *analysis {
	a := &analysis{cfg: cfg}
	for t := int64(0); t < durUS; t += frameUS {
		a.frames = append(a.frames, frameRec{pts: t})
	}
	a.dur, a.lastEnd, a.frameUS = durUS, durUS, frameUS
	return a
}

func boundsUS(a *analysis) []int64 {
	out := make([]int64, len(a.bounds))
	for i, b := range a.bounds {
		out[i] = b.t
	}
	return out
}

func wantBoundsUS(t *testing.T, a *analysis, want ...int64) {
	t.Helper()
	if got := boundsUS(a); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("boundaries = %v, want %v", got, want)
	}
}

func cutAt(us int64) SceneEvent { return SceneEvent{TimeUS: us, Kind: "cut", Distance: 1} }

// checkFusion asserts the BOUND-03 invariants on fused boundaries.
func checkFusion(t *testing.T, a *analysis, candidates int) {
	t.Helper()
	tol := us(a.cfg.Boundaries.FusionTolerance)
	causes := 0
	for i, b := range a.bounds {
		if i > 0 && b.t <= a.bounds[i-1].t {
			t.Errorf("boundary %d at %d is not after boundary %d at %d", i, b.t, i-1, a.bounds[i-1].t)
		}
		lo, hi := int64(1<<62), int64(-1<<62)
		for _, c := range b.causes {
			if c.Role != "boundary" || c.Signal == "timeline" && (c.Kind == "recording_start" || c.Kind == "recording_end") {
				continue
			}
			causes++
			lo, hi = min(lo, c.CandidateUS), max(hi, c.CandidateUS)
		}
		// No chain: the candidates fused into one boundary span at most the
		// tolerance (edge boundaries: at most the tolerance from the edge).
		if hi >= lo && hi-lo > tol {
			t.Errorf("boundary %d at %d fuses candidates spanning %d us (%d..%d), tolerance %d", i, b.t, hi-lo, lo, hi, tol)
		}
	}
	if causes != candidates {
		t.Errorf("%d candidate causes preserved, want %d", causes, candidates)
	}
}

// AT-11 / BOUND-03: a chain of candidates, each closer to the next than the
// tolerance, is not merged into one boundary.
func TestFusionChainsAreBounded(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Boundaries.FromScene = true

	// 34 cuts 60 ms apart over 2 s: each group is anchored at its first
	// candidate and accepts only candidates within 100 ms of that anchor.
	a := fusionAnalysis(cfg, 10_000_000, 20_000)
	var want []int64
	want = append(want, 0)
	for k := 0; k < 34; k++ {
		a.scene = append(a.scene, cutAt(1_000_000+int64(k)*60_000))
		if k%2 == 0 {
			want = append(want, 1_000_000+int64(k)*60_000)
		}
	}
	want = append(want, 10_000_000)
	a.buildBoundaries(false)
	wantBoundsUS(t, a, want...)
	checkFusion(t, a, 34)

	// 201 cuts 10 ms apart over 2 s: groups of eleven (0..100 ms inclusive).
	a = fusionAnalysis(cfg, 10_000_000, 10_000)
	want = []int64{0}
	for k := 0; k <= 200; k++ {
		a.scene = append(a.scene, cutAt(1_000_000+int64(k)*10_000))
		if k%11 == 0 {
			want = append(want, 1_000_000+int64(k)*10_000)
		}
	}
	want = append(want, 10_000_000)
	a.buildBoundaries(false)
	wantBoundsUS(t, a, want...)
	checkFusion(t, a, 201)

	// The tolerance is inclusive and configurable; zero fuses only equal
	// times.
	a = fusionAnalysis(cfg, 10_000_000, 1_000)
	a.scene = []SceneEvent{cutAt(1_000_000), cutAt(1_100_000), cutAt(1_200_001), cutAt(1_300_002)}
	a.buildBoundaries(false)
	wantBoundsUS(t, a, 0, 1_000_000, 1_200_001, 1_300_002, 10_000_000)
	checkFusion(t, a, 4)

	cfg0 := DefaultConfig()
	cfg0.Boundaries.FromScene = true
	cfg0.Boundaries.FusionTolerance = 0
	a = fusionAnalysis(cfg0, 10_000_000, 1_000)
	a.scene = []SceneEvent{cutAt(1_000_000), cutAt(1_000_001), cutAt(1_000_002)}
	a.buildBoundaries(false)
	wantBoundsUS(t, a, 0, 1_000_000, 1_000_001, 1_000_002, 10_000_000)
}

// AT-11 / BOUND-03: end to end, a chain of short silences (candidates every
// 60 ms for 3 s) yields bounded groups, preserved causes and nonempty
// blocks.
func TestFusionChainEndToEnd(t *testing.T) {
	evs := audioRecording(20, func(t int64) (float32, float32, bool) {
		if between(t, 5_040_000, 8_040_000) && (t/20_000)%3 != 2 {
			return silenceMS, silenceMS, true // 40 ms silent, 20 ms loud
		}
		return speechMS, speechMS, true
	})
	cfg := testConfig()
	cfg.Audio.MinSilence = 20 * time.Millisecond
	cfg.Boundaries.SilenceSceneAlign = false
	r := analyze(t, &SliceSource{Events: evs}, cfg)
	sil := r.Diagnostics.Events.Silence
	if len(sil) != 50 {
		t.Fatalf("%d silence events, want 50", len(sil))
	}
	bds := r.Diagnostics.Boundaries
	// Candidates at 5.06 s + k*60 ms; groups hold two candidates each.
	if len(bds) != 27 {
		t.Fatalf("%d boundaries, want 27 (start, 25 groups, end): %v", len(bds), boundaryTimes(r))
	}
	causes := 0
	for i, b := range bds[1 : len(bds)-1] {
		if want := int64(5_060_000 + i*120_000); b.TimeUS != want {
			t.Errorf("boundary %d at %d, want %d", i, b.TimeUS, want)
		}
		lo, hi := int64(1<<62), int64(0)
		for _, c := range b.Causes {
			causes++
			lo, hi = min(lo, c.CandidateUS), max(hi, c.CandidateUS)
			if c.Signal != SignalAudio || c.Kind != "silence" || c.Role != "boundary" {
				t.Errorf("cause = %+v", c)
			}
		}
		if hi-lo > 100_000 {
			t.Errorf("boundary at %d fuses candidates %d us apart", b.TimeUS, hi-lo)
		}
	}
	if causes != 50 {
		t.Errorf("%d causes preserved, want 50", causes)
	}
	// Silence-only groups are never ad evidence, however many there are.
	wantTimeline(t, r, "U0-20")
}

// AT-11 / BOUND-03: near-coincident signals fuse into one boundary without
// losing a cause; the group takes the timestamp of its highest-priority
// member and the largest uncertainty.
func TestFusionPreservesCausesAndPriority(t *testing.T) {
	s := newScenario(
		blk{D: 47 * sec, Cap: CaptionRollUp},
		blk{D: 53 * sec, Cap: CaptionPopOn, W: 128, H: 72},
	)
	cfg := testConfig()
	cfg.Boundaries.FromCaptions = true
	cfg.Boundaries.FromScene = true
	r := s.run(t, cfg)
	wantBoundaries(t, r, 0, 47, 100)
	b := mustBoundary(t, r, 47*sec)
	for _, want := range []struct{ signal, kind, role string }{
		{SignalResolution, "coded_size_change", "boundary"},
		{SignalDarkUniform, "dark", "boundary"},
		{SignalCaptions, "style_change", "boundary"},
		{SignalScene, "cut", "boundary"},
		{SignalAudio, "silence", "corroboration"},
	} {
		if !hasCause(b, want.signal, want.kind, want.role) {
			t.Errorf("cause %+v lost in fusion: %+v", want, b.Causes)
		}
	}
	// The scene cut (content to black) is one frame before the fused time
	// and keeps its own candidate time in the diagnostics.
	for _, c := range b.Causes {
		if c.Signal == SignalScene && c.CandidateUS != 46_900_000 {
			t.Errorf("scene cause candidate = %d", c.CandidateUS)
		}
	}
	if mustJSON(t, b.Evidence) != `["dark_uniform_boundary","av_corroborated_boundary"]` || b.UncertaintyUS != 100_000 {
		t.Errorf("evidence = %v uncertainty = %d", b.Evidence, b.UncertaintyUS)
	}

	// White box: kind priority decides the time, not arrival order; among
	// equal kinds the earlier candidate.
	c2 := DefaultConfig()
	c2.Boundaries.FromScene = true
	a := fusionAnalysis(c2, 10_000_000, 10_000)
	a.scene = []SceneEvent{cutAt(2_000_000), cutAt(2_050_000)}
	a.dark = []DarkEvent{{StartUS: 2_040_000, EndUS: 2_100_000, Kind: "dark", Frames: 6}} // candidate 2.07 s
	a.resolution = []ResolutionEvent{{TimeUS: 2_090_000, OldWidth: 1, OldHeight: 1, NewWidth: 2, NewHeight: 2}}
	a.buildBoundaries(false)
	wantBoundsUS(t, a, 0, 2_090_000, 10_000_000)
	if b := a.bounds[1]; len(b.causes) != 4 || !b.dark || b.unc != 30_000 {
		t.Errorf("fused boundary = %+v", b)
	}
	a = fusionAnalysis(c2, 10_000_000, 10_000)
	a.scene = []SceneEvent{cutAt(2_000_000), cutAt(2_050_000)}
	a.dark = []DarkEvent{{StartUS: 2_040_000, EndUS: 2_100_000, Kind: "dark", Frames: 6}}
	a.buildBoundaries(false)
	wantBoundsUS(t, a, 0, 2_070_000, 10_000_000) // dark outranks scene
	a = fusionAnalysis(c2, 10_000_000, 10_000)
	a.scene = []SceneEvent{cutAt(2_000_000), cutAt(2_050_000)}
	a.buildBoundaries(false)
	wantBoundsUS(t, a, 0, 2_000_000, 10_000_000) // equal kinds: the earlier one
}

// AT-11 / BOUND-03: exact kinds (coded size changes, coverage edges) keep
// their timestamps: two of them with different times never fuse, even
// inside the tolerance.
func TestFusionExactKindsKeepTimestamps(t *testing.T) {
	// 50 fps; new size at 2.00 s; three frames later the media stops until
	// 5.00 s. The size change and the gap start are 60 ms apart.
	var evs []Event
	add := func(pts int64, w, h int) {
		e := videoFrame(pts, 20_000, content)
		e.Video.CodedWidth, e.Video.CodedHeight = w, h
		evs = append(evs, e)
	}
	for i := 0; i < 100; i++ {
		add(int64(i)*20_000, 640, 360)
	}
	for i := 100; i < 103; i++ {
		add(int64(i)*20_000, 1280, 720)
	}
	for i := 250; i < 350; i++ {
		add(int64(i)*20_000, 1280, 720)
	}
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	wantBoundaries(t, r, 0, 2, 2.06, 5, 7)
	if b := mustBoundary(t, r, 2*sec); len(b.Causes) != 1 || !hasCause(b, SignalResolution, "coded_size_change", "boundary") {
		t.Errorf("size boundary = %+v", b)
	}
	if b := mustBoundary(t, r, us2d(2_060_000)); len(b.Causes) != 1 || !hasCause(b, "timeline", "coverage_gap_start", "boundary") {
		t.Errorf("gap start boundary = %+v", b)
	}
	// BLOCK-01 / BLOCK-02: the 60 ms block stays representable and the
	// missing interval is its own block.
	if b := blockSpan(t, r, 2*sec, us2d(2_060_000)); b.Missing || b.CodedWidth != 1280 {
		t.Errorf("short block = %+v", b)
	}
	if b := blockSpan(t, r, us2d(2_060_000), 5*sec); !b.Missing {
		t.Errorf("gap block = %+v", b)
	}

	// White box: a non-exact candidate between two exact ones joins a group
	// without moving either exact timestamp.
	cfg := DefaultConfig()
	cfg.Boundaries.FromScene = true
	a := fusionAnalysis(cfg, 10_000_000, 10_000)
	a.resolution = []ResolutionEvent{{TimeUS: 3_000_000}, {TimeUS: 3_050_000}}
	a.scene = []SceneEvent{cutAt(3_020_000), cutAt(3_090_000)}
	a.buildBoundaries(false)
	wantBoundsUS(t, a, 0, 3_000_000, 3_050_000, 10_000_000)
	checkFusion(t, a, 4)
}

// SIZE-01, BOUND-03, BLOCK-01: exact kinds keep their timestamps next to the
// recording edges too. A size change on the second frame, or a coverage
// edge within the fusion tolerance of the start or end, is its own boundary
// rather than being absorbed into the edge.
func TestFusionExactKindsNearRecordingEdges(t *testing.T) {
	// The second frame (0.1 s, exactly the tolerance) has a new size.
	evs := frameSeq(rep(content, 100)...)
	for i := 1; i < len(evs); i++ {
		evs[i].Video.CodedWidth, evs[i].Video.CodedHeight, evs[i].Video.Scale = 128, 72, 2
	}
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	if len(r.Diagnostics.Events.Resolution) != 1 {
		t.Fatalf("resolution events = %+v", r.Diagnostics.Events.Resolution)
	}
	wantBoundaries(t, r, 0, 0.1, 10)
	if b := boundaryAt(r, 100*ms); b == nil || !hasCause(b, SignalResolution, "coded_size_change", "boundary") {
		t.Errorf("no boundary at the first frame with the new dimensions: %v", boundaryTimes(r))
	}
	if b := r.Diagnostics.Blocks[0]; b.CodedWidth != 64 {
		t.Errorf("first block coded width = %d", b.CodedWidth)
	}

	// The last frame has a new size.
	evs = frameSeq(rep(content, 100)...)
	evs[99].Video.CodedWidth, evs[99].Video.CodedHeight, evs[99].Video.Scale = 128, 72, 2
	r = analyze(t, &SliceSource{Events: evs}, testConfig())
	wantBoundaries(t, r, 0, 9.9, 10)

	// One frame, then nothing for 3 s: the gap starts 0.1 s in.
	s := newScenario(blk{D: 100 * ms}, blk{D: 2900 * ms, Skip: true}, blk{D: 7 * sec})
	r = s.run(t, nil)
	wantBoundaries(t, r, 0, 0.1, 3, 10)
	if b := blockSpan(t, r, 100*ms, 3*sec); !b.Missing || b.Coverage != 0 {
		t.Errorf("gap block = %+v", b)
	}
	if b := blockSpan(t, r, 0, 100*ms); b.Missing || b.Coverage != 1 {
		t.Errorf("first block = %+v", b)
	}

	// A gap that ends one frame before the end of the recording.
	s = newScenario(blk{D: 7 * sec}, blk{D: 2900 * ms, Skip: true}, blk{D: 100 * ms})
	r = s.run(t, nil)
	wantBoundaries(t, r, 0, 7, 9.9, 10)
	if b := blockSpan(t, r, 7*sec, 9900*ms); !b.Missing {
		t.Errorf("gap block = %+v", b)
	}

	// Non-exact candidates next to an edge are still absorbed by it, so a
	// black frame at the very start or end does not create a sliver block.
	pics := join(rep(flatPicture(tW, tH, 0), 2), rep(content, 96), rep(flatPicture(tW, tH, 0), 2))
	r = analyze(t, &SliceSource{Events: frameSeq(pics...)}, testConfig())
	wantBoundaries(t, r, 0, 10)
	first, last := r.Diagnostics.Boundaries[0], r.Diagnostics.Boundaries[1]
	if !hasCause(&first, "timeline", "recording_start", "boundary") || !hasCause(&first, SignalDarkUniform, "dark", "boundary") {
		t.Errorf("start boundary causes = %+v", first.Causes)
	}
	if !hasCause(&last, "timeline", "recording_end", "boundary") || !hasCause(&last, SignalDarkUniform, "dark", "boundary") {
		t.Errorf("end boundary causes = %+v", last.Causes)
	}
}

// BOUND-01: recording start and analyzed end are always boundaries; the
// default sources are dark/uniform, silence, resolution, aspect and bumpers;
// logo, caption and scene transitions are individually selectable.
func TestBoundarySources(t *testing.T) {
	s := newScenario(
		blk{D: 30 * sec, Logo: true, Cap: CaptionRollUp, Level: 90},
		blk{D: 30 * sec, Cap: CaptionPopOn, Level: 150, Sep: sepNone},
		blk{D: 30 * sec, Logo: true, Cap: CaptionRollUp, Level: 90, Sep: sepNone},
	)
	// By default none of the three transitions at 30 s and 60 s is a
	// candidate.
	r := s.run(t, nil)
	wantBoundaries(t, r, 0, 90)
	for _, d := range r.Detectors {
		want := d.Signal == SignalDarkUniform || d.Signal == SignalResolution || d.Signal == SignalAspect || d.Signal == SignalAudio
		if d.Boundary != want {
			t.Errorf("detector %s boundary_source = %v, want %v", d.Signal, d.Boundary, want)
		}
	}

	only := func(set func(*BoundaryConfig)) *Config {
		cfg := testConfig()
		set(&cfg.Boundaries)
		return cfg
	}
	r = s.run(t, only(func(b *BoundaryConfig) { b.FromScene = true }))
	wantBoundaries(t, r, 0, 30, 60, 90)
	if b := mustBoundary(t, r, 30*sec); len(b.Causes) != 1 || b.Causes[0].Signal != SignalScene {
		t.Errorf("causes = %+v", b.Causes)
	}
	r = s.run(t, only(func(b *BoundaryConfig) { b.FromCaptions = true }))
	wantBoundaries(t, r, 0, 30, 60, 90)
	if b := mustBoundary(t, r, 30*sec); len(b.Causes) != 1 || b.Causes[0].Signal != SignalCaptions {
		t.Errorf("causes = %+v", b.Causes)
	}
	// The logo cannot be learned from 90 s, so enabling its boundaries has
	// no effect: an unavailable signal contributes no boundary (PIPE-02).
	r = s.run(t, only(func(b *BoundaryConfig) { b.FromLogo = true }))
	wantBoundaries(t, r, 0, 90)

	// Every source off: start and end remain.
	cfg := testConfig()
	def := DefaultConfig().Boundaries
	cfg.Boundaries = BoundaryConfig{FusionTolerance: 100 * time.Millisecond, SpanPolicy: "midpoint",
		ValidateMinEvents: def.ValidateMinEvents, ValidateMinShare: def.ValidateMinShare,
		DarkQuietWindow: def.DarkQuietWindow, DarkQuietLevel: def.DarkQuietLevel}
	r = newScenario(blk{D: 47 * sec}, blk{D: 30 * sec, LB: 4, W: 128, H: 72}, blk{D: 53 * sec}).run(t, cfg)
	wantBoundaries(t, r, 0, 130)
	if n := len(r.Diagnostics.Blocks); n != 1 {
		t.Errorf("%d blocks", n)
	}
}

// BOUND-03: the fusion tolerance is configurable.
func TestFusionToleranceConfigurable(t *testing.T) {
	// Two black flashes whose candidates are 400 ms apart.
	pics := join(rep(content, 100), rep(flatPicture(tW, tH, 0), 2), rep(content, 2), rep(flatPicture(tW, tH, 0), 2), rep(content, 100))
	evs := frameSeq(pics...)
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	wantBoundaries(t, r, 0, 10.1, 10.5, 20.6)
	// BLOCK-02: the 0.4 s block is representable.
	blockSpan(t, r, 10100*ms, 10500*ms)

	cfg := testConfig()
	cfg.Boundaries.FusionTolerance = 500 * time.Millisecond
	r = analyze(t, &SliceSource{Events: evs}, cfg)
	wantBoundaries(t, r, 0, 10.1, 20.6)
	if b := mustBoundary(t, r, 10100*ms); len(b.Causes) != 2 || b.Causes[1].CandidateUS != 10_500_000 {
		t.Errorf("fused causes = %+v", b.Causes)
	}
}

func TestSpanGap(t *testing.T) {
	for _, c := range []struct{ s0, e0, s1, e1, want int64 }{
		{0, 10, 20, 30, 10},
		{20, 30, 0, 10, 10},
		{0, 10, 10, 20, 0},
		{0, 10, 5, 20, 0},
		{5, 6, 0, 100, 0},
	} {
		if got := spanGap(c.s0, c.e0, c.s1, c.e1); got != c.want {
			t.Errorf("spanGap(%d,%d,%d,%d) = %d, want %d", c.s0, c.e0, c.s1, c.e1, got, c.want)
		}
	}
}

func TestNearestFrameTime(t *testing.T) {
	a := fusionAnalysis(DefaultConfig(), 1_000_000, 100_000)
	for _, c := range []struct{ t, want int64 }{
		{0, 0}, {49_999, 0}, {50_000, 0}, {50_001, 100_000}, {100_000, 100_000},
		{949_999, 900_000}, {950_000, 1_000_000}, {999_999, 1_000_000}, {1_000_000, 1_000_000}, {-5, 0},
	} {
		if got := a.nearestFrameTime(c.t); got != c.want {
			t.Errorf("nearestFrameTime(%d) = %d, want %d", c.t, got, c.want)
		}
	}
}
