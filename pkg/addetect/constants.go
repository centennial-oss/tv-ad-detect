// SPDX-License-Identifier: Apache-2.0

package addetect

import "github.com/centennial-oss/tv-ad-detect/pkg/cea608"

// This file is the single home of the package's scalar vocabulary: every
// string that appears in configuration values or in results, and every
// shared numeric scale. Code refers to these names, never to the literals.

// Words shared by several enumerations. Each enumeration below gives them a
// meaningful name; the literal itself is written once.
const (
	wordAll      = "all"
	wordAudio    = "audio"
	wordAuto     = "auto"
	wordBoth     = "both"
	wordDegraded = "degraded"
	wordEnd      = "end"
	wordFail     = "fail"
	wordNone     = "none"
	wordStart    = "start"
	wordStrict   = "strict"
	wordUnknown  = cea608.NameUnknown
	wordVideo    = "video"
)

// Configuration values (CFG-01). Field comments in config.go say which
// values a key accepts; enumBindings in configfile.go binds them.
const (
	// Track selection (input.video_track, input.audio_track,
	// input.caption_track).
	TrackAuto     = wordAuto
	TrackNone     = wordNone
	TrackEmbedded = "embedded"

	// input.decoder.
	DecoderFFmpeg = "ffmpeg"

	// input.unsupported_format_policy.
	FormatPolicyWarn = "warn"
	FormatPolicyFail = wordFail

	// input.optional_stream_policy.
	StreamPolicyDegraded = wordDegraded
	StreamPolicyStrict   = wordStrict

	// input.timestamp_reset_policy.
	ResetPolicyRebase = "rebase"
	ResetPolicyFail   = wordFail

	// input.skip_loop_filter.
	LoopFilterNone   = wordNone
	LoopFilterNonRef = "nonref"
	LoopFilterAll    = wordAll

	// package.manifest_type.
	ManifestAuto = wordAuto
	ManifestHLS  = "hls"
	ManifestDASH = "dash"

	// package.video_selection / package.audio_selection defaults.
	SelectionHighest = "highest"
	SelectionAuto    = wordAuto

	// package.failure_policy.
	FailurePolicyFail     = wordFail
	FailurePolicyDegraded = wordDegraded

	// sampling.decode_frames.
	DecodeAll       = wordAll
	DecodeReference = "reference"

	// dark.mode.
	DarkModeFixed = "fixed"
	DarkModeAuto  = wordAuto

	// captions.service.
	ServiceCC1 = "CC1"
	ServiceCC2 = "CC2"
	ServiceCC3 = "CC3"
	ServiceCC4 = "CC4"

	// audio.metric and audio.channel_policy.
	AudioMetricRMS     = "rms"
	ChannelsAll        = wordAll
	ChannelsExcludeLFE = "exclude_lfe"

	// bumpers.boundary.
	BumperBoundaryBoth  = wordBoth
	BumperBoundaryStart = wordStart
	BumperBoundaryEnd   = wordEnd

	// Bumper template geometry policy.
	GeometryStretch = "stretch"
	GeometryStrict  = wordStrict

	// boundaries.span_policy.
	SpanPolicyMidpoint = "midpoint"

	// scoring.boundary_evidence_edges.
	EdgesBoth   = wordBoth
	EdgesEither = "either"

	// outputs.diagnostics.
	DiagnosticsSummary  = "summary"
	DiagnosticsDetailed = "detailed"
	DiagnosticsFull     = "full"
)

// Configuration keys that code refers to by name. They must match the cfg
// struct tags in config.go; cfgFields panics at first use if one does not.
const (
	KeyVideoTrack   = "input.video_track"
	KeyAudioTrack   = "input.audio_track"
	KeyCaptionTrack = "input.caption_track"
	KeyDiagnostics  = "outputs.diagnostics"

	keySeparator       = "."
	sectionBumpers     = "bumpers"
	keyTemplates       = "templates"
	bumperTemplatesKey = sectionBumpers + keySeparator + keyTemplates
	bumperKeyPrefix    = bumperTemplatesKey + keySeparator

	// Keys of one bumper template.
	tmplKeyImage      = "image"
	tmplKeyRegion     = "region"
	tmplKeyGeometry   = "geometry"
	tmplKeyThreshold  = "threshold"
	tmplKeyMinSupport = "min_support"
	tmplKeySHA256     = "sha256"

	// Asset digest keys of the configuration identity.
	digestKeyLogoAsset = "logo.asset"
	// LogoCacheSuffix names logo cache entries (cache_key + suffix).
	LogoCacheSuffix = ".tvad-logo.json"
	// maxCacheKeyLen bounds logo.cache_key.
	maxCacheKeyLen = 128
	// WarnLogoCacheUnusable reports a logo cache entry that could not be
	// used; the logo is learned instead.
	WarnLogoCacheUnusable  = "logo_cache_unusable"
	digestKeyLogoAssetData = "logo.asset_data"
	// DigestPrefix prefixes hex SHA-256 digests in results.
	DigestPrefix = "sha256:"
)

