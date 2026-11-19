// SPDX-License-Identifier: Apache-2.0

package ffadapter_test

// AT-01 on real media: presentation-timestamp based timing (TIME-01..03,
// OUT-03), and AT-06: coded resolution changes (SIZE-01..02).

import (
	"strings"
	"testing"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/ffadapter"
)

// AT-01 / TIME-01, TIME-02: every frame's result time is its presentation
// timestamp relative to the first presented frame, compared frame by frame
// with the timestamps ffprobe demuxes. Covers a fractional rate
// (30000/1001), variable frame rate, reordered pictures (B-frames), nonzero
// stream starts (-output_ts_offset, MPEG-TS) and millisecond time bases.
func TestFrameTimesFollowPresentationTimestamps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, fixture, file string
		frames              int
		check               func(t *testing.T, res *addetect.Result, pts []int64)
	}{
		{"fractional 30000/1001 with B-frames", "ntsc", "ntsc.mp4", 90, func(t *testing.T, res *addetect.Result, pts []int64) {
			if res.Tracks.Video.FrameRate != "30000/1001" {
				t.Errorf("frame rate = %q", res.Tracks.Video.FrameRate)
			}
			// 90 frames of 1001/30000 s: 3.003 s, not 90/30 s.
			near(t, "duration_us", res.DurationUS, 3_003_000, 1)
			near(t, "median frame duration", res.Timing.MedianFrameDurationUS, 33_367, 1)
			if res.Timing.NormalizationOffsetUS != 0 {
				t.Errorf("normalization offset = %d, want 0", res.Timing.NormalizationOffsetUS)
			}
		}},
		{"variable frame rate", "vfr", "vfr.mkv", 72, func(t *testing.T, res *addetect.Result, pts []int64) {
			// The cadence is 4 x 33 ms, 2 x 100 ms. Frame count over the
			// nominal 30 fps would give 2.4 s; the timestamps span 3.93 s.
			if res.DurationUS < 3_900_000 || res.DurationUS > 3_970_000 {
				t.Errorf("duration_us = %d, want about 3.93 s from timestamps (72 frames / 30 fps would be 2.4 s)", res.DurationUS)
			}
			short, long := 0, 0
			for i := 1; i < len(pts); i++ {
				switch d := pts[i] - pts[i-1]; {
				case d >= 33_000 && d <= 34_000:
					short++
				case d >= 99_000 && d <= 101_000:
					long++
				}
			}
			if short < 40 || long < 20 {
				t.Fatalf("fixture is not VFR: %d short and %d long intervals", short, long)
			}
			// The 100 ms intervals are ordinary frame durations, not gaps.
			if len(res.Timing.Gaps) != 0 {
				t.Errorf("VFR intervals reported as gaps: %+v", res.Timing.Gaps)
			}
		}},
		{"nonzero start (37.5 s)", "offset", "offset.mkv", 60, func(t *testing.T, res *addetect.Result, pts []int64) {
			near(t, "normalization offset", res.Timing.NormalizationOffsetUS, 37_500_000, 1)
		}},
		{"MPEG-TS start with B-frames", "matrix", "ts_aac.ts", 60, func(t *testing.T, res *addetect.Result, pts []int64) {
			if res.Timing.NormalizationOffsetUS < 1_000_000 {
				t.Errorf("normalization offset = %d, want the transport stream's nonzero first PTS", res.Timing.NormalizationOffsetUS)
			}
		}},
		{"MP4 with B-frames", "matrix", "mp4_aac.mp4", 60, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := fixture(t, tc.fixture, tc.file)
			res, frames := analyzeFrames(t, ffadapter.Path(path))
			pts := probePTS(t, path, "v:0")
			if len(pts) != tc.frames || len(frames) != tc.frames || res.Timing.Frames != tc.frames {
				t.Fatalf("frames: ffprobe %d, observer %d, result %d, want %d", len(pts), len(frames), res.Timing.Frames, tc.frames)
			}
			near(t, "normalization_offset_us", res.Timing.NormalizationOffsetUS, pts[0], 1)
			if frames[0].TimeUS != 0 {
				t.Errorf("first frame at %d, want 0", frames[0].TimeUS)
			}
			for i, f := range frames {
				if absDiff(f.TimeUS, pts[i]-pts[0]) > 1 {
					t.Fatalf("frame %d at %d us, ffprobe presentation time is %d us", i, f.TimeUS, pts[i]-pts[0])
				}
				if i > 0 && f.TimeUS <= frames[i-1].TimeUS {
					t.Fatalf("frame %d at %d us is not after frame %d at %d us (presentation order)", i, f.TimeUS, i-1, frames[i-1].TimeUS)
				}
			}
			// The timeline ends one final-frame duration after the last frame.
			near(t, "duration_us", res.DurationUS, pts[len(pts)-1]-pts[0]+res.Timing.FinalFrameDurationUS, 1)
			if res.Timing.FinalFrameDurationUS <= 0 {
				t.Errorf("final frame duration = %d", res.Timing.FinalFrameDurationUS)
			}
			if res.Timing.Resets != 0 || res.Timing.Overlaps != 0 || res.Timing.MissingTimestamps != 0 || res.Timing.DecodeErrors != 0 {
				t.Errorf("timing anomalies on a clean file: %+v", res.Timing)
			}
			if res.Timing.DiscontinuityPolicy == "" {
				t.Errorf("the discontinuity policy is not reported")
			}
			if tc.check != nil {
				tc.check(t, res, pts)
			}
		})
	}
}

