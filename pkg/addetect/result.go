// SPDX-License-Identifier: Apache-2.0

package addetect

// SchemaVersion is the version of the canonical JSON result schema (OUT-01).
const SchemaVersion = "1"

// FeatureSchemaVersion identifies the extracted-measurement definitions
// (PIPE-03). It is part of the feature identity, independent of score
// weights.
const FeatureSchemaVersion = "1"

// ProductName is the product identifier reported in results.
const ProductName = "tv-ad-detect"

// Version is the library version reported in results.
const Version = "0.1.0"

// Labels of final segments (RESULT-01).
const (
	LabelAdvertisement = "advertisement"
	LabelProgram       = "program"
	LabelUnknown       = "unknown"
)

// Signal status values (PIPE-02).
const (
	StatusDisabled    = "disabled"
	StatusUnavailable = "unavailable"
	StatusUnreliable  = "unreliable"
	StatusUsable      = "usable"
)

// Signal names used in configuration and results.
const (
	SignalDarkUniform = "dark_uniform"
	SignalLogo        = "logo"
	SignalScene       = "scene"
	SignalResolution  = "resolution"
	SignalCaptions    = "captions"
	SignalAspect      = "aspect"
	SignalAudio       = "audio"
	SignalBumpers     = "bumpers"
)

// Result is the canonical analysis result. All times are integer
// microseconds relative to the first presented video frame; intervals are
// half-open [start_us, end_us) (TIME-02).
type Result struct {
	SchemaVersion string      `json:"schema_version"`
	Product       ProductInfo `json:"product"`
	// Complete is false when part of the presentation could not be
	// analyzed (degraded package input or a reported failure).
	Complete   bool         `json:"complete"`
	Error      *ErrorInfo   `json:"error,omitempty"`
	TimeUnit   string       `json:"time_unit"`
	DurationUS int64        `json:"duration_us"`
	Coverage   CoverageInfo `json:"coverage"`
	Input      InputInfo    `json:"input"`
	Tracks     TrackInfo    `json:"tracks"`
	Adapter    AdapterInfo  `json:"adapter"`
	Timing     TimingInfo   `json:"timing"`
	Package    *PackageInfo `json:"package,omitempty"`

	Config      ConfigIdentity  `json:"config"`
	FeatureID   string          `json:"feature_identity"`
	Detectors   []DetectorState `json:"detectors"`
	Warnings    []Warning       `json:"warnings"`
	Calibration Calibration     `json:"calibration"`
	Baselines   Baselines       `json:"baselines"`

	Segments      []Segment      `json:"segments"`
	AdBreaks      []AdBreak      `json:"ad_breaks"`
	AdUnits       []AdUnit       `json:"ad_units"`
	SkipIntervals []SkipInterval `json:"skip_intervals"`

	Diagnostics *Diagnostics `json:"diagnostics,omitempty"`
}

// ProductInfo identifies the software that produced a result.
type ProductInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Build   string `json:"build,omitempty"`
}

// ErrorInfo describes the failure of an explicitly partial result.
type ErrorInfo struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// TimeRange is a half-open interval.
type TimeRange struct {
	StartUS int64  `json:"start_us"`
	EndUS   int64  `json:"end_us"`
	Reason  string `json:"reason,omitempty"`
}

// CoverageInfo reports how much of the timeline was observed.
type CoverageInfo struct {
	VideoUS  int64       `json:"video_us"`
	Fraction float64     `json:"fraction"`
	Gaps     []TimeRange `json:"gaps"`
}

// AdapterInfo identifies the media adapter.
type AdapterInfo struct {
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	Libraries map[string]string `json:"libraries,omitempty"`
}

// MediaInfo is what a Stream reports about its media.
type MediaInfo struct {
	Adapter AdapterInfo
	Input   InputInfo
	Tracks  TrackInfo
	// DurationHintUS and FrameRateHint let the analysis pre-size compact
	// storage; zero when unknown.
	DurationHintUS int64
	FrameRateHint  float64
	// Incomplete is set by a source that could not read part of the
	// presentation (degraded package input); the result is then marked
	// incomplete.
	Incomplete bool
	// Package is set for HLS/DASH input. Sources may keep updating the
	// per-resource timing until Next returns io.EOF.
	Package *PackageInfo
}

// InputInfo identifies the analyzed input.
type InputInfo struct {
	Kind string `json:"kind"` // file, hls, dash or observations
	Path string `json:"path,omitempty"`
	// Identity is an adapter-defined content identity (for files: size and
	// modification time) used in the feature identity.
	Identity  string `json:"identity,omitempty"`
	Container string `json:"container,omitempty"`
}

