// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// measure runs a direct analysis and returns its result and the features
// handed to Options.OnFeatures.
func measure(t *testing.T, src Source, cfg *Config) (*Result, *Features) {
	t.Helper()
	var f *Features
	r, err := Analyze(context.Background(), src, cfg, &Options{OnFeatures: func(x *Features) { f = x }})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if f == nil {
		t.Fatalf("OnFeatures was not called")
	}
	return r, f
}

// roundTrip writes features and reads them back.
func roundTrip(t *testing.T, f *Features) (*Features, []byte) {
	t.Helper()
	var buf bytes.Buffer
	n, err := f.WriteTo(&buf)
	if err != nil || n != int64(buf.Len()) {
		t.Fatalf("WriteTo: %d bytes, err %v (buffer %d)", n, err, buf.Len())
	}
	g, err := ReadFeatures(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ReadFeatures: %v", err)
	}
	return g, buf.Bytes()
}

// PIPE-03: classifying saved features gives a byte-identical result to the
// direct analysis, also under other non-feature settings, and reading,
// writing and replaying are deterministic and leave the features intact.
func TestFeatureReplayEqualsDirect(t *testing.T) {
	cases := map[string]func() *Config{
		"defaults": testConfig,
		"full diagnostics": func() *Config {
			c := testConfig()
			c.Outputs.Diagnostics = DiagnosticsFull
			return c
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			src := logoScenario().source()
			cfg := mk()
			var learned []*LogoAsset
			var f *Features
			direct, err := Analyze(context.Background(), src, cfg, &Options{
				OnFeatures:    func(x *Features) { f = x },
				OnLogoLearned: func(l *LogoAsset) { learned = append(learned, l) },
			})
			if err != nil || f == nil {
				t.Fatalf("Analyze: %v (features %v)", err, f != nil)
			}
			if f.Identity() != direct.FeatureID || f.Frames() != direct.Timing.Frames || f.DurationUS() != direct.DurationUS {
				t.Errorf("features identity %s frames %d duration %d; result %s %d %d", f.Identity(), f.Frames(), f.DurationUS(), direct.FeatureID, direct.Timing.Frames, direct.DurationUS)
			}
			g, data := roundTrip(t, f)
			if _, again := roundTrip(t, g); !bytes.Equal(data, again) {
				t.Errorf("writing read-back features gives different bytes")
			}

			var replayLogos []*LogoAsset
			replay, err := AnalyzeFeatures(context.Background(), g, cfg, &Options{OnLogoLearned: func(l *LogoAsset) { replayLogos = append(replayLogos, l) }})
			if err != nil {
				t.Fatalf("AnalyzeFeatures: %v", err)
			}
			if got, want := mustJSON(t, replay), mustJSON(t, direct); got != want {
				t.Fatalf("replayed result differs from the direct one:\n%s", firstDifference(got, want))
			}
			if len(learned) == 0 || direct.Calibration.Logo[0].Status != StatusUsable {
				t.Fatalf("the scenario learned no usable logo; replay of logo decisions is not exercised")
			}
			if len(replayLogos) != len(learned) {
				t.Errorf("replay delivered %d learned logos, direct %d", len(replayLogos), len(learned))
			}
			// The in-memory features replay the same way, twice.
			for i := 0; i < 2; i++ {
				r, err := AnalyzeFeatures(context.Background(), f, cfg, nil)
				if err != nil || mustJSON(t, r) != mustJSON(t, direct) {
					t.Fatalf("in-memory replay %d differs (err %v)", i, err)
				}
			}

			// Other scoring, boundary and break settings: replay equals a
			// direct analysis with those settings.
			other := mk()
			other.Scoring.Threshold = 3
			other.Scoring.LogoAbsentFactor = 4
			other.Breaks.MinBreak = 10 * sec
			other.Boundaries.FromSilence = false
			other.Dark.MaxMeanLuma = 30
			// Logo presence decisions are classification settings too.
			other.Logo.PresentThreshold, other.Logo.AbsentThreshold = 0.75, 0.2
			other.Logo.MinStateDuration = 3 * sec
			other.Logo.MaxOccupancy = 0.95
			want := analyze(t, logoScenario().source(), other)
			got, err := AnalyzeFeatures(context.Background(), g, other, nil)
			if err != nil {
				t.Fatalf("AnalyzeFeatures with other settings: %v", err)
			}
			if a, b := mustJSON(t, got), mustJSON(t, want); a != b {
				t.Fatalf("replay under other settings differs from the direct analysis:\n%s", firstDifference(a, b))
			}
		})
	}
}

