// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// cfgField describes one configuration key bound to a Config field.
type cfgField struct {
	section, key string
	index        []int // reflect field index path from Config
	desc         string
	min, max     string
	enum         []string
	hasEnum      bool
	isPath       bool
	noDigest     bool
	feature      bool // affects extracted measurements (feature identity)
}

// Struct tag names understood on Config fields.
const (
	tagKey      = "cfg"
	tagDesc     = "desc"
	tagMin      = "min"
	tagMax      = "max"
	tagPath     = "path"
	tagNoDigest = "nodigest"
	tagFeature  = "feature"
)

// minHistogramBins is the smallest scene.histogram_bins choice; choices
// double up to lumaLevels.
const minHistogramBins = 16

// Textual value syntax shared by the YAML layer, --set overrides and the
// effective-configuration rendering.
const (
	listSeparator       = ","  // items of a list, components of a region
	regionListSeparator = ";"  // regions in a list of regions
	yamlFlowSeparator   = ", " // items of an emitted YAML flow sequence
	yamlNullTag         = "!!null"
	lineEnd             = "\n"
	decimalBase         = 10
	float64Bits         = 64
	fmtKeyError         = "%s: %v"
	bumperTemplateLabel = "bumper template "
)

var (
	trueWords  = [...]string{"true", "yes", "on", "1"}
	falseWords = [...]string{"false", "no", "off", "0"}
	// yamlOtherReserved are further plain scalars YAML reads as non-strings.
	yamlOtherReserved = [...]string{"null", "y", "n"}

	// sizeUnits are the ByteSize suffixes, largest first.
	sizeUnits = [...]struct {
		suffix string
		bytes  int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}}
)

// enumBinding ties an enumerated configuration field to its allowed values.
type enumBinding struct {
	field  any // *string or *int inside the Config given to enumBindings
	values []string
}

// enumBindings lists the enumerated fields of c with their allowed values.
// Binding by field address keeps the constants the only place a value is
// spelled.
func enumBindings(c *Config) []enumBinding {
	var bins []string
	for b := minHistogramBins; b <= lumaLevels; b *= 2 {
		bins = append(bins, strconv.Itoa(b))
	}
	return []enumBinding{
		{&c.Input.Decoder, []string{DecoderFFmpeg}},
		{&c.Input.UnsupportedFormat, []string{FormatPolicyWarn, FormatPolicyFail}},
		{&c.Input.OptionalStreams, []string{StreamPolicyDegraded, StreamPolicyStrict}},
		{&c.Input.TimestampResetPolicy, []string{ResetPolicyRebase, ResetPolicyFail}},
		{&c.Input.SkipLoopFilter, []string{LoopFilterNone, LoopFilterNonRef, LoopFilterAll}},
		{&c.Package.ManifestType, []string{ManifestAuto, ManifestHLS, ManifestDASH}},
		{&c.Package.FailurePolicy, []string{FailurePolicyFail, FailurePolicyDegraded}},
		{&c.Sampling.DecodeFrames, []string{DecodeAll, DecodeReference}},
		{&c.Dark.Mode, []string{DarkModeFixed, DarkModeAuto}},
		{&c.Audio.SilenceMode, []string{SilenceModeFixed, SilenceModeAuto}},
		{&c.Input.HardwareDecode, []string{HardwareDecodeOff, HardwareDecodeAuto}},
		{&c.Logo.AmbiguousState, []string{LogoAmbiguousHold, LogoAmbiguousUnknown}},
		{&c.Scoring.CaptionMatchCondition, []string{CaptionMatchAlways, CaptionMatchNoLogo, CaptionMatchNotPopOn, CaptionMatchNoLogoNotPopOn}},
		{&c.Logo.ThresholdMode, []string{LogoThresholdFixed, LogoThresholdRelative}},
		{&c.Scene.HistogramBins, bins},
		{&c.Captions.Service, []string{ServiceCC1, ServiceCC2, ServiceCC3, ServiceCC4}},
		{&c.Audio.Metric, []string{AudioMetricRMS}},
		{&c.Audio.ChannelPolicy, []string{ChannelsAll, ChannelsExcludeLFE}},
		{&c.Bumpers.Boundary, []string{BumperBoundaryBoth, BumperBoundaryStart, BumperBoundaryEnd}},
		{&c.Boundaries.SpanPolicy, []string{SpanPolicyMidpoint}},
		{&c.Baselines.CaptionStyle, []string{"", CaptionStyleRollUp, CaptionStylePopOn, CaptionStylePaintOn}},
		{&c.Scoring.BoundaryEdges, []string{EdgesBoth, EdgesEither}},
		{&c.Outputs.Diagnostics, []string{DiagnosticsSummary, DiagnosticsDetailed, DiagnosticsFull}},
	}
}

