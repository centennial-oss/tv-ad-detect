// SPDX-License-Identifier: Apache-2.0

package ffadapter_test

// AT-22: the packaged adapter's container/codec/pixel-format capabilities,
// track selection and its explicit behavior for unsupported input
// (MEDIA-01..05, CFG-03, API-04).

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/ffadapter"
)

// AT-22 / MEDIA-02, MEDIA-03: MP4, Matroska and MPEG-TS, each with H.264 +
// AAC and H.264 + AC-3. Every decoded frame is delivered (the count equals
// ffprobe -count_frames, so the end-of-stream drain loses nothing) and the
// chosen tracks and their properties are reported.
func TestContainerCodecMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		file, container, acodec string
		channels                int
	}{
		{"mp4_aac.mp4", "mp4", "aac", 1},
		{"mp4_ac3.mp4", "mp4", "ac3", 2},
		{"mkv_aac.mkv", "matroska", "aac", 1},
		{"mkv_ac3.mkv", "matroska", "ac3", 2},
		{"ts_aac.ts", "mpegts", "aac", 1},
		{"ts_ac3.ts", "mpegts", "ac3", 2},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			path := fixture(t, "matrix", tc.file)
			res := analyze(t, ffadapter.Path(path))

			if !res.Complete {
				t.Errorf("not complete")
			}
			if res.Input.Kind != "file" || res.Input.Path != path || !strings.Contains(res.Input.Container, tc.container) {
				t.Errorf("input = %+v, want a file in a %s container", res.Input, tc.container)
			}
			if res.Adapter.Name != ffadapter.AdapterName || res.Adapter.Version != ffadapter.AdapterVersion ||
				!strings.Contains(res.Adapter.Libraries["ffmpeg"], "libavformat") {
				t.Errorf("adapter identity = %+v", res.Adapter)
			}
			v := res.Tracks.Video
			if v.Codec != "h264" || v.CodedWidth != 320 || v.CodedHeight != 180 || v.BitDepth != 8 ||
				v.PixelFormat != "yuv420p" || v.FrameRate != "15/1" || v.SAR != "1:1" || v.TimeBase == "" {
				t.Errorf("video track = %+v", v)
			}
			if !strings.HasPrefix(v.Selection, "auto") || v.Index != 0 {
				t.Errorf("video selection = %q index %d, want automatic stream 0", v.Selection, v.Index)
			}
			if v.AnalysisWidth != 320 || v.AnalysisHeight != 180 || v.AnalysisScale != 1 {
				t.Errorf("analysis geometry = %dx%d /%d", v.AnalysisWidth, v.AnalysisHeight, v.AnalysisScale)
			}
			a := res.Tracks.Audio
			if a == nil || a.Codec != tc.acodec || a.SampleRate != 48000 || a.Channels != tc.channels || a.Index != 1 ||
				!strings.HasPrefix(a.Selection, "auto") {
				t.Fatalf("audio track = %+v, want automatically selected %s", a, tc.acodec)
			}
			if d := detector(t, res, addetect.SignalAudio); d.Status != addetect.StatusUsable || d.Coverage == nil || *d.Coverage < 0.97 {
				t.Errorf("audio detector = %+v, want usable with full coverage", d)
			}
			// H.264 user data is inspected for CEA-608, and none is present:
			// captions are unavailable, never a style (MEDIA-04).
			if c := res.Tracks.Captions; c.Source != "embedded_cea608" || c.Capability != "cea608" {
				t.Errorf("caption capability = %+v", c)
			}
			if d := detector(t, res, addetect.SignalCaptions); d.Status != addetect.StatusUnavailable {
				t.Errorf("captions detector = %+v, want unavailable", d)
			}

			if want := probeFrameCount(t, path); res.Timing.Frames != want {
				t.Errorf("decoded %d frames, ffprobe -count_frames reports %d", res.Timing.Frames, want)
			}
			if res.Timing.DecodeErrors != 0 || res.Timing.MissingTimestamps != 0 || res.Timing.Overlaps != 0 {
				t.Errorf("timing anomalies on a clean file: %+v", res.Timing)
			}
			// Four seconds of 15 fps video; Matroska rounds to milliseconds.
			near(t, "duration_us", res.DurationUS, 4_000_000, 1500)
			if res.Timing.FinalFrameDurationEstimated {
				t.Errorf("final frame duration was estimated although the container declares it")
			}
		})
	}
}

// AT-18, AT-22 / MEDIA-04: a recording without audio completes; only the
// audio evidence is unavailable and nothing is measured as silence.
func TestNoAudioTrack(t *testing.T) {
	t.Parallel()
	res := analyze(t, ffadapter.Path(fixture(t, "noaudio", "noaudio.mp4")))
	if !res.Complete || res.Tracks.Audio != nil {
		t.Errorf("complete=%v audio=%+v, want a complete result without an audio track", res.Complete, res.Tracks.Audio)
	}
	d := detector(t, res, addetect.SignalAudio)
	if d.Status != addetect.StatusUnavailable || d.Reason != "no_audio" || d.Events != 0 || d.CoverageUS != nil || d.Boundary {
		t.Errorf("audio detector = %+v, want unavailable/no_audio with null coverage", d)
	}
	if !hasWarning(res, "no_audio") {
		t.Errorf("warnings = %v, want no_audio", warningCodes(res))
	}
	if res.Timing.AudioStartOffsetUS != nil {
		t.Errorf("audio_start_offset_us = %d, want null", *res.Timing.AudioStartOffsetUS)
	}
	if n := len(res.Diagnostics.Events.Silence); n != 0 {
		t.Errorf("%d silence events from a recording without audio", n)
	}
	if res.Baselines.AudioEnergy.Reliable || res.Baselines.AudioEnergy.Value != nil {
		t.Errorf("audio baseline = %+v, want unavailable", res.Baselines.AudioEnergy)
	}
}

