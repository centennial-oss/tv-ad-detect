// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// media builds a finite Media Playlist from body lines.
func media(lines ...string) string {
	return "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:10\n" + strings.Join(lines, "\n") + "\n#EXT-X-ENDLIST\n"
}

func TestHLSDirectMediaPlaylist(t *testing.T) {
	// Presentation order is the playlist order, never filename order.
	files := map[string]string{"/rec/show/index.m3u8": "#EXTM3U\r\n" +
		"#EXT-X-VERSION:7\r\n" +
		"# a comment\r\n" +
		"#EXT-X-TARGETDURATION:6\r\n" +
		"#EXT-X-MEDIA-SEQUENCE:100\r\n" +
		"#EXT-X-PLAYLIST-TYPE:VOD\r\n" +
		"#EXT-X-MAP:URI=\"init.mp4\"\r\n" +
		"#EXTINF:6.006,first title, with comma\r\n" +
		"zzz.m4s\r\n" +
		"\r\n" +
		"#EXTINF:6.006,\r\n" +
		"  aaa.m4s  \r\n" +
		"#EXTINF:2.5\r\n" +
		"mmm.m4s\r\n" +
		"#EXT-X-ENDLIST\r\n"}
	p := mustLoad(t, files, "/rec/show/index.m3u8", Options{})

	equal(t, "format", p.Format, FormatHLS)
	equal(t, "segments", segStrs(p.Video), []string{
		"/rec/show/zzz.m4s[0:-1] #100 @0+6006000 init=0 epoch=0",
		"/rec/show/aaa.m4s[0:-1] #101 @6006000+6006000 init=0 epoch=0",
		"/rec/show/mmm.m4s[0:-1] #102 @12012000+2500000 init=0 epoch=0",
	})
	equal(t, "inits", resStrs(p.Video.Inits), []string{"/rec/show/init.mp4[0:-1]"})
	equal(t, "epochs", p.Epochs, []Epoch{{ID: "0", StartUS: 0, EndUS: 14512000}})
	equal(t, "track epochs", p.Video.Epochs, []TrackEpoch{{Available: true}})
	equal(t, "video id", p.Video.ID, "v0")
	equal(t, "manifest uri", p.Video.ManifestURI, "/rec/show/index.m3u8")
	if p.Audio != nil || !p.AudioMuxed {
		t.Errorf("direct playlist: Audio=%v AudioMuxed=%v, want nil/true", p.Audio, p.AudioMuxed)
	}
	equal(t, "rules", []string{p.Selection.VideoRule, p.Selection.AudioRule}, []string{"highest", "default"})
	equal(t, "candidates", len(p.Selection.VideoCandidates), 1)
	equal(t, "manifests", len(p.Manifests), 1)

	// Selection on a single playlist.
	for _, o := range []Options{{Video: "index=0"}, {Video: "id=v0"}, {Video: "id=/rec/show/index.m3u8"}, {Video: "lowest"}} {
		mustLoad(t, files, "/rec/show/index.m3u8", o)
	}
	for _, o := range []Options{{Video: "index=1"}, {Video: "id=v1"}, {Video: "bandwidth=5"}, {Audio: "lang=en"}, {Audio: "name=x"}} {
		loadErr(t, files, "/rec/show/index.m3u8", o, ErrSelection)
	}
	p = mustLoad(t, files, "/rec/show/index.m3u8", Options{Audio: "none"})
	if p.Audio != nil || p.AudioMuxed {
		t.Errorf("audio=none: Audio=%v AudioMuxed=%v", p.Audio, p.AudioMuxed)
	}
}

func TestHLSByteRanges(t *testing.T) {
	files := map[string]string{"/v/one.m3u8": media(
		`#EXT-X-MAP:URI="main.mp4",BYTERANGE="800@0"`,
		"#EXTINF:4,", "#EXT-X-BYTERANGE:1000@800", "main.mp4",
		"#EXTINF:4,", "#EXT-X-BYTERANGE:1500", "main.mp4", // implicit: follows the previous sub-range
		"#EXT-X-BYTERANGE:700", "#EXTINF:4,", "main.mp4", // tag order before the URI is free
		"#EXTINF:4,", "#EXT-X-BYTERANGE:300", "other.mp4", // different resource: starts at 0
		"#EXTINF:4,", "#EXT-X-BYTERANGE:200", "other.mp4",
		"#EXTINF:4,", "whole.mp4",
		"#EXTINF:4,", "#EXT-X-BYTERANGE:50", "main.mp4", // previous segment was not a sub-range of main.mp4
		"#EXTINF:4,", "#EXT-X-BYTERANGE:10@5000", "main.mp4",
		"#EXTINF:4,", "#EXT-X-BYTERANGE:5", "./main.mp4", // same resource spelled differently
	)}
	p := mustLoad(t, files, "/v/one.m3u8", Options{})
	equal(t, "inits", resStrs(p.Video.Inits), []string{"/v/main.mp4[0:800]"})
	var got []string
	for _, s := range p.Video.Segments {
		got = append(got, s.URI+"["+strconv.FormatInt(s.Offset, 10)+":"+strconv.FormatInt(s.Length, 10)+"]")
	}
	equal(t, "ranges", got, []string{
		"/v/main.mp4[800:1000]",
		"/v/main.mp4[1800:1500]",
		"/v/main.mp4[3300:700]",
		"/v/other.mp4[0:300]",
		"/v/other.mp4[300:200]",
		"/v/whole.mp4[0:-1]",
		"/v/main.mp4[0:50]",
		"/v/main.mp4[5000:10]",
		"/v/main.mp4[5010:5]",
	})

	for _, bad := range []string{"#EXT-X-BYTERANGE:abc", "#EXT-X-BYTERANGE:10@x", "#EXT-X-BYTERANGE:", "#EXT-X-BYTERANGE:-5@0",
		`#EXT-X-MAP:URI="i.mp4",BYTERANGE="x@0"`, "#EXT-X-BYTERANGE:9223372036854775807@9223372036854775807"} {
		files["/v/bad.m3u8"] = media(bad, "#EXTINF:4,", "main.mp4")
		loadErr(t, files, "/v/bad.m3u8", Options{}, ErrInvalid)
	}
}

