// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"fmt"
	"strings"
	"testing"
)

// mpd wraps Period markup in a static MPD.
func mpd(attrs string, body ...string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" xmlns:xlink="http://www.w3.org/1999/xlink" ` + attrs + ">\n" +
		strings.Join(body, "\n") + "\n</MPD>\n"
}

// videoSet is a minimal video AdaptationSet with number-based addressing.
func videoSet(id string, extra ...string) string {
	return `<AdaptationSet mimeType="video/mp4">` +
		`<SegmentTemplate duration="10" media="$RepresentationID$/$Number$.m4s" initialization="$RepresentationID$/init.mp4"/>` +
		`<Representation id="` + id + `" bandwidth="1000000" width="1280" height="720" codecs="avc1.64001f"/>` +
		strings.Join(extra, "") + `</AdaptationSet>`
}

func loadMPD(t *testing.T, doc string, opts Options) *Presentation {
	t.Helper()
	return mustLoad(t, map[string]string{"/d/manifest.mpd": doc}, "/d/manifest.mpd", opts)
}

func mpdErr(t *testing.T, doc string, opts Options, want error) error {
	t.Helper()
	return loadErr(t, map[string]string{"/d/manifest.mpd": doc}, "/d/manifest.mpd", opts, want)
}

// dashMultiPeriod exercises two Periods, BaseURL chains at every level,
// SegmentTemplate inheritance with per-attribute override, $Number$ with a
// width format, startNumber, fixed durations, a $Time$ timeline with
// repeats, presentationTimeOffset and a Period without audio.
const dashMultiPeriod = `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT1M10S" minBufferTime="PT2S" profiles="urn:mpeg:dash:profile:isoff-live:2011">
  <BaseURL>media/</BaseURL>
  <Period id="p0" duration="PT30S">
    <BaseURL>p0/</BaseURL>
    <AdaptationSet contentType="video" mimeType="video/mp4" frameRate="30000/1001">
      <Label>Main video</Label>
      <Accessibility schemeIdUri="urn:scte:dash:cc:cea-608:2015" value="CC1=eng;CC3=spa"/>
      <SegmentTemplate timescale="90000" duration="900000" startNumber="5" initialization="$RepresentationID$/init.mp4" media="$RepresentationID$/seg-$Number%05d$.m4s"/>
      <Representation id="v720" bandwidth="2000000" width="1280" height="720" codecs="avc1.64001f"/>
      <Representation id="v1080" bandwidth="5000000" width="1920" height="1080" codecs="avc1.640028">
        <BaseURL>hd/</BaseURL>
        <SegmentTemplate startNumber="1"/>
      </Representation>
    </AdaptationSet>
    <AdaptationSet contentType="audio" mimeType="audio/mp4" lang="en">
      <BaseURL>../audio/</BaseURL>
      <SegmentTemplate timescale="48000" duration="192000" initialization="$RepresentationID$-init.mp4" media="$RepresentationID$-$Number$.m4s"/>
      <Representation id="a-lo" bandwidth="64000" codecs="mp4a.40.5"/>
      <Representation id="a-hi" bandwidth="128000" codecs="mp4a.40.2"/>
    </AdaptationSet>
    <AdaptationSet contentType="text" mimeType="application/mp4" codecs="stpp" lang="en">
      <SegmentTemplate duration="10" media="text-$Number$.m4s"/>
      <Representation id="t1" bandwidth="1000"/>
    </AdaptationSet>
  </Period>
  <Period id="p1">
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <SegmentTemplate timescale="1000" presentationTimeOffset="5000" initialization="p1/$RepresentationID$/init.mp4" media="p1/$RepresentationID$/t$Time$.m4s">
        <SegmentTimeline>
          <S t="5000" d="10000" r="1"/>
          <S d="8000" r="-1"/>
          <S t="41000" d="2000" r="-1"/>
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v720" bandwidth="2000000" width="1280" height="720" codecs="avc1.64001f"/>
      <Representation id="v1080" bandwidth="5000000" width="1920" height="1080" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
</MPD>
`

func TestDASHMultiPeriod(t *testing.T) {
	p := loadMPD(t, dashMultiPeriod, Options{})
	equal(t, "format", p.Format, FormatDASH)
	equal(t, "manifests", len(p.Manifests), 1)
	equal(t, "epochs", p.Epochs, []Epoch{
		{ID: "p0", StartUS: 0, EndUS: 30_000_000},
		{ID: "p1", StartUS: 30_000_000, EndUS: 70_000_000},
	})

	v := p.Video
	equal(t, "video identity", []any{v.ID, v.Name, v.Codecs, v.Bandwidth, v.Width, v.Height, v.FrameRate, v.ManifestURI},
		[]any{"v1080", "Main video", "avc1.640028", int64(5000000), 1920, 1080, "30000/1001", "/d/manifest.mpd"})
	equal(t, "video inits", resStrs(v.Inits), []string{
		"/d/media/p0/hd/v1080/init.mp4[0:-1]",
		"/d/media/p1/v1080/init.mp4[0:-1]",
	})
	equal(t, "video segments", segStrs(v), []string{
		// Period p0: 30 s of 10 s segments; startNumber overridden to 1 at the Representation.
		"/d/media/p0/hd/v1080/seg-00001.m4s[0:-1] #1 @0+10000000 init=0 epoch=0",
		"/d/media/p0/hd/v1080/seg-00002.m4s[0:-1] #2 @10000000+10000000 init=0 epoch=0",
		"/d/media/p0/hd/v1080/seg-00003.m4s[0:-1] #3 @20000000+10000000 init=0 epoch=0",
		// Period p1: timeline; r=-1 runs to the next S@t, the last r=-1 to the Period end.
		"/d/media/p1/v1080/t5000.m4s[0:-1] #1 @30000000+10000000 init=1 epoch=1",
		"/d/media/p1/v1080/t15000.m4s[0:-1] #2 @40000000+10000000 init=1 epoch=1",
		"/d/media/p1/v1080/t25000.m4s[0:-1] #3 @50000000+8000000 init=1 epoch=1",
		"/d/media/p1/v1080/t33000.m4s[0:-1] #4 @58000000+8000000 init=1 epoch=1",
		"/d/media/p1/v1080/t41000.m4s[0:-1] #5 @66000000+2000000 init=1 epoch=1",
		"/d/media/p1/v1080/t43000.m4s[0:-1] #6 @68000000+2000000 init=1 epoch=1",
	})
	equal(t, "video track epochs", v.Epochs, []TrackEpoch{
		{Available: true, RepresentationID: "v1080", Timescale: 90000, PresentationTimeOffset: 0},
		{Available: true, RepresentationID: "v1080", Timescale: 1000, PresentationTimeOffset: 5000},
	})

	a := p.Audio
	if a == nil || p.AudioMuxed {
		t.Fatalf("Audio=%v AudioMuxed=%v", a, p.AudioMuxed)
	}
	equal(t, "audio identity", []any{a.ID, a.Language, a.Codecs, a.Bandwidth}, []any{"a-hi", "en", "mp4a.40.2", int64(128000)})
	equal(t, "audio inits", resStrs(a.Inits), []string{"/d/media/audio/a-hi-init.mp4[0:-1]"})
	// 30 s of 4 s segments: ceil(30/4) = 8, the last one overhangs the Period end.
	equal(t, "audio segment count", len(a.Segments), 8)
	equal(t, "audio first", segStr(a.Segments[0]), "/d/media/audio/a-hi-1.m4s[0:-1] #1 @0+4000000 init=0 epoch=0")
	equal(t, "audio last", segStr(a.Segments[7]), "/d/media/audio/a-hi-8.m4s[0:-1] #8 @28000000+4000000 init=0 epoch=0")
	equal(t, "audio track epochs", a.Epochs, []TrackEpoch{
		{Available: true, RepresentationID: "a-hi", Timescale: 48000},
		{Available: false},
	})
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], `Period "p1" has no audio`) {
		t.Errorf("warnings = %q, want one about missing audio in p1", p.Warnings)
	}
	equal(t, "captions", p.Captions, []CaptionService{
		{ID: "CC1", Name: "urn:scte:dash:cc:cea-608:2015", Language: "eng"},
		{ID: "CC3", Name: "urn:scte:dash:cc:cea-608:2015", Language: "spa"},
	})
	equal(t, "candidates", len(p.Selection.VideoCandidates), 2)
}

