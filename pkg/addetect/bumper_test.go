// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// AT-10: known bumper template matching (BUMPER-01..03).

// card renders a transition card: a checkerboard of cols x rows cells.
func card(w, h, cols, rows int) []byte {
	p := make([]byte, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := byte(60)
			if (x*cols/w+y*rows/h)%2 == 0 {
				v = 190
			}
			p[y*w+x] = v
		}
	}
	return p
}

func cardPNG(t *testing.T, w, h, cols, rows int) []byte {
	t.Helper()
	p := card(w, h, cols, rows)
	return encodeGray(t, w, h, func(x, y int) byte { return p[y*w+x] })
}

func cardTemplate(t *testing.T) BumperTemplate {
	return BumperTemplate{
		ID: "station_card", PNG: cardPNG(t, 128, 72, 8, 6), Region: Region{0, 0, 1, 1},
		Geometry: "stretch", Threshold: 0.9, MinSupport: 400 * time.Millisecond,
	}
}

func bumperConfig(t *testing.T, tmpl ...BumperTemplate) *Config {
	cfg := testConfig()
	if len(tmpl) == 0 {
		tmpl = []BumperTemplate{cardTemplate(t)}
	}
	cfg.BumperTemplates = tmpl
	return cfg
}

// bumperScenario: program, the card for 2 s, 30 s, the card again, program.
func bumperScenario(pic []byte) *scenario {
	return newScenario(
		blk{D: 47 * sec},
		blk{D: 2 * sec, Pic: pic, Sep: sepNone},
		blk{D: 30 * sec, Sep: sepNone},
		blk{D: 2 * sec, Pic: pic, Sep: sepNone},
		blk{D: 53 * sec, Sep: sepNone},
	)
}

// AT-10 / BUMPER-01, BUMPER-02: a supplied template matches the card, emits
// an event with identity, interval, quality and uncertainty, and supplies
// boundaries; match-only evidence never labels the following time as an ad.
func TestBumperMatch(t *testing.T) {
	r := bumperScenario(card(tW, tH, 8, 6)).run(t, bumperConfig(t))
	ev := r.Diagnostics.Events.Bumpers
	if len(ev) != 2 {
		t.Fatalf("bumper events = %+v", ev)
	}
	for i, want := range [][2]int64{{47_000_000, 49_000_000}, {79_000_000, 81_000_000}} {
		e := ev[i]
		if e.TemplateID != "station_card" || e.StartUS != want[0] || e.EndUS != want[1] || e.Quality < 0.99 || e.Quality > 1 || e.UncertaintyUS != 200_000 {
			t.Errorf("event %d = %+v", i, e)
		}
	}
	d := detector(t, r, SignalBumpers)
	if d.Status != StatusUsable || d.Events != 2 || !d.Boundary || *d.CoverageUS != r.DurationUS {
		t.Errorf("detector = %+v", d)
	}
	wantBoundaries(t, r, 0, 47, 49, 79, 81, 134)
	b := mustBoundary(t, r, 47*sec)
	if len(b.Causes) != 1 || !hasCause(b, SignalBumpers, "match_start", "boundary") || b.Causes[0].Detail != "station_card" || b.UncertaintyUS != 200_000 || len(b.Evidence) != 0 {
		t.Errorf("start boundary = %+v", b)
	}
	if b := mustBoundary(t, r, 49*sec); !hasCause(b, SignalBumpers, "match_end", "boundary") {
		t.Errorf("end boundary = %+v", b)
	}

	// BUMPER-02 / SCORE-06: the 30 s between two bumpers reaches the score
	// threshold on duration, but a bumper match alone is not evidence.
	if len(r.AdBreaks) != 0 {
		t.Fatalf("bumper match labeled following time as an ad: %s", timeline(r))
	}
	wantTimeline(t, r, "U0-134")
	h := hypothesis(t, r, 49*sec, 79*sec)
	if h.Score != 2 || h.Eligible || !hasString(h.Reasons, "no_evidence_family") {
		t.Errorf("hypothesis = %+v", h)
	}

	// Which edges supply boundaries is configurable.
	cfg := bumperConfig(t)
	cfg.Bumpers.Boundary = "start"
	wantBoundaries(t, bumperScenario(card(tW, tH, 8, 6)).run(t, cfg), 0, 47, 79, 134)
	cfg.Bumpers.Boundary = "end"
	wantBoundaries(t, bumperScenario(card(tW, tH, 8, 6)).run(t, cfg), 0, 49, 81, 134)
	cfg = bumperConfig(t)
	cfg.Boundaries.FromBumpers = false
	r = bumperScenario(card(tW, tH, 8, 6)).run(t, cfg)
	wantBoundaries(t, r, 0, 134)
	if d := detector(t, r, SignalBumpers); d.Events != 2 || d.Boundary {
		t.Errorf("detector = %+v", d)
	}
}