func TestHLSChangingMapAndDiscontinuities(t *testing.T) {
	files := map[string]string{"/v/p.m3u8": media(
		"#EXT-X-MEDIA-SEQUENCE:7",
		"#EXT-X-DISCONTINUITY-SEQUENCE:3",
		`#EXT-X-MAP:URI="init-a.mp4"`,
		"#EXTINF:4.0,", "s1.m4s",
		"#EXTINF:4.0,", "s2.m4s",
		"#EXT-X-DISCONTINUITY",
		`#EXT-X-MAP:URI="ads/init-b.mp4"`,
		"#EXTINF:5.5,", "ads/s3.m4s",
		"#EXT-X-DISCONTINUITY",
		`#EXT-X-MAP:URI="init-a.mp4"`, // back to the first init: reuses index 0
		"#EXTINF:4.0,", "s4.m4s",
		`#EXT-X-MAP:URI="never-used.mp4"`,
		`#EXT-X-MAP:URI="init-c.mp4",BYTERANGE="100@20"`, // changes without a discontinuity
		"#EXTINF:1.25,", "s5.m4s",
	)}
	p := mustLoad(t, files, "/v/p.m3u8", Options{})
	equal(t, "inits", resStrs(p.Video.Inits), []string{"/v/init-a.mp4[0:-1]", "/v/ads/init-b.mp4[0:-1]", "/v/init-c.mp4[20:100]"})
	equal(t, "segments", segStrs(p.Video), []string{
		"/v/s1.m4s[0:-1] #7 @0+4000000 init=0 epoch=0",
		"/v/s2.m4s[0:-1] #8 @4000000+4000000 init=0 epoch=0",
		"/v/ads/s3.m4s[0:-1] #9 @8000000+5500000 init=1 epoch=1",
		"/v/s4.m4s[0:-1] #10 @13500000+4000000 init=0 epoch=2",
		"/v/s5.m4s[0:-1] #11 @17500000+1250000 init=2 epoch=2",
	})
	equal(t, "epochs", p.Epochs, []Epoch{
		{ID: "3", StartUS: 0, EndUS: 8000000},
		{ID: "4", StartUS: 8000000, EndUS: 13500000},
		{ID: "5", StartUS: 13500000, EndUS: 18750000},
	})
	equal(t, "track epochs", len(p.Video.Epochs), 3)
	for i, s := range p.Video.Segments {
		init, ok := p.Video.InitFor(&p.Video.Segments[i])
		if !ok || init != p.Video.Inits[s.InitIndex] {
			t.Errorf("segment %d: InitFor = %v, %v", i, init, ok)
		}
	}

	// A discontinuity tag before the first segment does not start a new
	// epoch, and repeated tags each advance the sequence number.
	files["/v/q.m3u8"] = media("#EXT-X-DISCONTINUITY", "#EXTINF:1,", "a.ts", "#EXT-X-DISCONTINUITY", "#EXT-X-DISCONTINUITY", "#EXTINF:1,", "b.ts")
	p = mustLoad(t, files, "/v/q.m3u8", Options{})
	equal(t, "epochs", p.Epochs, []Epoch{{ID: "0", StartUS: 0, EndUS: 1000000}, {ID: "2", StartUS: 1000000, EndUS: 2000000}})

	for _, bad := range [][]string{
		{"#EXTINF:1,", "a.ts", "#EXT-X-MEDIA-SEQUENCE:5", "#EXTINF:1,", "b.ts"},
		{"#EXTINF:1,", "a.ts", "#EXT-X-DISCONTINUITY-SEQUENCE:5", "#EXTINF:1,", "b.ts"},
		{"#EXT-X-MEDIA-SEQUENCE:x", "#EXTINF:1,", "a.ts"},
		{"#EXT-X-MAP:BYTERANGE=\"1@0\"", "#EXTINF:1,", "a.ts"},
	} {
		files["/v/bad.m3u8"] = media(bad...)
		loadErr(t, files, "/v/bad.m3u8", Options{}, ErrInvalid)
	}
}

func TestHLSTransportStreamAndGap(t *testing.T) {
	// MPEG-TS style playlist: no EXT-X-MAP, so InitIndex is -1 and Inits is empty.
	files := map[string]string{"/ts/index.m3u8": media(
		"#EXTINF:10,", "seg0.ts",
		"#EXT-X-GAP", "#EXTINF:10,", "seg1.ts",
		"#EXTINF:10,", "seg2.ts",
	)}
	p := mustLoad(t, files, "/ts/index.m3u8", Options{})
	equal(t, "segments", segStrs(p.Video), []string{
		"/ts/seg0.ts[0:-1] #0 @0+10000000 init=-1 epoch=0",
		"/ts/seg1.ts[0:-1] #1 @10000000+10000000 init=-1 epoch=0 gap",
		"/ts/seg2.ts[0:-1] #2 @20000000+10000000 init=-1 epoch=0",
	})
	equal(t, "inits", len(p.Video.Inits), 0)
	if _, ok := p.Video.InitFor(&p.Video.Segments[0]); ok {
		t.Error("InitFor reported an init for a segment without EXT-X-MAP")
	}
}