// AT-01, AT-09 / TIME-01, MEDIA-04: audio that starts after or before the
// video shares the video's presentation timeline. The offset is reported,
// and time without audio is unknown audio coverage, never silence.
func TestAudioVideoStartOffset(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		wantOffset int64
	}{
		{"audio_late", 500_000},
		{"audio_early", -533_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := fixture(t, tc.name, tc.name+".mkv")
			res := analyze(t, ffadapter.Path(path))
			if res.Timing.AudioStartOffsetUS == nil {
				t.Fatal("audio_start_offset_us is null")
			}
			got := *res.Timing.AudioStartOffsetUS
			near(t, "audio_start_offset_us", got, tc.wantOffset, 2000)
			// Independent check against ffprobe's stream timestamps.
			v, a := probePTS(t, path, "v:0"), probePTS(t, path, "a:0")
			near(t, "audio_start_offset_us vs ffprobe", got, a[0]-v[0], 25_000)
			near(t, "normalization_offset_us", res.Timing.NormalizationOffsetUS, v[0], 1)

			d := detector(t, res, addetect.SignalAudio)
			if d.Status != addetect.StatusUsable || d.CoverageUS == nil {
				t.Fatalf("audio detector = %+v", d)
			}
			if tc.wantOffset > 0 {
				// The first 0.5 s has no audio: coverage is 3.5 of 4 s.
				near(t, "audio coverage", *d.CoverageUS, res.DurationUS-500_000, 60_000)
			} else if *d.CoverageUS > res.DurationUS {
				// Audio before the first video frame is outside the timeline.
				t.Errorf("audio coverage %d exceeds the duration %d", *d.CoverageUS, res.DurationUS)
			}
			if n := len(res.Diagnostics.Events.Silence); n != 0 {
				t.Errorf("%d silence events: missing audio was measured as silence", n)
			}
		})
	}
}

