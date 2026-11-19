// SPDX-License-Identifier: Apache-2.0

package cutlist_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/cutlist"
)

func sample() *addetect.Result {
	lead := int64(-2_275_844)
	return &addetect.Result{
		DurationUS: 400_000_000,
		Timing:     addetect.TimingInfo{AudioStartOffsetUS: &lead},
		Segments: []addetect.Segment{
			{StartUS: 0, EndUS: 212_312_100, Label: addetect.LabelProgram},
			{StartUS: 212_312_100, EndUS: 396_596_200, Label: addetect.LabelAdvertisement},
			{StartUS: 396_596_200, EndUS: 400_000_000, Label: addetect.LabelUnknown},
		},
		SkipIntervals: []addetect.SkipInterval{{StartUS: 212_312_100, EndUS: 396_596_200}},
	}
}

func render(t *testing.T, format string, o *cutlist.Options) string {
	t.Helper()
	var b bytes.Buffer
	if err := cutlist.Write(&b, format, sample(), o); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The player clock adds the audio lead; the result clock does not.
func TestEDL(t *testing.T) {
	if got, want := render(t, cutlist.FormatEDL, nil), "214.588\t398.872\t3\n"; got != want {
		t.Errorf("player clock EDL = %q, want %q", got, want)
	}
	if got, want := render(t, cutlist.FormatEDL, &cutlist.Options{Clock: cutlist.ClockResult}), "212.312\t396.596\t3\n"; got != want {
		t.Errorf("result clock EDL = %q, want %q", got, want)
	}
}

// Chapters cover the media from 0 with one chapter per segment.
func TestChapters(t *testing.T) {
	ff := render(t, cutlist.FormatFFMetadata, nil)
	want := ";FFMETADATA1\n\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=214588\ntitle=Program\n" +
		"\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=214588\nEND=398872\ntitle=Advertisement\n" +
		"\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=398872\nEND=402276\ntitle=Unknown\n"
	if ff != want {
		t.Errorf("ffmetadata =\n%s\nwant\n%s", ff, want)
	}
	ogm := render(t, cutlist.FormatOGM, nil)
	if want := "CHAPTER01=00:00:00.000\nCHAPTER01NAME=Program\nCHAPTER02=00:03:34.588\nCHAPTER02NAME=Advertisement\nCHAPTER03=00:06:38.872\nCHAPTER03NAME=Unknown\n"; ogm != want {
		t.Errorf("ogm =\n%s\nwant\n%s", ogm, want)
	}
}

func TestErrors(t *testing.T) {
	var b bytes.Buffer
	if err := cutlist.Write(&b, "srt", sample(), nil); err == nil || !strings.Contains(err.Error(), "edl") {
		t.Errorf("unknown format: %v", err)
	}
	if err := cutlist.Write(&b, cutlist.FormatEDL, sample(), &cutlist.Options{Clock: "wall"}); err == nil {
		t.Errorf("unknown clock accepted")
	}
	// No audio: the player clock equals the result clock.
	r := sample()
	r.Timing.AudioStartOffsetUS = nil
	if cutlist.PlayerOffsetUS(r) != 0 {
		t.Errorf("offset without audio")
	}
}
