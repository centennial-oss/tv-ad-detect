// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"context"
	"math/rand"
	"runtime"
	"testing"
	"time"
)

// Benchmarks (QUAL-03) and the steady-state allocation check of the
// per-frame path (QUAL-02: decode-side work scales with the processing
// window, not the recording).

const benchW, benchH = 384, 216 // the default analysis geometry of 16:9 video

// benchExtractor returns an extractor with every detector enabled (a bumper
// template included) and compact storage reserved for durationUS of video.
func benchExtractor(tb testing.TB, durationUS int64) *extractor {
	tb.Helper()
	cfg := DefaultConfig()
	p := card(128, 72, 8, 6)
	cfg.BumperTemplates = []BumperTemplate{{
		ID: "card", PNG: encodeGray(tb, 128, 72, func(x, y int) byte { return p[y*128+x] }),
		Region: Region{0.25, 0.25, 0.5, 0.5}, Geometry: "stretch", Threshold: 0.9, MinSupport: 400 * time.Millisecond,
	}}
	if err := cfg.Validate(); err != nil {
		tb.Fatal(err)
	}
	assets, err := LoadAssets(cfg)
	if err != nil {
		tb.Fatal(err)
	}
	x := newExtractor(cfg, assets, func(string, string, int64, bool) {})
	x.reserve(durationUS, 30)
	return x
}

// benchPictures are a few program-like 384x216 pictures with a logo.
func benchPictures() [][]byte {
	out := make([][]byte, 8)
	rng := rand.New(rand.NewSource(7))
	for i := range out {
		p := picture(benchW, benchH, 100+6*i, 0, 0, true)
		// Some texture so histograms, edges and borders have work to do.
		for k := 0; k < len(p); k += 1 + rng.Intn(5) {
			if p[k] < 230 {
				p[k] += byte(rng.Intn(12))
			}
		}
		out[i] = p
	}
	return out
}

func benchFrame(luma []byte, pts int64) VideoFrame {
	return VideoFrame{
		PTS: pts, Duration: 33_367, CodedWidth: 1920, CodedHeight: 1080,
		Luma: luma, Width: benchW, Height: benchH, Stride: benchW, Scale: 5,
		FullResBrightFraction: 0.02, Resource: -1,
	}
}

// BenchmarkExtractVideoFrame measures the per-frame extraction cost at
// 384x216 with all detectors on, at a 29.97 fps cadence (so logo, aspect and
// bumper observations happen at their configured intervals).
func BenchmarkExtractVideoFrame(b *testing.B) {
	pics := benchPictures()
	const chunk = 18_000 // ten minutes of video per extractor
	x := benchExtractor(b, 600_000_000)
	b.ReportAllocs()
	b.SetBytes(benchW * benchH)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		k := i % chunk
		if k == 0 && i > 0 {
			b.StopTimer()
			x = benchExtractor(b, 600_000_000)
			b.StartTimer()
		}
		// A new shot every two seconds.
		f := benchFrame(pics[(k/60)%len(pics)], int64(k)*33_367)
		if err := x.video(&f); err != nil {
			b.Fatal(err)
		}
	}
}

// benchBlocks is a long recording's worth of blocks: program stretches with
// the logo, breaks of several logo-free ads, some neutral blocks.
func benchBlocks(n int) []simBlock {
	rng := rand.New(rand.NewSource(11))
	std := []int64{10, 15, 20, 30, 30, 30, 45, 60}
	out := make([]simBlock, 0, n)
	for len(out) < n {
		for k := 2 + rng.Intn(4); k > 0 && len(out) < n; k-- {
			out = append(out, simBlock{us: (20 + int64(rng.Intn(400))) * 1_000_000, logo: 1, logoKnown: 1, plainStart: rng.Intn(3) == 0})
		}
		for k := 3 + rng.Intn(6); k > 0 && len(out) < n; k-- {
			b := simBlock{us: std[rng.Intn(len(std))]*1_000_000 + int64(rng.Intn(600_000)) - 300_000, logoKnown: 1}
			if rng.Intn(5) == 0 {
				b.us = (2 + int64(rng.Intn(9))) * 1_000_000
			}
			out = append(out, b)
		}
	}
	return out
}

// BenchmarkScoreAndSelect measures hypothesis scoring and unit selection
// over 4000 blocks (about nine thousand hypotheses, two thousand units).
func BenchmarkScoreAndSelect(b *testing.B) {
	cfg := DefaultConfig()
	cfg.Baselines.MinSupport, cfg.Baselines.MinRecording = 0, 0
	a := simulate(cfg, benchBlocks(4000))
	if len(a.units) < 500 || a.evaluated < 5_000 {
		b.Fatalf("unrepresentative input: %d units, %d hypotheses", len(a.units), a.evaluated)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.units, a.hyps, a.evaluated, a.truncated = a.units[:0], a.hyps[:0], 0, false
		for k := range a.blocks {
			blk := &a.blocks[k]
			blk.unit, blk.run, blk.program, blk.conflict = -1, -1, 0, false
		}
		a.scoreAndSelect()
	}
	b.ReportMetric(float64(a.evaluated), "hypotheses/op")
}