var (
	cfgFieldsOnce sync.Once
	cfgFieldList  []cfgField
	cfgFieldMap   map[string]*cfgField
	cfgSections   map[string]struct{}
)

func cfgFields() ([]cfgField, map[string]*cfgField) {
	cfgFieldsOnce.Do(func() {
		t := reflect.TypeOf(Config{})
		cfgSections = make(map[string]struct{})
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			sec := sf.Tag.Get(tagKey)
			if sec == "" || sf.Type.Kind() != reflect.Struct {
				continue
			}
			cfgSections[sec] = struct{}{}
			noDigest := sf.Tag.Get(tagNoDigest) != ""
			feature := sf.Tag.Get(tagFeature) != ""
			for j := 0; j < sf.Type.NumField(); j++ {
				kf := sf.Type.Field(j)
				key := kf.Tag.Get(tagKey)
				if key == "" {
					continue
				}
				cfgFieldList = append(cfgFieldList, cfgField{
					section: sec, key: key, index: []int{i, j},
					desc: kf.Tag.Get(tagDesc), min: kf.Tag.Get(tagMin), max: kf.Tag.Get(tagMax),
					isPath: kf.Tag.Get(tagPath) != "", noDigest: noDigest,
					feature: feature || kf.Tag.Get(tagFeature) != "",
				})
			}
		}
		cfgFieldMap = make(map[string]*cfgField, len(cfgFieldList))
		for i := range cfgFieldList {
			f := &cfgFieldList[i]
			cfgFieldMap[f.section+keySeparator+f.key] = f
		}
		// Bind enumerated fields to their allowed values by field address.
		var probe Config
		pv := reflect.ValueOf(&probe).Elem()
		for _, b := range enumBindings(&probe) {
			bound := false
			for i := range cfgFieldList {
				f := &cfgFieldList[i]
				if pv.FieldByIndex(f.index).Addr().Interface() == b.field {
					f.enum, f.hasEnum, bound = b.values, true, true
					break
				}
			}
			if !bound {
				panic("addetect: enumBindings names a field that is not a configuration key")
			}
		}
		// Keys and sections that code refers to by name must exist.
		named := []string{KeyVideoTrack, KeyAudioTrack, KeyCaptionTrack, KeyDiagnostics}
		for _, k := range named {
			if cfgFieldMap[k] == nil {
				panic("addetect: configuration constant " + k + " has no field")
			}
		}
		if _, ok := cfgSections[sectionBumpers]; !ok {
			panic("addetect: configuration constant " + sectionBumpers + " has no section")
		}
	})
	return cfgFieldList, cfgFieldMap
}

var (
	durationType = reflect.TypeOf(time.Duration(0))
	byteSizeType = reflect.TypeOf(ByteSize(0))
	regionType   = reflect.TypeOf(Region{})
)

func parseFinite(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), float64Bits)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%q is not finite", s)
	}
	return v, nil
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration with a unit (for example 100ms or 30s)", s)
	}
	return d, nil
}

func parseByteSize(s string) (ByteSize, error) {
	s = strings.TrimSpace(s)
	mult := int64(1)
	for _, u := range sizeUnits {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), u.bytes
			break
		}
	}
	n, err := strconv.ParseInt(s, decimalBase, float64Bits)
	if err != nil || n < 0 || (mult > 1 && n > math.MaxInt64/mult) {
		return 0, fmt.Errorf("%q is not a size (for example 512MiB)", s)
	}
	return ByteSize(n * mult), nil
}

func (b ByteSize) String() string {
	// The largest unit that divides the size exactly; bytes otherwise.
	unit := sizeUnits[len(sizeUnits)-1]
	for _, u := range sizeUnits {
		if int64(b) >= u.bytes && int64(b)%u.bytes == 0 {
			unit = u
			break
		}
	}
	return strconv.FormatInt(int64(b)/unit.bytes, decimalBase) + unit.suffix
}

