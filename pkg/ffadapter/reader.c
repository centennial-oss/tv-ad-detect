// SPDX-License-Identifier: Apache-2.0
//
// Demux/decode shim over libavformat/libavcodec. One call to tvad_next
// yields one event so the Go side crosses the cgo boundary about once per
// frame. Pixel and sample loops live here so the compiler can vectorize
// them; nothing but the reduced luma image and level sums leaves this file.

#include "reader.h"

#include <errno.h>
#include <stdatomic.h>
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/hwcontext.h>
#include <libavutil/channel_layout.h>
#include <libavutil/display.h>
#include <libavutil/pixdesc.h>

// Implemented in Go (//export). Returns bytes read, 0 at end, <0 on error.
extern int tvadGoRead(uintptr_t handle, uint8_t *buf, int size);

#define TVAD_MAX_CH 64

// Time base of every timestamp handed to Go: microseconds.
#define TVAD_US_PER_SEC 1000000
static const AVRational tvad_us = {1, TVAD_US_PER_SEC};

// Canonical luminance: 8-bit, full range. Limited ("video") range codes
// black at TVAD_LIMITED_BLACK and spans TVAD_LIMITED_SPAN code values.
#define TVAD_LUMA_LEVELS 256
#define TVAD_LUMA_MAX (TVAD_LUMA_LEVELS - 1)
#define TVAD_LIMITED_BLACK 16
#define TVAD_LIMITED_SPAN 219
#define TVAD_MIN_DEPTH 8
#define TVAD_MAX_DEPTH 16
// Box averages divide by multiplying with a 32-bit fixed-point reciprocal.
#define TVAD_RECIP_SHIFT 32
#define TVAD_RECIP_ONE ((uint64_t)1 << TVAD_RECIP_SHIFT)
#define TVAD_RECIP_HALF ((uint64_t)1 << (TVAD_RECIP_SHIFT - 1))
// Largest reduction factor whose column sums of 8-bit samples fit 16 bits.
#define TVAD_MAX_K16 (UINT16_MAX / TVAD_LUMA_MAX)

#define TVAD_CC_TRIPLET 3            // bytes per cc_data construct
#define TVAD_DEFAULT_IO_BUFFER 262144 // custom I/O buffer when unset
#define TVAD_AW_INITIAL_CAP 256       // first audio window buffer size
#define TVAD_MAX_DRAIN_ERRORS 64      // decode errors tolerated while flushing
#define TVAD_AVERR_MSG_LEN 96
#define TVAD_DISPLAY_MATRIX_LEN 9

static inline uint8_t tvad_clamp_luma(uint32_t v) { return (uint8_t)(v > TVAD_LUMA_MAX ? TVAD_LUMA_MAX : v); }
#define TVAD_AW_FLUSH 128 // windows buffered before they are handed to Go

struct tvad_reader {
    AVFormatContext *fmt;
    AVIOContext *avio;
    AVCodecContext *vdec, *adec;
    AVPacket *pkt;
    AVFrame *frame;
    AVFrame *swframe;     // hardware frames are transferred here
    AVBufferRef *hwdev;   // hardware device, NULL when decoding in software
    int vidx, aidx, cidx;
    int cc_embedded; // embedded captions are the selected caption source
    tvad_options opt;
    atomic_int cancel;
    uintptr_t io_handle;

    int video_pending; // decoder may hold output
    int draining;      // 0 reading, 1 flush sent, 2 done
    int io_error;

    // downsample scratch
    uint16_t *colsum16;
    uint32_t *colsum32;
    int colsum_cap;
    uint8_t lut_limited[TVAD_LUMA_LEVELS];

    // audio windowing
    int a_rate, a_ch, a_lfe;
    int a_need, a_have;
    int64_t a_start_us, a_next_us;
    double a_sum[TVAD_MAX_CH];
    int a_unsupported; // sample format without a level implementation
    tvad_audio_window *aw;
    int aw_n, aw_cap;
    int drain_errors;
};

static void set_err(char *err, int errlen, const char *what, int averr) {
    if (!err || errlen <= 0) return;
    if (averr) {
        char msg[TVAD_AVERR_MSG_LEN];
        av_strerror(averr, msg, sizeof msg);
        snprintf(err, errlen, "%s: %s", what, msg);
    } else {
        snprintf(err, errlen, "%s", what);
    }
}

static int classify(int averr) {
    if (averr == AVERROR_EXIT) return TVAD_ERR_CANCELED;
    if (averr == AVERROR(ENOMEM)) return TVAD_ERR_NOMEM;
    if (averr == AVERROR(ENOENT) || averr == AVERROR(EACCES) || averr == AVERROR(EIO) ||
        averr == AVERROR(EISDIR) || averr == AVERROR(EPERM))
        return TVAD_ERR_IO;
    if (averr == AVERROR_INVALIDDATA || averr == AVERROR_DECODER_NOT_FOUND ||
        averr == AVERROR_DEMUXER_NOT_FOUND || averr == AVERROR_PATCHWELCOME || averr == AVERROR(ENOSYS))
        return TVAD_ERR_UNSUPPORTED;
    return TVAD_ERR_MEDIA;
}

static int interrupt_cb(void *opaque) {
    return atomic_load_explicit(&((tvad_reader *)opaque)->cancel, memory_order_relaxed);
}

