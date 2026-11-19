// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
)

// Progress stages (API-05).
const (
	StageExtract  = "feature_extraction"
	StageClassify = "classification"
	StageDone     = "done"
)

// Progress reports analysis progress in observed media time.
type Progress struct {
	Stage string
	// MediaTimeUS is the normalized media time reached by extraction.
	MediaTimeUS int64
	// DurationHintUS is the source's duration estimate, 0 if unknown.
	DurationHintUS int64
	Frames         int
}

// FrameMeasurement is one frame's raw measurement, streamed to
// Options.FrameObserver so bulk per-frame diagnostics never accumulate in
// memory (API-02).
type FrameMeasurement struct {
	TimeUS         int64 // relative to the first frame
	Brightness     float64
	Nonuniformity  float64
	BrightFraction float64
	CodedWidth     int
	CodedHeight    int
}

// Options are optional per-call hooks. Callbacks run on the calling
// goroutine and must not block for long.
type Options struct {
	// Progress receives stage/media-time updates (about once per second of
	// media during extraction).
	Progress func(Progress)
	// FrameObserver receives every frame's raw measurements.
	FrameObserver func(FrameMeasurement)
	// OnLogoLearned receives each validated learned logo as an exportable
	// asset. Nothing is written to disk by the library (BUMPER-03).
	OnLogoLearned func(*LogoAsset)
	// Build is an optional build identity recorded in the result.
	Build string
	// OnFeatures receives the recording's measurements once extraction has
	// succeeded, before classification. Saved with Features.WriteTo, they
	// can be classified again with AnalyzeFeatures under other scoring,
	// boundary, baseline and break settings, without decoding (PIPE-03).
	OnFeatures func(*Features)
}