func parseRegion(s string) (Region, error) {
	parts := strings.Split(s, listSeparator)
	if len(parts) != 4 {
		return Region{}, fmt.Errorf("region %q must be x,y,w,h", s)
	}
	var v [4]float64
	for i, p := range parts {
		f, err := parseFinite(p)
		if err != nil {
			return Region{}, err
		}
		v[i] = f
	}
	return Region{v[0], v[1], v[2], v[3]}, nil
}

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'g', -1, float64Bits) }

func (r Region) String() string {
	return strings.Join([]string{fmtFloat(r.X), fmtFloat(r.Y), fmtFloat(r.W), fmtFloat(r.H)}, listSeparator)
}

func parseBool(s string) (bool, error) {
	word := strings.ToLower(strings.TrimSpace(s))
	if slices.Contains(trueWords[:], word) {
		return true, nil
	}
	if slices.Contains(falseWords[:], word) {
		return false, nil
	}
	return false, fmt.Errorf("%q is not a boolean (true or false)", s)
}

// setValue parses s into v according to v's type.
func setValue(v reflect.Value, s string) error {
	switch v.Type() {
	case durationType:
		d, err := parseDuration(s)
		if err != nil {
			return err
		}
		v.SetInt(int64(d))
		return nil
	case byteSizeType:
		b, err := parseByteSize(s)
		if err != nil {
			return err
		}
		v.SetInt(int64(b))
		return nil
	case regionType:
		r, err := parseRegion(s)
		if err != nil {
			return err
		}
		v.Set(reflect.ValueOf(r))
		return nil
	}
	switch v.Kind() {
	case reflect.Bool:
		b, err := parseBool(s)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Int:
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("%q is not an integer", s)
		}
		v.SetInt(int64(n))
	case reflect.Float64:
		f, err := parseFinite(s)
		if err != nil {
			return err
		}
		v.SetFloat(f)
	case reflect.String:
		v.SetString(s)
	case reflect.Slice:
		sep := listSeparator
		if v.Type().Elem() == regionType {
			sep = regionListSeparator
		}
		var parts []string
		if strings.TrimSpace(s) != "" {
			parts = strings.Split(s, sep)
		}
		if len(parts) == 0 {
			// An empty list is the zero value, as in the defaults.
			v.Set(reflect.Zero(v.Type()))
			return nil
		}
		out := reflect.MakeSlice(v.Type(), len(parts), len(parts))
		for i, p := range parts {
			if err := setValue(out.Index(i), strings.TrimSpace(p)); err != nil {
				return err
			}
		}
		v.Set(out) // lists replace, never concatenate (CFG-02)
	default:
		return fmt.Errorf("unsupported field type %s", v.Type())
	}
	return nil
}

func formatValue(v reflect.Value) string {
	switch v.Type() {
	case durationType:
		return formatDuration(time.Duration(v.Int()))
	case byteSizeType:
		return ByteSize(v.Int()).String()
	case regionType:
		return v.Interface().(Region).String()
	}
	switch v.Kind() {
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int:
		return strconv.FormatInt(v.Int(), decimalBase)
	case reflect.Float64:
		return fmtFloat(v.Float())
	case reflect.String:
		return v.String()
	case reflect.Slice:
		sep := listSeparator
		if v.Type().Elem() == regionType {
			sep = regionListSeparator
		}
		var sb strings.Builder
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				sb.WriteString(sep)
			}
			sb.WriteString(formatValue(v.Index(i)))
		}
		return sb.String()
	}
	return ""
}

// formatDuration prints a duration with an explicit unit and no redundant
// zero components (1m0s -> 1m, 0s stays 0s).
func formatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

// numeric returns the comparable magnitude of a scalar field value.
func numeric(v reflect.Value) (float64, bool) {
	switch v.Kind() {
	case reflect.Int, reflect.Int64:
		return float64(v.Int()), true
	case reflect.Float64:
		return v.Float(), true
	}
	return 0, false
}

func (f *cfgField) check(v reflect.Value) error {
	name := f.section + keySeparator + f.key
	if f.hasEnum {
		s := formatValue(v)
		for _, e := range f.enum {
			if e == s {
				return nil
			}
		}
		return Errorf(KindInvalidConfig, "%s: %q is not one of %s", name, s, strings.Join(f.enum, yamlFlowSeparator))
	}
	if n, ok := numeric(v); ok {
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return Errorf(KindInvalidConfig, "%s: value is not finite", name)
		}
		for _, lim := range [...]struct {
			tag   string
			upper bool
		}{{f.min, false}, {f.max, true}} {
			if lim.tag == "" {
				continue
			}
			lv := reflect.New(v.Type()).Elem()
			if err := setValue(lv, lim.tag); err != nil {
				return Errorf(KindInternal, "%s: bad limit tag %q", name, lim.tag)
			}
			l, _ := numeric(lv)
			if (!lim.upper && n < l) || (lim.upper && n > l) {
				return Errorf(KindInvalidConfig, "%s: %s is outside the range %s..%s", name, formatValue(v), f.min, f.max)
			}
		}
	}
	return nil
}