// PIPE-03: a configuration that changes a feature-affecting key is refused,
// naming the key; the error kind is invalid configuration.
func TestFeatureReplayRefusesOtherFeatureSettings(t *testing.T) {
	_, f := measure(t, logoScenario().source(), testConfig())
	for key, mut := range map[string]func(c *Config){
		"scene.cut_threshold":    func(c *Config) { c.Scene.CutThreshold = 0.6 },
		"logo.min_persistence":   func(c *Config) { c.Logo.MinPersistence = 0.5 },
		"signals.aspect":         func(c *Config) { c.Signals.Aspect = false },
		"dark.bright_pixel_luma": func(c *Config) { c.Dark.BrightPixelLuma = 80 },
	} {
		cfg := testConfig()
		mut(cfg)
		_, err := AnalyzeFeatures(context.Background(), f, cfg, nil)
		if KindOf(err) != KindInvalidConfig || !strings.Contains(err.Error(), key) {
			t.Errorf("%s changed: err %v, want an invalid-configuration error naming the key", key, err)
		}
	}
}

// PIPE-03: damaged, truncated, foreign and future feature files are refused
// with a clear kind, never misread.
func TestReadFeaturesRejectsBadFiles(t *testing.T) {
	_, f := measure(t, logoScenario().source(), testConfig())
	_, good := roundTrip(t, f)
	mutate := func(fn func(b []byte) []byte) []byte { return fn(append([]byte(nil), good...)) }
	for name, tc := range map[string]struct {
		data []byte
		kind ErrorKind
	}{
		"empty":           {nil, KindUnsupported},
		"not a feature":   {[]byte("{\"schema_version\":\"1\"}"), KindUnsupported},
		"truncated":       {good[:len(good)/2], KindIO},
		"header only":     {good[:12], KindIO},
		"flipped byte":    {mutate(func(b []byte) []byte { b[len(b)/2] ^= 1; return b }), KindIO},
		"flipped digest":  {mutate(func(b []byte) []byte { b[len(b)-1] ^= 1; return b }), KindIO},
		"another version": {mutate(func(b []byte) []byte { binary.LittleEndian.PutUint32(b[8:], 99); return b }), KindUnsupported},
	} {
		_, err := ReadFeatures(bytes.NewReader(tc.data))
		var e *Error
		if !errors.As(err, &e) || e.Kind != tc.kind {
			t.Errorf("%s: err %v, want kind %s", name, err, tc.kind)
		}
	}
	if !IsFeatureFile(good) || IsFeatureFile([]byte("RIFF")) {
		t.Errorf("IsFeatureFile misidentifies files")
	}
}

// PIPE-03: a failed extraction yields no features.
func TestNoFeaturesForAFailedExtraction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := false
	src := &testSource{events: midwayEvents(), onNext: func(n int) {
		if n == 1000 {
			cancel()
		}
	}}
	_, err := Analyze(ctx, src, testConfig(), &Options{OnFeatures: func(*Features) { called = true }})
	if err == nil || called {
		t.Errorf("err %v, OnFeatures called %v: a canceled extraction must not hand out features", err, called)
	}
}

// firstDifference shows the text around the first differing byte.
func firstDifference(got, want string) string {
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	lo := max(0, i-120)
	return "got  ..." + got[lo:min(len(got), i+120)] + "\nwant ..." + want[lo:min(len(want), i+120)]
}
