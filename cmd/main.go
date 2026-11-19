// SPDX-License-Identifier: Apache-2.0

// Command tv-ad-detect analyzes a recording (or an HLS/DASH package) and
// writes the canonical JSON analysis document produced by package addetect.
package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/centennial-oss/tv-ad-detect/pkg/addetect"
	"github.com/centennial-oss/tv-ad-detect/pkg/cutlist"
	"github.com/centennial-oss/tv-ad-detect/pkg/ffadapter"
)

// Build identity, set with -ldflags "-X main.commit=... -X main.buildDate=...".
var (
	commit    = buildUnknown
	buildDate = buildUnknown
)

// Command-line vocabulary and sizes.
const (
	buildUnknown = "unknown"

	cmdAnalyze = "analyze"
	cmdConfig  = "config"
	cmdBumper  = "bumper"
	subCapture = "capture"

	// stdoutPath is the --output value that selects standard output.
	stdoutPath = "-"
	// flagPrefix starts a flag; assign separates a flag or key from its
	// value.
	flagPrefix = "-"
	assign     = "="

	errorFormatText = "text"
	errorFormatJSON = "json"

	// outputBufferSize buffers result, CSV and stdout writes.
	outputBufferSize = 1 << 16
	// usPerSecond and usPerMinute convert media time for progress lines.
	usPerSecond = 1_000_000
	usPerMinute = 60 * usPerSecond
	// float64Bits is the precision of formatted measurements.
	float64Bits = 64
	// outputFileMode is the permission of written output files.
	outputFileMode  = 0o644
	msgWriteOutput  = "cannot write output: %w"
	msgMoveOutput   = "cannot move output into place: %w"
	msgOutputExists = "output %s already exists (use --overwrite)"
	// msgOutputIsInput and msgOutputsCollide refuse an output that would
	// replace something the analysis reads, or another output.
	msgOutputIsInput  = "output %s is also an input (%s); refusing to replace it"
	msgOutputsCollide = "outputs %s and %s are the same file"
	flagErrorFormat   = "--error-format"
	// flagSaveFeatures and flagFrameMeasurements name flags in messages.
	flagSaveFeatures = "--save-features"
	flagCutlist      = "--cutlist"
	flagCutlistClock = "--cutlist-clock"
	cutlistSeparator = ":"
	// cacheDirMode is the permission of a created logo cache directory.
	cacheDirMode          = 0o755
	flagFrameMeasurements = "--frame-measurements"
	// msgWriteResult reports a failed result write.
	msgWriteResult = "cannot write result: %w"
)

// settingFlags are the dedicated flags that set one configuration key.
var settingFlags = map[string]string{
	"--video-track":   addetect.KeyVideoTrack,
	"--audio-track":   addetect.KeyAudioTrack,
	"--caption-track": addetect.KeyCaptionTrack,
	"--diagnostics":   addetect.KeyDiagnostics,
}

// Exit codes (CLI-02).
const (
	exitOK          = 0
	exitInternal    = 1 // unexpected internal failure
	exitUsage       = 2 // argument or configuration error
	exitUnsupported = 3 // unsupported or unusable media/timing
	exitIO          = 4 // I/O or media-adapter failure
	exitResource    = 5 // resource limit
	exitCanceled    = 130
)