func validateTags(c *Config) error {
	fields, _ := cfgFields()
	rv := reflect.ValueOf(c).Elem()
	for i := range fields {
		if err := fields[i].check(rv.FieldByIndex(fields[i].index)); err != nil {
			return err
		}
	}
	return nil
}

// Set applies one explicit override (CFG-02 top layer). key is
// "section.key", for example "scoring.threshold"; bumper templates use
// "bumpers.templates.<id>.<key>". Relative paths are left relative to the
// working directory.
func (c *Config) Set(key, value string) error {
	return c.set(key, value, "")
}

func (c *Config) set(key, value, baseDir string) error {
	if !utf8.ValidString(value) {
		return Errorf(KindInvalidConfig, "%s: value is not valid UTF-8", key)
	}
	if rest, ok := strings.CutPrefix(key, bumperKeyPrefix); ok {
		i := strings.LastIndexByte(rest, '.')
		if i <= 0 {
			return Errorf(KindInvalidConfig, "unknown configuration key %q", key)
		}
		return c.setBumper(rest[:i], rest[i+1:], value, baseDir)
	}
	_, m := cfgFields()
	f := m[key]
	if f == nil {
		return Errorf(KindInvalidConfig, "unknown configuration key %q", key)
	}
	v := reflect.ValueOf(c).Elem().FieldByIndex(f.index)
	if err := setValue(v, value); err != nil {
		return Errorf(KindInvalidConfig, fmtKeyError, key, err)
	}
	if f.isPath && baseDir != "" && value != "" && !filepath.IsAbs(value) {
		v.SetString(filepath.Join(baseDir, value))
	}
	return f.check(v)
}

func (c *Config) setBumper(id, key, value, baseDir string) error {
	var t *BumperTemplate
	for i := range c.BumperTemplates {
		if c.BumperTemplates[i].ID == id {
			t = &c.BumperTemplates[i]
		}
	}
	if t == nil {
		c.BumperTemplates = append(c.BumperTemplates, BumperTemplate{
			ID: id, Region: fullFrameRegion, Geometry: GeometryStretch,
			Threshold: defaultBumperThreshold, MinSupport: defaultBumperMinSupport,
		})
		t = &c.BumperTemplates[len(c.BumperTemplates)-1]
	}
	name := bumperKeyPrefix + id + keySeparator + key
	var err error
	switch key {
	case tmplKeyImage:
		if baseDir != "" && value != "" && !filepath.IsAbs(value) {
			value = filepath.Join(baseDir, value)
		}
		t.Image, t.PNG = value, nil
	case tmplKeyRegion:
		t.Region, err = parseRegion(value)
	case tmplKeyGeometry:
		t.Geometry = value
	case tmplKeyThreshold:
		t.Threshold, err = parseFinite(value)
	case tmplKeyMinSupport:
		t.MinSupport, err = parseDuration(value)
	case tmplKeySHA256:
		t.SHA256 = strings.ToLower(strings.TrimSpace(value))
	default:
		return Errorf(KindInvalidConfig, "unknown configuration key %q", name)
	}
	if err != nil {
		return Errorf(KindInvalidConfig, fmtKeyError, name, err)
	}
	return nil
}

// LoadYAMLFile applies one YAML configuration layer from a file; relative
// paths inside it resolve against the file's directory (CFG-02).
func (c *Config) LoadYAMLFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return Errorf(KindInvalidConfig, "cannot read configuration: %w", err)
	}
	return c.LoadYAML(data, path, filepath.Dir(path))
}