func TestHLSUnsupportedAndInvalid(t *testing.T) {
	const seg = "#EXTINF:4,\ns.m4s\n"
	head := "#EXTM3U\n#EXT-X-TARGETDURATION:4\n"
	end := "#EXT-X-ENDLIST\n"
	cases := []struct {
		name, body string
		want       error
	}{
		{"growing (no ENDLIST)", head + seg, ErrUnsupported},
		{"event playlist still growing", head + "#EXT-X-PLAYLIST-TYPE:EVENT\n" + seg, ErrUnsupported},
		{"low-latency parts only", head + "#EXT-X-PART-INF:PART-TARGET=1\n#EXT-X-PART:DURATION=1,URI=\"p0.m4s\"\n#EXT-X-PART:DURATION=1,URI=\"p1.m4s\"\n", ErrUnsupported},
		{"delta update", head + "#EXT-X-SKIP:SKIPPED-SEGMENTS=3\n" + seg + end, ErrUnsupported},
		{"AES-128", head + "#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n" + seg + end, ErrUnsupported},
		{"SAMPLE-AES mid-playlist", head + seg + "#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://k\",KEYFORMAT=\"com.apple.streamingkeydelivery\"\n" + seg + end, ErrUnsupported},
		{"I-frames only", head + "#EXT-X-I-FRAMES-ONLY\n" + seg + end, ErrUnsupported},
		{"variable substitution", head + "#EXT-X-DEFINE:NAME=\"x\",VALUE=\"y\"\n" + seg + end, ErrUnsupported},
		{"key without method", head + "#EXT-X-KEY:URI=\"k\"\n" + seg + end, ErrInvalid},
		{"URI without EXTINF", head + "s.m4s\n" + end, ErrInvalid},
		{"bad EXTINF", head + "#EXTINF:abc,\ns.m4s\n" + end, ErrInvalid},
		{"negative EXTINF", head + "#EXTINF:-4,\ns.m4s\n" + end, ErrInvalid},
		{"no segments", head + end, ErrInvalid},
		{"header only", "#EXTM3U\n", ErrInvalid},
		{"mixed master and media", head + "#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n" + seg + end, ErrInvalid},
		{"STREAM-INF without URI", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n", ErrInvalid},
		{"two STREAM-INF in a row", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n#EXT-X-STREAM-INF:BANDWIDTH=2\nv.m3u8\n", ErrInvalid},
		{"malformed escape", head + "#EXTINF:4,\nbad%zz.m4s\n" + end, ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loadErr(t, map[string]string{"/x/p.m3u8": tc.body}, "/x/p.m3u8", Options{}, tc.want)
		})
	}

	// METHOD=NONE is not encryption.
	p := mustLoad(t, map[string]string{"/x/p.m3u8": head + "#EXT-X-KEY:METHOD=NONE\n" + seg + end}, "/x/p.m3u8", Options{})
	equal(t, "segments", len(p.Video.Segments), 1)
}

func TestHLSPartialSegmentsIgnored(t *testing.T) {
	files := map[string]string{"/ll/p.m3u8": media(
		"#EXT-X-PART-INF:PART-TARGET=1.0",
		`#EXT-X-PART:DURATION=1.0,URI="s0.part0.m4s"`,
		`#EXT-X-PART:DURATION=1.0,URI="s0.part1.m4s"`,
		"#EXTINF:2.0,", "s0.m4s",
		`#EXT-X-PART:DURATION=1.0,URI="s1.part0.m4s"`,
		"#EXTINF:1.0,", "s1.m4s",
		`#EXT-X-PRELOAD-HINT:TYPE=PART,URI="s2.part0.m4s"`,
	)}
	p := mustLoad(t, files, "/ll/p.m3u8", Options{})
	equal(t, "segments", uris(p.Video), []string{"/ll/s0.m4s", "/ll/s1.m4s"})
	if !containsText(p.Selection.Notes, "3 partial segment") {
		t.Errorf("notes %q do not mention ignored partial segments", p.Selection.Notes)
	}
}

func TestHLSMarkersAreDiagnosticsOnly(t *testing.T) {
	files := map[string]string{"/m/p.m3u8": media(
		`#EXT-X-DATERANGE:ID="early",START-DATE="2024-05-01T12:00:08Z",PLANNED-DURATION=30,SCTE35-OUT=0xFC30`,
		"#EXT-X-PROGRAM-DATE-TIME:2024-05-01T12:00:00.000Z",
		"#EXTINF:6,", "a.m4s",
		"#EXT-X-CUE-OUT:DURATION=30",
		"#EXT-X-PROGRAM-DATE-TIME:2024-05-01T12:00:06.000+00:00",
		"#EXTINF:6,", "b.m4s",
		"#EXT-X-CUE-OUT-CONT:ElapsedTime=6,Duration=30",
		`#EXT-X-DATERANGE:ID="late",START-DATE="2024-05-01T12:00:10.5Z",DURATION=1`,
		`#EXT-X-DATERANGE:ID="nodate",CLASS="x"`,
		`#EXT-OATCLS-SCTE35:/DAlAAAAAAAAAP/wFAUAAAABf+/+`,
		"#EXTINF:6,", "c.m4s",
		"#EXT-X-CUE-IN",
		"#EXTINF:6,", "d.m4s",
	)}
	p := mustLoad(t, files, "/m/p.m3u8", Options{})
	type mk struct {
		kind string
		us   int64
	}
	var got []mk
	for _, m := range p.Markers {
		got = append(got, mk{m.Kind, m.TimeUS})
	}
	equal(t, "markers", got, []mk{
		{"EXT-X-DATERANGE", 8_000_000},
		{"EXT-X-CUE-OUT", 6_000_000},
		{"EXT-X-CUE-OUT-CONT", 12_000_000},
		{"EXT-X-DATERANGE", 10_500_000},
		{"EXT-X-DATERANGE", -1},
		{"EXT-OATCLS-SCTE35", 12_000_000},
		{"EXT-X-CUE-IN", 18_000_000},
	})
	if !strings.Contains(p.Markers[0].Detail, `ID="early"`) || p.Markers[1].Detail != "DURATION=30" {
		t.Errorf("marker details: %q, %q", p.Markers[0].Detail, p.Markers[1].Detail)
	}
	// Markers never change the resource model.
	equal(t, "segments", uris(p.Video), []string{"/m/a.m4s", "/m/b.m4s", "/m/c.m4s", "/m/d.m4s"})
	equal(t, "epochs", len(p.Epochs), 1)

	// Without EXT-X-PROGRAM-DATE-TIME a DATERANGE has no presentation time.
	files["/m/q.m3u8"] = media(`#EXT-X-DATERANGE:ID="x",START-DATE="2024-05-01T12:00:08Z"`, "#EXTINF:6,", "a.m4s")
	equal(t, "marker time", mustLoad(t, files, "/m/q.m3u8", Options{}).Markers[0].TimeUS, int64(-1))
}

func TestHLSNoRoundingDrift(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:4\n")
	const n = 30000
	for i := 0; i < n; i++ {
		sb.WriteString("#EXTINF:3.336667,\ns")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(".m4s\n")
	}
	sb.WriteString("#EXT-X-ENDLIST\n")
	p := mustLoad(t, map[string]string{"/d/p.m3u8": sb.String()}, "/d/p.m3u8", Options{})
	last := p.Video.Segments[n-1]
	equal(t, "last start", last.StartUS, int64(n-1)*3_336_667)
	equal(t, "end", p.Epochs[0].EndUS, int64(n)*3_336_667)
}

func TestHLSSegmentLimit(t *testing.T) {
	three := media("#EXTINF:1,", "a.ts", "#EXTINF:1,", "b.ts", "#EXTINF:1,", "c.ts")
	files := map[string]string{
		"/l/master.m3u8": "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"en\",URI=\"audio.m3u8\"\n#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO=\"a\"\np.m3u8\n",
		"/l/p.m3u8":      three,
		"/l/audio.m3u8":  three,
	}
	mustLoad(t, files, "/l/p.m3u8", Options{MaxSegments: 3})
	loadErr(t, files, "/l/p.m3u8", Options{MaxSegments: 2}, ErrResourceLimit)
	// The limit covers video and audio together.
	mustLoad(t, files, "/l/master.m3u8", Options{MaxSegments: 6})
	loadErr(t, files, "/l/master.m3u8", Options{MaxSegments: 5}, ErrResourceLimit)
	mustLoad(t, files, "/l/master.m3u8", Options{MaxSegments: 3, Audio: "none"})
}

// hlsMasterFiles is a Master Playlist with nested directories, a separately
// packaged audio group, caption services, an audio-only variant and an
// I-frame playlist.
func hlsMasterFiles() map[string]string {
	video := func(dir string) string {
		return media(
			`#EXT-X-MAP:URI="../shared/init-`+dir+`.mp4"`,
			"#EXTINF:6.006,", "seg_b.m4s",
			"#EXTINF:6.006,", "seg_a.m4s",
			"#EXTINF:3.003,", "sub/seg_c.m4s",
		)
	}
	audio := func(lang string) string {
		// Audio is segmented differently from video.
		return media(
			`#EXT-X-MAP:URI="init.mp4"`,
			"#EXTINF:3.989333,", lang+"_1.m4s",
			"#EXTINF:3.989333,", lang+"_0.m4s",
			"#EXTINF:3.989333,", lang+"_3.m4s",
			"#EXTINF:3.047001,", lang+"_2.m4s",
		)
	}
	return map[string]string{
		"/pkg/master.m3u8": strings.Join([]string{
			"#EXTM3U",
			"#EXT-X-VERSION:7",
			"#EXT-X-INDEPENDENT-SEGMENTS",
			`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Español",LANGUAGE="es-MX",DEFAULT=NO,AUTOSELECT=YES,URI="audio/es/audio.m3u8"`,
			`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,AUTOSELECT=YES,URI="audio/en/audio.m3u8"`,
			`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="other",NAME="Français",LANGUAGE="fr",DEFAULT=YES,URI="audio/fr/audio.m3u8"`,
			`#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID="cc",NAME="English CC",LANGUAGE="en",INSTREAM-ID="CC1"`,
			`#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID="cc",NAME="Spanish CC",LANGUAGE="es",INSTREAM-ID="CC3"`,
			`#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID="cc2",NAME="Unrelated",LANGUAGE="de",INSTREAM-ID="SERVICE1"`,
			`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",LANGUAGE="en",URI="subs/en.m3u8"`,
			`#EXT-X-STREAM-INF:BANDWIDTH=800000,AVERAGE-BANDWIDTH=700000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2",AUDIO="aud",CLOSED-CAPTIONS="cc",FRAME-RATE=29.970`,
			"video/360p/media.m3u8",
			`#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2",AUDIO="aud",CLOSED-CAPTIONS="cc",FRAME-RATE=59.940,NAME="1080p"`,
			"video/1080p/media.m3u8",
			`#EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2",AUDIO="aud",CLOSED-CAPTIONS="cc"`,
			"video/720p/media.m3u8",
			`#EXT-X-STREAM-INF:BANDWIDTH=128000,CODECS="mp4a.40.2",AUDIO="aud"`,
			"audio/en/audio.m3u8",
			`#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=9000000,RESOLUTION=3840x2160,CODECS="avc1.640033",URI="video/iframe/media.m3u8"`,
			"",
		}, "\n"),
		"/pkg/video/360p/media.m3u8":  video("360p"),
		"/pkg/video/720p/media.m3u8":  video("720p"),
		"/pkg/video/1080p/media.m3u8": video("1080p"),
		"/pkg/audio/en/audio.m3u8":    audio("en"),
		"/pkg/audio/es/audio.m3u8":    audio("es"),
	}
}

func TestHLSMasterPlaylist(t *testing.T) {
	r := &memResolver{files: hlsMasterFiles()}
	p, err := Load(context.Background(), "/pkg/master.m3u8", r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Only the master and the two selected media playlists are read; the
	// I-frame playlist and unselected variants are never opened.
	equal(t, "opens", r.opens, []string{"/pkg/master.m3u8", "/pkg/video/1080p/media.m3u8", "/pkg/audio/en/audio.m3u8"})
	equal(t, "snapshots", len(p.Manifests), 3)

	v := p.Video
	equal(t, "video identity", []any{v.ID, v.Name, v.Codecs, v.Bandwidth, v.Width, v.Height, v.FrameRate, v.ManifestURI},
		[]any{"v1", "1080p", "avc1.640028,mp4a.40.2", int64(5000000), 1920, 1080, "59.940", "/pkg/video/1080p/media.m3u8"})
	equal(t, "video inits", resStrs(v.Inits), []string{"/pkg/video/shared/init-1080p.mp4[0:-1]"})
	equal(t, "video segments", segStrs(v), []string{
		"/pkg/video/1080p/seg_b.m4s[0:-1] #0 @0+6006000 init=0 epoch=0",
		"/pkg/video/1080p/seg_a.m4s[0:-1] #1 @6006000+6006000 init=0 epoch=0",
		"/pkg/video/1080p/sub/seg_c.m4s[0:-1] #2 @12012000+3003000 init=0 epoch=0",
	})

	a := p.Audio
	if a == nil || p.AudioMuxed {
		t.Fatalf("Audio=%v AudioMuxed=%v, want separate audio", a, p.AudioMuxed)
	}
	equal(t, "audio identity", []any{a.ID, a.Name, a.Language, a.Codecs, a.ManifestURI},
		[]any{"aud/English", "English", "en", "mp4a.40.2", "/pkg/audio/en/audio.m3u8"})
	equal(t, "audio inits", resStrs(a.Inits), []string{"/pkg/audio/en/init.mp4[0:-1]"})
	equal(t, "audio segments", segStrs(a), []string{
		"/pkg/audio/en/en_1.m4s[0:-1] #0 @0+3989333 init=0 epoch=0",
		"/pkg/audio/en/en_0.m4s[0:-1] #1 @3989333+3989333 init=0 epoch=0",
		"/pkg/audio/en/en_3.m4s[0:-1] #2 @7978666+3989333 init=0 epoch=0",
		"/pkg/audio/en/en_2.m4s[0:-1] #3 @11967999+3047001 init=0 epoch=0",
	})
	equal(t, "audio epochs", a.Epochs, []TrackEpoch{{Available: true}})
	equal(t, "epochs", p.Epochs, []Epoch{{ID: "0", StartUS: 0, EndUS: 15015000}})
	equal(t, "captions", p.Captions, []CaptionService{
		{ID: "CC1", Name: "English CC", Language: "en"},
		{ID: "CC3", Name: "Spanish CC", Language: "es"},
	})

	// The audio-only variant and the I-frame playlist are not candidates.
	equal(t, "candidate count", len(p.Selection.VideoCandidates), 3)
	if containsText(p.Selection.VideoCandidates, "3840x2160") || containsText(p.Selection.VideoCandidates, "iframe") {
		t.Errorf("I-frame playlist listed as a candidate: %q", p.Selection.VideoCandidates)
	}
	if !containsText(p.Selection.Notes, "I-frame") || !containsText(p.Selection.Notes, "audio-only") {
		t.Errorf("notes %q do not report ignored playlists", p.Selection.Notes)
	}
	equal(t, "warnings", len(p.Warnings), 0)
}

func TestHLSSelection(t *testing.T) {
	files := hlsMasterFiles()
	for _, tc := range []struct {
		opts      Options
		wantVideo string // Track.ID
		wantAudio string // Track.ID, "" = nil
	}{
		{Options{}, "v1", "aud/English"},
		{Options{Video: "highest"}, "v1", "aud/English"},
		{Options{Video: "lowest"}, "v0", "aud/English"},
		{Options{Video: "lowest", MinHeight: 361}, "v2", "aud/English"},
		{Options{Video: "lowest", MinHeight: 2000}, "v1", "aud/English"}, // none qualifies: highest
		{Options{Video: "index=0"}, "v0", "aud/English"},
		{Options{Video: "index=2"}, "v2", "aud/English"},
		{Options{Video: "id=v2"}, "v2", "aud/English"},
		{Options{Video: "id=1080p"}, "v1", "aud/English"},
		{Options{Video: "id=video/720p/media.m3u8"}, "v2", "aud/English"},
		{Options{Video: "id=/pkg/video/360p/media.m3u8"}, "v0", "aud/English"},
		{Options{Video: "bandwidth=2500000"}, "v2", "aud/English"},
		{Options{Audio: "lang=es"}, "v1", "aud/Español"}, // primary subtag match
		{Options{Audio: "lang=ES-mx"}, "v1", "aud/Español"},
		{Options{Audio: "lang=en-US"}, "v1", "aud/English"},
		{Options{Audio: "name=Español"}, "v1", "aud/Español"},
		{Options{Audio: "id=aud/Español"}, "v1", "aud/Español"},
		{Options{Audio: "id=audio/es/audio.m3u8"}, "v1", "aud/Español"},
		{Options{Audio: "none"}, "v1", ""},
	} {
		p := mustLoad(t, files, "/pkg/master.m3u8", tc.opts)
		got := ""
		if p.Audio != nil {
			got = p.Audio.ID
		}
		if p.Video.ID != tc.wantVideo || got != tc.wantAudio {
			t.Errorf("%+v: selected video %q audio %q; want %q / %q", tc.opts, p.Video.ID, got, tc.wantVideo, tc.wantAudio)
		}
		if tc.opts.Audio == "none" && p.AudioMuxed {
			t.Errorf("audio=none reported AudioMuxed")
		}
	}
	for _, o := range []Options{
		{Video: "index=3"}, // only three full-video candidates
		{Video: "id=4k"},
		{Video: "id=video/iframe/media.m3u8"}, // I-frame playlist can never be chosen
		{Video: "bandwidth=9000000"},
		{Video: "bandwidth=128000"}, // audio-only variant
		{Audio: "lang=fr"},          // exists only in another group
		{Audio: "name=Nobody"},
		{Audio: "id=other/Français"},
	} {
		loadErr(t, files, "/pkg/master.m3u8", o, ErrSelection)
	}
	p := mustLoad(t, files, "/pkg/master.m3u8", Options{Video: "id=1080p", Audio: "lang=es"})
	equal(t, "rules", []string{p.Selection.VideoRule, p.Selection.AudioRule}, []string{"id=1080p", "lang=es"})
}

func TestHLSMuxedAudioAndRenditionEdgeCases(t *testing.T) {
	v := media("#EXTINF:4,", "s.ts")
	load := func(master string, opts Options) (*Presentation, *memResolver, error) {
		r := &memResolver{files: map[string]string{"/h/master.m3u8": "#EXTM3U\n" + master, "/h/v.m3u8": v, "/h/a.m3u8": v}}
		p, err := Load(context.Background(), "/h/master.m3u8", r, opts)
		return p, r, err
	}

	// No AUDIO group: audio is assumed muxed.
	p, _, err := load("#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=640x360\nv.m3u8\n", Options{})
	if err != nil || p.Audio != nil || !p.AudioMuxed {
		t.Fatalf("no AUDIO group: %v %+v", err, p)
	}
	_, _, err = load("#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n", Options{Audio: "lang=en"})
	assertKind(t, err, ErrSelection)

	// Default rendition without URI: muxed in the variant; nothing else opened.
	const group = `#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="Alt",LANGUAGE="fr",URI="a.m3u8"` + "\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="Main",LANGUAGE="en",DEFAULT=YES` + "\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO="a"` + "\nv.m3u8\n"
	p, r, err := load(group, Options{})
	if err != nil || p.Audio != nil || !p.AudioMuxed {
		t.Fatalf("rendition without URI: %v %+v", err, p)
	}
	equal(t, "opens", r.opens, []string{"/h/master.m3u8", "/h/v.m3u8"})
	// Selecting the alternate rendition loads its playlist.
	p, _, err = load(group, Options{Audio: "lang=fr"})
	if err != nil || p.Audio == nil || p.AudioMuxed || p.Audio.ID != "a/Alt" {
		t.Fatalf("alternate rendition: %v %+v", err, p)
	}

	// Without DEFAULT=YES the first rendition of the group is used.
	p, _, err = load(`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="First",URI="a.m3u8"`+"\n"+
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="Second",URI="b.m3u8"`+"\n"+
		`#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO="a"`+"\nv.m3u8\n", Options{})
	if err != nil || p.Audio == nil || p.Audio.ID != "a/First" {
		t.Fatalf("first rendition: %v %+v", err, p)
	}

	// A rendition pointing at the video playlist itself is muxed audio.
	p, _, err = load(`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="Same",URI="v.m3u8"`+"\n"+
		`#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO="a"`+"\nv.m3u8\n", Options{})
	if err != nil || p.Audio != nil || !p.AudioMuxed {
		t.Fatalf("rendition sharing the video playlist: %v %+v", err, p)
	}

	// AUDIO group without renditions is malformed.
	_, _, err = load(`#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO="missing"`+"\nv.m3u8\n", Options{})
	assertKind(t, err, ErrInvalid)

	// Only audio-only variants: no full video.
	_, _, err = load(`#EXT-X-STREAM-INF:BANDWIDTH=1,CODECS="mp4a.40.2"`+"\na.m3u8\n", Options{})
	assertKind(t, err, ErrUnsupported)
	// Only an I-frame playlist: it must not stand in for full video.
	_, _, err = load(`#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=1,RESOLUTION=640x360,URI="v.m3u8"`+"\n", Options{})
	assertKind(t, err, ErrUnsupported)

	// Session keys mean DRM.
	_, _, err = load(`#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES,URI="skd://x"`+"\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n", Options{})
	assertKind(t, err, ErrUnsupported)

	// CLOSED-CAPTIONS=NONE suppresses caption services; no attribute lists all.
	cc := `#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID="cc",NAME="CC",INSTREAM-ID="CC1"` + "\n"
	p, _, _ = load(cc+"#EXT-X-STREAM-INF:BANDWIDTH=1,CLOSED-CAPTIONS=NONE\nv.m3u8\n", Options{})
	equal(t, "captions (NONE)", len(p.Captions), 0)
	p, _, _ = load(cc+"#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n", Options{})
	equal(t, "captions (unspecified)", p.Captions, []CaptionService{{ID: "CC1", Name: "CC"}})
}

func TestHLSSelectedPlaylistProblems(t *testing.T) {
	master := "#EXTM3U\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="en",URI="a.m3u8"` + "\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO="a"` + "\nv.m3u8\n"
	good := media("#EXTINF:4,", "s.ts")
	live := "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\ns.ts\n"
	enc := media(`#EXT-X-KEY:METHOD=AES-128,URI="k"`, "#EXTINF:4,", "s.ts")
	iframes := media("#EXT-X-I-FRAMES-ONLY", "#EXTINF:4,", "#EXT-X-BYTERANGE:10@0", "s.ts")
	for _, tc := range []struct {
		name         string
		video, audio string
		want         error
	}{
		{"live video", live, good, ErrUnsupported},
		{"live audio", good, live, ErrUnsupported},
		{"encrypted video", enc, good, ErrUnsupported},
		{"encrypted audio", good, enc, ErrUnsupported},
		{"variant is an I-frame playlist", iframes, good, ErrUnsupported},
		{"variant is a master", master, good, ErrInvalid},
		{"variant is an MPD", tinyMPD, good, ErrInvalid},
		{"audio is garbage", good, "nope", ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loadErr(t, map[string]string{"/h/master.m3u8": master, "/h/v.m3u8": tc.video, "/h/a.m3u8": tc.audio},
				"/h/master.m3u8", Options{}, tc.want)
		})
	}
	// Missing selected playlists are I/O failures.
	loadErr(t, map[string]string{"/h/master.m3u8": master}, "/h/master.m3u8", Options{}, ErrIO)
	loadErr(t, map[string]string{"/h/master.m3u8": master, "/h/v.m3u8": good}, "/h/master.m3u8", Options{}, ErrIO)
	// ...unless audio is not wanted.
	mustLoad(t, map[string]string{"/h/master.m3u8": master, "/h/v.m3u8": good}, "/h/master.m3u8", Options{Audio: "none"})
}

