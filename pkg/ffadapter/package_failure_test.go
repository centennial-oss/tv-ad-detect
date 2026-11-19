// SPDX-License-Identifier: Apache-2.0

package ffadapter_test

// AT-27: package failure policy, unsupported presentations, declared gaps,
// resource limits and cancellation (PACK-01, PACK-08, API-03, API-04).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/ffadapter"
)

var degraded = set("package.failure_policy", "degraded")

// hlsCopy returns a private, damageable copy of the HLS master package.
func hlsCopy(t *testing.T) (dir, master string) {
	t.Helper()
	dir = copyTree(t, fixture(t, "hls_master"), filepath.Join(t.TempDir(), "pkg"))
	return dir, filepath.Join(dir, "master.m3u8")
}

// failsWithIO asserts the default policy: an I/O error, and nothing that
// could pass for a completed analysis (PACK-08, QUAL-04).
func failsWithIO(t *testing.T, src addetect.Source, resource string) {
	t.Helper()
	res, err := tryAnalyze(src)
	wantKind(t, res, err, addetect.KindIO)
	if !strings.Contains(err.Error(), resource) {
		t.Errorf("error %q does not name the resource %s", err, resource)
	}
	if res != nil && (res.Complete || res.Error == nil || res.Error.Kind != "io") {
		t.Errorf("partial result: complete=%v error=%+v, want incomplete with an io error", res.Complete, res.Error)
	}
}

// degradedResult analyzes under package.failure_policy=degraded and asserts
// what every degraded result must report: incomplete, the loss as unknown
// coverage, and the resource with its reason.
func degradedResult(t *testing.T, src addetect.Source, lost string) *addetect.Result {
	t.Helper()
	res := analyze(t, src, degraded)
	if res.Complete {
		t.Errorf("a result with lost resources is marked complete")
	}
	if res.Error != nil {
		t.Errorf("degraded analysis carries an error object: %+v", res.Error)
	}
	found := false
	for _, r := range append(append([]addetect.PackageResource{}, res.Package.Resources...), res.Package.Inits...) {
		if strings.HasSuffix(r.URI, lost) && r.Status != "ok" {
			found = true
			if r.Reason == "" {
				t.Errorf("lost resource %s has no reason", r.URI)
			}
		}
	}
	if !found {
		t.Errorf("the lost resource %s is not reported", lost)
	}
	return res
}

// unknownOver asserts that [from, to) is entirely unknown, uncovered time.
func unknownOver(t *testing.T, res *addetect.Result, from, to int64) {
	t.Helper()
	var covered int64
	for _, g := range res.Coverage.Gaps {
		covered += max(0, min(g.EndUS, to)-max(g.StartUS, from))
	}
	if covered != to-from {
		t.Errorf("coverage gaps %+v cover %d us of the lost interval [%d,%d)", res.Coverage.Gaps, covered, from, to)
	}
	for _, s := range res.Segments {
		if s.StartUS < to && s.EndUS > from && s.Label != addetect.LabelUnknown {
			t.Errorf("segment [%d,%d) %s overlaps the lost interval [%d,%d)", s.StartUS, s.EndUS, s.Label, from, to)
		}
	}
}

