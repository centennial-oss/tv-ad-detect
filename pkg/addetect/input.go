// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"context"
	"io"
	"time"
)

// Source provides the observations of one recording (MEDIA-01, API-01).
//
// The packaged FFmpeg adapter (package ffadapter) implements Source for local
// recordings and HLS/DASH manifests. Applications can implement Source to
// supply already-decoded observations; see also SliceSource.
type Source interface {
	// Open starts a new, independent observation stream. The request tells
	// the source which measurements the configured detectors need.
	Open(ctx context.Context, req *OpenRequest) (Stream, error)
}

// Stream is one pass over a recording's observations. A Stream is owned by
// the analysis that opened it and is closed by it (API-03).
type Stream interface {
	// Info describes the media and the chosen tracks. It must be valid
	// after Open returns.
	Info() *MediaInfo
	// Next fills ev with the next observation. It returns io.EOF after the
	// last one. Slices referenced by ev are only valid until the next call.
	Next(ev *Event) error
	// Close releases all resources owned by the stream.
	Close() error
}

// OpenRequest carries the adapter-relevant part of the configuration.
type OpenRequest struct {
	VideoTrack string // "auto" or a container stream index
	// Program restricts automatic selection to one program of a
	// multi-program transport stream (its program number); 0 = any.
	Program int
	// HardwareDecode asks for hardware video decoding where the platform
	// offers it; the source falls back to software decoding otherwise.
	HardwareDecode bool
	AudioTrack     string // "auto", "none" or a container stream index
	CaptionTrack   string // "auto", "none", "embedded" or a container stream index

	NeedAudio    bool // audio measurements will be used
	NeedCaptions bool // caption data will be used

	// AnalysisMaxWidth/Height bound the canonical luminance image. Sources
	// reduce decoded pictures by an integer box-filter factor to fit.
	AnalysisMaxWidth  int
	AnalysisMaxHeight int
	// BrightPixelLuma (canonical 0-255) is the threshold for the optional
	// full-resolution bright-pixel measurement.
	BrightPixelLuma    int
	FullResBrightCheck bool

	AudioWindow time.Duration
	ExcludeLFE  bool

	Threads        int    // decoder worker bound (>=1)
	DecodeFrames   string // "all" or "reference" (coarse visual sampling)
	SkipLoopFilter string // "none", "nonref" or "all"

	FailOnUnsupportedFormat bool // HDR/unsupported processing: fail instead of degrade
	StrictOptionalStreams   bool // a requested optional stream that is unavailable is fatal

	Package PackageRequest

	// Warn reports a non-fatal condition. It is never nil.
	Warn func(Warning)
}

// PackageRequest carries HLS/DASH package options (section 3.1).
type PackageRequest struct {
	ManifestType     string // "auto", "hls" or "dash"
	VideoSelection   string
	AudioSelection   string
	MinHeight        int
	Degraded         bool // continue past unreadable resources, keeping their interval unknown
	AllowRemote      bool
	MaxManifestBytes int64
	MaxSegments      int
	IOBufferSize     int
	// HashMedia asks for media resources to be identified by the digest of
	// their bytes instead of by the resolver's fingerprint (PACK-09).
	HashMedia bool
}

// EventKind identifies the populated member of an Event.
type EventKind uint8

const (
	// EventVideo carries one decoded video frame in presentation order.
	EventVideo EventKind = iota + 1
	// EventAudio carries one or more audio level windows.
	EventAudio
	// EventCaptionData carries raw CEA-608/708 cc_data triplets.
	EventCaptionData
	// EventCaptionStyle carries a caller-decoded caption style interval.
	EventCaptionStyle
	// EventGap declares an interval of the presentation with no media
	// (for example an unreadable package resource in degraded mode).
	EventGap
	// EventDecodeError reports a recoverable decode failure.
	EventDecodeError
)

// Event is a tagged union of observations. It is reused between Next calls
// so steady-state streaming does not allocate.
type Event struct {
	Kind    EventKind
	Video   VideoFrame
	Audio   []AudioWindow
	Caption CaptionObservation
	Gap     GapObservation
	// DecodeError describes an EventDecodeError.
	DecodeError string
}