// AT-06 / SIZE-01, SIZE-02, MEDIA-04: a transport stream whose coded size
// changes mid-stream yields exactly one resolution event, at the first
// frame with the new size; the initial geometry is not an event, analysis
// continues, and geometry-dependent state (the logo model) is restarted.
func TestResolutionChangeMidStream(t *testing.T) {
	t.Parallel()
	path := fixture(t, "reschange", "reschange.ts")
	res, frames := analyzeFrames(t, ffadapter.Path(path))
	if want := probeFrameCount(t, path); res.Timing.Frames != want || want != 120 {
		t.Fatalf("frames = %d, ffprobe counts %d, want 120", res.Timing.Frames, want)
	}
	if !res.Complete {
		t.Errorf("not complete")
	}
	ev := res.Diagnostics.Events.Resolution
	if len(ev) != 1 {
		t.Fatalf("resolution events = %+v, want exactly one", ev)
	}
	e := ev[0]
	if e.OldWidth != 320 || e.OldHeight != 180 || e.NewWidth != 256 || e.NewHeight != 144 {
		t.Errorf("resolution event = %+v, want 320x180 -> 256x144", e)
	}
	// The first 60 frames are 320x180, the rest 256x144.
	for i, f := range frames {
		w, h := 320, 180
		if i >= 60 {
			w, h = 256, 144
		}
		if f.CodedWidth != w || f.CodedHeight != h {
			t.Fatalf("frame %d is %dx%d, want %dx%d", i, f.CodedWidth, f.CodedHeight, w, h)
		}
	}
	if e.TimeUS != frames[60].TimeUS {
		t.Errorf("resolution event at %d, want the first frame with the new size at %d", e.TimeUS, frames[60].TimeUS)
	}
	pts := probePTS(t, path, "v:0")
	near(t, "resolution event time vs ffprobe", e.TimeUS, pts[60]-pts[0], 1)
	if d := detector(t, res, addetect.SignalResolution); d.Status != addetect.StatusUsable || d.Events != 1 || !d.Boundary {
		t.Errorf("resolution detector = %+v", d)
	}
	// SIZE-01: the change is a boundary at exactly that time.
	found := false
	for _, b := range res.Diagnostics.Boundaries {
		for _, c := range b.Causes {
			if c.Signal == addetect.SignalResolution {
				found = true
				if b.TimeUS != e.TimeUS {
					t.Errorf("resolution boundary at %d, want %d", b.TimeUS, e.TimeUS)
				}
			}
		}
	}
	if !found {
		t.Errorf("no boundary carries the resolution change")
	}
	// SIZE-02: one logo span per geometry.
	if cal := res.Calibration.Logo; len(cal) != 2 || cal[0].EndUS != e.TimeUS || cal[1].StartUS != e.TimeUS {
		t.Errorf("logo calibration spans = %+v, want two spans split at %d", cal, e.TimeUS)
	}
	// Blocks on either side report their own geometry.
	for _, b := range res.Diagnostics.Blocks {
		w := 320
		if b.StartUS >= e.TimeUS {
			w = 256
		}
		if b.CodedWidth != w {
			t.Errorf("block %s [%d,%d) has coded width %d, want %d", b.ID, b.StartUS, b.EndUS, b.CodedWidth, w)
		}
	}
	if res.Tracks.Video.CodedWidth != 320 || res.Tracks.Video.AnalysisWidth != 320 {
		t.Errorf("track geometry = %+v, want the initial 320x180", res.Tracks.Video)
	}
	// The single-size transport stream has no event at all (SIZE-01:
	// initial dimensions establish state).
	single := analyze(t, ffadapter.Path(fixture(t, "matrix", "ts_aac.ts")))
	if n := len(single.Diagnostics.Events.Resolution); n != 0 {
		t.Errorf("%d resolution events without a size change", n)
	}
}

// AT-01 / TIME-03: a backward timestamp jump is reported and re-based by
// the documented policy so the timeline stays ordered; under
// input.timestamp_reset_policy=fail it is a timing error.
func TestTimestampResetPolicy(t *testing.T) {
	t.Parallel()
	path := fixture(t, "reschange", "reschange_reset.ts")
	res, frames := analyzeFrames(t, ffadapter.Path(path))
	if res.Timing.Frames != 120 || !res.Complete {
		t.Fatalf("frames=%d complete=%v, want all 120 frames", res.Timing.Frames, res.Complete)
	}
	if res.Timing.Resets != 1 || len(res.Timing.Rebases) != 1 || !hasWarning(res, "timestamp_reset") {
		t.Errorf("resets=%d rebases=%+v warnings=%v, want one reported reset", res.Timing.Resets, res.Timing.Rebases, warningCodes(res))
	}
	for i := 1; i < len(frames); i++ {
		if frames[i].TimeUS <= frames[i-1].TimeUS {
			t.Fatalf("frame %d at %d us is not after frame %d at %d us", i, frames[i].TimeUS, i-1, frames[i-1].TimeUS)
		}
	}
	// Two 4 s pieces placed back to back: no fabricated gap, no lost time.
	near(t, "duration_us", res.DurationUS, 8_000_000, 2)
	if len(res.Timing.Gaps) != 0 {
		t.Errorf("gaps = %+v, want none", res.Timing.Gaps)
	}
	if rb := res.Timing.Rebases; len(rb) == 1 {
		near(t, "rebase time", rb[0].AtUS, frames[60].TimeUS, 1)
	}
	if n := len(res.Diagnostics.Events.Resolution); n != 1 {
		t.Errorf("%d resolution events, want 1", n)
	}

	r, err := tryAnalyze(ffadapter.Path(path), set("input.timestamp_reset_policy", "fail"))
	if err == nil || addetect.KindOf(err) != addetect.KindTiming {
		t.Fatalf("timestamp_reset_policy=fail: err = %v, want a timing error", err)
	}
	if r != nil && r.Complete {
		t.Errorf("a timing failure returned a complete result")
	}
}