// Analyze runs the complete pipeline over one source and returns the
// canonical result (API-01).
//
// On failure the error is an *Error. When some of the recording was
// analyzed before the failure, a partial Result with Complete == false and
// Error set is returned together with the error; callers must not treat it
// as a completed analysis (API-04).
//
// Analyze does not modify cfg, never writes to stdout/stderr and converts
// internal panics into errors. Independent calls share no mutable state.
func Analyze(ctx context.Context, src Source, cfg *Config, opts *Options) (res *Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, &Error{Kind: KindInternal, Msg: fmt.Sprint("internal failure: ", r)}
		}
	}()
	if src == nil {
		return nil, Errorf(KindInvalidConfig, "no input source")
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

	warnings := append([]Warning(nil), assets.notes...)
	seenWarn := make(map[string]struct{})
	warn := func(code, msg string, t int64, hasT bool) {
		key := code + "\x00" + msg
		if _, dup := seenWarn[key]; dup || len(warnings) >= maxWarnings {
			return
		}
		seenWarn[key] = struct{}{}
		w := Warning{Code: code, Message: msg}
		if hasT {
			w.TimeUS = ptr(t) // media time; normalized when the result is built
		}
		warnings = append(warnings, w)
	}

	req := openRequestFor(cfg, func(w Warning) {
		if w.TimeUS != nil {
			warn(w.Code, w.Message, *w.TimeUS, true)
		} else {
			warn(w.Code, w.Message, 0, false)
		}
	})
	if err := ctx.Err(); err != nil {
		return nil, wrapCtx(err)
	}
	stream, err := src.Open(ctx, req)
	if err != nil {
		return nil, wrapCtx(err)
	}
	defer stream.Close()
	info := stream.Info()
	if info == nil {
		return nil, Errorf(KindInternal, "source returned no media information")
	}

	x := newExtractor(cfg, assets, warn)
	x.captionSource(info.Tracks.Captions.Source)
	x.reserve(info.DurationHintUS, info.FrameRateHint)
	var (
		ev         Event
		runErr     error
		incomplete bool
		firstPTS   int64
		nextProg   int64
		events     int
	)
	for {
		ev.Kind = 0
		if e := stream.Next(&ev); e != nil {
			if e != io.EOF {
				runErr = wrapCtx(e)
			}
			break
		}
		if events++; events&cancelCheckMask == 0 {
			if e := ctx.Err(); e != nil {
				runErr = wrapCtx(e)
				break
			}
		}
		switch ev.Kind {
		case EventVideo:
			before := len(x.frames)
			if runErr = x.video(&ev.Video); runErr != nil {
				break
			}
			if len(x.frames) == before {
				continue
			}
			f := &x.frames[before]
			if before == 0 {
				firstPTS = f.pts
			}
			if opts.FrameObserver != nil {
				ep := &x.epochs[len(x.epochs)-1]
				opts.FrameObserver(FrameMeasurement{
					TimeUS: f.pts - firstPTS, Brightness: f.meanLuma(), Nonuniformity: f.stdLuma(),
					BrightFraction: f.brightFrac(), CodedWidth: ep.codedW, CodedHeight: ep.codedH,
				})
			}
			if opts.Progress != nil && f.pts >= nextProg {
				nextProg = f.pts + progressIntervalUS
				opts.Progress(Progress{Stage: StageExtract, MediaTimeUS: f.pts - firstPTS, DurationHintUS: info.DurationHintUS, Frames: before + 1})
			}
		case EventAudio:
			runErr = x.audioWindows(ev.Audio)
		case EventCaptionData:
			if cfg.Signals.Captions && x.trackCC && len(ev.Caption.CC) > 0 {
				x.caps.feed(ev.Caption.PTS+x.offset, ev.Caption.CC)
			}
		case EventCaptionStyle:
			if cfg.Signals.Captions && x.trackCC {
				x.caps.style(ev.Caption.PTS+x.offset, ev.Caption.EndPTS+x.offset, ev.Caption.Style)
			}
		case EventGap:
			incomplete = incomplete || !ev.Gap.Declared
			x.declared = append(x.declared, TimeRange{StartUS: ev.Gap.StartUS + x.offset, EndUS: ev.Gap.EndUS + x.offset, Reason: ev.Gap.Reason})
		case EventDecodeError:
			x.decodeErrs++
			warn(WarnDecodeError, "recoverable decode failure: "+ev.DecodeError, 0, false)
		}
		if runErr != nil {
			break
		}
	}
	if runErr == nil {
		runErr = x.checkBudget()
	}
	if runErr == nil {
		if n := len(x.frames); n > 0 && float64(x.decodeErrs) > cfg.Input.MaxDecodeErrorRatio*float64(n+x.decodeErrs) {
			runErr = Errorf(KindMedia, "%d decode failures against %d decoded frames exceed input.max_decode_error_ratio", x.decodeErrs, n)
		}
	}
	if len(x.frames) == 0 {
		if runErr != nil {
			return nil, runErr
		}
		return nil, Errorf(KindMedia, "the selected video track produced no decodable frames")
	}
	if opts.Progress != nil {
		opts.Progress(Progress{Stage: StageClassify, MediaTimeUS: x.lastPTS - firstPTS, DurationHintUS: info.DurationHintUS, Frames: len(x.frames)})
	}

	a := &analysis{cfg: cfg, x: x}
	// Cancellation is honored through classification as well (API-04). After
	// a failed extraction the remaining stages run to the end, so that the
	// partial result can still be reported.
	if runErr == nil {
		a.stop = func() bool { return ctx.Err() != nil }
	}
	if err := a.buildTimeline(info.DurationHintUS); err != nil {
		if runErr != nil {
			return nil, runErr
		}
		return nil, err
	}
	if cfg.Signals.Logo {
		a.logoObs = x.logo.measure(a.dur + a.t0)
	}
	// Measurement ends here. The extractor, with its raw logo observations,
	// is released before classification.
	a.takeExtraction(x, firstPTS)
	a.x = nil
	incomplete = incomplete || info.Incomplete
	if runErr == nil && opts.OnFeatures != nil {
		opts.OnFeatures(newFeatures(&a.measured, cfg, assets, info, warnings, incomplete))
	}
	return a.decide(ctx, info, assets, warnings, opts, runErr, incomplete)
}

