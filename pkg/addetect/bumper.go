// SPDX-License-Identifier: Apache-2.0

package addetect

import "math"

// Bumper template matching (5.8).
//
// Preprocessing: the template PNG is converted to full-range luma and
// area-resampled to the pixel size its normalized region occupies in the
// analysis image. Metric: zero-mean normalized cross-correlation (ZNCC)
// between the template and the frame region, clamped to [0,1]; it is
// invariant to brightness offset and contrast gain. A match is ZNCC >=
// threshold; consecutive matching observations form one event when they
// span at least the template's minimum temporal support.
type bumperScaled struct {
	ok     bool
	x0, y0 int
	w, h   int
	t      []int32 // zero-mean template scaled by n (sum-free form)
	norm   float64 // sum of squares of t
}

type bumperRun struct {
	active      bool
	start, last int64
	best        float64
}

type bumperMatcher struct {
	imgs       []bumperImage
	scaled     []bumperScaled
	runs       []bumperRun
	events     []BumperEvent
	sampleUS   int64
	nextSample int64
	lastPTS    int64
}

func (b *bumperMatcher) init(cfg *Config, assets *Assets) {
	if !cfg.Signals.Bumpers || assets == nil {
		return
	}
	b.imgs = assets.bumpers
	b.scaled = make([]bumperScaled, len(b.imgs))
	b.runs = make([]bumperRun, len(b.imgs))
	b.sampleUS = us(cfg.Bumpers.SampleInterval)
}

func (b *bumperMatcher) active() bool { return len(b.imgs) > 0 }

// areaResample scales src (sw x sh) to tw x th by averaging each target
// pixel's source footprint.
func areaResample(src []uint8, sw, sh, tw, th int) []uint8 {
	out := make([]uint8, tw*th)
	for ty := 0; ty < th; ty++ {
		y0 := ty * sh / th
		y1 := max((ty+1)*sh/th, y0+1)
		for tx := 0; tx < tw; tx++ {
			x0 := tx * sw / tw
			x1 := max((tx+1)*sw/tw, x0+1)
			sum := 0
			for y := y0; y < y1; y++ {
				for _, v := range src[y*sw+x0 : y*sw+x1] {
					sum += int(v)
				}
			}
			n := (y1 - y0) * (x1 - x0)
			out[ty*tw+tx] = uint8((sum + n/2) / n)
		}
	}
	return out
}

func (b *bumperMatcher) newGeometry(ep *geomEpoch, warn func(code, msg string, t int64, hasT bool)) {
	for i := range b.imgs {
		img := &b.imgs[i]
		s := &b.scaled[i]
		*s = bumperScaled{}
		x0, y0, x1, y1 := img.tmpl.Region.pixels(ep.aw, ep.ah)
		w, h := x1-x0, y1-y0
		if w < 4 || h < 4 {
			warn(WarnBumperRegionTooSmall, bumperTemplateLabel+img.tmpl.ID+": region is smaller than 4x4 analysis pixels; template unavailable for this geometry", 0, false)
			continue
		}
		if img.tmpl.Geometry == GeometryStrict {
			display := float64(w) * float64(ep.sarN) / (float64(h) * float64(ep.sarD))
			own := float64(img.w) / float64(img.h)
			if math.Abs(display-own) > aspectCompatTolerance*own {
				warn(WarnBumperGeometryMismatch, bumperTemplateLabel+img.tmpl.ID+": aspect differs from its region (strict geometry); template unavailable for this geometry", 0, false)
				continue
			}
		}
		px := areaResample(img.luma, img.w, img.h, w, h)
		n := w * h
		sum := 0
		for _, v := range px {
			sum += int(v)
		}
		// t_i = n*p_i - sum keeps the zero-mean template in exact integers.
		s.t = make([]int32, n)
		for k, v := range px {
			d := n*int(v) - sum
			s.t[k] = int32(d)
			s.norm += float64(d) * float64(d)
		}
		if s.norm == 0 {
			warn(WarnBumperFlatTemplate, bumperTemplateLabel+img.tmpl.ID+": no structure left at analysis resolution; template unavailable for this geometry", 0, false)
			continue
		}
		s.ok, s.x0, s.y0, s.w, s.h = true, x0, y0, w, h
	}
}

func (b *bumperMatcher) closeRun(i int, end int64) {
	r := &b.runs[i]
	if !r.active {
		return
	}
	r.active = false
	t := b.imgs[i].tmpl
	if end-r.start >= us(t.MinSupport) && end > r.start {
		b.events = append(b.events, BumperEvent{
			TemplateID: t.ID, StartUS: r.start, EndUS: end, Quality: r.best, UncertaintyUS: b.sampleUS,
		})
	}
}

func (b *bumperMatcher) discontinuity(pts int64) {
	for i := range b.runs {
		b.closeRun(i, min(b.runs[i].last+max(b.sampleUS, 1), pts))
	}
	b.nextSample = pts
}

func (b *bumperMatcher) finish(end int64) {
	for i := range b.runs {
		b.closeRun(i, min(b.runs[i].last+max(b.sampleUS, 1), end))
	}
}

func (b *bumperMatcher) frame(pts int64, luma []byte, stride int) {
	b.lastPTS = pts
	if pts < b.nextSample {
		return
	}
	b.nextSample = pts + b.sampleUS
	for i := range b.scaled {
		s := &b.scaled[i]
		if !s.ok {
			continue
		}
		var sf, sff, sft int64
		for y := 0; y < s.h; y++ {
			row := luma[(s.y0+y)*stride+s.x0 : (s.y0+y)*stride+s.x0+s.w]
			trow := s.t[y*s.w : (y+1)*s.w]
			for x, v := range row {
				f := int64(v)
				sf += f
				sff += f * f
				sft += f * int64(trow[x])
			}
		}
		n := float64(s.w * s.h)
		varF := float64(sff) - float64(sf)*float64(sf)/n
		var q float64
		if varF > 0 {
			// sum(t) is zero, so sum(f*t) is already the covariance term.
			q = float64(sft) / math.Sqrt(varF*s.norm)
			q = max(0, min(q, 1))
		}
		r := &b.runs[i]
		if q >= b.imgs[i].tmpl.Threshold {
			if !r.active {
				*r = bumperRun{active: true, start: pts, best: q}
			}
			r.last = pts
			r.best = max(r.best, q)
		} else if r.active {
			b.closeRun(i, pts)
		}
	}
}
