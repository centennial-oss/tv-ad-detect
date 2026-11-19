// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"strings"
	"testing"
	"time"
)

// This file is the synthetic "recording builder" shared by the acceptance
// tests (section 14: synthetic feature streams for precise algorithm tests).
// A scenario is a list of blocks laid end to end on an exact frame grid.
// Each block boundary may carry a separator (black frames and/or silence)
// centred on the boundary, so that the fused candidate boundary lands exactly
// on the block edge and block durations are exact.

const (
	sec = time.Second
	ms  = time.Millisecond

	tW, tH = 64, 36 // default analysis picture

	speechMS  = float32(1e-2) // about -20 dBFS
	silenceMS = float32(1e-8) // about -80 dBFS
)

type sepKind uint8

const (
	sepBlackSilence sepKind = iota // black frames with simultaneous silence (default)
	sepBlack                       // black frames, audio continues
	sepSilence                     // silence only, picture continues
	sepUniform                     // solid mid-gray frames, audio continues
	sepNone                        // nothing marks the boundary
	sepBlackLogo                   // black frames with the station logo still drawn, with silence
)

// blk describes one interval of a scenario.
type blk struct {
	D time.Duration

	Logo     bool          // station logo drawn in the top-right search region
	Bug      bool          // a second on-screen graphic in the top-left search region
	LB, PB   int           // letterbox rows / pillarbox columns on each side
	Shot     time.Duration // shot length (default 2s)
	HardCuts bool          // shots differ enough for a scene cut
	Level    int           // fixed base luminance (0 = shot sequence)
	Pic      []byte        // fixed picture overriding everything else

	Cap CaptionStyle // caller-decoded caption style covering the block (EventCaptionStyle)
	CC  string       // raw CEA-608 on CC1: "ru", "pop", "paint" or "null"

	Silent  bool    // audio below the silence level for the whole block
	NoAudio bool    // no audio windows at all (a hole)
	PeakMS  float32 // override loudest-channel level
	MeanMS  float32 // override mean level

	W, H   int // analysis and coded size (0 = scenario default)
	SARNum int
	SARDen int

	Skip bool // no media at all: a timestamp gap of length D

	Sep       sepKind // separator centred on this block's start boundary
	SepFrames int     // separator length in frames (default 2)
}

// scenario lays blocks out on a constant frame grid.
type scenario struct {
	fps         int
	startPTS    int64
	audioOffset int64 // audio timestamps are shifted by this much
	noAudio     bool
	noDuration  bool // frames do not declare a duration
	audioWinUS  int64
	silences    [][2]int64 // extra silent spans [start,end) in scenario time
	holes       [][2]int64 // spans without audio windows
	blocks      []blk
	pics        map[picKey][]byte
}

func newScenario(blocks ...blk) *scenario {
	return &scenario{fps: 10, audioWinUS: 20_000, blocks: blocks, pics: map[picKey][]byte{}}
}

func (s *scenario) add(b ...blk) *scenario { s.blocks = append(s.blocks, b...); return s }

type picKey struct {
	w, h, level, lb, pb int
	logo, bug           bool
}

// logoRect is where the synthetic logo sits: inside the default top-right
// search region, clear of the picture border and of small letterboxes.
func logoRect(w, h int) (x0, y0, x1, y1 int) {
	return w * 47 / 64, h * 5 / 36, w * 55 / 64, h * 8 / 36
}

// picture renders program-like content: a smooth horizontal luminance ramp
// around level (no Sobel edges, nonuniformity about 17), optional black
// letterbox/pillarbox borders and an optional bright logo box.
func picture(w, h, level, lb, pb int, logo bool) []byte {
	return pictureWith(w, h, level, lb, pb, logo, false)
}

func pictureWith(w, h, level, lb, pb int, logo, bug bool) []byte {
	p := make([]byte, w*h)
	for y := lb; y < h-lb; y++ {
		for x := pb; x < w-pb; x++ {
			p[y*w+x] = byte(level - 30 + 60*x/(w-1))
		}
	}
	if logo {
		x0, y0, x1, y1 := logoRect(w, h)
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				p[y*w+x] = 240
			}
		}
	}
	if bug {
		// Mirror image of the logo box, in the top-left search region.
		x0, y0, x1, y1 := logoRect(w, h)
		for y := y0; y < y1; y++ {
			for x := w - x1; x < w-x0; x++ {
				p[y*w+x] = 240
			}
		}
	}
	return p
}

