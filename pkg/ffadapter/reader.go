// SPDX-License-Identifier: Apache-2.0

package ffadapter

/*
#cgo pkg-config: libavformat libavcodec libavutil
#cgo CFLAGS: -O3
#include <stdlib.h>
#include "reader.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/cgo"
	"strconv"
	"sync"
	"unsafe"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
)

// The C shim writes audio windows straight into Go memory; the layouts must
// agree.
var _ [unsafe.Sizeof(addetect.AudioWindow{})]byte = [unsafe.Sizeof(C.tvad_audio_window{})]byte{}

const (
	ccBufSize    = 3 * 512
	audioBufSize = 512
)

// byteSource feeds a demuxer through the custom I/O callback.
type byteSource interface {
	// read fills p and returns the count; 0 means end of stream.
	read(p []byte) (int, error)
}

//export tvadGoRead
func tvadGoRead(h C.uintptr_t, buf *C.uint8_t, size C.int) C.int {
	st := cgo.Handle(h).Value().(*ioState)
	if size <= 0 {
		return 0
	}
	n, err := st.src.read(unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(size)))
	if n > 0 {
		return C.int(n)
	}
	if err == nil || err == io.EOF {
		return 0
	}
	st.err = err
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return -2
	}
	return -1
}

type ioState struct {
	src byteSource
	err error // first Go-side read failure
}

// reader wraps one C demux/decode instance.
type reader struct {
	mu     sync.Mutex // guards c against a concurrent cancel
	c      *C.tvad_reader
	info   C.tvad_info
	ev     C.tvad_event
	luma   []byte
	cc     []byte
	aw     []addetect.AudioWindow
	io     *ioState
	handle cgo.Handle
	stop   func() bool
}

func parseTrack(s string) C.int {
	switch s {
	case "", addetect.TrackAuto:
		return C.TVAD_TRACK_AUTO
	case addetect.TrackNone:
		return C.TVAD_TRACK_NONE
	case addetect.TrackEmbedded:
		return C.TVAD_TRACK_EMBEDDED
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return C.TVAD_TRACK_AUTO
	}
	return C.int(n)
}

func baseOptions(req *addetect.OpenRequest) C.tvad_options {
	var o C.tvad_options
	o.video_index = parseTrack(req.VideoTrack)
	o.program = C.int(req.Program)
	if req.HardwareDecode {
		o.hw_decode = 1
	}
	o.audio_index = parseTrack(req.AudioTrack)
	o.caption_index = parseTrack(req.CaptionTrack)
	if !req.NeedAudio {
		o.audio_index = C.TVAD_TRACK_NONE
	}
	if !req.NeedCaptions {
		o.caption_index = C.TVAD_TRACK_NONE
	}
	o.max_w, o.max_h = C.int(req.AnalysisMaxWidth), C.int(req.AnalysisMaxHeight)
	o.bright_luma = -1
	if req.FullResBrightCheck {
		o.bright_luma = C.int(req.BrightPixelLuma)
	}
	o.threads = C.int(max(req.Threads, 1))
	switch req.SkipLoopFilter {
	case addetect.LoopFilterNonRef:
		o.skip_loop_filter = 1
	case addetect.LoopFilterAll:
		o.skip_loop_filter = 2
	}
	if req.DecodeFrames == addetect.DecodeReference {
		o.reference_only = 1
	}
	o.audio_window_us = C.int(req.AudioWindow.Microseconds())
	if req.ExcludeLFE {
		o.exclude_lfe = 1
	}
	o.io_buffer_size = C.int(req.Package.IOBufferSize)
	return o
}

func errKind(code C.int) addetect.ErrorKind {
	switch code {
	case C.TVAD_ERR_IO:
		return addetect.KindIO
	case C.TVAD_ERR_UNSUPPORTED:
		return addetect.KindUnsupported
	case C.TVAD_ERR_CANCELED:
		return addetect.KindCanceled
	case C.TVAD_ERR_NOMEM:
		return addetect.KindResourceLimit
	case C.TVAD_ERR_SELECTION:
		return addetect.KindInvalidConfig
	}
	return addetect.KindMedia
}

// openReader opens a path (src == nil) or a custom byte stream.
func openReader(ctx context.Context, path string, src byteSource, opt *C.tvad_options, lumaCap int) (*reader, error) {
	r := &reader{
		luma: make([]byte, lumaCap),
		cc:   make([]byte, ccBufSize),
		aw:   make([]addetect.AudioWindow, audioBufSize),
	}
	var cpath *C.char
	var handle C.uintptr_t
	if src != nil {
		r.io = &ioState{src: src}
		r.handle = cgo.NewHandle(r.io)
		handle = C.uintptr_t(r.handle)
	} else {
		cpath = C.CString(path)
		defer C.free(unsafe.Pointer(cpath))
	}
	var code C.int
	var msg [errorMessageLen]C.char
	r.c = C.tvad_open(cpath, handle, opt, &r.info, &code, &msg[0], C.int(len(msg)))
	if r.c == nil {
		err := r.failure(ctx, code, C.GoString(&msg[0]))
		if src != nil {
			r.handle.Delete()
		}
		return nil, err
	}
	// Cancellation interrupts blocking demuxer I/O promptly (API-03).
	r.stop = context.AfterFunc(ctx, func() {
		r.mu.Lock()
		if r.c != nil {
			C.tvad_cancel(r.c)
		}
		r.mu.Unlock()
	})
	return r, nil
}

func (r *reader) failure(ctx context.Context, code C.int, msg string) error {
	if err := ctx.Err(); err != nil {
		return addetect.Errorf(addetect.KindCanceled, msgCanceled, err)
	}
	if r.io != nil && r.io.err != nil {
		var ae *addetect.Error
		if errors.As(r.io.err, &ae) {
			return r.io.err
		}
		return addetect.Errorf(addetect.KindIO, "%s: %w", msg, r.io.err)
	}
	return addetect.Errorf(errKind(code), "%s", msg)
}

// next advances to the next event; r.ev describes it.
func (r *reader) next(ctx context.Context) error {
	if r.c == nil {
		// Closed: the C state is freed and must not be touched.
		return addetect.Errorf(addetect.KindInternal, msgStreamClosed)
	}
	code := C.tvad_next(r.c,
		(*C.uint8_t)(unsafe.Pointer(&r.luma[0])), C.int(len(r.luma)),
		(*C.uint8_t)(unsafe.Pointer(&r.cc[0])), C.int(len(r.cc)),
		(*C.tvad_audio_window)(unsafe.Pointer(&r.aw[0])), C.int(len(r.aw)),
		&r.ev)
	if code != 0 {
		return r.failure(ctx, code, C.GoString(&r.ev.err[0]))
	}
	return nil
}

func (r *reader) close() {
	if r.stop != nil {
		r.stop()
	}
	r.mu.Lock()
	if r.c != nil {
		C.tvad_close(r.c)
		r.c = nil
	}
	r.mu.Unlock()
	if r.io != nil {
		r.handle.Delete()
		r.io = nil
	}
}

// fill converts the current C event. It reports false for kinds the caller
// handles itself (end of stream).
func (r *reader) fill(ev *addetect.Event) bool {
	e := &r.ev
	switch e.kind {
	case C.TVAD_VIDEO:
		ev.Kind = addetect.EventVideo
		w, h := int(e.w), int(e.h)
		ev.Video = addetect.VideoFrame{
			PTS: int64(e.pts_us), NoPTS: e.no_pts != 0, Duration: int64(e.dur_us),
			CodedWidth: int(e.coded_w), CodedHeight: int(e.coded_h),
			SARNum: int(e.sar_num), SARDen: int(e.sar_den),
			Luma: r.luma[:w*h], Width: w, Height: h, Stride: w, Scale: int(e.scale),
			FullResBrightFraction: float32(e.bright_frac),
			CC:                    r.cc[:int(e.cc_len)],
			Interlaced:            e.interlaced != 0, Corrupt: e.corrupt != 0,
			Resource: -1,
		}
	case C.TVAD_AUDIO:
		ev.Kind = addetect.EventAudio
		ev.Audio = r.aw[:int(e.n_audio)]
	case C.TVAD_CAPTION:
		ev.Kind = addetect.EventCaptionData
		ev.Caption = addetect.CaptionObservation{PTS: int64(e.pts_us), CC: r.cc[:int(e.cc_len)]}
	case C.TVAD_DECODE_ERROR:
		ev.Kind = addetect.EventDecodeError
		ev.DecodeError = C.GoString(&e.err[0])
	default:
		return false
	}
	return true
}

func cstr(p *C.char) string { return C.GoString(p) }

// libraryVersions reports the linked FFmpeg library versions.
func libraryVersions() string {
	var buf [versionMessageLen]C.char
	C.tvad_versions(&buf[0], C.int(len(buf)))
	return C.GoString(&buf[0])
}

// describe fills track metadata from the C stream info (MEDIA-03).
func (r *reader) describe(info *addetect.MediaInfo, req *addetect.OpenRequest) {
	ci := &r.info
	// The program is reported where it is a choice: several programs, or
	// one requested.
	if ci.n_programs > 1 || req.Program > 0 {
		info.Tracks.Program = int(ci.program)
	}
	if ci.hw_active != 0 {
		info.Tracks.Video.HardwareDecoder = hardwareDecoderName
	}
	if ci.video_index >= 0 {
		v := &info.Tracks.Video
		v.Index = int(ci.video_index)
		v.Selection = selectionExplicit
		if req.VideoTrack == addetect.TrackAuto || req.VideoTrack == "" {
			v.Selection = selectionAutoVideo
		}
		v.Codec, v.Profile, v.PixelFormat = cstr(&ci.vcodec[0]), cstr(&ci.vprofile[0]), cstr(&ci.pixfmt[0])
		v.BitDepth = int(ci.bit_depth)
		v.CodedWidth, v.CodedHeight = int(ci.coded_w), int(ci.coded_h)
		if ci.sar_num > 0 && ci.sar_den > 0 {
			v.SAR = fmt.Sprintf("%d:%d", int(ci.sar_num), int(ci.sar_den))
		}
		v.ColorRange, v.ColorTransfer, v.FieldOrder = cstr(&ci.color_range[0]), cstr(&ci.color_trc[0]), cstr(&ci.field_order[0])
		if ci.fps_num > 0 && ci.fps_den > 0 {
			v.FrameRate = fmt.Sprintf(fmtRational, int(ci.fps_num), int(ci.fps_den))
			info.FrameRateHint = float64(ci.fps_num) / float64(ci.fps_den)
		}
		if ci.tb_den > 0 {
			v.TimeBase = fmt.Sprintf(fmtRational, int(ci.tb_num), int(ci.tb_den))
		}
		v.RotationDeg = int(ci.rotation)
	}
	if ci.audio_index >= 0 {
		sel := selectionExplicit
		if req.AudioTrack == addetect.TrackAuto || req.AudioTrack == "" {
			sel = selectionAutoAudio
		}
		info.Tracks.Audio = &addetect.AudioTrackInfo{
			Index: int(ci.audio_index), Selection: sel, Codec: cstr(&ci.acodec[0]),
			SampleRate: int(ci.sample_rate), Channels: int(ci.channels), ChannelLayout: cstr(&ci.ch_layout[0]),
		}
	}
	c := &info.Tracks.Captions
	switch ci.caption_kind {
	case C.TVAD_CC_EMBEDDED:
		*c = addetect.CaptionTrackInfo{Source: addetect.CaptionSourceEmbedded, Capability: addetect.CaptionCapabilityCEA608, Detail: "CEA-608 pairs from A/53 user data of the video stream"}
	case C.TVAD_CC_TRACK:
		*c = addetect.CaptionTrackInfo{Source: addetect.CaptionSourceTrack, Index: int(ci.caption_index), Capability: addetect.CaptionCapabilityCEA608, Detail: cstr(&ci.ccodec[0])}
	case C.TVAD_CC_UNSUPPORTED:
		*c = addetect.CaptionTrackInfo{Source: addetect.CaptionSourceTrack, Index: int(ci.caption_index), Capability: addetect.CaptionCapabilityUnsupported, Detail: cstr(&ci.ccodec[0]) + " has no presentation-style semantics"}
	default:
		*c = addetect.CaptionTrackInfo{Source: addetect.CaptionSourceNone, Capability: addetect.CaptionCapabilityNone}
	}
	if info.DurationHintUS == 0 {
		info.DurationHintUS = int64(ci.duration_us)
	}
}

// checkCapabilities applies the unsupported-format and optional-stream
// policies (MEDIA-04, MEDIA-05, CFG-03).
func (r *reader) checkCapabilities(req *addetect.OpenRequest, wantVideo bool) error {
	ci := &r.info
	warn := func(code, msg string) { req.Warn(addetect.Warning{Code: code, Message: msg}) }
	if wantVideo && req.HardwareDecode && ci.hw_active == 0 && ci.video_index >= 0 {
		warn(addetect.WarnHardwareDecodeUnavailable, "input.hardware_decode=auto: no hardware decoder is available for this video; it is decoded in software")
	}
	if wantVideo {
		if ci.video_index < 0 {
			return addetect.Errorf(addetect.KindUnsupported, "the input has no decodable video track")
		}
		if ci.hdr != 0 {
			if req.FailOnUnsupportedFormat {
				return addetect.Errorf(addetect.KindUnsupported, "HDR transfer %s is not supported (input.unsupported_format_policy=fail)", cstr(&ci.color_trc[0]))
			}
			warn(addetect.WarnHDRDegraded, "HDR transfer "+cstr(&ci.color_trc[0])+": luminance thresholds are defined for SDR; results are degraded")
		}
		if req.DecodeFrames == addetect.DecodeReference {
			warn(addetect.WarnCoarseDecode, "sampling.decode_frames=reference: only reference frames are decoded; event timing is limited to the reference-frame spacing and embedded caption data is unavailable")
		}
		if ci.rotation != 0 {
			warn(addetect.WarnRotationIgnored, fmt.Sprintf("display rotation of %d degrees is ignored; measurements use the coded orientation", int(ci.rotation)))
		}
		if cstr(&ci.color_range[0]) == colorRangeUnknown {
			warn(addetect.WarnColorRangeAssumed, "video range is not signalled; limited (16-235) range is assumed")
		}
	}
	if req.NeedAudio && req.AudioTrack != addetect.TrackNone {
		switch {
		case ci.audio_index == -3:
			if req.StrictOptionalStreams {
				return addetect.Errorf(addetect.KindInvalidConfig, "requested audio track %s is unavailable (input.optional_stream_policy=strict)", req.AudioTrack)
			}
			warn(addetect.WarnAudioUnavailable, "requested audio track "+req.AudioTrack+" is unavailable; audio evidence is disabled")
		case ci.audio_index < 0 && wantVideo:
			warn(addetect.WarnNoAudio, "no audio track; audio evidence is unavailable")
		}
	}
	if req.NeedCaptions && req.CaptionTrack != addetect.TrackNone {
		switch {
		case ci.caption_index == -3:
			if req.StrictOptionalStreams {
				return addetect.Errorf(addetect.KindInvalidConfig, "requested caption track %s is unavailable (input.optional_stream_policy=strict)", req.CaptionTrack)
			}
			warn(addetect.WarnCaptionsUnavailable, "requested caption track "+req.CaptionTrack+" is unavailable; caption evidence is disabled")
		case ci.caption_kind == C.TVAD_CC_UNSUPPORTED:
			if req.StrictOptionalStreams {
				return addetect.Errorf(addetect.KindUnsupported, "caption track %s has no presentation-style semantics (input.optional_stream_policy=strict)", req.CaptionTrack)
			}
			warn(addetect.WarnCaptionsUnsupported, "the selected subtitle track carries no presentation-style information; caption evidence is neutral")
		}
	}
	return nil
}
