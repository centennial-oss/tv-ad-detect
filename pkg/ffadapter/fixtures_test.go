// SPDX-License-Identifier: Apache-2.0

package ffadapter_test

// Shared infrastructure of the media-adapter integration tests.
//
// Fixtures are never committed: scripts/make-fixtures.sh synthesizes each
// one on first use from FFmpeg's lavfi signal generators with the ffmpeg
// command-line tool (test tooling only; the product never runs it). They
// land in a temporary directory created once per test binary, or in
// $TVAD_FIXTURE_DIR when that is set (then they are kept and reused).
// Tests that need a fixture skip when ffmpeg/ffprobe are not on PATH.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/ffadapter"
)

var fx struct {
	dir    string
	script string
	tools  bool

	mu   sync.Mutex
	once map[string]*sync.Once
	errs map[string]error
}

// fixtureDeps mirrors the "need" lines of the script so that concurrent
// tests never build one prerequisite twice.
var fixtureDeps = map[string][]string{
	"broadcast_low": {"broadcast"},
	"broadcast_ts":  {"broadcast"},
	"hls_master":    {"broadcast", "broadcast_low"},
	"hls_byterange": {"broadcast"},
	"hls_ts":        {"broadcast"},
	"dash":          {"broadcast", "broadcast_low"},
	"pieces":        {"broadcast"},
}

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(func() int {
		fx.once, fx.errs = map[string]*sync.Once{}, map[string]error{}
		fx.tools = true
		for _, tool := range []string{"ffmpeg", "ffprobe", "bash"} {
			if _, err := exec.LookPath(tool); err != nil {
				fx.tools = false
			}
		}
		script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "make-fixtures.sh"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fx.script = script
		if dir := os.Getenv("TVAD_FIXTURE_DIR"); dir != "" {
			if fx.dir, err = filepath.Abs(dir); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		} else {
			dir, err := os.MkdirTemp("", "tvad-fixtures-")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			defer os.RemoveAll(dir)
			// Resolve symlinks (macOS /var -> /private/var) so paths compare equal.
			if fx.dir, err = filepath.EvalSymlinks(dir); err != nil {
				fx.dir = dir
			}
		}
		return m.Run()
	}())
}

// fixture synthesizes the named fixture on first use and returns the path
// of a file inside it.
func fixture(t testing.TB, name string, rel ...string) string {
	t.Helper()
	if !fx.tools {
		t.Skip("ffmpeg, ffprobe or bash is not on PATH: media fixtures cannot be synthesized")
	}
	for _, dep := range fixtureDeps[name] {
		fixture(t, dep)
	}
	fx.mu.Lock()
	once := fx.once[name]
	if once == nil {
		once = new(sync.Once)
		fx.once[name] = once
	}
	fx.mu.Unlock()
	once.Do(func() {
		out, err := exec.Command("bash", fx.script, "-o", fx.dir, name).CombinedOutput()
		if err != nil {
			err = fmt.Errorf("make-fixtures.sh %s: %v\n%s", name, err, out)
		}
		fx.mu.Lock()
		fx.errs[name] = err
		fx.mu.Unlock()
	})
	fx.mu.Lock()
	err := fx.errs[name]
	fx.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(append([]string{fx.dir, name}, rel...)...)
}

// long marks a test that analyzes the several-minute synthetic broadcast.
func long(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("long test (analyzes the synthetic broadcast); skipped with -short")
	}
}

// ---- analysis helpers ------------------------------------------------------

type cfgOpt func(*addetect.Config)

func set(key, value string) cfgOpt {
	return func(c *addetect.Config) {
		if err := c.Set(key, value); err != nil {
			panic(fmt.Sprintf("config %s=%s: %v", key, value, err))
		}
	}
}

func testConfig(opts ...cfgOpt) *addetect.Config {
	cfg := addetect.DefaultConfig()
	cfg.Outputs.Diagnostics = "detailed"
	for _, o := range opts {
		o(cfg)
	}
	return cfg
}

// tryAnalyze runs the full pipeline and returns whatever Analyze returned.
func tryAnalyze(src addetect.Source, opts ...cfgOpt) (*addetect.Result, error) {
	return addetect.Analyze(context.Background(), src, testConfig(opts...), nil)
}

// analyze runs the full pipeline, requires success and checks the
// invariants every successful result must satisfy.
func analyze(t testing.TB, src addetect.Source, opts ...cfgOpt) *addetect.Result {
	t.Helper()
	res, err := tryAnalyze(src, opts...)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	checkResult(t, res)
	return res
}