const usage = `tv-ad-detect - detect advertisement breaks in recorded video

Usage:
  tv-ad-detect analyze INPUT [flags]     analyze a recording, .m3u8 or .mpd,
                                         or re-classify a saved feature file
  tv-ad-detect bumper capture INPUT --at TIME -o CARD.png [--region x,y,w,h]
                                         save a frame (or part of one) as a
                                         bumper template image
  tv-ad-detect config defaults           print the default configuration (YAML)
  tv-ad-detect config validate [flags]   validate configuration and assets
  tv-ad-detect --version                 print application, schema and build identity
  tv-ad-detect --help

Analyze flags:
  --output PATH, -o PATH     result file; "-" (default) writes JSON to stdout
  --config BASE.yaml         base configuration layer
  --channel-config CH.yaml   channel configuration layer (applied after --config)
  --set section.key=value    explicit override (repeatable; highest precedence)
  --video-track N|auto       same as --set input.video_track=...
  --audio-track N|auto|none  same as --set input.audio_track=...
  --caption-track N|auto|embedded|none
  --diagnostics LEVEL        summary | detailed | full
  --overwrite                replace an existing output file
  --write-partial            on failure, write the partial result marked incomplete
  --export-logo PATH         write the learned logo as a tvad-logo asset
  --frame-measurements PATH  write per-frame raw measurements as CSV
  --cutlist FORMAT:PATH      also write a cut list: edl (skip intervals),
                             ffmetadata or ogm (chapters); repeatable
  --cutlist-clock CLOCK      player (default: 0 is the start of the file as a
                             player counts it) or result (the result timeline)
  --save-features PATH       save the recording's measurements (a .tvadf
                             feature file) for fast re-classification
  --pretty                   indent the JSON output
  --progress                 print progress to stderr
  --quiet                    do not print warnings to stderr
  --error-format text|json   error report format on stderr

Config validate flags: --config, --channel-config, --set

Exit codes: 0 success (including no ads), 1 internal failure, 2 argument or
configuration error, 3 unsupported or unusable media/timing, 4 I/O or media
failure, 5 resource limit, 130 canceled.
`

type cliError struct {
	code int
	kind string
	err  error
}

func (e *cliError) Error() string { return e.err.Error() }

func usageErr(format string, args ...any) *cliError {
	return &cliError{code: exitUsage, kind: "usage", err: fmt.Errorf(format, args...)}
}

func exitCodeFor(err error) (int, string) {
	var ce *cliError
	if errors.As(err, &ce) {
		return ce.code, ce.kind
	}
	kind := addetect.KindOf(err)
	switch kind {
	case addetect.KindInvalidConfig:
		return exitUsage, kind.String()
	case addetect.KindUnsupported, addetect.KindTiming:
		return exitUnsupported, kind.String()
	case addetect.KindIO, addetect.KindMedia:
		return exitIO, kind.String()
	case addetect.KindResourceLimit:
		return exitResource, kind.String()
	case addetect.KindCanceled:
		return exitCanceled, kind.String()
	}
	return exitInternal, kind.String()
}

// options collected from the command line.
type options struct {
	positional   []string
	output       string
	config       string
	channel      string
	sets         []string
	exportLogo   string
	frames       string
	errorFormat  string
	overwrite    bool
	writePartial bool
	saveFeatures string
	at           string
	region       string
	cutlists     []cutlistOut
	cutlistClock string
	pretty       bool
	progress     bool
	quiet        bool
	overwriteSet bool
	partialSet   bool
}

// parseArgs accepts flags before or after positional arguments, in both
// "--flag value" and "--flag=value" forms.
func parseArgs(args []string) (*options, error) {
	o := &options{output: stdoutPath, errorFormat: errorFormatText}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == stdoutPath || !strings.HasPrefix(a, flagPrefix) {
			o.positional = append(o.positional, a)
			continue
		}
		if a == "--" {
			o.positional = append(o.positional, args[i+1:]...)
			break
		}
		name, val, hasVal := strings.Cut(a, assign)
		value := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", usageErr("flag %s needs a value", name)
			}
			i++
			return args[i], nil
		}
		boolean := func(dst *bool) error {
			*dst = true
			if hasVal {
				b, err := strconv.ParseBool(val)
				if err != nil {
					return usageErr("flag %s: %q is not a boolean", name, val)
				}
				*dst = b
			}
			return nil
		}
		var err error
		switch name {
		case "--output", "-o":
			o.output, err = value()
		case "--config":
			o.config, err = value()
		case "--channel-config":
			o.channel, err = value()
		case "--set":
			var v string
			if v, err = value(); err == nil {
				o.sets = append(o.sets, v)
			}
		case "--export-logo":
			o.exportLogo, err = value()
		case flagFrameMeasurements:
			o.frames, err = value()
		case flagSaveFeatures:
			o.saveFeatures, err = value()
		case flagCutlist:
			var v string
			if v, err = value(); err == nil {
				format, path, ok := strings.Cut(v, cutlistSeparator)
				if !ok || format == "" || path == "" {
					err = usageErr("--cutlist expects FORMAT:PATH (formats: %s), got %q", strings.Join(cutlist.Formats, ", "), v)
				} else {
					o.cutlists = append(o.cutlists, cutlistOut{format: format, path: path})
				}
			}
		case flagCutlistClock:
			o.cutlistClock, err = value()
		case "--at":
			o.at, err = value()
		case "--region":
			o.region, err = value()
		case flagErrorFormat:
			if o.errorFormat, err = value(); err == nil && o.errorFormat != errorFormatText && o.errorFormat != errorFormatJSON {
				err = usageErr("--error-format must be text or json")
			}
		case "--overwrite":
			o.overwriteSet = true
			err = boolean(&o.overwrite)
		case "--write-partial":
			o.partialSet = true
			err = boolean(&o.writePartial)
		case "--pretty":
			err = boolean(&o.pretty)
		case "--progress":
			err = boolean(&o.progress)
		case "--quiet":
			err = boolean(&o.quiet)
		default:
			key, ok := settingFlags[name]
			if !ok {
				err = usageErr("unknown flag %s", name)
				break
			}
			var v string
			if v, err = value(); err == nil {
				o.sets = append(o.sets, key+assign+v)
			}
		}
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

