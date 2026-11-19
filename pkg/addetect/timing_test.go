// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// AT-01: timing, normalization and coverage (TIME-01..03, OUT-03).

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	js, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	return string(js)
}

// program/ad/program with exact boundaries, reused by several timing tests.
func timingScenario() *scenario {
	return newScenario(
		blk{D: 47 * sec},
		blk{D: 30 * sec},
		blk{D: 15 * sec},
		blk{D: 53 * sec},
	)
}

// AT-01 / TIME-02: constant frame rate; the final frame's declared duration
// bounds the timeline and nothing is estimated.
func TestTimingConstantRate(t *testing.T) {
	r := timingScenario().run(t, nil)
	if r.DurationUS != 145_000_000 {
		t.Fatalf("duration = %d", r.DurationUS)
	}
	tm := r.Timing
	if tm.Frames != 1450 || tm.FinalFrameDurationUS != 100_000 || tm.FinalFrameDurationEstimated || tm.MedianFrameDurationUS != 100_000 {
		t.Errorf("timing = %+v", tm)
	}
	if tm.NormalizationOffsetUS != 0 || tm.Resets != 0 || tm.Overlaps != 0 || tm.MissingTimestamps != 0 || len(tm.Gaps) != 0 {
		t.Errorf("timing = %+v", tm)
	}
	if hasWarning(r, "final_frame_duration_estimated") {
		t.Errorf("unexpected estimate warning")
	}
	if r.Coverage.VideoUS != r.DurationUS || r.Coverage.Fraction != 1 || !r.Complete {
		t.Errorf("coverage = %+v complete = %v", r.Coverage, r.Complete)
	}
	// Boundaries are exact microsecond values on the block edges.
	wantBoundaries(t, r, 0, 47, 77, 92, 145)
	wantTimeline(t, r, "U0-47 A47-92 U92-145")
	// Half-open intervals: the dark event ends where the next picture starts.
	ev := r.Diagnostics.Events.DarkUniform
	if len(ev) != 3 || ev[0].StartUS != 46_900_000 || ev[0].EndUS != 47_100_000 || ev[0].Frames != 2 {
		t.Errorf("dark events = %+v", ev)
	}
}

// AT-01 / TIME-02: a nonzero stream start only changes the reported
// normalization offset; every result time is relative to the first frame.
func TestTimingNonzeroStartIsNormalized(t *testing.T) {
	base := timingScenario().run(t, nil)
	s := timingScenario()
	s.startPTS = 7_654_321
	r := s.run(t, nil)
	if r.Timing.NormalizationOffsetUS != 7_654_321 {
		t.Fatalf("normalization offset = %d", r.Timing.NormalizationOffsetUS)
	}
	if *r.Timing.AudioStartOffsetUS != 0 {
		t.Errorf("audio start offset = %d", *r.Timing.AudioStartOffsetUS)
	}
	r.Timing.NormalizationOffsetUS = 0
	if a, b := mustJSON(t, base), mustJSON(t, r); a != b {
		t.Errorf("result differs when the stream starts at a nonzero timestamp")
	}

	// Negative media timestamps normalize the same way.
	s = timingScenario()
	s.startPTS = -3_000_001
	r = s.run(t, nil)
	if r.Timing.NormalizationOffsetUS != -3_000_001 {
		t.Fatalf("normalization offset = %d", r.Timing.NormalizationOffsetUS)
	}
	r.Timing.NormalizationOffsetUS = 0
	if a, b := mustJSON(t, base), mustJSON(t, r); a != b {
		t.Errorf("result differs when the stream starts at a negative timestamp")
	}
}