// VideoFrame is one decoded picture reduced to the canonical luminance
// analysis image (MEDIA-05): 8-bit, full-range-equivalent 0-255 luma.
type VideoFrame struct {
	// PTS is the presentation timestamp in microseconds on the source's
	// presentation timeline (not yet normalized to the first frame).
	PTS int64
	// NoPTS is set when the media carried no timestamp for this frame.
	NoPTS bool
	// Duration is the declared frame duration in microseconds, 0 if unknown.
	Duration int64

	// CodedWidth/CodedHeight are the coded picture dimensions before any
	// analysis reduction (SIZE-01).
	CodedWidth, CodedHeight int
	// SARNum/SARDen is the sample (pixel) aspect ratio; 0/1 means unknown
	// and is treated as square.
	SARNum, SARDen int

	// Luma holds Height rows of Stride bytes; only the first Width bytes of
	// each row are meaningful.
	Luma                  []byte
	Width, Height, Stride int
	// Scale is the integer reduction factor from coded to analysis pixels.
	Scale int
	// FullResBrightFraction is the fraction of full-resolution pixels above
	// OpenRequest.BrightPixelLuma, or a negative value when not measured.
	FullResBrightFraction float32

	// CC holds CEA-608/708 cc_data triplets attached to this frame, if any.
	CC []byte

	Interlaced bool
	Corrupt    bool
	// Resource is the index of the package resource that carried the frame,
	// or -1.
	Resource int32
}

// AudioWindow is the level measurement of one fixed-duration window
// (AUDIO-01). Levels are linear mean-square values where 1.0 is full scale;
// RMS dBFS is 10*log10(mean-square).
type AudioWindow struct {
	StartUS    int64
	DurationUS int32
	// PeakMS is the largest per-channel mean-square among included channels.
	PeakMS float32
	// MeanMS is the mean of the per-channel mean-squares.
	MeanMS float32
}

// CaptionObservation is caption input for EventCaptionData and
// EventCaptionStyle.
type CaptionObservation struct {
	// PTS is the presentation time of raw data (EventCaptionData) or the
	// start of a style interval (EventCaptionStyle).
	PTS int64
	// EndPTS ends a style interval (EventCaptionStyle only).
	EndPTS int64
	// CC holds cc_data triplets (EventCaptionData only).
	CC []byte
	// Style is the caller-decoded style (EventCaptionStyle only).
	Style CaptionStyle
}

// GapObservation declares missing media on the presentation timeline.
type GapObservation struct {
	StartUS, EndUS int64
	Reason         string
	Resource       int32
	// Declared marks a gap the presentation itself declares (for example
	// EXT-X-GAP); it is unknown time but not a loss, so it does not make
	// the result incomplete.
	Declared bool
}

// CaptionStyle is a caption presentation style (CAP-01).
type CaptionStyle uint8

const (
	// CaptionUnknown: no decoding capability or insufficient observation.
	CaptionUnknown CaptionStyle = iota
	// CaptionNone: a supported service was observed with no active captions.
	CaptionNone
	CaptionRollUp
	CaptionPopOn
	CaptionPaintOn
	numCaptionStyles
)

var captionStyleNames = [numCaptionStyles]string{CaptionStyleUnknown, CaptionStyleNone, CaptionStyleRollUp, CaptionStylePopOn, CaptionStylePaintOn}

// String returns the canonical style name.
func (s CaptionStyle) String() string {
	if s < numCaptionStyles {
		return captionStyleNames[s]
	}
	return CaptionStyleUnknown
}

// ParseCaptionStyle parses a canonical style name.
func ParseCaptionStyle(s string) (CaptionStyle, bool) {
	for i, n := range captionStyleNames {
		if n == s {
			return CaptionStyle(i), true
		}
	}
	return CaptionUnknown, false
}

func (s CaptionStyle) recognized() bool { return s >= CaptionRollUp && s < numCaptionStyles }

// SliceSource is a Source over caller-provided, already-decoded observations.
// Events are replayed in slice order; video frames must be in presentation
// order. It is safe to analyze one SliceSource concurrently because streams
// only read the shared slices.
type SliceSource struct {
	Media  MediaInfo
	Events []Event
}

// Open implements Source.
func (s *SliceSource) Open(ctx context.Context, _ *OpenRequest) (Stream, error) {
	info := s.Media
	if info.Adapter.Name == "" {
		info.Adapter = AdapterInfo{Name: callerAdapterName, Version: callerAdapterVersion}
	}
	return &sliceStream{ctx: ctx, src: s, info: info}, nil
}

type sliceStream struct {
	ctx  context.Context
	src  *SliceSource
	info MediaInfo
	pos  int
}

func (s *sliceStream) Info() *MediaInfo { return &s.info }

func (s *sliceStream) Next(ev *Event) error {
	if s.pos&0xff == 0 {
		if err := s.ctx.Err(); err != nil {
			return err
		}
	}
	if s.pos >= len(s.src.Events) {
		return io.EOF
	}
	*ev = s.src.Events[s.pos]
	s.pos++
	return nil
}

