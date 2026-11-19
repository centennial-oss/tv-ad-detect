// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"math"
	"path/filepath"
	"runtime"
	"time"
)

// ConfigSchemaVersion identifies the configuration surface and its defaults
// (CFG-04). It changes whenever a key or a default changes.
const ConfigSchemaVersion = "1"

// Region is a rectangle in normalized frame coordinates (0..1).
type Region struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// ByteSize is a size in bytes; textual values accept B, KiB, MiB and GiB suffixes.
type ByteSize int64

// Config is the complete typed configuration (CFG-01). Obtain defaults with
// DefaultConfig and modify fields, or layer YAML files with LoadYAML. A Config
// is not modified by Analyze and may be reused for concurrent analyses.
//
// Struct tags drive the configuration-file mapping: `cfg` is the section or
// key name, `desc` the documentation emitted by WriteDefaultYAML, `min`/`max`
// the inclusive range, `path` marks file paths, `nodigest` sections excluded
// from the configuration digest and `feature` the sections and keys that
// affect extracted measurements. Allowed values of enumerated keys are bound
// to the constants of constants.go by enumBindings.
type Config struct {
	Input      InputConfig    `cfg:"input" feature:"1"`
	Package    PackageConfig  `cfg:"package" feature:"1"`
	Sampling   SamplingConfig `cfg:"sampling" feature:"1"`
	Signals    SignalsConfig  `cfg:"signals" feature:"1"`
	Dark       DarkConfig     `cfg:"dark"`
	Logo       LogoConfig     `cfg:"logo"`
	Scene      SceneConfig    `cfg:"scene" feature:"1"`
	Aspect     AspectConfig   `cfg:"aspect" feature:"1"`
	Captions   CaptionConfig  `cfg:"captions" feature:"1"`
	Audio      AudioConfig    `cfg:"audio"`
	Bumpers    BumperConfig   `cfg:"bumpers" feature:"1"`
	Boundaries BoundaryConfig `cfg:"boundaries"`
	Baselines  BaselineConfig `cfg:"baselines"`
	Scoring    ScoringConfig  `cfg:"scoring"`
	Breaks     BreakConfig    `cfg:"breaks"`
	Outputs    OutputConfig   `cfg:"outputs" nodigest:"1"`
	Resources  ResourceConfig `cfg:"resources" nodigest:"1"`

	// BumperTemplates are the known bumper templates (BUMPER-01), from the
	// bumpers.templates mapping of a YAML layer or set directly by the caller.
	BumperTemplates []BumperTemplate
	// LogoAssetData optionally supplies a logo asset directly (LOGO-02);
	// it takes precedence over Logo.Asset.
	LogoAssetData *LogoAsset
}

// InputConfig selects tracks and decoding policy.
type InputConfig struct {
	HardwareDecode       string        `cfg:"hardware_decode" desc:"off, or auto: decode video in hardware where the platform offers it (VideoToolbox on macOS), falling back to software. It lowers CPU use, not necessarily wall time; measurements are the same."`
	Program              int           `cfg:"program" min:"0" max:"65535" desc:"Program number of a multi-program transport stream to analyze (automatic track selection stays inside it); 0 = the first suitable streams of any program."`
	VideoTrack           string        `cfg:"video_track" desc:"Video track: auto (first non-attached-picture video stream) or a container stream index."`
	AudioTrack           string        `cfg:"audio_track" desc:"Audio track: auto (default-flagged, else first audio stream), none, or a container stream index."`
	CaptionTrack         string        `cfg:"caption_track" desc:"Caption source: auto (the first CEA-608 caption track when present, else CEA-608 embedded in the video stream), embedded, none, or a container stream index."`
	Decoder              string        `cfg:"decoder" desc:"Media adapter used for path inputs."`
	UnsupportedFormat    string        `cfg:"unsupported_format_policy" desc:"HDR transfer or other unsupported processing: warn = analyze in degraded mode with a warning; fail = capability error."`
	OptionalStreams      string        `cfg:"optional_stream_policy" desc:"A requested audio/caption track that is unavailable: degraded = warn and disable that evidence; strict = fail."`
	TimestampGap         time.Duration `cfg:"timestamp_gap_threshold" min:"1ms" max:"1h" desc:"A forward jump between a frame's expected end and the next frame larger than this is a coverage gap (unknown time)."`
	TimestampResetPolicy string        `cfg:"timestamp_reset_policy" desc:"Backward timestamp jump: rebase = continue directly after the previous frame and warn; fail = timing error."`
	MaxMissingTimestamps float64       `cfg:"max_missing_timestamp_ratio" min:"0" max:"1" desc:"Fail with a timing error when more than this ratio of frames carries no timestamp (missing ones are interpolated)."`
	SkipLoopFilter       string        `cfg:"skip_loop_filter" desc:"Decoder in-loop deblocking skipped for: none, non-reference frames (no drift), or all frames (fastest, slight drift)."`
	MaxDecodeErrorRatio  float64       `cfg:"max_decode_error_ratio" min:"0" max:"1" desc:"Fail with a media error when more than this ratio of video packets fails to decode."`
}

// PackageConfig controls HLS/DASH package input (section 3.1).
type PackageConfig struct {
	ManifestType     string   `cfg:"manifest_type" desc:"Manifest type; auto detects by content."`
	VideoSelection   string   `cfg:"video_selection" desc:"Variant/Representation: highest, lowest, index=<n>, id=<id>, bandwidth=<bps>."`
	AudioSelection   string   `cfg:"audio_selection" desc:"Audio rendition: auto, none, lang=<code>, name=<name>, id=<id>."`
	MinHeight        int      `cfg:"min_height" min:"0" max:"8640" desc:"Smallest acceptable picture height for video_selection=lowest."`
	FailurePolicy    string   `cfg:"failure_policy" desc:"Unreadable resource: fail the analysis, or (degraded) keep its interval unknown and mark the result incomplete."`
	AllowRemote      bool     `cfg:"allow_remote" desc:"Permit non-local resource URIs (requires a caller-provided resolver; the built-in resolver is local only)."`
	MaxManifestBytes ByteSize `cfg:"max_manifest_bytes" min:"1KiB" max:"1GiB" desc:"Largest manifest that will be read."`
	MaxSegments      int      `cfg:"max_segments" min:"1" max:"100000000" desc:"Largest number of media resources per track."`
	IOBufferSize     ByteSize `cfg:"io_buffer_size" min:"4KiB" max:"64MiB" desc:"Read buffer per open track; at most two tracks are open at a time."`
	HashMedia        bool     `cfg:"hash_media" desc:"Package identity covers a SHA-256 of every media resource's bytes as read (catches a replaced resource of the same size and time). Off: each resource's size and modification time, from the resolver."`
}

