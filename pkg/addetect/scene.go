// SPDX-License-Identifier: Apache-2.0

package addetect

import "math/bits"

// sceneDetector finds abrupt and gradual visual changes (SCENE-01).
//
// Metric: the luminance histogram of the measurement region is folded to
// scene.histogram_bins equal-width bins. The distance between two frames is
// the total-variation distance of their normalized histograms,
// 0.5 * sum(|a_i - b_i|), in [0,1]. A hard cut is an adjacent-frame distance
// at or above scene.cut_threshold. A gradual change is a distance at or above
// scene.gradual_threshold across scene.gradual_window with no hard cut
// inside the window. Events closer than scene.min_spacing to the previous
// event are suppressed.
type sceneDetector struct {
	shift     uint
	bins      int
	cutThr    float64
	gradThr   float64
	windowUS  int64
	spacingUS int64
	events    []SceneEvent
	lastEvent int64
	haveEvent bool
	gradArmed bool
	prevMean  float64
	// ring of recent folded histograms for the gradual comparison
	ring    [sceneRing][]uint32
	ringPTS [sceneRing]int64
	ringN   [sceneRing]int
	head    int // next write position
	count   int // valid entries
	storage []uint32
	lastCut int64
	haveCut bool
}

const sceneRing = 64

func (s *sceneDetector) init(cfg *Config) {
	s.bins = cfg.Scene.HistogramBins
	s.shift = uint(8 - bits.TrailingZeros(uint(s.bins)))
	s.cutThr = cfg.Scene.CutThreshold
	s.gradThr = cfg.Scene.GradualThreshold
	s.windowUS = us(cfg.Scene.GradualWindow)
	s.spacingUS = us(cfg.Scene.MinSpacing)
	s.storage = make([]uint32, sceneRing*s.bins)
	for i := range s.ring {
		s.ring[i] = s.storage[i*s.bins : (i+1)*s.bins]
	}
	s.gradArmed = true
}

// reset forgets temporal context (timestamp or geometry discontinuity).
func (s *sceneDetector) reset() { s.count = 0 }

// histDistance returns the total-variation distance of two histograms with
// na and nb samples.
func histDistance(a, b []uint32, na, nb int) float64 {
	if na == nb {
		var d uint64
		for i, v := range a {
			w := b[i]
			if v > w {
				d += uint64(v - w)
			} else {
				d += uint64(w - v)
			}
		}
		return float64(d) / float64(2*na)
	}
	var d float64
	fa, fb := 1/float64(na), 1/float64(nb)
	for i, v := range a {
		x := float64(v)*fa - float64(b[i])*fb
		if x < 0 {
			x = -x
		}
		d += x
	}
	return d / 2
}

func (s *sceneDetector) frame(pts int64, hist *[lumaLevels]uint32, n int, mean float64) {
	cur := s.ring[s.head]
	clear(cur)
	for v, c := range hist {
		cur[v>>s.shift] += c
	}
	s.ringPTS[s.head], s.ringN[s.head] = pts, n
	if s.count > 0 {
		prev := (s.head + sceneRing - 1) % sceneRing
		d := histDistance(cur, s.ring[prev], n, s.ringN[prev])
		if d >= s.cutThr {
			s.lastCut, s.haveCut = pts, true
			s.emit(pts, KindSceneCut, d, mean-s.prevMean)
		} else if s.windowUS > 0 {
			s.gradual(pts, cur, n, mean)
		}
	}
	s.prevMean = mean
	s.head = (s.head + 1) % sceneRing
	if s.count < sceneRing {
		s.count++
	}
}

func (s *sceneDetector) gradual(pts int64, cur []uint32, n int, mean float64) {
	// Newest retained frame at least one window old (oldest retained when
	// the ring is shorter than the window).
	ref := -1
	for k := 1; k <= s.count; k++ {
		i := (s.head + sceneRing - k) % sceneRing
		ref = i
		if pts-s.ringPTS[i] >= s.windowUS {
			break
		}
	}
	if ref < 0 || pts-s.ringPTS[ref] < s.windowUS {
		return
	}
	d := histDistance(cur, s.ring[ref], n, s.ringN[ref])
	if d < s.gradThr {
		s.gradArmed = true
		return
	}
	if !s.gradArmed || (s.haveCut && s.lastCut >= s.ringPTS[ref]) {
		return
	}
	s.gradArmed = false
	s.emit(s.ringPTS[ref]+(pts-s.ringPTS[ref])/2, KindSceneGradual, d, 0)
}

func (s *sceneDetector) emit(t int64, kind string, d, dMean float64) {
	if s.haveEvent && t-s.lastEvent < s.spacingUS {
		return // duplicate suppression around one transition
	}
	s.lastEvent, s.haveEvent = t, true
	s.events = append(s.events, SceneEvent{TimeUS: t, Kind: kind, Distance: d, BrightnessDelta: dMean})
}
