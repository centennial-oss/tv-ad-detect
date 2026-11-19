// SPDX-License-Identifier: Apache-2.0

//go:build unix

package ffadapter_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/ffadapter"
)

// MEDIA-01 (robustness): the manifest sniff only reads regular files. A
// FIFO given as input is not opened, read and closed by the sniff - which
// would consume its head and leave the demuxer waiting forever for a
// writer - so the analysis either reads the pipe or fails; it never
// deadlocks.
func TestFIFOInputDoesNotDeadlockInManifestSniff(t *testing.T) {
	t.Parallel()
	media, err := os.ReadFile(fixture(t, "matrix", "ts_aac.ts"))
	if err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(t.TempDir(), "in.ts")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo is not available: %v", err)
	}
	// One writer that sends the stream once and closes. If the sniff took
	// the first open, this writer would be gone before the demuxer opens
	// the pipe, and that open would block forever.
	wrote := make(chan error, 1)
	go func() {
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			wrote <- err
			return
		}
		_, err = w.Write(media)
		w.Close()
		wrote <- err
	}()
	type outcome struct {
		res *addetect.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := tryAnalyze(ffadapter.Path(fifo))
		done <- outcome{res, err}
	}()
	var out outcome
	select {
	case out = <-done:
	case <-time.After(20 * time.Second):
		// Release whichever side is still blocked in open before failing.
		if r, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
			defer r.Close()
		}
		t.Fatal("analysis of a FIFO did not return within 20 s: deadlock")
	}
	// Unblock the writer if the analysis failed before opening the pipe.
	if r, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
		defer r.Close()
	}
	select {
	case <-wrote:
	case <-time.After(5 * time.Second):
		t.Error("the writer is still blocked: the pipe was never opened for reading")
	}
	if out.err != nil {
		// Failing is acceptable; hanging or a plausible result is not.
		t.Logf("analysis of a FIFO failed (acceptable): %v", out.err)
		if out.res != nil && out.res.Complete {
			t.Errorf("a failed analysis returned a complete result")
		}
		return
	}
	checkResult(t, out.res)
	// Nothing was consumed before the demuxer: every frame is there.
	if out.res.Timing.Frames != 60 {
		t.Errorf("frames = %d, want all 60 (the head of the pipe must not be consumed by the sniff)", out.res.Timing.Frames)
	}
	t.Logf("analysis of a FIFO succeeded: %d frames, %d us, complete=%v, warnings %v", out.res.Timing.Frames, out.res.DurationUS, out.res.Complete, warningCodes(out.res))
}
