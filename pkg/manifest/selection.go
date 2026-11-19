// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"fmt"
	"strconv"
	"strings"
)

// videoCand is one full-video candidate (HLS variant or DASH Representation).
type videoCand struct {
	id            string   // DASH Representation@id; HLS "v<N>"
	alt           []string // further identifiers accepted by "id=" (HLS NAME, URI)
	width, height int
	bandwidth     int64
	codecs        string
	frameRate     string
	note          string // extra text for the candidate report
}

func (c *videoCand) pixels() int64 { return int64(c.width) * int64(c.height) }

func (c *videoCand) describe(index int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%d] id=%s", index, c.id)
	if c.width > 0 || c.height > 0 {
		fmt.Fprintf(&sb, " %dx%d", c.width, c.height)
	}
	if c.bandwidth > 0 {
		fmt.Fprintf(&sb, " bandwidth=%d", c.bandwidth)
	}
	if c.frameRate != "" {
		fmt.Fprintf(&sb, " fps=%s", c.frameRate)
	}
	if c.codecs != "" {
		fmt.Fprintf(&sb, " codecs=%q", c.codecs)
	}
	if c.note != "" {
		sb.WriteByte(' ')
		sb.WriteString(c.note)
	}
	return sb.String()
}

type videoRuleKind uint8

const (
	videoHighest videoRuleKind = iota
	videoLowest
	videoIndex
	videoID
	videoBandwidth
)

type videoRule struct {
	kind videoRuleKind
	n    int64
	s    string
}

func parseVideoRule(s string) (videoRule, error) {
	s = strings.TrimSpace(s)
	key, val, hasVal := strings.Cut(s, ruleAssign)
	switch {
	case s == "" || s == VideoHighest:
		return videoRule{kind: videoHighest}, nil
	case s == VideoLowest:
		return videoRule{kind: videoLowest}, nil
	case hasVal && key == RuleKeyIndex:
		if n, ok := parseUint(val); ok {
			return videoRule{kind: videoIndex, n: n}, nil
		}
	case hasVal && key == RuleKeyBandwidth:
		if n, ok := parseUint(val); ok {
			return videoRule{kind: videoBandwidth, n: n}, nil
		}
	case hasVal && key == RuleKeyID && val != "":
		return videoRule{kind: videoID, s: val}, nil
	}
	return videoRule{}, errf(ErrSelection, "malformed video selection rule %q", s)
}

func (r videoRule) String() string {
	switch r.kind {
	case videoLowest:
		return VideoLowest
	case videoIndex:
		return RuleKeyIndex + ruleAssign + strconv.FormatInt(r.n, decimal)
	case videoID:
		return RuleKeyID + ruleAssign + r.s
	case videoBandwidth:
		return RuleKeyBandwidth + ruleAssign + strconv.FormatInt(r.n, decimal)
	}
	return VideoHighest
}

// pick returns the index of the selected candidate, or -1.
func (r videoRule) pick(c []videoCand, minHeight int) int {
	switch r.kind {
	case videoLowest:
		best := -1
		for i := range c {
			if c[i].height < minHeight {
				continue
			}
			if best < 0 || c[i].pixels() < c[best].pixels() ||
				c[i].pixels() == c[best].pixels() && c[i].bandwidth < c[best].bandwidth {
				best = i
			}
		}
		if best >= 0 {
			return best
		}
		return pickHighest(c)
	case videoIndex:
		if r.n < int64(len(c)) {
			return int(r.n)
		}
		return -1
	case videoID:
		for i := range c {
			if c[i].id == r.s {
				return i
			}
		}
		for i := range c {
			for _, a := range c[i].alt {
				if a != "" && a == r.s {
					return i
				}
			}
		}
		return -1
	case videoBandwidth:
		for i := range c {
			if c[i].bandwidth == r.n {
				return i
			}
		}
		return -1
	}
	return pickHighest(c)
}

func pickHighest(c []videoCand) int {
	best := -1
	for i := range c {
		if best < 0 || c[i].pixels() > c[best].pixels() ||
			c[i].pixels() == c[best].pixels() && c[i].bandwidth > c[best].bandwidth {
			best = i
		}
	}
	return best
}

// audioCand is one audio candidate (HLS rendition or DASH Representation).
type audioCand struct {
	id        string // HLS "GROUP-ID/NAME"; DASH Representation@id
	alt       string // HLS rendition URI as written; DASH AdaptationSet@id
	name      string // HLS NAME; DASH AdaptationSet Label
	lang      string
	codecs    string
	group     int // candidates of one DASH AdaptationSet share a group; HLS renditions are their own group
	bandwidth int64
	def       bool // HLS DEFAULT=YES
}

type audioRuleKind uint8

