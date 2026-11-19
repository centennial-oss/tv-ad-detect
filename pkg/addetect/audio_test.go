// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// AT-09: silence and audio level (AUDIO-01..03, MEDIA-04).

func silenceSpans(r *Result) string {
	parts := make([]string, len(r.Diagnostics.Events.Silence))
	for i, s := range r.Diagnostics.Events.Silence {
		parts[i] = fmt.Sprintf("[%g,%g)", float64(s.StartUS)/1e6, float64(s.EndUS)/1e6)
	}
	return strings.Join(parts, " ")
}

// audioRecording is 20 s of content whose 20 ms audio windows come from fn
// (ok=false: no window, a hole).
func audioRecording(seconds int, fn func(t int64) (peak, mean float32, ok bool)) []Event {
	var evs []Event
	for i := 0; i < seconds*10; i++ {
		t := int64(i) * 100_000
		evs = append(evs, videoFrame(t, 100_000, content))
		var ws []AudioWindow
		for a := t; a < t+100_000; a += 20_000 {
			if peak, mean, ok := fn(a); ok {
				ws = append(ws, AudioWindow{StartUS: a, DurationUS: 20_000, PeakMS: peak, MeanMS: mean})
			}
		}
		evs = append(evs, Event{Kind: EventAudio, Audio: ws})
	}
	return evs
}

func msOf(dbfs float64) float32 { return float32(math.Pow(10, dbfs/10)) }

func between(t, s, e int64) bool { return t >= s && t < e }

// levelRecording exercises the silence level, the minimum duration, the
// loudest-channel rule and holes.
func levelRecording() []Event {
	return audioRecording(20, func(t int64) (float32, float32, bool) {
		switch {
		case between(t, 3_000_000, 3_080_000): // 80 ms: below audio.min_silence
			return silenceMS, silenceMS, true
		case between(t, 5_000_000, 5_100_000): // exactly 100 ms
			return silenceMS, silenceMS, true
		case between(t, 7_000_000, 7_500_000): // -49 dBFS: above the -50 dBFS level
			return msOf(-49), msOf(-49), true
		case between(t, 9_000_000, 9_500_000): // -51 dBFS: silent
			return msOf(-51), msOf(-51), true
		case between(t, 11_000_000, 11_500_000): // quiet on average, one loud channel
			return speechMS, silenceMS, true
		case between(t, 13_200_000, 13_300_000): // a hole inside a silence
			return 0, 0, false
		case between(t, 13_000_000, 13_500_000):
			return silenceMS, silenceMS, true
		case between(t, 15_000_000, 16_000_000): // a hole in speech
			return 0, 0, false
		}
		return speechMS, speechMS, true
	})
}

