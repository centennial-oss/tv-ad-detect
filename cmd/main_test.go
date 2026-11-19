// SPDX-License-Identifier: Apache-2.0

package main

// AT-21, AT-28: the command-line application (CLI-01..03, OUT-01..03,
// CFG-02, API-05). The tests drive the built binary so that exit codes,
// signal handling and the separation of stdout and stderr are the real
// ones. Media fixtures are synthesized by scripts/make-fixtures.sh with
// the ffmpeg command-line tool (test tooling only) and are never
// committed; tests that need them skip when ffmpeg is not on PATH.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/ffadapter"
)

const (
	testCommit    = "c0ffee1"
	testBuildDate = "2026-01-02T03:04:05Z"
)

var env struct {
	fixtures string
	script   string
	tools    bool
	scratch  string

	mu   sync.Mutex
	once map[string]*sync.Once
	errs map[string]error

	buildOnce sync.Once
	binary    string
	buildErr  error
}

var fixtureDeps = map[string][]string{
	"broadcast_low": {"broadcast"},
	"broadcast_ts":  {"broadcast"},
	"hls_master":    {"broadcast", "broadcast_low"},
	"hls_ts":        {"broadcast"},
	"dash":          {"broadcast", "broadcast_low"},
}

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(func() int {
		env.once, env.errs = map[string]*sync.Once{}, map[string]error{}
		env.tools = true
		for _, tool := range []string{"ffmpeg", "ffprobe", "bash"} {
			if _, err := exec.LookPath(tool); err != nil {
				env.tools = false
			}
		}
		var err error
		if env.script, err = filepath.Abs(filepath.Join("..", "scripts", "make-fixtures.sh")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		scratch, err := os.MkdirTemp("", "tvad-cli-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(scratch)
		if env.scratch, err = filepath.EvalSymlinks(scratch); err != nil {
			env.scratch = scratch
		}
		env.fixtures = filepath.Join(env.scratch, "fixtures")
		if dir := os.Getenv("TVAD_FIXTURE_DIR"); dir != "" {
			if env.fixtures, err = filepath.Abs(dir); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
		return m.Run()
	}())
}

// fixture synthesizes the named fixture on first use and returns the path
// of a file inside it.
func fixture(t testing.TB, name string, rel ...string) string {
	t.Helper()
	if !env.tools {
		t.Skip("ffmpeg, ffprobe or bash is not on PATH: media fixtures cannot be synthesized")
	}
	for _, dep := range fixtureDeps[name] {
		fixture(t, dep)
	}
	env.mu.Lock()
	once := env.once[name]
	if once == nil {
		once = new(sync.Once)
		env.once[name] = once
	}
	env.mu.Unlock()
	once.Do(func() {
		out, err := exec.Command("bash", env.script, "-o", env.fixtures, name).CombinedOutput()
		if err != nil {
			err = fmt.Errorf("make-fixtures.sh %s: %v\n%s", name, err, out)
		}
		env.mu.Lock()
		env.errs[name] = err
		env.mu.Unlock()
	})
	env.mu.Lock()
	err := env.errs[name]
	env.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(append([]string{env.fixtures, name}, rel...)...)
}

func long(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("long test (analyzes the synthetic broadcast); skipped with -short")
	}
}

// cliBinary builds the command once, with a known build identity.
func cliBinary(t testing.TB) string {
	t.Helper()
	env.buildOnce.Do(func() {
		env.binary = filepath.Join(env.scratch, "tv-ad-detect")
		if runtime.GOOS == "windows" {
			env.binary += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", env.binary,
			"-ldflags", "-X main.commit="+testCommit+" -X main.buildDate="+testBuildDate, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			env.buildErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if env.buildErr != nil {
		t.Fatal(env.buildErr)
	}
	return env.binary
}

type cliResult struct {
	code   int
	stdout string
	stderr string
}

// cli runs the binary in dir and returns its exit code and output.
func cli(t testing.TB, dir string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(cliBinary(t), args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := cliResult{stdout: stdout.String(), stderr: stderr.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.code = ee.ExitCode()
	default:
		t.Fatalf("running %v: %v", args, err)
	}
	return res
}

func (r cliResult) want(t testing.TB, code int) cliResult {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit code %d, want %d\nstdout: %.400s\nstderr: %s", r.code, code, r.stdout, r.stderr)
	}
	return r
}

// oneJSONDocument requires data to be exactly one JSON value followed only
// by whitespace, and decodes it strictly as the canonical result.
func oneJSONDocument(t testing.TB, data string) (*addetect.Result, map[string]json.RawMessage) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(data))
	dec.DisallowUnknownFields()
	var res addetect.Result
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("output is not a result document: %v\n%.300s", err, data)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("output holds more than one JSON document (next: %.80s, err %v)", extra, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		t.Fatal(err)
	}
	return &res, raw
}

// stderrIsLogOnly requires every stderr line to be a progress report or a
// warning: nothing else (in particular no decoder chatter) may appear.
func stderrIsLogOnly(t testing.TB, stderr string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimRight(stderr, "\n"), "\n") {
		if line != "" && !strings.HasPrefix(line, "progress: ") && !strings.HasPrefix(line, "warning: ") {
			t.Errorf("unexpected stderr line: %q", line)
		}
	}
}

func dirEntries(t testing.TB, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func writeFile(t testing.TB, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// brokenPackage copies the HLS master package without one video segment.
func brokenPackage(t testing.TB) string {
	t.Helper()
	src := fixture(t, "hls_master")
	dst := filepath.Join(t.TempDir(), "pkg")
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if filepath.Base(p) == "seg_010.m4s" && strings.Contains(p, "main") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dst, "master.m3u8")
}

// ---- tests -----------------------------------------------------------------

// AT-21 / CLI-01: --version reports application, schema and build identity.
func TestVersionAndHelp(t *testing.T) {
	t.Parallel()
	r := cli(t, "", "--version").want(t, 0)
	for _, want := range []string{
		addetect.ProductName + " " + addetect.Version,
		"result schema " + addetect.SchemaVersion,
		"config schema " + addetect.ConfigSchemaVersion,
		"feature schema " + addetect.FeatureSchemaVersion,
		ffadapter.AdapterName + "/" + ffadapter.AdapterVersion,
		testCommit, testBuildDate,
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("--version output lacks %q:\n%s", want, r.stdout)
		}
	}
	if r.stderr != "" {
		t.Errorf("--version wrote to stderr: %q", r.stderr)
	}
	h := cli(t, "", "--help").want(t, 0)
	for _, want := range []string{"analyze INPUT", "config defaults", "config validate", "--version", "--output", "--channel-config", "Exit codes"} {
		if !strings.Contains(h.stdout, want) {
			t.Errorf("--help output lacks %q", want)
		}
	}
}

// AT-21, AT-19 / CLI-01, CFG-01: "config defaults" prints a complete
// configuration that "config validate" accepts, and that reproduces the
// built-in defaults exactly; validation rejects bad layers and missing
// assets without decoding anything.
func TestConfigDefaultsValidateRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := cli(t, dir, "config", "defaults").want(t, 0)
	if d.stderr != "" {
		t.Errorf("config defaults wrote to stderr: %q", d.stderr)
	}
	path := writeFile(t, filepath.Join(dir, "defaults.yaml"), d.stdout)
	v := cli(t, dir, "config", "validate", "--config", path).want(t, 0)
	if !strings.Contains(v.stdout, "valid") {
		t.Errorf("config validate output = %q", v.stdout)
	}

	// The printed document sets every key to its default: layering it over
	// the defaults changes nothing, and it mentions every effective key.
	cfg := addetect.DefaultConfig()
	if err := cfg.LoadYAMLFile(path); err != nil {
		t.Fatalf("the default configuration does not load: %v", err)
	}
	var a, b bytes.Buffer
	if err := cfg.WriteYAML(&a, false); err != nil {
		t.Fatal(err)
	}
	if err := addetect.DefaultConfig().WriteYAML(&b, false); err != nil {
		t.Fatal(err)
	}
	if a.String() != b.String() {
		t.Errorf("loading the printed defaults changed the configuration")
	}
	for _, want := range []string{"max_mean_luma: 19", "silence_level_dbfs: -50", "min_silence: 100ms", "alignment: 250ms",
		"fusion_tolerance: 100ms", "threshold: 1.5", "strict_tolerance: 500ms", "min_break: 20s", "max_break: 10m", "failure_policy: fail"} {
		if !strings.Contains(d.stdout, want) {
			t.Errorf("config defaults lacks the documented initial value %q", want)
		}
	}
	if _, err := os.Stat("/dev/stdin"); err == nil {
		// The documented pipe: config defaults | config validate.
		cmd := exec.Command(cliBinary(t), "config", "validate", "--config", "/dev/stdin")
		cmd.Stdin = strings.NewReader(d.stdout)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("config defaults | config validate --config /dev/stdin: %v\n%s", err, out)
		}
	}

	// Layers and overrides are validated together.
	cli(t, dir, "config", "validate", "--config", path, "--set", "scoring.threshold=2").want(t, 0)
	cli(t, dir, "config", "validate").want(t, 0)
	for name, args := range map[string][]string{
		"unknown key":        {"--set", "scoring.no_such_key=1"},
		"out of range":       {"--set", "scoring.threshold=0.5"},
		"missing unit":       {"--set", "audio.min_silence=100"},
		"inverted tolerance": {"--set", "scoring.strict_tolerance=2s"},
		"inverted limits":    {"--set", "breaks.min_break=20m"},
		"missing layer":      {"--config", filepath.Join(dir, "nope.yaml")},
		"missing logo asset": {"--set", "logo.asset=" + filepath.Join(dir, "nope.json")},
		"positional":         {"extra"},
		"malformed override": {"--set", "novalue"},
	} {
		r := cli(t, dir, append([]string{"config", "validate"}, args...)...)
		if r.code != exitUsage {
			t.Errorf("config validate (%s): exit code %d, want %d; stderr: %s", name, r.code, exitUsage, r.stderr)
		}
		if r.stdout != "" || r.stderr == "" {
			t.Errorf("config validate (%s): stdout %q, stderr %q", name, r.stdout, r.stderr)
		}
	}
	bad := writeFile(t, filepath.Join(dir, "dup.yaml"), "scoring:\n  threshold: 2\n  threshold: 3\n")
	cli(t, dir, "config", "validate", "--config", bad).want(t, exitUsage)
	cli(t, dir, "config").want(t, exitUsage)
	cli(t, dir, "config", "frobnicate").want(t, exitUsage)
}

