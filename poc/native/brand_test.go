package main

import (
	"bytes"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/webkaz-labs/sobalink/internal/uibrand"
)

func colorContrast(a, b ui.Color) float64 {
	luminance := func(c ui.Color) float64 {
		var value float64
		for i, channel := range []byte{c.R, c.G, c.B} {
			v := float64(channel) / 255
			if v <= .04045 {
				v /= 12.92
			} else {
				v = math.Pow((v+.055)/1.055, 2.4)
			}
			value += v * []float64{.2126, .7152, .0722}[i]
		}
		return value
	}
	x, y := luminance(a), luminance(b)
	return (math.Max(x, y) + .05) / (math.Min(x, y) + .05)
}

func TestNativePaletteContrast(t *testing.T) {
	for _, base := range []*ui.Theme{ui.LightTheme(), ui.DarkTheme()} {
		theme := nativeTheme(base, ui.Preferences{})
		p := uibrand.Palette(base.Dark)
		for _, surface := range []ui.Color{theme.Background, theme.Surface, theme.SurfaceHover, theme.SurfacePressed, ui.Hex(p.SurfaceSubtle)} {
			for _, text := range []ui.Color{theme.Text, theme.TextMuted, theme.Success, theme.Warning, theme.Danger} {
				if ratio := colorContrast(text, surface); ratio < 4.5 {
					t.Errorf("dark=%t text=%v surface=%v contrast=%.2f", base.Dark, text, surface, ratio)
				}
			}
			for _, indicator := range []ui.Color{theme.Border, theme.Focus} {
				if ratio := colorContrast(indicator, surface); ratio < 3 {
					t.Errorf("dark=%t control/focus contrast=%.2f", base.Dark, ratio)
				}
			}
		}
		for _, fill := range []ui.Color{theme.Accent, theme.AccentHover, theme.AccentPressed} {
			if ratio := colorContrast(theme.AccentText, fill); ratio < 4.5 {
				t.Errorf("dark=%t primary text contrast=%.2f", base.Dark, ratio)
			}
		}
	}
}

func TestNativeStatusColorsRetainEvidenceBoundaries(t *testing.T) {
	for _, highContrast := range []bool{false, true} {
		tt := ui.NewTester(func(c *ui.Context) {
			c.SetTheme(nativeTheme(c.Theme(), c.Preferences()))
			for _, status := range []string{"submitted", "prepared", "sent", "received", "awaiting-acceptance", "sending", "unknown"} {
				if nativeStatusColor(c, status) != c.Theme().Text {
					t.Errorf("%s must not be presented as confirmed delivery", status)
				}
			}
			if c.Preferences().HighContrast {
				for _, status := range []string{"completed", "failed", "uncertain", "too_large"} {
					if nativeStatusColor(c, status) != c.Theme().Text {
						t.Errorf("%s bypasses high contrast", status)
					}
				}
			}
		}, 360, 560)
		tt.SetPreferences(ui.Preferences{HighContrast: highContrast})
	}
}

func TestNativeBrandRenders(t *testing.T) {
	for _, template := range []bool{false, true} {
		for _, size := range []int{16, 20, 24, 32, 256} {
			data, err := nativeBrandPNG(size, template)
			if err != nil {
				t.Fatal(err)
			}
			img, err := png.Decode(bytes.NewReader(data))
			if err != nil || img.Bounds().Dx() != size || img.Bounds().Dy() != size {
				t.Fatal("invalid shared brand PNG")
			}
			ink, transparent := 0, 0
			for y := range size {
				for x := range size {
					r, g, b, a := img.At(x, y).RGBA()
					if template && (r != 0 || g != 0 || b != 0) {
						t.Fatal("tray template must contain only black ink")
					}
					if a == 0 {
						transparent++
					} else {
						ink++
					}
				}
			}
			if ink < size || transparent < size {
				t.Fatal("brand mark is missing or lacks transparent padding")
			}
			if out := os.Getenv("SOBA_POC_THEME_OUTPUT"); out != "" {
				if err := os.WriteFile(filepath.Join(out, fmt.Sprintf("brand-%d-template-%t.png", size, template)), data, 0644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestNativeBrandRespectsHighContrastAndAccent(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, highContrast := range []bool{false, true} {
			tt := ui.NewTester(func(c *ui.Context) {
				c.SetTheme(nativeTheme(c.Theme(), c.Preferences()))
				style := nativeStyles(c)
				want := ui.Hex(uibrand.Palette(c.Theme().Dark).Primary)
				if c.Preferences().HighContrast {
					want = c.Theme().Text
				}
				if style.brand != want {
					t.Fatal("brand must keep product identity except in high contrast")
				}
				ui.Icon(c, brandMark).Size(36, 36).TextColor(style.brand)
			}, 40, 40)
			tt.SetDark(dark)
			tt.SetPreferences(ui.Preferences{Accent: ui.Hex("#facc15"), HighContrast: highContrast})
			// The SVG must inherit TextColor, rather than a favicon color baked in.
			want := ui.Hex(uibrand.Palette(dark).Primary)
			if highContrast {
				want = ui.LightTheme().Text
				if dark {
					want = ui.DarkTheme().Text
				}
			}
			found := false
			for y := range 36 {
				for x := range 36 {
					p := tt.Image().RGBAAt(x, y)
					found = found || p.A == 255 && p.R == want.R && p.G == want.G && p.B == want.B
				}
			}
			if !found {
				t.Fatal("brand did not paint its intended accessible foreground")
			}
		}
	}
}