// loadConfig applies the layers of CFG-02: defaults, base file, channel
// file, explicit overrides.
func loadConfig(o *options) (*addetect.Config, error) {
	cfg := addetect.DefaultConfig()
	for _, path := range []string{o.config, o.channel} {
		if path == "" {
			continue
		}
		if err := cfg.LoadYAMLFile(path); err != nil {
			return nil, err
		}
	}
	for _, kv := range o.sets {
		k, v, ok := strings.Cut(kv, assign)
		if !ok {
			return nil, usageErr("--set expects section.key=value, got %q", kv)
		}
		if err := cfg.Set(strings.TrimSpace(k), v); err != nil {
			return nil, err
		}
	}
	if o.overwriteSet {
		cfg.Outputs.Overwrite = o.overwrite
	}
	if o.partialSet {
		cfg.Outputs.WritePartial = o.writePartial
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func buildID() string { return commit + " " + buildDate }

func versionString() string {
	return fmt.Sprintf("%s %s\nresult schema %s, config schema %s, feature schema %s\nmedia adapter %s/%s\nbuild %s",
		addetect.ProductName, addetect.Version, addetect.SchemaVersion, addetect.ConfigSchemaVersion,
		addetect.FeatureSchemaVersion, ffadapter.AdapterName, ffadapter.AdapterVersion, buildID())
}

// writeAtomic writes data next to path and moves it into place only when
// complete (CLI-03). Without overwrite an existing file is never replaced.
func writeAtomic(path string, overwrite bool, write func(io.Writer) error) (err error) {
	if isSpecialFile(path) {
		// Devices and pipes (for example /dev/null) cannot be replaced
		// atomically; they are written in place.
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return addetect.Errorf(addetect.KindIO, msgWriteOutput, err)
		}
		bw := bufio.NewWriterSize(f, outputBufferSize)
		if err = write(bw); err == nil {
			err = bw.Flush()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return addetect.Errorf(addetect.KindIO, msgWriteOutput, err)
		}
		return nil
	}
	tmp, err := newTemp(path)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	bw := bufio.NewWriterSize(tmp, outputBufferSize)
	if err = write(bw); err == nil {
		err = bw.Flush()
	}
	if err != nil {
		return addetect.Errorf(addetect.KindIO, msgWriteOutput, err)
	}
	return commitTemp(tmp, path, overwrite)
}

// isSpecialFile reports whether path exists and is not a regular file.
func isSpecialFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.Mode().IsRegular() && !st.IsDir()
}

// newTemp creates the temporary file an output is written to before it is
// moved into place.
func newTemp(path string) (*os.File, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return nil, addetect.Errorf(addetect.KindIO, "cannot create output: %w", err)
	}
	return tmp, nil
}