// SamplingConfig controls the canonical analysis image.
type SamplingConfig struct {
	AnalysisMaxWidth   int    `cfg:"analysis_max_width" min:"64" max:"1920" desc:"Maximum width of the canonical luminance analysis image; pictures are reduced by an integer box-filter factor (reported as analysis_scale)."`
	AnalysisMaxHeight  int    `cfg:"analysis_max_height" min:"36" max:"1080" desc:"Maximum height of the canonical luminance analysis image."`
	Region             Region `cfg:"region" desc:"Measurement region [x, y, w, h] (normalized 0..1) for dark/uniform and scene measurements."`
	FullResBrightCheck bool   `cfg:"full_resolution_bright_check" desc:"Measure the bright-pixel safeguard on every full-resolution frame instead of the reduced analysis image."`
	DecodeFrames       string `cfg:"decode_frames" desc:"Visual sampling: all = every frame; reference = decode only reference frames (coarse: less CPU, timing limited to the reference-frame spacing, embedded caption data unavailable; reported as a warning)."`
}

// SignalsConfig holds the independent detector switches.
type SignalsConfig struct {
	DarkUniform bool `cfg:"dark_uniform" desc:"Black and uniform frame detection."`
	Logo        bool `cfg:"logo" desc:"Station logo presence."`
	Scene       bool `cfg:"scene" desc:"Scene-change measurement."`
	Resolution  bool `cfg:"resolution" desc:"Coded resolution change detection."`
	Captions    bool `cfg:"captions" desc:"Caption presentation style."`
	Aspect      bool `cfg:"aspect" desc:"Active-picture aspect ratio."`
	Audio       bool `cfg:"audio" desc:"Silence and audio level."`
	Bumpers     bool `cfg:"bumpers" desc:"Known bumper template matching (reported unavailable when no template is supplied)."`
}

// DarkConfig controls black/uniform frame detection (5.1).
type DarkConfig struct {
	Mode                 string  `cfg:"mode" desc:"fixed thresholds, or auto calibration from the recording with fixed fallback."`
	MaxMeanLuma          float64 `cfg:"max_mean_luma" min:"0" max:"255" desc:"Fixed maximum mean luminance (0-255) of a dark frame."`
	BrightPixelLuma      int     `cfg:"bright_pixel_luma" feature:"1" min:"1" max:"255" desc:"Luminance (0-255) above which a pixel counts as bright for the dark safeguard."`
	MaxBrightFraction    float64 `cfg:"max_bright_fraction" min:"0" max:"1" desc:"Ratio of bright pixels above which a frame is not dark."`
	DarkMaxNonuniformity float64 `cfg:"dark_max_nonuniformity" min:"0" max:"128" desc:"Luminance standard deviation (0-255 units) above which a frame is not dark."`
	IgnoreLogoRegion     bool    `cfg:"ignore_logo_region" desc:"A frame that is not dark is tested again without the search region holding the validated logo, so a logo kept on screen over a black separator does not hide it."`
	DetectUniform        bool    `cfg:"detect_uniform" desc:"Detect nearly solid (non-dark) frames as a separate event kind."`
	UniformMaxNonunif    float64 `cfg:"uniform_max_nonuniformity" min:"0" max:"128" desc:"Fixed maximum luminance standard deviation of a uniform frame."`
	AutoMinLuma          float64 `cfg:"auto_min_luma" min:"0" max:"255" desc:"Lower bound of the calibrated dark threshold."`
	AutoMaxLuma          float64 `cfg:"auto_max_luma" min:"0" max:"255" desc:"Upper bound of the calibrated dark threshold."`
	AutoMargin           float64 `cfg:"auto_margin" min:"0" max:"32" desc:"Margin added above the darkest frame cluster when calibrating."`
	AutoMinFrames        int     `cfg:"auto_min_frames" min:"1" max:"100000000" desc:"Minimum usable frames required to attempt calibration."`
	AutoMinSupport       int     `cfg:"auto_min_support" min:"1" max:"100000000" desc:"Minimum frames in the darkest cluster for a stable estimate."`
	AutoUniformMin       float64 `cfg:"auto_uniform_min" min:"0" max:"128" desc:"Lower bound of the calibrated uniform threshold."`
	AutoUniformMax       float64 `cfg:"auto_uniform_max" min:"0" max:"128" desc:"Upper bound of the calibrated uniform threshold."`
}