// openRequestFor is the stream request of a configuration.
func openRequestFor(cfg *Config, warn func(Warning)) *OpenRequest {
	return &OpenRequest{
		VideoTrack: cfg.Input.VideoTrack, AudioTrack: cfg.Input.AudioTrack, CaptionTrack: cfg.Input.CaptionTrack,
		Program: cfg.Input.Program, HardwareDecode: cfg.Input.HardwareDecode == HardwareDecodeAuto,
		NeedAudio: cfg.Signals.Audio, NeedCaptions: cfg.Signals.Captions,
		AnalysisMaxWidth: cfg.Sampling.AnalysisMaxWidth, AnalysisMaxHeight: cfg.Sampling.AnalysisMaxHeight,
		BrightPixelLuma: cfg.Dark.BrightPixelLuma, FullResBrightCheck: cfg.Sampling.FullResBrightCheck,
		AudioWindow: cfg.Audio.Window, ExcludeLFE: cfg.Audio.ChannelPolicy == ChannelsExcludeLFE,
		Threads: cfg.workers(), SkipLoopFilter: cfg.Input.SkipLoopFilter, DecodeFrames: cfg.Sampling.DecodeFrames,
		FailOnUnsupportedFormat: cfg.Input.UnsupportedFormat == FormatPolicyFail,
		StrictOptionalStreams:   cfg.Input.OptionalStreams == StreamPolicyStrict,
		Package: PackageRequest{
			ManifestType: cfg.Package.ManifestType, VideoSelection: cfg.Package.VideoSelection,
			AudioSelection: cfg.Package.AudioSelection, MinHeight: cfg.Package.MinHeight,
			Degraded: cfg.Package.FailurePolicy == FailurePolicyDegraded, AllowRemote: cfg.Package.AllowRemote,
			MaxManifestBytes: int64(cfg.Package.MaxManifestBytes), MaxSegments: cfg.Package.MaxSegments,
			IOBufferSize: int(cfg.Package.IOBufferSize), HashMedia: cfg.Package.HashMedia,
		},
		Warn: warn,
	}
}

// decide runs classification over the measurements and assembles the
// result. A direct analysis and a feature-file replay share it, so equal
// measurements and settings give byte-identical results (PIPE-03).
func (a *analysis) decide(ctx context.Context, info *MediaInfo, assets *Assets, warnings []Warning, opts *Options, runErr error, incomplete bool) (*Result, error) {
	cfg := a.cfg
	canceled := func() bool { return a.stopped || (a.stop != nil && a.stop()) }
	capUsable := false
	stages := [...]func(){
		func() { a.decideLogo(opts.OnLogoLearned) },
		a.detectDark,
		a.detectSilence,
		func() {
			capUsable = cfg.Signals.Captions && len(a.caps) > 0
			a.programBaselines(capUsable)
		},
		func() { a.buildBoundaries(capUsable) },
		a.buildBlocks,
		a.checkClassificationBudget,
		a.recordingBaselines,
		a.scoreAndSelect,
		a.assembleBreaks,
	}
	for _, stage := range stages {
		if canceled() {
			return nil, wrapCtx(ctx.Err())
		}
		stage()
		if a.err != nil {
			return nil, a.err
		}
	}
	if canceled() {
		return nil, wrapCtx(ctx.Err())
	}

	res := a.result(info, assets, warnings, opts)
	if canceled() {
		return nil, wrapCtx(ctx.Err())
	}
	res.Complete = runErr == nil && !incomplete
	if runErr != nil {
		res.Error = &ErrorInfo{Kind: KindOf(runErr).String(), Message: runErr.Error()}
	}
	if opts.Progress != nil && runErr == nil {
		opts.Progress(Progress{Stage: StageDone, MediaTimeUS: a.dur, DurationHintUS: info.DurationHintUS, Frames: len(a.frames)})
	}
	return res, runErr
}

func blockID(i int) string    { return idPrefixBlock + strconv.Itoa(i) }
func boundaryID(i int) string { return idPrefixBoundary + strconv.Itoa(i) }
func hypID(i int32) string    { return idPrefixHypothesis + strconv.Itoa(int(i)) }

func blockIDs(i, j int) []string {
	out := make([]string, 0, j-i+1)
	for k := i; k <= j; k++ {
		out = append(out, blockID(k))
	}
	return out
}

func (a *analysis) factorOut(f *factorRec) Factor {
	o := Factor{Family: ruleFamilies[f.rule], Rule: ruleNames[f.rule], Multiplier: f.mult}
	switch f.rule {
	case ruleStrict, ruleLoose, ruleCombined:
		o.TargetUS, o.MeasuredUS = ptr(f.target), ptr(f.measured)
		o.DeviationUS, o.ToleranceUS = ptr(f.measured-f.target), ptr(f.tol)
	case ruleAdPhrase, ruleProgramPhrase:
		o.Value = ptr(f.value)
	case ruleLongBlock:
		o.MeasuredUS, o.ToleranceUS = ptr(f.measured), ptr(f.tol)
	case ruleCaption, ruleCaptionMatch, ruleCaptionAbsent, ruleCaptionUnexpected:
		o.Coverage = ptr(f.coverage)
		o.Detail = CaptionStyle(f.value).String() + " vs program " + CaptionStyle(f.base).String()
	case ruleLogoPresent, ruleLogoAbsent:
		o.Value, o.Coverage = ptr(f.value), ptr(f.coverage)
	default:
		o.Value, o.Baseline, o.Coverage = ptr(f.value), ptr(f.base), ptr(f.coverage)
	}
	return o
}

