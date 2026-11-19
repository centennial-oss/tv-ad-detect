// SPDX-License-Identifier: Apache-2.0

package cea608

import (
	"math/bits"
	"testing"
)

func odd(b byte) byte {
	b &= 0x7f
	if bits.OnesCount8(b)%2 == 0 {
		b |= 0x80
	}
	return b
}

// pairs builds field-1 triplets.
func pairs(ps ...[2]byte) []byte {
	var out []byte
	for _, p := range ps {
		out = append(out, 0xfc, odd(p[0]), odd(p[1]))
	}
	return out
}

func text(s string) [][2]byte {
	var out [][2]byte
	for i := 0; i < len(s); i += 2 {
		p := [2]byte{s[i], 0}
		if i+1 < len(s) {
			p[1] = s[i+1]
		}
		out = append(out, p)
	}
	return out
}

func TestTextDecoder(t *testing.T) {
	d := NewTextDecoder(1)
	var got []byte
	// Pop-on: RCL (sent twice), a PAC, text, a backspace, an extended
	// character, a special character, a carriage return, more text.
	cc := pairs([2]byte{0x14, 0x20}, [2]byte{0x14, 0x20}, [2]byte{0x14, 0x70})
	cc = append(cc, pairs(text("ASK YOUR DOCTOX")...)...)
	cc = append(cc, pairs([2]byte{0x14, 0x21})...) // backspace drops the X
	cc = append(cc, pairs(text("R")...)...)
	cc = append(cc, pairs([2]byte{0x14, 0x2d})...) // carriage return
	cc = append(cc, pairs(text("CAF")...)...)
	cc = append(cc, pairs([2]byte{0x12, 0x21})...) // É replaces the last character (F)
	cc = append(cc, pairs([2]byte{0x11, 0x37})...) // ♪
	got = d.Feed(cc, got)
	if want := " ASK YOUR DOCTOR CAÉ♪"; string(got) != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	// Channel 2 and field 2 data are ignored for CC1.
	other := pairs([2]byte{0x1c, 0x20}, [2]byte{0x1c, 0x70})
	other = append(other, pairs(text("NOT CC1")...)...)
	if more := d.Feed(other, nil); len(more) != 0 {
		t.Errorf("CC2 text leaked into CC1: %q", more)
	}
	f2 := []byte{0xfd, odd('H'), odd('I')}
	if more := d.Feed(f2, nil); len(more) != 0 {
		t.Errorf("field-2 text leaked into CC1: %q", more)
	}
	// Basic-set substitutions.
	d.Reset()
	if got := d.Feed(pairs([2]byte{0x14, 0x2c}, [2]byte{0x14, 0x70}, [2]byte{0x5c, 0x7e}), nil); string(got) != " éñ" {
		t.Errorf("basic substitutions = %q", got)
	}
	// Pop-on loading continues through an erasure of the displayed caption,
	// even mid-word; a mid-row code is a space.
	d.Reset()
	cc = pairs([2]byte{0x14, 0x20}, [2]byte{0x14, 0x70})
	cc = append(cc, pairs(text("ME")...)...)
	cc = append(cc, pairs([2]byte{0x14, 0x2c})...) // erase displayed memory
	cc = append(cc, pairs(text("DICARE")...)...)
	cc = append(cc, pairs([2]byte{0x11, 0x2e})...) // mid-row: italics
	cc = append(cc, pairs(text("NOW")...)...)
	if got := d.Feed(cc, nil); string(got) != " MEDICARE NOW" {
		t.Errorf("erasure while loading = %q", got)
	}
}
