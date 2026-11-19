// SPDX-License-Identifier: Apache-2.0

package ffadapter_test

// AT-23..AT-26, AT-28: HLS/DASH CMAF packages analyzed as one logical
// recording (PACK-01..07, PACK-09, API-01). Every package here carries the
// synthetic broadcast, so its detections are compared with the single-file
// analysis of the same pictures.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/ffadapter"
)

// noResourceBoundaries asserts PACK-07: resource boundaries, discontinuity
// tags and Period transitions create no cuts. Every boundary other than
// the recording edges must be one of the five true black/silence
// boundaries of the broadcast.
func noResourceBoundaries(t testing.TB, res *addetect.Result) {
	t.Helper()
	if res.Diagnostics == nil {
		t.Fatal("no diagnostics")
	}
	for _, b := range res.Diagnostics.Boundaries {
		if b.TimeUS == 0 || b.TimeUS == res.DurationUS {
			continue
		}
		ok := false
		for _, u := range bcUnits {
			ok = ok || absDiff(b.TimeUS, u) <= bcFrameUS+bcWindowUS
		}
		if !ok {
			t.Errorf("boundary %s at %d us is not a content boundary: %+v", b.ID, b.TimeUS, b.Causes)
		}
	}
	if len(res.Coverage.Gaps) != 0 || res.Timing.Resets != 0 || res.Timing.Overlaps != 0 {
		t.Errorf("gaps %+v, %d resets, %d overlaps: the package is not one continuous timeline", res.Coverage.Gaps, res.Timing.Resets, res.Timing.Overlaps)
	}
	for _, b := range res.AdBreaks {
		if b.TruncatedStart || b.TruncatedEnd {
			t.Errorf("break %+v is flagged truncated", b)
		}
	}
}

// checkPackageResources asserts the PACK-09 resource mapping: every
// resource was read, every video resource carries the result-time range of
// the samples presented from it, and those ranges tile the timeline in
// presentation order without overlap.
func checkPackageResources(t testing.TB, res *addetect.Result) {
	t.Helper()
	pk := res.Package
	if pk == nil {
		t.Fatal("result has no package information")
	}
	if !strings.HasPrefix(pk.Identity, "sha256:") || len(pk.Identity) != 7+64 {
		t.Errorf("package identity = %q", pk.Identity)
	}
	for i, r := range pk.Inits {
		if r.Index != i || r.Status != "ok" || r.Init != -1 {
			t.Errorf("initialization resource %d = %+v", i, r)
		}
	}
	var at int64
	video := 0
	for i, r := range pk.Resources {
		if r.Index != i || r.Status != "ok" {
			t.Errorf("resource %d = %+v, want status ok", i, r)
		}
		if r.Init < 0 || r.Init >= len(pk.Inits) || pk.Inits[r.Init].Track != r.Track {
			t.Errorf("resource %d names initialization resource %d", i, r.Init)
		}
		if r.Epoch < 0 || r.Epoch >= len(pk.Epochs) {
			t.Errorf("resource %d names epoch %d", i, r.Epoch)
		}
		if r.Track != "video" {
			continue
		}
		video++
		if r.StartUS == nil || r.EndUS == nil {
			t.Errorf("video resource %d (%s) has no result-time mapping", i, filepath.Base(r.URI))
			continue
		}
		if *r.StartUS != at || *r.EndUS <= *r.StartUS {
			t.Errorf("video resource %d maps to [%d,%d), want it to start at %d", i, *r.StartUS, *r.EndUS, at)
		}
		at = *r.EndUS
	}
	if at != res.DurationUS {
		t.Errorf("video resources end at %d, duration is %d", at, res.DurationUS)
	}
	if video == 0 {
		t.Errorf("no video resources")
	}
	for i, e := range pk.Epochs {
		if e.MediaOffsetUS == nil {
			t.Errorf("epoch %d has no media offset", i)
		}
	}
}

// AT-23, AT-25, AT-28 / PACK-01, PACK-02, PACK-04..07, PACK-09: an HLS
// master playlist with a separately segmented audio rendition (video 5 s,
// audio 3 s CMAF segments, media clock starting at 10 s).
func TestHLSMasterWithSeparateAudio(t *testing.T) {
	long(t)
	t.Parallel()
	master := fixture(t, "hls_master", "master.m3u8")
	res := analyze(t, ffadapter.Path(master))
	ref := broadcastRef(t)

	checkBroadcast(t, res)
	sameDetections(t, "HLS master", res, ref, bcFrameUS)
	noResourceBoundaries(t, res)
	checkPackageResources(t, res)

	if res.Input.Kind != "hls" || res.Input.Path != master || res.Input.Container != "cmaf" {
		t.Errorf("input = %+v", res.Input)
	}
	pk := res.Package
	if pk.Format != "hls" {
		t.Errorf("package format = %q", pk.Format)
	}
	// PACK-09: one snapshot per manifest read, with its content digest.
	dir := filepath.Dir(master)
	wantManifests := []string{master, filepath.Join(dir, "video", "main", "media.m3u8"), filepath.Join(dir, "audio", "en", "audio.m3u8")}
	if len(pk.Manifests) != len(wantManifests) {
		t.Fatalf("manifest snapshots = %+v, want %d", pk.Manifests, len(wantManifests))
	}
	for i, m := range pk.Manifests {
		data, err := os.ReadFile(wantManifests[i])
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if m.URI != wantManifests[i] || m.Size != int64(len(data)) || m.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("manifest snapshot %d = %+v, want %s (%d bytes)", i, m, wantManifests[i], len(data))
		}
	}
	// PACK-05: the full-resolution variant and its audio rendition, with
	// the rule that chose them.
	if v := pk.Video; v.ID != "v1" || v.Width != 320 || v.Height != 180 || v.Bandwidth != 320000 || v.Rule != "highest" || v.Manifest != wantManifests[1] {
		t.Errorf("selected video = %+v", v)
	}
	if a := pk.Audio; a == nil || a.ID != "aud/English" || a.Language != "en" || a.Manifest != wantManifests[2] {
		t.Errorf("selected audio = %+v", a)
	}
	if res.Tracks.Video.CodedWidth != 320 || res.Tracks.Audio == nil || res.Tracks.Audio.Codec != "aac" {
		t.Errorf("tracks = %+v / %+v", res.Tracks.Video, res.Tracks.Audio)
	}
	if d := detector(t, res, addetect.SignalAudio); d.Status != addetect.StatusUsable {
		t.Errorf("audio detector = %+v, want usable (separate rendition)", d)
	}
	// The separate rendition supplies the audio: nothing is missing.
	if hasWarning(res, "no_audio") || hasWarning(res, "audio_unavailable") {
		t.Errorf("warnings = %v: the audio rendition is in use", warningCodes(res))
	}
	// One epoch; media time 10 s is result time 0 (PACK-06).
	if len(pk.Epochs) != 1 || pk.Epochs[0].MediaOffsetUS == nil || *pk.Epochs[0].MediaOffsetUS != -10_000_000 || !pk.Epochs[0].AudioAvailable {
		t.Errorf("epochs = %+v, want one with media offset -10 s", pk.Epochs)
	}
	near(t, "normalization offset", res.Timing.NormalizationOffsetUS, 10_000_000, 1)
	// 57 video resources of 5 s, 94 audio resources of 3 s; two inits.
	nv, na := 0, 0
	for _, r := range pk.Resources {
		switch r.Track {
		case "video":
			if want := int64(nv) * 5_000_000; *r.StartUS != want || r.DeclaredStartUS != want || r.Offset != 0 || r.Length != -1 {
				t.Errorf("video resource %d = %+v, want result time %d", nv, r, want)
			}
			if want := fmt.Sprintf("seg_%03d.m4s", nv); filepath.Base(r.URI) != want {
				t.Errorf("video resource %d is %s, want %s", nv, r.URI, want)
			}
			nv++
		case "audio":
			na++
		}
	}
	if nv != 57 || na != 94 || len(pk.Inits) != 2 {
		t.Errorf("%d video, %d audio, %d initialization resources, want 57, 94, 2", nv, na, len(pk.Inits))
	}
	// Audio/video stay aligned by presentation time although their
	// segments are cut differently (PACK-05): silence still lines up.
	if len(res.Diagnostics.Events.Silence) != 5 {
		t.Errorf("silence events = %+v, want 5", res.Diagnostics.Events.Silence)
	}
	for i, s := range res.Diagnostics.Events.Silence {
		near(t, "silence start", s.StartUS, bcUnits[i]-300_000, 2*bcWindowUS+5_000)
	}
	// Repeatable identity (PACK-09, QUAL-01).
	again := analyze(t, ffadapter.Path(master))
	if again.Package.Identity != pk.Identity || again.FeatureID != res.FeatureID {
		t.Errorf("package identity or feature identity changed between identical runs")
	}
	if !bytes.Equal(normalized(t, again), normalized(t, res)) {
		t.Errorf("two analyses of the same package differ: %s", firstDiff(normalized(t, again), normalized(t, res)))
	}
}

