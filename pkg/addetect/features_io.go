// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
)

// Feature file format (PIPE-03). A feature file is:
//
//	magic       8 bytes  "TVADFEAT"
//	version     uint32   featureFileVersion
//	body                 fields in a fixed order, see writeBody
//	digest      32 bytes SHA-256 of everything before it
//
// Integers are little-endian fixed width, floats are their IEEE-754 bits,
// so every value reads back exactly. Strings and byte strings carry a uvarint
// length. Exported structures that the result reports (media description,
// warnings, events, calibrations, assets) are embedded as JSON, which
// round-trips their values exactly. A reader refuses other versions and a
// file whose digest does not match.
const (
	// FeatureFileMagic starts every feature file.
	FeatureFileMagic = "TVADFEAT"
	// FeatureFileExtension is the conventional feature file name extension.
	FeatureFileExtension = ".tvadf"

	featureFileVersion = 3
	featureDigestLen   = sha256.Size
	// featureMaxString bounds one length-prefixed string or JSON blob.
	featureMaxString = 1 << 30
)

// Fixed record sizes in the body, for count validation on read.
const (
	frameRecordLen      = 8 + 2 + 2 + 2 + 1
	audioRecordLen      = 8 + 4 + 4 + 4
	aspectRecordLen     = 8 + 8 + 8 + 4*8
	logoEpochRecordLen  = 5*8 + 3 + 2
	logoSampleRecordLen = 8 + 1
	logoTrialRecordLen  = 6*8 + 1
	logoCountRecordLen  = 4 + 4 + 4
	capRecordLen        = 8 + 8 + 1
	epochRecordLen      = 8 * 8
)

// IsFeatureFile reports whether head (the first bytes of a file) starts like
// a feature file.
func IsFeatureFile(head []byte) bool {
	return bytes.HasPrefix(head, []byte(FeatureFileMagic))
}

// WriteTo writes the features in the versioned feature file format. It
// implements io.WriterTo.
func (f *Features) WriteTo(w io.Writer) (int64, error) {
	cw := &countingWriter{w: w}
	h := sha256.New()
	bw := bufio.NewWriter(io.MultiWriter(cw, h))
	e := &fenc{w: bw}
	e.raw([]byte(FeatureFileMagic))
	e.u32(featureFileVersion)
	f.writeBody(e)
	if e.err == nil {
		e.err = bw.Flush()
	}
	if e.err == nil {
		_, e.err = cw.Write(h.Sum(nil))
	}
	if e.err != nil {
		return cw.n, Errorf(KindIO, "cannot write features: %w", e.err)
	}
	return cw.n, nil
}