// AT-09 / AUDIO-01, AUDIO-02: silence must meet the level and the minimum
// duration; a loud channel prevents silence; missing windows are unknown and
// never silence.
func TestSilenceLevelDurationAndHoles(t *testing.T) {
	r := analyze(t, &SliceSource{Events: levelRecording()}, testConfig())
	if got, want := silenceSpans(r), "[5,5.1) [9,9.5) [13,13.2) [13.3,13.5)"; got != want {
		t.Fatalf("silence events = %s\n                want %s", got, want)
	}
	ev := r.Diagnostics.Events.Silence
	if ev[0].Windows != 5 || ev[1].Windows != 25 {
		t.Errorf("windows = %d, %d", ev[0].Windows, ev[1].Windows)
	}
	if math.Abs(ev[1].PeakDBFS-(-51)) > 0.01 || math.Abs(ev[0].PeakDBFS-(-80)) > 0.01 {
		t.Errorf("peak levels = %v, %v", ev[1].PeakDBFS, ev[0].PeakDBFS)
	}
	// MEDIA-04: the two holes (0.1 s and 1 s) are not measured audio.
	d := detector(t, r, SignalAudio)
	if d.Status != StatusUsable || *d.CoverageUS != 20_000_000-1_100_000 || d.Events != 4 || !d.Boundary {
		t.Errorf("detector = %+v coverage %d", d, *d.CoverageUS)
	}
	// AUDIO-02 / BOUND-02: each silence-only span is one candidate at its
	// midpoint; it supports a boundary but is not ad evidence.
	wantBoundaries(t, r, 0, 5.05, 9.25, 13.1, 13.4, 20)
	for _, b := range r.Diagnostics.Boundaries[1:5] {
		if len(b.Causes) != 1 || !hasCause(&b, SignalAudio, "silence", "boundary") || len(b.Evidence) != 0 {
			t.Errorf("silence boundary = %+v", b)
		}
	}
	if b := mustBoundary(t, r, us2d(9_250_000)); b.UncertaintyUS != 250_000 || b.Causes[0].EventStartUS != 9_000_000 || b.Causes[0].EventEndUS != 9_500_000 {
		t.Errorf("boundary = %+v", b)
	}

	// AUDIO-03: per-block energy and silence fraction over usable audio.
	blk := blockSpan(t, r, us2d(13_400_000), 20*sec)
	if *blk.AudioKnownUS != 5_600_000 {
		t.Errorf("audio known in the last block = %d (a 1 s hole must not count)", *blk.AudioKnownUS)
	}
	if got := *blk.SilenceFraction; math.Abs(got-100_000.0/5_600_000) > 1e-9 {
		t.Errorf("silence fraction = %v", got)
	}
	if got := *blk.AudioEnergyDBFS; got > -20 || got < -20.2 {
		t.Errorf("audio energy = %v dBFS", got)
	}
}

// AUDIO-02: level and minimum duration are configurable.
func TestSilenceConfigurable(t *testing.T) {
	cfg := testConfig()
	cfg.Audio.SilenceLevel = -45
	r := analyze(t, &SliceSource{Events: levelRecording()}, cfg)
	if got, want := silenceSpans(r), "[5,5.1) [7,7.5) [9,9.5) [13,13.2) [13.3,13.5)"; got != want {
		t.Errorf("-45 dBFS: %s, want %s", got, want)
	}
	cfg = testConfig()
	cfg.Audio.MinSilence = 60 * time.Millisecond
	r = analyze(t, &SliceSource{Events: levelRecording()}, cfg)
	if got, want := silenceSpans(r), "[3,3.08) [5,5.1) [9,9.5) [13,13.2) [13.3,13.5)"; got != want {
		t.Errorf("min 60 ms: %s, want %s", got, want)
	}
	cfg = testConfig()
	cfg.Audio.MinSilence = 300 * time.Millisecond
	r = analyze(t, &SliceSource{Events: levelRecording()}, cfg)
	if got, want := silenceSpans(r), "[9,9.5)"; got != want {
		t.Errorf("min 300 ms: %s, want %s", got, want)
	}
	cfg = testConfig()
	cfg.Boundaries.FromSilence = false
	r = analyze(t, &SliceSource{Events: levelRecording()}, cfg)
	wantBoundaries(t, r, 0, 20)
	if d := detector(t, r, SignalAudio); d.Boundary || d.Events != 4 {
		t.Errorf("detector = %+v", d)
	}
}

// AUDIO-01, TIME-01: overlapping and unordered audio windows are placed by
// presentation time and never counted twice.
func TestAudioOverlapAndOrder(t *testing.T) {
	base := levelRecording()
	// Every audio event delivered twice, the copies after all the originals.
	var evs, dup []Event
	for _, e := range base {
		evs = append(evs, e)
		if e.Kind == EventAudio {
			dup = append(dup, e)
		}
	}
	for i := len(dup) - 1; i >= 0; i-- { // and in reverse order
		evs = append(evs, dup[i])
	}
	want := analyze(t, &SliceSource{Events: base}, testConfig())
	got := analyze(t, &SliceSource{Events: evs}, testConfig())
	if silenceSpans(got) != silenceSpans(want) || *detector(t, got, SignalAudio).CoverageUS != *detector(t, want, SignalAudio).CoverageUS {
		t.Errorf("duplicated audio changed the measurement: %s coverage %d", silenceSpans(got), *detector(t, got, SignalAudio).CoverageUS)
	}
	if mustJSON(t, got.Diagnostics.Blocks) != mustJSON(t, want.Diagnostics.Blocks) {
		t.Errorf("duplicated audio changed block aggregates")
	}
}