static int read_cb(void *opaque, uint8_t *buf, int size) {
    tvad_reader *r = opaque;
    if (atomic_load_explicit(&r->cancel, memory_order_relaxed)) return AVERROR_EXIT;
    int n = tvadGoRead(r->io_handle, buf, size);
    if (n == 0) return AVERROR_EOF;
    if (n < 0) {
        r->io_error = 1;
        return n == -2 ? AVERROR_EXIT : AVERROR(EIO);
    }
    return n;
}

void tvad_cancel(tvad_reader *r) {
    if (r) atomic_store_explicit(&r->cancel, 1, memory_order_relaxed);
}

int tvad_audio_unsupported(const tvad_reader *r) { return r ? r->a_unsupported : 0; }

void tvad_versions(char *buf, int len) {
    unsigned f = avformat_version(), c = avcodec_version(), u = avutil_version();
    snprintf(buf, len, "libavformat %u.%u.%u libavcodec %u.%u.%u libavutil %u.%u.%u",
             AV_VERSION_MAJOR(f), AV_VERSION_MINOR(f), AV_VERSION_MICRO(f),
             AV_VERSION_MAJOR(c), AV_VERSION_MINOR(c), AV_VERSION_MICRO(c),
             AV_VERSION_MAJOR(u), AV_VERSION_MINOR(u), AV_VERSION_MICRO(u));
}

void tvad_close(tvad_reader *r) {
    if (!r) return;
    avcodec_free_context(&r->vdec);
    avcodec_free_context(&r->adec);
    av_packet_free(&r->pkt);
    av_frame_free(&r->frame);
    av_frame_free(&r->swframe);
    av_buffer_unref(&r->hwdev);
    if (r->fmt) avformat_close_input(&r->fmt);
    if (r->avio) {
        av_freep(&r->avio->buffer);
        avio_context_free(&r->avio);
    }
    free(r->colsum16);
    free(r->colsum32);
    free(r->aw);
    free(r);
}

static void copy_name(char *dst, size_t n, const char *src) {
    snprintf(dst, n, "%s", src ? src : "");
}

// pick_hw_format chooses the hardware surface format when the decoder
// offers it, else the first software format.
static enum AVPixelFormat pick_hw_format(AVCodecContext *c, const enum AVPixelFormat *fmts) {
    (void)c;
    for (const enum AVPixelFormat *p = fmts; *p != AV_PIX_FMT_NONE; p++)
        if (*p == AV_PIX_FMT_VIDEOTOOLBOX) return *p;
    for (const enum AVPixelFormat *p = fmts; *p != AV_PIX_FMT_NONE; p++) {
        const AVPixFmtDescriptor *d = av_pix_fmt_desc_get(*p);
        if (d && !(d->flags & AV_PIX_FMT_FLAG_HWACCEL)) return *p;
    }
    return AV_PIX_FMT_NONE;
}

static int open_decoder(AVCodecContext **out, AVStream *st, const tvad_options *opt, int video, AVBufferRef *hwdev) {
    const AVCodec *codec = avcodec_find_decoder(st->codecpar->codec_id);
    if (!codec) return AVERROR_DECODER_NOT_FOUND;
    AVCodecContext *c = avcodec_alloc_context3(codec);
    if (!c) return AVERROR(ENOMEM);
    int ret = avcodec_parameters_to_context(c, st->codecpar);
    if (ret < 0) {
        avcodec_free_context(&c);
        return ret;
    }
    c->pkt_timebase = st->time_base;
    if (video) {
        c->thread_count = opt->threads > 0 ? opt->threads : 1;
        c->thread_type = FF_THREAD_FRAME | FF_THREAD_SLICE;
        c->flags |= AV_CODEC_FLAG_COPY_OPAQUE;
        if (opt->skip_loop_filter == 1) c->skip_loop_filter = AVDISCARD_NONREF;
        if (opt->skip_loop_filter == 2) c->skip_loop_filter = AVDISCARD_ALL;
        if (opt->reference_only) c->skip_frame = AVDISCARD_NONREF;
        if (hwdev) {
            c->hw_device_ctx = av_buffer_ref(hwdev);
            c->get_format = pick_hw_format;
            c->thread_count = 1; // the hardware decoder runs its own pipeline
        }
    } else {
        c->thread_count = 1;
    }
    ret = avcodec_open2(c, codec, NULL);
    if (ret < 0) {
        avcodec_free_context(&c);
        return ret;
    }
    *out = c;
    return 0;
}