// AT-27 / PACK-08: a missing media segment fails by default; under the
// explicit degraded policy its interval is unknown, the timeline is not
// shortened, the result is incomplete and the resource is reported.
func TestPackageMissingMediaSegment(t *testing.T) {
	long(t)
	t.Parallel()
	dir, master := hlsCopy(t)
	// Video segment 10 carries 50 s .. 55 s (program A).
	if err := os.Remove(filepath.Join(dir, "video", "main", "seg_010.m4s")); err != nil {
		t.Fatal(err)
	}
	failsWithIO(t, ffadapter.Path(master), "seg_010.m4s")

	res := degradedResult(t, ffadapter.Path(master), "seg_010.m4s")
	near(t, "duration_us", res.DurationUS, bcDuration, 1)
	if res.Timing.Frames != bcFrames-50 {
		t.Errorf("frames = %d, want %d", res.Timing.Frames, bcFrames-50)
	}
	if len(res.Coverage.Gaps) != 1 || res.Coverage.Gaps[0].StartUS != 50_000_000 || res.Coverage.Gaps[0].EndUS != 55_000_000 ||
		res.Coverage.Gaps[0].Reason != "package_resource_missing" {
		t.Errorf("coverage gaps = %+v, want [50 s, 55 s) package_resource_missing", res.Coverage.Gaps)
	}
	unknownOver(t, res, 50_000_000, 55_000_000)
	if !hasWarning(res, "package_resource_unreadable") {
		t.Errorf("warnings = %v", warningCodes(res))
	}
	if r := res.Package.Resources[10]; r.Status != "missing" || r.StartUS != nil || r.EndUS != nil {
		t.Errorf("resource 10 = %+v, want missing without a time mapping", r)
	}
	// The loss conceals nothing else: the break is found as before.
	if len(res.AdBreaks) != 1 || res.AdBreaks[0].StartUS != bcBreakIn || res.AdBreaks[0].EndUS != bcBreakOut || len(res.AdUnits) != 4 {
		t.Errorf("breaks = %+v (units %d), want the one break at [100.3 s, 190.3 s)", res.AdBreaks, len(res.AdUnits))
	}
	// No silence, black frame or boundary is synthesized at the hole.
	for _, s := range res.Diagnostics.Events.Silence {
		if s.StartUS < 56_000_000 && s.EndUS > 49_000_000 {
			t.Errorf("silence event [%d,%d) at the lost segment", s.StartUS, s.EndUS)
		}
	}
	for _, d := range res.Diagnostics.Events.DarkUniform {
		if d.StartUS < 56_000_000 && d.EndUS > 49_000_000 {
			t.Errorf("dark event [%d,%d) at the lost segment", d.StartUS, d.EndUS)
		}
	}
}

// AT-27 / PACK-08, MEDIA-04: a lost audio resource costs audio evidence
// only; the hole is unknown audio, never silence.
func TestPackageMissingAudioSegment(t *testing.T) {
	long(t)
	t.Parallel()
	dir, master := hlsCopy(t)
	// Audio segment 20 carries about 60 s .. 63 s.
	if err := os.Remove(filepath.Join(dir, "audio", "en", "seg_020.m4s")); err != nil {
		t.Fatal(err)
	}
	failsWithIO(t, ffadapter.Path(master), "seg_020.m4s")

	res := degradedResult(t, ffadapter.Path(master), filepath.Join("audio", "en", "seg_020.m4s"))
	near(t, "duration_us", res.DurationUS, bcDuration, 1)
	if res.Timing.Frames != bcFrames || len(res.Coverage.Gaps) != 0 {
		t.Errorf("frames %d, gaps %+v: the video timeline must be untouched", res.Timing.Frames, res.Coverage.Gaps)
	}
	d := detector(t, res, addetect.SignalAudio)
	if d.CoverageUS == nil {
		t.Fatalf("audio detector = %+v", d)
	}
	near(t, "audio coverage", *d.CoverageUS, bcDuration-3_000_000, 100_000)
	if len(res.Diagnostics.Events.Silence) != 5 {
		t.Errorf("silence events = %+v, want only the five real ones", res.Diagnostics.Events.Silence)
	}
	if b := res.AdBreaks; len(b) != 1 || b[0].StartUS != bcBreakIn || b[0].EndUS != bcBreakOut {
		t.Errorf("breaks = %+v", res.AdBreaks)
	}
}