// AT-22 / MEDIA-03: two audio tracks. The default-flagged one (stream 2,
// digital silence) is chosen automatically; explicit selection of stream 1
// (a tone) is honored. The tracks are never mixed: one is all silence, the
// other has none.
func TestExplicitAudioTrackSelection(t *testing.T) {
	t.Parallel()
	path := fixture(t, "two_audio", "two_audio.mkv")

	auto := analyze(t, ffadapter.Path(path))
	if a := auto.Tracks.Audio; a == nil || a.Index != 2 || a.Codec != "ac3" || !strings.HasPrefix(a.Selection, "auto") {
		t.Fatalf("automatic audio track = %+v, want default-flagged stream 2 (ac3)", a)
	}
	sil := auto.Diagnostics.Events.Silence
	if len(sil) != 1 || sil[0].StartUS > 100_000 || sil[0].EndUS < auto.DurationUS-100_000 {
		t.Errorf("silence events on the silent track = %+v, want one spanning the recording", sil)
	}

	exp := analyze(t, ffadapter.Path(path), set("input.audio_track", "1"))
	if a := exp.Tracks.Audio; a == nil || a.Index != 1 || a.Codec != "aac" || a.Selection != "explicit" {
		t.Fatalf("explicit audio track = %+v, want stream 1 (aac)", a)
	}
	if n := len(exp.Diagnostics.Events.Silence); n != 0 {
		t.Errorf("%d silence events on the tone track", n)
	}

	none := analyze(t, ffadapter.Path(path), set("input.audio_track", "none"))
	if none.Tracks.Audio != nil || detector(t, none, addetect.SignalAudio).Status != addetect.StatusUnavailable {
		t.Errorf("audio_track=none still used audio: %+v", none.Tracks.Audio)
	}
	if hasWarning(none, "no_audio") || hasWarning(none, "audio_unavailable") {
		t.Errorf("audio_track=none warned about missing audio: %v", warningCodes(none))
	}
}

// AT-22 / MEDIA-03: an attached-picture (cover art) video stream is never
// selected automatically and cannot be selected explicitly.
func TestAttachedPictureNotSelected(t *testing.T) {
	t.Parallel()
	path := fixture(t, "coverart", "coverart.mp4")
	// Locate the streams with ffprobe rather than assuming an order.
	out := ffprobe(t, "-show_entries", "stream=index,codec_name:stream_disposition=attached_pic", "-of", "csv=p=0", path)
	video, cover := -1, -1
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(strings.TrimSpace(line), ",")
		if len(f) < 3 {
			continue
		}
		idx, err := strconv.Atoi(f[0])
		if err != nil {
			t.Fatalf("ffprobe stream line %q", line)
		}
		switch {
		case f[1] == "h264":
			video = idx
		case f[2] == "1":
			cover = idx
		}
	}
	if video < 0 || cover < 0 {
		t.Fatalf("fixture has no cover-art stream: %q", out)
	}

	res := analyze(t, ffadapter.Path(path))
	if v := res.Tracks.Video; v.Index != video || v.Codec != "h264" || v.CodedWidth != 320 {
		t.Errorf("selected video = %+v, want the h264 stream %d", v, video)
	}
	if res.Timing.Frames != 60 {
		t.Errorf("frames = %d, want 60 (the still picture must not be analyzed)", res.Timing.Frames)
	}

	for _, sel := range []string{strconv.Itoa(cover), "9"} {
		r, err := tryAnalyze(ffadapter.Path(path), set("input.video_track", sel))
		wantKind(t, r, err, addetect.KindInvalidConfig)
	}
	// The audio stream is not a video track either.
	r, err := tryAnalyze(ffadapter.Path(path), set("input.video_track", "1"))
	wantKind(t, r, err, addetect.KindInvalidConfig)
}