// Result vocabulary: units, input kinds and adapter identities.
const (
	TimeUnitMicroseconds = "microseconds"

	InputKindFile         = "file"
	InputKindObservations = "observations"
	InputKindHLS          = ManifestHLS
	InputKindDASH         = ManifestDASH

	callerAdapterName    = "caller"
	callerAdapterVersion = "1"

	// Caption source and capability (CaptionTrackInfo).
	CaptionSourceEmbedded        = "embedded_cea608"
	CaptionSourceTrack           = "track"
	CaptionSourceNone            = wordNone
	CaptionCapabilityCEA608      = "cea608"
	CaptionCapabilityUnsupported = "unsupported"
	CaptionCapabilityNone        = wordNone

	// Package track names and resource statuses (PackageResource).
	PackageTrackVideo     = wordVideo
	PackageTrackAudio     = wordAudio
	ResourceStatusOK      = "ok"
	ResourceStatusGap     = "gap"
	ResourceStatusMissing = "missing"
	ResourceStatusUnread  = "unread"
)

// SignalTimeline names the timeline itself as the source of recording-edge
// and coverage-edge boundaries.
const SignalTimeline = "timeline"

// Boundary cause roles and kinds (BoundaryCause).
const (
	RoleBoundary      = "boundary"
	RoleCorroboration = "corroboration"
	// RoleVeto marks a cause that removes a boundary's evidence role.
	RoleVeto = "veto"
	// KindLoudDarkBoundary is the C1 veto: the audio stays loud across a
	// dark/uniform boundary.
	KindLoudDarkBoundary = "loud_dark_boundary"
	// WarnSourceInvalidated reports a boundary source that failed the C3
	// per-recording check.
	WarnSourceInvalidated = "boundary_source_invalidated"
	// Source names in that warning.
	SourceSilenceOnly = "silence_only"
	SourceUniform     = "uniform"

	KindDark             = "dark"
	KindUniform          = "uniform"
	KindDarkUniformSpan  = "dark_uniform_span"
	KindSilence          = "silence"
	KindCodedSizeChange  = "coded_size_change"
	KindRatioChange      = "ratio_change"
	KindMatchStart       = "match_start"
	KindMatchEnd         = "match_end"
	KindLogoAppeared     = "appeared"
	KindLogoDisappeared  = "disappeared"
	KindStyleChange      = "style_change"
	KindSceneCut         = "cut"
	KindSceneGradual     = "gradual"
	KindCoverageGapStart = "coverage_gap_start"
	KindCoverageGapEnd   = "coverage_gap_end"
	KindRecordingStart   = "recording_start"
	KindRecordingEnd     = "recording_end"
)

// Caption style and logo state names.
const (
	CaptionStyleUnknown = wordUnknown
	CaptionStyleNone    = wordNone
	CaptionStyleRollUp  = cea608.NameRollUp
	CaptionStylePopOn   = cea608.NamePopOn
	CaptionStylePaintOn = cea608.NamePaintOn

	LogoStateUnknown = wordUnknown

	// logo.ambiguous_state and logo.threshold_mode.
	LogoAmbiguousHold     = "hold"
	LogoAmbiguousUnknown  = wordUnknown
	LogoThresholdFixed    = "fixed"
	LogoThresholdRelative = "relative"

	// wordAlways is a policy value.
	wordAlways = "always"

	// input.hardware_decode.
	HardwareDecodeOff  = "off"
	HardwareDecodeAuto = DarkModeAuto

	// audio.silence_mode.
	SilenceModeFixed = DarkModeFixed
	SilenceModeAuto  = DarkModeAuto
	// Silence calibration fallback reason.
	SilenceFallbackInsufficient = "insufficient_audio"

	// scoring.caption_match_condition.
	CaptionMatchAlways         = wordAlways
	CaptionMatchNoLogo         = "no_logo_evidence"
	CaptionMatchNotPopOn       = "program_not_pop_on"
	CaptionMatchNoLogoNotPopOn = "no_logo_evidence_and_program_not_pop_on"
	LogoStatePresent           = "present"
	LogoStateAbsent            = "absent"
)

