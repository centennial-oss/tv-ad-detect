// SPDX-License-Identifier: Apache-2.0

// Package cutlist renders an analysis result as cut lists for players and
// editors: an EDL of skip intervals and chapter files of the labeled
// segments (ffmetadata and OGM chapter text). Each format is written from
// its public description.
//
// Cut lists carry intervals only. The canonical result (JSON) remains the
// record of labels for unknown time, evidence, coverage and completeness;
// nothing here replaces it.
package cutlist

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
)

// Format names.
const (
	// FormatEDL is the MPlayer/Kodi edit decision list: one
	// "start end action" line per skip interval, times in seconds.
	FormatEDL = "edl"
	// FormatFFMetadata is FFmpeg's metadata file with one [CHAPTER] per
	// segment.
	FormatFFMetadata = "ffmetadata"
	// FormatOGM is OGM/Matroska simple chapter text
	// (CHAPTERnn=HH:MM:SS.mmm, CHAPTERnnNAME=...).
	FormatOGM = "ogm"
)

// Formats lists the supported format names.
var Formats = []string{FormatEDL, FormatFFMetadata, FormatOGM}

// Clocks for the times written.
const (
	// ClockPlayer shifts result times so that 0 is the start of the media
	// file as a player counts it: the result timeline starts at the first
	// video frame, which in a tuner capture comes after the first audio.
	ClockPlayer = "player"
	// ClockResult writes result times unchanged.
	ClockResult = "result"
)

// Format-specific constants.
const (
	edlActionCommercial = 3 // Kodi/MPlayer "commercial break"
	usPerMilli          = 1_000
	msPerSecond         = 1_000
	msPerMinute         = 60 * msPerSecond
	msPerHour           = 60 * msPerMinute
	secondsPerMinute    = 60
	minutesPerHour      = 60
	ffmetadataHeader    = ";FFMETADATA1"
	ffmetadataTimebase  = "TIMEBASE=1/1000"
	chapterNameProgram  = "Program"
	chapterNameAd       = "Advertisement"
	chapterNameUnknown  = "Unknown"
)

// Options control rendering.
type Options struct {
	// Clock is ClockPlayer (default when empty) or ClockResult.
	Clock string
}

// PlayerOffsetUS is the time between the start of the media as a player
// counts it and the result's time zero (the first video frame): the audio
// lead of a tuner capture, never negative.
func PlayerOffsetUS(res *addetect.Result) int64 {
	if res.Timing.AudioStartOffsetUS != nil && *res.Timing.AudioStartOffsetUS < 0 {
		return -*res.Timing.AudioStartOffsetUS
	}
	return 0
}

func offset(res *addetect.Result, o *Options) (int64, error) {
	switch o.Clock {
	case "", ClockPlayer:
		return PlayerOffsetUS(res), nil
	case ClockResult:
		return 0, nil
	}
	return 0, fmt.Errorf("cutlist: unknown clock %q (use %s or %s)", o.Clock, ClockPlayer, ClockResult)
}

// Write renders res in format to w.
func Write(w io.Writer, format string, res *addetect.Result, o *Options) error {
	if o == nil {
		o = &Options{}
	}
	off, err := offset(res, o)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(w)
	switch format {
	case FormatEDL:
		writeEDL(bw, res, off)
	case FormatFFMetadata:
		writeFFMetadata(bw, res, off)
	case FormatOGM:
		writeOGM(bw, res, off)
	default:
		return fmt.Errorf("cutlist: unknown format %q (use %s)", format, strings.Join(Formats, ", "))
	}
	return bw.Flush()
}

// seconds formats microseconds as seconds with millisecond precision.
func seconds(us int64) string {
	ms := (us + usPerMilli/2) / usPerMilli
	return fmt.Sprintf("%d.%03d", ms/msPerSecond, ms%msPerSecond)
}

// writeEDL writes one commercial-break line per skip interval.
func writeEDL(w *bufio.Writer, res *addetect.Result, off int64) {
	for _, s := range res.SkipIntervals {
		fmt.Fprintf(w, "%s\t%s\t%d\n", seconds(s.StartUS+off), seconds(s.EndUS+off), edlActionCommercial)
	}
}

type chapter struct {
	start, end int64 // microseconds, output clock
	name       string
}

// chapters covers the whole media with one chapter per segment; the player
// offset before the first segment joins the first chapter.
func chapters(res *addetect.Result, off int64) []chapter {
	out := make([]chapter, 0, len(res.Segments))
	for i, s := range res.Segments {
		c := chapter{start: s.StartUS + off, end: s.EndUS + off, name: chapterName(s.Label)}
		if i == 0 {
			c.start = 0
		}
		out = append(out, c)
	}
	return out
}

func chapterName(label string) string {
	switch label {
	case addetect.LabelProgram:
		return chapterNameProgram
	case addetect.LabelAdvertisement:
		return chapterNameAd
	}
	return chapterNameUnknown
}

// ffmetadataEscape escapes the characters ffmetadata treats specially.
func ffmetadataEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "=", `\=`, ";", `\;`, "#", `\#`, "\n", `\`+"\n")
	return r.Replace(s)
}

func writeFFMetadata(w *bufio.Writer, res *addetect.Result, off int64) {
	w.WriteString(ffmetadataHeader + "\n")
	for _, c := range chapters(res, off) {
		fmt.Fprintf(w, "\n[CHAPTER]\n%s\nSTART=%d\nEND=%d\ntitle=%s\n", ffmetadataTimebase,
			(c.start+usPerMilli/2)/usPerMilli, (c.end+usPerMilli/2)/usPerMilli, ffmetadataEscape(c.name))
	}
}

// clock formats microseconds as HH:MM:SS.mmm.
func clock(us int64) string {
	ms := (us + usPerMilli/2) / usPerMilli
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/msPerHour, ms/msPerMinute%minutesPerHour, ms/msPerSecond%secondsPerMinute, ms%msPerSecond)
}

func writeOGM(w *bufio.Writer, res *addetect.Result, off int64) {
	for i, c := range chapters(res, off) {
		n := i + 1
		fmt.Fprintf(w, "CHAPTER%02d=%s\nCHAPTER%02dNAME=%s\n", n, clock(c.start), n, c.name)
	}
}
