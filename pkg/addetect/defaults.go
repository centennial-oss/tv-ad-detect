// SPDX-License-Identifier: Apache-2.0

package addetect

import "time"

// Built-in default of every numeric configuration key, one constant per
// key, named default<Section><Field>. Values the requirements fix are the
// required initial values; the others are initial product choices
// (SCOPE-04). Enumerated defaults are the named values of constants.go.
const (
	// [input]
	defaultInputTimestampGap         = 500 * time.Millisecond
	defaultInputMaxMissingTimestamps = 0.5
	defaultInputMaxDecodeErrorRatio  = 0.5

	// [package]
	defaultPackageMinHeight        = 360
	defaultPackageMaxManifestBytes = 16 << 20
	defaultPackageMaxSegments      = 1000000
	defaultPackageIOBufferSize     = 256 << 10
	defaultPackageHashMedia        = false
	// C3, off until evaluated.
	defaultBoundariesValidateMinEvents = 8
	defaultBoundariesValidateMinShare  = 0.3
	// On by default (ruling 2026-11-16, C1).
	defaultBoundariesDarkRequiresQuiet = true
	defaultBoundariesDarkQuietWindow   = 1300 * time.Millisecond
	defaultBoundariesDarkQuietLevel    = -35
	// On by default (ruling 2026-11-16, C5).
	defaultDarkIgnoreLogoRegion = true

	// [sampling]
	defaultSamplingAnalysisMaxWidth  = 384
	defaultSamplingAnalysisMaxHeight = 216

	// [dark]
	defaultDarkMaxMeanLuma          = 19
	defaultDarkBrightPixelLuma      = 60
	defaultDarkMaxBrightFraction    = 0.01
	defaultDarkDarkMaxNonuniformity = 16
	defaultDarkUniformMaxNonunif    = 3
	defaultDarkAutoMinLuma          = 8
	defaultDarkAutoMaxLuma          = 40
	defaultDarkAutoMargin           = 2
	defaultDarkAutoMinFrames        = 1000
	defaultDarkAutoMinSupport       = 8
	defaultDarkAutoUniformMin       = 1
	defaultDarkAutoUniformMax       = 8

	// [logo]
	defaultLogoSampleInterval     = 500 * time.Millisecond
	defaultLogoLearnMinSeparation = 2 * time.Second
	defaultLogoLearnMinChange     = 0.05
	defaultLogoEdgeThreshold      = 10
	defaultLogoMinPersistence     = 0.35
	defaultLogoMinLearnSamples    = 60
	defaultLogoMinEdgePixels      = 24
	defaultLogoMinWidth           = 0.02
	defaultLogoMaxWidth           = 0.3
	defaultLogoMinHeight          = 0.02
	defaultLogoMaxHeight          = 0.24
	defaultLogoClusterGap         = 0.02
	defaultLogoPresentThreshold   = 0.6
	defaultLogoAbsentThreshold    = 0.35
	// T2 keys; defaults keep today's behavior until evaluated.
	defaultLogoAmbiguousState       = LogoAmbiguousHold
	defaultLogoThresholdMode        = LogoThresholdFixed
	defaultLogoReferenceQuantile    = 0.9
	defaultLogoRelativePresent      = 0.8
	defaultLogoRelativeAbsent       = 0.3
	defaultLogoMaxBackgroundDensity = 0.85
	defaultLogoMinSpecificity       = 0.1
	defaultLogoMinStateDuration     = 2 * time.Second
	defaultLogoMinOccupancy         = 0.4
	defaultLogoMaxOccupancy         = 0.92
	defaultLogoMinCoverage          = 0.5

	// [scene]
	defaultSceneHistogramBins    = 64
	defaultSceneCutThreshold     = 0.4
	defaultSceneGradualThreshold = 0.55
	defaultSceneGradualWindow    = 400 * time.Millisecond
	defaultSceneMinSpacing       = 400 * time.Millisecond

	// [aspect]
	defaultAspectBorderMaxLuma      = 24
	defaultAspectBorderMaxDeviation = 12
	defaultAspectSampleInterval     = 200 * time.Millisecond
	defaultAspectPersistence        = 2 * time.Second
	defaultAspectRatioTolerance     = 0.04
	defaultAspectMinActiveFraction  = 0.4
	defaultAspectSymmetryTolerance  = 0.01

	// [captions]
	defaultCaptionsInactiveTimeout  = 20 * time.Second
	defaultCaptionsDataTimeout      = 2 * time.Second
	defaultCaptionsStylePersistence = 2 * time.Second

	// [audio]
	defaultAudioWindow       = 20 * time.Millisecond
	defaultAudioSilenceLevel = -50
	defaultAudioMinSilence   = 100 * time.Millisecond
	// C2, off until evaluated.
	defaultAudioSilenceMode      = SilenceModeFixed
	defaultAudioAutoQuantile     = 0.05
	defaultAudioAutoMarginDB     = 6
	defaultAudioAutoMinDBFS      = -70
	defaultAudioAutoMaxDBFS      = -35
	defaultAudioSilenceContextDB = 10
	// defaultAudioAutoMinSupport is the audio an estimate needs.
	defaultAudioAutoMinSupport = time.Minute
	defaultAudioAlignment      = 250 * time.Millisecond

	// [bumpers]
	defaultBumpersSampleInterval = 200 * time.Millisecond

	// [boundaries]
	defaultBoundariesFusionTolerance = 100 * time.Millisecond

	// [baselines]
	defaultBaselinesMinSupport         = 120 * time.Second
	defaultBaselinesMinDominance       = 0.6
	defaultBaselinesMinRecording       = 60 * time.Second
	defaultBaselinesFloorBrightness    = 1
	defaultBaselinesFloorNonuniformity = 0.5
	defaultBaselinesFloorAudioDBFS     = -70
	defaultBaselinesFloorSilence       = 0.01
	defaultBaselinesFloorSceneRate     = 0.02
	defaultBaselinesAspectRatio        = 0

	// [scoring]
	defaultScoringThreshold              = 1.5
	defaultScoringStrictTolerance        = 500 * time.Millisecond
	defaultScoringLooseTolerance         = 1 * time.Second
	defaultScoringUncertaintyExtension   = 0
	defaultScoringDurationMaxUncertainty = 1 * time.Second
	defaultScoringStrictFactor           = 2
	defaultScoringLooseFactor            = 1.5
	defaultScoringCombinedFactor         = 1.5
	defaultScoringLogoPresentFactor      = 0.1
	defaultScoringLogoAbsentFactor       = 2
	defaultScoringCaptionFactor          = 2
	defaultScoringCaptionMatchFactor     = 0.4
	// Neutral until evaluated (REFINEMENTS-TODO section 3).
	defaultScoringCaptionMatchCondition   = CaptionMatchAlways
	defaultScoringCaptionAbsentFactor     = 1
	defaultScoringCaptionUnexpectedFactor = 1
	defaultScoringLongBlockFactor         = 1
	defaultScoringTolerancePerSeparator   = 0
	defaultScoringThresholdPercentile     = 0
	defaultScoringSilentBlockMinDuration  = 10 * time.Second
	defaultScoringSilentBlockMinFraction  = 0.9
	defaultCaptionsPhraseRepeat           = 10 * time.Second
	defaultScoringAspectFactor            = 2
	defaultScoringRelativeFactor          = 2
	defaultScoringLogoPresentOccupancy    = 0.8
	defaultScoringLogoAbsentOccupancy     = 0.2
	defaultScoringMinCoverageVideo        = 0.8
	defaultScoringMinCoverageLogo         = 0.8
	defaultScoringMinCoverageCaptions     = 0.8
	defaultScoringMinCoverageAspect       = 0.8
	defaultScoringMinCoverageAudio        = 0.8
	defaultScoringRelExcessRatio          = 1.3
	defaultScoringRelMinSupport           = 2 * time.Second
	defaultScoringSceneMinEvents          = 3
	defaultScoringMaxUnitBlocks           = 256

	// [breaks]
	defaultBreaksMinUnit  = 4 * time.Second
	defaultBreaksMaxUnit  = 120 * time.Second
	defaultBreaksMinBreak = 20 * time.Second
	defaultBreaksMaxBreak = 600 * time.Second
	// Rulings 2026-11-16: bridging and truncated edges on by default.
	defaultBreaksMaxBridgeGap        = 15 * time.Second
	defaultBreaksBridgeGaps          = true
	defaultBreaksBridgeLogoProgram   = true
	defaultBreaksAllowTruncatedEdges = true

	// [outputs]
	defaultOutputsSkipPaddingStart = 0
	defaultOutputsSkipPaddingEnd   = 0
	defaultOutputsMaxHypotheses    = 20000

	// [resources]
	defaultResourcesWorkers      = 0
	defaultResourcesMemoryBudget = 512 << 20
	defaultResourcesScratchQuota = 0
)