// AT-21 / CLI-01, CLI-02, OUT-01, OUT-03, API-05: "analyze INPUT --output -"
// writes exactly one valid JSON document to stdout and only progress and
// warnings to stderr, also for inputs that make the decoder complain.
func TestAnalyzeStdoutIsExactlyOneJSONDocument(t *testing.T) {
	t.Parallel()
	inputs := []string{
		fixture(t, "matrix", "mp4_aac.mp4"),
		fixture(t, "coverart", "coverart.mp4"),    // libavformat: "timescale not set"
		fixture(t, "reschange", "reschange.ts"),   // libavformat: "Packet corrupt"
		fixture(t, "noaudio", "noaudio.mp4"),      // a warning
		fixture(t, "interlaced", "interlaced.ts"), // a warning appended by the core
	}
	for _, in := range inputs {
		t.Run(filepath.Base(in), func(t *testing.T) {
			for _, args := range [][]string{
				{"analyze", in, "--output", "-", "--progress"},
				{"analyze", in, "--progress", "--pretty", "--diagnostics", "full"}, // "-" is the default
				{"analyze", "--output=-", "--diagnostics=detailed", in},            // flags before the input
			} {
				r := cli(t, t.TempDir(), args...).want(t, exitOK)
				res, raw := oneJSONDocument(t, r.stdout)
				stderrIsLogOnly(t, r.stderr)
				if !strings.Contains(r.stderr, "progress: ") && strings.Contains(strings.Join(args, " "), "--progress") {
					t.Errorf("no progress on stderr: %q", r.stderr)
				}
				for _, key := range []string{"schema_version", "product", "complete", "time_unit", "duration_us", "coverage", "input",
					"tracks", "adapter", "timing", "config", "feature_identity", "detectors", "warnings", "calibration", "baselines",
					"segments", "ad_breaks", "ad_units", "skip_intervals"} {
					if _, ok := raw[key]; !ok {
						t.Errorf("result lacks %q (OUT-01)", key)
					}
				}
				// OUT-03: empty detections are empty arrays, not null.
				for _, key := range []string{"ad_breaks", "ad_units", "skip_intervals", "warnings"} {
					if s := strings.TrimSpace(string(raw[key])); !strings.HasPrefix(s, "[") {
						t.Errorf("%s = %s, want an array", key, s)
					}
				}
				if res.SchemaVersion != addetect.SchemaVersion || res.TimeUnit != "microseconds" || !res.Complete || res.Error != nil {
					t.Errorf("schema %q, unit %q, complete %v, error %+v", res.SchemaVersion, res.TimeUnit, res.Complete, res.Error)
				}
				if res.Product.Name != addetect.ProductName || res.Product.Build != testCommit+" "+testBuildDate {
					t.Errorf("product = %+v", res.Product)
				}
				// Every warning in the document is also reported on stderr.
				for _, w := range res.Warnings {
					if !strings.Contains(r.stderr, "warning: "+w.Code+": ") {
						t.Errorf("warning %s is not on stderr", w.Code)
					}
				}
				if len(res.AdBreaks) != 0 {
					t.Errorf("breaks in a few seconds of test pattern: %+v", res.AdBreaks)
				}
				wantDiag := strings.Contains(strings.Join(args, " "), "--diagnostics")
				if (res.Diagnostics != nil) != wantDiag {
					t.Errorf("diagnostics present = %v, want %v", res.Diagnostics != nil, wantDiag)
				}
			}
		})
	}
	// --quiet keeps warnings out of stderr; they stay in the document.
	r := cli(t, t.TempDir(), "analyze", fixture(t, "noaudio", "noaudio.mp4"), "--quiet").want(t, exitOK)
	res, _ := oneJSONDocument(t, r.stdout)
	if r.stderr != "" || len(res.Warnings) == 0 {
		t.Errorf("--quiet: stderr %q, %d warnings in the document", r.stderr, len(res.Warnings))
	}
}

