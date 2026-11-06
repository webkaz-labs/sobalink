// Package uibrand shares semantic colors and vector artwork across user interfaces.
package uibrand

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
)

// Colors contains the Web baseline palette. Native controls may retain OS accents
// and high-contrast overrides instead of these application theme colors.
type Colors struct {
	Background    string `json:"background"`
	Surface       string `json:"surface"`
	SurfaceSubtle string `json:"surface-subtle"`
	SurfaceHover  string `json:"surface-hover"`
	Text          string `json:"text"`
	Muted         string `json:"muted"`
	ControlBorder string `json:"control-border"`
	OnPrimary     string `json:"on-primary"`
	DisabledText  string `json:"disabled-text"`
	Border        string `json:"border"`
	Primary       string `json:"primary"`
	PrimaryHover  string `json:"primary-hover"`
	PrimarySoft   string `json:"primary-soft"`
	PrimaryText   string `json:"primary-text"`
	Green         string `json:"green"`
	GreenSoft     string `json:"green-soft"`
	Amber         string `json:"amber"`
	AmberSoft     string `json:"amber-soft"`
	Red           string `json:"red"`
	RedSoft       string `json:"red-soft"`
}

//go:embed palette.json
var paletteJSON []byte

//go:embed icons.json
var iconsJSON []byte

//go:embed mark.svg
var markSVG []byte

var palettes struct {
	Light Colors
	Dark  Colors
}
var icons map[string]string

func init() {
	if err := json.Unmarshal(paletteJSON, &palettes); err != nil {
		panic(err)
	}
	if err := json.Unmarshal(iconsJSON, &icons); err != nil {
		panic(err)
	}
}

// Palette returns a value copy of the selected semantic palette.
func Palette(dark bool) Colors {
	if dark {
		return palettes.Dark
	}
	return palettes.Light
}

// IconSVG returns monochrome artwork using currentColor, or false for an unknown
// name. Callers choose tint and presentation size without changing the geometry.
func IconSVG(name string) ([]byte, bool) {
	path, ok := icons[name]
	if !ok {
		return nil, false
	}
	return []byte(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="%s"/></svg>`, path)), true
}

// MarkSVG returns a fresh copy of the tilted three-bar brand mark, inheriting
// the caller's currentColor rather than the standalone favicon's purple fallback.
func MarkSVG() []byte {
	return bytes.Replace(markSVG, []byte(` color="`+Palette(false).Primary+`"`), nil, 1)
}

// MarkOpaqueSVG returns the same geometry with solid bars for small tray icons.
func MarkOpaqueSVG() []byte { return bytes.ReplaceAll(MarkSVG(), []byte(` opacity="0.6"`), nil) }
