// SPDX-License-Identifier: Apache-2.0

package ffadapter_test

// End-to-end detection on the synthetic broadcast (annotated in
// scripts/make-fixtures.sh): AT-04, AT-09, AT-13, AT-20 on real decoded
// media, TEST-01 boundary accuracy and QUAL-01 repeatability.

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/ffadapter"
)

// TEST-01, AT-04, AT-13 / LOGO-01..05, SCORE-02, BREAK-01: the break of
// four exact 15/30 s spots is found within one frame interval plus one
// audio window of the true boundaries, from black + silence boundaries and
// the learned logo's absence.
func TestBroadcastBreakDetection(t *testing.T) {
	long(t)
	t.Parallel()
	res := broadcastRef(t)
	checkResult(t, res)
	checkBroadcast(t, res)

	// VIS-03: each 0.6 s black span is one dark event with exact frame
	// bounds (6 frames at 10 fps).
	dark := res.Diagnostics.Events.DarkUniform
	if len(dark) != 5 {
		t.Fatalf("dark/uniform events = %+v, want the five black spans", dark)
	}
	for i, d := range dark {
		start := bcUnits[i] - 300_000
		if d.Kind != "dark" || d.StartUS != start || d.EndUS != start+600_000 || d.Frames != 6 || d.MeanBrightness > 1 {
			t.Errorf("dark event %d = %+v, want 6 black frames at [%d,%d)", i, d, start, start+600_000)
		}
	}
	// AUDIO-02: the digital silence under each black span is one silence
	// event, placed by presentation time to within one window (plus the
	// AAC transform's smear at the cut).
	sil := res.Diagnostics.Events.Silence
	if len(sil) != 5 {
		t.Fatalf("silence events = %+v, want five", sil)
	}
	for i, s := range sil {
		start := bcUnits[i] - 300_000
		near(t, "silence start", s.StartUS, start, 2*bcWindowUS+5_000)
		near(t, "silence end", s.EndUS, start+600_000, 2*bcWindowUS+5_000)
		if s.PeakDBFS > -50 {
			t.Errorf("silence event %d peaks at %.1f dBFS", i, s.PeakDBFS)
		}
	}
	// BOUND-02: every unit boundary is a fused black + silence candidate
	// at the midpoint of the span, on a frame boundary.
	n := 0
	for _, b := range res.Diagnostics.Boundaries {
		hasDark, hasAV := false, false
		for _, e := range b.Evidence {
			hasDark = hasDark || e == "dark_uniform_boundary"
			hasAV = hasAV || e == "av_corroborated_boundary"
		}
		if !hasDark {
			continue
		}
		if n < len(bcUnits) {
			if b.TimeUS != bcUnits[n] {
				t.Errorf("boundary %s at %d, want the black span's midpoint %d", b.ID, b.TimeUS, bcUnits[n])
			}
			if !hasAV {
				t.Errorf("boundary %s lacks audio/visual corroboration: %+v", b.ID, b.Causes)
			}
			if b.TimeUS%bcFrameUS != 0 {
				t.Errorf("boundary %s at %d is not on a frame boundary", b.ID, b.TimeUS)
			}
		}
		n++
	}
	if n != len(bcUnits) {
		t.Errorf("%d dark boundaries, want %d", n, len(bcUnits))
	}

	// LOGO-01, LOGO-04: the logo was learned at its true place and
	// validated; occupancy is the program share of the recording.
	cal := res.Calibration.Logo
	if len(cal) != 1 || cal[0].Origin != "learned" || cal[0].Status != addetect.StatusUsable || cal[0].Box == nil || cal[0].Occupancy == nil {
		t.Fatalf("logo calibration = %+v, want one validated learned logo", cal)
	}
	box := *cal[0].Box
	// The fixture draws the logo at x 252..296, y 14..38 of 320x180.
	if box.X*320 < 247 || box.X*320 > 256 || (box.X+box.W)*320 < 292 || (box.X+box.W)*320 > 301 ||
		box.Y*180 < 9 || box.Y*180 > 18 || (box.Y+box.H)*180 < 34 || (box.Y+box.H)*180 > 43 {
		t.Errorf("learned logo box = %+v, want about x 252..296, y 14..38 of 320x180", box)
	}
	if want := 190.0 / 280.6; math.Abs(*cal[0].Occupancy-want) > 0.03 {
		t.Errorf("logo occupancy = %.3f, want about %.3f", *cal[0].Occupancy, want)
	}
	// LOGO-03: presence intervals follow the program/break structure.
	var states []string
	for _, l := range res.Diagnostics.Events.Logo {
		states = append(states, l.State)
		switch l.State {
		case "absent":
			near(t, "logo absent from", l.StartUS, bcBreakIn-300_000, 3_000_000)
			near(t, "logo absent until", l.EndUS, bcBreakOut+300_000, 3_000_000)
		}
	}
	if len(states) != 3 || states[0] != "present" || states[1] != "absent" || states[2] != "present" {
		t.Errorf("logo states = %v, want present, absent, present", states)
	}
	// AT-13: strict standard duration (2.0) x validated logo absence (2.0).
	want := []int64{15_000_000, 30_000_000, 15_000_000, 30_000_000}
	for i, u := range res.AdUnits {
		if u.Score != 4 {
			t.Errorf("unit %d score = %v, want 4.0 (strict duration x logo absent): %+v", i, u.Score, u.Factors)
		}
		if got := u.EndUS - u.StartUS; got != want[i] {
			t.Errorf("unit %d lasts %d us, want %d", i, got, want[i])
		}
		rules := map[string]bool{}
		for _, f := range u.Factors {
			rules[f.Family] = true
			if f.TargetUS != nil && *f.TargetUS != want[i] {
				t.Errorf("unit %d matched standard duration %d, want %d", i, *f.TargetUS, want[i])
			}
		}
		if len(u.Factors) != 2 || !rules["duration"] || !rules["logo"] {
			t.Errorf("unit %d factors = %+v, want one duration and one logo factor", i, u.Factors)
		}
	}
	// SCORE-07: both program parts carry positive program evidence.
	for _, s := range res.Segments {
		if s.Label == addetect.LabelProgram && (len(s.Reasons) == 0 || s.Reasons[0] != "logo_present") {
			t.Errorf("program segment [%d,%d) reasons = %v", s.StartUS, s.EndUS, s.Reasons)
		}
		if s.Coverage != 1 {
			t.Errorf("segment [%d,%d) coverage = %v, want 1", s.StartUS, s.EndUS, s.Coverage)
		}
	}
	if res.Timing.AudioStartOffsetUS == nil || *res.Timing.AudioStartOffsetUS != 0 || res.Timing.NormalizationOffsetUS != 0 {
		t.Errorf("timing = %+v, want audio and video starting together at 0", res.Timing)
	}
}