// AT-01 / TIME-01: fractional (30000/1001) frame rate. Times come from the
// presentation timestamps, so nothing drifts and rounding of the per-frame
// duration never becomes a gap or an overlap.
func TestTimingFractionalRate(t *testing.T) {
	const n = 900
	pts := func(i int) int64 { return int64(i) * 1_001_000 / 30 }
	black := flatPicture(tW, tH, 0)
	var evs []Event
	for i := 0; i < n; i++ {
		luma := content
		if i >= 450 && i < 453 {
			luma = black
		}
		evs = append(evs, videoFrame(1_000+pts(i), 33_367, luma))
	}
	cfg := testConfig()
	r := analyze(t, &SliceSource{Events: evs}, cfg)
	if want := pts(n-1) + 33_367; r.DurationUS != want {
		t.Errorf("duration = %d, want %d", r.DurationUS, want)
	}
	tm := r.Timing
	if tm.Frames != n || tm.Overlaps != 0 || len(tm.Gaps) != 0 || tm.FinalFrameDurationEstimated {
		t.Errorf("timing = %+v", tm)
	}
	if tm.MedianFrameDurationUS != 33_366 && tm.MedianFrameDurationUS != 33_367 {
		t.Errorf("median frame duration = %d", tm.MedianFrameDurationUS)
	}
	ev := r.Diagnostics.Events.DarkUniform
	if len(ev) != 1 || ev[0].StartUS != pts(450) || ev[0].EndUS != pts(453) {
		t.Fatalf("dark events = %+v, want [%d,%d)", ev, pts(450), pts(453))
	}
	// BOUND-02: the candidate is quantized to a frame presentation boundary
	// next to the span midpoint.
	bds := r.Diagnostics.Boundaries
	if len(bds) != 3 {
		t.Fatalf("boundaries = %v", boundaryTimes(r))
	}
	if got := bds[1].TimeUS; got != pts(451) && got != pts(452) {
		t.Errorf("boundary at %d is not the frame boundary nearest the midpoint (%d or %d)", got, pts(451), pts(452))
	}
}

// AT-01 / TIME-01, TIME-02: variable frame rate without declared durations.
// The final frame's duration is the documented estimate (the median frame
// duration) and is reported as such.
func TestTimingVariableRateEstimatesFinalFrame(t *testing.T) {
	deltas := []int64{40_000, 20_000, 60_000, 40_000, 40_000}
	var evs []Event
	var pts int64
	for i := 0; i < 500; i++ {
		evs = append(evs, videoFrame(pts, 0, content))
		pts += deltas[i%len(deltas)]
	}
	last := pts - deltas[(500-1)%len(deltas)]
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	tm := r.Timing
	if !tm.FinalFrameDurationEstimated || tm.FinalFrameDurationUS != 40_000 || tm.MedianFrameDurationUS != 40_000 {
		t.Errorf("timing = %+v", tm)
	}
	if r.DurationUS != last+40_000 {
		t.Errorf("duration = %d, want %d", r.DurationUS, last+40_000)
	}
	if !hasWarning(r, "final_frame_duration_estimated") {
		t.Errorf("the estimate is not reported: %+v", r.Warnings)
	}
	if len(tm.Gaps) != 0 || tm.Frames != 500 {
		t.Errorf("timing = %+v", tm)
	}
}

// TIME-01 / BLOCK-01: values are weighted by presentation duration, not by
// frame count. One long bright frame and nine short darker ones per second.
func TestTimingDurationWeightedAggregation(t *testing.T) {
	bright := picture(tW, tH, 200, 0, 0, false)
	dim := picture(tW, tH, 100, 0, 0, false)
	var evs []Event
	var pts int64
	for s := 0; s < 70; s++ {
		evs = append(evs, videoFrame(pts, 460_000, bright))
		pts += 460_000
		for k := 0; k < 9; k++ {
			evs = append(evs, videoFrame(pts, 60_000, dim))
			pts += 60_000
		}
	}
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	b := blockSpan(t, r, 0, 70*sec)
	// 46% of the time at ~200 and 54% at ~100; by frame count it would be 110.
	if got := *b.Brightness; got < 144 || got > 148 {
		t.Errorf("block brightness = %v, want about 146 (duration weighted)", got)
	}
	if got := *r.Baselines.Brightness.Value; got < 144 || got > 148 {
		t.Errorf("brightness baseline = %v, want about 146", got)
	}
}

// AT-01 / TIME-01: audio starting later or earlier than the video is placed
// on the one presentation timeline; time without audio is not measured.
func TestTimingAudioStartOffset(t *testing.T) {
	for _, off := range []int64{300_000, -300_000} {
		s := newScenario(blk{D: 47 * sec}, blk{D: 53 * sec, Sep: sepSilence})
		s.audioOffset = off
		r := s.run(t, nil)
		if got := r.Timing.AudioStartOffsetUS; got == nil || *got != off {
			t.Fatalf("offset %d: audio start offset = %v", off, got)
		}
		d := detector(t, r, SignalAudio)
		if d.Status != StatusUsable || *d.CoverageUS != 100_000_000-300_000 {
			t.Errorf("offset %d: audio detector = %+v coverage %d", off, d, *d.CoverageUS)
		}
		// The silence is found where its presentation time says it is.
		sil := r.Diagnostics.Events.Silence
		if len(sil) != 1 || sil[0].StartUS != 46_900_000+off || sil[0].EndUS != 47_100_000+off {
			t.Errorf("offset %d: silence = %+v", off, sil)
		}
		mustBoundary(t, r, us2d(47_000_000+off))
	}
}