// normalizeResult makes two results of the same analysis comparable:
// build identity is the caller's.
func normalizeResult(t testing.TB, res *addetect.Result) []byte {
	t.Helper()
	c := *res
	c.Product.Build = ""
	b, err := json.MarshalIndent(&c, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// AT-21, AT-28 / CLI-01, API-01, QUAL-01: the CLI is the library: for a
// recording, an HLS master playlist and a DASH MPD, the document the
// command writes equals the result of addetect.Analyze over the same path.
func TestCLIAndLibraryResultsAgree(t *testing.T) {
	long(t)
	t.Parallel()
	for _, tc := range []struct{ name, input, kind string }{
		{"recording", fixture(t, "broadcast", "broadcast.mp4"), "file"},
		{"HLS master playlist", fixture(t, "hls_master", "master.m3u8"), "hls"},
		{"DASH manifest", fixture(t, "dash", "manifest.mpd"), "dash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := filepath.Join(t.TempDir(), "result.json")
			r := cli(t, "", "analyze", tc.input, "--output", out, "--diagnostics", "detailed").want(t, exitOK)
			if r.stdout != "" {
				t.Errorf("stdout is not empty when writing to a file: %.200s", r.stdout)
			}
			stderrIsLogOnly(t, r.stderr)
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := oneJSONDocument(t, string(data))

			cfg := addetect.DefaultConfig()
			cfg.Outputs.Diagnostics = "detailed"
			want, err := addetect.Analyze(context.Background(), ffadapter.Path(tc.input), cfg, nil)
			if err != nil {
				t.Fatalf("library analysis: %v", err)
			}
			// Compare through the same serialization.
			wantJSON, _ := json.Marshal(want)
			var wantRT addetect.Result
			if err := json.Unmarshal(wantJSON, &wantRT); err != nil {
				t.Fatal(err)
			}
			if a, b := normalizeResult(t, got), normalizeResult(t, &wantRT); !bytes.Equal(a, b) {
				la, lb := bytes.Split(a, []byte("\n")), bytes.Split(b, []byte("\n"))
				for i := 0; i < len(la) && i < len(lb); i++ {
					if !bytes.Equal(la[i], lb[i]) {
						t.Fatalf("CLI and library results differ: %s vs %s", la[i], lb[i])
					}
				}
				t.Fatalf("CLI and library results differ in length")
			}

			// The annotated break of the synthetic broadcast (see
			// scripts/make-fixtures.sh): [100.3 s, 190.3 s), four units.
			if got.Input.Kind != tc.kind || !got.Complete || got.DurationUS != 280_600_000 {
				t.Errorf("input kind %q, complete %v, duration %d", got.Input.Kind, got.Complete, got.DurationUS)
			}
			if len(got.AdBreaks) != 1 || got.AdBreaks[0].StartUS != 100_300_000 || got.AdBreaks[0].EndUS != 190_300_000 || len(got.AdUnits) != 4 {
				t.Errorf("breaks = %+v, units %d; want one break [100.3 s, 190.3 s) of 4 units", got.AdBreaks, len(got.AdUnits))
			}
			// AT-28: one presentation-level result with the resource and
			// byte-range mapping for packaged input.
			if tc.kind == "file" {
				if got.Package != nil {
					t.Errorf("a recording has package information")
				}
				return
			}
			pk := got.Package
			if pk == nil || pk.Format != tc.kind || len(pk.Manifests) == 0 || len(pk.Resources) < 57 || len(pk.Inits) != 2 || len(pk.Epochs) != 1 {
				t.Fatalf("package information = %+v", pk)
			}
			for _, r := range pk.Resources {
				if r.Status != "ok" || r.URI == "" || (r.Track == "video" && (r.StartUS == nil || r.EndUS == nil)) {
					t.Fatalf("resource mapping %+v", r)
				}
			}
			// Resource boundaries are neither cuts nor truncated edges.
			if got.AdBreaks[0].TruncatedStart || got.AdBreaks[0].TruncatedEnd || len(got.Segments) != 3 {
				t.Errorf("break %+v, %d segments", got.AdBreaks[0], len(got.Segments))
			}
		})
	}
}

// AT-28 / PACK-05, CLI-01: fixed selection is honored from the command line.
func TestCLIPackageSelection(t *testing.T) {
	long(t)
	t.Parallel()
	r := cli(t, "", "analyze", fixture(t, "hls_master", "master.m3u8"), "--set", "package.video_selection=index=0").want(t, exitOK)
	res, _ := oneJSONDocument(t, r.stdout)
	if res.Package.Video.ID != "v0" || res.Tracks.Video.CodedWidth != 160 || res.Timing.Frames != 2806 {
		t.Errorf("selected %+v, coded width %d, %d frames", res.Package.Video, res.Tracks.Video.CodedWidth, res.Timing.Frames)
	}
	r = cli(t, "", "analyze", fixture(t, "dash", "manifest.mpd"), "--set", "package.video_selection=id=1", "--set", "package.audio_selection=none").want(t, exitOK)
	res, _ = oneJSONDocument(t, r.stdout)
	if res.Package.Video.ID != "1" || res.Package.Audio != nil || res.Tracks.Video.CodedWidth != 160 {
		t.Errorf("selected video %+v audio %+v", res.Package.Video, res.Package.Audio)
	}
	cli(t, "", "analyze", fixture(t, "dash", "manifest.mpd"), "--set", "package.video_selection=id=77").want(t, exitUsage)
}

// AT-19, AT-21 / CFG-02, CLI-01: built-in defaults < --config <
// --channel-config < --set and the stream-selection flags. Only supplied
// keys replace lower layers, lists replace, and the effective
// configuration with its digest is in the result.
func TestConfigLayering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	in := fixture(t, "two_audio", "two_audio.mkv")
	base := writeFile(t, filepath.Join(dir, "base.yaml"), `
scoring:
  threshold: 3
  standard_durations: [15s, 30s]
audio:
  silence_level_dbfs: -40
  min_silence: 300ms
breaks:
  min_break: 30s
`)
	channel := writeFile(t, filepath.Join(dir, "channel.yaml"), `
scoring:
  threshold: 2.5
audio:
  min_silence: 200ms
input:
  audio_track: "2"
`)
	effective := func(args ...string) (*addetect.Result, map[string]string) {
		t.Helper()
		r := cli(t, dir, append([]string{"analyze", in}, args...)...).want(t, exitOK)
		res, _ := oneJSONDocument(t, r.stdout)
		return res, res.Config.Effective
	}
	check := func(eff map[string]string, want map[string]string) {
		t.Helper()
		for k, v := range want {
			if eff[k] != v {
				t.Errorf("effective %s = %q, want %q", k, eff[k], v)
			}
		}
	}

	def, eff := effective()
	check(eff, map[string]string{"scoring.threshold": "1.5", "audio.silence_level_dbfs": "-50", "audio.min_silence": "100ms",
		"breaks.min_break": "20s", "input.audio_track": "auto", "scoring.standard_durations": "10s,15s,20s,30s,45s,1m,1m30s,2m"})

	b, eff := effective("--config", base)
	check(eff, map[string]string{"scoring.threshold": "3", "audio.silence_level_dbfs": "-40", "audio.min_silence": "300ms",
		"breaks.min_break": "30s", "scoring.standard_durations": "15s,30s", "breaks.max_break": "10m"})

	// Relative paths are resolved against the working directory.
	bc, eff := effective("--config", "base.yaml", "--channel-config", "channel.yaml")
	check(eff, map[string]string{"scoring.threshold": "2.5", "audio.silence_level_dbfs": "-40", "audio.min_silence": "200ms",
		"breaks.min_break": "30s", "scoring.standard_durations": "15s,30s", "input.audio_track": "2"})
	if bc.Tracks.Audio == nil || bc.Tracks.Audio.Index != 2 || bc.Tracks.Audio.Selection != "explicit" {
		t.Errorf("channel layer selected audio %+v, want stream 2", bc.Tracks.Audio)
	}

	all, eff := effective("--config", base, "--channel-config", channel,
		"--set", "audio.silence_level_dbfs=-45", "--set", "scoring.standard_durations=10s,20s", "--audio-track", "1", "--set", "breaks.min_break=25s")
	check(eff, map[string]string{"scoring.threshold": "2.5", "audio.silence_level_dbfs": "-45", "audio.min_silence": "200ms",
		"breaks.min_break": "25s", "scoring.standard_durations": "10s,20s", "input.audio_track": "1"})
	if all.Tracks.Audio == nil || all.Tracks.Audio.Index != 1 {
		t.Errorf("--audio-track 1 selected %+v", all.Tracks.Audio)
	}

	// The channel layer alone leaves the base layer's keys at defaults.
	_, eff = effective("--channel-config", channel)
	check(eff, map[string]string{"scoring.threshold": "2.5", "audio.silence_level_dbfs": "-50", "breaks.min_break": "20s"})

	digests := map[string]bool{def.Config.Digest: true, b.Config.Digest: true, bc.Config.Digest: true, all.Config.Digest: true}
	if len(digests) != 4 || !strings.HasPrefix(def.Config.Digest, "sha256:") {
		t.Errorf("configuration digests are not distinct per effective configuration: %v", digests)
	}
	// Output-only settings do not change the digest; --overwrite is one.
	ow, _ := effective("--overwrite", "--pretty")
	if ow.Config.Digest != def.Config.Digest {
		t.Errorf("--overwrite changed the configuration digest")
	}
	// Score weights do not invalidate extracted features; feature settings
	// do (PIPE-03).
	scoreOnly, _ := effective("--set", "scoring.threshold=3", "--set", "breaks.min_break=30s")
	featureSetting, _ := effective("--set", "audio.window=40ms")
	if scoreOnly.FeatureID != def.FeatureID || scoreOnly.Config.Digest == def.Config.Digest {
		t.Errorf("scoring-only overrides: feature identity changed=%v, digest changed=%v", scoreOnly.FeatureID != def.FeatureID, scoreOnly.Config.Digest != def.Config.Digest)
	}
	if featureSetting.FeatureID == def.FeatureID {
		t.Errorf("audio.window did not change the feature identity")
	}

	// Errors in any layer are configuration errors before analysis.
	bad := writeFile(t, filepath.Join(dir, "bad.yaml"), "scoring:\n  treshold: 2\n")
	for _, args := range [][]string{
		{"--config", bad},
		{"--channel-config", bad},
		{"--config", base, "--set", "scoring.threshold=abc"},
		{"--set", "input.audio_track=first"},
		{"--diagnostics", "verbose"},
	} {
		r := cli(t, dir, append([]string{"analyze", in}, args...)...)
		if r.code != exitUsage || r.stdout != "" {
			t.Errorf("analyze %v: exit code %d (want %d), stdout %q, stderr %s", args, r.code, exitUsage, r.stdout, r.stderr)
		}
	}
}

// AT-21 / CLI-02: exit codes 0, 2, 3, 4 and 5, each with an error on
// stderr and nothing on stdout.
func TestExitCodes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ok := fixture(t, "matrix", "mp4_aac.mp4")
	existing := writeFile(t, filepath.Join(dir, "existing.json"), "{}")
	hls := ""
	if !testing.Short() {
		hls = fixture(t, "hls_master", "master.m3u8")
	}
	cases := []struct {
		name string
		args []string
		code int
		kind string
	}{
		{"no command", nil, exitUsage, "usage"},
		{"unknown command", []string{"frobnicate"}, exitUsage, "usage"},
		{"no input", []string{"analyze"}, exitUsage, "usage"},
		{"two inputs", []string{"analyze", ok, ok}, exitUsage, "usage"},
		{"unknown flag", []string{"analyze", ok, "--frobnicate"}, exitUsage, "usage"},
		{"flag without value", []string{"analyze", ok, "--output"}, exitUsage, "usage"},
		{"bad error format", []string{"analyze", ok, "--error-format", "xml"}, exitUsage, "usage"},
		{"invalid configuration", []string{"analyze", ok, "--set", "scoring.threshold=1"}, exitUsage, "invalid_config"},
		{"existing output", []string{"analyze", ok, "--output", existing}, exitUsage, "usage"},
		{"invalid video track", []string{"analyze", ok, "--video-track", "1"}, exitUsage, "invalid_config"},
		{"strict optional stream", []string{"analyze", ok, "--audio-track", "5", "--set", "input.optional_stream_policy=strict"}, exitUsage, "invalid_config"},

		{"not a media file", []string{"analyze", fixture(t, "audio_only", "not_media.mp4")}, exitUnsupported, "unsupported_input"},
		{"no video", []string{"analyze", fixture(t, "audio_only", "audio_only.mp4")}, exitUnsupported, "unsupported_input"},
		{"HDR under the fail policy", []string{"analyze", fixture(t, "hdr", "hdr.mp4"), "--set", "input.unsupported_format_policy=fail"}, exitUnsupported, "unsupported_input"},
		{"unusable timing", []string{"analyze", fixture(t, "reschange", "reschange_reset.ts"), "--set", "input.timestamp_reset_policy=fail"}, exitUnsupported, "unusable_timing"},

		{"missing input", []string{"analyze", filepath.Join(dir, "nope.mp4")}, exitIO, "io"},
		{"input is a directory", []string{"analyze", dir}, exitIO, "io"},
		{"missing manifest", []string{"analyze", filepath.Join(dir, "nope.mpd")}, exitIO, "io"},
		{"output directory missing", []string{"analyze", ok, "--output", filepath.Join(dir, "no", "such", "dir", "r.json")}, exitIO, "io"},
	}
	if hls != "" {
		cases = append(cases,
			struct {
				name string
				args []string
				code int
				kind string
			}{"package resource limit", []string{"analyze", hls, "--set", "package.max_segments=10"}, exitResource, "resource_limit"},
			struct {
				name string
				args []string
				code int
				kind string
			}{"missing package resource", []string{"analyze", brokenPackage(t)}, exitIO, "io"},
		)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := cli(t, dir, tc.args...).want(t, tc.code)
			if r.stdout != "" {
				t.Errorf("stdout on failure: %.200s", r.stdout)
			}
			if !strings.Contains(r.stderr, "tv-ad-detect: ") {
				t.Errorf("stderr has no error message: %q", r.stderr)
			}
			if len(tc.args) == 0 || tc.args[0] != "analyze" || tc.name == "bad error format" {
				return // no flags are parsed there, or the flag is itself the error
			}
			// --error-format json: one machine-readable object, same code.
			// The flag comes first so it is seen before the failing argument.
			j := cli(t, dir, append([]string{"analyze", "--error-format=json"}, tc.args[1:]...)...)
			j.want(t, tc.code)
			var doc struct {
				Error struct {
					Kind     string `json:"kind"`
					Message  string `json:"message"`
					ExitCode int    `json:"exit_code"`
				} `json:"error"`
			}
			lines := strings.Split(strings.TrimSpace(j.stderr), "\n")
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &doc); err != nil {
				t.Fatalf("--error-format json: stderr %q: %v", j.stderr, err)
			}
			if doc.Error.Kind != tc.kind || doc.Error.ExitCode != tc.code || doc.Error.Message == "" {
				t.Errorf("error document = %+v, want kind %s, exit code %d", doc.Error, tc.kind, tc.code)
			}
			if j.stdout != "" {
				t.Errorf("stdout on failure: %.200s", j.stdout)
			}
		})
	}
	// 0 covers a completed analysis with zero ads (RESULT-02).
	r := cli(t, dir, "analyze", ok).want(t, exitOK)
	if res, _ := oneJSONDocument(t, r.stdout); len(res.AdBreaks) != 0 || !res.Complete {
		t.Errorf("breaks %+v complete %v", res.AdBreaks, res.Complete)
	}
	if string(mustRead(t, existing)) != "{}" {
		t.Errorf("the existing output file was modified")
	}
}

