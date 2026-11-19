// SPDX-License-Identifier: Apache-2.0

package cea608

import (
	"math/bits"
	"math/rand"
	"testing"
)

// par returns b with CEA-608 odd parity in bit 7.
func par(b byte) byte {
	b &= 0x7f
	if bits.OnesCount8(b)%2 == 0 {
		b |= 0x80
	}
	return b
}

// f1 and f2 build one valid cc_data triplet for field 1 / field 2.
func f1(b1, b2 byte) []byte { return []byte{0xFC, par(b1), par(b2)} }
func f2(b1, b2 byte) []byte { return []byte{0xFD, par(b1), par(b2)} }

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

var (
	ru2CC1  = f1(0x14, 0x25)
	rclCC1  = f1(0x14, 0x20)
	eocCC1  = f1(0x14, 0x2f)
	rdcCC1  = f1(0x14, 0x29)
	trCC1   = f1(0x14, 0x2a)
	rtdCC1  = f1(0x14, 0x2b)
	textAB  = f1('A', 'B')
	nullPad = []byte{0xFC, 0x80, 0x80}
)

func feed(t *testing.T, d *Decoder, cc []byte, wantData, wantAct bool, wantStyle Style) {
	t.Helper()
	data, act, st := d.Feed(cc)
	if data != wantData || act != wantAct || st != wantStyle {
		t.Fatalf("Feed(% x) = data %v activity %v style %v; want %v %v %v", cc, data, act, st, wantData, wantAct, wantStyle)
	}
}

// AT-07 / CAP-01: the three presentation styles are recognized from their
// mode commands; before any command the style is unknown.
func TestStyles(t *testing.T) {
	cases := []struct {
		name string
		b2   byte
		want Style
	}{
		{"RCL", 0x20, StylePopOn},
		{"EOC", 0x2f, StylePopOn},
		{"RU2", 0x25, StyleRollUp},
		{"RU3", 0x26, StyleRollUp},
		{"RU4", 0x27, StyleRollUp},
		{"RDC", 0x29, StylePaintOn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := NewDecoder(1)
			if d.Style() != StyleUnknown {
				t.Fatalf("initial style = %v, want unknown", d.Style())
			}
			feed(t, d, f1(0x14, c.b2), true, true, c.want)
			if d.Style() != c.want {
				t.Fatalf("Style() = %v, want %v", d.Style(), c.want)
			}
			// Characters after the mode command are caption activity.
			feed(t, d, textAB, true, true, c.want)
		})
	}
}

func TestStyleString(t *testing.T) {
	for s, want := range map[Style]string{
		StyleUnknown: "unknown", StyleRollUp: "roll_up", StylePopOn: "pop_on", StylePaintOn: "paint_on", Style(99): "unknown",
	} {
		if got := s.String(); got != want {
			t.Errorf("Style(%d).String() = %q, want %q", s, got, want)
		}
	}
}

// AT-07: characters, PACs and mid-row codes are not caption activity until a
// caption mode has been established (insufficient observation is unknown).
func TestNoActivityBeforeMode(t *testing.T) {
	d := NewDecoder(1)
	feed(t, d, textAB, true, false, StyleUnknown)
	feed(t, d, f1(0x11, 0x40), true, false, StyleUnknown) // PAC row 1
	feed(t, d, f1(0x11, 0x20), true, false, StyleUnknown) // mid-row
	feed(t, d, ru2CC1, true, true, StyleRollUp)
	feed(t, d, f1(0x11, 0x40), true, true, StyleRollUp)
	feed(t, d, f1(0x11, 0x20), true, true, StyleRollUp)
	feed(t, d, f1(0x14, 0x2d), true, true, StyleRollUp) // CR
}

// AT-07: null padding makes the service observable without activity, which
// is what lets the tracker report "none" instead of "unknown".
func TestNullPaddingIsObservableIdle(t *testing.T) {
	d := NewDecoder(1)
	feed(t, d, nullPad, true, false, StyleUnknown)
	feed(t, d, ru2CC1, true, true, StyleRollUp)
	feed(t, d, nullPad, true, false, StyleRollUp)
}