// AT-01, AT-11 / TIME-03, BLOCK-01: a forward timestamp jump is a reported
// coverage gap. The missing time stays on the timeline as unknown; ordinary
// coverage is never fabricated across it.
func TestTimestampGapIsUnknownCoverage(t *testing.T) {
	t.Parallel()
	path := fixture(t, "reschange", "reschange_gap.ts")
	res, frames := analyzeFrames(t, ffadapter.Path(path))
	if res.Timing.Frames != 120 {
		t.Fatalf("frames = %d, want 120", res.Timing.Frames)
	}
	if len(res.Timing.Gaps) != 1 || len(res.Coverage.Gaps) != 1 {
		t.Fatalf("gaps = %+v / %+v, want exactly one", res.Timing.Gaps, res.Coverage.Gaps)
	}
	g := res.Coverage.Gaps[0]
	// The gap runs from the end of the last frame before the jump to the
	// first frame after it.
	near(t, "gap start", g.StartUS, frames[59].TimeUS+66_667, 2)
	if g.EndUS != frames[60].TimeUS {
		t.Errorf("gap ends at %d, want the next frame at %d", g.EndUS, frames[60].TimeUS)
	}
	if d := g.EndUS - g.StartUS; d < 2_900_000 || d > 3_000_000 {
		t.Errorf("gap lasts %d us, want about 3 s", d)
	}
	if g.Reason != "timestamp_gap" || !hasWarning(res, "coverage_gaps") {
		t.Errorf("gap reason %q, warnings %v", g.Reason, warningCodes(res))
	}
	// The timeline keeps the missing time: 8 s of video over about 11 s.
	near(t, "duration_us", res.DurationUS, 8_000_000+(g.EndUS-g.StartUS), 2)
	near(t, "coverage.video_us", res.Coverage.VideoUS, res.DurationUS-(g.EndUS-g.StartUS), 1)
	if res.Coverage.Fraction >= 0.8 {
		t.Errorf("coverage fraction = %.3f, want about 0.73", res.Coverage.Fraction)
	}
	// BLOCK-01: the gap is its own identifiable block, and it is unknown.
	var missing []addetect.Block
	for _, b := range res.Diagnostics.Blocks {
		if b.Missing {
			missing = append(missing, b)
		}
	}
	if len(missing) != 1 || missing[0].StartUS != g.StartUS || missing[0].EndUS != g.EndUS || missing[0].Label != addetect.LabelUnknown {
		t.Errorf("missing-coverage blocks = %+v, want one unknown block over the gap", missing)
	}
	for _, s := range res.Segments {
		if s.StartUS < g.EndUS && s.EndUS > g.StartUS && s.Label != addetect.LabelUnknown {
			t.Errorf("segment [%d,%d) %s overlaps the gap", s.StartUS, s.EndUS, s.Label)
		}
	}
	// Audio is missing there too: unknown, not silent.
	for _, s := range res.Diagnostics.Events.Silence {
		if s.StartUS < g.EndUS && s.EndUS > g.StartUS {
			t.Errorf("silence event [%d,%d) inside the gap", s.StartUS, s.EndUS)
		}
	}
	// A threshold above the jump treats it as one long frame instead.
	wide := analyze(t, ffadapter.Path(path), set("input.timestamp_gap_threshold", "5s"))
	if len(wide.Timing.Gaps) != 0 || strings.Contains(strings.Join(warningCodes(wide), ","), "coverage_gaps") {
		t.Errorf("timestamp_gap_threshold=5s still reports gaps: %+v", wide.Timing.Gaps)
	}
}
