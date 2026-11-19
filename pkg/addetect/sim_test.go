// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

// White-box scoring harness (TEST-01: "scoring tests SHALL inject controlled
// observations and check factor arithmetic, rule eligibility, and reason
// codes"). A sim is a list of blocks with exact microsecond durations and
// directly specified observations; the real aggregation, baseline, scoring
// and break-assembly code runs on it.

type simBlock struct {
	us int64 // duration in microseconds

	// Boundary at the start of this block (ignored for the first block):
	// dark/uniform evidence by default.
	plainStart bool // no evidence family (e.g. a scene cut or size change)
	avStart    bool // corroborated audio/visual boundary instead of dark

	logo      float64 // share of the known logo time with the logo present
	logoKnown float64 // share of the block with a known logo state; 0 = no logo observations

	mean, std float64 // luminance statistics (default 100 / 20)
	scenes    int

	audioMS    float32 // mean-square level (default speech); <0 = no audio
	silentFrac float64 // share of the audio below the silence level

	cap     CaptionStyle // caption style over capFrac of the block
	capFrac float64      // default 1 when cap is set

	aspect     float64 // active ratio over aspectFrac of the block
	aspectFrac float64 // default 1 when aspect is set

	gap bool // missing media coverage
}

func d(dur time.Duration) int64 { return us(dur) }

type simOpt func(a *analysis)

// simulate builds the post-extraction state and runs the pipeline from
// baselines to break assembly.
func simulate(cfg *Config, blocks []simBlock, opts ...simOpt) *analysis {
	if cfg == nil {
		cfg = simConfig()
	}
	a := &analysis{cfg: cfg}
	a.epochs = []geomEpoch{{codedW: 64, codedH: 36, aw: 64, ah: 36, scale: 1, sarN: 1, sarD: 1}}
	thr := float32(math.Pow(10, cfg.Audio.SilenceLevel/10))
	var t int64
	a.bounds = append(a.bounds, boundary{t: 0, edge: true})
	for i, b := range blocks {
		s, e := t, t+b.us
		if i > 0 {
			a.bounds = append(a.bounds, boundary{t: s, dark: !b.plainStart && !b.avStart, av: b.avStart})
		}
		t = e
		if b.gap {
			a.gaps = append(a.gaps, TimeRange{StartUS: s, EndUS: e, Reason: "timestamp_gap"})
			if n := len(a.frames); n > 0 {
				a.frames[n-1].flags |= fGapAfter
			}
			continue
		}
		mean, std := b.mean, b.std
		if mean == 0 {
			mean = 100
		}
		if std == 0 {
			std = 20
		}
		a.frames = append(a.frames, frameRec{pts: s, mean: uint16(math.Round(mean * 256)), std: uint16(math.Round(std * 256))})
		if b.logoKnown > 0 {
			a.logoUsable = true
			known := int64(math.Round(b.logoKnown * float64(b.us)))
			present := int64(math.Round(b.logo * float64(known)))
			if present > 0 {
				a.logo = append(a.logo, logoSpan{start: s, end: s + present, state: logoPresent})
			}
			if known > present {
				a.logo = append(a.logo, logoSpan{start: s + present, end: s + known, state: logoAbsent})
			}
			if known < b.us {
				a.logo = append(a.logo, logoSpan{start: s + known, end: e, state: logoUnknown})
			}
		}
		for k := 0; k < b.scenes; k++ {
			a.scene = append(a.scene, SceneEvent{TimeUS: s + int64(k+1)*b.us/int64(b.scenes+1), Kind: "cut"})
		}
		if b.audioMS >= 0 {
			level := b.audioMS
			if level == 0 {
				level = speechMS
			}
			silent := int64(math.Round(b.silentFrac * float64(b.us)))
			// AudioWindow durations are int32: split long spans.
			emit := func(from, to int64, peak, mean float32) {
				for from < to {
					n := min(to-from, 1_000_000_000)
					a.audio = append(a.audio, AudioWindow{StartUS: from, DurationUS: int32(n), PeakMS: peak, MeanMS: mean})
					a.audioKnown += n
					from += n
				}
			}
			emit(s, s+silent, thr/10, thr/10)
			emit(s+silent, e, level, level)
		}
		if b.cap != CaptionUnknown {
			f := b.capFrac
			if f == 0 {
				f = 1
			}
			a.caps = append(a.caps, capSpan{start: s, end: s + int64(math.Round(f*float64(b.us))), style: b.cap})
		}
		if b.aspect > 0 {
			f := b.aspectFrac
			if f == 0 {
				f = 1
			}
			a.aspect = append(a.aspect, aspectSpan{start: s, end: s + int64(math.Round(f*float64(b.us))), ratio: b.aspect})
		}
	}
	a.bounds = append(a.bounds, boundary{t: t, edge: true})
	a.dur, a.lastEnd, a.frameUS = t, t, 40_000
	if n := len(blocks); n > 0 && blocks[n-1].gap {
		a.lastEnd = t - blocks[n-1].us
	}
	for _, o := range opts {
		o(a)
	}
	a.programBaselines(len(a.caps) > 0)
	a.buildBlocks()
	a.recordingBaselines()
	a.scoreAndSelect()
	a.assembleBreaks()
	return a
}