// AT-22 / CFG-03, MEDIA-04: a requested optional stream that is unavailable
// follows the strict-or-degraded policy; the default is degraded with a
// warning and only that evidence disabled.
func TestUnavailableOptionalStreamPolicy(t *testing.T) {
	t.Parallel()
	src := ffadapter.Path(fixture(t, "matrix", "mp4_aac.mp4"))

	t.Run("audio degraded", func(t *testing.T) {
		res := analyze(t, src, set("input.audio_track", "7"))
		if !res.Complete || res.Tracks.Audio != nil || !hasWarning(res, "audio_unavailable") {
			t.Errorf("complete=%v audio=%+v warnings=%v", res.Complete, res.Tracks.Audio, warningCodes(res))
		}
		if d := detector(t, res, addetect.SignalAudio); d.Status != addetect.StatusUnavailable {
			t.Errorf("audio detector = %+v, want unavailable", d)
		}
		// A video stream index is not an audio track.
		res = analyze(t, src, set("input.audio_track", "0"))
		if res.Tracks.Audio != nil || !hasWarning(res, "audio_unavailable") {
			t.Errorf("audio_track=0 (video stream): audio=%+v warnings=%v", res.Tracks.Audio, warningCodes(res))
		}
	})
	t.Run("audio strict", func(t *testing.T) {
		res, err := tryAnalyze(src, set("input.audio_track", "7"), set("input.optional_stream_policy", "strict"))
		wantKind(t, res, err, addetect.KindInvalidConfig)
	})
	t.Run("captions degraded", func(t *testing.T) {
		res := analyze(t, src, set("input.caption_track", "1"))
		if !res.Complete || !hasWarning(res, "captions_unavailable") {
			t.Errorf("complete=%v warnings=%v", res.Complete, warningCodes(res))
		}
		if d := detector(t, res, addetect.SignalCaptions); d.Status != addetect.StatusUnavailable {
			t.Errorf("captions detector = %+v, want unavailable", d)
		}
		// The audio evidence is untouched.
		if d := detector(t, res, addetect.SignalAudio); d.Status != addetect.StatusUsable {
			t.Errorf("audio detector = %+v, want usable", d)
		}
	})
	t.Run("captions strict", func(t *testing.T) {
		res, err := tryAnalyze(src, set("input.caption_track", "1"), set("input.optional_stream_policy", "strict"))
		wantKind(t, res, err, addetect.KindInvalidConfig)
	})
	t.Run("malformed selection", func(t *testing.T) {
		cfg := addetect.DefaultConfig()
		cfg.Input.AudioTrack = "first"
		res, err := addetect.Analyze(context.Background(), src, cfg, nil)
		wantKind(t, res, err, addetect.KindInvalidConfig)
	})
}

// AT-22 / MEDIA-05: full-range and limited-range luma, 8 and 10 bit, are
// normalized to one canonical 0-255 scale: a flat picture of level v
// measures v whichever way it is coded.
func TestLumaRangeAndBitDepthNormalization(t *testing.T) {
	t.Parallel()
	for _, v := range []int{0, 64, 128, 255} {
		for _, rng := range []string{"limited", "full"} {
			name := "gray_" + strconv.Itoa(v) + "_" + rng + ".mp4"
			t.Run(name, func(t *testing.T) {
				_, frames := analyzeFrames(t, ffadapter.Path(fixture(t, "gray", name)))
				if len(frames) != 15 {
					t.Fatalf("%d frames, want 15", len(frames))
				}
				for _, f := range frames {
					if math.Abs(f.Brightness-float64(v)) > 1 || f.Nonuniformity > 0.5 {
						t.Fatalf("frame at %d: brightness %.3f nonuniformity %.3f, want %d flat", f.TimeUS, f.Brightness, f.Nonuniformity, v)
					}
					// The full-resolution bright-pixel count uses the same
					// canonical scale (threshold dark.bright_pixel_luma = 60).
					want := 0.0
					if v > 60 {
						want = 1
					}
					if math.Abs(f.BrightFraction-want) > 1e-4 {
						t.Fatalf("frame at %d: bright fraction %.5f, want %.0f", f.TimeUS, f.BrightFraction, want)
					}
				}
			})
		}
	}
	t.Run("10 bit", func(t *testing.T) {
		path := fixture(t, "gray", "gray_128_limited_10.mp4")
		if _, err := os.Stat(path); err != nil {
			t.Skip("the local libx264 cannot encode 10-bit H.264")
		}
		res, frames := analyzeFrames(t, ffadapter.Path(path))
		if res.Tracks.Video.BitDepth != 10 {
			t.Errorf("bit depth = %d, want 10", res.Tracks.Video.BitDepth)
		}
		for _, f := range frames {
			if math.Abs(f.Brightness-128) > 1 || f.Nonuniformity > 0.5 || f.BrightFraction < 0.9999 {
				t.Fatalf("10-bit frame: brightness %.3f nonuniformity %.3f bright %.4f, want 128 flat", f.Brightness, f.Nonuniformity, f.BrightFraction)
			}
		}
	})
	// Unsignalled range is treated as limited and says so (MEDIA-05:
	// range assumptions are explicit).
	res := analyze(t, ffadapter.Path(fixture(t, "gray", "gray_128_limited.mp4")))
	if res.Tracks.Video.ColorRange == "unknown" && !hasWarning(res, "color_range_assumed") {
		t.Errorf("unsignalled range without a color_range_assumed warning: %v", warningCodes(res))
	}
	full := analyze(t, ffadapter.Path(fixture(t, "gray", "gray_128_full.mp4")))
	if full.Tracks.Video.ColorRange != "pc" || hasWarning(full, "color_range_assumed") {
		t.Errorf("full-range file: range %q warnings %v", full.Tracks.Video.ColorRange, warningCodes(full))
	}
}