// AT-27 / PACK-08: a truncated (corrupt) media segment addressed as a
// whole file. Nothing declares its length, so the adapter must notice that
// the resource ends in the middle of its box structure instead of feeding
// the following resources to a demuxer that has lost its place.
func TestPackageTruncatedMediaSegment(t *testing.T) {
	long(t)
	t.Parallel()
	dir, master := hlsCopy(t)
	seg := filepath.Join(dir, "video", "main", "seg_010.m4s")
	truncateCopy(t, seg, fileSize(t, seg)/2)

	failsWithIO(t, ffadapter.Path(master), "seg_010.m4s")

	res := degradedResult(t, ffadapter.Path(master), "seg_010.m4s")
	near(t, "duration_us", res.DurationUS, bcDuration, 1)
	if len(res.Coverage.Gaps) != 1 {
		t.Fatalf("coverage gaps = %+v, want one inside [50 s, 55 s)", res.Coverage.Gaps)
	}
	// Samples before the cut may be presented; the rest of the segment is
	// unknown and the next segment resumes exactly at 55 s.
	g := res.Coverage.Gaps[0]
	if g.StartUS < 50_000_000 || g.StartUS > 54_000_000 || g.EndUS != 55_000_000 {
		t.Errorf("gap = [%d,%d), want it to start inside segment 10 and end at 55 s", g.StartUS, g.EndUS)
	}
	unknownOver(t, res, g.StartUS, g.EndUS)
	if len(res.AdBreaks) != 1 || res.AdBreaks[0].StartUS != bcBreakIn || res.AdBreaks[0].EndUS != bcBreakOut {
		t.Errorf("breaks = %+v, want the one break at [100.3 s, 190.3 s)", res.AdBreaks)
	}
	for i, r := range res.Package.Resources {
		if r.Track == "video" && i != 10 && r.Status != "ok" {
			t.Errorf("resource %d (%s) has status %s; only segment 10 is damaged", i, filepath.Base(r.URI), r.Status)
		}
	}
}

// AT-27 / PACK-08: a truncated single media file addressed by byte ranges:
// the declared ranges run past the end of the file. In degraded mode all
// lost time is unknown and the declared end of the presentation is kept.
func TestPackageTruncatedByteRangeFile(t *testing.T) {
	long(t)
	t.Parallel()
	dir := copyTree(t, fixture(t, "hls_byterange"), filepath.Join(t.TempDir(), "pkg"))
	media := filepath.Join(dir, "media.m4s")
	truncateCopy(t, media, fileSize(t, media)*4/10)
	playlist := filepath.Join(dir, "media.m3u8")

	failsWithIO(t, ffadapter.Path(playlist), "media.m4s")

	res := degradedResult(t, ffadapter.Path(playlist), "media.m4s")
	// Declared durations place the lost resources: the timeline keeps its
	// full length (never shortened to conceal the loss).
	near(t, "duration_us", res.DurationUS, bcDuration, bcFrameUS)
	if len(res.Coverage.Gaps) == 0 {
		t.Fatalf("no coverage gaps")
	}
	first := res.Coverage.Gaps[0].StartUS
	if first < 40_000_000 || first > 200_000_000 {
		t.Errorf("first gap starts at %d", first)
	}
	unknownOver(t, res, first, res.DurationUS)
	near(t, "coverage.video_us", res.Coverage.VideoUS, first, 1)
	// Every frame lies before the loss. The cut segment may have lost a few
	// pictures in decode order, so slightly fewer than 10 per second exist.
	if got := int64(res.Timing.Frames) * bcFrameUS; got > first || got < first-1_000_000 {
		t.Errorf("%d frames before the loss at %d us", res.Timing.Frames, first)
	}
	if len(res.AdBreaks) != 0 && res.AdBreaks[0].EndUS > first {
		t.Errorf("break %+v extends into lost coverage", res.AdBreaks[0])
	}
}

