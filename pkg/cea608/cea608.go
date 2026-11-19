// SPDX-License-Identifier: Apache-2.0

// Package cea608 decodes CEA-608 closed captions from cc_data triplets (the
// form carried in H.264/MPEG-2 user data and in MP4 c608 tracks). Decoder
// tracks the presentation style from caption mode commands and activity and
// decodes no text. TextDecoder, used only for opt-in phrase matching,
// decodes the characters; neither retains caption text.
package cea608

import "math/bits"

// Style is a caption presentation style.
type Style uint8

const (
	// StyleUnknown: no caption mode command has been observed yet, or the
	// channel is in text mode.
	StyleUnknown Style = iota
	StyleRollUp
	StylePopOn
	StylePaintOn
)

// String returns roll_up, pop_on, paint_on or unknown.
func (s Style) String() string {
	switch s {
	case StyleRollUp:
		return NameRollUp
	case StylePopOn:
		return NamePopOn
	case StylePaintOn:
		return NamePaintOn
	}
	return NameUnknown
}

// cc_data triplet header bits and CEA-608 byte ranges.
const (
	ccTypeMask  = 0x03 // cc_type: 0 = field 1, 1 = field 2
	ccValidFlag = 0x04
	parityMask  = 0x7f // data bits of a byte; bit 7 is odd parity

	ctrlFirst     = 0x10 // first byte of a control pair
	ctrlLast      = 0x1f
	ctrlChanBit   = 0x08 // set on the first byte for channel 2
	ctrlChanShift = 3
	charFirst     = 0x20 // first printable character
	xdsEnd        = 0x0f // closes an XDS packet on field 2

	// Miscellaneous control codes: first byte miscCtrl1 (field 1) or
	// miscCtrl2 (field 2) once the channel bit is cleared, second byte in
	// miscFirst..miscLast.
	miscCtrl1 = 0x14
	miscCtrl2 = 0x15
	miscFirst = 0x20
	miscLast  = 0x2f

	cmdRCL = 0x20 // resume caption loading (pop-on)
	cmdRU2 = 0x25 // roll-up, 2 rows
	cmdRU3 = 0x26 // roll-up, 3 rows
	cmdRU4 = 0x27 // roll-up, 4 rows
	cmdRDC = 0x29 // resume direct captioning (paint-on)
	cmdTR  = 0x2a // text restart
	cmdRTD = 0x2b // resume text display
	cmdEOC = 0x2f // end of caption (pop-on)

	firstService     = 1
	lastService      = 4
	channelsPerField = 2

	tripletLen = 3
	fieldTwo   = 1 // cc_type of field 2

)

// Canonical style names returned by Style.String.
const (
	NameRollUp  = "roll_up"
	NamePopOn   = "pop_on"
	NamePaintOn = "paint_on"
	NameUnknown = "unknown"
)

// Decoder follows one caption service (CC1..CC4). The zero value is not
// usable; use NewDecoder. A Decoder is not safe for concurrent use.
type Decoder struct {
	field    uint8 // cc_type of the service's field: 0 = field 1, 1 = field 2
	channel  uint8 // 1 or 2
	curChan  uint8 // channel addressed by the last control code, 0 unknown
	style    Style
	text     bool // selected channel is in text mode
	inXDS    bool
	lastCtrl [2]byte
	repeat   bool // lastCtrl may be followed by its redundant copy
}

// NewDecoder returns a decoder for service 1..4 (CC1..CC4). CC1/CC2 are
// channels 1/2 of field 1; CC3/CC4 are channels 1/2 of field 2.
func NewDecoder(service int) *Decoder {
	if service < firstService || service > lastService {
		service = firstService
	}
	// CC1/CC2 are channels 1/2 of field 1; CC3/CC4 of field 2.
	n := service - firstService
	return &Decoder{field: uint8(n / channelsPerField), channel: uint8(n%channelsPerField + 1)}
}

// Reset forgets all state (use after a discontinuity).
func (d *Decoder) Reset() {
	*d = Decoder{field: d.field, channel: d.channel}
}

// Style returns the style established by the most recent mode command.
func (d *Decoder) Style() Style {
	if d.text {
		return StyleUnknown
	}
	return d.style
}

// Feed processes the cc_data triplets of one frame (3 bytes each: a header
// with cc_valid in bit 2 and cc_type in bits 0-1, then two data bytes).
//
// data reports that valid CEA-608 bytes for the service's field were
// present (including null padding), i.e. the service is observable.
// activity reports caption activity on the selected channel while a caption
// style is established. style is the current style.
func (d *Decoder) Feed(cc []byte) (data, activity bool, style Style) {
	for i := 0; i+tripletLen <= len(cc); i += tripletLen {
		hdr := cc[i]
		if hdr&ccTypeMask != d.field || hdr&ccValidFlag == 0 {
			continue
		}
		data = true
		b1, b2 := cc[i+1], cc[i+2]
		if bits.OnesCount8(b1)&1 == 0 || bits.OnesCount8(b2)&1 == 0 {
			d.repeat = false
			continue // parity error
		}
		b1, b2 = b1&parityMask, b2&parityMask
		switch {
		case b1 == 0:
			// Null padding (or an unpaired trailing byte): observable, idle.
			// The redundant-copy state deliberately survives padding: real
			// encoders re-send a control code after padding while nothing
			// is captioned, and counting each copy as activity would keep a
			// style alive through uncaptioned content.
			continue
		case b1 >= ctrlFirst && b1 <= ctrlLast:
			if d.repeat && d.lastCtrl == [2]byte{b1, b2} {
				d.repeat = false // redundant copy of the previous control code
				continue
			}
			d.lastCtrl, d.repeat = [2]byte{b1, b2}, true
			d.inXDS = false
			d.curChan = 1 + (b1>>ctrlChanShift)&1
			if d.curChan != d.channel {
				continue
			}
			if d.control(b1&^ctrlChanBit, b2) {
				activity = true
			}
		case b1 >= charFirst:
			d.repeat = false
			if d.inXDS && d.field == fieldTwo {
				continue
			}
			if d.curChan == d.channel && !d.text && d.style != StyleUnknown {
				activity = true
			}
		default: // 0x01..0x0f: XDS control on field 2
			d.repeat = false
			if d.field == fieldTwo {
				d.inXDS = b1 != xdsEnd
			}
		}
	}
	return data, activity, d.Style()
}

// control handles a control pair for the selected channel (channel bit
// already cleared from b1) and reports caption activity.
func (d *Decoder) control(b1, b2 byte) bool {
	if (b1 == miscCtrl1 || b1 == miscCtrl2) && b2 >= miscFirst && b2 <= miscLast {
		switch b2 {
		case cmdRCL, cmdEOC:
			d.style, d.text = StylePopOn, false
			return true
		case cmdRU2, cmdRU3, cmdRU4:
			d.style, d.text = StyleRollUp, false
			return true
		case cmdRDC:
			d.style, d.text = StylePaintOn, false
			return true
		case cmdTR, cmdRTD: // text mode is not a caption style
			d.text = true
			return false
		}
	}
	// Preamble address codes, mid-row codes, special/extended characters,
	// tab offsets and the remaining miscellaneous commands.
	return !d.text && d.style != StyleUnknown
}