func TestDASHRepresentationSelection(t *testing.T) {
	for _, tc := range []struct {
		opts      Options
		wantVideo string
		wantAudio string
	}{
		{Options{}, "v1080", "a-hi"},
		{Options{Video: "lowest"}, "v720", "a-hi"},
		{Options{Video: "lowest", MinHeight: 1000}, "v1080", "a-hi"},
		{Options{Video: "index=0"}, "v720", "a-hi"},
		{Options{Video: "id=v720"}, "v720", "a-hi"},
		{Options{Video: "bandwidth=2000000"}, "v720", "a-hi"},
		{Options{Audio: "id=a-lo"}, "v1080", "a-lo"},
		{Options{Audio: "lang=EN"}, "v1080", "a-hi"},
		{Options{Audio: "lang=en-GB"}, "v1080", "a-hi"},
		{Options{Audio: "none"}, "v1080", ""},
	} {
		p := loadMPD(t, dashMultiPeriod, tc.opts)
		got := ""
		if p.Audio != nil {
			got = p.Audio.ID
		}
		if p.Video.ID != tc.wantVideo || got != tc.wantAudio {
			t.Errorf("%+v: selected video %q audio %q; want %q / %q", tc.opts, p.Video.ID, got, tc.wantVideo, tc.wantAudio)
		}
		if tc.opts.Audio == "none" && len(p.Warnings) != 0 {
			t.Errorf("audio=none produced warnings %q", p.Warnings)
		}
	}
	// An explicitly selected Representation is used in every Period that has it.
	p := loadMPD(t, dashMultiPeriod, Options{Video: "id=v720"})
	equal(t, "first", p.Video.Segments[0].URI, "/d/media/p0/v720/seg-00005.m4s") // inherited startNumber=5
	equal(t, "last", p.Video.Segments[len(p.Video.Segments)-1].URI, "/d/media/p1/v720/t43000.m4s")
	equal(t, "numbers", []int64{p.Video.Segments[0].Number, p.Video.Segments[2].Number}, []int64{5, 7})

	for _, o := range []Options{
		{Video: "index=2"}, {Video: "id=t1"}, {Video: "id=a-hi"}, {Video: "bandwidth=1"},
		{Audio: "lang=fr"}, {Audio: "id=v720"}, {Audio: "name=Commentary"},
	} {
		mpdErr(t, dashMultiPeriod, o, ErrSelection)
	}
}

func TestDASHTemplateIdentifiers(t *testing.T) {
	doc := mpd(`type="static" mediaPresentationDuration="PT6S"`,
		`<Period>`,
		`<AdaptationSet mimeType="video/mp4">`,
		`<SegmentTemplate timescale="10" startNumber="0" initialization="i_$RepresentationID$_$Bandwidth$_$$.mp4" media="$$$RepresentationID$$$/b$Bandwidth%08d$/n$Number%03d$-t$Time%6d$-$Number$.m4s">`,
		`<SegmentTimeline><S t="100" d="20"/><S d="20" r="1"/></SegmentTimeline>`,
		`</SegmentTemplate>`,
		`<Representation id="r1" bandwidth="1500" width="640" height="360"/>`,
		`</AdaptationSet></Period>`)
	p := loadMPD(t, strings.Replace(doc, `timescale="10"`, `timescale="10" presentationTimeOffset="100"`, 1), Options{})
	equal(t, "inits", resStrs(p.Video.Inits), []string{"/d/i_r1_1500_$.mp4[0:-1]"})
	equal(t, "segments", segStrs(p.Video), []string{
		"/d/$r1$/b00001500/n000-t   100-0.m4s[0:-1] #0 @0+2000000 init=0 epoch=0",
		"/d/$r1$/b00001500/n001-t   120-1.m4s[0:-1] #1 @2000000+2000000 init=0 epoch=0",
		"/d/$r1$/b00001500/n002-t   140-2.m4s[0:-1] #2 @4000000+2000000 init=0 epoch=0",
	})

	for _, tc := range []struct {
		media string
		want  error
	}{
		{"seg-$Number.m4s", ErrInvalid},            // unterminated
		{"seg-$Foo$.m4s", ErrInvalid},              // unknown identifier
		{"seg-$Number%05x$.m4s", ErrInvalid},       // unsupported conversion
		{"seg-$Number%d5$.m4s", ErrInvalid},        // malformed format
		{"$RepresentationID%02d$.m4s", ErrInvalid}, // no format on RepresentationID
		{"seg-$SubNumber$.m4s", ErrUnsupported},
	} {
		bad := mpd(`type="static" mediaPresentationDuration="PT6S"`, `<Period><AdaptationSet mimeType="video/mp4">`,
			`<SegmentTemplate duration="2" media="`+tc.media+`"/>`,
			`<Representation id="r1" bandwidth="1" width="640" height="360"/></AdaptationSet></Period>`)
		mpdErr(t, bad, Options{}, tc.want)
	}
	// $Number$/$Time$ make no sense in an initialization template.
	mpdErr(t, mpd(`type="static" mediaPresentationDuration="PT6S"`, `<Period><AdaptationSet mimeType="video/mp4">`,
		`<SegmentTemplate duration="2" media="s$Number$.m4s" initialization="i$Number$.mp4"/>`,
		`<Representation id="r1" bandwidth="1"/></AdaptationSet></Period>`), Options{}, ErrInvalid)
}