// AT-22 / MEDIA-05 (review of the C box filter): the analysis image is an
// integer box reduction whose remainder columns/rows are dropped. The
// 770x434 fixture (factor 3 -> 256x144) has white columns 0..384, black
// columns 385.., and a mid-gray band over rows 0..99 of the white part, so
// a wrong stride, factor or rounding shows up in individual pixels.
func TestLumaDownsamplingGeometry(t *testing.T) {
	t.Parallel()
	path := fixture(t, "luma_split", "luma_split.mp4")
	rs := readStream(t, ffadapter.Path(path), nil)
	if len(rs.frames) != 15 {
		t.Fatalf("%d frames, want 15", len(rs.frames))
	}
	f := rs.frames[0]
	if f.codedW != 770 || f.codedH != 434 || f.scale != 3 || f.w != 256 || f.h != 144 {
		t.Fatalf("geometry: coded %dx%d scale %d analysis %dx%d, want 770x434 /3 -> 256x144", f.codedW, f.codedH, f.scale, f.w, f.h)
	}

	// Expected reduced picture, computed here from the fixture's definition
	// with the same documented rule (addetect.DownsampleLuma: rounded mean
	// of each 3x3 block, remainder dropped).
	full := make([]byte, 770*434)
	for y := 0; y < 434; y++ {
		for x := 0; x < 385; x++ {
			if y < 100 {
				full[y*770+x] = 128
			} else {
				full[y*770+x] = 255
			}
		}
	}
	want := make([]byte, 256*144)
	if w, h := addetect.DownsampleLuma(want, full, 770, 434, 770, 3); w != 256 || h != 144 {
		t.Fatalf("reference reduction is %dx%d", w, h)
	}

	st, err := ffadapter.Path(path).Open(context.Background(), openRequest(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var ev addetect.Event
	for {
		if err := st.Next(&ev); err != nil {
			t.Fatalf("no video frame: %v", err)
		}
		if ev.Kind == addetect.EventVideo {
			break
		}
	}
	got := ev.Video.Luma
	bad := 0
	for i := range want {
		// One code value of slack for the limited-range conversion.
		if d := int(got[i]) - int(want[i]); d < -1 || d > 1 {
			if bad++; bad <= 5 {
				t.Errorf("analysis pixel (%d,%d) = %d, want %d", i%256, i/256, got[i], want[i])
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d analysis pixels differ from the box-filter reference", bad, len(want))
	}
	// Bright pixels (> 60) at full resolution over the 768x432 covered
	// area: columns 0..384 of every row.
	if wantFrac := 385.0 / 768.0; math.Abs(float64(ev.Video.FullResBrightFraction)-wantFrac) > 1e-4 {
		t.Errorf("full-resolution bright fraction = %.5f, want %.5f", ev.Video.FullResBrightFraction, wantFrac)
	}

	res := analyze(t, ffadapter.Path(path))
	if v := res.Tracks.Video; v.AnalysisWidth != 256 || v.AnalysisHeight != 144 || v.AnalysisScale != 3 {
		t.Errorf("reported analysis geometry %dx%d /%d, want 256x144 /3", v.AnalysisWidth, v.AnalysisHeight, v.AnalysisScale)
	}
	// A smaller analysis bound raises the factor: ceil(770/128) = 7 -> 110x62.
	res = analyze(t, ffadapter.Path(path), set("sampling.analysis_max_width", "128"), set("sampling.analysis_max_height", "72"))
	if v := res.Tracks.Video; v.AnalysisWidth != 110 || v.AnalysisHeight != 62 || v.AnalysisScale != 7 {
		t.Errorf("reported analysis geometry %dx%d /%d, want 110x62 /7", v.AnalysisWidth, v.AnalysisHeight, v.AnalysisScale)
	}
}

// AT-22 / MEDIA-02, MEDIA-05: 10-bit H.264 decodes and is measured on the
// same scale as 8-bit (skipped when the local libx264 is 8-bit only).
func TestTenBitH264(t *testing.T) {
	t.Parallel()
	path := fixture(t, "hi10", "hi10.mp4")
	if _, err := os.Stat(path); err != nil {
		t.Skip("the local libx264 cannot encode 10-bit H.264")
	}
	res, frames := analyzeFrames(t, ffadapter.Path(path))
	v := res.Tracks.Video
	if v.BitDepth != 10 || v.PixelFormat != "yuv420p10le" || !strings.Contains(v.Profile, "10") {
		t.Errorf("video track = %+v, want 10-bit", v)
	}
	if res.Timing.Frames != 30 || !res.Complete {
		t.Errorf("frames=%d complete=%v", res.Timing.Frames, res.Complete)
	}
	// The same pattern in 8 bit measures the same brightness.
	_, ref := analyzeFrames(t, ffadapter.Path(fixture(t, "matrix", "mp4_aac.mp4")))
	if d := math.Abs(frames[0].Brightness - ref[0].Brightness); d > 2 {
		t.Errorf("10-bit brightness %.2f vs 8-bit %.2f for the same picture", frames[0].Brightness, ref[0].Brightness)
	}
}

// AT-22, AT-08 / MEDIA-03, ASPECT-01: a non-square sample aspect ratio is
// exposed and enters the displayed active-picture ratio (240x180 at SAR
// 4:3 displays as 16:9).
func TestSampleAspectRatio(t *testing.T) {
	t.Parallel()
	path := fixture(t, "sar", "sar.mp4")
	res := analyze(t, ffadapter.Path(path))
	if v := res.Tracks.Video; v.SAR != "4:3" || v.CodedWidth != 240 || v.CodedHeight != 180 {
		t.Errorf("video track = %+v, want 240x180 SAR 4:3", v)
	}
	rs := readStream(t, ffadapter.Path(path), nil)
	for _, f := range rs.frames {
		if f.sarN != 4 || f.sarD != 3 {
			t.Fatalf("frame SAR = %d:%d, want 4:3", f.sarN, f.sarD)
		}
	}
	asp := res.Diagnostics.Events.Aspect
	if len(asp) == 0 || asp[0].Ratio == nil {
		t.Fatalf("no active-picture ratio: %+v", asp)
	}
	if r := *asp[0].Ratio; math.Abs(r-16.0/9.0) > 0.03 {
		t.Errorf("displayed active ratio = %.4f, want 1.7778 (coded 4:3 x SAR 4:3)", r)
	}
}

// AT-22 / MEDIA-05: an HDR (PQ) transfer is never analyzed silently with
// SDR thresholds: by default it warns, under
// input.unsupported_format_policy=fail it is a capability error.
func TestHDRTransferPolicy(t *testing.T) {
	t.Parallel()
	src := ffadapter.Path(fixture(t, "hdr", "hdr.mp4"))
	res := analyze(t, src)
	if res.Tracks.Video.ColorTransfer != "smpte2084" {
		t.Fatalf("color transfer = %q, want smpte2084", res.Tracks.Video.ColorTransfer)
	}
	if !hasWarning(res, "hdr_degraded") || !res.Complete {
		t.Errorf("complete=%v warnings=%v, want a completed analysis with hdr_degraded", res.Complete, warningCodes(res))
	}
	r, err := tryAnalyze(src, set("input.unsupported_format_policy", "fail"))
	wantKind(t, r, err, addetect.KindUnsupported)
	if r != nil {
		t.Errorf("a capability error returned a result")
	}
	// SDR input is unaffected by the strict policy.
	sdr := analyze(t, ffadapter.Path(fixture(t, "matrix", "mp4_aac.mp4")), set("input.unsupported_format_policy", "fail"))
	if hasWarning(sdr, "hdr_degraded") {
		t.Errorf("SDR input warned hdr_degraded")
	}
}

// AT-22 / MEDIA-05: interlaced coding is analyzed with an explicit warning
// stating the assumption (woven frames, no deinterlacing).
func TestInterlacedIsExplicit(t *testing.T) {
	t.Parallel()
	path := fixture(t, "interlaced", "interlaced.ts")
	res := analyze(t, ffadapter.Path(path))
	if fo := res.Tracks.Video.FieldOrder; fo != "tt" {
		t.Errorf("field order = %q, want tt", fo)
	}
	if !hasWarning(res, "interlaced_video") || !res.Complete {
		t.Errorf("complete=%v warnings=%v, want interlaced_video", res.Complete, warningCodes(res))
	}
	if want := probeFrameCount(t, path); res.Timing.Frames != want {
		t.Errorf("frames = %d, ffprobe counts %d", res.Timing.Frames, want)
	}
	prog := analyze(t, ffadapter.Path(fixture(t, "matrix", "ts_aac.ts")))
	if hasWarning(prog, "interlaced_video") {
		t.Errorf("progressive input warned interlaced_video")
	}
}

// AT-22 / MEDIA-04, API-04: inputs that cannot be analyzed fail with
// distinguishable error kinds and no result.
func TestUnusableInputErrorKinds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	empty := writeFile(t, filepath.Join(dir, "empty.mp4"), "")
	cases := []struct {
		name string
		src  addetect.Source
		kind addetect.ErrorKind
	}{
		{"missing file", ffadapter.Path(filepath.Join(dir, "nope.mp4")), addetect.KindIO},
		{"directory", ffadapter.Path(dir), addetect.KindIO},
		{"empty path", ffadapter.Path(""), addetect.KindInvalidConfig},
		{"remote url", ffadapter.Path("https://example.invalid/recording.mp4"), addetect.KindUnsupported},
		{"not a media file", ffadapter.Path(fixture(t, "audio_only", "not_media.mp4")), addetect.KindUnsupported},
		{"empty file", ffadapter.Path(empty), addetect.KindUnsupported},
		{"audio only", ffadapter.Path(fixture(t, "audio_only", "audio_only.mp4")), addetect.KindUnsupported},
		{"missing manifest", ffadapter.Path(filepath.Join(dir, "nope.m3u8")), addetect.KindIO},
		{"nil source", nil, addetect.KindInvalidConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tryAnalyze(tc.src)
			wantKind(t, res, err, tc.kind)
			if res != nil {
				t.Errorf("an input that cannot be opened returned a result")
			}
		})
	}
}

// AT-09 / AUDIO-01 (review of the C level loops): every measurable sample
// format yields the same documented units. The fixture's left channel is a
// 1 kHz tone of amplitude 0.25 (mean square 0.03125 = -15.05 dBFS over each
// 20 ms window, which holds exactly 20 cycles), the right channel is digital
// silence: the conservative per-channel peak must see the tone, the mean
// must be half of it.
func TestAudioLevelsPerSampleFormat(t *testing.T) {
	t.Parallel()
	for _, codec := range []string{"pcm_u8", "pcm_s16le", "pcm_s32le", "pcm_f32le", "pcm_f64le"} {
		t.Run(codec, func(t *testing.T) {
			path := fixture(t, "pcm", codec+".mkv")
			rs := readStream(t, ffadapter.Path(path), nil)
			if len(rs.audio) < 95 || len(rs.audio) > 100 {
				t.Fatalf("%d windows for 2 s of audio, want 100", len(rs.audio))
			}
			tol := 0.002
			if codec == "pcm_u8" {
				tol = 0.01 // 8-bit quantization
			}
			for i, w := range rs.audio {
				if w.DurationUS != 20_000 && i != len(rs.audio)-1 {
					t.Fatalf("window %d lasts %d us, want 20000", i, w.DurationUS)
				}
				if math.Abs(float64(w.PeakMS)/0.03125-1) > tol || math.Abs(float64(w.MeanMS)/0.015625-1) > tol {
					t.Fatalf("window %d at %d us: peak %.6f mean %.6f, want 0.031250 / 0.015625", i, w.StartUS, w.PeakMS, w.MeanMS)
				}
				// Fixed windows on the sample clock; Matroska's millisecond
				// timestamps allow up to 1 ms of jitter, never drift.
				near(t, "start of window "+strconv.Itoa(i), w.StartUS, rs.audio[0].StartUS+int64(i)*20_000, 1000)
			}
			// The tone in one channel keeps every window from being silent
			// (AUDIO-01's conservative rule).
			res := analyze(t, ffadapter.Path(path))
			if n := len(res.Diagnostics.Events.Silence); n != 0 {
				t.Errorf("%d silence events although the left channel carries a tone", n)
			}
		})
	}
}

// AT-09 / AUDIO-01: compressed audio is measured in the same units. The
// adapter's windows are compared with levels computed independently from
// PCM decoded by the ffmpeg tool.
func TestAudioLevelsMatchDecodedPCM(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"mp4_aac.mp4", "mkv_ac3.mkv", "ts_aac.ts"} {
		t.Run(file, func(t *testing.T) {
			path := fixture(t, "matrix", file)
			rs := readStream(t, ffadapter.Path(path), nil)
			channels := rs.info.Tracks.Audio.Channels
			pcm, err := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-i", path, "-map", "0:a:0", "-f", "f32le", "-").Output()
			if err != nil {
				t.Fatalf("ffmpeg pcm decode: %v", err)
			}
			n := len(pcm) / 4 / channels
			sums := make([]float64, channels)
			for i := 0; i < n*channels; i++ {
				v := float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[i*4:])))
				sums[i%channels] += v * v
			}
			var peak, mean float64
			for _, s := range sums {
				ms := s / float64(n)
				peak = math.Max(peak, ms)
				mean += ms / float64(channels)
			}
			var gotPeak, gotMean, dur float64
			for _, w := range rs.audio {
				d := float64(w.DurationUS)
				gotPeak += float64(w.PeakMS) * d
				gotMean += float64(w.MeanMS) * d
				dur += d
			}
			gotPeak, gotMean = gotPeak/dur, gotMean/dur
			if math.Abs(gotPeak/peak-1) > 0.03 || math.Abs(gotMean/mean-1) > 0.03 {
				t.Errorf("mean-square levels peak %.6f mean %.6f, ffmpeg-decoded PCM gives %.6f / %.6f", gotPeak, gotMean, peak, mean)
			}
			// Coverage: the windows account for the decoded samples (a final
			// partial window shorter than half a window is dropped).
			if pcmUS := float64(n) / 48000 * 1e6; math.Abs(dur-pcmUS) > 45_000 {
				t.Errorf("windows cover %.0f us, decoded PCM lasts %.0f us", dur, pcmUS)
			}
			// Window timing: the first window starts at the first decoded
			// audio frame (encoder priming is not presented) and the windows
			// stay on the sample clock. Matroska stores millisecond
			// timestamps, so its windows may sit up to 1 ms off the grid.
			first := decimalUS(t, strings.TrimRight(strings.TrimSpace(strings.Split(ffprobe(t, "-select_streams", "a:0",
				"-show_entries", "frame=pts_time", "-of", "csv=p=0", path), "\n")[0]), ","))
			slack := int64(2)
			if strings.HasSuffix(file, ".mkv") {
				slack = 1000
			}
			near(t, "first audio window start", rs.audio[0].StartUS, first, slack)
			for i, w := range rs.audio {
				near(t, "start of window "+strconv.Itoa(i), w.StartUS, rs.audio[0].StartUS+int64(i)*20_000, slack)
			}
		})
	}
}

