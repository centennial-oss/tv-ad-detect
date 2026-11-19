// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"math"
	"os"
	"path/filepath"
)

// LogoAssetFormat and LogoAssetVersion identify the logo asset format
// (BUMPER-03): a JSON document holding the logo's edge mask.
const (
	LogoAssetFormat  = "tvad-logo"
	LogoAssetVersion = 1
)

// LogoAsset is a user-supplied or exported station logo (LOGO-02).
//
// The mask is an 8-bit grayscale PNG of Width x Height pixels; nonzero
// pixels are logo edge pixels. X, Y, Width and Height are in the analysis
// geometry AnalysisWidth x AnalysisHeight. For a recording with a different
// analysis geometry the box and mask are scaled by the ratio of the
// geometries (nearest neighbour); the asset is incompatible when the
// horizontal and vertical ratios differ by more than 2%.
type LogoAsset struct {
	Format         string `json:"format"`
	Version        int    `json:"version"`
	AssetVersion   string `json:"asset_version,omitempty"`
	AnalysisWidth  int    `json:"analysis_width"`
	AnalysisHeight int    `json:"analysis_height"`
	X              int    `json:"x"`
	Y              int    `json:"y"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	MaskPNG        []byte `json:"mask_png_base64"`
	// MaskSHA256 is the hex SHA-256 of the PNG bytes (integrity check).
	MaskSHA256 string `json:"mask_sha256"`

	mask []bool // decoded, Width*Height
}

func logoAssetErr(err error) error {
	return Errorf(KindInvalidConfig, "logo asset: %v", err)
}

// ParseLogoAsset decodes and validates a logo asset.
func ParseLogoAsset(data []byte) (*LogoAsset, error) {
	a := new(LogoAsset)
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(a); err != nil {
		return nil, logoAssetErr(err)
	}
	if err := a.validate(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *LogoAsset) validate() error {
	bad := func(format string, args ...any) error {
		return Errorf(KindInvalidConfig, "logo asset: "+format, args...)
	}
	if a.Format != LogoAssetFormat {
		return bad("format is %q, want %q", a.Format, LogoAssetFormat)
	}
	if a.Version != LogoAssetVersion {
		return bad("unsupported version %d (supported: %d)", a.Version, LogoAssetVersion)
	}
	if a.AnalysisWidth < minLogoGeometry || a.AnalysisHeight < minLogoGeometry || a.AnalysisWidth > maxLogoGeometry || a.AnalysisHeight > maxLogoGeometry {
		return bad("analysis geometry %dx%d is out of range", a.AnalysisWidth, a.AnalysisHeight)
	}
	if a.Width < 2 || a.Height < 2 || a.X < 0 || a.Y < 0 || a.X+a.Width > a.AnalysisWidth || a.Y+a.Height > a.AnalysisHeight {
		return bad("box %d,%d %dx%d does not fit the analysis geometry %dx%d", a.X, a.Y, a.Width, a.Height, a.AnalysisWidth, a.AnalysisHeight)
	}
	sum := sha256.Sum256(a.MaskPNG)
	if a.MaskSHA256 == "" || hex.EncodeToString(sum[:]) != a.MaskSHA256 {
		return bad("mask_sha256 does not match the mask data (integrity check failed)")
	}
	img, err := png.Decode(bytes.NewReader(a.MaskPNG))
	if err != nil {
		return bad("mask is not a valid PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != a.Width || b.Dy() != a.Height {
		return bad("mask is %dx%d but the box is %dx%d", b.Dx(), b.Dy(), a.Width, a.Height)
	}
	a.mask = make([]bool, a.Width*a.Height)
	set := 0
	for y := 0; y < a.Height; y++ {
		for x := 0; x < a.Width; x++ {
			if color.GrayModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.Gray).Y != 0 {
				a.mask[y*a.Width+x] = true
				set++
			}
		}
	}
	if set == 0 {
		return bad("mask has no edge pixels")
	}
	return nil
}

// Marshal encodes the asset as JSON.
func (a *LogoAsset) Marshal() ([]byte, error) {
	return json.MarshalIndent(a, "", "  ")
}

// project maps the asset into a recording's analysis geometry and returns
// the model, the pixel rectangle it occupies and a failure reason.
func (a *LogoAsset) project(aw, ah int) (*logoModel, [4]int, string) {
	sx, sy := float64(aw)/float64(a.AnalysisWidth), float64(ah)/float64(a.AnalysisHeight)
	if math.Abs(sx-sy) > aspectCompatTolerance*sx {
		return nil, [4]int{}, LogoFailAssetIncompatible
	}
	x0 := int(math.Round(float64(a.X) * sx))
	y0 := int(math.Round(float64(a.Y) * sy))
	x1 := int(math.Round(float64(a.X+a.Width) * sx))
	y1 := int(math.Round(float64(a.Y+a.Height) * sy))
	x0, y0 = max(x0, 0), max(y0, 0)
	x1, y1 = min(max(x1, x0+1), aw), min(max(y1, y0+1), ah)
	if x1-x0 < 2 || y1-y0 < 2 {
		return nil, [4]int{}, LogoFailAssetIncompatible
	}
	rw, rh := x1-x0, y1-y0
	m := &logoModel{mask: make([]uint64, bitWords(rw*rh)), box: [4]int{x0, y0, x1, y1}, origin: LogoOriginAsset, version: a.AssetVersion,
		scaled: aw != a.AnalysisWidth || ah != a.AnalysisHeight}
	for y := 0; y < rh; y++ {
		srcY := min(int((float64(y)+pixelCenter)*float64(a.Height)/float64(rh)), a.Height-1)
		for x := 0; x < rw; x++ {
			srcX := min(int((float64(x)+pixelCenter)*float64(a.Width)/float64(rw)), a.Width-1)
			if a.mask[srcY*a.Width+srcX] {
				p := y*rw + x
				setBit(m.mask, p)
				m.pixels++
			}
		}
	}
	// Enlarged, the mask's edges become several pixels thick while the
	// picture's edges stay as thin as they were: a mask pixel then counts
	// as found when an edge lies within the enlargement distance.
	if f := max(float64(rw)/float64(a.Width), float64(rh)/float64(a.Height)); f > 1 {
		m.reach = int(math.Ceil(f)) - 1
	}
	if m.pixels == 0 {
		return nil, [4]int{}, LogoFailAssetIncompatible
	}
	m.trim()
	return m, m.box, ""
}

// export converts a learned model into an asset (explicit export only).
func (m *logoModel) export(e *logoEpoch) *LogoAsset {
	bw, bh := m.box[2]-m.box[0], m.box[3]-m.box[1]
	img := image.NewGray(image.Rect(0, 0, bw, bh))
	for ri, r := range e.rects {
		rw := r[2] - r[0]
		n := rw * (r[3] - r[1])
		for p := 0; p < n; p++ {
			bit := e.rectOff[ri] + p
			if !hasBit(m.mask, bit) {
				continue
			}
			x, y := r[0]+p%rw-m.box[0], r[1]+p/rw-m.box[1]
			if x >= 0 && y >= 0 && x < bw && y < bh {
				img.Pix[y*img.Stride+x] = lumaMax
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	sum := sha256.Sum256(buf.Bytes())
	return &LogoAsset{
		Format: LogoAssetFormat, Version: LogoAssetVersion, AssetVersion: LogoOriginLearned,
		AnalysisWidth: e.geom.aw, AnalysisHeight: e.geom.ah,
		X: m.box[0], Y: m.box[1], Width: bw, Height: bh,
		MaskPNG: buf.Bytes(), MaskSHA256: hex.EncodeToString(sum[:]),
	}
}

// bumperImage is a decoded template: full-range luma of the PNG.
type bumperImage struct {
	tmpl *BumperTemplate
	w, h int
	luma []uint8
}

// Assets are the validated logo and bumper assets of a configuration
// (BUMPER-03). Assets are immutable and may be shared by concurrent
// analyses.
type Assets struct {
	Logo    *LogoAsset
	bumpers []bumperImage
	digests map[string]string
	// FromCache is set when the logo came from the logo cache.
	FromCache bool
	// notes are warnings about assets that were skipped (an unusable logo
	// cache entry).
	notes []Warning
}

// LoadAssets reads and validates every asset the configuration selects.
// Invalid or incompatible explicitly selected assets fail validation.
func LoadAssets(cfg *Config) (*Assets, error) {
	as := &Assets{digests: make(map[string]string)}
	switch {
	case cfg.LogoAssetData != nil:
		a := *cfg.LogoAssetData
		if err := a.validate(); err != nil {
			return nil, err
		}
		as.Logo = &a
		// The identity covers the whole asset (position and analysis
		// geometry as well as the mask), like the digest of an asset file.
		enc, err := a.Marshal()
		if err != nil {
			return nil, logoAssetErr(err)
		}
		sum := sha256.Sum256(enc)
		as.digests[digestKeyLogoAssetData] = hex.EncodeToString(sum[:])
	case cfg.Logo.Asset != "":
		data, err := os.ReadFile(cfg.Logo.Asset)
		if err != nil {
			return nil, Errorf(KindInvalidConfig, "logo.asset: %w", err)
		}
		a, err := ParseLogoAsset(data)
		if err != nil {
			return nil, err
		}
		as.Logo = a
		sum := sha256.Sum256(data)
		as.digests[digestKeyLogoAsset] = hex.EncodeToString(sum[:])
	case cfg.LogoCachePath() != "":
		// A cache entry is used like an asset; a missing one means "learn",
		// an unusable one is reported and also means "learn".
		path := cfg.LogoCachePath()
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			as.notes = append(as.notes, Warning{Code: WarnLogoCacheUnusable, Message: fmt.Sprintf("logo cache entry %s: %v; learning the logo", filepath.Base(path), err)})
		default:
			a, perr := ParseLogoAsset(data)
			if perr != nil {
				as.notes = append(as.notes, Warning{Code: WarnLogoCacheUnusable, Message: fmt.Sprintf("logo cache entry %s: %v; learning the logo", filepath.Base(path), perr)})
				break
			}
			as.Logo, as.FromCache = a, true
			sum := sha256.Sum256(data)
			as.digests[digestKeyLogoAsset] = hex.EncodeToString(sum[:])
		}
	}
	for i := range cfg.BumperTemplates {
		t := &cfg.BumperTemplates[i]
		name := bumperKeyPrefix + t.ID
		data := t.PNG
		if len(data) == 0 {
			var err error
			if data, err = os.ReadFile(t.Image); err != nil {
				return nil, Errorf(KindInvalidConfig, "%s.image: %w", name, err)
			}
		}
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		if t.SHA256 != "" && t.SHA256 != digest {
			return nil, Errorf(KindInvalidConfig, "%s: sha256 mismatch (integrity check failed): have %s", name, digest)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, Errorf(KindInvalidConfig, "%s: image is not a valid PNG: %v", name, err)
		}
		b := img.Bounds()
		if b.Dx() < minBumperSide || b.Dy() < minBumperSide || b.Dx() > maxBumperSide || b.Dy() > maxBumperSide {
			return nil, Errorf(KindInvalidConfig, "%s: image is %dx%d; dimensions must be %d..%d", name, b.Dx(), b.Dy(), minBumperSide, maxBumperSide)
		}
		bi := bumperImage{tmpl: t, w: b.Dx(), h: b.Dy(), luma: make([]uint8, b.Dx()*b.Dy())}
		lo, hi := uint8(lumaMax), uint8(0)
		for y := 0; y < bi.h; y++ {
			for x := 0; x < bi.w; x++ {
				v := color.GrayModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.Gray).Y
				bi.luma[y*bi.w+x] = v
				lo, hi = min(lo, v), max(hi, v)
			}
		}
		if hi-lo < minBumperRange {
			return nil, Errorf(KindInvalidConfig, "%s: image has no luminance structure to match", name)
		}
		as.bumpers = append(as.bumpers, bi)
		as.digests[name+".image"] = digest
	}
	return as, nil
}
