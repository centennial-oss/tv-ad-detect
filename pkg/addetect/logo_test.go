// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// AT-04: station logo learning, presence, smoothing, validation and assets
// (LOGO-01..05, BUMPER-03).

// logoScenario: program with the logo, a 30 s logo-free ad, a 30 s block
// with the logo, more program. The logo is on screen 80% of the time.
func logoScenario() *scenario {
	return newScenario(
		blk{D: 50 * sec, Logo: true},
		blk{D: 30 * sec},
		blk{D: 30 * sec, Logo: true},
		blk{D: 40 * sec, Logo: true},
	)
}

func logoStates(r *Result) string {
	var sb strings.Builder
	for i, s := range r.Diagnostics.Events.Logo {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(s.State)
	}
	return sb.String()
}

// AT-04 / LOGO-01, LOGO-03: a stationary edge pattern over changing content
// is learned from many separated samples; presence intervals follow it.
func TestLogoLearnedFromSeparatedSamples(t *testing.T) {
	r := logoScenario().run(t, nil)
	if len(r.Calibration.Logo) != 1 {
		t.Fatalf("logo calibration = %+v", r.Calibration.Logo)
	}
	c := r.Calibration.Logo[0]
	if c.Origin != "learned" || c.Status != StatusUsable || c.Failed != "" {
		t.Fatalf("calibration = %+v", c)
	}
	// One learning observation per 2 s (logo.learn_min_separation), far fewer
	// than the 300 presence observations (logo.sample_interval).
	if c.Samples != 300 || c.LearnSamples < 60 || c.LearnSamples > 75 {
		t.Errorf("samples = %d learn samples = %d", c.Samples, c.LearnSamples)
	}
	if c.StartUS != 0 || c.EndUS != r.DurationUS {
		t.Errorf("calibration span [%d,%d)", c.StartUS, c.EndUS)
	}
	// The learned box is the logo (edge pixels straddle its outline by one
	// pixel), not the picture.
	x0, y0, x1, y1 := logoRect(tW, tH)
	bx0, by0 := c.Box.X*tW, c.Box.Y*tH
	bx1, by1 := bx0+c.Box.W*tW, by0+c.Box.H*tH
	if math.Abs(bx0-float64(x0)) > 1.01 || math.Abs(by0-float64(y0)) > 1.01 || math.Abs(bx1-float64(x1)) > 1.01 || math.Abs(by1-float64(y1)) > 1.01 {
		t.Errorf("learned box [%v,%v)-[%v,%v), logo is [%d,%d)-[%d,%d)", bx0, by0, bx1, by1, x0, y0, x1, y1)
	}
	if c.EdgePixels < 24 {
		t.Errorf("edge pixels = %d", c.EdgePixels)
	}
	if c.Occupancy == nil || math.Abs(*c.Occupancy-0.8) > 0.01 || c.Coverage == nil || *c.Coverage != 1 {
		t.Errorf("occupancy = %v coverage = %v", c.Occupancy, c.Coverage)
	}

	// Presence intervals: present, absent, present, with transitions within
	// one sample interval (reported as the transition uncertainty).
	iv := r.Diagnostics.Events.Logo
	if logoStates(r) != "present absent present" {
		t.Fatalf("logo intervals = %+v", iv)
	}
	for i, want := range []int64{50_000_000, 80_000_000} {
		got := iv[i+1].StartUS
		if got < want || got > want+500_000 || iv[i].EndUS != got || iv[i+1].TransitionUncertaintyUS != 500_000 {
			t.Errorf("transition %d at %d, want within 500 ms after %d (uncertainty %d)", i, got, want, iv[i+1].TransitionUncertaintyUS)
		}
	}
	if iv[0].StartUS != 0 || iv[2].EndUS != r.DurationUS {
		t.Errorf("intervals do not span the recording: %+v", iv)
	}
	if iv[0].MeanMatch < 0.9 || iv[1].MeanMatch > 0.1 {
		t.Errorf("mean match present %v absent %v", iv[0].MeanMatch, iv[1].MeanMatch)
	}

	// LOGO-03: block occupancy is present time over known time.
	ad := blockSpan(t, r, 50*sec, 80*sec)
	if ad.LogoOccupancy == nil || *ad.LogoOccupancy > 0.02 || *ad.LogoKnownUS != 30_000_000 {
		t.Errorf("ad block occupancy %v known %v", ad.LogoOccupancy, ad.LogoKnownUS)
	}
	if p := blockSpan(t, r, 0, 50*sec); *p.LogoOccupancy != 1 {
		t.Errorf("program block occupancy %v", *p.LogoOccupancy)
	}

	d := detector(t, r, SignalLogo)
	if d.Status != StatusUsable || d.Boundary || *d.CoverageUS != r.DurationUS {
		t.Errorf("detector = %+v", d)
	}
	// Logo transitions are not boundaries by default (BOUND-01).
	for _, b := range r.Diagnostics.Boundaries {
		if hasCause(&b, SignalLogo, "", "") {
			t.Errorf("logo boundary %+v although boundaries.from_logo is off", b)
		}
	}
	wantTimeline(t, r, "P0-50 A50-80 P80-150")
}