// alignScenario: black frames (without their own silence) centred on 47 s
// and an independent silence.
func alignScenario(sepFrames int, silence [2]int64) *scenario {
	s := newScenario(blk{D: 47 * sec}, blk{D: 53 * sec, Sep: sepBlack, SepFrames: sepFrames})
	s.silences = [][2]int64{silence}
	return s
}

// AT-09 / AUDIO-02, BOUND-03: a silence near a visual candidate corroborates
// it within the alignment window without moving it.
func TestSilenceAlignsWithVisualCandidate(t *testing.T) {
	// Dark span [46.9,47.1); silence starts 200 ms after it ends.
	r := alignScenario(2, [2]int64{47_300_000, 47_500_000}).run(t, nil)
	wantBoundaries(t, r, 0, 47, 100)
	b := mustBoundary(t, r, 47*sec)
	if !hasCause(b, SignalDarkUniform, "dark", "boundary") || !hasCause(b, SignalAudio, "silence", "corroboration") {
		t.Fatalf("causes = %+v", b.Causes)
	}
	for _, c := range b.Causes {
		if c.Signal == SignalAudio && (c.EventStartUS != 47_300_000 || c.EventEndUS != 47_500_000 || c.CandidateUS != 47_000_000) {
			t.Errorf("silence cause = %+v", c)
		}
	}
	if mustJSON(t, b.Evidence) != `["dark_uniform_boundary","av_corroborated_boundary"]` {
		t.Errorf("evidence = %v", b.Evidence)
	}
	// Alignment adds boundary support; it is not an ad classification.
	wantTimeline(t, r, "U0-100")

	// 300 ms away is outside the 250 ms window: two candidates, no
	// corroboration.
	r = alignScenario(2, [2]int64{47_400_000, 47_600_000}).run(t, nil)
	wantBoundaries(t, r, 0, 47, 47.5, 100)
	if b := mustBoundary(t, r, 47*sec); hasCause(b, SignalAudio, "", "") || mustJSON(t, b.Evidence) != `["dark_uniform_boundary"]` {
		t.Errorf("dark boundary = %+v", b)
	}
	if b := mustBoundary(t, r, us2d(47_500_000)); !hasCause(b, SignalAudio, "silence", "boundary") || len(b.Evidence) != 0 {
		t.Errorf("silence boundary = %+v", b)
	}

	// The window is configurable.
	cfg := testConfig()
	cfg.Audio.Alignment = 100 * time.Millisecond
	r = alignScenario(2, [2]int64{47_300_000, 47_500_000}).run(t, cfg)
	wantBoundaries(t, r, 0, 47, 47.4, 100)
	cfg.Audio.Alignment = 400 * time.Millisecond
	r = alignScenario(2, [2]int64{47_400_000, 47_600_000}).run(t, cfg)
	wantBoundaries(t, r, 0, 47, 100)

	// A silence before the dark span aligns the same way.
	r = alignScenario(2, [2]int64{46_500_000, 46_700_000}).run(t, nil)
	wantBoundaries(t, r, 0, 47, 100)
	if b := mustBoundary(t, r, 47*sec); !hasCause(b, SignalAudio, "silence", "corroboration") {
		t.Errorf("causes = %+v", b.Causes)
	}
}