// LoadYAML applies one UTF-8 YAML configuration layer. Only keys present in
// the layer replace existing values; lists replace rather than concatenate.
// name is used in error messages; baseDir resolves relative paths.
//
// The document is a mapping of sections to mappings of keys:
//
//	scoring:
//	  threshold: 1.5
//	  standard_durations: [10s, 15s, 30s]
//	bumpers:
//	  templates:
//	    station_card: {image: card.png, region: [0, 0, 1, 1]}
//
// Unknown sections or keys, duplicate keys within the layer and malformed
// values are errors (CFG-03).
func (c *Config) LoadYAML(data []byte, name, baseDir string) error {
	cfgFields()
	if !utf8.Valid(data) {
		return Errorf(KindInvalidConfig, "%s: not valid UTF-8", name)
	}
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil {
		if err == io.EOF {
			return nil // empty layer
		}
		return Errorf(KindInvalidConfig, fmtKeyError, name, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return Errorf(KindInvalidConfig, "%s: only one YAML document per file is supported", name)
	}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		root = root.Content[0]
	}
	if root.Kind == yaml.ScalarNode && root.Tag == yamlNullTag {
		return nil
	}
	fail := func(n *yaml.Node, format string, args ...any) error {
		return Errorf(KindInvalidConfig, "%s:%d: %s", name, n.Line, fmt.Sprintf(format, args...))
	}
	pairs := func(n *yaml.Node, what string, fn func(k, v *yaml.Node) error) error {
		if n.Kind != yaml.MappingNode {
			return fail(n, "%s must be a mapping", what)
		}
		seen := make(map[string]int, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind != yaml.ScalarNode {
				return fail(k, "keys of %s must be plain scalars", what)
			}
			if prev, dup := seen[k.Value]; dup {
				return fail(k, "duplicate key %q (first set on line %d)", k.Value, prev)
			}
			seen[k.Value] = k.Line
			if err := fn(k, v); err != nil {
				return err
			}
		}
		return nil
	}
	setKey := func(full string, v *yaml.Node) error {
		text, err := yamlValueText(v)
		if err != nil {
			return fail(v, fmtKeyError, full, err)
		}
		if err := c.set(full, text, baseDir); err != nil {
			return fail(v, "%s", strings.TrimPrefix(err.Error(), KindInvalidConfig.String()+kindSeparator))
		}
		return nil
	}
	return pairs(root, "the configuration", func(sk, sv *yaml.Node) error {
		section := sk.Value
		if _, known := cfgSections[section]; !known {
			return fail(sk, "unknown section %q", section)
		}
		return pairs(sv, "section "+section, func(k, v *yaml.Node) error {
			if section == sectionBumpers && k.Value == keyTemplates {
				if v.Kind == yaml.ScalarNode && v.Tag == yamlNullTag {
					return nil
				}
				return pairs(v, bumperTemplatesKey, func(idNode, tv *yaml.Node) error {
					if idNode.Value == "" || strings.ContainsAny(idNode.Value, ". \t") {
						return fail(idNode, "bumper template id %q must be non-empty without dots or spaces", idNode.Value)
					}
					return pairs(tv, bumperTemplateLabel+idNode.Value, func(tk, tvv *yaml.Node) error {
						return setKey(bumperKeyPrefix+idNode.Value+keySeparator+tk.Value, tvv)
					})
				})
			}
			return setKey(section+keySeparator+k.Value, v)
		})
	})
}

// yamlValueText flattens a YAML value into the textual form understood by
// setValue: scalars as written; a sequence of scalars joined with ','; a
// sequence of sequences (a list of regions) joined with ';'.
func yamlValueText(n *yaml.Node) (string, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == yamlNullTag {
			return "", nil
		}
		return n.Value, nil
	case yaml.SequenceNode:
		parts := make([]string, len(n.Content))
		nested := false
		for i, item := range n.Content {
			switch item.Kind {
			case yaml.ScalarNode:
				parts[i] = item.Value
				// "x,y,w,h" strings are regions inside a list of regions.
				nested = nested || strings.Contains(item.Value, listSeparator)
			case yaml.SequenceNode:
				t, err := yamlValueText(item)
				if err != nil {
					return "", err
				}
				parts[i], nested = t, true
			default:
				return "", fmt.Errorf("list items must be scalars or lists")
			}
		}
		if nested {
			return strings.Join(parts, regionListSeparator), nil
		}
		return strings.Join(parts, listSeparator), nil
	case yaml.AliasNode:
		return yamlValueText(n.Alias)
	}
	return "", fmt.Errorf("expected a scalar or a list")
}

