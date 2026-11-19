#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Synthesizes the media fixtures used by the adapter and CLI integration
# tests (pkg/ffadapter, cmd). Every fixture is generated from FFmpeg's
# built-in lavfi signal generators, so the fixtures are independently
# created and redistributable by construction; nothing here reads a real
# recording and no fixture is committed to the repository.
#
# The ffmpeg command-line tool is test tooling only: the product links the
# FFmpeg libraries and never starts an ffmpeg process.
#
# Usage:
#   scripts/make-fixtures.sh [-o DIR] [NAME ...]
#
# Without names every fixture is generated. A fixture that already exists in
# DIR is kept, so the script is cheap to re-run; each fixture is built under
# a temporary name and renamed into place, so concurrent invocations sharing
# one DIR are safe. DIR defaults to $TVAD_FIXTURE_DIR, then to
# ${TMPDIR:-/tmp}/tv-ad-detect-fixtures.
#
# Provenance and expected annotations of the "broadcast" fixture
# (320x180, 10 fps, H.264 with B-frames, 5 s closed GOPs, AAC 48 kHz mono):
#
#     0.0 - 100.0 s  program A: zooming Mandelbrot set, stationary logo
#   100.0 - 100.6 s  black + digital silence
#   100.6 - 115.0 s  spot 1 (testsrc2)            unit 100.3 - 115.3 (15 s)
#   115.0 - 115.6 s  black + digital silence
#   115.6 - 145.0 s  spot 2 (linear gradients)    unit 115.3 - 145.3 (30 s)
#   145.0 - 145.6 s  black + digital silence
#   145.6 - 160.0 s  spot 3 (rgbtestsrc)          unit 145.3 - 160.3 (15 s)
#   160.0 - 160.6 s  black + digital silence
#   160.6 - 190.0 s  spot 4 (radial gradients)    unit 160.3 - 190.3 (30 s)
#   190.0 - 190.6 s  black + digital silence
#   190.6 - 280.6 s  program B: spiral gradients, stationary logo
#
# The logo is three nested boxes at x 252..296, y 14..38. Audio is a 440 Hz
# tone at -18 dBFS that is exactly zero during the black frames. The true
# break is [100.3 s, 190.3 s) (midpoints of the separating black spans, which
# fall exactly on frame boundaries).

set -euo pipefail

OUT="${TVAD_FIXTURE_DIR:-${TMPDIR:-/tmp}/tv-ad-detect-fixtures}"
if [ "${1:-}" = "-o" ]; then
    OUT="$2"
    shift 2
fi

ALL="matrix noaudio two_audio two_video mpts hevc_gray codecs subs rotated pcm lfe coverart gray luma_split hi10 sar hdr
interlaced ntsc vfr offset audio_late audio_early reschange audio_only
declared broadcast broadcast_texture broadcast_low broadcast_ts hls_master
hls_byterange hls_ts dash pieces"

if ! command -v ffmpeg >/dev/null 2>&1; then
    echo "make-fixtures: the ffmpeg command-line tool is required" >&2
    exit 3
fi

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"

FF=(ffmpeg -hide_banner -loglevel error -nostdin -y)
# Small, fast and deterministic H.264: one thread, bit-exact flags.
X264=(-c:v libx264 -preset ultrafast -threads 1 -flags +bitexact -pix_fmt yuv420p)
AAC=(-c:a aac -b:a 64k -ar 48000 -ac 1)
AC3=(-c:a ac3 -b:a 96k -ar 48000 -ac 2)
BITEXACT=(-fflags +bitexact -map_metadata -1)

SMALL="s=320x180:r=15"
TONE="sine=frequency=440:sample_rate=48000"

