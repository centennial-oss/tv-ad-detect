# tv-ad-detect design note

Status: implementation baseline for requirements v0.2. This note names the
choices the requirements leave to the implementation. IDs such as `VIS-01`
refer to [requirements.md](requirements.md). Open items and unmet
requirements are listed in [traceability.md](traceability.md).

## 1. Structure

| Package | Role |
|---|---|
| `pkg/addetect` | Pure-Go core: typed configuration and YAML layers, observation input abstraction, single-pass feature extraction, events, boundaries, blocks, baselines, scoring, break assembly, result types. No cgo. |
| `pkg/ffadapter` | Packaged media adapter over FFmpeg's `libavformat`/`libavcodec`/`libavutil` through cgo. Implements `addetect.Source`. |
| `pkg/manifest` | Pure-Go HLS/DASH manifest resolution into ordered CMAF resources. |
| `pkg/cea608` | Pure-Go CEA-608 presentation-style decoder. |
| `cmd` | The `tv-ad-detect` CLI. |

Decoding and detection are separable (MEDIA-01): the core consumes
`addetect.Source` / `Stream` / `Event` and knows nothing about FFmpeg. Detector
tests inject observations with `addetect.SliceSource`.

## 2. Media adapter

**Choice (MEDIA-02): a binding, not a subprocess.** `pkg/ffadapter` links the
FFmpeg libraries and never starts an `ffmpeg` process, so there is no
subprocess to terminate and no pipe copying of raw video. One C function
(`tvad_next` in `reader.c`) demuxes, decodes and reduces one event per call,
so Go crosses the cgo boundary about once per frame. Pixel and sample loops
are in C so the compiler vectorizes them; only the reduced luma image, caption
bytes and audio level windows cross into Go, written directly into reusable
Go-owned buffers (no per-frame allocation).

FFmpeg's own logging is silenced process-wide (`av_log_set_level`), because
library code must not write to stderr (API-05); decode problems surface as
warnings and counters in the result instead.

Decoding uses FFmpeg's software decoders with frame and slice threading
bounded by `resources.workers`. Hardware decoding was measured and not
adopted: on the benchmark machine VideoToolbox decoded the 1080p sample about
8x slower in wall time than threaded software decoding (section 9).

### Capability matrix

| Area | Supported | Notes |
|---|---|---|
| Containers | MP4/MOV, Matroska/WebM, MPEG-TS; CMAF fragmented MP4 through HLS/DASH | Anything else libavformat probes is attempted but is outside the tested matrix. |
| Video codec | H.264 (required). | Other codecs with a libavcodec software decoder (MPEG-2, HEVC) decode but are untested. |
| Pixel formats | Planar or semi-planar YUV and gray, 8 to 16 bit, little-endian, luma in plane 0 | RGB, paletted, big-endian and hardware formats fail with an unsupported-input error. |
| Bit depth | 8-bit native; 9 to 16-bit reduced to 8 bits per sample before averaging | |
| Range | Limited (16-235) and full | Unsignalled range is assumed limited and reported as a warning. |
| Transfer | SDR | PQ / HLG: degraded-mode warning, or an error with `input.unsupported_format_policy: fail` (MEDIA-05). |
| Interlace | Decoded as woven frames, no deinterlacing | Reported as a warning with the frame count. |
| Rotation | Ignored; measurements use the coded orientation | Reported as a warning. |
| Audio codec | AAC, AC-3 (required); any libavcodec audio decoder producing 8/16/32-bit integer or float/double samples | Other sample formats make audio unavailable (never silent). |
| Captions | CEA-608 from A/53 user data of the video stream; CEA-608 (`c608`) caption tracks | Styles `roll_up`, `pop_on`, `paint_on`, `none`. CEA-708 service blocks and text subtitle formats carry no usable style semantics and are reported `unsupported` / neutral (CAP-01). |
| Manifests | Finite HLS media and master playlists; static DASH MPDs | See section 3. |

### Track selection (MEDIA-03)

