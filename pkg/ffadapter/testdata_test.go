// SPDX-License-Identifier: Apache-2.0

package ffadapter_test

// Optional local-only checks over the over-the-air test recordings in
// ref/testdata-ota-2026 (see doc/development.md). They decode several
// 35-minute recordings, so they run only with TVAD_TESTDATA=1; the
// recordings are not redistributable and are never committed.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/ffadapter"
)

const (
	testdataEnv     = "TVAD_TESTDATA"
	testdataChanEnv = "TVAD_TESTDATA_CHANNELS"
	testdataDir     = "testdata-ota-2026"
	// frameSlackUS is one 29.97 fps frame: re-encoded files may place a
	// break edge one frame apart.
	frameSlackUS = 34_000
)

// R11, R12 (phase 6): every format of one capture gives the same breaks.
// MP4 and HLS share one bitstream and must agree exactly; DASH may end
// early where its manifest declares a shorter presentation; the original
// transport stream and the MKVs agree with the MP4 to within one frame.
func TestTestdataFormatsAgree(t *testing.T) {
	if os.Getenv(testdataEnv) != "1" {
		t.Skip("set " + testdataEnv + "=1 to analyze the local test recordings")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", "ref", testdataDir))
	if err != nil {
		t.Fatal(err)
	}
	channels := []string{"ch-2-1", "ch-4-1"}
	if v := os.Getenv(testdataChanEnv); v != "" {
		channels = strings.Split(v, ",")
	}
	for _, ch := range channels {
		t.Run(ch, func(t *testing.T) {
			dir := filepath.Join(root, ch)
			mp4 := filepath.Join(dir, ch+".mp4")
			if _, err := os.Stat(mp4); err != nil {
				t.Skip(ch + " is not present")
			}
			ref := analyze(t, ffadapter.Path(mp4))
			exact := []string{filepath.Join(dir, "cmaf", "master.m3u8")}
			close := []string{filepath.Join(dir, "original", ch+".ts"), filepath.Join(dir, ch+".h264.mkv"), filepath.Join(dir, ch+".mpeg2.mkv")}
			for _, p := range exact {
				if r := analyzeIfPresent(t, p); r != nil && segString(r) != segString(ref) {
					t.Errorf("%s: segments differ from the MP4\n%s\n%s", filepath.Base(p), segString(r), segString(ref))
				}
			}
			if r := analyzeIfPresent(t, filepath.Join(dir, "cmaf", "manifest.mpd")); r != nil {
				sameBreaks(t, "manifest.mpd", r, ref, frameSlackUS, true)
			}
			for _, p := range close {
				if r := analyzeIfPresent(t, p); r != nil {
					sameBreaks(t, filepath.Base(p), r, ref, frameSlackUS, false)
				}
			}
		})
	}
}

func analyzeIfPresent(t *testing.T, path string) *addetect.Result {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	return analyze(t, ffadapter.Path(path))
}

// sameBreaks requires the same number of breaks with edges within slack;
// with endEarly the last break may end earlier (a shorter presentation).
func sameBreaks(t *testing.T, what string, got, want *addetect.Result, slack int64, endEarly bool) {
	t.Helper()
	if len(got.AdBreaks) != len(want.AdBreaks) {
		t.Errorf("%s: %d breaks, MP4 has %d\n%s\n%s", what, len(got.AdBreaks), len(want.AdBreaks), segString(got), segString(want))
		return
	}
	for i := range got.AdBreaks {
		g, w := got.AdBreaks[i], want.AdBreaks[i]
		endOK := absDiff(g.EndUS, w.EndUS) <= slack || (endEarly && i == len(got.AdBreaks)-1 && g.EndUS == got.DurationUS && g.EndUS < w.EndUS)
		if absDiff(g.StartUS, w.StartUS) > slack || !endOK {
			t.Errorf("%s: break %d [%d, %d), MP4 [%d, %d)", what, i, g.StartUS, g.EndUS, w.StartUS, w.EndUS)
		}
	}
}