func flatPicture(w, h int, v byte) []byte {
	return bytes.Repeat([]byte{v}, w*h)
}

func (s *scenario) pic(k picKey) []byte {
	if p, ok := s.pics[k]; ok {
		return p
	}
	p := pictureWith(k.w, k.h, k.level, k.lb, k.pb, k.logo, k.bug)
	s.pics[k] = p
	return p
}

// shotLevel is the base luminance of shot k. The drifting sequence changes
// the histogram enough for logo learning (>= 0.05) but far too little for a
// scene cut; the hard-cut sequence alternates between disjoint ranges.
func shotLevel(k int64, hard bool) int {
	if hard {
		if k%2 == 0 {
			return 90
		}
		return 150
	}
	tri := k % 12
	if tri > 6 {
		tri = 12 - tri
	}
	return 96 + 8*int(tri)
}

func par(b byte) byte {
	b &= 0x7f
	if bits.OnesCount8(b)%2 == 0 {
		b |= 0x80
	}
	return b
}

// cc608 builds field-1 cc_data triplets from byte pairs.
func cc608(pairs ...[2]byte) []byte {
	out := make([]byte, 0, 3*len(pairs))
	for _, p := range pairs {
		out = append(out, 0xFC, par(p[0]), par(p[1]))
	}
	return out
}

var (
	ccNull  = []byte{0xFC, 0x80, 0x80}
	ccRU2   = cc608([2]byte{0x14, 0x25}, [2]byte{0x14, 0x25}, [2]byte{'A', 'B'})
	ccPop   = cc608([2]byte{0x14, 0x20}, [2]byte{0x14, 0x20}, [2]byte{'A', 'B'})
	ccPaint = cc608([2]byte{0x14, 0x29}, [2]byte{0x14, 0x29}, [2]byte{'A', 'B'})
)

func (s *scenario) frameUS() int64 { return int64(1_000_000 / s.fps) }

// bounds returns the cumulative block boundaries in microseconds.
func (s *scenario) bounds() []int64 {
	out := make([]int64, len(s.blocks)+1)
	for i, b := range s.blocks {
		out[i+1] = out[i] + us(b.D)
	}
	return out
}