Video: explicit stream index, else the lowest-index video stream that is not
an attached picture. Audio: explicit index, else the default-flagged audio
stream, else the lowest-index one. Captions: explicit index, else the first
CEA-608 caption track when present, else data embedded in the video stream.
The chosen tracks are reported under `tracks`. A requested optional track
that is unavailable follows `input.optional_stream_policy` (default: warn and
disable that evidence). Unselected streams are discarded at the demuxer.

### Canonical luminance (MEDIA-05)

The analysis image is the luma plane reduced by an integer box filter of
factor `k = max(ceil(W / analysis_max_width), ceil(H / analysis_max_height))`
(defaults 384 x 216, so 1080p uses k = 5, 720p k = 4, 2160p k = 10), then
mapped to full-range 0-255 (`(v - 16) * 255 / 219` for limited range).
Remainder columns/rows at the right/bottom edge are dropped. `k` is reported
as `tracks.video.analysis_scale` and is the spatial resolution limit of every
visual measurement. The optional full-resolution bright-pixel count
(`sampling.full_resolution_bright_check`, default on) is taken in the same
pass over the full-resolution rows.

### Timing (TIME-01 to TIME-03)

Presentation timestamps are converted from the stream's rational time base to
integer microseconds per timestamp (`av_rescale_q`), never accumulated from
frame counts. Result time zero is the first presented video frame; the offset
is reported as `timing.normalization_offset_us`. Discontinuity policy:

| Condition | Policy |
|---|---|
| Forward jump beyond `input.timestamp_gap_threshold` (500 ms) past the previous frame's expected end | Coverage gap: the interval is `unknown`, boundaries are placed at both edges, nothing is bridged. |
| Backward jump larger than the gap threshold | `input.timestamp_reset_policy`: `rebase` (default) continues directly after the previous frame and reports a warning and the rebase; `fail` returns a timing error. |
| Timestamp at or before the previous one (within the threshold) | The frame is dropped and counted as an overlap; never counted twice. |
| Missing timestamp | Interpolated from the previous frame; more than `input.max_missing_timestamp_ratio` of frames fails with a timing error. |
| Last frame | Its declared duration when present, else the median frame duration, reported as `final_frame_duration_estimated`. |

## 3. Packaged input (HLS, DASH, CMAF)

`pkg/manifest` resolves a presentation into two tracks (video, optional
separate audio) of ordered segments, each with its initialization resource
and continuity epoch (HLS discontinuity epoch or DASH Period). Resources are
read in manifest order through a `manifest.Resolver` (local files by default,
or caller-provided through `ffadapter.Package`).

**Supported subset.** HLS: media playlists and master playlists; `EXTINF`,
`EXT-X-MAP` (including changes), `EXT-X-BYTERANGE` with implicit offsets,
media/discontinuity sequences, `EXT-X-DISCONTINUITY`, `EXT-X-GAP`,
`EXT-X-ENDLIST` required; audio renditions through `EXT-X-MEDIA` groups;
I-frame playlists never selected. DASH: static MPDs, multiple Periods,
BaseURL and SegmentTemplate inheritance, `$Number$`/`$Time$`/
`$RepresentationID$`/`$Bandwidth$` with width formats, `startNumber`, fixed
durations, `SegmentTimeline` with repeats (including negative), `SegmentList`
with byte ranges, `presentationTimeOffset`. **Rejected explicitly** with an
unsupported-input error: growing/live playlists, dynamic MPDs, encryption
(`EXT-X-KEY`, `ContentProtection`), `SegmentBase`/index-range addressing,
low-latency parts, remote URIs without a caller resolver. MPEG-TS HLS
segments (no `EXT-X-MAP`) are read the same way but are outside the tested
matrix.

