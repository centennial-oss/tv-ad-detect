// SPDX-License-Identifier: Apache-2.0

package ffadapter

import (
	"encoding/binary"
	"testing"
)

// PACK-08 / AT-27 (unit): the top-level ISO BMFF walk that detects a
// truncated CMAF resource when the manifest declares no length for it.
func TestBoxWalkDetectsTruncation(t *testing.T) {
	box := func(typ string, payload int) []byte {
		b := make([]byte, 8+payload)
		binary.BigEndian.PutUint32(b, uint32(len(b)))
		copy(b[4:], typ)
		for i := 8; i < len(b); i++ {
			b[i] = byte(i) // payload that never looks like a header by accident
		}
		return b
	}
	large := func(typ string, payload int) []byte {
		b := make([]byte, 16+payload)
		binary.BigEndian.PutUint32(b, 1)
		copy(b[4:], typ)
		binary.BigEndian.PutUint64(b[8:], uint64(len(b)))
		return b
	}
	toEnd := func(typ string, payload int) []byte {
		b := box(typ, payload)
		binary.BigEndian.PutUint32(b, 0)
		return b
	}
	cat := func(parts ...[]byte) []byte {
		var out []byte
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	initSeg := cat(box("ftyp", 16), box("moov", 700))
	media := cat(box("styp", 12), box("moof", 120), box("mdat", 5000), box("moof", 96), box("mdat", 300))
	tsPacket := append([]byte{0x47, 0x40, 0x00, 0x10, 0x00, 0x00, 0xb0, 0x0d}, make([]byte, 180)...)

	cases := []struct {
		name      string
		data      []byte
		media     bool
		truncated bool
	}{
		{"whole initialization segment", initSeg, false, false},
		{"whole media segment", media, true, false},
		{"empty resource", nil, true, true},
		{"cut inside mdat", media[:len(media)-100], true, true},
		{"cut inside a box header", media[:20+128+3], true, true},
		{"cut inside moov", initSeg[:200], false, true},
		{"cut between moof and mdat", media[:20+128], true, true},
		{"initialization data may end with any box", cat(box("ftyp", 16), box("moof", 40)), false, false},
		{"64-bit box size", cat(box("styp", 12), box("moof", 40), large("mdat", 900)), true, false},
		{"64-bit box size, cut", cat(box("styp", 12), box("moof", 40), large("mdat", 900))[:400], true, true},
		{"cut inside a 64-bit size field", cat(box("moof", 40), large("mdat", 900))[:48+12], true, true},
		{"size 0 extends to the end", cat(box("moof", 40), toEnd("mdat", 77)), true, false},
		{"impossible size", cat(box("moof", 40), []byte{0, 0, 0, 4, 'm', 'd', 'a', 't'}), true, true},
		{"unknown later box types are walked by size", cat(box("styp", 12), box("abcd", 30), box("moof", 40), box("mdat", 10)), true, false},
		{"MPEG-TS is not checked", cat(tsPacket, tsPacket, tsPacket[:100]), true, false},
		{"arbitrary bytes are not checked", []byte("#EXTM3U\n#EXT-X-VERSION:7\n"), true, false},
	}
	for _, tc := range cases {
		for _, chunk := range []int{1, 7, 4096} {
			var w boxWalk
			w.reset()
			for i := 0; i < len(tc.data); i += chunk {
				w.write(tc.data[i:min(i+chunk, len(tc.data))])
			}
			if got := w.truncated(tc.media); got != tc.truncated {
				t.Errorf("%s (fed %d bytes at a time): truncated = %v, want %v", tc.name, chunk, got, tc.truncated)
			}
		}
	}
}
