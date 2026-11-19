// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// AT-18 (degraded inputs), AT-20 (concurrency, cancellation, limits,
// determinism) and the OUT-03 JSON invariants.

// richScenario exercises every detector: logo, captions, an aspect change, a
// coded size change, a coverage gap, ads and a bumper.
func richScenario() *scenario {
	return newScenario(
		blk{D: 70 * sec, Logo: true, Cap: CaptionRollUp},
		blk{D: 30 * sec, Cap: CaptionPopOn, LB: 4},
		blk{D: 15 * sec, Cap: CaptionPopOn, HardCuts: true},
		blk{D: 2 * sec, Pic: card(tW, tH, 8, 6), Sep: sepNone},
		blk{D: 61 * sec, Logo: true, Cap: CaptionRollUp, Sep: sepNone},
		blk{D: 4 * sec, Skip: true},
		blk{D: 53 * sec, Logo: true, Cap: CaptionRollUp, W: 128, H: 72},
	)
}

func richConfig(t *testing.T) *Config {
	cfg := bumperConfig(t)
	cfg.Boundaries.FromLogo = true
	cfg.Boundaries.FromCaptions = true
	cfg.Dark.Mode = "auto"
	cfg.Scoring.RelBrightness, cfg.Scoring.RelAudioEnergy, cfg.Scoring.RelSceneRate = true, true, true
	cfg.Outputs.SkipPaddingStart = 500 * ms
	return cfg
}

// AT-18 / RESULT-01, RESULT-02, PIPE-02: a recording with no ads and no
// usable classification evidence completes, with every detector reporting
// its state, and is unknown rather than fabricated program.
func TestNoAdNoEvidenceRecording(t *testing.T) {
	s := newScenario(blk{D: 137 * sec})
	s.noAudio = true
	r := s.run(t, nil)
	if !r.Complete || r.Error != nil || len(r.AdBreaks) != 0 {
		t.Fatalf("complete %v error %v breaks %d", r.Complete, r.Error, len(r.AdBreaks))
	}
	wantTimeline(t, r, "U0-137")
	if got := mustJSON(t, r.Segments[0].Reasons); got != `["insufficient_evidence"]` {
		t.Errorf("reasons = %s", got)
	}
	want := map[string][2]string{
		SignalDarkUniform: {StatusUsable, ""},
		SignalLogo:        {StatusUnavailable, "no_persistent_candidate"},
		SignalScene:       {StatusUsable, ""},
		SignalResolution:  {StatusUsable, ""},
		SignalCaptions:    {StatusUnavailable, "no_caption_data"},
		SignalAspect:      {StatusUsable, ""},
		SignalAudio:       {StatusUnavailable, "no_audio"},
		SignalBumpers:     {StatusUnavailable, "no_templates"},
	}
	if len(r.Detectors) != len(want) {
		t.Fatalf("detectors = %s", mustJSON(t, r.Detectors))
	}
	order := []string{SignalDarkUniform, SignalLogo, SignalScene, SignalResolution, SignalCaptions, SignalAspect, SignalAudio, SignalBumpers}
	for i, d := range r.Detectors {
		if d.Signal != order[i] {
			t.Errorf("detector %d is %s, want %s", i, d.Signal, order[i])
		}
		w := want[d.Signal]
		if d.Status != w[0] || d.Reason != w[1] {
			t.Errorf("detector %s: status %s reason %q, want %s %q", d.Signal, d.Status, d.Reason, w[0], w[1])
		}
		// PIPE-02: an unavailable signal contributes no boundary, and its
		// coverage is the documented null, never an invented zero (OUT-03).
		if d.Status != StatusUsable && (d.Boundary || (d.Signal != SignalLogo && d.CoverageUS != nil)) {
			t.Errorf("detector %s: boundary %v coverage %v", d.Signal, d.Boundary, d.CoverageUS)
		}
	}
	// A program label needs positive evidence: there is none.
	for _, b := range r.Diagnostics.Blocks {
		if b.Label == LabelProgram || len(b.ProgramEvidence) != 0 {
			t.Errorf("block %s: label %s evidence %v", b.ID, b.Label, b.ProgramEvidence)
		}
	}
	// With a no-ad recording that does have program evidence, the label is
	// program and there are still no breaks.
	s = newScenario(blk{D: 137 * sec, Cap: CaptionRollUp})
	r = s.run(t, nil)
	if len(r.AdBreaks) != 0 {
		t.Fatalf("breaks = %d", len(r.AdBreaks))
	}
	wantTimeline(t, r, "P0-137")
	if got := mustJSON(t, r.Segments[0].Reasons); got != `["stable_long_interval"]` {
		t.Errorf("reasons = %s", got)
	}
}