// LOGO-05 / BOUND-01: validated logo transitions supply boundaries only when
// explicitly enabled; they are quantized to a frame and carry the sampling
// uncertainty.
func TestLogoBoundariesWhenEnabled(t *testing.T) {
	s := newScenario(
		blk{D: 50 * sec, Logo: true},
		blk{D: 30 * sec, Sep: sepNone},
		blk{D: 70 * sec, Logo: true, Sep: sepNone},
	)
	r := s.run(t, nil)
	wantBoundaries(t, r, 0, 150)

	cfg := testConfig()
	cfg.Boundaries.FromLogo = true
	r = s.run(t, cfg)
	if len(r.Diagnostics.Boundaries) != 4 {
		t.Fatalf("boundaries = %v", boundaryTimes(r))
	}
	b1, b2 := r.Diagnostics.Boundaries[1], r.Diagnostics.Boundaries[2]
	if !hasCause(&b1, SignalLogo, "disappeared", "boundary") || !hasCause(&b2, SignalLogo, "appeared", "boundary") {
		t.Errorf("causes = %+v / %+v", b1.Causes, b2.Causes)
	}
	if b1.TimeUS != 50_000_000 || b2.TimeUS != 80_000_000 || b1.UncertaintyUS != 500_000 {
		t.Errorf("logo boundaries at %d and %d (uncertainty %d)", b1.TimeUS, b2.TimeUS, b1.UncertaintyUS)
	}
	// A logo transition is not itself a SCORE-06 boundary family, but
	// validated absence is evidence: strict 30 s x logo absent.
	if len(b1.Evidence) != 0 {
		t.Errorf("logo boundary evidence = %v", b1.Evidence)
	}
	if d := detector(t, r, SignalLogo); !d.Boundary {
		t.Errorf("detector not reported as a boundary source")
	}
	wantTimeline(t, r, "P0-50 A50-80 P80-150")
	u := r.AdUnits[0]
	if u.Score != 4 || !hasString(u.EvidenceFamilies, "logo_absent") || len(u.EvidenceFamilies) != 1 {
		t.Errorf("unit = %+v", u)
	}
}

// AT-04 / LOGO-03: time-based smoothing. A state change shorter than
// logo.min_state_duration (fade, occlusion, a logo-like graphic) does not
// change the presence state.
func TestLogoSmoothing(t *testing.T) {
	s := newScenario(
		blk{D: 30 * sec, Logo: true},
		blk{D: 1 * sec, Sep: sepNone}, // logo briefly occluded
		blk{D: 29 * sec, Logo: true, Sep: sepNone},
		blk{D: 14 * sec, Sep: sepNone},
		blk{D: 1 * sec, Logo: true, Sep: sepNone}, // logo-like flash inside the ad
		blk{D: 15 * sec, Sep: sepNone},
		blk{D: 60 * sec, Logo: true, Sep: sepNone},
	)
	r := s.run(t, nil)
	if got := logoStates(r); got != "present absent present" {
		t.Fatalf("smoothed states = %s: %+v", got, r.Diagnostics.Events.Logo)
	}
	iv := r.Diagnostics.Events.Logo
	if d := iv[1].StartUS - 60_000_000; d < 0 || d > 500_000 {
		t.Errorf("absent interval starts at %d", iv[1].StartUS)
	}
	if d := iv[2].StartUS - 90_000_000; d < 0 || d > 500_000 {
		t.Errorf("present interval resumes at %d", iv[2].StartUS)
	}

	// Without smoothing every change shows.
	cfg := testConfig()
	cfg.Logo.MinStateDuration = 0
	r = s.run(t, cfg)
	if got := logoStates(r); got != "present absent present absent present absent present" {
		t.Errorf("unsmoothed states = %s", got)
	}
}

