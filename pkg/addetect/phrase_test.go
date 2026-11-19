// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"context"
	"strings"
	"testing"
)

// cc608Text builds field-1 cc_data for CC1: a pop-on preamble, then text.
func cc608Text(s string) [][]byte {
	frames := [][]byte{cc608([2]byte{0x14, 0x20}, [2]byte{0x14, 0x20}), cc608([2]byte{0x14, 0x70}, [2]byte{0x14, 0x70})}
	for i := 0; i < len(s); i += 2 {
		p := [2]byte{s[i], 0}
		if i+1 < len(s) {
			p[1] = s[i+1]
		}
		frames = append(frames, cc608(p))
	}
	return append(frames, cc608([2]byte{0x14, 0x2f}, [2]byte{0x14, 0x2f}))
}

func TestNormalizePhrase(t *testing.T) {
	for in, want := range map[string]string{
		"Ask your doctor if":         "ask your doctor if",
		"  What's in YOUR wallet?! ": "whats in your wallet",
		"Mountain-High Appliance":    "mountain high appliance",
		"...":                        "",
	} {
		if got := normalizePhrase(in); got != want {
			t.Errorf("normalizePhrase(%q) = %q, want %q", in, got, want)
		}
	}
}

// Phrases match across caption line breaks and punctuation, at word
// boundaries, once per repeat interval, and only the configured phrase is
// recorded.
func TestPhraseMatcher(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Captions.AdPhrases = []string{"ask your doctor if", "what's in your wallet"}
	cfg.Captions.ProgramPhrases = []string{"previously on"}
	m := newPhraseMatcher(cfg)
	feed := func(pts int64, s string) {
		for i, f := range cc608Text(s) {
			m.feed(pts+int64(i), f)
		}
	}
	feed(1_000_000, "ASK YOUR")                        // the phrase spans two captions
	feed(2_000_000, "DOCTOR, IF IT'S RIGHT FOR YOU. ") // completes here
	feed(3_000_000, "ASK YOUR DOCTOR IFFY ")           // not at a word boundary
	feed(4_000_000, "ASK YOUR DOCTOR IF ")             // inside the 10 s repeat
	feed(20_000_000, "WHAT'S IN YOUR WALLET? ")
	feed(30_000_000, "PREVIOUSLY, ON THE SHOW ")
	var got []string
	for _, h := range m.hits {
		got = append(got, h.List+":"+h.Phrase)
	}
	want := "advertisement:ask your doctor if advertisement:what's in your wallet program:previously on"
	if strings.Join(got, " ") != want {
		t.Errorf("hits %v\nwant %s", got, want)
	}
	if m.hits[0].TimeUS < 2_000_000 || m.hits[0].TimeUS > 2_100_000 {
		t.Errorf("first hit at %d, want when the phrase completed (2 s)", m.hits[0].TimeUS)
	}
	if len(m.window) > 2*(m.maxLen+phraseWindowSlack) {
		t.Errorf("window kept %d bytes", len(m.window))
	}
	if newPhraseMatcher(DefaultConfig()) != nil {
		t.Errorf("phrase matching is on without phrase lists")
	}
}

// End to end: an ad phrase inside a 30 s block adds the factor and the
// evidence family; the result reports the hit, never the caption text.
func TestPhraseFactorEndToEnd(t *testing.T) {
	s := newScenario(blk{D: 130 * sec, CC: "pop"}, blk{D: 30 * sec}, blk{D: 63 * sec, CC: "pop"})
	evs := s.events()
	// Put "ask your doctor if" on the frames starting at 140 s.
	text := cc608Text("ASK YOUR DOCTOR IF ZEBRAS ")
	k := 0
	for i := range evs {
		if evs[i].Kind == EventVideo && evs[i].Video.PTS-evs[0].Video.PTS >= 140_000_000 && k < len(text) {
			evs[i].Video.CC = text[k]
			k++
		}
	}
	cfg := testConfig()
	cfg.Captions.AdPhrases = []string{"Ask your doctor if"}
	cfg.Scoring.AdPhraseFactor = 3
	r, err := Analyze(context.Background(), &SliceSource{Events: evs}, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Diagnostics.Events.Phrases) != 1 || r.Diagnostics.Events.Phrases[0].Phrase != "Ask your doctor if" {
		t.Fatalf("phrase hits %+v", r.Diagnostics.Events.Phrases)
	}
	b := blockSpan(t, r, 130*sec, 160*sec)
	if f := findFactor(b.Factors, RuleCaptionAdPhrase); f == nil || f.Multiplier != 3 {
		t.Errorf("ad block factors %s", rules(b.Factors))
	}
	if js := mustJSON(t, r); strings.Contains(strings.ToLower(js), "zebra") {
		t.Errorf("caption text leaked into the result")
	}
	// Without lists nothing is decoded or reported.
	r, _ = Analyze(context.Background(), &SliceSource{Events: evs}, testConfig(), nil)
	if len(r.Diagnostics.Events.Phrases) != 0 {
		t.Errorf("phrases reported without lists")
	}
}

// Phrase lists load from YAML sequences; a phrase without letters or digits
// is a configuration error.
func TestPhraseConfig(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.LoadYAML([]byte("captions:\n  ad_phrases: [\"ask your doctor if\", \"is responsible for the content of this ad\"]\nscoring:\n  caption_ad_phrase_factor: 2\n"), "x.yaml", "."); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Captions.AdPhrases) != 2 || cfg.Captions.AdPhrases[1] != "is responsible for the content of this ad" {
		t.Errorf("ad phrases = %q", cfg.Captions.AdPhrases)
	}
	cfg.Captions.ProgramPhrases = []string{"--"}
	if err := cfg.Validate(); KindOf(err) != KindInvalidConfig {
		t.Errorf("empty phrase accepted: %v", err)
	}
}