func mustRead(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// CLI-02 (unit): every error kind of the library maps to its documented
// exit code.
func TestExitCodeMapping(t *testing.T) {
	for kind, want := range map[addetect.ErrorKind]int{
		addetect.KindInvalidConfig: 2, addetect.KindUnsupported: 3, addetect.KindTiming: 3,
		addetect.KindIO: 4, addetect.KindMedia: 4, addetect.KindResourceLimit: 5,
		addetect.KindCanceled: 130, addetect.KindInternal: 1,
	} {
		code, name := exitCodeFor(addetect.Errorf(kind, "x"))
		if code != want || name != kind.String() {
			t.Errorf("kind %s -> exit %d (%s), want %d", kind, code, name, want)
		}
	}
	if code, _ := exitCodeFor(context.Canceled); code != 130 {
		t.Errorf("context.Canceled -> exit %d, want 130", code)
	}
	if code, _ := exitCodeFor(errors.New("boom")); code != 1 {
		t.Errorf("foreign error -> exit %d, want 1", code)
	}
	if exitOK != 0 || exitUsage != 2 || exitUnsupported != 3 || exitIO != 4 || exitResource != 5 || exitCanceled != 130 {
		t.Errorf("exit code constants differ from CLI-02")
	}
}

// AT-21 / CLI-03: named outputs are written atomically and never replace
// an existing file unless --overwrite is given; the refusal happens before
// any decoding.
func TestOutputFileProtectionAndAtomicWrite(t *testing.T) {
	t.Parallel()
	in := fixture(t, "matrix", "mp4_aac.mp4")
	dir := t.TempDir()
	out := filepath.Join(dir, "result.json")

	r := cli(t, dir, "analyze", in, "--output", out).want(t, exitOK)
	if r.stdout != "" {
		t.Errorf("stdout is not empty when writing to a file")
	}
	first := mustRead(t, out)
	oneJSONDocument(t, string(first))
	if got := dirEntries(t, dir); len(got) != 1 || got[0] != "result.json" {
		t.Errorf("output directory holds %v, want only result.json (no temporary files)", got)
	}

	// Refused without --overwrite; the file is untouched.
	r = cli(t, dir, "analyze", in, "-o", out).want(t, exitUsage)
	if !strings.Contains(r.stderr, "already exists") || !bytes.Equal(mustRead(t, out), first) {
		t.Errorf("stderr %q; file changed: %v", r.stderr, !bytes.Equal(mustRead(t, out), first))
	}
	// The refusal precedes decoding: it happens even for a missing input.
	cli(t, dir, "analyze", filepath.Join(dir, "missing.mp4"), "-o", out).want(t, exitUsage)
	// A symbolic link at the output path is an existing file too.
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(filepath.Join(dir, "dangling"), link); err == nil {
		cli(t, dir, "analyze", in, "-o", link).want(t, exitUsage)
		os.Remove(link)
	}

	// --overwrite replaces it (here with different content).
	cli(t, dir, "analyze", in, "-o", out, "--overwrite", "--pretty").want(t, exitOK)
	second := mustRead(t, out)
	oneJSONDocument(t, string(second))
	if bytes.Equal(first, second) || !bytes.Contains(second, []byte("\n  ")) {
		t.Errorf("--overwrite --pretty did not replace the file")
	}
	// The same switch from a configuration layer.
	layer := writeFile(t, filepath.Join(t.TempDir(), "out.yaml"), "outputs:\n  overwrite: true\n")
	cli(t, dir, "analyze", in, "-o", out, "--config", layer).want(t, exitOK)
	if got := dirEntries(t, dir); len(got) != 1 {
		t.Errorf("output directory holds %v after overwriting", got)
	}

	// Companion outputs are protected the same way.
	logo := writeFile(t, filepath.Join(dir, "logo.json"), "keep")
	frames := writeFile(t, filepath.Join(dir, "frames.csv"), "keep")
	cli(t, dir, "analyze", in, "--export-logo", logo).want(t, exitUsage)
	cli(t, dir, "analyze", in, "--frame-measurements", frames).want(t, exitUsage)
	if string(mustRead(t, logo)) != "keep" || string(mustRead(t, frames)) != "keep" {
		t.Errorf("an existing companion file was modified")
	}
}

// AT-21, AT-27 / CLI-03, API-04, QUAL-04: a failed analysis leaves no
// output and no temporary file. Only --write-partial writes a document,
// and that document is explicitly incomplete with an error object.
func TestFailureLeavesNoOutputUnlessWritePartial(t *testing.T) {
	long(t)
	t.Parallel()
	pkg := brokenPackage(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "result.json")

	r := cli(t, dir, "analyze", pkg, "--output", out).want(t, exitIO)
	if got := dirEntries(t, dir); len(got) != 0 {
		t.Errorf("a failed analysis left %v in the output directory", got)
	}
	if !strings.Contains(r.stderr, "seg_010.m4s") || r.stdout != "" {
		t.Errorf("stderr %q stdout %q", r.stderr, r.stdout)
	}
	// To stdout: nothing that could be parsed as a result.
	r = cli(t, dir, "analyze", pkg).want(t, exitIO)
	if r.stdout != "" {
		t.Errorf("a failed analysis wrote to stdout: %.200s", r.stdout)
	}

	r = cli(t, dir, "analyze", pkg, "--output", out, "--write-partial").want(t, exitIO)
	res, raw := oneJSONDocument(t, string(mustRead(t, out)))
	if res.Complete || res.Error == nil || res.Error.Kind != "io" || !strings.Contains(res.Error.Message, "seg_010.m4s") {
		t.Errorf("partial document: complete=%v error=%+v", res.Complete, res.Error)
	}
	if string(raw["complete"]) != "false" {
		t.Errorf(`"complete" = %s`, raw["complete"])
	}
	// It covers only what was analyzed before the failure (segment 10
	// starts at 50 s).
	if res.DurationUS <= 0 || res.DurationUS > 50_000_000 || len(res.AdBreaks) != 0 {
		t.Errorf("partial document spans %d us with %d breaks", res.DurationUS, len(res.AdBreaks))
	}
	if !strings.Contains(r.stderr, "partial") {
		t.Errorf("stderr does not announce the partial result: %q", r.stderr)
	}
	if got := dirEntries(t, dir); len(got) != 1 {
		t.Errorf("output directory holds %v", got)
	}
	// The partial document goes to stdout as one JSON value as well.
	r = cli(t, dir, "analyze", pkg, "--write-partial").want(t, exitIO)
	if res, _ := oneJSONDocument(t, r.stdout); res.Complete || res.Error == nil {
		t.Errorf("partial document on stdout: complete=%v error=%+v", res.Complete, res.Error)
	}
	// A failure before anything was analyzed has no partial result.
	cli(t, dir, "analyze", filepath.Join(dir, "missing.mp4"), "--output", filepath.Join(dir, "none.json"), "--write-partial").want(t, exitIO)
	if _, err := os.Stat(filepath.Join(dir, "none.json")); err == nil {
		t.Errorf("a document was written although nothing was analyzed")
	}

	// The degraded package policy is a different thing: exit 0, a full
	// document, marked incomplete, without an error object.
	r = cli(t, dir, "analyze", pkg, "--set", "package.failure_policy=degraded").want(t, exitOK)
	res, _ = oneJSONDocument(t, r.stdout)
	if res.Complete || res.Error != nil || res.DurationUS != 280_600_000 || len(res.Coverage.Gaps) != 1 {
		t.Errorf("degraded: complete=%v error=%+v duration=%d gaps=%+v", res.Complete, res.Error, res.DurationUS, res.Coverage.Gaps)
	}
	if !strings.Contains(r.stderr, "warning: package_resource_unreadable") {
		t.Errorf("degraded: stderr %q", r.stderr)
	}
}

// AT-21, AT-04 / CLI-01, LOGO-02, BUMPER-03, API-02: --export-logo writes
// the learned logo as an asset only on request, and that asset reproduces
// the detection when supplied; --frame-measurements writes one CSV row per
// frame.
func TestExportLogoAndFrameMeasurements(t *testing.T) {
	long(t)
	t.Parallel()
	in := fixture(t, "broadcast", "broadcast.mp4")
	dir := t.TempDir()
	out, logo, frames := filepath.Join(dir, "r.json"), filepath.Join(dir, "logo.json"), filepath.Join(dir, "frames.csv")

	cli(t, dir, "analyze", in, "-o", out, "--export-logo", logo, "--frame-measurements", frames).want(t, exitOK)
	if got := dirEntries(t, dir); len(got) != 3 {
		t.Errorf("output directory holds %v, want the three requested files", got)
	}
	learned, _ := oneJSONDocument(t, string(mustRead(t, out)))
	for _, f := range []string{out, logo, frames} {
		wantMode(t, f, 0o644)
	}

	asset, err := addetect.ParseLogoAsset(mustRead(t, logo))
	if err != nil {
		t.Fatalf("the exported logo is not a valid asset: %v", err)
	}
	if asset == nil {
		t.Fatal("no asset")
	}
	cli(t, dir, "config", "validate", "--set", "logo.asset="+logo).want(t, exitOK)
	withAsset := cli(t, dir, "analyze", in, "--set", "logo.asset="+logo).want(t, exitOK)
	res, _ := oneJSONDocument(t, withAsset.stdout)
	if len(res.Calibration.Logo) != 1 || res.Calibration.Logo[0].Origin != "asset" || res.Calibration.Logo[0].Status != addetect.StatusUsable {
		t.Errorf("logo calibration with the asset = %+v", res.Calibration.Logo)
	}
	if len(res.AdBreaks) != 1 || res.AdBreaks[0].StartUS != learned.AdBreaks[0].StartUS || res.AdBreaks[0].EndUS != learned.AdBreaks[0].EndUS {
		t.Errorf("breaks with the exported asset = %+v, learned = %+v", res.AdBreaks, learned.AdBreaks)
	}
	if res.Config.Effective["logo.asset"] != "logo.json" {
		t.Errorf("effective logo.asset = %q, want the redacted base name", res.Config.Effective["logo.asset"])
	}
	// A corrupted asset fails validation before any decoding.
	bad := writeFile(t, filepath.Join(dir, "bad-logo.json"), strings.Replace(string(mustRead(t, logo)), `"mask_sha256": "`, `"mask_sha256": "00`, 1))
	cli(t, dir, "config", "validate", "--set", "logo.asset="+bad).want(t, exitUsage)
	cli(t, dir, "analyze", in, "--set", "logo.asset="+bad).want(t, exitUsage)

	rows, err := csv.NewReader(bytes.NewReader(mustRead(t, frames))).ReadAll()
	if err != nil {
		t.Fatalf("frame measurements are not CSV: %v", err)
	}
	if want := "time_us,brightness,nonuniformity,bright_fraction,coded_width,coded_height"; strings.Join(rows[0], ",") != want {
		t.Errorf("CSV header = %v", rows[0])
	}
	if len(rows) != 1+2806 {
		t.Fatalf("%d CSV rows, want a header and 2806 frames", len(rows))
	}
	for i, row := range rows[1:] {
		ts, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil || ts != int64(i)*100_000 || row[4] != "320" || row[5] != "180" {
			t.Fatalf("CSV row %d = %v", i, row)
		}
		// Six black frames start each of the five separators.
		black := false
		for _, at := range []int{1000, 1150, 1450, 1600, 1900} {
			black = black || (i >= at && i < at+6)
		}
		if b, _ := strconv.ParseFloat(row[1], 64); black != (b < 1) {
			t.Fatalf("CSV row %d brightness %v, black frame expected: %v", i, row[1], black)
		}
	}

	// Nothing is exported without an explicit request (QUAL-04), and a
	// recording without a learnable logo exports nothing.
	quiet := t.TempDir()
	cli(t, quiet, "analyze", in, "-o", filepath.Join(quiet, "r.json")).want(t, exitOK)
	if got := dirEntries(t, quiet); len(got) != 1 {
		t.Errorf("unrequested outputs: %v", got)
	}
	r := cli(t, quiet, "analyze", fixture(t, "matrix", "mp4_aac.mp4"), "--export-logo", filepath.Join(quiet, "none.json")).want(t, exitOK)
	if _, err := os.Stat(filepath.Join(quiet, "none.json")); err == nil || !strings.Contains(r.stderr, "no validated logo") {
		t.Errorf("--export-logo without a logo: file exists=%v, stderr %q", err == nil, r.stderr)
	}
}

// AT-21, AT-20 / CLI-02, API-03: SIGINT in the middle of an analysis exits
// with code 130, promptly, with no output file, no temporary file and no
// JSON on stdout. The input is the broadcast transport stream repeated 16
// times (several seconds of work), and the signal is sent as soon as
// progress shows that decoding is under way.
func TestInterruptExitsWith130(t *testing.T) {
	long(t)
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt cannot be sent to a child process on Windows")
	}
	t.Parallel()
	media := mustRead(t, fixture(t, "broadcast_ts", "broadcast.ts"))
	input := filepath.Join(t.TempDir(), "long.ts")
	f, err := os.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		if _, err := f.Write(media); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	interrupt := func(t *testing.T, extra ...string) (cliResult, string) {
		outDir := t.TempDir()
		cmd := exec.Command(cliBinary(t), append([]string{"analyze", input, "--progress", "--output", filepath.Join(outDir, "result.json")}, extra...)...)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		var stderr strings.Builder
		var signaledAt time.Time
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			sc := bufio.NewScanner(stderrPipe)
			for sc.Scan() {
				line := sc.Text()
				stderr.WriteString(line + "\n")
				// Decoding is under way once media time has advanced.
				if signaledAt.IsZero() && strings.HasPrefix(line, "progress: ") && !strings.Contains(line, "(0 s of media)") {
					signaledAt = time.Now()
					cmd.Process.Signal(os.Interrupt)
				}
			}
		}()
		waitErr := make(chan error, 1)
		go func() { <-readDone; waitErr <- cmd.Wait() }()
		var res cliResult
		select {
		case err := <-waitErr:
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				res.code = ee.ExitCode()
			} else if err != nil {
				t.Fatalf("wait: %v", err)
			}
		case <-time.After(60 * time.Second):
			cmd.Process.Kill()
			<-waitErr
			t.Fatalf("the command did not exit\nstderr: %s", stderr.String())
		}
		res.stdout, res.stderr = stdout.String(), stderr.String()
		if signaledAt.IsZero() {
			t.Fatalf("no progress was reported before the command exited with code %d\nstderr: %s", res.code, res.stderr)
		}
		if d := time.Since(signaledAt); d > 3*time.Second {
			t.Errorf("exit took %v after SIGINT", d)
		}
		return res, outDir
	}

	t.Run("no output", func(t *testing.T) {
		res, outDir := interrupt(t)
		res.want(t, exitCanceled)
		if res.stdout != "" {
			t.Errorf("stdout after SIGINT: %.200s", res.stdout)
		}
		if got := dirEntries(t, outDir); len(got) != 0 {
			t.Errorf("SIGINT left %v in the output directory", got)
		}
		if !strings.Contains(res.stderr, "tv-ad-detect: canceled") {
			t.Errorf("stderr does not report the cancellation: %q", res.stderr)
		}
	})
	t.Run("write-partial", func(t *testing.T) {
		res, outDir := interrupt(t, "--write-partial")
		res.want(t, exitCanceled)
		if got := dirEntries(t, outDir); len(got) != 1 || got[0] != "result.json" {
			t.Fatalf("output directory holds %v, want the partial result only", got)
		}
		doc, _ := oneJSONDocument(t, string(mustRead(t, filepath.Join(outDir, "result.json"))))
		if doc.Complete || doc.Error == nil || doc.Error.Kind != "canceled" {
			t.Errorf("partial document: complete=%v error=%+v", doc.Complete, doc.Error)
		}
	})
}