// AT-27 / PACK-08: missing or truncated initialization data.
func TestPackageInitializationLoss(t *testing.T) {
	long(t)
	t.Parallel()
	t.Run("missing", func(t *testing.T) {
		dir, master := hlsCopy(t)
		if err := os.Remove(filepath.Join(dir, "video", "main", "init.mp4")); err != nil {
			t.Fatal(err)
		}
		failsWithIO(t, ffadapter.Path(master), "init.mp4")
		// Nothing of the only video track can be decoded: degraded mode
		// has no known content to continue with and fails as well.
		res, err := tryAnalyze(ffadapter.Path(master), degraded)
		wantKind(t, res, err, addetect.KindIO)
		if res != nil {
			t.Errorf("a package without decodable video returned a result")
		}
	})
	t.Run("truncated", func(t *testing.T) {
		dir, master := hlsCopy(t)
		init := filepath.Join(dir, "video", "main", "init.mp4")
		truncateCopy(t, init, fileSize(t, init)/2)
		failsWithIO(t, ffadapter.Path(master), "init.mp4")
	})
	t.Run("missing audio init", func(t *testing.T) {
		dir, master := hlsCopy(t)
		if err := os.Remove(filepath.Join(dir, "audio", "en", "init.mp4")); err != nil {
			t.Fatal(err)
		}
		failsWithIO(t, ffadapter.Path(master), "init.mp4")
		res := degradedResult(t, ffadapter.Path(master), filepath.Join("audio", "en", "init.mp4"))
		if res.Timing.Frames != bcFrames || len(res.Coverage.Gaps) != 0 {
			t.Errorf("frames %d, gaps %+v: the video must be complete", res.Timing.Frames, res.Coverage.Gaps)
		}
		if d := detector(t, res, addetect.SignalAudio); d.Status != addetect.StatusUnavailable || d.Events != 0 {
			t.Errorf("audio detector = %+v, want unavailable (no audio could be read)", d)
		}
	})
	t.Run("second epoch", func(t *testing.T) {
		// Two epochs with their own initialization data: losing the second
		// one loses exactly that epoch.
		src := fixture(t, "pieces")
		dir := t.TempDir()
		for _, p := range []string{"p0", "p1"} {
			copyTree(t, filepath.Join(src, p), filepath.Join(dir, p))
		}
		master := discontinuityMaster(t, dir)
		if err := os.Remove(filepath.Join(dir, "p1", "init-0.m4s")); err != nil {
			t.Fatal(err)
		}
		failsWithIO(t, ffadapter.Path(master), "init-0.m4s")

		res := degradedResult(t, ffadapter.Path(master), filepath.Join("p1", "init-0.m4s"))
		if res.Timing.Frames != 1300 {
			t.Errorf("frames = %d, want the first epoch's 1300", res.Timing.Frames)
		}
		// The playlist declares 31 segments of 4.9 s for the lost epoch.
		near(t, "duration_us", res.DurationUS, 130_000_000+31*4_900_000, bcFrameUS)
		unknownOver(t, res, 130_000_000, res.DurationUS)
		for _, r := range res.Package.Resources {
			if r.Track == "video" && r.Epoch == 1 && r.Status != "missing" {
				t.Errorf("resource %s of the lost epoch has status %s", filepath.Base(r.URI), r.Status)
			}
		}
		if len(res.AdBreaks) != 0 {
			t.Errorf("breaks = %+v: a break cannot end in lost coverage", res.AdBreaks)
		}
	})
}

// AT-27 / PACK-06, PACK-08 (review of the degraded timeline mapping): when
// the last segment of an HLS discontinuity epoch is lost, the next epoch is
// still placed after the missing duration. Frames that leave the decoder
// after the loss was detected must not cancel it.
func TestPackageLossBeforeDiscontinuityKeepsTime(t *testing.T) {
	long(t)
	t.Parallel()
	src := fixture(t, "pieces")
	dir := t.TempDir()
	for _, p := range []string{"p0", "p1"} {
		copyTree(t, filepath.Join(src, p), filepath.Join(dir, p))
	}
	// The last video segment of the first epoch carries 125 s .. 130 s.
	master := discontinuityMaster(t, dir)
	segs := pieceSegments(t, dir, "p0", 0)
	last := segs[len(segs)-1]
	if err := os.Remove(filepath.Join(dir, last)); err != nil {
		t.Fatal(err)
	}
	failsWithIO(t, ffadapter.Path(master), filepath.Base(last))

	res := degradedResult(t, ffadapter.Path(master), filepath.Base(last))
	// The playlist declares 4.9 s for the lost segment (it held 5 s), so
	// the second epoch may sit 0.1 s early - but not 5 s early.
	near(t, "duration_us", res.DurationUS, bcDuration, 150_000)
	if res.Timing.Frames != bcFrames-50 {
		t.Errorf("frames = %d, want %d", res.Timing.Frames, bcFrames-50)
	}
	if len(res.Coverage.Gaps) != 1 {
		t.Fatalf("coverage gaps = %+v, want one", res.Coverage.Gaps)
	}
	g := res.Coverage.Gaps[0]
	if g.StartUS != 125_000_000 || absDiff(g.EndUS, 130_000_000) > 150_000 {
		t.Errorf("gap = [%d,%d), want about [125 s, 130 s)", g.StartUS, g.EndUS)
	}
	unknownOver(t, res, g.StartUS, g.EndUS)
	// Content of the second epoch keeps its place: the break still ends
	// near 190.3 s.
	found := false
	for _, b := range res.Diagnostics.Boundaries {
		found = found || absDiff(b.TimeUS, bcBreakOut) <= 150_000+bcFrameUS
	}
	if !found {
		t.Errorf("no boundary near the end of the break at %d us: the second epoch is misplaced", bcBreakOut)
	}
}

