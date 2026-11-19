# Benchmarks

Measured on 2026-11-16 (QUAL-03). These are measurements on one machine, not
throughput guarantees.

## Setup

| Item | Value |
|---|---|
| Machine | Apple M3 Max, 16 cores, 128 GiB RAM, macOS 27.2.1 |
| Build | `make build` (Go 1.27.4, cgo `-O3`), FFmpeg 9.0.2 libraries (software decoding) |
| Command | `/usr/bin/time -l bin/tv-ad-detect analyze INPUT -o /dev/null --quiet` |
| Adapter settings | Defaults unless noted: `resources.workers` = 16 decoder threads, `input.skip_loop_filter: nonref`, `sampling.decode_frames: all` |
| Enabled signals | All defaults: dark/uniform, logo (auto-learn), scene, resolution, captions (embedded CEA-608), aspect, audio. No bumper templates. Diagnostics `summary`. |
| Scratch storage | None is used in any configuration. |

## Inputs

| Name | Content |
|---|---|
| 35 min 1080p | A real DVR recording: H.264 High 1920x1080, 29.97 fps, 6.1 Mb/s, stereo AAC 48 kHz, embedded CEA-608 captions; 63,209 frames. |
| 2 h 21 min 1080p | The same recording stream-copied four times end to end (8,457 s, 253,463 frames, 6.4 GB). A scaling test for duration, not an independent recording. |
| 10 min 2160p | The first 10 minutes of the recording re-encoded to H.264 3840x2160 (x264 ultrafast, 28 Mb/s). A decode-cost test for a higher resolution, not native 4K material. |

## Results

| Input | Settings | Wall time | Speed vs real time | CPU time (user + sys) | Peak RSS |
|---|---|---:|---:|---:|---:|
| 35 min 1080p | defaults | 28.2 s | 75x | 269 s | 211 MiB |
| 35 min 1080p | `sampling.decode_frames: reference` | 25.4 s | 83x | 180 s | 202 MiB |
| 35 min 1080p | `resources.workers: 4` | 88.3 s | 24x | 261 s | 132 MiB |
| 2 h 21 min 1080p | defaults | 119.9 s | 71x | 1,149 s | 382 MiB |
| 10 min 2160p | defaults | 17.4 s | 35x | 129 s | 464 MiB |

Observations:

- **Decoding is the floor.** Decoding the 35-minute recording's video with the
  `ffmpeg` tool alone and discarding the frames (`-f null`, 16 threads) took
  26.4 s on the same machine; the complete analysis (video, audio, captions,
  every detector, scoring) takes 28.2 s.
- **Memory grows slowly with duration.** Four times the duration added about
  170 MiB: 16 bytes per frame, 24 bytes per audio window and about 3 KiB per
  logo observation (two per second). The rest is decoder state, which scales
  with resolution and worker count, not duration (464 MiB at 2160p for 10
  minutes; 132 MiB at 1080p with 4 workers).
- **Reference-frame decoding** cuts CPU time by about a third on this stream
  (71% of its frames are B-frames) at the cost described in the design note.
- **Hardware decoding was not adopted.** VideoToolbox through the `ffmpeg`
  tool decoded the same recording in 3 min 51 s wall (46 s CPU): far less CPU,
  but about 8 times slower than threaded software decoding.

## Micro-benchmarks

`make bench` (Go benchmarks, same machine):

| Benchmark | Time per op | Allocations per op |
|---|---:|---:|
| Per-frame extraction, 384x216 analysis image, all detectors and one bumper template (`BenchmarkExtractVideoFrame`) | 32 µs | 0 |
| Scoring and selection, 4,000 blocks / 9,103 hypotheses (`BenchmarkScoreAndSelect`) | 0.59 ms | 20 |
| CEA-608 decode of one frame's caption bytes (`BenchmarkFeed`) | 18 ns | 0 |

The per-frame path performs no heap allocation in steady state; retained
storage grows by amortized appends only.
