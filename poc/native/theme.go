package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/webkaz-labs/sobalink/internal/uibrand"
)

// Copy the system theme rather than changing MyGo's shared theme. Keep its
// metrics and accessibility adaptations. Desktop contrast and accent choices
// take precedence over the companion palette.
func nativeTheme(base *ui.Theme, prefs ui.Preferences) *ui.Theme {
	theme := *base
	if prefs.HighContrast {
		return &theme
	}
	p := uibrand.Palette(theme.Dark)
	theme.Background = ui.Hex(p.Background)
	theme.Surface = ui.Hex(p.Surface)
	theme.SurfaceHover = ui.Hex(p.SurfaceHover)
	theme.SurfacePressed = ui.Hex(p.PrimarySoft)
	// MyGo's Border identifies controls. Decorative cards use a separate token.
	theme.Border = ui.Hex(p.ControlBorder)
	theme.Text = ui.Hex(p.Text)
	theme.TextMuted = ui.Hex(p.Muted)
	if prefs.Accent.A == 0 {
		theme.Accent = ui.Hex(p.Primary)
		theme.AccentHover = ui.Hex(p.PrimaryHover)
		theme.AccentPressed = theme.AccentHover
		theme.AccentText = ui.Hex(p.OnPrimary)
		theme.Selection = theme.Accent.Alpha(.25)
		theme.Focus = theme.Accent
	}
	// Success is a semantic status color, not an arbitrary desktop accent.
	theme.Success = ui.Hex(p.Green)
	theme.Warning = ui.Hex(p.Amber)
	theme.Danger = ui.Hex(p.Red)
	return &theme
}

// These are component-specific tokens; ordinary controls use c.Theme().
// Typography retains the existing sizes at the platform default and scales
// with the supplied theme's font size. All spacing uses Theme.Space.
type nativeStyle struct {
	status, history, outgoing, border, brand                  ui.Color
	positiveText, warningText, dangerText                     ui.Color
	title, device, body, caption, metadata, chevron           float32
	cardRadius, historyRadius, rowBorder                      float32
	screenPadding, sectionGap, itemGap, controlGap, actionGap float32
	cardPadding, rowPadding, detailGap, paneGap               float32
}

func nativeStyles(c *ui.Context) nativeStyle {
	t := c.Theme()
	base := ui.LightTheme().FontSize
	s := nativeStyle{
		status: t.SurfacePressed, history: t.SurfaceHover, outgoing: t.SurfacePressed, border: t.Border, brand: t.Text,
		positiveText: t.Success, warningText: t.Warning, dangerText: t.Danger,
		title: t.Rem(25 / base), device: t.Rem(16 / base),
		body: t.Rem(13 / base), caption: t.Rem(12 / base),
		metadata: t.Rem(11 / base), chevron: t.Rem(22 / base),
		cardRadius: t.Radius * 1.5, historyRadius: t.Radius, rowBorder: t.Space(.375),
		screenPadding: t.Space(4), sectionGap: t.Space(3), itemGap: t.Space(1.75), controlGap: t.Space(2), actionGap: t.Space(1.5),
		cardPadding: t.Space(3), rowPadding: t.Space(2.75), detailGap: t.Space(1), paneGap: t.Space(2.5),
	}
	if c.Preferences().HighContrast {
		s.positiveText, s.warningText, s.dangerText = t.Text, t.Text, t.Text
	} else {
		p := uibrand.Palette(t.Dark)
		s.status, s.history = ui.Hex(p.SurfaceSubtle), ui.Hex(p.SurfaceSubtle)
		s.outgoing, s.border, s.brand = ui.Hex(p.PrimarySoft), ui.Hex(p.Border), ui.Hex(p.Primary)
	}
	return s
}

// Color supports the unchanged status label; it never turns an accepted or
// submitted request into delivery evidence. High contrast uses its foreground.
func nativeStatusColor(c *ui.Context, status string) ui.Color {
	style := nativeStyles(c)
	switch status {
	case "completed":
		return style.positiveText
	case "uncertain":
		return style.warningText
	case "failed", "too_large", "configured_too_large":
		return style.dangerText
	default:
		return c.Theme().Text
	}
}

// Use Japanese faces for the whole run, including punctuation. These are
// installed system-font candidates, not bundled font assets.
func uiFont(japanese bool) string {
	if !japanese {
		return ""
	}
	return "Hiragino Sans, Yu Gothic UI, Meiryo, Noto Sans CJK JP, Noto Sans JP"
}