**Selection (PACK-05).** `package.video_selection` (default `highest`:
highest resolution, then bandwidth, then manifest order; also `lowest`,
`index=`, `id=`, `bandwidth=`) and `package.audio_selection` choose one
rendition for the whole analysis; nothing adapts mid-run. Across DASH Periods
the same Representation id is preferred and remappings are reported.
`lowest` with `package.min_height` is the efficient choice when a ladder is
available: the analysis image is at most 384 x 216, so a 480p rendition
yields the same measurements class at a fraction of the decode cost.

**Reading (PACK-04).** Each track is demuxed as a sequence of runs. A run is
a maximal series of consecutive segments with the same initialization
resource and epoch, presented to one demuxer as the byte concatenation of the
initialization data and the segments. No transcoding, temporary files or
concatenated copies are made; at most one resource per track is open, read
through a bounded buffer (`package.io_buffer_size`). A new run starts at an
initialization change, an epoch change or a declared gap.

**Timeline (PACK-06).** Sample timestamps define time; duration tags never
do.

- DASH: `presentation = sample_pts - presentationTimeOffset + Period start`,
  per track. Samples outside `[Period start, Period end)` are trimmed so
  overlapping boundary samples are not counted twice.
- HLS: the first epoch keeps its sample timestamps. Each later epoch is
  placed so its first video frame follows the previous epoch's last video
  frame (plus any declared-missing duration). Audio of an epoch shares that
  epoch's video offset.
- The whole presentation is then normalized once to the first video frame.

Separate audio and video tracks are interleaved by presentation time, not by
resource index. Detector state (logo learning, audio windows, caption state,
scene context, baselines) lives in the core and is unaware of resource
boundaries, so those never create cuts, evidence or truncated edges
(PACK-07). `Result.package` records manifests with digests, the selection,
epochs with their media-to-result offsets, and every resource with its byte
range, declared time, presented time span and status (PACK-09). The package
identity digest covers manifests, selected tracks, resource addresses and
the content digests of initialization data.

**Failure policy (PACK-08).** Default `fail`: any unreadable initialization
or media resource aborts with an I/O error and no result. `degraded`: the
lost run ends, its known interval is reported as a coverage gap (`unknown`),
the resource and reason are listed, and the result is marked
`complete: false`. Time is never shortened or bridged.

## 4. Measurements

| Signal | Measurement | Sampling |
|---|---|---|
| Dark/uniform (VIS) | Mean luminance; nonuniformity = luminance standard deviation; bright-pixel ratio = share of pixels above `dark.bright_pixel_luma`. A frame is dark when mean <= threshold, bright ratio <= `max_bright_fraction` and nonuniformity <= `dark_max_nonuniformity`; uniform when nonuniformity <= `uniform_max_nonuniformity` and it is not dark. Uniformity is judged on luminance only. | Every frame |
| Scene (SCENE) | Total-variation distance `0.5 * sum(abs(a_i - b_i))` between normalized luminance histograms (64 bins by default) of adjacent frames; hard cut at `cut_threshold`. Gradual change: the same distance across `gradual_window` without a cut. Events closer than `min_spacing` are suppressed. Brightness delta is recorded with each cut. | Every frame |
| Logo (LOGO) | Sobel edge map `(abs(gx) + abs(gy)) / 8 >= edge_threshold` of the search regions, stored as one bit per pixel per observation. Learning counts per-pixel edge persistence over observations at least `learn_min_separation` apart whose histogram moved (`learn_min_change`), excluding dark/uniform frames. Persistent pixels form connected components; oversized components (picture borders, static sets) are dropped; the rest are clustered. Candidates are ranked by mean persistence and validated best-first; the first candidate whose recording occupancy lies in `min_occupancy..max_occupancy` with enough coverage is used. Match measure = share of mask pixels that are edges; hysteresis between `absent_threshold` and `present_threshold`; an observation in which more than `max_background_density` of the other pixels in the logo box are edges is unknown (dense texture cannot be told from the logo); a present match is also unknown (`min_specificity`, default 0.1; 0 turns it off) unless it beats the best match of the mask moved 3 analysis pixels in any direction by that share of the mask's non-self-overlapping pixels (text where the logo would be matches a moved mask too; the logo does not); states shorter than `min_state_duration` are absorbed. | Every `logo.sample_interval` (500 ms) |
| Resolution (SIZE) | Coded width/height of each decoded frame before reduction. | Every frame |
| Captions (CAP) | CEA-608 mode commands of the selected service: RU2/RU3/RU4 = roll-up, RCL/EOC = pop-on, RDC = paint-on. Activity on the service keeps a style interval alive; no activity for `inactive_timeout` is observed absence (`none`); no service data at all for `data_timeout` is `unknown`. No text is decoded or kept. | Every frame carrying caption data |
| Aspect (ASPECT) | Inward scan for border rows/columns (mean <= `border_max_luma`, no pixel more than `border_max_deviation` above it). Dark/uniform frames, crops below `min_active_fraction` and asymmetric borders (`symmetry_tolerance`) are unknown. Ratio = active width x SAR / active height. A changed ratio must persist for `persistence`; the change is dated at its first supporting observation. | Every `aspect.sample_interval` (200 ms) |
| Audio (AUDIO) | Per-channel mean-square in fixed windows on the track's own sample clock; RMS dBFS = `10 * log10(mean-square)`, full scale = 1.0. A window is silent only when the loudest included channel is at or below `silence_level_dbfs`. A timestamp jump of more than half a window restarts the window, so holes are unknown, never silence. | Every `audio.window` (20 ms) |
| Bumpers (BUMPER) | Template PNG converted to luma and area-resampled to the pixel size of its normalized region; zero-mean normalized cross-correlation against the frame region, clamped to 0..1. Consecutive matches spanning `min_support` form one event. | Every `bumpers.sample_interval` (200 ms) |

