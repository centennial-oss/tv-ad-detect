# Third-party inventory

Source distributed as part of `tv-ad-detect` is licensed under the Apache
License 2.0 (SCOPE-01). This file lists what the product depends on, how it is
used and what redistribution of a built binary requires.

## Compiled into the binary

| Component | Version | License | Use | Distribution note |
|---|---|---|---|---|
| Go standard library and runtime | 1.27 | BSD-3-Clause | everything | Reproduce the Go license notice with binaries. |
| `go.yaml.in/yaml/v3` | 3.0.6 | MIT and Apache-2.0 (dual-noticed) | Parsing YAML configuration layers | Reproduce its `LICENSE` and `NOTICE` with binaries. |

## Linked at build time, not distributed in this repository

| Component | License | Use | Distribution note |
|---|---|---|---|
| FFmpeg `libavformat`, `libavcodec`, `libavutil` (tested with FFmpeg 9.0) | LGPL-2.1-or-later by default; GPL-2.0-or-later / GPL-3.0-or-later when FFmpeg was configured with `--enable-gpl` / `--enable-version3` | Demuxing and decoding in `pkg/ffadapter` through cgo (dynamic linking) | The license of a distributed binary follows the FFmpeg build it links. Linking an LGPL build dynamically keeps this project's Apache-2.0 terms for its own code, subject to the LGPL's relinking and notice conditions. Linking a GPL-configured build (for example Homebrew's default, which enables `--enable-gpl --enable-version3`) makes the combined binary distributable only under the GPL. Codec patent licensing (H.264, AAC, AC-3) is the distributor's responsibility. |

The pure-Go packages (`pkg/addetect`, `pkg/manifest`, `pkg/cea608`) have no
FFmpeg dependency; only `pkg/ffadapter` and the CLI do.

## Test tooling only

| Component | Use |
|---|---|
| `ffmpeg` / `ffprobe` command-line tools | Synthesizing media fixtures for the adapter and CLI integration tests. Never invoked by the library or the CLI. |

## Assets

No logo, bumper or media assets are distributed. Test media is synthesized
from FFmpeg's built-in generators at test time.

This inventory must be re-checked before each release (SCOPE-01).