// yamlScalar renders a formatted value as a YAML scalar or flow sequence.
func yamlScalar(v reflect.Value) string {
	if v.Type() == regionType {
		return "[" + strings.ReplaceAll(formatValue(v), listSeparator, yamlFlowSeparator) + "]"
	}
	if v.Kind() == reflect.Slice {
		var sb strings.Builder
		sb.WriteByte('[')
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				sb.WriteString(yamlFlowSeparator)
			}
			sb.WriteString(yamlScalar(v.Index(i)))
		}
		sb.WriteByte(']')
		return sb.String()
	}
	s := formatValue(v)
	if v.Kind() == reflect.String {
		return yamlString(s)
	}
	return s
}

// yamlString quotes a string unless it is a safe plain scalar that cannot
// be read as another YAML type.
func yamlString(s string) string {
	plain := s != ""
	for i := 0; i < len(s) && plain; i++ {
		ch := s[i]
		ok := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch == '_' ||
			(i > 0 && (ch >= '0' && ch <= '9' || ch == '-' || ch == '.' || ch == '/' || ch == '='))
		plain = ok
	}
	if w := strings.ToLower(s); slices.Contains(trueWords[:], w) || slices.Contains(falseWords[:], w) || slices.Contains(yamlOtherReserved[:], w) {
		plain = false
	}
	if plain {
		return s
	}
	return strconv.Quote(s)
}

// WriteDefaultYAML writes the complete default configuration with every
// supported key, its default, range and description (CFG-01).
func WriteDefaultYAML(w io.Writer) error {
	return DefaultConfig().WriteYAML(w, true)
}

// WriteYAML writes the configuration as YAML. With comments it includes the
// description and range of every key.
func (c *Config) WriteYAML(w io.Writer, comments bool) error {
	fields, _ := cfgFields()
	bw := bufio.NewWriter(w)
	rv := reflect.ValueOf(c).Elem()
	if comments {
		fmt.Fprintf(bw, "# tv-ad-detect configuration, schema version %s\n", ConfigSchemaVersion)
		bw.WriteString("# Durations carry units (100ms, 30s). Ratios are 0..1. Luminance is 0..255.\n")
		bw.WriteString("# Layers: built-in defaults < --config < --channel-config < --set overrides.\n")
	}
	writeTemplates := func() {
		if len(c.BumperTemplates) == 0 {
			if comments {
				bw.WriteString("  # Bumper templates (none built in), keyed by template id:\n")
				bw.WriteString("  # templates:\n")
				bw.WriteString("  #   station_card:\n")
				bw.WriteString("  #     image: card.png        # PNG, relative to this file\n")
				bw.WriteString("  #     region: [0, 0, 1, 1]   # normalized frame region the image occupies\n")
				bw.WriteString("  #     geometry: stretch      # stretch | strict (aspect must match within 2%)\n")
				bw.WriteString("  #     threshold: 0.9         # zero-mean normalized cross-correlation, (0,1]\n")
				bw.WriteString("  #     min_support: 400ms     # minimum match duration\n")
				bw.WriteString("  #     sha256: \"\"             # optional integrity check of the PNG file\n")
			}
			return
		}
		bw.WriteString("  templates:\n")
		for i := range c.BumperTemplates {
			t := &c.BumperTemplates[i]
			fmt.Fprintf(bw, "    %s:\n      image: %s\n      region: %s\n      geometry: %s\n      threshold: %s\n      min_support: %s\n",
				yamlString(t.ID), yamlString(t.Image), yamlScalar(reflect.ValueOf(t.Region)), yamlString(t.Geometry),
				fmtFloat(t.Threshold), formatDuration(t.MinSupport))
			if t.SHA256 != "" {
				fmt.Fprintf(bw, "      sha256: %s\n", strconv.Quote(t.SHA256))
			}
		}
	}
	section := ""
	for i := range fields {
		f := &fields[i]
		if f.section != section {
			if section == sectionBumpers {
				writeTemplates()
			}
			section = f.section
			fmt.Fprintf(bw, "\n%s:\n", section)
		}
		if comments {
			fmt.Fprintf(bw, "  # %s\n", f.desc)
			switch {
			case f.hasEnum:
				vals := make([]string, len(f.enum))
				for j, e := range f.enum {
					vals[j] = yamlString(e)
				}
				fmt.Fprintf(bw, "  # values: %s\n", strings.Join(vals, " | "))
			case f.min != "" || f.max != "":
				fmt.Fprintf(bw, "  # range: %s .. %s\n", f.min, f.max)
			}
			if f.feature {
				bw.WriteString("  " + featureKeyNote + "\n")
			}
		}
		fmt.Fprintf(bw, "  %s: %s\n", f.key, yamlScalar(rv.FieldByIndex(f.index)))
	}
	if section == sectionBumpers {
		writeTemplates()
	}
	return bw.Flush()
}