// PIPE-02: every detector can be switched off independently; with all of
// them off the analysis still completes and everything is unknown.
func TestAllDetectorsDisabled(t *testing.T) {
	cfg := richConfig(t)
	cfg.Signals = SignalsConfig{}
	r := richScenario().run(t, cfg)
	for _, d := range r.Detectors {
		if d.Status != StatusDisabled || d.Boundary || d.Events != 0 || d.CoverageUS != nil {
			t.Errorf("detector = %+v", d)
		}
	}
	// Only the timeline's own structure remains: the coverage gap.
	wantBoundaries(t, r, 0, 178, 182, 235)
	wantTimeline(t, r, "U0-235")
	if len(r.Calibration.Logo) != 0 {
		t.Errorf("logo calibration = %+v", r.Calibration.Logo)
	}

	// Each switch alone disables exactly its detector.
	signals := []string{SignalDarkUniform, SignalLogo, SignalScene, SignalResolution, SignalCaptions, SignalAspect, SignalAudio, SignalBumpers}
	fields := reflect.ValueOf(&SignalsConfig{}).Elem().NumField()
	if fields != len(signals) {
		t.Fatalf("SignalsConfig has %d switches, test knows %d", fields, len(signals))
	}
	for i, sig := range signals {
		cfg := richConfig(t)
		reflect.ValueOf(&cfg.Signals).Elem().Field(i).SetBool(false)
		r := richScenario().run(t, cfg)
		for _, d := range r.Detectors {
			if (d.Status == StatusDisabled) != (d.Signal == sig) {
				t.Errorf("switch %d (%s) off: detector %s is %s", i, sig, d.Signal, d.Status)
			}
		}
	}
}

// OUT-01..03: the canonical JSON of a result that exercises everything.
func TestResultJSONShape(t *testing.T) {
	r := richScenario().run(t, richConfig(t))
	js := mustJSON(t, r)
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(js), &top); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"schema_version", "product", "complete", "time_unit", "duration_us", "coverage", "input", "tracks",
		"adapter", "timing", "config", "feature_identity", "detectors", "warnings", "calibration",
		"baselines", "segments", "ad_breaks", "ad_units", "skip_intervals", "diagnostics",
	} {
		if _, ok := top[key]; !ok {
			t.Errorf("result JSON lacks %q", key)
		}
	}
	if string(top["schema_version"]) != `"1"` || string(top["time_unit"]) != `"microseconds"` || string(top["complete"]) != "true" {
		t.Errorf("header = %s %s %s", top["schema_version"], top["time_unit"], top["complete"])
	}
	if _, ok := top["error"]; ok {
		t.Errorf("a complete result carries an error")
	}
	// The result round-trips through its own types without loss.
	var back Result
	if err := json.Unmarshal([]byte(js), &back); err != nil {
		t.Fatalf("result JSON does not decode into Result: %v", err)
	}
	if again := mustJSON(t, &back); again != js {
		t.Errorf("result JSON does not round-trip")
	}
	if r.Input.Kind != "observations" || r.Adapter.Name != "caller" {
		t.Errorf("input %+v adapter %+v", r.Input, r.Adapter)
	}
	if !strings.HasPrefix(r.Config.Digest, "sha256:") || len(r.Config.Digest) != 71 || !strings.HasPrefix(r.FeatureID, "sha256:") || r.Config.SchemaVersion != ConfigSchemaVersion {
		t.Errorf("config identity %q feature identity %q", r.Config.Digest, r.FeatureID)
	}
	if r.Config.Effective["scoring.threshold"] != "1.5" || r.Config.Effective["dark.mode"] != "auto" {
		t.Errorf("effective config = %v", r.Config.Effective)
	}

	// The scenario really does exercise everything.
	if len(r.AdBreaks) == 0 || len(r.Diagnostics.Events.Bumpers) == 0 || len(r.Diagnostics.Events.Resolution) == 0 || len(r.Coverage.Gaps) == 0 || len(r.Diagnostics.Events.Captions) == 0 || len(r.Diagnostics.Events.Logo) == 0 {
		t.Errorf("rich scenario is missing a feature: %s", timeline(r))
	}
	// OUT-02: segments link to blocks, breaks to units, by stable IDs.
	blocks := map[string]Block{}
	for _, b := range r.Diagnostics.Blocks {
		blocks[b.ID] = b
	}
	for _, s := range r.Segments {
		at := s.StartUS
		for _, id := range s.BlockIDs {
			b, ok := blocks[id]
			if !ok || b.StartUS != at || b.Label != s.Label {
				t.Errorf("segment [%d,%d) %s: block %s does not continue it", s.StartUS, s.EndUS, s.Label, id)
			}
			at = b.EndUS
		}
		if at != s.EndUS {
			t.Errorf("segment [%d,%d): blocks end at %d", s.StartUS, s.EndUS, at)
		}
	}
}