// Score rule names (Factor.Rule) and families (Factor.Family).
const (
	RuleStrictStandardDuration   = "strict_standard_duration"
	RuleLooseStandardDuration    = "loose_standard_duration"
	RuleCombinedStandardDuration = "combined_standard_duration"
	RuleLogoPresent              = "logo_present"
	RuleLogoAbsent               = "logo_absent"
	RuleCaptionStyleMismatch     = "caption_style_mismatch"
	RuleAspectRatioMismatch      = "aspect_ratio_mismatch"
	RuleRelativeBrightness       = "relative_brightness"
	RuleRelativeNonuniformity    = "relative_nonuniformity"
	RuleRelativeAudioEnergy      = "relative_audio_energy"
	RuleRelativeSilenceFraction  = "relative_silence_fraction"
	RuleRelativeSceneRate        = "relative_scene_rate"
	RuleCaptionStyleMatch        = "caption_style_match"
	RuleCaptionAbsent            = "caption_absent"
	RuleCaptionUnexpected        = "caption_unexpected"
	RuleLongBlock                = "long_block"
	RuleSilentBlock              = "silent_block"
	RuleCaptionAdPhrase          = "caption_ad_phrase"
	// RejectTooFewSignals: no standard length and fewer independent ad
	// signals than scoring.unmatched_length_min_families.
	RejectTooFewSignals = "too_few_signals_without_standard_length"
	// RejectCombinedTooFewSignals: a standard length only as the sum of
	// several blocks, with fewer independent ad signals than
	// scoring.combined_length_min_families.
	RejectCombinedTooFewSignals = "too_few_signals_for_combined_length"
	// RejectCombinedProgramEdge: a standard length only as the sum of
	// several blocks, whose first or last block carries only program
	// evidence (scoring.combined_length_program_edges).
	RejectCombinedProgramEdge = "combined_length_edge_matches_program"
	RuleCaptionProgramPhrase  = "caption_program_phrase"
	// Phrase lists (PhraseHit.List).
	PhraseListAdvertisement = LabelAdvertisement
	PhraseListProgram       = LabelProgram

	FamilyDuration = "duration"
)

// Evidence families of the SCORE-06 gate (AdUnit.EvidenceFamilies,
// Boundary.Evidence).
const (
	EvidenceDarkUniformBoundary    = "dark_uniform_boundary"
	EvidenceAVCorroboratedBoundary = "av_corroborated_boundary"
)

// Reasons a hypothesis is not an accepted ad unit (Hypothesis.Reasons).
const (
	RejectBelowThreshold     = "below_threshold"
	RejectNoEvidenceFamily   = "no_evidence_family"
	RejectUnitDurationLimits = "unit_duration_limits"
	RejectIncompleteCoverage = "incomplete_coverage"
	RejectContainsProgram    = "contains_program_block"
	RejectNotSelected        = "not_selected"
	RejectBreakConstraint    = "break_constraint"
	RejectSpansDarkBoundary  = "spans_dark_boundary"
)

// Segment and block reason codes (OUT-02). Evidence-derived reasons reuse
// the rule and evidence names.
const (
	ReasonStandardDuration     = "standard_duration"
	ReasonBridgedGap           = "bridged_gap"
	ReasonLogoPresent          = RuleLogoPresent
	ReasonStableLongInterval   = "stable_long_interval"
	ReasonLongBlock            = RuleLongBlock
	ReasonMissingCoverage      = "missing_coverage"
	ReasonBreakTooShort        = "break_too_short"
	ReasonBreakTooLong         = "break_too_long"
	ReasonConflictingEvidence  = "conflicting_evidence"
	ReasonRejectedAdHypothesis = "rejected_ad_hypothesis"
	ReasonInsufficientEvidence = "insufficient_evidence"
	// Context rules (C4, C7).
	ReasonContextSliver   = "context_sliver"
	ReasonDarkTail        = "dark_tail"
	ReasonAdjacentProgram = "adjacent_program"
)

