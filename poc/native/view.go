package main

import "github.com/egoist/mygo/ui"

// The frame boundary applies worker results before composing the current screen.
func (a *app) view(c *ui.Context) {
	a.drain()
	c.SetTheme(nativeTheme(c.Theme()))
	if a.conversation != "" {
		a.prepareConversation()
		a.exchangeView(c)
		return
	}
	a.devicesView(c)
}

func (a *app) devicesView(c *ui.Context) {
	theme, style := c.Theme(), nativeStyles(c)
	ui.Column(c).Fill().Lang(a.t("en", "ja")).Font(uiFont(a.ja)).Background(theme.Background).Padding(theme.Space(4)).Gap(theme.Space(3)).TextColor(theme.Text).Children(func() {
		a.devicesHeader(c)
		ui.Text(c, a.modeLabel(false)).FontSize(style.caption).Bold().TextColor(theme.TextMuted)
		a.networkCard(c, a.networkPresentation())
		ui.TextInput(c, &a.query).Placeholder(a.t("Filter devices", "端末を絞り込む")).Label(a.t("Device filter", "端末を絞り込む")).FillWidth()
		a.deviceList(c, a.filteredPeers())
		if a.selected != "" {
			if ui.Button(c, a.t("Open messages / files", "メッセージ・ファイルを開く")).Clicked() {
				a.openConversation()
			}
		}
		a.devicesFooter(c)
	})
}
func (a *app) devicesHeader(c *ui.Context) {
	ui.Row(c).FillWidth().Gap(c.Theme().Space(2)).Children(func() {
		ui.Text(c, "soba").FontSize(nativeStyles(c).title).Bold().Grow(1)
		if ui.Button(c, a.t("日本語", "English")).Clicked() {
			a.toggleLocale()
		}
	})
}
func (a *app) networkCard(c *ui.Context, v networkPresentation) {
	theme, style := c.Theme(), nativeStyles(c)
	ui.Column(c).FillWidth().Padding(theme.Space(3)).Gap(theme.Space(1.25)).Background(style.status).Radius(style.cardRadius).Children(func() {
		ui.Row(c).FillWidth().Gap(theme.Space(2)).Children(func() {
			ui.Text(c, v.status).Bold().Grow(1)
			if !a.demo && ui.Button(c, v.refresh).Disabled(a.busy).Clicked() {
				a.refresh(c.Services().Invalidate)
			}
		})
		if v.count != "" {
			ui.Text(c, v.count).FontSize(style.body).TextColor(theme.TextMuted)
		}
		if v.help != "" {
			ui.Text(c, v.help).FontSize(style.body)
		}
		if v.warning != "" {
			ui.Text(c, v.warning).FontSize(style.body).TextColor(theme.Warning)
		}
	})
}
func (a *app) deviceList(c *ui.Context, peers []peer) {
	ui.Scroll(c).FillWidth().Grow(1).Children(func() {
		ui.Column(c).FillWidth().Gap(c.Theme().Space(1.75)).Children(func() {
			for _, p := range peers {
				a.deviceRow(c, a.devicePresentation(p))
			}
			if a.loaded && len(peers) == 0 {
				ui.Text(c, a.emptyDevicesLabel())
			}
		})
	})
}
func (a *app) deviceRow(c *ui.Context, v devicePresentation) {
	theme, style := c.Theme(), nativeStyles(c)
	row := ui.ButtonBase(c.Key(v.peer.ID)).FillWidth().Padding(theme.Space(2.75)).Background(theme.Surface).Border(style.rowBorder, theme.Border).Radius(style.cardRadius).Cursor(ui.CursorPointer).Role(ui.RoleButton).Label(v.peer.Name).Description(v.description).Expanded(v.expanded)
	if row.Hovered() {
		row.Background(theme.SurfaceHover)
	}
	if row.Focused() {
		row.BorderColor(theme.Focus)
	}
	if row.Clicked() {
		a.togglePeer(v.peer.ID)
	}
	row.Children(func() {
		ui.Column(c).FillWidth().Gap(theme.Space(1.75)).Children(func() {
			a.deviceSummary(c, v)
			if v.expanded {
				a.deviceDetails(c, v)
			}
		})
	})
}
func (a *app) deviceSummary(c *ui.Context, v devicePresentation) {
	theme, style := c.Theme(), nativeStyles(c)
	stateColor := theme.TextMuted
	if v.peer.Online {
		stateColor = theme.Success
	}
	ui.Row(c).FillWidth().Gap(theme.Space(2.5)).Children(func() {
		ui.Column(c).Grow(1).Gap(theme.Space(1)).Children(func() {
			ui.Text(c, v.peer.Name).FontSize(style.device).Bold()
			ui.Text(c, v.state).FontSize(style.body).TextColor(stateColor)
		})
		ui.Text(c, v.chevron).FontSize(style.chevron).TextColor(theme.TextMuted).Role(ui.RoleNone)
	})
}
func (a *app) deviceDetails(c *ui.Context, v devicePresentation) {
	theme, style := c.Theme(), nativeStyles(c)
	ui.Text(c, v.trust).FontSize(style.body).TextColor(theme.TextMuted)
	ui.Text(c, a.t("Service permission is separate. Application health is unverified.", "サービスの許可は別です。アプリの動作は未確認です。")).FontSize(style.body).TextColor(theme.TextMuted)
}
func (a *app) devicesFooter(c *ui.Context) {
	theme, style := c.Theme(), nativeStyles(c)
	if !a.updated.IsZero() {
		ui.Text(c, a.t("Last read: ", "最終取得: ")+a.updated.Format("15:04:05")).FontSize(style.caption)
	}
	ui.Row(c).FillWidth().Gap(theme.Space(2)).Children(func() {
		ui.Text(c, a.t("Native UI prototype", "Native UI 試作")).FontSize(style.caption).TextColor(theme.TextMuted).Grow(1)
		if ui.Button(c, a.aboutLabel()).Clicked() {
			a.toggleAbout()
		}
	})
	if a.about {
		ui.Text(c, a.t("No connection or permission changes. Sending requires --enable-send and review. Demo never sends.", "接続・許可は変更しません。実送信には --enable-send と確認が必要です。デモでは送信しません。")).FontSize(style.body).TextColor(theme.TextMuted)
	}
}