// Default logo search regions: one per picture corner, inset by the margins.
const (
	logoRegionMarginX = 0.02
	logoRegionMarginY = 0.03
	logoRegionW       = 0.32
	logoRegionH       = 0.26
)

// Defaults of a bumper template declared without the corresponding key.
const (
	defaultBumperThreshold  = 0.9
	defaultBumperMinSupport = 400 * time.Millisecond
)

// fullFrameRegion is the whole picture.
var fullFrameRegion = Region{X: 0, Y: 0, W: 1, H: 1}

// standardAdSeconds are the initial standard advertisement lengths
// (SCORE-03).
var standardAdSeconds = [...]int{10, 15, 20, 30, 45, 60, 90, 120}

func defaultStandardDurations() []time.Duration {
	out := make([]time.Duration, len(standardAdSeconds))
	for i, s := range standardAdSeconds {
		out[i] = time.Duration(s) * time.Second
	}
	return out
}

// defaultLogoRegions returns the four corner search regions.
func defaultLogoRegions() []Region {
	right, bottom := 1-logoRegionMarginX-logoRegionW, 1-logoRegionMarginY-logoRegionH
	return []Region{
		{logoRegionMarginX, logoRegionMarginY, logoRegionW, logoRegionH},
		{right, logoRegionMarginY, logoRegionW, logoRegionH},
		{logoRegionMarginX, bottom, logoRegionW, logoRegionH},
		{right, bottom, logoRegionW, logoRegionH},
	}
}