// wantMode requires path to be a regular file with the given permissions.
func wantMode(t testing.TB, path string, mode os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != mode {
		t.Errorf("%s has mode %v, want a regular file with %v", filepath.Base(path), fi.Mode(), mode)
	}
}

// AT-21 / CLI-03: --frame-measurements is a named output like the result:
// it appears only after a successful analysis (written to a temporary file
// and moved into place), with mode 0644 like the result; a failed analysis
// leaves nothing at its path and no temporary file; an existing file is
// refused up front without --overwrite.
func TestFrameMeasurementsAreWrittenAtomically(t *testing.T) {
	t.Parallel()
	ok := fixture(t, "matrix", "mp4_aac.mp4")
	// This input fails half-way (after 60 of 120 frames) under the strict
	// timestamp policy: a failure in the middle of the frame stream.
	failing := fixture(t, "reschange", "reschange_reset.ts")
	failArgs := []string{"--set", "input.timestamp_reset_policy=fail"}

	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		out, frames := filepath.Join(dir, "r.json"), filepath.Join(dir, "frames.csv")
		cli(t, dir, "analyze", ok, "-o", out, "--frame-measurements", frames).want(t, exitOK)
		if got := dirEntries(t, dir); len(got) != 2 {
			t.Errorf("output directory holds %v, want the two requested files and no temporary file", got)
		}
		wantMode(t, out, 0o644)
		wantMode(t, frames, 0o644)
		rows, err := csv.NewReader(bytes.NewReader(mustRead(t, frames))).ReadAll()
		if err != nil || len(rows) != 61 || rows[0][0] != "time_us" || rows[60][0] != "3933333" {
			t.Errorf("CSV: %d rows, err %v, last %v; want a header and 60 frames", len(rows), err, rows[len(rows)-1])
		}
	})
	t.Run("failed analysis leaves nothing", func(t *testing.T) {
		dir := t.TempDir()
		args := append([]string{"analyze", failing, "-o", filepath.Join(dir, "r.json"), "--frame-measurements", filepath.Join(dir, "frames.csv")}, failArgs...)
		r := cli(t, dir, args...).want(t, exitUnsupported)
		if got := dirEntries(t, dir); len(got) != 0 {
			t.Errorf("a failed analysis left %v", got)
		}
		if !strings.Contains(r.stderr, "unusable_timing") {
			t.Errorf("stderr = %q", r.stderr)
		}
		// With --write-partial the incomplete result is written, but frame
		// measurements of a failed analysis are still not published.
		r = cli(t, dir, append(args, "--write-partial")...).want(t, exitUnsupported)
		if got := dirEntries(t, dir); len(got) != 1 || got[0] != "r.json" {
			t.Errorf("--write-partial: output directory holds %v, want only the partial result", got)
		}
		if res, _ := oneJSONDocument(t, string(mustRead(t, filepath.Join(dir, "r.json")))); res.Complete || res.Error == nil || res.Error.Kind != "unusable_timing" {
			t.Errorf("partial document: complete=%v error=%+v", res.Complete, res.Error)
		}
		// The CSV to a path while the result goes to stdout.
		dir2 := t.TempDir()
		cli(t, dir2, append([]string{"analyze", failing, "--frame-measurements", filepath.Join(dir2, "frames.csv")}, failArgs...)...).want(t, exitUnsupported)
		if got := dirEntries(t, dir2); len(got) != 0 {
			t.Errorf("a failed analysis left %v", got)
		}
	})
	t.Run("existing file", func(t *testing.T) {
		dir := t.TempDir()
		frames := writeFile(t, filepath.Join(dir, "frames.csv"), "keep")
		// Refused up front: before the input is even opened.
		r := cli(t, dir, "analyze", filepath.Join(dir, "missing.mp4"), "--frame-measurements", frames, "--progress").want(t, exitUsage)
		if !strings.Contains(r.stderr, "already exists") || strings.Contains(r.stderr, "progress: ") {
			t.Errorf("stderr = %q", r.stderr)
		}
		cli(t, dir, "analyze", ok, "--frame-measurements", frames).want(t, exitUsage)
		if string(mustRead(t, frames)) != "keep" {
			t.Errorf("the existing file was modified")
		}
		if got := dirEntries(t, dir); len(got) != 1 {
			t.Errorf("output directory holds %v", got)
		}
		// A failed analysis with --overwrite keeps the old file too.
		cli(t, dir, append([]string{"analyze", failing, "--frame-measurements", frames, "--overwrite"}, failArgs...)...).want(t, exitUnsupported)
		if string(mustRead(t, frames)) != "keep" || len(dirEntries(t, dir)) != 1 {
			t.Errorf("a failed analysis replaced the existing file or left a temporary file: %v", dirEntries(t, dir))
		}
		cli(t, dir, "analyze", ok, "--frame-measurements", frames, "--overwrite").want(t, exitOK)
		if !strings.HasPrefix(string(mustRead(t, frames)), "time_us,") {
			t.Errorf("--overwrite did not replace the file")
		}
		wantMode(t, frames, 0o644)
		if got := dirEntries(t, dir); len(got) != 1 {
			t.Errorf("output directory holds %v", got)
		}
	})
}