// analyzeFrames is analyze plus the per-frame raw measurements (API-02).
func analyzeFrames(t testing.TB, src addetect.Source, opts ...cfgOpt) (*addetect.Result, []addetect.FrameMeasurement) {
	t.Helper()
	var frames []addetect.FrameMeasurement
	res, err := addetect.Analyze(context.Background(), src, testConfig(opts...), &addetect.Options{
		FrameObserver: func(m addetect.FrameMeasurement) { frames = append(frames, m) },
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	checkResult(t, res)
	return res, frames
}

// wantKind requires err to be an addetect error of the given kind and the
// result to be absent (a failure never leaves a plausible result, QUAL-04).
func wantKind(t testing.TB, res *addetect.Result, err error, kind addetect.ErrorKind) {
	t.Helper()
	if err == nil {
		t.Fatalf("analysis succeeded; want a %s error", kind)
	}
	if got := addetect.KindOf(err); got != kind {
		t.Fatalf("error kind = %s, want %s (error: %v)", got, kind, err)
	}
	var ae *addetect.Error
	if !errors.As(err, &ae) {
		t.Errorf("error %T is not an *addetect.Error", err)
	}
	if res != nil && res.Complete {
		t.Errorf("a failed analysis returned a result marked complete")
	}
}

// checkResult asserts TIME-02 / OUT-03: integer-microsecond half-open
// intervals that start at zero, are ordered, nonempty and non-overlapping,
// and partition [0, duration) exactly once; breaks are advertisement time.
func checkResult(t testing.TB, res *addetect.Result) {
	t.Helper()
	if res == nil {
		t.Fatal("nil result")
	}
	if res.TimeUnit != "microseconds" {
		t.Errorf("time_unit = %q", res.TimeUnit)
	}
	if res.DurationUS <= 0 {
		t.Fatalf("duration_us = %d", res.DurationUS)
	}
	if len(res.Segments) == 0 {
		t.Fatal("no segments")
	}
	var at int64
	for i, s := range res.Segments {
		if s.StartUS != at {
			t.Errorf("segment %d starts at %d, want %d (gap or overlap)", i, s.StartUS, at)
		}
		if s.EndUS <= s.StartUS {
			t.Errorf("segment %d is empty: [%d,%d)", i, s.StartUS, s.EndUS)
		}
		switch s.Label {
		case addetect.LabelAdvertisement, addetect.LabelProgram, addetect.LabelUnknown:
		default:
			t.Errorf("segment %d has label %q", i, s.Label)
		}
		if i > 0 && res.Segments[i-1].Label == s.Label {
			t.Errorf("segments %d and %d share label %q but were not merged", i-1, i, s.Label)
		}
		at = s.EndUS
	}
	if at != res.DurationUS {
		t.Errorf("segments end at %d, duration_us is %d", at, res.DurationUS)
	}
	var adUS, breakUS, prev int64
	for _, s := range res.Segments {
		if s.Label == addetect.LabelAdvertisement {
			adUS += s.EndUS - s.StartUS
		}
	}
	for i, b := range res.AdBreaks {
		if b.StartUS < prev || b.EndUS <= b.StartUS || b.EndUS > res.DurationUS {
			t.Errorf("break %d [%d,%d) is unordered, empty or out of range", i, b.StartUS, b.EndUS)
		}
		prev = b.EndUS
		breakUS += b.EndUS - b.StartUS
		if len(b.UnitIDs) == 0 {
			t.Errorf("break %d has no units", i)
		}
	}
	if adUS != breakUS {
		t.Errorf("advertisement segments cover %d us but accepted breaks cover %d us", adUS, breakUS)
	}
	for _, g := range res.Coverage.Gaps {
		if g.StartUS < 0 || g.EndUS > res.DurationUS || g.EndUS <= g.StartUS {
			t.Errorf("coverage gap [%d,%d) is outside the timeline", g.StartUS, g.EndUS)
		}
	}
	if res.Timing.Frames <= 0 {
		t.Errorf("timing.frames = %d", res.Timing.Frames)
	}
	// OUT-03: the canonical document is valid JSON (no NaN/Inf).
	if _, err := json.Marshal(res); err != nil {
		t.Errorf("result does not serialize: %v", err)
	}
}

func detector(t testing.TB, res *addetect.Result, signal string) addetect.DetectorState {
	t.Helper()
	for _, d := range res.Detectors {
		if d.Signal == signal {
			return d
		}
	}
	t.Fatalf("no detector state for %q", signal)
	return addetect.DetectorState{}
}

func hasWarning(res *addetect.Result, code string) bool {
	for _, w := range res.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func warningCodes(res *addetect.Result) []string {
	var out []string
	for _, w := range res.Warnings {
		out = append(out, w.Code)
	}
	return out
}

func absDiff(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

func near(t testing.TB, what string, got, want, tol int64) {
	t.Helper()
	if absDiff(got, want) > tol {
		t.Errorf("%s = %d, want %d (+/- %d)", what, got, want, tol)
	}
}

// ---- raw stream access -----------------------------------------------------

// openRequest mirrors the request Analyze builds from the default
// configuration, for tests that read the adapter's observations directly.
func openRequest(warn *[]addetect.Warning) *addetect.OpenRequest {
	return &addetect.OpenRequest{
		VideoTrack: "auto", AudioTrack: "auto", CaptionTrack: "auto",
		NeedAudio: true, NeedCaptions: true,
		AnalysisMaxWidth: 384, AnalysisMaxHeight: 216,
		BrightPixelLuma: 60, FullResBrightCheck: true,
		AudioWindow: 20 * time.Millisecond,
		Threads:     2, DecodeFrames: "all", SkipLoopFilter: "nonref",
		Package: addetect.PackageRequest{
			ManifestType: "auto", VideoSelection: "highest", AudioSelection: "auto", MinHeight: 360,
			MaxManifestBytes: 16 << 20, MaxSegments: 1000000, IOBufferSize: 256 << 10,
		},
		Warn: func(w addetect.Warning) {
			if warn != nil {
				*warn = append(*warn, w)
			}
		},
	}
}

type rawFrame struct {
	pts, dur         int64
	codedW, codedH   int
	w, h, scale      int
	sarN, sarD       int
	mean             float64
	minV, maxV       byte
	bright           float32
	resource         int32
	interlaced, noTS bool
}

type rawStream struct {
	info       addetect.MediaInfo
	frames     []rawFrame
	audio      []addetect.AudioWindow
	gaps       []addetect.GapObservation
	decodeErrs []string
	warnings   []addetect.Warning
	// maxSkewUS is the largest distance, in delivery order, between the
	// video and audio presentation times reached so far.
	maxSkewUS int64
}

// readStream drains a source through the public Source/Stream contract.
func readStream(t testing.TB, src addetect.Source, mutate func(*addetect.OpenRequest)) *rawStream {
	t.Helper()
	rs := &rawStream{}
	req := openRequest(&rs.warnings)
	if mutate != nil {
		mutate(req)
	}
	st, err := src.Open(context.Background(), req)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	var ev addetect.Event
	for {
		ev.Kind = 0
		if err := st.Next(&ev); err != nil {
			if err != io.EOF {
				t.Fatalf("Next: %v", err)
			}
			break
		}
		switch ev.Kind {
		case addetect.EventVideo:
			f := &ev.Video
			rf := rawFrame{
				pts: f.PTS, dur: f.Duration, codedW: f.CodedWidth, codedH: f.CodedHeight,
				w: f.Width, h: f.Height, scale: f.Scale, sarN: f.SARNum, sarD: f.SARDen,
				bright: f.FullResBrightFraction, resource: f.Resource, interlaced: f.Interlaced, noTS: f.NoPTS,
				minV: 255,
			}
			if len(f.Luma) != f.Width*f.Height || f.Stride != f.Width {
				t.Fatalf("frame at %d: luma %d bytes, stride %d for %dx%d", f.PTS, len(f.Luma), f.Stride, f.Width, f.Height)
			}
			var sum uint64
			for _, v := range f.Luma {
				sum += uint64(v)
				rf.minV, rf.maxV = min(rf.minV, v), max(rf.maxV, v)
			}
			rf.mean = float64(sum) / float64(len(f.Luma))
			rs.frames = append(rs.frames, rf)
		case addetect.EventAudio:
			rs.audio = append(rs.audio, ev.Audio...)
		}
		if len(rs.frames) > 0 && len(rs.audio) > 0 && (ev.Kind == addetect.EventVideo || ev.Kind == addetect.EventAudio) {
			rs.maxSkewUS = max(rs.maxSkewUS, absDiff(rs.frames[len(rs.frames)-1].pts, rs.audio[len(rs.audio)-1].StartUS))
		}
		switch ev.Kind {
		case addetect.EventGap:
			rs.gaps = append(rs.gaps, ev.Gap)
		case addetect.EventDecodeError:
			rs.decodeErrs = append(rs.decodeErrs, ev.DecodeError)
		}
	}
	rs.info = *st.Info()
	return rs
}

// ---- ffprobe oracles -------------------------------------------------------

func ffprobe(t testing.TB, args ...string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", append([]string{"-v", "error"}, args...)...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("ffprobe %v: %v\n%s", args, err, ee.Stderr)
		}
		t.Fatalf("ffprobe %v: %v", args, err)
	}
	return string(out)
}

// decimalUS converts ffprobe's decimal seconds ("12.345678") to
// microseconds without going through binary floating point.
func decimalUS(t testing.TB, s string) int64 {
	t.Helper()
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	frac = (frac + "000000")[:6]
	w, err1 := strconv.ParseInt(whole, 10, 64)
	f, err2 := strconv.ParseInt(frac, 10, 64)
	if err1 != nil || err2 != nil {
		t.Fatalf("bad ffprobe time %q", s)
	}
	v := w*1_000_000 + f
	if neg {
		v = -v
	}
	return v
}

// probePTS returns the presentation timestamps (microseconds, ascending)
// of every packet of one stream, as ffprobe demuxes them.
func probePTS(t testing.TB, path, stream string) []int64 {
	t.Helper()
	out := ffprobe(t, "-select_streams", stream, "-show_entries", "packet=pts_time", "-of", "csv=p=0", path)
	var pts []int64
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(strings.TrimSpace(line), ",")
		if line == "" || line == "N/A" {
			continue
		}
		pts = append(pts, decimalUS(t, line))
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i] < pts[j] })
	return pts
}