// LogoConfig controls station-logo learning and presence (5.2). Learning
// keys affect measurements (feature keys); the presence decision keys
// (thresholds, smoothing, validation) are applied during classification and
// may change when saved features are classified again.
type LogoConfig struct {
	SearchRegions        []Region      `cfg:"search_regions" feature:"1" desc:"Logo search regions, a list of [x, y, w, h] (normalized)."`
	SampleInterval       time.Duration `cfg:"sample_interval" feature:"1" min:"10ms" max:"1m" desc:"Spacing of logo observations."`
	LearnMinSeparation   time.Duration `cfg:"learn_min_separation" feature:"1" min:"0s" max:"10m" desc:"Minimum spacing between observations used for learning."`
	LearnMinChange       float64       `cfg:"learn_min_change" feature:"1" min:"0" max:"1" desc:"Minimum luminance-histogram distance from the previous learning observation (rejects static scenes)."`
	EdgeThreshold        float64       `cfg:"edge_threshold" feature:"1" min:"1" max:"255" desc:"Sobel gradient magnitude (|gx|+|gy|)/8 in luminance units above which a pixel is an edge."`
	MinPersistence       float64       `cfg:"min_persistence" feature:"1" min:"0.01" max:"1" desc:"Ratio of learning observations in which a pixel must be an edge to belong to the logo."`
	MinLearnSamples      int           `cfg:"min_learn_samples" feature:"1" min:"2" max:"1000000" desc:"Minimum learning observations (learning support)."`
	MinEdgePixels        int           `cfg:"min_edge_pixels" feature:"1" min:"1" max:"100000" desc:"Minimum persistent edge pixels of a candidate."`
	MinWidth             float64       `cfg:"min_width" feature:"1" min:"0" max:"1" desc:"Minimum candidate width as a ratio of frame width."`
	MaxWidth             float64       `cfg:"max_width" feature:"1" min:"0" max:"1" desc:"Maximum candidate width as a ratio of frame width."`
	MinHeight            float64       `cfg:"min_height" feature:"1" min:"0" max:"1" desc:"Minimum candidate height as a ratio of frame height."`
	MaxHeight            float64       `cfg:"max_height" feature:"1" min:"0" max:"1" desc:"Maximum candidate height as a ratio of frame height."`
	ClusterGap           float64       `cfg:"cluster_gap" feature:"1" min:"0" max:"0.5" desc:"Persistent edge groups closer than this ratio of frame width are merged into one candidate."`
	PresentThreshold     float64       `cfg:"present_threshold" min:"0" max:"1" desc:"Match measure (ratio of logo edge pixels found) at or above which the logo is present."`
	AbsentThreshold      float64       `cfg:"absent_threshold" min:"0" max:"1" desc:"Match measure at or below which the logo is absent; between the thresholds the previous state is kept."`
	MinSpecificity       float64       `cfg:"min_specificity" min:"0" max:"1" desc:"A present match must exceed the best match of the mask moved 3 analysis pixels by this share of the mask (text or texture in the logo's place matches a moved mask too); otherwise the observation is unknown. 0 = off."`
	MaxBackgroundDensity float64       `cfg:"max_background_density" min:"0" max:"1" desc:"When more than this ratio of the non-logo pixels in the logo box are edges, the picture is too textured to judge and the observation is unknown."`
	MinStateDuration     time.Duration `cfg:"min_state_duration" min:"0s" max:"10m" desc:"A presence state change must persist this long (time-based smoothing)."`
	MinPresentDuration   time.Duration `cfg:"min_present_duration" min:"0s" max:"10m" desc:"Smoothing for present runs only; 0 = min_state_duration. Longer than min_absent_duration makes the logo slower to turn on than off."`
	MinAbsentDuration    time.Duration `cfg:"min_absent_duration" min:"0s" max:"10m" desc:"Smoothing for absent runs only; 0 = min_state_duration."`
	PresentTrim          time.Duration `cfg:"present_trim" min:"0s" max:"1m" desc:"Each present run gives up this much at both ends to unknown, so the logo's fade in and out never vouches for the neighbouring content (0 = off)."`
	MinOccupancy         float64       `cfg:"min_occupancy" min:"0" max:"1" desc:"Validation: minimum recording-level presence ratio."`
	MaxOccupancy         float64       `cfg:"max_occupancy" min:"0" max:"1" desc:"Validation: maximum recording-level presence ratio."`
	AmbiguousState       string        `cfg:"ambiguous_state" desc:"A match measure between absent_threshold and present_threshold keeps the previous state (hold) or is unknown."`
	ThresholdMode        string        `cfg:"threshold_mode" desc:"fixed: present_threshold and absent_threshold as given. relative: each a share of the recording's own logo level (the reference_quantile of the candidate's match measures)."`
	ReferenceQuantile    float64       `cfg:"reference_quantile" min:"0.5" max:"1" desc:"relative mode: quantile of the usable match measures taken as the logo's own level."`
	RelativePresent      float64       `cfg:"relative_present" min:"0" max:"1" desc:"relative mode: present at or above this share of the logo level."`
	RelativeAbsent       float64       `cfg:"relative_absent" min:"0" max:"1" desc:"relative mode: absent at or below this share of the logo level."`
	MinCoverage          float64       `cfg:"min_coverage" min:"0" max:"1" desc:"Validation: minimum ratio of video time with a known logo state."`
	Asset                string        `cfg:"asset" feature:"1" path:"1" desc:"Path of a logo asset (tvad-logo JSON); empty = auto-learn."`
	CacheDir             string        `cfg:"cache_dir" path:"1" desc:"Directory of saved learned logos, one per cache_key; empty = no cache. With a cache entry and no asset, the entry is used as the asset; a missing or unusable entry falls back to learning (with a warning for an unusable one). The library only reads the cache; the command line also saves a newly validated learned logo there."`
	CacheKey             string        `cfg:"cache_key" desc:"Name of this recording's entry in cache_dir, normally the channel (letters, digits, dot, dash, underscore)."`
}

// SceneConfig controls scene-change detection (5.3).
type SceneConfig struct {
	HistogramBins    int           `cfg:"histogram_bins" desc:"Luminance histogram bins."`
	CutThreshold     float64       `cfg:"cut_threshold" min:"0" max:"1" desc:"Hard cut: total-variation distance (0..1) between adjacent normalized histograms."`
	GradualThreshold float64       `cfg:"gradual_threshold" min:"0" max:"1" desc:"Gradual change: distance across gradual_window without a hard cut."`
	GradualWindow    time.Duration `cfg:"gradual_window" min:"0s" max:"10s" desc:"Span compared for gradual changes; 0 disables gradual detection."`
	MinSpacing       time.Duration `cfg:"min_spacing" min:"0s" max:"1m" desc:"Duplicate suppression: minimum spacing of scene events."`
}

// AspectConfig controls active-picture detection (5.6).
type AspectConfig struct {
	BorderMaxLuma      float64       `cfg:"border_max_luma" min:"0" max:"255" desc:"Maximum mean luminance of a border row/column."`
	BorderMaxDeviation float64       `cfg:"border_max_deviation" min:"0" max:"255" desc:"Maximum luminance above the row/column mean inside a border (spatial consistency)."`
	SampleInterval     time.Duration `cfg:"sample_interval" min:"0s" max:"1m" desc:"Spacing of active-picture observations (0 = every frame)."`
	Persistence        time.Duration `cfg:"persistence" min:"0s" max:"10m" desc:"A changed ratio must persist this long to be confirmed."`
	RatioTolerance     float64       `cfg:"ratio_tolerance" min:"0.001" max:"1" desc:"Relative ratio difference treated as a change or mismatch."`
	MinActiveFraction  float64       `cfg:"min_active_fraction" min:"0.05" max:"1" desc:"Crops leaving less than this ratio of width or height are invalid."`
	SymmetryTolerance  float64       `cfg:"symmetry_tolerance" min:"0" max:"1" desc:"Opposite borders may differ by at most this ratio of the frame dimension; asymmetric estimates are unknown."`
}

