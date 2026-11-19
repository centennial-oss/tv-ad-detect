// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"encoding/xml"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const trickModeScheme = "http://dashif.org/guidelines/trickmode"

// ---------------------------------------------------------------------------
// MPD document model (encoding/xml)
// ---------------------------------------------------------------------------

// optInt is an optional integer attribute.
type optInt struct {
	v   int64
	set bool
}

// UnmarshalXMLAttr implements xml.UnmarshalerAttr.
func (o *optInt) UnmarshalXMLAttr(a xml.Attr) error {
	v, ok := parseInt(strings.TrimSpace(a.Value))
	if !ok {
		return fmt.Errorf("attribute %s=%q is not an integer", a.Name.Local, truncate(a.Value, maxQuoted))
	}
	o.v, o.set = v, true
	return nil
}

// optStr is an optional string attribute.
type optStr struct {
	v   string
	set bool
}

// UnmarshalXMLAttr implements xml.UnmarshalerAttr.
func (o *optStr) UnmarshalXMLAttr(a xml.Attr) error {
	o.v, o.set = a.Value, true
	return nil
}

type xMPD struct {
	XMLName    xml.Name
	Type       string        `xml:"type,attr"`
	Duration   optStr        `xml:"mediaPresentationDuration,attr"`
	BaseURLs   []string      `xml:"BaseURL"`
	Protection []xDescriptor `xml:"ContentProtection"`
	Periods    []xPeriod     `xml:"Period"`
}

type xPeriod struct {
	ID       optStr   `xml:"id,attr"`
	Start    optStr   `xml:"start,attr"`
	Duration optStr   `xml:"duration,attr"`
	Href     string   `xml:"href,attr"`
	BaseURLs []string `xml:"BaseURL"`
	xAddressing
	Sets         []xAdaptationSet `xml:"AdaptationSet"`
	EventStreams []xEventStream   `xml:"EventStream"`
}

// xAddressing is the segment addressing information allowed at the Period,
// AdaptationSet and Representation levels.
type xAddressing struct {
	Base     *xSegmentBase     `xml:"SegmentBase"`
	List     *xSegmentList     `xml:"SegmentList"`
	Template *xSegmentTemplate `xml:"SegmentTemplate"`
}

// xCommon holds the attributes and elements shared by AdaptationSet and
// Representation.
type xCommon struct {
	MimeType  string   `xml:"mimeType,attr"`
	Codecs    string   `xml:"codecs,attr"`
	Width     optInt   `xml:"width,attr"`
	Height    optInt   `xml:"height,attr"`
	FrameRate string   `xml:"frameRate,attr"`
	BaseURLs  []string `xml:"BaseURL"`
	xAddressing
	Protection   []xDescriptor `xml:"ContentProtection"`
	Essential    []xDescriptor `xml:"EssentialProperty"`
	Supplemental []xDescriptor `xml:"SupplementalProperty"`
	Inband       []xDescriptor `xml:"InbandEventStream"`
}

type xAdaptationSet struct {
	ID          optStr `xml:"id,attr"`
	ContentType string `xml:"contentType,attr"`
	Lang        string `xml:"lang,attr"`
	Label       string `xml:"Label"`
	xCommon
	Components    []xComponent      `xml:"ContentComponent"`
	Accessibility []xDescriptor     `xml:"Accessibility"`
	Reps          []xRepresentation `xml:"Representation"`
}

type xRepresentation struct {
	ID        string `xml:"id,attr"`
	Bandwidth optInt `xml:"bandwidth,attr"`
	xCommon
}

type xComponent struct {
	ContentType string `xml:"contentType,attr"`
}

type xDescriptor struct {
	Scheme string `xml:"schemeIdUri,attr"`
	Value  string `xml:"value,attr"`
}

type xSegmentBase struct{}

type xURL struct {
	SourceURL optStr `xml:"sourceURL,attr"`
	Range     optStr `xml:"range,attr"`
}

// xMultiBase holds what SegmentTemplate and SegmentList share.
type xMultiBase struct {
	Timescale   optInt     `xml:"timescale,attr"`
	PTO         optInt     `xml:"presentationTimeOffset,attr"`
	Duration    optInt     `xml:"duration,attr"`
	StartNumber optInt     `xml:"startNumber,attr"`
	EndNumber   optInt     `xml:"endNumber,attr"`
	InitElem    *xURL      `xml:"Initialization"`
	Timeline    *xTimeline `xml:"SegmentTimeline"`
}

type xSegmentTemplate struct {
	xMultiBase
	Media optStr `xml:"media,attr"`
	Init  optStr `xml:"initialization,attr"`
}

type xSegmentList struct {
	xMultiBase
	Href string        `xml:"href,attr"`
	URLs []xSegmentURL `xml:"SegmentURL"`
}

type xSegmentURL struct {
	media, mediaRange optStr
}

// UnmarshalXML decodes a SegmentURL without reflection (lists can be long).
func (u *xSegmentURL) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "media":
			u.media = optStr{a.Value, true}
		case "mediaRange":
			u.mediaRange = optStr{a.Value, true}
		}
	}
	return d.Skip()
}

type xS struct {
	t, d, r int64
	hasT    bool
}

type xTimeline struct {
	S []xS
}