// AT-09 / AUDIO-01: audio.channel_policy=exclude_lfe leaves the LFE channel
// out of the levels. The fixture's only signal is in the LFE channel.
func TestAudioExcludeLFE(t *testing.T) {
	t.Parallel()
	src := ffadapter.Path(fixture(t, "lfe", "lfe.mkv"))
	all := analyze(t, src)
	if a := all.Tracks.Audio; a == nil || a.Channels != 6 {
		t.Fatalf("audio track = %+v, want 5.1", a)
	}
	if n := len(all.Diagnostics.Events.Silence); n != 0 {
		t.Errorf("channel_policy=all: %d silence events although the LFE channel carries a tone", n)
	}
	excl := analyze(t, src, set("audio.channel_policy", "exclude_lfe"))
	sil := excl.Diagnostics.Events.Silence
	if len(sil) != 1 || sil[0].EndUS-sil[0].StartUS < excl.DurationUS-200_000 {
		t.Errorf("channel_policy=exclude_lfe: silence events %+v, want one spanning the recording", sil)
	}
}

// AT-22 / MEDIA-03: with two real video tracks the default is the first
// one, deterministically; an explicit index selects the other. One track
// is analyzed, never a mix.
func TestExplicitVideoTrackSelection(t *testing.T) {
	t.Parallel()
	path := fixture(t, "two_video", "two_video.mkv")
	auto := analyze(t, ffadapter.Path(path))
	if v := auto.Tracks.Video; v.Index != 0 || v.CodedWidth != 320 || !strings.HasPrefix(v.Selection, "auto") {
		t.Errorf("automatic video track = %+v, want stream 0 (320x180)", v)
	}
	exp, frames := analyzeFrames(t, ffadapter.Path(path), set("input.video_track", "1"))
	if v := exp.Tracks.Video; v.Index != 1 || v.CodedWidth != 160 || v.CodedHeight != 90 || v.Selection != "explicit" {
		t.Errorf("explicit video track = %+v, want stream 1 (160x90)", v)
	}
	if exp.Timing.Frames != 45 || auto.Timing.Frames != 45 {
		t.Errorf("frames = %d / %d, want 45 each (one track)", auto.Timing.Frames, exp.Timing.Frames)
	}
	for _, f := range frames {
		if f.CodedWidth != 160 {
			t.Fatalf("frame at %d is %d wide: tracks were mixed", f.TimeUS, f.CodedWidth)
		}
	}
	if n := len(exp.Diagnostics.Events.Resolution); n != 0 {
		t.Errorf("%d resolution events", n)
	}
	// The audio track's index is not a video track.
	r, err := tryAnalyze(ffadapter.Path(path), set("input.video_track", "2"))
	wantKind(t, r, err, addetect.KindInvalidConfig)
}

