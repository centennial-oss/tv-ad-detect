# Configuration guide

Configuration schema version: 1. Every supported key, with its default, range
and description, is printed by:

```bash
tv-ad-detect config defaults
```

A copy is kept in [examples/default.yaml](examples/default.yaml). That output
is the reference for individual keys; this guide covers the contract around
them (CFG-01 to CFG-04).

## Format

Configuration files are UTF-8 **YAML**: a mapping of sections to mappings of
keys.

```yaml
scoring:
  threshold: 1.5
  standard_durations: [10s, 15s, 20s, 30s, 45s, 60s, 90s, 120s]
audio:
  silence_level_dbfs: -50
  min_silence: 100ms
```

| Value kind | Written as | Examples |
|---|---|---|
| Duration | Number with an explicit unit | `100ms`, `30s`, `2m` |
| Ratio | Plain number 0..1 (never a percentage) | `0.8` |
| Luminance | Number on the canonical 0-255 scale | `19` |
| Level | dBFS | `-50` |
| Size | Integer with `B`, `KiB`, `MiB` or `GiB` | `512MiB` |
| Region | `[x, y, w, h]`, normalized to the frame (0..1) | `[0.66, 0.03, 0.32, 0.26]` |
| List | YAML sequence; a list in a higher layer **replaces** the lower one | `[10s, 15s, 30s]` |
| Switch | `true` / `false` | |

Signals and boundary sources are named keys, never bit masks.

## Layers and precedence

1. Built-in defaults.
2. Base file: `--config BASE.yaml`.
3. Channel file: `--channel-config CHANNEL.yaml`. A channel profile is only
   ever selected explicitly; nothing is inferred from file names.
4. Explicit overrides: `--set section.key=value` (repeatable) and the
   dedicated flags (`--video-track`, `--audio-track`, `--caption-track`,
   `--diagnostics`, `--overwrite`, `--write-partial`). In `--set`, lists are
   comma-separated and lists of regions are separated by `;`.

Only keys that a layer actually contains replace lower layers. Relative paths
inside a file resolve against that file's directory; paths given on the
command line resolve against the working directory.

Library callers get the same layers with `addetect.DefaultConfig()`,
`(*Config).LoadYAMLFile` / `LoadYAML`, `(*Config).Set`, or by assigning the
typed fields directly.

The result records the effective configuration with paths reduced to their
base names, and a digest (`config.digest`). The digest covers every
detection-relevant key and the content digests of logo and bumper assets; it
ignores the `outputs` and `resources` sections and machine-specific paths, so
equal digests mean equal detection settings.

## Validation

These are configuration errors (CLI exit code 2), reported before any
decoding, with the file and line where applicable:

- unknown sections or keys, duplicate keys within one layer, more than one
  YAML document in a file, invalid UTF-8;
- malformed values: durations without a unit, non-numeric or non-finite
  numbers, values outside the documented range, unknown enumeration values;
- invalid regions (must satisfy `0 <= x`, `0 <= y`, `w > 0`, `h > 0`,
  `x + w <= 1`, `y + h <= 1`);
- invalid track selections;
- assets that cannot be read, fail their integrity check, are not valid PNG /
  logo assets, or have no usable content;
- interdependent constraints:

| Constraint |
|---|
| `scoring.strict_tolerance` <= `scoring.loose_tolerance` |
| `scoring.logo_present_occupancy` > `scoring.logo_absent_occupancy` |
| `scoring.standard_durations` non-empty, positive, strictly increasing |
| `scoring.threshold` finite and > 1 |
| `logo.min_occupancy` < `logo.max_occupancy` |
| `logo.absent_threshold` <= `logo.present_threshold` |
| `logo.min_width` <= `logo.max_width`, `logo.min_height` <= `logo.max_height` |
| `breaks.min_unit` <= `breaks.max_unit`, `breaks.min_break` <= `breaks.max_break` |
| `dark.auto_min_luma` <= `dark.auto_max_luma`, `dark.auto_uniform_min` <= `dark.auto_uniform_max` |
| `scene.gradual_threshold` >= `scene.cut_threshold` when gradual detection is on |
| every bumper template has a unique id, an image, a valid region, `geometry` of `stretch` or `strict`, `threshold` in (0, 1] and a non-negative `min_support` |

