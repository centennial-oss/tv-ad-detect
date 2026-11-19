// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

const largeCount = 100_000

func largeHLS(byteRanges bool) string {
	var sb strings.Builder
	sb.Grow(largeCount * 48)
	sb.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:7\n#EXT-X-MAP:URI=\"init.mp4\"\n")
	for i := 0; i < largeCount; i++ {
		if i%5000 == 4999 {
			sb.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		sb.WriteString("#EXTINF:6.006,\n")
		if byteRanges {
			sb.WriteString("#EXT-X-BYTERANGE:750000\nmedia.mp4\n")
			continue
		}
		sb.WriteString("seg-")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(".m4s\n")
	}
	sb.WriteString("#EXT-X-ENDLIST\n")
	return sb.String()
}

func largeDASH(kind string) string {
	var sb strings.Builder
	sb.Grow(largeCount * 40)
	sb.WriteString(`<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static"><Period duration="PT200000S"><AdaptationSet mimeType="video/mp4">`)
	switch kind {
	case "timeline":
		sb.WriteString(`<SegmentTemplate timescale="90000" media="$RepresentationID$/$Time$.m4s" initialization="$RepresentationID$/init.mp4"><SegmentTimeline>`)
		for i := 0; i < largeCount; i++ {
			// Alternating durations defeat run-length compression: one S per segment.
			sb.WriteString(`<S t="`)
			sb.WriteString(strconv.Itoa(i * 180000))
			sb.WriteString(`" d="`)
			sb.WriteString(strconv.Itoa(180000 - i%2))
			sb.WriteString(`"/>`)
		}
		sb.WriteString(`</SegmentTimeline></SegmentTemplate><Representation id="v" bandwidth="1" width="1280" height="720"/>`)
	case "number":
		sb.WriteString(`<SegmentTemplate timescale="90000" duration="180000" media="$RepresentationID$/$Number%06d$.m4s" initialization="$RepresentationID$/init.mp4"/><Representation id="v" bandwidth="1" width="1280" height="720"/>`)
	case "list":
		sb.WriteString(`<Representation id="v" bandwidth="1" width="1280" height="720"><BaseURL>v.mp4</BaseURL><SegmentList timescale="90000" duration="180000"><Initialization range="0-999"/>`)
		for i := 0; i < largeCount; i++ {
			sb.WriteString(`<SegmentURL mediaRange="`)
			sb.WriteString(strconv.Itoa(1000 + i*1000))
			sb.WriteByte('-')
			sb.WriteString(strconv.Itoa(1999 + i*1000))
			sb.WriteString(`"/>`)
		}
		sb.WriteString(`</SegmentList></Representation>`)
	}
	sb.WriteString(`</AdaptationSet></Period></MPD>`)
	return sb.String()
}

func largeInputs() map[string]string {
	return map[string]string{
		"/big/hls.m3u8":       largeHLS(false),
		"/big/hls-range.m3u8": largeHLS(true),
		"/big/timeline.mpd":   largeDASH("timeline"),
		"/big/number.mpd":     largeDASH("number"),
		"/big/list.mpd":       largeDASH("list"),
	}
}

// 100k-segment manifests must resolve completely, in order, and quickly.
func TestLargeManifests(t *testing.T) {
	files := largeInputs()
	for uri := range files {
		t.Run(uri, func(t *testing.T) {
			start := time.Now()
			p := mustLoad(t, files, uri, Options{})
			elapsed := time.Since(start)
			segs := p.Video.Segments
			if len(segs) != largeCount {
				t.Fatalf("%d segments, want %d", len(segs), largeCount)
			}
			for i := 1; i < len(segs); i++ {
				if segs[i].StartUS < segs[i-1].StartUS || segs[i].Number != segs[i-1].Number+1 {
					t.Fatalf("segment %d out of order: %s after %s", i, segStr(segs[i]), segStr(segs[i-1]))
				}
			}
			equal(t, "inits", len(p.Video.Inits), 1)
			switch uri {
			case "/big/hls.m3u8":
				equal(t, "last", segStr(segs[largeCount-1]), "/big/seg-99999.m4s[0:-1] #99999 @600593994000+6006000 init=0 epoch=20")
				equal(t, "epochs", len(p.Epochs), 21)
			case "/big/hls-range.m3u8":
				equal(t, "last", segStr(segs[largeCount-1]), "/big/media.mp4[74999250000:750000] #99999 @600593994000+6006000 init=0 epoch=20")
			case "/big/timeline.mpd":
				equal(t, "last", segStr(segs[largeCount-1]), "/big/v/17999820000.m4s[0:-1] #100000 @199998000000+1999988 init=0 epoch=0")
			case "/big/number.mpd":
				equal(t, "last", segStr(segs[largeCount-1]), "/big/v/100000.m4s[0:-1] #100000 @199998000000+2000000 init=0 epoch=0")
			case "/big/list.mpd":
				equal(t, "last", segStr(segs[largeCount-1]), "/big/v.mp4[100000000:1000] #100000 @199998000000+2000000 init=0 epoch=0")
			}
			// Generous bound (typical: a few tens of milliseconds); it only
			// guards against accidental quadratic behaviour.
			if elapsed > 5*time.Second {
				t.Errorf("loading %d segments took %v", largeCount, elapsed)
			}
			t.Logf("%s: %d segments in %v", uri, largeCount, elapsed)
		})
	}
}

func BenchmarkLoad100k(b *testing.B) {
	files := largeInputs()
	for _, uri := range []string{"/big/hls.m3u8", "/big/hls-range.m3u8", "/big/timeline.mpd", "/big/number.mpd", "/big/list.mpd"} {
		b.Run(strings.TrimPrefix(uri, "/big/"), func(b *testing.B) {
			r := &memResolver{files: files}
			ctx := context.Background()
			b.SetBytes(int64(len(files[uri])))
			b.ReportAllocs()
			for b.Loop() {
				r.opens = r.opens[:0]
				if _, err := Load(ctx, uri, r, Options{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