// AT-01 / TIME-03: a forward timestamp jump is a coverage gap. The gap is
// reported, stays unknown and is delimited by boundaries at both edges.
func TestTimingGapIsUnknownCoverage(t *testing.T) {
	s := newScenario(blk{D: 47 * sec}, blk{D: 3 * sec, Skip: true}, blk{D: 53 * sec})
	r := s.run(t, nil)
	if r.DurationUS != 103_000_000 {
		t.Fatalf("duration = %d (time must not be shortened across a gap)", r.DurationUS)
	}
	want := []TimeRange{{StartUS: 47_000_000, EndUS: 50_000_000, Reason: "timestamp_gap"}}
	if mustJSON(t, r.Timing.Gaps) != mustJSON(t, want) || mustJSON(t, r.Coverage.Gaps) != mustJSON(t, want) {
		t.Errorf("gaps: timing %+v coverage %+v", r.Timing.Gaps, r.Coverage.Gaps)
	}
	if r.Coverage.VideoUS != 100_000_000 || r.Timing.Frames != 1000 {
		t.Errorf("coverage %+v frames %d", r.Coverage, r.Timing.Frames)
	}
	if !hasWarning(r, "coverage_gaps") {
		t.Errorf("gap not reported as a warning")
	}
	wantBoundaries(t, r, 0, 47, 50, 103)
	if b := mustBoundary(t, r, 47*sec); !hasCause(b, "timeline", "coverage_gap_start", "boundary") || b.UncertaintyUS != 0 {
		t.Errorf("gap start boundary = %+v", b)
	}
	if b := mustBoundary(t, r, 50*sec); !hasCause(b, "timeline", "coverage_gap_end", "boundary") {
		t.Errorf("gap end boundary = %+v", b)
	}
	gap := blockSpan(t, r, 47*sec, 50*sec)
	if !gap.Missing || gap.Coverage != 0 || gap.Brightness != nil || gap.AudioEnergyDBFS != nil {
		t.Errorf("gap block = %+v", gap)
	}
	seg := segmentAt(t, r, 48*sec)
	if seg.Label != LabelUnknown || !hasString(seg.Reasons, "missing_coverage") {
		t.Errorf("gap segment = %+v", seg)
	}
	// A timestamp gap in the media is not a failure of the analysis.
	if !r.Complete {
		t.Errorf("complete = false")
	}
	// No detector fabricates observations inside the gap.
	for _, e := range r.Diagnostics.Events.DarkUniform {
		if e.StartUS < 50_000_000 && e.EndUS > 47_000_000 {
			t.Errorf("dark event %+v overlaps the gap", e)
		}
	}
	for _, e := range r.Diagnostics.Events.Silence {
		if e.StartUS < 50_000_000 && e.EndUS > 47_000_000 {
			t.Errorf("silence event %+v overlaps the gap (missing audio is not silence)", e)
		}
	}
}