// TrackInfo reports the chosen tracks (MEDIA-03).
type TrackInfo struct {
	// Program is the transport stream program number of the selected
	// streams; omitted for inputs without programs.
	Program  int              `json:"program,omitempty"`
	Video    VideoTrackInfo   `json:"video"`
	Audio    *AudioTrackInfo  `json:"audio"`
	Captions CaptionTrackInfo `json:"captions"`
}

// VideoTrackInfo describes the selected video track.
type VideoTrackInfo struct {
	Index int `json:"index"`
	// HardwareDecoder names the hardware decoder in use (input.hardware_decode).
	HardwareDecoder string `json:"hardware_decoder,omitempty"`
	Selection       string `json:"selection,omitempty"`
	Codec           string `json:"codec,omitempty"`
	Profile         string `json:"profile,omitempty"`
	PixelFormat     string `json:"pixel_format,omitempty"`
	BitDepth        int    `json:"bit_depth,omitempty"`
	CodedWidth      int    `json:"coded_width"`
	CodedHeight     int    `json:"coded_height"`
	SAR             string `json:"sample_aspect_ratio,omitempty"`
	ColorRange      string `json:"color_range,omitempty"`
	ColorTransfer   string `json:"color_transfer,omitempty"`
	FieldOrder      string `json:"field_order,omitempty"`
	FrameRate       string `json:"frame_rate,omitempty"`
	TimeBase        string `json:"time_base,omitempty"`
	RotationDeg     int    `json:"rotation_degrees,omitempty"`
	// Analysis geometry of the first frame; AnalysisScale pixels of coded
	// picture map to one analysis pixel (the spatial resolution limit).
	AnalysisWidth  int `json:"analysis_width"`
	AnalysisHeight int `json:"analysis_height"`
	AnalysisScale  int `json:"analysis_scale"`
}