// AT-14, OUT-03: a degenerate recording (black, digitally silent, static)
// with every optional rule and auto calibration enabled still yields finite,
// valid JSON.
func TestDegenerateRecordingIsFinite(t *testing.T) {
	var evs []Event
	black := flatPicture(tW, tH, 0)
	for i := 0; i < 1370; i++ {
		t0 := int64(i) * 100_000
		evs = append(evs, videoFrame(t0, 100_000, black))
		evs = append(evs, Event{Kind: EventAudio, Audio: []AudioWindow{{StartUS: t0, DurationUS: 100_000}}})
	}
	cfg := testConfig()
	cfg.Dark.Mode = "auto"
	s := &cfg.Scoring
	s.RelBrightness, s.RelNonuniformity, s.RelAudioEnergy, s.RelSilence, s.RelSceneRate = true, true, true, true, true
	cfg.Baselines.FloorBrightness, cfg.Baselines.FloorNonuniformity, cfg.Baselines.FloorSilence, cfg.Baselines.FloorSceneRate = 0, 0, 0, 0
	r := analyze(t, &SliceSource{Events: evs}, cfg) // checkInvariants encodes the JSON
	if len(r.AdBreaks) != 0 {
		t.Errorf("degenerate recording produced ads: %s", timeline(r))
	}
	b := r.Diagnostics.Blocks[0]
	if *b.AudioEnergyDBFS != -160 || *b.SilenceFraction != 1 || *b.Brightness != 0 {
		t.Errorf("block = %s", mustJSON(t, b))
	}
	if ev := r.Diagnostics.Events.Silence; len(ev) != 1 || ev[0].PeakDBFS != -160 {
		t.Errorf("silence = %+v", ev)
	}
}

// AT-20 / QUAL-01: repeated runs over the same observations, configuration
// and assets produce byte-identical results.
func TestDeterminism(t *testing.T) {
	src := richScenario().source()
	cfg := richConfig(t)
	a := mustJSON(t, analyze(t, src, cfg))
	for i := 0; i < 3; i++ {
		if b := mustJSON(t, analyze(t, src, cfg)); a != b {
			t.Fatalf("run %d differs from the first run", i+2)
		}
	}
	// A freshly built, equal scenario gives the same result too.
	if b := mustJSON(t, analyze(t, richScenario().source(), richConfig(t))); a != b {
		t.Errorf("equal inputs built separately give different results")
	}
	// Worker settings do not alter decisions (QUAL-01).
	cfg2 := richConfig(t)
	cfg2.Resources.Workers = 7
	r2 := analyze(t, src, cfg2)
	if r2.Config.Effective["resources.workers"] != "7" {
		t.Errorf("effective resources.workers = %q", r2.Config.Effective["resources.workers"])
	}
	r2.Config.Effective["resources.workers"] = "0"
	if b := mustJSON(t, r2); a != b {
		t.Errorf("resources.workers changed the result")
	}
}