// events renders the scenario.
func (s *scenario) events() []Event {
	fd := s.frameUS()
	bnd := s.bounds()
	total := bnd[len(bnd)-1]
	if total%fd != 0 {
		panic("scenario duration is not on the frame grid")
	}
	// sepAt reports the separator covering time t, if any.
	sepAt := func(t int64) (sepKind, bool) {
		for k := 1; k < len(s.blocks); k++ {
			b := &s.blocks[k]
			if b.Sep == sepNone || b.Skip || s.blocks[k-1].Skip {
				continue
			}
			n := b.SepFrames
			if n == 0 {
				n = 2
			}
			lo := bnd[k] - int64(n/2)*fd
			hi := lo + int64(n)*fd
			if t >= lo && t < hi {
				return b.Sep, true
			}
		}
		return 0, false
	}
	var evs []Event
	k := 0
	capDone := make([]bool, len(s.blocks))
	for t := int64(0); t < total; t += fd {
		for t >= bnd[k+1] {
			k++
		}
		b := &s.blocks[k]
		if b.Skip {
			continue
		}
		if b.Cap != CaptionUnknown && !capDone[k] {
			capDone[k] = true
			evs = append(evs, Event{Kind: EventCaptionStyle, Caption: CaptionObservation{
				PTS: s.startPTS + bnd[k], EndPTS: s.startPTS + bnd[k+1], Style: b.Cap,
			}})
		}
		w, h := b.W, b.H
		if w == 0 {
			w, h = tW, tH
		}
		sep, inSep := sepAt(t)
		var luma []byte
		switch {
		case inSep && (sep == sepBlackSilence || sep == sepBlack):
			luma = s.flat(w, h, 0)
		case inSep && sep == sepBlackLogo:
			luma = s.pic(picKey{w: w, h: h, lb: h, pb: w, logo: true})
		case inSep && sep == sepUniform:
			luma = s.flat(w, h, 128)
		case b.Pic != nil:
			luma = b.Pic
		default:
			level := b.Level
			if level == 0 {
				shot := us(b.Shot)
				if shot == 0 {
					shot = 2_000_000
				}
				level = shotLevel(t/shot, b.HardCuts)
			}
			luma = s.pic(picKey{w: w, h: h, level: level, lb: b.LB, pb: b.PB, logo: b.Logo, bug: b.Bug})
		}
		f := VideoFrame{
			PTS: s.startPTS + t, Duration: fd, CodedWidth: w, CodedHeight: h,
			SARNum: b.SARNum, SARDen: b.SARDen,
			Luma: luma, Width: w, Height: h, Stride: w, Scale: 1,
			FullResBrightFraction: -1, Resource: -1,
		}
		if s.noDuration {
			f.Duration = 0
		}
		switch b.CC {
		case "ru":
			f.CC = ccRU2
		case "pop":
			f.CC = ccPop
		case "paint":
			f.CC = ccPaint
		case "null":
			f.CC = ccNull
		}
		evs = append(evs, Event{Kind: EventVideo, Video: f})

		if s.noAudio || b.NoAudio {
			continue
		}
		ws := make([]AudioWindow, 0, fd/s.audioWinUS)
		for a := t; a < t+fd; a += s.audioWinUS {
			if inSpans(s.holes, a) {
				continue
			}
			peak, mean := speechMS, speechMS
			if b.PeakMS != 0 {
				peak = b.PeakMS
			}
			if b.MeanMS != 0 {
				mean = b.MeanMS
			}
			if sk, in := sepAt(a); b.Silent || inSpans(s.silences, a) || (in && (sk == sepBlackSilence || sk == sepSilence || sk == sepBlackLogo)) {
				peak, mean = silenceMS, silenceMS
			}
			ws = append(ws, AudioWindow{StartUS: s.startPTS + s.audioOffset + a, DurationUS: int32(s.audioWinUS), PeakMS: peak, MeanMS: mean})
		}
		evs = append(evs, Event{Kind: EventAudio, Audio: ws})
	}
	return evs
}

func inSpans(spans [][2]int64, t int64) bool {
	for _, sp := range spans {
		if t >= sp[0] && t < sp[1] {
			return true
		}
	}
	return false
}

func (s *scenario) flat(w, h int, v byte) []byte {
	k := picKey{w: w, h: h, level: -1 - int(v)}
	if p, ok := s.pics[k]; ok {
		return p
	}
	p := flatPicture(w, h, v)
	s.pics[k] = p
	return p
}

func (s *scenario) source() *SliceSource { return &SliceSource{Events: s.events()} }

// testConfig returns the defaults with full diagnostics.
func testConfig() *Config {
	c := DefaultConfig()
	c.Outputs.Diagnostics = "full"
	return c
}

