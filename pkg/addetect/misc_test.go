// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// Remaining core behaviours: package mapping pass-through, logo candidate
// selection, program evidence details and storage reservation.

// PACK-09, TIME-02: resource and epoch times recorded by a package source
// on the media timeline are normalized once with the rest of the result;
// the source's own description is not modified.
func TestPackageInfoIsNormalized(t *testing.T) {
	s := newScenario(blk{D: 47 * sec}, blk{D: 30 * sec}, blk{D: 53 * sec})
	s.startPTS = 10_000_000
	src := s.source()
	p := func(v int64) *int64 { return &v }
	pkg := &PackageInfo{
		Format: "hls", Identity: "sha256:abc",
		Manifests: []ManifestSnapshot{{URI: "master.m3u8", Size: 100, SHA256: "00"}},
		Video:     PackageTrack{ID: "v720", Rule: "highest"},
		Epochs:    []PackageEpoch{{ID: "e0", MediaOffsetUS: p(0)}},
		Resources: []PackageResource{
			{Index: 0, Track: "video", URI: "seg0.m4s", Length: -1, Init: 0, StartUS: p(10_000_000), EndUS: p(75_000_000), Status: "ok"},
			{Index: 1, Track: "video", URI: "seg1.m4s", Length: -1, Init: 0, StartUS: p(75_000_000), EndUS: p(140_000_000), Status: "ok"},
			{Index: 2, Track: "video", URI: "seg2.m4s", Length: -1, Init: 0, Status: "unread"},
		},
	}
	src.Media = MediaInfo{Input: InputInfo{Kind: "hls", Path: "master.m3u8"}, Package: pkg}
	before := mustJSON(t, pkg)
	r := analyze(t, src, testConfig())
	if r.Package == nil || r.Package.Format != "hls" || len(r.Package.Resources) != 3 {
		t.Fatalf("package = %+v", r.Package)
	}
	res := r.Package.Resources
	if *res[0].StartUS != 0 || *res[0].EndUS != 65_000_000 || *res[1].StartUS != 65_000_000 || *res[1].EndUS != 130_000_000 {
		t.Errorf("resource times not normalized: %s", mustJSON(t, res))
	}
	if res[2].StartUS != nil || res[2].EndUS != nil {
		t.Errorf("an unread resource got times: %s", mustJSON(t, res[2]))
	}
	if *r.Package.Epochs[0].MediaOffsetUS != -10_000_000 {
		t.Errorf("epoch media offset = %d", *r.Package.Epochs[0].MediaOffsetUS)
	}
	if after := mustJSON(t, pkg); after != before {
		t.Errorf("the source's package description was modified")
	}
	// PACK-07: resource boundaries are not content cuts or evidence: the
	// result is that of the same recording without package information.
	plain := s.run(t, nil)
	if timeline(r) != timeline(plain) || mustJSON(t, boundaryTimes(r)) != mustJSON(t, boundaryTimes(plain)) {
		t.Errorf("package information changed the detection: %s vs %s", timeline(r), timeline(plain))
	}
	// The package identity is part of the feature identity (PACK-09).
	pkg.Identity = "sha256:def"
	r2 := analyze(t, src, testConfig())
	if r2.FeatureID == r.FeatureID {
		t.Errorf("package identity is not part of the feature identity")
	}
}

// MEDIA-05: interlaced input is an explicit degraded-mode warning.
func TestInterlacedWarning(t *testing.T) {
	evs := frameSeq(rep(content, 20)...)
	for i := 0; i < 5; i++ {
		evs[i].Video.Interlaced = true
	}
	r := analyze(t, &SliceSource{Events: evs}, testConfig())
	if !hasWarning(r, "interlaced_video") {
		t.Errorf("warnings = %+v", r.Warnings)
	}
	r = analyze(t, &SliceSource{Events: frameSeq(rep(content, 20)...)}, testConfig())
	if hasWarning(r, "interlaced_video") || len(r.Warnings) != 0 {
		t.Errorf("progressive input warned: %+v", r.Warnings)
	}
}

// LOGO-01, LOGO-04: learned candidates are validated best first. A graphic
// that never leaves the screen (a "live" bug) is the most persistent
// candidate but fails validation; the station logo, which does leave during
// ads, is then the one used.
func TestLogoSecondCandidateIsUsed(t *testing.T) {
	s := newScenario(
		blk{D: 50 * sec, Logo: true, Bug: true},
		blk{D: 30 * sec, Bug: true},
		blk{D: 70 * sec, Logo: true, Bug: true},
	)
	r := s.run(t, nil)
	c := r.Calibration.Logo[0]
	if c.Status != StatusUsable || c.Occupancy == nil || *c.Occupancy < 0.7 || *c.Occupancy > 0.9 {
		t.Fatalf("calibration = %+v", c)
	}
	// The accepted box is the station logo on the right, not the bug on the
	// left.
	if c.Box.X < 0.5 {
		t.Errorf("accepted logo box at x=%v: the always-present graphic was used", c.Box.X)
	}
	wantTimeline(t, r, "P0-50 A50-80 P80-150")

	// With only the permanent graphic there is no usable logo, and the
	// reported failure is that of the best candidate.
	s = newScenario(blk{D: 47 * sec, Bug: true}, blk{D: 30 * sec, Bug: true}, blk{D: 73 * sec, Bug: true})
	r = s.run(t, nil)
	c = r.Calibration.Logo[0]
	if c.Status != StatusUnreliable || c.Failed != "occupancy_above_maximum" || c.Box == nil || c.Box.X > 0.5 {
		t.Errorf("calibration = %+v", c)
	}
}