// AT-20 / API-03: concurrent analyses on one shared Config, shared assets
// and one shared SliceSource are isolated. Run with -race.
func TestConcurrentAnalysesAreIsolated(t *testing.T) {
	src := richScenario().source()
	cfg := richConfig(t)
	cfg.LogoAssetData = learnedAsset(t)
	before := cfg.Clone()
	want := mustJSON(t, analyze(t, src, cfg))

	// A second, different analysis runs at the same time on its own source.
	other := logoScenario().source()
	wantOther := mustJSON(t, analyze(t, other, cfg))
	if want == wantOther {
		t.Fatalf("the two scenarios should differ")
	}

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, 2*workers)
	for i := 0; i < workers; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			var learned int32
			r, err := Analyze(context.Background(), src, cfg, &Options{OnLogoLearned: func(*LogoAsset) { atomic.AddInt32(&learned, 1) }})
			if err != nil {
				errs <- err
				return
			}
			js, _ := json.Marshal(r)
			if string(js) != want {
				errs <- errors.New("concurrent result differs from the serial result")
			}
		}()
		go func() {
			defer wg.Done()
			r, err := Analyze(context.Background(), other, cfg, nil)
			if err != nil {
				errs <- err
				return
			}
			js, _ := json.Marshal(r)
			if string(js) != wantOther {
				errs <- errors.New("concurrent result of the second recording differs from its serial result")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	// Analyze never modifies the caller's configuration.
	if !reflect.DeepEqual(before, cfg) {
		t.Errorf("Analyze modified the shared configuration")
	}
}

// testSource wraps a SliceSource with failure injection and bookkeeping.
type testSource struct {
	events     []Event
	info       *MediaInfo
	openErr    error
	failAt     int // Next call index at which nextErr is returned (0 = never)
	nextErr    error
	panicAt    int // Next call index that panics (0 = never)
	panicOpen  bool
	nilInfo    bool
	onNext     func(n int)
	opens      int32
	closes     int32
	lastStream *testStream
}

type testStream struct {
	src *testSource
	ctx context.Context
	n   int
}

func (s *testSource) Open(ctx context.Context, req *OpenRequest) (Stream, error) {
	atomic.AddInt32(&s.opens, 1)
	if s.panicOpen {
		panic("open exploded")
	}
	if s.openErr != nil {
		return nil, s.openErr
	}
	if req.Warn == nil {
		panic("OpenRequest.Warn is nil")
	}
	s.lastStream = &testStream{src: s, ctx: ctx}
	return s.lastStream, nil
}

func (st *testStream) Info() *MediaInfo {
	if st.src.nilInfo {
		return nil
	}
	if st.src.info != nil {
		return st.src.info
	}
	return &MediaInfo{Adapter: AdapterInfo{Name: "test", Version: "0"}}
}

func (st *testStream) Next(ev *Event) error {
	st.n++
	if st.src.onNext != nil {
		st.src.onNext(st.n)
	}
	if st.src.panicAt != 0 && st.n == st.src.panicAt {
		panic(fmt.Sprintf("decoder exploded at event %d", st.n))
	}
	if st.src.failAt != 0 && st.n == st.src.failAt {
		return st.src.nextErr
	}
	if st.n > len(st.src.events) {
		return io.EOF
	}
	*ev = st.src.events[st.n-1]
	return nil
}

func (st *testStream) Close() error {
	atomic.AddInt32(&st.src.closes, 1)
	return nil
}

func midwayEvents() []Event {
	return newScenario(blk{D: 47 * sec}, blk{D: 30 * sec}, blk{D: 53 * sec}).events()
}

// AT-20 / API-03, API-04: cancellation returns a distinguishable error,
// closes the stream and never yields a complete result.
func TestCancellation(t *testing.T) {
	// Already canceled: nothing is opened.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := &testSource{events: midwayEvents()}
	r, err := Analyze(ctx, src, testConfig(), nil)
	if !errors.Is(err, ErrCanceled) || !IsCanceled(err) || KindOf(err) != KindCanceled || !errors.Is(err, context.Canceled) || r != nil {
		t.Fatalf("pre-canceled: result %v err %v", r, err)
	}
	if src.opens != 0 {
		t.Errorf("source opened after cancellation")
	}

	// Canceled part-way: a canceled error plus an explicitly partial result.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	src = &testSource{events: midwayEvents(), onNext: func(n int) {
		if n == 1000 {
			cancel()
		}
	}}
	r, err = Analyze(ctx, src, testConfig(), nil)
	if !errors.Is(err, ErrCanceled) || !IsCanceled(err) {
		t.Fatalf("mid-way cancel: err %v", err)
	}
	if src.closes != 1 {
		t.Errorf("stream closed %d times", src.closes)
	}
	if r == nil {
		t.Fatalf("no partial result")
	}
	if r.Complete || r.Error == nil || r.Error.Kind != "canceled" || r.DurationUS >= 130_000_000 {
		t.Errorf("partial result: complete %v error %+v duration %d", r.Complete, r.Error, r.DurationUS)
	}
	checkInvariants(t, r)
	// Cancellation is noticed within a bounded number of events.
	if n := src.lastStream.n; n > 1000+64 {
		t.Errorf("analysis read %d events after cancellation at 1000", n)
	}

	// The SliceSource itself honours cancellation.
	ctx, cancel = context.WithCancel(context.Background())
	var frames int
	_, err = Analyze(ctx, &SliceSource{Events: midwayEvents()}, testConfig(), &Options{FrameObserver: func(FrameMeasurement) {
		if frames++; frames == 300 {
			cancel()
		}
	}})
	if !IsCanceled(err) {
		t.Errorf("SliceSource cancel: err %v", err)
	}

	// A deadline is a cancellation too.
	ctx, cancel = context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	_, err = Analyze(ctx, &SliceSource{Events: midwayEvents()}, testConfig(), nil)
	if !errors.Is(err, ErrCanceled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("deadline: err %v", err)
	}
	if IsCanceled(errors.New("x")) || IsCanceled(nil) {
		t.Errorf("IsCanceled on unrelated errors")
	}
}