// BOUND-02: when the dark and silent spans overlap, the candidate is the
// midpoint of the shared interval, on a frame boundary.
func TestSilenceOverlapUsesSharedMidpoint(t *testing.T) {
	// Dark span [46.7,47.3) (midpoint 47.0); silence [47.1,47.5); shared
	// interval [47.1,47.3), midpoint 47.2.
	r := alignScenario(6, [2]int64{47_100_000, 47_500_000}).run(t, nil)
	wantBoundaries(t, r, 0, 47.2, 100)
	b := mustBoundary(t, r, us2d(47_200_000))
	if !hasCause(b, SignalDarkUniform, "dark", "boundary") || !hasCause(b, SignalAudio, "silence", "corroboration") {
		t.Errorf("causes = %+v", b.Causes)
	}
	// The original event extents stay in the diagnostics.
	for _, c := range b.Causes {
		if c.Signal == SignalDarkUniform && (c.EventStartUS != 46_700_000 || c.EventEndUS != 47_300_000 || c.CandidateUS != 47_200_000) {
			t.Errorf("dark cause = %+v", c)
		}
	}
	// Without overlap the visual midpoint is used.
	r = alignScenario(6, [2]int64{47_400_000, 47_600_000}).run(t, nil)
	wantBoundaries(t, r, 0, 47, 100)
}

// SCORE-06, AUDIO-02: a silence-only candidate next to a scene cut moves to
// the cut and is a corroborated audio/visual boundary; silence alone is not.
func TestSilenceWithSceneCut(t *testing.T) {
	cutAndSilence := func() *scenario {
		return newScenario(
			blk{D: 47 * sec, Level: 90},
			blk{D: 30 * sec, Level: 150, Sep: sepSilence},
			blk{D: 53 * sec, Level: 90, Sep: sepSilence},
		)
	}
	r := cutAndSilence().run(t, nil)
	wantBoundaries(t, r, 0, 47, 77, 130)
	b := mustBoundary(t, r, 47*sec)
	if !hasCause(b, SignalAudio, "silence", "boundary") || !hasCause(b, SignalScene, "cut", "corroboration") || mustJSON(t, b.Evidence) != `["av_corroborated_boundary"]` || b.UncertaintyUS != 0 {
		t.Errorf("boundary = %+v", b)
	}
	wantTimeline(t, r, "U0-47 A47-77 U77-130")
	if got := mustJSON(t, r.AdUnits[0].EvidenceFamilies); got != `["av_corroborated_boundary"]` {
		t.Errorf("unit evidence = %s", got)
	}

	// The corroboration can be switched off.
	cfg := testConfig()
	cfg.Boundaries.SilenceSceneAlign = false
	r = cutAndSilence().run(t, cfg)
	wantBoundaries(t, r, 0, 47, 77, 130)
	if b := mustBoundary(t, r, 47*sec); len(b.Evidence) != 0 {
		t.Errorf("boundary = %+v", b)
	}
	wantTimeline(t, r, "U0-130")

	// Silence alone (no cut): the 30 s block reaches the threshold on
	// duration and is rejected by the evidence gate.
	r = newScenario(blk{D: 47 * sec}, blk{D: 30 * sec, Sep: sepSilence}, blk{D: 53 * sec, Sep: sepSilence}).run(t, nil)
	wantBoundaries(t, r, 0, 47, 77, 130)
	wantTimeline(t, r, "U0-130")
	h := hypothesis(t, r, 47*sec, 77*sec)
	if h.Score != 2 || h.Eligible || !hasString(h.Reasons, "no_evidence_family") {
		t.Errorf("hypothesis = %+v", h)
	}
}

// AUDIO-03: a silent track or a long silent program scene does not by
// itself establish an ad.
func TestSilentTrackIsNotAnAd(t *testing.T) {
	// Entirely silent, 60 s: the single silence candidate splits it into two
	// 30 s blocks.
	r := newScenario(blk{D: 60 * sec, Silent: true}).run(t, nil)
	if got := silenceSpans(r); got != "[0,60)" {
		t.Fatalf("silence = %s", got)
	}
	wantBoundaries(t, r, 0, 30, 60)
	wantTimeline(t, r, "U0-60")
	if b := r.Diagnostics.Blocks[0]; *b.SilenceFraction != 1 || *b.AudioEnergyDBFS > -79 {
		t.Errorf("block silence fraction %v energy %v", *b.SilenceFraction, *b.AudioEnergyDBFS)
	}

	// A 30 s silent scene inside the program.
	r = newScenario(blk{D: 47 * sec}, blk{D: 30 * sec, Silent: true, Sep: sepNone}, blk{D: 53 * sec, Sep: sepNone}).run(t, nil)
	if got := silenceSpans(r); got != "[47,77)" {
		t.Fatalf("silence = %s", got)
	}
	wantBoundaries(t, r, 0, 62, 130)
	wantTimeline(t, r, "U0-130")
}