tvad_reader *tvad_open(const char *path, uintptr_t io_handle, const tvad_options *opt,
                       tvad_info *info, int *code, char *err, int errlen) {
    int ret;
    // The FFmpeg libraries log to the process's stderr by default. Library
    // operations must not print (API-05): failures are reported through the
    // returned errors and decode-error events instead. Set once so a host
    // application that configures FFmpeg logging afterwards keeps its choice.
    static atomic_flag log_quiet = ATOMIC_FLAG_INIT;
    if (!atomic_flag_test_and_set(&log_quiet)) av_log_set_level(AV_LOG_QUIET);
    tvad_reader *r = calloc(1, sizeof *r);
    if (!r) {
        *code = TVAD_ERR_NOMEM;
        set_err(err, errlen, "out of memory", 0);
        return NULL;
    }
    r->opt = *opt;
    r->io_handle = io_handle;
    r->vidx = r->aidx = r->cidx = -1;
    memset(info, 0, sizeof *info);
    info->video_index = info->audio_index = info->caption_index = -1;
    for (int v = 0; v < TVAD_LUMA_LEVELS; v++) {
        int x = ((v - TVAD_LIMITED_BLACK) * TVAD_LUMA_MAX + TVAD_LIMITED_SPAN / 2) / TVAD_LIMITED_SPAN;
        r->lut_limited[v] = x < 0 ? 0 : tvad_clamp_luma((uint32_t)x);
    }

    r->fmt = avformat_alloc_context();
    r->pkt = av_packet_alloc();
    r->frame = av_frame_alloc();
    if (!r->fmt || !r->pkt || !r->frame) {
        ret = AVERROR(ENOMEM);
        goto fail_av;
    }
    r->fmt->interrupt_callback.callback = interrupt_cb;
    r->fmt->interrupt_callback.opaque = r;
    if (!path) {
        int bs = opt->io_buffer_size > 0 ? opt->io_buffer_size : TVAD_DEFAULT_IO_BUFFER;
        uint8_t *buf = av_malloc(bs);
        if (!buf) {
            ret = AVERROR(ENOMEM);
            goto fail_av;
        }
        r->avio = avio_alloc_context(buf, bs, 0, r, read_cb, NULL, NULL);
        if (!r->avio) {
            av_free(buf);
            ret = AVERROR(ENOMEM);
            goto fail_av;
        }
        r->fmt->pb = r->avio;
        r->fmt->flags |= AVFMT_FLAG_CUSTOM_IO;
    }
    // avformat_open_input frees the context on failure.
    ret = avformat_open_input(&r->fmt, path, NULL, NULL);
    if (ret < 0) {
        r->fmt = NULL;
        set_err(err, errlen, "cannot open input", ret);
        *code = r->io_error ? TVAD_ERR_IO : classify(ret);
        tvad_close(r);
        return NULL;
    }
    ret = avformat_find_stream_info(r->fmt, NULL);
    if (ret < 0) {
        set_err(err, errlen, "cannot read stream information", ret);
        *code = r->io_error ? TVAD_ERR_IO : classify(ret);
        tvad_close(r);
        return NULL;
    }
    AVFormatContext *f = r->fmt;
    info->n_streams = (int)f->nb_streams;
    copy_name(info->container, sizeof info->container, f->iformat ? f->iformat->name : "");
    if (f->duration > 0) info->duration_us = f->duration; // AV_TIME_BASE is microseconds

    // Program selection (multi-program transport streams): automatic
    // selection only considers the streams of the requested program.
    info->n_programs = (int)f->nb_programs;
    const AVProgram *prog = NULL;
    if (opt->program > 0) {
        for (unsigned p = 0; p < f->nb_programs; p++)
            if (f->programs[p]->id == opt->program) prog = f->programs[p];
        if (!prog) {
            set_err(err, errlen, "the requested program is not in the input", 0);
            *code = TVAD_ERR_SELECTION;
            tvad_close(r);
            return NULL;
        }
    }

    // Deterministic track selection (MEDIA-03): explicit index, else the
    // lowest-index non-attached-picture video stream; the default-flagged
    // audio stream, else the lowest-index one.
    int first_audio = -1, default_audio = -1, c608 = -1;
    for (unsigned i = 0; i < f->nb_streams; i++) {
        AVStream *st = f->streams[i];
        if (prog) {
            int in = 0;
            for (unsigned k = 0; k < prog->nb_stream_indexes; k++)
                if (prog->stream_index[k] == i) in = 1;
            if (!in) continue;
        }
        enum AVMediaType t = st->codecpar->codec_type;
        if (t == AVMEDIA_TYPE_VIDEO && !(st->disposition & AV_DISPOSITION_ATTACHED_PIC) && r->vidx < 0 &&
            opt->video_index == TVAD_TRACK_AUTO)
            r->vidx = (int)i;
        if (t == AVMEDIA_TYPE_AUDIO || (t == AVMEDIA_TYPE_VIDEO && !(st->disposition & AV_DISPOSITION_ATTACHED_PIC)))
            info->n_av_streams++;
        if (t == AVMEDIA_TYPE_AUDIO) {
            if (first_audio < 0) first_audio = (int)i;
            if (default_audio < 0 && (st->disposition & AV_DISPOSITION_DEFAULT)) default_audio = (int)i;
        }
        if (t == AVMEDIA_TYPE_SUBTITLE && st->codecpar->codec_id == AV_CODEC_ID_EIA_608 && c608 < 0) c608 = (int)i;
    }
    if (opt->video_index >= 0) {
        if (opt->video_index >= (int)f->nb_streams ||
            f->streams[opt->video_index]->codecpar->codec_type != AVMEDIA_TYPE_VIDEO ||
            (f->streams[opt->video_index]->disposition & AV_DISPOSITION_ATTACHED_PIC)) {
            set_err(err, errlen, "selected video track is not a video stream", 0);
            *code = TVAD_ERR_SELECTION;
            tvad_close(r);
            return NULL;
        }
        r->vidx = opt->video_index;
    }
    if (opt->audio_index >= 0) {
        if (opt->audio_index < (int)f->nb_streams &&
            f->streams[opt->audio_index]->codecpar->codec_type == AVMEDIA_TYPE_AUDIO)
            r->aidx = opt->audio_index;
        else
            info->audio_index = -3; // requested but unavailable
    } else if (opt->audio_index == TVAD_TRACK_AUTO) {
        r->aidx = default_audio >= 0 ? default_audio : first_audio;
    }
    if (opt->caption_index >= 0) {
        if (opt->caption_index < (int)f->nb_streams &&
            f->streams[opt->caption_index]->codecpar->codec_type == AVMEDIA_TYPE_SUBTITLE) {
            if (f->streams[opt->caption_index]->codecpar->codec_id == AV_CODEC_ID_EIA_608) {
                r->cidx = opt->caption_index;
                info->caption_kind = TVAD_CC_TRACK;
            } else {
                info->caption_kind = TVAD_CC_UNSUPPORTED;
            }
            info->caption_index = opt->caption_index;
            copy_name(info->ccodec, sizeof info->ccodec,
                      avcodec_get_name(f->streams[opt->caption_index]->codecpar->codec_id));
        } else {
            info->caption_index = -3;
        }
    } else if (opt->caption_index == TVAD_TRACK_AUTO && c608 >= 0) {
        r->cidx = c608;
        info->caption_kind = TVAD_CC_TRACK;
        info->caption_index = c608;
        copy_name(info->ccodec, sizeof info->ccodec, "eia_608");
    } else if (opt->caption_index == TVAD_TRACK_AUTO || opt->caption_index == TVAD_TRACK_EMBEDDED) {
        info->caption_kind = r->vidx >= 0 && !opt->reference_only ? TVAD_CC_EMBEDDED : TVAD_CC_NONE;
    }
    r->cc_embedded = info->caption_kind == TVAD_CC_EMBEDDED;
    if (opt->video_index == TVAD_TRACK_NONE) r->vidx = -1;
    // Report the program that carries the selected video (or audio) stream.
    for (unsigned p = 0; p < f->nb_programs && !info->program; p++) {
        const AVProgram *pg = f->programs[p];
        for (unsigned k = 0; k < pg->nb_stream_indexes; k++) {
            int si = (int)pg->stream_index[k];
            if ((r->vidx >= 0 && si == r->vidx) || (r->vidx < 0 && si == r->aidx)) info->program = pg->id;
        }
    }

    for (unsigned i = 0; i < f->nb_streams; i++)
        if ((int)i != r->vidx && (int)i != r->aidx && (int)i != r->cidx) f->streams[i]->discard = AVDISCARD_ALL;

    if (r->vidx >= 0) {
        AVStream *st = f->streams[r->vidx];
        AVCodecParameters *p = st->codecpar;
        // Hardware decoding is an optimization: any failure to set it up
        // falls back to software decoding.
        if (opt->hw_decode) {
            enum AVHWDeviceType type = av_hwdevice_find_type_by_name("videotoolbox");
            if (type != AV_HWDEVICE_TYPE_NONE && av_hwdevice_ctx_create(&r->hwdev, type, NULL, NULL, 0) < 0) r->hwdev = NULL;
        }
        ret = open_decoder(&r->vdec, st, opt, 1, r->hwdev);
        if (ret < 0 && r->hwdev) {
            av_buffer_unref(&r->hwdev);
            ret = open_decoder(&r->vdec, st, opt, 1, NULL);
        }
        info->hw_active = r->hwdev != NULL;
        if (ret < 0) {
            set_err(err, errlen, "cannot open video decoder", ret);
            *code = classify(ret) == TVAD_ERR_MEDIA ? TVAD_ERR_UNSUPPORTED : classify(ret);
            tvad_close(r);
            return NULL;
        }
        info->video_index = r->vidx;
        if (st->duration > 0) info->video_duration_us = av_rescale_q(st->duration, st->time_base, tvad_us);
        copy_name(info->vcodec, sizeof info->vcodec, avcodec_get_name(p->codec_id));
        copy_name(info->vprofile, sizeof info->vprofile, avcodec_profile_name(p->codec_id, p->profile));
        copy_name(info->pixfmt, sizeof info->pixfmt, av_get_pix_fmt_name(p->format));
        const AVPixFmtDescriptor *d = av_pix_fmt_desc_get(p->format);
        info->bit_depth = d ? d->comp[0].depth : 0;
        info->coded_w = p->width;
        info->coded_h = p->height;
        AVRational sar = av_guess_sample_aspect_ratio(f, st, NULL);
        info->sar_num = sar.num;
        info->sar_den = sar.den;
        copy_name(info->color_range, sizeof info->color_range, av_color_range_name(p->color_range));
        copy_name(info->color_trc, sizeof info->color_trc, av_color_transfer_name(p->color_trc));
        info->hdr = p->color_trc == AVCOL_TRC_SMPTE2084 || p->color_trc == AVCOL_TRC_ARIB_STD_B67;
        const char *fo = "unknown";
        switch (p->field_order) {
        case AV_FIELD_PROGRESSIVE: fo = "progressive"; break;
        case AV_FIELD_TT: fo = "tt"; break;
        case AV_FIELD_BB: fo = "bb"; break;
        case AV_FIELD_TB: fo = "tb"; break;
        case AV_FIELD_BT: fo = "bt"; break;
        default: break;
        }
        copy_name(info->field_order, sizeof info->field_order, fo);
        AVRational fr = av_guess_frame_rate(f, st, NULL);
        info->fps_num = fr.num;
        info->fps_den = fr.den;
        info->tb_num = st->time_base.num;
        info->tb_den = st->time_base.den;
        const AVPacketSideData *sd =
            av_packet_side_data_get(p->coded_side_data, p->nb_coded_side_data, AV_PKT_DATA_DISPLAYMATRIX);
        if (sd && sd->size >= TVAD_DISPLAY_MATRIX_LEN * sizeof(int32_t)) {
            double rot = av_display_rotation_get((const int32_t *)sd->data);
            info->rotation = (int)(rot < 0 ? rot - 0.5 : rot + 0.5);
        }
    }
    if (r->aidx >= 0) {
        AVStream *st = f->streams[r->aidx];
        ret = open_decoder(&r->adec, st, opt, 0, NULL);
        if (ret < 0) {
            // Audio is optional evidence: report as unavailable (MEDIA-04).
            r->aidx = -1;
            info->audio_index = -3;
        } else {
            info->audio_index = r->aidx;
            copy_name(info->acodec, sizeof info->acodec, avcodec_get_name(st->codecpar->codec_id));
            info->sample_rate = st->codecpar->sample_rate;
            info->channels = st->codecpar->ch_layout.nb_channels;
            av_channel_layout_describe(&st->codecpar->ch_layout, info->ch_layout, sizeof info->ch_layout);
        }
    }
    *code = 0;
    return r;

fail_av:
    set_err(err, errlen, "cannot allocate demuxer", ret);
    *code = classify(ret);
    tvad_close(r);
    return NULL;
}