// normalized serializes a result without the fields QUAL-01 lets vary.
func normalized(t testing.TB, res *addetect.Result) []byte {
	t.Helper()
	c := *res
	c.Config.Effective = map[string]string{}
	for k, v := range res.Config.Effective {
		if k != "resources.workers" {
			c.Config.Effective[k] = v
		}
	}
	b, err := json.MarshalIndent(&c, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func firstDiff(a, b []byte) string {
	la, lb := bytes.Split(a, []byte("\n")), bytes.Split(b, []byte("\n"))
	for i := 0; i < len(la) && i < len(lb); i++ {
		if !bytes.Equal(la[i], lb[i]) {
			return "line " + string(la[i]) + " vs " + string(lb[i])
		}
	}
	return "different lengths"
}

// AT-20 / QUAL-01: the same input, configuration and software give the
// same document, run after run, and worker scheduling (decoder threads)
// does not alter any decision, boundary or score explanation.
func TestBroadcastDeterminism(t *testing.T) {
	long(t)
	t.Parallel()
	src := ffadapter.Path(fixture(t, "broadcast", "broadcast.mp4"))
	ref := normalized(t, broadcastRef(t))

	again := analyze(t, src)
	if got := normalized(t, again); !bytes.Equal(got, ref) {
		t.Errorf("a second run differs from the first: %s", firstDiff(got, ref))
	}
	one := analyze(t, src, set("resources.workers", "1"))
	if got := normalized(t, one); !bytes.Equal(got, ref) {
		t.Errorf("resources.workers=1 differs from the default: %s", firstDiff(got, ref))
	}
	if one.Config.Digest != again.Config.Digest || one.FeatureID != again.FeatureID {
		t.Errorf("the worker count changed the configuration digest or feature identity")
	}
	many := analyze(t, src, set("resources.workers", "7"))
	if got := normalized(t, many); !bytes.Equal(got, ref) {
		t.Errorf("resources.workers=7 differs from the default: %s", firstDiff(got, ref))
	}
}

// AT-25 (single-file forms) / MEDIA-02, QUAL-01: the same coded samples in
// a transport stream (nonzero start, no priming edit) give the same
// detections within one frame interval.
func TestBroadcastTransportStreamMatchesMP4(t *testing.T) {
	long(t)
	t.Parallel()
	path := fixture(t, "broadcast_ts", "broadcast.ts")
	res := analyze(t, ffadapter.Path(path))
	checkBroadcast(t, res)
	sameDetections(t, "MPEG-TS", res, broadcastRef(t), bcFrameUS)
	if res.Timing.NormalizationOffsetUS <= 0 {
		t.Errorf("normalization offset = %d, want the stream's nonzero start", res.Timing.NormalizationOffsetUS)
	}
	if want := probeFrameCount(t, path); res.Timing.Frames != want {
		t.Errorf("frames = %d, ffprobe counts %d", res.Timing.Frames, want)
	}
}

// AT-17 / BREAK-05: skip padding only shapes the derived skip list; the
// canonical segments, breaks and units are unchanged.
func TestBroadcastSkipPaddingKeepsCanonicalSegments(t *testing.T) {
	long(t)
	t.Parallel()
	src := ffadapter.Path(fixture(t, "broadcast", "broadcast.mp4"))
	ref := broadcastRef(t)

	padded := analyze(t, src, set("outputs.skip_padding_start", "-2s"), set("outputs.skip_padding_end", "-1s"))
	sameDetections(t, "skip padding", padded, ref, 0)
	if len(padded.SkipIntervals) != 1 {
		t.Fatalf("skip intervals = %+v", padded.SkipIntervals)
	}
	sk := padded.SkipIntervals[0]
	if sk.StartUS != ref.AdBreaks[0].StartUS+2_000_000 || sk.EndUS != ref.AdBreaks[0].EndUS-1_000_000 {
		t.Errorf("skip interval = %+v, want the break shrunk by 2 s / 1 s", sk)
	}
	if len(ref.SkipIntervals) != 1 || ref.SkipIntervals[0].StartUS != ref.AdBreaks[0].StartUS || ref.SkipIntervals[0].EndUS != ref.AdBreaks[0].EndUS {
		t.Errorf("default skip intervals = %+v, want the unpadded break", ref.SkipIntervals)
	}
}
