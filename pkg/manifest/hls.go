// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// hlsVariant is one EXT-X-STREAM-INF entry of a Master Playlist.
type hlsVariant struct {
	rawURI        string
	name          string
	bandwidth     int64
	codecs        string
	width, height int
	frameRate     string
	audio         string // AUDIO group id
	cc            string // CLOSED-CAPTIONS group id or valueNone
}

// hlsRendition is one EXT-X-MEDIA entry of a Master Playlist.
type hlsRendition struct {
	typ, group, name, lang, uri, instream string
	def                                   bool
}

// hlsDoc is the single-pass parse result of one playlist of either kind.
type hlsDoc struct {
	uri string

	// Master Playlist content.
	variants      []hlsVariant
	renditions    []hlsRendition
	iframeStreams int

	// Media Playlist content.
	mediaTags   bool // any Media Playlist tag was seen
	endList     bool
	iframesOnly bool
	parts       int
	segs        []Segment // Epoch holds the playlist-local epoch index
	inits       initSet
	epochSeq    []int64 // discontinuity sequence number per local epoch
	epochStart  []int64 // cumulative EXTINF (µs) at the start of each local epoch
	totalUS     int64
	markers     []Marker
}

func (d *hlsDoc) master() bool {
	return len(d.variants) > 0 || len(d.renditions) > 0 || d.iframeStreams > 0
}

// attrIter iterates an HLS attribute list without allocating.
type attrIter struct{ s string }

