// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Features are the compact measurements of one recording, taken after
// extraction and before classification (PIPE-03): the normalized timeline,
// every detector's events and per-frame and per-window statistics, the media
// description and the extraction warnings. They hold no pictures and no
// audio samples.
//
// Classifying Features again with AnalyzeFeatures gives the same result as
// a direct analysis with the same settings, without decoding the recording.
// Only settings that do not affect measurements may differ: scoring,
// boundaries, baselines, breaks, dark/uniform detection and outputs. A
// different feature-affecting setting (an input, package, sampling, signal,
// logo, scene, aspect, caption, audio or bumper key, or an asset) is refused
// by name.
//
// Features are obtained from Options.OnFeatures or ReadFeatures, and
// written with WriteTo. A Features value is not modified by AnalyzeFeatures
// and may be used concurrently.
type Features struct {
	identity   string
	keys       map[string]string // feature-affecting keys as the identity hashes them
	info       MediaInfo
	warnings   []Warning
	incomplete bool
	m          measured
}

// Identity is the feature identity of the measurements: the
// feature_identity of every result classified from them.
func (f *Features) Identity() string { return f.identity }

// DurationUS is the analyzed duration of the recording.
func (f *Features) DurationUS() int64 { return f.m.dur }

// Frames is the number of video frames measured.
func (f *Features) Frames() int { return len(f.m.frames) }

// newFeatures takes a deep copy of the measurements, so classification
// cannot change what was handed out.
func newFeatures(m *measured, cfg *Config, assets *Assets, info *MediaInfo, warnings []Warning, incomplete bool) *Features {
	ci := cfg.identity(assets.digests)
	f := &Features{
		identity:   featureIdentity(info, &ci),
		keys:       featureValues(&ci),
		info:       cloneMediaInfo(info),
		warnings:   cloneWarnings(warnings),
		incomplete: incomplete,
		m:          m.clone(),
	}
	return f
}

// featureValues returns the feature-affecting keys with the values the
// identity hashes (asset content digests, not file names).
func featureValues(ci *ConfigIdentity) map[string]string {
	keys := featureKeys(ci.Effective)
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = ci.hashed[k]
	}
	return out
}

// clone copies every slice of the measurements that classification may
// reorder or rewrite.
func (m *measured) clone() measured {
	c := *m
	c.frames = slices.Clone(m.frames)
	c.gaps = slices.Clone(m.gaps)
	c.scene = slices.Clone(m.scene)
	c.resolution = slices.Clone(m.resolution)
	c.aspect = slices.Clone(m.aspect)
	// Logo observations are only read by classification.
	c.logoObs = slices.Clone(m.logoObs)
	c.caps = slices.Clone(m.caps)
	c.phraseHits = slices.Clone(m.phraseHits)
	c.bumpers = slices.Clone(m.bumpers)
	c.audio = slices.Clone(m.audio)
	c.epochs = slices.Clone(m.epochs)
	c.regStats = slices.Clone(m.regStats)
	c.rebases = slices.Clone(m.rebases)
	return c
}

func cloneWarnings(ws []Warning) []Warning {
	out := make([]Warning, len(ws))
	for i, w := range ws {
		out[i] = w
		if w.TimeUS != nil {
			out[i].TimeUS = ptr(*w.TimeUS)
		}
	}
	return out
}

// cloneMediaInfo copies the media description deeply enough that the
// result builder (which copies package resources before shifting them) can
// never alias the stored one.
func cloneMediaInfo(info *MediaInfo) MediaInfo {
	c := *info
	if info.Package != nil {
		pk := *info.Package
		pk.Manifests = slices.Clone(pk.Manifests)
		pk.Epochs = slices.Clone(pk.Epochs)
		pk.Inits = slices.Clone(pk.Inits)
		pk.Resources = slices.Clone(pk.Resources)
		pk.Markers = slices.Clone(pk.Markers)
		pk.Notes = slices.Clone(pk.Notes)
		c.Package = &pk
	}
	return c
}

// AnalyzeFeatures classifies saved measurements under cfg and returns the
// canonical result, exactly as Analyze would for the same recording and
// settings (PIPE-03). It fails with KindInvalidConfig, naming the keys, when
// cfg differs from the measurement settings in any feature-affecting key or
// asset. opts.Progress receives the classification and done stages;
// opts.OnLogoLearned receives each accepted learned logo, as in Analyze;
// opts.FrameObserver and opts.OnFeatures are not called.
func AnalyzeFeatures(ctx context.Context, f *Features, cfg *Config, opts *Options) (res *Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, &Error{Kind: KindInternal, Msg: fmt.Sprint("internal failure: ", r)}
		}
	}()
	if f == nil {
		return nil, Errorf(KindInvalidConfig, "no features")
	}
	if cfg == nil {
		cfg = DefaultConfig()
	}
	cfg = cfg.Clone()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	assets, err := LoadAssets(cfg)
	if err != nil {
		return nil, err
	}
	if opts == nil {
		opts = &Options{}
	}
	ci := cfg.identity(assets.digests)
	if diff := diffFeatureKeys(f.keys, featureValues(&ci)); len(diff) > 0 {
		return nil, Errorf(KindInvalidConfig, "the configuration differs from the one the features were measured with in feature-affecting keys: %s (only scoring, boundaries, baselines, breaks, dark/uniform detection and outputs may change)", strings.Join(diff, ", "))
	}
	if err := ctx.Err(); err != nil {
		return nil, wrapCtx(err)
	}
	info := cloneMediaInfo(&f.info)
	a := &analysis{cfg: cfg, measured: f.m.clone()}
	a.stop = func() bool { return ctx.Err() != nil }
	if opts.Progress != nil {
		opts.Progress(Progress{Stage: StageClassify, MediaTimeUS: a.lastPTS - a.firstPTS, DurationHintUS: info.DurationHintUS, Frames: len(a.frames)})
	}
	return a.decide(ctx, &info, assets, cloneWarnings(f.warnings), opts, nil, f.incomplete)
}

// diffFeatureKeys lists, sorted, the keys whose values differ or that only
// one side has.
func diffFeatureKeys(saved, now map[string]string) []string {
	var out []string
	for k, v := range saved {
		if w, ok := now[k]; !ok || w != v {
			out = append(out, k)
		}
	}
	for k := range now {
		if _, ok := saved[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
