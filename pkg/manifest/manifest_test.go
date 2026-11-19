// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// memResolver serves manifests from memory and records every open.
type memResolver struct {
	files  map[string]string
	opens  []string
	onOpen func(uri string)
}

func (m *memResolver) Open(ctx context.Context, uri string, offset, length int64) (io.ReadCloser, error) {
	m.opens = append(m.opens, uri)
	if m.onOpen != nil {
		m.onOpen(uri)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, ok := m.files[filepath.ToSlash(uri)]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: uri, Err: fs.ErrNotExist}
	}
	if offset != 0 || length >= 0 {
		return nil, fmt.Errorf("unexpected ranged manifest read %d+%d", offset, length)
	}
	return io.NopCloser(strings.NewReader(data)), nil
}

var sentinels = []error{ErrUnsupported, ErrInvalid, ErrIO, ErrResourceLimit, ErrSelection}

// assertKind checks that err wraps exactly the wanted sentinel.
func assertKind(t *testing.T, err, want error) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want %v", want)
	}
	for _, s := range sentinels {
		if got := errors.Is(err, s); got != (s == want) {
			t.Fatalf("errors.Is(%q, %v) = %v; want exactly %v", err, s, got, want)
		}
	}
}

func mustLoad(t *testing.T, files map[string]string, uri string, opts Options) *Presentation {
	t.Helper()
	p, err := Load(context.Background(), uri, &memResolver{files: files}, opts)
	if err != nil {
		t.Fatalf("Load(%s): %v", uri, err)
	}
	return p
}

func loadErr(t *testing.T, files map[string]string, uri string, opts Options, want error) error {
	t.Helper()
	p, err := Load(context.Background(), uri, &memResolver{files: files}, opts)
	if p != nil {
		t.Fatalf("Load(%s) returned a presentation alongside error %v", uri, err)
	}
	assertKind(t, err, want)
	return err
}

// segStr renders a segment compactly: uri[offset:length] #number @start+duration init=N epoch=N.
func segStr(s Segment) string {
	out := fmt.Sprintf("%s[%d:%d] #%d @%d+%d init=%d epoch=%d",
		filepath.ToSlash(s.URI), s.Offset, s.Length, s.Number, s.StartUS, s.DurationUS, s.InitIndex, s.Epoch)
	if s.Gap {
		out += " gap"
	}
	return out
}

func segStrs(tr *Track) []string {
	out := make([]string, len(tr.Segments))
	for i, s := range tr.Segments {
		out[i] = segStr(s)
	}
	return out
}

func resStrs(rs []Resource) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = fmt.Sprintf("%s[%d:%d]", filepath.ToSlash(r.URI), r.Offset, r.Length)
	}
	return out
}

func uris(tr *Track) []string {
	out := make([]string, len(tr.Segments))
	for i, s := range tr.Segments {
		out[i] = filepath.ToSlash(s.URI)
	}
	return out
}

func equal[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s mismatch\n got: %#v\nwant: %#v", what, got, want)
	}
}