// AT-20 / API-04, QUAL-04: a stream failure part-way returns a typed error
// and a partial result that is explicitly incomplete.
func TestStreamErrorGivesPartialResult(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind ErrorKind
		is   error
	}{
		{"media", Errorf(KindMedia, "decoder failed: %w", io.ErrUnexpectedEOF), KindMedia, ErrMedia},
		{"io", Errorf(KindIO, "read failed"), KindIO, ErrIO},
		{"unsupported", Errorf(KindUnsupported, "HDR transfer"), KindUnsupported, ErrUnsupported},
		{"foreign", errors.New("disk on fire"), KindInternal, ErrInternal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := &testSource{events: midwayEvents(), failAt: 2000, nextErr: c.err}
			r, err := Analyze(context.Background(), src, testConfig(), nil)
			if err == nil || KindOf(err) != c.kind || !errors.Is(err, c.is) {
				t.Fatalf("err = %v (kind %v)", err, KindOf(err))
			}
			if c.name == "media" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("wrapped cause lost: %v", err)
			}
			if c.name == "foreign" && !strings.Contains(err.Error(), "disk on fire") {
				t.Errorf("foreign error text lost: %v", err)
			}
			if r == nil {
				t.Fatalf("no partial result")
			}
			if r.Complete || r.Error == nil || r.Error.Kind != c.kind.String() || r.Error.Message == "" {
				t.Errorf("partial result: complete %v error %+v", r.Complete, r.Error)
			}
			if r.DurationUS <= 0 || r.DurationUS >= 130_000_000 {
				t.Errorf("partial duration = %d", r.DurationUS)
			}
			checkInvariants(t, r)
			if src.closes != 1 {
				t.Errorf("stream closed %d times", src.closes)
			}
			// The failure is visible in the JSON, never a plausible complete
			// result (QUAL-04).
			if js := mustJSON(t, r); !strings.Contains(js, `"complete":false`) || !strings.Contains(js, `"error":{"kind":"`+c.kind.String()+`"`) {
				t.Errorf("partial JSON does not show the failure")
			}
		})
	}

	// A failure before any frame: no result at all.
	src := &testSource{events: midwayEvents(), failAt: 1, nextErr: Errorf(KindIO, "cannot read")}
	r, err := Analyze(context.Background(), src, testConfig(), nil)
	if !errors.Is(err, ErrIO) || r != nil {
		t.Errorf("early failure: result %v err %v", r, err)
	}
	// Open fails: typed error, no result.
	src = &testSource{openErr: Errorf(KindUnsupported, "no such container")}
	r, err = Analyze(context.Background(), src, testConfig(), nil)
	if !errors.Is(err, ErrUnsupported) || r != nil {
		t.Errorf("open failure: result %v err %v", r, err)
	}
	// A source that reports itself incomplete makes the result incomplete
	// without an error (degraded package input).
	src = &testSource{events: midwayEvents(), info: &MediaInfo{Incomplete: true}}
	r, err = Analyze(context.Background(), src, testConfig(), nil)
	if err != nil || r == nil || r.Complete || r.Error != nil {
		t.Errorf("incomplete source: complete %v err %v", r != nil && r.Complete, err)
	}
}

// AT-20 / API-05: panics inside a Source become errors; the host process is
// never terminated and the stream is still closed.
func TestSourcePanicsBecomeErrors(t *testing.T) {
	src := &testSource{events: midwayEvents(), panicAt: 500}
	r, err := Analyze(context.Background(), src, testConfig(), nil)
	if err == nil || KindOf(err) != KindInternal || !errors.Is(err, ErrInternal) || !strings.Contains(err.Error(), "decoder exploded at event 500") || r != nil {
		t.Fatalf("panic in Next: result %v err %v", r, err)
	}
	if src.closes != 1 {
		t.Errorf("stream closed %d times after a panic", src.closes)
	}
	src = &testSource{panicOpen: true}
	r, err = Analyze(context.Background(), src, testConfig(), nil)
	if KindOf(err) != KindInternal || err == nil || r != nil {
		t.Fatalf("panic in Open: result %v err %v", r, err)
	}
	src = &testSource{events: midwayEvents(), nilInfo: true}
	r, err = Analyze(context.Background(), src, testConfig(), nil)
	if err == nil || r != nil || src.closes != 1 {
		t.Fatalf("nil media info: result %v err %v closes %d", r, err, src.closes)
	}
	// Panicking callbacks are contained as well.
	r, err = Analyze(context.Background(), &SliceSource{Events: midwayEvents()}, testConfig(), &Options{FrameObserver: func(FrameMeasurement) { panic("observer bug") }})
	if KindOf(err) != KindInternal || err == nil || r != nil {
		t.Fatalf("panic in a callback: result %v err %v", r, err)
	}
}