func TestDASHFixedDurationCount(t *testing.T) {
	tmpl := func(period, st string) string {
		return mpd(`type="static"`, `<Period duration="`+period+`"><AdaptationSet mimeType="video/mp4">`,
			`<SegmentTemplate `+st+` media="s$Number$.m4s" initialization="i.mp4"/>`,
			`<Representation id="r" bandwidth="1" width="640" height="360"/></AdaptationSet></Period>`)
	}
	for _, tc := range []struct {
		period, st  string
		count       int
		first, last int64 // segment numbers
		lastStartUS int64
	}{
		{"PT30S", `duration="10"`, 3, 1, 3, 20_000_000},
		{"PT30.000001S", `duration="10"`, 4, 1, 4, 30_000_000}, // ceil
		{"PT29.999999S", `duration="10"`, 3, 1, 3, 20_000_000},
		{"PT1S", `duration="10"`, 1, 1, 1, 0},
		{"PT10S", `timescale="30000" duration="60060"`, 5, 1, 5, 8_008_000}, // 2.002 s segments
		{"PT10.01S", `timescale="30000" duration="60060"`, 5, 1, 5, 8_008_000},
		{"PT10.011S", `timescale="30000" duration="60060"`, 6, 1, 6, 10_010_000},
		{"PT30S", `duration="10" startNumber="0"`, 3, 0, 2, 20_000_000},
		{"PT30S", `duration="10" startNumber="100"`, 3, 100, 102, 20_000_000},
		{"PT30S", `duration="10" startNumber="7" endNumber="8"`, 2, 7, 8, 10_000_000},
		{"PT30S", `duration="10" endNumber="50"`, 3, 1, 3, 20_000_000},
	} {
		p := loadMPD(t, tmpl(tc.period, tc.st), Options{})
		segs := p.Video.Segments
		if len(segs) != tc.count {
			t.Errorf("%s %s: %d segments, want %d", tc.period, tc.st, len(segs), tc.count)
			continue
		}
		if segs[0].Number != tc.first || segs[len(segs)-1].Number != tc.last || segs[len(segs)-1].StartUS != tc.lastStartUS {
			t.Errorf("%s %s: numbers %d..%d last start %d; want %d..%d, %d", tc.period, tc.st,
				segs[0].Number, segs[len(segs)-1].Number, segs[len(segs)-1].StartUS, tc.first, tc.last, tc.lastStartUS)
		}
	}
	// 30000/1001-style durations accumulate no drift: segment k starts at exactly k*d/timescale.
	p := loadMPD(t, tmpl("PT2H", `timescale="30000" duration="60060"`), Options{})
	for _, k := range []int{1, 1000, 3596} {
		equal(t, fmt.Sprintf("start of segment %d", k), p.Video.Segments[k].StartUS, int64(k)*2_002_000)
	}
	equal(t, "count", len(p.Video.Segments), 3597) // ceil(7200 / 2.002)

	for _, st := range []string{`duration="0"`, `duration="-5"`, `duration="10" timescale="0"`, `duration="x"`,
		`duration="10" startNumber="-1"`, `duration="10" presentationTimeOffset="-1"`} {
		mpdErr(t, tmpl("PT30S", st), Options{}, ErrInvalid)
	}
}

func TestDASHTimeline(t *testing.T) {
	tl := func(period, attrs, timeline string) string {
		return mpd(`type="static"`, `<Period `+period+`><AdaptationSet mimeType="video/mp4">`,
			`<SegmentTemplate `+attrs+` media="$Time$_$Number$.m4s" initialization="i.mp4">`,
			`<SegmentTimeline>`+timeline+`</SegmentTimeline></SegmentTemplate>`,
			`<Representation id="r" bandwidth="1" width="640" height="360"/></AdaptationSet></Period>`)
	}
	names := func(p *Presentation) []string {
		var out []string
		for _, s := range p.Video.Segments {
			out = append(out, fmt.Sprintf("%s@%d+%d", strings.TrimSuffix(strings.TrimPrefix(s.URI, "/d/"), ".m4s"), s.StartUS/1000, s.DurationUS/1000))
		}
		return out
	}
	for _, tc := range []struct {
		name, period, attrs, timeline string
		want                          []string
	}{
		{"plain repeats", `duration="PT1M"`, `timescale="1000"`, `<S t="0" d="2000" r="2"/><S d="1000"/><S d="3000" r="1"/>`,
			[]string{"0_1@0+2000", "2000_2@2000+2000", "4000_3@4000+2000", "6000_4@6000+1000", "7000_5@7000+3000", "10000_6@10000+3000"}},
		{"first S@t defaults to 0; later S@t may leave a gap", `duration="PT1M"`, `timescale="1000"`, `<S d="2000"/><S t="5000" d="2000"/>`,
			[]string{"0_1@0+2000", "5000_2@5000+2000"}},
		{"negative repeat bounded by the next S@t", `duration="PT1M"`, `timescale="1000"`, `<S t="0" d="4000" r="-1"/><S t="10000" d="1000"/>`,
			[]string{"0_1@0+4000", "4000_2@4000+4000", "8000_3@8000+4000", "10000_4@10000+1000"}},
		{"negative repeat bounded by the Period end", `duration="PT9S"`, `timescale="1000"`, `<S t="0" d="4000" r="-1"/>`,
			[]string{"0_1@0+4000", "4000_2@4000+4000", "8000_3@8000+4000"}},
		{"negative repeat ending exactly at the Period end", `duration="PT8S"`, `timescale="1000"`, `<S t="0" d="4000" r="-1"/>`,
			[]string{"0_1@0+4000", "4000_2@4000+4000"}},
		{"negative repeat with presentationTimeOffset", `duration="PT6S"`, `timescale="1000" presentationTimeOffset="100000"`, `<S t="100000" d="4000" r="-1"/>`,
			[]string{"100000_1@0+4000", "104000_2@4000+4000"}},
		{"segments beyond the Period end are not emitted", `duration="PT5S"`, `timescale="1000"`, `<S t="0" d="2000" r="9"/>`,
			[]string{"0_1@0+2000", "2000_2@2000+2000", "4000_3@4000+2000"}},
		{"segments before the Period start are skipped but keep their numbers", `duration="PT40S"`, `presentationTimeOffset="25"`, `<S t="0" d="10" r="9"/>`,
			[]string{"20_3@-5000+10000", "30_4@5000+10000", "40_5@15000+10000", "50_6@25000+10000", "60_7@35000+10000"}},
		{"endNumber bounds the timeline", `duration="PT1M"`, `timescale="1000" startNumber="10" endNumber="11"`, `<S t="0" d="2000" r="5"/>`,
			[]string{"0_10@0+2000", "2000_11@2000+2000"}},
		{"Period start offsets the presentation time", `start="PT1M40S" duration="PT4S"`, `timescale="1000" presentationTimeOffset="7000"`, `<S t="7000" d="2000" r="1"/>`,
			[]string{"7000_1@100000+2000", "9000_2@102000+2000"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			equal(t, "segments", names(loadMPD(t, tl(tc.period, tc.attrs, tc.timeline), Options{})), tc.want)
		})
	}

	// Explicit repeats need no Period end; unbounded negative repeats do.
	p := loadMPD(t, tl("", `timescale="1000"`, `<S t="0" d="2000" r="1"/>`), Options{})
	equal(t, "open-ended Period", p.Epochs, []Epoch{{ID: "0", StartUS: 0, EndUS: -1}})
	equal(t, "segments", len(p.Video.Segments), 2)
	mpdErr(t, tl("", `timescale="1000"`, `<S t="0" d="2000" r="-1"/>`), Options{}, ErrInvalid)

	for _, timeline := range []string{`<S t="0"/>`, `<S t="0" d="0"/>`, `<S t="0" d="-4"/>`, `<S t="x" d="4"/>`, `<S t="-1" d="4"/>`,
		`<S t="9223372036854775000" d="1000" r="5"/>`} {
		mpdErr(t, tl(`duration="PT1M"`, `timescale="1000"`, timeline), Options{}, ErrInvalid)
	}

	// Nanosecond timescale over 24 hours stays exact (needs a 128-bit intermediate).
	p = loadMPD(t, tl(`duration="PT24H"`, `timescale="1000000000"`, `<S t="0" d="6000000000" r="-1"/>`), Options{})
	equal(t, "count", len(p.Video.Segments), 14400)
	last := p.Video.Segments[14399]
	equal(t, "last", []int64{last.StartUS, last.DurationUS}, []int64{86_394_000_000, 6_000_000})
	equal(t, "track epoch", p.Video.Epochs[0], TrackEpoch{Available: true, RepresentationID: "r", Timescale: 1_000_000_000})
}