// SCORE-07: inside a long stable run, a block whose net evidence leans
// toward an ad (an ad-raising factor and a score of at least 1) is not
// swept into the program label.
func TestStableRunSkipsBlocksWithAdEvidence(t *testing.T) {
	cfg := simConfig()
	cfg.Scoring.LogoAbsentFactor = 1.2 // below the threshold on its own
	cfg.Scoring.CaptionMatchFactor = 1 // nothing offsets the ad-raising factor
	st := func(us int64, logoKnown float64) simBlock {
		return simBlock{us: us, cap: CaptionRollUp, aspect: 1.78, logoKnown: logoKnown}
	}
	a := simulate(cfg, []simBlock{st(d(130*sec), 0), st(d(47*sec), 1), st(d(53*sec), 0)})
	if a.labels() != "PUP" {
		t.Fatalf("labels = %s", a.labels())
	}
	if a.blocks[1].score != 1.2 || a.ruleNames(1) != "logo_absent" {
		t.Errorf("middle block: score %v rules %s", a.blocks[1].score, a.ruleNames(1))
	}
	if got := reasonList(a.blocks[2].reasons); len(got) != 1 || got[0] != "stable_long_interval" {
		t.Errorf("reasons = %v", got)
	}
	// With the default caption match factor the same block nets below 1
	// (1.2 x 0.4): its evidence leans program and it joins the stable run.
	cfg.Scoring.CaptionMatchFactor = 0.4
	a = simulate(cfg, []simBlock{st(d(130*sec), 0), st(d(47*sec), 1), st(d(53*sec), 0)})
	if a.labels() != "PPP" || a.blocks[1].score != 1.2*0.4 || a.ruleNames(1) != "logo_absent,caption_style_match" {
		t.Errorf("net program evidence: labels %s score %v rules %s", a.labels(), a.blocks[1].score, a.ruleNames(1))
	}
	// Exactly 1 still leans ad: excluded.
	cfg.Scoring.LogoAbsentFactor, cfg.Scoring.CaptionMatchFactor = 1.25, 0.8
	a = simulate(cfg, []simBlock{st(d(130*sec), 0), st(d(47*sec), 1), st(d(53*sec), 0)})
	if a.labels() != "PUP" || a.blocks[1].score != 1 {
		t.Errorf("score exactly 1: labels %s score %v", a.labels(), a.blocks[1].score)
	}
}

// YAML anchors and aliases resolve like the values they refer to.
func TestConfigYAMLAlias(t *testing.T) {
	c := DefaultConfig()
	err := loadYAML(t, c, "audio:\n  min_silence: &d 200ms\n  alignment: *d\nscoring:\n  standard_durations: &l [7s, 9s]\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Audio.MinSilence != 200*ms || c.Audio.Alignment != 200*ms || !reflect.DeepEqual(c.Scoring.StandardDurations, []time.Duration{7 * sec, 9 * sec}) {
		t.Errorf("audio %+v durations %v", c.Audio, c.Scoring.StandardDurations)
	}
}

// QUAL-02: storage reserved from the source's hints is bounded by the
// memory budget and tolerates useless hints.
func TestReserveIsBounded(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Resources.MemoryBudget = 16 << 20
	x := newExtractor(cfg, &Assets{}, func(string, string, int64, bool) {})
	x.reserve(1_000*3600*1_000_000, 240) // a thousand hours at 240 fps
	if got, limit := int64(cap(x.frames))*16, int64(cfg.Resources.MemoryBudget)/4; got > limit {
		t.Errorf("reserved %d bytes of frame storage, limit %d", got, limit)
	}
	if got, limit := int64(cap(x.audio))*24, int64(cfg.Resources.MemoryBudget)/4; got > limit {
		t.Errorf("reserved %d bytes of audio storage, limit %d", got, limit)
	}
	x = newExtractor(cfg, &Assets{}, func(string, string, int64, bool) {})
	x.reserve(0, 30)
	x.reserve(-5, 0)
	if cap(x.frames) != 0 {
		t.Errorf("reserved %d frames without a duration hint", cap(x.frames))
	}
	x.reserve(10_000_000, -1) // unknown frame rate: 30 fps is assumed
	if c := cap(x.frames); c < 300 || c > 400 {
		t.Errorf("reserved %d frames for 10 s", c)
	}
	// The hints reach the extractor through MediaInfo and do not change the
	// result.
	src := newScenario(blk{D: 47 * sec}, blk{D: 30 * sec}, blk{D: 53 * sec}).source()
	base := mustJSON(t, analyze(t, src, testConfig()))
	src.Media.DurationHintUS, src.Media.FrameRateHint = 130_000_000, 10
	if got := mustJSON(t, analyze(t, src, testConfig())); got != base {
		t.Errorf("duration/frame-rate hints changed the result")
	}
	src.Media.DurationHintUS = 5_000_000 // a wrong hint is only a hint
	if got := mustJSON(t, analyze(t, src, testConfig())); got != base {
		t.Errorf("a wrong duration hint changed the result")
	}
	var last Progress
	if _, err := Analyze(context.Background(), src, testConfig(), &Options{Progress: func(p Progress) { last = p }}); err != nil {
		t.Fatal(err)
	}
	if last.Stage != StageDone || last.DurationHintUS != 5_000_000 || last.MediaTimeUS != 130_000_000 || last.Frames != 1300 {
		t.Errorf("final progress = %+v", last)
	}
}