// API-01, CFG-03: argument and configuration errors are reported before any
// media is opened.
func TestAnalyzeArguments(t *testing.T) {
	if r, err := Analyze(context.Background(), nil, testConfig(), nil); !errors.Is(err, ErrInvalidConfig) || r != nil {
		t.Errorf("nil source: %v", err)
	}
	src := &testSource{events: midwayEvents()}
	cfg := testConfig()
	cfg.Scoring.Threshold = 0.5
	if r, err := Analyze(context.Background(), src, cfg, nil); !errors.Is(err, ErrInvalidConfig) || r != nil || src.opens != 0 {
		t.Errorf("invalid config: err %v opens %d", err, src.opens)
	}
	// A nil configuration means the defaults; nil options are fine.
	r, err := Analyze(context.Background(), &SliceSource{Events: midwayEvents()}, nil, nil)
	if err != nil || r.Diagnostics != nil || timeline(r) != "U0-47 A47-77 U77-130" {
		t.Errorf("nil config: %v", err)
	}
	if r.Config.Effective["outputs.diagnostics"] != "summary" {
		t.Errorf("default diagnostics level = %q", r.Config.Effective["outputs.diagnostics"])
	}
	// The stream's media description is carried into the result.
	info := &MediaInfo{
		Adapter: AdapterInfo{Name: "test", Version: "9"},
		Input:   InputInfo{Kind: "file", Path: "/media/show.ts", Identity: "123:456", Container: "mpegts"},
		Tracks:  TrackInfo{Video: VideoTrackInfo{Index: 0, Codec: "h264", CodedWidth: 1920, CodedHeight: 1080}, Audio: &AudioTrackInfo{Index: 1, Codec: "ac3", Channels: 6}},
	}
	r, err = Analyze(context.Background(), &testSource{events: midwayEvents(), info: info}, testConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Adapter.Version != "9" || r.Input.Path != "/media/show.ts" || r.Tracks.Video.Codec != "h264" || r.Tracks.Video.CodedWidth != 1920 || r.Tracks.Audio.Codec != "ac3" || r.Tracks.Video.AnalysisWidth != tW {
		t.Errorf("media info not carried: %s", mustJSON(t, r.Tracks))
	}
}

// AT-20 / QUAL-02: exceeding resources.memory_budget is an explicit
// resource-limit error, not silent degradation.
func TestMemoryBudgetExceeded(t *testing.T) {
	// 4096 tiny frames and one audio delivery of 800,000 level windows
	// (19.2 MB of compact storage against the 16 MiB minimum budget).
	evs := make([]Event, 0, 5000)
	ws := make([]AudioWindow, 800_000)
	for i := range ws {
		ws[i] = AudioWindow{StartUS: int64(i) * 500, DurationUS: 500, PeakMS: speechMS, MeanMS: speechMS}
	}
	evs = append(evs, videoFrame(0, 100_000, content), Event{Kind: EventAudio, Audio: ws})
	for i := 1; i < 4200; i++ {
		evs = append(evs, videoFrame(int64(i)*100_000, 100_000, content))
	}
	cfg := testConfig()
	cfg.Resources.MemoryBudget = 16 << 20
	cfg.Signals.Logo = false
	r, err := Analyze(context.Background(), &SliceSource{Events: evs}, cfg, nil)
	if !errors.Is(err, ErrResourceLimit) || KindOf(err) != KindResourceLimit || !strings.Contains(err.Error(), "memory_budget") {
		t.Fatalf("err = %v, want a resource-limit error", err)
	}
	if r == nil || r.Complete || r.Error == nil || r.Error.Kind != "resource_limit" {
		t.Errorf("partial result = %+v", r)
	}
	// With the default budget the same input completes.
	cfg.Resources.MemoryBudget = 512 << 20
	if _, err := Analyze(context.Background(), &SliceSource{Events: evs}, cfg, nil); err != nil {
		t.Errorf("default budget: %v", err)
	}
}

// API-04: the error type.
func TestErrorKinds(t *testing.T) {
	names := map[ErrorKind]string{
		KindInternal: "internal", KindInvalidConfig: "invalid_config", KindUnsupported: "unsupported_input",
		KindMedia: "media_failure", KindTiming: "unusable_timing", KindIO: "io",
		KindCanceled: "canceled", KindResourceLimit: "resource_limit",
	}
	sentinels := map[ErrorKind]error{
		KindInternal: ErrInternal, KindInvalidConfig: ErrInvalidConfig, KindUnsupported: ErrUnsupported,
		KindMedia: ErrMedia, KindTiming: ErrTiming, KindIO: ErrIO, KindCanceled: ErrCanceled, KindResourceLimit: ErrResourceLimit,
	}
	for k, name := range names {
		if k.String() != name {
			t.Errorf("kind %d name %q, want %q", k, k.String(), name)
		}
		err := Errorf(k, "problem %d", 7)
		if KindOf(err) != k || err.Error() != name+": problem 7" {
			t.Errorf("Errorf(%s) = %q kind %v", name, err.Error(), KindOf(err))
		}
		for k2, s := range sentinels {
			if errors.Is(err, s) != (k == k2) {
				t.Errorf("errors.Is(%s error, %s sentinel) = %v", name, names[k2], errors.Is(err, s))
			}
		}
		// Wrapped by a caller, the kind is still found.
		if KindOf(fmt.Errorf("outer: %w", err)) != k {
			t.Errorf("kind lost through wrapping")
		}
	}
	if ErrorKind(200).String() != "internal" {
		t.Errorf("out-of-range kind name")
	}
	cause := errors.New("root cause")
	err := Errorf(KindIO, "open %s: %w", "x.ts", cause)
	if !errors.Is(err, cause) || errors.Unwrap(err) != cause || err.Error() != "io: open x.ts: root cause" {
		t.Errorf("wrapping: %v", err)
	}
	if (&Error{Kind: KindMedia}).Error() != "media_failure" || (&Error{Kind: KindMedia, Err: cause}).Error() != "media_failure: root cause" {
		t.Errorf("Error() without a message")
	}
	if KindOf(context.Canceled) != KindCanceled || KindOf(context.DeadlineExceeded) != KindCanceled || KindOf(errors.New("x")) != KindInternal {
		t.Errorf("KindOf foreign errors")
	}
	// Two different errors of the same kind are not "the same error".
	if errors.Is(Errorf(KindIO, "a"), Errorf(KindIO, "b")) {
		t.Errorf("distinct errors compare equal")
	}
}

// R6 / API-04: cancellation that arrives after extraction, while the
// recording is being classified, still ends the analysis with the canceled
// error and without a result. It must not return a complete one.
func TestCancellationDuringClassification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stages []string
	opts := &Options{Progress: func(p Progress) {
		stages = append(stages, p.Stage)
		if p.Stage == StageClassify {
			cancel()
		}
	}}
	r, err := Analyze(ctx, &testSource{events: midwayEvents()}, testConfig(), opts)
	if !errors.Is(err, ErrCanceled) || !IsCanceled(err) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel during classification: err %v", err)
	}
	if r != nil {
		t.Fatalf("cancel during classification returned a result (complete=%v)", r.Complete)
	}
	if n := len(stages); n == 0 || stages[n-1] != StageClassify {
		t.Errorf("progress stages end with %v, want classify and no done report", stages)
	}

	// The same analysis without cancellation completes.
	r, err = Analyze(context.Background(), &testSource{events: midwayEvents()}, testConfig(), nil)
	if err != nil || r == nil || !r.Complete {
		t.Fatalf("uncanceled control: result %v err %v", r, err)
	}
}