func TestDASHSegmentList(t *testing.T) {
	doc := mpd(`type="static" mediaPresentationDuration="PT12S"`,
		`<Period>`,
		`<AdaptationSet mimeType="video/mp4">`,
		` <Representation id="v" bandwidth="1000" width="640" height="360">`,
		`  <BaseURL>single/video.mp4</BaseURL>`,
		`  <SegmentList timescale="1000" duration="4000" startNumber="3">`,
		`   <Initialization range="0-799"/>`,
		`   <SegmentURL mediaRange="800-1799"/>`,
		`   <SegmentURL mediaRange="1800-3299"/>`,
		`   <SegmentURL media="tail.m4s"/>`,
		`  </SegmentList>`,
		` </Representation>`,
		`</AdaptationSet>`,
		`<AdaptationSet mimeType="audio/mp4" lang="en">`,
		` <SegmentList timescale="48000">`,
		`  <Initialization sourceURL="a/init.mp4"/>`,
		`  <SegmentTimeline><S t="0" d="288000" r="1"/></SegmentTimeline>`,
		` </SegmentList>`,
		` <Representation id="a" bandwidth="64000">`,
		`  <SegmentList>`,
		`   <SegmentURL media="a/2.m4s"/>`,
		`   <SegmentURL media="a/1.m4s" mediaRange="10-19"/>`,
		`  </SegmentList>`,
		` </Representation>`,
		`</AdaptationSet>`,
		`</Period>`)
	p := loadMPD(t, doc, Options{})
	equal(t, "video inits", resStrs(p.Video.Inits), []string{"/d/single/video.mp4[0:800]"})
	equal(t, "video segments", segStrs(p.Video), []string{
		"/d/single/video.mp4[800:1000] #3 @0+4000000 init=0 epoch=0",
		"/d/single/video.mp4[1800:1500] #4 @4000000+4000000 init=0 epoch=0",
		"/d/single/tail.m4s[0:-1] #5 @8000000+4000000 init=0 epoch=0",
	})
	// SegmentList attributes, Initialization and timeline are inherited from the AdaptationSet.
	equal(t, "audio inits", resStrs(p.Audio.Inits), []string{"/d/a/init.mp4[0:-1]"})
	equal(t, "audio segments", segStrs(p.Audio), []string{
		"/d/a/2.m4s[0:-1] #1 @0+6000000 init=0 epoch=0",
		"/d/a/1.m4s[10:10] #2 @6000000+6000000 init=0 epoch=0",
	})
	equal(t, "audio epoch", p.Audio.Epochs[0], TrackEpoch{Available: true, RepresentationID: "a", Timescale: 48000})

	list := func(attrs string, body ...string) string {
		return mpd(`type="static" mediaPresentationDuration="PT12S"`, `<Period><AdaptationSet mimeType="video/mp4">`,
			`<Representation id="v" bandwidth="1" width="640" height="360"><BaseURL>v.mp4</BaseURL>`,
			`<SegmentList `+attrs+`>`+strings.Join(body, "")+`</SegmentList></Representation></AdaptationSet></Period>`)
	}
	// A single SegmentURL without timing spans the Period.
	p = loadMPD(t, list(``, `<SegmentURL mediaRange="100-199"/>`), Options{})
	equal(t, "single", segStrs(p.Video), []string{"/d/v.mp4[100:100] #1 @0+12000000 init=-1 epoch=0"})
	// SegmentURLs beyond the Period end are dropped.
	p = loadMPD(t, list(`duration="5"`, `<SegmentURL media="1"/>`, `<SegmentURL media="2"/>`, `<SegmentURL media="3"/>`, `<SegmentURL media="4"/>`), Options{})
	equal(t, "bounded", uris(p.Video), []string{"/d/1", "/d/2", "/d/3"})

	for _, tc := range []struct {
		doc  string
		want error
	}{
		{list(``, `<SegmentURL media="1"/>`, `<SegmentURL media="2"/>`), ErrInvalid}, // no timing
		{list(`duration="4"`, `<SegmentURL mediaRange="5-1"/>`), ErrInvalid},
		{list(`duration="4"`, `<SegmentURL mediaRange="abc"/>`), ErrInvalid},
		{list(`duration="4"`, `<Initialization range="1"/>`, `<SegmentURL media="1"/>`), ErrInvalid},
		{list(``, `<SegmentTimeline><S t="0" d="4"/></SegmentTimeline>`, `<SegmentURL media="1"/>`, `<SegmentURL media="2"/>`), ErrInvalid},
		{list(`xlink:href="http://x.example/list.xml" duration="4"`, `<SegmentURL media="1"/>`), ErrUnsupported},
	} {
		mpdErr(t, tc.doc, Options{}, tc.want)
	}
}

func TestDASHUnsupported(t *testing.T) {
	rep := func(inner string) string {
		return mpd(`type="static" mediaPresentationDuration="PT12S"`, `<Period><AdaptationSet mimeType="video/mp4">`,
			`<Representation id="v" bandwidth="1" width="640" height="360">`+inner+`</Representation></AdaptationSet></Period>`)
	}
	tmpl := `<SegmentTemplate duration="4" media="$Number$.m4s"/>`
	for name, doc := range map[string]string{
		"SegmentBase index range":            rep(`<BaseURL>v.mp4</BaseURL><SegmentBase indexRange="800-1200"><Initialization range="0-799"/></SegmentBase>`),
		"self-indexed single file":           rep(`<BaseURL>v.mp4</BaseURL>`),
		"SegmentTemplate without media":      rep(`<BaseURL>v.mp4</BaseURL><SegmentTemplate initialization="i.mp4"/>`),
		"dynamic":                            strings.Replace(rep(tmpl), `type="static"`, `type="dynamic"`, 1),
		"ContentProtection (Representation)": rep(tmpl + `<ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>`),
		"ContentProtection (AdaptationSet)": mpd(`type="static" mediaPresentationDuration="PT12S"`, `<Period>`,
			videoSet("v", `<ContentProtection schemeIdUri="urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"/>`), `</Period>`),
		"ContentProtection (MPD)": mpd(`type="static" mediaPresentationDuration="PT12S"`,
			`<ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011"/>`, `<Period>`, videoSet("v"), `</Period>`),
		"remote Period": mpd(`type="static" mediaPresentationDuration="PT12S"`, `<Period xlink:href="http://x.example/p.xml"/>`),
		"audio only":    mpd(`type="static" mediaPresentationDuration="PT12S"`, `<Period><AdaptationSet mimeType="audio/mp4">`+tmpl+`<Representation id="a" bandwidth="1"/></AdaptationSet></Period>`),
		"remote BaseURL": mpd(`type="static" mediaPresentationDuration="PT12S"`, `<BaseURL>https://cdn.example/x/</BaseURL>`,
			`<Period>`, videoSet("v"), `</Period>`),
	} {
		t.Run(name, func(t *testing.T) { mpdErr(t, doc, Options{}, ErrUnsupported) })
	}
	// SegmentBase at a higher level does not poison a SegmentTemplate below it,
	// and a missing @type means static.
	p := loadMPD(t, mpd(`mediaPresentationDuration="PT8S"`, `<Period><SegmentBase timescale="1"/>`, videoSet("v"), `</Period>`), Options{})
	equal(t, "segments", len(p.Video.Segments), 1)

	// An encrypted audio track fails too, unless audio is not wanted.
	enc := mpd(`type="static" mediaPresentationDuration="PT8S"`, `<Period>`, videoSet("v"),
		`<AdaptationSet mimeType="audio/mp4"><ContentProtection schemeIdUri="urn:x"/>`+tmpl+`<Representation id="a" bandwidth="1"/></AdaptationSet>`, `</Period>`)
	mpdErr(t, enc, Options{}, ErrUnsupported)
	loadMPD(t, enc, Options{Audio: "none"})
}