// ---- video -----------------------------------------------------------------

// Reduce the luma plane by an integer box filter and normalize it to the
// canonical full-range 0-255 scale. Returns 0 or a TVAD_ERR_*.
static int reduce_luma(tvad_reader *r, const AVFrame *f, uint8_t *dst, int dst_cap, tvad_event *ev) {
    const AVPixFmtDescriptor *d = av_pix_fmt_desc_get(f->format);
    if (!d || (d->flags & (AV_PIX_FMT_FLAG_RGB | AV_PIX_FMT_FLAG_PAL | AV_PIX_FMT_FLAG_BITSTREAM |
                           AV_PIX_FMT_FLAG_HWACCEL | AV_PIX_FMT_FLAG_BE)) ||
        d->comp[0].plane != 0 || d->comp[0].offset != 0 ||
        (d->comp[0].shift != 0 && (d->comp[0].depth <= TVAD_MIN_DEPTH || d->comp[0].shift + d->comp[0].depth > 16)) ||
        d->comp[0].depth < TVAD_MIN_DEPTH || d->comp[0].depth > TVAD_MAX_DEPTH ||
        d->comp[0].step != (d->comp[0].depth > TVAD_MIN_DEPTH ? 2 : 1)) {
        snprintf(ev->err, sizeof ev->err, "unsupported pixel format %s", d ? d->name : "?");
        return TVAD_ERR_UNSUPPORTED;
    }
    int depth = d->comp[0].depth;
    int w = f->width, h = f->height;
    int k = 1;
    while ((w + k - 1) / k > r->opt.max_w || (h + k - 1) / k > r->opt.max_h) k++;
    int ow = w / k, oh = h / k;
    if (ow < 1 || oh < 1 || ow * oh > dst_cap) {
        snprintf(ev->err, sizeof ev->err, "picture %dx%d cannot be reduced to the analysis bound", w, h);
        return TVAD_ERR_UNSUPPORTED;
    }
    int cols = ow * k;
    if (cols > r->colsum_cap) {
        free(r->colsum16);
        free(r->colsum32);
        r->colsum16 = malloc(sizeof(uint16_t) * (size_t)cols);
        r->colsum32 = malloc(sizeof(uint32_t) * (size_t)cols);
        if (!r->colsum16 || !r->colsum32) return TVAD_ERR_NOMEM;
        r->colsum_cap = cols;
    }
    int limited = f->color_range != AVCOL_RANGE_JPEG;
    const uint8_t *lut = r->lut_limited;
    // Bright threshold in coded 8-bit units (canonical value mapped back).
    int thr8 = -1;
    if (r->opt.bright_luma >= 0) thr8 = limited ? TVAD_LIMITED_BLACK + (r->opt.bright_luma * TVAD_LIMITED_SPAN + TVAD_LUMA_MAX / 2) / TVAD_LUMA_MAX : r->opt.bright_luma;
    uint64_t bright = 0;
    const uint32_t area = (uint32_t)(k * k);
    const uint64_t inv = (TVAD_RECIP_ONE + area / 2) / area;
    int use16 = depth == TVAD_MIN_DEPTH && k <= TVAD_MAX_K16;

    for (int oy = 0; oy < oh; oy++) {
        if (depth == TVAD_MIN_DEPTH) {
            if (use16) {
                uint16_t *acc = r->colsum16;
                memset(acc, 0, sizeof(uint16_t) * (size_t)cols);
                for (int dy = 0; dy < k; dy++) {
                    const uint8_t *src = f->data[0] + (ptrdiff_t)(oy * k + dy) * f->linesize[0];
                    for (int x = 0; x < cols; x++) acc[x] += src[x];
                    if (thr8 >= 0) {
                        uint32_t c = 0;
                        for (int x = 0; x < cols; x++) c += src[x] > thr8;
                        bright += c;
                    }
                }
                uint8_t *out = dst + (ptrdiff_t)oy * ow;
                for (int ox = 0; ox < ow; ox++) {
                    uint32_t s = 0;
                    for (int j = 0; j < k; j++) s += acc[ox * k + j];
                    uint8_t v = tvad_clamp_luma((uint32_t)(((uint64_t)s * inv + TVAD_RECIP_HALF) >> TVAD_RECIP_SHIFT));
                    out[ox] = limited ? lut[v] : v;
                }
                continue;
            }
            uint32_t *acc = r->colsum32;
            memset(acc, 0, sizeof(uint32_t) * (size_t)cols);
            for (int dy = 0; dy < k; dy++) {
                const uint8_t *src = f->data[0] + (ptrdiff_t)(oy * k + dy) * f->linesize[0];
                for (int x = 0; x < cols; x++) acc[x] += src[x];
                if (thr8 >= 0) {
                    uint32_t c = 0;
                    for (int x = 0; x < cols; x++) c += src[x] > thr8;
                    bright += c;
                }
            }
            uint8_t *out = dst + (ptrdiff_t)oy * ow;
            for (int ox = 0; ox < ow; ox++) {
                uint64_t s = 0;
                for (int j = 0; j < k; j++) s += acc[ox * k + j];
                uint8_t v = tvad_clamp_luma((uint32_t)((s * inv + TVAD_RECIP_HALF) >> TVAD_RECIP_SHIFT));
                out[ox] = limited ? lut[v] : v;
            }
        } else {
            // High bit depth: reduce each sample to 8 bits (bit-depth
            // normalization) before averaging.
            int sh = depth - TVAD_MIN_DEPTH;
            // Samples stored in the high bits (P010 from hardware decoders)
            // are moved down first.
            int pos = d->comp[0].shift;
            uint32_t *acc = r->colsum32;
            memset(acc, 0, sizeof(uint32_t) * (size_t)cols);
            for (int dy = 0; dy < k; dy++) {
                const uint16_t *src = (const uint16_t *)(f->data[0] + (ptrdiff_t)(oy * k + dy) * f->linesize[0]);
                int thr = (thr8 << sh) | ((1 << sh) - 1);
                uint32_t c = 0;
                if (pos == 0) {
                    for (int x = 0; x < cols; x++) acc[x] += src[x];
                    if (thr8 >= 0)
                        for (int x = 0; x < cols; x++) c += src[x] > thr;
                } else {
                    for (int x = 0; x < cols; x++) {
                        int v = src[x] >> pos;
                        acc[x] += (uint32_t)v;
                        c += v > thr;
                    }
                }
                if (thr8 >= 0) bright += c;
            }
            uint8_t *out = dst + (ptrdiff_t)oy * ow;
            for (int ox = 0; ox < ow; ox++) {
                uint64_t s = 0;
                for (int j = 0; j < k; j++) s += acc[ox * k + j];
                uint8_t v = tvad_clamp_luma((uint32_t)((s * inv + TVAD_RECIP_HALF) >> TVAD_RECIP_SHIFT) >> sh);
                out[ox] = limited ? lut[v] : v;
            }
        }
    }
    ev->w = ow;
    ev->h = oh;
    ev->scale = k;
    ev->bright_frac = thr8 >= 0 ? (float)((double)bright / ((double)cols * (double)(oh * k))) : -1.0f;
    return 0;
}

