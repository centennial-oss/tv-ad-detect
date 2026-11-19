// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"context"
	"image"
	"io"
)

// CapturedFrame is one decoded picture, as luminance at the analysis
// resolution of cfg (canonical 0-255 scale).
type CapturedFrame struct {
	// TimeUS is the frame time on the result timeline (0 = the first
	// frame), so it matches the times of results and diagnostics.
	TimeUS int64
	// Image holds the analysis-resolution luminance.
	Image *image.Gray
	// CodedWidth and CodedHeight are the picture's coded size.
	CodedWidth, CodedHeight int
}

// CaptureFrame decodes src from the start and returns the first frame at or
// after atUS on the result timeline, for making a bumper template by hand
// (BUMPER-01). Raise cfg.Sampling.AnalysisMaxWidth/Height for a sharper
// picture. Nothing is written anywhere (BUMPER-03).
func CaptureFrame(ctx context.Context, src Source, cfg *Config, atUS int64) (*CapturedFrame, error) {
	if src == nil {
		return nil, Errorf(KindInvalidConfig, "no input source")
	}
	if cfg == nil {
		cfg = DefaultConfig()
	}
	cfg = cfg.Clone()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// Only pictures are needed.
	cfg.Signals.Audio, cfg.Signals.Captions = false, false
	req := openRequestFor(cfg, func(Warning) {})
	stream, err := src.Open(ctx, req)
	if err != nil {
		return nil, wrapCtx(err)
	}
	defer stream.Close()
	var (
		ev    Event
		first int64
		have  bool
		n     int
	)
	for {
		ev.Kind = 0
		if err := stream.Next(&ev); err != nil {
			if err == io.EOF {
				return nil, Errorf(KindInvalidConfig, "the recording ends before %d us", atUS)
			}
			return nil, wrapCtx(err)
		}
		if n++; n&cancelCheckMask == 0 {
			if err := ctx.Err(); err != nil {
				return nil, wrapCtx(err)
			}
		}
		if ev.Kind != EventVideo || ev.Video.NoPTS {
			continue
		}
		f := &ev.Video
		if !have {
			first, have = f.PTS, true
		}
		t := f.PTS - first
		if t < atUS {
			continue
		}
		luma, w, h, stride := f.Luma, f.Width, f.Height, f.Stride
		if k := max(ceilDiv(w, cfg.Sampling.AnalysisMaxWidth), ceilDiv(h, cfg.Sampling.AnalysisMaxHeight)); k > 1 {
			buf := make([]byte, (w/k)*(h/k))
			w, h = DownsampleLuma(buf, luma, w, h, stride, k)
			luma, stride = buf, w
		}
		img := image.NewGray(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			copy(img.Pix[y*img.Stride:y*img.Stride+w], luma[y*stride:y*stride+w])
		}
		cw, ch := f.CodedWidth, f.CodedHeight
		if cw <= 0 || ch <= 0 {
			cw, ch = w*max(f.Scale, 1), h*max(f.Scale, 1)
		}
		return &CapturedFrame{TimeUS: t, Image: img, CodedWidth: cw, CodedHeight: ch}, nil
	}
}

// Crop returns the part of img inside a normalized region, as a new image.
func (r Region) Crop(img *image.Gray) *image.Gray {
	b := img.Bounds()
	x0, y0, x1, y1 := r.pixels(b.Dx(), b.Dy())
	out := image.NewGray(image.Rect(0, 0, x1-x0, y1-y0))
	for y := y0; y < y1; y++ {
		copy(out.Pix[(y-y0)*out.Stride:(y-y0)*out.Stride+(x1-x0)], img.Pix[(y+b.Min.Y)*img.Stride+b.Min.X+x0:(y+b.Min.Y)*img.Stride+b.Min.X+x1])
	}
	return out
}