// AT-09, AT-18 / MEDIA-04: a recording without audio completes; only the
// audio evidence is unavailable.
func TestMissingAudioDisablesOnlyAudio(t *testing.T) {
	s := newScenario(blk{D: 47 * sec}, blk{D: 30 * sec}, blk{D: 53 * sec})
	s.noAudio = true
	r := s.run(t, nil)
	d := detector(t, r, SignalAudio)
	if d.Status != StatusUnavailable || d.Reason != "no_audio" || d.Boundary || d.CoverageUS != nil || d.Coverage != nil || d.Events != 0 {
		t.Errorf("detector = %+v", d)
	}
	if r.Timing.AudioStartOffsetUS != nil {
		t.Errorf("audio start offset = %d", *r.Timing.AudioStartOffsetUS)
	}
	if len(r.Diagnostics.Events.Silence) != 0 {
		t.Errorf("missing audio became silence: %s", silenceSpans(r))
	}
	for _, bl := range []Baseline{r.Baselines.AudioEnergy, r.Baselines.SilenceFraction} {
		if bl.Reliable || bl.Value != nil || bl.Reason != "signal_unavailable" {
			t.Errorf("audio baseline = %+v", bl)
		}
	}
	for _, b := range r.Diagnostics.Blocks {
		if b.AudioKnownUS != nil || b.AudioEnergyDBFS != nil || b.SilenceFraction != nil {
			t.Errorf("block %s reports audio measurements", b.ID)
		}
	}
	// Dark boundaries still carry the detection; no audio/visual
	// corroboration is invented.
	wantTimeline(t, r, "U0-47 A47-77 U77-130")
	if got := mustJSON(t, r.AdUnits[0].EvidenceFamilies); got != `["dark_uniform_boundary"]` {
		t.Errorf("unit evidence = %s", got)
	}
	if !r.Complete {
		t.Errorf("complete = false")
	}

	// A disabled audio detector ignores audio that is present.
	cfg := testConfig()
	cfg.Signals.Audio = false
	r = newScenario(blk{D: 47 * sec}, blk{D: 30 * sec}, blk{D: 53 * sec}).run(t, cfg)
	if d := detector(t, r, SignalAudio); d.Status != StatusDisabled || d.Boundary {
		t.Errorf("detector = %+v", d)
	}
	if len(r.Diagnostics.Events.Silence) != 0 || r.Timing.AudioStartOffsetUS != nil {
		t.Errorf("disabled audio produced output")
	}

	// Audio for only part of the recording: the rest is unknown, not silent.
	s = newScenario(blk{D: 47 * sec}, blk{D: 30 * sec, NoAudio: true}, blk{D: 53 * sec})
	r = s.run(t, nil)
	if d := detector(t, r, SignalAudio); d.Status != StatusUsable || *d.CoverageUS != 100_000_000 {
		t.Errorf("detector = %+v", d)
	}
	for _, e := range r.Diagnostics.Events.Silence {
		if e.EndUS-e.StartUS > 200_000 {
			t.Errorf("silence event %+v spans the audio hole", e)
		}
	}
	if b := blockAt(t, r, 60*sec); b.AudioKnownUS != nil && *b.AudioKnownUS > 200_000 {
		t.Errorf("audio known inside the hole = %d", *b.AudioKnownUS)
	}
}