// AT-10 / BUMPER-01: an unrelated picture does not match; the metric is
// invariant to brightness offset and contrast gain; threshold and minimum
// temporal support are per template.
func TestBumperUnrelatedAndInvariance(t *testing.T) {
	// A different card (other cell layout) and ordinary content.
	for name, pic := range map[string][]byte{
		"other card": card(tW, tH, 5, 3),
		"stripes":    card(tW, tH, 16, 1),
		"program":    picture(tW, tH, 120, 0, 0, false),
		"inverted":   invert(card(tW, tH, 8, 6)),
	} {
		r := bumperScenario(pic).run(t, bumperConfig(t))
		if n := len(r.Diagnostics.Events.Bumpers); n != 0 {
			t.Errorf("%s matched: %+v", name, r.Diagnostics.Events.Bumpers)
		}
		wantBoundaries(t, r, 0, 134)
	}

	// Darker and lower contrast: still the same card.
	dim := card(tW, tH, 8, 6)
	for i, v := range dim {
		dim[i] = byte(float64(v)*0.4 + 20)
	}
	r := bumperScenario(dim).run(t, bumperConfig(t))
	if ev := r.Diagnostics.Events.Bumpers; len(ev) != 2 || ev[0].Quality < 0.99 {
		t.Errorf("dimmed card: %+v", ev)
	}

	// A degraded card (half blended with another picture) falls below the
	// 0.9 threshold and matches with a lower one.
	other := card(tW, tH, 5, 3)
	blend := card(tW, tH, 8, 6)
	for i := range blend {
		blend[i] = byte((int(blend[i]) + int(other[i])) / 2)
	}
	if n := len(bumperScenario(blend).run(t, bumperConfig(t)).Diagnostics.Events.Bumpers); n != 0 {
		t.Errorf("degraded card matched at threshold 0.9")
	}
	loose := cardTemplate(t)
	loose.Threshold = 0.5
	r = bumperScenario(blend).run(t, bumperConfig(t, loose))
	if ev := r.Diagnostics.Events.Bumpers; len(ev) != 2 || ev[0].Quality < 0.5 || ev[0].Quality >= 0.9 {
		t.Errorf("degraded card at threshold 0.5: %+v", ev)
	}

	// Minimum temporal support: a 200 ms flash of the card is not a match.
	s := newScenario(blk{D: 47 * sec}, blk{D: 200 * ms, Pic: card(tW, tH, 8, 6), Sep: sepNone}, blk{D: 53 * sec, Sep: sepNone})
	if n := len(s.run(t, bumperConfig(t)).Diagnostics.Events.Bumpers); n != 0 {
		t.Errorf("200 ms flash matched with min_support 400 ms")
	}
	short := cardTemplate(t)
	short.MinSupport = 200 * time.Millisecond
	if n := len(s.run(t, bumperConfig(t, short)).Diagnostics.Events.Bumpers); n != 1 {
		t.Errorf("200 ms flash with min_support 200 ms: %d events", n)
	}
}

func invert(p []byte) []byte {
	out := make([]byte, len(p))
	for i, v := range p {
		out[i] = 255 - v
	}
	return out
}