static int emit_video(tvad_reader *r, uint8_t *luma, int luma_cap, uint8_t *cc, int cc_cap, tvad_event *ev) {
    AVFrame *f = r->frame;
    AVStream *st = r->fmt->streams[r->vidx];
    ev->kind = TVAD_VIDEO;
    int64_t ts = f->best_effort_timestamp;
    ev->no_pts = ts == AV_NOPTS_VALUE;
    ev->pts_us = ev->no_pts ? 0 : av_rescale_q(ts, st->time_base, tvad_us);
    // With coarse sampling a frame stands for the skipped ones after it, so
    // its own coded duration is not its presentation interval.
    ev->dur_us = f->duration > 0 && !r->opt.reference_only ? av_rescale_q(f->duration, st->time_base, tvad_us) : 0;
    ev->coded_w = f->width;
    ev->coded_h = f->height;
    AVRational sar = av_guess_sample_aspect_ratio(r->fmt, st, f);
    ev->sar_num = sar.num;
    ev->sar_den = sar.den;
    ev->interlaced = (f->flags & AV_FRAME_FLAG_INTERLACED) != 0;
    // A frame is corrupt when the decoder flags it or reports errors it
    // concealed (decode_error_flags), as FFmpeg's own tools judge it.
    ev->corrupt = (f->flags & AV_FRAME_FLAG_CORRUPT) != 0 || f->decode_error_flags != 0;
    ev->pos = (int64_t)(intptr_t)f->opaque - 1;
    ev->cc_len = 0;
    const AVFrameSideData *sd = av_frame_get_side_data(f, AV_FRAME_DATA_A53_CC);
    // Frame-attached caption bytes are passed on only when embedded captions
    // are the selected source: never with captions off, a selected caption
    // track, or an unavailable or unsupported one. Caption bytes of skipped
    // frames are lost, so partial data is withheld as well.
    if (r->cc_embedded && sd && sd->size > 0 && cc_cap > 0 && !r->opt.reference_only) {
        int n = (int)sd->size < cc_cap ? (int)sd->size : cc_cap;
        n -= n % TVAD_CC_TRIPLET;
        memcpy(cc, sd->data, (size_t)n);
        ev->cc_len = n;
    }
    const AVFrame *pix = f;
    if (f->hw_frames_ctx) {
        // A hardware surface: copy it to memory (NV12 or P010, luma in
        // plane 0) and measure that.
        if (!r->swframe && !(r->swframe = av_frame_alloc())) return TVAD_ERR_NOMEM;
        av_frame_unref(r->swframe);
        if (av_hwframe_transfer_data(r->swframe, f, 0) < 0) {
            snprintf(ev->err, sizeof ev->err, "cannot read a hardware-decoded picture");
            av_frame_unref(f);
            return TVAD_ERR_MEDIA;
        }
        r->swframe->color_range = f->color_range;
        r->swframe->width = f->width;
        r->swframe->height = f->height;
        pix = r->swframe;
    }
    int ret = reduce_luma(r, pix, luma, luma_cap, ev);
    av_frame_unref(f);
    return ret;
}