// CaptionConfig controls caption-style evidence (5.5).
type CaptionConfig struct {
	Service          string        `cfg:"service" desc:"CEA-608 caption service."`
	InactiveTimeout  time.Duration `cfg:"inactive_timeout" min:"100ms" max:"10m" desc:"No caption activity for this long means observed absence (none)."`
	DataTimeout      time.Duration `cfg:"data_timeout" min:"100ms" max:"10m" desc:"No caption data at all for this long means unknown."`
	AdPhrases        []string      `cfg:"ad_phrases" desc:"Opt-in phrase matching: phrases typical of advertisements (drug names, \"ask your doctor if\", slogans). Matched case-insensitively in the decoded caption text of the service, ignoring punctuation and across line breaks; caption text is never stored or reported. Empty = off. Commas cannot appear inside a phrase."`
	ProgramPhrases   []string      `cfg:"program_phrases" desc:"Opt-in phrase matching: phrases typical of the program (a show's catchphrase or credits). As ad_phrases."`
	PhraseRepeat     time.Duration `cfg:"phrase_repeat" min:"0s" max:"10m" desc:"A phrase matched again within this time counts once."`
	StylePersistence time.Duration `cfg:"style_persistence" min:"0s" max:"10m" desc:"Style intervals shorter than this are absorbed by the preceding interval."`
}

// AudioConfig controls silence and level measurement (5.7).
type AudioConfig struct {
	Metric           string        `cfg:"metric" feature:"1" desc:"Energy metric: per-channel RMS in dBFS."`
	ChannelPolicy    string        `cfg:"channel_policy" feature:"1" desc:"Channels included in levels. A window is silent only when every included channel is below the silence level."`
	Window           time.Duration `cfg:"window" feature:"1" min:"1ms" max:"1s" desc:"Fixed measurement window."`
	SilenceLevel     float64       `cfg:"silence_level_dbfs" min:"-160" max:"0" desc:"Silence threshold in dBFS."`
	MinSilence       time.Duration `cfg:"min_silence" min:"0s" max:"1m" desc:"Minimum duration of a silence event."`
	SilenceMode      string        `cfg:"silence_mode" desc:"fixed: silence_level_dbfs. auto: estimate the recording's silence floor from its quietest windows (bounded by auto_min_dbfs..auto_max_dbfs), falling back to the fixed level with a reason."`
	AutoQuantile     float64       `cfg:"auto_quantile" min:"0" max:"0.5" desc:"auto mode: duration-weighted quantile of window levels taken as the silence floor."`
	AutoMarginDB     float64       `cfg:"auto_margin_db" min:"0" max:"40" desc:"auto mode: the silence level is this many dB above the floor."`
	AutoMinDBFS      float64       `cfg:"auto_min_dbfs" min:"-160" max:"0" desc:"auto mode: lowest allowed silence level."`
	AutoMaxDBFS      float64       `cfg:"auto_max_dbfs" min:"-160" max:"0" desc:"auto mode: highest allowed silence level."`
	SilenceContext   time.Duration `cfg:"silence_context" min:"0s" max:"10s" desc:"A silence counts only when the audio within this time before and after it is louder than the silence level by silence_context_db (sides without audio are neutral); 0 = off."`
	SilenceContextDB float64       `cfg:"silence_context_db" min:"0" max:"60" desc:"Margin above the silence level that the context must reach."`
	Alignment        time.Duration `cfg:"alignment" min:"0s" max:"10s" desc:"Tolerance for matching silence with visual boundary events."`
}

// BumperConfig controls bumper template matching (5.8).
type BumperConfig struct {
	SampleInterval time.Duration `cfg:"sample_interval" min:"0s" max:"1m" desc:"Spacing of template comparisons (0 = every frame)."`
	Boundary       string        `cfg:"boundary" desc:"Which edges of a match interval supply candidate boundaries."`
}

// BumperTemplate is one known bumper template (BUMPER-01).
type BumperTemplate struct {
	ID         string
	Image      string // path of a PNG; ignored when PNG is set
	PNG        []byte // caller-provided PNG data
	Region     Region // normalized frame region the template occupies
	Geometry   string // "stretch" (scale to region) or "strict" (aspect must match within 2%)
	Threshold  float64
	MinSupport time.Duration
	SHA256     string // optional integrity check of the PNG data (hex)
}

// BoundaryConfig controls candidate boundary construction (section 6).
type BoundaryConfig struct {
	FusionTolerance   time.Duration `cfg:"fusion_tolerance" min:"0s" max:"10s" desc:"Candidates within this span of a group's first candidate are fused."`
	SpanPolicy        string        `cfg:"span_policy" desc:"Representative time of a dark/uniform or silence span."`
	FromDarkUniform   bool          `cfg:"from_dark_uniform" desc:"Dark/uniform events supply boundaries."`
	FromSilence       bool          `cfg:"from_silence" desc:"Silence events supply boundaries."`
	FromResolution    bool          `cfg:"from_resolution" desc:"Coded resolution changes supply boundaries."`
	FromAspect        bool          `cfg:"from_aspect" desc:"Sustained active-picture ratio changes supply boundaries."`
	FromBumpers       bool          `cfg:"from_bumpers" desc:"Bumper matches supply boundaries."`
	FromLogo          bool          `cfg:"from_logo" desc:"Validated logo transitions supply boundaries."`
	FromCaptions      bool          `cfg:"from_captions" desc:"Caption style changes supply boundaries."`
	FromScene         bool          `cfg:"from_scene" desc:"Scene changes supply boundaries."`
	ValidateSources   bool          `cfg:"validate_sources" desc:"Check silence-only and uniform boundaries per recording: when too few spacings between consecutive boundaries of a source are standard ad lengths, that source stops supplying boundary evidence (its boundaries remain)."`
	ValidateMinEvents int           `cfg:"validate_min_events" min:"2" max:"100000" desc:"validate_sources: a source is judged only with at least this many boundaries."`
	ValidateMinShare  float64       `cfg:"validate_min_share" min:"0" max:"1" desc:"validate_sources: minimum share of spacings within the loose tolerance of a standard duration."`
	DarkRequiresQuiet bool          `cfg:"dark_requires_quiet" desc:"A dark/uniform boundary counts as evidence only when the audio around it gets quiet; known audio that stays loud keeps the boundary but drops its evidence (flagged loud_dark_boundary). Unknown audio is neutral."`
	DarkQuietWindow   time.Duration `cfg:"dark_quiet_window" min:"0s" max:"10s" desc:"Audio searched for quiet around a dark/uniform boundary, beyond the dark span itself (split evenly before and after)."`
	DarkQuietLevel    float64       `cfg:"dark_quiet_level_dbfs" min:"-160" max:"0" desc:"Loudest-channel level (dBFS) at or below which some audio window must fall for a dark/uniform boundary to keep its evidence."`
	SilenceSceneAlign bool          `cfg:"silence_scene_corroboration" desc:"A silence-only candidate with a scene cut inside the alignment window moves to the cut and counts as a corroborated audio/visual boundary."`
}

