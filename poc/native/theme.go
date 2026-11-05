package main

import "github.com/egoist/mygo/ui"

// Copy the system theme rather than changing MyGo's shared theme. Keep its
// metrics and dark appearance; the light palette preserves the companion look.
func nativeTheme(base *ui.Theme) *ui.Theme {
	theme := *base
	if !theme.Dark {
		theme.Background = ui.Hex("#f4f7f5")
		theme.Surface = ui.Hex("#ffffff")
		theme.SurfaceHover = ui.Hex("#edf5f1")
		theme.SurfacePressed = ui.Hex("#e2efea")
		theme.Border = ui.Hex("#d8e3e0")
		theme.Text = ui.Hex("#172e34")
		theme.TextMuted = ui.Hex("#52666a")
		theme.Accent = ui.Hex("#26745b")
		theme.AccentHover = ui.Hex("#205f4b")
		theme.AccentPressed = ui.Hex("#194e3d")
		theme.Focus = theme.Accent
		theme.Success = theme.Accent
		theme.Warning = ui.Hex("#9a4427")
	}
	return &theme
}

// These are component-specific tokens; ordinary controls use c.Theme().
// Typography retains the existing sizes at the platform default and scales
// with the supplied theme's font size. All spacing uses Theme.Space.
type nativeStyle struct {
	status, history                                 ui.Color
	title, device, body, caption, metadata, chevron float32
	cardRadius, historyRadius, rowBorder            float32
}

func nativeStyles(c *ui.Context) nativeStyle {
	t := c.Theme()
	base := ui.LightTheme().FontSize
	s := nativeStyle{
		status: t.SurfacePressed, history: t.SurfaceHover,
		title: t.Rem(25 / base), device: t.Rem(16 / base),
		body: t.Rem(13 / base), caption: t.Rem(12 / base),
		metadata: t.Rem(11 / base), chevron: t.Rem(22 / base),
		cardRadius: t.Space(2.25), historyRadius: t.Space(1.5), rowBorder: t.Space(.375),
	}
	if !t.Dark {
		s.history = ui.Hex("#edf3f0")
	}
	return s
}

// Use Japanese faces for the whole run, including punctuation. These are
// installed system-font candidates, not bundled font assets.
func uiFont(japanese bool) string {
	if !japanese {
		return ""
	}
	return "Hiragino Sans, Yu Gothic UI, Meiryo, Noto Sans CJK JP, Noto Sans JP"
}
