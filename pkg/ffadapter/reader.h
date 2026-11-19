// SPDX-License-Identifier: Apache-2.0

#ifndef TVAD_READER_H
#define TVAD_READER_H

#include <stdint.h>

// Sizes of the fixed text fields below.
#define TVAD_NAME_LEN 32
#define TVAD_SHORT_NAME_LEN 16
#define TVAD_PROFILE_LEN 48
#define TVAD_LAYOUT_LEN 64
#define TVAD_ERR_LEN 160

// Track selection sentinels.
#define TVAD_TRACK_AUTO (-1)
#define TVAD_TRACK_NONE (-2)
#define TVAD_TRACK_EMBEDDED (-3)

// Event kinds returned by tvad_next.
#define TVAD_EOF 0
#define TVAD_VIDEO 1
#define TVAD_AUDIO 2
#define TVAD_CAPTION 3
#define TVAD_DECODE_ERROR 4

// Caption capability.
#define TVAD_CC_NONE 0
#define TVAD_CC_EMBEDDED 1    // CEA-608/708 in video user data (A53)
#define TVAD_CC_TRACK 2       // CEA-608 caption track
#define TVAD_CC_UNSUPPORTED 3 // subtitle track without presentation styles

// Error classes returned by tvad_open / tvad_next (negative values).
#define TVAD_ERR_IO (-1)
#define TVAD_ERR_UNSUPPORTED (-2)
#define TVAD_ERR_MEDIA (-3)
#define TVAD_ERR_CANCELED (-4)
#define TVAD_ERR_NOMEM (-5)
#define TVAD_ERR_SELECTION (-6)

typedef struct tvad_reader tvad_reader;

typedef struct {
    int video_index;   // stream index, TVAD_TRACK_AUTO or TVAD_TRACK_NONE
    int audio_index;   // stream index, TVAD_TRACK_AUTO or TVAD_TRACK_NONE
    int caption_index; // stream index, AUTO, NONE or EMBEDDED
    int max_w, max_h;  // analysis image bound
    int bright_luma;   // canonical 0-255 threshold; < 0 disables the count
    int threads;
    int skip_loop_filter; // 0 none, 1 non-reference, 2 all
    int reference_only;   // decode reference frames only (coarse sampling)
    int audio_window_us;
    int exclude_lfe;
    int io_buffer_size;   // custom I/O only
    int program;          // transport stream program number; 0 = any
    int hw_decode;        // 1: try hardware video decoding (VideoToolbox), fall back to software
} tvad_options;

typedef struct {
    char container[TVAD_NAME_LEN];
    int video_index, audio_index, caption_index;
    int caption_kind;
    int n_streams;
    int n_av_streams;          // audio and (non-picture) video streams in the input
    int program;               // program number of the selected streams, 0 when the input has none
    int n_programs;            // programs in the input
    int hw_active;             // video is decoded in hardware
    int64_t video_duration_us; // declared duration of the selected video stream, 0 unknown
    // video
    char vcodec[TVAD_NAME_LEN];
    char vprofile[TVAD_PROFILE_LEN];
    char pixfmt[TVAD_NAME_LEN];
    int bit_depth;
    int coded_w, coded_h;
    int sar_num, sar_den;
    char color_range[TVAD_SHORT_NAME_LEN];
    char color_trc[TVAD_NAME_LEN];
    char field_order[TVAD_SHORT_NAME_LEN];
    int fps_num, fps_den;
    int tb_num, tb_den;
    int rotation;
    int hdr; // PQ or HLG transfer
    // audio
    char acodec[TVAD_NAME_LEN];
    int sample_rate;
    int channels;
    char ch_layout[TVAD_LAYOUT_LEN];
    int64_t duration_us; // 0 when unknown
    char ccodec[TVAD_NAME_LEN];
} tvad_info;

// tvad_audio_unsupported reports that the audio track's sample format
// cannot be measured (its windows are then absent, never silent).
int tvad_audio_unsupported(const tvad_reader *r);

// Layout matches addetect.AudioWindow.
typedef struct {
    int64_t start_us;
    int32_t dur_us;
    float peak_ms;
    float mean_ms;
} tvad_audio_window;

typedef struct {
    int kind;
    int64_t pts_us;
    int no_pts;
    int64_t dur_us;
    int coded_w, coded_h;
    int sar_num, sar_den;
    int w, h, scale;
    float bright_frac;
    int interlaced, corrupt;
    int cc_len;
    int n_audio;
    int64_t pos; // byte position of the packet in the input, -1 unknown
    char err[TVAD_ERR_LEN];
} tvad_event;

// tvad_open opens path, or, when path is NULL, a custom byte stream read
// through the Go callback identified by io_handle. On failure it returns
// NULL and sets *code to a TVAD_ERR_* class with a message in err.
tvad_reader *tvad_open(const char *path, uintptr_t io_handle, const tvad_options *opt,
                       tvad_info *info, int *code, char *err, int errlen);

// tvad_next produces the next event. luma receives the analysis image
// (luma_cap >= max_w*max_h), cc the caption bytes, aw the audio windows.
// It returns 0 on success (ev->kind is TVAD_EOF at end) or a TVAD_ERR_*.
int tvad_next(tvad_reader *r, uint8_t *luma, int luma_cap, uint8_t *cc, int cc_cap,
              tvad_audio_window *aw, int aw_cap, tvad_event *ev);

// tvad_cancel makes blocking demuxer I/O return promptly. Thread safe.
void tvad_cancel(tvad_reader *r);

void tvad_close(tvad_reader *r);

// tvad_versions writes "libavformat a.b.c libavcodec ... libavutil ...".
void tvad_versions(char *buf, int len);

#endif