func containsText(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

const tinyHLS = "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\na.m4s\n#EXT-X-ENDLIST\n"

const tinyMPD = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT4S">
 <Period><AdaptationSet mimeType="video/mp4">
  <SegmentTemplate duration="4" media="$RepresentationID$-$Number$.m4s" initialization="$RepresentationID$-init.mp4"/>
  <Representation id="v" bandwidth="1" width="640" height="360"/>
 </AdaptationSet></Period>
</MPD>`

func TestIsManifestPath(t *testing.T) {
	for path, want := range map[string]bool{
		"a.m3u8":                             true,
		"/x/Y.M3U8":                          true,
		"list.m3u":                           true,
		"/x/stream.MPD":                      true,
		"https://h.example/a/b.mpd?token=1":  true,
		"https://h.example/a/b.m3u8#frag":    true,
		"movie.mp4":                          false,
		"m3u8":                               false,
		"/dir.mpd/file.ts":                   false,
		"":                                   false,
		"https://h.example/a/b.mp4?x=a.m3u8": false,
	} {
		if got := IsManifestPath(path); got != want {
			t.Errorf("IsManifestPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestFormatDetectedByContent(t *testing.T) {
	// Extensions are only hints: content decides.
	files := map[string]string{
		"/x/dash-named.m3u8": tinyMPD,
		"/x/hls-named.mpd":   "\xEF\xBB\xBF\n" + tinyHLS,
		"/x/noext":           tinyHLS,
	}
	equal(t, "format", mustLoad(t, files, "/x/dash-named.m3u8", Options{}).Format, FormatDASH)
	equal(t, "format", mustLoad(t, files, "/x/hls-named.mpd", Options{}).Format, FormatHLS)
	equal(t, "format", mustLoad(t, files, "/x/noext", Options{}).Format, FormatHLS)
}

func TestLoadInvalidAndMissing(t *testing.T) {
	files := map[string]string{
		"/x/garbage.m3u8": "hello world\n",
		"/x/empty.m3u8":   "",
		"/x/html.mpd":     "<html><body/></html>",
		"/x/broken.mpd":   "<MPD><Period></MPD>",
		"/x/badhdr.m3u8":  "#EXTM3UX\n",
	}
	for uri := range files {
		loadErr(t, files, uri, Options{}, ErrInvalid)
	}
	loadErr(t, files, "", Options{}, ErrInvalid)

	err := loadErr(t, files, "/x/missing.m3u8", Options{}, ErrIO)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing manifest error %q does not keep fs.ErrNotExist in its chain", err)
	}
}

func TestMalformedSelectionRules(t *testing.T) {
	files := map[string]string{"/x/a.m3u8": tinyHLS}
	for _, o := range []Options{
		{Video: "best"}, {Video: "index=x"}, {Video: "index=-1"}, {Video: "id="}, {Video: "bandwidth=fast"},
		{Audio: "loudest"}, {Audio: "lang="}, {Audio: "name="}, {Audio: "id="},
	} {
		loadErr(t, files, "/x/a.m3u8", o, ErrSelection)
	}
}

func TestRemoteRefusedUnlessAllowed(t *testing.T) {
	const top = "https://media.example/vod/show/master.m3u8"
	files := map[string]string{
		top: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=640x360\nv/media.m3u8?sig=abc\n",
		"https://media.example/vod/show/v/media.m3u8?sig=abc": "#EXTM3U\n#EXT-X-MAP:URI=\"../init.mp4\"\n#EXTINF:2,\nseg%201.m4s?t=1\n#EXTINF:2,\n/abs/seg2.m4s\n#EXTINF:2,\nhttp://other.example/seg3.m4s\n#EXT-X-ENDLIST\n",
		"/local/remote-seg.m3u8":                              "#EXTM3U\n#EXTINF:2,\nhttps://media.example/seg.m4s\n#EXT-X-ENDLIST\n",
		"/local/remote-map.m3u8":                              "#EXTM3U\n#EXT-X-MAP:URI=\"ftp://media.example/init.mp4\"\n#EXTINF:2,\nseg.m4s\n#EXT-X-ENDLIST\n",
		"/local/remote-variant.m3u8":                          "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhttps://media.example/v.m3u8\n",
	}
	r := &memResolver{files: files}
	if _, err := Load(context.Background(), top, r, Options{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("remote top-level without AllowRemote: %v", err)
	}
	if len(r.opens) != 0 {
		t.Errorf("resolver was asked to open %v without AllowRemote", r.opens)
	}
	for _, uri := range []string{"/local/remote-seg.m3u8", "/local/remote-map.m3u8", "/local/remote-variant.m3u8"} {
		loadErr(t, files, uri, Options{}, ErrUnsupported)
	}

	// With AllowRemote the URIs are resolved by RFC 3986 and passed through.
	p := mustLoad(t, files, top, Options{AllowRemote: true})
	equal(t, "manifests", []string{p.Manifests[0].URI, p.Manifests[1].URI},
		[]string{top, "https://media.example/vod/show/v/media.m3u8?sig=abc"})
	equal(t, "inits", resStrs(p.Video.Inits), []string{"https://media.example/vod/show/init.mp4[0:-1]"})
	equal(t, "segments", uris(p.Video), []string{
		"https://media.example/vod/show/v/seg%201.m4s?t=1",
		"https://media.example/abs/seg2.m4s",
		"http://other.example/seg3.m4s",
	})
	p = mustLoad(t, files, "/local/remote-seg.m3u8", Options{AllowRemote: true})
	equal(t, "segments", uris(p.Video), []string{"https://media.example/seg.m4s"})
}

func TestManifestSizeLimit(t *testing.T) {
	files := map[string]string{
		"/x/a.m3u8":      tinyHLS,
		"/x/master.m3u8": "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nbig.m3u8\n",
		"/x/big.m3u8":    tinyHLS + strings.Repeat("# padding\n", 100),
	}
	limit := int64(len(tinyHLS))
	p := mustLoad(t, files, "/x/a.m3u8", Options{MaxManifestBytes: limit})
	equal(t, "size", p.Manifests[0].Size, limit)
	loadErr(t, files, "/x/a.m3u8", Options{MaxManifestBytes: limit - 1}, ErrResourceLimit)
	// The bound applies to every manifest read, not just the first.
	loadErr(t, files, "/x/master.m3u8", Options{MaxManifestBytes: 100}, ErrResourceLimit)
	// Default limit is 16 MiB.
	files["/x/huge.m3u8"] = tinyHLS + strings.Repeat("#\n", (16<<20)/2)
	loadErr(t, files, "/x/huge.m3u8", Options{}, ErrResourceLimit)
}

func TestSnapshots(t *testing.T) {
	files := map[string]string{
		"/x/master.m3u8": "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"en\",URI=\"a.m3u8\"\n#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO=\"a\"\nv.m3u8\n",
		"/x/v.m3u8":      tinyHLS,
		"/x/a.m3u8":      strings.Replace(tinyHLS, "a.m4s", "audio.m4s", 1),
	}
	p := mustLoad(t, files, "/x/master.m3u8", Options{})
	var want []Snapshot
	for _, uri := range []string{"/x/master.m3u8", "/x/v.m3u8", "/x/a.m3u8"} {
		sum := sha256.Sum256([]byte(files[uri]))
		want = append(want, Snapshot{URI: uri, Size: int64(len(files[uri])), SHA256: hex.EncodeToString(sum[:])})
	}
	for i := range p.Manifests {
		p.Manifests[i].URI = filepath.ToSlash(p.Manifests[i].URI)
	}
	equal(t, "snapshots", p.Manifests, want)
}

func TestCancellation(t *testing.T) {
	files := map[string]string{
		"/x/master.m3u8": "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n",
		"/x/v.m3u8":      tinyHLS,
	}
	// Already cancelled: nothing is opened.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &memResolver{files: files}
	_, err := Load(ctx, "/x/master.m3u8", r, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled Load: %v", err)
	}
	if len(r.opens) != 0 {
		t.Errorf("opened %v after cancellation", r.opens)
	}
	for _, s := range sentinels {
		if errors.Is(err, s) {
			t.Errorf("cancellation error %q also wraps %v", err, s)
		}
	}

	// Cancelled between manifest reads: the second manifest is not opened.
	ctx, cancel = context.WithCancel(context.Background())
	r = &memResolver{files: files}
	r.onOpen = func(string) { defer cancel() }
	_, err = Load(ctx, "/x/master.m3u8", r, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Load cancelled between reads: %v", err)
	}
	equal(t, "opens", r.opens, []string{"/x/master.m3u8"})

	// A resolver failing because of cancellation is reported as cancellation.
	ctx, cancel = context.WithCancel(context.Background())
	r = &memResolver{files: files}
	r.onOpen = func(string) { cancel() }
	_, err = Load(ctx, "/x/master.m3u8", r, Options{})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrIO) {
		t.Fatalf("Load with resolver cancellation: %v", err)
	}
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }
func (f failingReader) Close() error             { return nil }

type failingResolver struct{ err error }

func (f failingResolver) Open(context.Context, string, int64, int64) (io.ReadCloser, error) {
	return failingReader{f.err}, nil
}

func TestReadErrorIsIO(t *testing.T) {
	cause := errors.New("disk on fire")
	_, err := Load(context.Background(), "/x/a.m3u8", failingResolver{cause}, Options{})
	assertKind(t, err, ErrIO)
	if !errors.Is(err, cause) {
		t.Errorf("error %q lost its cause", err)
	}
}

func TestDeterministic(t *testing.T) {
	for _, tc := range []struct {
		files map[string]string
		uri   string
	}{
		{hlsMasterFiles(), "/pkg/master.m3u8"},
		{map[string]string{"/d/manifest.mpd": dashMultiPeriod}, "/d/manifest.mpd"},
	} {
		a := mustLoad(t, tc.files, tc.uri, Options{})
		b := mustLoad(t, tc.files, tc.uri, Options{})
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s: two loads of identical input differ", tc.uri)
		}
	}
}

func TestInitFor(t *testing.T) {
	tr := &Track{Inits: []Resource{{URI: "i"}}}
	if r, ok := tr.InitFor(&Segment{InitIndex: 0}); !ok || r.URI != "i" {
		t.Errorf("InitFor(0) = %v, %v", r, ok)
	}
	if _, ok := tr.InitFor(&Segment{InitIndex: -1}); ok {
		t.Error("InitFor(-1) reported an init")
	}
}

func TestFileResolverAndTestdata(t *testing.T) {
	ctx := context.Background()
	abs, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}

	// nil resolver = FileResolver; relative top-level path.
	p, err := Load(ctx, filepath.Join("testdata", "hls", "master.m3u8"), nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	equal(t, "video segments", uris(p.Video), []string{
		"testdata/hls/video/720p/seg_b.m4s",
		"testdata/hls/video/720p/seg_a.m4s",
		"testdata/hls/video/720p/more/seg_c.m4s",
	})
	equal(t, "video inits", resStrs(p.Video.Inits), []string{"testdata/hls/video/init/720p.mp4[0:-1]"})
	equal(t, "audio segments", uris(p.Audio), []string{
		"testdata/hls/audio/en/main.mp4", "testdata/hls/audio/en/main.mp4",
	})
	equal(t, "manifest count", len(p.Manifests), 3)

	// file:// URI with percent-encoding; resources come back as local paths.
	fileURI := "file://" + filepath.ToSlash(abs) + "/dash/my%20show.mpd"
	if !strings.HasPrefix(filepath.ToSlash(abs), "/") {
		fileURI = "file:///" + filepath.ToSlash(abs) + "/dash/my%20show.mpd"
	}
	p, err = Load(ctx, fileURI, FileResolver{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	equal(t, "format", p.Format, FormatDASH)
	equal(t, "manifest uri", p.Manifests[0].URI, filepath.Join(abs, "dash", "my show.mpd"))
	equal(t, "segments", p.Video.Segments[0].URI, filepath.Join(abs, "dash", "video", "v1-1.m4s"))

	// Missing file keeps fs.ErrNotExist.
	_, err = Load(ctx, filepath.Join("testdata", "nope.mpd"), nil, Options{})
	assertKind(t, err, ErrIO)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error %q does not wrap fs.ErrNotExist", err)
	}

	// Ranged reads.
	path := filepath.Join(t.TempDir(), "blob.bin")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		uri         string
		off, length int64
		want        string
	}{
		{path, 0, -1, "0123456789"},
		{path, 3, -1, "3456789"},
		{path, 3, 4, "3456"},
		{path, 8, 10, "89"},
		{path, 0, 0, ""},
		{"file://" + filepath.ToSlash(path), 1, 2, "12"},
	} {
		if !strings.HasPrefix(filepath.ToSlash(path), "/") && strings.HasPrefix(tc.uri, "file://") {
			continue // drive-letter paths need file:///; covered by LocalPath tests
		}
		rc, err := FileResolver{}.Open(ctx, tc.uri, tc.off, tc.length)
		if err != nil {
			t.Fatalf("Open(%s, %d, %d): %v", tc.uri, tc.off, tc.length, err)
		}
		got, err := io.ReadAll(rc)
		if err != nil || !bytes.Equal(got, []byte(tc.want)) {
			t.Errorf("Open(%s, %d, %d) read %q, %v; want %q", tc.uri, tc.off, tc.length, got, err, tc.want)
		}
		if err := rc.Close(); err != nil {
			t.Error(err)
		}
	}
	if _, err := (FileResolver{}).Open(ctx, "https://example.test/a.m4s", 0, -1); !errors.Is(err, ErrUnsupported) {
		t.Errorf("FileResolver opened a remote URI: %v", err)
	}
	if _, err := (FileResolver{}).Open(ctx, path, -1, -1); err == nil {
		t.Error("FileResolver accepted a negative offset")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (FileResolver{}).Open(cctx, path, 0, -1); !errors.Is(err, context.Canceled) {
		t.Errorf("FileResolver ignored cancellation: %v", err)
	}
}