// simConfig: defaults with every hypothesis retained and baselines that need
// no minimum support, so small sims have reliable baselines.
func simConfig() *Config {
	cfg := DefaultConfig()
	cfg.Outputs.Diagnostics = "full"
	cfg.Baselines.MinSupport = 0
	cfg.Baselines.MinRecording = 0
	return cfg
}

// labels renders one letter per block: A(d), P(rogram), U(nknown).
func (a *analysis) labels() string {
	var sb strings.Builder
	for i := range a.blocks {
		sb.WriteByte("UPA"[a.blocks[i].label])
	}
	return sb.String()
}

func (a *analysis) ruleNames(i int) string {
	b := &a.blocks[i]
	names := make([]string, b.nf)
	for k := 0; k < int(b.nf); k++ {
		names[k] = ruleNames[b.factors[k].rule]
	}
	return strings.Join(names, ",")
}

// hyp returns the retained hypothesis over blocks i..j.
func (a *analysis) hyp(t *testing.T, i, j int) *hypEval {
	t.Helper()
	for k := range a.hyps {
		if int(a.hyps[k].i) == i && int(a.hyps[k].j) == j {
			return &a.hyps[k]
		}
	}
	t.Fatalf("hypothesis %d..%d was not evaluated", i, j)
	return nil
}

func (h *hypEval) rules() string {
	names := make([]string, h.nf)
	for k := 0; k < int(h.nf); k++ {
		names[k] = ruleNames[h.factors[k].rule]
	}
	return strings.Join(names, ",")
}

func (h *hypEval) rejects() string { return strings.Join(bitNames(h.reject, rejNames[:]), ",") }
func (h *hypEval) families() string {
	return strings.Join(bitNames(h.fam, famNames[:]), ",")
}

// unitSpans renders the selected units as block index ranges: "1-1 2-3".
func (a *analysis) unitSpans() string {
	parts := make([]string, len(a.units))
	for i, u := range a.units {
		parts[i] = strconv.Itoa(int(u.h.i)) + "-" + strconv.Itoa(int(u.h.j))
	}
	return strings.Join(parts, " ")
}

// prog is a block with positive program evidence (validated logo present).
func prog(dur time.Duration) simBlock { return simBlock{us: d(dur), logo: 1, logoKnown: 1} }

// noLogo is a block where the validated logo is absent.
func noLogo(dur time.Duration) simBlock { return simBlock{us: d(dur), logo: 0, logoKnown: 1} }

// plain is a block without any logo observation.
func plain(dur time.Duration) simBlock { return simBlock{us: d(dur)} }