func TestHLSAudioEpochAlignment(t *testing.T) {
	master := "#EXTM3U\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="en",URI="audio/a.m3u8"` + "\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=640x360,AUDIO="a"` + "\nvideo/v.m3u8\n"
	video := media(
		"#EXT-X-DISCONTINUITY-SEQUENCE:10",
		"#EXTINF:6,", "v0.m4s", "#EXTINF:6,", "v1.m4s", // epoch 10: 12 s
		"#EXT-X-DISCONTINUITY",
		"#EXTINF:6,", "v2.m4s", "#EXTINF:4,", "v3.m4s", // epoch 11: 10 s
		"#EXT-X-DISCONTINUITY",
		"#EXTINF:6,", "v4.m4s", // epoch 12
	)
	audio := media(
		"#EXT-X-DISCONTINUITY-SEQUENCE:10",
		"#EXTINF:4.01,", "a0.m4s", "#EXTINF:4.01,", "a1.m4s", "#EXTINF:4.01,", "a2.m4s", // 12.03 s: slightly longer than video
		"#EXT-X-DISCONTINUITY",
		"#EXTINF:5,", "a3.m4s", "#EXTINF:5,", "a4.m4s",
		"#EXT-X-DISCONTINUITY",
		"#EXTINF:6,", "a5.m4s",
	)
	files := map[string]string{"/e/master.m3u8": master, "/e/video/v.m3u8": video, "/e/audio/a.m3u8": audio}
	p := mustLoad(t, files, "/e/master.m3u8", Options{})
	equal(t, "epochs", p.Epochs, []Epoch{
		{ID: "10", StartUS: 0, EndUS: 12_000_000},
		{ID: "11", StartUS: 12_000_000, EndUS: 22_000_000},
		{ID: "12", StartUS: 22_000_000, EndUS: 28_000_000},
	})
	// Audio epochs are anchored at the video-declared epoch starts.
	equal(t, "audio segments", segStrs(p.Audio), []string{
		"/e/audio/a0.m4s[0:-1] #0 @0+4010000 init=-1 epoch=0",
		"/e/audio/a1.m4s[0:-1] #1 @4010000+4010000 init=-1 epoch=0",
		"/e/audio/a2.m4s[0:-1] #2 @8020000+4010000 init=-1 epoch=0",
		"/e/audio/a3.m4s[0:-1] #3 @12000000+5000000 init=-1 epoch=1",
		"/e/audio/a4.m4s[0:-1] #4 @17000000+5000000 init=-1 epoch=1",
		"/e/audio/a5.m4s[0:-1] #5 @22000000+6000000 init=-1 epoch=2",
	})
	equal(t, "warnings", len(p.Warnings), 0)

	// Audio missing one of the video's epochs: that epoch is unavailable.
	files["/e/audio/a.m3u8"] = media(
		"#EXT-X-DISCONTINUITY-SEQUENCE:10",
		"#EXTINF:6,", "a0.m4s",
		"#EXT-X-DISCONTINUITY", "#EXT-X-DISCONTINUITY",
		"#EXTINF:6,", "a5.m4s",
	)
	p = mustLoad(t, files, "/e/master.m3u8", Options{})
	equal(t, "audio track epochs", p.Audio.Epochs, []TrackEpoch{{Available: true}, {Available: false}, {Available: true}})
	equal(t, "audio segments", segStrs(p.Audio), []string{
		"/e/audio/a0.m4s[0:-1] #0 @0+6000000 init=-1 epoch=0",
		"/e/audio/a5.m4s[0:-1] #1 @22000000+6000000 init=-1 epoch=2",
	})
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "epoch 11") {
		t.Errorf("warnings = %q, want one about epoch 11", p.Warnings)
	}

	// An audio discontinuity sequence unknown to the video playlist is invalid.
	for _, bad := range []string{
		media("#EXT-X-DISCONTINUITY-SEQUENCE:9", "#EXTINF:6,", "a0.m4s"),
		media("#EXT-X-DISCONTINUITY-SEQUENCE:12", "#EXTINF:6,", "a0.m4s", "#EXT-X-DISCONTINUITY", "#EXTINF:6,", "a1.m4s"),
		media("#EXTINF:6,", "a0.m4s"),
	} {
		files["/e/audio/a.m3u8"] = bad
		loadErr(t, files, "/e/master.m3u8", Options{}, ErrInvalid)
	}
}

func TestAttrIter(t *testing.T) {
	type kv struct{ k, v string }
	for in, want := range map[string][]kv{
		`BANDWIDTH=1280000,CODECS="avc1.4d401f,mp4a.40.2",RESOLUTION=1280x720`: {{"BANDWIDTH", "1280000"}, {"CODECS", "avc1.4d401f,mp4a.40.2"}, {"RESOLUTION", "1280x720"}},
		`URI="a,b=c.mp4" , BYTERANGE="10@0"`:                                   {{"URI", "a,b=c.mp4"}, {"BYTERANGE", "10@0"}},
		`A=1,,B=2,`:                                                            {{"A", "1"}, {"B", "2"}},
		`K="unterminated`:                                                      {{"K", "unterminated"}},
		`NAME=""`:                                                              {{"NAME", ""}},
		``:                                                                     nil,
		`novalue`:                                                              nil,
	} {
		var got []kv
		it := attrIter{in}
		for {
			k, v, ok := it.next()
			if !ok {
				break
			}
			got = append(got, kv{k, v})
		}
		equal(t, "attrs of "+in, got, want)
	}
}