// BaselineConfig controls baseline estimation (section 6).
type BaselineConfig struct {
	MinSupport         time.Duration `cfg:"min_support" min:"0s" max:"24h" desc:"Minimum supporting duration of a caption/aspect program baseline."`
	MinDominance       float64       `cfg:"min_dominance" min:"0.5" max:"1" desc:"Minimum share of the supporting duration held by the baseline value."`
	MinRecording       time.Duration `cfg:"min_recording_support" min:"0s" max:"24h" desc:"Minimum usable observation time for a recording-relative baseline."`
	FloorBrightness    float64       `cfg:"floor_brightness" min:"0" max:"255" desc:"Near-zero floor of the brightness baseline."`
	FloorNonuniformity float64       `cfg:"floor_nonuniformity" min:"0" max:"128" desc:"Near-zero floor of the nonuniformity baseline."`
	FloorAudioDBFS     float64       `cfg:"floor_audio_energy_dbfs" min:"-160" max:"0" desc:"Near-zero floor of the audio-energy baseline in dBFS."`
	FloorSilence       float64       `cfg:"floor_silence_fraction" min:"0" max:"1" desc:"Near-zero floor of the silence-fraction baseline."`
	FloorSceneRate     float64       `cfg:"floor_scene_rate" min:"0" max:"100" desc:"Near-zero floor of the scene-change rate baseline (events per second)."`
	CaptionStyle       string        `cfg:"caption_style" desc:"Explicit program caption style (channel profile); empty = estimate."`
	AspectRatio        float64       `cfg:"aspect_ratio" min:"0" max:"10" desc:"Explicit program active-picture ratio (channel profile); 0 = estimate."`
}

