#!/usr/bin/env bash
# Builds test derivatives of over-the-air MPEG-2 transport stream captures.
#
# Usage:
#   scripts/make-ref-derivatives.sh [-o OUT_DIR] [CAPTURE.ts ...]
#
#   scripts/make-ref-derivatives.sh                   # every capture found
#   scripts/make-ref-derivatives.sh ref/testfile.ts   # selected captures
#   scripts/make-ref-derivatives.sh -o ref/trial      # another output root
#
# With no captures named it processes ref/*.ts and, for re-runs, the
# captures it already filed under OUT_DIR/*/original/. The output root is
# a staging area: move finished sets from it to wherever they belong.
#
# For each capture NAME.ts it writes, under OUT_DIR/NAME/:
#   NAME.mp4             H.264 video, AAC audio               (the one encode)
#   cmaf/manifest.mpd    DASH   \ one set of CMAF segments, described by
#   cmaf/master.m3u8     HLS    / both manifest types
#   NAME.h264.mkv        the MP4's H.264 video, original AC-3 audio
#   NAME.mpeg2.mkv       original MPEG-2 video and AC-3 audio (remux only)
#   NAME.srt             caption text, for choosing test phrases
#   original/NAME.ts     the capture itself, moved here once all is built
#
# The video is encoded once, straight to MP4, so the MP4 and the CMAF
# package keep exact frame timestamps (Matroska rounds them to whole
# milliseconds, which is why nothing is derived from an MKV). The encoder
# and the MP4 video track keep the capture's 90 kHz time base: with the
# default frame-rate time base, the first frame snaps to a whole frame from
# zero, which moves the video up to half a frame against the audio.
# Every audio track is kept. Closed captions travel inside the video stream.
#
# Existing outputs are skipped; set FORCE=1 to rebuild them.
#
# Environment:
#   OUT_DIR      output root; -o overrides it   (default: ref/derived)
#   PRESET       libx264 preset                 (default: fast)
#   CRF          libx264 quality                (default: 20)
#   SEGMENT      segment and keyframe interval  (default: 6, seconds)
#   THREADS      encoder threads; a low number (for example 4) keeps the
#                machine cool at the cost of a longer run (default: all cores)
#   SKIP_MKV     1 to not build missing MKV files (existing ones are kept)
#   KEEP_SOURCE  1 to leave the capture where it is instead of moving it
#   LIMIT        only process the first N seconds of each capture; a trial
#                run, so the capture is never moved (default: all)
#   FORCE        1 to rebuild existing outputs

set -euo pipefail

readonly DEFAULT_OUT_DIR="ref/derived"
OUT_DIR="${OUT_DIR:-$DEFAULT_OUT_DIR}"
readonly PRESET="${PRESET:-fast}"
readonly CRF="${CRF:-20}"
readonly SEGMENT="${SEGMENT:-6}"
readonly THREADS="${THREADS:-}"
readonly SKIP_MKV="${SKIP_MKV:-0}"
readonly KEEP_SOURCE="${KEEP_SOURCE:-0}"
readonly LIMIT="${LIMIT:-}"
readonly FORCE="${FORCE:-0}"
readonly AAC_BITRATE="192k"
# VIDEO_TIMESCALE is the MPEG transport stream clock (90 kHz).
readonly VIDEO_TIMESCALE=90000
readonly PROBE_SIZE="20M"
readonly TMP_SUFFIX=".partial"
readonly CAPTION_PROBE_SECONDS=5
readonly ORIGINAL_DIR="original"
readonly CMAF_DIR="cmaf"
readonly DASH_MANIFEST="manifest.mpd"
readonly HLS_MASTER="master.m3u8"

FF=(ffmpeg -hide_banner -loglevel warning -stats -nostdin -y)
readonly FF

die() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || die "$1 not found in PATH"
}

# wanted PATH: true when PATH must be (re)built.
wanted() {
  [[ "$FORCE" == "1" || ! -e "$1" ]]
}