// DefaultConfig returns the built-in defaults (CFG-02 layer 1).
func DefaultConfig() *Config {
	return &Config{
		Input: InputConfig{
			VideoTrack:           TrackAuto,
			HardwareDecode:       HardwareDecodeOff,
			AudioTrack:           TrackAuto,
			CaptionTrack:         TrackAuto,
			Decoder:              DecoderFFmpeg,
			UnsupportedFormat:    FormatPolicyWarn,
			OptionalStreams:      StreamPolicyDegraded,
			TimestampGap:         defaultInputTimestampGap,
			TimestampResetPolicy: ResetPolicyRebase,
			MaxMissingTimestamps: defaultInputMaxMissingTimestamps,
			SkipLoopFilter:       LoopFilterNonRef,
			MaxDecodeErrorRatio:  defaultInputMaxDecodeErrorRatio,
		},
		Package: PackageConfig{
			ManifestType:     ManifestAuto,
			VideoSelection:   SelectionHighest,
			AudioSelection:   SelectionAuto,
			MinHeight:        defaultPackageMinHeight,
			FailurePolicy:    FailurePolicyFail,
			AllowRemote:      false,
			MaxManifestBytes: defaultPackageMaxManifestBytes,
			MaxSegments:      defaultPackageMaxSegments,
			IOBufferSize:     defaultPackageIOBufferSize,
			HashMedia:        defaultPackageHashMedia,
		},
		Sampling: SamplingConfig{
			AnalysisMaxWidth:   defaultSamplingAnalysisMaxWidth,
			AnalysisMaxHeight:  defaultSamplingAnalysisMaxHeight,
			Region:             fullFrameRegion,
			FullResBrightCheck: true,
			DecodeFrames:       DecodeAll,
		},
		Signals: SignalsConfig{
			DarkUniform: true,
			Logo:        true,
			Scene:       true,
			Resolution:  true,
			Captions:    true,
			Aspect:      true,
			Audio:       true,
			Bumpers:     true,
		},
		Dark: DarkConfig{
			Mode:                 DarkModeFixed,
			MaxMeanLuma:          defaultDarkMaxMeanLuma,
			BrightPixelLuma:      defaultDarkBrightPixelLuma,
			MaxBrightFraction:    defaultDarkMaxBrightFraction,
			DarkMaxNonuniformity: defaultDarkDarkMaxNonuniformity,
			DetectUniform:        true,
			IgnoreLogoRegion:     defaultDarkIgnoreLogoRegion,
			UniformMaxNonunif:    defaultDarkUniformMaxNonunif,
			AutoMinLuma:          defaultDarkAutoMinLuma,
			AutoMaxLuma:          defaultDarkAutoMaxLuma,
			AutoMargin:           defaultDarkAutoMargin,
			AutoMinFrames:        defaultDarkAutoMinFrames,
			AutoMinSupport:       defaultDarkAutoMinSupport,
			AutoUniformMin:       defaultDarkAutoUniformMin,
			AutoUniformMax:       defaultDarkAutoUniformMax,
		},
		Logo: LogoConfig{
			SearchRegions:        defaultLogoRegions(),
			SampleInterval:       defaultLogoSampleInterval,
			LearnMinSeparation:   defaultLogoLearnMinSeparation,
			LearnMinChange:       defaultLogoLearnMinChange,
			EdgeThreshold:        defaultLogoEdgeThreshold,
			MinPersistence:       defaultLogoMinPersistence,
			MinLearnSamples:      defaultLogoMinLearnSamples,
			MinEdgePixels:        defaultLogoMinEdgePixels,
			MinWidth:             defaultLogoMinWidth,
			MaxWidth:             defaultLogoMaxWidth,
			MinHeight:            defaultLogoMinHeight,
			MaxHeight:            defaultLogoMaxHeight,
			ClusterGap:           defaultLogoClusterGap,
			PresentThreshold:     defaultLogoPresentThreshold,
			AbsentThreshold:      defaultLogoAbsentThreshold,
			AmbiguousState:       defaultLogoAmbiguousState,
			ThresholdMode:        defaultLogoThresholdMode,
			ReferenceQuantile:    defaultLogoReferenceQuantile,
			RelativePresent:      defaultLogoRelativePresent,
			RelativeAbsent:       defaultLogoRelativeAbsent,
			MinSpecificity:       defaultLogoMinSpecificity,
			MaxBackgroundDensity: defaultLogoMaxBackgroundDensity,
			MinStateDuration:     defaultLogoMinStateDuration,
			MinPresentDuration:   0,
			MinAbsentDuration:    0,
			PresentTrim:          0,
			MinOccupancy:         defaultLogoMinOccupancy,
			MaxOccupancy:         defaultLogoMaxOccupancy,
			MinCoverage:          defaultLogoMinCoverage,
		},
		Scene: SceneConfig{
			HistogramBins:    defaultSceneHistogramBins,
			CutThreshold:     defaultSceneCutThreshold,
			GradualThreshold: defaultSceneGradualThreshold,
			GradualWindow:    defaultSceneGradualWindow,
			MinSpacing:       defaultSceneMinSpacing,
		},
		Aspect: AspectConfig{
			BorderMaxLuma:      defaultAspectBorderMaxLuma,
			BorderMaxDeviation: defaultAspectBorderMaxDeviation,
			SampleInterval:     defaultAspectSampleInterval,
			Persistence:        defaultAspectPersistence,
			RatioTolerance:     defaultAspectRatioTolerance,
			MinActiveFraction:  defaultAspectMinActiveFraction,
			SymmetryTolerance:  defaultAspectSymmetryTolerance,
		},
		Captions: CaptionConfig{
			Service:          ServiceCC1,
			InactiveTimeout:  defaultCaptionsInactiveTimeout,
			DataTimeout:      defaultCaptionsDataTimeout,
			StylePersistence: defaultCaptionsStylePersistence,
			PhraseRepeat:     defaultCaptionsPhraseRepeat,
		},
		Audio: AudioConfig{
			Metric:           AudioMetricRMS,
			ChannelPolicy:    ChannelsAll,
			Window:           defaultAudioWindow,
			SilenceLevel:     defaultAudioSilenceLevel,
			MinSilence:       defaultAudioMinSilence,
			SilenceMode:      defaultAudioSilenceMode,
			AutoQuantile:     defaultAudioAutoQuantile,
			AutoMarginDB:     defaultAudioAutoMarginDB,
			AutoMinDBFS:      defaultAudioAutoMinDBFS,
			AutoMaxDBFS:      defaultAudioAutoMaxDBFS,
			SilenceContextDB: defaultAudioSilenceContextDB,
			Alignment:        defaultAudioAlignment,
		},
		Bumpers: BumperConfig{
			SampleInterval: defaultBumpersSampleInterval,
			Boundary:       BumperBoundaryBoth,
		},
		Boundaries: BoundaryConfig{
			FusionTolerance:   defaultBoundariesFusionTolerance,
			SpanPolicy:        SpanPolicyMidpoint,
			FromDarkUniform:   true,
			FromSilence:       true,
			FromResolution:    true,
			FromAspect:        true,
			FromBumpers:       true,
			FromLogo:          false,
			FromCaptions:      false,
			FromScene:         false,
			SilenceSceneAlign: true,
			DarkRequiresQuiet: defaultBoundariesDarkRequiresQuiet,
			ValidateMinEvents: defaultBoundariesValidateMinEvents,
			ValidateMinShare:  defaultBoundariesValidateMinShare,
			DarkQuietWindow:   defaultBoundariesDarkQuietWindow,
			DarkQuietLevel:    defaultBoundariesDarkQuietLevel,
		},
		Baselines: BaselineConfig{
			MinSupport:         defaultBaselinesMinSupport,
			MinDominance:       defaultBaselinesMinDominance,
			MinRecording:       defaultBaselinesMinRecording,
			FloorBrightness:    defaultBaselinesFloorBrightness,
			FloorNonuniformity: defaultBaselinesFloorNonuniformity,
			FloorAudioDBFS:     defaultBaselinesFloorAudioDBFS,
			FloorSilence:       defaultBaselinesFloorSilence,
			FloorSceneRate:     defaultBaselinesFloorSceneRate,
			AspectRatio:        defaultBaselinesAspectRatio,
		},
		Scoring: ScoringConfig{
			Threshold:               defaultScoringThreshold,
			StandardDurations:       defaultStandardDurations(),
			StrictTolerance:         defaultScoringStrictTolerance,
			LooseTolerance:          defaultScoringLooseTolerance,
			UncertaintyExtension:    defaultScoringUncertaintyExtension,
			DurationMaxUncertainty:  defaultScoringDurationMaxUncertainty,
			StrictFactor:            defaultScoringStrictFactor,
			LooseFactor:             defaultScoringLooseFactor,
			CombinedFactor:          defaultScoringCombinedFactor,
			LogoPresentFactor:       defaultScoringLogoPresentFactor,
			LogoAbsentFactor:        defaultScoringLogoAbsentFactor,
			CaptionFactor:           defaultScoringCaptionFactor,
			CaptionMatchFactor:      defaultScoringCaptionMatchFactor,
			CaptionMatchCondition:   defaultScoringCaptionMatchCondition,
			CaptionAbsentFactor:     defaultScoringCaptionAbsentFactor,
			CaptionUnexpectedFactor: defaultScoringCaptionUnexpectedFactor,
			LongBlockFactor:         defaultScoringLongBlockFactor,
			TolerancePerSeparator:   defaultScoringTolerancePerSeparator,
			ThresholdPercentile:     defaultScoringThresholdPercentile,
			UnmatchedMinFamilies:    1,
			CombinedProgramEdges:    true,
			AdPhraseFactor:          1,
			ProgramPhraseFactor:     1,
			SilentBlockFactor:       1,
			SilentBlockMinDuration:  defaultScoringSilentBlockMinDuration,
			SilentBlockMinFraction:  defaultScoringSilentBlockMinFraction,
			AspectFactor:            defaultScoringAspectFactor,
			RelativeFactor:          defaultScoringRelativeFactor,
			LogoPresentOccupancy:    defaultScoringLogoPresentOccupancy,
			LogoAbsentOccupancy:     defaultScoringLogoAbsentOccupancy,
			MinCoverageVideo:        defaultScoringMinCoverageVideo,
			MinCoverageLogo:         defaultScoringMinCoverageLogo,
			MinCoverageCaptions:     defaultScoringMinCoverageCaptions,
			MinCoverageAspect:       defaultScoringMinCoverageAspect,
			MinCoverageAudio:        defaultScoringMinCoverageAudio,
			RelBrightness:           false,
			RelNonuniformity:        false,
			RelAudioEnergy:          false,
			RelSilence:              false,
			RelSceneRate:            false,
			RelExcessRatio:          defaultScoringRelExcessRatio,
			RelMinSupport:           defaultScoringRelMinSupport,
			SceneMinEvents:          defaultScoringSceneMinEvents,
			BoundaryEdges:           EdgesBoth,
			MaxUnitBlocks:           defaultScoringMaxUnitBlocks,
		},
		Breaks: BreakConfig{
			MinUnit:             defaultBreaksMinUnit,
			MaxUnit:             defaultBreaksMaxUnit,
			MinBreak:            defaultBreaksMinBreak,
			MaxBreak:            defaultBreaksMaxBreak,
			AllowTruncatedEdges: defaultBreaksAllowTruncatedEdges,
			BridgeGaps:          defaultBreaksBridgeGaps,
			MaxBridgeGap:        defaultBreaksMaxBridgeGap,
			BridgeLogoProgram:   defaultBreaksBridgeLogoProgram,
		},
		Outputs: OutputConfig{
			Diagnostics:      DiagnosticsSummary,
			SkipPaddingStart: defaultOutputsSkipPaddingStart,
			SkipPaddingEnd:   defaultOutputsSkipPaddingEnd,
			Overwrite:        false,
			WritePartial:     false,
			MaxHypotheses:    defaultOutputsMaxHypotheses,
		},
		Resources: ResourceConfig{
			Workers:      defaultResourcesWorkers,
			MemoryBudget: defaultResourcesMemoryBudget,
			ScratchQuota: defaultResourcesScratchQuota,
		},
	}
}