func (a *analysis) factorsOut(fs []factorRec) []Factor {
	out := make([]Factor, len(fs))
	for i := range fs {
		out[i] = a.factorOut(&fs[i])
	}
	return out
}

func ratio(num, den int64) *float64 {
	if den <= 0 {
		return nil
	}
	return ptr(float64(num) / float64(den))
}

// featureIdentity digests what determines the measurements: feature schema,
// adapter, input (and package) identity and the feature-affecting settings
// with assets by content (PIPE-03).
func featureIdentity(info *MediaInfo, ci *ConfigIdentity) string {
	fh := sha256.New()
	fmt.Fprintf(fh, "features/%s\nadapter=%s/%s\ninput=%s|%s|%s\n", FeatureSchemaVersion, info.Adapter.Name, info.Adapter.Version, info.Input.Kind, info.Input.Path, info.Input.Identity)
	if info.Package != nil {
		fmt.Fprintf(fh, "package=%s\n", info.Package.Identity)
	}
	for _, k := range featureKeys(ci.Effective) {
		fmt.Fprintf(fh, "%s=%s\n", k, ci.hashed[k])
	}
	return DigestPrefix + hex.EncodeToString(fh.Sum(nil))
}

// result assembles the canonical output (section 12).
func (a *analysis) result(info *MediaInfo, assets *Assets, warnings []Warning, opts *Options) *Result {
	cfg := a.cfg
	detailed := cfg.Outputs.Diagnostics != DiagnosticsSummary

	res := &Result{
		SchemaVersion: SchemaVersion,
		Product:       ProductInfo{Name: ProductName, Version: Version, Build: opts.Build},
		TimeUnit:      TimeUnitMicroseconds,
		DurationUS:    a.dur,
		Input:         info.Input,
		Tracks:        info.Tracks,
		Adapter:       info.Adapter,
		Package:       info.Package,
		Config:        cfg.identity(assets.digests),
		Segments:      []Segment{},
		AdBreaks:      []AdBreak{},
		AdUnits:       []AdUnit{},
		SkipIntervals: []SkipInterval{},
	}
	if res.Input.Kind == "" {
		res.Input.Kind = InputKindObservations
	}
	if ep := a.epochs[0]; true {
		v := &res.Tracks.Video
		v.AnalysisWidth, v.AnalysisHeight, v.AnalysisScale = ep.aw, ep.ah, ep.scale
		if v.CodedWidth == 0 {
			v.CodedWidth, v.CodedHeight = ep.codedW, ep.codedH
		}
	}

	// Feature identity (PIPE-03): input identity, feature-affecting settings,
	// adapter identity and feature schema; score weights are excluded. Assets
	// enter by content digest, not by the file name the result displays, so
	// a changed image or logo under the same name changes the identity.
	res.FeatureID = featureIdentity(info, &res.Config)

	// Coverage and timing.
	var gapTotal int64
	gaps := make([]TimeRange, len(a.gaps))
	copy(gaps, a.gaps)
	for _, g := range gaps {
		gapTotal += g.EndUS - g.StartUS
	}
	res.Coverage = CoverageInfo{VideoUS: a.dur - gapTotal, Fraction: float64(a.dur-gapTotal) / float64(a.dur), Gaps: gaps}
	res.Timing = TimingInfo{
		NormalizationOffsetUS: a.t0, Frames: len(a.frames),
		FinalFrameDurationUS: a.finalUS, FinalFrameDurationEstimated: a.finalEstimated,
		Gaps: gaps, Resets: len(a.rebases), Overlaps: a.overlaps, MissingTimestamps: a.missingPTS,
		DecodeErrors: a.decodeErrs, CorruptFrames: a.corrupt, MedianFrameDurationUS: a.frameUS,
		DiscontinuityPolicy: "forward jumps beyond input.timestamp_gap_threshold are unknown coverage gaps; backward jumps follow input.timestamp_reset_policy=" + cfg.Input.TimestampResetPolicy + "; frames at or before the previous timestamp are dropped as overlaps; missing timestamps are interpolated",
	}
	for _, r := range a.rebases {
		res.Timing.Rebases = append(res.Timing.Rebases, Rebase{AtUS: r.AtUS - a.t0, OffsetUS: r.OffsetUS})
	}
	if a.haveAudio {
		res.Timing.AudioStartOffsetUS = ptr(a.firstAudio - a.t0)
	}
	if a.corrupt > 0 {
		warnings = append(warnings, Warning{Code: WarnCorruptFrames, Message: fmt.Sprintf("%d of %d frames were flagged corrupt by the decoder", a.corrupt, len(a.frames))})
	}
	if a.interlaced > 0 {
		warnings = append(warnings, Warning{Code: WarnInterlacedVideo, Message: fmt.Sprintf("%d of %d frames are interlaced; measurements use the woven frame without deinterlacing", a.interlaced, len(a.frames))})
	}
	if a.finalEstimated {
		warnings = append(warnings, Warning{Code: WarnFinalFrameEstimated, Message: "the last frame's duration was not declared; the median frame duration was used"})
	}
	if a.missingPTS > 0 {
		warnings = append(warnings, Warning{Code: WarnMissingTimestamps, Message: fmt.Sprintf("%d frames carried no timestamp and were interpolated", a.missingPTS)})
	}
	if a.overlaps > 0 {
		warnings = append(warnings, Warning{Code: WarnTimestampOverlaps, Message: fmt.Sprintf("%d frames repeated or preceded the previous presentation time and were dropped", a.overlaps)})
	}
	if len(a.gaps) > 0 {
		warnings = append(warnings, Warning{Code: WarnCoverageGaps, Message: fmt.Sprintf("%d intervals (%d us) have no video and are unknown", len(a.gaps), gapTotal)})
	}
	warnings = append(warnings, a.notes...)
	if a.truncated {
		warnings = append(warnings, Warning{Code: WarnHypothesesTruncated, Message: "diagnostics retained only outputs.max_hypotheses hypotheses"})
	}
	for i := range warnings {
		if warnings[i].TimeUS != nil {
			warnings[i].TimeUS = ptr(*warnings[i].TimeUS - a.t0)
		}
	}
	if warnings == nil {
		warnings = []Warning{}
	}
	res.Warnings = warnings

	if info.Package != nil {
		// Package resource times were recorded on the media timeline.
		shift := func(p *int64) *int64 {
			if p == nil {
				return nil
			}
			return ptr(*p - a.t0)
		}
		pk := *info.Package
		pk.Resources = append([]PackageResource(nil), pk.Resources...)
		for i := range pk.Resources {
			pk.Resources[i].StartUS, pk.Resources[i].EndUS = shift(pk.Resources[i].StartUS), shift(pk.Resources[i].EndUS)
		}
		pk.Epochs = append([]PackageEpoch(nil), pk.Epochs...)
		for i := range pk.Epochs {
			pk.Epochs[i].MediaOffsetUS = shift(pk.Epochs[i].MediaOffsetUS)
		}
		res.Package = &pk
	}

	res.Detectors = a.detectorStates(info)
	res.Calibration = Calibration{Dark: a.darkCal, Logo: a.logoCal, Silence: a.silenceCal, Score: ScoreCalibration{
		Threshold: cfg.Scoring.Threshold, Percentile: cfg.Scoring.ThresholdPercentile, Effective: a.threshold,
	}}
	if res.Calibration.Logo == nil {
		res.Calibration.Logo = []LogoCalibration{}
	}
	res.Baselines = a.base.out

	// Breaks and units.
	breakOf := make([]int, len(a.runs))
	for ri := range a.runs {
		r := &a.runs[ri]
		breakOf[ri] = -1
		if !r.accepted {
			continue
		}
		breakOf[ri] = len(res.AdBreaks)
		br := AdBreak{
			ID: idPrefixBreak + strconv.Itoa(len(res.AdBreaks)), StartUS: r.start, EndUS: r.end,
			TruncatedStart: r.truncStart, TruncatedEnd: r.truncEnd,
			BoundaryIDs: []string{boundaryID(r.firstBlock), boundaryID(r.lastBlock + 1)},
			BridgedGaps: r.bridged,
		}
		for u := r.firstUnit; u <= r.lastUnit; u++ {
			h := &a.units[u].h
			id := idPrefixUnit + strconv.Itoa(len(res.AdUnits))
			br.UnitIDs = append(br.UnitIDs, id)
			res.AdUnits = append(res.AdUnits, AdUnit{
				ID: id, BreakID: br.ID, StartUS: a.blocks[h.i].start, EndUS: a.blocks[h.j].end,
				BlockIDs: blockIDs(int(h.i), int(h.j)), HypothesisID: hypID(h.id), Score: h.score,
				Factors: a.factorsOut(h.factors[:h.nf]), EvidenceFamilies: bitNames(h.fam, famNames[:]),
				StartBoundaryID: boundaryID(int(h.i)), EndBoundaryID: boundaryID(int(h.j) + 1),
			})
		}
		res.AdBreaks = append(res.AdBreaks, br)
	}
	res.SkipIntervals = a.skipIntervals(res.AdBreaks)

	// Segments: adjacent blocks with the same label form one segment; the
	// segments partition [0,duration) exactly once (OUT-03).
	for i := 0; i < len(a.blocks); {
		j := i
		for j+1 < len(a.blocks) && a.blocks[j+1].label == a.blocks[i].label {
			j++
		}
		var reasons uint32
		var gap int64
		for k := i; k <= j; k++ {
			reasons |= a.blocks[k].reasons
			gap += a.blocks[k].gapUS
		}
		s, e := a.blocks[i].start, a.blocks[j].end
		seg := Segment{
			StartUS: s, EndUS: e, Label: labelNames[a.blocks[i].label], BlockIDs: blockIDs(i, j),
			Reasons: reasonList(reasons), Coverage: 1 - float64(gap)/float64(e-s),
		}
		if a.blocks[i].label == lblAd {
			if b := breakOf[a.blocks[i].run]; b >= 0 {
				seg.BreakID = res.AdBreaks[b].ID
				seg.UnitIDs = res.AdBreaks[b].UnitIDs
			}
		}
		res.Segments = append(res.Segments, seg)
		i = j + 1
	}

	if detailed {
		res.Diagnostics = a.diagnostics()
	}
	return res
}