// AT-23, AT-28 / PACK-05: explicit variant and rendition selection is
// honored and reported; one rendition is analyzed, never several in a row.
func TestHLSExplicitSelection(t *testing.T) {
	long(t)
	t.Parallel()
	src := ffadapter.Path(fixture(t, "hls_master", "master.m3u8"))
	for _, sel := range []string{"index=0", "id=v0", "bandwidth=120000", "lowest"} {
		t.Run(sel, func(t *testing.T) {
			res := analyze(t, src, set("package.video_selection", sel), set("package.min_height", "0"))
			if v := res.Package.Video; v.ID != "v0" || v.Width != 160 || v.Height != 90 || v.Rule != sel {
				t.Errorf("selected video = %+v, want variant v0 (160x90) by rule %s", v, sel)
			}
			if v := res.Tracks.Video; v.CodedWidth != 160 || v.CodedHeight != 90 {
				t.Errorf("decoded video is %dx%d, want 160x90", v.CodedWidth, v.CodedHeight)
			}
			if res.Timing.Frames != bcFrames {
				t.Errorf("frames = %d, want %d (exactly one rendition)", res.Timing.Frames, bcFrames)
			}
			near(t, "duration_us", res.DurationUS, bcDuration, 1)
			for _, r := range res.Package.Resources {
				if r.Track == "video" && !strings.Contains(r.URI, filepath.Join("video", "low")) {
					t.Fatalf("resource %s is not from the selected variant", r.URI)
				}
			}
		})
	}
	t.Run("audio none", func(t *testing.T) {
		res := analyze(t, src, set("package.audio_selection", "none"))
		if res.Package.Audio != nil || detector(t, res, addetect.SignalAudio).Status != addetect.StatusUnavailable {
			t.Errorf("audio_selection=none still used audio: %+v", res.Package.Audio)
		}
		for _, r := range res.Package.Resources {
			if r.Track == "audio" {
				t.Fatalf("audio resource %s listed although no audio was selected", r.URI)
			}
		}
	})
	t.Run("audio by language", func(t *testing.T) {
		res := analyze(t, src, set("package.audio_selection", "lang=en"))
		if a := res.Package.Audio; a == nil || a.ID != "aud/English" || a.Rule != "lang=en" {
			t.Errorf("selected audio = %+v", a)
		}
	})
	for _, bad := range [][2]string{{"package.video_selection", "index=7"}, {"package.video_selection", "id=nope"}, {"package.audio_selection", "lang=fr"}} {
		t.Run(bad[0]+"="+bad[1], func(t *testing.T) {
			res, err := tryAnalyze(src, set(bad[0], bad[1]))
			wantKind(t, res, err, addetect.KindInvalidConfig)
		})
	}
	t.Run("manifest type mismatch", func(t *testing.T) {
		res, err := tryAnalyze(src, set("package.manifest_type", "dash"))
		if err == nil || res != nil {
			t.Fatalf("an HLS playlist was accepted as package.manifest_type=dash")
		}
	})
}

// AT-23 / PACK-01: resources are read in manifest order. Renaming every
// segment so that directory/lexical order differs from presentation order
// (and fixing up the playlists) changes nothing.
func TestHLSFileNameOrderDoesNotMatter(t *testing.T) {
	long(t)
	t.Parallel()
	orig := analyze(t, ffadapter.Path(fixture(t, "hls_master", "master.m3u8")))
	dir := copyTree(t, fixture(t, "hls_master"), filepath.Join(t.TempDir(), "renamed"))

	shuffle := func(sub, playlist string, mul int) {
		t.Helper()
		pl := filepath.Join(dir, sub, playlist)
		lines := strings.Split(readFile(t, pl), "\n")
		var segs []int
		for i, l := range lines {
			if strings.HasSuffix(l, ".m4s") {
				segs = append(segs, i)
			}
		}
		n := len(segs)
		var names []string
		for k, i := range segs {
			// A permutation whose lexical order is unrelated to k.
			name := fmt.Sprintf("part-%03d.m4s", (k*mul+11)%n)
			if err := os.Rename(filepath.Join(dir, sub, lines[i]), filepath.Join(dir, sub, name)); err != nil {
				t.Fatal(err)
			}
			lines[i] = name
			names = append(names, name)
		}
		if sort.StringsAreSorted(names) {
			t.Fatalf("renaming kept the lexical order")
		}
		writeFile(t, pl, strings.Join(lines, "\n"))
	}
	shuffle(filepath.Join("video", "main"), "media.m3u8", 37) // 57 segments
	shuffle(filepath.Join("audio", "en"), "audio.m3u8", 41)   // 94 segments

	res := analyze(t, ffadapter.Path(filepath.Join(dir, "master.m3u8")))
	checkBroadcast(t, res)
	sameDetections(t, "renamed segments", res, orig, 0)
	checkPackageResources(t, res)
	// The identity covers resource identities, so it changed (PACK-09).
	if res.Package.Identity == orig.Package.Identity {
		t.Errorf("package identity did not change although every resource URI changed")
	}
}