# build NAME FUNCTION: run FUNCTION in a scratch directory and move the
# result to $OUT/NAME atomically. An existing fixture is left untouched.
build() {
    local name="$1" fn="$2"
    if [ -e "$OUT/$name" ]; then
        return 0
    fi
    local tmp status
    tmp="$(mktemp -d "$OUT/.tmp.$name.XXXXXX")"
    # Run outside any conditional so that errexit applies to every command
    # of the recipe: a fixture is only published when all of them succeeded.
    set +e
    (
        set -e
        cd "$tmp"
        "$fn"
    )
    status=$?
    set -e
    if [ "$status" -ne 0 ]; then
        rm -rf "$tmp"
        echo "make-fixtures: fixture $name failed" >&2
        return 1
    fi
    # Lost the race against a concurrent run: keep the other copy.
    mv "$tmp" "$OUT/$name" 2>/dev/null || rm -rf "$tmp"
}

# need NAME: make sure a prerequisite fixture exists.
need() { make_one "$1"; }

# ---- MEDIA-02: container x codec matrix (4 s) ------------------------------
fx_matrix() {
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=4" -f lavfi -i "${TONE}:duration=4" \
        "${X264[@]}" -bf 2 -g 30 "${AAC[@]}" "${BITEXACT[@]}" mp4_aac.mp4 \
        "${X264[@]}" -bf 2 -g 30 "${AC3[@]}" "${BITEXACT[@]}" mp4_ac3.mp4 \
        "${X264[@]}" -bf 2 -g 30 "${AAC[@]}" "${BITEXACT[@]}" mkv_aac.mkv \
        "${X264[@]}" -bf 2 -g 30 "${AC3[@]}" "${BITEXACT[@]}" mkv_ac3.mkv \
        "${X264[@]}" -bf 2 -g 30 "${AAC[@]}" "${BITEXACT[@]}" ts_aac.ts \
        "${X264[@]}" -bf 2 -g 30 "${AC3[@]}" "${BITEXACT[@]}" ts_ac3.ts
}

# ---- MEDIA-04: no audio ------------------------------------------------------
fx_noaudio() {
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=4" "${X264[@]}" -g 30 "${BITEXACT[@]}" noaudio.mp4
}

# ---- MEDIA-03: two audio tracks; stream 1 is a tone, stream 2 is digital
# silence and carries the default flag ----------------------------------------
fx_two_audio() {
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=6" -f lavfi -i "${TONE}:duration=6" \
        -f lavfi -i "anullsrc=r=48000:cl=stereo:d=6" \
        -map 0:v -map 1:a -map 2:a "${X264[@]}" -g 30 \
        -c:a:0 aac -b:a:0 64k -ac:a:0 1 -c:a:1 ac3 -b:a:1 96k \
        -disposition:a:0 0 -disposition:a:1 default "${BITEXACT[@]}" two_audio.mkv
}

# ---- MEDIA-03: two real video tracks (stream 0: 320x180, stream 1: 160x90) --
fx_two_video() {
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=3" -f lavfi -i "testsrc2=s=160x90:r=15:d=3" \
        -f lavfi -i "${TONE}:duration=3" -map 0:v -map 1:v -map 2:a \
        "${X264[@]}" -g 30 "${AAC[@]}" "${BITEXACT[@]}" two_video.mkv
}

# ---- Program selection: a two-program transport stream. Program 101 is a
# 320x180 picture with a tone; program 202 is a 160x90 picture with digital
# silence. ------------------------------------------------------------------
fx_mpts() {
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=4" -f lavfi -i "${TONE}:duration=4" \
        -f lavfi -i "testsrc2=s=160x90:r=15:d=4" -f lavfi -i "anullsrc=r=48000:cl=mono:d=4" \
        -map 0:v -map 1:a -map 2:v -map 3:a "${X264[@]}" -g 30 "${AAC[@]}" \
        -program program_num=101:st=0:st=1 -program program_num=202:st=2:st=3 \
        "${BITEXACT[@]}" -f mpegts mpts.ts
}

# ---- CAP-01: a text subtitle track (SubRip, stream 2) has no presentation-
# style semantics ----------------------------------------------------------------
fx_subs() {
    printf '1\n00:00:00,500 --> 00:00:02,000\nfirst line\n\n2\n00:00:02,200 --> 00:00:03,500\nsecond line\n' >subs.srt
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=4" -f lavfi -i "${TONE}:duration=4" -i subs.srt \
        -map 0:v -map 1:a -map 2:s "${X264[@]}" -g 30 "${AAC[@]}" -c:s srt "${BITEXACT[@]}" subs.mkv
    rm -f subs.srt
}