// ScoringConfig controls heuristic scoring (section 7).
type ScoringConfig struct {
	Threshold               float64         `cfg:"threshold" min:"1.000001" max:"1000000" desc:"Inclusive advertisement score threshold (>1)."`
	StandardDurations       []time.Duration `cfg:"standard_durations" desc:"Ordered standard advertisement durations."`
	StrictTolerance         time.Duration   `cfg:"strict_tolerance" min:"0s" max:"1m" desc:"Inclusive strict duration tolerance."`
	LooseTolerance          time.Duration   `cfg:"loose_tolerance" min:"0s" max:"1m" desc:"Inclusive loose duration tolerance."`
	UncertaintyExtension    time.Duration   `cfg:"uncertainty_tolerance_extension" min:"0s" max:"10s" desc:"Upper bound by which boundary uncertainty may widen duration tolerances (0 = off)."`
	DurationMaxUncertainty  time.Duration   `cfg:"duration_max_boundary_uncertainty" min:"0s" max:"1h" desc:"A standard-duration modifier is credited only when both boundaries of the unit are located within this uncertainty (the midpoint of a long dark or silent span is not a precise boundary)."`
	StrictFactor            float64         `cfg:"strict_duration_factor" min:"0.000001" max:"1000000" desc:"Single block within strict tolerance."`
	LooseFactor             float64         `cfg:"loose_duration_factor" min:"0.000001" max:"1000000" desc:"Single block within loose tolerance only."`
	CombinedFactor          float64         `cfg:"combined_duration_factor" min:"0.000001" max:"1000000" desc:"Multi-block hypothesis with a standard duration."`
	LogoPresentFactor       float64         `cfg:"logo_present_factor" min:"0.000001" max:"1000000" desc:"Validated logo occupancy at or above logo_present_occupancy."`
	LogoAbsentFactor        float64         `cfg:"logo_absent_factor" min:"0.000001" max:"1000000" desc:"Validated logo occupancy at or below logo_absent_occupancy."`
	CaptionMatchFactor      float64         `cfg:"caption_match_factor" min:"0.000001" max:"1000000" desc:"Recognized caption style equals the program baseline (program evidence; 1 disables)."`
	CaptionMatchCondition   string          `cfg:"caption_match_condition" desc:"When caption_match_factor applies: always; only to blocks without logo evidence; only when the program style is not pop-on (ads are mostly pop-on); or only when both hold."`
	CaptionAbsentFactor     float64         `cfg:"caption_absent_factor" min:"0.000001" max:"1000000" desc:"Observed absence of displayed captions in a block while the program has a caption style (1 = neutral)."`
	CaptionUnexpectedFactor float64         `cfg:"caption_unexpected_factor" min:"0.000001" max:"1000000" desc:"A recognized caption style in a block while the program is reliably uncaptioned (1 = disabled)."`
	CombinedMinFamilies     int             `cfg:"combined_length_min_families" min:"0" max:"8" desc:"A hypothesis whose standard length is only the sum of several blocks (combined_standard_duration) needs at least this many independent ad signals (logo absence, caption, aspect, relative statistics, ad phrases); boundary evidence does not count. 0 = none required."`
	CombinedProgramEdges    bool            `cfg:"combined_length_program_edges" desc:"Refuse a hypothesis whose standard length is only the sum of several blocks (combined_standard_duration) when its first or last block, scored alone, carries only program evidence (caption style match, program phrase, logo present): the sum then reaches into the neighboring program."`
	UnmatchedMinFamilies    int             `cfg:"unmatched_length_min_families" min:"1" max:"8" desc:"A hypothesis that earns no standard-duration factor needs at least this many independent ad signals (logo absence, caption, aspect, relative statistics, ad phrases); boundary evidence does not count. 1 = any one."`
	AdPhraseFactor          float64         `cfg:"caption_ad_phrase_factor" min:"0.000001" max:"1000000" desc:"A hypothesis containing a matched captions.ad_phrases phrase (once per hypothesis; an evidence family). 1 = disabled."`
	ProgramPhraseFactor     float64         `cfg:"caption_program_phrase_factor" min:"0.000001" max:"1000000" desc:"A hypothesis containing a matched captions.program_phrases phrase (once per hypothesis). 1 = disabled."`
	SilentBlockFactor       float64         `cfg:"silent_block_factor" min:"0.000001" max:"1000000" desc:"A hypothesis of at least silent_block_min_duration whose known audio is at least silent_block_min_fraction silent (1 = disabled)."`
	SilentBlockMinDuration  time.Duration   `cfg:"silent_block_min_duration" min:"0s" max:"1h" desc:"silent_block_factor: minimum duration."`
	SilentBlockMinFraction  float64         `cfg:"silent_block_min_fraction" min:"0" max:"1" desc:"silent_block_factor: minimum silent share of the known audio."`
	LongBlockFactor         float64         `cfg:"long_block_factor" min:"0.000001" max:"1" desc:"Program evidence for a block longer than any ad unit (breaks.max_unit plus loose_tolerance); 1 = disabled."`
	LooseDurations          []time.Duration `cfg:"loose_standard_durations" desc:"Further standard durations that earn only the loose or combined duration factor, never the strict one; empty = none."`
	TolerancePerSeparator   float64         `cfg:"tolerance_per_separator" min:"0" max:"1" desc:"Widen duration tolerances by this share of the dark/uniform separators at a unit's edges, bounded by uncertainty_tolerance_extension; 0 = the extension follows boundary uncertainty."`
	ThresholdPercentile     float64         `cfg:"threshold_percentile" min:"0" max:"1" desc:"When above 0, raise the threshold to this duration-weighted quantile of block scores (never below threshold); 0 = off."`
	CaptionFactor           float64         `cfg:"caption_mismatch_factor" min:"0.000001" max:"1000000" desc:"Recognized caption style differs from the program baseline."`
	AspectFactor            float64         `cfg:"aspect_mismatch_factor" min:"0.000001" max:"1000000" desc:"Stable active-picture ratio differs from the program baseline."`
	RelativeFactor          float64         `cfg:"relative_factor" min:"0.000001" max:"1000000" desc:"Each enabled relative-statistics rule that fires."`
	LogoPresentOccupancy    float64         `cfg:"logo_present_occupancy" min:"0" max:"1" desc:"Block occupancy at or above which the logo is substantially present."`
	LogoAbsentOccupancy     float64         `cfg:"logo_absent_occupancy" min:"0" max:"1" desc:"Block occupancy at or below which the logo is substantially absent."`
	MinCoverageVideo        float64         `cfg:"min_coverage_video" min:"0" max:"1" desc:"Known-observation ratio required for brightness, nonuniformity and scene factors."`
	MinCoverageLogo         float64         `cfg:"min_coverage_logo" min:"0" max:"1" desc:"Known-observation ratio required for logo factors."`
	MinCoverageCaptions     float64         `cfg:"min_coverage_captions" min:"0" max:"1" desc:"Known-observation ratio required for caption factors."`
	MinCoverageAspect       float64         `cfg:"min_coverage_aspect" min:"0" max:"1" desc:"Known-observation ratio required for aspect factors."`
	MinCoverageAudio        float64         `cfg:"min_coverage_audio" min:"0" max:"1" desc:"Known-observation ratio required for audio factors."`
	RelBrightness           bool            `cfg:"relative_brightness" desc:"Relative rule: unusually high brightness."`
	RelNonuniformity        bool            `cfg:"relative_nonuniformity" desc:"Relative rule: unusually high nonuniformity."`
	RelAudioEnergy          bool            `cfg:"relative_audio_energy" desc:"Relative rule: unusually high audio energy (linear)."`
	RelSilence              bool            `cfg:"relative_silence_fraction" desc:"Relative rule: unusually high silence fraction."`
	RelSceneRate            bool            `cfg:"relative_scene_rate" desc:"Relative rule: unusually high scene-change rate."`
	RelExcessRatio          float64         `cfg:"relative_excess_ratio" min:"1" max:"1000" desc:"A relative rule fires when value >= baseline x this ratio."`
	RelMinSupport           time.Duration   `cfg:"relative_min_support" min:"0s" max:"1h" desc:"Minimum known observation time in a hypothesis for a relative rule."`
	SceneMinEvents          int             `cfg:"scene_min_events" min:"0" max:"100000" desc:"Minimum scene events in a hypothesis for the scene-rate rule."`
	BoundaryEdges           string          `cfg:"boundary_evidence_edges" desc:"Boundary-based evidence families require qualifying boundaries at both edges of a unit (recording edges exempt) or at either edge."`
	MaxUnitBlocks           int             `cfg:"max_unit_blocks" min:"1" max:"100000" desc:"Largest number of consecutive blocks combined into one ad-unit hypothesis."`
}

// BreakConfig controls ad-unit and break limits (section 8).
type BreakConfig struct {
	MinUnit             time.Duration `cfg:"min_unit" min:"0s" max:"24h" desc:"Minimum individual ad-unit duration."`
	MaxUnit             time.Duration `cfg:"max_unit" min:"1ms" max:"24h" desc:"Maximum individual ad-unit duration; a unit may measure over it by at most scoring.loose_tolerance."`
	MinBreak            time.Duration `cfg:"min_break" min:"0s" max:"24h" desc:"Minimum accepted break duration."`
	MaxBreak            time.Duration `cfg:"max_break" min:"1ms" max:"24h" desc:"Maximum accepted break duration."`
	AllowTruncatedEdges bool          `cfg:"allow_truncated_edges" desc:"Relax min_unit and min_break for candidates touching the recording start or end."`
	BridgeGaps          bool          `cfg:"bridge_gaps" desc:"Merge ad runs separated by short unknown intervals; a merged break may bridge several gaps, each at most max_bridge_gap."`
	MaxBridgeGap        time.Duration `cfg:"max_bridge_gap" min:"0s" max:"10m" desc:"Largest unknown interval that may be bridged."`
	SliverMax           time.Duration `cfg:"sliver_max" min:"0s" max:"1m" desc:"An unknown block no longer than this between an accepted break and a stretch of program longer than any ad unit becomes program (context_sliver); 0 = off."`
	DarkTailMax         time.Duration `cfg:"dark_tail_max" min:"0s" max:"10m" desc:"An ad unit that directly follows program and ends in at least this much dark is not an ad (a fade to black, dark_tail); 0 = off."`
	MarginStart         time.Duration `cfg:"margin_start" min:"0s" max:"1h" desc:"Declared recording lead-in: breaks and units starting inside it get the truncated-edge relaxation of the minimum durations; 0 = none."`
	MarginEnd           time.Duration `cfg:"margin_end" min:"0s" max:"1h" desc:"Declared recording lead-out, as margin_start for the end."`
	MarkAdjacentProgram bool          `cfg:"mark_adjacent_program" desc:"Unknown time separated from the main program by a break inside a margin gets the reason adjacent_program (the neighbouring show)."`
	BridgeLogoProgram   bool          `cfg:"bridge_logo_program" desc:"A short gap labeled program only because the logo is present may be bridged too (on-screen text in the logo's position, a channel promo); program evidence from captions or a stable interval is never bridged."`
}