// AT-23, AT-25, AT-28 / PACK-01, PACK-02, PACK-09: byte-range addressing
// into one physical media file with multiplexed audio, through a direct
// Media Playlist.
func TestHLSByteRangeSingleFile(t *testing.T) {
	long(t)
	t.Parallel()
	playlist := fixture(t, "hls_byterange", "media.m3u8")
	res := analyze(t, ffadapter.Path(playlist))
	checkBroadcast(t, res)
	sameDetections(t, "HLS byte ranges", res, broadcastRef(t), bcFrameUS)
	noResourceBoundaries(t, res)
	checkPackageResources(t, res)

	pk := res.Package
	if pk.Audio != nil || res.Tracks.Audio == nil || detector(t, res, addetect.SignalAudio).Status != addetect.StatusUsable {
		t.Errorf("multiplexed audio: package audio %+v, track %+v", pk.Audio, res.Tracks.Audio)
	}
	// The reported byte ranges are the playlist's (explicit offsets).
	type rng struct{ off, n int64 }
	var want []rng
	var initRange rng
	for _, l := range strings.Split(readFile(t, playlist), "\n") {
		var r rng
		if strings.HasPrefix(l, "#EXT-X-MAP:") {
			if _, err := fmt.Sscanf(l[strings.Index(l, `BYTERANGE="`):], `BYTERANGE="%d@%d"`, &initRange.n, &initRange.off); err != nil {
				t.Fatalf("playlist line %q: %v", l, err)
			}
		}
		if n, _ := fmt.Sscanf(l, "#EXT-X-BYTERANGE:%d@%d", &r.n, &r.off); n == 2 {
			want = append(want, r)
		}
	}
	media := filepath.Join(filepath.Dir(playlist), "media.m4s")
	if len(pk.Inits) != 1 || pk.Inits[0].URI != media || pk.Inits[0].Offset != initRange.off || pk.Inits[0].Length != initRange.n {
		t.Errorf("initialization resource = %+v, want bytes %d+%d of %s", pk.Inits, initRange.off, initRange.n, media)
	}
	if len(pk.Resources) != len(want) || len(want) != 57 {
		t.Fatalf("%d resources, playlist lists %d, want 57", len(pk.Resources), len(want))
	}
	next := initRange.off + initRange.n
	for i, r := range pk.Resources {
		if r.URI != media || r.Offset != want[i].off || r.Length != want[i].n || r.Offset != next {
			t.Errorf("resource %d = bytes %d+%d of %s, want %d+%d", i, r.Offset, r.Length, r.URI, want[i].off, want[i].n)
		}
		next = r.Offset + r.Length
	}
	if next != fileSize(t, media) {
		t.Errorf("byte ranges end at %d, the media file has %d bytes", next, fileSize(t, media))
	}

	// EXT-X-BYTERANGE without an offset continues after the previous
	// range of the same resource (implicit offsets, PACK-02).
	dir := t.TempDir()
	if err := os.Symlink(media, filepath.Join(dir, "media.m4s")); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	first := true
	for _, l := range strings.Split(readFile(t, playlist), "\n") {
		if strings.HasPrefix(l, "#EXT-X-BYTERANGE:") {
			if !first {
				l = l[:strings.IndexByte(l, '@')]
			}
			first = false
		}
		sb.WriteString(l + "\n")
	}
	implicit := analyze(t, ffadapter.Path(writeFile(t, filepath.Join(dir, "implicit.m3u8"), sb.String())))
	sameDetections(t, "implicit byte-range offsets", implicit, res, 0)
	for i, r := range implicit.Package.Resources {
		if r.Offset != want[i].off || r.Length != want[i].n {
			t.Fatalf("implicit offsets: resource %d = bytes %d+%d, want %d+%d", i, r.Offset, r.Length, want[i].off, want[i].n)
		}
	}
}

// AT-24, AT-25, AT-28 / PACK-03, PACK-05, PACK-09: a static MPD with
// SegmentTemplate/SegmentTimeline, $RepresentationID$/$Number%05d$
// templates, two video Representations and a separate audio AdaptationSet.
func TestDASHSegmentTimeline(t *testing.T) {
	long(t)
	t.Parallel()
	mpd := fixture(t, "dash", "manifest.mpd")
	res := analyze(t, ffadapter.Path(mpd))
	checkBroadcast(t, res)
	sameDetections(t, "DASH", res, broadcastRef(t), bcFrameUS)
	noResourceBoundaries(t, res)
	checkPackageResources(t, res)

	pk := res.Package
	if res.Input.Kind != "dash" || pk.Format != "dash" || len(pk.Manifests) != 1 || pk.Manifests[0].URI != mpd {
		t.Errorf("input %+v, format %q, manifests %+v", res.Input, pk.Format, pk.Manifests)
	}
	if v := pk.Video; v.ID != "0" || v.Width != 320 || v.Height != 180 || v.Rule != "highest" {
		t.Errorf("selected video = %+v, want Representation 0 (320x180)", v)
	}
	if a := pk.Audio; a == nil || a.ID != "2" {
		t.Errorf("selected audio = %+v, want Representation 2", a)
	}
	if len(pk.Epochs) != 1 || pk.Epochs[0].VideoID != "0" || pk.Epochs[0].AudioID != "2" || !pk.Epochs[0].AudioAvailable ||
		pk.Epochs[0].DeclaredEndUS == nil || *pk.Epochs[0].DeclaredEndUS != bcDuration {
		t.Errorf("epochs = %+v", pk.Epochs)
	}
	dir := filepath.Dir(mpd)
	nv := 0
	for _, r := range pk.Resources {
		if r.Track != "video" {
			continue
		}
		nv++
		if want := filepath.Join(dir, fmt.Sprintf("chunk-0-%05d.m4s", nv)); r.URI != want {
			t.Errorf("video resource %d = %s, want %s", nv, r.URI, want)
		}
	}
	if nv != 57 || len(pk.Resources) != 114 || len(pk.Inits) != 2 || pk.Inits[0].URI != filepath.Join(dir, "init-0.m4s") {
		t.Errorf("%d video of %d resources, inits %+v", nv, len(pk.Resources), pk.Inits)
	}
	if hasWarning(res, "no_audio") {
		t.Errorf("warnings = %v: the audio AdaptationSet is in use", warningCodes(res))
	}

	// Explicit Representation selection (PACK-05).
	low := analyze(t, ffadapter.Path(mpd), set("package.video_selection", "id=1"))
	if v := low.Package.Video; v.ID != "1" || v.Width != 160 || v.Rule != "id=1" {
		t.Errorf("selected video = %+v, want Representation 1", v)
	}
	if low.Tracks.Video.CodedWidth != 160 || low.Timing.Frames != bcFrames || low.Package.Epochs[0].VideoID != "1" {
		t.Errorf("Representation 1: coded width %d, %d frames, epoch %+v", low.Tracks.Video.CodedWidth, low.Timing.Frames, low.Package.Epochs[0])
	}
	if low.Package.Identity == pk.Identity || low.FeatureID == res.FeatureID {
		t.Errorf("selecting another Representation did not change the package/feature identity")
	}
	r, err := tryAnalyze(ffadapter.Path(mpd), set("package.video_selection", "id=9"))
	wantKind(t, r, err, addetect.KindInvalidConfig)
}