// AT-10 / BUMPER-01: the template is matched inside its normalized region
// only.
func TestBumperRegion(t *testing.T) {
	region := Region{0.25, 0.25, 0.5, 0.5} // 32x18 analysis pixels at 16,9
	tmpl := BumperTemplate{ID: "corner_bug", PNG: cardPNG(t, 64, 36, 8, 6), Region: region, Geometry: "strict", Threshold: 0.9, MinSupport: 400 * time.Millisecond}
	paint := func(x0, y0 int) []byte {
		p := picture(tW, tH, 120, 0, 0, false)
		c := card(32, 18, 8, 6)
		for y := 0; y < 18; y++ {
			copy(p[(y0+y)*tW+x0:], c[y*32:(y+1)*32])
		}
		return p
	}
	r := bumperScenario(paint(16, 9)).run(t, bumperConfig(t, tmpl))
	ev := r.Diagnostics.Events.Bumpers
	if len(ev) != 2 || ev[0].TemplateID != "corner_bug" || ev[0].StartUS != 47_000_000 || ev[0].Quality < 0.99 {
		t.Fatalf("in-region events = %+v", ev)
	}
	// The same graphic elsewhere in the picture is not this template.
	if n := len(bumperScenario(paint(30, 2)).run(t, bumperConfig(t, tmpl)).Diagnostics.Events.Bumpers); n != 0 {
		t.Errorf("graphic outside its region matched")
	}
	// Whatever surrounds the region is irrelevant.
	busy := paint(16, 9)
	for y := 0; y < 9; y++ {
		for x := 0; x < tW; x++ {
			busy[y*tW+x] = byte(40 + 5*x%200)
		}
	}
	if n := len(bumperScenario(busy).run(t, bumperConfig(t, tmpl)).Diagnostics.Events.Bumpers); n != 2 {
		t.Errorf("content outside the region affected the match: %d events", n)
	}

	// Two templates are matched independently.
	full := cardTemplate(t)
	r = newScenario(
		blk{D: 20 * sec},
		blk{D: 2 * sec, Pic: card(tW, tH, 8, 6), Sep: sepNone},
		blk{D: 20 * sec, Sep: sepNone},
		blk{D: 2 * sec, Pic: paint(16, 9), Sep: sepNone},
		blk{D: 20 * sec, Sep: sepNone},
	).run(t, bumperConfig(t, full, tmpl))
	ev = r.Diagnostics.Events.Bumpers
	if len(ev) != 2 || ev[0].TemplateID != "station_card" || ev[0].StartUS != 20_000_000 || ev[1].TemplateID != "corner_bug" || ev[1].StartUS != 42_000_000 {
		t.Errorf("two templates: %+v", ev)
	}
}

// BUMPER-01: the geometry policy. A strict template whose shape differs
// from its region is unavailable (with a warning) rather than stretched.
func TestBumperGeometryPolicy(t *testing.T) {
	square := BumperTemplate{ID: "square", PNG: cardPNG(t, 64, 64, 8, 6), Region: Region{0, 0, 1, 1}, Geometry: "strict", Threshold: 0.9, MinSupport: 400 * time.Millisecond}
	r := bumperScenario(card(tW, tH, 8, 6)).run(t, bumperConfig(t, square))
	if n := len(r.Diagnostics.Events.Bumpers); n != 0 {
		t.Errorf("strict template with the wrong shape matched")
	}
	if !hasWarning(r, "bumper_geometry_mismatch") {
		t.Errorf("warnings = %+v", r.Warnings)
	}
	square.Geometry = "stretch"
	r = bumperScenario(card(tW, tH, 8, 6)).run(t, bumperConfig(t, square))
	if n := len(r.Diagnostics.Events.Bumpers); n != 2 {
		t.Errorf("stretched template: %d events", n)
	}

	tiny := BumperTemplate{ID: "tiny", PNG: cardPNG(t, 64, 36, 8, 6), Region: Region{0.5, 0.5, 0.03, 0.05}, Geometry: "stretch", Threshold: 0.9}
	r = bumperScenario(card(tW, tH, 8, 6)).run(t, bumperConfig(t, tiny))
	if !hasWarning(r, "bumper_region_too_small") || len(r.Diagnostics.Events.Bumpers) != 0 {
		t.Errorf("tiny region: warnings %+v", r.Warnings)
	}
}

// AT-10 / BUMPER-02: templates are not required; an enabled matcher without
// templates is unavailable and neutral.
func TestBumperNoTemplates(t *testing.T) {
	r := bumperScenario(card(tW, tH, 8, 6)).run(t, nil)
	d := detector(t, r, SignalBumpers)
	if d.Status != StatusUnavailable || d.Reason != "no_templates" || d.Boundary || d.CoverageUS != nil {
		t.Errorf("detector = %+v", d)
	}
	if len(r.Diagnostics.Events.Bumpers) != 0 {
		t.Errorf("events without templates")
	}

	cfg := bumperConfig(t)
	cfg.Signals.Bumpers = false
	r = bumperScenario(card(tW, tH, 8, 6)).run(t, cfg)
	if d := detector(t, r, SignalBumpers); d.Status != StatusDisabled || d.Boundary {
		t.Errorf("detector = %+v", d)
	}
	if len(r.Diagnostics.Events.Bumpers) != 0 {
		t.Errorf("disabled matcher produced events")
	}
	wantBoundaries(t, r, 0, 134)
}

