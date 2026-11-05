package main

import "github.com/egoist/mygo/ui"

func (s *nativeShell) fallbackView(a *app, c *ui.Context) {
	c.SetTheme(nativeTheme(c.Theme()))
	theme, style := c.Theme(), nativeStyles(c)
	ui.Column(c).Fill().Lang(a.t("en", "ja")).Font(uiFont(a.ja)).Background(theme.Background).Children(func() {
		ui.Text(c, a.t("Tray unavailable. Using a normal window; close it to quit the UI. The daemon is unchanged.", "トレイを利用できないため通常表示です。ウィンドウを閉じるとUIを終了します。本体の状態は変わりません。")).FillWidth().Padding(theme.Space(3)).FontSize(style.body).TextColor(theme.Text)
		ui.Column(c).FillWidth().Grow(1).Children(func() { a.view(c) })
	})
}
func (s *nativeShell) view(a *app, c *ui.Context) {
	c.SetTheme(nativeTheme(c.Theme()))
	theme := c.Theme()
	ui.Column(c).Fill().Lang(a.t("en", "ja")).Font(uiFont(a.ja)).Background(theme.Background).Children(func() {
		ui.Column(c).FillWidth().Grow(1).Children(func() { a.view(c) })
		s.controlsView(a, c)
	})
}
func (s *nativeShell) controlsView(a *app, c *ui.Context) {
	theme := c.Theme()
	ui.Row(c).FillWidth().Padding(theme.Space(3)).Gap(theme.Space(2)).Children(func() {
		if ui.Button(c, a.t("Hide", "隠す")).Clicked() {
			s.hideWindow()
		}
		ui.Text(c, "").Grow(1)
		if ui.Button(c, a.t("Quit UI (daemon continues)", "UIを終了（本体は継続）")).Clicked() {
			s.quitUI()
		}
	})
}