// AT-27 / PACK-01: presentations outside the supported subset fail with a
// capability error before any media is read; they never yield a silently
// incomplete recording.
func TestUnsupportedPresentationsFailClearly(t *testing.T) {
	t.Parallel()
	dir, master := hlsCopy(t)
	video := filepath.Join(dir, "video", "main", "media.m3u8")
	orig := readFile(t, video)
	mpd := readFile(t, fixture(t, "dash", "manifest.mpd"))
	dashDir := filepath.Dir(fixture(t, "dash", "manifest.mpd"))

	unsupported := func(name string, src addetect.Source) {
		t.Run(name, func(t *testing.T) {
			res, err := tryAnalyze(src)
			wantKind(t, res, err, addetect.KindUnsupported)
			if res != nil {
				t.Errorf("an unsupported presentation returned a result")
			}
			// Degraded mode is about lost resources, not capabilities.
			res, err = tryAnalyze(src, degraded)
			wantKind(t, res, err, addetect.KindUnsupported)
		})
	}

	writeFile(t, video, strings.Replace(orig, "#EXT-X-ENDLIST\n", "", 1))
	if strings.Contains(readFile(t, video), "ENDLIST") {
		t.Fatal("fixture playlist still has EXT-X-ENDLIST")
	}
	unsupported("growing HLS playlist (no EXT-X-ENDLIST)", ffadapter.Path(master))

	writeFile(t, video, orig)

	enc := filepath.Join(dir, "video", "main", "encrypted.m3u8")
	writeFile(t, enc, strings.Replace(orig, "#EXT-X-MAP:", "#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\",IV=0x00000000000000000000000000000001\n#EXT-X-MAP:", 1))
	unsupported("encrypted HLS (EXT-X-KEY)", ffadapter.Path(enc))

	sampleAES := filepath.Join(dir, "video", "main", "cbcs.m3u8")
	writeFile(t, sampleAES, strings.Replace(orig, "#EXT-X-MAP:", "#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://key\",KEYFORMAT=\"com.apple.streamingkeydelivery\"\n#EXT-X-MAP:", 1))
	unsupported("DRM HLS (SAMPLE-AES)", ffadapter.Path(sampleAES))

	dyn := filepath.Join(t.TempDir(), "dynamic.mpd")
	writeFile(t, dyn, strings.Replace(strings.Replace(mpd, `type="static"`, `type="dynamic" availabilityStartTime="2026-01-01T00:00:00Z" minimumUpdatePeriod="PT2S"`, 1),
		`<Period id="0" start="PT0.0S">`, `<Period id="0" start="PT0.0S"><BaseURL>`+dashDir+`/</BaseURL>`, 1))
	unsupported("dynamic DASH MPD", ffadapter.Path(dyn))

	prot := filepath.Join(t.TempDir(), "protected.mpd")
	writeFile(t, prot, strings.Replace(strings.Replace(mpd, `<Representation id="0"`,
		`<ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>`+"\n\t\t\t"+`<Representation id="0"`, 1),
		`<Period id="0" start="PT0.0S">`, `<Period id="0" start="PT0.0S"><BaseURL>`+dashDir+`/</BaseURL>`, 1))
	unsupported("DRM DASH (ContentProtection)", ffadapter.Path(prot))

	base := filepath.Join(t.TempDir(), "segmentbase.mpd")
	writeFile(t, base, `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S" minBufferTime="PT2S">
  <Period>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <Representation id="v" codecs="avc1.4d400c" bandwidth="100000" width="320" height="180">
        <BaseURL>video.mp4</BaseURL>
        <SegmentBase indexRange="800-1200"><Initialization range="0-799"/></SegmentBase>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`)
	unsupported("DASH SegmentBase/index addressing", ffadapter.Path(base))
}