func (f *Features) writeBody(e *fenc) {
	m := &f.m
	e.str(FeatureSchemaVersion)
	e.str(f.identity)
	keys := make([]string, 0, len(f.keys))
	for k := range f.keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	e.uvar(uint64(len(keys)))
	for _, k := range keys {
		e.str(k)
		e.str(f.keys[k])
	}
	e.json(&f.info)
	e.json(f.warnings)
	e.bool(f.incomplete)

	for _, v := range [...]int64{m.t0, m.dur, m.frameUS, m.lastEnd, m.finalUS, m.audioKnown, int64(m.overlaps), int64(m.missingPTS),
		int64(m.decodeErrs), int64(m.corrupt), int64(m.interlaced), m.firstAudio, m.featureBytes, m.firstPTS, m.lastPTS} {
		e.i64(v)
	}
	e.bool(m.finalEstimated)
	e.bool(m.haveAudio)
	e.bool(m.bumpersActive)

	e.uvar(uint64(len(m.frames)))
	for i := range m.frames {
		fr := &m.frames[i]
		e.i64(fr.pts)
		e.u16(fr.mean)
		e.u16(fr.std)
		e.u16(fr.bright)
		e.u8(fr.flags)
	}
	e.uvar(uint64(len(m.audio)))
	for i := range m.audio {
		w := &m.audio[i]
		e.i64(w.StartUS)
		e.u32(uint32(w.DurationUS))
		e.u32(math.Float32bits(w.PeakMS))
		e.u32(math.Float32bits(w.MeanMS))
	}
	e.uvar(uint64(len(m.aspect)))
	for i := range m.aspect {
		s := &m.aspect[i]
		e.i64(s.start)
		e.i64(s.end)
		e.f64(s.ratio)
		for _, v := range [...]int{s.l, s.t, s.w, s.h} {
			e.i64(int64(v))
		}
	}
	e.uvar(uint64(len(m.logoObs)))
	for i := range m.logoObs {
		ob := &m.logoObs[i]
		for _, v := range [...]int64{ob.start, ob.end, int64(ob.aw), int64(ob.ah), int64(ob.learnN)} {
			e.i64(v)
		}
		e.str(ob.origin)
		e.str(ob.assetVersion)
		e.str(ob.failed)
		e.uvar(uint64(len(ob.samples)))
		for _, sm := range ob.samples {
			e.i64(sm.pts)
			e.bool(sm.usable)
		}
		e.uvar(uint64(len(ob.trials)))
		for k := range ob.trials {
			tr := &ob.trials[k]
			for _, v := range [...]int64{int64(tr.box[0]), int64(tr.box[1]), int64(tr.box[2]), int64(tr.box[3]), int64(tr.pixels), int64(tr.bgPixels), int64(tr.selfShift)} {
				e.i64(v)
			}
			var asset []byte
			if tr.learned != nil {
				var err error
				if asset, err = tr.learned.Marshal(); err != nil && e.err == nil {
					e.err = err
				}
			}
			e.bytes(asset)
			for j := range ob.samples {
				e.u32(tr.hits[j])
				e.u32(tr.noise[j])
				e.u32(tr.shift[j])
			}
		}
	}
	e.uvar(uint64(len(m.caps)))
	for i := range m.caps {
		s := &m.caps[i]
		e.i64(s.start)
		e.i64(s.end)
		e.u8(uint8(s.style))
	}
	e.uvar(uint64(len(m.epochs)))
	for i := range m.epochs {
		ep := &m.epochs[i]
		for _, v := range [...]int{ep.firstFrame, ep.codedW, ep.codedH, ep.aw, ep.ah, ep.scale, ep.sarN, ep.sarD} {
			e.i64(int64(v))
		}
	}
	e.i64(int64(m.regN))
	e.uvar(uint64(len(m.regStats)))
	for _, r := range m.regStats {
		e.u16(r.mean)
		e.u16(r.std)
		e.u16(r.bright)
	}
	e.json(m.gaps)
	e.json(m.scene)
	e.json(m.resolution)
	e.json(m.bumpers)
	e.json(m.rebases)
	e.json(m.phraseHits)
}

// ReadFeatures reads a feature file written by Features.WriteTo. It fails
// with KindUnsupported for another format or version and with KindIO for a
// truncated or damaged file.
func ReadFeatures(r io.Reader) (*Features, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, Errorf(KindIO, "cannot read features: %w", err)
	}
	if !IsFeatureFile(data) {
		return nil, Errorf(KindUnsupported, "not a feature file (no %s header)", FeatureFileMagic)
	}
	if len(data) < len(FeatureFileMagic)+4+featureDigestLen {
		return nil, Errorf(KindIO, "the feature file is truncated")
	}
	if v := binary.LittleEndian.Uint32(data[len(FeatureFileMagic):]); v != featureFileVersion {
		return nil, Errorf(KindUnsupported, "feature file version %d is not supported (this build reads version %d)", v, featureFileVersion)
	}
	body, sum := data[:len(data)-featureDigestLen], data[len(data)-featureDigestLen:]
	if got := sha256.Sum256(body); !bytes.Equal(got[:], sum) {
		return nil, Errorf(KindIO, "the feature file is damaged (digest mismatch)")
	}
	d := &fdec{b: body[len(FeatureFileMagic)+4:]}
	f, err := readBody(d)
	if err == nil && d.err == nil && len(d.b) != 0 {
		d.err = fmt.Errorf("%d unexpected bytes after the last field", len(d.b))
	}
	if err == nil {
		err = d.err
	}
	if err != nil {
		var ae *Error
		if errors.As(err, &ae) {
			return nil, err
		}
		return nil, Errorf(KindIO, "the feature file is malformed: %w", err)
	}
	return f, nil
}