func (a *analysis) detectorStates(info *MediaInfo) []DetectorState {
	cfg := a.cfg
	bc := &cfg.Boundaries
	vidUS := a.pre[len(a.pre)-1].vidUS
	st := func(signal string, enabled, boundary bool) DetectorState {
		d := DetectorState{Signal: signal, Status: StatusUsable, Boundary: boundary && enabled}
		if !enabled {
			d.Status, d.Boundary = StatusDisabled, false
		}
		return d
	}
	cover := func(d *DetectorState, known int64) {
		d.CoverageUS = ptr(known)
		d.Coverage = ptr(float64(known) / float64(a.dur))
	}
	out := make([]DetectorState, 0, 8)

	d := st(SignalDarkUniform, cfg.Signals.DarkUniform, bc.FromDarkUniform)
	if cfg.Signals.DarkUniform {
		cover(&d, vidUS)
		d.Events = len(a.dark)
	}
	out = append(out, d)

	d = st(SignalLogo, cfg.Signals.Logo, bc.FromLogo)
	if cfg.Signals.Logo {
		var known int64
		for _, s := range a.logo {
			if s.state != logoUnknown {
				known += s.end - s.start
			}
		}
		cover(&d, known)
		d.Events = len(a.logo)
		if !a.logoUsable {
			// Invalid logo evidence contributes neither modifiers nor
			// boundaries (LOGO-04).
			d.Status, d.Reason, d.Boundary = StatusUnavailable, DetectorNoLogoModel, false
			for _, c := range a.logoCal {
				if c.Status == StatusUnreliable {
					d.Status = StatusUnreliable
				}
				if c.Failed != "" {
					d.Reason = c.Failed
				}
			}
		}
	}
	out = append(out, d)

	d = st(SignalScene, cfg.Signals.Scene, bc.FromScene)
	if cfg.Signals.Scene {
		cover(&d, vidUS)
		d.Events = len(a.scene)
	}
	out = append(out, d)

	d = st(SignalResolution, cfg.Signals.Resolution, bc.FromResolution)
	if cfg.Signals.Resolution {
		cover(&d, vidUS)
		d.Events = len(a.resolution)
	}
	out = append(out, d)

	d = st(SignalCaptions, cfg.Signals.Captions, bc.FromCaptions)
	if cfg.Signals.Captions {
		var known int64
		for _, s := range a.caps {
			known += s.end - s.start
		}
		d.Events = len(a.caps)
		switch {
		case known > 0:
			cover(&d, known)
		case info.Tracks.Captions.Capability == CaptionCapabilityUnsupported:
			d.Status, d.Reason, d.Boundary = StatusUnavailable, DetectorUnsupportedCaptions, false
		default:
			d.Status, d.Reason, d.Boundary = StatusUnavailable, DetectorNoCaptionData, false
		}
	}
	out = append(out, d)

	d = st(SignalAspect, cfg.Signals.Aspect, bc.FromAspect)
	if cfg.Signals.Aspect {
		var known int64
		for _, s := range a.aspect {
			known += s.end - s.start
		}
		cover(&d, known)
		d.Events = len(a.aspect)
		if known == 0 {
			d.Status, d.Reason, d.Boundary = StatusUnreliable, DetectorNoStablePicture, false
		}
	}
	out = append(out, d)

	d = st(SignalAudio, cfg.Signals.Audio, bc.FromSilence)
	if cfg.Signals.Audio {
		if a.audioKnown > 0 {
			cover(&d, a.audioKnown)
			d.Events = len(a.silence)
		} else {
			d.Status, d.Reason, d.Boundary = StatusUnavailable, DetectorNoAudio, false
		}
	}
	out = append(out, d)

	d = st(SignalBumpers, cfg.Signals.Bumpers, bc.FromBumpers)
	if cfg.Signals.Bumpers {
		if a.bumpersActive {
			cover(&d, vidUS)
			d.Events = len(a.bumpers)
		} else {
			d.Status, d.Reason, d.Boundary = StatusUnavailable, DetectorNoTemplates, false
		}
	}
	return append(out, d)
}