// Control codes are transmitted twice; the redundant copy must be ignored,
// but only when it immediately follows the original.
func TestControlCodeDoubling(t *testing.T) {
	t.Run("across feeds", func(t *testing.T) {
		d := NewDecoder(1)
		feed(t, d, ru2CC1, true, true, StyleRollUp)
		feed(t, d, ru2CC1, true, false, StyleRollUp) // redundant copy
		feed(t, d, ru2CC1, true, true, StyleRollUp)  // a third one is a new command
	})
	t.Run("within one feed", func(t *testing.T) {
		d := NewDecoder(1)
		feed(t, d, cat(rclCC1, rclCC1), true, true, StylePopOn)
	})
	t.Run("doubled mode change is applied once and not undone", func(t *testing.T) {
		d := NewDecoder(1)
		feed(t, d, cat(ru2CC1, ru2CC1), true, true, StyleRollUp)
		feed(t, d, cat(rdcCC1, rdcCC1), true, true, StylePaintOn)
	})
	t.Run("intervening characters end the pairing", func(t *testing.T) {
		d := NewDecoder(1)
		feed(t, d, ru2CC1, true, true, StyleRollUp)
		feed(t, d, textAB, true, true, StyleRollUp)
		feed(t, d, ru2CC1, true, true, StyleRollUp)
	})
	t.Run("a parity error ends the pairing", func(t *testing.T) {
		d := NewDecoder(1)
		feed(t, d, ru2CC1, true, true, StyleRollUp)
		feed(t, d, []byte{0xFC, 0x14, 0x25 | 0x80}, true, false, StyleRollUp) // both bytes even parity
		feed(t, d, ru2CC1, true, true, StyleRollUp)
	})
	t.Run("different control code is not a copy", func(t *testing.T) {
		d := NewDecoder(1)
		feed(t, d, ru2CC1, true, true, StyleRollUp)
		feed(t, d, rclCC1, true, true, StylePopOn)
	})
}

// CAP-03: service changes do not silently redefine a style. CC1 and CC2
// share field 1 and are separated by the channel bit of control codes.
func TestChannelSeparation(t *testing.T) {
	ru2CC2 := f1(0x1c, 0x25)
	rclCC2 := f1(0x1c, 0x20)

	t.Run("CC1 decoder ignores CC2 commands", func(t *testing.T) {
		d := NewDecoder(1)
		feed(t, d, ru2CC2, true, false, StyleUnknown)
		feed(t, d, textAB, true, false, StyleUnknown)
	})
	t.Run("CC2 decoder follows CC2", func(t *testing.T) {
		d := NewDecoder(2)
		feed(t, d, ru2CC1, true, false, StyleUnknown)
		feed(t, d, rclCC2, true, true, StylePopOn)
		feed(t, d, textAB, true, true, StylePopOn)
	})
	t.Run("characters belong to the channel of the last control code", func(t *testing.T) {
		d := NewDecoder(1)
		feed(t, d, ru2CC1, true, true, StyleRollUp)
		feed(t, d, textAB, true, true, StyleRollUp)
		// CC2 takes over the field: its characters are not CC1 activity and
		// CC1 keeps its own style.
		feed(t, d, rclCC2, true, false, StyleRollUp)
		feed(t, d, textAB, true, false, StyleRollUp)
		// A CC1 PAC switches back.
		feed(t, d, f1(0x11, 0x40), true, true, StyleRollUp)
		feed(t, d, textAB, true, true, StyleRollUp)
	})
	t.Run("interleaved services keep independent styles", func(t *testing.T) {
		d1, d2 := NewDecoder(1), NewDecoder(2)
		stream := cat(ru2CC1, f1('h', 'i'), rclCC2, f1('y', 'o'))
		_, _, s1 := d1.Feed(stream)
		_, _, s2 := d2.Feed(stream)
		if s1 != StyleRollUp || s2 != StylePopOn {
			t.Fatalf("CC1 %v CC2 %v; want roll_up pop_on", s1, s2)
		}
	})
}