// ---- audio -----------------------------------------------------------------

static void audio_emit_window(tvad_reader *r) {
    if (r->a_have > 0 && r->aw_n == r->aw_cap) {
        // Grow instead of dropping: a lost window would shrink audio coverage.
        int cap = r->aw_cap ? r->aw_cap * 2 : TVAD_AW_INITIAL_CAP;
        tvad_audio_window *p = realloc(r->aw, sizeof(tvad_audio_window) * (size_t)cap);
        if (p) {
            r->aw = p;
            r->aw_cap = cap;
        }
    }
    if (r->a_have > 0 && r->aw_n < r->aw_cap) {
        double peak = 0, sum = 0;
        int used = 0;
        for (int c = 0; c < r->a_ch; c++) {
            if (c == r->a_lfe && r->a_ch > 1) continue;
            double ms = r->a_sum[c] / r->a_have;
            if (ms > peak) peak = ms;
            sum += ms;
            used++;
        }
        tvad_audio_window *w = &r->aw[r->aw_n++];
        w->start_us = r->a_start_us;
        w->dur_us = (int32_t)((int64_t)r->a_have * TVAD_US_PER_SEC / r->a_rate);
        w->peak_ms = (float)peak;
        w->mean_ms = (float)(used ? sum / used : 0);
    }
    r->a_have = 0;
}