func TestDASHInvalid(t *testing.T) {
	for name, doc := range map[string]string{
		"no Period":              mpd(`type="static"`),
		"bad MPD duration":       mpd(`type="static" mediaPresentationDuration="12 seconds"`, `<Period>`, videoSet("v"), `</Period>`),
		"negative MPD duration":  mpd(`type="static" mediaPresentationDuration="-PT5S"`, `<Period>`, videoSet("v"), `</Period>`),
		"bad Period start":       mpd(`type="static"`, `<Period start="soon" duration="PT8S">`, videoSet("v"), `</Period>`),
		"bad Period duration":    mpd(`type="static"`, `<Period duration="PT">`, videoSet("v"), `</Period>`),
		"unknown Period start":   mpd(`type="static" mediaPresentationDuration="PT20S"`, `<Period>`, videoSet("v"), `</Period>`, `<Period>`, videoSet("v"), `</Period>`),
		"Periods out of order":   mpd(`type="static"`, `<Period start="PT10S" duration="PT5S">`, videoSet("v"), `</Period>`, `<Period start="PT5S" duration="PT5S">`, videoSet("v"), `</Period>`),
		"unknown Period end":     mpd(`type="static"`, `<Period>`, videoSet("v"), `</Period>`),
		"non-integer attribute":  mpd(`type="static" mediaPresentationDuration="PT8S"`, `<Period><AdaptationSet mimeType="video/mp4"><SegmentTemplate duration="4.5" media="$Number$.m4s"/><Representation id="v" bandwidth="1"/></AdaptationSet></Period>`),
		"truncated XML":          `<?xml version="1.0"?><MPD type="static"><Period><AdaptationSet>`,
		"MPD ends before Period": mpd(`type="static" mediaPresentationDuration="PT5S"`, `<Period start="PT10S">`, videoSet("v"), `</Period>`),
	} {
		t.Run(name, func(t *testing.T) { mpdErr(t, doc, Options{}, ErrInvalid) })
	}
}

func TestDASHPeriodTiming(t *testing.T) {
	doc := mpd(`type="static" mediaPresentationDuration="PT1M"`,
		`<Period start="PT5S" duration="PT10S">`, videoSet("v"), `</Period>`, // explicit start and duration
		`<Period>`, videoSet("v"), `</Period>`, // start = previous start + duration; end = next start
		`<Period id="third" start="PT40S">`, videoSet("v"), `</Period>`, // end = mediaPresentationDuration
	)
	p := loadMPD(t, doc, Options{})
	equal(t, "epochs", p.Epochs, []Epoch{
		{ID: "0", StartUS: 5_000_000, EndUS: 15_000_000},
		{ID: "1", StartUS: 15_000_000, EndUS: 40_000_000},
		{ID: "third", StartUS: 40_000_000, EndUS: 60_000_000},
	})
	// All Periods are preserved, in order, each exactly once.
	equal(t, "segments", segStrs(p.Video), []string{
		"/d/v/1.m4s[0:-1] #1 @5000000+10000000 init=0 epoch=0",
		"/d/v/1.m4s[0:-1] #1 @15000000+10000000 init=0 epoch=1",
		"/d/v/2.m4s[0:-1] #2 @25000000+10000000 init=0 epoch=1",
		"/d/v/3.m4s[0:-1] #3 @35000000+10000000 init=0 epoch=1",
		"/d/v/1.m4s[0:-1] #1 @40000000+10000000 init=0 epoch=2",
		"/d/v/2.m4s[0:-1] #2 @50000000+10000000 init=0 epoch=2",
	})
	equal(t, "inits", resStrs(p.Video.Inits), []string{"/d/v/init.mp4[0:-1]"}) // shared init interned once
	equal(t, "track epochs", len(p.Video.Epochs), 3)
}

func TestDASHBaseURLInheritance(t *testing.T) {
	set := func(asBase, repBase string) string {
		return `<AdaptationSet mimeType="video/mp4">` + asBase +
			`<SegmentTemplate duration="10" media="s$Number$.m4s" initialization="init.mp4"/>` +
			`<Representation id="v" bandwidth="1" width="640" height="360">` + repBase + `</Representation></AdaptationSet>`
	}
	for _, tc := range []struct {
		name, mpdBase, periodBase, asBase, repBase string
		want                                       string
	}{
		{"none", "", "", "", "", "/d/s1.m4s"},
		{"all four levels chain", "<BaseURL>a/</BaseURL>", "<BaseURL>b/</BaseURL>", "<BaseURL>c/</BaseURL>", "<BaseURL>d/</BaseURL>", "/d/a/b/c/d/s1.m4s"},
		{"parent traversal", "<BaseURL>a/b/</BaseURL>", "<BaseURL>../x/</BaseURL>", "", "<BaseURL>./y/</BaseURL>", "/d/a/x/y/s1.m4s"},
		{"absolute path resets", "<BaseURL>a/</BaseURL>", "<BaseURL>/abs/</BaseURL>", "<BaseURL>c/</BaseURL>", "", "/abs/c/s1.m4s"},
		{"file-like BaseURL resolves against its directory", "<BaseURL>a/</BaseURL>", "", "<BaseURL>stream.mp4</BaseURL>", "", "/d/a/s1.m4s"},
		{"first alternative wins", "<BaseURL> a/ </BaseURL><BaseURL>ignored/</BaseURL>", "", "", "", "/d/a/s1.m4s"},
		{"percent-encoded and file URI", "<BaseURL>file:///mnt/my%20media/</BaseURL>", "<BaseURL>p%231/</BaseURL>", "", "", "/mnt/my media/p#1/s1.m4s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := loadMPD(t, mpd(`type="static" mediaPresentationDuration="PT10S"`, tc.mpdBase, `<Period>`, tc.periodBase, set(tc.asBase, tc.repBase), `</Period>`), Options{})
			equal(t, "segment", uris(p.Video), []string{tc.want})
			equal(t, "init", p.Video.Inits[0].URI, strings.Replace(tc.want, "s1.m4s", "init.mp4", 1))
		})
	}

	// Remote BaseURLs resolve by URI rules when allowed.
	doc := mpd(`type="static" mediaPresentationDuration="PT10S"`, `<BaseURL>https://cdn.example/x/y/</BaseURL>`, `<Period><BaseURL>../z/</BaseURL>`, set("", ""), `</Period>`)
	p := mustLoad(t, map[string]string{"/d/manifest.mpd": doc}, "/d/manifest.mpd", Options{AllowRemote: true})
	equal(t, "remote", uris(p.Video), []string{"https://cdn.example/x/z/s1.m4s"})
}