// Field 2 carries CC3/CC4 and XDS; a field-1 service never sees it.
func TestFieldSeparationAndXDS(t *testing.T) {
	t.Run("CC1 ignores field 2", func(t *testing.T) {
		d := NewDecoder(1)
		feed(t, d, f2(0x15, 0x25), false, false, StyleUnknown)
		feed(t, d, f2(0x14, 0x25), false, false, StyleUnknown)
		feed(t, d, cat(f2(0x15, 0x20), ru2CC1), true, true, StyleRollUp)
	})
	t.Run("CC3 ignores field 1", func(t *testing.T) {
		d := NewDecoder(3)
		feed(t, d, ru2CC1, false, false, StyleUnknown)
	})
	t.Run("CC3 uses the field-2 command codes", func(t *testing.T) {
		d := NewDecoder(3)
		feed(t, d, f2(0x15, 0x25), true, true, StyleRollUp)
		feed(t, d, f2('A', 'B'), true, true, StyleRollUp)
		d = NewDecoder(3)
		feed(t, d, f2(0x14, 0x29), true, true, StylePaintOn) // field-1 form is tolerated
	})
	t.Run("CC4 is channel 2 of field 2", func(t *testing.T) {
		d := NewDecoder(4)
		feed(t, d, f2(0x15, 0x25), true, false, StyleUnknown) // CC3
		feed(t, d, f2(0x1d, 0x20), true, true, StylePopOn)
	})
	t.Run("XDS packets are not caption activity", func(t *testing.T) {
		d := NewDecoder(3)
		feed(t, d, f2(0x15, 0x25), true, true, StyleRollUp)
		feed(t, d, f2('A', 'B'), true, true, StyleRollUp)
		// XDS start (class current, type program name) followed by payload.
		feed(t, d, f2(0x01, 0x03), true, false, StyleRollUp)
		feed(t, d, f2('N', 'E'), true, false, StyleRollUp)
		feed(t, d, f2('W', 'S'), true, false, StyleRollUp)
		// XDS end + checksum, then captions resume.
		feed(t, d, f2(0x0f, 0x1d), true, false, StyleRollUp)
		feed(t, d, f2('C', 'D'), true, true, StyleRollUp)
	})
	t.Run("a caption control code interrupts XDS", func(t *testing.T) {
		d := NewDecoder(3)
		feed(t, d, f2(0x15, 0x20), true, true, StylePopOn)
		feed(t, d, f2(0x01, 0x03), true, false, StylePopOn)
		feed(t, d, f2(0x11, 0x40), true, true, StylePopOn) // PAC
		feed(t, d, f2('A', 'B'), true, true, StylePopOn)
	})
	t.Run("field 1 has no XDS: 0x01-0x0f is ignored", func(t *testing.T) {
		d := NewDecoder(1)
		feed(t, d, ru2CC1, true, true, StyleRollUp)
		feed(t, d, f1(0x01, 0x03), true, false, StyleRollUp)
		feed(t, d, textAB, true, true, StyleRollUp)
	})
}

// CAP-01: text mode is not a caption presentation style.
func TestTextMode(t *testing.T) {
	for name, cmd := range map[string][]byte{"TR": trCC1, "RTD": rtdCC1} {
		t.Run(name, func(t *testing.T) {
			d := NewDecoder(1)
			feed(t, d, ru2CC1, true, true, StyleRollUp)
			feed(t, d, cmd, true, false, StyleUnknown)
			feed(t, d, textAB, true, false, StyleUnknown)
			feed(t, d, f1(0x11, 0x40), true, false, StyleUnknown)
			if d.Style() != StyleUnknown {
				t.Fatalf("Style() in text mode = %v", d.Style())
			}
			// A caption mode command leaves text mode.
			feed(t, d, rclCC1, true, true, StylePopOn)
			feed(t, d, textAB, true, true, StylePopOn)
		})
	}
	t.Run("text mode before any caption mode", func(t *testing.T) {
		d := NewDecoder(1)
		feed(t, d, trCC1, true, false, StyleUnknown)
		feed(t, d, textAB, true, false, StyleUnknown)
		feed(t, d, rdcCC1, true, true, StylePaintOn)
	})
}