#define SUMSQ(TYPE, EXPR)                                                        \
    do {                                                                         \
        if (planar) {                                                            \
            const TYPE *p = (const TYPE *)f->extended_data[c] + off;             \
            for (int i = 0; i < take; i++) { double v = EXPR(p[i]); s += v * v; } \
        } else {                                                                 \
            const TYPE *p = (const TYPE *)f->extended_data[0] + (size_t)off * ch + c; \
            for (int i = 0; i < take; i++) { double v = EXPR(p[(size_t)i * ch]); s += v * v; } \
        }                                                                        \
    } while (0)

#define AS_F(x) ((double)(x))
#define AS_S16(x) ((double)(x) * (1.0 / 32768.0))
#define AS_S32(x) ((double)(x) * (1.0 / 2147483648.0))
#define AS_U8(x) (((double)(x) - 128.0) * (1.0 / 128.0))

// Fixed windows on the track's own sample clock (AUDIO-01). A timestamp
// jump of more than half a window restarts the window so a hole is never
// measured as audio.
static void audio_frame(tvad_reader *r, const AVFrame *f) {
    AVStream *st = r->fmt->streams[r->aidx];
    int ch = f->ch_layout.nb_channels;
    int rate = f->sample_rate;
    if (ch <= 0 || rate <= 0 || f->nb_samples <= 0) return;
    if (ch > TVAD_MAX_CH) ch = TVAD_MAX_CH;
    int64_t ts = f->best_effort_timestamp;
    int64_t pts_us = ts == AV_NOPTS_VALUE ? r->a_next_us : av_rescale_q(ts, st->time_base, tvad_us);
    if (rate != r->a_rate || ch != r->a_ch) {
        if (r->a_have * 2 >= r->a_need) audio_emit_window(r);
        r->a_have = 0;
        r->a_rate = rate;
        r->a_ch = ch;
        r->a_need = (int)((int64_t)r->opt.audio_window_us * rate / TVAD_US_PER_SEC);
        if (r->a_need < 1) r->a_need = 1;
        r->a_lfe = r->opt.exclude_lfe ? av_channel_layout_index_from_channel(&f->ch_layout, AV_CHAN_LOW_FREQUENCY) : -1;
    } else if (r->a_have > 0) {
        int64_t d = pts_us - r->a_next_us;
        if (d < 0) d = -d;
        if (d > r->opt.audio_window_us / 2) {
            if (r->a_have * 2 >= r->a_need) audio_emit_window(r);
            r->a_have = 0;
        }
    }
    int planar = av_sample_fmt_is_planar(f->format);
    enum AVSampleFormat packed = av_get_packed_sample_fmt(f->format);
    if (packed != AV_SAMPLE_FMT_FLT && packed != AV_SAMPLE_FMT_DBL && packed != AV_SAMPLE_FMT_S16 &&
        packed != AV_SAMPLE_FMT_S32 && packed != AV_SAMPLE_FMT_U8) {
        // Unmeasurable audio is missing evidence, never silence (MEDIA-04).
        r->a_unsupported = 1;
        r->a_have = 0;
        return;
    }
    int n = f->nb_samples;
    for (int off = 0; off < n;) {
        if (r->a_have == 0) {
            r->a_start_us = pts_us + (int64_t)off * TVAD_US_PER_SEC / rate;
            memset(r->a_sum, 0, sizeof(double) * (size_t)ch);
        }
        int take = r->a_need - r->a_have;
        if (take > n - off) take = n - off;
        for (int c = 0; c < ch; c++) {
            double s = 0;
            switch (packed) {
            case AV_SAMPLE_FMT_FLT: SUMSQ(float, AS_F); break;
            case AV_SAMPLE_FMT_DBL: SUMSQ(double, AS_F); break;
            case AV_SAMPLE_FMT_S16: SUMSQ(int16_t, AS_S16); break;
            case AV_SAMPLE_FMT_S32: SUMSQ(int32_t, AS_S32); break;
            case AV_SAMPLE_FMT_U8: SUMSQ(uint8_t, AS_U8); break;
            default: break;
            }
            r->a_sum[c] += s;
        }
        r->a_have += take;
        off += take;
        if (r->a_have == r->a_need) audio_emit_window(r);
    }
    r->a_next_us = pts_us + (int64_t)n * TVAD_US_PER_SEC / rate;
}