// commitTemp makes a finished temporary file durable and moves it to path.
// Without overwrite the move fails if path exists. The temporary file is
// removed when the move fails.
func commitTemp(tmp *os.File, path string, overwrite bool) (err error) {
	defer func() {
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()
	if err = tmp.Chmod(outputFileMode); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return addetect.Errorf(addetect.KindIO, msgWriteOutput, err)
	}
	if overwrite {
		if err = os.Rename(tmp.Name(), path); err != nil {
			return addetect.Errorf(addetect.KindIO, msgMoveOutput, err)
		}
		return nil
	}
	// Link fails atomically when the destination exists.
	if err = os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return usageErr(msgOutputExists, path)
		}
		return addetect.Errorf(addetect.KindIO, msgMoveOutput, err)
	}
	os.Remove(tmp.Name())
	return nil
}

func checkWritable(path string, overwrite bool) error {
	if path == "" || path == stdoutPath {
		return nil
	}
	// A missing output directory is reported before any decoding.
	if st, err := os.Stat(filepath.Dir(path)); err != nil || !st.IsDir() {
		return addetect.Errorf(addetect.KindIO, "output directory %s does not exist", filepath.Dir(path))
	}
	if _, err := os.Lstat(path); err == nil && !overwrite && !isSpecialFile(path) {
		return usageErr(msgOutputExists, path)
	}
	return nil
}

// sameTarget reports whether two paths name the same file, however they are
// spelled: through symbolic links, relative forms or hard links. A path that
// does not exist yet is compared by its directory's real path and its name.
func sameTarget(a, b string) bool {
	sa, ea := os.Stat(a)
	sb, eb := os.Stat(b)
	if ea == nil && eb == nil {
		return os.SameFile(sa, sb)
	}
	ca, ok1 := canonicalPath(a)
	cb, ok2 := canonicalPath(b)
	return ok1 && ok2 && ca == cb
}

// canonicalPath resolves the directory of path to its real location and
// keeps the final name.
func canonicalPath(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", false
	}
	return filepath.Join(dir, filepath.Base(abs)), true
}

// cutlistOut is one --cutlist request.
type cutlistOut struct{ format, path string }

// namedOutputs returns the output paths that are files (not stdout).
func namedOutputs(o *options) []string {
	var out []string
	for _, p := range []string{o.output, o.exportLogo, o.frames, o.saveFeatures} {
		if p != "" && p != stdoutPath {
			out = append(out, p)
		}
	}
	for _, c := range o.cutlists {
		out = append(out, c.path)
	}
	return out
}

// checkOutputAliases refuses an output that is the same file as an input or
// as another output (CLI-03): writing it would destroy a recording, a
// manifest, a media segment, a configuration file or an asset.
func checkOutputAliases(outputs, inputs []string) error {
	for i, out := range outputs {
		for _, in := range inputs {
			if in != "" && sameTarget(out, in) {
				return usageErr(msgOutputIsInput, out, in)
			}
		}
		for _, other := range outputs[:i] {
			if sameTarget(out, other) {
				return usageErr(msgOutputsCollide, other, out)
			}
		}
	}
	return nil
}

// readFeatureInput returns the features in path when it is a feature file
// (recognized by its header), and nil for any other input.
func readFeatureInput(path string) (*addetect.Features, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil // the media adapter reports a missing input
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, nil
	}
	head := make([]byte, len(addetect.FeatureFileMagic))
	if _, err := io.ReadFull(f, head); err != nil || !addetect.IsFeatureFile(head) {
		return nil, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, addetect.Errorf(addetect.KindIO, "cannot read %s: %w", path, err)
	}
	return addetect.ReadFeatures(bufio.NewReaderSize(f, outputBufferSize))
}

func encodeResult(w io.Writer, res *addetect.Result, pretty bool) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if pretty {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(res)
}