// AT-21 / CLI-03: a missing output directory - for the result, the logo or
// the frame measurements - is an I/O error (exit 4) reported before any
// decoding, and no other output is created.
func TestMissingOutputDirectoryFailsBeforeDecoding(t *testing.T) {
	t.Parallel()
	in := fixture(t, "matrix", "mp4_aac.mp4")
	for _, flag := range []string{"--output", "--export-logo", "--frame-measurements"} {
		t.Run(flag, func(t *testing.T) {
			dir := t.TempDir()
			missing := filepath.Join(dir, "no", "such", "dir", "file.out")
			good := filepath.Join(dir, "good.json")
			args := []string{"analyze", in, "--progress", flag, missing}
			if flag != "--output" {
				args = append(args, "--output", good)
			}
			start := time.Now()
			r := cli(t, dir, args...).want(t, exitIO)
			if strings.Contains(r.stderr, "progress: ") {
				t.Errorf("decoding started before the missing directory was reported:\n%s", r.stderr)
			}
			if !strings.Contains(r.stderr, "does not exist") || !strings.Contains(r.stderr, filepath.Dir(missing)) {
				t.Errorf("stderr = %q, want the missing directory named", r.stderr)
			}
			if r.stdout != "" {
				t.Errorf("stdout: %.200s", r.stdout)
			}
			if got := dirEntries(t, dir); len(got) != 0 {
				t.Errorf("outputs were created although the run was refused: %v", got)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("the refusal took %v", d)
			}
			// Machine-readable form.
			j := cli(t, dir, append(args, "--error-format", "json")...).want(t, exitIO)
			if !strings.Contains(j.stderr, `"kind":"io"`) || !strings.Contains(j.stderr, `"exit_code":4`) {
				t.Errorf("--error-format json: stderr = %q", j.stderr)
			}
			// The path's parent being a file is the same error.
			notDir := writeFile(t, filepath.Join(dir, "plain"), "x")
			cli(t, dir, "analyze", in, flag, filepath.Join(notDir, "file.out")).want(t, exitIO)
		})
	}
	// Even a missing input does not come first: outputs are checked before
	// anything is opened.
	r := cli(t, "", "analyze", filepath.Join(t.TempDir(), "missing.mp4"), "-o", filepath.Join(t.TempDir(), "no", "dir", "r.json"))
	r.want(t, exitIO)
	if !strings.Contains(r.stderr, "output directory") {
		t.Errorf("stderr = %q", r.stderr)
	}
}

// AT-21 / CLI-01, CLI-02: --error-format json applies wherever it appears
// on the command line - also after the argument that fails - in both the
// "--flag value" and "--flag=value" forms, for every command.
func TestErrorFormatJSONAppliesAnywhere(t *testing.T) {
	t.Parallel()
	in := fixture(t, "matrix", "mp4_aac.mp4")
	cases := []struct {
		name string
		args []string
		code int
		kind string
	}{
		{"after an unknown flag", []string{"analyze", "--bogus-flag", "--error-format", "json"}, exitUsage, "usage"},
		{"after an unknown flag, = form", []string{"analyze", "--bogus-flag", "--error-format=json"}, exitUsage, "usage"},
		{"before an unknown flag", []string{"analyze", "--error-format", "json", "--bogus-flag"}, exitUsage, "usage"},
		{"after the input and an unknown flag", []string{"analyze", in, "--bogus-flag", "--error-format=json"}, exitUsage, "usage"},
		{"after a bad override", []string{"analyze", in, "--set", "scoring.threshold=0", "--error-format", "json"}, exitUsage, "invalid_config"},
		{"after a malformed boolean", []string{"analyze", in, "--pretty=maybe", "--error-format=json"}, exitUsage, "usage"},
		{"missing input", []string{"analyze", filepath.Join(t.TempDir(), "missing.mp4"), "--error-format", "json"}, exitIO, "io"},
		{"no input", []string{"analyze", "--error-format=json"}, exitUsage, "usage"},
		{"config validate", []string{"config", "validate", "--set", "nope.key=1", "--error-format", "json"}, exitUsage, "invalid_config"},
		{"config without a subcommand", []string{"config", "--error-format=json"}, exitUsage, "usage"},
		{"unknown command", []string{"frobnicate", "--error-format", "json"}, exitUsage, "usage"},
		{"unknown command, = form", []string{"frobnicate", "--error-format=json"}, exitUsage, "usage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := cli(t, t.TempDir(), tc.args...).want(t, tc.code)
			if r.stdout != "" {
				t.Errorf("stdout: %.200s", r.stdout)
			}
			lines := strings.Split(strings.TrimSpace(r.stderr), "\n")
			var doc struct {
				Error struct {
					Kind     string `json:"kind"`
					Message  string `json:"message"`
					ExitCode int    `json:"exit_code"`
				} `json:"error"`
			}
			dec := json.NewDecoder(strings.NewReader(lines[len(lines)-1]))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&doc); err != nil {
				t.Fatalf("the error is not reported as JSON: %q (%v)", r.stderr, err)
			}
			if doc.Error.Kind != tc.kind || doc.Error.ExitCode != tc.code || doc.Error.Message == "" {
				t.Errorf("error document = %+v, want kind %s, exit code %d", doc.Error, tc.kind, tc.code)
			}
			if strings.Contains(r.stderr, "tv-ad-detect: ") {
				t.Errorf("a text error was printed as well: %q", r.stderr)
			}
			// The text form stays the default (and can be asked for).
			var textArgs []string
			for i := 0; i < len(tc.args); i++ {
				if tc.args[i] == "--error-format" {
					i++
					continue
				}
				if tc.args[i] != "--error-format=json" {
					textArgs = append(textArgs, tc.args[i])
				}
			}
			x := cli(t, t.TempDir(), textArgs...).want(t, tc.code)
			if !strings.Contains(x.stderr, "tv-ad-detect: ") || strings.Contains(x.stderr, `{"error"`) {
				t.Errorf("default error format: stderr = %q", x.stderr)
			}
		})
	}
	// A successful run is unaffected by the flag.
	r := cli(t, t.TempDir(), "analyze", in, "--error-format", "json", "--quiet").want(t, exitOK)
	if r.stderr != "" {
		t.Errorf("stderr = %q", r.stderr)
	}
	oneJSONDocument(t, r.stdout)
}