// AT-10 / BUMPER-03: invalid template assets fail configuration validation
// before any analysis.
func TestBumperAssetValidation(t *testing.T) {
	good := cardPNG(t, 64, 36, 8, 6)
	dir := t.TempDir()
	path := filepath.Join(dir, "card.png")
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatal(err)
	}
	base := func() BumperTemplate {
		return BumperTemplate{ID: "card", PNG: good, Region: Region{0, 0, 1, 1}, Geometry: "stretch", Threshold: 0.9, MinSupport: 400 * time.Millisecond}
	}

	// Valid: caller data, a file, and a file with a matching checksum.
	for name, mut := range map[string]func(*BumperTemplate){
		"data":            func(*BumperTemplate) {},
		"file":            func(b *BumperTemplate) { b.PNG, b.Image = nil, path },
		"file and sha256": func(b *BumperTemplate) { b.PNG, b.Image, b.SHA256 = nil, path, sha(good) },
	} {
		cfg := testConfig()
		b := base()
		mut(&b)
		cfg.BumperTemplates = []BumperTemplate{b}
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s: Validate: %v", name, err)
		}
		as, err := LoadAssets(cfg)
		if err != nil || len(as.bumpers) != 1 || as.digests["bumpers.templates.card.image"] != sha(good) {
			t.Errorf("%s: LoadAssets: %v", name, err)
		}
	}

	flat := encodeGray(t, 64, 36, func(int, int) byte { return 100 })
	small := cardPNG(t, 4, 4, 2, 2)
	assetCases := []struct {
		name string
		mut  func(*BumperTemplate)
		want string
	}{
		{"not a png", func(b *BumperTemplate) { b.PNG = []byte("GIF89a....") }, "not a valid PNG"},
		{"truncated png", func(b *BumperTemplate) { b.PNG = good[:len(good)/2] }, "not a valid PNG"},
		{"too small", func(b *BumperTemplate) { b.PNG = small }, "dimensions"},
		{"no structure", func(b *BumperTemplate) { b.PNG = flat }, "no luminance structure"},
		{"integrity", func(b *BumperTemplate) { b.SHA256 = sha([]byte("something else")) }, "integrity"},
		{"missing file", func(b *BumperTemplate) { b.PNG, b.Image = nil, filepath.Join(dir, "nope.png") }, "nope.png"},
	}
	for _, c := range assetCases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testConfig()
			b := base()
			c.mut(&b)
			cfg.BumperTemplates = []BumperTemplate{b}
			_, err := LoadAssets(cfg)
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("LoadAssets: %v, want invalid config mentioning %q", err, c.want)
			}
			res, err := Analyze(context.Background(), &SliceSource{Events: frameSeq(rep(content, 5)...)}, cfg, nil)
			if !errors.Is(err, ErrInvalidConfig) || res != nil {
				t.Errorf("Analyze: result %v, err %v", res, err)
			}
		})
	}

	fieldCases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"no id", func(c *Config) { c.BumperTemplates[0].ID = "" }, "no id"},
		{"duplicate id", func(c *Config) { c.BumperTemplates = append(c.BumperTemplates, c.BumperTemplates[0]) }, "duplicate"},
		{"no image", func(c *Config) { c.BumperTemplates[0].PNG = nil }, "image is required"},
		{"region outside", func(c *Config) { c.BumperTemplates[0].Region = Region{0.5, 0.5, 0.6, 0.2} }, "region"},
		{"empty region", func(c *Config) { c.BumperTemplates[0].Region = Region{0.5, 0.5, 0, 0.2} }, "region"},
		{"geometry", func(c *Config) { c.BumperTemplates[0].Geometry = "crop" }, "geometry"},
		{"threshold zero", func(c *Config) { c.BumperTemplates[0].Threshold = 0 }, "threshold"},
		{"threshold above one", func(c *Config) { c.BumperTemplates[0].Threshold = 1.2 }, "threshold"},
		{"negative support", func(c *Config) { c.BumperTemplates[0].MinSupport = -time.Second }, "min_support"},
	}
	for _, c := range fieldCases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.BumperTemplates = []BumperTemplate{base()}
			c.mut(cfg)
			err := cfg.Validate()
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate: %v, want invalid config mentioning %q", err, c.want)
			}
		})
	}
}

func TestAreaResample(t *testing.T) {
	src := []uint8{
		0, 0, 100, 100,
		0, 0, 100, 100,
		200, 200, 50, 50,
		200, 200, 50, 51,
	}
	got := areaResample(src, 4, 4, 2, 2)
	if got[0] != 0 || got[1] != 100 || got[2] != 200 || got[3] != 50 {
		t.Errorf("2x reduction = %v", got)
	}
	// Same size is the identity; enlarging repeats source pixels.
	same := areaResample(src, 4, 4, 4, 4)
	for i := range src {
		if same[i] != src[i] {
			t.Fatalf("identity resample differs at %d", i)
		}
	}
	up := areaResample([]uint8{10, 20}, 2, 1, 4, 2)
	if len(up) != 8 || up[0] != 10 || up[1] != 10 || up[2] != 20 || up[7] != 20 {
		t.Errorf("enlarged = %v", up)
	}
}