// TIME-03 / PACK-08: a gap the source declares as a loss carries the
// source's reason and marks the result incomplete; a gap the presentation
// itself declares does not.
func TestTimingDeclaredGaps(t *testing.T) {
	build := func(declared bool) []Event {
		s := newScenario(blk{D: 47 * sec}, blk{D: 3 * sec, Skip: true}, blk{D: 53 * sec})
		evs := s.events()
		return append(evs, Event{Kind: EventGap, Gap: GapObservation{StartUS: 47_000_000, EndUS: 50_000_000, Reason: "resource_unreadable", Resource: 3, Declared: declared}})
	}
	r := analyze(t, &SliceSource{Events: build(false)}, testConfig())
	if r.Complete {
		t.Errorf("a lost interval must make the result incomplete")
	}
	if len(r.Coverage.Gaps) != 1 || r.Coverage.Gaps[0].Reason != "resource_unreadable" {
		t.Errorf("gaps = %+v", r.Coverage.Gaps)
	}
	r = analyze(t, &SliceSource{Events: build(true)}, testConfig())
	if !r.Complete {
		t.Errorf("a presentation-declared gap is not a loss")
	}

	// A declared gap after the last frame keeps the timeline from being
	// shortened; the tail is unknown.
	s := newScenario(blk{D: 47 * sec})
	evs := append(s.events(), Event{Kind: EventGap, Gap: GapObservation{StartUS: 47_000_000, EndUS: 52_000_000, Reason: "resource_missing"}})
	r = analyze(t, &SliceSource{Events: evs}, testConfig())
	if r.DurationUS != 52_000_000 || r.Complete {
		t.Fatalf("duration = %d complete = %v", r.DurationUS, r.Complete)
	}
	wantBoundaries(t, r, 0, 47, 52)
	if seg := segmentAt(t, r, 50*sec); seg.Label != LabelUnknown || !hasString(seg.Reasons, "missing_coverage") {
		t.Errorf("tail segment = %+v", seg)
	}
	if len(r.Coverage.Gaps) != 1 || r.Coverage.Gaps[0] != (TimeRange{StartUS: 47_000_000, EndUS: 52_000_000, Reason: "resource_missing"}) {
		t.Errorf("gaps = %+v", r.Coverage.Gaps)
	}
}

// resetEvents: 10 s of media, then the timestamps restart at zero.
func resetEvents() []Event {
	a := newScenario(blk{D: 10 * sec}).events()
	b := newScenario(blk{D: 10 * sec}).events()
	return append(a, b...)
}

// AT-01 / TIME-03: backward jump, rebase policy. Later media continues
// directly after the previous frame; the reset is reported.
func TestTimingBackwardJumpRebase(t *testing.T) {
	r := analyze(t, &SliceSource{Events: resetEvents()}, testConfig())
	if r.DurationUS != 20_000_000 || r.Timing.Frames != 200 {
		t.Fatalf("duration = %d frames = %d", r.DurationUS, r.Timing.Frames)
	}
	tm := r.Timing
	if tm.Resets != 1 || len(tm.Gaps) != 0 || tm.Overlaps != 0 {
		t.Errorf("timing = %+v", tm)
	}
	if len(tm.Rebases) != 1 || tm.Rebases[0] != (Rebase{AtUS: 10_000_000, OffsetUS: 10_000_000}) {
		t.Errorf("rebases = %+v", tm.Rebases)
	}
	var w *Warning
	for i := range r.Warnings {
		if r.Warnings[i].Code == "timestamp_reset" {
			w = &r.Warnings[i]
		}
	}
	if w == nil || w.TimeUS == nil || *w.TimeUS != 10_000_000 {
		t.Errorf("reset warning = %+v", w)
	}
	// Audio that follows the reset moves with the video: no hole, no overlap.
	if d := detector(t, r, SignalAudio); *d.CoverageUS != 20_000_000 {
		t.Errorf("audio coverage = %d", *d.CoverageUS)
	}
	if !r.Complete {
		t.Errorf("complete = false")
	}
}

// AT-01 / TIME-03: backward jump, fail policy: a specific timing error and
// an explicitly partial result.
func TestTimingBackwardJumpFail(t *testing.T) {
	cfg := testConfig()
	cfg.Input.TimestampResetPolicy = "fail"
	r, err := Analyze(context.Background(), &SliceSource{Events: resetEvents()}, cfg, nil)
	if !errors.Is(err, ErrTiming) || KindOf(err) != KindTiming {
		t.Fatalf("err = %v, want a timing error", err)
	}
	if r == nil {
		t.Fatalf("no partial result")
	}
	if r.Complete || r.Error == nil || r.Error.Kind != "unusable_timing" || r.DurationUS != 10_000_000 {
		t.Errorf("partial result: complete %v error %+v duration %d", r.Complete, r.Error, r.DurationUS)
	}
	checkInvariants(t, r)
}