// Baseline origins, units and unreliability reasons (Baseline).
const (
	OriginNone              = wordNone
	OriginRecording         = "recording"
	OriginRecordingFloored  = "recording_floored"
	OriginChannelProfile    = "channel_profile"
	OriginLogoPresent       = RuleLogoPresent
	OriginLongStable        = "long_stable_intervals"
	OriginRecordingDominant = "recording_dominant_fallback"

	UnitLuma            = "luma"
	UnitRatio           = "ratio"
	UnitMeanSquare      = "mean_square"
	UnitEventsPerSecond = "events_per_second"

	BaselineSignalUnavailable   = "signal_unavailable"
	BaselineInsufficientSupport = "insufficient_support"
	BaselineAmbiguous           = "ambiguous"
)

// Calibration vocabulary: logo origins and validation failures
// (LogoCalibration), dark calibration fallback reasons (DarkCalibration).
const (
	LogoOriginLearned = "learned"
	LogoOriginAsset   = "asset"

	LogoFailInsufficientLearning = "insufficient_learning_support"
	LogoFailNoPersistent         = "no_persistent_candidate"
	LogoFailNoCandidateInLimits  = "no_candidate_within_size_limits"
	LogoFailNoObservations       = "no_observations"
	LogoFailInsufficientCoverage = "insufficient_coverage"
	LogoFailOccupancyBelow       = "occupancy_below_minimum"
	LogoFailOccupancyAbove       = "occupancy_above_maximum"
	LogoFailAssetIncompatible    = "asset_incompatible_geometry"

	DarkFallbackInsufficientData    = "insufficient_data"
	DarkFallbackNoDarkFrames        = "no_dark_frames"
	DarkFallbackAmbiguous           = "ambiguous_distribution"
	DarkFallbackInsufficientSupport = BaselineInsufficientSupport
)

// Detector state reasons (DetectorState.Reason).
const (
	DetectorNoLogoModel         = "no_logo_model"
	DetectorNoCaptionData       = "no_caption_data"
	DetectorUnsupportedCaptions = "unsupported_caption_format"
	DetectorNoStablePicture     = "no_stable_active_picture"
	DetectorNoAudio             = "no_audio"
	DetectorNoTemplates         = "no_templates"
)

// Coverage gap reasons (TimeRange.Reason).
const (
	GapReasonTimestamp       = "timestamp_gap"
	GapReasonResourcePrefix  = "package_resource_"
	GapReasonResourceMissing = GapReasonResourcePrefix + ResourceStatusMissing
	GapReasonResourceGap     = GapReasonResourcePrefix + ResourceStatusGap
)

// Warning codes (Warning.Code).
const (
	WarnDecodeError            = "decode_error"
	WarnTimestampReset         = "timestamp_reset"
	WarnInterlacedVideo        = "interlaced_video"
	WarnFinalFrameEstimated    = "final_frame_duration_estimated"
	WarnMissingTimestamps      = "missing_timestamps"
	WarnTimestampOverlaps      = "timestamp_overlaps"
	WarnCoverageGaps           = "coverage_gaps"
	WarnHypothesesTruncated    = "hypotheses_truncated"
	WarnBumperRegionTooSmall   = "bumper_region_too_small"
	WarnBumperGeometryMismatch = "bumper_geometry_mismatch"
	WarnBumperFlatTemplate     = "bumper_flat_template"
	WarnCorruptFrames          = "corrupt_frames"
	WarnTruncatedInput         = "truncated_input"
	// Adapter-reported conditions.
	WarnHDRDegraded  = "hdr_degraded"
	WarnCoarseDecode = "coarse_decode"
	// WarnHardwareDecodeUnavailable: hardware decoding was requested but
	// the video is decoded in software.
	WarnHardwareDecodeUnavailable = "hardware_decode_unavailable"
	WarnRotationIgnored           = "rotation_ignored"
	WarnColorRangeAssumed         = "color_range_assumed"
	WarnAudioUnavailable          = "audio_unavailable"
	WarnNoAudio                   = DetectorNoAudio
	WarnCaptionsUnavailable       = "captions_unavailable"
	WarnCaptionsUnsupported       = "captions_unsupported"
	WarnManifest                  = "manifest"
	WarnResourceUnreadable        = "package_resource_unreadable"
	WarnResourceCorrupt           = "package_resource_corrupt"
	WarnPackageRunFailed          = "package_run_failed"
	WarnLeadingGap                = "leading_gap"
	WarnAudioUnplaced             = "audio_unplaced"
)

// Identifier prefixes of result records.
const (
	idPrefixBlock      = "b"
	idPrefixBoundary   = "bd"
	idPrefixHypothesis = "h"
	idPrefixUnit       = "u"
	idPrefixBreak      = "break"
)