// ---- hand-written manifests over separately encoded pieces ----------------

// piecesDir returns a directory holding links to the "pieces" fixture, for
// tests that write their own manifests next to the media.
func piecesDir(t testing.TB) string {
	t.Helper()
	src := fixture(t, "pieces")
	dir := t.TempDir()
	for _, p := range []string{"p0", "p1", "p0m", "p1m"} {
		if err := os.Symlink(filepath.Join(src, p), filepath.Join(dir, p)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// firstVideoPTS concatenates the given resources (initialization data
// followed by media) into a scratch file and returns the earliest video
// presentation timestamp ffprobe demuxes from it.
func firstVideoPTS(t testing.TB, files ...string) int64 {
	t.Helper()
	var buf []byte
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		buf = append(buf, data...)
	}
	tmp := filepath.Join(t.TempDir(), "probe.mp4")
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return probePTS(t, tmp, "v:0")[0]
}

// pieceSegments lists a piece's media segments of one stream in order.
func pieceSegments(t testing.TB, dir, piece string, stream int) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, piece, fmt.Sprintf("seg-%d-*.m4s", stream)))
	if err != nil || len(m) == 0 {
		t.Fatalf("no segments for %s stream %d: %v", piece, stream, err)
	}
	sort.Strings(m)
	for i := range m {
		m[i] = piece + "/" + filepath.Base(m[i])
	}
	return m
}

// piecePTS is the first video presentation timestamp of a piece's CMAF track.
func piecePTS(t testing.TB, dir, piece string) int64 {
	t.Helper()
	return firstVideoPTS(t, filepath.Join(dir, piece, "init-0.m4s"), filepath.Join(dir, piece, "seg-0-001.m4s"))
}

// discontinuityPlaylist writes a Media Playlist whose two pieces are
// separated by EXT-X-DISCONTINUITY, each with its own EXT-X-MAP. Every
// EXTINF deliberately claims 4.9 s (the segments last 5 s or less): only
// sample timestamps may define time (PACK-06).
func discontinuityPlaylist(t testing.TB, dir, name string, stream int, mutate func(piece, n int, uri string) string) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:5\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	for p, piece := range []string{"p0", "p1"} {
		if p > 0 {
			sb.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		fmt.Fprintf(&sb, "#EXT-X-MAP:URI=\"%s/init-%d.m4s\"\n", piece, stream)
		for n, uri := range pieceSegments(t, dir, piece, stream) {
			entry := "#EXTINF:4.900,\n" + uri + "\n"
			if mutate != nil {
				entry = mutate(p, n, entry)
			}
			sb.WriteString(entry)
		}
	}
	sb.WriteString("#EXT-X-ENDLIST\n")
	return writeFile(t, filepath.Join(dir, name), sb.String())
}

func discontinuityMaster(t testing.TB, dir string) string {
	t.Helper()
	discontinuityPlaylist(t, dir, "video.m3u8", 0, nil)
	discontinuityPlaylist(t, dir, "audio.m3u8", 1, nil)
	return writeFile(t, filepath.Join(dir, "master.m3u8"), `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="main",DEFAULT=YES,URI="audio.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=200000,RESOLUTION=320x180,CODECS="avc1.4d400c,mp4a.40.2",AUDIO="a"
video.m3u8
`)
}