func (a *analysis) diagnostics() *Diagnostics {
	cfg := a.cfg
	dg := &Diagnostics{
		Level:               cfg.Outputs.Diagnostics,
		HypothesesEvaluated: a.evaluated,
		HypothesesTruncated: a.truncated,
		Hypotheses:          make([]Hypothesis, 0, len(a.hyps)),
		RejectedRuns:        []RejectedRun{},
	}
	dg.Boundaries = make([]Boundary, len(a.bounds))
	for i := range a.bounds {
		b := &a.bounds[i]
		ev := make([]string, 0, 2)
		if b.dark {
			ev = append(ev, famNames[0])
		}
		if b.av {
			ev = append(ev, famNames[1])
		}
		dg.Boundaries[i] = Boundary{ID: boundaryID(i), TimeUS: b.t, UncertaintyUS: b.unc, Causes: b.causes, Evidence: ev}
	}
	dg.Blocks = make([]Block, len(a.blocks))
	for i := range a.blocks {
		b := &a.blocks[i]
		dur := b.end - b.start
		o := Block{
			ID: blockID(i), StartUS: b.start, EndUS: b.end,
			StartBoundaryID: boundaryID(i), EndBoundaryID: boundaryID(i + 1),
			Missing: b.vidUS == 0, Coverage: 1 - float64(b.gapUS)/float64(dur),
			CodedWidth: b.codedW, CodedHeight: b.codedH,
			CaptionStylesUS: map[string]int64{}, CaptionStyle: CaptionUnknown.String(),
			Score: b.score, Factors: a.factorsOut(b.factors[:b.nf]),
			ProgramEvidence: []string{}, Label: labelNames[b.label], Reasons: reasonList(b.reasons),
		}
		if b.vidUS > 0 {
			o.Brightness = ptr(b.sumMean / float64(b.vidUS))
			o.Nonuniformity = ptr(b.sumStd / float64(b.vidUS))
			if cfg.Signals.Scene {
				o.SceneChanges = ptr(int(b.scenes))
				o.SceneRate = ptr(float64(b.scenes) / (float64(b.vidUS) / usPerSecond))
			}
		}
		if a.logoUsable {
			o.LogoKnownUS = ptr(b.logoKnown)
			o.LogoOccupancy = ratio(b.logoPresent, b.logoKnown)
		}
		if b.aspKnown > 0 {
			o.AspectKnownUS = ptr(b.aspKnown)
			o.ActiveRatio = ptr(b.aspSum / float64(b.aspKnown))
		}
		top := CaptionUnknown
		for s := CaptionNone; s < numCaptionStyles; s++ {
			if b.cap[s] > 0 {
				o.CaptionStylesUS[s.String()] = b.cap[s]
				if top == CaptionUnknown || b.cap[s] > b.cap[top] {
					top = s
				}
			}
		}
		o.CaptionStyle = top.String()
		if b.audKnown > 0 {
			o.AudioKnownUS = ptr(b.audKnown)
			o.AudioEnergyDBFS = ptr(dbfs(b.audEnergy / float64(b.audKnown)))
			o.SilenceFraction = ratio(b.audSilent, b.audKnown)
		}
		if b.program&programLogo != 0 {
			o.ProgramEvidence = append(o.ProgramEvidence, ReasonLogoPresent)
		}
		if b.program&programStable != 0 {
			o.ProgramEvidence = append(o.ProgramEvidence, ReasonStableLongInterval)
		}
		if b.program&programLong != 0 {
			o.ProgramEvidence = append(o.ProgramEvidence, ReasonLongBlock)
		}
		dg.Blocks[i] = o
	}
	addHyp := func(h *hypEval, extra uint16) {
		s, e := a.blocks[h.i].start, a.blocks[h.j].end
		dg.Hypotheses = append(dg.Hypotheses, Hypothesis{
			ID: hypID(h.id), StartUS: s, EndUS: e, BlockIDs: blockIDs(int(h.i), int(h.j)),
			Score: h.score, Factors: a.factorsOut(h.factors[:h.nf]),
			EvidenceFamilies: bitNames(h.fam, famNames[:]),
			Coverage:         1 - float64(a.pre[h.j+1].gapUS-a.pre[h.i].gapUS)/float64(e-s),
			Eligible:         h.eligible, Selected: h.selected,
			Reasons: bitNames(h.reject|extra, rejNames[:]),
		})
	}
	have := make(map[int32]struct{}, len(a.hyps))
	for k := range a.hyps {
		h := &a.hyps[k]
		var extra uint16
		if h.selected && a.blocks[h.i].run >= 0 && !a.runs[a.blocks[h.i].run].accepted {
			extra = rejBreakConstraint
		}
		have[h.id] = struct{}{}
		addHyp(h, extra)
	}
	// Selected units are always present even when retention was truncated.
	for k := range a.units {
		h := &a.units[k].h
		if _, ok := have[h.id]; !ok {
			var extra uint16
			if !a.runs[a.units[k].run].accepted {
				extra = rejBreakConstraint
			}
			addHyp(h, extra)
		}
	}
	sort.SliceStable(dg.Hypotheses, func(i, j int) bool {
		x, _ := strconv.Atoi(dg.Hypotheses[i].ID[1:])
		y, _ := strconv.Atoi(dg.Hypotheses[j].ID[1:])
		return x < y
	})
	for ri := range a.runs {
		r := &a.runs[ri]
		if r.accepted {
			continue
		}
		rr := RejectedRun{StartUS: r.start, EndUS: r.end, Reason: reasonList(r.reason)[0]}
		for u := r.firstUnit; u <= r.lastUnit; u++ {
			rr.HypothesisIDs = append(rr.HypothesisIDs, hypID(a.units[u].h.id))
		}
		dg.RejectedRuns = append(dg.RejectedRuns, rr)
	}

	ev := &dg.Events
	ev.DarkUniform = append([]DarkEvent{}, a.dark...)
	ev.Silence = append([]SilenceEvent{}, a.silence...)
	ev.Scene = append([]SceneEvent{}, a.scene...)
	ev.Resolution = append([]ResolutionEvent{}, a.resolution...)
	ev.Bumpers = append([]BumperEvent{}, a.bumpers...)
	ev.Phrases = append([]PhraseHit{}, a.phraseHits...)
	ev.Aspect = make([]AspectInterval, len(a.aspect))
	for i, s := range a.aspect {
		ev.Aspect[i] = AspectInterval{StartUS: s.start, EndUS: s.end, Ratio: ptr(s.ratio), Left: s.l, Top: s.t, Width: s.w, Height: s.h}
	}
	ev.Logo = make([]LogoInterval, len(a.logo))
	for i, s := range a.logo {
		ev.Logo[i] = LogoInterval{StartUS: s.start, EndUS: s.end, State: logoStateNames[s.state], TransitionUncertaintyUS: s.uncertainty, MeanMatch: s.meanMatch}
	}
	ev.Captions = make([]CaptionInterval, len(a.caps))
	for i, s := range a.caps {
		ev.Captions[i] = CaptionInterval{StartUS: s.start, EndUS: s.end, Style: s.style.String()}
	}
	return dg
}

// IsCanceled reports whether err is a cancellation.
func IsCanceled(err error) bool {
	return errors.Is(err, ErrCanceled) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