// UnmarshalXML decodes a SegmentTimeline without reflection (timelines can
// hold one S element per segment).
func (t *xTimeline) UnmarshalXML(d *xml.Decoder, _ xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch el := tok.(type) {
		case xml.StartElement:
			if el.Name.Local == "S" {
				var s xS
				hasD := false
				for _, a := range el.Attr {
					var dst *int64
					switch a.Name.Local {
					case "t":
						dst, s.hasT = &s.t, true
					case "d":
						dst, hasD = &s.d, true
					case attrRepeat:
						dst = &s.r
					default:
						continue
					}
					v, ok := parseInt(strings.TrimSpace(a.Value))
					if !ok || (v < 0 && a.Name.Local != attrRepeat) {
						return fmt.Errorf("SegmentTimeline S@%s=%q is not a valid integer", a.Name.Local, truncate(a.Value, maxQuoted))
					}
					*dst = v
				}
				if !hasD || s.d <= 0 {
					return fmt.Errorf("SegmentTimeline S element needs a positive @d")
				}
				t.S = append(t.S, s)
			}
			if err := d.Skip(); err != nil {
				return err
			}
		case xml.EndElement:
			return nil
		}
	}
}

type xEventStream struct {
	Scheme    string   `xml:"schemeIdUri,attr"`
	Value     string   `xml:"value,attr"`
	Timescale optInt   `xml:"timescale,attr"`
	PTO       optInt   `xml:"presentationTimeOffset,attr"`
	Events    []xEvent `xml:"Event"`
}

type xEvent struct {
	ID       string `xml:"id,attr"`
	Time     optInt `xml:"presentationTime,attr"`
	Duration optInt `xml:"duration,attr"`
}

// ---------------------------------------------------------------------------
// Period and Representation model
// ---------------------------------------------------------------------------

type dashPeriod struct {
	x       *xPeriod
	id      string
	startUS int64
	durUS   int64 // -1 when @duration is absent
	endUS   int64 // -1 when unknown
	base    string
}

// dashRep is one audio or video Representation with inherited properties.
type dashRep struct {
	as            *xAdaptationSet
	rep           *xRepresentation
	group         int // AdaptationSet ordinal within the Period
	id            string
	codecs        string
	frameRate     string
	width, height int
	bandwidth     int64
	protected     bool
}