func runAnalyze(ctx context.Context, o *options, stdout, stderr io.Writer) error {
	if len(o.positional) != 1 {
		return usageErr("analyze needs exactly one INPUT (got %d)", len(o.positional))
	}
	cfg, err := loadConfig(o)
	if err != nil {
		return err
	}
	// Fail before decoding when an output would be refused later. An output
	// that is also an input is refused whatever --overwrite says.
	outputs := namedOutputs(o)
	inputs := []string{o.positional[0], o.config, o.channel, cfg.Logo.Asset}
	for i := range cfg.BumperTemplates {
		inputs = append(inputs, cfg.BumperTemplates[i].Image)
	}
	// The logo cache entry may be rewritten, so it may not alias anything
	// either; it is not an output that --overwrite governs.
	aliased := outputs
	if cp := cfg.LogoCachePath(); cp != "" {
		aliased = append(append([]string(nil), outputs...), cp)
	}
	if err := checkOutputAliases(aliased, inputs); err != nil {
		return err
	}
	for _, p := range outputs {
		if err := checkWritable(p, cfg.Outputs.Overwrite); err != nil {
			return err
		}
	}

	// Cut-list formats and the clock are checked before any decoding.
	for _, c := range o.cutlists {
		if err := cutlist.Write(io.Discard, c.format, &addetect.Result{}, &cutlist.Options{Clock: o.cutlistClock}); err != nil {
			return usageErr("%v", err)
		}
	}

	// A feature file is re-classified without decoding; it has no frames to
	// report and is not itself saved again.
	features, err := readFeatureInput(o.positional[0])
	if err != nil {
		return err
	}
	if features != nil {
		for flag, set := range map[string]bool{flagFrameMeasurements: o.frames != "", flagSaveFeatures: o.saveFeatures != ""} {
			if set {
				return usageErr("%s needs a recording; %s is a feature file", flag, o.positional[0])
			}
		}
	}

	opts := &addetect.Options{Build: buildID()}
	if o.progress {
		last := int64(-1)
		opts.Progress = func(p addetect.Progress) {
			switch {
			case p.Stage != addetect.StageExtract:
				fmt.Fprintf(stderr, "progress: %s (%d frames)\n", p.Stage, p.Frames)
			case p.DurationHintUS > 0:
				if pct := p.MediaTimeUS * 100 / p.DurationHintUS; pct/5 != last {
					last = pct / 5
					fmt.Fprintf(stderr, "progress: %s %d%% (%.0f s of media)\n", p.Stage, pct, float64(p.MediaTimeUS)/usPerSecond)
				}
			default:
				if s := p.MediaTimeUS / usPerMinute; s != last {
					last = s
					fmt.Fprintf(stderr, "progress: %s %.0f s of media\n", p.Stage, float64(p.MediaTimeUS)/usPerSecond)
				}
			}
		}
	}
	var logo *addetect.LogoAsset
	cachePath := cfg.LogoCachePath()
	if o.exportLogo != "" || cachePath != "" {
		opts.OnLogoLearned = func(a *addetect.LogoAsset) {
			if logo == nil {
				logo = a
			}
		}
	}
	// Frame measurements stream to a temporary file that only becomes the
	// named output after a successful analysis (CLI-03).
	var frameErr error
	var framesFile *os.File
	var framesCSV *csv.Writer
	if o.frames != "" {
		if framesFile, err = newTemp(o.frames); err != nil {
			return err
		}
		defer func() {
			if framesFile != nil {
				framesFile.Close()
				os.Remove(framesFile.Name())
			}
		}()
		cw := csv.NewWriter(bufio.NewWriterSize(framesFile, outputBufferSize))
		framesCSV = cw
		cw.Write([]string{"time_us", "brightness", "nonuniformity", "bright_fraction", "coded_width", "coded_height"})
		rec := make([]string, 6)
		opts.FrameObserver = func(m addetect.FrameMeasurement) {
			rec[0] = strconv.FormatInt(m.TimeUS, 10)
			rec[1] = strconv.FormatFloat(m.Brightness, 'f', 3, float64Bits)
			rec[2] = strconv.FormatFloat(m.Nonuniformity, 'f', 3, float64Bits)
			rec[3] = strconv.FormatFloat(m.BrightFraction, 'f', 5, float64Bits)
			rec[4] = strconv.Itoa(m.CodedWidth)
			rec[5] = strconv.Itoa(m.CodedHeight)
			if e := cw.Write(rec); e != nil && frameErr == nil {
				frameErr = e
			}
		}
	}

	var (
		res    *addetect.Result
		runErr error
		saved  *addetect.Features
	)
	if features != nil {
		res, runErr = addetect.AnalyzeFeatures(ctx, features, cfg, opts)
	} else {
		if o.saveFeatures != "" {
			opts.OnFeatures = func(f *addetect.Features) { saved = f }
		}
		src := ffadapter.Path(o.positional[0])
		res, runErr = addetect.Analyze(ctx, src, cfg, opts)
		if runErr != nil && (res == nil || !cfg.Outputs.WritePartial) {
			return runErr
		}
		// The files a package refers to are only known once it has been
		// read: check them before anything is written.
		if err := checkOutputAliases(outputs, src.LocalInputs()); err != nil {
			return err
		}
	}
	if runErr != nil && (res == nil || !cfg.Outputs.WritePartial) {
		return runErr
	}
	if !o.quiet {
		for _, w := range res.Warnings {
			fmt.Fprintf(stderr, "warning: %s: %s\n", w.Code, w.Message)
		}
		if runErr != nil {
			fmt.Fprintln(stderr, "warning: writing a partial result marked incomplete (--write-partial)")
		}
	}
	if o.output == stdoutPath {
		// Buffered so logs can never interleave with JSON on stdout.
		bw := bufio.NewWriterSize(stdout, outputBufferSize)
		if err := encodeResult(bw, res, o.pretty); err != nil {
			return addetect.Errorf(addetect.KindIO, msgWriteResult, err)
		}
		if err := bw.Flush(); err != nil {
			return addetect.Errorf(addetect.KindIO, msgWriteResult, err)
		}
	} else if err := writeAtomic(o.output, cfg.Outputs.Overwrite, func(w io.Writer) error {
		return encodeResult(w, res, o.pretty)
	}); err != nil {
		return err
	}
	if runErr != nil {
		return runErr
	}
	for _, c := range o.cutlists {
		if err := writeAtomic(c.path, cfg.Outputs.Overwrite, func(w io.Writer) error {
			return cutlist.Write(w, c.format, res, &cutlist.Options{Clock: o.cutlistClock})
		}); err != nil {
			return err
		}
	}
	if saved != nil {
		if err := writeAtomic(o.saveFeatures, cfg.Outputs.Overwrite, func(w io.Writer) error {
			_, e := saved.WriteTo(w)
			return e
		}); err != nil {
			return err
		}
	}
	// A newly learned, validated logo refreshes the logo cache entry (the
	// library only reads the cache).
	if cachePath != "" && logo != nil {
		if err := os.MkdirAll(filepath.Dir(cachePath), cacheDirMode); err != nil {
			return addetect.Errorf(addetect.KindIO, "cannot create logo.cache_dir: %w", err)
		}
		data, err := logo.Marshal()
		if err != nil {
			return err
		}
		if err := writeAtomic(cachePath, true, func(w io.Writer) error {
			_, e := w.Write(append(data, '\n'))
			return e
		}); err != nil {
			return err
		}
	}
	if o.exportLogo != "" {
		if logo == nil {
			if !o.quiet {
				fmt.Fprintln(stderr, "warning: no validated logo was learned; --export-logo wrote nothing")
			}
		} else {
			data, err := logo.Marshal()
			if err != nil {
				return err
			}
			if err := writeAtomic(o.exportLogo, cfg.Outputs.Overwrite, func(w io.Writer) error {
				_, e := w.Write(append(data, '\n'))
				return e
			}); err != nil {
				return err
			}
		}
	}
	if framesCSV != nil {
		framesCSV.Flush()
		if e := framesCSV.Error(); e != nil && frameErr == nil {
			frameErr = e
		}
		if frameErr != nil {
			return addetect.Errorf(addetect.KindIO, "cannot write frame measurements: %w", frameErr)
		}
		tmp := framesFile
		framesFile = nil
		return commitTemp(tmp, o.frames, cfg.Outputs.Overwrite)
	}
	return nil
}