func TestDASHTemplateInheritance(t *testing.T) {
	// Period-level template supplies defaults; AdaptationSet overrides the
	// timeline and media; Representation overrides only startNumber.
	doc := mpd(`type="static"`,
		`<Period duration="PT20S">`,
		`<SegmentTemplate timescale="1000" duration="5000" startNumber="1" presentationTimeOffset="1000" initialization="$RepresentationID$.init" media="period-$RepresentationID$-$Number$.m4s"/>`,
		`<AdaptationSet mimeType="video/mp4">`,
		` <SegmentTemplate media="as-$RepresentationID$-$Number$-$Time$.m4s"><SegmentTimeline><S t="1000" d="10000" r="1"/></SegmentTimeline></SegmentTemplate>`,
		` <Representation id="v" bandwidth="1" width="640" height="360"><SegmentTemplate startNumber="40"/></Representation>`,
		`</AdaptationSet>`,
		`<AdaptationSet mimeType="audio/mp4">`,
		` <Representation id="a" bandwidth="1"/>`,
		`</AdaptationSet>`,
		`</Period>`)
	p := loadMPD(t, doc, Options{})
	equal(t, "video", segStrs(p.Video), []string{
		"/d/as-v-40-1000.m4s[0:-1] #40 @0+10000000 init=0 epoch=0",
		"/d/as-v-41-11000.m4s[0:-1] #41 @10000000+10000000 init=0 epoch=0",
	})
	equal(t, "video init", resStrs(p.Video.Inits), []string{"/d/v.init[0:-1]"})
	equal(t, "video epoch", p.Video.Epochs[0], TrackEpoch{Available: true, RepresentationID: "v", Timescale: 1000, PresentationTimeOffset: 1000})
	// Audio inherits everything from the Period, including the fixed duration.
	equal(t, "audio", uris(p.Audio), []string{"/d/period-a-1.m4s", "/d/period-a-2.m4s", "/d/period-a-3.m4s", "/d/period-a-4.m4s"})
	equal(t, "audio init", resStrs(p.Audio.Inits), []string{"/d/a.init[0:-1]"})

	// A lower-level @duration replaces an inherited SegmentTimeline.
	doc = mpd(`type="static"`, `<Period duration="PT20S"><AdaptationSet mimeType="video/mp4">`,
		`<SegmentTemplate timescale="1000" media="$Number$.m4s"><SegmentTimeline><S t="0" d="20000"/></SegmentTimeline></SegmentTemplate>`,
		`<Representation id="v" bandwidth="1" width="640" height="360"><SegmentTemplate duration="5000"/></Representation>`,
		`</AdaptationSet></Period>`)
	equal(t, "count", len(loadMPD(t, doc, Options{}).Video.Segments), 4)
}

// crossPeriod builds an MPD whose Periods list the given video and audio
// Representations: "id:WxH:codecs" and "id:lang:codecs".
func crossPeriod(periods ...[2][]string) string {
	var sb strings.Builder
	for i, p := range periods {
		fmt.Fprintf(&sb, `<Period id="P%d" duration="PT10S">`, i)
		if len(p[0]) > 0 {
			sb.WriteString(`<AdaptationSet contentType="video"><SegmentTemplate duration="10" media="$RepresentationID$-$Number$.m4s"/>`)
			for j, v := range p[0] {
				f := strings.Split(v, ":")
				wh := strings.Split(f[1], "x")
				fmt.Fprintf(&sb, `<Representation id="%s" width="%s" height="%s" codecs="%s" bandwidth="%d"/>`, f[0], wh[0], wh[1], f[2], 1000*(j+1))
			}
			sb.WriteString(`</AdaptationSet>`)
		}
		for _, a := range p[1] {
			f := strings.Split(a, ":")
			fmt.Fprintf(&sb, `<AdaptationSet contentType="audio" lang="%s"><SegmentTemplate duration="10" media="$RepresentationID$-$Number$.m4s"/>`+
				`<Representation id="%s" codecs="%s" bandwidth="64000"/></AdaptationSet>`, f[1], f[0], f[2])
		}
		sb.WriteString(`</Period>`)
	}
	return mpd(`type="static"`, sb.String())
}

func repIDs(tr *Track) []string {
	out := make([]string, len(tr.Epochs))
	for i, e := range tr.Epochs {
		out[i] = e.RepresentationID
		if !e.Available {
			out[i] = "-"
		}
	}
	return out
}

func TestDASHCrossPeriodTrackContinuity(t *testing.T) {
	doc := crossPeriod(
		[2][]string{{"v720:1280x720:avc1.64001f", "v1080:1920x1080:avc1.640028"}, {"en:en:mp4a.40.2", "es:es:mp4a.40.2"}},
		// Same properties under different ids: continues silently (a note, no warning).
		[2][]string{{"ad1080:1920x1080:avc1.640028", "ad720:1280x720:avc1.64001f"}, {"ad-es:es:mp4a.40.2", "ad-en:en:mp4a.40.2"}},
		// Nothing compatible: video is remapped with a warning; audio is absent.
		[2][]string{{"c480:854x480:avc1.4d401e", "c360:640x360:avc1.4d401e"}, nil},
		// Same ids as the first Period, listed in another order; audio only in another language/codec.
		[2][]string{{"v1080:1920x1080:avc1.640028", "v720:1280x720:avc1.64001f"}, {"fr:fr:ac-3"}},
		// A Period without video.
		[2][]string{nil, {"en:en:mp4a.40.2"}},
	)
	p := loadMPD(t, doc, Options{})
	equal(t, "video per Period", repIDs(p.Video), []string{"v1080", "ad1080", "c480", "v1080", "-"})
	equal(t, "audio per Period", repIDs(p.Audio), []string{"en", "ad-en", "-", "fr", "en"})
	equal(t, "track ids", []string{p.Video.ID, p.Audio.ID}, []string{"v1080", "en"})
	equal(t, "epoch count", len(p.Epochs), 5)

	// Every selected Period contributes exactly its own segments, in order.
	equal(t, "video segments", segStrs(p.Video), []string{
		"/d/v1080-1.m4s[0:-1] #1 @0+10000000 init=-1 epoch=0",
		"/d/ad1080-1.m4s[0:-1] #1 @10000000+10000000 init=-1 epoch=1",
		"/d/c480-1.m4s[0:-1] #1 @20000000+10000000 init=-1 epoch=2",
		"/d/v1080-1.m4s[0:-1] #1 @30000000+10000000 init=-1 epoch=3",
	})
	equal(t, "audio segments", uris(p.Audio), []string{"/d/en-1.m4s", "/d/ad-en-1.m4s", "/d/fr-1.m4s", "/d/en-1.m4s"})

	wantWarnings := []string{
		`Period "P2": no video Representation compatible with "v1080"`,
		`Period "P4" has no video Representation`,
		`Period "P2" has no audio Representation`,
		`Period "P3": no audio Representation compatible with "en"`,
	}
	if len(p.Warnings) != len(wantWarnings) {
		t.Fatalf("warnings = %q, want %d", p.Warnings, len(wantWarnings))
	}
	for i, w := range wantWarnings {
		if !strings.Contains(p.Warnings[i], w) {
			t.Errorf("warning %d = %q, want it to contain %q", i, p.Warnings[i], w)
		}
	}
	if !strings.Contains(p.Warnings[0], `remapped to "c480"`) || !strings.Contains(p.Warnings[3], `remapped to "fr"`) {
		t.Errorf("remapping warnings do not name the new Representation: %q", p.Warnings)
	}
	if !containsText(p.Selection.Notes, `"ad1080" continues logical track "v1080"`) || !containsText(p.Selection.Notes, `"ad-en" continues logical track "en"`) {
		t.Errorf("notes %q do not report the compatible continuation", p.Selection.Notes)
	}

	// Explicit rules: applied in the first Period, then compatibility, then
	// the rule again; an audio rule that matches nothing leaves the Period
	// without audio rather than switching language.
	p = loadMPD(t, doc, Options{Video: "id=v720", Audio: "lang=es"})
	equal(t, "video per Period", repIDs(p.Video), []string{"v720", "ad720", "c480", "v720", "-"})
	equal(t, "audio per Period", repIDs(p.Audio), []string{"es", "ad-es", "-", "-", "-"})
	p = loadMPD(t, doc, Options{Video: "index=1", Audio: "none"})
	equal(t, "video per Period", repIDs(p.Video), []string{"v1080", "ad1080", "c360", "v1080", "-"})
	if p.Audio != nil {
		t.Error("audio=none produced an audio track")
	}

	// The first Period with candidates establishes the track.
	doc = crossPeriod(
		[2][]string{{"v:1280x720:avc1"}, nil},
		[2][]string{{"v:1280x720:avc1"}, {"a:en:mp4a"}},
	)
	p = loadMPD(t, doc, Options{})
	equal(t, "audio per Period", repIDs(p.Audio), []string{"-", "a"})
	equal(t, "audio epoch of first segment", p.Audio.Segments[0].Epoch, 1)
	equal(t, "audio start", p.Audio.Segments[0].StartUS, int64(10_000_000))
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], `"P0" has no audio`) {
		t.Errorf("warnings = %q", p.Warnings)
	}

	// No audio anywhere: no track, no warnings; an explicit rule fails.
	doc = crossPeriod([2][]string{{"v:1280x720:avc1"}, nil})
	p = loadMPD(t, doc, Options{})
	if p.Audio != nil || p.AudioMuxed || len(p.Warnings) != 0 {
		t.Errorf("video-only MPD: Audio=%v AudioMuxed=%v warnings=%q", p.Audio, p.AudioMuxed, p.Warnings)
	}
	mpdErr(t, doc, Options{Audio: "lang=en"}, ErrSelection)
}

