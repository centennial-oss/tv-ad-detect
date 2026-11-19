// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"fmt"
	"strings"
	"testing"
)

// AT-07: caption presentation style (CAP-01..03, BASE-02).

func captionSpans(r *Result) string {
	parts := make([]string, len(r.Diagnostics.Events.Captions))
	for i, c := range r.Diagnostics.Events.Captions {
		parts[i] = fmt.Sprintf("%s[%g,%g)", c.Style, float64(c.StartUS)/1e6, float64(c.EndUS)/1e6)
	}
	return strings.Join(parts, " ")
}

// AT-07 / CAP-01: CEA-608 data carried with the video frames distinguishes
// the three styles, observed absence (none) and unknown.
func TestCaptionStylesFromCEA608(t *testing.T) {
	s := newScenario(
		blk{D: 30 * sec, CC: "ru"},
		blk{D: 30 * sec, CC: "pop", Sep: sepNone},
		blk{D: 30 * sec, CC: "paint", Sep: sepNone},
		blk{D: 30 * sec, CC: "null", Sep: sepNone}, // service present, nothing displayed
		blk{D: 30 * sec, Sep: sepNone},             // no caption data at all
	)
	cfg := testConfig()
	cfg.Boundaries.FromCaptions = true
	r := s.run(t, cfg)
	if got, want := captionSpans(r), "roll_up[0,30) pop_on[30,60) paint_on[60,90) none[90,120)"; got != want {
		t.Fatalf("caption spans = %s\n               want %s", got, want)
	}
	d := detector(t, r, SignalCaptions)
	if d.Status != StatusUsable || *d.CoverageUS != 120_000_000 || !d.Boundary {
		t.Errorf("detector = %+v", d)
	}
	// CAP-02: duration by style and the dominant style per block.
	wantBoundaries(t, r, 0, 30, 60, 90, 150)
	for i, want := range []string{"roll_up", "pop_on", "paint_on"} {
		b := r.Diagnostics.Blocks[i]
		if b.CaptionStyle != want || len(b.CaptionStylesUS) != 1 || b.CaptionStylesUS[want] != 30_000_000 {
			t.Errorf("block %d: style %s durations %v", i, b.CaptionStyle, b.CaptionStylesUS)
		}
	}
	// The last block is half observed absence, half unknown: unknown time is
	// not a style and is not counted.
	last := r.Diagnostics.Blocks[3]
	if last.CaptionStyle != "none" || len(last.CaptionStylesUS) != 1 || last.CaptionStylesUS["none"] != 30_000_000 {
		t.Errorf("last block: style %s durations %v", last.CaptionStyle, last.CaptionStylesUS)
	}
	// CAP-03: style changes are boundaries only when enabled, quantized to a
	// frame, with one frame of uncertainty.
	for _, at := range []int64{30_000_000, 60_000_000, 90_000_000} {
		b := mustBoundary(t, r, us2d(at))
		if len(b.Causes) != 1 || !hasCause(b, SignalCaptions, "style_change", "boundary") || b.UncertaintyUS != 100_000 || len(b.Evidence) != 0 {
			t.Errorf("caption boundary at %d = %+v", at, b)
		}
	}
	r = s.run(t, nil)
	wantBoundaries(t, r, 0, 150)
	if d := detector(t, r, SignalCaptions); d.Boundary {
		t.Errorf("captions reported as a boundary source by default")
	}
}