static int flush_audio_out(tvad_reader *r, tvad_audio_window *aw, int aw_cap, tvad_event *ev) {
    int n = r->aw_n < aw_cap ? r->aw_n : aw_cap;
    memcpy(aw, r->aw, sizeof(tvad_audio_window) * (size_t)n);
    if (n < r->aw_n) memmove(r->aw, r->aw + n, sizeof(tvad_audio_window) * (size_t)(r->aw_n - n));
    r->aw_n -= n;
    ev->kind = TVAD_AUDIO;
    ev->n_audio = n;
    return 0;
}

static void decode_audio(tvad_reader *r, AVPacket *pkt) {
    if (avcodec_send_packet(r->adec, pkt) < 0) return; // optional evidence: skip undecodable audio
    for (;;) {
        int ret = avcodec_receive_frame(r->adec, r->frame);
        if (ret < 0) break;
        audio_frame(r, r->frame);
        av_frame_unref(r->frame);
    }
}

// ---- main loop -------------------------------------------------------------

int tvad_next(tvad_reader *r, uint8_t *luma, int luma_cap, uint8_t *cc, int cc_cap,
              tvad_audio_window *aw, int aw_cap, tvad_event *ev) {
    memset(ev, 0, offsetof(tvad_event, err));
    ev->err[0] = 0;
    ev->pos = -1;
    for (;;) {
        if (atomic_load_explicit(&r->cancel, memory_order_relaxed)) {
            snprintf(ev->err, sizeof ev->err, "canceled");
            return TVAD_ERR_CANCELED;
        }
        if (r->vdec && r->video_pending) {
            int ret = avcodec_receive_frame(r->vdec, r->frame);
            if (ret == 0) return emit_video(r, luma, luma_cap, cc, cc_cap, ev);
            r->video_pending = 0;
            if (ret == AVERROR_EOF) {
                r->draining = 2;
            } else if (ret != AVERROR(EAGAIN)) {
                if (r->draining == 1 && ++r->drain_errors > TVAD_MAX_DRAIN_ERRORS) r->draining = 2;
                ev->kind = TVAD_DECODE_ERROR;
                set_err(ev->err, sizeof ev->err, "video decode", ret);
                return 0;
            }
        }
        if (r->aw_n >= TVAD_AW_FLUSH) return flush_audio_out(r, aw, aw_cap, ev);
        if (r->draining) {
            if (r->draining == 1) {
                r->video_pending = 1;
                continue;
            }
            if (r->aw_n > 0) return flush_audio_out(r, aw, aw_cap, ev);
            ev->kind = TVAD_EOF;
            return 0;
        }
        int ret = av_read_frame(r->fmt, r->pkt);
        if (ret == AVERROR(EAGAIN)) continue;
        if (ret < 0) {
            if (ret != AVERROR_EOF || r->io_error) {
                set_err(ev->err, sizeof ev->err, "read", ret);
                return r->io_error ? TVAD_ERR_IO : classify(ret);
            }
            // End of input: flush both decoders.
            if (r->adec) {
                decode_audio(r, NULL);
                if (r->a_have * 2 >= r->a_need) audio_emit_window(r);
                r->a_have = 0;
            }
            r->draining = 1;
            if (r->vdec) {
                avcodec_send_packet(r->vdec, NULL);
                r->video_pending = 1;
            } else {
                r->draining = 2;
            }
            continue;
        }
        int idx = r->pkt->stream_index;
        if (idx == r->vidx && r->vdec) {
            r->pkt->opaque = (void *)(intptr_t)(r->pkt->pos + 1);
            ret = avcodec_send_packet(r->vdec, r->pkt);
            av_packet_unref(r->pkt);
            r->video_pending = 1;
            if (ret < 0 && ret != AVERROR(EAGAIN)) {
                ev->kind = TVAD_DECODE_ERROR;
                set_err(ev->err, sizeof ev->err, "video packet", ret);
                return 0;
            }
        } else if (idx == r->aidx && r->adec) {
            decode_audio(r, r->pkt);
            av_packet_unref(r->pkt);
        } else if (idx == r->cidx) {
            AVStream *st = r->fmt->streams[idx];
            int n = r->pkt->size < cc_cap ? r->pkt->size : cc_cap;
            n -= n % TVAD_CC_TRIPLET;
            if (n > 0 && r->pkt->pts != AV_NOPTS_VALUE) {
                memcpy(cc, r->pkt->data, (size_t)n);
                ev->kind = TVAD_CAPTION;
                ev->cc_len = n;
                ev->pts_us = av_rescale_q(r->pkt->pts, st->time_base, tvad_us);
                av_packet_unref(r->pkt);
                return 0;
            }
            av_packet_unref(r->pkt);
        } else {
            av_packet_unref(r->pkt);
        }
    }
}