const (
	audioDefault audioRuleKind = iota
	audioNone
	audioLang
	audioName
	audioID
)

type audioRule struct {
	kind audioRuleKind
	s    string
}

func parseAudioRule(s string) (audioRule, error) {
	s = strings.TrimSpace(s)
	key, val, hasVal := strings.Cut(s, ruleAssign)
	switch {
	case s == "" || s == AudioDefault:
		return audioRule{kind: audioDefault}, nil
	case s == AudioNone:
		return audioRule{kind: audioNone}, nil
	case hasVal && val != "" && key == RuleKeyLang:
		return audioRule{kind: audioLang, s: val}, nil
	case hasVal && val != "" && key == RuleKeyName:
		return audioRule{kind: audioName, s: val}, nil
	case hasVal && val != "" && key == RuleKeyID:
		return audioRule{kind: audioID, s: val}, nil
	}
	return audioRule{}, errf(ErrSelection, "malformed audio selection rule %q", s)
}

func (r audioRule) String() string {
	switch r.kind {
	case audioNone:
		return AudioNone
	case audioLang:
		return RuleKeyLang + ruleAssign + r.s
	case audioName:
		return RuleKeyName + ruleAssign + r.s
	case audioID:
		return RuleKeyID + ruleAssign + r.s
	}
	return AudioDefault
}

func (r audioRule) explicit() bool { return r.kind >= audioLang }

// langMatch grades a language match: 2 exact (case-insensitive), 1 same
// primary subtag, 0 none.
func langMatch(have, want string) int {
	if have == "" || want == "" {
		return 0
	}
	if strings.EqualFold(have, want) {
		return 2
	}
	h, _, _ := strings.Cut(have, subtagSeparator)
	w, _, _ := strings.Cut(want, subtagSeparator)
	if strings.EqualFold(h, w) {
		return 1
	}
	return 0
}

// pick returns the index of the selected candidate, or -1. Once a group is
// chosen, the winner inside it is its first DEFAULT candidate, else its
// highest-bandwidth candidate, else its first.
func (r audioRule) pick(c []audioCand) int {
	if len(c) == 0 {
		return -1
	}
	inGroup := func(anchor int, keep func(*audioCand) bool) int {
		best := -1
		for i := range c {
			if c[i].group != c[anchor].group || !keep(&c[i]) {
				continue
			}
			if c[i].def {
				return i
			}
			if best < 0 || c[i].bandwidth > c[best].bandwidth {
				best = i
			}
		}
		return best
	}
	switch r.kind {
	case audioNone:
		return -1
	case audioLang:
		anchor, grade := -1, 0
		for i := range c {
			if g := langMatch(c[i].lang, r.s); g > grade {
				anchor, grade = i, g
				if g == 2 {
					break
				}
			}
		}
		if anchor < 0 {
			return -1
		}
		return inGroup(anchor, func(a *audioCand) bool { return langMatch(a.lang, r.s) == grade })
	case audioName:
		for i := range c {
			if c[i].name == r.s {
				return inGroup(i, func(a *audioCand) bool { return a.name == r.s })
			}
		}
		return -1
	case audioID:
		for i := range c {
			if c[i].id == r.s {
				return i
			}
		}
		for i := range c {
			if c[i].alt != "" && c[i].alt == r.s {
				return inGroup(i, func(a *audioCand) bool { return a.alt == r.s })
			}
		}
		return -1
	}
	// Default: the first DEFAULT=YES candidate (HLS), else the best
	// candidate of the first group (DASH: the first audio AdaptationSet).
	for i := range c {
		if c[i].def {
			return i
		}
	}
	return inGroup(0, func(*audioCand) bool { return true })
}

// isAudioCodec reports whether an RFC 6381 codec string names an audio codec.
func isAudioCodec(codec string) bool {
	codec = strings.TrimSpace(codec)
	name, _, _ := strings.Cut(codec, codecSeparator)
	switch strings.ToLower(name) {
	case "mp4a", "ac-3", "ec-3", "ac-4", "opus", "flac", "alac", "mp3", "vorbis",
		"dtsc", "dtse", "dtsh", "dtsl", "dtsx", "mha1", "mhm1", "samr", "sawb", "ipcm", "fpcm":
		return true
	}
	return false
}

// splitCodecs partitions a comma-separated codecs attribute into its audio
// entries; allAudio is true when every entry is an audio codec.
func splitCodecs(codecs string) (audio string, allAudio bool) {
	if strings.TrimSpace(codecs) == "" {
		return "", false
	}
	allAudio = true
	var sb strings.Builder
	for part := range strings.SplitSeq(codecs, listSeparator) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if isAudioCodec(part) {
			if sb.Len() > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(part)
		} else {
			allAudio = false
		}
	}
	return sb.String(), allAudio
}