# ---- MEDIA-05: display rotation metadata (90 degrees) ------------------------
fx_rotated() {
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=2" "${X264[@]}" -g 30 "${BITEXACT[@]}" plain.mp4
    "${FF[@]}" -display_rotation:v:0 90 -i plain.mp4 -c copy "${BITEXACT[@]}" rotated.mp4
    rm -f plain.mp4
}

# ---- AUDIO-01: exact levels in every measurable sample format. The left
# channel is a 1 kHz tone of amplitude 0.25 (mean square 0.03125 over any
# whole number of cycles, so over every 20 ms window), the right channel is
# digital silence ---------------------------------------------------------------
fx_pcm() {
    local codec
    for codec in pcm_u8 pcm_s16le pcm_s32le pcm_f32le pcm_f64le; do
        "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=2" \
            -f lavfi -i "aevalsrc='0.25*sin(2*PI*1000*t)|0':s=48000:c=stereo:d=2" \
            "${X264[@]}" -g 30 -c:a "$codec" "${BITEXACT[@]}" "${codec}.mkv"
    done
}

# ---- AUDIO-01: 5.1 AC-3 whose only signal is a 60 Hz tone in the LFE
# channel (audio.channel_policy=exclude_lfe) ----------------------------------
fx_lfe() {
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=3" \
        -f lavfi -i "aevalsrc='0|0|0|0.5*sin(2*PI*60*t)|0|0':s=48000:c=5.1:d=3" \
        "${X264[@]}" -g 30 -c:a ac3 -b:a 192k "${BITEXACT[@]}" lfe.mkv
}

# ---- MEDIA-03: attached picture (cover art) video stream --------------------
fx_coverart() {
    "${FF[@]}" -f lavfi -i "color=c=red:s=160x160:d=1" -frames:v 1 cover.png
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=4" -f lavfi -i "${TONE}:duration=4" -i cover.png \
        -map 2:v -map 0:v -map 1:a -c:v:0 png -disposition:v:0 attached_pic \
        -c:v:1 libx264 -preset ultrafast -threads 1 -pix_fmt:v:1 yuv420p -g 30 "${AAC[@]}" \
        "${BITEXACT[@]}" coverart.mp4
    rm -f cover.png
}

# ---- MEDIA-05: known luminance, limited vs full range, 8 vs 10 bit ----------
# gray_<v>_<range>[_10].mp4 holds a flat picture whose canonical luminance
# is <v> on the 0-255 scale.
fx_gray() {
    local v hex
    for v in 0 64 128 255; do
        hex=$(printf '0x%02x%02x%02x' "$v" "$v" "$v")
        "${FF[@]}" -f lavfi -i "color=c=${hex}:${SMALL}:d=1" \
            -vf "scale=out_range=tv:flags=accurate_rnd+full_chroma_int,format=yuv420p" -color_range tv \
            "${X264[@]}" -qp 0 "${BITEXACT[@]}" "gray_${v}_limited.mp4"
        "${FF[@]}" -f lavfi -i "color=c=${hex}:${SMALL}:d=1" \
            -vf "scale=out_range=pc:flags=accurate_rnd+full_chroma_int,format=yuvj420p" -color_range pc \
            -c:v libx264 -preset ultrafast -threads 1 -pix_fmt yuvj420p -qp 0 "${BITEXACT[@]}" "gray_${v}_full.mp4"
    done
    # 10 bit is optional: older libx264 builds are 8-bit only.
    "${FF[@]}" -f lavfi -i "color=c=0x808080:${SMALL}:d=1" \
        -vf "scale=out_range=tv,format=yuv420p10le" -color_range tv \
        -c:v libx264 -preset ultrafast -threads 1 -pix_fmt yuv420p10le -qp 0 "${BITEXACT[@]}" \
        gray_128_limited_10.mp4 2>/dev/null || rm -f gray_128_limited_10.mp4
}