func (s *sliceStream) Close() error { return nil }

// AudioWindower converts caller-provided PCM samples into AudioWindow
// measurements using the same rule as the packaged adapter: fixed windows on
// the track's own sample clock, per-channel mean-square, restart after a
// timestamp discontinuity. The zero value is not usable; use NewAudioWindower.
type AudioWindower struct {
	window  time.Duration
	exclude int // channel index excluded from levels, -1 for none
	rate    int
	chans   int
	need    int // samples per window
	have    int
	startUS int64
	nextUS  int64
	sums    []float64
}

// NewAudioWindower returns a windower. excludeChannel is the index of a
// channel to leave out (for example the LFE channel), or -1.
func NewAudioWindower(window time.Duration, excludeChannel int) *AudioWindower {
	return &AudioWindower{window: window, exclude: excludeChannel}
}

// Push adds interleaved float samples (full scale 1.0) starting at ptsUS and
// appends completed windows to dst.
func (a *AudioWindower) Push(dst []AudioWindow, ptsUS int64, sampleRate, channels int, interleaved []float32) []AudioWindow {
	if sampleRate <= 0 || channels <= 0 {
		return dst
	}
	if sampleRate != a.rate || channels != a.chans {
		dst = a.Flush(dst)
		a.rate, a.chans = sampleRate, channels
		a.need = int(int64(a.window) * int64(sampleRate) / int64(time.Second))
		if a.need < 1 {
			a.need = 1
		}
		if cap(a.sums) < channels {
			a.sums = make([]float64, channels)
		}
		a.sums = a.sums[:channels]
	}
	// A timestamp jump of more than half a window restarts the window so a
	// gap never becomes measured audio.
	if a.have > 0 {
		d := ptsUS - a.nextUS
		if d < 0 {
			d = -d
		}
		if d > int64(a.window/time.Microsecond)/2 {
			dst = a.Flush(dst)
		}
	}
	n := len(interleaved) / channels
	for i := 0; i < n; {
		if a.have == 0 {
			a.startUS = ptsUS + int64(i)*usPerSecond/int64(sampleRate)
			clear(a.sums)
		}
		take := min(a.need-a.have, n-i)
		for c := 0; c < channels; c++ {
			var s float64
			for k := i*channels + c; k < (i+take)*channels; k += channels {
				v := float64(interleaved[k])
				s += v * v
			}
			a.sums[c] += s
		}
		a.have += take
		i += take
		if a.have == a.need {
			dst = a.emit(dst)
		}
	}
	a.nextUS = ptsUS + int64(n)*usPerSecond/int64(sampleRate)
	return dst
}

// Flush emits a final partial window when it covers at least half a window.
func (a *AudioWindower) Flush(dst []AudioWindow) []AudioWindow {
	if a.have*2 >= a.need && a.have > 0 {
		return a.emit(dst)
	}
	a.have = 0
	return dst
}

func (a *AudioWindower) emit(dst []AudioWindow) []AudioWindow {
	var peak, sum float64
	used := 0
	for c, s := range a.sums {
		if c == a.exclude && len(a.sums) > 1 {
			continue
		}
		ms := s / float64(a.have)
		if ms > peak {
			peak = ms
		}
		sum += ms
		used++
	}
	w := AudioWindow{
		StartUS:    a.startUS,
		DurationUS: int32(int64(a.have) * usPerSecond / int64(a.rate)),
		PeakMS:     float32(peak),
		MeanMS:     float32(sum / float64(used)),
	}
	a.have = 0
	return append(dst, w)
}

// DownsampleLuma box-filters an 8-bit luma plane by an integer factor into
// dst, which must hold (w/factor)*(h/factor) bytes. It returns the reduced
// dimensions. Remainder columns/rows at the right/bottom edge are dropped.
func DownsampleLuma(dst, src []byte, w, h, stride, factor int) (int, int) {
	ow, oh := w/factor, h/factor
	if factor <= 1 {
		for y := 0; y < h; y++ {
			copy(dst[y*w:(y+1)*w], src[y*stride:y*stride+w])
		}
		return w, h
	}
	area := uint32(factor * factor)
	half := area / 2
	for oy := 0; oy < oh; oy++ {
		out := dst[oy*ow : (oy+1)*ow]
		for ox := range out {
			var s uint32
			for dy := 0; dy < factor; dy++ {
				row := src[(oy*factor+dy)*stride+ox*factor:]
				for _, v := range row[:factor] {
					s += uint32(v)
				}
			}
			out[ox] = uint8((s + half) / area)
		}
	}
	return ow, oh
}
