// SPDX-License-Identifier: Apache-2.0

package ffadapter_test

// AT-20: isolation, cancellation and resource ownership of the adapter
// (API-03, API-04, API-05, QUAL-01, QUAL-02), plus reviews of the stream
// contract that the cgo layer has to honor.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/ffadapter"
)

// residentKB returns the resident set size of this process, or 0 when it
// cannot be measured. C allocations (demuxer, decoder, I/O buffers) are
// invisible to the Go heap statistics, so leaks are looked for here.
func residentKB() int64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return kb
}

// settleGoroutines waits for transient goroutines (context.AfterFunc
// callbacks) to finish and returns the count.
func settleGoroutines(limit int) int {
	n := runtime.NumGoroutine()
	for i := 0; i < 100 && n > limit; i++ {
		time.Sleep(10 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}

// AT-20 / API-03: Stream.Close after a mid-stream cancel releases
// everything. The cycle is repeated for a file, a failing open and a
// package, under the race detector when enabled; goroutines return to the
// baseline and the process does not grow (the demuxer, decoders and the
// custom-I/O buffer are freed on every path).
//
// Not parallel: it compares process-wide counters.
func TestCancelAndCloseReleaseEverything(t *testing.T) {
	file := fixture(t, "matrix", "mkv_aac.mkv")
	bad := fixture(t, "audio_only", "not_media.mp4")
	pkg := loadPackage(t, fixture(t, "pieces", "p0m"), "/pkg/")

	cycle := func(i int) {
		// 1. Cancel in the middle of a file and close.
		ctx, cancel := context.WithCancel(context.Background())
		st, err := ffadapter.Path(file).Open(ctx, openRequest(nil))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		var ev addetect.Event
		for k := 0; k < 5+i%20; k++ {
			if err := st.Next(&ev); err != nil {
				t.Fatalf("Next %d: %v", k, err)
			}
		}
		cancel()
		err = st.Next(&ev)
		if err == nil || err == io.EOF || addetect.KindOf(err) != addetect.KindCanceled {
			t.Fatalf("Next after cancel = %v, want a cancellation error", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}

		// 2. An open that fails after the demuxer was allocated.
		if _, err := ffadapter.Path(bad).Open(context.Background(), openRequest(nil)); err == nil {
			t.Fatal("opening a non-media file succeeded")
		}

		// 3. Cancel in the middle of a package (custom I/O) and close.
		ctx, cancel = context.WithCancel(context.Background())
		st, err = ffadapter.Package("/pkg/media.m3u8", pkg).Open(ctx, openRequest(nil))
		if err != nil {
			t.Fatalf("package Open: %v", err)
		}
		for k := 0; k < 30; k++ {
			if err := st.Next(&ev); err != nil {
				t.Fatalf("package Next %d: %v", k, err)
			}
		}
		cancel()
		if err := st.Next(&ev); err == nil || addetect.KindOf(err) != addetect.KindCanceled {
			t.Fatalf("package Next after cancel = %v, want a cancellation error", err)
		}
		st.Close()

		// 4. A package whose open fails on its first media resource.
		broken := &memResolver{files: map[string][]byte{"/pkg/media.m3u8": pkg.files["/pkg/media.m3u8"]}}
		if _, err := ffadapter.Package("/pkg/media.m3u8", broken).Open(context.Background(), openRequest(nil)); addetect.KindOf(err) != addetect.KindIO {
			t.Fatalf("package without media: err = %v, want an I/O error", err)
		}
	}

	const warm, rounds = 40, 240
	for i := 0; i < warm; i++ {
		cycle(i)
	}
	baseG := settleGoroutines(0)
	runtime.GC()
	baseKB := residentKB()
	for i := 0; i < rounds; i++ {
		cycle(i)
	}
	if o, c := pkg.opens.Load(), pkg.closes.Load(); o != c {
		t.Errorf("resolver: %d opens but %d closes", o, c)
	}
	if n := pkg.badArgs.Load(); n != 0 {
		t.Errorf("%d reads after close or double closes", n)
	}
	if g := settleGoroutines(baseG); g > baseG {
		t.Errorf("goroutines: %d after %d cancel/close cycles, baseline %d", g, rounds, baseG)
	}
	runtime.GC()
	if endKB := residentKB(); baseKB > 0 && endKB > 0 {
		// Each leaked demuxer context is tens of kilobytes and each leaked
		// custom-I/O buffer 256 KiB: 240 rounds would add well over 32 MiB.
		if grown := endKB - baseKB; grown > 32*1024 {
			t.Errorf("resident memory grew by %d KiB over %d open/cancel/close cycles (%d -> %d KiB): something is leaked", grown, rounds, baseKB, endKB)
		} else {
			t.Logf("resident memory: %d -> %d KiB over %d cycles", baseKB, endKB, rounds)
		}
	}
}

// AT-20 / API-03, API-04: cancelling a whole analysis mid-way returns a
// cancellation error promptly; a partial result, if any, is explicitly
// incomplete.
func TestAnalyzeCancellationMidFile(t *testing.T) {
	long(t)
	t.Parallel()
	src := ffadapter.Path(fixture(t, "broadcast", "broadcast.mp4"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var canceledAt time.Time
	frames := 0
	res, err := addetect.Analyze(ctx, src, testConfig(), &addetect.Options{
		FrameObserver: func(addetect.FrameMeasurement) {
			if frames++; frames == 700 {
				canceledAt = time.Now()
				cancel()
			}
		},
	})
	wantKind(t, res, err, addetect.KindCanceled)
	if !errors.Is(err, addetect.ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Errorf("error %v does not match ErrCanceled and context.Canceled", err)
	}
	if elapsed := time.Since(canceledAt); elapsed > 2*time.Second {
		t.Errorf("Analyze returned %v after cancellation", elapsed)
	}
	if frames > 800 {
		t.Errorf("%d frames decoded; decoding continued after cancellation at frame 700", frames)
	}
	if res != nil {
		if res.Complete || res.Error == nil || res.Error.Kind != "canceled" {
			t.Errorf("partial result: complete=%v error=%+v", res.Complete, res.Error)
		}
		if res.DurationUS > 90_000_000 {
			t.Errorf("partial result spans %d us although only ~70 s were decoded", res.DurationUS)
		}
	}
}

// AT-20 / API-03, QUAL-01: concurrent analyses of the same file share no
// mutable state: each one equals the sequential result. A Source is
// reusable and may be opened concurrently.
func TestConcurrentAnalysesAreIsolated(t *testing.T) {
	t.Parallel()
	files := []string{
		fixture(t, "ntsc", "ntsc.mp4"),
		fixture(t, "matrix", "ts_ac3.ts"),
		fixture(t, "reschange", "reschange.ts"),
	}
	hls := ""
	if !testing.Short() {
		hls = fixture(t, "hls_master", "master.m3u8")
		files = append(files, hls)
	}
	srcs := make([]*ffadapter.Source, len(files))
	want := make([][]byte, len(files))
	for i, f := range files {
		srcs[i] = ffadapter.Path(f)
		want[i] = normalized(t, analyze(t, srcs[i]))
	}
	const perFile = 4
	var wg sync.WaitGroup
	errs := make(chan string, len(files)*perFile)
	for i := range files {
		for k := 0; k < perFile; k++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Different worker counts on purpose: scheduling must not matter.
				res, err := tryAnalyze(srcs[i], set("resources.workers", strconv.Itoa(1+k)))
				if err != nil {
					errs <- files[i] + ": " + err.Error()
					return
				}
				if got := normalized(t, res); !bytes.Equal(got, want[i]) {
					errs <- files[i] + ": concurrent result differs: " + firstDiff(got, want[i])
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// API-03 (review): the Stream contract after Close. A stream that is used
// after it was closed must fail cleanly - never touch freed demuxer state
// (which would crash the host process) or acquire new resources.
func TestStreamUseAfterClose(t *testing.T) {
	t.Parallel()
	var ev addetect.Event

	st, err := ffadapter.Path(fixture(t, "matrix", "mp4_aac.mp4")).Open(context.Background(), openRequest(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Next(&ev); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := st.Next(&ev); err == nil || err == io.EOF {
		t.Errorf("file stream: Next after Close = %v, want an error", err)
	}
	if st.Info() == nil {
		t.Errorf("Info is nil after Close")
	}

	m := loadPackage(t, fixture(t, "pieces", "p0m"), "/pkg/")
	st, err = ffadapter.Package("/pkg/media.m3u8", m).Open(context.Background(), openRequest(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Next(&ev); err != nil {
		t.Fatal(err)
	}
	st.Close()
	opens := m.opens.Load()
	if err := st.Next(&ev); err == nil || err == io.EOF {
		t.Errorf("package stream: Next after Close = %v, want an error", err)
	}
	if o, c := m.opens.Load(), m.closes.Load(); o != opens || o != c {
		t.Errorf("package stream after Close: %d opens (was %d), %d closes", o, opens, c)
	}
}

// API-05: library operations never print to the process's stdout or
// stderr. The FFmpeg libraries log to stderr by default; analyses that
// provoke demuxer and decoder diagnostics are run in a child process whose
// output must stay empty.
func TestLibraryDoesNotWriteToStdio(t *testing.T) {
	if os.Getenv("TVAD_TEST_QUIET_CHILD") == "1" {
		// Child: inputs known to make libavformat/libavcodec complain.
		tryAnalyze(ffadapter.Path(fixture(t, "coverart", "coverart.mp4")))    // "stream 0, timescale not set"
		tryAnalyze(ffadapter.Path(fixture(t, "audio_only", "not_media.mp4"))) // "moov atom not found"
		tryAnalyze(ffadapter.Path(fixture(t, "reschange", "reschange.ts")))   // "Packet corrupt"
		tryAnalyze(ffadapter.Path(fixture(t, "reschange", "reschange_reset.ts")))
		dir := copyTree(t, fixture(t, "pieces", "p0m"), t.TempDir()+"/pkg")
		truncateCopy(t, dir+"/media.m4s", fileSize(t, dir+"/media.m4s")/3)
		tryAnalyze(ffadapter.Path(dir+"/media.m3u8"), degraded) // decoder errors on the cut fragment
		return
	}
	// Make sure the fixtures exist before the child runs (it shares them).
	fixture(t, "coverart")
	fixture(t, "audio_only")
	fixture(t, "reschange")
	fixture(t, "pieces")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLibraryDoesNotWriteToStdio$")
	cmd.Env = append(os.Environ(), "TVAD_TEST_QUIET_CHILD=1", "TVAD_FIXTURE_DIR="+fx.dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("child: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("the library wrote to stderr:\n%s", stderr.String())
	}
	if out := strings.TrimSpace(stdout.String()); out != "PASS" {
		t.Errorf("the library wrote to stdout:\n%s", out)
	}
}

// CFG-04 / sampling: coarse visual sampling (reference frames only) is an
// explicit, reported mode: fewer frames, a warning naming the resolution
// limit, and an estimated final frame duration that is reported as such
// (TIME-02).
func TestCoarseReferenceFrameDecoding(t *testing.T) {
	t.Parallel()
	path := fixture(t, "matrix", "mp4_aac.mp4")
	full := analyze(t, ffadapter.Path(path))
	res := analyze(t, ffadapter.Path(path), set("sampling.decode_frames", "reference"))
	if res.Timing.Frames >= full.Timing.Frames || res.Timing.Frames < 10 {
		t.Errorf("reference decoding produced %d frames (all: %d)", res.Timing.Frames, full.Timing.Frames)
	}
	if !hasWarning(res, "coarse_decode") {
		t.Errorf("warnings = %v, want coarse_decode", warningCodes(res))
	}
	if !res.Timing.FinalFrameDurationEstimated || !hasWarning(res, "final_frame_duration_estimated") {
		t.Errorf("final frame duration: estimated=%v warnings=%v", res.Timing.FinalFrameDurationEstimated, warningCodes(res))
	}
	// The timeline still spans the recording to within the coarser spacing.
	near(t, "duration_us", res.DurationUS, full.DurationUS, 3*res.Timing.MedianFrameDurationUS)
	if hasWarning(full, "coarse_decode") || full.Timing.FinalFrameDurationEstimated {
		t.Errorf("full decoding reported coarse sampling")
	}
}