// Bytes failing the odd-parity check are dropped without changing state.
func TestParityErrors(t *testing.T) {
	even := func(b byte) byte { return par(b) ^ 0x80 }
	d := NewDecoder(1)
	// The service is still observable (valid field-1 triplet) but the
	// command must not be applied.
	feed(t, d, []byte{0xFC, even(0x14), par(0x25)}, true, false, StyleUnknown)
	feed(t, d, []byte{0xFC, par(0x14), even(0x25)}, true, false, StyleUnknown)
	feed(t, d, ru2CC1, true, true, StyleRollUp)
	// A corrupted mode change keeps the old style; corrupted text is idle.
	feed(t, d, []byte{0xFC, even(0x14), par(0x20)}, true, false, StyleRollUp)
	feed(t, d, []byte{0xFC, even('A'), par('B')}, true, false, StyleRollUp)
	feed(t, d, textAB, true, true, StyleRollUp)
}

// Only valid CEA-608 triplets of the service's field count as data.
func TestHeaderFiltering(t *testing.T) {
	d := NewDecoder(1)
	feed(t, d, []byte{0xF8, par(0x14), par(0x25)}, false, false, StyleUnknown) // cc_valid = 0
	feed(t, d, []byte{0xFE, par(0x14), par(0x25)}, false, false, StyleUnknown) // DTVCC packet data
	feed(t, d, []byte{0xFF, par(0x14), par(0x25)}, false, false, StyleUnknown) // DTVCC packet start
	feed(t, d, nil, false, false, StyleUnknown)
	feed(t, d, []byte{}, false, false, StyleUnknown)
	// Marker bits other than cc_valid/cc_type are irrelevant.
	feed(t, d, []byte{0x04, par(0x14), par(0x25)}, true, true, StyleRollUp)
}

// Truncated input never panics; a trailing partial triplet is ignored.
func TestTruncatedInput(t *testing.T) {
	d := NewDecoder(1)
	feed(t, d, []byte{0xFC}, false, false, StyleUnknown)
	feed(t, d, []byte{0xFC, par(0x14)}, false, false, StyleUnknown)
	feed(t, d, append(append([]byte{}, ru2CC1...), 0xFC, par(0x14)), true, true, StyleRollUp)
}

func TestNewDecoderRangeAndReset(t *testing.T) {
	for _, svc := range []int{0, -1, 5, 100} {
		d := NewDecoder(svc)
		// Out-of-range services fall back to CC1.
		feed(t, d, ru2CC1, true, true, StyleRollUp)
	}
	d := NewDecoder(2)
	feed(t, d, f1(0x1c, 0x25), true, true, StyleRollUp)
	d.Reset()
	if d.Style() != StyleUnknown {
		t.Fatalf("style after Reset = %v", d.Style())
	}
	// Reset keeps the selected service and forgets the doubling state.
	feed(t, d, ru2CC1, true, false, StyleUnknown)
	feed(t, d, f1(0x1c, 0x25), true, true, StyleRollUp)
	d.Reset()
	feed(t, d, f1(0x1c, 0x25), true, true, StyleRollUp)
}

// CAP-03 / QUAL-04: the decoder holds a fixed amount of state and retains no
// caption text, so feeding never allocates.
func TestFeedDoesNotAllocate(t *testing.T) {
	d := NewDecoder(1)
	cc := cat(ru2CC1, ru2CC1, f1(0x11, 0x40), textAB, textAB, nullPad)
	if n := testing.AllocsPerRun(200, func() { d.Feed(cc) }); n != 0 {
		t.Fatalf("Feed allocates %v times per call", n)
	}
}