// runBumper implements "bumper capture": the first frame at or after --at
// (result timeline), cropped to --region, written as a PNG template image,
// and a configuration snippet that uses it on stdout. Nothing is captured
// implicitly (BUMPER-03).
func runBumper(ctx context.Context, o *options, stdout io.Writer) error {
	if len(o.positional) != 2 || o.positional[0] != subCapture {
		return usageErr("usage: bumper capture INPUT --at TIME -o CARD.png [--region x,y,w,h]")
	}
	in := o.positional[1]
	if o.at == "" || o.output == "" || o.output == stdoutPath {
		return usageErr("bumper capture needs --at TIME and --output CARD.png")
	}
	at, err := addetect.ParseDuration(o.at)
	if err != nil || at < 0 {
		return usageErr("--at %q: want a non-negative duration with a unit, such as 12.5s or 1m3s", o.at)
	}
	region := addetect.Region{X: 0, Y: 0, W: 1, H: 1}
	if o.region != "" {
		if region, err = addetect.ParseRegion(o.region); err != nil {
			return usageErr("--region: %v", err)
		}
	}
	cfg, err := loadConfig(o)
	if err != nil {
		return err
	}
	if err := checkOutputAliases([]string{o.output}, []string{in, o.config, o.channel}); err != nil {
		return err
	}
	if err := checkWritable(o.output, cfg.Outputs.Overwrite); err != nil {
		return err
	}
	fr, err := addetect.CaptureFrame(ctx, ffadapter.Path(in), cfg, at.Microseconds())
	if err != nil {
		return err
	}
	img := region.Crop(fr.Image)
	if err := writeAtomic(o.output, cfg.Outputs.Overwrite, func(w io.Writer) error { return png.Encode(w, img) }); err != nil {
		return err
	}
	id := strings.TrimSuffix(filepath.Base(o.output), filepath.Ext(o.output))
	fmt.Fprintf(stdout, "# frame at %.3f s, %dx%d analysis pixels\nbumpers:\n  templates:\n    %s:\n      image: %s\n      region: [%s]\n",
		float64(fr.TimeUS)/usPerSecond, img.Bounds().Dx(), img.Bounds().Dy(), id, o.output, region.String())
	return nil
}