audio_count() {
  ffprobe -v error -probesize "$PROBE_SIZE" -analyzeduration "$PROBE_SIZE" \
    -select_streams a -show_entries stream=index -of csv=p=0 "$1" | grep -c .
}

has_captions() {
  # Broadcast video carries caption data on every frame (padding included),
  # so a few seconds of frame side data is enough to tell.
  local info
  info="$(ffprobe -v error -select_streams v:0 -read_intervals "%+$CAPTION_PROBE_SECONDS" \
    -show_entries frame_side_data=side_data_type -of csv=p=0 "$1" 2>/dev/null || true)"
  [[ "$info" == *"Closed Captions"* ]]
}

report_captions() { # LABEL FILE
  if has_captions "$2"; then
    echo "    captions: present  ($1)"
  else
    echo "    captions: MISSING  ($1)" >&2
  fi
}

process() {
  local src="$1"
  local name dir limit_args=() thread_args=()
  name="$(basename "${src%.*}")"
  dir="$OUT_DIR/$name"
  mkdir -p "$dir"
  if [[ -n "$LIMIT" ]]; then
    limit_args=(-t "$LIMIT")
  fi
  if [[ -n "$THREADS" ]]; then
    thread_args=(-threads "$THREADS" -filter_threads "$THREADS")
  fi

  local mp4="$dir/$name.mp4"
  local cmaf="$dir/$CMAF_DIR"
  local h264="$dir/$name.h264.mkv"
  local mpeg2="$dir/$name.mpeg2.mkv"
  local srt="$dir/$name.srt"
  local filed="$dir/$ORIGINAL_DIR/$name.ts"

  echo "== $src"
  report_captions "source" "$src"

  # 1. The one video encode, straight to MP4. Interlaced frames are
  #    deinterlaced one frame per frame so caption data is neither dropped
  #    nor duplicated; -a53cc keeps the captions in the H.264 stream.
  #    Keyframes land on segment boundaries.
  if wanted "$mp4"; then
    echo "  -> $mp4"
    "${FF[@]}" -probesize "$PROBE_SIZE" -analyzeduration "$PROBE_SIZE" \
      -fflags +genpts+discardcorrupt -i "$src" ${limit_args[@]+"${limit_args[@]}"} \
      -map 0:v:0 -map 0:a \
      -vf yadif=deint=interlaced \
      -c:v libx264 -preset "$PRESET" -crf "$CRF" -pix_fmt yuv420p -a53cc 1 \
      ${thread_args[@]+"${thread_args[@]}"} \
      -force_key_frames "expr:gte(t,n_forced*$SEGMENT)" \
      -enc_time_base:v demux -video_track_timescale "$VIDEO_TIMESCALE" \
      -c:a aac -b:a "$AAC_BITRATE" \
      -movflags +faststart -f mp4 "$mp4$TMP_SUFFIX"
    mv "$mp4$TMP_SUFFIX" "$mp4"
  fi
  report_captions "mp4" "$mp4"

  # 2. One CMAF package for both HLS and DASH: the DASH muxer writes the
  #    segments once, plus manifest.mpd and HLS playlists that point at the
  #    same files. One adaptation set (and HLS rendition) per track.
  if wanted "$cmaf/$DASH_MANIFEST" || wanted "$cmaf/$HLS_MASTER"; then
    echo "  -> $cmaf/$DASH_MANIFEST + $HLS_MASTER"
    rm -rf "${cmaf:?}$TMP_SUFFIX"
    mkdir -p "$cmaf$TMP_SUFFIX"
    local tracks sets="id=0,streams=0" j
    tracks="$(audio_count "$mp4")"
    for ((j = 1; j <= tracks; j++)); do
      sets+=" id=$j,streams=$j"
    done
    "${FF[@]}" -i "$mp4" -map 0:v:0 -map 0:a -c copy \
      -f dash -dash_segment_type mp4 -seg_duration "$SEGMENT" \
      -use_template 1 -use_timeline 1 \
      -adaptation_sets "$sets" \
      -hls_playlist 1 -hls_master_name "$HLS_MASTER" \
      "$cmaf$TMP_SUFFIX/$DASH_MANIFEST"
    rm -rf "${cmaf:?}"
    mv "$cmaf$TMP_SUFFIX" "$cmaf"
  fi
  report_captions "hls" "$cmaf/$HLS_MASTER"

  if [[ "$SKIP_MKV" != "1" ]]; then
    # 3. Matroska with the MP4's video bitstream and the capture's original
    #    AC-3 audio.
    if wanted "$h264"; then
      echo "  -> $h264"
      "${FF[@]}" -i "$mp4" \
        -probesize "$PROBE_SIZE" -analyzeduration "$PROBE_SIZE" \
        -fflags +genpts+discardcorrupt -i "$src" ${limit_args[@]+"${limit_args[@]}"} \
        -map 0:v:0 -map 1:a -c copy \
        -f matroska "$h264$TMP_SUFFIX"
      mv "$h264$TMP_SUFFIX" "$h264"
    fi

    # 4. Original streams in Matroska (no transcode).
    if wanted "$mpeg2"; then
      echo "  -> $mpeg2"
      "${FF[@]}" -probesize "$PROBE_SIZE" -analyzeduration "$PROBE_SIZE" \
        -fflags +genpts+discardcorrupt -i "$src" ${limit_args[@]+"${limit_args[@]}"} \
        -map 0:v:0 -map 0:a -c copy \
        -f matroska "$mpeg2$TMP_SUFFIX"
      mv "$mpeg2$TMP_SUFFIX" "$mpeg2"
    fi
  fi
  if [[ -e "$h264" ]]; then
    report_captions "h264.mkv" "$h264"
  fi

  # 5. Caption text from the source, for choosing test phrases. A capture
  #    without captions is reported, not fatal. Field 1 is forced: left on
  #    auto the decoder locks onto whichever field shows data first, which
  #    on some stations is the field-2 side channel.
  if wanted "$srt"; then
    echo "  -> $srt"
    if "${FF[@]}" -data_field first -f lavfi -i "movie='$src'[out0+subcc]" \
      ${limit_args[@]+"${limit_args[@]}"} -map 0:s:0 -c:s srt -f srt "$srt$TMP_SUFFIX"; then
      mv "$srt$TMP_SUFFIX" "$srt"
    else
      rm -f "${srt:?}$TMP_SUFFIX"
      echo "    caption text could not be extracted from $src" >&2
    fi
  fi

  # 6. File the capture with its derivatives. Never during a trial run.
  if [[ "$KEEP_SOURCE" != "1" && -z "$LIMIT" && "$src" != "$filed" ]]; then
    [[ ! -e "$filed" ]] || die "$filed already exists; not replacing it with $src"
    mkdir -p "$dir/$ORIGINAL_DIR"
    mv "$src" "$filed"
    echo "  moved $src -> $filed"
  fi
}

usage() {
  echo "usage: $0 [-o OUT_DIR] [CAPTURE.ts ...]" >&2
  exit 2
}

main() {
  local opt
  while getopts ":o:h" opt; do
    case "$opt" in
      o) OUT_DIR="$OPTARG" ;;
      *) usage ;;
    esac
  done
  shift $((OPTIND - 1))
  [[ -n "$OUT_DIR" ]] || die "the output directory must not be empty"
  readonly OUT_DIR

  need ffmpeg
  need ffprobe

  local sources=("$@")
  if ((${#sources[@]} == 0)); then
    shopt -s nullglob
    sources=(ref/*.ts "$OUT_DIR"/*/"$ORIGINAL_DIR"/*.ts)
    shopt -u nullglob
  fi
  ((${#sources[@]} > 0)) ||
    die "no captures given and none found in ref/ or $OUT_DIR/*/$ORIGINAL_DIR/ (run from the repository root)"

  local src
  for src in "${sources[@]}"; do
    [[ -f "$src" ]] || die "not a file: $src"
  done
  for src in "${sources[@]}"; do
    process "$src"
  done
  echo "done: $OUT_DIR"
}

main "$@"