// OutputConfig controls result detail and the CLI's output files.
type OutputConfig struct {
	Diagnostics      string        `cfg:"diagnostics" desc:"summary: segments, breaks, units. detailed: adds boundaries, blocks, events and scored hypotheses that reached the threshold. full: adds every evaluated hypothesis."`
	SkipPaddingStart time.Duration `cfg:"skip_padding_start" min:"-10m" max:"10m" desc:"Derived skip list only: move each skip start earlier (positive) or later (negative)."`
	SkipPaddingEnd   time.Duration `cfg:"skip_padding_end" min:"-10m" max:"10m" desc:"Derived skip list only: move each skip end later (positive) or earlier (negative)."`
	KeepFirst        time.Duration `cfg:"keep_first" min:"0s" max:"1h" desc:"Derived skip list only: never skip the first this-much of the recording (labels are unchanged)."`
	KeepLast         time.Duration `cfg:"keep_last" min:"0s" max:"1h" desc:"Derived skip list only: never skip the last this-much of the recording."`
	Overwrite        bool          `cfg:"overwrite" desc:"CLI: replace an existing output file."`
	WritePartial     bool          `cfg:"write_partial" desc:"CLI: write an explicitly incomplete result when analysis fails part-way."`
	MaxHypotheses    int           `cfg:"max_hypotheses" min:"0" max:"100000000" desc:"Largest number of hypotheses retained in diagnostics (a warning reports truncation)."`
}

// ResourceConfig bounds resource use (QUAL-02).
type ResourceConfig struct {
	Workers      int      `cfg:"workers" min:"0" max:"256" desc:"Decoder worker threads; 0 = number of CPUs, at most 16."`
	MemoryBudget ByteSize `cfg:"memory_budget" min:"16MiB" max:"1024GiB" desc:"Bound on retained compact feature storage; exceeding it is a resource-limit error (no spill)."`
	ScratchDir   string   `cfg:"scratch_dir" path:"1" desc:"Scratch directory (unused: this release never spills to disk)."`
	ScratchQuota ByteSize `cfg:"scratch_quota" min:"0B" max:"1024GiB" desc:"Scratch quota (this release uses no scratch storage)."`
}

// Clone returns a deep copy.
func (c *Config) Clone() *Config {
	d := *c
	d.Logo.SearchRegions = append([]Region(nil), c.Logo.SearchRegions...)
	d.Scoring.StandardDurations = append([]time.Duration(nil), c.Scoring.StandardDurations...)
	d.Scoring.LooseDurations = append([]time.Duration(nil), c.Scoring.LooseDurations...)
	d.Captions.AdPhrases = append([]string(nil), c.Captions.AdPhrases...)
	d.Captions.ProgramPhrases = append([]string(nil), c.Captions.ProgramPhrases...)
	d.BumperTemplates = append([]BumperTemplate(nil), c.BumperTemplates...)
	return &d
}

func (c *Config) workers() int {
	if c.Resources.Workers > 0 {
		return c.Resources.Workers
	}
	return min(runtime.NumCPU(), 16)
}