# ---- MEDIA-05: box-filter geometry. 770x434 is not a multiple of the
# reduction factor 3; columns 0..384 are white, the rest black, and the top
# 100 rows of the white part are mid-gray -------------------------------------
fx_luma_split() {
    "${FF[@]}" -f lavfi -i "color=c=black:s=770x434:r=15:d=1" \
        -vf "drawbox=x=0:y=0:w=385:h=434:color=white:t=fill,drawbox=x=0:y=0:w=385:h=100:color=0x808080:t=fill,format=yuv420p" \
        "${X264[@]}" -qp 0 "${BITEXACT[@]}" luma_split.mp4
}

# ---- MEDIA-02/05: 10-bit H.264 (optional) -----------------------------------
fx_hi10() {
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=2" -f lavfi -i "${TONE}:duration=2" \
        -c:v libx264 -preset ultrafast -threads 1 -pix_fmt yuv420p10le -g 30 "${AAC[@]}" \
        "${BITEXACT[@]}" hi10.mp4 2>/dev/null || { rm -f hi10.mp4; : >unsupported; }
}

# ---- Codec matrix beyond H.264 (6 s, 15 fps): HEVC and MPEG-2 in transport
# stream and MP4, and transport streams whose timestamps are large or wrap
# around the 33-bit clock (2^33 / 90 kHz = 95443.7 s) mid-file. ----------------
fx_codecs() {
    local src=(-f lavfi -i "testsrc2=${SMALL}:d=6" -f lavfi -i "${TONE}:duration=6")
    "${FF[@]}" "${src[@]}" -c:v libx265 -x265-params log-level=none -g 30 -tag:v hvc1 "${AAC[@]}" "${BITEXACT[@]}" hevc.mp4 \
        -c:v libx265 -x265-params log-level=none -g 30 "${AC3[@]}" "${BITEXACT[@]}" -f mpegts hevc.ts \
        -c:v mpeg2video -q:v 4 -g 15 -bf 2 "${AAC[@]}" "${BITEXACT[@]}" mpeg2.mp4 \
        -c:v mpeg2video -q:v 4 -g 15 -bf 2 "${AC3[@]}" "${BITEXACT[@]}" -f mpegts mpeg2.ts
    "${FF[@]}" "${src[@]}" -c:v mpeg2video -q:v 4 -g 15 -bf 2 "${AC3[@]}" -output_ts_offset 50000 \
        "${BITEXACT[@]}" -f mpegts long_ts.ts
    "${FF[@]}" "${src[@]}" -c:v mpeg2video -q:v 4 -g 15 -bf 2 "${AC3[@]}" -output_ts_offset 95440 \
        "${BITEXACT[@]}" -f mpegts wrap_ts.ts
}

# ---- Hardware decoding: flat gray HEVC pictures (limited range), 8 bit
# (decoded to NV12 by VideoToolbox) and 10 bit (P010), lossless. ---------------
fx_hevc_gray() {
    local v hex
    for v in 0 64 128 255; do
        hex=$(printf '0x%02x%02x%02x' "$v" "$v" "$v")
        for d in 8 10; do
            fmt=yuv420p
            [ "$d" = 10 ] && fmt=yuv420p10le
            "${FF[@]}" -f lavfi -i "color=c=${hex}:${SMALL}:d=1" \
                -vf "scale=out_range=tv:flags=accurate_rnd+full_chroma_int,format=${fmt}" -color_range tv \
                -c:v libx265 -x265-params "lossless=1:log-level=none" -pix_fmt "$fmt" -tag:v hvc1 \
                "${BITEXACT[@]}" "hevc_${v}_${d}.mp4" 2>/dev/null || { rm -f "hevc_${v}_${d}.mp4"; : >unsupported; }
        done
    done
}

# ---- MEDIA-03: sample aspect ratio 4:3 ---------------------------------------
fx_sar() {
    "${FF[@]}" -f lavfi -i "testsrc2=s=240x180:r=15:d=3" -f lavfi -i "${TONE}:duration=3" \
        -vf "setsar=4/3" "${X264[@]}" -g 30 "${AAC[@]}" "${BITEXACT[@]}" sar.mp4
}