// R6: the hypothesis loop polls for cancellation, so a long classification
// is interrupted inside the stage and not only between stages.
func TestHypothesisLoopPollsCancellation(t *testing.T) {
	blocks := make([]simBlock, 0, 400)
	for i := 0; i < 400; i++ {
		blocks = append(blocks, simBlock{us: 500_000})
	}
	a := simulate(nil, blocks)
	polls := 0
	a.stop = func() bool { polls++; return polls > 2 }
	a.stopped = false
	a.scoreAndSelect()
	if !a.stopped || polls != 3 {
		t.Errorf("stopped=%v after %d polls, want the loop to stop on the third poll", a.stopped, polls)
	}
}

// R5 / QUAL-02: the budget is checked at every growth point, not every
// 4096th frame: a short input (well under 4096 frames) whose audio alone
// exceeds the budget fails, and so does a classification whose structures
// would push the total over it.
func TestMemoryBudgetCheckedAtEveryGrowthPoint(t *testing.T) {
	ws := make([]AudioWindow, 800_000) // 19.2 MB of level windows
	for i := range ws {
		ws[i] = AudioWindow{StartUS: int64(i) * 500, DurationUS: 500, PeakMS: speechMS, MeanMS: speechMS}
	}
	evs := []Event{videoFrame(0, 100_000, content), {Kind: EventAudio, Audio: ws}}
	for i := 1; i < 100; i++ {
		evs = append(evs, videoFrame(int64(i)*100_000, 100_000, content))
	}
	cfg := testConfig()
	cfg.Resources.MemoryBudget = 16 << 20
	cfg.Signals.Logo = false
	_, err := Analyze(context.Background(), &SliceSource{Events: evs}, cfg, nil)
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("100 frames with 19 MB of audio under a 16 MiB budget: err = %v, want a resource limit", err)
	}

	// Classification: blocks, selection arrays and the hypotheses that
	// diagnostics retain are accounted before scoring.
	cfg = testConfig()
	cfg.Resources.MemoryBudget = 16 << 20
	cfg.Outputs.Diagnostics = DiagnosticsFull
	cfg.Outputs.MaxHypotheses = 100_000_000
	blocks := make([]simBlock, 4000)
	for i := range blocks {
		blocks[i] = simBlock{us: 500_000}
	}
	a := simulate(cfg, blocks)
	a.stop, a.err = func() bool { return false }, nil
	a.checkClassificationBudget()
	if !errors.Is(a.err, ErrResourceLimit) || !strings.Contains(a.err.Error(), "classification") {
		t.Fatalf("classification over the budget: err %v", a.err)
	}
	cfg.Outputs.MaxHypotheses = 1000
	a.err = nil
	a.checkClassificationBudget()
	if a.err != nil {
		t.Errorf("bounded retention: %v", a.err)
	}
	// The whole pipeline reports the limit without a result.
	cfg.Outputs.MaxHypotheses = 100_000_000
	r, err := Analyze(context.Background(), &testSource{events: newScenario(manyBlocks(300)...).events()}, cfg, nil)
	if !errors.Is(err, ErrResourceLimit) || r != nil {
		t.Errorf("Analyze over the classification budget: result %v err %v", r != nil, err)
	}
}

