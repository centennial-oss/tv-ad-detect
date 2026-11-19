// SPDX-License-Identifier: Apache-2.0

package cea608

import (
	"math/bits"
	"unicode/utf8"
)

// TextDecoder decodes the characters of one caption service as they are
// sent, for phrase matching. It does not lay out the screen: rows, roll-ups,
// caption ends and erasures of the caption being loaded become word breaks,
// so the output is a running stream of
// caption text in sending order (pop-on text arrives shortly before it is
// shown). Nothing is retained between calls except the control-code state;
// the caller decides how much text to keep. The zero value is not usable;
// use NewTextDecoder. A TextDecoder is not safe for concurrent use.
type TextDecoder struct {
	field    uint8
	channel  uint8
	curChan  uint8
	inXDS    bool
	lastCtrl [2]byte
	repeat   bool
}

// Characters and control pairs that only the text decoder needs.
const (
	pacFirstSecond   = 0x40 // second byte of a preamble address code
	specialFirst     = 0x11 // first byte of special characters (and mid-row codes)
	extendedFirstA   = 0x12 // first byte of extended Spanish/French characters
	extendedFirstB   = 0x13 // first byte of extended Portuguese/German characters
	specialLow       = 0x30 // special characters: second byte 0x30..0x3f
	extendedLow      = 0x20 // extended characters: second byte 0x20..0x3f
	extendedHigh     = 0x3f
	midRowLow        = 0x20 // mid-row codes: second byte 0x20..0x2f
	midRowHigh       = 0x2f
	cmdBS            = 0x21 // backspace
	cmdCR            = 0x2d // carriage return
	cmdENM           = 0x2e // erase non-displayed memory
	tabFirst         = 0x17 // tab offsets: first byte 0x17, second 0x21..0x23
	space            = ' '
	transparentSpace = 0x39 // special character "transparent space"
)

// basic608 is the CEA-608 basic character set where it differs from ASCII.
var basic608 = map[byte]rune{
	0x2a: 'á', 0x5c: 'é', 0x5e: 'í', 0x5f: 'ó', 0x60: 'ú',
	0x7b: 'ç', 0x7c: '÷', 0x7d: 'Ñ', 0x7e: 'ñ', 0x7f: '█',
}

// special608 is the special character set (second byte 0x30..0x3f).
var special608 = [16]rune{'®', '°', '½', '¿', '™', '¢', '£', '♪', 'à', ' ', 'è', 'â', 'ê', 'î', 'ô', 'û'}

// extendedA608 and extendedB608 are the extended character sets (second
// byte 0x20..0x3f); each replaces the basic character sent before it.
var extendedA608 = [32]rune{
	'Á', 'É', 'Ó', 'Ú', 'Ü', 'ü', '‘', '¡', '*', '\'', '—', '©', '℠', '•', '“', '”',
	'À', 'Â', 'Ç', 'È', 'Ê', 'Ë', 'ë', 'Î', 'Ï', 'ï', 'Ô', 'Ù', 'ù', 'Û', '«', '»',
}

var extendedB608 = [32]rune{
	'Ã', 'ã', 'Í', 'Ì', 'ì', 'Ò', 'ò', 'Õ', 'õ', '{', '}', '\\', '^', '_', '|', '~',
	'Ä', 'ä', 'Ö', 'ö', 'ß', '¥', '¤', '¦', 'Å', 'å', 'Ø', 'ø', '┌', '┐', '└', '┘',
}

// NewTextDecoder returns a text decoder for service 1..4 (CC1..CC4).
func NewTextDecoder(service int) *TextDecoder {
	if service < firstService || service > lastService {
		service = firstService
	}
	n := service - firstService
	return &TextDecoder{field: uint8(n / channelsPerField), channel: uint8(n%channelsPerField + 1)}
}

// Reset forgets the control-code state (use after a discontinuity).
func (d *TextDecoder) Reset() { *d = TextDecoder{field: d.field, channel: d.channel} }