// AT-01 / TIME-03: frames at or before the previous presentation time
// (duplicates, overlapping resources, a late out-of-order picture) are
// dropped and counted, never presented twice.
func TestTimingOverlapsDropped(t *testing.T) {
	clean := newScenario(blk{D: 20 * sec}).events()
	var evs []Event
	dups := 0
	for i, e := range clean {
		evs = append(evs, e)
		if e.Kind != EventVideo {
			continue
		}
		switch {
		case i%50 == 0: // exact duplicate
			evs = append(evs, e)
			dups++
		case i%70 == 0: // late picture 150 ms in the past
			late := e
			late.Video.PTS -= 150_000
			evs = append(evs, late)
			dups++
		}
	}
	base := analyze(t, &SliceSource{Events: clean}, testConfig())
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	if r.Timing.Overlaps != dups || r.Timing.Frames != 200 || r.DurationUS != 20_000_000 {
		t.Fatalf("overlaps = %d (want %d) frames = %d duration = %d", r.Timing.Overlaps, dups, r.Timing.Frames, r.DurationUS)
	}
	if !hasWarning(r, "timestamp_overlaps") {
		t.Errorf("overlaps not reported")
	}
	if len(r.Timing.Gaps) != 0 || r.Timing.Resets != 0 {
		t.Errorf("timing = %+v", r.Timing)
	}
	// Apart from the report, the result is that of the clean stream.
	r.Timing.Overlaps = 0
	r.Warnings = base.Warnings
	if mustJSON(t, base) != mustJSON(t, r) {
		t.Errorf("dropping overlaps changed the analysis")
	}
}

// AT-01 / TIME-03: frames without a timestamp are interpolated and counted;
// too many of them is a specific timing error.
func TestTimingMissingTimestamps(t *testing.T) {
	strip := func(every int) []Event {
		evs := timingScenario().events()
		n := 0
		for i := range evs {
			if evs[i].Kind == EventVideo {
				if n%every == every-1 {
					evs[i].Video.NoPTS = true
					evs[i].Video.PTS = -999_999_999 // garbage, must be ignored
				}
				n++
			}
		}
		return evs
	}
	base := timingScenario().run(t, nil)
	r := analyze(t, &SliceSource{Events: strip(10)}, testConfig())
	if r.Timing.MissingTimestamps != 145 || !hasWarning(r, "missing_timestamps") {
		t.Errorf("missing = %d warnings %+v", r.Timing.MissingTimestamps, r.Warnings)
	}
	if r.DurationUS != base.DurationUS || timeline(r) != timeline(base) || mustJSON(t, boundaryTimes(r)) != mustJSON(t, boundaryTimes(base)) {
		t.Errorf("interpolated timeline differs: %s vs %s", timeline(r), timeline(base))
	}

	// More than input.max_missing_timestamp_ratio (0.5): no reliable timeline.
	_, err := Analyze(context.Background(), &SliceSource{Events: strip(1)}, testConfig(), nil)
	if !errors.Is(err, ErrTiming) {
		t.Fatalf("err = %v, want a timing error", err)
	}

	// A first frame without a timestamp starts the timeline at zero.
	evs := newScenario(blk{D: 5 * sec}).events()
	evs[0].Video.NoPTS, evs[0].Video.PTS = true, 123_456
	r = analyze(t, &SliceSource{Events: evs}, testConfig())
	if r.Timing.NormalizationOffsetUS != 0 || r.DurationUS != 5_000_000 || r.Timing.MissingTimestamps != 1 {
		t.Errorf("timing = %+v duration %d", r.Timing, r.DurationUS)
	}
}

