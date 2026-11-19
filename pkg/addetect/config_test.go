// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// AT-19: the configuration contract (CFG-01..04). Configuration files are
// YAML (the maintainer's decision supersedes the INI wording of CFG-01).

func loadYAML(t *testing.T, c *Config, doc string) error {
	t.Helper()
	return c.LoadYAML([]byte(doc), "test.yaml", "")
}

func wantInvalid(t *testing.T, err error, mentions ...string) {
	t.Helper()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want an invalid-configuration error", err)
		return
	}
	for _, m := range mentions {
		if !strings.Contains(err.Error(), m) {
			t.Errorf("error %q does not mention %q", err, m)
		}
	}
}

// CFG-04: the built-in defaults are the initial values of the requirements.
func TestDefaultConfigValues(t *testing.T) {
	c := DefaultConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults do not validate: %v", err)
	}
	sc := c.Scoring
	if sc.Threshold != 1.5 || sc.StrictTolerance != 500*ms || sc.LooseTolerance != sec ||
		sc.StrictFactor != 2 || sc.LooseFactor != 1.5 || sc.CombinedFactor != 1.5 ||
		sc.LogoPresentFactor != 0.1 || sc.LogoAbsentFactor != 2 || sc.CaptionFactor != 2 || sc.AspectFactor != 2 ||
		sc.RelativeFactor != 2 || sc.RelExcessRatio != 1.3 || sc.LogoPresentOccupancy != 0.8 || sc.LogoAbsentOccupancy != 0.2 ||
		sc.UncertaintyExtension != 0 {
		t.Errorf("scoring defaults = %+v", sc)
	}
	if sc.RelBrightness || sc.RelNonuniformity || sc.RelAudioEnergy || sc.RelSilence || sc.RelSceneRate {
		t.Errorf("relative rules are not off by default")
	}
	for _, v := range []float64{sc.MinCoverageVideo, sc.MinCoverageLogo, sc.MinCoverageCaptions, sc.MinCoverageAspect, sc.MinCoverageAudio} {
		if v != 0.8 {
			t.Errorf("coverage requirement default %v, want 0.8", v)
		}
	}
	wantStd := []time.Duration{10 * sec, 15 * sec, 20 * sec, 30 * sec, 45 * sec, 60 * sec, 90 * sec, 120 * sec}
	if !reflect.DeepEqual(sc.StandardDurations, wantStd) {
		t.Errorf("standard durations = %v", sc.StandardDurations)
	}
	if c.Dark.Mode != "fixed" || c.Dark.MaxMeanLuma != 19 {
		t.Errorf("dark defaults = %+v", c.Dark)
	}
	if c.Logo.MinOccupancy != 0.40 || c.Logo.MaxOccupancy != 0.92 || c.Logo.Asset != "" {
		t.Errorf("logo defaults = %+v", c.Logo)
	}
	if c.Audio.SilenceLevel != -50 || c.Audio.MinSilence != 100*ms || c.Audio.Alignment != 250*ms || c.Audio.Metric != "rms" {
		t.Errorf("audio defaults = %+v", c.Audio)
	}
	b := c.Boundaries
	if b.FusionTolerance != 100*ms || !b.FromDarkUniform || !b.FromSilence || !b.FromResolution || !b.FromAspect || !b.FromBumpers || b.FromLogo || b.FromCaptions || b.FromScene {
		t.Errorf("boundary defaults = %+v", b)
	}
	br := c.Breaks
	if br.MinUnit != 4*sec || br.MaxUnit != 120*sec || br.MinBreak != 20*sec || br.MaxBreak != 600*sec || !br.AllowTruncatedEdges || !br.BridgeGaps || br.MaxBridgeGap != 15*sec || !br.BridgeLogoProgram {
		t.Errorf("break defaults = %+v", br)
	}
	if c.Signals != (SignalsConfig{true, true, true, true, true, true, true, true}) {
		t.Errorf("signal defaults = %+v", c.Signals)
	}
	if c.Scoring.CaptionAbsentFactor != 1 || c.Captions.Service != "CC1" {
		t.Errorf("caption defaults = %+v", c.Captions)
	}
	if c.Outputs.SkipPaddingStart != 0 || c.Outputs.SkipPaddingEnd != 0 || c.Outputs.Overwrite || c.Outputs.Diagnostics != "summary" {
		t.Errorf("output defaults = %+v", c.Outputs)
	}
	if c.Input.OptionalStreams != "degraded" || c.Input.TimestampResetPolicy != "rebase" || c.Package.FailurePolicy != "fail" || c.Package.AllowRemote {
		t.Errorf("input/package defaults = %+v %+v", c.Input, c.Package)
	}
	if len(c.BumperTemplates) != 0 || c.LogoAssetData != nil {
		t.Errorf("built-in assets present")
	}
}