// AT-07 / CAP-01: the same styles through the tracker from a separate
// caption track (EventCaptionData) and from caller-decoded intervals
// (EventCaptionStyle).
func TestCaptionStylesFromEvents(t *testing.T) {
	// Raw data on a caption track, timestamped independently of the frames.
	var evs []Event
	for i := 0; i < 900; i++ {
		pts := int64(i) * 100_000
		evs = append(evs, videoFrame(1_000_000+pts, 100_000, content))
		var cc []byte
		switch {
		case i < 300:
			cc = ccRU2
		case i < 600:
			cc = ccPop
		default:
			cc = ccPaint
		}
		evs = append(evs, Event{Kind: EventCaptionData, Caption: CaptionObservation{PTS: 1_000_000 + pts, CC: cc}})
	}
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	if got, want := captionSpans(r), "roll_up[0,30) pop_on[30,60) paint_on[60,90)"; got != want {
		t.Errorf("caption track spans = %s, want %s", got, want)
	}

	// Caller-decoded style intervals.
	s := newScenario(
		blk{D: 30 * sec, Cap: CaptionRollUp},
		blk{D: 30 * sec, Cap: CaptionPopOn, Sep: sepNone},
		blk{D: 30 * sec, Cap: CaptionPaintOn, Sep: sepNone},
		blk{D: 30 * sec, Cap: CaptionNone, Sep: sepNone},
		blk{D: 30 * sec, Sep: sepNone},
	)
	s.startPTS = 5_000_000
	r = s.run(t, nil)
	if got, want := captionSpans(r), "roll_up[0,30) pop_on[30,60) paint_on[60,90) none[90,120)"; got != want {
		t.Errorf("style interval spans = %s, want %s", got, want)
	}
	if d := detector(t, r, SignalCaptions); d.Status != StatusUsable || *d.CoverageUS != 120_000_000 {
		t.Errorf("detector = %+v", d)
	}
}

// CAP-03: a different service on the same field does not redefine the
// selected service's style.
func TestCaptionServiceSelection(t *testing.T) {
	s := newScenario(blk{D: 30 * sec, CC: "ru"})
	cfg := testConfig()
	cfg.Captions.Service = "CC2"
	r := s.run(t, cfg)
	// CC1 data proves field 1 is observable; CC2 shows nothing.
	if got := captionSpans(r); got != "none[0,30)" {
		t.Errorf("CC2 spans = %s", got)
	}
	cfg.Captions.Service = "CC3" // field 2 is not present at all
	r = s.run(t, cfg)
	if got := captionSpans(r); got != "" {
		t.Errorf("CC3 spans = %s", got)
	}
	if d := detector(t, r, SignalCaptions); d.Status != StatusUnavailable || d.Reason != "no_caption_data" {
		t.Errorf("detector = %+v", d)
	}
}

// CAP-01: timeouts and persistence of the tracker.
func TestCaptionTrackerTimeouts(t *testing.T) {
	t.Run("pause within the inactive timeout keeps the style", func(t *testing.T) {
		s := newScenario(
			blk{D: 20 * sec, CC: "ru"},
			blk{D: 10 * sec, CC: "null", Sep: sepNone},
			blk{D: 20 * sec, CC: "ru", Sep: sepNone},
		)
		if got := captionSpans(s.run(t, nil)); got != "roll_up[0,50)" {
			t.Errorf("spans = %s", got)
		}
	})
	t.Run("a long pause is observed absence", func(t *testing.T) {
		s := newScenario(
			blk{D: 20 * sec, CC: "ru"},
			blk{D: 25 * sec, CC: "null", Sep: sepNone},
			blk{D: 20 * sec, CC: "ru", Sep: sepNone},
		)
		if got := captionSpans(s.run(t, nil)); got != "roll_up[0,20) none[20,45) roll_up[45,65)" {
			t.Errorf("spans = %s", got)
		}
	})
	t.Run("a style change is not an absence", func(t *testing.T) {
		s := newScenario(
			blk{D: 20 * sec, CC: "ru"},
			blk{D: 10 * sec, CC: "null", Sep: sepNone},
			blk{D: 20 * sec, CC: "pop", Sep: sepNone},
		)
		if got := captionSpans(s.run(t, nil)); got != "roll_up[0,30) pop_on[30,50)" {
			t.Errorf("spans = %s", got)
		}
	})
	t.Run("short style intervals are absorbed", func(t *testing.T) {
		s := newScenario(
			blk{D: 20 * sec, CC: "ru"},
			blk{D: 1 * sec, CC: "pop", Sep: sepNone},
			blk{D: 20 * sec, CC: "ru", Sep: sepNone},
		)
		if got := captionSpans(s.run(t, nil)); got != "roll_up[0,41)" {
			t.Errorf("spans = %s", got)
		}
		cfg := testConfig()
		cfg.Captions.StylePersistence = 0
		if got := captionSpans(s.run(t, cfg)); got != "roll_up[0,20) pop_on[20,21) roll_up[21,41)" {
			t.Errorf("spans without persistence = %s", got)
		}
	})
	t.Run("missing caption data is unknown, not absence", func(t *testing.T) {
		// MEDIA-04: 5 s without any caption data (longer than
		// captions.data_timeout) inside continuing roll-up captions.
		s := newScenario(
			blk{D: 20 * sec, CC: "ru"},
			blk{D: 5 * sec, Sep: sepNone},
			blk{D: 20 * sec, CC: "ru", Sep: sepNone},
		)
		r := s.run(t, nil)
		if got := captionSpans(r); got != "roll_up[0,20) roll_up[25,45)" {
			t.Errorf("spans = %s, want roll_up[0,20) roll_up[25,45)", got)
		}
		if d := detector(t, r, SignalCaptions); *d.CoverageUS != 40_000_000 {
			t.Errorf("caption coverage = %d", *d.CoverageUS)
		}
	})
	t.Run("missing data between different styles", func(t *testing.T) {
		s := newScenario(
			blk{D: 20 * sec, CC: "ru"},
			blk{D: 5 * sec, Sep: sepNone},
			blk{D: 20 * sec, CC: "pop", Sep: sepNone},
		)
		if got := captionSpans(s.run(t, nil)); got != "roll_up[0,20) pop_on[25,45)" {
			t.Errorf("spans = %s", got)
		}
	})
}