// AT-07, AT-22 / CAP-01, CAP-02, MEDIA-04: a text subtitle track carries no
// presentation-style information. It is never chosen automatically and
// never assigned a guessed style; selected explicitly, caption evidence is
// unavailable with a warning (or a capability error under the strict
// policy).
func TestTextSubtitlesHaveNoCaptionStyle(t *testing.T) {
	t.Parallel()
	src := ffadapter.Path(fixture(t, "subs", "subs.mkv"))

	auto := analyze(t, src)
	if c := auto.Tracks.Captions; c.Source != "embedded_cea608" {
		t.Errorf("automatic caption source = %+v, want the video stream's CEA-608 (not the SubRip track)", c)
	}
	if d := detector(t, auto, addetect.SignalCaptions); d.Status != addetect.StatusUnavailable || d.Events != 0 {
		t.Errorf("captions detector = %+v, want unavailable", d)
	}

	exp := analyze(t, src, set("input.caption_track", "2"))
	if c := exp.Tracks.Captions; c.Source != "track" || c.Index != 2 || c.Capability != "unsupported" || !strings.Contains(c.Detail, "subrip") {
		t.Errorf("caption track = %+v, want the unsupported SubRip track", c)
	}
	d := detector(t, exp, addetect.SignalCaptions)
	if d.Status != addetect.StatusUnavailable || d.Reason != "unsupported_caption_format" || d.CoverageUS != nil || d.Boundary {
		t.Errorf("captions detector = %+v, want unavailable/unsupported_caption_format", d)
	}
	if !hasWarning(exp, "captions_unsupported") || !exp.Complete {
		t.Errorf("complete=%v warnings=%v", exp.Complete, warningCodes(exp))
	}
	if n := len(exp.Diagnostics.Events.Captions); n != 0 {
		t.Errorf("%d caption style intervals guessed from text subtitles", n)
	}
	if b := exp.Baselines.CaptionStyle; b.Reliable || b.Style != "" {
		t.Errorf("caption baseline = %+v, want none", b)
	}
	for _, b := range exp.Diagnostics.Blocks {
		if b.CaptionStyle != "unknown" || len(b.CaptionStylesUS) != 0 {
			t.Errorf("block %s caption style %q %v, want unknown", b.ID, b.CaptionStyle, b.CaptionStylesUS)
		}
	}

	r, err := tryAnalyze(src, set("input.caption_track", "2"), set("input.optional_stream_policy", "strict"))
	wantKind(t, r, err, addetect.KindUnsupported)

	none := analyze(t, src, set("input.caption_track", "none"))
	if c := none.Tracks.Captions; c.Source != "none" || c.Capability != "none" {
		t.Errorf("caption_track=none: %+v", c)
	}
}