```bash
tv-ad-detect config validate --config base.yaml --channel-config channel.yaml
```

validates the layered configuration and its assets without decoding a
recording.

## Sections

| Section | Controls |
|---|---|
| `input` | Track selection, decoder, unsupported-format and optional-stream policies, timestamp policies, decoder deblocking skip. |
| `package` | Manifest type, variant/rendition selection, failure policy, manifest and segment limits, read buffer. |
| `sampling` | Analysis image bound, measurement region, full-resolution bright check, all-frame or reference-frame decoding. |
| `signals` | One switch per detector. |
| `dark`, `logo`, `scene`, `aspect`, `captions`, `audio`, `bumpers` | Detector thresholds, temporal filtering and sampling. |
| `boundaries` | Fusion tolerance and the individually selectable boundary sources. |
| `baselines` | Support, dominance, near-zero floors, explicit channel baselines. |
| `scoring` | Threshold, standard durations and tolerances, every modifier, occupancy limits, per-signal evidence coverage, relative-statistics rules, evidence-gate edge policy. |
| `breaks` | Unit and break limits, truncated edges, gap bridging. |
| `outputs` | Diagnostics level, skip padding, overwrite and partial-result policy. |
| `resources` | Decoder workers, memory budget, scratch settings. |

No hidden channel-specific constants exist: every threshold that affects a
result is one of these keys.

Example layers: [examples/base.yaml](examples/base.yaml),
[examples/channel-example.yaml](examples/channel-example.yaml).

## Assets

Assets are never built in, never learned onto disk implicitly and never
overwritten without an explicit request (BUMPER-03).

### Logo asset (`tvad-logo`, version 1)

A JSON document:

| Field | Meaning |
|---|---|
| `format` | `"tvad-logo"` |
| `version` | `1` |
| `asset_version` | Free-form version label, reported in results |
| `analysis_width`, `analysis_height` | Analysis geometry the coordinates refer to |
| `x`, `y`, `width`, `height` | Logo box in analysis pixels |
| `mask_png_base64` | 8-bit grayscale PNG of `width` x `height`; nonzero pixels are logo edge pixels |
| `mask_sha256` | Hex SHA-256 of the PNG bytes (integrity check) |

Loading checks the format and version, the box against the geometry, the PNG
dimensions against the box, the digest, and that the mask is not empty. For a
recording with a different analysis geometry the box and mask are scaled by
the ratio of the geometries (nearest neighbour); when the horizontal and
vertical ratios differ by more than 2% the asset is incompatible for that
geometry, which is reported and makes logo evidence unavailable there (never
absence).

Select it with `logo.asset: path.json`, or in the library with
`Config.LogoAssetData`. Export a learned logo explicitly with
`--export-logo PATH` (library: `Options.OnLogoLearned`).

### Bumper templates

Declared under `bumpers.templates`, keyed by template id:

| Key | Meaning | Default |
|---|---|---|
| `image` | PNG file (any color type; converted to luma) | required |
| `region` | Normalized frame region the image occupies | `[0, 0, 1, 1]` |
| `geometry` | `stretch`: scale to the region. `strict`: the image aspect must match the region's display aspect within 2%, else the template is unavailable for that geometry | `stretch` |
| `threshold` | Minimum zero-mean normalized cross-correlation, (0, 1] | `0.9` |
| `min_support` | Minimum duration of consecutive matches | `400ms` |
| `sha256` | Optional hex SHA-256 of the PNG file (integrity check) | none |

Images must be 8 to 16384 pixels per side and contain luminance structure.
Library callers can pass PNG bytes directly in `BumperTemplate.PNG`.
