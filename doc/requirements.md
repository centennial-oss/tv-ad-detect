# Video advertisement detection: product and technical requirements

Version: 0.2\
Status: implementation baseline  
Date: 2026-11-16  
Product: `tv-ad-detect` — a Go library and command-line application  
License: Apache License 2.0

## 1. Purpose and scope

The product analyzes recorded video and identifies intervals likely to contain advertisements, separating them from the main program. Applications can call a Go library; users and batch jobs can run a CLI backed by the same library. The output is a timestamped classification timeline with evidence explaining the decisions.

The initial release SHALL provide offline analysis of complete local recordings, including packaged presentations described by HLS or DASH manifests with CMAF media spanning multiple files. It SHALL implement all eight signal families in section 5, subject to available media data and explicitly enabled configuration. Detection is heuristic: a boundary, logo absence, caption change, or unusual duration alone does not establish the semantic identity of a segment.

`SHALL` identifies a release requirement. `SHOULD` identifies a recommendation that may be departed from with a documented reason. `MAY` identifies an optional capability. Requirement IDs are stable review and test identifiers.

**SCOPE-01.** The deliverables SHALL be an independently authored Go library, CLI, configuration documentation, output schema, and tests. Source distributed as part of this product SHALL use Apache License 2.0. Dependencies and separately installed media tools SHALL be inventoried with their licenses and distribution requirements before release.

**SCOPE-02.** Detection SHALL work locally without a cloud service, account, network request, downloaded model, advertising database, or uploaded recording. This includes HLS/DASH packages whose manifests, initialization data, and media resources are available locally. Remote manifest/resource retrieval MAY be supported as an explicitly enabled input option; it SHALL not be required for local analysis or permit incidental network access by a local package. User-supplied logo assets and bumper templates SHALL be supported.

**SCOPE-03.** The first release need not identify brands, interpret speech or caption text (beyond the opt-in phrase matching of CAP-04), recognize embedded sponsorships, remove advertisements, write an edited video, provide a GUI, or analyze a growing live stream. It SHALL never modify the source recording. Promos, station idents, and bumpers may be grouped with advertisements when the available evidence supports that decision; the output SHALL not claim to identify their semantic subtype.

**SCOPE-04.** Numerical defaults in this document are initial product choices, not measured accuracy claims. Settings expressly left to the implementation SHALL be selected, explained, versioned, and covered by tests before release. Algorithm and dependency choices remain open where observable requirements do not prescribe them.

## 2. Vocabulary and result meaning

| Term | Meaning |
|---|---|
| Program | Main title content, including its normal introductions and credits. |
| Signal | A measured visual, audio, caption, or stream property. |
| Event | A timestamp or time interval at which a signal meets a detection condition. |
| Candidate boundary | A possible division between blocks, with contributing events and timing uncertainty. |
| Block | An interval between consecutive candidate boundaries. A block is an analysis unit, not necessarily one advertisement. |
| Ad unit | One or more consecutive blocks that meet the configured individual-ad duration and evidence rules. Individual-ad identity is not guaranteed. |
| Ad break | A contiguous run of accepted ad units, subject to break duration rules. |
| Baseline | An estimated recording-level or program-level property used for comparison. |
| Unknown | Insufficient or conflicting evidence, inadequate coverage, or a rejected ad hypothesis without positive program evidence. |
| Heuristic score | Relative evidence for an ad hypothesis. It is not a probability. |

**RESULT-01.** The complete timeline SHALL use `advertisement`, `program`, and `unknown` labels. `program` SHALL mean positive program evidence as defined in SCORE-07; a low ad score by itself is insufficient. An application MAY choose how to display unknown intervals, but canonical results SHALL preserve them.

**RESULT-02.** Accepted ad breaks SHALL be available as a separate list for applications that only need skip intervals. A successful analysis with no accepted breaks SHALL return an empty list, not an error.

## 3. Media input and timing

**MEDIA-01.** The product SHALL accept a local recording file path or a local HLS (`.m3u8`) or DASH (`.mpd`) manifest path as one input. The library SHALL also accept caller-provided timestamped decoded video, audio measurements or samples, caption events, and stream metadata through a documented input abstraction. Decoding and detection SHALL be separable so detector tests do not require a media executable.

**MEDIA-02.** The initial packaged media adapter SHALL support MP4, Matroska, and MPEG transport stream recordings containing H.264 video and AAC or AC-3 audio, when present. Its supported codec, pixel-format, and caption capability matrix SHALL be documented and exercised with redistributable fixtures. A media dependency MAY run as a subprocess or be integrated through a binding. The choice SHALL be documented; the public detection API SHALL not expose dependency-specific structures.

**MEDIA-03.** The adapter SHALL expose coded width and height, sample aspect ratio, presentation timestamps, available video range/transfer metadata, audio timing, and caption capability when available. It SHALL allow explicit video, audio, and caption track selection. Defaults SHALL be deterministic, select a non-attached-picture video track, and report the chosen tracks. Tracks SHALL not be mixed implicitly.

**TIME-01.** Time SHALL be based on presentation timestamps, not frame count divided by a nominal frame rate. Variable frame rate, fractional frame rate, reordered decoded pictures, nonzero stream starts, and audio/video start offsets SHALL be handled on one presentation timeline. Internal arithmetic SHALL preserve at least microsecond precision or the original rational time base. Frame numbers MAY supplement timestamps but SHALL not be authoritative.

**TIME-02.** Canonical results SHALL use integer microseconds relative to the first presented video frame, at time zero. Media timestamps and the normalization offset SHALL remain available in diagnostics. Intervals SHALL be half-open: `[start_us, end_us)`. They SHALL be ordered, nonempty, nonoverlapping, and bounded by the analyzed video duration. The final frame's duration SHALL come from available timing metadata or a documented estimate; any estimate SHALL be reported.

**TIME-03.** Timestamp gaps, resets, overlaps, missing timestamps, and decode failures SHALL be reported. The adapter SHALL use a documented deterministic discontinuity policy and SHALL not fabricate ordinary continuous coverage across a gap. Unobserved portions of the established timeline SHALL be unknown. If a reliable timeline cannot be established, analysis SHALL fail with a specific timing error.

**MEDIA-04.** Missing audio, absent captions, unsupported caption formats, and unavailable metadata SHALL disable only the affected evidence. Missing data SHALL never become silence, logo absence, a caption style, or an aspect-ratio mismatch. A recording with no decodable video SHALL fail. Resolution changes SHALL be supported without terminating analysis.