// AT-22 / MEDIA-05: display rotation is not applied; the assumption is
// explicit in a warning and in the track metadata.
func TestDisplayRotationIsExplicit(t *testing.T) {
	t.Parallel()
	res := analyze(t, ffadapter.Path(fixture(t, "rotated", "rotated.mp4")))
	if got := res.Tracks.Video.RotationDeg; got != 90 && got != -90 && got != 270 {
		t.Errorf("rotation_degrees = %d, want a quarter turn", got)
	}
	if !hasWarning(res, "rotation_ignored") {
		t.Errorf("warnings = %v, want rotation_ignored", warningCodes(res))
	}
	if v := res.Tracks.Video; v.CodedWidth != 320 || v.CodedHeight != 180 || v.AnalysisWidth != 320 {
		t.Errorf("geometry = %+v, want the coded orientation 320x180", v)
	}
	if hasWarning(analyze(t, ffadapter.Path(fixture(t, "noaudio", "noaudio.mp4"))), "rotation_ignored") {
		t.Errorf("an unrotated file warned rotation_ignored")
	}
}

// input.program: automatic selection stays inside the requested program of
// a multi-program transport stream; the program is reported; a program the
// input does not carry is a selection error.
func TestProgramSelection(t *testing.T) {
	t.Parallel()
	src := ffadapter.Path(fixture(t, "mpts", "mpts.ts"))
	first := analyze(t, src)
	if first.Tracks.Program != 101 || first.Tracks.Video.CodedWidth != 320 {
		t.Errorf("default: program %d, video %dx%d", first.Tracks.Program, first.Tracks.Video.CodedWidth, first.Tracks.Video.CodedHeight)
	}
	second := analyze(t, src, set("input.program", "202"))
	if second.Tracks.Program != 202 || second.Tracks.Video.CodedWidth != 160 || second.Tracks.Audio == nil {
		t.Fatalf("program 202: program %d, video %+v, audio %+v", second.Tracks.Program, second.Tracks.Video, second.Tracks.Audio)
	}
	// Program 202's audio is digital silence, program 101's a tone.
	if d := detector(t, second, addetect.SignalAudio); d.Events == 0 {
		t.Errorf("program 202 audio detector %+v: want silence events", d)
	}
	if d := detector(t, first, addetect.SignalAudio); d.Events != 0 {
		t.Errorf("program 101 audio detector %+v: want no silence", d)
	}
	_, err := tryAnalyze(src, set("input.program", "7"))
	wantKind(t, nil, err, addetect.KindInvalidConfig)
	// Single-program inputs report no program.
	if r := analyze(t, ffadapter.Path(fixture(t, "matrix", "ts_aac.ts"))); r.Tracks.Program != 0 {
		t.Errorf("single-program input reports program %d", r.Tracks.Program)
	}
}