// Numeric scales shared across the package.
const (
	// usPerSecond converts between seconds and the microsecond timeline.
	usPerSecond = 1_000_000
	// lumaLevels is the number of canonical 8-bit luminance values.
	lumaLevels = 256
	// lumaMax is the largest canonical luminance value.
	lumaMax = lumaLevels - 1
	// lumaFixedScale stores per-frame luminance statistics as value x 256
	// in 16 bits.
	lumaFixedScale = 256
	// ratioFixedScale stores per-frame ratios as value x 65535 in 16 bits.
	ratioFixedScale = 65535
	// minDBFS is the floor reported for digital silence.
	minDBFS = -160
	// minMeanSquare is the mean-square level at minDBFS (10^(minDBFS/10)).
	minMeanSquare = 1e-16
	// decibelBase is both the logarithm base and the power-ratio scale of
	// decibels: dB = 10 * log10(power ratio).
	decibelBase = 10
	// pixelCenter samples a pixel at its center when resampling.
	pixelCenter = 0.5
	// logoSampleBytes, logoWordBytes and logoCountBytes are the retained
	// sizes of logo observations, bit-set words and persistence counters.
	logoSampleBytes = 16
	logoWordBytes   = 8
	logoCountBytes  = 4
	// fallbackFrameRate sizes reservations when the source gives no rate.
	fallbackFrameRate = 30
	maxPlausibleFPS   = 240
	// aspectCompatTolerance is the relative aspect difference tolerated
	// when projecting assets onto a recording's geometry.
	aspectCompatTolerance = 0.02

	// wordBits, wordShift and wordMask address bits in []uint64 bit sets.
	wordBits  = 64
	wordShift = 6
	wordMask  = wordBits - 1

	// frameRecBytes and audioWindowBytes are the retained sizes of one
	// frame record and one audio window (memory budget accounting).
	frameRecBytes    = 16
	audioWindowBytes = 24
	// reserveSlack is extra capacity added to duration-based reservations.
	reserveSlack = 64
	// cancelCheckMask spaces context checks in the event loop (every 64).
	cancelCheckMask = 63
	// featureKeyNote marks feature keys in the commented default YAML.
	featureKeyNote = "# feature key: affects measurements; a saved feature file cannot be re-classified with a different value"
	// maxWarnings bounds the warnings kept in one result.
	maxWarnings = 256
	// progressIntervalUS is the media time between progress callbacks.
	progressIntervalUS = usPerSecond
	// defaultFrameUS is the frame duration assumed when a recording gives
	// no way to estimate one (30000/1001 fps).
	defaultFrameUS = 33367
	// maxFrameDeltaSamples bounds the sample used for the median frame
	// duration.
	maxFrameDeltaSamples = 4096

	// stdBinsPerLuma and stdBinCount shape the nonuniformity histogram of
	// dark calibration (1/16 luma bins over 0..128).
	stdBinsPerLuma = 16
	stdBinCount    = 128*stdBinsPerLuma + 1
	// darkClusterMaxEmptyBins is the largest run of empty luminance bins
	// inside the darkest cluster.
	darkClusterMaxEmptyBins = 2
	// uniformPercentile and uniformHeadroom derive the calibrated uniform
	// threshold from the dark cluster's nonuniformity distribution.
	uniformPercentile = 95
	uniformHeadroom   = 1.5

	// logoHistBins is the size of the folded histogram that detects
	// static scenes between logo learning observations.
	logoHistBins = 64
	// logoMaxKnownSteps bounds how many sample intervals one logo
	// observation vouches for.
	logoMaxKnownSteps = 3

	// Asset dimension limits.
	minLogoGeometry = 16
	maxLogoGeometry = 8192
	minBumperSide   = 8
	maxBumperSide   = 16384
	minBumperRange  = 8 // minimum luminance range of a usable template

	// selectionTieEpsilon is the relative difference below which two
	// unit-selection totals are a tie, so tie-breaks do not depend on the
	// order of floating-point additions.
	selectionTieEpsilon = 1e-12
	// regionEpsilon tolerates rounding in normalized region sums.
	regionEpsilon = 1e-6
)

// bitWords returns the number of 64-bit words holding n bits.
func bitWords(n int) int { return (n + wordMask) / wordBits }

func setBit(set []uint64, p int)      { set[p>>wordShift] |= 1 << (uint(p) & wordMask) }
func hasBit(set []uint64, p int) bool { return set[p>>wordShift]&(1<<(uint(p)&wordMask)) != 0 }