// Feed appends the text carried by one frame's cc_data triplets to dst and
// returns it. Row changes, carriage returns, mid-row codes (shown as a
// space), tabs, end of caption and erasure of non-displayed memory append a
// space. Erasing displayed memory does not: pop-on encoders send it while
// the next caption is loading, often in the middle of a word. A backspace
// removes the last rune of dst; an extended character replaces the last
// rune of dst.
func (d *TextDecoder) Feed(cc []byte, dst []byte) []byte {
	for i := 0; i+tripletLen <= len(cc); i += tripletLen {
		hdr := cc[i]
		if hdr&ccTypeMask != d.field || hdr&ccValidFlag == 0 {
			continue
		}
		b1, b2 := cc[i+1], cc[i+2]
		if bits.OnesCount8(b1)&1 == 0 || bits.OnesCount8(b2)&1 == 0 {
			d.repeat = false
			continue
		}
		b1, b2 = b1&parityMask, b2&parityMask
		switch {
		case b1 == 0:
			continue
		case b1 >= ctrlFirst && b1 <= ctrlLast:
			if d.repeat && d.lastCtrl == [2]byte{b1, b2} {
				d.repeat = false
				continue
			}
			d.lastCtrl, d.repeat = [2]byte{b1, b2}, true
			d.inXDS = false
			d.curChan = 1 + (b1>>ctrlChanShift)&1
			if d.curChan == d.channel {
				dst = d.control(b1&^ctrlChanBit, b2, dst)
			}
		case b1 >= charFirst:
			d.repeat = false
			if (d.inXDS && d.field == fieldTwo) || d.curChan != d.channel {
				continue
			}
			dst = appendBasic(dst, b1)
			if b2 >= charFirst {
				dst = appendBasic(dst, b2)
			}
		default:
			d.repeat = false
			if d.field == fieldTwo {
				d.inXDS = b1 != xdsEnd
			}
		}
	}
	return dst
}

func appendBasic(dst []byte, b byte) []byte {
	if r, ok := basic608[b]; ok {
		return utf8.AppendRune(dst, r)
	}
	return append(dst, b)
}

// control handles a control pair of the selected channel (channel bit
// cleared).
func (d *TextDecoder) control(b1, b2 byte, dst []byte) []byte {
	switch {
	case b2 >= pacFirstSecond:
		return appendSpace(dst) // preamble address code: a new row
	case b1 == specialFirst && b2 >= specialLow && b2 <= extendedHigh:
		if b2-specialLow == transparentSpace-specialLow {
			return appendSpace(dst)
		}
		return utf8.AppendRune(dst, special608[b2-specialLow])
	case b1 == specialFirst && b2 >= midRowLow && b2 <= midRowHigh:
		return appendSpace(dst) // mid-row code: a style change, shown as a space
	case (b1 == extendedFirstA || b1 == extendedFirstB) && b2 >= extendedLow && b2 <= extendedHigh:
		set := &extendedA608
		if b1 == extendedFirstB {
			set = &extendedB608
		}
		return utf8.AppendRune(dropLastRune(dst), set[b2-extendedLow])
	case (b1 == miscCtrl1 || b1 == miscCtrl2) && b2 == cmdBS:
		return dropLastRune(dst)
	case (b1 == miscCtrl1 || b1 == miscCtrl2) && (b2 == cmdCR || b2 == cmdENM || b2 == cmdEOC || b2 == cmdRCL):
		return appendSpace(dst)
	case b1 == tabFirst:
		return appendSpace(dst)
	}
	return dst
}

func appendSpace(dst []byte) []byte {
	if n := len(dst); n > 0 && dst[n-1] == space {
		return dst
	}
	return append(dst, space)
}

func dropLastRune(dst []byte) []byte {
	if len(dst) == 0 {
		return dst
	}
	_, n := utf8.DecodeLastRune(dst)
	return dst[:len(dst)-n]
}