# ---- MEDIA-05: PQ (HDR) transfer tag -----------------------------------------
fx_hdr() {
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=2" \
        -vf "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc" \
        "${X264[@]}" -g 30 "${BITEXACT[@]}" hdr.mp4
}

# ---- MEDIA-05: interlaced coding ---------------------------------------------
fx_interlaced() {
    "${FF[@]}" -f lavfi -i "testsrc2=s=320x192:r=15:d=3" -f lavfi -i "${TONE}:duration=3" \
        "${X264[@]}" -g 30 -flags +ilme+ildct+bitexact -x264opts tff=1 "${AAC[@]}" "${BITEXACT[@]}" interlaced.ts
}

# ---- TIME-01: 30000/1001 fps with reordered pictures -------------------------
fx_ntsc() {
    "${FF[@]}" -f lavfi -i "testsrc2=s=320x180:r=30000/1001:d=3.003" -f lavfi -i "${TONE}:duration=3.003" \
        "${X264[@]}" -bf 3 -b_strategy 0 -g 48 "${AAC[@]}" "${BITEXACT[@]}" ntsc.mp4
}

# ---- TIME-01: variable frame rate (33/33/33/33/100/100 ms cadence) -----------
fx_vfr() {
    "${FF[@]}" -f lavfi -i "testsrc2=s=320x180:r=30:d=4" -f lavfi -i "${TONE}:duration=4" \
        -vf "select='lt(mod(n,10),5)+eq(mod(n,10),7)'" -fps_mode vfr \
        "${X264[@]}" -bf 2 -g 30 "${AAC[@]}" "${BITEXACT[@]}" vfr.mkv
}

# ---- TIME-01/02: nonzero stream start ----------------------------------------
fx_offset() {
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=4" -f lavfi -i "${TONE}:duration=4" \
        "${X264[@]}" -bf 2 -g 30 "${AAC[@]}" -output_ts_offset 37.5 "${BITEXACT[@]}" offset.mkv \
        "${X264[@]}" -bf 2 -g 30 "${AAC[@]}" -output_ts_offset 37.5 -movflags +faststart "${BITEXACT[@]}" offset.mp4
}

# ---- TIME-01: audio starting 0.5 s after / before the video -----------------
fx_audio_late() {
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=4" -itsoffset 0.5 -f lavfi -i "${TONE}:duration=3.5" \
        "${X264[@]}" -g 30 "${AAC[@]}" "${BITEXACT[@]}" audio_late.mkv
}
fx_audio_early() {
    "${FF[@]}" -itsoffset 0.5 -f lavfi -i "testsrc2=${SMALL}:d=3.5" -f lavfi -i "${TONE}:duration=4" \
        "${X264[@]}" -g 30 "${AAC[@]}" "${BITEXACT[@]}" audio_early.mkv
}

# ---- SIZE-01, TIME-03: transport streams whose coded size changes mid-way.
# a.ts is 320x180, b_*.ts are 256x144; the concatenations are
#   reschange.ts        timestamps continue across the change
#   reschange_reset.ts  timestamps restart at the change (timestamp reset)
#   reschange_gap.ts    3 s of timeline are missing at the change (gap) -------
fx_reschange() {
    "${FF[@]}" -f lavfi -i "testsrc2=s=320x180:r=15:d=4" -f lavfi -i "${TONE}:duration=4" \
        "${X264[@]}" -g 30 "${AAC[@]}" "${BITEXACT[@]}" a.ts
    local off
    for off in 0 4 7; do
        "${FF[@]}" -f lavfi -i "testsrc2=s=256x144:r=15:d=4" -f lavfi -i "${TONE}:duration=4" \
            "${X264[@]}" -g 30 "${AAC[@]}" -output_ts_offset "$off" "${BITEXACT[@]}" "b_${off}.ts"
    done
    cat a.ts b_4.ts >reschange.ts
    cat a.ts b_0.ts >reschange_reset.ts
    cat a.ts b_7.ts >reschange_gap.ts
    rm -f b_0.ts b_4.ts b_7.ts
}