// dashTrackState accumulates one logical track across Periods.
type dashTrackState struct {
	tr    *Track
	inits initSet
	ref   dashRep // Representation that established the logical track
	have  bool
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func dimension(rep, as optInt) int {
	v := rep
	if !v.set {
		v = as
	}
	if !v.set || v.v < 0 || v.v > math.MaxInt32 {
		return 0
	}
	return int(v.v)
}

func hasScheme(ds []xDescriptor, scheme string) bool {
	for i := range ds {
		if strings.EqualFold(strings.TrimSpace(ds[i].Scheme), scheme) {
			return true
		}
	}
	return false
}

// repKind classifies a Representation as "video", "audio" or something else.
func repKind(as *xAdaptationSet, rep *xRepresentation) string {
	if ct := strings.ToLower(strings.TrimSpace(as.ContentType)); ct != "" {
		return ct
	}
	if mime := strings.TrimSpace(firstNonEmpty(rep.MimeType, as.MimeType)); mime != "" {
		major, _, _ := strings.Cut(strings.ToLower(mime), pathSeparator)
		return major
	}
	kind := ""
	for i := range as.Components {
		switch strings.ToLower(strings.TrimSpace(as.Components[i].ContentType)) {
		case kindVideo:
			return kindVideo
		case kindAudio:
			kind = kindAudio
		}
	}
	if kind != "" {
		return kind
	}
	if _, allAudio := splitCodecs(firstNonEmpty(rep.Codecs, as.Codecs)); allAudio {
		return kindAudio
	}
	if dimension(rep.Width, as.Width) > 0 || dimension(rep.Height, as.Height) > 0 {
		return kindVideo
	}
	return ""
}

// collectReps lists the Period's video and audio Representations in manifest
// order. Trick-mode AdaptationSets and Representations are never candidates.
func collectReps(xp *xPeriod) (video, audio []dashRep, trick int) {
	for ai := range xp.Sets {
		as := &xp.Sets[ai]
		if hasScheme(as.Essential, trickModeScheme) || hasScheme(as.Supplemental, trickModeScheme) {
			trick++
			continue
		}
		for ri := range as.Reps {
			rep := &as.Reps[ri]
			kind := repKind(as, rep)
			if kind != kindVideo && kind != kindAudio {
				continue
			}
			if hasScheme(rep.Essential, trickModeScheme) || hasScheme(rep.Supplemental, trickModeScheme) {
				trick++
				continue
			}
			r := dashRep{
				as:        as,
				rep:       rep,
				group:     ai,
				id:        rep.ID,
				codecs:    firstNonEmpty(rep.Codecs, as.Codecs),
				frameRate: firstNonEmpty(rep.FrameRate, as.FrameRate),
				width:     dimension(rep.Width, as.Width),
				height:    dimension(rep.Height, as.Height),
				bandwidth: max(rep.Bandwidth.v, 0),
				protected: len(as.Protection) > 0 || len(rep.Protection) > 0,
			}
			if kind == kindVideo {
				video = append(video, r)
			} else {
				audio = append(audio, r)
			}
		}
	}
	return video, audio, trick
}

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------

func (l *loader) loadDASH(uri string, data []byte) error {
	var mpd xMPD
	if err := xml.Unmarshal(data, &mpd); err != nil {
		return errf(ErrInvalid, "%s: %v", uri, err)
	}
	if strings.EqualFold(strings.TrimSpace(mpd.Type), "dynamic") {
		return errf(ErrUnsupported, "%s: dynamic (live) MPD", uri)
	}
	if len(mpd.Protection) > 0 {
		return errf(ErrUnsupported, "%s: encrypted media (ContentProtection)", uri)
	}
	n := len(mpd.Periods)
	if n == 0 {
		return errf(ErrInvalid, "%s: MPD has no Period", uri)
	}
	duration := func(what string, o optStr) (int64, error) {
		if !o.set {
			return -1, nil
		}
		us, ok := parseISODuration(o.v)
		if !ok || us < 0 {
			return 0, errf(ErrInvalid, "%s: malformed %s %q", uri, what, truncate(o.v, maxQuoted))
		}
		return us, nil
	}
	mpdDur, err := duration("MPD@mediaPresentationDuration", mpd.Duration)
	if err != nil {
		return err
	}
	rootBase, err := l.chainBase(uri, mpd.BaseURLs)
	if err != nil {
		return err
	}

	// Period timing: explicit @start, else previous start + duration, else 0
	// for the first Period.
	periods := make([]dashPeriod, n)
	for i := range periods {
		xp := &mpd.Periods[i]
		pi := &periods[i]
		pi.x = xp
		pi.id = strconv.Itoa(i)
		if xp.ID.set && xp.ID.v != "" {
			pi.id = xp.ID.v
		}
		if xp.Href != "" {
			return errf(ErrUnsupported, "%s: Period %q is a remote element (xlink:href)", uri, pi.id)
		}
		if pi.durUS, err = duration("Period@duration", xp.Duration); err != nil {
			return err
		}
		start, err := duration("Period@start", xp.Start)
		if err != nil {
			return err
		}
		switch {
		case start >= 0:
			pi.startUS = start
		case i == 0:
			pi.startUS = 0
		case periods[i-1].durUS >= 0:
			pi.startUS = periods[i-1].startUS + periods[i-1].durUS
		default:
			return errf(ErrInvalid, "%s: cannot determine the start of Period %q (no @start and the previous Period has no @duration)", uri, pi.id)
		}
		if i > 0 && pi.startUS < periods[i-1].startUS {
			return errf(ErrInvalid, "%s: Period %q starts before the previous Period", uri, pi.id)
		}
		if pi.base, err = l.chainBase(rootBase, xp.BaseURLs); err != nil {
			return err
		}
	}
	epochs := make([]Epoch, n)
	for i := range periods {
		pi := &periods[i]
		switch {
		case pi.durUS >= 0:
			if pi.durUS > math.MaxInt64-pi.startUS {
				return errf(ErrInvalid, "%s: Period %q end overflows", uri, pi.id)
			}
			pi.endUS = pi.startUS + pi.durUS
		case i+1 < n:
			pi.endUS = periods[i+1].startUS
		case mpdDur >= 0:
			pi.endUS = mpdDur
		default:
			pi.endUS = -1
		}
		if pi.endUS >= 0 && pi.endUS < pi.startUS {
			return errf(ErrInvalid, "%s: Period %q ends before it starts", uri, pi.id)
		}
		epochs[i] = Epoch{ID: pi.id, StartUS: pi.startUS, EndUS: pi.endUS}
	}
	l.p.Epochs = epochs

	video := &dashTrackState{tr: &Track{ManifestURI: uri, Epochs: make([]TrackEpoch, n)}}
	audio := &dashTrackState{tr: &Track{ManifestURI: uri, Epochs: make([]TrackEpoch, n)}}
	var audioWarnings []string
	trick := 0

	for i := range periods {
		pi := &periods[i]
		vc, ac, t := collectReps(pi.x)
		trick += t

		// Video.
		vi, err := l.dashPickVideo(video, pi, vc)
		if err != nil {
			return err
		}
		if vi < 0 {
			l.warnf("Period %q has no video Representation; video is unavailable there", pi.id)
		} else {
			c := &vc[vi]
			if !video.have {
				video.have, video.ref = true, *c
				fillDashTrack(video.tr, c)
				l.dashCaptions(c.as)
				l.notef("selected video Representation %q in Period %q", c.id, pi.id)
			}
			if video.tr.Epochs[i], err = l.dashBuild(video, i, pi, c, kindVideo); err != nil {
				return err
			}
		}

		// Audio.
		if l.arule.kind != audioNone {
			ai, warn, err := l.dashPickAudio(audio, pi, ac)
			if err != nil {
				return err
			}
			if warn != "" {
				audioWarnings = append(audioWarnings, warn)
			}
			if ai >= 0 {
				c := &ac[ai]
				if !audio.have {
					audio.have, audio.ref = true, *c
					fillDashTrack(audio.tr, c)
					l.notef("selected audio Representation %q in Period %q", c.id, pi.id)
				}
				if audio.tr.Epochs[i], err = l.dashBuild(audio, i, pi, c, kindAudio); err != nil {
					return err
				}
			}
		}

		l.dashEvents(pi)
	}

	if trick > 0 {
		l.notef("ignored %d trick-mode AdaptationSet(s)/Representation(s)", trick)
	}
	if !video.have {
		return errf(ErrUnsupported, "%s: MPD has no video Representation", uri)
	}
	video.tr.Inits = video.inits.list
	l.p.Video = video.tr
	switch {
	case audio.have:
		audio.tr.Inits = audio.inits.list
		l.p.Audio = audio.tr
		l.p.Warnings = append(l.p.Warnings, audioWarnings...)
	case l.arule.explicit():
		return errf(ErrSelection, "audio rule %q matches no audio Representation in %s", l.arule, uri)
	case l.arule.kind != audioNone:
		l.notef("MPD has no audio AdaptationSet")
	}
	return nil
}

// chainBase applies one level of BaseURL inheritance.
func (l *loader) chainBase(base string, urls []string) (string, error) {
	if len(urls) == 0 {
		return base, nil
	}
	ref := strings.TrimSpace(urls[0])
	if ref == "" {
		return base, nil
	}
	return l.resolve(l.newBase(base), ref)
}

func fillDashTrack(tr *Track, c *dashRep) {
	tr.ID = c.id
	tr.Name = strings.TrimSpace(c.as.Label)
	tr.Language = c.as.Lang
	tr.Codecs = c.codecs
	tr.Bandwidth = c.bandwidth
	tr.Width = c.width
	tr.Height = c.height
	tr.FrameRate = c.frameRate
}

func absDiff(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

// dashPickVideo selects the Period's video Representation, or -1 when the
// Period has none.
func (l *loader) dashPickVideo(st *dashTrackState, pi *dashPeriod, cands []dashRep) (int, error) {
	if len(cands) == 0 {
		return -1, nil
	}
	vc := make([]videoCand, len(cands))
	for i := range cands {
		c := &cands[i]
		vc[i] = videoCand{
			id: c.id, width: c.width, height: c.height, bandwidth: c.bandwidth,
			codecs: c.codecs, frameRate: c.frameRate,
			note: detailPeriod + pi.id + " adaptationSet=" + strconv.Itoa(c.group),
		}
		if c.protected {
			vc[i].note += " protected"
		}
	}
	if !st.have {
		sel := &l.p.Selection
		for i := range vc {
			sel.VideoCandidates = append(sel.VideoCandidates, vc[i].describe(i))
		}
		i := l.vrule.pick(vc, l.opts.MinHeight)
		if i < 0 {
			return -1, errf(ErrSelection, "video rule %q matches none of the %d video Representation(s) of Period %q", l.vrule, len(vc), pi.id)
		}
		return i, nil
	}
	ref := &st.ref
	if ref.id != "" {
		for i := range cands {
			if cands[i].id == ref.id {
				return i, nil
			}
		}
	}
	best := -1
	for i := range cands {
		c := &cands[i]
		if c.width != ref.width || c.height != ref.height || c.codecs != ref.codecs {
			continue
		}
		if best < 0 || absDiff(c.bandwidth, ref.bandwidth) < absDiff(cands[best].bandwidth, ref.bandwidth) {
			best = i
		}
	}
	if best >= 0 {
		l.notef("Period %q: video Representation %q continues logical track %q (same resolution and codecs)", pi.id, cands[best].id, ref.id)
		return best, nil
	}
	i := l.vrule.pick(vc, l.opts.MinHeight)
	how := "rule " + l.vrule.String()
	if i < 0 {
		i, how = pickHighest(vc), "rule highest (rule "+l.vrule.String()+" matched nothing)"
	}
	c := &cands[i]
	l.warnf("Period %q: no video Representation compatible with %q (%dx%d %s); remapped to %q (%dx%d %s) by %s",
		pi.id, ref.id, ref.width, ref.height, ref.codecs, c.id, c.width, c.height, c.codecs, how)
	return i, nil
}

// dashPickAudio selects the Period's audio Representation. It returns -1 and
// a warning when the Period has no (compatible) audio.
func (l *loader) dashPickAudio(st *dashTrackState, pi *dashPeriod, cands []dashRep) (int, string, error) {
	if len(cands) == 0 {
		return -1, fmt.Sprintf("Period %q has no audio Representation; audio is unavailable there", pi.id), nil
	}
	ac := make([]audioCand, len(cands))
	for i := range cands {
		c := &cands[i]
		ac[i] = audioCand{
			id: c.id, alt: c.as.ID.v, name: strings.TrimSpace(c.as.Label), lang: c.as.Lang,
			codecs: c.codecs, group: c.group, bandwidth: c.bandwidth,
		}
	}
	if !st.have {
		i := l.arule.pick(ac)
		if i < 0 {
			return -1, "", errf(ErrSelection, "audio rule %q matches none of the %d audio Representation(s) of Period %q", l.arule, len(ac), pi.id)
		}
		return i, "", nil
	}
	ref := &st.ref
	if ref.id != "" {
		for i := range cands {
			if cands[i].id == ref.id {
				return i, "", nil
			}
		}
	}
	best := -1
	for i := range cands {
		c := &cands[i]
		if !strings.EqualFold(c.as.Lang, ref.as.Lang) || c.codecs != ref.codecs {
			continue
		}
		if best < 0 || absDiff(c.bandwidth, ref.bandwidth) < absDiff(cands[best].bandwidth, ref.bandwidth) {
			best = i
		}
	}
	if best >= 0 {
		l.notef("Period %q: audio Representation %q continues logical track %q (same language and codecs)", pi.id, cands[best].id, ref.id)
		return best, "", nil
	}
	i := l.arule.pick(ac)
	if i < 0 {
		return -1, fmt.Sprintf("Period %q has no audio Representation compatible with %q or matching rule %s; audio is unavailable there",
			pi.id, ref.id, l.arule), nil
	}
	c := &cands[i]
	return i, fmt.Sprintf("Period %q: no audio Representation compatible with %q (language %q, %s); remapped to %q (language %q, %s) by rule %s",
		pi.id, ref.id, ref.as.Lang, ref.codecs, c.id, c.as.Lang, c.codecs, l.arule), nil
}

// dashCaptions records embedded caption services declared on the selected
// video AdaptationSet (informational).
func (l *loader) dashCaptions(as *xAdaptationSet) {
	for i := range as.Accessibility {
		d := &as.Accessibility[i]
		scheme := strings.ToLower(d.Scheme)
		if !strings.Contains(scheme, "cea-608") && !strings.Contains(scheme, "cea-708") {
			continue
		}
		if strings.TrimSpace(d.Value) == "" {
			l.p.Captions = append(l.p.Captions, CaptionService{Name: d.Scheme})
			continue
		}
		for svc := range strings.SplitSeq(d.Value, ";") {
			svc = strings.TrimSpace(svc)
			if svc == "" {
				continue
			}
			cs := CaptionService{Name: d.Scheme}
			if id, rest, ok := strings.Cut(svc, ruleAssign); ok {
				cs.ID = id
				cs.Language, _, _ = strings.Cut(rest, listSeparator)
				cs.Language = strings.TrimPrefix(cs.Language, "lang:")
			} else {
				cs.Language = svc
			}
			l.p.Captions = append(l.p.Captions, cs)
		}
	}
}

// dashEvents retains a Period's EventStream events as diagnostics markers.
func (l *loader) dashEvents(pi *dashPeriod) {
	for si := range pi.x.EventStreams {
		es := &pi.x.EventStreams[si]
		timescale := int64(1)
		if es.Timescale.set && es.Timescale.v > 0 {
			timescale = es.Timescale.v
		}
		for ei := range es.Events {
			ev := &es.Events[ei]
			m := Marker{Kind: "EventStream", TimeUS: -1}
			if us, ok := mulDiv(ev.Time.v-es.PTO.v, usPerSecond, timescale, false); ok && us <= math.MaxInt64-pi.startUS {
				m.TimeUS = max(pi.startUS+us, -1)
			}
			m.Detail = detailPeriod + pi.id + detailScheme + es.Scheme
			if es.Value != "" {
				m.Detail += detailValue + es.Value
			}
			if ev.ID != "" {
				m.Detail += " id=" + ev.ID
			}
			if ev.Duration.set {
				m.Detail += " duration=" + strconv.FormatInt(ev.Duration.v, decimal) + pathSeparator + strconv.FormatInt(timescale, decimal)
			}
			m.Detail = truncate(m.Detail, maxMarkerDetail)
			l.p.Markers = append(l.p.Markers, m)
		}
	}
}

// ---------------------------------------------------------------------------
// Segment addressing
// ---------------------------------------------------------------------------

type addrKind uint8

const (
	addrNone addrKind = iota
	addrBase
	addrList
	addrTemplate
)

// dashAddr is the effective addressing of one Representation after
// inheritance.
type dashAddr struct {
	kind                                             addrKind
	timescale, pto, duration, startNumber, endNumber optInt
	timeline                                         *xTimeline
	initElem                                         *xURL
	media, init                                      optStr        // SegmentTemplate
	urls                                             []xSegmentURL // SegmentList
	href                                             bool
}

// merge overlays a lower level's attributes and elements; whatever the lower
// level declares takes precedence, everything else is inherited.
func (a *dashAddr) merge(m *xMultiBase) {
	if m.Timescale.set {
		a.timescale = m.Timescale
	}
	if m.PTO.set {
		a.pto = m.PTO
	}
	if m.StartNumber.set {
		a.startNumber = m.StartNumber
	}
	if m.EndNumber.set {
		a.endNumber = m.EndNumber
	}
	if m.InitElem != nil {
		a.initElem = m.InitElem
	}
	// @duration and SegmentTimeline are alternatives: the one declared at
	// the lowest level wins (the timeline when one element has both).
	switch {
	case m.Timeline != nil:
		a.timeline, a.duration = m.Timeline, optInt{}
	case m.Duration.set:
		a.timeline, a.duration = nil, m.Duration
	}
}

// mergeAddressing resolves addressing inheritance from the Period down to
// the Representation. The lowest level that declares any addressing element
// decides the addressing kind; same-kind elements of higher levels are
// inherited attribute by attribute.
func mergeAddressing(levels ...*xAddressing) dashAddr {
	var tmpl, list dashAddr
	kind := addrNone
	for _, lv := range levels {
		switch {
		case lv.Template != nil:
			tmpl.merge(&lv.Template.xMultiBase)
			if lv.Template.Media.set {
				tmpl.media = lv.Template.Media
			}
			if lv.Template.Init.set {
				tmpl.init = lv.Template.Init
			}
			kind = addrTemplate
		case lv.List != nil:
			list.merge(&lv.List.xMultiBase)
			if len(lv.List.URLs) > 0 {
				list.urls = lv.List.URLs
			}
			if lv.List.Href != "" {
				list.href = true
			}
			kind = addrList
		case lv.Base != nil:
			kind = addrBase
		}
	}
	switch kind {
	case addrTemplate:
		tmpl.kind = kind
		return tmpl
	case addrList:
		list.kind = kind
		return list
	}
	return dashAddr{kind: kind}
}

// Template identifiers.
const (
	partLiteral = iota
	partRepID
	partNumber
	partTime
	partBandwidth
)

type tmplPart struct {
	lit   string
	kind  uint8
	width int
	pad   byte
}

// segTemplate is a compiled SegmentTemplate URL template.
type segTemplate struct {
	parts            []tmplPart
	usesNumberOrTime bool
}

// compileTemplate parses $RepresentationID$, $Number$, $Time$, $Bandwidth$,
// "$$" and printf-style width formats such as $Number%05d$.
func compileTemplate(s string) (*segTemplate, error) {
	t := &segTemplate{}
	lit := func(v string) {
		if n := len(t.parts); n > 0 && t.parts[n-1].kind == partLiteral {
			t.parts[n-1].lit += v
			return
		}
		t.parts = append(t.parts, tmplPart{kind: partLiteral, lit: v})
	}
	orig := s
	for len(s) > 0 {
		i := strings.IndexByte(s, '$')
		if i < 0 {
			lit(s)
			break
		}
		if i > 0 {
			lit(s[:i])
		}
		s = s[i+1:]
		j := strings.IndexByte(s, '$')
		if j < 0 {
			return nil, errf(ErrInvalid, "unterminated '$' in segment template %q", orig)
		}
		ident := s[:j]
		s = s[j+1:]
		if ident == "" {
			lit("$")
			continue
		}
		name, format := ident, ""
		if k := strings.IndexByte(ident, '%'); k >= 0 {
			name, format = ident[:k], ident[k+1:]
		}
		p := tmplPart{pad: ' '}
		if format != "" {
			if format[0] == '0' {
				p.pad, format = '0', format[1:]
			}
			if len(format) == 0 || format[len(format)-1] != 'd' {
				return nil, errf(ErrInvalid, "unsupported format tag in segment template %q", orig)
			}
			if digits := format[:len(format)-1]; digits != "" {
				w, ok := parseUint(digits)
				if !ok || w > 64 {
					return nil, errf(ErrInvalid, "unsupported format width in segment template %q", orig)
				}
				p.width = int(w)
			}
		}
		switch name {
		case "RepresentationID":
			if strings.IndexByte(ident, '%') >= 0 {
				return nil, errf(ErrInvalid, "$RepresentationID$ takes no format tag in segment template %q", orig)
			}
			p.kind = partRepID
		case "Number":
			p.kind, t.usesNumberOrTime = partNumber, true
		case "Time":
			p.kind, t.usesNumberOrTime = partTime, true
		case "Bandwidth":
			p.kind = partBandwidth
		case "SubNumber":
			return nil, errf(ErrUnsupported, "$SubNumber$ (segment sequences) in segment template %q", orig)
		default:
			return nil, errf(ErrInvalid, "unknown identifier $%s$ in segment template %q", ident, orig)
		}
		t.parts = append(t.parts, p)
	}
	return t, nil
}

// appendTo appends the expansion of the template to buf.
func (t *segTemplate) appendTo(buf []byte, repID string, bandwidth, number, time int64) []byte {
	for i := range t.parts {
		p := &t.parts[i]
		var v int64
		switch p.kind {
		case partLiteral:
			buf = append(buf, p.lit...)
			continue
		case partRepID:
			buf = append(buf, repID...)
			continue
		case partNumber:
			v = number
		case partTime:
			v = time
		case partBandwidth:
			v = bandwidth
		}
		var tmp [20]byte
		digits := strconv.AppendInt(tmp[:0], v, decimal)
		for n := len(digits); n < p.width; n++ {
			buf = append(buf, p.pad)
		}
		buf = append(buf, digits...)
	}
	return buf
}

// parseDashRange parses "first-last" (inclusive) into offset and length.
func parseDashRange(s string) (off, length int64, ok bool) {
	a, b, found := strings.Cut(strings.TrimSpace(s), rangeSeparator)
	first, ok1 := parseUint(a)
	last, ok2 := parseUint(b)
	if !found || !ok1 || !ok2 || last < first {
		return 0, 0, false
	}
	return first, last - first + 1, true
}

// dashURL builds a resource from an optional URL (default: the BaseURL
// itself) and an optional inclusive byte range.
func (l *loader) dashURL(rb *refBase, src, rng optStr) (Resource, error) {
	res := Resource{URI: rb.raw, Length: -1}
	if src.set && strings.TrimSpace(src.v) != "" {
		u, err := l.resolve(rb, strings.TrimSpace(src.v))
		if err != nil {
			return res, err
		}
		res.URI = u
	}
	if rng.set {
		off, n, ok := parseDashRange(rng.v)
		if !ok {
			return res, errf(ErrInvalid, "malformed byte range %q", truncate(rng.v, maxQuoted))
		}
		res.Offset, res.Length = off, n
	}
	return res, nil
}

// dashGen emits the segments of one Representation in one Period.
type dashGen struct {
	l           *loader
	tr          *Track
	rb          *refBase
	rep         *dashRep
	epoch       int
	initIdx     int
	startUS     int64 // Period start
	timescale   int64
	pto         int64
	startNumber int64
	endAbs      int64 // Period end on the media timeline (timescale units); -1 unknown

	media  *segTemplate  // SegmentTemplate
	urls   []xSegmentURL // SegmentList
	buf    []byte
	prefix int  // length of the directory prefix kept in buf (fast path)
	fast   bool // template expansions need no URI normalization
}

// toUS maps a media-timeline time onto the presentation timeline.
func (g *dashGen) toUS(t int64) (int64, bool) {
	us, ok := mulDiv(t-g.pto, usPerSecond, g.timescale, false)
	if !ok || (us > 0 && g.startUS > math.MaxInt64-us) {
		return 0, false
	}
	return g.startUS + us, true
}

// emit appends segment idx (0-based position in the Representation's
// segment sequence) covering media time [t, t+d).
func (g *dashGen) emit(idx, t, d int64) error {
	if err := g.l.takeSegment(); err != nil {
		return err
	}
	var res Resource
	var err error
	if g.media != nil {
		g.buf = g.media.appendTo(g.buf[:g.prefix], g.rep.id, g.rep.bandwidth, g.startNumber+idx, t)
		res.Length = -1
		if g.fast {
			res.URI = string(g.buf)
		} else if res.URI, err = g.l.resolve(g.rb, string(g.buf)); err != nil {
			return err
		}
	} else {
		u := &g.urls[idx]
		if res, err = g.l.dashURL(g.rb, u.media, u.mediaRange); err != nil {
			return err
		}
	}
	start, ok1 := g.toUS(t)
	end, ok2 := g.toUS(t + d)
	if !ok1 || !ok2 {
		return errf(ErrInvalid, "segment time %d/%d overflows", t, g.timescale)
	}
	g.tr.Segments = append(g.tr.Segments, Segment{
		Resource:   res,
		InitIndex:  g.initIdx,
		Epoch:      g.epoch,
		Number:     g.startNumber + idx,
		StartUS:    start,
		DurationUS: end - start,
	})
	return nil
}

// grow preallocates room for n more segments (bounded by the budget).
func (g *dashGen) grow(n int64) {
	n = min(n, int64(g.l.segBudget))
	if n > 0 {
		g.tr.Segments = slices.Grow(g.tr.Segments, int(n))
	}
}

// timeline expands a SegmentTimeline. Segments entirely outside the Period
// are not emitted but still consume their position (and $Number$). It
// returns the number of positions the timeline describes, capped at maxIdx.
func (g *dashGen) timeline(S []xS, maxIdx int64) (int64, error) {
	var est int64
	for i := range S {
		est += max(S[i].r, 0) + 1
		if est > int64(g.l.segBudget) {
			break
		}
	}
	g.grow(min(est, maxIdx))

	var t, idx int64
	for i := range S {
		s := &S[i]
		if s.hasT {
			t = s.t
		}
		var n int64
		if s.r >= 0 {
			n = s.r + 1
		} else {
			// Negative repeat: until the next S@t, else the Period end.
			until := g.endAbs
			if i+1 < len(S) && S[i+1].hasT {
				until = S[i+1].t
			}
			if until < 0 {
				return idx, errf(ErrInvalid, "SegmentTimeline negative repeat needs a following S@t or a finite Period end")
			}
			if until > t {
				n = (until-t-1)/s.d + 1
			}
		}
		if n > (math.MaxInt64-t)/s.d || n > math.MaxInt64-idx {
			return idx, errf(ErrInvalid, "SegmentTimeline overflows")
		}
		lo, hi := int64(0), n
		if g.pto > t {
			lo = (g.pto - t) / s.d // segments ending at or before the Period start
		}
		if g.endAbs >= 0 {
			if g.endAbs <= t {
				hi = 0
			} else {
				hi = min(hi, (g.endAbs-t-1)/s.d+1) // segments starting before the Period end
			}
		}
		hi = min(hi, maxIdx-idx)
		if hi-lo > int64(g.l.segBudget) {
			return idx, g.l.overLimit()
		}
		if s.r < 0 {
			g.grow(hi - lo) // the estimate above could not size a negative repeat
		}
		for k := lo; k < hi; k++ {
			if err := g.emit(idx+k, t+k*s.d, s.d); err != nil {
				return idx, err
			}
		}
		idx += n
		t += n * s.d
		if idx >= maxIdx {
			return maxIdx, nil
		}
	}
	return idx, nil
}

// fixed emits n segments of constant duration d starting at the
// presentation time offset.
func (g *dashGen) fixed(d, n int64) error {
	if n > int64(g.l.segBudget) {
		return g.l.overLimit()
	}
	g.grow(n)
	for k := int64(0); k < n; k++ {
		if k > (math.MaxInt64-g.pto-d)/d {
			return errf(ErrInvalid, "segment time overflows")
		}
		if err := g.emit(k, g.pto+k*d, d); err != nil {
			return err
		}
	}
	return nil
}

// dashBuild appends the segments of Representation c in Period pi to the
// logical track.
func (l *loader) dashBuild(st *dashTrackState, epoch int, pi *dashPeriod, c *dashRep, kind string) (TrackEpoch, error) {
	var te TrackEpoch
	fail := func(err error) (TrackEpoch, error) {
		return te, fmt.Errorf("%w (Period %q, %s Representation %q)", err, pi.id, kind, c.id)
	}
	if c.protected {
		return fail(errf(ErrUnsupported, "encrypted media (ContentProtection)"))
	}
	base, err := l.chainBase(pi.base, c.as.BaseURLs)
	if err == nil {
		base, err = l.chainBase(base, c.rep.BaseURLs)
	}
	if err != nil {
		return fail(err)
	}
	if len(c.as.BaseURLs) > 1 || len(c.rep.BaseURLs) > 1 {
		l.notef("Period %q, Representation %q: multiple BaseURL alternatives; using the first", pi.id, c.id)
	}
	rb := l.newBase(base)

	a := mergeAddressing(&pi.x.xAddressing, &c.as.xAddressing, &c.rep.xAddressing)
	switch {
	case a.kind == addrNone || a.kind == addrBase:
		return fail(errf(ErrUnsupported, "SegmentBase / index-range (sidx) addressing is not supported"))
	case a.href:
		return fail(errf(ErrUnsupported, "remote SegmentList (xlink:href)"))
	case a.kind == addrTemplate && !a.media.set:
		return fail(errf(ErrUnsupported, "SegmentTemplate without @media (index-range addressing) is not supported"))
	}

	g := dashGen{
		l: l, tr: st.tr, rb: rb, rep: c, epoch: epoch, initIdx: -1,
		startUS: pi.startUS, timescale: 1, startNumber: 1, endAbs: -1,
	}
	if a.timescale.set {
		if a.timescale.v <= 0 {
			return fail(errf(ErrInvalid, "non-positive @timescale"))
		}
		g.timescale = a.timescale.v
	}
	if a.pto.set {
		if a.pto.v < 0 {
			return fail(errf(ErrInvalid, "negative @presentationTimeOffset"))
		}
		g.pto = a.pto.v
	}
	if a.startNumber.set {
		if a.startNumber.v < 0 {
			return fail(errf(ErrInvalid, "negative @startNumber"))
		}
		g.startNumber = a.startNumber.v
	}
	// Period duration in timescale units (rounded up): a segment lies
	// entirely beyond the Period when it starts at or after pto + endRel.
	endRel := int64(-1)
	if pi.endUS >= 0 {
		var ok bool
		if endRel, ok = mulDiv(pi.endUS-pi.startUS, g.timescale, usPerSecond, true); !ok || endRel > math.MaxInt64-g.pto {
			return fail(errf(ErrInvalid, "Period duration overflows at timescale %d", g.timescale))
		}
		g.endAbs = g.pto + endRel
	}

	// Initialization resource.
	switch {
	case a.kind == addrTemplate && a.init.set:
		t, err := compileTemplate(a.init.v)
		if err != nil {
			return fail(err)
		}
		if t.usesNumberOrTime {
			return fail(errf(ErrInvalid, "initialization template %q uses $Number$ or $Time$", a.init.v))
		}
		u, err := l.resolve(rb, string(t.appendTo(nil, c.id, c.bandwidth, 0, 0)))
		if err != nil {
			return fail(err)
		}
		g.initIdx = st.inits.intern(Resource{URI: u, Length: -1})
	case a.initElem != nil:
		res, err := l.dashURL(rb, a.initElem.SourceURL, a.initElem.Range)
		if err != nil {
			return fail(err)
		}
		g.initIdx = st.inits.intern(res)
	}

	// maxIdx bounds the 0-based segment position (exclusive).
	maxIdx := int64(math.MaxInt64)
	if a.endNumber.set {
		maxIdx = max(a.endNumber.v-g.startNumber+1, 0)
	}
	if a.kind == addrList {
		g.urls = a.urls
		maxIdx = min(maxIdx, int64(len(a.urls)))
	} else {
		if g.media, err = compileTemplate(a.media.v); err != nil {
			return fail(err)
		}
		sample := string(g.media.appendTo(nil, c.id, c.bandwidth, g.startNumber, 0))
		if !rb.remote && filepath.Separator == '/' && isSimpleRef(sample) {
			g.fast = true
			g.buf = append(make([]byte, 0, len(rb.dir)+len(sample)+pathSlack), rb.dir...)
			g.prefix = len(rb.dir)
		}
	}

	before := len(st.tr.Segments)
	switch {
	case a.timeline != nil:
		described, err := g.timeline(a.timeline.S, maxIdx)
		if err != nil {
			return fail(err)
		}
		if a.kind == addrList && described < maxIdx {
			return fail(errf(ErrInvalid, "SegmentList has %d SegmentURL(s) but its SegmentTimeline describes only %d", maxIdx, described))
		}
	case a.duration.set:
		d := a.duration.v
		if d <= 0 {
			return fail(errf(ErrInvalid, "non-positive segment @duration"))
		}
		n := maxIdx
		if endRel >= 0 {
			count := endRel / d
			if endRel%d != 0 {
				count++
			}
			n = min(n, count)
		}
		if n == math.MaxInt64 {
			return fail(errf(ErrInvalid, "cannot derive the segment count: the Period end is unknown and there is no @endNumber"))
		}
		if err := g.fixed(d, n); err != nil {
			return fail(err)
		}
	case a.kind == addrTemplate || len(a.urls) == 1:
		// A single segment spanning the Period.
		if maxIdx > 0 {
			if err := g.emit(0, g.pto, max(endRel, 0)); err != nil {
				return fail(err)
			}
		}
	default:
		return fail(errf(ErrInvalid, "SegmentList with %d SegmentURLs has neither @duration nor SegmentTimeline", len(a.urls)))
	}

	if len(st.tr.Segments) == before {
		l.warnf("Period %q: %s Representation %q has no segments within the Period bounds", pi.id, kind, c.id)
		return te, nil
	}
	for i := range c.as.Inband {
		l.dashInband(pi, c, &c.as.Inband[i])
	}
	for i := range c.rep.Inband {
		l.dashInband(pi, c, &c.rep.Inband[i])
	}
	te = TrackEpoch{Available: true, RepresentationID: c.id, Timescale: g.timescale, PresentationTimeOffset: g.pto}
	return te, nil
}

// dashInband notes an InbandEventStream declaration as a diagnostics marker.
func (l *loader) dashInband(pi *dashPeriod, c *dashRep, d *xDescriptor) {
	detail := detailPeriod + pi.id + " representation=" + c.id + detailScheme + d.Scheme
	if d.Value != "" {
		detail += detailValue + d.Value
	}
	l.p.Markers = append(l.p.Markers, Marker{Kind: "InbandEventStream", TimeUS: -1, Detail: truncate(detail, maxMarkerDetail)})
}
