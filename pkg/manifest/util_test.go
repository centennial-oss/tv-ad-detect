// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"errors"
	"math"
	"path/filepath"
	"testing"
)

func TestParseDecimalMicros(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"6", 6_000_000, true},
		{"6.006", 6_006_000, true},
		{"9.009000", 9_009_000, true},
		{"0.000001", 1, true},
		{".5", 500_000, true},
		{"5.", 5_000_000, true},
		{"1.0000004", 1_000_000, true},
		{"1.0000005", 1_000_001, true},
		{"1.99999999", 2_000_000, true},
		{"3.3333333333333", 3_333_333, true},
		{"86400.123456", 86_400_123_456, true},
		{"", 0, false},
		{".", 0, false},
		{"-1", 0, false},
		{"1e3", 0, false},
		{"1.2.3", 0, false},
		{"abc", 0, false},
		{"99999999999999999999", 0, false},
		{"9223372036854", 0, false},
	} {
		got, ok := parseDecimalMicros(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseDecimalMicros(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseISODuration(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"PT0S", 0, true},
		{"PT1H2M3.5S", (3600+120+3)*1_000_000 + 500_000, true},
		{"PT30S", 30_000_000, true},
		{"PT0.000001S", 1, true},
		{"PT634.566S", 634_566_000, true},
		{"PT1M", 60_000_000, true},
		{"PT90M", 5_400_000_000, true},
		{"P1D", 86_400_000_000, true},
		{"P1DT12H", 129_600_000_000, true},
		{"P1W", 7 * 86_400_000_000, true},
		{"P1M", 30 * 86_400_000_000, true},
		{"P1Y", 365 * 86_400_000_000, true},
		{"P0Y0M0DT0H3M30.000S", 210_000_000, true},
		{" PT5S ", 5_000_000, true},
		{"-PT5S", -5_000_000, true},
		{"PT1.5H", 0, false},
		{"P", 0, false},
		{"PT", 0, false},
		{"P1DT", 0, false},
		{"PT5", 0, false},
		{"T5S", 0, false},
		{"PT5X", 0, false},
		{"P1H", 0, false},
		{"PT1D", 0, false},
		{"PTT1S", 0, false},
		{"5S", 0, false},
		{"", 0, false},
		{"PT99999999999999999999S", 0, false},
		{"P999999999999Y", 0, false},
	} {
		got, ok := parseISODuration(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseISODuration(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseInt(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true}, {"42", 42, true}, {"-1", -1, true}, {"+7", 7, true},
		{"9223372036854775807", math.MaxInt64, true},
		{"9223372036854775808", 0, false}, {"", 0, false}, {"-", 0, false}, {"1 ", 0, false}, {"0x10", 0, false},
	} {
		got, ok := parseInt(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseInt(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestMulDiv(t *testing.T) {
	for _, tc := range []struct {
		a, b, c int64
		ceil    bool
		want    int64
		ok      bool
	}{
		{10, 3, 4, false, 7, true},
		{10, 3, 4, true, 8, true},
		{-10, 3, 4, false, -8, true},
		{-10, 3, 4, true, -7, true},
		{12, 3, 4, true, 9, true},
		{-12, 3, 4, false, -9, true},
		{0, 5, 7, true, 0, true},
		// 128-bit intermediate: 24 h in nanoseconds scaled to microseconds.
		{86_400_000_000_000, 1_000_000, 1_000_000_000, false, 86_400_000_000, true},
		{math.MaxInt64, math.MaxInt64, math.MaxInt64, false, math.MaxInt64, true},
		{math.MaxInt64, 2, 1, false, 0, false},
		{1, 0, 1, false, 0, false},
		{1, 1, 0, false, 0, false},
	} {
		got, ok := mulDiv(tc.a, tc.b, tc.c, tc.ceil)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("mulDiv(%d, %d, %d, %v) = %d, %v; want %d, %v", tc.a, tc.b, tc.c, tc.ceil, got, ok, tc.want, tc.ok)
		}
	}
}

func TestURIScheme(t *testing.T) {
	for in, want := range map[string]string{
		"http://a/b":      "http",
		"HTTPS://a/b":     "HTTPS",
		"file:///a/b":     "file",
		"data:text/plain": "data",
		"s3+x.y-z://b":    "s3+x.y-z",
		"/abs/path":       "",
		"rel/path.m4s":    "",
		"C:/windows/path": "",
		`C:\windows\path`: "",
		"1a://x":          "",
		"a b://x":         "",
		"dir/with:colon":  "",
		"":                "",
		"noscheme":        "",
	} {
		if got := uriScheme(in); got != want {
			t.Errorf("uriScheme(%q) = %q, want %q", in, got, want)
		}
	}
	if IsRemote("file:///a") || IsRemote("/a/b") || IsRemote("a/b") || !IsRemote("http://h/a") || !IsRemote("s3://b/k") {
		t.Error("IsRemote misclassifies")
	}
}

func TestLocalPath(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		kind     error
	}{
		{"/a/b.m3u8", "/a/b.m3u8", nil},
		{"rel/b.m3u8", "rel/b.m3u8", nil},
		{"file:///a/b%20c.mpd", "/a/b c.mpd", nil},
		{"file://localhost/a/b.mpd", "/a/b.mpd", nil},
		{"FILE:///a/b.mpd", "/a/b.mpd", nil},
		{"file:/a/b.mpd", "/a/b.mpd", nil},
		{"file:rel/b%23.mpd", "rel/b#.mpd", nil},
		{"file://server/share/b.mpd", "", ErrUnsupported},
		{"https://h/a.mpd", "", ErrUnsupported},
		{"file://", "", ErrInvalid},
		{"file:///a/%zz", "", ErrInvalid},
	} {
		got, err := LocalPath(tc.in)
		if tc.kind != nil {
			if !errors.Is(err, tc.kind) {
				t.Errorf("LocalPath(%q) error = %v, want %v", tc.in, err, tc.kind)
			}
			continue
		}
		if err != nil || filepath.ToSlash(got) != tc.want {
			t.Errorf("LocalPath(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestResolve(t *testing.T) {
	for _, tc := range []struct {
		base, ref, want string
		remote          bool // result requires AllowRemote
	}{
		// Local bases: path joining relative to the declaring manifest.
		{"/a/b/master.m3u8", "seg.m4s", "/a/b/seg.m4s", false},
		{"/a/b/master.m3u8", "v/720/media.m3u8", "/a/b/v/720/media.m3u8", false},
		{"/a/b/master.m3u8", "../c/seg.m4s", "/a/c/seg.m4s", false},
		{"/a/b/master.m3u8", "./seg.m4s", "/a/b/seg.m4s", false},
		{"/a/b/master.m3u8", "x/./y/../seg.m4s", "/a/b/x/seg.m4s", false},
		{"/a/b/master.m3u8", "/abs/seg.m4s", "/abs/seg.m4s", false},
		{"/a/b/master.m3u8", "seg%20one%2B.m4s", "/a/b/seg one+.m4s", false},
		{"/a/b/master.m3u8", "seg.m4s?token=abc#t=1", "/a/b/seg.m4s", false},
		{"/a/b/master.m3u8", "a//b.m4s", "/a/b/a/b.m4s", false},
		{"/a/b/master.m3u8", ".hidden.m4s", "/a/b/.hidden.m4s", false},
		{"/a/b/master.m3u8", "file:///other/seg%201.m4s", "/other/seg 1.m4s", false},
		{"/a/b/master.m3u8", "", "/a/b/master.m3u8", false},
		{"/a/b/master.m3u8", "#frag", "/a/b/master.m3u8", false},
		{"/a/b/../c/master.m3u8", "seg.m4s", "/a/c/seg.m4s", false},
		{"master.m3u8", "seg.m4s", "seg.m4s", false},
		{"master.m3u8", "v/seg.m4s", "v/seg.m4s", false},
		{"master.m3u8", "../seg.m4s", "../seg.m4s", false},
		{"rel/dir/master.m3u8", "../seg.m4s", "rel/seg.m4s", false},
		{"./rel/master.m3u8", "seg.m4s", "rel/seg.m4s", false},
		{"/master.m3u8", "seg.m4s", "/seg.m4s", false},
		// Directory bases (DASH BaseURL): a trailing slash is significant.
		{"/a/b/manifest.mpd", "video/", "/a/b/video/", false},
		{"/a/b/video/", "seg.m4s", "/a/b/video/seg.m4s", false},
		{"/a/b/video/", "../audio/", "/a/b/audio/", false},
		{"/a/b/video/", "..", "/a/b/", false},
		{"/a/b/video", "seg.m4s", "/a/b/seg.m4s", false},
		// Absolute remote references and remote bases.
		{"/a/b/master.m3u8", "https://h.example/x/seg.m4s", "https://h.example/x/seg.m4s", true},
		{"https://h.example/x/y/master.m3u8", "seg.m4s", "https://h.example/x/y/seg.m4s", true},
		{"https://h.example/x/y/master.m3u8", "../z/seg%201.m4s?q=1", "https://h.example/x/z/seg%201.m4s?q=1", true},
		{"https://h.example/x/y/master.m3u8", "/root.m4s", "https://h.example/root.m4s", true},
		{"https://h.example/x/y/master.m3u8", "//cdn.example/seg.m4s", "https://cdn.example/seg.m4s", true},
		{"https://h.example/x/y/master.m3u8", "file:///local/seg.m4s", "/local/seg.m4s", false},
	} {
		for _, allow := range []bool{false, true} {
			l := &loader{opts: Options{AllowRemote: allow}}
			got, err := l.resolve(l.newBase(tc.base), tc.ref)
			if tc.remote && !allow {
				if !errors.Is(err, ErrUnsupported) {
					t.Errorf("resolve(%q, %q) without AllowRemote: %v, want ErrUnsupported", tc.base, tc.ref, err)
				}
				continue
			}
			if err != nil || filepath.ToSlash(got) != tc.want {
				t.Errorf("resolve(%q, %q) = %q, %v; want %q", tc.base, tc.ref, got, err, tc.want)
			}
		}
	}

	l := &loader{}
	if _, err := l.resolve(l.newBase("/a/b.m3u8"), "bad%zz.m4s"); !errors.Is(err, ErrInvalid) {
		t.Errorf("malformed escape: %v", err)
	}
}

// The allocation-light fast path must agree with the general path.
func TestResolveFastPathMatchesSlowPath(t *testing.T) {
	l := &loader{}
	for _, base := range []string{"/a/b/m.m3u8", "m.m3u8", "x/m.m3u8", "/a/b/dir/", "/m.m3u8", "../m.m3u8"} {
		for _, ref := range []string{"seg.m4s", "a/b/c.m4s", "seg-00001.m4s", "a.b/c.d.m4s", "weird name.m4s"} {
			if !isSimpleRef(ref) {
				t.Fatalf("%q should be a simple reference", ref)
			}
			fast, err1 := l.resolve(l.newBase(base), ref)
			slow, err2 := l.resolveSlow(l.newBase(base), ref)
			if err1 != nil || err2 != nil || fast != slow {
				t.Errorf("base %q ref %q: fast %q (%v) != slow %q (%v)", base, ref, fast, err1, slow, err2)
			}
		}
	}
	for _, ref := range []string{"", "/abs", "./a", "../a", "a/../b", "a//b", "a/./b", "a%20b", "a?b", "a#b", "sch:eme", "dir/", `a\b`, ".hidden", "a/.hidden"} {
		if isSimpleRef(ref) {
			t.Errorf("isSimpleRef(%q) = true", ref)
		}
	}
}