// AT-21 / CLI-03: a device file as output (/dev/null) is written in place:
// it needs no --overwrite although it exists, and it is not replaced by a
// regular file.
func TestOutputToDeviceFile(t *testing.T) {
	t.Parallel()
	fi, err := os.Stat(os.DevNull)
	if err != nil || fi.Mode()&os.ModeDevice == 0 {
		t.Skipf("%s is not a device file here", os.DevNull)
	}
	in := fixture(t, "matrix", "mp4_aac.mp4")
	dir := t.TempDir()
	for _, args := range [][]string{
		{"analyze", in, "--output", os.DevNull},
		{"analyze", in, "--output", os.DevNull, "--frame-measurements", filepath.Join(dir, "f.csv"), "--overwrite"},
		{"analyze", in, "-o", os.DevNull, "--pretty"},
	} {
		r := cli(t, dir, args...).want(t, exitOK)
		if r.stdout != "" {
			t.Errorf("stdout: %.200s", r.stdout)
		}
		stderrIsLogOnly(t, r.stderr)
	}
	after, err := os.Stat(os.DevNull)
	if err != nil || after.Mode()&os.ModeDevice == 0 || after.Mode() != fi.Mode() {
		t.Fatalf("%s was replaced or changed: %v (was %v), err %v", os.DevNull, after.Mode(), fi.Mode(), err)
	}
	// No temporary file was created next to the device.
	ents, _ := filepath.Glob(filepath.Join(filepath.Dir(os.DevNull), ".null.tmp-*"))
	if len(ents) != 0 {
		t.Errorf("temporary files next to %s: %v", os.DevNull, ents)
	}
	// A failed analysis has nothing to write there, and still fails.
	cli(t, dir, "analyze", filepath.Join(dir, "missing.mp4"), "--output", os.DevNull).want(t, exitIO)
}

// AT-21 / CLI-03: the result is created with mode 0644 (not the 0600 of a
// temporary file), also when it replaces an existing file.
func TestResultFileMode(t *testing.T) {
	t.Parallel()
	in := fixture(t, "matrix", "mp4_aac.mp4")
	dir := t.TempDir()
	out := filepath.Join(dir, "r.json")
	cli(t, dir, "analyze", in, "-o", out).want(t, exitOK)
	wantMode(t, out, 0o644)
	if err := os.Chmod(out, 0o600); err != nil {
		t.Fatal(err)
	}
	cli(t, dir, "analyze", in, "-o", out, "--overwrite").want(t, exitOK)
	wantMode(t, out, 0o644)
}

// AT-21, AT-20 / QUAL-04, CLI-02: a recording cut off before the duration
// its container declares analyzes successfully (exit 0) but the document
// is marked incomplete and the truncation is a warning on stderr.
func TestCLITruncatedInputSucceedsButIsIncomplete(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"declared.mp4", "declared.mkv"} {
		whole := fixture(t, "declared", file)
		data := mustRead(t, whole)
		cut := filepath.Join(t.TempDir(), "cut-"+file)
		if err := os.WriteFile(cut, data[:len(data)/2], 0o644); err != nil {
			t.Fatal(err)
		}
		r := cli(t, "", "analyze", cut).want(t, exitOK)
		res, raw := oneJSONDocument(t, r.stdout)
		if res.Complete || string(raw["complete"]) != "false" || res.Error != nil {
			t.Errorf("%s cut in half: complete=%v error=%+v", file, res.Complete, res.Error)
		}
		if !strings.Contains(r.stderr, "warning: truncated_input: ") {
			t.Errorf("%s cut in half: stderr = %q", file, r.stderr)
		}
		stderrIsLogOnly(t, r.stderr)
		if res.DurationUS < 5_000_000 || res.DurationUS > 7_000_000 {
			t.Errorf("%s cut in half: duration %d us, want about 6 s", file, res.DurationUS)
		}
		r = cli(t, "", "analyze", whole).want(t, exitOK)
		if res, _ := oneJSONDocument(t, r.stdout); !res.Complete || strings.Contains(r.stderr, "truncated_input") {
			t.Errorf("%s intact: complete=%v stderr %q", file, res.Complete, r.stderr)
		}
	}
}

// AT-28 / OUT-03: in the CLI's document the package's resource lists are
// JSON arrays, never null - also without any initialization resource.
func TestCLIPackageListsAreArrays(t *testing.T) {
	long(t)
	t.Parallel()
	for _, in := range []string{fixture(t, "hls_ts", "media.m3u8"), fixture(t, "hls_master", "master.m3u8")} {
		r := cli(t, "", "analyze", in).want(t, exitOK)
		_, raw := oneJSONDocument(t, r.stdout)
		var pkg map[string]json.RawMessage
		if err := json.Unmarshal(raw["package"], &pkg); err != nil {
			t.Fatalf("package: %v", err)
		}
		for _, key := range []string{"initialization_resources", "resources", "manifests", "epochs"} {
			if v := strings.TrimSpace(string(pkg[key])); !strings.HasPrefix(v, "[") {
				t.Errorf("%s: package.%s = %.40s, want an array", filepath.Base(in), key, v)
			}
		}
	}
}

// R1 / CLI-03: an output that is the same file as an input is refused, with
// or without --overwrite, however the path is spelled, and the input is left
// untouched. Outputs may not collide with each other either.
func TestOutputNeverReplacesAnInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	in := filepath.Join(dir, "rec.mp4")
	orig := mustRead(t, fixture(t, "matrix", "mp4_aac.mp4"))
	writeFile(t, in, string(orig))
	intact := func(what string) {
		t.Helper()
		if !bytes.Equal(mustRead(t, in), orig) {
			t.Fatalf("%s: the recording was modified", what)
		}
	}
	refused := func(what string, args ...string) {
		t.Helper()
		r := cli(t, dir, args...).want(t, exitUsage)
		if !strings.Contains(r.stderr, "refusing to replace") && !strings.Contains(r.stderr, "same file") {
			t.Errorf("%s: stderr %q", what, r.stderr)
		}
		intact(what)
	}

	refused("same path", "analyze", in, "-o", in, "--overwrite")
	refused("same path without overwrite", "analyze", in, "-o", in)
	refused("relative alias", "analyze", in, "-o", filepath.Join(".", "sub", "..", "rec.mp4"), "--overwrite")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	refused("relative alias through a directory", "analyze", in, "-o", filepath.Join("sub", "..", "rec.mp4"), "--overwrite")
	link := filepath.Join(dir, "alias.json")
	if err := os.Symlink(in, link); err == nil {
		refused("symbolic link alias", "analyze", in, "-o", link, "--overwrite")
	}
	hard := filepath.Join(dir, "hard.json")
	if err := os.Link(in, hard); err == nil {
		refused("hard link alias", "analyze", in, "-o", hard, "--overwrite")
	}
	refused("logo export onto the input", "analyze", in, "--export-logo", in, "--overwrite")
	refused("frame measurements onto the input", "analyze", in, "--frame-measurements", in, "--overwrite")
	refused("two outputs, one file", "analyze", in, "-o", filepath.Join(dir, "x.json"), "--export-logo", filepath.Join(dir, "sub", "..", "x.json"))

	// A configuration layer is an input too.
	layer := writeFile(t, filepath.Join(dir, "layer.yaml"), "outputs:\n  overwrite: true\n")
	refused("configuration file as output", "analyze", in, "--config", layer, "-o", layer)
	if got := string(mustRead(t, layer)); !strings.HasPrefix(got, "outputs:") {
		t.Errorf("the configuration file was modified: %q", got)
	}
	for _, name := range dirEntries(t, dir) {
		if strings.Contains(name, ".tmp-") {
			t.Errorf("temporary file left behind: %s", name)
		}
	}
}

// R1: the manifest and every media resource of a package are inputs. They
// are only known after the package has been read, and are still protected.
func TestOutputNeverReplacesAPackageResource(t *testing.T) {
	long(t)
	t.Parallel()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Dir(fixture(t, "hls_master", "master.m3u8")))); err != nil {
		t.Fatal(err)
	}
	master := filepath.Join(dir, "master.m3u8")
	var segment string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && segment == "" && (strings.HasSuffix(path, ".m4s") || strings.HasSuffix(path, ".mp4")) {
			segment = path
		}
		return nil
	})
	if segment == "" {
		t.Fatal("the fixture has no media segment")
	}
	for _, target := range []string{master, segment} {
		before := mustRead(t, target)
		r := cli(t, dir, "analyze", master, "-o", target, "--overwrite").want(t, exitUsage)
		if !strings.Contains(r.stderr, "refusing to replace") {
			t.Errorf("%s: stderr %q", target, r.stderr)
		}
		if !bytes.Equal(mustRead(t, target), before) {
			t.Fatalf("%s was modified", target)
		}
	}
	// An unrelated output in the same directory is fine.
	cli(t, dir, "analyze", master, "-o", filepath.Join(dir, "result.json")).want(t, exitOK)
}