// multiPeriodMPD writes a two-Period static MPD over the pieces. Period
// "second" starts at secondStart seconds and its media timestamps start at
// 1000 s (presentationTimeOffset). BaseURL is inherited from the Period and
// SegmentTemplate from the AdaptationSet.
func multiPeriodMPD(t testing.TB, dir, name string, secondStart float64) string {
	t.Helper()
	period := func(id, base, start, duration string, pto int) string {
		tmpl := fmt.Sprintf(`<SegmentTemplate timescale="1000" duration="5000" startNumber="1" presentationTimeOffset="%d" initialization="init-$RepresentationID$.m4s" media="seg-$RepresentationID$-$Number%%03d$.m4s"/>`, pto)
		return fmt.Sprintf(`  <Period id="%s" start="%s"%s>
    <BaseURL>%s/</BaseURL>
    <AdaptationSet id="0" contentType="video" mimeType="video/mp4" segmentAlignment="true">
      %s
      <Representation id="0" codecs="avc1.4d400c" bandwidth="140000" width="320" height="180"/>
    </AdaptationSet>
    <AdaptationSet id="1" contentType="audio" mimeType="audio/mp4" lang="en">
      %s
      <Representation id="1" codecs="mp4a.40.2" bandwidth="64000" audioSamplingRate="48000"/>
    </AdaptationSet>
  </Period>
`, id, start, duration, base, tmpl, tmpl)
	}
	total := secondStart + 150.6
	doc := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" profiles="urn:mpeg:dash:profile:isoff-live:2011" type="static" mediaPresentationDuration="PT%.1fS" minBufferTime="PT2S">
`, total) +
		period("first", "p0", "PT0S", ` duration="PT130S"`, 0) +
		period("second", "p1", fmt.Sprintf("PT%.1fS", secondStart), "", 1_000_000) +
		"</MPD>\n"
	return writeFile(t, filepath.Join(dir, name), doc)
}

// checkTwoEpochs asserts the per-epoch timestamp mapping of the two-piece
// packages: the second piece's media clock (starting at 1000 s) continues
// the first at 130 s of result time, with no boundary, gap or duplicate
// coverage at the junction (PACK-06, PACK-07).
func checkTwoEpochs(t *testing.T, what string, res *addetect.Result, firstPTS0, firstPTS1 int64) {
	t.Helper()
	checkBroadcast(t, res)
	sameDetections(t, what, res, broadcastRef(t), bcFrameUS)
	noResourceBoundaries(t, res)
	checkPackageResources(t, res)
	pk := res.Package
	if len(pk.Epochs) != 2 {
		t.Fatalf("%s: epochs = %+v, want 2", what, pk.Epochs)
	}
	e0, e1 := pk.Epochs[0], pk.Epochs[1]
	if e0.MediaOffsetUS == nil || e1.MediaOffsetUS == nil {
		t.Fatalf("%s: epoch media offsets = %+v", what, pk.Epochs)
	}
	// result = media + offset. The first frame of piece 0 (media time
	// firstPTS0, as ffprobe reads it) is result time 0 and the first frame
	// of piece 1 (media time firstPTS1, near 1000 s) is result time 130 s.
	if *e0.MediaOffsetUS != -firstPTS0 || *e1.MediaOffsetUS != 130_000_000-firstPTS1 {
		t.Errorf("%s: epoch media offsets = %d, %d; want %d, %d", what, *e0.MediaOffsetUS, *e1.MediaOffsetUS, -firstPTS0, 130_000_000-firstPTS1)
	}
	if firstPTS1 < 900_000_000 {
		t.Fatalf("%s: piece 1 starts at media time %d; the fixture should start near 1000 s", what, firstPTS1)
	}
	// The ad that straddles the junction (spot 2, 115.3 - 145.3 s) is one unit.
	u := res.AdUnits[1]
	if u.StartUS > 130_000_000 || u.EndUS < 130_000_000 || len(u.BlockIDs) != 1 {
		t.Errorf("%s: the unit across the junction is %+v, want one block spanning 130 s", what, u)
	}
	// Resources of the second epoch map to result time from 130 s on.
	first := true
	for _, r := range pk.Resources {
		if r.Track != "video" || r.Epoch != 1 {
			continue
		}
		if first && *r.StartUS != 130_000_000 {
			t.Errorf("%s: the second epoch's first resource starts at %d, want 130 s", what, *r.StartUS)
		}
		first = false
	}
	if first {
		t.Errorf("%s: no video resource in the second epoch", what)
	}
	if len(pk.Inits) < 2 {
		t.Errorf("%s: %d initialization resources, want one per piece and track", what, len(pk.Inits))
	}
}

// AT-23, AT-26 / PACK-02, PACK-04, PACK-06, PACK-07: EXT-X-DISCONTINUITY
// with a changing EXT-X-MAP, separately segmented audio, composition
// offsets and a media clock that jumps from 130 s to 1000 s. A
// discontinuity is not missing time and is not a content cut.
func TestHLSDiscontinuityEpochs(t *testing.T) {
	long(t)
	t.Parallel()
	dir := piecesDir(t)
	res := analyze(t, ffadapter.Path(discontinuityMaster(t, dir)))
	checkTwoEpochs(t, "HLS discontinuity", res, piecePTS(t, dir, "p0"), piecePTS(t, dir, "p1"))
	if len(res.Package.Inits) != 4 {
		t.Errorf("initialization resources = %+v, want 4 (two tracks x two maps)", res.Package.Inits)
	}
	// EXTINF claimed 4.9 s per segment; sample timing defines the result.
	near(t, "duration_us", res.DurationUS, bcDuration, 1)
	if d := res.Package.Resources[0].DeclaredDurationUS; d != 4_900_000 {
		t.Errorf("declared duration = %d, want the playlist's 4.9 s", d)
	}
	for _, r := range res.Package.Resources[:5] {
		if *r.EndUS-*r.StartUS != 5_000_000 {
			t.Errorf("resource %s presents %d us, want its 5 s of samples", filepath.Base(r.URI), *r.EndUS-*r.StartUS)
		}
	}
}

// AT-23, AT-26 / PACK-02, PACK-04, PACK-06: the same two pieces with
// multiplexed audio in byte-range addressed files; multiplexed audio that
// is demuxed before its epoch's first video frame is still placed.
func TestHLSDiscontinuityMuxedByteRanges(t *testing.T) {
	long(t)
	t.Parallel()
	dir := piecesDir(t)
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:5\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	for p, piece := range []string{"p0m", "p1m"} {
		if p > 0 {
			sb.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		for _, l := range strings.Split(readFile(t, filepath.Join(dir, piece, "media.m3u8")), "\n") {
			switch {
			case strings.HasPrefix(l, "#EXT-X-MAP:"):
				sb.WriteString(strings.Replace(l, `URI="`, `URI="`+piece+`/`, 1) + "\n")
			case strings.HasPrefix(l, "#EXTINF:"), strings.HasPrefix(l, "#EXT-X-BYTERANGE:"):
				sb.WriteString(l + "\n")
			case strings.HasSuffix(l, ".m4s"):
				sb.WriteString(piece + "/" + l + "\n")
			}
		}
	}
	sb.WriteString("#EXT-X-ENDLIST\n")
	res := analyze(t, ffadapter.Path(writeFile(t, filepath.Join(dir, "muxed.m3u8"), sb.String())))
	checkTwoEpochs(t, "HLS discontinuity (muxed)", res,
		firstVideoPTS(t, filepath.Join(dir, "p0m", "media.m4s")), firstVideoPTS(t, filepath.Join(dir, "p1m", "media.m4s")))
	if res.Package.Audio != nil || detector(t, res, addetect.SignalAudio).Status != addetect.StatusUsable {
		t.Errorf("multiplexed audio is not in use")
	}
	// Audio of the second epoch is on the timeline: silence at 145 s and
	// later still aligns with the black frames.
	sil := res.Diagnostics.Events.Silence
	if len(sil) != 5 {
		t.Fatalf("silence events = %+v, want 5", sil)
	}
	for i, s := range sil {
		near(t, "silence start", s.StartUS, bcUnits[i]-300_000, 3*bcWindowUS)
	}
}

// AT-24, AT-26 / PACK-03, PACK-06, PACK-07: a multi-Period static MPD with
// inherited BaseURL and SegmentTemplate, $Number%03d$ addressing with fixed
// durations and a presentationTimeOffset in the second Period. Both Periods
// are analyzed once, as one timeline.
func TestDASHMultiPeriod(t *testing.T) {
	long(t)
	t.Parallel()
	dir := piecesDir(t)
	res := analyze(t, ffadapter.Path(multiPeriodMPD(t, dir, "multi.mpd", 130)))
	// DASH: result = media - presentationTimeOffset + Period start, so the
	// offsets follow from the MPD alone (0 and 130 s - 1000 s); the pieces'
	// first frames sit exactly at those media times.
	if p0, p1 := piecePTS(t, dir, "p0"), piecePTS(t, dir, "p1"); p0 != 0 || p1 != 1_000_000_000 {
		t.Fatalf("pieces start at media times %d and %d, want 0 and 1000 s", p0, p1)
	}
	checkTwoEpochs(t, "DASH multi-Period", res, 0, 1_000_000_000)
	pk := res.Package
	if pk.Epochs[0].ID != "first" || pk.Epochs[1].ID != "second" || pk.Epochs[1].DeclaredStartUS != 130_000_000 ||
		pk.Epochs[0].VideoID != "0" || pk.Epochs[1].AudioID != "1" || !pk.Epochs[1].AudioAvailable {
		t.Errorf("epochs = %+v", pk.Epochs)
	}
	// 26 + 31 video segments, resolved through the Period BaseURLs.
	nv := 0
	for _, r := range pk.Resources {
		if r.Track == "video" {
			nv++
			want := filepath.Join(dir, []string{"p0", "p1"}[r.Epoch])
			if filepath.Dir(r.URI) != want {
				t.Errorf("resource %s is not under %s", r.URI, want)
			}
		}
	}
	if nv != 57 {
		t.Errorf("%d video resources, want 57", nv)
	}
}

// AT-26 / PACK-06, TIME-03: a real hole between two Periods is unknown
// time; the timeline is not shortened and nothing is bridged across it.
func TestDASHPeriodGapStaysUnknown(t *testing.T) {
	long(t)
	t.Parallel()
	dir := piecesDir(t)
	// Period "second" starts 6 s after the first one ends.
	res := analyze(t, ffadapter.Path(multiPeriodMPD(t, dir, "hole.mpd", 136)))
	near(t, "duration_us", res.DurationUS, bcDuration+6_000_000, 1)
	if res.Timing.Frames != bcFrames {
		t.Errorf("frames = %d, want %d", res.Timing.Frames, bcFrames)
	}
	if len(res.Coverage.Gaps) != 1 || res.Coverage.Gaps[0].StartUS != 130_000_000 || res.Coverage.Gaps[0].EndUS != 136_000_000 {
		t.Fatalf("coverage gaps = %+v, want [130 s, 136 s)", res.Coverage.Gaps)
	}
	for _, s := range res.Segments {
		if s.StartUS < 136_000_000 && s.EndUS > 130_000_000 && s.Label != addetect.LabelUnknown {
			t.Errorf("segment [%d,%d) %s overlaps the hole", s.StartUS, s.EndUS, s.Label)
		}
	}
	// Content after the hole sits 6 s later; no unit spans the hole.
	for _, u := range res.AdUnits {
		if u.StartUS < 130_000_000 && u.EndUS > 136_000_000 {
			t.Errorf("unit %+v bridges missing coverage", u)
		}
	}
	found := false
	for _, b := range res.Diagnostics.Boundaries {
		found = found || absDiff(b.TimeUS, bcUnits[2]+6_000_000) <= bcFrameUS
	}
	if !found {
		t.Errorf("the black span at 145.3 s of content was not found at 151.3 s")
	}
	if got := *res.Package.Epochs[1].MediaOffsetUS; got != 136_000_000-1_000_000_000 {
		t.Errorf("second epoch media offset = %d, want %d", got, 136_000_000-1_000_000_000)
	}
}

// AT-26 / PACK-06: samples outside their Period are trimmed, so media that
// overlaps a Period bound is never presented (or counted) twice.
func TestDASHPeriodBoundsTrimSamples(t *testing.T) {
	long(t)
	t.Parallel()
	dir := piecesDir(t)

	// Start trim: one Period over piece 1 whose presentationTimeOffset
	// (1002 s) lies 2 s inside the first segment. Video and audio declare
	// it in different timescales.
	t.Run("presentationTimeOffset inside the first segment", func(t *testing.T) {
		doc := `<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT98S" minBufferTime="PT2S">
  <Period id="only" start="PT0S">
    <BaseURL>p1/</BaseURL>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <Representation id="0" codecs="avc1.4d400c" bandwidth="140000" width="320" height="180">
        <SegmentTemplate timescale="90000" duration="450000" startNumber="1" presentationTimeOffset="90180000" initialization="init-0.m4s" media="seg-0-$Number%03d$.m4s"/>
      </Representation>
    </AdaptationSet>
    <AdaptationSet contentType="audio" mimeType="audio/mp4">
      <Representation id="1" codecs="mp4a.40.2" bandwidth="64000">
        <SegmentTemplate timescale="48000" duration="240000" startNumber="1" presentationTimeOffset="48096000" initialization="init-1.m4s" media="seg-1-$Number%03d$.m4s"/>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`
		res := analyze(t, ffadapter.Path(writeFile(t, filepath.Join(dir, "trim.mpd"), doc)))
		// Media 1002 s .. 1100 s is presented as 0 .. 98 s: 980 frames.
		if res.Timing.Frames != 980 {
			t.Errorf("frames = %d, want 980 (media before the offset is not presented)", res.Timing.Frames)
		}
		near(t, "duration_us", res.DurationUS, 98_000_000, 1)
		if got := *res.Package.Epochs[0].MediaOffsetUS; got != -1_002_000_000 {
			t.Errorf("media offset = %d, want -1002 s", got)
		}
		// Piece 1 starts at 130 s of the broadcast: its black spans at
		// 145.0, 160.0 and 190.0 s appear 132 s earlier.
		var starts []int64
		for _, d := range res.Diagnostics.Events.DarkUniform {
			starts = append(starts, d.StartUS)
		}
		if fmt.Sprint(starts) != fmt.Sprint([]int64{13_000_000, 28_000_000, 58_000_000}) {
			t.Errorf("black spans at %v, want [13 s, 28 s, 58 s]", starts)
		}
		// The first resource presents only its last 3 s.
		if r := res.Package.Resources[0]; r.StartUS == nil || *r.StartUS != 0 || *r.EndUS != 3_000_000 {
			t.Errorf("first resource maps to %v..%v, want [0, 3 s)", r.StartUS, r.EndUS)
		}
		// Audio is trimmed to the same Period: no window before 0.
		if off := res.Timing.AudioStartOffsetUS; off == nil || *off < 0 || *off > bcWindowUS {
			t.Errorf("audio start offset = %v, want within one window after 0", off)
		}
	})

	// End trim: the first Period ends at 128 s although its media runs to
	// 130 s; the second Period follows directly. The 2 s past the Period
	// end are dropped instead of overlapping the next Period.
	t.Run("media past the Period end", func(t *testing.T) {
		doc := strings.Replace(readFile(t, multiPeriodMPD(t, dir, "short.mpd", 128)), `duration="PT130S"`, `duration="PT128S"`, 1)
		res, frames := analyzeFrames(t, ffadapter.Path(writeFile(t, filepath.Join(dir, "short.mpd"), doc)))
		if res.Timing.Frames != 1280+1506 {
			t.Errorf("frames = %d, want %d (1280 of the first Period + 1506)", res.Timing.Frames, 1280+1506)
		}
		near(t, "duration_us", res.DurationUS, 278_600_000, 1)
		if res.Timing.Overlaps != 0 || res.Timing.Resets != 0 || len(res.Coverage.Gaps) != 0 {
			t.Errorf("overlaps %d, resets %d, gaps %+v: Period-bound samples were presented twice or dropped", res.Timing.Overlaps, res.Timing.Resets, res.Coverage.Gaps)
		}
		for i, f := range frames {
			if f.TimeUS != int64(i)*bcFrameUS {
				t.Fatalf("frame %d at %d us, want %d", i, f.TimeUS, int64(i)*bcFrameUS)
			}
		}
		// The last resource of the first Period presents 3 of its 5 s.
		for i, r := range res.Package.Resources {
			if r.Track == "video" && r.Epoch == 0 && i+1 < len(res.Package.Resources) && res.Package.Resources[i+1].Epoch == 1 {
				if *r.StartUS != 125_000_000 || *r.EndUS != 128_000_000 {
					t.Errorf("last resource of the first Period maps to [%d,%d), want [125 s, 128 s)", *r.StartUS, *r.EndUS)
				}
			}
		}
	})
}

// ---- caller-provided resolver (API-01) ------------------------------------

// memResolver serves a package from memory and counts reader lifetimes.
type memResolver struct {
	mu      sync.Mutex
	files   map[string][]byte
	opens   atomic.Int64
	closes  atomic.Int64
	badArgs atomic.Int64
	onOpen  func(n int64, uri string)
	lastCtx context.Context
	opened  map[string]int
}

type memReader struct {
	r      *bytes.Reader
	owner  *memResolver
	closed bool
}

func (m *memReader) Read(p []byte) (int, error) {
	if m.closed {
		m.owner.badArgs.Add(1)
		return 0, fmt.Errorf("read after close")
	}
	return m.r.Read(p)
}

func (m *memReader) Close() error {
	if m.closed {
		m.owner.badArgs.Add(1)
		return nil
	}
	m.closed = true
	m.owner.closes.Add(1)
	return nil
}

func (m *memResolver) Open(ctx context.Context, uri string, offset, length int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	data, ok := m.files[uri]
	m.lastCtx = ctx
	if m.opened == nil {
		m.opened = map[string]int{}
	}
	m.opened[uri]++
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("memResolver: %s: %w", uri, os.ErrNotExist)
	}
	if offset < 0 || offset > int64(len(data)) {
		m.badArgs.Add(1)
		return nil, fmt.Errorf("memResolver: offset %d outside %s", offset, uri)
	}
	data = data[offset:]
	if length >= 0 && length < int64(len(data)) {
		data = data[:length]
	}
	n := m.opens.Add(1)
	if m.onOpen != nil {
		m.onOpen(n, uri)
	}
	return &memReader{r: bytes.NewReader(data), owner: m}, nil
}

// loadPackage reads a fixture directory into memory under a virtual prefix.
func loadPackage(t testing.TB, dir, prefix string) *memResolver {
	t.Helper()
	m := &memResolver{files: map[string][]byte{}}
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		data, err := os.ReadFile(p)
		m.files[prefix+filepath.ToSlash(rel)] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// AT-28 / API-01, PACK-08: a caller-provided in-memory resolver serves the
// manifests, initialization data and byte ranges; no file is touched, every
// reader the adapter obtains is closed exactly once and the result equals
// the file-based analysis.
func TestCallerProvidedResolver(t *testing.T) {
	long(t)
	t.Parallel()
	t.Run("byte ranges", func(t *testing.T) {
		m := loadPackage(t, fixture(t, "hls_byterange"), "/virtual/show/")
		res := analyze(t, ffadapter.Package("/virtual/show/media.m3u8", m))
		checkBroadcast(t, res)
		file := analyze(t, ffadapter.Path(fixture(t, "hls_byterange", "media.m3u8")))
		sameDetections(t, "in-memory resolver", res, file, 0)
		checkPackageResources(t, res)
		if o, c := m.opens.Load(), m.closes.Load(); o != c || o < 58 {
			t.Errorf("resolver: %d opens, %d closes; want every reader closed (at least 1 manifest + 1 init + 57 ranges)", o, c)
		}
		if n := m.badArgs.Load(); n != 0 {
			t.Errorf("%d invalid resolver calls (bad range, read after close or double close)", n)
		}
		if res.Input.Path != "/virtual/show/media.m3u8" || res.Package.Resources[0].URI != "/virtual/show/media.m4s" {
			t.Errorf("input %q, first resource %q", res.Input.Path, res.Package.Resources[0].URI)
		}
		// Presentation order: each byte range is opened once, in order.
		if n := m.opened["/virtual/show/media.m4s"]; n != 58 {
			t.Errorf("media.m4s opened %d times, want 58 (init + 57 ranges)", n)
		}
	})
	t.Run("remote URIs need allow_remote", func(t *testing.T) {
		const base = "https://cdn.example.invalid/show/"
		m := loadPackage(t, fixture(t, "dash"), base)
		res, err := tryAnalyze(ffadapter.Package(base+"manifest.mpd", m))
		wantKind(t, res, err, addetect.KindUnsupported)
		if m.opens.Load() != 0 {
			t.Errorf("%d resolver opens although remote retrieval was not enabled (SCOPE-02)", m.opens.Load())
		}
		res = analyze(t, ffadapter.Package(base+"manifest.mpd", m), set("package.allow_remote", "true"))
		checkBroadcast(t, res)
		if o, c := m.opens.Load(), m.closes.Load(); o != c || o == 0 {
			t.Errorf("resolver: %d opens, %d closes", o, c)
		}
		if got := res.Package.Resources[0].URI; got != base+"chunk-0-00001.m4s" {
			t.Errorf("first resource = %q", got)
		}
		// The built-in resolver never fetches: allow_remote without a
		// caller-provided resolver is a capability error.
		r2, err := tryAnalyze(ffadapter.Path(fixture(t, "dash", "manifest.mpd")), set("package.allow_remote", "true"))
		wantKind(t, r2, err, addetect.KindUnsupported)
	})
}

// AT-28 / PACK-09, PIPE-03: the package identity (and with it the feature
// identity) changes when a manifest or initialization data changes, even
// though the top-level manifest path and the media are the same.
func TestPackageIdentityTracksManifestsAndInits(t *testing.T) {
	long(t)
	t.Parallel()
	src := fixture(t, "hls_master")
	base := analyze(t, ffadapter.Path(filepath.Join(src, "master.m3u8")))
	id := func(mutate func(m *memResolver)) (string, string) {
		m := loadPackage(t, src, "/pkg/")
		mutate(m)
		res := analyze(t, ffadapter.Package("/pkg/master.m3u8", m))
		sameDetections(t, "identity variant", res, base, 0)
		return res.Package.Identity, res.FeatureID
	}
	same1, feat1 := id(func(*memResolver) {})
	same2, feat2 := id(func(*memResolver) {})
	if same1 != same2 || feat1 != feat2 {
		t.Errorf("identical packages have different identities")
	}
	// A comment in a nested media playlist: same resources, new snapshot.
	man, _ := id(func(m *memResolver) {
		m.files["/pkg/video/main/media.m3u8"] = append(m.files["/pkg/video/main/media.m3u8"], []byte("# edited\n")...)
	})
	if man == same1 {
		t.Errorf("editing a media playlist did not change the package identity")
	}
	// A trailing 'free' box in the initialization segment: same URIs and
	// manifests, different initialization data.
	ini, feat3 := id(func(m *memResolver) {
		k := "/pkg/video/main/init.mp4"
		m.files[k] = append(append([]byte{}, m.files[k]...), 0, 0, 0, 8, 'f', 'r', 'e', 'e')
	})
	if ini == same1 || ini == man || feat3 == feat1 {
		t.Errorf("changing initialization data did not change the package/feature identity")
	}
}

// AT-26 / PACK-05, PACK-06, QUAL-02 (review of the track interleaving):
// separately packaged audio and video are delivered interleaved by
// presentation time on one clock. Video frames arrive in presentation
// order; audio windows are contiguous across resource boundaries, never
// duplicated; and neither track runs ahead of the other by more than a few
// seconds, so buffering is bounded by segments, not by the recording.
func TestPackageTracksInterleavedByPresentationTime(t *testing.T) {
	long(t)
	t.Parallel()
	for _, tc := range []struct{ name, fixture, manifest string }{
		{"HLS 5 s video / 3 s audio", "hls_master", "master.m3u8"},
		{"DASH", "dash", "manifest.mpd"},
		{"HLS multiplexed", "hls_byterange", "media.m3u8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := readStream(t, ffadapter.Path(fixture(t, tc.fixture, tc.manifest)), nil)
			if len(rs.frames) != bcFrames {
				t.Fatalf("%d frames, want %d", len(rs.frames), bcFrames)
			}
			t0 := rs.frames[0].pts
			for i, f := range rs.frames {
				if f.pts-t0 != int64(i)*bcFrameUS || f.dur != bcFrameUS {
					t.Fatalf("frame %d at %d us lasting %d us, want %d / %d", i, f.pts-t0, f.dur, int64(i)*bcFrameUS, bcFrameUS)
				}
				if f.resource < 0 || int(f.resource) >= len(rs.info.Package.Resources) || rs.info.Package.Resources[f.resource].Track != "video" {
					t.Fatalf("frame %d names resource %d", i, f.resource)
				}
				if want := int32(i / 50); f.resource != want {
					t.Fatalf("frame %d is attributed to resource %d, want %d (5 s segments)", i, f.resource, want)
				}
			}
			// Audio: 48 kHz AAC, one priming frame before video time 0.
			if n := len(rs.audio); n < 14_000 || n > 14_040 {
				t.Fatalf("%d audio windows for 280.6 s, want about 14031", n)
			}
			first := rs.audio[0].StartUS
			if first-t0 > 0 || first-t0 < -25_000 {
				t.Errorf("audio starts %d us relative to the video, want within one AAC frame before it", first-t0)
			}
			for i, w := range rs.audio {
				if absDiff(w.StartUS, first+int64(i)*20_000) > 2 || (w.DurationUS != 20_000 && i != len(rs.audio)-1) {
					t.Fatalf("audio window %d at %d us lasting %d us, want %d (contiguous across resources)", i, w.StartUS, w.DurationUS, first+int64(i)*20_000)
				}
			}
			if rs.maxSkewUS > 8_000_000 {
				t.Errorf("audio and video were delivered up to %d us apart; want interleaving within a few seconds", rs.maxSkewUS)
			}
			if len(rs.gaps) != 0 || len(rs.decodeErrs) != 0 {
				t.Errorf("gaps %+v, decode errors %v", rs.gaps, rs.decodeErrs)
			}
		})
	}
}

// PACK-08 guard: HLS with MPEG-TS segments is outside the required CMAF
// subset, but it must not be rejected by the ISO BMFF truncation check,
// which only applies to resources that are ISO BMFF.
func TestHLSTransportStreamSegmentsNotRejected(t *testing.T) {
	long(t)
	t.Parallel()
	res := analyze(t, ffadapter.Path(fixture(t, "hls_ts", "media.m3u8")))
	checkBroadcast(t, res)
	sameDetections(t, "HLS MPEG-TS segments", res, broadcastRef(t), bcFrameUS)
	if len(res.Package.Inits) != 0 {
		t.Errorf("initialization resources = %+v, want none", res.Package.Inits)
	}
	for _, r := range res.Package.Resources {
		if r.Status != "ok" || r.Init != -1 {
			t.Fatalf("resource %+v, want ok without initialization data", r)
		}
	}
}

// R8 / PACK-09: the package identity covers each media resource's version.
// By default that is the resolver's fingerprint (size and modification time
// for local files), so a replaced segment changes the identity without
// hashing every byte; package.hash_media covers the bytes themselves, which
// also catches a replacement of the same size and time. A resolver without
// fingerprints contributes addresses only.
func TestPackageIdentityTracksMediaResources(t *testing.T) {
	long(t)
	t.Parallel()
	dir := copyTree(t, fixture(t, "hls_master"), t.TempDir())
	master := filepath.Join(dir, "master.m3u8")
	var seg string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && seg == "" && strings.HasSuffix(path, ".m4s") {
			seg = path
		}
		return nil
	})
	if seg == "" {
		t.Fatal("the fixture has no media segment")
	}
	// copyTree hard-links media into the shared fixture; this test changes
	// the segment's timestamps and bytes, so it gets a private copy first.
	private, err := os.ReadFile(seg)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(seg)
	if err := os.WriteFile(seg, private, 0o600); err != nil {
		t.Fatal(err)
	}
	ident := func(opts ...cfgOpt) (string, string) {
		res := analyze(t, ffadapter.Path(master), opts...)
		return res.Package.Identity, res.FeatureID
	}
	base, baseFeat := ident()
	if again, _ := ident(); again != base {
		t.Fatalf("an unchanged package changed identity")
	}

	// A new modification time (same bytes) is a new fingerprint.
	st, err := os.Stat(seg)
	if err != nil {
		t.Fatal(err)
	}
	later := st.ModTime().Add(time.Hour)
	if err := os.Chtimes(seg, later, later); err != nil {
		t.Fatal(err)
	}
	touched, touchedFeat := ident()
	if touched == base || touchedFeat == baseFeat {
		t.Errorf("a segment with a new modification time kept the package/feature identity")
	}

	// Same size, same time, different bytes: only content hashing sees it.
	hashed, _ := ident(set("package.hash_media", "true"))
	data, err := os.ReadFile(seg)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte near the end (inside sample data, past the box headers),
	// then restore the modification time.
	data[len(data)-2] ^= 0xff
	if err := os.WriteFile(seg, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(seg, later, later); err != nil {
		t.Fatal(err)
	}
	if fp, _ := ident(); fp != touched {
		t.Errorf("the fingerprint changed although size and time did not")
	}
	if h2, _ := ident(set("package.hash_media", "true")); h2 == hashed {
		t.Errorf("package.hash_media did not notice changed media bytes")
	}

	// A caller resolver without fingerprints: addresses only, stable.
	m := loadPackage(t, dir, "/pkg/")
	r1 := analyze(t, ffadapter.Package("/pkg/master.m3u8", m))
	r2 := analyze(t, ffadapter.Package("/pkg/master.m3u8", loadPackage(t, dir, "/pkg/")))
	if r1.Package.Identity != r2.Package.Identity {
		t.Errorf("a resolver without fingerprints gave unstable identities")
	}
}