func runConfig(o *options, stdout io.Writer) error {
	if len(o.positional) == 0 {
		return usageErr("config needs a subcommand: defaults or validate")
	}
	switch o.positional[0] {
	case "defaults":
		return addetect.WriteDefaultYAML(stdout)
	case "validate":
		if len(o.positional) > 1 {
			return usageErr("config validate takes no positional arguments")
		}
		cfg, err := loadConfig(o)
		if err != nil {
			return err
		}
		// Assets are read and checked without decoding any recording.
		if _, err := addetect.LoadAssets(cfg); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "configuration is valid")
		return nil
	}
	return usageErr("unknown config subcommand %q", o.positional[0])
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return usageErr("no command given")
	}
	switch args[0] {
	case "--version", "version":
		fmt.Fprintln(stdout, versionString())
		return nil
	case "--help", "-h", "help":
		fmt.Fprint(stdout, usage)
		return nil
	case cmdAnalyze, cmdConfig, cmdBumper:
		o, err := parseArgs(args[1:])
		if err != nil {
			return &flagError{o: o, err: err}
		}
		switch args[0] {
		case cmdConfig:
			err = runConfig(o, stdout)
		case cmdBumper:
			err = runBumper(ctx, o, stdout)
		default:
			err = runAnalyze(ctx, o, stdout, stderr)
		}
		if err != nil {
			return &flagError{o: o, err: err}
		}
		return nil
	}
	return usageErr("unknown command %q (see --help)", args[0])
}

// flagError carries the parsed options so the error can be reported in the
// requested format.
type flagError struct {
	o   *options
	err error
}

func (e *flagError) Error() string { return e.err.Error() }
func (e *flagError) Unwrap() error { return e.err }

// requestedErrorFormat finds --error-format anywhere on the command line,
// so the requested format applies even to an error in an earlier argument.
func requestedErrorFormat(args []string) string {
	for i, a := range args {
		name, val, hasVal := strings.Cut(a, assign)
		if name != flagErrorFormat {
			continue
		}
		if !hasVal && i+1 < len(args) {
			val = args[i+1]
		}
		if val == errorFormatJSON {
			return errorFormatJSON
		}
	}
	return errorFormatText
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err == nil {
		os.Exit(exitOK)
	}
	code, kind := exitCodeFor(err)
	format := requestedErrorFormat(os.Args[1:])
	if format == errorFormatJSON {
		json.NewEncoder(os.Stderr).Encode(map[string]any{
			"error": map[string]any{"kind": kind, "message": err.Error(), "exit_code": code},
		})
	} else {
		fmt.Fprintf(os.Stderr, "tv-ad-detect: %s\n", err)
	}
	os.Exit(code)
}