func TestDASHAudioSelection(t *testing.T) {
	audio := func(attrs, label, reps string) string {
		return `<AdaptationSet contentType="audio" ` + attrs + `>` + label +
			`<SegmentTemplate duration="10" media="$RepresentationID$-$Number$.m4s"/>` + reps + `</AdaptationSet>`
	}
	doc := mpd(`type="static" mediaPresentationDuration="PT10S"`, `<Period>`, videoSet("v"),
		audio(`id="10" lang="en"`, `<Label>English</Label>`,
			`<Representation id="en-64" bandwidth="64000" codecs="mp4a.40.5"/><Representation id="en-192" bandwidth="192000" codecs="mp4a.40.2"/><Representation id="en-192b" bandwidth="192000" codecs="mp4a.40.2"/>`),
		audio(`id="20" lang="es-419"`, `<Label>Español</Label>`,
			`<Representation id="es-128" bandwidth="128000" codecs="mp4a.40.2"/><Representation id="es-256" bandwidth="256000" codecs="ec-3"/>`),
		`</Period>`)
	for rule, want := range map[string]string{
		"":             "en-192", // first AdaptationSet, highest bandwidth, first on ties
		"default":      "en-192",
		"lang=es":      "es-256",
		"lang=es-419":  "es-256",
		"lang=en":      "en-192",
		"name=Español": "es-256",
		"id=es-128":    "es-128",
		"id=20":        "es-256", // AdaptationSet@id
		"id=en-64":     "en-64",
	} {
		p := loadMPD(t, doc, Options{Audio: rule})
		if p.Audio == nil || p.Audio.ID != want {
			t.Errorf("audio rule %q selected %+v, want %q", rule, p.Audio, want)
		}
	}
	p := loadMPD(t, doc, Options{Audio: "lang=es"})
	equal(t, "audio track", []any{p.Audio.Name, p.Audio.Language, p.Audio.Codecs, p.Audio.Bandwidth}, []any{"Español", "es-419", "ec-3", int64(256000)})
	equal(t, "rule", p.Selection.AudioRule, "lang=es")
}

func TestDASHTrickModeAndContentTypes(t *testing.T) {
	doc := mpd(`type="static" mediaPresentationDuration="PT10S"`, `<Period>`,
		// Trick-mode set with the highest resolution must never be selected.
		`<AdaptationSet mimeType="video/mp4"><EssentialProperty schemeIdUri="http://dashif.org/guidelines/trickmode" value="1"/>`,
		`<SegmentTemplate duration="10" media="trick-$Number$.m4s"/><Representation id="trick" bandwidth="9000000" width="3840" height="2160"/></AdaptationSet>`,
		// Image/thumbnail and text sets are not video.
		`<AdaptationSet mimeType="image/jpeg" contentType="image"><SegmentTemplate duration="10" media="thumb-$Number$.jpg"/><Representation id="thumbs" bandwidth="1" width="4096" height="4096"/></AdaptationSet>`,
		`<AdaptationSet mimeType="application/mp4" codecs="stpp"><SegmentTemplate duration="10" media="text-$Number$.m4s"/><Representation id="text" bandwidth="1"/></AdaptationSet>`,
		// Type known only from the Representation mimeType.
		`<AdaptationSet><SegmentTemplate duration="10" media="$RepresentationID$-$Number$.m4s"/>`,
		`<Representation id="real" mimeType="video/mp4" bandwidth="100" width="640" height="360"/></AdaptationSet>`,
		// Type known only from ContentComponent / codecs.
		`<AdaptationSet><ContentComponent contentType="audio"/><SegmentTemplate duration="10" media="$RepresentationID$-$Number$.m4s"/><Representation id="snd" bandwidth="100"/></AdaptationSet>`,
		`<AdaptationSet codecs="mp4a.40.2"><SegmentTemplate duration="10" media="$RepresentationID$-$Number$.m4s"/><Representation id="snd2" bandwidth="900"/></AdaptationSet>`,
		`</Period>`)
	p := loadMPD(t, doc, Options{})
	equal(t, "video", p.Video.ID, "real")
	equal(t, "audio", p.Audio.ID, "snd")
	equal(t, "candidates", len(p.Selection.VideoCandidates), 1)
	if !containsText(p.Selection.Notes, "trick-mode") {
		t.Errorf("notes %q do not mention the trick-mode set", p.Selection.Notes)
	}
	mpdErr(t, doc, Options{Video: "id=trick"}, ErrSelection)
	mpdErr(t, doc, Options{Video: "id=thumbs"}, ErrSelection)
}

func TestDASHEventMarkers(t *testing.T) {
	doc := mpd(`type="static"`,
		`<Period id="a" duration="PT30S">`,
		`<EventStream schemeIdUri="urn:scte:scte35:2013:xml" value="1" timescale="90000" presentationTimeOffset="900000">`,
		`<Event presentationTime="1350000" duration="2700000" id="7"><Signal xmlns="http://www.scte.org/schemas/35/2016"><Binary>/DAl</Binary></Signal></Event>`,
		`</EventStream>`,
		videoSet("v", `<InbandEventStream schemeIdUri="urn:scte:scte35:2013:bin" value="x"/>`),
		`</Period>`,
		`<Period id="b" duration="PT30S">`,
		`<EventStream schemeIdUri="urn:example:other"><Event presentationTime="12"/><Event/></EventStream>`,
		videoSet("v"),
		`</Period>`)
	p := loadMPD(t, doc, Options{})
	equal(t, "markers", p.Markers, []Marker{
		{Kind: "InbandEventStream", TimeUS: -1, Detail: "period=a representation=v schemeIdUri=urn:scte:scte35:2013:bin value=x"},
		{Kind: "EventStream", TimeUS: 5_000_000, Detail: "period=a schemeIdUri=urn:scte:scte35:2013:xml value=1 id=7 duration=2700000/90000"},
		{Kind: "EventStream", TimeUS: 42_000_000, Detail: "period=b schemeIdUri=urn:example:other"},
		{Kind: "EventStream", TimeUS: 30_000_000, Detail: "period=b schemeIdUri=urn:example:other"},
	})
	// Markers are diagnostics: they do not alter the resource model.
	equal(t, "segments", len(p.Video.Segments), 6)
	equal(t, "warnings", len(p.Warnings), 0)
}