// next returns the next attribute. Quoted values are returned unquoted.
func (it *attrIter) next() (key, val string, ok bool) {
	s := it.s
	for len(s) > 0 && (s[0] == ',' || s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	eq := strings.IndexByte(s, '=')
	if eq < 0 {
		it.s = ""
		return "", "", false
	}
	key = trimSpace(s[:eq])
	s = s[eq+1:]
	if len(s) > 0 && s[0] == '"' {
		if end := strings.IndexByte(s[1:], '"'); end < 0 {
			val, s = s[1:], ""
		} else {
			val, s = s[1:1+end], s[2+end:]
		}
	} else if end := strings.IndexByte(s, ','); end < 0 {
		val, s = trimSpace(s), ""
	} else {
		val, s = trimSpace(s[:end]), s[end:]
	}
	it.s = s
	return key, val, true
}

// parseHLSRange parses "<n>[@<o>]"; off is -1 when the offset is omitted.
func parseHLSRange(s string) (n, off int64, ok bool) {
	s = trimSpace(s)
	off = -1
	if i := strings.IndexByte(s, '@'); i >= 0 {
		if off, ok = parseUint(s[i+1:]); !ok {
			return 0, 0, false
		}
		s = s[:i]
	}
	n, ok = parseUint(s)
	if ok && off > math.MaxInt64-n {
		ok = false
	}
	return n, off, ok
}

// parseHLSTime parses an ISO-8601 date-time as used by
// EXT-X-PROGRAM-DATE-TIME and EXT-X-DATERANGE.
func parseHLSTime(s string) (time.Time, bool) {
	s = trimSpace(s)
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02T15:04:05.999999999Z0700", s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// parseHLS parses one playlist in a single pass. Segment URIs are resolved
// against uri; variant and rendition URIs are kept as written.
func (l *loader) parseHLS(uri, text string) (*hlsDoc, error) {
	d := &hlsDoc{uri: uri}
	base := l.newBase(uri)
	if n := strings.Count(text, "\n#EXTINF:"); n > 0 {
		d.segs = make([]Segment, 0, min(n, l.segBudget))
	}

	var (
		lineNo         int
		haveInf        bool
		infUS          int64
		haveRange      bool
		rangeLen       int64
		rangeOff       int64
		discs          int64 // EXT-X-DISCONTINUITY tags since the previous segment
		gap            bool
		pendingVariant bool
		curInit        Resource
		initIdx        = -1 // -1: no EXT-X-MAP in effect; -2: declared, not yet used
		mediaSeq       int64
		dseq           int64
		lastRangeURI   string // URI of the previous segment when it was a sub-range
		lastRangeEnd   int64

		pdtRaw       string // most recent EXT-X-PROGRAM-DATE-TIME and its position
		pdtUS        int64
		pdtTime      time.Time
		pdtState     int8 // 0 none, 1 unparsed, 2 parsed, -1 unparsable
		pendingDates []int
		pendingStart []time.Time
	)
	bad := func(format string, args ...any) error {
		return errf(ErrInvalid, "%s:%d: %s", uri, lineNo, fmt.Sprintf(format, args...))
	}
	anchor := func() bool {
		if pdtState == 1 {
			pdtState = -1
			if t, ok := parseHLSTime(pdtRaw); ok {
				pdtTime, pdtState = t, 2
			}
		}
		return pdtState == 2
	}
	dateToUS := func(t time.Time) int64 {
		if us := pdtUS + t.Sub(pdtTime).Microseconds(); us >= 0 {
			return us
		}
		return -1
	}

	for len(text) > 0 {
		var line string
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			line, text = text[:i], text[i+1:]
		} else {
			line, text = text, ""
		}
		lineNo++
		line = trimSpace(line)
		if line == "" {
			continue
		}

		if line[0] != '#' {
			if pendingVariant {
				d.variants[len(d.variants)-1].rawURI = line
				pendingVariant = false
				continue
			}
			if !haveInf {
				return nil, bad("URI %q is not preceded by EXTINF or EXT-X-STREAM-INF", truncate(line, 80))
			}
			if err := l.takeSegment(); err != nil {
				return nil, err
			}
			res, err := l.resolve(base, line)
			if err != nil {
				return nil, err
			}
			seg := Segment{
				Resource:   Resource{URI: res, Length: -1},
				Number:     mediaSeq + int64(len(d.segs)),
				StartUS:    d.totalUS,
				DurationUS: infUS,
				Gap:        gap,
			}
			if haveRange {
				off := rangeOff
				if off < 0 {
					// Implicit offset: continue after the previous
					// sub-range of the same resource, else start at 0.
					off = 0
					if lastRangeURI == res {
						off = lastRangeEnd
					}
				}
				if off > math.MaxInt64-rangeLen {
					return nil, bad("byte range overflows")
				}
				seg.Offset, seg.Length = off, rangeLen
				lastRangeURI, lastRangeEnd = res, off+rangeLen
			} else {
				lastRangeURI = ""
			}
			if len(d.segs) == 0 || discs > 0 {
				if len(d.segs) > 0 {
					dseq += discs
				}
				d.epochSeq = append(d.epochSeq, dseq)
				d.epochStart = append(d.epochStart, d.totalUS)
			}
			seg.Epoch = len(d.epochSeq) - 1
			if initIdx == -2 {
				initIdx = d.inits.intern(curInit)
			}
			seg.InitIndex = initIdx
			if infUS > math.MaxInt64-d.totalUS {
				return nil, bad("total duration overflows")
			}
			d.segs = append(d.segs, seg)
			d.totalUS += infUS
			haveInf, haveRange, gap, discs = false, false, false, 0
			continue
		}

		if len(line) < 4 || line[1] != 'E' || line[2] != 'X' || line[3] != 'T' {
			continue // comment
		}
		name, val := line, ""
		if i := strings.IndexByte(line, ':'); i >= 0 {
			name, val = line[:i], line[i+1:]
		}
		switch name {
		case "#EXTINF":
			dur := val
			if i := strings.IndexByte(val, ','); i >= 0 {
				dur = val[:i]
			}
			us, ok := parseDecimalMicros(trimSpace(dur))
			if !ok {
				return nil, bad("malformed EXTINF duration %q", truncate(dur, maxQuoted))
			}
			infUS, haveInf, d.mediaTags = us, true, true

		case "#EXT-X-BYTERANGE":
			n, off, ok := parseHLSRange(val)
			if !ok {
				return nil, bad("malformed EXT-X-BYTERANGE %q", truncate(val, maxQuoted))
			}
			rangeLen, rangeOff, haveRange, d.mediaTags = n, off, true, true

		case "#EXT-X-DISCONTINUITY":
			discs++
			d.mediaTags = true

		case "#EXT-X-GAP":
			gap, d.mediaTags = true, true

		case "#EXT-X-MAP":
			d.mediaTags = true
			var mapURI string
			hasURI := false
			init := Resource{Length: -1}
			it := attrIter{val}
			for {
				k, v, ok := it.next()
				if !ok {
					break
				}
				switch k {
				case attrURI:
					mapURI, hasURI = v, true
				case "BYTERANGE":
					n, off, ok := parseHLSRange(v)
					if !ok {
						return nil, bad("malformed EXT-X-MAP BYTERANGE %q", truncate(v, maxQuoted))
					}
					init.Offset, init.Length = max(off, 0), n
				}
			}
			if !hasURI || mapURI == "" {
				return nil, bad("EXT-X-MAP without URI")
			}
			res, err := l.resolve(base, mapURI)
			if err != nil {
				return nil, err
			}
			init.URI = res
			curInit, initIdx = init, -2

		case tagKey, "#EXT-X-SESSION-KEY":
			method := ""
			it := attrIter{val}
			for {
				k, v, ok := it.next()
				if !ok {
					break
				}
				if k == "METHOD" {
					method = v
				}
			}
			if method == "" {
				return nil, bad("%s without METHOD", name[1:])
			}
			if method != valueNone {
				return nil, errf(ErrUnsupported, "%s:%d: encrypted media (%s METHOD=%s)", uri, lineNo, name[1:], method)
			}
			if name == tagKey {
				d.mediaTags = true
			}

		case tagMediaSequence, "#EXT-X-DISCONTINUITY-SEQUENCE":
			n, ok := parseUint(trimSpace(val))
			if !ok {
				return nil, bad("malformed %s %q", name[1:], truncate(val, maxQuoted))
			}
			if len(d.segs) > 0 {
				return nil, bad("%s after the first Media Segment", name[1:])
			}
			if name == tagMediaSequence {
				mediaSeq = n
			} else {
				dseq = n
			}
			d.mediaTags = true

		case "#EXT-X-ENDLIST":
			d.endList, d.mediaTags = true, true

		case "#EXT-X-I-FRAMES-ONLY":
			d.iframesOnly, d.mediaTags = true, true

		case "#EXT-X-TARGETDURATION", "#EXT-X-PLAYLIST-TYPE", "#EXT-X-PART-INF",
			"#EXT-X-SERVER-CONTROL", "#EXT-X-PRELOAD-HINT", "#EXT-X-RENDITION-REPORT", "#EXT-X-BITRATE":
			d.mediaTags = true

		case "#EXT-X-PART":
			d.parts++ // partial segments are never used; only full segments count
			d.mediaTags = true

		case "#EXT-X-SKIP":
			return nil, errf(ErrUnsupported, "%s:%d: playlist delta update (EXT-X-SKIP)", uri, lineNo)

		case "#EXT-X-DEFINE":
			return nil, errf(ErrUnsupported, "%s:%d: playlist variable substitution (EXT-X-DEFINE)", uri, lineNo)

		case "#EXT-X-STREAM-INF":
			if pendingVariant {
				return nil, bad("EXT-X-STREAM-INF without a URI line")
			}
			var v hlsVariant
			it := attrIter{val}
			for {
				k, a, ok := it.next()
				if !ok {
					break
				}
				switch k {
				case "BANDWIDTH":
					v.bandwidth, _ = parseUint(a)
				case "CODECS":
					v.codecs = a
				case "RESOLUTION":
					if w, h, ok := strings.Cut(strings.ToLower(a), "x"); ok {
						wn, _ := parseUint(w)
						hn, _ := parseUint(h)
						if wn <= math.MaxInt32 && hn <= math.MaxInt32 {
							v.width, v.height = int(wn), int(hn)
						}
					}
				case "FRAME-RATE":
					v.frameRate = a
				case attrAudio:
					v.audio = a
				case attrCaptions:
					v.cc = a
				case attrName:
					v.name = a
				}
			}
			d.variants = append(d.variants, v)
			pendingVariant = true

		case "#EXT-X-I-FRAME-STREAM-INF":
			d.iframeStreams++ // never a full-video candidate

		case "#EXT-X-MEDIA":
			var r hlsRendition
			it := attrIter{val}
			for {
				k, a, ok := it.next()
				if !ok {
					break
				}
				switch k {
				case "TYPE":
					r.typ = a
				case "GROUP-ID":
					r.group = a
				case attrName:
					r.name = a
				case "LANGUAGE":
					r.lang = a
				case attrURI:
					r.uri = a
				case "INSTREAM-ID":
					r.instream = a
				case "DEFAULT":
					r.def = a == "YES"
				}
			}
			d.renditions = append(d.renditions, r)

		case "#EXT-X-PROGRAM-DATE-TIME":
			pdtRaw, pdtUS, pdtState = val, d.totalUS, 1
			if len(pendingDates) > 0 {
				if anchor() {
					for i, mi := range pendingDates {
						d.markers[mi].TimeUS = dateToUS(pendingStart[i])
					}
				}
				pendingDates, pendingStart = pendingDates[:0], pendingStart[:0]
			}

		case "#EXT-X-DATERANGE":
			m := Marker{Kind: "EXT-X-DATERANGE", TimeUS: -1, Detail: truncate(val, maxMarkerDetail)}
			var start time.Time
			haveStart := false
			it := attrIter{val}
			for {
				k, a, ok := it.next()
				if !ok {
					break
				}
				if k == "START-DATE" {
					start, haveStart = parseHLSTime(a)
				}
			}
			if haveStart {
				if pdtState != 0 {
					if anchor() {
						m.TimeUS = dateToUS(start)
					}
				} else {
					pendingDates = append(pendingDates, len(d.markers))
					pendingStart = append(pendingStart, start)
				}
			}
			d.markers = append(d.markers, m)

		default:
			// Ad-signalling tags (EXT-X-CUE-OUT, EXT-X-CUE-IN, EXT-X-SCTE35,
			// EXT-OATCLS-SCTE35, ...) are diagnostics at the playlist position.
			if strings.Contains(name, "CUE") || strings.Contains(name, "SCTE") {
				d.markers = append(d.markers, Marker{Kind: name[1:], TimeUS: d.totalUS, Detail: truncate(val, maxMarkerDetail)})
			}
		}
	}

	if pendingVariant {
		return nil, bad("EXT-X-STREAM-INF without a URI line")
	}
	if d.master() && (d.mediaTags || len(d.segs) > 0) {
		return nil, errf(ErrInvalid, "%s: playlist mixes Master Playlist and Media Playlist tags", uri)
	}
	return d, nil
}

// readHLS reads and parses a playlist referenced from a Master Playlist.
func (l *loader) readHLS(uri string) (*hlsDoc, error) {
	format, body, err := l.readManifest(uri)
	if err != nil {
		return nil, err
	}
	if format != FormatHLS {
		return nil, errf(ErrInvalid, "%s: expected an HLS Media Playlist", uri)
	}
	return l.parseHLS(uri, string(body))
}

// checkMedia verifies that d is a finite, complete, full-segment Media Playlist.
func checkMedia(d *hlsDoc) error {
	switch {
	case d.master():
		return errf(ErrInvalid, "%s: expected a Media Playlist, found a Master Playlist", d.uri)
	case d.iframesOnly:
		return errf(ErrUnsupported, "%s: I-frame-only playlist cannot be used as full video", d.uri)
	case !d.endList && !d.mediaTags:
		return errf(ErrInvalid, "%s: empty playlist", d.uri)
	case !d.endList:
		return errf(ErrUnsupported, "%s: growing or live playlist (no EXT-X-ENDLIST)", d.uri)
	case len(d.segs) == 0:
		return errf(ErrInvalid, "%s: Media Playlist has no Media Segments", d.uri)
	}
	return nil
}

func (l *loader) loadHLS(uri, text string) error {
	doc, err := l.parseHLS(uri, text)
	if err != nil {
		return err
	}
	sel := &l.p.Selection
	if !doc.master() {
		// Direct Media Playlist: one candidate, audio (if any) is muxed.
		cands := []videoCand{{id: soleVariantID, alt: []string{uri}}}
		sel.VideoCandidates = []string{cands[0].describe(0) + " uri=" + uri}
		if err := checkMedia(doc); err != nil {
			return err
		}
		if l.vrule.pick(cands, l.opts.MinHeight) < 0 {
			return errf(ErrSelection, "video rule %q matches nothing: %s is a single Media Playlist", l.vrule, uri)
		}
		if l.arule.explicit() {
			return errf(ErrSelection, "audio rule %q matches nothing: %s is a single Media Playlist without renditions", l.arule, uri)
		}
		l.p.AudioMuxed = l.arule.kind != audioNone
		l.assembleHLS(doc, &Track{ID: soleVariantID, ManifestURI: uri})
		return nil
	}

	base := l.newBase(uri)
	cands := make([]videoCand, 0, len(doc.variants))
	candVariant := make([]int, 0, len(doc.variants))
	audioOnly := 0
	for i := range doc.variants {
		v := &doc.variants[i]
		if _, allAudio := splitCodecs(v.codecs); allAudio && v.width == 0 && v.height == 0 {
			audioOnly++
			continue
		}
		resolved, _ := l.resolve(base, v.rawURI) // only the selected variant must resolve
		c := videoCand{
			id:        "v" + strconv.Itoa(len(cands)),
			alt:       []string{v.name, v.rawURI, resolved},
			width:     v.width,
			height:    v.height,
			bandwidth: v.bandwidth,
			codecs:    v.codecs,
			frameRate: v.frameRate,
			note:      "uri=" + v.rawURI,
		}
		if v.name != "" {
			c.note = RuleKeyName + ruleAssign + strconv.Quote(v.name) + " " + c.note
		}
		sel.VideoCandidates = append(sel.VideoCandidates, c.describe(len(cands)))
		cands = append(cands, c)
		candVariant = append(candVariant, i)
	}
	if doc.iframeStreams > 0 {
		l.notef("ignored %d I-frame-only playlist(s)", doc.iframeStreams)
	}
	if audioOnly > 0 {
		l.notef("ignored %d audio-only variant(s)", audioOnly)
	}
	if len(cands) == 0 {
		return errf(ErrUnsupported, "%s: Master Playlist has no full video variant", uri)
	}
	ci := l.vrule.pick(cands, l.opts.MinHeight)
	if ci < 0 {
		return errf(ErrSelection, "video rule %q matches none of the %d variant(s) of %s", l.vrule, len(cands), uri)
	}
	v := &doc.variants[candVariant[ci]]
	l.notef("selected video variant %s", cands[ci].describe(ci))

	videoURI, err := l.resolve(base, v.rawURI)
	if err != nil {
		return err
	}
	vdoc, err := l.readHLS(videoURI)
	if err != nil {
		return err
	}
	if err := checkMedia(vdoc); err != nil {
		return err
	}
	vt := &Track{
		ID:          cands[ci].id,
		Name:        v.name,
		Codecs:      v.codecs,
		Bandwidth:   v.bandwidth,
		Width:       v.width,
		Height:      v.height,
		FrameRate:   v.frameRate,
		ManifestURI: videoURI,
	}
	l.assembleHLS(vdoc, vt)

	// Caption services are informational only.
	if v.cc != valueNone {
		for i := range doc.renditions {
			r := &doc.renditions[i]
			if r.typ == attrCaptions && (v.cc == "" || r.group == v.cc) {
				l.p.Captions = append(l.p.Captions, CaptionService{ID: r.instream, Name: r.name, Language: r.lang})
			}
		}
	}

	return l.loadHLSAudio(doc, base, v, vdoc, videoURI)
}

// loadHLSAudio selects the variant's audio rendition and, when it is
// separately packaged, loads and aligns its Media Playlist.
func (l *loader) loadHLSAudio(doc *hlsDoc, base *refBase, v *hlsVariant, vdoc *hlsDoc, videoURI string) error {
	if l.arule.kind == audioNone {
		return nil
	}
	if v.audio == "" {
		if l.arule.explicit() {
			return errf(ErrSelection, "audio rule %q matches nothing: the selected variant has no AUDIO group", l.arule)
		}
		l.p.AudioMuxed = true
		l.notef("selected variant declares no AUDIO group; audio, if any, is muxed with video")
		return nil
	}
	var cands []audioCand
	var rends []*hlsRendition
	for i := range doc.renditions {
		r := &doc.renditions[i]
		if r.typ != attrAudio || r.group != v.audio {
			continue
		}
		cands = append(cands, audioCand{
			id:    r.group + pathSeparator + r.name,
			alt:   r.uri,
			name:  r.name,
			lang:  r.lang,
			group: len(cands),
			def:   r.def,
		})
		rends = append(rends, r)
	}
	if len(cands) == 0 {
		return errf(ErrInvalid, "%s: variant references AUDIO group %q, which has no renditions", doc.uri, v.audio)
	}
	ai := l.arule.pick(cands)
	if ai < 0 {
		return errf(ErrSelection, "audio rule %q matches none of the %d rendition(s) of AUDIO group %q", l.arule, len(cands), v.audio)
	}
	r := rends[ai]
	if r.uri == "" {
		l.p.AudioMuxed = true
		l.notef("selected audio rendition %q has no URI; audio is muxed with video", cands[ai].id)
		return nil
	}
	audioURI, err := l.resolve(base, r.uri)
	if err != nil {
		return err
	}
	if audioURI == videoURI {
		l.p.AudioMuxed = true
		l.notef("selected audio rendition %q shares the video playlist; audio is muxed with video", cands[ai].id)
		return nil
	}
	adoc, err := l.readHLS(audioURI)
	if err != nil {
		return err
	}
	if err := checkMedia(adoc); err != nil {
		return err
	}
	l.notef("selected audio rendition %q (language %q) uri=%s", cands[ai].id, r.lang, r.uri)

	// Align audio discontinuity epochs with the video playlist's epochs by
	// discontinuity sequence number.
	epochs := l.p.Epochs
	remap := make([]int, len(adoc.epochSeq))
	j := 0
	for i, seq := range adoc.epochSeq {
		for j < len(vdoc.epochSeq) && vdoc.epochSeq[j] < seq {
			j++
		}
		if j == len(vdoc.epochSeq) || vdoc.epochSeq[j] != seq {
			return errf(ErrInvalid, "%s: audio discontinuity sequence %d has no counterpart in the video playlist %s", audioURI, seq, videoURI)
		}
		remap[i] = j
	}
	audioCodecs, _ := splitCodecs(v.codecs)
	at := &Track{
		ID:          cands[ai].id,
		Name:        r.name,
		Language:    r.lang,
		Codecs:      audioCodecs,
		ManifestURI: audioURI,
		Inits:       adoc.inits.list,
		Segments:    adoc.segs,
		Epochs:      make([]TrackEpoch, len(epochs)),
	}
	for _, e := range remap {
		at.Epochs[e].Available = true
	}
	// Anchor each audio epoch at the video-declared epoch start so both
	// tracks share one presentation timeline.
	for i := range at.Segments {
		s := &at.Segments[i]
		local := s.Epoch
		s.Epoch = remap[local]
		s.StartUS += epochs[s.Epoch].StartUS - adoc.epochStart[local]
	}
	for i := range at.Epochs {
		if !at.Epochs[i].Available {
			l.warnf("audio rendition %q has no media in discontinuity epoch %s", at.ID, epochs[i].ID)
		}
	}
	if adoc.parts > 0 {
		l.notef("ignored %d partial segment(s) in %s", adoc.parts, audioURI)
	}
	l.p.Audio = at
	return nil
}

// assembleHLS installs the video playlist as the presentation's video track
// and derives the continuity epochs from it.
func (l *loader) assembleHLS(vdoc *hlsDoc, vt *Track) {
	n := len(vdoc.epochSeq)
	epochs := make([]Epoch, n)
	for i := range epochs {
		end := vdoc.totalUS
		if i+1 < n {
			end = vdoc.epochStart[i+1]
		}
		epochs[i] = Epoch{ID: strconv.FormatInt(vdoc.epochSeq[i], decimal), StartUS: vdoc.epochStart[i], EndUS: end}
	}
	vt.Inits = vdoc.inits.list
	vt.Segments = vdoc.segs
	vt.Epochs = make([]TrackEpoch, n)
	for i := range vt.Epochs {
		vt.Epochs[i].Available = true
	}
	if vdoc.parts > 0 {
		l.notef("ignored %d partial segment(s) in %s", vdoc.parts, vdoc.uri)
	}
	l.p.Epochs = epochs
	l.p.Video = vt
	l.p.Markers = vdoc.markers
}