// AudioTrackInfo describes the selected audio track.
type AudioTrackInfo struct {
	Index         int    `json:"index"`
	Selection     string `json:"selection,omitempty"`
	Codec         string `json:"codec,omitempty"`
	SampleRate    int    `json:"sample_rate,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	ChannelLayout string `json:"channel_layout,omitempty"`
}

// CaptionTrackInfo describes the caption source and capability.
type CaptionTrackInfo struct {
	// Source is embedded_cea608, track, caller or none.
	Source string `json:"source"`
	Index  int    `json:"index,omitempty"`
	// Capability is cea608 when presentation styles can be decoded,
	// unsupported for formats without style semantics, or none.
	Capability string `json:"capability"`
	Service    string `json:"service,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// TimingInfo keeps media timestamps and the timeline policy outcomes
// (TIME-02, TIME-03).
type TimingInfo struct {
	// NormalizationOffsetUS is subtracted from media presentation
	// timestamps to obtain result times: the first video frame's PTS.
	NormalizationOffsetUS       int64       `json:"normalization_offset_us"`
	Frames                      int         `json:"frames"`
	FinalFrameDurationUS        int64       `json:"final_frame_duration_us"`
	FinalFrameDurationEstimated bool        `json:"final_frame_duration_estimated"`
	Gaps                        []TimeRange `json:"gaps"`
	Resets                      int         `json:"timestamp_resets"`
	Overlaps                    int         `json:"timestamp_overlaps"`
	MissingTimestamps           int         `json:"missing_timestamps"`
	DecodeErrors                int         `json:"decode_errors"`
	CorruptFrames               int         `json:"corrupt_frames"`
	AudioStartOffsetUS          *int64      `json:"audio_start_offset_us"`
	DiscontinuityPolicy         string      `json:"discontinuity_policy"`
	MedianFrameDurationUS       int64       `json:"median_frame_duration_us"`
	Rebases                     []Rebase    `json:"rebases,omitempty"`
}

// Rebase records one timestamp reset that was re-based.
type Rebase struct {
	AtUS     int64 `json:"at_us"`
	OffsetUS int64 `json:"offset_us"`
}

// PackageInfo is the identity and resource mapping of packaged input
// (PACK-09).
type PackageInfo struct {
	Format    string             `json:"format"`
	Identity  string             `json:"identity"`
	Manifests []ManifestSnapshot `json:"manifests"`
	Video     PackageTrack       `json:"video"`
	Audio     *PackageTrack      `json:"audio"`
	Epochs    []PackageEpoch     `json:"epochs"`
	Inits     []PackageResource  `json:"initialization_resources"`
	Resources []PackageResource  `json:"resources"`
	Markers   []PackageMarker    `json:"manifest_markers,omitempty"`
	Notes     []string           `json:"notes,omitempty"`
}

// ManifestSnapshot identifies one manifest as read.
type ManifestSnapshot struct {
	URI    string `json:"uri"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// PackageTrack reports a selected variant/Representation.
type PackageTrack struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Language  string `json:"language,omitempty"`
	Codecs    string `json:"codecs,omitempty"`
	Bandwidth int64  `json:"bandwidth,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Rule      string `json:"rule"`
	Manifest  string `json:"manifest,omitempty"`
}

// PackageEpoch is an HLS discontinuity epoch or DASH Period.
type PackageEpoch struct {
	ID              string `json:"id"`
	DeclaredStartUS int64  `json:"declared_start_us"`
	DeclaredEndUS   *int64 `json:"declared_end_us"`
	VideoID         string `json:"video_id,omitempty"`
	AudioID         string `json:"audio_id,omitempty"`
	AudioAvailable  bool   `json:"audio_available"`
	// MediaOffsetUS maps this epoch's media timestamps to result time:
	// result = media_pts_us + media_offset_us. Null when no frame was read.
	MediaOffsetUS *int64 `json:"media_offset_us"`
}

// PackageResource maps one initialization or media resource to result time.
type PackageResource struct {
	Index  int    `json:"index"`
	Track  string `json:"track"`
	URI    string `json:"uri"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"` // -1: through end of resource
	Init   int    `json:"init"`   // index into initialization_resources, -1 if none
	Epoch  int    `json:"epoch"`
	// Declared times come from the manifest and are informational.
	DeclaredStartUS    int64 `json:"declared_start_us"`
	DeclaredDurationUS int64 `json:"declared_duration_us"`
	// StartUS/EndUS bound the samples actually presented from this
	// resource on the result timeline; null when none were.
	StartUS *int64 `json:"start_us"`
	EndUS   *int64 `json:"end_us"`
	Status  string `json:"status"` // ok, gap, missing or unread
	Reason  string `json:"reason,omitempty"`
}

// PackageMarker is a manifest ad marker retained as a diagnostic only.
type PackageMarker struct {
	Kind   string `json:"kind"`
	TimeUS *int64 `json:"time_us"`
	Detail string `json:"detail,omitempty"`
}

// DetectorState reports one signal family's availability (PIPE-02).
type DetectorState struct {
	Signal string `json:"signal"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// CoverageUS is the duration with known observations of this signal;
	// Coverage is its ratio to the analyzed duration. Null when the signal
	// produced no measurement.
	CoverageUS *int64         `json:"coverage_us"`
	Coverage   *float64       `json:"coverage"`
	Events     int            `json:"events"`
	Boundary   bool           `json:"boundary_source"`
	Details    map[string]any `json:"details,omitempty"`
}

// Warning is a non-fatal condition.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	TimeUS  *int64 `json:"time_us,omitempty"`
}

// Calibration summarizes detector calibration and validation.
type Calibration struct {
	Dark    DarkCalibration    `json:"dark_uniform"`
	Logo    []LogoCalibration  `json:"logo"`
	Score   ScoreCalibration   `json:"score"`
	Silence SilenceCalibration `json:"silence"`
}

// SilenceCalibration reports the silence level in use (audio.silence_mode).
type SilenceCalibration struct {
	Mode           string   `json:"mode"`
	LevelDBFS      float64  `json:"level_dbfs"`
	Estimated      bool     `json:"estimated"`
	FloorDBFS      *float64 `json:"floor_dbfs,omitempty"`
	FallbackReason string   `json:"fallback_reason,omitempty"`
}

// ScoreCalibration reports the advertisement score threshold in use: the
// configured threshold, or the duration-weighted percentile of block scores
// when scoring.threshold_percentile is set and that is higher.
type ScoreCalibration struct {
	Threshold  float64 `json:"threshold"`
	Percentile float64 `json:"percentile"`
	Effective  float64 `json:"effective_threshold"`
}

// DarkCalibration reports the dark/uniform thresholds in use (VIS-02).
type DarkCalibration struct {
	Mode             string  `json:"mode"`
	DarkThreshold    float64 `json:"dark_threshold"`
	UniformThreshold float64 `json:"uniform_threshold"`
	Estimated        bool    `json:"estimated"`
	SupportFrames    int     `json:"support_frames"`
	UsableFrames     int     `json:"usable_frames"`
	FallbackReason   string  `json:"fallback_reason,omitempty"`
}

// LogoCalibration reports logo learning and validation for one span of
// constant analysis geometry (LOGO-01, LOGO-04).
type LogoCalibration struct {
	StartUS      int64   `json:"start_us"`
	EndUS        int64   `json:"end_us"`
	Origin       string  `json:"origin"` // learned or asset
	Status       string  `json:"status"`
	Failed       string  `json:"validation_failed,omitempty"`
	Samples      int     `json:"samples"`
	LearnSamples int     `json:"learn_samples"`
	EdgePixels   int     `json:"edge_pixels"`
	Box          *Region `json:"box"`
	// PresentThreshold and AbsentThreshold are the match-measure thresholds
	// in use (relative mode derives them from Reference, the recording's own
	// logo level).
	PresentThreshold *float64 `json:"present_threshold,omitempty"`
	AbsentThreshold  *float64 `json:"absent_threshold,omitempty"`
	Reference        *float64 `json:"reference_level,omitempty"`
	Occupancy        *float64 `json:"occupancy"`
	Coverage         *float64 `json:"coverage"`
	AssetVersion     string   `json:"asset_version,omitempty"`
}

// Baseline reports one frozen baseline (BASE-03).
type Baseline struct {
	Value     *float64 `json:"value"`
	Style     string   `json:"style,omitempty"`
	Unit      string   `json:"unit,omitempty"`
	SupportUS int64    `json:"support_us"`
	Dominance *float64 `json:"dominance,omitempty"`
	Origin    string   `json:"origin"`
	Reliable  bool     `json:"reliable"`
	Reason    string   `json:"reason,omitempty"`
}

// Baselines are the recording-relative and program baselines.
type Baselines struct {
	Brightness      Baseline `json:"brightness"`
	Nonuniformity   Baseline `json:"nonuniformity"`
	AudioEnergy     Baseline `json:"audio_energy"`
	SilenceFraction Baseline `json:"silence_fraction"`
	SceneRate       Baseline `json:"scene_rate"`
	CaptionStyle    Baseline `json:"caption_style"`
	AspectRatio     Baseline `json:"aspect_ratio"`
}

// Segment is one interval of the final classification timeline (OUT-02).
type Segment struct {
	StartUS  int64    `json:"start_us"`
	EndUS    int64    `json:"end_us"`
	Label    string   `json:"label"`
	BlockIDs []string `json:"block_ids"`
	Reasons  []string `json:"reasons"`
	// Coverage is the ratio of the interval with decoded video.
	Coverage float64  `json:"coverage"`
	BreakID  string   `json:"break_id,omitempty"`
	UnitIDs  []string `json:"unit_ids,omitempty"`
}

// AdUnit is an accepted ad-unit hypothesis.
type AdUnit struct {
	ID               string   `json:"id"`
	BreakID          string   `json:"break_id"`
	StartUS          int64    `json:"start_us"`
	EndUS            int64    `json:"end_us"`
	BlockIDs         []string `json:"block_ids"`
	HypothesisID     string   `json:"hypothesis_id"`
	Score            float64  `json:"score"`
	Factors          []Factor `json:"factors"`
	EvidenceFamilies []string `json:"evidence_families"`
	StartBoundaryID  string   `json:"start_boundary_id"`
	EndBoundaryID    string   `json:"end_boundary_id"`
	Bridged          bool     `json:"bridged_gap,omitempty"`
}

// AdBreak is an accepted advertisement break (RESULT-02).
type AdBreak struct {
	ID             string      `json:"id"`
	StartUS        int64       `json:"start_us"`
	EndUS          int64       `json:"end_us"`
	UnitIDs        []string    `json:"unit_ids"`
	TruncatedStart bool        `json:"truncated_start"`
	TruncatedEnd   bool        `json:"truncated_end"`
	BoundaryIDs    []string    `json:"boundary_ids"`
	BridgedGaps    []TimeRange `json:"bridged_gaps,omitempty"`
}

// SkipInterval is a derived skip interval (BREAK-05); it never changes the
// canonical segments.
type SkipInterval struct {
	StartUS        int64    `json:"start_us"`
	EndUS          int64    `json:"end_us"`
	BreakIDs       []string `json:"break_ids"`
	PaddingStartUS int64    `json:"padding_start_us"`
	PaddingEndUS   int64    `json:"padding_end_us"`
	Clamped        bool     `json:"clamped"`
	Merged         bool     `json:"merged"`
}

// Factor is one applied score modifier (SCORE-02).
type Factor struct {
	Family     string   `json:"family"`
	Rule       string   `json:"rule"`
	Multiplier float64  `json:"multiplier"`
	Value      *float64 `json:"value,omitempty"`
	Baseline   *float64 `json:"baseline,omitempty"`
	Coverage   *float64 `json:"coverage,omitempty"`
	// Duration rules report the match (SCORE-03).
	TargetUS    *int64 `json:"target_us,omitempty"`
	MeasuredUS  *int64 `json:"measured_us,omitempty"`
	DeviationUS *int64 `json:"deviation_us,omitempty"`
	ToleranceUS *int64 `json:"tolerance_us,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// Diagnostics holds detailed evidence (OUT-01); present at diagnostics
// levels "detailed" and "full".
type Diagnostics struct {
	Level               string        `json:"level"`
	Boundaries          []Boundary    `json:"boundaries"`
	Blocks              []Block       `json:"blocks"`
	Hypotheses          []Hypothesis  `json:"hypotheses"`
	HypothesesEvaluated int           `json:"hypotheses_evaluated"`
	HypothesesTruncated bool          `json:"hypotheses_truncated"`
	RejectedRuns        []RejectedRun `json:"rejected_runs"`
	Events              EventLists    `json:"events"`
}

// Boundary is a fused candidate boundary (BOUND-03).
type Boundary struct {
	ID            string          `json:"id"`
	TimeUS        int64           `json:"time_us"`
	UncertaintyUS int64           `json:"uncertainty_us"`
	Causes        []BoundaryCause `json:"causes"`
	// Evidence lists the boundary-evidence families this boundary supplies
	// to the SCORE-06 gate: dark_uniform_boundary, av_corroborated_boundary.
	Evidence []string `json:"evidence"`
}

// BoundaryCause links a boundary to a contributing event (PIPE-04).
type BoundaryCause struct {
	Signal string `json:"signal"`
	Kind   string `json:"kind"`
	// Role is "boundary" for a candidate source and "corroboration" for an
	// aligned event that supports without moving the boundary.
	Role         string `json:"role"`
	EventStartUS int64  `json:"event_start_us"`
	EventEndUS   int64  `json:"event_end_us"`
	CandidateUS  int64  `json:"candidate_us"`
	Detail       string `json:"detail,omitempty"`
}

// Block is an interval between consecutive boundaries with its aggregated
// measurements (BLOCK-01). Null measurements were not observed.
type Block struct {
	ID              string  `json:"id"`
	StartUS         int64   `json:"start_us"`
	EndUS           int64   `json:"end_us"`
	StartBoundaryID string  `json:"start_boundary_id"`
	EndBoundaryID   string  `json:"end_boundary_id"`
	Missing         bool    `json:"missing_coverage"`
	Coverage        float64 `json:"coverage"`

	Brightness      *float64         `json:"brightness"`
	Nonuniformity   *float64         `json:"nonuniformity"`
	SceneChanges    *int             `json:"scene_changes"`
	SceneRate       *float64         `json:"scene_rate"`
	LogoOccupancy   *float64         `json:"logo_occupancy"`
	LogoKnownUS     *int64           `json:"logo_known_us"`
	CodedWidth      int              `json:"coded_width"`
	CodedHeight     int              `json:"coded_height"`
	ActiveRatio     *float64         `json:"active_ratio"`
	AspectKnownUS   *int64           `json:"aspect_known_us"`
	CaptionStylesUS map[string]int64 `json:"caption_styles_us"`
	CaptionStyle    string           `json:"caption_style"`
	AudioEnergyDBFS *float64         `json:"audio_energy_dbfs"`
	SilenceFraction *float64         `json:"silence_fraction"`
	AudioKnownUS    *int64           `json:"audio_known_us"`

	Score           float64  `json:"score"`
	Factors         []Factor `json:"factors"`
	ProgramEvidence []string `json:"program_evidence"`
	Label           string   `json:"label"`
	Reasons         []string `json:"reasons"`
}

// Hypothesis is one evaluated ad-unit hypothesis (SCORE-04).
type Hypothesis struct {
	ID               string   `json:"id"`
	StartUS          int64    `json:"start_us"`
	EndUS            int64    `json:"end_us"`
	BlockIDs         []string `json:"block_ids"`
	Score            float64  `json:"score"`
	Factors          []Factor `json:"factors"`
	EvidenceFamilies []string `json:"evidence_families"`
	Coverage         float64  `json:"coverage"`
	Eligible         bool     `json:"eligible"`
	Selected         bool     `json:"selected"`
	Reasons          []string `json:"reasons"`
}

// RejectedRun is a run of selected units that violated a break constraint
// (BREAK-02); its time is unknown in the final timeline.
type RejectedRun struct {
	StartUS       int64    `json:"start_us"`
	EndUS         int64    `json:"end_us"`
	HypothesisIDs []string `json:"hypothesis_ids"`
	Reason        string   `json:"reason"`
}

// EventLists holds detector events (PIPE-02).
type EventLists struct {
	DarkUniform []DarkEvent       `json:"dark_uniform"`
	Silence     []SilenceEvent    `json:"silence"`
	Scene       []SceneEvent      `json:"scene"`
	Resolution  []ResolutionEvent `json:"resolution"`
	Aspect      []AspectInterval  `json:"aspect"`
	Logo        []LogoInterval    `json:"logo"`
	Captions    []CaptionInterval `json:"captions"`
	Bumpers     []BumperEvent     `json:"bumpers"`
	// Phrases are the opt-in caption phrase matches (empty unless phrase
	// lists are configured).
	Phrases []PhraseHit `json:"phrases"`
}

// DarkEvent is a run of dark or uniform frames (VIS-03).
type DarkEvent struct {
	StartUS           int64   `json:"start_us"`
	EndUS             int64   `json:"end_us"`
	Kind              string  `json:"kind"` // dark or uniform
	Frames            int     `json:"frames"`
	MinBrightness     float64 `json:"min_brightness"`
	MeanBrightness    float64 `json:"mean_brightness"`
	MeanNonuniformity float64 `json:"mean_nonuniformity"`
	Coverage          float64 `json:"coverage"`
}

// SilenceEvent is a coalesced silence span (AUDIO-02).
type SilenceEvent struct {
	StartUS  int64   `json:"start_us"`
	EndUS    int64   `json:"end_us"`
	PeakDBFS float64 `json:"peak_dbfs"` // loudest window (channel maximum); floor -160
	Windows  int     `json:"windows"`
}

// PhraseHit is one match of a configured caption phrase (opt-in). Only the
// configured phrase is reported, never the caption text around it.
type PhraseHit struct {
	TimeUS int64  `json:"time_us"`
	List   string `json:"list"` // advertisement or program
	Phrase string `json:"phrase"`
}

// SceneEvent is a detected scene change (SCENE-01).
type SceneEvent struct {
	TimeUS          int64   `json:"time_us"`
	Kind            string  `json:"kind"` // cut or gradual
	Distance        float64 `json:"distance"`
	BrightnessDelta float64 `json:"brightness_delta"`
}

// ResolutionEvent is a coded size change (SIZE-01).
type ResolutionEvent struct {
	TimeUS    int64 `json:"time_us"`
	OldWidth  int   `json:"old_width"`
	OldHeight int   `json:"old_height"`
	NewWidth  int   `json:"new_width"`
	NewHeight int   `json:"new_height"`
}

// AspectInterval is a span of confirmed active-picture ratio; a null ratio
// means unknown (ASPECT-02).
type AspectInterval struct {
	StartUS int64    `json:"start_us"`
	EndUS   int64    `json:"end_us"`
	Ratio   *float64 `json:"ratio"`
	// Active picture in coded pixels (resolution limited by analysis_scale).
	Left   int `json:"left"`
	Top    int `json:"top"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// LogoInterval is a span of smoothed logo presence (LOGO-03).
type LogoInterval struct {
	StartUS int64  `json:"start_us"`
	EndUS   int64  `json:"end_us"`
	State   string `json:"state"` // present, absent or unknown
	// TransitionUncertaintyUS bounds the error of the interval start.
	TransitionUncertaintyUS int64   `json:"transition_uncertainty_us"`
	MeanMatch               float64 `json:"mean_match"`
}

// CaptionInterval is a span of caption presentation style (CAP-01).
type CaptionInterval struct {
	StartUS int64  `json:"start_us"`
	EndUS   int64  `json:"end_us"`
	Style   string `json:"style"`
}

// BumperEvent is a template match (BUMPER-02).
type BumperEvent struct {
	TemplateID    string  `json:"template_id"`
	StartUS       int64   `json:"start_us"`
	EndUS         int64   `json:"end_us"`
	Quality       float64 `json:"quality"`
	UncertaintyUS int64   `json:"uncertainty_us"`
}

func ptr[T any](v T) *T { return &v }