// analyze runs the pipeline, requires success and checks the output
// invariants every result must satisfy.
func analyze(t *testing.T, src Source, cfg *Config) *Result {
	t.Helper()
	res, err := Analyze(context.Background(), src, cfg, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	checkInvariants(t, res)
	return res
}

func (s *scenario) run(t *testing.T, cfg *Config) *Result {
	t.Helper()
	if cfg == nil {
		cfg = testConfig()
	}
	return analyze(t, s.source(), cfg)
}

// checkInvariants asserts TIME-02 / OUT-03 on any result, complete or not.
func checkInvariants(t *testing.T, r *Result) {
	t.Helper()
	if r.TimeUnit != "microseconds" || r.SchemaVersion != SchemaVersion {
		t.Errorf("time unit %q schema %q", r.TimeUnit, r.SchemaVersion)
	}
	if r.DurationUS <= 0 {
		t.Fatalf("duration %d", r.DurationUS)
	}
	// Segments partition [0,duration) exactly once: ordered, nonempty,
	// half-open and gapless.
	if len(r.Segments) == 0 {
		t.Fatalf("no segments")
	}
	var at int64
	for i, s := range r.Segments {
		if s.StartUS != at {
			t.Errorf("segment %d starts at %d, want %d (gap or overlap)", i, s.StartUS, at)
		}
		if s.EndUS <= s.StartUS {
			t.Errorf("segment %d is empty: [%d,%d)", i, s.StartUS, s.EndUS)
		}
		switch s.Label {
		case LabelAdvertisement, LabelProgram, LabelUnknown:
		default:
			t.Errorf("segment %d has label %q", i, s.Label)
		}
		if i > 0 && r.Segments[i-1].Label == s.Label {
			t.Errorf("segments %d and %d are adjacent with the same label %q", i-1, i, s.Label)
		}
		if len(s.BlockIDs) == 0 || len(s.Reasons) == 0 {
			t.Errorf("segment %d has no block ids or no reasons: %+v", i, s)
		}
		if s.Coverage < 0 || s.Coverage > 1 {
			t.Errorf("segment %d coverage %v", i, s.Coverage)
		}
		at = s.EndUS
	}
	if at != r.DurationUS {
		t.Errorf("segments end at %d, duration is %d", at, r.DurationUS)
	}
	// Aspect intervals are ordered, nonempty and never overlap (R3).
	if r.Diagnostics != nil {
		for i, iv := range r.Diagnostics.Events.Aspect {
			if iv.EndUS <= iv.StartUS {
				t.Errorf("aspect interval %d is empty: [%d,%d)", i, iv.StartUS, iv.EndUS)
			}
			if i > 0 && iv.StartUS < r.Diagnostics.Events.Aspect[i-1].EndUS {
				t.Errorf("aspect intervals %d and %d overlap: [%d,%d) and [%d,%d)", i-1, i,
					r.Diagnostics.Events.Aspect[i-1].StartUS, r.Diagnostics.Events.Aspect[i-1].EndUS, iv.StartUS, iv.EndUS)
			}
		}
	}
	// Accepted breaks correspond exactly to advertisement-labeled segments.
	var ads []Segment
	for _, s := range r.Segments {
		if s.Label == LabelAdvertisement {
			ads = append(ads, s)
		}
	}
	if len(ads) != len(r.AdBreaks) {
		t.Errorf("%d advertisement segments but %d ad breaks", len(ads), len(r.AdBreaks))
	} else {
		for i, b := range r.AdBreaks {
			if b.StartUS != ads[i].StartUS || b.EndUS != ads[i].EndUS || ads[i].BreakID != b.ID {
				t.Errorf("break %s [%d,%d) does not match advertisement segment [%d,%d) (break id %q)", b.ID, b.StartUS, b.EndUS, ads[i].StartUS, ads[i].EndUS, ads[i].BreakID)
			}
		}
	}
	// Units tile their break except for bridged gaps.
	units := map[string]AdUnit{}
	for _, u := range r.AdUnits {
		units[u.ID] = u
		if math.IsNaN(u.Score) || math.IsInf(u.Score, 0) || u.Score <= 0 {
			t.Errorf("unit %s score %v", u.ID, u.Score)
		}
	}
	for _, b := range r.AdBreaks {
		at := b.StartUS
		var bridged int64
		for _, g := range b.BridgedGaps {
			bridged += g.EndUS - g.StartUS
		}
		var sum int64
		for _, id := range b.UnitIDs {
			u, ok := units[id]
			if !ok {
				t.Errorf("break %s references unknown unit %s", b.ID, id)
				continue
			}
			if u.BreakID != b.ID || u.StartUS < at || u.EndUS > b.EndUS {
				t.Errorf("unit %s [%d,%d) misplaced in break %s [%d,%d)", id, u.StartUS, u.EndUS, b.ID, b.StartUS, b.EndUS)
			}
			at = u.EndUS
			sum += u.EndUS - u.StartUS
		}
		if sum+bridged != b.EndUS-b.StartUS {
			t.Errorf("break %s: units cover %d + bridged %d of %d", b.ID, sum, bridged, b.EndUS-b.StartUS)
		}
	}
	for i, s := range r.SkipIntervals {
		if s.StartUS < 0 || s.EndUS > r.DurationUS || s.EndUS <= s.StartUS {
			t.Errorf("skip interval %d [%d,%d) outside the timeline", i, s.StartUS, s.EndUS)
		}
		if i > 0 && s.StartUS <= r.SkipIntervals[i-1].EndUS {
			t.Errorf("skip intervals %d and %d overlap or touch", i-1, i)
		}
	}
	if d := r.Diagnostics; d != nil {
		for i, b := range d.Boundaries {
			if i > 0 && b.TimeUS <= d.Boundaries[i-1].TimeUS {
				t.Errorf("boundary %s at %d is not after %s at %d", b.ID, b.TimeUS, d.Boundaries[i-1].ID, d.Boundaries[i-1].TimeUS)
			}
			if len(b.Causes) == 0 {
				t.Errorf("boundary %s has no cause", b.ID)
			}
		}
		if n := len(d.Boundaries); n < 2 || d.Boundaries[0].TimeUS != 0 || d.Boundaries[n-1].TimeUS != r.DurationUS {
			t.Errorf("boundaries do not span the recording")
		}
		if len(d.Blocks) != len(d.Boundaries)-1 {
			t.Errorf("%d blocks for %d boundaries", len(d.Blocks), len(d.Boundaries))
		}
		for i, b := range d.Blocks {
			// BLOCK-01: gapless partition between consecutive boundaries, no
			// zero-length blocks.
			if b.StartUS != d.Boundaries[i].TimeUS || b.EndUS != d.Boundaries[i+1].TimeUS || b.EndUS <= b.StartUS {
				t.Errorf("block %s [%d,%d) does not match its boundaries", b.ID, b.StartUS, b.EndUS)
			}
			if math.IsNaN(b.Score) || math.IsInf(b.Score, 0) || b.Score <= 0 {
				t.Errorf("block %s score %v", b.ID, b.Score)
			}
		}
		for _, h := range d.Hypotheses {
			if math.IsNaN(h.Score) || math.IsInf(h.Score, 0) || h.Score <= 0 {
				t.Errorf("hypothesis %s score %v", h.ID, h.Score)
			}
			if h.Selected && !h.Eligible {
				t.Errorf("hypothesis %s selected but not eligible", h.ID)
			}
		}
	}
	// OUT-03: the canonical JSON has no NaN/Inf (encoding/json rejects them)
	// and represents empty lists as [] rather than null.
	js, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("result does not encode as JSON: %v", err)
	}
	var tree any
	if err := json.Unmarshal(js, &tree); err != nil {
		t.Fatalf("result JSON does not decode: %v", err)
	}
	checkNoNullLists(t, "", tree)
}