func readBody(d *fdec) (*Features, error) {
	if v := d.str(); d.err == nil && v != FeatureSchemaVersion {
		return nil, Errorf(KindUnsupported, "feature schema %q is not supported (this build uses %q)", v, FeatureSchemaVersion)
	}
	f := &Features{identity: d.str()}
	n := d.count(2)
	f.keys = make(map[string]string, n)
	for i := 0; i < n && d.err == nil; i++ {
		k := d.str()
		f.keys[k] = d.str()
	}
	d.json(&f.info)
	d.json(&f.warnings)
	f.incomplete = d.bool()

	m := &f.m
	for _, p := range [...]*int64{&m.t0, &m.dur, &m.frameUS, &m.lastEnd, &m.finalUS, &m.audioKnown} {
		*p = d.i64()
	}
	for _, p := range [...]*int{&m.overlaps, &m.missingPTS, &m.decodeErrs, &m.corrupt, &m.interlaced} {
		*p = int(d.i64())
	}
	for _, p := range [...]*int64{&m.firstAudio, &m.featureBytes, &m.firstPTS, &m.lastPTS} {
		*p = d.i64()
	}
	m.finalEstimated = d.bool()
	m.haveAudio = d.bool()
	m.bumpersActive = d.bool()

	if n = d.count(frameRecordLen); d.err == nil {
		m.frames = make([]frameRec, n)
		for i := range m.frames {
			fr := &m.frames[i]
			fr.pts, fr.mean, fr.std, fr.bright, fr.flags = d.i64(), d.u16(), d.u16(), d.u16(), d.u8()
		}
	}
	if n = d.count(audioRecordLen); d.err == nil {
		m.audio = make([]AudioWindow, n)
		for i := range m.audio {
			w := &m.audio[i]
			w.StartUS, w.DurationUS = d.i64(), int32(d.u32())
			w.PeakMS, w.MeanMS = math.Float32frombits(d.u32()), math.Float32frombits(d.u32())
		}
	}
	if n = d.count(aspectRecordLen); d.err == nil {
		m.aspect = make([]aspectSpan, n)
		for i := range m.aspect {
			s := &m.aspect[i]
			s.start, s.end, s.ratio = d.i64(), d.i64(), d.f64()
			s.l, s.t, s.w, s.h = int(d.i64()), int(d.i64()), int(d.i64()), int(d.i64())
		}
	}
	if n = d.count(logoEpochRecordLen); d.err == nil {
		m.logoObs = make([]logoEpochObs, n)
		for i := range m.logoObs {
			if err := readLogoEpoch(d, &m.logoObs[i]); err != nil {
				return nil, err
			}
		}
	}
	if n = d.count(capRecordLen); d.err == nil {
		m.caps = make([]capSpan, n)
		for i := range m.caps {
			s := &m.caps[i]
			s.start, s.end, s.style = d.i64(), d.i64(), CaptionStyle(d.u8())
		}
	}
	if n = d.count(epochRecordLen); d.err == nil {
		m.epochs = make([]geomEpoch, n)
		for i := range m.epochs {
			ep := &m.epochs[i]
			for _, p := range [...]*int{&ep.firstFrame, &ep.codedW, &ep.codedH, &ep.aw, &ep.ah, &ep.scale, &ep.sarN, &ep.sarD} {
				*p = int(d.i64())
			}
		}
	}
	m.regN = int(d.i64())
	if n = d.count(regStatBytes); d.err == nil {
		m.regStats = make([]regStat, n)
		for i := range m.regStats {
			m.regStats[i] = regStat{mean: d.u16(), std: d.u16(), bright: d.u16()}
		}
		if m.regN < 0 || (m.regN > 0 && len(m.regStats) != m.regN*len(m.frames)) || (m.regN == 0 && len(m.regStats) != 0) {
			d.err = errors.New("region statistics do not match the frames")
		}
	}
	d.json(&m.gaps)
	d.json(&m.scene)
	d.json(&m.resolution)
	d.json(&m.bumpers)
	d.json(&m.rebases)
	d.json(&m.phraseHits)
	if d.err == nil && (len(m.frames) == 0 || len(m.epochs) == 0 || m.dur <= 0) {
		d.err = errors.New("no video measurements")
	}
	return f, nil
}