# ---- QUAL-04: containers that declare their duration up front (MP4 with the
# moov box first, Matroska), 12 s long, for truncation tests; and a file
# whose audio (8 s) outlasts its video (3 s) ----------------------------------
fx_declared() {
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=12" -f lavfi -i "${TONE}:duration=12" \
        "${X264[@]}" -bf 2 -g 30 "${AAC[@]}" -movflags +faststart "${BITEXACT[@]}" declared.mp4 \
        "${X264[@]}" -bf 2 -g 30 "${AAC[@]}" "${BITEXACT[@]}" declared.mkv
    "${FF[@]}" -f lavfi -i "testsrc2=${SMALL}:d=3" -f lavfi -i "${TONE}:duration=8" \
        "${X264[@]}" -g 30 "${AAC[@]}" "${BITEXACT[@]}" audio_longer.mkv \
        "${X264[@]}" -g 30 "${AAC[@]}" -movflags +faststart "${BITEXACT[@]}" audio_longer.mp4
}

# ---- MEDIA-04: a media file without video ------------------------------------
fx_audio_only() {
    "${FF[@]}" -f lavfi -i "${TONE}:duration=2" "${AAC[@]}" "${BITEXACT[@]}" audio_only.mp4
    printf 'this is not a media file\n%.0s' $(seq 1 200) >not_media.mp4
}

# ---- the synthetic broadcast (see the header for the annotation) ------------
BSZ="s=320x180:r=10"
LOGO="drawbox=x=252:y=14:w=44:h=24:color=white@0.9:t=fill,drawbox=x=258:y=19:w=32:h=14:color=black@0.9:t=fill,drawbox=x=266:y=23:w=16:h=6:color=white@0.9:t=fill"
BGOP=(-bf 2 -g 50 -keyint_min 50 -sc_threshold 0)
# The gradients source picks random colors and end points (its seed option
# does not make it repeatable), so both are given: the fixtures are then
# reproducible bit for bit.
GRAD_A="x0=40:y0=30:x1=280:y1=150:c0=red:c1=blue:c2=yellow:c3=green:c4=white"
GRAD_B="x0=160:y0=90:x1=300:y1=170:c0=orange:c1=purple:c2=cyan"
GRAD_C="x0=160:y0=90:x1=310:y1=175:c0=navy:c1=white:c2=red:c3=yellow:c4=teal:c5=magenta"

# broadcast_encode SPOT3 FILE: the broadcast with the given source as its
# third spot (145.6 s .. 160.0 s).
broadcast_encode() {
    local spot3="$1" file="$2"
    local black="color=c=black:${BSZ}:d=0.6"
    local graph="
mandelbrot=${BSZ}:start_scale=2.5:end_scale=0.05:end_pts=1200,trim=duration=100,setpts=PTS-STARTPTS,${LOGO}[p0];
${black}[b0];
testsrc2=${BSZ}:d=14.4[a1];
${black}[b1];
gradients=${BSZ}:speed=0.2:n=5:${GRAD_A},trim=duration=29.4,setpts=PTS-STARTPTS[a2];
${black}[b2];
${spot3}[a3];
${black}[b3];
gradients=${BSZ}:speed=0.3:n=3:type=radial:${GRAD_B},trim=duration=29.4,setpts=PTS-STARTPTS[a4];
${black}[b4];
gradients=${BSZ}:speed=0.1:n=6:type=spiral:${GRAD_C},trim=duration=90,setpts=PTS-STARTPTS,${LOGO}[p1];
[p0][b0][a1][b1][a2][b2][a3][b3][a4][b4][p1]concat=n=11:v=1:a=0,format=yuv420p[v];
aevalsrc='0.125*sin(2*PI*440*t)*(1-between(t,100,100.6)-between(t,115,115.6)-between(t,145,145.6)-between(t,160,160.6)-between(t,190,190.6))':s=48000:d=280.6[a]"
    "${FF[@]}" -filter_complex "$(printf '%s' "$graph" | tr -d '\n')" -map "[v]" -map "[a]" \
        "${X264[@]}" -crf 30 "${BGOP[@]}" "${AAC[@]}" -movflags +faststart "${BITEXACT[@]}" "$file"
}

