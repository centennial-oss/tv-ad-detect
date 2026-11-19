// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"sort"

	"github.com/centennial-oss/tv-ad-detect/pkg/cea608"
)

// capSpan is an interval of caption style; time outside every span is
// unknown (CAP-01).
type capSpan struct {
	start, end int64
	style      CaptionStyle
}

type capRun struct {
	start, last int64
	style       CaptionStyle
}

// captionTracker reduces caption data to compact runs: runs of observable
// service data and runs of caption activity per style. No text is kept
// (CAP-03, QUAL-04).
type captionTracker struct {
	dec       *cea608.Decoder
	timeoutUS int64
	dataUS    int64
	persistUS int64
	act       []capRun
	data      []capRun
	caller    []capSpan
	events    int
	phrases   *phraseMatcher // opt-in phrase matching, nil when off
}

func (c *captionTracker) init(cfg *Config) {
	c.dec = cea608.NewDecoder(int(cfg.Captions.Service[2] - '0'))
	c.timeoutUS = us(cfg.Captions.InactiveTimeout)
	c.dataUS = us(cfg.Captions.DataTimeout)
	c.persistUS = us(cfg.Captions.StylePersistence)
	c.phrases = newPhraseMatcher(cfg)
}

var styleFrom608 = [...]CaptionStyle{
	cea608.StyleUnknown: CaptionUnknown,
	cea608.StyleRollUp:  CaptionRollUp,
	cea608.StylePopOn:   CaptionPopOn,
	cea608.StylePaintOn: CaptionPaintOn,
}

// feed ingests the cc_data triplets of one frame.
func (c *captionTracker) feed(pts int64, cc []byte) {
	if c.phrases != nil {
		c.phrases.feed(pts, cc)
	}
	hasData, activity, st := c.dec.Feed(cc)
	if !hasData {
		return
	}
	c.events++
	if n := len(c.data); n > 0 && pts-c.data[n-1].last <= c.dataUS && pts >= c.data[n-1].last {
		c.data[n-1].last = pts
	} else {
		c.data = append(c.data, capRun{start: pts, last: pts})
	}
	style := styleFrom608[st]
	if !activity || style == CaptionUnknown {
		return
	}
	if n := len(c.act); n > 0 && c.act[n-1].style == style && pts-c.act[n-1].last <= c.timeoutUS && pts >= c.act[n-1].last {
		c.act[n-1].last = pts
	} else {
		c.act = append(c.act, capRun{start: pts, last: pts, style: style})
	}
}

func (c *captionTracker) style(start, end int64, style CaptionStyle) {
	if end > start && style != CaptionUnknown {
		c.caller = append(c.caller, capSpan{start, end, style})
		c.events++
	}
}

func (c *captionTracker) shift(d int64) {
	for i := range c.act {
		c.act[i].start -= d
		c.act[i].last -= d
	}
	for i := range c.data {
		c.data[i].start -= d
		c.data[i].last -= d
	}
	for i := range c.caller {
		c.caller[i].start -= d
		c.caller[i].end -= d
	}
}

// spans builds the style timeline on [0,dur). frameUS is the nominal frame
// duration used to close a run after its last observation.
func (c *captionTracker) spans(dur, frameUS int64) []capSpan {
	var out []capSpan
	add := func(s capSpan) {
		s.start, s.end = max(s.start, 0), min(s.end, dur)
		if s.end <= s.start {
			return
		}
		if n := len(out); n > 0 {
			p := &out[n-1]
			if s.start < p.end {
				s.start = p.end
				if s.end <= s.start {
					return
				}
			}
			if p.end == s.start && p.style == s.style {
				p.end = s.end
				return
			}
		}
		out = append(out, s)
	}
	if len(c.caller) > 0 {
		sort.SliceStable(c.caller, func(i, j int) bool { return c.caller[i].start < c.caller[j].start })
		for _, s := range c.caller {
			add(s)
		}
	}
	ai := 0
	for _, d := range c.data {
		dEnd := d.last + frameUS
		cursor := d.start
		for ; ai < len(c.act) && c.act[ai].start < dEnd; ai++ {
			a := c.act[ai]
			aEnd := a.last + frameUS
			// Activity of the next run within the timeout keeps the style
			// up to that run (a style change is not an absence).
			if ai+1 < len(c.act) && c.act[ai+1].start-a.last <= c.timeoutUS {
				aEnd = c.act[ai+1].start
			}
			aEnd = min(aEnd, dEnd)
			if a.start > cursor {
				add(capSpan{cursor, a.start, CaptionNone})
			}
			add(capSpan{max(a.start, cursor), aEnd, a.style})
			cursor = max(cursor, aEnd)
			if a.last > d.last {
				// The activity continues in a later data run (a hole in the
				// caption data, not in the captions): keep the run for it.
				break
			}
		}
		if cursor < dEnd {
			add(capSpan{cursor, dEnd, CaptionNone})
		}
	}
	// Style persistence: a short interval is absorbed by its predecessor.
	if c.persistUS > 0 && len(out) > 1 {
		k := 1
		for i := 1; i < len(out); i++ {
			s := out[i]
			p := &out[k-1]
			if p.end == s.start && (s.style == p.style || s.end-s.start < c.persistUS) {
				p.end = s.end
				continue
			}
			out[k] = s
			k++
		}
		out = out[:k]
	}
	return out
}