// captionAdScenario: 130 s of roll-up program, a 30 s block in adStyle, 63 s
// more program. No logo, so captions and aspect are the program evidence.
func captionAdScenario(adStyle CaptionStyle) *scenario {
	return newScenario(
		blk{D: 130 * sec, Cap: CaptionRollUp},
		blk{D: 30 * sec, Cap: adStyle},
		blk{D: 63 * sec, Cap: CaptionRollUp},
	)
}

// AT-07 / CAP-02, BASE-02: a recognized style that differs from a reliable
// program baseline raises the ad score once.
func TestCaptionMismatchWithReliableBaseline(t *testing.T) {
	r := captionAdScenario(CaptionPopOn).run(t, nil)
	bl := r.Baselines.CaptionStyle
	if !bl.Reliable || bl.Style != "roll_up" || bl.Origin != "long_stable_intervals" || bl.SupportUS != 130_000_000 || *bl.Dominance != 1 {
		t.Fatalf("caption baseline = %+v", bl)
	}
	ad := blockSpan(t, r, 130*sec, 160*sec)
	f := findFactor(ad.Factors, "caption_style_mismatch")
	if f == nil || f.Multiplier != 2 || f.Family != "captions" || f.Detail != "pop_on vs program roll_up" || *f.Coverage != 1 {
		t.Fatalf("caption factor = %+v (factors %s)", f, rules(ad.Factors))
	}
	if ad.Score != 4 || rules(ad.Factors) != "strict_standard_duration,caption_style_mismatch" {
		t.Errorf("ad block score %v factors %s", ad.Score, rules(ad.Factors))
	}
	u := r.AdUnits[0]
	if !hasString(u.EvidenceFamilies, "caption_style_mismatch") {
		t.Errorf("unit evidence = %v", u.EvidenceFamilies)
	}
	// SCORE-07: the long stable interval whose caption style and active
	// ratio match the baselines is positive program evidence; the shorter
	// one is not, and stays unknown.
	wantTimeline(t, r, "P0-130 A130-160 U160-223")
	if p := blockSpan(t, r, 0, 130*sec); !hasString(p.ProgramEvidence, "stable_long_interval") || !hasString(p.Reasons, "stable_long_interval") {
		t.Errorf("program block = %+v", p)
	}
	if seg := segmentAt(t, r, 200*sec); !hasString(seg.Reasons, "insufficient_evidence") {
		t.Errorf("trailing segment reasons = %v", seg.Reasons)
	}

	// The matching style is no mismatch; it is program evidence instead
	// (scoring.caption_match_factor, 0.4), so the 30 s block is not an ad.
	r = captionAdScenario(CaptionRollUp).run(t, nil)
	if b := blockSpan(t, r, 130*sec, 160*sec); findFactor(b.Factors, "caption_style_mismatch") != nil || b.Score != 0.8 || rules(b.Factors) != "strict_standard_duration,caption_style_match" {
		t.Errorf("matching style: score %v factors %s", b.Score, rules(b.Factors))
	}
	if len(r.AdBreaks) != 0 {
		t.Errorf("matching style: %s", timeline(r))
	}
	// With the match factor neutral the matching style contributes nothing.
	r = captionAdScenario(CaptionRollUp).run(t, cfg1(t, "scoring.caption_match_factor", "1"))
	if b := blockSpan(t, r, 130*sec, 160*sec); b.Score != 2 || rules(b.Factors) != "strict_standard_duration" {
		t.Errorf("matching style, match factor 1: score %v factors %s", b.Score, rules(b.Factors))
	}

	// A neutral multiplier disables the rule (SCORE-02).
	cfg := testConfig()
	cfg.Scoring.CaptionFactor = 1
	r = captionAdScenario(CaptionPopOn).run(t, cfg)
	if b := blockSpan(t, r, 130*sec, 160*sec); findFactor(b.Factors, "caption_style_mismatch") != nil || b.Score != 2 {
		t.Errorf("neutral factor: score %v factors %s", b.Score, rules(b.Factors))
	}
}