// Arbitrary bytes never panic and never produce an out-of-range style.
func TestRandomInputIsSafe(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for svc := 1; svc <= 4; svc++ {
		d := NewDecoder(svc)
		buf := make([]byte, 0, 64)
		for i := 0; i < 20000; i++ {
			buf = buf[:rng.Intn(cap(buf))]
			for k := range buf {
				buf[k] = byte(rng.Intn(256))
			}
			_, act, st := d.Feed(buf)
			if st > StylePaintOn {
				t.Fatalf("style %d out of range", st)
			}
			if act && st == StyleUnknown {
				t.Fatalf("activity reported without an established style")
			}
		}
	}
}

func BenchmarkFeed(b *testing.B) {
	d := NewDecoder(1)
	cc := cat(ru2CC1, ru2CC1, f1(0x11, 0x40), textAB, textAB, nullPad)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		d.Feed(cc)
	}
}

// A control pair, null padding, then the identical control pair again: the
// second pair is still treated as the redundant copy of the first (no
// activity, no state change). This is deliberate. Real encoders re-send a
// control code after padding during uncaptioned content (for example a
// periodic erase or mode command on an otherwise idle service); counting the
// re-sent copy as fresh caption activity made idle stretches of a real
// recording look captioned. Null padding therefore does not end the pairing;
// only different data on the field (characters, another control code, a
// parity error) does.
func TestRepeatedControlPairAcrossNullPadding(t *testing.T) {
	edm := f1(0x14, 0x2c) // erase displayed memory

	d := NewDecoder(1)
	feed(t, d, ru2CC1, true, true, StyleRollUp)
	feed(t, d, nullPad, true, false, StyleRollUp)
	feed(t, d, ru2CC1, true, false, StyleRollUp) // redundant copy after padding
	// The pairing is consumed by that copy: a third identical pair is a new
	// command.
	feed(t, d, ru2CC1, true, true, StyleRollUp)

	// Several frames of padding in between make no difference, in one Feed
	// call or across calls.
	d = NewDecoder(1)
	feed(t, d, cat(rclCC1, nullPad, nullPad, nullPad, rclCC1), true, true, StylePopOn)
	feed(t, d, nullPad, true, false, StylePopOn)
	feed(t, d, rclCC1, true, true, StylePopOn) // the pairing above was already consumed

	// The idle-service pattern: a command re-sent after padding, again and
	// again, alternates original/copy and never reports more activity than
	// one command per pair.
	d = NewDecoder(1)
	feed(t, d, ru2CC1, true, true, StyleRollUp)
	activity := 0
	for i := 0; i < 20; i++ {
		feed(t, d, nullPad, true, false, StyleRollUp)
		if _, act, _ := d.Feed(edm); act {
			activity++
		}
	}
	if activity != 10 {
		t.Errorf("20 re-sent control pairs separated by padding reported activity %d times, want 10", activity)
	}

	// Anything other than padding ends the pairing.
	d = NewDecoder(1)
	feed(t, d, ru2CC1, true, true, StyleRollUp)
	feed(t, d, nullPad, true, false, StyleRollUp)
	feed(t, d, textAB, true, true, StyleRollUp)
	feed(t, d, ru2CC1, true, true, StyleRollUp)

	// Padding on the other field is invisible to the service either way.
	d = NewDecoder(1)
	feed(t, d, ru2CC1, true, true, StyleRollUp)
	feed(t, d, []byte{0xFD, 0x80, 0x80}, false, false, StyleRollUp)
	feed(t, d, ru2CC1, true, false, StyleRollUp)
}

// The canonical style names are exported for callers.
func TestStyleNameConstants(t *testing.T) {
	for s, want := range map[Style]string{StyleRollUp: NameRollUp, StylePopOn: NamePopOn, StylePaintOn: NamePaintOn, StyleUnknown: NameUnknown} {
		if s.String() != want {
			t.Errorf("Style(%d).String() = %q, want %q", s, s.String(), want)
		}
	}
	if NameRollUp != "roll_up" || NamePopOn != "pop_on" || NamePaintOn != "paint_on" || NameUnknown != "unknown" {
		t.Errorf("style names changed: %q %q %q %q", NameRollUp, NamePopOn, NamePaintOn, NameUnknown)
	}
}