// listKeys are the JSON keys whose values are lists in the result schema.
var listKeys = map[string]bool{
	"segments": true, "ad_breaks": true, "ad_units": true, "skip_intervals": true,
	"detectors": true, "warnings": true, "gaps": true, "logo": true,
	"block_ids": true, "reasons": true, "unit_ids": true, "boundary_ids": true,
	"break_ids": true, "factors": true, "evidence_families": true,
	"boundaries": true, "blocks": true, "hypotheses": true, "rejected_runs": true,
	"causes": true, "evidence": true, "program_evidence": true, "hypothesis_ids": true,
	"dark_uniform": true, "silence": true, "scene": true, "resolution": true,
	"aspect": true, "captions": true, "bumpers": true,
}

func checkNoNullLists(t *testing.T, path string, v any) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			p := path + "." + k
			if c == nil && listKeys[k] {
				t.Errorf("JSON %s is null, want []", p)
			}
			checkNoNullLists(t, p, c)
		}
	case []any:
		for i, c := range x {
			checkNoNullLists(t, fmt.Sprintf("%s[%d]", path, i), c)
		}
	}
}

// timeline renders the segments compactly, e.g. "U0-60 A60-90 P90-150"
// (seconds).
func timeline(r *Result) string {
	var sb strings.Builder
	for i, s := range r.Segments {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteByte(strings.ToUpper(s.Label)[0])
		fmt.Fprintf(&sb, "%g-%g", float64(s.StartUS)/1e6, float64(s.EndUS)/1e6)
	}
	return sb.String()
}

func wantTimeline(t *testing.T, r *Result, want string) {
	t.Helper()
	if got := timeline(r); got != want {
		t.Errorf("timeline = %s\n            want %s", got, want)
	}
}