// AT-27 / PACK-02: EXT-X-GAP declares missing coverage. The gap segment is
// never read, its interval is unknown time (not content, not a loss), and
// the timeline keeps its length.
func TestHLSGapSegmentsAreUnknownTime(t *testing.T) {
	long(t)
	t.Parallel()
	dir, master := hlsCopy(t)
	video := filepath.Join(dir, "video", "main", "media.m3u8")
	// Segments 4 (20 s .. 25 s) and 56 (the last, 280 s .. 280.6 s).
	pl := readFile(t, video)
	for _, seg := range []string{"seg_004.m4s", "seg_056.m4s"} {
		i := strings.Index(pl, seg)
		j := strings.LastIndex(pl[:i], "#EXTINF")
		if i < 0 || j < 0 {
			t.Fatalf("playlist has no %s", seg)
		}
		pl = pl[:j] + "#EXT-X-GAP\n" + pl[j:]
		// A gap segment's URI must not be fetched: remove the files.
		if err := os.Remove(filepath.Join(dir, "video", "main", seg)); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, video, pl)

	// No failure policy is involved: the presentation itself declares it.
	res := analyze(t, ffadapter.Path(master))
	if !res.Complete {
		t.Errorf("a declared gap made the result incomplete")
	}
	near(t, "duration_us", res.DurationUS, bcDuration, 1)
	if len(res.Coverage.Gaps) != 2 {
		t.Fatalf("coverage gaps = %+v, want two", res.Coverage.Gaps)
	}
	want := [][2]int64{{20_000_000, 25_000_000}, {280_000_000, bcDuration}}
	for i, g := range res.Coverage.Gaps {
		if g.StartUS != want[i][0] || g.EndUS != want[i][1] || g.Reason != "package_resource_gap" {
			t.Errorf("gap %d = %+v, want [%d,%d) package_resource_gap", i, g, want[i][0], want[i][1])
		}
		unknownOver(t, res, g.StartUS, g.EndUS)
	}
	for _, i := range []int{4, 56} {
		if r := res.Package.Resources[i]; r.Status != "gap" || r.StartUS != nil {
			t.Errorf("resource %d = %+v, want status gap without presented samples", i, r)
		}
	}
	if res.Timing.Frames != bcFrames-56 {
		t.Errorf("frames = %d, want %d", res.Timing.Frames, bcFrames-56)
	}
	if len(res.AdBreaks) != 1 || res.AdBreaks[0].StartUS != bcBreakIn || res.AdBreaks[0].EndUS != bcBreakOut {
		t.Errorf("breaks = %+v, want the one break at [100.3 s, 190.3 s)", res.AdBreaks)
	}
	// The gap is not a cut that splits program evidence into an ad.
	for _, s := range res.Segments {
		if s.Label == addetect.LabelAdvertisement && (s.StartUS < bcBreakIn || s.EndUS > bcBreakOut) {
			t.Errorf("advertisement segment [%d,%d) outside the true break", s.StartUS, s.EndUS)
		}
	}
}

// AT-27 / PACK-08, QUAL-02: resource limits apply to the whole package and
// fail with the resource-limit kind.
func TestPackageResourceLimits(t *testing.T) {
	t.Parallel()
	src := ffadapter.Path(fixture(t, "hls_master", "master.m3u8"))
	res, err := tryAnalyze(src, set("package.max_segments", "20"))
	wantKind(t, res, err, addetect.KindResourceLimit)
	res, err = tryAnalyze(src, set("package.max_manifest_bytes", "1KiB"))
	wantKind(t, res, err, addetect.KindResourceLimit)
	if res != nil {
		t.Errorf("a resource-limit failure returned a result")
	}
}