func TestHLSManyInitializationMaps(t *testing.T) {
	// Many distinct maps (beyond the small linear-search window) are still
	// interned once each, in first-use order.
	var lines []string
	const maps = 60
	for round := 0; round < 3; round++ {
		for i := 0; i < maps; i++ {
			lines = append(lines, `#EXT-X-MAP:URI="init-`+strconv.Itoa(i)+`.mp4"`, "#EXTINF:1,", "s.m4s")
		}
	}
	p := mustLoad(t, map[string]string{"/i/p.m3u8": media(lines...)}, "/i/p.m3u8", Options{})
	equal(t, "init count", len(p.Video.Inits), maps)
	for i, s := range p.Video.Segments {
		if want := i % maps; s.InitIndex != want || p.Video.Inits[s.InitIndex].URI != "/i/init-"+strconv.Itoa(want)+".mp4" {
			t.Fatalf("segment %d: init index %d (%v), want %d", i, s.InitIndex, p.Video.Inits[s.InitIndex], want)
		}
	}
}

func TestHLSDateFormats(t *testing.T) {
	for in, ok := range map[string]bool{
		"2024-05-01T12:00:00Z":            true,
		"2024-05-01T12:00:00.123456789Z":  true,
		"2024-05-01T12:00:00.000+02:00":   true,
		"2024-05-01T12:00:00.000+0200":    true,
		" 2024-05-01T12:00:00-0700 ":      true,
		"2024-05-01 12:00:00":             false,
		"yesterday":                       false,
		"":                                false,
		"2024-13-01T12:00:00Z":            false,
		"2024-05-01T12:00:00.000+02:00:1": false,
	} {
		if _, got := parseHLSTime(in); got != ok {
			t.Errorf("parseHLSTime(%q) ok = %v, want %v", in, got, ok)
		}
	}
	// A DATERANGE before the playlist's first date anchor maps to no time;
	// an unparsable anchor leaves markers without a time.
	files := map[string]string{"/m/p.m3u8": media(
		"#EXT-X-PROGRAM-DATE-TIME:2024-05-01T12:00:10.000+0000",
		`#EXT-X-DATERANGE:ID="before",START-DATE="2024-05-01T12:00:00Z"`,
		`#EXT-X-DATERANGE:ID="in",START-DATE="2024-05-01T12:00:11.250Z"`,
		"#EXTINF:6,", "a.m4s",
		"#EXT-X-PROGRAM-DATE-TIME:garbage",
		`#EXT-X-DATERANGE:ID="lost",START-DATE="2024-05-01T12:00:12Z"`,
		"#EXTINF:6,", "b.m4s",
	)}
	p := mustLoad(t, files, "/m/p.m3u8", Options{})
	equal(t, "marker times", []int64{p.Markers[0].TimeUS, p.Markers[1].TimeUS, p.Markers[2].TimeUS}, []int64{-1, 1_250_000, -1})
}