// AT-19 / CFG-02: precedence is defaults < base < channel < explicit
// overrides; only supplied keys replace lower layers; lists replace.
func TestConfigLayering(t *testing.T) {
	c := DefaultConfig()
	base := `
scoring:
  threshold: 2.0
  loose_tolerance: 2s
  standard_durations: [10s, 20s, 40s]
audio:
  silence_level_dbfs: -45
logo:
  search_regions: [[0, 0, 0.5, 0.5], [0.5, 0, 0.5, 0.5]]
`
	channel := `
scoring:
  threshold: 1.8
  standard_durations: [15s]
breaks:
  allow_truncated_edges: true
`
	if err := loadYAML(t, c, base); err != nil {
		t.Fatalf("base: %v", err)
	}
	if c.Scoring.Threshold != 2.0 || len(c.Scoring.StandardDurations) != 3 {
		t.Fatalf("after base: %+v", c.Scoring)
	}
	if err := loadYAML(t, c, channel); err != nil {
		t.Fatalf("channel: %v", err)
	}
	if err := c.Set("scoring.threshold", "1.7"); err != nil {
		t.Fatalf("override: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("layered config: %v", err)
	}
	if c.Scoring.Threshold != 1.7 {
		t.Errorf("threshold = %v, want the explicit override 1.7", c.Scoring.Threshold)
	}
	if c.Scoring.LooseTolerance != 2*sec {
		t.Errorf("loose tolerance = %v, want the base value (the channel layer did not supply it)", c.Scoring.LooseTolerance)
	}
	// Lists replace rather than concatenate.
	if !reflect.DeepEqual(c.Scoring.StandardDurations, []time.Duration{15 * sec}) {
		t.Errorf("standard durations = %v, want [15s]", c.Scoring.StandardDurations)
	}
	if !reflect.DeepEqual(c.Logo.SearchRegions, []Region{{0, 0, 0.5, 0.5}, {0.5, 0, 0.5, 0.5}}) {
		t.Errorf("search regions = %v", c.Logo.SearchRegions)
	}
	if c.Audio.SilenceLevel != -45 || !c.Breaks.AllowTruncatedEdges {
		t.Errorf("audio %+v breaks %+v", c.Audio, c.Breaks)
	}
	// Keys no layer supplied keep their defaults.
	d := DefaultConfig()
	if c.Scoring.StrictTolerance != d.Scoring.StrictTolerance || c.Dark != d.Dark || c.Audio.MinSilence != d.Audio.MinSilence || c.Breaks.MinBreak != d.Breaks.MinBreak {
		t.Errorf("unsupplied keys changed")
	}
	// An empty or comment-only layer changes nothing.
	before := c.Clone()
	for _, doc := range []string{"", "# nothing here\n", "---\n", "~\n"} {
		if err := loadYAML(t, c, doc); err != nil {
			t.Errorf("empty layer %q: %v", doc, err)
		}
	}
	if !reflect.DeepEqual(before, c) {
		t.Errorf("an empty layer changed the configuration")
	}
}

// AT-19 / CFG-02: relative paths resolve against the declaring file;
// explicit overrides are left relative to the working directory.
func TestConfigRelativePaths(t *testing.T) {
	baseDir, chanDir := t.TempDir(), t.TempDir()
	png := cardPNG(t, 64, 36, 8, 6)
	if err := os.MkdirAll(filepath.Join(baseDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(baseDir, "assets", "card.png"), png, 0o600)
	os.WriteFile(filepath.Join(chanDir, "bug.png"), png, 0o600)
	asset, _ := learnedAsset(t).Marshal()
	os.WriteFile(filepath.Join(chanDir, "logo.json"), asset, 0o600)

	basePath := filepath.Join(baseDir, "base.yaml")
	os.WriteFile(basePath, []byte(`
bumpers:
  templates:
    card:
      image: assets/card.png
      threshold: 0.8
resources:
  scratch_dir: scratch
`), 0o600)
	chanPath := filepath.Join(chanDir, "channel.yaml")
	os.WriteFile(chanPath, []byte(`
logo:
  asset: logo.json
bumpers:
  templates:
    bug: {image: bug.png, region: [0.5, 0.5, 0.25, 0.25], geometry: strict, min_support: 1s}
`), 0o600)

	c := DefaultConfig()
	if err := c.LoadYAMLFile(basePath); err != nil {
		t.Fatalf("base: %v", err)
	}
	if err := c.LoadYAMLFile(chanPath); err != nil {
		t.Fatalf("channel: %v", err)
	}
	if c.Logo.Asset != filepath.Join(chanDir, "logo.json") {
		t.Errorf("logo.asset = %q", c.Logo.Asset)
	}
	if c.Resources.ScratchDir != filepath.Join(baseDir, "scratch") {
		t.Errorf("resources.scratch_dir = %q", c.Resources.ScratchDir)
	}
	if len(c.BumperTemplates) != 2 {
		t.Fatalf("templates = %+v", c.BumperTemplates)
	}
	card, bug := c.BumperTemplates[0], c.BumperTemplates[1]
	if card.ID != "card" || card.Image != filepath.Join(baseDir, "assets", "card.png") || card.Threshold != 0.8 {
		t.Errorf("card = %+v", card)
	}
	// Unsupplied template keys take the documented template defaults.
	if card.Region != (Region{0, 0, 1, 1}) || card.Geometry != "stretch" || card.MinSupport != 400*ms {
		t.Errorf("card defaults = %+v", card)
	}
	if bug.ID != "bug" || bug.Image != filepath.Join(chanDir, "bug.png") || bug.Region != (Region{0.5, 0.5, 0.25, 0.25}) || bug.Geometry != "strict" || bug.MinSupport != sec || bug.Threshold != 0.9 {
		t.Errorf("bug = %+v", bug)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	as, err := LoadAssets(c)
	if err != nil {
		t.Fatalf("LoadAssets: %v", err)
	}
	if as.Logo == nil || len(as.bumpers) != 2 {
		t.Errorf("assets = %+v", as)
	}

	// Absolute paths are kept; explicit overrides are not re-based.
	abs := filepath.Join(chanDir, "bug.png")
	os.WriteFile(chanPath, []byte("logo:\n  asset: "+filepath.Join(chanDir, "logo.json")+"\n"), 0o600)
	c2 := DefaultConfig()
	if err := c2.LoadYAMLFile(chanPath); err != nil || c2.Logo.Asset != filepath.Join(chanDir, "logo.json") {
		t.Errorf("absolute path: %v %q", err, c2.Logo.Asset)
	}
	if err := c2.Set("logo.asset", "relative/logo.json"); err != nil || c2.Logo.Asset != "relative/logo.json" {
		t.Errorf("override path: %v %q", err, c2.Logo.Asset)
	}
	if err := c2.Set("bumpers.templates.x.image", "x.png"); err != nil || c2.BumperTemplates[0].Image != "x.png" {
		t.Errorf("override template path: %v", err)
	}
	_ = abs

	// A later layer refines a template of an earlier layer key by key.
	c3 := DefaultConfig()
	c3.LoadYAMLFile(basePath)
	if err := loadYAML(t, c3, "bumpers:\n  templates:\n    card: {threshold: 0.95}\n"); err != nil {
		t.Fatal(err)
	}
	if len(c3.BumperTemplates) != 1 || c3.BumperTemplates[0].Threshold != 0.95 || c3.BumperTemplates[0].Image != filepath.Join(baseDir, "assets", "card.png") {
		t.Errorf("refined template = %+v", c3.BumperTemplates)
	}

	// A missing file is a configuration error.
	wantInvalid(t, DefaultConfig().LoadYAMLFile(filepath.Join(baseDir, "nope.yaml")))
}

// AT-19 / CFG-03: duplicate keys within one layer, unknown keys and unknown
// sections are actionable errors.
func TestConfigDuplicateAndUnknownKeys(t *testing.T) {
	cases := []struct {
		name, doc string
		mentions  []string
	}{
		{"duplicate key", "scoring:\n  threshold: 2\n  threshold: 3\n", []string{"duplicate key", "threshold", "test.yaml:3"}},
		{"duplicate section", "scoring:\n  threshold: 2\naudio:\n  window: 20ms\nscoring:\n  loose_tolerance: 2s\n", []string{"duplicate key", "scoring"}},
		{"duplicate template", "bumpers:\n  templates:\n    a: {image: a.png}\n    a: {image: b.png}\n", []string{"duplicate key"}},
		{"duplicate template key", "bumpers:\n  templates:\n    a: {image: a.png, image: b.png}\n", []string{"duplicate key", "image"}},
		{"unknown key", "scoring:\n  treshold: 2\n", []string{"unknown configuration key", "scoring.treshold", "test.yaml:2"}},
		{"unknown section", "scorring:\n  threshold: 2\n", []string{"unknown section", "scorring"}},
		{"unknown signal name", "signals:\n  subtitles: true\n", []string{"signals.subtitles"}},
		{"unknown template key", "bumpers:\n  templates:\n    a: {image: a.png, colour: red}\n", []string{"colour"}},
		{"bad template id", "bumpers:\n  templates:\n    a.b: {image: a.png}\n", []string{"template id"}},
		{"key in wrong section", "audio:\n  threshold: 2\n", []string{"audio.threshold"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantInvalid(t, loadYAML(t, DefaultConfig(), c.doc), c.mentions...)
		})
	}
	// The same key in two different layers is not a duplicate.
	c := DefaultConfig()
	if err := loadYAML(t, c, "scoring:\n  threshold: 2\n"); err != nil {
		t.Fatal(err)
	}
	if err := loadYAML(t, c, "scoring:\n  threshold: 3\n"); err != nil || c.Scoring.Threshold != 3 {
		t.Errorf("second layer: %v", err)
	}
	wantInvalid(t, DefaultConfig().Set("scoring.nope", "1"), "unknown configuration key")
	wantInvalid(t, DefaultConfig().Set("nope", "1"), "unknown configuration key")
	wantInvalid(t, DefaultConfig().Set("bumpers.templates.x", "1"), "unknown configuration key")
	wantInvalid(t, DefaultConfig().Set("bumpers.templates.x.nope", "1"), "unknown configuration key")
}

// AT-19 / CFG-01, CFG-03: malformed values, missing units, nonfinite
// numbers, negative durations, out-of-range values and unknown enumeration
// values are rejected when the layer is loaded.
func TestConfigMalformedValues(t *testing.T) {
	cases := []struct {
		key, value string
		mentions   []string
	}{
		// Durations carry explicit units.
		{"audio.min_silence", "100", []string{"audio.min_silence", "unit"}},
		{"audio.min_silence", "fast", []string{"audio.min_silence"}},
		{"audio.min_silence", "-100ms", []string{"audio.min_silence", "range"}},
		{"breaks.min_unit", "-4s", []string{"breaks.min_unit"}},
		{"scoring.standard_durations", "[10s, 20]", []string{"scoring.standard_durations"}},
		// Nonfinite and malformed numbers.
		{"scoring.threshold", ".inf", []string{"scoring.threshold"}},
		{"scoring.threshold", "inf", []string{"scoring.threshold", "finite"}},
		{"scoring.threshold", ".nan", []string{"scoring.threshold"}},
		{"scoring.threshold", "NaN", []string{"scoring.threshold", "finite"}},
		{"scoring.threshold", "high", []string{"scoring.threshold", "not a number"}},
		// Ratios are not percentages.
		{"logo.min_occupancy", "40%", []string{"logo.min_occupancy"}},
		{"logo.min_occupancy", "40", []string{"logo.min_occupancy", "range"}},
		// Ranges.
		{"scoring.threshold", "1.0", []string{"scoring.threshold", "range"}},
		{"scoring.threshold", "0", []string{"scoring.threshold", "range"}},
		{"dark.max_mean_luma", "300", []string{"dark.max_mean_luma", "range"}},
		{"dark.max_mean_luma", "-1", []string{"dark.max_mean_luma", "range"}},
		{"audio.silence_level_dbfs", "3", []string{"audio.silence_level_dbfs", "range"}},
		{"scoring.logo_absent_factor", "0", []string{"scoring.logo_absent_factor", "range"}},
		{"scoring.logo_absent_factor", "-2", []string{"scoring.logo_absent_factor", "range"}},
		{"resources.memory_budget", "1MiB", []string{"resources.memory_budget", "range"}},
		{"resources.workers", "1000", []string{"resources.workers", "range"}},
		// Enumerations and signal/policy names.
		{"dark.mode", "sometimes", []string{"dark.mode", "fixed", "auto"}},
		{"captions.service", "CC9", []string{"captions.service"}},
		{"captions.absence_policy", "neutral", []string{"captions.absence_policy"}},
		{"input.timestamp_reset_policy", "ignore", []string{"input.timestamp_reset_policy"}},
		{"outputs.diagnostics", "verbose", []string{"outputs.diagnostics"}},
		{"scene.histogram_bins", "50", []string{"scene.histogram_bins"}},
		{"scoring.boundary_evidence_edges", "none", []string{"scoring.boundary_evidence_edges"}},
		// Other types.
		{"signals.logo", "maybe", []string{"signals.logo", "boolean"}},
		{"scoring.scene_min_events", "2.5", []string{"scoring.scene_min_events", "integer"}},
		{"resources.memory_budget", "lots", []string{"resources.memory_budget", "size"}},
		{"resources.memory_budget", "-5MiB", []string{"resources.memory_budget"}},
		{"sampling.region", "[0, 0, 1]", []string{"sampling.region"}},
		{"sampling.region", "[0, 0, 1, wide]", []string{"sampling.region"}},
		{"logo.search_regions", "[[0, 0, 1]]", []string{"logo.search_regions"}},
		{"scoring.threshold", "{a: 1}", []string{"scoring.threshold"}},
		{"bumpers.templates.x.threshold", "big", []string{"bumpers.templates.x.threshold"}},
		{"bumpers.templates.x.min_support", "3", []string{"bumpers.templates.x.min_support"}},
		{"bumpers.templates.x.region", "[1, 2]", []string{"bumpers.templates.x.region"}},
	}
	for _, c := range cases {
		t.Run(c.key+"="+c.value, func(t *testing.T) {
			sec, key, _ := strings.Cut(c.key, ".")
			var doc string
			if rest, ok := strings.CutPrefix(c.key, "bumpers.templates."); ok {
				id, k, _ := strings.Cut(rest, ".")
				doc = "bumpers:\n  templates:\n    " + id + ":\n      " + k + ": " + c.value + "\n"
			} else {
				doc = sec + ":\n  " + key + ": " + c.value + "\n"
			}
			cfg := DefaultConfig()
			err := loadYAML(t, cfg, doc)
			if err == nil {
				// Template field ranges are checked by Validate.
				err = cfg.Validate()
			}
			wantInvalid(t, err, c.mentions...)
			// The explicit override path applies the same checks.
			if !strings.HasPrefix(c.value, "[") && !strings.HasPrefix(c.value, "{") {
				cfg = DefaultConfig()
				err = cfg.Set(c.key, c.value)
				if err == nil {
					err = cfg.Validate()
				}
				wantInvalid(t, err)
			}
		})
	}
	// Structural problems of the document.
	for name, doc := range map[string]string{
		"malformed yaml":      "scoring:\n  threshold: [1, 2\n",
		"section is a scalar": "scoring: 5\n",
		"section is a list":   "scoring: [1, 2]\n",
		"top-level list":      "- scoring\n",
		"top-level scalar":    "hello\n",
		"two documents":       "scoring:\n  threshold: 2\n---\nscoring:\n  threshold: 3\n",
		"templates not a map": "bumpers:\n  templates: [a, b]\n",
		"template not a map":  "bumpers:\n  templates:\n    a: image.png\n",
		"invalid utf-8":       "scoring:\n  threshold: \xff\n",
	} {
		wantInvalid(t, loadYAML(t, DefaultConfig(), doc))
		_ = name
	}
	wantInvalid(t, DefaultConfig().Set("input.video_track", "\xff\xfe"), "UTF-8")

	// Accepted spellings.
	c := DefaultConfig()
	ok := `
signals: {logo: no, scene: "off", audio: yes, captions: "on", aspect: false}
audio: {min_silence: 0s, window: 40ms, silence_level_dbfs: -60.5}
scoring: {standard_durations: [5s, 1m, 1m30s], relative_excess_ratio: 2}
resources: {memory_budget: 64MiB, scratch_quota: 0B}
sampling: {region: [0.1, 0.1, 0.8, 0.8]}
logo: {search_regions: [[0, 0, 1, 1]]}
package: {max_manifest_bytes: 4KiB, io_buffer_size: 8192B}
`
	if err := loadYAML(t, c, ok); err != nil {
		t.Fatalf("accepted spellings rejected: %v", err)
	}
	if c.Signals.Logo || c.Signals.Scene || !c.Signals.Audio || !c.Signals.Captions || c.Signals.Aspect {
		t.Errorf("booleans = %+v", c.Signals)
	}
	if c.Audio.MinSilence != 0 || c.Audio.Window != 40*ms || c.Audio.SilenceLevel != -60.5 {
		t.Errorf("audio = %+v", c.Audio)
	}
	if !reflect.DeepEqual(c.Scoring.StandardDurations, []time.Duration{5 * sec, 60 * sec, 90 * sec}) || c.Resources.MemoryBudget != 64<<20 || c.Resources.ScratchQuota != 0 {
		t.Errorf("scoring %+v resources %+v", c.Scoring.StandardDurations, c.Resources)
	}
	if c.Sampling.Region != (Region{0.1, 0.1, 0.8, 0.8}) || len(c.Logo.SearchRegions) != 1 || c.Package.MaxManifestBytes != 4096 || c.Package.IOBufferSize != 8192 {
		t.Errorf("regions/sizes: %+v %+v %+v", c.Sampling.Region, c.Logo.SearchRegions, c.Package)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// AT-19 / CFG-03: the interdependent constraints of Config.Validate.
func TestConfigInterdependentConstraints(t *testing.T) {
	cases := []struct {
		name     string
		mut      func(c *Config)
		mentions []string
	}{
		{"strict tolerance above loose", func(c *Config) { c.Scoring.StrictTolerance = 2 * sec }, []string{"strict_tolerance", "loose_tolerance"}},
		{"present occupancy equal to absent", func(c *Config) { c.Scoring.LogoPresentOccupancy, c.Scoring.LogoAbsentOccupancy = 0.5, 0.5 }, []string{"logo_present_occupancy", "logo_absent_occupancy"}},
		{"present occupancy below absent", func(c *Config) { c.Scoring.LogoPresentOccupancy = 0.1 }, []string{"logo_present_occupancy"}},
		{"logo validation min equal to max", func(c *Config) { c.Logo.MinOccupancy, c.Logo.MaxOccupancy = 0.5, 0.5 }, []string{"logo.min_occupancy", "logo.max_occupancy"}},
		{"logo validation inverted", func(c *Config) { c.Logo.MinOccupancy = 0.95 }, []string{"logo.min_occupancy"}},
		{"unit minimum above maximum", func(c *Config) { c.Breaks.MinUnit = 121 * sec }, []string{"breaks.min_unit", "breaks.max_unit"}},
		{"break minimum above maximum", func(c *Config) { c.Breaks.MinBreak = 601 * sec }, []string{"breaks.min_break", "breaks.max_break"}},
		{"no standard durations", func(c *Config) { c.Scoring.StandardDurations = nil }, []string{"standard_durations"}},
		{"unordered standard durations", func(c *Config) { c.Scoring.StandardDurations = []time.Duration{30 * sec, 15 * sec} }, []string{"standard_durations", "increasing"}},
		{"repeated standard duration", func(c *Config) { c.Scoring.StandardDurations = []time.Duration{15 * sec, 15 * sec} }, []string{"standard_durations"}},
		{"zero standard duration", func(c *Config) { c.Scoring.StandardDurations = []time.Duration{0, 15 * sec} }, []string{"standard_durations"}},
		{"logo absent threshold above present", func(c *Config) { c.Logo.AbsentThreshold = 0.9 }, []string{"logo.absent_threshold"}},
		{"logo size inverted", func(c *Config) { c.Logo.MinWidth = 0.5 }, []string{"logo"}},
		{"logo height inverted", func(c *Config) { c.Logo.MinHeight = 0.5 }, []string{"logo"}},
		{"no logo search region", func(c *Config) { c.Logo.SearchRegions = nil }, []string{"logo.search_regions"}},
		{"auto luma bounds inverted", func(c *Config) { c.Dark.AutoMinLuma = 50 }, []string{"dark.auto_min_luma"}},
		{"auto uniform bounds inverted", func(c *Config) { c.Dark.AutoUniformMin = 9 }, []string{"dark.auto_uniform_min"}},
		{"gradual threshold below cut threshold", func(c *Config) { c.Scene.GradualThreshold = 0.2 }, []string{"scene.gradual_threshold"}},
		{"invalid crop: empty", func(c *Config) { c.Sampling.Region = Region{0, 0, 0, 1} }, []string{"region"}},
		{"invalid crop: outside", func(c *Config) { c.Sampling.Region = Region{0.5, 0, 0.6, 1} }, []string{"region"}},
		{"invalid crop: negative", func(c *Config) { c.Sampling.Region = Region{-0.1, 0, 0.5, 1} }, []string{"region"}},
		{"invalid crop: nonfinite", func(c *Config) { c.Sampling.Region = Region{0, 0, math.NaN(), 1} }, []string{"region"}},
		{"invalid search region", func(c *Config) { c.Logo.SearchRegions = []Region{{0, 0.9, 0.5, 0.2}} }, []string{"region"}},
		{"video track none", func(c *Config) { c.Input.VideoTrack = "none" }, []string{"input.video_track"}},
		{"video track embedded", func(c *Config) { c.Input.VideoTrack = "embedded" }, []string{"input.video_track"}},
		{"audio track embedded", func(c *Config) { c.Input.AudioTrack = "embedded" }, []string{"input.audio_track"}},
		{"track name", func(c *Config) { c.Input.AudioTrack = "english" }, []string{"input.audio_track"}},
		{"negative track", func(c *Config) { c.Input.CaptionTrack = "-1" }, []string{"input.caption_track"}},
		{"empty track", func(c *Config) { c.Input.VideoTrack = "" }, []string{"input.video_track"}},
		{"absurd track index", func(c *Config) { c.Input.VideoTrack = "1234567" }, []string{"input.video_track"}},
		// Typed API values get the same range and finiteness checks.
		{"nan threshold", func(c *Config) { c.Scoring.Threshold = math.NaN() }, []string{"scoring.threshold", "finite"}},
		{"infinite factor", func(c *Config) { c.Scoring.StrictFactor = math.Inf(1) }, []string{"scoring.strict_duration_factor"}},
		{"threshold not above one", func(c *Config) { c.Scoring.Threshold = 1 }, []string{"scoring.threshold"}},
		{"negative duration", func(c *Config) { c.Boundaries.FusionTolerance = -time.Millisecond }, []string{"boundaries.fusion_tolerance"}},
		{"zero factor", func(c *Config) { c.Scoring.CaptionFactor = 0 }, []string{"scoring.caption_mismatch_factor"}},
		{"unknown enum", func(c *Config) { c.Dark.Mode = "adaptive" }, []string{"dark.mode"}},
		{"budget below minimum", func(c *Config) { c.Resources.MemoryBudget = 1 << 20 }, []string{"resources.memory_budget"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := DefaultConfig()
			c.mut(cfg)
			wantInvalid(t, cfg.Validate(), c.mentions...)
			// Analyze refuses the configuration before opening the source.
			src := &testSource{events: frameSeq(rep(content, 3)...)}
			if _, err := Analyze(context.Background(), src, cfg, nil); !errors.Is(err, ErrInvalidConfig) || src.opens != 0 {
				t.Errorf("Analyze: err %v, source opened %d times", err, src.opens)
			}
		})
	}
	// Boundary values that must be accepted.
	okays := map[string]func(c *Config){
		"strict equal to loose":      func(c *Config) { c.Scoring.StrictTolerance = c.Scoring.LooseTolerance },
		"unit min equal to max":      func(c *Config) { c.Breaks.MinUnit = c.Breaks.MaxUnit },
		"break min equal to max":     func(c *Config) { c.Breaks.MinBreak = c.Breaks.MaxBreak },
		"audio none":                 func(c *Config) { c.Input.AudioTrack = "none" },
		"caption embedded":           func(c *Config) { c.Input.CaptionTrack = "embedded" },
		"caption none":               func(c *Config) { c.Input.CaptionTrack = "none" },
		"track index":                func(c *Config) { c.Input.VideoTrack, c.Input.AudioTrack = "0", "12" },
		"gradual off below cut":      func(c *Config) { c.Scene.GradualWindow, c.Scene.GradualThreshold = 0, 0.1 },
		"absent equal present":       func(c *Config) { c.Logo.AbsentThreshold = c.Logo.PresentThreshold },
		"one standard duration":      func(c *Config) { c.Scoring.StandardDurations = []time.Duration{30 * sec} },
		"full-frame search region":   func(c *Config) { c.Logo.SearchRegions = []Region{{0, 0, 1, 1}} },
		"neutral factors":            func(c *Config) { c.Scoring.StrictFactor, c.Scoring.LogoAbsentFactor = 1, 1 },
		"zero fusion tolerance":      func(c *Config) { c.Boundaries.FusionTolerance = 0 },
		"negative skip padding":      func(c *Config) { c.Outputs.SkipPaddingStart = -2 * sec },
		"explicit channel baselines": func(c *Config) { c.Baselines.CaptionStyle, c.Baselines.AspectRatio = "pop_on", 1.3333 },
	}
	for name, mut := range okays {
		cfg := DefaultConfig()
		mut(cfg)
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// AT-19 / CFG-01: the exported default configuration contains every
// supported key with its default, range and description, and loads back to
// an identical effective configuration.
func TestWriteDefaultYAMLRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDefaultYAML(&buf); err != nil {
		t.Fatal(err)
	}
	doc := buf.String()

	// Loaded onto an all-zero configuration it reproduces the defaults, so
	// every key is present with its default value.
	zero := &Config{}
	if err := zero.LoadYAML(buf.Bytes(), "defaults.yaml", ""); err != nil {
		t.Fatalf("default YAML does not load: %v", err)
	}
	if !reflect.DeepEqual(zero, DefaultConfig()) {
		t.Errorf("default YAML does not reproduce the defaults:\n got %+v\nwant %+v", zero, DefaultConfig())
	}
	if err := zero.Validate(); err != nil {
		t.Errorf("reloaded defaults do not validate: %v", err)
	}
	// Loaded onto the defaults it changes nothing and has the same identity.
	d := DefaultConfig()
	if err := d.LoadYAML(buf.Bytes(), "defaults.yaml", ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d, DefaultConfig()) {
		t.Errorf("default YAML changes the defaults")
	}
	if a, b := d.identity(nil).Digest, DefaultConfig().identity(nil).Digest; a != b {
		t.Errorf("digest after round trip %s != %s", a, b)
	}

	// Every supported key appears as "key: value" under its section, with a
	// description comment; numeric keys state their range and enumerations
	// their values.
	eff := DefaultConfig().identity(nil).Effective
	lines := strings.Split(doc, "\n")
	section := ""
	present := map[string]bool{}
	described := map[string]bool{}
	for i, ln := range lines {
		switch {
		case ln != "" && !strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "#") && strings.HasSuffix(ln, ":"):
			section = strings.TrimSuffix(ln, ":")
		case strings.HasPrefix(ln, "  ") && !strings.HasPrefix(strings.TrimSpace(ln), "#"):
			key, _, ok := strings.Cut(strings.TrimSpace(ln), ":")
			if !ok {
				continue
			}
			full := section + "." + key
			present[full] = true
			// The lines directly above are its comments.
			for j := i - 1; j >= 0 && strings.HasPrefix(strings.TrimSpace(lines[j]), "#"); j-- {
				if c := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[j]), "#")); c != "" && !strings.HasPrefix(c, "range:") && !strings.HasPrefix(c, "values:") {
					described[full] = true
				}
			}
		}
	}
	var missing []string
	for key := range eff {
		if !present[key] {
			missing = append(missing, key)
		} else if !described[key] {
			missing = append(missing, key+" (no description)")
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf("default YAML lacks: %v", missing)
	}
	if len(present) != len(eff) {
		t.Errorf("default YAML has %d keys, the configuration has %d", len(present), len(eff))
	}
	for _, want := range []string{
		"schema version " + ConfigSchemaVersion,
		"# range: 1.000001 .. 1000000", // scoring.threshold
		"# values: fixed | auto",       // dark.mode
		"threshold: 1.5",
		"standard_durations: [10s, 15s, 20s, 30s, 45s, 1m, 1m30s, 2m]",
		"silence_level_dbfs: -50",
		"fusion_tolerance: 100ms",
		"min_unit: 4s",
		"max_break: 10m",
		"templates:", // the commented template example
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("default YAML lacks %q", want)
		}
	}
	if len(doc) < 5000 || !strings.Contains(doc, "\nscoring:\n") || !strings.Contains(doc, "\nresources:\n") {
		t.Errorf("default YAML looks incomplete (%d bytes)", len(doc))
	}
	// The output is stable.
	var again bytes.Buffer
	WriteDefaultYAML(&again)
	if again.String() != doc {
		t.Errorf("default YAML is not deterministic")
	}
}

// CFG-01: a non-default configuration, including templates, strings that
// need quoting and lists, survives WriteYAML -> LoadYAML.
func TestWriteYAMLRoundTripNonDefault(t *testing.T) {
	c := DefaultConfig()
	c.Scoring.Threshold = 2.25
	c.Scoring.StandardDurations = []time.Duration{7 * sec, 90 * sec, 2*60*sec + 500*ms}
	c.Logo.SearchRegions = []Region{{0.1, 0.2, 0.3, 0.4}}
	c.Sampling.Region = Region{0.05, 0.05, 0.9, 0.9}
	c.Signals.Scene, c.Boundaries.FromScene = false, true
	c.Dark.Mode = "auto"
	c.Input.AudioTrack, c.Input.CaptionTrack = "none", "3"
	c.Logo.Asset = "/assets/my logo: v2.json"
	c.Resources.ScratchDir = "/tmp/scratch #1"
	c.Baselines.CaptionStyle = "roll_up"
	c.Outputs.SkipPaddingStart = -1500 * ms
	c.Resources.MemoryBudget = 100 << 20
	c.Package.VideoSelection = "id=video/720p"
	c.BumperTemplates = []BumperTemplate{
		{ID: "station_card", Image: "/assets/card one.png", Region: Region{0, 0, 1, 1}, Geometry: "stretch", Threshold: 0.9, MinSupport: 400 * ms},
		{ID: "bug", Image: "/assets/bug.png", Region: Region{0.7, 0.1, 0.2, 0.2}, Geometry: "strict", Threshold: 0.75, MinSupport: 2 * sec, SHA256: strings.Repeat("ab", 32)},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, comments := range []bool{false, true} {
		var buf bytes.Buffer
		if err := c.WriteYAML(&buf, comments); err != nil {
			t.Fatal(err)
		}
		back := &Config{}
		if err := back.LoadYAML(buf.Bytes(), "roundtrip.yaml", ""); err != nil {
			t.Fatalf("comments=%v: written YAML does not load: %v\n%s", comments, err, buf.String())
		}
		if !reflect.DeepEqual(back, c) {
			t.Errorf("comments=%v: round trip differs:\n got %+v\nwant %+v", comments, back, c)
		}
	}
}

// CFG-01: explicit overrides through Set, including bumper templates.
func TestConfigSet(t *testing.T) {
	c := DefaultConfig()
	for _, kv := range [][2]string{
		{"scoring.threshold", "1.75"},
		{"scoring.standard_durations", "5s, 10s"},
		{"signals.audio", "false"},
		{"dark.mode", "auto"},
		{"audio.silence_level_dbfs", "-42.5"},
		{"logo.search_regions", "0,0,0.5,0.5;0.5,0.5,0.5,0.5"},
		{"sampling.region", "0.1,0.1,0.8,0.8"},
		{"resources.memory_budget", "1GiB"},
		{"scene.histogram_bins", "128"},
		{"bumpers.templates.card.image", "card.png"},
		{"bumpers.templates.card.threshold", "0.85"},
		{"bumpers.templates.card.region", "0,0,0.5,1"},
		{"bumpers.templates.card.geometry", "strict"},
		{"bumpers.templates.card.min_support", "750ms"},
		{"bumpers.templates.card.sha256", "  ABCDEF  "},
	} {
		if err := c.Set(kv[0], kv[1]); err != nil {
			t.Fatalf("Set(%s, %s): %v", kv[0], kv[1], err)
		}
	}
	if c.Scoring.Threshold != 1.75 || !reflect.DeepEqual(c.Scoring.StandardDurations, []time.Duration{5 * sec, 10 * sec}) || c.Signals.Audio || c.Dark.Mode != "auto" || c.Audio.SilenceLevel != -42.5 {
		t.Errorf("scalar overrides: %+v", c.Scoring)
	}
	if len(c.Logo.SearchRegions) != 2 || c.Logo.SearchRegions[1] != (Region{0.5, 0.5, 0.5, 0.5}) || c.Sampling.Region != (Region{0.1, 0.1, 0.8, 0.8}) || c.Resources.MemoryBudget != 1<<30 || c.Scene.HistogramBins != 128 {
		t.Errorf("list/size overrides: %+v %+v", c.Logo.SearchRegions, c.Resources)
	}
	want := BumperTemplate{ID: "card", Image: "card.png", Region: Region{0, 0, 0.5, 1}, Geometry: "strict", Threshold: 0.85, MinSupport: 750 * ms, SHA256: "abcdef"}
	if len(c.BumperTemplates) != 1 || !reflect.DeepEqual(c.BumperTemplates[0], want) {
		t.Errorf("template = %+v", c.BumperTemplates)
	}
	// A failed override leaves a clear error.
	wantInvalid(t, c.Set("scoring.threshold", "abc"), "scoring.threshold")
}

// Clone is a deep copy: the layers of one analysis never leak into another.
func TestConfigClone(t *testing.T) {
	c := DefaultConfig()
	c.BumperTemplates = []BumperTemplate{{ID: "a"}}
	d := c.Clone()
	if !reflect.DeepEqual(c, d) {
		t.Fatalf("clone differs")
	}
	d.Scoring.StandardDurations[0] = time.Hour
	d.Logo.SearchRegions[0].X = 0.9
	d.BumperTemplates[0].ID = "b"
	d.Scoring.Threshold = 9
	e := DefaultConfig()
	e.BumperTemplates = []BumperTemplate{{ID: "a"}}
	if !reflect.DeepEqual(c, e) {
		t.Errorf("modifying a clone changed the original")
	}
}

// altValue finds a different valid value for a configuration key.
func altValue(t *testing.T, key, current string) (string, bool) {
	candidates := []string{
		"true", "false", "auto", "fixed", "none", "embedded", "fail", "warn", "strict", "degraded", "rebase",
		"all", "reference", "nonref", "hls", "dash", "lowest", "highest", "exclude_lfe", "mismatch", "neutral",
		"start", "end", "either", "both", "detailed", "full", "summary", "CC2", "CC1", "roll_up", "pop_on",
		"unknown", "relative", "no_logo_evidence",
		"32", "128", "3", "7", "0.5", "0.25", "0.75", "1.5", "2.5", "12", "40", "100", "-45", "-65",
		"3s", "250ms", "50ms", "90s", "7s",
		"64MiB", "32MiB", "8KiB", "1MiB",
		"0.1,0.1,0.5,0.5", "0.2,0.2,0.3,0.3;0.6,0.6,0.3,0.3", "7s,9s",
		"other.json", "other-dir",
	}
	for _, v := range candidates {
		c := DefaultConfig()
		if err := c.Set(key, v); err != nil {
			continue
		}
		if c.identity(nil).Effective[key] != current {
			return v, true
		}
	}
	return "", false
}

// AT-19 / CFG-02: the effective configuration identity. The digest is
// stable, changes with every detection key, and ignores outputs.* and
// resources.*.
func TestConfigIdentityDigest(t *testing.T) {
	base := DefaultConfig().identity(nil)
	if !strings.HasPrefix(base.Digest, "sha256:") || len(base.Digest) != 7+64 || base.SchemaVersion != ConfigSchemaVersion {
		t.Fatalf("identity = %+v", base)
	}
	if again := DefaultConfig().Clone().identity(nil); again.Digest != base.Digest || !reflect.DeepEqual(again.Effective, base.Effective) {
		t.Errorf("digest is not stable")
	}

	keys := make([]string, 0, len(base.Effective))
	for k := range base.Effective {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) < 150 {
		t.Errorf("only %d effective keys", len(keys))
	}
	var untested []string
	for _, key := range keys {
		alt, ok := altValue(t, key, base.Effective[key])
		if !ok {
			untested = append(untested, key)
			continue
		}
		c := DefaultConfig()
		if err := c.Set(key, alt); err != nil {
			t.Fatal(err)
		}
		id := c.identity(nil)
		if id.Effective[key] == base.Effective[key] {
			t.Errorf("%s: effective value did not change", key)
		}
		section, _, _ := strings.Cut(key, ".")
		ignored := section == "outputs" || section == "resources"
		// logo.asset and logo.cache_dir are paths: what enters the digest is
		// the content digest of the logo actually used, covered below.
		if key == "logo.asset" || key == "logo.cache_dir" {
			continue
		}
		if ignored && id.Digest != base.Digest {
			t.Errorf("%s=%s changed the digest; outputs.* and resources.* must not", key, alt)
		}
		if !ignored && id.Digest == base.Digest {
			t.Errorf("%s=%s did not change the digest", key, alt)
		}
	}
	// Keys with a single allowed value cannot be varied.
	sort.Strings(untested)
	if got := strings.Join(untested, ","); got != "audio.metric,boundaries.span_policy,input.decoder" {
		t.Errorf("keys without a testable alternative value: %s", got)
	}

	// Two different changes give two different digests.
	a, b := DefaultConfig(), DefaultConfig()
	a.Scoring.Threshold, b.Scoring.Threshold = 1.6, 1.7
	if a.identity(nil).Digest == b.identity(nil).Digest {
		t.Errorf("different values share a digest")
	}
}

// AT-19 / CFG-02, BUMPER-03: asset content is part of the identity; where
// the asset lives is not, and paths are redacted to their base name.
func TestConfigIdentityAssets(t *testing.T) {
	digest := func(c *Config) (string, map[string]string) {
		t.Helper()
		as, err := LoadAssets(c)
		if err != nil {
			t.Fatalf("LoadAssets: %v", err)
		}
		id := c.identity(as.digests)
		return id.Digest, id.Effective
	}
	withTemplate := func(png []byte, path string) *Config {
		c := DefaultConfig()
		c.BumperTemplates = []BumperTemplate{{ID: "card", PNG: png, Image: path, Region: Region{0, 0, 1, 1}, Geometry: "stretch", Threshold: 0.9, MinSupport: 400 * ms}}
		return c
	}
	pngA, pngB := cardPNG(t, 64, 36, 8, 6), cardPNG(t, 64, 36, 5, 3)
	none, _ := digest(DefaultConfig())
	dA, _ := digest(withTemplate(pngA, ""))
	dB, _ := digest(withTemplate(pngB, ""))
	if dA == none || dA == dB {
		t.Errorf("bumper template content does not change the digest (none %s, A %s, B %s)", none, dA, dB)
	}
	// The same image from two different directories: same digest, and the
	// effective configuration shows only the base name.
	dir1, dir2 := t.TempDir(), t.TempDir()
	p1, p2 := filepath.Join(dir1, "card.png"), filepath.Join(dir2, "card.png")
	os.WriteFile(p1, pngA, 0o600)
	os.WriteFile(p2, pngA, 0o600)
	d1, eff1 := digest(withTemplate(nil, p1))
	d2, _ := digest(withTemplate(nil, p2))
	if d1 != d2 || d1 != dA {
		t.Errorf("digest depends on where the asset is stored: %s %s (data: %s)", d1, d2, dA)
	}
	if eff1["bumpers.templates.card.image"] != "card.png" {
		t.Errorf("effective image = %q, want the redacted base name", eff1["bumpers.templates.card.image"])
	}
	for k, v := range eff1 {
		if strings.Contains(v, dir1) {
			t.Errorf("effective %s leaks a directory: %q", k, v)
		}
	}
	// Changing the file's content changes the digest.
	os.WriteFile(p2, pngB, 0o600)
	if d2b, _ := digest(withTemplate(nil, p2)); d2b == d1 {
		t.Errorf("changed template file did not change the digest")
	}
	// Template parameters are detection keys too.
	c := withTemplate(pngA, "")
	c.BumperTemplates[0].Threshold = 0.8
	if d, _ := digest(c); d == dA {
		t.Errorf("template threshold did not change the digest")
	}
	c = withTemplate(pngA, "")
	c.BumperTemplates[0].Region = Region{0, 0, 0.5, 1}
	if d, _ := digest(c); d == dA {
		t.Errorf("template region did not change the digest")
	}

	// Logo assets: content matters, as caller data and as a file.
	asset := learnedAsset(t)
	withLogo := func(a *LogoAsset) *Config {
		c := DefaultConfig()
		c.LogoAssetData = a
		return c
	}
	dl, eff := digest(withLogo(asset))
	if dl == none || eff["logo.asset_data"] == "" {
		t.Errorf("logo asset data does not change the digest")
	}
	// The same mask at a different position is a different logo.
	moved := *asset
	moved.X -= 5
	if dm, _ := digest(withLogo(&moved)); dm == dl {
		t.Errorf("moving the logo asset (same mask, different position) did not change the digest")
	}
	relabeled := *asset
	relabeled.AnalysisWidth, relabeled.AnalysisHeight = 128, 72
	if dm, _ := digest(withLogo(&relabeled)); dm == dl {
		t.Errorf("changing the logo asset's analysis geometry did not change the digest")
	}
	data, _ := asset.Marshal()
	f1, f2 := filepath.Join(dir1, "logo.json"), filepath.Join(dir2, "logo.json")
	os.WriteFile(f1, data, 0o600)
	os.WriteFile(f2, data, 0o600)
	fileCfg := func(p string) *Config { c := DefaultConfig(); c.Logo.Asset = p; return c }
	df1, effF := digest(fileCfg(f1))
	df2, _ := digest(fileCfg(f2))
	if df1 != df2 || df1 == none || effF["logo.asset"] != "logo.json" {
		t.Errorf("logo asset file: %s %s effective %q", df1, df2, effF["logo.asset"])
	}
	movedData, _ := (&moved).Marshal()
	os.WriteFile(f2, movedData, 0o600)
	if df2b, _ := digest(fileCfg(f2)); df2b == df1 {
		t.Errorf("changed logo asset file did not change the digest")
	}
}

// PIPE-03: the feature identity covers input identity and feature settings
// but not score weights, so changing weights alone does not invalidate
// extracted features; the configuration digest covers both.
func TestFeatureIdentity(t *testing.T) {
	src := newScenario(blk{D: 5 * sec}).source()
	run := func(mut func(c *Config)) *Result {
		cfg := testConfig()
		if mut != nil {
			mut(cfg)
		}
		return analyze(t, src, cfg)
	}
	base := run(nil)
	weights := run(func(c *Config) { c.Scoring.Threshold = 3; c.Scoring.LogoAbsentFactor = 5; c.Breaks.MinBreak = 30 * sec })
	if weights.FeatureID != base.FeatureID {
		t.Errorf("score weights changed the feature identity")
	}
	if weights.Config.Digest == base.Config.Digest {
		t.Errorf("score weights did not change the configuration digest")
	}
	for name, mut := range map[string]func(c *Config){
		"scene bins":      func(c *Config) { c.Scene.HistogramBins = 32 },
		"audio window":    func(c *Config) { c.Audio.Window = 40 * ms },
		"logo sampling":   func(c *Config) { c.Logo.SampleInterval = 250 * ms },
		"signal switch":   func(c *Config) { c.Signals.Aspect = false },
		"analysis size":   func(c *Config) { c.Sampling.AnalysisMaxWidth = 320 },
		"bright pixel":    func(c *Config) { c.Dark.BrightPixelLuma = 80 },
		"caption service": func(c *Config) { c.Captions.Service = "CC3" },
	} {
		if r := run(mut); r.FeatureID == base.FeatureID {
			t.Errorf("%s did not change the feature identity", name)
		}
	}
	out := run(func(c *Config) { c.Outputs.SkipPaddingStart = sec; c.Resources.Workers = 3 })
	if out.FeatureID != base.FeatureID || out.Config.Digest != base.Config.Digest {
		t.Errorf("output/resource settings changed an identity")
	}
	// A different input is a different feature identity.
	src2 := newScenario(blk{D: 5 * sec}).source()
	src2.Media.Input = InputInfo{Kind: "file", Path: "/x/a.ts", Identity: "100:200"}
	a := analyze(t, src2, testConfig())
	src2.Media.Input.Identity = "100:201"
	b := analyze(t, src2, testConfig())
	if a.FeatureID == b.FeatureID || a.FeatureID == base.FeatureID {
		t.Errorf("input identity is not part of the feature identity")
	}
}

func TestByteSizeAndDurationFormatting(t *testing.T) {
	for _, c := range []struct {
		in   string
		want ByteSize
		str  string
	}{
		{"512MiB", 512 << 20, "512MiB"}, {"1GiB", 1 << 30, "1GiB"}, {"4KiB", 4096, "4KiB"}, {"100B", 100, "100B"},
		{"1024", 1024, "1KiB"}, {"1536KiB", 1536 << 10, "1536KiB"}, {" 2 MiB ", 2 << 20, "2MiB"}, {"0", 0, "0B"}, {"1025B", 1025, "1025B"},
	} {
		got, err := parseByteSize(c.in)
		if err != nil || got != c.want || got.String() != c.str {
			t.Errorf("parseByteSize(%q) = %d (%s) err %v, want %d (%s)", c.in, got, got, err, c.want, c.str)
		}
	}
	for _, bad := range []string{"", "MiB", "1.5MiB", "-1B", "12 parsecs", "99999999999GiB"} {
		if _, err := parseByteSize(bad); err == nil {
			t.Errorf("parseByteSize(%q) accepted", bad)
		}
	}
	for d, want := range map[time.Duration]string{
		0: "0s", 100 * ms: "100ms", 30 * sec: "30s", 60 * sec: "1m", 90 * sec: "1m30s", time.Hour: "1h", 90 * time.Minute: "1h30m", 24 * time.Hour: "24h",
	} {
		got := formatDuration(d)
		if got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
		back, err := parseDuration(got)
		if err != nil || back != d {
			t.Errorf("parseDuration(%q) = %v %v", got, back, err)
		}
	}
}

func TestRegionPixels(t *testing.T) {
	for _, c := range []struct {
		r              Region
		w, h           int
		x0, y0, x1, y1 int
	}{
		{Region{0, 0, 1, 1}, 64, 36, 0, 0, 64, 36},
		{Region{0.25, 0.25, 0.5, 0.5}, 64, 36, 16, 9, 48, 27},
		{Region{0.66, 0.03, 0.32, 0.26}, 64, 36, 42, 1, 63, 10},
		// Never empty, never outside the picture.
		{Region{0.999, 0.999, 0.001, 0.001}, 64, 36, 63, 35, 64, 36},
		{Region{0, 0, 0.001, 0.001}, 64, 36, 0, 0, 1, 1},
	} {
		x0, y0, x1, y1 := c.r.pixels(c.w, c.h)
		if x0 != c.x0 || y0 != c.y0 || x1 != c.x1 || y1 != c.y1 {
			t.Errorf("%v.pixels(%d,%d) = %d,%d,%d,%d, want %d,%d,%d,%d", c.r, c.w, c.h, x0, y0, x1, y1, c.x0, c.y0, c.x1, c.y1)
		}
	}
}

// R7 / PIPE-03: the feature identity covers the contents of assets, not the
// names the result displays: a different bumper image or logo asset, as
// caller data or as a file of the same name, is a different identity.
func TestFeatureIdentityCoversAssetContents(t *testing.T) {
	src := newScenario(blk{D: 5 * sec}).source()
	id := func(cfg *Config) string {
		t.Helper()
		return analyze(t, src, cfg).FeatureID
	}
	// A geometry that fits the scenario's frames is not needed here: assets
	// are identified whether or not they match.
	bumper := func(png []byte, path string) *Config {
		cfg := testConfig()
		cfg.BumperTemplates = []BumperTemplate{{
			ID: "card", PNG: png, Image: path, Region: Region{0, 0, 1, 1},
			Geometry: "stretch", Threshold: 0.9, MinSupport: 400 * time.Millisecond,
		}}
		return cfg
	}
	pngA, pngB := cardPNG(t, 128, 72, 8, 6), cardPNG(t, 128, 72, 4, 3)
	none := id(testConfig())
	a, b := id(bumper(pngA, "")), id(bumper(pngB, ""))
	if a == none || a == b {
		t.Errorf("in-memory bumper pixels are not part of the feature identity (none %s, A %s, B %s)", none, a, b)
	}
	if again := id(bumper(pngA, "")); again != a {
		t.Errorf("the same bumper image gave two feature identities")
	}
	// The same file name with different contents.
	path := filepath.Join(t.TempDir(), "card.png")
	if err := os.WriteFile(path, pngA, 0o600); err != nil {
		t.Fatal(err)
	}
	fa := id(bumper(nil, path))
	if err := os.WriteFile(path, pngB, 0o600); err != nil {
		t.Fatal(err)
	}
	if fb := id(bumper(nil, path)); fb == fa {
		t.Errorf("a changed bumper file under the same name kept the feature identity")
	}

	// Logo assets, as caller data and as a file.
	asset := learnedAsset(t)
	moved := *asset
	moved.X -= 5
	logo := func(l *LogoAsset) *Config { cfg := testConfig(); cfg.LogoAssetData = l; return cfg }
	la, lb := id(logo(asset)), id(logo(&moved))
	if la == none || la == lb {
		t.Errorf("logo asset data is not part of the feature identity (none %s, A %s, B %s)", none, la, lb)
	}
	lpath := filepath.Join(t.TempDir(), "logo.json")
	data, _ := asset.Marshal()
	os.WriteFile(lpath, data, 0o600)
	file := func() *Config { cfg := testConfig(); cfg.Logo.Asset = lpath; return cfg }
	f1 := id(file())
	data, _ = (&moved).Marshal()
	os.WriteFile(lpath, data, 0o600)
	if f2 := id(file()); f2 == f1 {
		t.Errorf("a changed logo asset file under the same name kept the feature identity")
	}
}
