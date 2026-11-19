// SPDX-License-Identifier: Apache-2.0

package manifest

// Selection rule spellings accepted by Options.Video and Options.Audio.
// Keyed rules are written "<key>=<value>", for example "id=720p".
const (
	VideoHighest = "highest"
	VideoLowest  = "lowest"
	AudioDefault = "default"
	AudioNone    = "none"

	RuleKeyIndex     = "index"
	RuleKeyBandwidth = "bandwidth"
	RuleKeyID        = "id"
	RuleKeyLang      = "lang"
	RuleKeyName      = "name"

	ruleAssign = "="
)

// Scalars shared by the HLS and DASH parsers.
const (
	// decimal is the base of every number in a manifest.
	decimal = 10
	// maxQuoted bounds manifest text quoted in error messages.
	maxQuoted = 40
	// maxMarkerDetail bounds the detail kept for a diagnostic marker.
	maxMarkerDetail = 1024
	// schemeFile is the only URI scheme treated as local.
	schemeFile = "file"
	// msgMalformedFileURI reports an unusable file URI.
	msgMalformedFileURI = "malformed file URI %q"

	// Track kinds of a DASH Representation.
	kindVideo = "video"
	kindAudio = "audio"

	// minusSign marks a negative number; rangeSeparator splits "first-last"
	// byte ranges; codecSeparator splits a codec name from its profile
	// ("avc1.64001f"); currentDir is the "." path segment.
	minusSign      = "-"
	rangeSeparator = "-"
	codecSeparator = "."
	currentDir     = "."
	// smallInitSet is the size up to which initialization resources are
	// de-duplicated by linear search; pathSlack is spare capacity for
	// formatted template numbers.
	smallInitSet = 16
	pathSlack    = 16

	// subtagSeparator separates language subtags ("en-US").
	subtagSeparator = "-"
	// pathSeparator joins an HLS rendition group and name into a track id
	// and separates the major type of a MIME type.
	pathSeparator = "/"
	// listSeparator separates comma-delimited manifest lists.
	listSeparator = ","

	// DASH diagnostic detail labels.
	detailPeriod = "period="
	detailScheme = " schemeIdUri="
	detailValue  = " value="
	// attrRepeat is the SegmentTimeline S@r attribute.
	attrRepeat = "r"

	// HLS tags and attribute names referred to more than once.
	tagKey           = "#EXT-X-KEY"
	tagMediaSequence = "#EXT-X-MEDIA-SEQUENCE"
	attrURI          = "URI"
	attrName         = "NAME"
	attrAudio        = "AUDIO"
	attrCaptions     = "CLOSED-CAPTIONS"
	valueNone        = "NONE"
	// soleVariantID is the track id of a media playlist loaded directly.
	soleVariantID = "v0"
)