fx_broadcast() {
    broadcast_encode "rgbtestsrc=${BSZ}:d=14.4" broadcast.mp4
}

# The same broadcast whose third spot is a densely textured picture (an
# elementary cellular automaton, rule 110): edges everywhere, including
# where the logo is in the program parts. It carries no logo. The seed is
# fixed so the fixture is reproducible; seed 3 is one of the harder ones
# (for about 2 s the texture inside the logo box is slightly sparser).
fx_broadcast_texture() {
    broadcast_encode "cellauto=${BSZ}:rule=110:random_seed=3,trim=duration=14.4,setpts=PTS-STARTPTS" texture.mp4
}

# A second, lower-resolution rendition of the same pictures (160x90).
fx_broadcast_low() {
    need broadcast
    "${FF[@]}" -i "$OUT/broadcast/broadcast.mp4" -an -vf "scale=160:90" \
        "${X264[@]}" -crf 32 "${BGOP[@]}" "${BITEXACT[@]}" low.mp4
}

# The same samples in a transport stream (stream copy).
fx_broadcast_ts() {
    need broadcast
    "${FF[@]}" -i "$OUT/broadcast/broadcast.mp4" -c copy "${BITEXACT[@]}" broadcast.ts
}

# ---- PACK-02: HLS, fragmented MP4, master playlist with two variants and a
# separately segmented audio rendition (video 5 s, audio 3 s segments). The
# renditions are packaged by separate runs, so -copyts with one common
# offset keeps them on one clock: media time 10 s is broadcast time 0 -------
fx_hls_master() {
    need broadcast
    need broadcast_low
    mkdir -p video/main video/low audio/en
    local clock=(-c copy -output_ts_offset 10 -avoid_negative_ts disabled)
    local hls=(-f hls -hls_playlist_type vod -hls_segment_type fmp4 -hls_fmp4_init_filename init.mp4)
    "${FF[@]}" -copyts -i "$OUT/broadcast/broadcast.mp4" -map 0:v "${clock[@]}" "${hls[@]}" -hls_time 5 \
        -hls_flags independent_segments -hls_segment_filename "video/main/seg_%03d.m4s" video/main/media.m3u8
    "${FF[@]}" -copyts -i "$OUT/broadcast_low/low.mp4" -map 0:v "${clock[@]}" "${hls[@]}" -hls_time 5 \
        -hls_flags independent_segments -hls_segment_filename "video/low/seg_%03d.m4s" video/low/media.m3u8
    "${FF[@]}" -copyts -i "$OUT/broadcast/broadcast.mp4" -map 0:a "${clock[@]}" "${hls[@]}" -hls_time 3 \
        -hls_segment_filename "audio/en/seg_%03d.m4s" audio/en/audio.m3u8
    cat >master.m3u8 <<'EOF'
#EXTM3U
#EXT-X-VERSION:7
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,AUTOSELECT=YES,URI="audio/en/audio.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=120000,RESOLUTION=160x90,CODECS="avc1.4d400a,mp4a.40.2",AUDIO="aud"
video/low/media.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=320000,RESOLUTION=320x180,CODECS="avc1.4d400c,mp4a.40.2",AUDIO="aud"
video/main/media.m3u8
EOF
}

# ---- PACK-02: HLS with byte-range addressing into one media file; audio is
# multiplexed with the video ----------------------------------------------------
fx_hls_byterange() {
    need broadcast
    "${FF[@]}" -i "$OUT/broadcast/broadcast.mp4" -c copy -f hls -hls_time 5 -hls_playlist_type vod \
        -hls_segment_type fmp4 -hls_flags single_file+independent_segments media.m3u8
}