func TestSmoothLogoSpans(t *testing.T) {
	sp := func(s, e int64, st uint8) logoSpan { return logoSpan{start: s, end: e, state: st} }
	show := func(spans []logoSpan) string {
		parts := make([]string, len(spans))
		for i, s := range spans {
			parts[i] = fmt.Sprintf("%s[%d,%d)", logoStateNames[s.state][:1], s.start, s.end)
		}
		return strings.Join(parts, " ")
	}
	cases := []struct {
		name string
		in   []logoSpan
		min  int64
		want string
	}{
		{"short run absorbed by predecessor", []logoSpan{sp(0, 10, logoPresent), sp(10, 11, logoAbsent), sp(11, 20, logoPresent)}, 2, "p[0,20)"},
		{"long runs kept", []logoSpan{sp(0, 10, logoPresent), sp(10, 15, logoAbsent), sp(15, 20, logoPresent)}, 2, "p[0,10) a[10,15) p[15,20)"},
		{"short first run joins successor", []logoSpan{sp(0, 1, logoAbsent), sp(1, 20, logoPresent)}, 2, "p[0,20)"},
		{"short unknown absorbed", []logoSpan{sp(0, 10, logoPresent), sp(10, 11, logoUnknown), sp(11, 20, logoAbsent)}, 2, "p[0,11) a[11,20)"},
		{"long unknown kept", []logoSpan{sp(0, 10, logoPresent), sp(10, 15, logoUnknown), sp(15, 20, logoPresent)}, 2, "p[0,10) u[10,15) p[15,20)"},
		{"smoothing off", []logoSpan{sp(0, 10, logoPresent), sp(10, 11, logoAbsent), sp(11, 20, logoPresent)}, 0, "p[0,10) a[10,11) p[11,20)"},
		{"single span", []logoSpan{sp(0, 1, logoPresent)}, 2, "p[0,1)"},
		{"consecutive short runs", []logoSpan{sp(0, 10, logoPresent), sp(10, 11, logoAbsent), sp(11, 12, logoUnknown), sp(12, 13, logoAbsent), sp(13, 20, logoPresent)}, 2, "p[0,20)"},
	}
	for _, c := range cases {
		got := smoothLogoSpans(append([]logoSpan(nil), c.in...), 0, c.min)
		if s := show(got); s != c.want {
			t.Errorf("%s: %s, want %s", c.name, s, c.want)
		}
		// Smoothing never changes the covered extent or creates gaps.
		if len(got) == 0 || got[0].start != c.in[0].start || got[len(got)-1].end != c.in[len(c.in)-1].end {
			t.Errorf("%s: extent changed: %s", c.name, show(got))
		}
		for i := 1; i < len(got); i++ {
			if got[i].start != got[i-1].end || got[i].state == got[i-1].state {
				t.Errorf("%s: spans not contiguous or not coalesced: %s", c.name, show(got))
			}
		}
	}
	// Spans before `first` belong to an earlier geometry epoch and are left
	// alone.
	in := []logoSpan{sp(0, 1, logoAbsent), sp(1, 10, logoPresent), sp(10, 11, logoAbsent), sp(11, 20, logoPresent)}
	got := smoothLogoSpans(in, 1, 2)
	if s := show(got); s != "a[0,1) p[1,20)" {
		t.Errorf("first offset: %s", s)
	}
}

// AT-04 / LOGO-04: a logo present almost all the time does not distinguish
// program from ads. Logo evidence is disabled for the recording: status
// unreliable with the failed validation, neutral factors, no logo
// boundaries, and no fabricated program label.
func TestLogoTooCommonIsRejected(t *testing.T) {
	s := newScenario(
		blk{D: 47 * sec, Logo: true},
		blk{D: 30 * sec, Logo: true},
		blk{D: 73 * sec, Logo: true},
	)
	cfg := testConfig()
	cfg.Boundaries.FromLogo = true
	exported := 0
	r, err := Analyze(context.Background(), s.source(), cfg, &Options{OnLogoLearned: func(*LogoAsset) { exported++ }})
	if err != nil {
		t.Fatal(err)
	}
	checkInvariants(t, r)
	c := r.Calibration.Logo[0]
	if c.Status != StatusUnreliable || c.Failed != "occupancy_above_maximum" || c.Occupancy == nil || *c.Occupancy != 1 || c.LearnSamples < 60 {
		t.Errorf("calibration = %+v", c)
	}
	d := detector(t, r, SignalLogo)
	if d.Status != StatusUnreliable || d.Reason != "occupancy_above_maximum" || d.Boundary {
		t.Errorf("detector = %+v", d)
	}
	if exported != 0 {
		t.Errorf("an invalid logo was offered for export")
	}
	// The logo state of the recording is unknown, never present or absent.
	if got := logoStates(r); got != "unknown" {
		t.Errorf("logo states = %s", got)
	}
	for _, b := range r.Diagnostics.Blocks {
		if b.LogoOccupancy != nil || b.LogoKnownUS != nil {
			t.Errorf("block %s reports logo occupancy for an invalid logo", b.ID)
		}
		if findFactor(b.Factors, "logo_present") != nil || findFactor(b.Factors, "logo_absent") != nil {
			t.Errorf("block %s has a logo factor: %s", b.ID, rules(b.Factors))
		}
		if hasString(b.ProgramEvidence, "logo_present") {
			t.Errorf("block %s has logo program evidence", b.ID)
		}
	}
	for _, b := range r.Diagnostics.Boundaries {
		if hasCause(&b, SignalLogo, "", "") {
			t.Errorf("logo boundary from an invalid logo: %+v", b)
		}
	}
	// LOGO-05: the channel is still analyzable through other signals (dark
	// boundaries and a standard duration), and nothing is called program.
	wantTimeline(t, r, "U0-47 A47-77 U77-150")
	if got := blockSpan(t, r, 47*sec, 77*sec).Score; got != 2 {
		t.Errorf("ad block score = %v, want 2 (logo contributes 1.0)", got)
	}
}

