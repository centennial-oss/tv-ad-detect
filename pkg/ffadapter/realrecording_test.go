// SPDX-License-Identifier: Apache-2.0

package ffadapter_test

// Optional local-only run over a real DVR recording. the test asset is
// not redistributable and is never copied or used to derive fixtures; the
// test skips when the file is absent, with -short, and (because it decodes
// 35 minutes of 1080p video) under the race detector unless
// TVAD_REAL_RECORDING=1 is set.
//
// ref/sample-dvr.mp4 may be a symbolic link. The assertions expect a
// captioned 1080-line H.264 recording at 29.97 frames per second with AAC
// audio, such as the MP4 that scripts/make-ref-derivatives.sh builds from
// a 1080i capture (see doc/development.md).

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/ffadapter"
)

const testAsset = "sample-dvr.mp4"

// AT-22 (usable captions on real carriage), AT-18, AT-20 / MEDIA-02..05,
// CAP-01, QUAL-01, QUAL-02: a real H.264/AAC recording with CEA-608 in
// A/53 user data analyzes completely, with caption styles decoded from the
// video stream, on a timeline that matches the container, within the
// documented memory behavior. Detected breaks are logged, not asserted:
// this is not an accuracy evaluation (TEST-02).
func TestRealRecordingOptional(t *testing.T) {
	if testing.Short() {
		t.Skip("optional real-recording run; skipped with -short")
	}
	path, err := filepath.Abs(filepath.Join("..", "..", "ref", testAsset))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skip("ref/" + testAsset + " is not present (optional, local-only input)")
	}
	if raceEnabled && os.Getenv("TVAD_REAL_RECORDING") != "1" {
		t.Skip("skipped under the race detector; set TVAD_REAL_RECORDING=1 to run it")
	}

	var progress []addetect.Progress
	start := time.Now()
	res, err := addetect.Analyze(context.Background(), ffadapter.Path(path), testConfig(), &addetect.Options{
		Progress: func(p addetect.Progress) { progress = append(progress, p) },
	})
	wall := time.Since(start)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	checkResult(t, res)
	if !res.Complete {
		t.Errorf("not complete")
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	t.Logf("wall %.1fs, %d frames, %.1f s of media (%.0fx real time), Go heap in use %d MiB, process RSS %d MiB",
		wall.Seconds(), res.Timing.Frames, float64(res.DurationUS)/1e6, float64(res.DurationUS)/1e6/wall.Seconds(),
		ms.HeapInuse>>20, residentKB()>>10)
	for _, b := range res.AdBreaks {
		t.Logf("break %s: %.2f s .. %.2f s (%.1f s, %d units)", b.ID, float64(b.StartUS)/1e6, float64(b.EndUS)/1e6, float64(b.EndUS-b.StartUS)/1e6, len(b.UnitIDs))
	}
	var labels []string
	for _, s := range res.Segments {
		labels = append(labels, s.Label+"@"+strconv.FormatInt(s.StartUS/1_000_000, 10))
	}
	t.Logf("segments: %s", strings.Join(labels, " "))

	// The timeline agrees with the video track's own duration. The
	// container's duration is not the reference: a tuner capture starts its
	// audio before the first decodable video frame, so the container spans
	// more than the video timeline the result is measured on.
	dur := decimalUS(t, strings.TrimSpace(ffprobe(t, "-select_streams", "v:0", "-show_entries", "stream=duration", "-of", "csv=p=0", path)))
	near(t, "duration_us vs video track duration", res.DurationUS, dur, 1_000_000)
	v := res.Tracks.Video
	if v.Codec != "h264" || v.CodedWidth != 1920 || v.CodedHeight != 1080 || v.AnalysisScale != 5 || v.AnalysisWidth != 384 || v.AnalysisHeight != 216 {
		t.Errorf("video track = %+v, want 1080p H.264 reduced by 5 to 384x216", v)
	}
	if res.Tracks.Audio == nil || res.Tracks.Audio.Codec != "aac" {
		t.Errorf("audio track = %+v", res.Tracks.Audio)
	}
	if res.Timing.DecodeErrors != 0 || len(res.Timing.Gaps) != 0 || res.Timing.MissingTimestamps != 0 {
		t.Errorf("timing anomalies: %+v", res.Timing)
	}
	near(t, "median frame duration", res.Timing.MedianFrameDurationUS, 33_367, 1)

	// CAP-01: CEA-608 carried in the H.264 stream yields recognized styles.
	if c := res.Tracks.Captions; c.Source != "embedded_cea608" || c.Capability != "cea608" {
		t.Errorf("caption source = %+v", c)
	}
	d := detector(t, res, addetect.SignalCaptions)
	if d.Status != addetect.StatusUsable || d.Events == 0 || d.Coverage == nil || *d.Coverage < 0.9 {
		t.Errorf("captions detector = %+v, want usable with coverage", d)
	}
	styles := map[string]bool{}
	for _, c := range res.Diagnostics.Events.Captions {
		styles[c.Style] = true
	}
	if !styles["roll_up"] && !styles["pop_on"] && !styles["paint_on"] {
		t.Errorf("caption styles %v: no recognized presentation style", styles)
	}
	for _, sig := range []string{addetect.SignalDarkUniform, addetect.SignalScene, addetect.SignalAudio, addetect.SignalAspect} {
		if d := detector(t, res, sig); d.Status != addetect.StatusUsable {
			t.Errorf("%s detector = %+v, want usable", sig, d)
		}
	}
	// API-05: extraction progress is monotonic in media time and is
	// followed by the classification and done stages.
	if len(progress) < 100 {
		t.Fatalf("%d progress reports", len(progress))
	}
	last := int64(-1)
	for _, p := range progress {
		if p.Stage == addetect.StageExtract {
			if p.MediaTimeUS < last {
				t.Fatalf("progress went backwards: %d after %d", p.MediaTimeUS, last)
			}
			last = p.MediaTimeUS
		}
	}
	if n := len(progress); progress[n-2].Stage != addetect.StageClassify || progress[n-1].Stage != addetect.StageDone {
		t.Errorf("final progress stages = %s, %s", progress[n-2].Stage, progress[n-1].Stage)
	}
	// QUAL-02: compact features only - nothing near the size of the video.
	if rss := residentKB() >> 10; rss > 1024 {
		t.Errorf("process RSS is %d MiB after analyzing a 1.5 GiB recording", rss)
	}
}
