package main

import (
	"bytes"
	"image/png"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/webkaz-labs/sobalink/internal/uibrand"
)

// Parse the shared artwork once. Icons inherit the surrounding semantic text
// color, including high contrast; their labels belong to the containing action.
var brandMark = ui.MustParseSVG(uibrand.MarkSVG())
var trayMark = ui.MustParseSVG(uibrand.MarkOpaqueSVG())
var actionIcons = func() map[string]*ui.SVG {
	icons := make(map[string]*ui.SVG)
	for _, name := range []string{"back", "refresh", "chevron", "message", "folder", "send"} {
		source, ok := uibrand.IconSVG(name)
		if !ok {
			panic("missing shared action icon")
		}
		icons[name] = ui.MustParseSVG(source)
	}
	return icons
}()

// An action with a shared glyph retains its visible label and accessible name.
// Like other MyGo elements, this return value is used only during this build.
func nativeIconButton(c *ui.Context, icon, label string) ui.Element {
	return ui.Button(c, "").Label(label).MaxWidthPercent(100).Children(func() {
		ui.Icon(c, actionIcons[icon])
		ui.Text(c, label).Shrink(1)
	})
}

func nativePrimaryButton(c *ui.Context, icon, label string) ui.Element {
	return ui.PrimaryButton(c, "").Label(label).MaxWidthPercent(100).Children(func() {
		ui.Icon(c, actionIcons[icon])
		ui.Text(c, label).Shrink(1)
	})
}

// Runtime PNGs are derived from the same SVG as the Web header, not a second
// hand-drawn mark. The tray uses solid ink so its third bar survives at 16pt.
// These process-local images do not change executable resources or packaging.
var nativeAppIcon = sync.OnceValues(func() ([]byte, error) { return nativeBrandPNG(256, false) })

func nativeTrayIcon() ([]byte, error) { return nativeBrandPNG(32, true) }

func nativeBrandPNG(size int, template bool) ([]byte, error) {
	img := ui.Render(func(c *ui.Context) {
		theme := *c.Theme()
		theme.Background = ui.Transparent
		c.SetTheme(&theme)
		if template {
			ui.Icon(c, trayMark).Size(float32(size), float32(size)).TextColor(ui.Hex("#000000"))
			return
		}
		s := float32(size)
		ui.Column(c).Fill().Padding(s / 32).Children(func() {
			ui.Column(c).Fill().Padding(s / 8).Background(ui.Hex(uibrand.Palette(false).Primary)).Radius(s / 5).Children(func() {
				ui.Icon(c, brandMark).Size(s*11/16, s*11/16).TextColor(ui.Hex(uibrand.Palette(false).OnPrimary))
			})
		})
	}, size, size, 1)
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