// PIPE-03: --save-features writes a feature file; analyzing that file gives
// the same result as the recording, under other scoring settings too; a
// feature-affecting change is refused by name; and options that need the
// recording are refused for a feature file.
func TestSaveAndReplayFeatures(t *testing.T) {
	long(t)
	t.Parallel()
	in := fixture(t, "matrix", "mp4_aac.mp4")
	dir := t.TempDir()
	feats := filepath.Join(dir, "rec.tvadf")
	direct := cli(t, dir, "analyze", in, "--diagnostics", "full", "--save-features", feats).want(t, exitOK)
	if head := mustRead(t, feats); !bytes.HasPrefix(head, []byte("TVADFEAT")) {
		t.Fatalf("the feature file has no header")
	}
	replay := cli(t, dir, "analyze", feats, "--diagnostics", "full").want(t, exitOK)
	if replay.stdout != direct.stdout {
		t.Errorf("replayed result differs from the direct one")
	}
	set := []string{"--set", "scoring.threshold=3", "--set", "breaks.min_break=10s"}
	d2 := cli(t, dir, append([]string{"analyze", in}, set...)...).want(t, exitOK)
	r2 := cli(t, dir, append([]string{"analyze", feats}, set...)...).want(t, exitOK)
	if d2.stdout != r2.stdout {
		t.Errorf("replay under other scoring settings differs from the direct analysis")
	}
	r := cli(t, dir, "analyze", feats, "--set", "scene.cut_threshold=0.6").want(t, exitUsage)
	if !strings.Contains(r.stderr, "scene.cut_threshold") {
		t.Errorf("stderr %q does not name the differing key", r.stderr)
	}
	cli(t, dir, "analyze", feats, "--frame-measurements", filepath.Join(dir, "f.csv")).want(t, exitUsage)
	cli(t, dir, "analyze", feats, "--save-features", filepath.Join(dir, "again.tvadf")).want(t, exitUsage)
	// The feature file is protected like any output.
	cli(t, dir, "analyze", in, "--save-features", feats).want(t, exitUsage)
	cli(t, dir, "analyze", feats, "-o", feats, "--overwrite").want(t, exitUsage)
	// A damaged feature file is an I/O error, never a misread.
	data := mustRead(t, feats)
	data[len(data)/2] ^= 1
	bad := writeFile(t, filepath.Join(dir, "bad.tvadf"), string(data))
	cli(t, dir, "analyze", bad).want(t, exitIO)
}

// Cut lists are written next to the result, atomically, protected like any
// output, and never leak onto stdout; bad formats fail before decoding.
func TestCutlistOutputs(t *testing.T) {
	t.Parallel()
	in := fixture(t, "matrix", "mp4_aac.mp4")
	dir := t.TempDir()
	edl, ff, ogm := filepath.Join(dir, "a.edl"), filepath.Join(dir, "a.ffmeta"), filepath.Join(dir, "a.chapters")
	r := cli(t, dir, "analyze", in, "--cutlist", "edl:"+edl, "--cutlist", "ffmetadata:"+ff, "--cutlist", "ogm:"+ogm).want(t, exitOK)
	res, _ := oneJSONDocument(t, r.stdout)
	if got := string(mustRead(t, ff)); !strings.HasPrefix(got, ";FFMETADATA1\n") || strings.Count(got, "[CHAPTER]") != len(res.Segments) {
		t.Errorf("ffmetadata = %q for %d segments", got, len(res.Segments))
	}
	if got := string(mustRead(t, ogm)); !strings.HasPrefix(got, "CHAPTER01=00:00:00.000\n") {
		t.Errorf("ogm = %q", got)
	}
	if got := string(mustRead(t, edl)); strings.Count(got, "\n") != len(res.SkipIntervals) {
		t.Errorf("edl has %d lines for %d skip intervals", strings.Count(got, "\n"), len(res.SkipIntervals))
	}
	for _, name := range dirEntries(t, dir) {
		if strings.Contains(name, ".tmp-") {
			t.Errorf("temporary file left behind: %s", name)
		}
	}
	// Existing cut lists are refused without --overwrite, untouched.
	before := mustRead(t, ff)
	cli(t, dir, "analyze", in, "--cutlist", "ffmetadata:"+ff).want(t, exitUsage)
	if !bytes.Equal(mustRead(t, ff), before) {
		t.Errorf("an existing cut list was modified")
	}
	cli(t, dir, "analyze", in, "--cutlist", "ffmetadata:"+ff, "--overwrite").want(t, exitOK)
	// Bad requests fail before decoding (even for a missing input).
	missing := filepath.Join(dir, "missing.mp4")
	for _, args := range [][]string{
		{"--cutlist", "srt:" + filepath.Join(dir, "x.srt")},
		{"--cutlist", "edl"},
		{"--cutlist", "edl:" + filepath.Join(dir, "y.edl"), "--cutlist-clock", "wall"},
		{"--cutlist", "edl:" + in},
	} {
		cli(t, dir, append([]string{"analyze", missing}, args...)...).want(t, exitUsage)
	}
}

// logo.cache_dir / cache_key: the first run learns the logo and saves it;
// the next run uses the saved entry as its asset; an unusable entry is
// reported and learning takes over again.
func TestLogoCache(t *testing.T) {
	long(t)
	t.Parallel()
	in := fixture(t, "broadcast", "broadcast.mp4")
	dir := t.TempDir()
	cache := filepath.Join(dir, "logos")
	args := []string{"analyze", in, "--set", "logo.cache_dir=" + cache, "--set", "logo.cache_key=kdvr-4.1"}
	r := cli(t, dir, args...).want(t, exitOK)
	first, _ := oneJSONDocument(t, r.stdout)
	if c := first.Calibration.Logo[0]; c.Origin != "learned" || c.Status != "usable" {
		t.Fatalf("first run calibration %+v", c)
	}
	entry := filepath.Join(cache, "kdvr-4.1.tvad-logo.json")
	if _, err := os.Stat(entry); err != nil {
		t.Fatalf("no cache entry: %v", err)
	}
	r = cli(t, dir, args...).want(t, exitOK)
	second, _ := oneJSONDocument(t, r.stdout)
	if c := second.Calibration.Logo[0]; c.Origin != "asset" || c.Status != "usable" {
		t.Errorf("second run calibration %+v, want the cached asset", c)
	}
	if a, b := mustJSONString(t, second.Segments), mustJSONString(t, first.Segments); a != b {
		t.Errorf("cached logo changed the segments:\n%s\n%s", a, b)
	}
	writeFile(t, entry, "{not json")
	r = cli(t, dir, args...).want(t, exitOK)
	third, _ := oneJSONDocument(t, r.stdout)
	if c := third.Calibration.Logo[0]; c.Origin != "learned" || !strings.Contains(r.stderr, "logo_cache_unusable") {
		t.Errorf("unusable entry: origin %s stderr %q", c.Origin, r.stderr)
	}
	if _, err := addetect.ParseLogoAsset(mustRead(t, entry)); err != nil {
		t.Errorf("the unusable entry was not refreshed: %v", err)
	}
	// Half a pair is a configuration error.
	cli(t, dir, "analyze", in, "--set", "logo.cache_dir="+cache).want(t, exitUsage)
	cli(t, dir, "analyze", in, "--set", "logo.cache_dir="+cache, "--set", "logo.cache_key=../x").want(t, exitUsage)
}

func mustJSONString(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// "bumper capture" writes the frame at --at (cropped to --region) as a PNG
// template and prints a configuration snippet that validates; bad requests
// fail with usage errors and nothing is written.
func TestBumperCapture(t *testing.T) {
	t.Parallel()
	in := fixture(t, "matrix", "mp4_aac.mp4")
	dir := t.TempDir()
	card := filepath.Join(dir, "card.png")
	r := cli(t, dir, "bumper", "capture", in, "--at", "2s", "--region", "0.25,0.25,0.5,0.5", "-o", card).want(t, exitOK)
	img, err := png.Decode(bytes.NewReader(mustRead(t, card)))
	if err != nil {
		t.Fatalf("not a PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 160 || b.Dy() != 90 {
		t.Errorf("template is %dx%d, want 160x90 (half of 320x180)", b.Dx(), b.Dy())
	}
	if !strings.Contains(r.stdout, "frame at 2.000 s") || !strings.Contains(r.stdout, "region: [0.25,0.25,0.5,0.5]") {
		t.Errorf("snippet = %q", r.stdout)
	}
	layer := writeFile(t, filepath.Join(dir, "bumpers.yaml"), r.stdout[strings.Index(r.stdout, "bumpers:"):])
	cli(t, dir, "config", "validate", "--config", layer).want(t, exitOK)

	for _, args := range [][]string{
		{"bumper", "capture", in, "-o", filepath.Join(dir, "a.png")},                                          // no --at
		{"bumper", "capture", in, "--at", "2", "-o", filepath.Join(dir, "b.png")},                             // no unit
		{"bumper", "capture", in, "--at", "2s", "--region", "0.8,0,0.5,1", "-o", filepath.Join(dir, "c.png")}, // outside the frame
		{"bumper", "capture", in, "--at", "2s", "-o", card},                                                   // exists
		{"bumper", "capture", in, "--at", "2s", "-o", in, "--overwrite"},                                      // the input
		{"bumper", "grab", in},
	} {
		cli(t, dir, args...).want(t, exitUsage)
	}
	cli(t, dir, "bumper", "capture", in, "--at", "1h", "-o", filepath.Join(dir, "late.png")).want(t, exitUsage)
	for _, n := range []string{"a.png", "b.png", "c.png", "late.png"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			t.Errorf("%s was written for a failed request", n)
		}
	}
}
