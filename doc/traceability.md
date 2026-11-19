# Requirement traceability

Maps each requirement ID in [requirements.md](requirements.md) to where it is
implemented and how it is verified. Tests carry their acceptance IDs
(`AT-xx`) and requirement IDs in comments, so `grep -rn "AT-12" pkg cmd`
finds the checks for an ID. Paths are relative to the repository root.

Status values: **met**, **partial** (delivered with a stated limit), **gap**
(a required behavior not delivered; see [Release gaps](#release-gaps)).

## Scope and results

| ID | Status | Implementation | Verification |
|---|---|---|---|
| SCOPE-01 | partial | Apache-2.0 `LICENSE`, SPDX headers, `THIRD_PARTY.md` | Inventory must be re-checked per release; see gaps for corpus/benchmark deliverables |
| SCOPE-02 | met | No network code; remote URIs refused (`pkg/manifest/uri.go`, `pkg/ffadapter/source.go`); user logo/bumper assets (`pkg/addetect/assets.go`) | AT-04, AT-10, AT-27 |
| SCOPE-03 | met | Input is opened read-only; no output but the result document | AT-21 |
| SCOPE-04 | partial | All defaults are named keys (`pkg/addetect/config.go`), versioned by `ConfigSchemaVersion` | Defaults are untuned initial choices (TEST-03 gap) |
| RESULT-01 | met | Labels in `pkg/addetect/breaks.go` (`assembleBreaks`), segments in `analyze.go` (`result`) | AT-18 |
| RESULT-02 | met | `Result.AdBreaks` always a list (`analyze.go`) | AT-18, AT-21 |

## Media and timing

| ID | Status | Implementation | Verification |
|---|---|---|---|
| MEDIA-01 | met | `addetect.Source`/`Stream`/`Event` (`input.go`); `ffadapter.Path`, `ffadapter.Package` | AT-01..20 use injected observations; AT-22 |
| MEDIA-02 | met | `pkg/ffadapter` (cgo binding, documented in `doc/design.md` section 2) | AT-22 |
| MEDIA-03 | met | Track selection and metadata in `pkg/ffadapter/reader.c` (`tvad_open`), `reader.go` (`describe`) | AT-22 |
| MEDIA-04 | met | Optional evidence disabled, never fabricated: `reader.go` (`checkCapabilities`), `analyze.go` (`detectorStates`); resolution changes in `extract.go` | AT-06, AT-18, AT-22 |
| MEDIA-05 | met | Luma reduction and range/bit-depth normalization in `reader.c` (`reduce_luma`); dBFS in `timeline.go`; HDR policy in `reader.go` | AT-22 |
| TIME-01 | met | Per-timestamp rational-to-microsecond conversion (`reader.c`), single timeline in `extract.go` | AT-01 |
| TIME-02 | met | Normalization and final-frame duration in `timeline.go` (`buildTimeline`) | AT-01 |
| TIME-03 | met | Gap/reset/overlap/missing-timestamp policy in `extract.go` (`video`), reported in `Result.Timing` | AT-01 |

## Packaged input

| ID | Status | Implementation | Verification |
|---|---|---|---|
| PACK-01 | met | `pkg/manifest` (`Load`); runs in `pkg/ffadapter/package.go` | AT-23, AT-24, AT-27 |
| PACK-02 | met | `pkg/manifest/hls.go` | `pkg/manifest/hls_test.go`, AT-23 |
| PACK-03 | partial | `pkg/manifest/dash.go`; `SegmentBase`/index ranges rejected explicitly (permitted by the requirement) | `pkg/manifest/dash_test.go`, AT-24 |
| PACK-04 | met | Run concatenation and decoder continuity in `package.go` (`segSource`, `startRun`) | AT-23, AT-25 |
| PACK-05 | met | Selection in `pkg/manifest/selection.go`; time interleaving in `package.go` (`Next`) | AT-23, AT-24, AT-26 |
| PACK-06 | met | Epoch offsets and trimming in `package.go` (`offset`, `inEpoch`, `Next`) | AT-26 |
| PACK-07 | met | All detector state lives in the core, unaware of resources | AT-25 |
| PACK-08 | met | `package.go` (`resourceFailed`, `markMissing`); limits in `pkg/manifest` and `package.io_buffer_size` | AT-27 |
| PACK-09 | partial | `Result.Package` (`result.go`), identity in `package.go` (`finish`: manifests, initialization digests and a version of every media resource, the resolver fingerprint or, with `package.hash_media`, a SHA-256 of its bytes), feature identity in `analyze.go`. No feature cache exists, so "cache invalidation" is delivered as the identity only | AT-28 |

## Pipeline and signals

| ID | Status | Implementation | Verification |
|---|---|---|---|
| PIPE-01 | met | `analyze.go` (`Analyze`) orchestrates the stages | all |
| PIPE-02 | met | `DetectorState` with the four statuses and coverage (`analyze.go` `detectorStates`); per-signal coverage thresholds in `score.go` | AT-18 |
| PIPE-03 | met | Single decode pass; whole-recording steps reuse compact measurements. Feature files (`features.go`, `features_io.go`; CLI `--save-features`, feature-file input) replay classification without decoding, keyed by `feature_identity`; feature-affecting keys are refused by name, score weights and other classification keys may change | AT-20; `TestFeatureReplayEqualsDirect`, `TestSaveAndReplayFeatures` |
| PIPE-04 | met | Boundary causes vs score factors are separate records (`boundary.go`, `score.go`); each family applies once | AT-11, AT-12 |
| VIS-01..03 | met | `extract.go` (measurement), `timeline.go` (`detectDark`, `calibrateDark`) | AT-02, AT-03 |
| LOGO-01 | met | `logo.go` (`frame`, `learn`) | AT-04 |
| LOGO-02 | met | `assets.go` (`LogoAsset`, `project`, `export`) | AT-04 |
| LOGO-03 | met | `logo.go` (`evaluate`, `smoothLogoSpans`); occupancy in `block.go`/`score.go` | AT-04 |
| LOGO-04 | met | `logo.go` (`evaluate`, `finish`), per geometry span | AT-04 |
| LOGO-05 | met | `score.go` (`evaluate`), `boundary.go` (logo boundaries opt-in) | AT-04, AT-13 |
| SCENE-01..02 | met | `scene.go`; rates in `block.go`; scene boundaries opt-in in `boundary.go` | AT-05 |
| SIZE-01..02 | met | `extract.go` (`video`: resolution events, geometry epochs, state reset) | AT-06 |
| CAP-01 | met | `pkg/cea608`, `caption.go`; adapter carriage in `reader.c` | AT-07 |
| CAP-02..03 | met | `caption.go`, `block.go`, `score.go` | AT-07 |
| CAP-04 | met | `pkg/cea608/text.go` (text decoder), `phrase.go` (matching), `score.go` (`caption_ad_phrase_factor`, `caption_program_phrase_factor`) | `TestPhraseMatcher`, `TestPhraseFactorEndToEnd` |
| ASPECT-01..03 | met | `aspect.go`; boundaries in `boundary.go`; mismatch in `score.go` | AT-08 |
| AUDIO-01 | met | `reader.c` (`audio_frame`), `input.go` (`AudioWindower`) | AT-09 |
| AUDIO-02..03 | met | `timeline.go` (`detectSilence`), `boundary.go` (alignment), `block.go` | AT-09 |
| BUMPER-01..02 | met | `bumper.go` | AT-10 |
| BUMPER-03 | met | `assets.go` (`LoadAssets`), formats in `doc/configuration.md` | AT-10, AT-19 |

## Boundaries, scoring and breaks

| ID | Status | Implementation | Verification |
|---|---|---|---|
| BOUND-01..03 | met | `boundary.go` (`buildBoundaries`) | AT-11 |
| BLOCK-01..02 | met | `block.go` (`buildBlocks`), multi-block hypotheses in `score.go` | AT-11, AT-12 |
| BASE-01 | met | `block.go` (`recordingBaselines`) | AT-14 |
| BASE-02..03 | met | `block.go` (`programBaselines`); frozen before scoring, no iteration | AT-07, AT-14 |
| SCORE-01..03 | met | `score.go` (`evaluate`) | AT-12, AT-13 |
| SCORE-04 | met | `score.go` (`scoreAndSelect`) | AT-12 |
| SCORE-05 | met | `score.go` (`evaluate`, relative rules) | AT-14 |
| SCORE-06 | met | `score.go` (`evaluate`, boundary evidence); default requires both unit edges (stricter than the minimum, configurable). See known limitation 1 | AT-14 |
| SCORE-07 | met | `score.go` (`stableProgram`), `breaks.go` | AT-18 |
| SCORE-08 | met | `Result.Diagnostics` (`analyze.go` `diagnostics`) | AT-12 |
| BREAK-01..02 | met | `breaks.go` (`assembleBreaks`) | AT-15, AT-16 |
| BREAK-03 | met | `breaks.go` (gap bridge on by default, 15 s; chains while the merged break fits `max_break`; logo-only program gaps per `bridge_logo_program`) | AT-16 |
| BREAK-04 | met | `breaks.go`, `score.go` (edge relaxation) | AT-17 |
| BREAK-05 | met | `breaks.go` (`skipIntervals`) | AT-17 |

## Configuration, API, CLI, output, quality

| ID | Status | Implementation | Verification |
|---|---|---|---|
| CFG-01 | met | YAML layers and typed options in `config.go`; `WriteDefaultYAML` in `configfile.go` | AT-19 |
| CFG-02 | met | `configfile.go` (`LoadYAML`, `Set`, `identity`); CLI layers in `cmd/main.go` (`loadConfig`) | AT-19 |
| CFG-03 | met | `configfile.go` (tag checks), `config.go` (`Validate`), `assets.go` | AT-19 |
| CFG-04 | met | `config.go` sections; `doc/configuration.md` | AT-19 |
| API-01 | met | `addetect.Analyze`; `ffadapter.Path`/`Package`; `manifest.Resolver` | AT-25, AT-28 |
| API-02 | met | `result.go`; streamed per-frame diagnostics via `Options.FrameObserver` | AT-21 |
| API-03 | met | Per-call state, cloned config, cancel paths in `analyze.go`, `reader.go` | AT-20 |
| API-04 | met | `errors.go`; partial results in `analyze.go` | AT-20, AT-27 |
| API-05 | met | `Options.Progress` stages; no stdout/stderr use, panics converted | AT-20 |
| CLI-01 | met | `cmd/main.go` | AT-21 |
| CLI-02 | met | `cmd/main.go` (`exitCodeFor`); internal failures exit 1 | AT-21 |
| CLI-03 | met | `cmd/main.go` (`writeAtomic`, `checkOutputAliases`; package resources via `ffadapter.Source.LocalInputs`); no decoder process exists | AT-21 |
| OUT-01..03 | met | `result.go`, `analyze.go`; schema `doc/result.schema.json` (generated by `internal/schemagen`) | AT-21 |
| QUAL-01 | met | Single-goroutine core, ordered frame delivery | AT-20, AT-25 |
| QUAL-02 | partial | `resources.workers`, `resources.memory_budget` (explicit resource error, checked at every growth point and before classification), bounded package I/O. No spill policy and no scratch use, so the scratch quota is trivially met | AT-20 |
| QUAL-03 | partial | `doc/benchmarks.md` | See gaps |
| QUAL-04 | partial | No frames, samples or caption text retained; exports only on explicit request; truncated inputs are marked incomplete. See known limitation 2 | AT-07, AT-21 |

## Test suites

| Location | Scope |
|---|---|
| `pkg/addetect/*_test.go` | AT-01 to AT-20 on injected observations, configuration (AT-19), output invariants (OUT-03), regression tests, benchmarks. 97.6% statement coverage. |
| `pkg/cea608/cea608_test.go` | Caption style decoding (AT-07). 100% statement coverage. |
| `pkg/manifest/*_test.go` | HLS/DASH manifest resolution (PACK-01 to PACK-03, PACK-05). |
| `pkg/ffadapter/*_test.go` | Media matrix, real-media timing, a synthetic annotated broadcast, HLS/DASH packages, failure policies, lifecycle (AT-01, AT-06, AT-22 to AT-28). Fixtures are synthesized at test time by `scripts/make-fixtures.sh` with the `ffmpeg` tool; tests skip without it. |
| `cmd/main_test.go` | The built CLI (AT-21, AT-19, AT-28). |

`go test -short ./...` skips the long and optional tests. An optional test
analyzes a local, non-redistributable real recording when it is present.

## Release gaps

Required behaviors or deliverables that are **not** met by this baseline:

1. **TEST-02, TEST-03 (corpus evaluation and held-out accuracy).** No
   annotated corpus exists in this repository, so there is no
   precision/recall report and the defaults are untuned. The analyzer has
   been exercised on one real 35-minute 1080p recording without ground-truth
   annotations. No accuracy claim is made.
2. **QUAL-03 (benchmarks).** `doc/benchmarks.md` reports measurements on one
   machine. The two-hour and higher-resolution cases were produced from the
   single available real recording (looped / upscaled), not from independent
   recordings.
3. **AT-22 caption fixtures.** CEA-608 carriage is verified against
   synthetic cc_data at the core level and against a non-redistributable
   local recording; there is no redistributable media fixture with embedded
   captions, because the fixture tooling cannot synthesize A/53 caption data.
4. **Media matrix beyond H.264.** HEVC/MPEG-2 video and MPEG-TS HLS segments
   decode through the same path but have no fixtures.
5. **Remote retrieval** (optional in SCOPE-02) is not implemented; a caller
   can supply its own resolver.
6. **Measurement cache**: feature files are implemented (PIPE-03). Scene,
   aspect, caption and bumper thresholds remain feature keys; only their
   events are stored, so changing them needs a decode.

## Known limitations and open questions

Found in review and left open; none is hidden by the result document.

1. **A single class mismatch can make an ad (spec tension).** With the
   required multipliers, validated logo absence, a caption-style mismatch or
   an aspect mismatch alone scores 2.0 against the 1.5 threshold and is an
   eligible evidence family (SCORE-02, SCORE-06), so a 37 s letterboxed or
   logo-free interval is labeled an advertisement without a duration match.
   ASPECT-03 says a mismatch "SHALL not independently prove an ad". The
   scoring table was followed; raising `scoring.threshold` above 2 requires
   two factors. This needs a decision in the requirements.
2. **Corrupt media inside an intact container.** A package segment whose
   boxes are intact but whose sample data is garbage decodes with errors
   (`timing.decode_errors`, `decode_error` warnings, the affected time
   becomes a `timestamp_gap`), but the resource status stays `ok` and the
   result stays `complete: true` unless the error ratio exceeds
   `input.max_decode_error_ratio`.
3. **Scaled logo assets.** Applying a logo asset to a larger analysis
   geometry than it was made for matches poorly (the nearest-neighbour mask
   is thicker than the edges it must match); applying it to a smaller
   geometry works. Export assets at the geometry they will be used with.
4. **Package audio resources** carry declared times and status but no
   per-resource presented time span (video resources do); the epoch's
   `media_offset_us` gives the mapping.
5. **Memory budget granularity.** `resources.memory_budget` is checked every
   4,096 frames.
6. **Dense texture over the logo.** A logo observation is unknown when the
   area around the logo is mostly edges (`logo.max_background_density`);
   below that density, busy content can still read as logo present.
   `logo.min_specificity` (0.1 by default) catches text in the logo's
   place, such as a phone number on an ad, at the cost of rejecting a
   learned "logo" that is itself on-screen text.