// AUDIO-01: AudioWindower turns PCM into fixed windows on the sample clock
// with per-channel mean-square levels.
func TestAudioWindower(t *testing.T) {
	const rate = 48000
	pcm := func(frames int, l, r float32) []float32 {
		out := make([]float32, 2*frames)
		for i := 0; i < frames; i++ {
			out[2*i], out[2*i+1] = l, r
		}
		return out
	}
	near := func(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-6 }

	w := NewAudioWindower(20*time.Millisecond, -1)
	// 2.5 windows of a loud left channel and a silent right channel.
	ws := w.Push(nil, 1_000_000, rate, 2, pcm(2400, 0.5, 0))
	if len(ws) != 2 {
		t.Fatalf("windows = %+v", ws)
	}
	for i, x := range ws {
		if x.StartUS != 1_000_000+int64(i)*20_000 || x.DurationUS != 20_000 || !near(x.PeakMS, 0.25) || !near(x.MeanMS, 0.125) {
			t.Errorf("window %d = %+v", i, x)
		}
	}
	// The loudest channel decides silence: -6 dBFS on one channel is not
	// silent although the other is digital silence.
	if ws[0].PeakMS <= msOf(-50) {
		t.Errorf("peak %v would be silent", ws[0].PeakMS)
	}
	// A contiguous push completes the pending window across the call.
	ws = w.Push(ws[:0], 1_050_000, rate, 2, pcm(480, 0.5, 0))
	if len(ws) != 1 || ws[0].StartUS != 1_040_000 || ws[0].DurationUS != 20_000 {
		t.Fatalf("window across pushes = %+v", ws)
	}
	// Flush keeps a final partial window only when it covers at least half.
	ws = w.Push(ws[:0], 1_060_000, rate, 2, pcm(480, 0.1, 0.1))
	if len(ws) != 0 {
		t.Fatalf("half window emitted early: %+v", ws)
	}
	ws = w.Flush(ws)
	if len(ws) != 1 || ws[0].StartUS != 1_060_000 || ws[0].DurationUS != 10_000 || !near(ws[0].PeakMS, 0.01) {
		t.Fatalf("flushed half window = %+v", ws)
	}
	ws = w.Push(ws[:0], 2_000_000, rate, 2, pcm(100, 0.1, 0.1))
	if ws = w.Flush(ws); len(ws) != 0 {
		t.Fatalf("a short remainder was emitted: %+v", ws)
	}

	// A timestamp discontinuity restarts the window: the gap is never
	// measured audio (AUDIO-01).
	w = NewAudioWindower(20*time.Millisecond, -1)
	ws = w.Push(nil, 0, rate, 2, pcm(600, 0.5, 0.5)) // 12.5 ms pending
	ws = w.Push(ws, 5_000_000, rate, 2, pcm(960, 0.1, 0.1))
	if len(ws) != 2 || ws[0].StartUS != 0 || ws[0].DurationUS != 12_500 || !near(ws[0].PeakMS, 0.25) || ws[1].StartUS != 5_000_000 || !near(ws[1].PeakMS, 0.01) {
		t.Fatalf("windows around a discontinuity = %+v", ws)
	}

	// Excluding a channel (for example LFE) from the levels.
	w = NewAudioWindower(20*time.Millisecond, 0)
	ws = w.Push(nil, 0, rate, 2, pcm(960, 0.9, 0.001))
	if len(ws) != 1 || !near(ws[0].PeakMS, 1e-6) || !near(ws[0].MeanMS, 1e-6) {
		t.Fatalf("excluded channel: %+v", ws)
	}
	// A mono track never excludes its only channel.
	w = NewAudioWindower(20*time.Millisecond, 0)
	ws = w.Push(nil, 0, rate, 1, make([]float32, 960))
	if len(ws) != 1 || ws[0].PeakMS != 0 || ws[0].MeanMS != 0 || math.IsNaN(float64(ws[0].MeanMS)) {
		t.Fatalf("mono: %+v", ws)
	}

	// A format change flushes and re-sizes the window.
	w = NewAudioWindower(20*time.Millisecond, -1)
	ws = w.Push(nil, 0, rate, 2, pcm(600, 0.5, 0.5))
	ws = w.Push(ws, 12_500, 44100, 1, make([]float32, 882))
	if len(ws) != 2 || ws[0].DurationUS != 12_500 || ws[1].StartUS != 12_500 || ws[1].DurationUS != 20_000 {
		t.Fatalf("format change: %+v", ws)
	}
	// Invalid formats are ignored.
	if got := w.Push(nil, 0, 0, 2, pcm(10, 1, 1)); len(got) != 0 {
		t.Errorf("zero sample rate produced %+v", got)
	}
	if got := w.Push(nil, 0, rate, 0, pcm(10, 1, 1)); len(got) != 0 {
		t.Errorf("zero channels produced %+v", got)
	}
}