// AT-07 / BASE-02: without a reliable baseline a different style is no
// evidence at all.
func TestCaptionMismatchNeedsReliableBaseline(t *testing.T) {
	check := func(t *testing.T, r *Result, at int, reason string) {
		t.Helper()
		bl := r.Baselines.CaptionStyle
		if bl.Reliable || bl.Reason != reason {
			t.Errorf("caption baseline = %+v, want unreliable (%s)", bl, reason)
		}
		for _, b := range r.Diagnostics.Blocks {
			if findFactor(b.Factors, "caption_style_mismatch") != nil {
				t.Errorf("block %s has a caption factor without a reliable baseline", b.ID)
			}
		}
		for _, h := range r.Diagnostics.Hypotheses {
			if hasString(h.EvidenceFamilies, "caption_style_mismatch") {
				t.Errorf("hypothesis %s counts caption evidence", h.ID)
			}
		}
	}
	t.Run("insufficient support", func(t *testing.T) {
		// 110 s of recognized captions, below baselines.min_support (120 s).
		s := newScenario(blk{D: 37 * sec, Cap: CaptionRollUp}, blk{D: 30 * sec, Cap: CaptionPopOn}, blk{D: 43 * sec, Cap: CaptionRollUp})
		r := s.run(t, nil)
		check(t, r, 37, "insufficient_support")
		if b := blockSpan(t, r, 37*sec, 67*sec); b.Score != 2 {
			t.Errorf("score = %v", b.Score)
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		// 70 s roll-up against 60 s pop-on: dominance 0.54 < 0.6.
		s := newScenario(blk{D: 70 * sec, Cap: CaptionRollUp}, blk{D: 30 * sec, Cap: CaptionPopOn}, blk{D: 30 * sec, Cap: CaptionPopOn})
		r := s.run(t, nil)
		check(t, r, 70, "ambiguous")
		bl := r.Baselines.CaptionStyle
		if bl.Origin != "recording_dominant_fallback" || bl.SupportUS != 130_000_000 || *bl.Dominance >= 0.6 {
			t.Errorf("baseline = %+v", bl)
		}
	})
	t.Run("fallback origin is reported", func(t *testing.T) {
		// Enough support only when every interval counts.
		s := newScenario(blk{D: 47 * sec, Cap: CaptionRollUp}, blk{D: 30 * sec, Cap: CaptionPopOn}, blk{D: 53 * sec, Cap: CaptionRollUp})
		r := s.run(t, nil)
		bl := r.Baselines.CaptionStyle
		if !bl.Reliable || bl.Origin != "recording_dominant_fallback" || bl.Style != "roll_up" {
			t.Errorf("baseline = %+v", bl)
		}
		if b := blockSpan(t, r, 47*sec, 77*sec); b.Score != 4 {
			t.Errorf("score = %v", b.Score)
		}
	})
	t.Run("channel profile baseline", func(t *testing.T) {
		s := newScenario(blk{D: 37 * sec, Cap: CaptionRollUp}, blk{D: 30 * sec, Cap: CaptionPopOn}, blk{D: 43 * sec, Cap: CaptionRollUp})
		cfg := testConfig()
		cfg.Baselines.CaptionStyle = "roll_up"
		r := s.run(t, cfg)
		bl := r.Baselines.CaptionStyle
		if !bl.Reliable || bl.Origin != "channel_profile" || bl.Style != "roll_up" {
			t.Errorf("baseline = %+v", bl)
		}
		if b := blockSpan(t, r, 37*sec, 67*sec); b.Score != 4 || findFactor(b.Factors, "caption_style_mismatch") == nil {
			t.Errorf("score = %v factors %s", b.Score, rules(b.Factors))
		}
	})
	t.Run("logo-supported baseline is preferred", func(t *testing.T) {
		s := newScenario(
			blk{D: 70 * sec, Cap: CaptionRollUp, Logo: true},
			blk{D: 30 * sec, Cap: CaptionPopOn},
			blk{D: 77 * sec, Cap: CaptionRollUp, Logo: true},
		)
		r := s.run(t, nil)
		bl := r.Baselines.CaptionStyle
		if !bl.Reliable || bl.Origin != "logo_present" || bl.Style != "roll_up" || *bl.Dominance < 0.99 {
			t.Errorf("baseline = %+v", bl)
		}
		// Strict duration x logo absent x caption mismatch.
		if b := blockSpan(t, r, 70*sec, 100*sec); b.Score != 8 {
			t.Errorf("score = %v factors %s", b.Score, rules(b.Factors))
		}
		wantTimeline(t, r, "P0-70 A70-100 P100-177")
	})
}

// AT-07 / CAP-02: observed absence of captions is neutral by default and a
// mismatch only under the explicit policy.
func TestCaptionAbsencePolicy(t *testing.T) {
	r := captionAdScenario(CaptionNone).run(t, nil)
	b := blockSpan(t, r, 130*sec, 160*sec)
	if b.CaptionStyle != "none" || findFactor(b.Factors, "caption_style_mismatch") != nil || b.Score != 2 {
		t.Errorf("neutral policy: style %s score %v factors %s", b.CaptionStyle, b.Score, rules(b.Factors))
	}
	for _, h := range r.Diagnostics.Hypotheses {
		if hasString(h.EvidenceFamilies, "caption_style_mismatch") {
			t.Errorf("hypothesis %s counts caption absence as evidence", h.ID)
		}
	}

	cfg := testConfig()
	cfg.Scoring.CaptionAbsentFactor = 2
	r = captionAdScenario(CaptionNone).run(t, cfg)
	b = blockSpan(t, r, 130*sec, 160*sec)
	f := findFactor(b.Factors, "caption_absent")
	if f == nil || b.Score != 4 || f.Detail != "none vs program roll_up" || f.Family != "captions" {
		t.Errorf("caption_absent_factor 2: score %v factor %+v", b.Score, f)
	}
	if findFactor(b.Factors, "caption_style_mismatch") != nil {
		t.Errorf("observed absence is not a style mismatch")
	}
}

// AT-07 / CAP-02, MEDIA-04: no caption data, undecodable subtitles and a
// disabled detector are always neutral.
func TestCaptionUnavailableIsNeutral(t *testing.T) {
	plain := func() *scenario {
		return newScenario(blk{D: 130 * sec}, blk{D: 30 * sec}, blk{D: 63 * sec})
	}
	neutral := func(t *testing.T, r *Result) {
		t.Helper()
		if bl := r.Baselines.CaptionStyle; bl.Reliable || bl.Origin != "none" || bl.Reason != "signal_unavailable" {
			t.Errorf("caption baseline = %+v", bl)
		}
		for _, b := range r.Diagnostics.Blocks {
			if findFactor(b.Factors, "caption_style_mismatch") != nil || b.CaptionStyle != "unknown" || len(b.CaptionStylesUS) != 0 {
				t.Errorf("block %s: style %s factors %s", b.ID, b.CaptionStyle, rules(b.Factors))
			}
		}
		if len(r.Diagnostics.Events.Captions) != 0 {
			t.Errorf("caption spans = %s", captionSpans(r))
		}
		// Without caption evidence nothing is called program.
		wantTimeline(t, r, "U0-130 A130-160 U160-223")
	}
	t.Run("no caption data", func(t *testing.T) {
		r := plain().run(t, nil)
		if d := detector(t, r, SignalCaptions); d.Status != StatusUnavailable || d.Reason != "no_caption_data" || d.Boundary || d.CoverageUS != nil {
			t.Errorf("detector = %+v", d)
		}
		neutral(t, r)
	})
	t.Run("unsupported subtitle format", func(t *testing.T) {
		src := plain().source()
		src.Media.Tracks.Captions = CaptionTrackInfo{Source: "track", Index: 2, Capability: "unsupported", Detail: "subrip"}
		cfg := testConfig()
		cfg.Scoring.CaptionAbsentFactor = 2 // must not turn missing decoding into evidence
		r := analyze(t, src, cfg)
		if d := detector(t, r, SignalCaptions); d.Status != StatusUnavailable || d.Reason != "unsupported_caption_format" {
			t.Errorf("detector = %+v", d)
		}
		neutral(t, r)
		if r.Tracks.Captions.Capability != "unsupported" {
			t.Errorf("caption track info = %+v", r.Tracks.Captions)
		}
	})
	t.Run("captions disabled", func(t *testing.T) {
		cfg := testConfig()
		cfg.Signals.Captions = false
		r := captionAdScenario(CaptionPopOn).run(t, cfg)
		if d := detector(t, r, SignalCaptions); d.Status != StatusDisabled {
			t.Errorf("detector = %+v", d)
		}
		neutral(t, r)
		// Raw data is ignored too.
		r = newScenario(blk{D: 130 * sec, CC: "ru"}, blk{D: 30 * sec, CC: "pop"}, blk{D: 63 * sec, CC: "ru"}).run(t, cfg)
		neutral(t, r)
	})
	t.Run("style of unknown only", func(t *testing.T) {
		// A caller that reports "unknown" intervals supplies no style.
		s := newScenario(blk{D: 130 * sec}, blk{D: 30 * sec}, blk{D: 63 * sec})
		evs := append([]Event{{Kind: EventCaptionStyle, Caption: CaptionObservation{PTS: 0, EndPTS: 223_000_000, Style: CaptionUnknown}}}, s.events()...)
		r := analyze(t, &SliceSource{Events: evs}, testConfig())
		for _, b := range r.Diagnostics.Blocks {
			if findFactor(b.Factors, "caption_style_mismatch") != nil || b.CaptionStyle != "unknown" {
				t.Errorf("block %s: style %s factors %s", b.ID, b.CaptionStyle, rules(b.Factors))
			}
		}
		if bl := r.Baselines.CaptionStyle; bl.Reliable {
			t.Errorf("caption baseline = %+v", bl)
		}
	})
}

func TestCaptionStyleNames(t *testing.T) {
	for st, name := range map[CaptionStyle]string{
		CaptionUnknown: "unknown", CaptionNone: "none", CaptionRollUp: "roll_up", CaptionPopOn: "pop_on", CaptionPaintOn: "paint_on",
	} {
		if st.String() != name {
			t.Errorf("%d.String() = %q", st, st.String())
		}
		got, ok := ParseCaptionStyle(name)
		if !ok || got != st {
			t.Errorf("ParseCaptionStyle(%q) = %v %v", name, got, ok)
		}
	}
	if CaptionStyle(200).String() != "unknown" {
		t.Errorf("out-of-range style name")
	}
	if _, ok := ParseCaptionStyle("karaoke"); ok {
		t.Errorf("unknown name parsed")
	}
}

// R2 / CAP-01: caption bytes are used only from the carriage the source
// selected. Frame-attached bytes are ignored when captions are off or a
// caption track is the source, and caption-track events are ignored when
// embedded captions are the source. A source that does not say may deliver
// either way.
func TestCaptionBytesFollowTheSelectedSource(t *testing.T) {
	frames := func(source string) *Result {
		t.Helper()
		src := newScenario(blk{D: 30 * sec, CC: "ru"}, blk{D: 30 * sec, CC: "pop", Sep: sepNone}).source()
		src.Media.Tracks.Captions = CaptionTrackInfo{Source: source, Capability: CaptionCapabilityCEA608}
		return analyze(t, src, testConfig())
	}
	for _, source := range []string{"", CaptionSourceEmbedded} {
		if got := captionSpans(frames(source)); got != "roll_up[0,30) pop_on[30,60)" {
			t.Errorf("source %q: caption spans = %q, want the frame-attached styles", source, got)
		}
	}
	for _, source := range []string{CaptionSourceNone, CaptionSourceTrack} {
		r := frames(source)
		if got := captionSpans(r); got != "" {
			t.Errorf("source %q: frame-attached bytes produced caption spans %q", source, got)
		}
		if d := detector(t, r, SignalCaptions); d.Status == StatusUsable || d.Events != 0 {
			t.Errorf("source %q: captions detector = %+v, want no caption evidence", source, d)
		}
	}
}
