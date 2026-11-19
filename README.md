# tv-ad-detect

A Go library and command-line tool that analyzes recorded video and reports the
intervals likely to be advertisements, with the evidence behind each decision.

- Offline and local: no cloud service, model download or network access.
- One decode pass; detection runs on compact features, never on retained video.
- Eight signal families: black/uniform frames, station logo, scene changes, coded
  resolution changes, CEA-608 caption style, active-picture aspect ratio,
  silence/audio level, and known bumper templates.
- Inputs: MP4, Matroska and MPEG-TS recordings, and finite local HLS (`.m3u8`)
  or DASH (`.mpd`) presentations of CMAF resources, analyzed as one recording.
- Output: a versioned JSON document with a complete `advertisement` / `program`
  / `unknown` timeline, accepted ad breaks, a derived skip list and diagnostics.

Detection is heuristic. Scores are relative evidence, not probabilities, and
time without positive evidence is reported as `unknown` rather than guessed.

The behavioral contract is [doc/requirements.md](doc/requirements.md). The
[design note](doc/design.md), [configuration guide](doc/configuration.md),
[result schema](doc/result.schema.json) and [traceability map](doc/traceability.md)
describe what is implemented and what is still open.

## Build

Requirements: Go 1.27+, a C compiler, and the FFmpeg development libraries
(`libavformat`, `libavcodec`, `libavutil`) discoverable through `pkg-config`.
The `ffmpeg` program is not used by the product (only by the test suite to
synthesize media fixtures).

```bash
make build
```

```bash
make check
```

The [development guide](doc/development.md) covers local test recordings and
the optional real-recording test.

## Command line

```bash
bin/tv-ad-detect analyze recording.ts --output result.json
```

```bash
bin/tv-ad-detect analyze package/master.m3u8 --output -
```

```bash
bin/tv-ad-detect analyze recording.mp4 --config base.yaml --channel-config channel.yaml --set scoring.threshold=2 -o result.json
```

Save a recording's measurements once, then re-classify them under other
scoring, boundary, baseline, break, dark/uniform or logo-presence settings in
well under a second, without decoding:

```bash
bin/tv-ad-detect analyze recording.ts --save-features recording.tvadf -o result.json
```

```bash
bin/tv-ad-detect analyze recording.tvadf --set scoring.threshold=2 -o result-2.json
```

A setting that changes the measurements themselves (a feature key, marked in
`config defaults`) is refused for a feature file, naming the key.

Cut lists for players and editors, next to the canonical JSON: an EDL of the
skip intervals, or chapter files of the labeled segments. Times are on the
player's clock by default (0 is the start of the file; tuner captures start
their audio before the first video frame), or `--cutlist-clock result`:

```bash
bin/tv-ad-detect analyze recording.ts -o result.json --cutlist edl:recording.edl --cutlist ffmetadata:chapters.txt
```

Save a frame, or part of one, as a bumper template image; the command prints
the configuration snippet that uses it:

```bash
bin/tv-ad-detect bumper capture recording.ts --at 23m19s --region 0.3,0.3,0.4,0.4 -o station-card.png
```

Other options worth knowing: `logo.cache_dir` with `logo.cache_key` reuses a
channel's learned logo across recordings; `input.program` picks one program
of a multi-program transport stream; `input.hardware_decode: auto` decodes
video in hardware where available (much less CPU, not faster);
`captions.ad_phrases` and `captions.program_phrases` turn on opt-in caption
phrase matching (only the configured phrases are ever reported, never the
caption text).

```bash
bin/tv-ad-detect config defaults
```

```bash
bin/tv-ad-detect config validate --config base.yaml
```

`tv-ad-detect --help` lists every flag. Exit codes: `0` success (including no
ads found), `1` internal failure, `2` argument or configuration error, `3`
unsupported or unusable media/timing, `4` I/O or media failure, `5` resource
limit, `130` canceled. With `--output -` the JSON goes to stdout and everything
else to stderr. Named outputs are written atomically and never replace an
existing file without `--overwrite`. An output that is the same file as an
input (the recording, a manifest or segment, a configuration file or an
asset) is refused even with `--overwrite`.

## Library

```go
package main

import (
	"context"
	"fmt"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/ffadapter"
)

func main() {
	cfg := addetect.DefaultConfig()
	cfg.Outputs.Diagnostics = "detailed"

	res, err := addetect.Analyze(context.Background(), ffadapter.Path("recording.ts"), cfg, nil)
	if err != nil {
		panic(err) // *addetect.Error; inspect with addetect.KindOf(err)
	}
	for _, b := range res.AdBreaks {
		fmt.Printf("ad break %s: %d us .. %d us\n", b.ID, b.StartUS, b.EndUS)
	}
}
```

Packages:

| Package | Purpose |
|---|---|
| `pkg/addetect` | Detection core (pure Go): configuration, observation input types, the pipeline and the result schema. `Analyze` takes any `Source`. |
| `pkg/ffadapter` | Packaged media adapter (cgo, FFmpeg libraries): `Path` for files and manifests, `Package` for a caller-provided resource resolver. |
| `pkg/manifest` | HLS/DASH manifest resolution into ordered CMAF resources (pure Go). |
| `pkg/cea608` | CEA-608 caption presentation-style decoder (pure Go). |

Applications that already have decoded data implement `addetect.Source` (or use
`addetect.SliceSource`) and never touch the adapter or FFmpeg.

Measurements can be kept and classified again: `Options.OnFeatures` hands out
an `addetect.Features` after extraction, `Features.WriteTo` and
`addetect.ReadFeatures` store and load it, and `addetect.AnalyzeFeatures`
classifies it with the same result `Analyze` would give for the same settings.

## License

Apache License 2.0; see [LICENSE](LICENSE) and
[THIRD_PARTY.md](THIRD_PARTY.md) for the dependency inventory.