// AT-04 / LOGO-01, LOGO-04: a logo on screen too little of the time.
func TestLogoTooRareIsRejected(t *testing.T) {
	s := newScenario(
		blk{D: 47 * sec},
		blk{D: 30 * sec, Logo: true},
		blk{D: 73 * sec},
	)
	// With the default persistence the candidate is not even learned
	// (insufficiently supported candidates are rejected, LOGO-01).
	r := s.run(t, nil)
	c := r.Calibration.Logo[0]
	if c.Status != StatusUnavailable || c.Failed != "no_persistent_candidate" || c.Box != nil || c.Occupancy != nil {
		t.Errorf("calibration = %+v", c)
	}
	d := detector(t, r, SignalLogo)
	if d.Status != StatusUnavailable || d.Reason != "no_persistent_candidate" || d.Boundary {
		t.Errorf("detector = %+v", d)
	}

	// Learned with a low persistence requirement, it fails validation:
	// occupancy 0.2 is below logo.min_occupancy (0.40).
	cfg := testConfig()
	cfg.Logo.MinPersistence = 0.1
	cfg.Boundaries.FromLogo = true
	r = s.run(t, cfg)
	c = r.Calibration.Logo[0]
	if c.Status != StatusUnreliable || c.Failed != "occupancy_below_minimum" || c.Occupancy == nil || math.Abs(*c.Occupancy-0.2) > 0.01 {
		t.Errorf("calibration = %+v", c)
	}
	d = detector(t, r, SignalLogo)
	if d.Status != StatusUnreliable || d.Reason != "occupancy_below_minimum" || d.Boundary {
		t.Errorf("detector = %+v", d)
	}
	for _, b := range r.Diagnostics.Blocks {
		if findFactor(b.Factors, "logo_present") != nil || findFactor(b.Factors, "logo_absent") != nil || b.LogoOccupancy != nil {
			t.Errorf("block %s uses an invalid logo: %s", b.ID, rules(b.Factors))
		}
	}
	// The 30 s block is an ad by duration and dark boundaries alone (2.0),
	// not 2.0 x 0.1 from the "logo".
	if got := blockSpan(t, r, 47*sec, 77*sec).Score; got != 2 {
		t.Errorf("score = %v", got)
	}

	// The occupancy limits are configurable (LOGO-04).
	cfg = testConfig()
	cfg.Logo.MinPersistence = 0.1
	cfg.Logo.MinOccupancy = 0.15
	r = s.run(t, cfg)
	if c := r.Calibration.Logo[0]; c.Status != StatusUsable {
		t.Errorf("with min_occupancy 0.15: %+v", c)
	}
}

// LOGO-01: insufficient learning support, static scenes and persistent
// picture borders.
func TestLogoLearningRejections(t *testing.T) {
	t.Run("too few learning observations", func(t *testing.T) {
		s := newScenario(blk{D: 20 * sec, Logo: true}, blk{D: 10 * sec})
		r := s.run(t, nil)
		c := r.Calibration.Logo[0]
		if c.Status != StatusUnavailable || c.Failed != "insufficient_learning_support" || c.LearnSamples >= 60 {
			t.Errorf("calibration = %+v", c)
		}
		if d := detector(t, r, SignalLogo); d.Status != StatusUnavailable || d.Reason != "insufficient_learning_support" {
			t.Errorf("detector = %+v", d)
		}
	})
	t.Run("static scene adds no learning support", func(t *testing.T) {
		// 150 s of one unchanging picture: every edge persists, so nothing
		// can be told apart from a logo.
		s := newScenario(blk{D: 150 * sec, Logo: true, Level: 120})
		r := s.run(t, nil)
		c := r.Calibration.Logo[0]
		if c.LearnSamples != 1 || c.Status != StatusUnavailable || c.Failed != "insufficient_learning_support" {
			t.Errorf("calibration = %+v", c)
		}
	})
	t.Run("persistent border is not the logo", func(t *testing.T) {
		// A constant letterbox edge crosses the whole search region.
		s := newScenario(
			blk{D: 50 * sec, Logo: true, LB: 2},
			blk{D: 30 * sec, LB: 2},
			blk{D: 70 * sec, Logo: true, LB: 2},
		)
		r := s.run(t, nil)
		c := r.Calibration.Logo[0]
		if c.Status != StatusUsable {
			t.Fatalf("calibration = %+v", c)
		}
		if top := c.Box.Y * tH; top < 3.5 {
			t.Errorf("learned box starts at row %v: it includes the letterbox edge", top)
		}
		if w := c.Box.W * tW; w > 11 {
			t.Errorf("learned box is %v pixels wide", w)
		}
	})
	t.Run("logo disabled", func(t *testing.T) {
		cfg := testConfig()
		cfg.Signals.Logo = false
		r := logoScenario().run(t, cfg)
		if d := detector(t, r, SignalLogo); d.Status != StatusDisabled || d.CoverageUS != nil {
			t.Errorf("detector = %+v", d)
		}
		if len(r.Calibration.Logo) != 0 || len(r.Diagnostics.Events.Logo) != 0 {
			t.Errorf("disabled logo produced output")
		}
		// AT-13: disabled logo evidence contributes 1.0.
		wantTimeline(t, r, "U0-50 A50-110 U110-150")
	})
}

