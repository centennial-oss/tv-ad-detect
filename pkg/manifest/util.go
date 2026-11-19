// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"math"
	"math/bits"
	"strings"
)

const (
	usPerSecond int64 = 1_000_000
	usPerMinute       = 60 * usPerSecond
	usPerHour         = 60 * usPerMinute
	usPerDay          = 24 * usPerHour
)

// parseUint parses a non-negative decimal integer that fits in an int64.
func parseUint(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i] - '0'
		if c > 9 {
			return 0, false
		}
		if n > (math.MaxInt64-int64(c))/decimal {
			return 0, false
		}
		n = n*decimal + int64(c)
	}
	return n, true
}

// parseInt parses an optionally signed decimal integer.
func parseInt(s string) (int64, bool) {
	if s != "" && (s[0] == '-' || s[0] == '+') {
		n, ok := parseUint(s[1:])
		if s[0] == '-' {
			n = -n
		}
		return n, ok
	}
	return parseUint(s)
}

// parseDecimalMicros converts a non-negative decimal number of seconds
// ("9", "9.009", ".5") into integer microseconds without floating point.
// Digits beyond the sixth fractional digit are rounded half up.
func parseDecimalMicros(s string) (int64, bool) {
	whole, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		whole, frac = s[:i], s[i+1:]
	}
	if whole == "" && frac == "" {
		return 0, false
	}
	var sec int64
	if whole != "" {
		var ok bool
		if sec, ok = parseUint(whole); !ok || sec > math.MaxInt64/usPerSecond-1 {
			return 0, false
		}
	}
	var us int64
	roundUp := false
	for i := 0; i < len(frac); i++ {
		c := frac[i] - '0'
		if c > 9 {
			return 0, false
		}
		if i < 6 {
			us = us*decimal + int64(c)
		} else if i == 6 {
			roundUp = c >= 5
		}
	}
	for i := len(frac); i < 6; i++ {
		us *= decimal
	}
	if roundUp {
		us++
	}
	return sec*usPerSecond + us, true
}

// parseISODuration parses an ISO-8601 / xs:duration value such as
// "PT1H2M3.5S" or "P1DT12H" exactly into microseconds. Calendar units use the
// conventional fixed lengths (Y = 365 days, M = 30 days, W = 7 days). A
// leading '-' yields a negative result.
func parseISODuration(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	neg := false
	if strings.HasPrefix(s, minusSign) {
		neg, s = true, s[1:]
	}
	if len(s) < 2 || s[0] != 'P' {
		return 0, false
	}
	s = s[1:]
	var total int64
	inTime := false
	for len(s) > 0 {
		if s[0] == 'T' {
			if inTime || len(s) == 1 {
				return 0, false
			}
			inTime, s = true, s[1:]
			continue
		}
		i := 0
		for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
			i++
		}
		if i == 0 || i == len(s) {
			return 0, false
		}
		num, unit := s[:i], s[i]
		s = s[i+1:]
		var add int64
		if inTime && unit == 'S' {
			v, ok := parseDecimalMicros(num)
			if !ok {
				return 0, false
			}
			add = v
		} else {
			var mult int64
			switch {
			case !inTime && unit == 'Y':
				mult = 365 * usPerDay
			case !inTime && unit == 'M':
				mult = 30 * usPerDay
			case !inTime && unit == 'W':
				mult = 7 * usPerDay
			case !inTime && unit == 'D':
				mult = usPerDay
			case inTime && unit == 'H':
				mult = usPerHour
			case inTime && unit == 'M':
				mult = usPerMinute
			default:
				return 0, false
			}
			v, ok := parseUint(num)
			if !ok || v > math.MaxInt64/mult {
				return 0, false
			}
			add = v * mult
		}
		if add > math.MaxInt64-total {
			return 0, false
		}
		total += add
	}
	if neg {
		total = -total
	}
	return total, true
}

// mulDiv returns a*b/c computed with a 128-bit intermediate. b and c must be
// positive. The quotient is rounded toward negative infinity, or toward
// positive infinity when ceil is set. ok is false on overflow.
func mulDiv(a, b, c int64, ceil bool) (int64, bool) {
	if b <= 0 || c <= 0 {
		return 0, false
	}
	neg := a < 0
	ua := uint64(a)
	if neg {
		ua = -ua
	}
	hi, lo := bits.Mul64(ua, uint64(b))
	if hi >= uint64(c) {
		return 0, false
	}
	q, r := bits.Div64(hi, lo, uint64(c))
	if r != 0 && neg != ceil {
		// floor of a negative value and ceil of a positive value both
		// move away from zero.
		q++
	}
	if q > math.MaxInt64 {
		return 0, false
	}
	if neg {
		return -int64(q), true
	}
	return int64(q), true
}

// trimSpace trims ASCII blanks; it avoids the Unicode handling of
// strings.TrimSpace on hot paths.
func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\t' && c != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// truncate bounds diagnostic text.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