// BenchmarkScoreAndSelectDense is the worst case for hypothesis storage
// (R9): 4000 half-second blocks without a logo, split by corroborated
// audio/visual boundaries, so that nearly every grouping up to the maximum
// unit length is an eligible ad unit (about 970 thousand hypotheses).
func BenchmarkScoreAndSelectDense(b *testing.B) {
	cfg := DefaultConfig()
	cfg.Baselines.MinSupport, cfg.Baselines.MinRecording = 0, 0
	blocks := make([]simBlock, 4000)
	for i := range blocks {
		blocks[i] = simBlock{us: 500_000, logoKnown: 1, avStart: true}
	}
	a := simulate(cfg, blocks)
	if a.evaluated < 500_000 {
		b.Fatalf("unrepresentative input: %d hypotheses", a.evaluated)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.units, a.hyps, a.evaluated, a.truncated = a.units[:0], a.hyps[:0], 0, false
		for k := range a.blocks {
			blk := &a.blocks[k]
			blk.unit, blk.run, blk.program, blk.conflict = -1, -1, 0, false
		}
		a.scoreAndSelect()
	}
	b.ReportMetric(float64(a.evaluated), "hypotheses/op")
}

// BenchmarkAnalyze is the whole pipeline on a 150 s, 10 fps, 64x36
// synthetic recording (1500 frames, 7500 audio windows).
func BenchmarkAnalyze(b *testing.B) {
	src := logoScenario().source()
	cfg := DefaultConfig()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Analyze(context.Background(), src, cfg, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// The steady-state per-frame path performs no heap allocation once storage
// is reserved: a frame that is not a logo/aspect/bumper observation and
// raises no event only updates fixed-size state and the reserved frame
// table.
func TestExtractFrameSteadyStateDoesNotAllocate(t *testing.T) {
	x := benchExtractor(t, 600_000_000)
	pic := benchPictures()[0]
	// The first frame establishes the geometry and takes the first logo,
	// aspect and bumper observations.
	first := benchFrame(pic, 0)
	if err := x.video(&first); err != nil {
		t.Fatal(err)
	}
	pts := int64(0)
	f := benchFrame(pic, 0)
	f.Duration = 0
	allocs := testing.AllocsPerRun(2000, func() {
		// One microsecond apart: far inside every sampling interval.
		pts++
		f.PTS = pts
		if err := x.video(&f); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("steady-state frame ingestion allocates %v times per frame", allocs)
	}
	if len(x.frames) < 2000 {
		t.Fatalf("only %d frames were ingested", len(x.frames))
	}
	if n := len(x.logo.epochs[0].samples); n != 1 {
		t.Errorf("%d logo observations taken; the measured frames were meant to be non-sample frames", n)
	}
}

// At the real cadence, where observations and events do happen, allocation
// is amortized growth of compact storage only: far less than one allocation
// per frame.
func TestExtractFrameAmortizedAllocations(t *testing.T) {
	pics := benchPictures()
	const frames = 3600 // two minutes at 29.97 fps
	x := benchExtractor(t, frames*33_367)
	// Warm up: geometry, first observations, scene ring.
	for k := 0; k < 60; k++ {
		f := benchFrame(pics[0], int64(k)*33_367)
		if err := x.video(&f); err != nil {
			t.Fatal(err)
		}
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for k := 60; k < frames; k++ {
		f := benchFrame(pics[(k/60)%len(pics)], int64(k)*33_367)
		if err := x.video(&f); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	total := float64(after.Mallocs - before.Mallocs)
	perFrame := total / float64(frames-60)
	t.Logf("%.0f allocations over %d frames (%.4f per frame); %d logo observations, %d scene events", total, frames-60, perFrame, len(x.logo.epochs[0].samples), len(x.scene.events))
	if perFrame > 0.02 {
		t.Errorf("%.4f allocations per frame at the real cadence, want amortized growth only", perFrame)
	}
}

// Audio windows and caption data are ingested without per-event allocation
// once storage is reserved.
func TestAudioAndCaptionIngestDoesNotAllocate(t *testing.T) {
	x := benchExtractor(t, 600_000_000)
	first := benchFrame(benchPictures()[0], 0)
	if err := x.video(&first); err != nil {
		t.Fatal(err)
	}
	ws := make([]AudioWindow, 5)
	var at int64
	allocs := testing.AllocsPerRun(2000, func() {
		for i := range ws {
			ws[i] = AudioWindow{StartUS: at, DurationUS: 20_000, PeakMS: speechMS, MeanMS: speechMS}
			at += 20_000
		}
		x.audioWindows(ws)
	})
	if allocs != 0 {
		t.Errorf("audio window ingestion allocates %v times per event", allocs)
	}
	// Continuing roll-up captions extend the current runs in place.
	x.caps.feed(0, ccRU2)
	var pts int64
	allocs = testing.AllocsPerRun(2000, func() {
		pts += 33_367
		x.caps.feed(pts, ccRU2)
	})
	if allocs != 0 {
		t.Errorf("caption ingestion allocates %v times per frame", allocs)
	}
}