// featureKeys returns, sorted, the effective-configuration keys that affect
// extracted measurements: fields tagged as features, every bumper template
// key and a logo asset supplied as data (PIPE-03). Score weights are never
// among them.
func featureKeys(effective map[string]string) []string {
	fields, _ := cfgFields()
	keys := make([]string, 0, len(fields))
	for i := range fields {
		if fields[i].feature {
			keys = append(keys, fields[i].section+keySeparator+fields[i].key)
		}
	}
	for k := range effective {
		if strings.HasPrefix(k, bumperKeyPrefix) || k == digestKeyLogoAssetData {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// ConfigIdentity is the redacted effective configuration and its digest
// (CFG-02). Paths are reduced to their base name; the digest covers
// detection-relevant keys and the content digests of the assets, so it is
// stable across machines and unaffected by output/resource settings.
type ConfigIdentity struct {
	SchemaVersion string            `json:"schema_version"`
	Digest        string            `json:"digest"`
	Effective     map[string]string `json:"effective"`

	// hashed holds, per key, the value the digest covers: the content
	// digest of an asset where Effective shows only its file name.
	hashed map[string]string
}

func (c *Config) identity(assetDigests map[string]string) ConfigIdentity {
	fields, _ := cfgFields()
	rv := reflect.ValueOf(c).Elem()
	eff := make(map[string]string, len(fields)+8)
	hashedVals := make(map[string]string, len(fields)+8)
	h := sha256.New()
	io.WriteString(h, "tvad-config/"+ConfigSchemaVersion+lineEnd)
	add := func(key, shown, hashed string, digest bool) {
		eff[key] = shown
		hashedVals[key] = hashed
		if digest {
			io.WriteString(h, key)
			io.WriteString(h, "=")
			io.WriteString(h, hashed)
			io.WriteString(h, lineEnd)
		}
	}
	for i := range fields {
		f := &fields[i]
		key := f.section + keySeparator + f.key
		val := formatValue(rv.FieldByIndex(f.index))
		if f.isPath {
			shown := ""
			if val != "" {
				shown = filepath.Base(val)
			}
			add(key, shown, assetDigests[key], !f.noDigest)
			continue
		}
		add(key, val, val, !f.noDigest)
	}
	tmpl := make([]*BumperTemplate, len(c.BumperTemplates))
	for i := range c.BumperTemplates {
		tmpl[i] = &c.BumperTemplates[i]
	}
	sort.Slice(tmpl, func(i, j int) bool { return tmpl[i].ID < tmpl[j].ID })
	for _, t := range tmpl {
		p := bumperKeyPrefix + t.ID + keySeparator
		add(p+tmplKeyImage, filepath.Base(t.Image), assetDigests[p+tmplKeyImage], true)
		add(p+tmplKeyRegion, t.Region.String(), t.Region.String(), true)
		add(p+tmplKeyGeometry, t.Geometry, t.Geometry, true)
		add(p+tmplKeyThreshold, fmtFloat(t.Threshold), fmtFloat(t.Threshold), true)
		add(p+tmplKeyMinSupport, formatDuration(t.MinSupport), formatDuration(t.MinSupport), true)
	}
	if d, ok := assetDigests[digestKeyLogoAssetData]; ok {
		add(digestKeyLogoAssetData, DigestPrefix+d, d, true)
	}
	return ConfigIdentity{
		SchemaVersion: ConfigSchemaVersion,
		Digest:        DigestPrefix + hex.EncodeToString(h.Sum(nil)),
		Effective:     eff,
		hashed:        hashedVals,
	}
}

// ParseDuration parses a duration with an explicit unit, as configuration
// files write it (for example 12.5s or 1m3s).
func ParseDuration(s string) (time.Duration, error) { return parseDuration(s) }

// ParseRegion parses a normalized region written x,y,w,h and checks that it
// lies inside the frame.
func ParseRegion(s string) (Region, error) {
	r, err := parseRegion(s)
	if err != nil {
		return Region{}, err
	}
	if err := r.validate(); err != nil {
		return Region{}, err
	}
	return r, nil
}