Everything is computed in one decode pass (PIPE-03).
Extraction ends at a fixed seam: the normalized timeline and every
detector's compact measurements (the `measured` set). Classification reads
nothing else, so the set can be saved as a feature file and classified again
without decoding (`--save-features`, `AnalyzeFeatures`), byte-identical to a
direct run. Logo presence is decided during classification from per-sample
counts of each learned candidate (edge pixels found in the mask and in the
rest of the box), so the presence thresholds, smoothing and validation keys
are classification settings; the learning keys remain feature keys. The
feature file is versioned (`TVADFEAT`, version 3), little-endian with exact
float bits, and ends in a SHA-256 of its contents; a different version or a
damaged file is refused. A 35-minute recording gives a file of about 3 MB,
and replaying it takes about 60 ms against about 34 s to decode.
 What is retained scales
with duration but is compact: 16 bytes per video frame, 24 bytes per audio
window, about 3 KiB per logo observation, and events. Whole-recording steps
(dark calibration, logo learning and validation, baselines) reprocess these
compact measurements, never video. Retained storage is checked against
`resources.memory_budget`; exceeding it is a resource-limit error (there is
no spill to disk, so no scratch storage is used).

## 5. Boundaries, blocks and scoring

- **Candidates (BOUND-01, BOUND-02).** A continuous dark/uniform span is one
  candidate at its midpoint, or at the midpoint of the interval shared with an
  overlapping silence, quantized to a frame boundary. Point changes keep their
  first-new-state timestamp. A silence within `audio.alignment` of a visual
  candidate corroborates it without moving it and adds no boundary of its own.
  A silence with no visual candidate but with a scene event inside the
  alignment window moves to that event and counts as an audio/visual
  corroborated boundary (`boundaries.silence_scene_corroboration`, on by
  default); otherwise it is a silence-only candidate at its midpoint. An
  aspect-ratio change first observed just after a dark/uniform span (the
  picture bounds are unknown on dark frames) joins that span's candidate
  instead of adding a boundary one sample later.
- **Fusion (BOUND-03).** Candidates sorted by time are grouped; a group only
  accepts candidates within `fusion_tolerance` of its first member, so chains
  cannot grow. The group takes the time of its highest-priority member in the
  fixed order: coverage edge, resolution change, aspect change, dark/uniform,
  bumper, logo, caption, scene, silence (ties: earliest). Two exact kinds with
  different timestamps never share a group.
