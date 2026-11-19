// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"bytes"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/centennial-oss/tv-ad-detect/pkg/cea608"
)

// Opt-in caption phrase matching (CAP-03 exception). Caption characters are
// decoded only while phrase lists are configured; they pass through a short
// normalized window that is overwritten as captions continue, and only the
// matches (configured phrase, list and time) are kept.

// phraseWindowSlack is extra window length beyond the longest phrase.
const phraseWindowSlack = 16

// normalizePhrase lowercases letters and digits, drops apostrophes so that
// "what's" matches "whats", and turns every other run of characters into
// one space.
func normalizePhrase(s string) string {
	var b strings.Builder
	sp := true
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			sp = false
		case r == '\'' || r == '’' || r == '‘':
		case !sp:
			b.WriteByte(' ')
			sp = true
		}
	}
	return strings.TrimSpace(b.String())
}

type phrase struct {
	list, text string // configured list and phrase (reported)
	norm       []byte // normalized, with a leading space
	last       int64  // time of the last counted match
	matched    bool
}

// phraseMatcher matches the configured phrases in decoded caption text.
type phraseMatcher struct {
	dec     *cea608.TextDecoder
	phrases []phrase
	repeat  int64
	raw     []byte
	window  []byte // normalized text, starting with a space
	maxLen  int
	hits    []PhraseHit
}

func newPhraseMatcher(cfg *Config) *phraseMatcher {
	c := &cfg.Captions
	if len(c.AdPhrases) == 0 && len(c.ProgramPhrases) == 0 {
		return nil
	}
	m := &phraseMatcher{dec: cea608.NewTextDecoder(int(c.Service[2] - '0')), repeat: us(c.PhraseRepeat), window: []byte{' '}}
	add := func(list string, ps []string) {
		for _, p := range ps {
			n := normalizePhrase(p)
			m.phrases = append(m.phrases, phrase{list: list, text: p, norm: []byte(" " + n)})
			m.maxLen = max(m.maxLen, len(n)+1)
		}
	}
	add(PhraseListAdvertisement, c.AdPhrases)
	add(PhraseListProgram, c.ProgramPhrases)
	return m
}

// feed decodes one frame's caption bytes and records matches at pts.
func (m *phraseMatcher) feed(pts int64, cc []byte) {
	m.raw = m.dec.Feed(cc, m.raw[:0])
	for len(m.raw) > 0 {
		r, n := utf8.DecodeRune(m.raw)
		m.raw = m.raw[n:]
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			m.window = utf8.AppendRune(m.window, unicode.ToLower(r))
		case r == '\'' || r == '’' || r == '‘':
		default:
			if m.window[len(m.window)-1] != ' ' {
				// A word ended: phrases end at word boundaries.
				m.match(pts)
				m.window = append(m.window, ' ')
			}
		}
	}
	// Keep the window short: only text that can still complete a phrase.
	if keep := m.maxLen + phraseWindowSlack; len(m.window) > 2*keep {
		cut := len(m.window) - keep
		if i := bytes.IndexByte(m.window[cut:], ' '); i >= 0 {
			m.window = append(m.window[:0], m.window[cut+i:]...)
		}
	}
}

func (m *phraseMatcher) match(pts int64) {
	for k := range m.phrases {
		p := &m.phrases[k]
		if !bytes.HasSuffix(m.window, p.norm) {
			continue
		}
		if p.matched && pts-p.last < m.repeat {
			continue
		}
		p.matched, p.last = true, pts
		m.hits = append(m.hits, PhraseHit{TimeUS: pts, List: p.list, Phrase: p.text})
	}
}

// phraseTimes returns the sorted hit times of one list.
func phraseTimes(hits []PhraseHit, list string) []int64 {
	var out []int64
	for _, h := range hits {
		if h.List == list {
			out = append(out, h.TimeUS)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// countIn counts sorted times in [start, end).
func countIn(ts []int64, start, end int64) int {
	return sort.Search(len(ts), func(i int) bool { return ts[i] >= end }) - sort.Search(len(ts), func(i int) bool { return ts[i] >= start })
}