func boundaryTimes(r *Result) []float64 {
	out := make([]float64, len(r.Diagnostics.Boundaries))
	for i, b := range r.Diagnostics.Boundaries {
		out[i] = float64(b.TimeUS) / 1e6
	}
	return out
}

func wantBoundaries(t *testing.T, r *Result, want ...float64) {
	t.Helper()
	got := boundaryTimes(r)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("boundaries = %v, want %v", got, want)
	}
}

// boundaryAt returns the boundary at exactly t, or nil.
func boundaryAt(r *Result, t time.Duration) *Boundary {
	for i := range r.Diagnostics.Boundaries {
		if r.Diagnostics.Boundaries[i].TimeUS == us(t) {
			return &r.Diagnostics.Boundaries[i]
		}
	}
	return nil
}

func mustBoundary(t *testing.T, r *Result, at time.Duration) *Boundary {
	t.Helper()
	b := boundaryAt(r, at)
	if b == nil {
		t.Fatalf("no boundary at %v; have %v", at, boundaryTimes(r))
	}
	return b
}

func hasCause(b *Boundary, signal, kind, role string) bool {
	for _, c := range b.Causes {
		if c.Signal == signal && (kind == "" || c.Kind == kind) && (role == "" || c.Role == role) {
			return true
		}
	}
	return false
}

// blockAt returns the block containing t.
func blockAt(t *testing.T, r *Result, at time.Duration) *Block {
	t.Helper()
	for i := range r.Diagnostics.Blocks {
		b := &r.Diagnostics.Blocks[i]
		if b.StartUS <= us(at) && us(at) < b.EndUS {
			return b
		}
	}
	t.Fatalf("no block at %v", at)
	return nil
}

// blockSpan returns the block spanning exactly [s,e).
func blockSpan(t *testing.T, r *Result, s, e time.Duration) *Block {
	t.Helper()
	b := blockAt(t, r, s)
	if b.StartUS != us(s) || b.EndUS != us(e) {
		t.Fatalf("block at %v is [%d,%d), want [%d,%d); boundaries %v", s, b.StartUS, b.EndUS, us(s), us(e), boundaryTimes(r))
	}
	return b
}

// hypothesis returns the evaluated hypothesis spanning exactly [s,e).
func hypothesis(t *testing.T, r *Result, s, e time.Duration) *Hypothesis {
	t.Helper()
	for i := range r.Diagnostics.Hypotheses {
		h := &r.Diagnostics.Hypotheses[i]
		if h.StartUS == us(s) && h.EndUS == us(e) {
			return h
		}
	}
	t.Fatalf("no hypothesis [%v,%v); boundaries %v", s, e, boundaryTimes(r))
	return nil
}

func findFactor(fs []Factor, rule string) *Factor {
	for i := range fs {
		if fs[i].Rule == rule {
			return &fs[i]
		}
	}
	return nil
}

func rules(fs []Factor) string {
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = f.Rule
	}
	return strings.Join(names, ",")
}

func detector(t *testing.T, r *Result, signal string) DetectorState {
	t.Helper()
	for _, d := range r.Detectors {
		if d.Signal == signal {
			return d
		}
	}
	t.Fatalf("no detector state for %q", signal)
	return DetectorState{}
}

func hasString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func hasWarning(r *Result, code string) bool {
	for _, w := range r.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func segmentAt(t *testing.T, r *Result, at time.Duration) Segment {
	t.Helper()
	for _, s := range r.Segments {
		if s.StartUS <= us(at) && us(at) < s.EndUS {
			return s
		}
	}
	t.Fatalf("no segment at %v", at)
	return Segment{}
}

// videoFrame builds one synthetic frame for hand-written event lists.
func videoFrame(pts, dur int64, luma []byte) Event {
	return Event{Kind: EventVideo, Video: VideoFrame{
		PTS: pts, Duration: dur, CodedWidth: tW, CodedHeight: tH,
		Luma: luma, Width: tW, Height: tH, Stride: tW, Scale: 1,
		FullResBrightFraction: -1, Resource: -1,
	}}
}

// content is a non-dark, non-uniform default picture.
var content = picture(tW, tH, 110, 0, 0, false)

func us2d(u int64) time.Duration { return time.Duration(u) * time.Microsecond }