- **Blocks (BLOCK-01).** Consecutive boundaries partition the timeline.
  Aggregates are duration-weighted by intersecting each observation's interval
  with the block. Running sums over blocks make any consecutive group of
  blocks constant-time to aggregate.
- **Baselines (BASE).** Recording-relative baselines use observed time only
  and are floored. Program caption style and aspect ratio come, in order, from
  time with a validated logo present, from stable intervals longer than the
  maximum ad-unit duration, then from the recording-dominant value (flagged as
  a fallback). Support without dominance is `ambiguous` and disables the
  mismatch rule. Baselines are frozen before scoring and never use ad
  classifications; there is no iterative refinement.
- **Scoring (SCORE).** Each block and each consecutive group of blocks up to
  the maximum ad-unit duration is evaluated exactly once, in order of start
  block then length, from the running sums. Factors are those of SCORE-02;
  a modifier of 1.0 disables its rule. A standard-duration modifier is only
  credited when both boundaries of the unit are located within
  `scoring.duration_max_boundary_uncertainty` (1 s): the midpoint of a long
  dark or silent span, such as dark opening titles, is not a precise boundary
  (VIS-03).
- **Combined lengths at program edges.** A standard length reached only by
  adding several blocks up (`combined_standard_duration`) is refused when its
  first or last block, scored alone, carries only program evidence (captions
  in the program's style, a program phrase, the logo present):
  `scoring.combined_length_program_edges`, on by default. Without it, short
  program blocks next to a break are swept into a unit whenever the sum lands
  on a standard length.
- **Evidence gate (SCORE-06).** A unit needs a non-duration family. The
  boundary families (dark/uniform boundary, audio/visual corroborated
  boundary) are required at **both** edges of the unit by default
  (`scoring.boundary_evidence_edges: both`; recording edges are exempt but
  never count as evidence). This is stricter than the minimum the requirement
  states and was chosen because program blocks next to a break otherwise match
  a standard duration by chance; `either` restores the minimum.
- **Selection (SCORE-04).** Weighted interval scheduling over eligible units
  maximizes `sum(duration x ln(score))`; ties prefer fewer units, then the
  earlier boundary.
- **Program evidence (SCORE-07).** A block is `program` when the validated
  logo is substantially present, or when it lies in a run of consecutive
  unselected, fully covered blocks longer than the maximum ad-unit duration
  whose caption style and aspect ratio both match reliable baselines, and it
  has no ad-raising factor and a score below the threshold. Everything else
  that is not an accepted ad is `unknown`. An ad-raising factor only blocks
  the stable-interval program label when the block's score is still 1 or more.
- **Program-style captions (addition to SCORE-02).** A recognized caption
  style equal to the reliable program baseline multiplies the score by
  `scoring.caption_match_factor` (0.4). Broadcasters often drop the logo
  during sponsor billboards and features inside the program; captions in the
  program's own style identify those as program.
- **Optional scoring rules (neutral by default).**
  `scoring.caption_absent_factor` scores observed absence of captions against
  a captioned program (family `captions`). `scoring.caption_unexpected_factor`
  scores a recognized style against a reliably uncaptioned program; that
  baseline (style `none`) is only established when this rule is enabled.
  `scoring.long_block_factor` (at most 1) marks a block longer than any ad
  unit as program (`long_block`). `scoring.loose_standard_durations` are
  lengths that earn only the loose or combined factor.
  `scoring.tolerance_per_separator` makes the duration-tolerance extension a
  share of the dark/uniform separators at the unit's edges instead of their
  uncertainty, still bounded by `uncertainty_tolerance_extension`.
  `scoring.threshold_percentile` raises the threshold to the
  duration-weighted quantile of block scores; the result reports the
  configured and effective thresholds in `calibration.score`.
- **Robustness rules (phase 4).** On by default: a dark/uniform boundary
  keeps its evidence only when the audio around it (the dark span plus
  `boundaries.dark_quiet_window`) drops to `dark_quiet_level_dbfs` at least
  once (`loud_dark_boundary` veto otherwise; unknown audio is neutral); and a
  frame that is not dark is tested again without the search region holding
  the validated logo (`dark.ignore_logo_region`, from per-region statistics
  kept for every frame). Off by default, for evaluation: relative logo
  thresholds and an unknown state between them (`logo.threshold_mode`,
  `logo.ambiguous_state`), a condition on the caption-match factor
  (`scoring.caption_match_condition`), an estimated silence floor and a loud
  context for silences (`audio.silence_mode`, `audio.silence_context`),
  per-recording validation of silence-only and uniform boundaries
  (`boundaries.validate_sources`), the sliver and fade-tail rules
  (`breaks.sliver_max`, `breaks.dark_tail_max`), logo run trimming and
  per-state smoothing (`logo.present_trim`, `logo.min_present_duration`,
  `logo.min_absent_duration`), and recording margins (`breaks.margin_*`,
  `breaks.mark_adjacent_program`).
- **Dark boundaries separate units.** Consecutive blocks are combined across
  a dark/uniform boundary only when their total is a standard length;
  otherwise each side is evaluated on its own (`spans_dark_boundary`).
- **Gap bridging and edges (BREAK-03, BREAK-04).** On by default: runs merge
  across unknown gaps of up to 15 s, repeatedly while the merged break stays
  within `max_break`; a gap whose only program evidence is the logo may be
  bridged (`breaks.bridge_logo_program`). Breaks touching the recording start
  or end get the truncated-edge relaxation.
- **Breaks (BREAK).** Adjacent selected units form a run; a run outside the
  break limits is `unknown` with `break_too_short` / `break_too_long`, never
  split or relabeled as program. Gap bridging and truncated edges are off by
  default.

## 6. Defaults left open by the requirements

Every tunable is a named key in the exported default configuration
(`tv-ad-detect config defaults`, also [examples/default.yaml](examples/default.yaml)),
with its range and description. The values the requirements did not fix are
initial product choices, not measured accuracy claims (SCOPE-04). The
configuration schema version makes default changes observable.

## 7. Degraded modes

| Mode | Trigger | Effect |
|---|---|---|
| Optional stream missing | No audio/captions, or the requested track is unavailable | Warning; that detector is `unavailable` and neutral. |
| HDR transfer | PQ/HLG with policy `warn` | Warning `hdr_degraded`; thresholds are SDR-defined. |
| Unsignalled range | No color range | Limited range assumed, warning. |
| Coarse sampling | `sampling.decode_frames: reference` | Only reference frames are decoded (about a third less CPU on the benchmark recording). Timing resolution drops to the reference-frame spacing and embedded caption data is unavailable; warning `coarse_decode`. |
| Package loss | `package.failure_policy: degraded` | Section 3. Result `complete: false`. |
| Truncated input | A file whose media ends more than 2 s and 2% before its declared duration, or a DASH presentation whose media ends before the last Period's declared end | Warning `truncated_input`, result `complete: false`; for DASH the missing tail stays on the timeline as unknown. |
| Partial result | Failure after some media was analyzed | `Analyze` returns the error plus a result with `complete: false` and `error`; the CLI writes it only with `--write-partial`. |

## 8. Concurrency and determinism

An analysis owns all of its state; `Config` is cloned and assets are
immutable, so concurrent analyses share nothing mutable (API-03). The core
runs on the calling goroutine; decoder worker threads are internal to FFmpeg
and frames are delivered in presentation order, so results do not depend on
scheduling or on `resources.workers` (QUAL-01). Cancellation is checked per
event and interrupts blocking demuxer I/O through an interrupt callback.

## 9. Measured performance

See [benchmarks.md](benchmarks.md) for the method and current numbers
(QUAL-03). No real-time throughput claim is made beyond those measurements.