// input.hardware_decode: where a hardware decoder is available, flat HEVC
// pictures (8 bit to NV12, 10 bit to P010) measure the same canonical level
// as software decoding; elsewhere the request falls back to software with a
// warning. Hardware decoding is deterministic run to run.
func TestHardwareDecoding(t *testing.T) {
	t.Parallel()
	dir := filepath.Dir(fixture(t, "hevc_gray", "hevc_0_8.mp4"))
	if _, err := os.Stat(filepath.Join(dir, "unsupported")); err == nil {
		t.Skip("the local ffmpeg cannot encode HEVC")
	}
	for _, v := range []int{0, 64, 128, 255} {
		for _, depth := range []string{"8", "10"} {
			name := "hevc_" + strconv.Itoa(v) + "_" + depth + ".mp4"
			t.Run(name, func(t *testing.T) {
				src := ffadapter.Path(filepath.Join(dir, name))
				sw, swf := analyzeFrames(t, src)
				hw, hwf := analyzeFrames(t, src, set("input.hardware_decode", "auto"))
				if hw.Tracks.Video.HardwareDecoder == "" {
					if !hasWarning(hw, addetect.WarnHardwareDecodeUnavailable) {
						t.Fatalf("no hardware decoder and no warning: %v", warningCodes(hw))
					}
					t.Skip("no hardware decoder on this machine")
				}
				if len(hwf) != len(swf) || len(hwf) == 0 {
					t.Fatalf("hardware %d frames, software %d", len(hwf), len(swf))
				}
				for i := range hwf {
					if math.Abs(hwf[i].Brightness-float64(v)) > 1 || math.Abs(hwf[i].Brightness-swf[i].Brightness) > 0.5 {
						t.Fatalf("frame %d: hardware %.3f, software %.3f, want %d", i, hwf[i].Brightness, swf[i].Brightness, v)
					}
					if hwf[i].BrightFraction != swf[i].BrightFraction {
						t.Fatalf("frame %d: bright fraction hardware %v software %v", i, hwf[i].BrightFraction, swf[i].BrightFraction)
					}
				}
				if sw.Tracks.Video.HardwareDecoder != "" {
					t.Errorf("software run reports a hardware decoder")
				}
				again, againF := analyzeFrames(t, src, set("input.hardware_decode", "auto"))
				if segString(again) != segString(hw) || len(againF) != len(hwf) {
					t.Errorf("hardware decoding is not deterministic")
				}
				for i := range againF {
					if againF[i] != hwf[i] {
						t.Fatalf("frame %d differs between hardware runs", i)
					}
				}
			})
		}
	}

}

// Phase 6: HEVC and MPEG-2 in transport stream and MP4, and transport
// streams with large timestamps and timestamps that wrap around the 33-bit
// clock mid-file, analyze completely on one gapless timeline with the frame
// count the probe tool reports.
func TestCodecAndTimestampMatrix(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ file, codec string }{
		{"hevc.mp4", "hevc"}, {"hevc.ts", "hevc"},
		{"mpeg2.mp4", "mpeg2video"}, {"mpeg2.ts", "mpeg2video"},
		{"long_ts.ts", "mpeg2video"}, {"wrap_ts.ts", "mpeg2video"},
	} {
		t.Run(c.file, func(t *testing.T) {
			path := fixture(t, "codecs", c.file)
			res := analyze(t, ffadapter.Path(path))
			if !res.Complete || res.Tracks.Video.Codec != c.codec {
				t.Fatalf("complete=%v codec=%s warnings=%v", res.Complete, res.Tracks.Video.Codec, warningCodes(res))
			}
			if want := probeFrameCount(t, path); res.Timing.Frames != want {
				t.Errorf("frames = %d, probe says %d", res.Timing.Frames, want)
			}
			near(t, "duration_us", res.DurationUS, 6_000_000, 70_000)
			if len(res.Coverage.Gaps) != 0 || res.Timing.Resets != 0 || res.Timing.Overlaps != 0 {
				t.Errorf("gaps %+v resets %d overlaps %d", res.Coverage.Gaps, res.Timing.Resets, res.Timing.Overlaps)
			}
			if d := detector(t, res, addetect.SignalAudio); d.Status != addetect.StatusUsable {
				t.Errorf("audio detector %+v", d)
			}
		})
	}
}