# ---- HLS with MPEG-TS segments (no initialization resource). Not a required
# capability (PACK-04 requires CMAF); it guards the ISO BMFF truncation check
# against rejecting resources that are not ISO BMFF -------------------------
fx_hls_ts() {
    need broadcast
    "${FF[@]}" -i "$OUT/broadcast/broadcast.mp4" -c copy -f hls -hls_time 5 -hls_playlist_type vod \
        -hls_segment_type mpegts -hls_segment_filename "seg_%03d.ts" media.m3u8
}

# ---- PACK-03: DASH, SegmentTemplate with SegmentTimeline, two video
# Representations and one audio AdaptationSet ----------------------------------
fx_dash() {
    need broadcast
    need broadcast_low
    "${FF[@]}" -i "$OUT/broadcast/broadcast.mp4" -i "$OUT/broadcast_low/low.mp4" \
        -map 0:v -map 1:v -map 0:a -c copy -f dash -seg_duration 5 -use_template 1 -use_timeline 1 \
        -adaptation_sets "id=0,streams=v id=1,streams=a" \
        -init_seg_name 'init-$RepresentationID$.m4s' -media_seg_name 'chunk-$RepresentationID$-$Number%05d$.m4s' \
        manifest.mpd
}

# ---- PACK-06: two separately encoded pieces of the broadcast, split inside
# spot 2 at 130 s; each piece is encoded on its own and then packaged twice
# by stream copy:
#   p0/, p1/    CMAF with separate video (stream 0) and audio (stream 1)
#               tracks in 5 s segments (ffmpeg's dash muxer)
#   p0m/, p1m/  one fragmented MP4 with multiplexed audio and byte-range
#               addressed 5 s segments (ffmpeg's hls muxer)
# Piece 1 carries media timestamps starting near 1000 s. The tests write a
# multi-Period MPD and EXT-X-DISCONTINUITY playlists over these resources ----
fx_pieces() {
    need broadcast
    mkdir -p p0 p1 p0m p1m
    local seg=(-c copy -f dash -seg_duration 5 -use_template 1 -use_timeline 0
        -init_seg_name 'init-$RepresentationID$.m4s' -media_seg_name 'seg-$RepresentationID$-$Number%03d$.m4s')
    local hls=(-c copy -f hls -hls_time 5 -hls_playlist_type vod -hls_segment_type fmp4
        -hls_flags single_file+independent_segments)
    "${FF[@]}" -i "$OUT/broadcast/broadcast.mp4" -t 130 \
        "${X264[@]}" -crf 30 "${BGOP[@]}" "${AAC[@]}" "${BITEXACT[@]}" piece0.mp4
    "${FF[@]}" -ss 130 -i "$OUT/broadcast/broadcast.mp4" \
        "${X264[@]}" -crf 30 "${BGOP[@]}" "${AAC[@]}" "${BITEXACT[@]}" piece1.mp4
    "${FF[@]}" -i piece0.mp4 "${seg[@]}" p0/piece.mpd
    "${FF[@]}" -i piece1.mp4 -output_ts_offset 1000 "${seg[@]}" p1/piece.mpd
    "${FF[@]}" -i piece0.mp4 "${hls[@]}" p0m/media.m3u8
    "${FF[@]}" -i piece1.mp4 -output_ts_offset 1000 "${hls[@]}" p1m/media.m3u8
    rm -f piece0.mp4 piece1.mp4
}

make_one() {
    case "$1" in
    matrix | noaudio | two_audio | two_video | mpts | hevc_gray | codecs | subs | rotated | pcm | lfe | coverart | gray | luma_split | hi10 | sar | hdr | interlaced | ntsc | vfr | \
        offset | audio_late | audio_early | reschange | audio_only | declared | broadcast | broadcast_texture | \
        broadcast_low | broadcast_ts | \
        hls_master | hls_byterange | hls_ts | dash | pieces)
        build "$1" "fx_$1"
        ;;
    *)
        echo "make-fixtures: unknown fixture '$1' (known: $(echo $ALL))" >&2
        return 2
        ;;
    esac
}

if [ "$#" -eq 0 ]; then
    # shellcheck disable=SC2086
    set -- $ALL
fi
for name in "$@"; do
    make_one "$name"
done
echo "$OUT"
