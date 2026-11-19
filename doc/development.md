# Development guide

## Build and test

```bash
make build
```

```bash
make check
```

`make build` needs the FFmpeg development libraries (libavformat, libavcodec,
libavutil) through pkg-config. `make fixtures` synthesizes the small media
fixtures the adapter tests use; it needs the `ffmpeg` command-line tool, which
is test tooling only.

## Local test recordings

Real recordings are not redistributable, so they are never committed.
Everything under `ref/` is ignored by git, and every test that reads from it
skips when the file is absent.

### Layout

```
ref/
  sample-dvr.mp4 -> testdata-ota-2026/ch-4-1/ch-4-1.mp4
  testdata-ota-2026/
    <name>/
      <name>.mp4            H.264 video, AAC audio
      <name>.h264.mkv       the same H.264 video, original AC-3 audio
      <name>.mpeg2.mkv      original MPEG-2 video and AC-3 audio
      <name>.srt            caption text
      cmaf/manifest.mpd     DASH manifest  \ one shared set of
      cmaf/master.m3u8      HLS playlists  / CMAF segments
      original/<name>.ts    the capture itself
```

### Making the recordings

1. Capture an over-the-air channel from an HDHomeRun tuner to a transport
   stream, without transcoding, into `ref/`:

   ```bash
   ffmpeg -probesize 10M -analyzeduration 10M -fflags +genpts+discardcorrupt -i "TUNER_URL" -map 0:v:0 -map 0:a -c copy -t 2100 -f mpegts ref/NAME.ts
   ```

2. Build the derivatives. With no arguments the script processes every
   `ref/*.ts`, writes each set to `ref/derived/<name>/`, then moves the
   capture into that set's `original/` folder:

   ```bash
   scripts/make-ref-derivatives.sh
   ```

   `-o DIR` (or `OUT_DIR=DIR`) chooses another output root.

3. `ref/derived/` is a staging area. Once a set is checked, move its
   folder by hand into the test data directory it belongs to, for example
   `ref/testdata-ota-2026/`.

The script encodes the video once, straight to MP4, and builds the CMAF
package from that MP4, so both keep exact frame timestamps. Finished outputs
are skipped on a re-run. Its header lists the options (`FORCE`, `SKIP_MKV`,
`KEEP_SOURCE`, `LIMIT`, `THREADS`, `OUT_DIR`, `PRESET`, `CRF`, `SEGMENT`).
`LIMIT=60` gives a quick trial on the first minute and leaves the capture in
place; `THREADS=4` keeps the machine cool at the cost of a longer run.

The encode is done in software on purpose: the VideoToolbox hardware encoder
in FFmpeg 9.0.2 fails when asked to carry closed captions.

Things to know about tuner captures:

- The audio starts before the first decodable video frame (by one to two
  seconds in our captures), so a container's duration is longer than the
  video timeline our result is measured on.
- Captions (CEA-608/708) travel inside the video stream, so they survive a
  stream copy, and survive the H.264 encode because the script passes
  `-a53cc 1`.
- These captures cannot be used to study transport-level tables: writing them
  through ffmpeg rewrites the program tables. That needs the tuner's raw
  bytes (for example `curl -o NAME.ts "TUNER_URL"`).

### The optional real-recording test

`TestRealRecordingOptional` in `pkg/ffadapter` analyses `ref/sample-dvr.mp4`
from end to end. It skips when the file is absent, with `-short`, and under
the race detector unless `TVAD_REAL_RECORDING=1` is set.

`ref/sample-dvr.mp4` may be a symbolic link. The test's assertions expect a
captioned 1080-line H.264 recording at 29.97 frames per second with AAC
audio, which is what the script's MP4 of a 1080i capture is:

```bash
ln -s testdata-ota-2026/ch-4-1/ch-4-1.mp4 ref/sample-dvr.mp4
```

```bash
go test ./pkg/ffadapter -run TestRealRecordingOptional -count=1 -v
```

Do not link it to an MKV (timestamps there are rounded to whole
milliseconds, which fails the frame-duration check) or to a 720p capture.

### Checking every format of the test recordings

`TestTestdataFormatsAgree` in `pkg/ffadapter` analyzes every format of a
capture and checks that they agree: MP4 and HLS exactly, DASH up to its
declared end, the original transport stream and the MKVs within one frame.
It decodes several 35-minute recordings (about 3 minutes per channel), so it
runs only on request:

```bash
TVAD_TESTDATA=1 go test ./pkg/ffadapter -run TestTestdataFormatsAgree -timeout 60m -v
```

`TVAD_TESTDATA_CHANNELS=ch-2-1,ch-9-1` chooses the channels (default
`ch-2-1,ch-4-1`).

## Synthetic fixtures

`scripts/make-fixtures.sh` builds every fixture from FFmpeg's signal
generators (nothing is copied from a real recording). Among them: a
two-program transport stream (`mpts`), HEVC and MPEG-2 in transport stream
and MP4 with large and wrapping timestamps (`codecs`), and lossless flat-gray
HEVC at 8 and 10 bits for the hardware decoding test (`hevc_gray`). The
hardware decoding test runs only where a hardware decoder is available
(VideoToolbox on macOS) and otherwise checks the fallback warning.