func manyBlocks(n int) []blk {
	out := make([]blk, n)
	for i := range out {
		out[i] = blk{D: 2 * sec}
	}
	return out
}

// Phase 6: a caller's luminance plane whose line stride is wider than the
// picture (padding at the end of each line) measures exactly as the same
// picture stored without padding.
func TestStrideWiderThanWidth(t *testing.T) {
	evs := newScenario(blk{D: 47 * sec, Logo: true}, blk{D: 30 * sec}, blk{D: 53 * sec, Logo: true}).events()
	padded := make([]Event, len(evs))
	copy(padded, evs)
	const pad = 13
	for i := range padded {
		f := &padded[i].Video
		if padded[i].Kind != EventVideo {
			continue
		}
		stride := f.Width + pad
		buf := make([]byte, stride*f.Height)
		for y := 0; y < f.Height; y++ {
			copy(buf[y*stride:], f.Luma[y*f.Stride:y*f.Stride+f.Width])
			for x := f.Width; x < stride; x++ {
				buf[y*stride+x] = 255 // padding must never be measured
			}
		}
		f.Luma, f.Stride = buf, stride
	}
	cfg := testConfig()
	cfg.Outputs.Diagnostics = DiagnosticsFull
	a := analyze(t, &SliceSource{Events: evs}, cfg)
	b := analyze(t, &SliceSource{Events: padded}, cfg)
	if mustJSON(t, a.Segments) != mustJSON(t, b.Segments) || mustJSON(t, a.Diagnostics.Blocks) != mustJSON(t, b.Diagnostics.Blocks) {
		t.Errorf("a padded stride changed the measurements")
	}
}

// Phase 6 / C7: recording margins never change logo learning; only break
// limits near the edges.
func TestLogoLearningIgnoresMargins(t *testing.T) {
	base := logoScenario().run(t, nil)
	cfg := testConfig()
	cfg.Breaks.MarginStart, cfg.Breaks.MarginEnd = 60*sec, 60*sec
	cfg.Breaks.MarkAdjacentProgram = true
	r := logoScenario().run(t, cfg)
	if mustJSON(t, r.Calibration.Logo) != mustJSON(t, base.Calibration.Logo) || logoStates(r) != logoStates(base) {
		t.Errorf("margins changed logo learning: %s vs %s", mustJSON(t, r.Calibration.Logo), mustJSON(t, base.Calibration.Logo))
	}
}