**MEDIA-05.** Visual measurements SHALL operate on a documented canonical luminance representation, with full/limited range and bit depth normalized to an equivalent 0–255 scale. Audio levels SHALL use documented digital units, preferably dBFS. Range, transfer, interlace, rotation, or pixel-aspect assumptions affecting measurements SHALL be explicit. Unsupported HDR or other unsupported processing modes SHALL cause a clear capability error or an explicit degraded-mode warning, never silent use of inappropriate thresholds.

### 3.1 Packaged video: HLS, DASH, and CMAF

Protocol terminology follows [HLS playlist and fragmented-MP4 definitions](https://www.rfc-editor.org/rfc/rfc8216.html) and the [DASH-IF timing model](https://dashif.org/Guidelines-TimingModel/). The following requirements define the supported product subset; packaging SHALL not alter the detection rules.

**PACK-01.** Resolve a complete finite HLS or DASH presentation into an ordered collection of initialization resources and CMAF fragmented-MP4 media resources, then analyze the selected content as one logical recording. Read resources in manifest presentation order, not directory order or filename order. Directory scanning SHALL not substitute for manifest interpretation. A single physical file containing multiple byte ranges SHALL also be supported. Growing HLS playlists, dynamic DASH presentations, low-latency partial-segment acquisition, and encrypted/DRM-protected media are outside the required initial subset; unsupported inputs SHALL fail with a specific capability error rather than yield a silently incomplete recording.

**PACK-02.** HLS support SHALL include a direct Media Playlist and a Master Playlist selecting a full video variant with its associated audio/caption renditions. Honor segment URIs, `EXTINF`, `EXT-X-MAP` initialization resources, `EXT-X-BYTERANGE` including implicit offsets, media/discontinuity sequences, and `EXT-X-DISCONTINUITY`. Required finite Media Playlists SHALL end with `EXT-X-ENDLIST`. I-frame-only playlists SHALL not replace full video. Support relative resource URIs resolved against their declaring playlist and initialization changes during the presentation. An HLS gap declaration SHALL identify missing coverage, not ordinary content.

**PACK-03.** DASH support SHALL include static MPDs with one or multiple Periods, AdaptationSets, and Representations; inherited `BaseURL` and addressing attributes; `SegmentTemplate` with `$Number$` or `$Time$`, initialization templates, `startNumber`, fixed durations, and `SegmentTimeline`; and explicit `SegmentList` initialization/media references with byte ranges. Expand timeline repeat counts, including a negative repeat bounded by the next timeline entry or a finite Period endpoint. Addressing via `SegmentBase`/index ranges MAY be supported; if absent from the capability matrix, reject it explicitly. Preserve all selected Periods of the presentation, not just the first. Resolve resources using manifest hierarchy and URI rules.

**PACK-04.** Decode each CMAF media resource with its applicable initialization data, including separately packaged video and audio tracks. Multiple movie fragments in one resource SHALL be handled. Initialization data SHALL not be counted as presented content or require a particular filename extension. Continue compatible decoder state across adjacent resources, and reinitialize at actual configuration changes when required. The required media codec support remains defined by MEDIA-02 and the published adapter matrix. Reading a package SHALL not require transcoding, manual concatenation, or a user-created monolithic video file.

**PACK-05.** Select and report one full video rendition/Representation and the associated chosen audio/caption services deterministically, with explicit selection overrides. Do not analyze every bitrate as consecutive content or switch quality adaptively during analysis. Across DASH Periods, retain compatible logical track selection where available, report any required remapping or unavailable track, and apply MEDIA-04 to missing optional evidence. Differently segmented audio and video SHALL be aligned by presentation time rather than resource index.

**PACK-06.** Map sample presentation timestamps, media timescales, composition offsets, and manifest presentation structure onto the TIME-01–03 timeline. For DASH, account for Period start, presentation-time offsets, and Period bounds; for HLS, account for discontinuity epochs and track alignment. Duration tags SHALL not replace available sample timing or accumulate rounding drift. Honor presentation trimming/overlap rules so samples spanning adjacent Periods or resources are not counted twice. A declared discontinuity or Period transition SHALL not automatically imply missing time; preserve real gaps as unknown and fail when a reliable mapping cannot be established. Normalize the entire presentation once, not each file separately.

**PACK-07.** Preserve logo learning/validation, calibration distributions, baselines, event coalescing, audio windows, caption state, scoring context, and break assembly across ordinary resource boundaries. Blocks, ad units, ad breaks, and final classification intervals SHALL be able to span multiple CMAF resources or continuous Periods, subject to the same duration and evidence rules as single-file input. File starts/ends, segment duration, manifest discontinuities, and Period transitions SHALL not alone create content cuts, ad evidence, or truncated recording edges. Actual decoded geometry/timing changes SHALL follow the existing signal and coverage rules, including invalidation of only affected state. Manifest ad markers MAY be retained as diagnostics but SHALL not bypass signal-based scoring.

**PACK-08.** Missing, corrupt, or unreadable initialization/media resources SHALL follow an explicit package failure policy. Default behavior SHALL fail analysis rather than omit files and report success. An explicit degraded policy MAY continue if the affected presentation interval is known; retain that interval as unknown, report the resource and reason, and mark the result incomplete. Never shorten time, synthesize silence/black frames, bridge missing coverage, or reset break minimums at resource boundaries to conceal loss. Bound resource reads, prefetch/concurrency, open files, and scratch use under QUAL-02. Cancellation SHALL stop package reads and any owned retrieval tasks.

**PACK-09.** Package identity and diagnostics SHALL record the manifest snapshot(s), selected variant/Representation/Period identities, resource identities and byte ranges, applicable initialization resources, and mappings between resource-local timestamps and normalized program time. Feature-cache invalidation SHALL account for changes to those inputs, not only the top-level manifest path. Results SHALL remain one presentation-level timeline and break list; source-resource mapping SHALL allow an application to locate an interval spanning multiple files without converting it to independent per-file detections.

## 4. Required analysis stages

**PIPE-01.** The logical pipeline SHALL consist of media inspection and decoding, feature extraction, signal calibration and validation, event detection, candidate boundary construction, block aggregation, baseline estimation, scoring, break assembly, and result serialization. These are behavioral responsibilities, not prescribed function names or package boundaries.

**PIPE-02.** A detector SHALL emit timestamped measurements/events with a signal name, availability status, coverage, and relevant quality information. A signal SHALL distinguish `disabled`, `unavailable`, `unreliable`, and `usable`. An unavailable or unreliable signal SHALL contribute neither a score modifier nor a boundary. Unless a detector has an explicit override, a block/hypothesis-level classification factor SHALL require known observations covering at least 0.8 of the interval's duration, configurable by signal. A reliable localized boundary event MAY still be used without that much whole-block coverage; its support SHALL be reported independently.

**PIPE-03.** Recording-wide calibration and baselines MAY require another pass. Full-video retention SHALL not be required. Reprocessing cached compact measurements instead of decoding again SHOULD be supported. If a measurement cache is implemented, its identity SHALL include input identity, feature settings, adapter identity/version, and feature schema version; changing score weights alone SHOULD not invalidate extracted features.

**PIPE-04.** Boundary evidence and classification evidence SHALL remain distinguishable in the result. A signal MAY serve both roles, but an implementation SHALL not count the same observation repeatedly merely because it produced several internal events.

## 5. Detection signals

All enabled signal families below are required capabilities. Individual detectors SHALL expose thresholds, temporal filtering, and sampling settings through named configuration fields. Public configuration SHALL use signal names rather than numeric bitmasks.

### 5.1 Black and uniform frames

**VIS-01.** Measure mean luminance and spatial nonuniformity over the configured analysis region. Identify both dark frames and nearly solid-color frames. A dark classification SHALL include a configurable bright-pixel or spatial-uniformity safeguard so a mostly dark scene with localized visible content need not become a black-frame event. Uniform-color detection SHALL remain distinguishable from dark-frame detection.

**VIS-02.** Provide fixed-threshold and automatic-calibration modes. The initial fixed maximum mean luminance SHALL be 19 on the canonical 0–255 scale. Automatic calibration SHALL estimate dark/uniform thresholds from recording-level distributions, enforce configured bounds, require a minimum amount of usable data, and fall back to fixed thresholds when the distributions do not support a stable estimate. Report the estimated thresholds, support, and fallback reason.

**VIS-03.** Merge consecutive qualifying observations into events with start/end, minimum and mean brightness, uniformity, and coverage. Permit single-frame events when full frame coverage is available. A sustained black screen SHALL be one event rather than a cut for every frame. Dark scenes, fades, opening titles, and credits SHALL not automatically be classified as advertisements.

### 5.2 Station logo presence

**LOGO-01.** Learn a stationary logo candidate from spatial edges that persist in the same image locations across many temporally separated samples. The learning process SHALL use multiple observations, configurable search regions, support thresholds, and minimum/maximum candidate size. It SHALL reject insufficiently supported candidates and SHOULD avoid persistent image borders, caption regions, and nearly static scenes.

**LOGO-02.** Support an explicit user-supplied logo asset with position, analysis geometry, and version metadata. Auto-learning and asset loading SHALL produce the same downstream contract: a timestamped match measure and a presence state of `present`, `absent`, or `unknown`. Asset incompatibility SHALL be reported. Automatic scaling across different geometries SHALL require a documented coordinate transformation.

**LOGO-03.** Apply configurable presence thresholds and time-based smoothing to prevent brief fades, animation, occlusion, or compression noise from causing rapid state changes. Retain transition uncertainty and usable observation duration. Compute block logo occupancy as duration present divided by duration with a known logo state, not by the count of decoded frames.

**LOGO-04.** Validate the learned or supplied logo against the recording. Presence covering too little or almost all usable time SHALL disable logo evidence for that recording because it does not reliably distinguish program and ads. Initial acceptable occupancy SHALL be 0.40 through 0.92 inclusive, with minimum coverage and learning support also required. These limits SHALL be configurable. An observation that matches about as well with the logo moved a few pixels (text or texture where the logo would be) SHALL be unknown rather than present; this threshold SHALL be configurable (`logo.min_specificity`, default 0.1) and the unknown time does not count towards occupancy. Report which validation failed. Revalidate or relearn after geometry changes; never interpret invalid geometry as logo absence.

**LOGO-05.** When logo evidence is usable, substantial presence SHALL lower the ad score and substantial absence MAY raise it. Low coverage or an unclassified occupancy range SHALL be neutral. Channels whose logo remains over advertisements SHALL still be analyzable through other signals. Validated logo transitions MAY generate boundaries when explicitly enabled.

### 5.3 Scene changes

**SCENE-01.** Detect abrupt visual changes by comparing normalized luminance histograms of temporally adjacent frames. The distance metric, binning, normalization, and threshold SHALL be documented. A brightness discontinuity MAY provide an additional event measure. Suppress duplicate events around the same transition and distinguish hard cuts from gradual changes where possible.

**SCENE-02.** Aggregate scene-change count and rate per second over usable coverage. High scene-change rate MAY raise the ad score when the optional relative-statistics rule is enabled. A scene change SHALL not directly establish an ad. Scene-only boundaries SHALL be disabled by default and configurable because ordinary program editing can produce many cuts.

### 5.4 Coded resolution changes

**SIZE-01.** Detect a change in coded video width or height before any analysis downscaling or cropping. A change SHALL create a boundary at the first frame with the new dimensions. Report old and new geometry. The initial dimensions SHALL establish state without generating a change event.

**SIZE-02.** Reset, reproject, or invalidate geometry-dependent state, including logo masks and border models. A coded resolution change SHALL be distinguishable from an active-picture aspect-ratio change and SHALL not alone establish an ad.

### 5.5 Caption presentation style

**CAP-01.** Recognize caption presentation styles `roll_up`, `pop_on`, and `paint_on` when the input exposes the necessary semantics. The packaged adapter SHALL support these styles for CEA-608 data, including carriage in supported video streams. `none` SHALL represent observed lack of active captions in a supported service; `unknown` SHALL represent absent decoding capability, unsupported semantics, or insufficient observation. Text subtitles without presentation-style information SHALL not be assigned a guessed style.

**CAP-02.** For the selected caption service, compute duration and coverage by style and a dominant style per block. Compare it with a sufficiently supported program baseline. A different recognized style SHALL increase ad likelihood when enabled. Lack of active captions SHALL be neutral by default; an explicit configurable policy MAY treat observed absence as evidence. Unsupported or missing caption decoding SHALL always be neutral.

**CAP-03.** Caption style changes MAY produce boundaries when explicitly enabled. Service/language changes SHALL not silently redefine a style. Detection SHALL not require caption text, keywords, transcription, or retained transcripts.

**CAP-04.** Opt-in phrase matching MAY decode the caption text of the selected service when the user configures phrase lists (advertisement and program phrases). Matching SHALL be case-insensitive, ignore punctuation and span caption line breaks. Caption text SHALL pass only through a short window needed for matching and SHALL not be stored, reported or written; results report only the configured phrase, its list and its time. Without phrase lists no caption text is decoded. Matched phrases MAY adjust the score through factors that are neutral by default.

### 5.6 Active-picture aspect ratio

**ASPECT-01.** Estimate active-picture bounds by scanning inward from frame edges for persistent letterbox and pillarbox regions. Use configurable darkness, spatial consistency, and temporal persistence tests. Account for sample aspect ratio when computing the displayed active-picture ratio. Keep coded dimensions, display aspect ratio, and active-picture ratio as separate properties.

**ASPECT-02.** Ignore short-lived border estimates, invalid crops, and wholly dark or uniform frames whose content bounds cannot be determined. Report uncertain bounds as unknown. Brief darkness SHALL not establish a new aspect ratio.

**ASPECT-03.** A sustained ratio change exceeding a configurable tolerance SHALL create a boundary at the first supporting observation, after persistence is confirmed. Compare block ratios to a supported program baseline. A stable mismatch SHALL increase ad likelihood; it SHALL not independently prove an ad.

### 5.7 Silence and audio level

**AUDIO-01.** Measure audio energy in fixed-duration timestamped windows for the selected track. The initial metric SHALL be RMS level in dBFS, calculated per channel, with a documented conservative rule that does not call a window silent while an included channel has significant energy. Missing windows and decode gaps SHALL be unknown, never zero amplitude.

**AUDIO-02.** Detect silence below a configurable threshold for a minimum duration. Initial values SHALL be −50 dBFS and 100 ms. Coalesce adjoining silence windows. Match nearby silence and visual boundary events within a configurable alignment window; initial alignment tolerance SHALL be 250 ms. Alignment SHALL increase boundary support, not create an automatic ad classification.

**AUDIO-03.** Aggregate mean energy and silence fraction per block over usable audio duration. Optional relative-statistics rules SHALL support unusually high audio energy and unusually high silence fraction as distinct evidence. A silent track or long silent program scene SHALL not by itself establish an ad.

### 5.8 Known bumper template matching

**BUMPER-01.** Match decoded images against user-supplied still-image templates of known bumpers or transition cards. Each template SHALL have an identifier, image, normalized region, geometry policy, match threshold, and minimum temporal support. The image comparison metric and preprocessing SHALL be documented. PNG SHALL be a supported template image format.

**BUMPER-02.** A match SHALL emit an event containing template identity, interval, match quality, and uncertainty. Matches SHALL support boundary construction; matching an image SHALL not imply that all following content is advertising. Template assets SHALL not be required for ordinary detection. An enabled matcher with no templates SHALL be reported as unavailable and otherwise be neutral.

**BUMPER-03.** Template and logo assets SHALL have documented versioned formats with dimensions and integrity checks. Invalid or incompatible explicitly selected assets SHALL fail configuration validation. The library SHALL allow caller-provided asset data. Assets SHALL not be learned or overwritten on disk without an explicit export request.

## 6. Boundaries, blocks, and baselines

**BOUND-01.** By default, black/uniform events, silence events, coded resolution changes, sustained active-picture ratio changes, and enabled bumper matches SHALL supply candidate boundaries. Validated logo, caption-style, and scene-change transitions SHALL be individually selectable additional boundary sources. Recording start and analyzed end SHALL always be boundaries.

**BOUND-02.** Collapse continuous dark/uniform or silence spans to one representative candidate. Use the midpoint of the shared interval when visual and audio spans overlap; otherwise prefer the midpoint of the visual span. A silence-only candidate SHALL use its midpoint. Point changes SHALL retain their first-new-state timestamp. Quantize visual candidates to a known frame presentation boundary. An event's original start/end and the resulting boundary uncertainty SHALL remain in diagnostics.

**BOUND-03.** Fuse candidates within the configured tolerance without losing their causes. Initial fusion tolerance SHALL be 100 ms. Grouping SHALL be deterministic and SHALL not let a chain of nearby candidates merge an arbitrarily long interval. Resolution changes and confirmed format transitions SHALL retain their transition timestamp when fused. Ties SHALL use a documented fixed ordering. A separate silence alignment window MAY supply corroboration without moving the visual boundary.

**BLOCK-01.** Consecutive final boundaries SHALL define a gapless partition of the analyzed timeline. Avoid zero-length and duplicate blocks. Keep missing-data intervals identifiable, splitting blocks at coverage edges where necessary. Aggregate duration-weighted signal values and coverage for each block: brightness, nonuniformity, scene-change rate, logo occupancy, coded geometry, active-picture ratio, caption-style distribution, audio energy, and silence fraction.

**BLOCK-02.** Very short blocks SHALL remain representable. They MAY be combined with neighbors for ad-unit hypotheses; a minimum individual-ad duration SHALL not discard the underlying evidence. Internal cuts within an advertisement SHALL therefore be recoverable through combined-block analysis.

**BASE-01.** Estimate recording-relative brightness, nonuniformity, audio energy, silence fraction, and scene-change rate over usable observation time. Do not let long data gaps or different sampling rates bias these values. Relative audio comparisons SHALL use linear energy, not ratios of negative dBFS numbers. Near-zero baselines SHALL use a documented floor or suppress the affected rule.

**BASE-02.** Estimate typical program caption style and active-picture ratio from positively supported program observations, preferring intervals with validated logo presence or long stable intervals exceeding the maximum individual-ad duration. If insufficient, the duration-dominant recording value MAY be used with an explicit fallback status. A baseline SHALL require configurable minimum support and dominance; ambiguous or multimodal data SHALL disable the corresponding mismatch rule. A channel profile MAY supply an explicit baseline.

**BASE-03.** Freeze baselines before the final scoring pass. Iterative refinement MAY be supported but SHALL have a bounded iteration count and deterministic stopping rules. Report baseline values, support, origin, and reliability. Do not silently treat a preliminary ad classification as ground truth for baseline learning.

## 7. Scoring and ad-unit hypotheses

**SCORE-01.** Each block SHALL start with a neutral heuristic ad score of 1.0. Configured positive finite multipliers SHALL raise or lower the score according to usable evidence. Multiplication or equivalent log-space arithmetic MAY be used. Neutral/unavailable factors SHALL be 1.0. The initial advertisement threshold SHALL be 1.5, inclusive; a configured threshold SHALL be finite and greater than 1.0. Arithmetic SHALL remain finite and deterministic.

**SCORE-02.** Initial scoring rules SHALL be:

| Evidence | Initial multiplier | Conditions |
|---|---:|---|
| Strict standard duration | 2.0 | A single block's duration is within strict tolerance. |
| Loose standard duration | 1.5 | A single block is within loose tolerance but not strict tolerance. |
| Combined-block standard duration | 1.5 | A consecutive multi-block hypothesis has standard duration within strict or loose tolerance. |
| Logo present | 0.1 | Validated logo, sufficient coverage, occupancy at least 0.8. |
| Logo absent | 2.0 | Validated logo, sufficient coverage, occupancy at most 0.2. |
| Caption-style mismatch | 2.0 | Recognized style differs from a reliable program baseline. |
| Active-picture ratio mismatch | 2.0 | Stable ratio differs from a reliable program baseline. |
| Optional relative-statistics rule | 2.0 per enabled metric | The metric exceeds its baseline by the configured ratio. |

All listed weights and occupancy limits SHALL be configurable. A neutral modifier SHALL disable a rule's scoring effect. The same evidence family SHALL apply at most once per scored hypothesis; strict, loose, and combined duration bonuses SHALL not multiply each other. If more than one duration rule applies, use the strongest applicable modifier and report the choice. Repeated events or overlapping combined hypotheses SHALL not stack bonuses on a block.

**SCORE-03.** Standard ad durations SHALL initially be 10, 15, 20, 30, 45, 60, 90, and 120 seconds. The ordered set SHALL be configurable for different markets. Initial strict tolerance SHALL be ±0.5 s and loose tolerance ±1.0 s, inclusive. Matching SHALL use elapsed presentation time and report the target, measured duration, deviation, and tolerance. Boundary uncertainty SHALL be reported separately; increasing tolerance from uncertainty SHALL require an explicit bounded setting and SHALL be off by default.

**SCORE-04.** Evaluate both individual blocks and consecutive groups of blocks. Ad-unit hypotheses SHALL not exceed the configured maximum individual-ad duration. Evaluate each hypothesis once in a deterministic order; overlapping hypotheses SHALL not repeatedly modify constituent block scores. A hypothesis SHALL include duration-weighted aggregates, contributing block IDs, applied factors, and evidence coverage. A group SHALL not absorb a constituent block already meeting the positive program conditions in SCORE-07. Selection of nonoverlapping eligible units SHALL maximize the sum of `unit_duration × log(unit_score)`, with ties resolved by fewer units and then earlier boundaries. Eligibility SHALL include the score threshold, evidence gate, individual-ad duration limits, and complete usable media coverage; unknown signal values do not themselves imply missing media coverage. Unselected time SHALL retain its block-level evidence for final program/unknown decisions.

**SCORE-05.** Optional relative-statistics rules SHALL be individually enabled for brightness, nonuniformity, audio energy, silence fraction, and scene-change rate. Each SHALL compare the block/hypothesis value against a recording baseline multiplied by a configurable excess ratio, initially 1.3. All these rules SHALL be disabled by default. More nonuniformity means less spatial uniformity. Silence fraction means proportion of observed time below the silence threshold; the rule SHALL use that meaning consistently. Minimum support and, for scene rate, minimum event count SHALL be configurable.

**SCORE-06.** An ad-unit hypothesis SHALL require the threshold score and at least one usable evidence family other than duration. Eligible families are a dark/uniform boundary, a corroborated audio/visual boundary, validated logo absence, a reliable caption or aspect mismatch, or an enabled relative-statistics rule. A scene-only cut, resolution change, silence alone, or bumper match alone SHALL not satisfy this gate. Events supporting interval boundaries SHALL be recorded as such. This gate SHALL not be bypassed by repeated duration matches.

**SCORE-07.** Positive program evidence SHALL mean either substantial validated logo presence, or a stable interval longer than the configured maximum individual-ad duration whose known caption style and active-picture ratio both match reliable program baselines. A program assignment also requires a score below the ad threshold, sufficient coverage, and no unresolved conflicting evidence. A caller-supplied program-baseline profile MAY support this evaluation. All remaining unaccepted time SHALL be unknown. Thus a logo-free recording without captions may legitimately contain unknown time even when no ads are accepted.

**SCORE-08.** Preserve raw measurements, initial block scores, hypothesis scores, accepted/rejected hypotheses, and the reasons for classification. An ad-unit/break score SHALL not be presented as a probability or as a calibrated confidence percentage. Quality indicators such as coverage and logo validation SHALL remain separate.

## 8. Break assembly and boundary policy

**BREAK-01.** Individual ad-unit duration SHALL initially be between 4 and 120 seconds inclusive. A unit MAY measure over the maximum by no more than the loose duration tolerance (SCORE-03), so that a unit of the longest standard length is not rejected for a measured duration that still matches it. Accepted complete breaks SHALL initially be between 20 and 600 seconds inclusive. All four limits SHALL be configurable and validated. A group of short blocks MAY satisfy the individual-ad limit together; a long interval SHALL not be arbitrarily split at invented timestamps to satisfy it. A group of blocks whose first or last block carries only program evidence SHALL not earn a standard length from the group's sum; this SHALL be configurable (`scoring.combined_length_program_edges`, on by default).

**BREAK-02.** Select nonoverlapping qualifying ad units under SCORE-04, then assemble adjacent units into breaks. A complete accepted break SHALL have a valid decomposition into units meeting the individual-ad limits and SHALL meet the break limits. If a maximal adjacent run violates the break limits, retain its raw hypotheses and mark that run unknown with a constraint reason. Do not reclassify it as program or slice it into plausible breaks without observed separating evidence.

**BREAK-03.** A gap-bridging rule SHALL merge ad runs separated by short unknown intervals. It SHALL be enabled by default (maximum gap 15 s) and be possible to disable. It SHALL have a maximum gap duration, require an acceptable merged break duration and unit decomposition, preserve each absorbed interval and its reason in diagnostics, and refuse to bridge missing media coverage or positive program evidence, except that a gap whose only program evidence is logo presence MAY be bridged when so configured (on by default). A merged break MAY absorb several gaps; the maximum break duration bounds the sequence.

**BREAK-04.** Recording-edge breaks MAY be truncated. By default `allow_truncated_edges` SHALL be enabled, because recordings routinely start or end inside a break; disabled, the same minimum/maximum duration rules apply at the edges as in the interior. The setting MAY relax the minimum break and minimum unit durations only when the candidate touches time zero or analyzed end. It SHALL still require score/evidence support, retain maximum limits, and report `truncated_start` or `truncated_end`. Being at an edge SHALL never itself establish an ad.

**BREAK-05.** Canonical classification boundaries SHALL use the fused candidates. Optional skip padding SHALL be applied only to a separate derived skip list, SHALL default to zero, SHALL be clamped to the available timeline, and SHALL not cross a positively identified program interval. Merged or clamped skip intervals SHALL be reported separately from detection results.

## 9. Configuration contract

**CFG-01.** The CLI SHALL support UTF-8 YAML configuration with named sections. The Go API SHALL expose equivalent typed options with units. Duration values SHALL use explicit units such as `100ms` or `30s`; ratios SHALL be distinguished from percentages and luminance units. An exported default configuration SHALL contain all supported keys, defaults, ranges, and descriptions.

**CFG-02.** Precedence SHALL be built-in defaults, optional base YAML, explicitly selected channel YAML, then explicit CLI/API overrides. Only explicitly supplied overrides SHALL replace lower layers. Lists SHALL replace rather than concatenate. Paths SHALL resolve relative to the declaring YAML file or, for CLI paths, the current directory. Do not discover or infer a channel profile from a filename or network request. Emit a redacted effective configuration and its stable digest in results.

**CFG-03.** Invalid keys, duplicate keys within one layer, unknown signal names, malformed values, invalid masks/templates, nonfinite numbers, negative durations, inverted ranges, invalid crops, and incompatible track selections SHALL produce actionable configuration errors before analysis when detectable. Require strict tolerance ≤ loose tolerance; present occupancy threshold > absent occupancy threshold; logo validation minimum < maximum; minimum duration ≤ maximum duration for both units and breaks. Any additional interdependent constraints SHALL be documented. A requested unavailable optional media stream SHALL follow an explicit strict-or-degraded policy; the default SHALL be degraded with a warning.

**CFG-04.** The configuration surface SHALL cover at least:

| Group | Required controls | Initial policy |
|---|---|---|
| Input | Track/service selection, decoder choice, unsupported-format policy, timestamp policy | Deterministic automatic tracks; warn for unavailable optional streams |
| Packaged input | Manifest type/detection, variant/Representation selection, associated renditions, package failure policy, resource limits, optional remote retrieval | Local HLS/DASH with CMAF; finite presentations; fixed selection; fail on resource loss; network off |
| Sampling | Visual sampling, full-resolution boundary verification, audio window, measurement regions | Every frame eligible for dark/resolution detection; coarse analysis must report resolution limits |
| Signal enablement | Independent detector switches and boundary-source switches | Dark/uniform, logo, scene measurement, resolution, captions, aspect, and audio enabled; templates enabled when supplied |
| Dark/uniform | Fixed threshold, automatic mode, calibration bounds/support, uniformity and bright-pixel tests | Fixed luminance 19; auto-calibration off |
| Logo | Search regions, edge persistence, sample spacing, geometry limits, support, match threshold, smoothing, validation, occupancy thresholds, asset path | Auto-learn; recording occupancy 0.40–0.92; block absent ≤0.2, present ≥0.8 |
| Scene | Histogram settings, distance/cut threshold, duplicate suppression, minimum count | Scene boundaries off; rate measured |
| Aspect | Border detection, persistence, ratio tolerance, baseline support/dominance | Sustained changes supply boundaries |
| Captions | Service, style persistence, support/dominance; scoring factors for style mismatch, observed absence and captions in an uncaptioned program | Recognized style mismatch enabled; absence neutral; unexpected captions neutral; caption boundaries off |
| Audio | Energy metric, channel policy, window, silence level, minimum silence, alignment | RMS; −50 dBFS; 100 ms silence; 250 ms alignment |
| Bumpers | Template files/data, comparison threshold, region, preprocessing, temporal support | No built-in template assets |
| Boundaries | Event span policy, fusion tolerance, enabled sources | Section 6 policy; 100 ms fusion; logo boundaries off |
| Baselines | Support, coverage, dominance, explicit channel baselines, near-zero floor | Insufficient support disables a comparison |
| Scoring | Standard lengths/tolerances, score threshold, each modifier, relative metrics/ratio, evidence coverage | Section 7 values; relative metrics off |
| Breaks | Unit and break limits, truncated-edge policy, gap bridge | Units 4–120 s (plus loose tolerance); breaks 20–600 s; truncated edges on; gap bridging on (15 s) |
| Outputs | Diagnostics detail, output paths, skip padding, overwrite behavior | JSON; zero padding; no overwrite |
| Resources | Worker limit, memory budget, scratch directory/quota, cancellation | Bounded resources; no full-video buffering |

Thresholds not assigned a numeric default here SHALL be named and defined in the shipped configuration guide. No hidden channel-specific constants or undocumented heuristic switches SHALL affect a result. A configuration/schema version SHALL make default changes observable.

## 10. Public Go library

**API-01.** Provide context-aware analysis operations taking an input source and typed configuration and returning structured results plus errors. The same analysis entry point SHALL accept a recording path or HLS/DASH manifest source; a package SHALL return one presentation-level result. The input abstraction SHALL allow a caller-provided resource resolver for manifest-relative initialization/media reads and byte ranges, with cancellation and explicit ownership. An input abstraction SHALL allow applications to provide decoded observations without invoking the CLI. The exact Go package layout and type names are implementation choices.

**API-02.** Expose typed records for metadata, configuration identity, detector states, feature coverage, candidate boundaries, blocks, hypotheses, final segments, breaks, warnings, and diagnostics. Callers SHALL be able to inspect the evidence for an individual result. Bulk per-frame diagnostics MAY be optional or streamed to avoid excessive memory use.

**API-03.** Concurrent independent analyses SHALL not share mutable recording state, learned logos, configuration, or baselines. Objects documented as safe for reuse SHALL be safe for that use. Cancellation SHALL interrupt decoding and analysis, terminate owned subprocesses, close owned resources, and clean up temporary files. Caller-owned streams SHALL follow an explicit ownership contract.

**API-04.** Provide distinguishable errors for invalid configuration, unsupported input, media tool failure, unusable timing, I/O, cancellation, and resource limits. Missing optional evidence SHALL be a warning/status, not a fatal error. Partial results SHALL be explicitly marked with analyzed coverage and completeness; they SHALL never be presented as a successful complete analysis.

**API-05.** The library SHALL support progress callbacks or events based on observed media time and analysis stage. Progress SHALL distinguish feature extraction from final classification; a preliminary result SHALL not silently become a final one. Library operations SHALL not print to process stdout/stderr or terminate the host process.

## 11. Command-line application

**CLI-01.** Provide the following capabilities, with these command names as the initial interface:

| Invocation | Required behavior |
|---|---|
| `tv-ad-detect analyze INPUT --output RESULT.json` | Analyze one local recording or packaged presentation and write canonical JSON. |
| `tv-ad-detect analyze PACKAGE/master.m3u8 --output RESULT.json` | Resolve HLS renditions and CMAF resources as one recording. |
| `tv-ad-detect analyze PACKAGE/manifest.mpd --output RESULT.json` | Resolve DASH Periods, tracks, and CMAF resources as one recording. |
| `tv-ad-detect analyze INPUT --output -` | Write JSON to stdout and progress/warnings to stderr. |
| `tv-ad-detect analyze INPUT --config BASE.yaml --channel-config CHANNEL.yaml --output RESULT.json` | Apply the documented configuration layers. |
| `tv-ad-detect config defaults` | Print complete default YAML and descriptions. |
| `tv-ad-detect config validate --config BASE.yaml` | Validate configuration and assets without decoding a recording. |
| `tv-ad-detect --version` | Report application, schema, and build identity. |

Allow explicit stream selection, configuration overrides, optional diagnostics, and machine-readable error output. Exact flags beyond those above SHALL be documented before release.

**CLI-02.** Exit codes SHALL be 0 for completed analysis, including zero ads; 2 for argument/configuration errors; 3 for unsupported or unusable media/timing; 4 for I/O or media-tool failures; 5 for resource-limit failures; and 130 for user cancellation. Unexpected internal failures SHALL use a documented nonzero code. Logs SHALL never corrupt JSON on stdout.

**CLI-03.** Named output files SHALL be written atomically on success and SHALL not overwrite existing files unless explicitly requested. An output that is the same file as an input (the recording, a manifest or media resource, a configuration file or an asset), however its path is spelled, or as another output, SHALL be refused whether or not overwriting was requested. Writing partial/error results SHALL require an explicit option and identify incompleteness. Decoder processes SHALL be invoked with argument arrays, without shell interpolation of paths or configuration values.

## 12. Output and diagnostics

**OUT-01.** Canonical JSON SHALL be UTF-8 with a versioned schema. For packaged input, it SHALL identify the input manifest and selected tracks/Periods and expose the source-resource timeline mapping required by PACK-09 through metadata or linked diagnostics. It SHALL include analysis completeness, normalized duration/time unit, input/track metadata, product and media-adapter versions, effective configuration identity, detector states, warnings, baseline/calibration summaries, final segments, and accepted ad breaks. Detailed boundaries, blocks, score factors, and hypotheses SHALL be available through a documented diagnostics level or companion file with stable IDs linking back to segments.

**OUT-02.** Each final segment SHALL contain start/end, label, supporting block IDs, reason codes, and coverage/quality information. Breaks SHALL include constituent ad-unit IDs, evidence links, and truncation flags. Reason codes SHALL be stable machine-readable names; prose explanations MAY supplement them. Raw ad scores SHALL remain associated with the blocks/hypotheses they describe; a merged break SHALL not imply one uniform score across all its time.

**OUT-03.** Final segments SHALL partition the established video timeline exactly once, including unknown intervals. Accepted breaks SHALL correspond to advertisement-labeled time. Derived skip intervals SHALL identify padding/merging and SHALL be separate. Empty detections SHALL be represented as empty arrays. Optional/unavailable measurements SHALL use a documented null/status representation, never invented zeros or non-JSON NaN/infinity values.

The following is an illustrative excerpt, not the full required schema:

```json
{
  "schema_version": "1",
  "complete": true,
  "time_unit": "microseconds",
  "duration_us": 180000000,
  "segments": [
    {"start_us": 0, "end_us": 60000000, "label": "program", "block_ids": ["b0"], "reasons": ["logo_present"]},
    {"start_us": 60000000, "end_us": 90000000, "label": "advertisement", "block_ids": ["b1"], "reasons": ["standard_duration", "logo_absent"]},
    {"start_us": 90000000, "end_us": 180000000, "label": "program", "block_ids": ["b2"], "reasons": ["logo_present"]}
  ],
  "ad_breaks": [
    {"id": "break0", "start_us": 60000000, "end_us": 90000000, "unit_ids": ["u0"], "truncated_start": false, "truncated_end": false}
  ]
}
```

## 13. Reliability, resource use, and repeatability

**QUAL-01.** For the same observations, configuration, assets, and software versions, analysis SHALL produce the same normalized classifications, boundaries, and score explanations. Runtime timestamps, elapsed processing time, and environmental paths MAY differ. Worker scheduling SHALL not alter decisions.

**QUAL-02.** Bound memory and worker use through configuration. Decode buffers SHALL scale with processing windows rather than full recording duration. Compact feature/event storage MAY scale with duration, but SHALL obey a documented memory limit or spill policy. Scratch storage SHALL have a quota. Reaching a limit SHALL produce an explicit resource error or a documented degradation warning; detectors SHALL not silently disappear.

**QUAL-03.** Publish benchmarks using documented hardware, adapter settings, duration, resolution, frame rate, enabled signals, peak memory, scratch use, and processing time. Include a two-hour 1080p recording and a higher-resolution case. No universal real-time throughput claim is required for the first release; numerical performance claims SHALL be backed by measurements.

**QUAL-04.** Default diagnostics SHALL avoid retaining decoded frames, audio samples, caption text, or transcripts (opt-in phrase matching reports configured phrases only, CAP-04). Asset export and detailed visual debug artifacts SHALL require explicit requests and output paths. Recoverable media failures SHALL retain coverage/warnings. Failures SHALL not leave a plausible-looking complete result.

## 14. Acceptance and verification

Use synthetic feature streams for precise algorithm tests and independently created or redistributable media fixtures for adapter/integration tests. Record fixture provenance and expected annotations. Deterministic functional tests SHALL be separate from real-recording accuracy evaluation.

| Test ID | Required scenario and observable result | Requirements |
|---|---|---|
| AT-01 | Constant and variable frame rates, fractional rates, reordered pictures, nonzero starts, and offset audio produce correct microsecond intervals; adjacent segments have no overlap/gap. | TIME-01–03, OUT-03 |
| AT-02 | Dark, uniform colored, nearly dark with a bright object, fades, and persistent black sequences exercise distinct measurements; a long dark program scene is not automatically an ad. | VIS-01–03 |
| AT-03 | Automatic dark/uniform calibration reports estimates on suitable distributions and fixed fallback on insufficient/ambiguous data. A fixed-mode run does not change its threshold. | VIS-02, CFG-04 |
| AT-04 | A stationary edge logo is learned from multiple separated samples; absent/present intervals and smoothing behave predictably. Too-rare/too-common logos disable logo boundaries and modifiers. | LOGO-01–05 |
| AT-05 | Hard scene cuts emit deduplicated events and rates; scene-only editing does not directly create ads; enabling scene boundaries changes candidates without bypassing scoring. | SCENE-01–02, SCORE-06 |
| AT-06 | A coded size change emits one boundary before resizing, invalidates geometry-dependent state, and continues analysis. Initial geometry is not a change event. | SIZE-01–02 |
| AT-07 | Supported caption data distinguishes three styles, observed absence, and unknown; mismatch affects scoring only with a reliable baseline. Unsupported subtitles and unavailable captions remain neutral. | CAP-01–03, BASE-02 |
| AT-08 | Persistent letterbox/pillarbox changes affect active ratio; dark frames and short border transients do not. Pixel aspect ratio affects the displayed measurement correctly. | ASPECT-01–03 |
| AT-09 | Silence meets level/duration limits; missing samples remain unknown; channel energy prevents false silence; offset silence aligns with a nearby visual candidate using presentation time. | AUDIO-01–03, MEDIA-04 |
| AT-10 | A supplied bumper image matches within its configured region; an unrelated image does not; match-only evidence does not label following program time as advertising. | BUMPER-01–03 |
| AT-11 | Continuous events, near-coincident signals, and chains of nearby candidates produce deterministic bounded fusion, preserved causes, and nonempty blocks. Coverage gaps remain identifiable. | BOUND-01–03, BLOCK-01 |
| AT-12 | Standard lengths are tested on and just inside/outside strict and loose limits. Combined short blocks can form one unit. Overlapping hypotheses do not stack duration bonuses. | SCORE-02–04, BLOCK-02 |
| AT-13 | A controlled 30 s logo-absent hypothesis with validated logo support scores 4.0 from strict duration × absence; a comparable logo-present hypothesis scores 0.2. Disabled/missing logo evidence contributes 1.0. | SCORE-01–02 |
| AT-14 | A duration-only hypothesis fails the evidence gate; an enabled relative metric affects only its own factor; near-zero baselines cannot cause nonfinite results. Ambiguous baselines disable mismatches. | BASE-01–03, SCORE-05–07 |
| AT-15 | Units at 4/120 s and breaks at 20/600 s pass duration checks when evidence supports them; just-outside cases fail (for the unit maximum, just outside means beyond the loose duration tolerance). A 10 s interior candidate cannot become a complete default break. | BREAK-01–02 |
| AT-16 | An excessive adjacent ad run is not split without evidence. Gap bridging is on by default, refuses coverage gaps and program gaps (other than logo-only ones when allowed), and enforces the gap and break limits. | BREAK-02–03 |
| AT-17 | Truncated-edge mode (on by default) only relaxes minimums at actual recording edges; disabled, edge candidates follow the interior rules. Skip padding never changes canonical labels or consumes positive program time. | BREAK-04–05 |
| AT-18 | No-ad, missing-audio, missing-caption, and unreliable-logo recordings complete with correct detector status; no usable evidence yields unknown rather than fabricated program certainty. | MEDIA-04, PIPE-02, RESULT-01–02 |
| AT-19 | Base/profile/explicit overrides, relative paths, list replacement, duplicate keys, invalid units/ranges/assets, and effective configuration identity follow the documented contract. | CFG-01–04 |
| AT-20 | Concurrent analyses are isolated; cancellation closes resources; limits and decoder failures yield typed errors and no complete output; repeated runs are deterministic. | API-03–05, QUAL-01–02 |
| AT-21 | CLI/library results agree; stdout is valid JSON; exit codes, existing-output protection, atomic writes, schema validation, and partial-result labeling work as specified. | CLI-01–03, OUT-01–03 |
| AT-22 | Packaged adapter fixtures demonstrate each required container/codec capability, usable captions, geometry changes, and explicit behavior for unsupported HDR/interlacing/timing. | MEDIA-02–05 |
| AT-23 | Finite HLS direct/master playlists resolve nested relative URIs, associated separately segmented audio, changing initialization maps, and explicit/implicit byte ranges; filename order does not affect presentation order. | PACK-01–02, PACK-04–05 |
| AT-24 | Static DASH fixtures exercise multiple Periods, inherited BaseURLs/attributes, Number/Time templates, initialization, start numbers, fixed durations, bounded timeline repeats, SegmentList byte ranges, and explicit Representation selection. All selected Periods are analyzed once. | PACK-01, PACK-03–05 |
| AT-25 | Identical decoded samples provided as a continuous recording and as HLS/DASH CMAF resources yield the same normalized features, scores, labels, and breaks, within timestamp quantization. A supported 30 s ad split across six 5 s resources is one continuous detection; logo learning, silence events, and caption state continue across file boundaries. | PACK-04, PACK-06–07, QUAL-01 |
| AT-26 | Different audio/video segment durations, nonzero sample offsets, composition offsets, discontinuities, Period presentation-time offsets, and overlapping boundary samples preserve one synchronized timeline without rounding drift or duplicate coverage. Genuine gaps stay unknown. | PACK-05–07, TIME-01–03 |
| AT-27 | Missing/corrupt resources and initialization fail by default; explicit degraded mode preserves known gaps and marks output incomplete. Growing/live, encrypted, and unsupported addressing inputs fail clearly. Resource limits and cancellation apply across the whole package. | PACK-01, PACK-03–04, PACK-08, API-03–04 |
| AT-28 | CLI and library accept both manifest formats, return one presentation result with resource/byte-range mappings, and honor fixed selection. Cache identity changes when manifests, selected resources, or initialization data change; resource boundaries do not become truncated edges or ad evidence. | PACK-05, PACK-07–09, API-01, CLI-01, OUT-01 |

**TEST-01.** Detector tests SHALL assert measurements/events and coverage separately from final labels. Scoring tests SHALL inject controlled observations and check factor arithmetic, rule eligibility, and reason codes. Boundary accuracy on controlled media SHALL be within one decoded frame interval plus documented detector sampling/window uncertainty; synthetic timestamp tests SHALL match their specified exact timestamps.

**TEST-02.** Evaluate a separately annotated recording corpus covering logo/no-logo broadcasts, logos retained during ads, letterboxed programs, news, sports, films with dark scenes, quiet program passages, and partial recordings. Report duration-weighted advertisement precision/recall, program time mislabeled as ads, missed ad time, unknown duration fraction, boundary timing error, and results by signal availability. Unknown ad time SHALL count as missed ad time in recall. Report corpus size, annotation policy, settings, and uncertainty; aggregate accuracy SHALL not hide one failing content class.

**TEST-03.** Tune on a separate development set and report results on held-out recordings. Fixed production accuracy targets SHALL be chosen from these measurements before making user-facing accuracy claims. Functional completion requires AT-01–28 and a documented corpus evaluation; a specific precision/recall target is not invented by this specification.

## 15. Implementation handoff and review checklist

The implementation agent SHALL use this specification as the behavioral contract. Before declaring the first release complete, it SHALL deliver:

1. A Go library and CLI implementing the required signal families, single-file and HLS/DASH CMAF inputs, unified presentation timing, scoring, and break policies.
2. A short design note naming the media adapter, supported manifest/addressing subset, rendition selection and resource/timestamp mapping, measurements/metrics, sampling strategy, default thresholds left open here, and any supported degraded modes.
3. Complete default/base/channel configuration examples, versioned asset specifications, API documentation, CLI help, and the canonical JSON schema.
4. An ID-based traceability map from requirements to implementation locations and acceptance tests, with explicit explanations for any unmet SHALL requirement.
5. Automated functional/integration test results, fixture provenance, corpus evaluation, and resource benchmarks.
6. Apache License 2.0 project licensing and an inventory of third-party dependencies/assets and their required notices.

Review SHALL check behavioral correctness and evidence traceability against these requirements. Any required behavior not delivered SHALL be reported as a release gap rather than silently reinterpreted as optional.