func TestDASHSegmentLimit(t *testing.T) {
	doc := mpd(`type="static"`, `<Period duration="PT1H"><AdaptationSet mimeType="video/mp4">`,
		`<SegmentTemplate duration="1" media="$Number$.m4s"/><Representation id="v" bandwidth="1" width="640" height="360"/></AdaptationSet>`,
		`<AdaptationSet mimeType="audio/mp4"><SegmentTemplate timescale="1000"  media="$Time$.m4s"><SegmentTimeline><S t="0" d="1000" r="-1"/></SegmentTimeline></SegmentTemplate>`,
		`<Representation id="a" bandwidth="1"/></AdaptationSet></Period>`)
	p := loadMPD(t, doc, Options{MaxSegments: 7200})
	equal(t, "counts", []int{len(p.Video.Segments), len(p.Audio.Segments)}, []int{3600, 3600})
	mpdErr(t, doc, Options{MaxSegments: 7199}, ErrResourceLimit)
	mpdErr(t, doc, Options{MaxSegments: 100}, ErrResourceLimit)
	loadMPD(t, doc, Options{MaxSegments: 3600, Audio: "none"})

	// A pathological repeat count is bounded by the limit, not by memory.
	huge := mpd(`type="static"`, `<Period duration="P300D"><AdaptationSet mimeType="video/mp4">`,
		`<SegmentTemplate timescale="1000000000" media="$Time$.m4s"><SegmentTimeline><S t="0" d="1" r="-1"/></SegmentTimeline></SegmentTemplate>`,
		`<Representation id="v" bandwidth="1" width="640" height="360"/></AdaptationSet></Period>`)
	mpdErr(t, huge, Options{}, ErrResourceLimit)
	huge = strings.Replace(huge, `<SegmentTimeline><S t="0" d="1" r="-1"/></SegmentTimeline>`, ``, 1)
	huge = strings.Replace(huge, `timescale="1000000000"`, `timescale="1000000000" duration="1"`, 1)
	mpdErr(t, huge, Options{}, ErrResourceLimit)
}

func TestDASHClosestBandwidthContinuation(t *testing.T) {
	period := func(id string, reps ...string) string {
		return `<Period id="` + id + `" duration="PT10S"><AdaptationSet contentType="video" width="1280" height="720" codecs="avc1.64001f">` +
			`<SegmentTemplate duration="10" media="$RepresentationID$-$Number$.m4s"/>` + strings.Join(reps, "") + `</AdaptationSet></Period>`
	}
	rep := func(id string, bw int) string { return fmt.Sprintf(`<Representation id="%s" bandwidth="%d"/>`, id, bw) }
	doc := mpd(`type="static"`,
		period("a", rep("main", 3_000_000)),
		// Same resolution and codecs under new ids: the closest bandwidth continues the track.
		period("b", rep("x1", 1_000_000), rep("x2", 3_500_000), rep("x3", 2_600_000), rep("x4", 9_000_000)),
		period("c", rep("y1", 2_500_000), rep("y2", 3_500_000)), // tie: first in manifest order
	)
	p := loadMPD(t, doc, Options{})
	equal(t, "video per Period", repIDs(p.Video), []string{"main", "x3", "y1"})
	equal(t, "warnings", len(p.Warnings), 0)
	equal(t, "inherited size", []int{p.Video.Width, p.Video.Height}, []int{1280, 720})
}

func TestDASHRepresentationOutsidePeriod(t *testing.T) {
	doc := mpd(`type="static"`, `<Period duration="PT5S"><AdaptationSet mimeType="video/mp4">`,
		`<SegmentTemplate timescale="1000" media="$Time$.m4s"><SegmentTimeline><S t="100000" d="1000" r="3"/></SegmentTimeline></SegmentTemplate>`,
		`<Representation id="v" bandwidth="1" width="640" height="360"><BaseURL>a/</BaseURL><BaseURL>b/</BaseURL></Representation></AdaptationSet></Period>`,
		`<Period duration="PT5S"><AdaptationSet mimeType="video/mp4">`,
		`<SegmentTemplate timescale="1000" media="$Time$.m4s"><SegmentTimeline><S t="0" d="5000"/></SegmentTimeline></SegmentTemplate>`,
		`<Representation id="v" bandwidth="1" width="640" height="360"><BaseURL>a/</BaseURL><BaseURL>b/</BaseURL></Representation></AdaptationSet></Period>`)
	p := loadMPD(t, doc, Options{})
	equal(t, "track epochs", repIDs(p.Video), []string{"-", "v"})
	equal(t, "segments", segStrs(p.Video), []string{"/d/a/0.m4s[0:-1] #1 @5000000+5000000 init=-1 epoch=1"})
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "no segments within the Period bounds") {
		t.Errorf("warnings = %q", p.Warnings)
	}
	if !containsText(p.Selection.Notes, "multiple BaseURL alternatives") {
		t.Errorf("notes %q do not mention BaseURL alternatives", p.Selection.Notes)
	}
}

func TestDASHCaptionDeclarations(t *testing.T) {
	for value, want := range map[string][]CaptionService{
		``:                            {{Name: "urn:scte:dash:cc:cea-708:2015"}},
		`eng`:                         {{Name: "urn:scte:dash:cc:cea-708:2015", Language: "eng"}},
		`1=lang:eng;2=lang:spa,war:1`: {{ID: "1", Name: "urn:scte:dash:cc:cea-708:2015", Language: "eng"}, {ID: "2", Name: "urn:scte:dash:cc:cea-708:2015", Language: "spa"}},
	} {
		doc := mpd(`type="static" mediaPresentationDuration="PT10S"`, `<Period>`,
			videoSet("v", `<Accessibility schemeIdUri="urn:scte:dash:cc:cea-708:2015" value="`+value+`"/><Accessibility schemeIdUri="urn:tva:metadata:cs:AudioPurposeCS:2007" value="2"/>`),
			`</Period>`)
		equal(t, "captions for "+value, loadMPD(t, doc, Options{}).Captions, want)
	}
}

func TestDASHNamespacePrefixes(t *testing.T) {
	doc := `<?xml version="1.0"?><!-- prefixed elements -->
<dash:MPD xmlns:dash="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT4S">
 <dash:Period><dash:AdaptationSet mimeType="video/mp4">
  <dash:SegmentTemplate duration="2" media="$Number$.m4s"/>
  <dash:Representation id="v" bandwidth="1" width="640" height="360"/>
 </dash:AdaptationSet></dash:Period>
</dash:MPD>`
	equal(t, "segments", uris(loadMPD(t, doc, Options{}).Video), []string{"/d/1.m4s", "/d/2.m4s"})
}