// AT-27, AT-20 / PACK-08, API-03: cancellation stops package reads
// promptly, is reported as cancellation, and every reader obtained from
// the caller's resolver is closed.
func TestPackageCancellationClosesReaders(t *testing.T) {
	long(t)
	t.Parallel()
	for _, name := range []string{"hls_master", "hls_byterange", "dash"} {
		t.Run(name, func(t *testing.T) {
			manifest := map[string]string{"hls_master": "master.m3u8", "hls_byterange": "media.m3u8", "dash": "manifest.mpd"}[name]
			m := loadPackage(t, fixture(t, name), "/pkg/")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var canceledAt time.Time
			m.onOpen = func(n int64, uri string) {
				if n == 40 {
					canceledAt = time.Now()
					cancel()
				}
			}
			res, err := addetect.Analyze(ctx, ffadapter.Package("/pkg/"+manifest, m), testConfig(), nil)
			elapsed := time.Since(canceledAt)
			wantKind(t, res, err, addetect.KindCanceled)
			if !addetect.IsCanceled(err) {
				t.Errorf("IsCanceled(%v) = false", err)
			}
			if canceledAt.IsZero() {
				t.Fatalf("the analysis ended before the 40th resource was opened")
			}
			if elapsed > 2*time.Second {
				t.Errorf("analysis returned %v after cancellation", elapsed)
			}
			o, c := m.opens.Load(), m.closes.Load()
			if o != c {
				t.Errorf("resolver: %d opens but %d closes after cancellation", o, c)
			}
			if o > 45 {
				t.Errorf("%d resources opened; reads continued after cancellation at the 40th", o)
			}
			if n := m.badArgs.Load(); n != 0 {
				t.Errorf("%d reads after close or double closes", n)
			}
			if res != nil && (res.Complete || res.Error == nil || res.Error.Kind != "canceled") {
				t.Errorf("partial result: complete=%v error=%+v", res.Complete, res.Error)
			}
		})
	}
	t.Run("already canceled", func(t *testing.T) {
		m := loadPackage(t, fixture(t, "hls_byterange"), "/pkg/")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res, err := addetect.Analyze(ctx, ffadapter.Package("/pkg/media.m3u8", m), testConfig(), nil)
		wantKind(t, res, err, addetect.KindCanceled)
		if res != nil || m.opens.Load() != 0 {
			t.Errorf("result %v, %d opens: nothing may be read under a canceled context", res != nil, m.opens.Load())
		}
	})
	t.Run("deadline", func(t *testing.T) {
		m := loadPackage(t, fixture(t, "hls_byterange"), "/pkg/")
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		res, err := addetect.Analyze(ctx, ffadapter.Package("/pkg/media.m3u8", m), testConfig(set("resources.workers", "1")), nil)
		if err == nil {
			t.Skip("the analysis finished within the deadline on this machine")
		}
		wantKind(t, res, err, addetect.KindCanceled)
		if o, c := m.opens.Load(), m.closes.Load(); o != c {
			t.Errorf("resolver: %d opens but %d closes after the deadline", o, c)
		}
	})
}

// AT-27 / API-03 (review): a package rejected after its first resources
// were opened - here by the strict optional-stream policy - closes the
// reader it holds.
func TestPackageRejectedAfterOpenClosesReaders(t *testing.T) {
	t.Parallel()
	m := loadPackage(t, fixture(t, "hls_byterange"), "/pkg/")
	res, err := tryAnalyze(ffadapter.Package("/pkg/media.m3u8", m),
		set("input.audio_track", "7"), set("input.optional_stream_policy", "strict"))
	wantKind(t, res, err, addetect.KindInvalidConfig)
	if o, c := m.opens.Load(), m.closes.Load(); o != c || o == 0 {
		t.Errorf("resolver: %d opens but %d closes after the package was rejected", o, c)
	}
}

// AT-26, AT-27 / PACK-06, TIME-03, API-04: an MPD whose timing does not
// match its media (no presentationTimeOffset for media that starts at
// 1000 s) places every sample outside its Period. No reliable timeline
// exists: that is a timing error, not a decoder failure.
func TestDASHUnmappableTimelineIsTimingError(t *testing.T) {
	t.Parallel()
	// ffmpeg's dash muxer wrote exactly such an MPD for piece 1.
	res, err := tryAnalyze(ffadapter.Path(fixture(t, "pieces", "p1", "piece.mpd")))
	wantKind(t, res, err, addetect.KindTiming)
	if res != nil {
		t.Errorf("an unmappable presentation returned a result")
	}
}
