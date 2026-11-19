// SPDX-License-Identifier: Apache-2.0

package ffadapter

// Scalars of the adapter. Result and configuration vocabulary shared with
// the core lives in addetect's constants.
const (
	// libraryFFmpeg keys the linked library versions in AdapterInfo.
	libraryFFmpeg = "ffmpeg"

	// selectionExplicit reports a track chosen by an explicit index.
	selectionExplicit = "explicit"
	// selectionAutoVideo and selectionAutoAudio describe the automatic
	// track rules (MEDIA-03).
	selectionAutoVideo = "auto: first non-attached-picture video stream"
	selectionAutoAudio = "auto: default-flagged, else first audio stream"

	// Container names reported for inputs.
	containerMP4  = "mp4"
	containerCMAF = "cmaf"

	// colorRangeUnknown is libavutil's name for an unsignalled color range.
	colorRangeUnknown = "unknown"

	// audioRuleDefault reports the default audio rendition rule.
	audioRuleDefault = "default"

	// Resource kinds in package failure messages.
	resourceKindMedia = "media"
	resourceKindInit  = "initialization"

	// hlsGapReason is the reason recorded for a declared HLS gap segment.
	hlsGapReason = "EXT-X-GAP"

	// msgStreamClosed reports use of a stream after Close.
	msgStreamClosed = "stream is closed"
	// fmtRational renders a rational such as a frame rate or time base.
	fmtRational = "%d/%d"

	// msgCanceled is the message of a cancellation error.
	msgCanceled = "analysis canceled: %w"

	// A file is reported truncated when its media ends more than
	// truncationSlackUS, and more than 1/truncationSlackDivisor of the
	// declared duration, before the duration its container declares.
	truncationSlackUS      = 2 * usPerSecond
	truncationSlackDivisor = 50
	// periodTailSlackUS is how far the media may end before the last DASH
	// Period's declared end without being reported as missing.
	periodTailSlackUS = usPerSecond / 2
	// gapReasonShortPeriod labels media that ends before its declared
	// Period end.
	gapReasonShortPeriod = "package_media_shorter_than_declared"

	// usPerSecond converts seconds to the microsecond timeline.
	usPerSecond = 1_000_000

	// ISO BMFF box header layout: a 32-bit size and a 4-byte type, followed
	// by a 64-bit size when the 32-bit size is boxSizeIsLarge. A size of
	// boxSizeToEnd means the box runs to the end of the resource.
	boxTypeOffset     = 4
	boxHeaderLen      = 8
	boxLargeHeaderLen = 16
	boxSizeIsLarge    = 1
	boxSizeToEnd      = 0
	boxMovieFragment  = "moof"

	// Buffer sizes for strings returned by the C shim and for sniffing the
	// head of an input file.
	errorMessageLen   = 200
	versionMessageLen = 160
	sniffLen          = 2048
)

// topLevelBoxes are the box types a CMAF initialization or media resource
// may start with; anything else is not checked for truncation.
var topLevelBoxes = [...]string{
	"ftyp", "styp", "moov", boxMovieFragment, "mdat", "sidx", "ssix", "emsg", "prft", "free", "skip", "uuid",
}

// hardwareDecoderName is reported when video is decoded in hardware.
const hardwareDecoderName = "videotoolbox"