func readLogoEpoch(d *fdec, ob *logoEpochObs) error {
	ob.start, ob.end = d.i64(), d.i64()
	ob.aw, ob.ah, ob.learnN = int(d.i64()), int(d.i64()), int(d.i64())
	ob.origin, ob.assetVersion, ob.failed = d.str(), d.str(), d.str()
	n := d.count(logoSampleRecordLen)
	ob.samples = make([]logoSample, n)
	for i := range ob.samples {
		ob.samples[i] = logoSample{pts: d.i64(), usable: d.bool()}
	}
	nt := d.count(logoTrialRecordLen)
	ob.trials = make([]logoTrial, nt)
	for k := range ob.trials {
		tr := &ob.trials[k]
		for _, p := range [...]*int{&tr.box[0], &tr.box[1], &tr.box[2], &tr.box[3], &tr.pixels, &tr.bgPixels, &tr.selfShift} {
			*p = int(d.i64())
		}
		if asset := d.bytes(); d.err == nil && len(asset) > 0 {
			l, err := ParseLogoAsset(asset)
			if err != nil {
				return err
			}
			tr.learned = l
		}
		if d.err == nil && len(d.b) < n*logoCountRecordLen {
			d.err = io.ErrUnexpectedEOF
		}
		if d.err != nil {
			return nil
		}
		tr.hits, tr.noise, tr.shift = make([]uint32, n), make([]uint32, n), make([]uint32, n)
		for j := 0; j < n; j++ {
			tr.hits[j], tr.noise[j], tr.shift[j] = d.u32(), d.u32(), d.u32()
		}
		if tr.pixels <= 0 {
			d.err = errors.New("a logo candidate without pixels")
		}
	}
	return nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// fenc writes feature file fields; the first error sticks.
type fenc struct {
	w   io.Writer
	err error
	buf [binary.MaxVarintLen64]byte
}

func (e *fenc) raw(p []byte) {
	if e.err == nil {
		_, e.err = e.w.Write(p)
	}
}
func (e *fenc) u8(v uint8)   { e.buf[0] = v; e.raw(e.buf[:1]) }
func (e *fenc) u16(v uint16) { binary.LittleEndian.PutUint16(e.buf[:], v); e.raw(e.buf[:2]) }
func (e *fenc) u32(v uint32) { binary.LittleEndian.PutUint32(e.buf[:], v); e.raw(e.buf[:4]) }
func (e *fenc) i64(v int64)  { binary.LittleEndian.PutUint64(e.buf[:], uint64(v)); e.raw(e.buf[:8]) }
func (e *fenc) f64(v float64) {
	binary.LittleEndian.PutUint64(e.buf[:], math.Float64bits(v))
	e.raw(e.buf[:8])
}
func (e *fenc) uvar(v uint64) { e.raw(e.buf[:binary.PutUvarint(e.buf[:], v)]) }
func (e *fenc) bool(v bool) {
	if v {
		e.u8(1)
	} else {
		e.u8(0)
	}
}
func (e *fenc) bytes(p []byte) { e.uvar(uint64(len(p))); e.raw(p) }
func (e *fenc) str(s string)   { e.bytes([]byte(s)) }
func (e *fenc) json(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		if e.err == nil {
			e.err = err
		}
		return
	}
	e.bytes(data)
}

// fdec reads feature file fields from memory; the first error sticks and
// later reads return zero values.
type fdec struct {
	b   []byte
	err error
}

func (d *fdec) take(n int) []byte {
	if d.err != nil {
		return nil
	}
	if n < 0 || n > len(d.b) {
		d.err = io.ErrUnexpectedEOF
		return nil
	}
	p := d.b[:n]
	d.b = d.b[n:]
	return p
}

func (d *fdec) u8() uint8 {
	if p := d.take(1); p != nil {
		return p[0]
	}
	return 0
}
func (d *fdec) u16() uint16 {
	if p := d.take(2); p != nil {
		return binary.LittleEndian.Uint16(p)
	}
	return 0
}
func (d *fdec) u32() uint32 {
	if p := d.take(4); p != nil {
		return binary.LittleEndian.Uint32(p)
	}
	return 0
}
func (d *fdec) i64() int64 {
	if p := d.take(8); p != nil {
		return int64(binary.LittleEndian.Uint64(p))
	}
	return 0
}
func (d *fdec) f64() float64 { return math.Float64frombits(uint64(d.i64())) }
func (d *fdec) bool() bool   { return d.u8() != 0 }

func (d *fdec) uvar() uint64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.err = io.ErrUnexpectedEOF
		return 0
	}
	d.b = d.b[n:]
	return v
}

// count reads an element count and checks that that many records of at
// least minLen bytes can still be present, so a damaged count never causes
// a huge allocation.
func (d *fdec) count(minLen int) int {
	v := d.uvar()
	if d.err == nil && v > uint64(len(d.b)/minLen) {
		d.err = fmt.Errorf("a count of %d does not fit the remaining %d bytes", v, len(d.b))
	}
	if d.err != nil {
		return 0
	}
	return int(v)
}

func (d *fdec) bytes() []byte {
	n := d.uvar()
	if d.err == nil && n > featureMaxString {
		d.err = fmt.Errorf("a field of %d bytes is too large", n)
	}
	return d.take(int(n))
}

func (d *fdec) str() string { return string(d.bytes()) }

func (d *fdec) json(v any) {
	p := d.bytes()
	if d.err == nil {
		if err := json.Unmarshal(p, v); err != nil {
			d.err = err
		}
	}
}