// learnedAsset runs the logo scenario and returns the exported asset.
func learnedAsset(t *testing.T) *LogoAsset {
	t.Helper()
	var got []*LogoAsset
	r, err := Analyze(context.Background(), logoScenario().source(), testConfig(), &Options{OnLogoLearned: func(a *LogoAsset) { got = append(got, a) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || r.Calibration.Logo[0].Status != StatusUsable {
		t.Fatalf("exported %d assets", len(got))
	}
	return got[0]
}

// AT-04 / LOGO-02, BUMPER-03: a validated learned logo is offered for
// export, round-trips through the documented asset format and, supplied
// back, produces the same downstream contract.
func TestLogoAssetRoundTrip(t *testing.T) {
	a := learnedAsset(t)
	if a.Format != LogoAssetFormat || a.Version != LogoAssetVersion || a.AnalysisWidth != tW || a.AnalysisHeight != tH {
		t.Fatalf("asset = %+v", a)
	}
	x0, y0, _, _ := logoRect(tW, tH)
	if a.X < x0-1 || a.X > x0 || a.Y < y0-1 || a.Y > y0 || a.Width < 8 || a.Width > 10 || a.Height < 3 || a.Height > 5 {
		t.Errorf("asset box %d,%d %dx%d", a.X, a.Y, a.Width, a.Height)
	}
	data, err := a.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseLogoAsset(data)
	if err != nil {
		t.Fatalf("ParseLogoAsset: %v", err)
	}
	again, _ := parsed.Marshal()
	if !bytes.Equal(data, again) {
		t.Errorf("asset does not round-trip:\n%s\n%s", data, again)
	}

	learned := logoScenario().run(t, nil)

	// Supplied as caller data.
	cfg := testConfig()
	cfg.LogoAssetData = parsed
	r := logoScenario().run(t, cfg)
	c := r.Calibration.Logo[0]
	if c.Origin != "asset" || c.Status != StatusUsable || c.AssetVersion != "learned" || c.LearnSamples != 0 {
		t.Errorf("calibration = %+v", c)
	}
	if mustJSON(t, r.Diagnostics.Events.Logo) != mustJSON(t, learned.Diagnostics.Events.Logo) {
		t.Errorf("asset intervals differ from learned intervals:\n%s\n%s", mustJSON(t, r.Diagnostics.Events.Logo), mustJSON(t, learned.Diagnostics.Events.Logo))
	}
	if timeline(r) != timeline(learned) {
		t.Errorf("timeline %s vs %s", timeline(r), timeline(learned))
	}
	if got := r.Config.Effective["logo.asset_data"]; !strings.HasPrefix(got, "sha256:") || len(got) != 7+64 {
		t.Errorf("effective logo.asset_data = %q", got)
	}

	// Supplied as a file (logo.asset).
	path := filepath.Join(t.TempDir(), "station.logo.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = testConfig()
	cfg.Logo.Asset = path
	r2 := logoScenario().run(t, cfg)
	if mustJSON(t, r2.Diagnostics.Events.Logo) != mustJSON(t, r.Diagnostics.Events.Logo) || r2.Calibration.Logo[0].Origin != "asset" {
		t.Errorf("file asset behaves differently from data asset")
	}
	// The effective configuration is redacted to the base name.
	if got := r2.Config.Effective["logo.asset"]; got != "station.logo.json" {
		t.Errorf("effective logo.asset = %q", got)
	}

	// An asset makes logo evidence available where learning is impossible:
	// a recording too short to learn from.
	short := newScenario(blk{D: 17 * sec, Logo: true}, blk{D: 15 * sec}, blk{D: 13 * sec, Logo: true})
	if c := short.run(t, nil).Calibration.Logo[0]; c.Status != StatusUnavailable {
		t.Fatalf("short recording learned a logo: %+v", c)
	}
	cfg = testConfig()
	cfg.LogoAssetData = parsed
	r = short.run(t, cfg)
	if c := r.Calibration.Logo[0]; c.Status != StatusUsable || c.Origin != "asset" {
		t.Errorf("short recording with asset: %+v", c)
	}
	// 15 s is a unit but not a complete default break, so it stays unknown.
	wantTimeline(t, r, "P0-17 U17-32 P32-45")
	if b := blockSpan(t, r, 17*sec, 32*sec); findFactor(b.Factors, "logo_absent") == nil || b.Score != 4 {
		t.Errorf("15 s logo-free block: score %v factors %s", b.Score, rules(b.Factors))
	}

	// A supplied asset is validated like a learned one: over a recording
	// where the logo never leaves it is unreliable.
	always := newScenario(blk{D: 47 * sec, Logo: true}, blk{D: 30 * sec, Logo: true}, blk{D: 73 * sec, Logo: true})
	r = always.run(t, cfg)
	if c := r.Calibration.Logo[0]; c.Status != StatusUnreliable || c.Failed != "occupancy_above_maximum" || c.Origin != "asset" {
		t.Errorf("always-present asset logo: %+v", c)
	}
	if d := detector(t, r, SignalLogo); d.Status != StatusUnreliable {
		t.Errorf("detector = %+v", d)
	}
}

// AT-04 / LOGO-02: geometry compatibility. A different analysis size with
// the same shape uses the documented scaling; a different shape is reported
// as incompatible and never read as logo absence.
func TestLogoAssetGeometry(t *testing.T) {
	// Learn at 128x72, apply at 64x36 (a factor of two smaller).
	big := newScenario(
		blk{D: 50 * sec, Logo: true, W: 128, H: 72},
		blk{D: 30 * sec, W: 128, H: 72},
		blk{D: 70 * sec, Logo: true, W: 128, H: 72},
	)
	var asset *LogoAsset
	if _, err := Analyze(context.Background(), big.source(), testConfig(), &Options{OnLogoLearned: func(a *LogoAsset) { asset = a }}); err != nil {
		t.Fatal(err)
	}
	if asset == nil || asset.AnalysisWidth != 128 || asset.AnalysisHeight != 72 {
		t.Fatalf("asset = %+v", asset)
	}
	cfg := testConfig()
	cfg.LogoAssetData = asset
	r := logoScenario().run(t, cfg)
	c := r.Calibration.Logo[0]
	if c.Origin != "asset" || c.Status != StatusUsable {
		t.Fatalf("scaled asset: %+v", c)
	}
	if got := logoStates(r); got != "present absent present" {
		t.Errorf("scaled asset states = %s", got)
	}

	// 64x48 has a different shape: incompatible.
	odd := newScenario(
		blk{D: 47 * sec, Logo: true, W: 64, H: 48},
		blk{D: 30 * sec, W: 64, H: 48},
		blk{D: 73 * sec, Logo: true, W: 64, H: 48},
	)
	r = odd.run(t, cfg)
	c = r.Calibration.Logo[0]
	if c.Status != StatusUnavailable || c.Failed != "asset_incompatible_geometry" || c.Origin != "asset" {
		t.Errorf("incompatible asset: %+v", c)
	}
	d := detector(t, r, SignalLogo)
	if d.Status != StatusUnavailable || d.Reason != "asset_incompatible_geometry" {
		t.Errorf("detector = %+v", d)
	}
	for _, b := range r.Diagnostics.Blocks {
		if findFactor(b.Factors, "logo_absent") != nil || b.LogoOccupancy != nil {
			t.Errorf("block %s: incompatible geometry read as logo absence", b.ID)
		}
	}
	// With an asset selected the recording is not auto-learned instead.
	if c.LearnSamples != 0 {
		t.Errorf("learned although an asset was selected: %+v", c)
	}
}

func encodeGray(t testing.TB, w, h int, set func(x, y int) byte) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Pix[y*img.Stride+x] = set(x, y)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// AT-04 / BUMPER-03: integrity, dimension and format failures of a logo
// asset are configuration errors.
func TestLogoAssetValidation(t *testing.T) {
	mask := encodeGray(t, 8, 4, func(x, y int) byte {
		if x == 0 || y == 0 || x == 7 || y == 3 {
			return 255
		}
		return 0
	})
	valid := func() *LogoAsset {
		return &LogoAsset{
			Format: LogoAssetFormat, Version: LogoAssetVersion, AssetVersion: "v7",
			AnalysisWidth: 64, AnalysisHeight: 36, X: 47, Y: 5, Width: 8, Height: 4,
			MaskPNG: mask, MaskSHA256: sha(mask),
		}
	}
	data, _ := valid().Marshal()
	if _, err := ParseLogoAsset(data); err != nil {
		t.Fatalf("valid asset rejected: %v", err)
	}
	empty := encodeGray(t, 8, 4, func(int, int) byte { return 0 })
	cases := []struct {
		name string
		mut  func(a *LogoAsset)
		want string
	}{
		{"format", func(a *LogoAsset) { a.Format = "something-else" }, "format"},
		{"version", func(a *LogoAsset) { a.Version = 2 }, "version"},
		{"analysis geometry", func(a *LogoAsset) { a.AnalysisWidth = 8 }, "analysis geometry"},
		{"analysis geometry too large", func(a *LogoAsset) { a.AnalysisHeight = 9000 }, "analysis geometry"},
		{"box outside", func(a *LogoAsset) { a.X = 60 }, "does not fit"},
		{"negative position", func(a *LogoAsset) { a.Y = -1 }, "does not fit"},
		{"integrity", func(a *LogoAsset) { a.MaskSHA256 = sha([]byte("other")) }, "integrity"},
		{"integrity missing", func(a *LogoAsset) { a.MaskSHA256 = "" }, "integrity"},
		{"tampered mask", func(a *LogoAsset) { a.MaskPNG = append([]byte{}, a.MaskPNG...); a.MaskPNG[len(a.MaskPNG)-5] ^= 1 }, "integrity"},
		{"not a png", func(a *LogoAsset) { a.MaskPNG = []byte("not a png"); a.MaskSHA256 = sha(a.MaskPNG) }, "PNG"},
		{"mask dimensions", func(a *LogoAsset) { a.Width = 9 }, "mask is 8x4"},
		{"empty mask", func(a *LogoAsset) { a.MaskPNG = empty; a.MaskSHA256 = sha(empty) }, "no edge pixels"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := valid()
			c.mut(a)
			data, _ := a.Marshal()
			_, err := ParseLogoAsset(data)
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ParseLogoAsset: %v, want invalid config mentioning %q", err, c.want)
			}
			// Caller-provided asset data is validated before analysis.
			cfg := testConfig()
			cfg.LogoAssetData = a
			if _, err := LoadAssets(cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("LoadAssets: %v", err)
			}
			res, err := Analyze(context.Background(), &SliceSource{Events: frameSeq(rep(content, 5)...)}, cfg, nil)
			if !errors.Is(err, ErrInvalidConfig) || res != nil {
				t.Errorf("Analyze: result %v err %v", res, err)
			}
		})
	}
	for name, doc := range map[string]string{
		"malformed json": `{"format": "tvad-logo"`,
		"unknown field":  strings.Replace(string(data), `"format"`, `"surprise": 1, "format"`, 1),
		"wrong type":     strings.Replace(string(data), `"version": 1`, `"version": "one"`, 1),
		"empty":          ``,
	} {
		if _, err := ParseLogoAsset([]byte(doc)); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A selected asset file that is missing or invalid fails validation.
	cfg := testConfig()
	cfg.Logo.Asset = filepath.Join(t.TempDir(), "missing.json")
	if _, err := LoadAssets(cfg); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("missing asset file: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{"format":"nope"}`), 0o600)
	cfg.Logo.Asset = bad
	if _, err := LoadAssets(cfg); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("invalid asset file: %v", err)
	}
	// Caller data takes precedence over the path.
	cfg.LogoAssetData = valid()
	as, err := LoadAssets(cfg)
	if err != nil || as.Logo == nil || as.Logo.AssetVersion != "v7" {
		t.Errorf("asset data precedence: %v %+v", err, as)
	}
}

// LOGO-02: the documented projection. Same geometry is the identity; an
// integer scale keeps position and shape.
func TestLogoAssetProject(t *testing.T) {
	mask := encodeGray(t, 8, 4, func(x, y int) byte {
		if x == 0 || y == 0 || x == 7 || y == 3 {
			return 255
		}
		return 0
	})
	a := &LogoAsset{Format: LogoAssetFormat, Version: LogoAssetVersion, AnalysisWidth: 64, AnalysisHeight: 36, X: 40, Y: 6, Width: 8, Height: 4, MaskPNG: mask, MaskSHA256: sha(mask)}
	if err := a.validate(); err != nil {
		t.Fatal(err)
	}
	m, rect, why := a.project(64, 36)
	if m == nil || why != "" || rect != [4]int{40, 6, 48, 10} || m.pixels != 20 {
		t.Fatalf("identity projection: %+v %v %q", m, rect, why)
	}
	m, rect, why = a.project(128, 72)
	if m == nil || why != "" || rect != [4]int{80, 12, 96, 20} || m.pixels != 80 {
		t.Fatalf("2x projection: %+v %v %q", m, rect, why)
	}
	m, rect, _ = a.project(32, 18)
	if m == nil || rect != [4]int{20, 3, 24, 5} {
		t.Fatalf("half projection: %+v %v", m, rect)
	}
	for _, g := range [][2]int{{64, 48}, {64, 37}, {100, 36}} {
		if m, _, why := a.project(g[0], g[1]); m != nil || why != "asset_incompatible_geometry" {
			t.Errorf("%dx%d: projected although the shape differs (%q)", g[0], g[1], why)
		}
	}
	// 1% anamorphic difference is inside the documented 2% allowance.
	if m, _, why := a.project(640, 363); m == nil {
		t.Errorf("640x363 rejected: %s", why)
	}
}

// C5 / VIS-02: a black separator with the station logo kept on screen is
// dark once the logo's search region is left out (dark.ignore_logo_region,
// on by default), and is missed without it.
func TestDarkIgnoresTheLogoRegion(t *testing.T) {
	sc := func() *scenario {
		return newScenario(
			blk{D: 50 * sec, Logo: true},
			blk{D: 30 * sec, Sep: sepBlackLogo, SepFrames: 5},
			blk{D: 30 * sec, Logo: true, Sep: sepBlackLogo, SepFrames: 5},
			blk{D: 40 * sec, Logo: true},
		)
	}
	darkAt := func(r *Result) []int64 {
		var out []int64
		for _, d := range r.Diagnostics.Events.DarkUniform {
			if d.Kind == KindDark {
				out = append(out, d.StartUS/1_000_000)
			}
		}
		return out
	}
	cfg := testConfig()
	cfg.Dark.IgnoreLogoRegion = false
	off := sc().run(t, cfg)
	if c := off.Calibration.Logo[0]; c.Status != StatusUsable {
		t.Fatalf("logo calibration %+v", c)
	}
	// 109 s is an ordinary black separator without the logo.
	if got := darkAt(off); fmt.Sprint(got) != "[109]" {
		t.Errorf("without the logo mask: dark spans at %v s, want only 109 (the logo makes the others bright)", got)
	}
	on := sc().run(t, nil)
	if got := darkAt(on); fmt.Sprint(got) != "[49 79 109]" {
		t.Errorf("with the logo mask: dark spans at %v s, want 49, 79 and 109", got)
	}
	// The same setting replays from saved features.
	_, f := measure(t, sc().source(), cfg)
	r, err := AnalyzeFeatures(context.Background(), f, testConfig(), nil)
	if err != nil || fmt.Sprint(darkAt(r)) != "[49 79 109]" {
		t.Errorf("replay with the mask on: err %v dark %v", err, darkAt(r))
	}
}

// C6: present runs can be trimmed at both ends, and present and absent runs
// can be smoothed with different minimum durations. Both are off by default.
func TestLogoTrimAndAsymmetricSmoothing(t *testing.T) {
	states := func(r *Result) string { return logoStates(r) }
	base := logoScenario().run(t, nil)
	cfg := testConfig()
	cfg.Logo.PresentTrim = sec
	r := logoScenario().run(t, cfg)
	if states(r) == states(base) {
		t.Fatalf("present_trim changed nothing: %s", states(r))
	}
	var trimmed int64
	for _, s := range r.Diagnostics.Events.Logo {
		if s.State == LogoStateUnknown {
			trimmed += s.EndUS - s.StartUS
		}
	}
	var before int64
	for _, s := range base.Diagnostics.Events.Logo {
		if s.State == LogoStateUnknown {
			before += s.EndUS - s.StartUS
		}
	}
	// Two present runs (0-50 s and 80-150 s, edges at 0 and 150 included):
	// 1 s off each end of each run.
	if trimmed-before != 4*1_000_000 {
		t.Errorf("trimmed unknown time grew by %d us, want 4 s; states %s", trimmed-before, states(r))
	}

	// A 1.5 s logo dropout survives 1 s absent smoothing and is absorbed
	// by the default 2 s.
	sp := []logoSpan{
		{start: 0, end: 10_000_000, state: logoPresent},
		{start: 10_000_000, end: 11_500_000, state: logoAbsent},
		{start: 11_500_000, end: 20_000_000, state: logoPresent},
	}
	c := DefaultConfig().Logo
	if got := smoothLogoSpansBy(append([]logoSpan(nil), sp...), 0, logoMinDurations(&c)); len(got) != 1 {
		t.Errorf("default smoothing: %d runs, want the dropout absorbed", len(got))
	}
	c.MinAbsentDuration = sec
	if got := smoothLogoSpansBy(append([]logoSpan(nil), sp...), 0, logoMinDurations(&c)); len(got) != 3 {
		t.Errorf("1 s absent smoothing: %d runs, want the dropout kept", len(got))
	}
}

// An asset learned at one analysis size is used on recordings analyzed at
// two and three times that size: the enlarged mask keeps one-pixel edges,
// still validates and gives the same presence intervals.
func TestLogoAssetAcrossAnalysisSizes(t *testing.T) {
	asset := learnedAsset(t)
	for _, k := range []struct{ w, h int }{{tW * 2, tH * 2}, {tW * 3, tH * 3}} {
		sc := newScenario(
			blk{D: 50 * sec, Logo: true, W: k.w, H: k.h},
			blk{D: 30 * sec, W: k.w, H: k.h},
			blk{D: 30 * sec, Logo: true, W: k.w, H: k.h},
			blk{D: 40 * sec, Logo: true, W: k.w, H: k.h},
		)
		cfg := testConfig()
		cfg.LogoAssetData = asset
		cfg.Sampling.AnalysisMaxWidth, cfg.Sampling.AnalysisMaxHeight = max(k.w, 64), max(k.h, 36)
		r := sc.run(t, cfg)
		c := r.Calibration.Logo[0]
		if c.Status != StatusUsable || c.Origin != LogoOriginAsset {
			t.Errorf("%dx%d: calibration %+v (failed %q)", k.w, k.h, c, c.Failed)
			continue
		}
		if got := logoStates(r); got != "present absent present" {
			t.Errorf("%dx%d: logo states %s", k.w, k.h, got)
		}
	}
}

// logo.min_specificity: a match that holds about as well with the mask
// moved (text or texture in the logo's place) is unknown, not present.
func TestLogoSpecificity(t *testing.T) {
	// The moved masks of a vertical line stay inside its region.
	m := &logoModel{mask: make([]uint64, 2)}
	e := &logoEpoch{rects: [][4]int{{0, 0, 10, 10}}, rectOff: []int{0}}
	for y := 0; y < 10; y++ {
		setBit(m.mask, y*10+5)
	}
	moved := m.shiftedMasks(e)
	want := []int{10, 10, 10 - logoShiftPixels, 10 - logoShiftPixels}
	for k, sm := range moved {
		n, self := 0, 0
		for w := range sm {
			n += bits.OnesCount64(sm[w])
			self += bits.OnesCount64(sm[w] & m.mask[w])
		}
		if n != want[k] {
			t.Errorf("move %d keeps %d pixels, want %d", k, n, want[k])
		}
		if (k < 2) != (self == 0) {
			t.Errorf("move %d overlaps the mask by %d pixels", k, self)
		}
	}

	// 60 samples a second apart, all matching 90 %; the second half also
	// matches almost as well when moved.
	const n = 60
	ob := &logoEpochObs{start: 0, end: d(n * sec), aw: 100, ah: 100}
	tr := &logoTrial{box: [4]int{0, 0, 10, 10}, pixels: 100, selfShift: 40}
	for i := 0; i < n; i++ {
		ob.samples = append(ob.samples, logoSample{pts: d(time.Duration(i) * sec), usable: true})
		tr.hits, tr.noise = append(tr.hits, 90), append(tr.noise, 0)
		tr.shift = append(tr.shift, map[bool]uint32{true: 45, false: 88}[i < n/2])
	}
	present := func(c *Config) int64 {
		var rec LogoCalibration
		spans, _ := evaluateLogo(c, ob, tr, nil, &rec)
		var p int64
		for _, s := range spans {
			if s.state == logoPresent {
				p += s.end - s.start
			}
		}
		return p
	}
	cfg := DefaultConfig()
	cfg.Logo.SampleInterval = time.Second
	cfg.Logo.MinSpecificity = 0
	if got := present(cfg); got != d(n*sec) {
		t.Errorf("off: present %d us, want all", got)
	}
	cfg.Logo.MinSpecificity = 0.1
	if got := present(cfg); got != d(n/2*sec) {
		t.Errorf("0.1: present %d us, want the first half", got)
	}
}