// AT-01 / TIME-03, MEDIA-04: decode failures are reported; a recording with
// no decodable video fails.
func TestTimingDecodeErrors(t *testing.T) {
	evs := newScenario(blk{D: 10 * sec}).events()
	evs = append(evs, Event{Kind: EventDecodeError, DecodeError: "bad slice"}, Event{Kind: EventDecodeError, DecodeError: "bad slice"})
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	if r.Timing.DecodeErrors != 2 || !hasWarning(r, "decode_error") || !r.Complete {
		t.Errorf("decode errors = %d warnings = %+v complete = %v", r.Timing.DecodeErrors, r.Warnings, r.Complete)
	}

	// Above input.max_decode_error_ratio the analysis fails with a media
	// error and whatever was analyzed is explicitly partial.
	evs = newScenario(blk{D: 1 * sec}).events()
	for i := 0; i < 11; i++ {
		evs = append(evs, Event{Kind: EventDecodeError, DecodeError: "bad slice"})
	}
	r, err := Analyze(context.Background(), &SliceSource{Events: evs}, testConfig(), nil)
	if !errors.Is(err, ErrMedia) {
		t.Fatalf("err = %v, want a media error", err)
	}
	if r == nil || r.Complete || r.Error == nil || r.Error.Kind != "media_failure" {
		t.Errorf("partial result = %+v", r)
	}

	// No video at all.
	only := []Event{{Kind: EventAudio, Audio: []AudioWindow{{StartUS: 0, DurationUS: 20_000, PeakMS: speechMS, MeanMS: speechMS}}}}
	r, err = Analyze(context.Background(), &SliceSource{Events: only}, testConfig(), nil)
	if !errors.Is(err, ErrMedia) || r != nil {
		t.Fatalf("no video: result %v err %v", r, err)
	}
	r, err = Analyze(context.Background(), &SliceSource{}, testConfig(), nil)
	if !errors.Is(err, ErrMedia) || r != nil {
		t.Fatalf("empty source: result %v err %v", r, err)
	}

	// A frame whose luminance plane is too small is a media error, not a
	// crash.
	bad := videoFrame(0, 100_000, content[:100])
	_, err = Analyze(context.Background(), &SliceSource{Events: []Event{bad}}, testConfig(), nil)
	if !errors.Is(err, ErrMedia) {
		t.Fatalf("short plane: err = %v", err)
	}
}

// TIME-02: the shortest possible recording still has a positive, bounded
// timeline.
func TestTimingSingleFrame(t *testing.T) {
	r := analyze(t, &SliceSource{Events: []Event{videoFrame(5_000, 40_000, content)}}, testConfig())
	if r.DurationUS != 40_000 || r.Timing.Frames != 1 || r.Timing.FinalFrameDurationEstimated {
		t.Errorf("duration = %d timing = %+v", r.DurationUS, r.Timing)
	}
	wantTimeline(t, r, "U0-0.04")

	// Without a declared duration the documented fallback is used and
	// reported as an estimate.
	r = analyze(t, &SliceSource{Events: []Event{videoFrame(5_000, 0, content)}}, testConfig())
	if r.DurationUS <= 0 || !r.Timing.FinalFrameDurationEstimated || r.DurationUS != r.Timing.FinalFrameDurationUS {
		t.Errorf("duration = %d timing = %+v", r.DurationUS, r.Timing)
	}
}

// API-05, API-02: progress distinguishes extraction from classification and
// reports media time; per-frame measurements are streamed, not retained.
func TestProgressAndFrameObserver(t *testing.T) {
	s := timingScenario()
	s.startPTS = 1_000_000
	var stages []string
	var lastExtract int64 = -1
	frames := 0
	var lastFrame int64 = -1
	opts := &Options{
		Progress: func(p Progress) {
			if len(stages) == 0 || stages[len(stages)-1] != p.Stage {
				stages = append(stages, p.Stage)
			}
			if p.Stage == StageExtract {
				if p.MediaTimeUS <= lastExtract {
					t.Errorf("extraction progress went from %d to %d", lastExtract, p.MediaTimeUS)
				}
				lastExtract = p.MediaTimeUS
			}
		},
		FrameObserver: func(m FrameMeasurement) {
			frames++
			if m.TimeUS <= lastFrame {
				t.Errorf("frame time went from %d to %d", lastFrame, m.TimeUS)
			}
			lastFrame = m.TimeUS
			if m.CodedWidth != tW || m.CodedHeight != tH || m.Brightness < 0 || m.Brightness > 255 {
				t.Errorf("measurement = %+v", m)
			}
		},
		Build: "test-build",
	}
	r, err := Analyze(context.Background(), s.source(), testConfig(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustJSON(t, stages); got != `["feature_extraction","classification","done"]` {
		t.Errorf("stages = %s", got)
	}
	if frames != 1450 || lastFrame != 144_900_000 {
		t.Errorf("observed %d frames, last at %d", frames, lastFrame)
	}
	if lastExtract < 143_000_000 || lastExtract > 144_900_000 {
		t.Errorf("last extraction progress at %d", lastExtract)
	}
	if r.Product.Build != "test-build" || r.Product.Name != ProductName || r.Product.Version != Version {
		t.Errorf("product = %+v", r.Product)
	}
}