// probeFrameCount returns the number of frames ffprobe decodes from the
// first video stream (-count_frames).
func probeFrameCount(t testing.TB, path string) int {
	t.Helper()
	out := ffprobe(t, "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=nb_read_frames", "-of", "csv=p=0", path)
	n, err := strconv.Atoi(strings.TrimRight(strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0]), ","))
	if err != nil {
		t.Fatalf("ffprobe frame count %q: %v", out, err)
	}
	return n
}

// ---- file helpers ----------------------------------------------------------

// writeFile creates or replaces a file. An existing file is removed first,
// so a hard link into the shared fixture directory is never written through.
func writeFile(t testing.TB, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// copyTree copies a fixture directory so a test can damage or rename its
// files. Media files are hard-linked when possible; the shared fixtures are
// never modified in place (writeFile and truncateCopy replace the link).
func copyTree(t testing.TB, src, dst string) string {
	t.Helper()
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		manifest := strings.HasSuffix(p, ".m3u8") || strings.HasSuffix(p, ".mpd")
		if !manifest && os.Link(p, target) == nil {
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
	return dst
}

// truncateCopy replaces path (possibly a hard link into the shared fixture
// directory) with a private copy cut to the given number of bytes.
func truncateCopy(t testing.TB, path string, size int64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if size > int64(len(data)) {
		t.Fatalf("%s has only %d bytes", path, len(data))
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[:size], 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileSize(t testing.TB, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// ---- the synthetic broadcast (see scripts/make-fixtures.sh) ----------------

const (
	bcFrameUS  = 100_000     // 10 fps
	bcWindowUS = 20_000      // audio.window default
	bcDuration = 280_600_000 // 2806 frames
	bcFrames   = 2806
	bcBreakIn  = 100_300_000
	bcBreakOut = 190_300_000
)

// bcUnits are the true ad-unit boundaries: the midpoints of the black spans.
var bcUnits = []int64{100_300_000, 115_300_000, 145_300_000, 160_300_000, 190_300_000}

var bcRef struct {
	once sync.Once
	res  *addetect.Result
	err  error
}

// broadcastRef analyzes the single-file broadcast once with the default
// configuration; packaged forms are compared against it (AT-25).
func broadcastRef(t testing.TB) *addetect.Result {
	t.Helper()
	path := fixture(t, "broadcast", "broadcast.mp4")
	bcRef.once.Do(func() {
		bcRef.res, bcRef.err = tryAnalyze(ffadapter.Path(path))
	})
	if bcRef.err != nil {
		t.Fatalf("reference analysis of the broadcast failed: %v", bcRef.err)
	}
	return bcRef.res
}

// checkBroadcast asserts the annotated detections of the broadcast: one
// break made of the four 15/30 s spots, found within one frame interval
// plus one audio window of the true boundaries (TEST-01), with program on
// both sides.
func checkBroadcast(t testing.TB, res *addetect.Result) {
	t.Helper()
	const tol = bcFrameUS + bcWindowUS
	near(t, "duration_us", res.DurationUS, bcDuration, 1)
	if res.Timing.Frames != bcFrames {
		t.Errorf("frames = %d, want %d", res.Timing.Frames, bcFrames)
	}
	if !res.Complete {
		t.Errorf("result is not complete")
	}
	if len(res.AdBreaks) != 1 {
		t.Fatalf("ad breaks = %+v, want exactly one", res.AdBreaks)
	}
	b := res.AdBreaks[0]
	near(t, "break start", b.StartUS, bcBreakIn, tol)
	near(t, "break end", b.EndUS, bcBreakOut, tol)
	if b.TruncatedStart || b.TruncatedEnd {
		t.Errorf("break is flagged truncated: %+v", b)
	}
	if len(res.AdUnits) != 4 {
		t.Fatalf("ad units = %d, want 4 (15 s, 30 s, 15 s, 30 s)", len(res.AdUnits))
	}
	for i, u := range res.AdUnits {
		near(t, fmt.Sprintf("unit %d start", i), u.StartUS, bcUnits[i], tol)
		near(t, fmt.Sprintf("unit %d end", i), u.EndUS, bcUnits[i+1], tol)
	}
	var labels []string
	for _, s := range res.Segments {
		labels = append(labels, s.Label)
	}
	if got := strings.Join(labels, ","); got != "program,advertisement,program" {
		t.Errorf("segment labels = %s, want program,advertisement,program", got)
	}
	if d := detector(t, res, addetect.SignalLogo); d.Status != addetect.StatusUsable {
		t.Errorf("logo detector is %s (%s), want usable", d.Status, d.Reason)
	}
}

// sameDetections asserts that two analyses of the same pictures agree
// within tol on duration, segments, breaks and units (AT-25).
func sameDetections(t testing.TB, what string, got, want *addetect.Result, tol int64) {
	t.Helper()
	near(t, what+": duration_us", got.DurationUS, want.DurationUS, tol)
	if got.Timing.Frames != want.Timing.Frames {
		t.Errorf("%s: %d frames, want %d", what, got.Timing.Frames, want.Timing.Frames)
	}
	if len(got.Segments) != len(want.Segments) {
		t.Fatalf("%s: %d segments, want %d\n got: %s\nwant: %s", what, len(got.Segments), len(want.Segments), segString(got), segString(want))
	}
	for i := range want.Segments {
		g, w := got.Segments[i], want.Segments[i]
		if g.Label != w.Label || absDiff(g.StartUS, w.StartUS) > tol || absDiff(g.EndUS, w.EndUS) > tol {
			t.Errorf("%s: segment %d = [%d,%d) %s, want [%d,%d) %s", what, i, g.StartUS, g.EndUS, g.Label, w.StartUS, w.EndUS, w.Label)
		}
	}
	if len(got.AdBreaks) != len(want.AdBreaks) || len(got.AdUnits) != len(want.AdUnits) {
		t.Fatalf("%s: %d breaks / %d units, want %d / %d", what, len(got.AdBreaks), len(got.AdUnits), len(want.AdBreaks), len(want.AdUnits))
	}
	for i := range want.AdBreaks {
		g, w := got.AdBreaks[i], want.AdBreaks[i]
		if absDiff(g.StartUS, w.StartUS) > tol || absDiff(g.EndUS, w.EndUS) > tol ||
			g.TruncatedStart != w.TruncatedStart || g.TruncatedEnd != w.TruncatedEnd || len(g.UnitIDs) != len(w.UnitIDs) {
			t.Errorf("%s: break %d = %+v, want %+v", what, i, g, w)
		}
	}
	for i := range want.AdUnits {
		g, w := got.AdUnits[i], want.AdUnits[i]
		if absDiff(g.StartUS, w.StartUS) > tol || absDiff(g.EndUS, w.EndUS) > tol || g.Score != w.Score {
			t.Errorf("%s: unit %d = [%d,%d) score %v, want [%d,%d) score %v", what, i, g.StartUS, g.EndUS, g.Score, w.StartUS, w.EndUS, w.Score)
		}
	}
}

func segString(res *addetect.Result) string {
	var sb strings.Builder
	for _, s := range res.Segments {
		fmt.Fprintf(&sb, "[%d,%d)%s ", s.StartUS, s.EndUS, s.Label)
	}
	return sb.String()
}