// The windows from AudioWindower drive silence detection end to end.
func TestAudioWindowerFeedsSilenceDetection(t *testing.T) {
	const rate = 8000
	w := NewAudioWindower(20*time.Millisecond, -1)
	var evs []Event
	for i := 0; i < 100; i++ {
		t0 := int64(i) * 100_000
		evs = append(evs, videoFrame(t0, 100_000, content))
		pcm := make([]float32, rate/10)
		amp := float32(0.1)
		if i >= 40 && i < 43 {
			amp = 0.0001 // -80 dBFS
		}
		for k := range pcm {
			pcm[k] = amp
		}
		evs = append(evs, Event{Kind: EventAudio, Audio: w.Push(nil, t0, rate, 1, pcm)})
	}
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	if got := silenceSpans(r); got != "[4,4.3)" {
		t.Errorf("silence = %s", got)
	}
	if d := detector(t, r, SignalAudio); *d.CoverageUS != 10_000_000 {
		t.Errorf("coverage = %d", *d.CoverageUS)
	}
}

func TestDownsampleLuma(t *testing.T) {
	// 4x4 with stride 6, reduced by 2: each output is the rounded mean of a
	// 2x2 cell.
	src := []byte{
		0, 2, 10, 10, 99, 99,
		2, 4, 10, 11, 99, 99,
		255, 255, 0, 0, 99, 99,
		255, 255, 0, 1, 99, 99,
	}
	dst := make([]byte, 4)
	w, h := DownsampleLuma(dst, src, 4, 4, 6, 2)
	if w != 2 || h != 2 || dst[0] != 2 || dst[1] != 10 || dst[2] != 255 || dst[3] != 0 {
		t.Errorf("downsample = %dx%d %v", w, h, dst)
	}
	// Factor 1 copies, honouring the stride.
	dst = make([]byte, 16)
	w, h = DownsampleLuma(dst, src, 4, 4, 6, 1)
	if w != 4 || h != 4 || dst[4] != 2 || dst[7] != 11 || dst[15] != 1 {
		t.Errorf("copy = %dx%d %v", w, h, dst)
	}
	// Remainder rows/columns are dropped.
	dst = make([]byte, 1)
	if w, h = DownsampleLuma(dst, src, 3, 3, 6, 2); w != 1 || h != 1 || dst[0] != 2 {
		t.Errorf("remainder = %dx%d %v", w, h, dst)
	}
}

// Caller frames larger than the analysis bound are reduced by the library
// and report the resulting scale (MEDIA-05, CFG-04 "coarse analysis must
// report resolution limits").
func TestOversizedFramesAreReduced(t *testing.T) {
	big := picture(768, 432, 110, 0, 0, false)
	var evs []Event
	for i := 0; i < 30; i++ {
		evs = append(evs, Event{Kind: EventVideo, Video: VideoFrame{
			PTS: int64(i) * 100_000, Duration: 100_000, Luma: big, Width: 768, Height: 432, Stride: 768,
			Scale: 1, FullResBrightFraction: -1, Resource: -1,
		}})
	}
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	v := r.Tracks.Video
	if v.AnalysisWidth != 384 || v.AnalysisHeight != 216 || v.AnalysisScale != 2 || v.CodedWidth != 768 || v.CodedHeight != 432 {
		t.Errorf("video track = %+v", v)
	}
	if b := r.Diagnostics.Blocks[0]; math.Abs(*b.Brightness-110) > 1 {
		t.Errorf("brightness = %v", *b.Brightness)
	}
}