// Validate checks ranges and the interdependent constraints of CFG-03. It
// does not read asset files; LoadAssets (called by Analyze and by the CLI's
// "config validate") does.
func (c *Config) Validate() error {
	if err := validateTags(c); err != nil {
		return err
	}
	bad := func(format string, args ...any) error {
		return Errorf(KindInvalidConfig, format, args...)
	}
	for _, r := range append([]Region{c.Sampling.Region}, c.Logo.SearchRegions...) {
		if err := r.validate(); err != nil {
			return bad("region %v: %v", r, err)
		}
	}
	if len(c.Logo.SearchRegions) == 0 {
		return bad("logo.search_regions must list at least one region")
	}
	s := &c.Scoring
	if s.StrictTolerance > s.LooseTolerance {
		return bad("scoring.strict_tolerance (%v) must be <= scoring.loose_tolerance (%v)", s.StrictTolerance, s.LooseTolerance)
	}
	if s.LogoPresentOccupancy <= s.LogoAbsentOccupancy {
		return bad("scoring.logo_present_occupancy (%v) must be > scoring.logo_absent_occupancy (%v)", s.LogoPresentOccupancy, s.LogoAbsentOccupancy)
	}
	if len(s.StandardDurations) == 0 {
		return bad("scoring.standard_durations must not be empty")
	}
	for i, d := range s.StandardDurations {
		if d <= 0 {
			return bad("scoring.standard_durations: %v is not positive", d)
		}
		if i > 0 && d <= s.StandardDurations[i-1] {
			return bad("scoring.standard_durations must be strictly increasing (%v after %v)", d, s.StandardDurations[i-1])
		}
	}
	for i, d := range s.LooseDurations {
		if d <= 0 {
			return bad("scoring.loose_standard_durations: %v is not positive", d)
		}
		if i > 0 && d <= s.LooseDurations[i-1] {
			return bad("scoring.loose_standard_durations must be strictly increasing (%v after %v)", d, s.LooseDurations[i-1])
		}
		for _, sd := range s.StandardDurations {
			if d == sd {
				return bad("scoring.loose_standard_durations: %v is already a standard duration", d)
			}
		}
	}
	if c.Logo.MinOccupancy >= c.Logo.MaxOccupancy {
		return bad("logo.min_occupancy (%v) must be < logo.max_occupancy (%v)", c.Logo.MinOccupancy, c.Logo.MaxOccupancy)
	}
	for _, list := range [][]string{c.Captions.AdPhrases, c.Captions.ProgramPhrases} {
		for _, ph := range list {
			if normalizePhrase(ph) == "" {
				return bad("captions phrase %q has no letters or digits", ph)
			}
		}
	}
	if c.Audio.AutoMinDBFS > c.Audio.AutoMaxDBFS {
		return bad("audio.auto_min_dbfs (%v) must be <= audio.auto_max_dbfs (%v)", c.Audio.AutoMinDBFS, c.Audio.AutoMaxDBFS)
	}
	if (c.Logo.CacheDir == "") != (c.Logo.CacheKey == "") {
		return bad("logo.cache_dir and logo.cache_key are set together")
	}
	if c.Logo.CacheKey != "" && !validCacheKey(c.Logo.CacheKey) {
		return bad("logo.cache_key %q may only contain letters, digits, '.', '-' and '_' (and may not start with '.')", c.Logo.CacheKey)
	}
	if c.Logo.RelativeAbsent > c.Logo.RelativePresent {
		return bad("logo.relative_absent (%v) must be <= logo.relative_present (%v)", c.Logo.RelativeAbsent, c.Logo.RelativePresent)
	}
	if c.Logo.AbsentThreshold > c.Logo.PresentThreshold {
		return bad("logo.absent_threshold (%v) must be <= logo.present_threshold (%v)", c.Logo.AbsentThreshold, c.Logo.PresentThreshold)
	}
	if c.Logo.MinWidth > c.Logo.MaxWidth || c.Logo.MinHeight > c.Logo.MaxHeight {
		return bad("logo minimum candidate size must be <= maximum candidate size")
	}
	b := &c.Breaks
	if b.MinUnit > b.MaxUnit {
		return bad("breaks.min_unit (%v) must be <= breaks.max_unit (%v)", b.MinUnit, b.MaxUnit)
	}
	if b.MinBreak > b.MaxBreak {
		return bad("breaks.min_break (%v) must be <= breaks.max_break (%v)", b.MinBreak, b.MaxBreak)
	}
	if c.Dark.AutoMinLuma > c.Dark.AutoMaxLuma {
		return bad("dark.auto_min_luma (%v) must be <= dark.auto_max_luma (%v)", c.Dark.AutoMinLuma, c.Dark.AutoMaxLuma)
	}
	if c.Dark.AutoUniformMin > c.Dark.AutoUniformMax {
		return bad("dark.auto_uniform_min (%v) must be <= dark.auto_uniform_max (%v)", c.Dark.AutoUniformMin, c.Dark.AutoUniformMax)
	}
	if c.Scene.GradualWindow > 0 && c.Scene.GradualThreshold < c.Scene.CutThreshold {
		return bad("scene.gradual_threshold (%v) must be >= scene.cut_threshold (%v)", c.Scene.GradualThreshold, c.Scene.CutThreshold)
	}
	for _, sel := range [...]struct{ name, v string }{
		{KeyVideoTrack, c.Input.VideoTrack},
		{KeyAudioTrack, c.Input.AudioTrack},
		{KeyCaptionTrack, c.Input.CaptionTrack},
	} {
		if err := validateTrackSel(sel.name, sel.v); err != nil {
			return err
		}
	}
	seen := make(map[string]struct{}, len(c.BumperTemplates))
	for i := range c.BumperTemplates {
		t := &c.BumperTemplates[i]
		if t.ID == "" {
			return bad("bumper template %d has no id", i)
		}
		if _, dup := seen[t.ID]; dup {
			return bad("duplicate bumper template id %q", t.ID)
		}
		seen[t.ID] = struct{}{}
		if t.Image == "" && len(t.PNG) == 0 {
			return bad("bumpers.templates.%s: image is required", t.ID)
		}
		if err := t.Region.validate(); err != nil {
			return bad("bumpers.templates.%s: region: %v", t.ID, err)
		}
		if t.Geometry != GeometryStretch && t.Geometry != GeometryStrict {
			return bad("bumpers.templates.%s: geometry must be stretch or strict, got %q", t.ID, t.Geometry)
		}
		if !(t.Threshold > 0 && t.Threshold <= 1) {
			return bad("bumpers.templates.%s: threshold must be in (0,1], got %v", t.ID, t.Threshold)
		}
		if t.MinSupport < 0 {
			return bad("bumpers.templates.%s: min_support must not be negative", t.ID)
		}
	}
	return nil
}

func validateTrackSel(name, v string) error {
	switch v {
	case TrackAuto:
		return nil
	case TrackNone:
		if name != KeyVideoTrack {
			return nil
		}
	case TrackEmbedded:
		if name == KeyCaptionTrack {
			return nil
		}
	default:
		ok := v != ""
		for _, ch := range v {
			if ch < '0' || ch > '9' {
				ok = false
			}
		}
		if ok && len(v) < 6 {
			return nil
		}
	}
	return Errorf(KindInvalidConfig, "%s: invalid track selection %q", name, v)
}

func (r Region) validate() error {
	for _, v := range [...]float64{r.X, r.Y, r.W, r.H} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return Errorf(KindInvalidConfig, "nonfinite value")
		}
	}
	if r.X < 0 || r.Y < 0 || r.W <= 0 || r.H <= 0 || r.X+r.W > 1+regionEpsilon || r.Y+r.H > 1+regionEpsilon {
		return Errorf(KindInvalidConfig, "must satisfy 0<=x, 0<=y, w>0, h>0, x+w<=1, y+h<=1")
	}
	return nil
}

// pixels converts the region to a pixel rectangle inside w x h, never empty.
func (r Region) pixels(w, h int) (x0, y0, x1, y1 int) {
	x0 = int(math.Round(r.X * float64(w)))
	y0 = int(math.Round(r.Y * float64(h)))
	x1 = int(math.Round((r.X + r.W) * float64(w)))
	y1 = int(math.Round((r.Y + r.H) * float64(h)))
	x0, y0 = max(0, min(x0, w-1)), max(0, min(y0, h-1))
	x1, y1 = max(x0+1, min(x1, w)), max(y0+1, min(y1, h))
	return
}

// validCacheKey accepts a plain file-name stem.
func validCacheKey(k string) bool {
	if k == "" || k[0] == '.' || len(k) > maxCacheKeyLen {
		return false
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// LogoCachePath is the cache entry of the configuration (logo.cache_dir and
// logo.cache_key), or "" when no cache is configured.
func (c *Config) LogoCachePath() string {
	if c.Logo.CacheDir == "" || c.Logo.CacheKey == "" {
		return ""
	}
	return filepath.Join(c.Logo.CacheDir, c.Logo.CacheKey+LogoCacheSuffix)
}
