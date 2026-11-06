package main

import "github.com/egoist/mygo/ui"

// nativeFrame is the single frame boundary for normal, tray and fallback
// windows. Apply worker results and the visual environment before any content.
// The content callback is only used during this build, never retained.
func (a *app) nativeFrame(c *ui.Context, content func()) {
	a.drain()
	c.SetTheme(nativeTheme(c.Theme(), c.Preferences()))
	theme := c.Theme()
	ui.Column(c).Fill().Lang(a.t("en", "ja")).Font(uiFont(a.ja)).Background(theme.Background).TextColor(theme.Text).Children(content)
}

// nativeScreen shares the content inset without imposing a screen's spacing
// or actions on other screens. Window notices and controls stay outside it.
func nativeScreen(c *ui.Context, gap float32, content func()) {
	ui.Column(c).Fill().Padding(nativeStyles(c).screenPadding).Gap(gap).Children(content)
}

// nativeHeader shares title placement, not navigation or action policy.
// Each screen supplies its own controls and keeps their existing order.
func nativeHeader(c *ui.Context, title string, size float32, leading, trailing func()) {
	ui.Row(c).FillWidth().Gap(nativeStyles(c).controlGap).Children(func() {
		if leading != nil {
			leading()
		}
		ui.Text(c, title).FontSize(size).Bold().Grow(1)
		if trailing != nil {
			trailing()
		}
	})
}
