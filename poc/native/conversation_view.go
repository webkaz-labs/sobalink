package main

import "github.com/egoist/mygo/ui"

func (a *app) exchangeView(c *ui.Context) {
	v := a.conversationPresentation()
	theme, style := c.Theme(), nativeStyles(c)
	ui.Column(c).Fill().Lang(a.t("en", "ja")).Font(uiFont(a.ja)).Background(theme.Background).TextColor(theme.Text).Padding(theme.Space(4)).Gap(theme.Space(2.5)).Children(func() {
		a.conversationHeader(c, v)
		ui.Text(c, v.mode).FontSize(style.caption).Bold()
		if !v.enabled {
			ui.Text(c, a.t("Sending is disabled. A trusted, unpaused device and --enable-send are required.", "送信は無効です。許可済みで一時停止していない端末と --enable-send が必要です。")).FontSize(style.caption)
		}
		a.conversationTabs(c)
		a.conversationContent(c, v)
		ui.Text(c, a.t("Refresh history to check the latest result. Acceptance is not proof of delivery.", "最新の結果は履歴を更新して確認してください。受付は到達の証明ではありません。")).FontSize(style.caption).TextColor(theme.TextMuted)
	})
}
func (a *app) conversationHeader(c *ui.Context, v conversationPresentation) {
	ui.Row(c).FillWidth().Gap(c.Theme().Space(2)).Children(func() {
		if ui.Button(c, a.t("Back", "戻る")).Clicked() {
			a.closeConversation()
		}
		ui.Text(c, v.name).Bold().Grow(1)
		if !a.demo && ui.Button(c, a.t("Refresh", "更新")).Disabled(a.busy).Clicked() {
			a.refresh(c.Services().Invalidate)
		}
	})
}
func (a *app) conversationTabs(c *ui.Context) {
	ui.Row(c).Gap(c.Theme().Space(2)).Children(func() {
		if ui.Button(c, a.t("Messages", "メッセージ")).Clicked() {
			a.showMessages()
		}
		if ui.Button(c, a.t("Files", "ファイル")).Clicked() {
			a.showFiles()
		}
	})
}
func (a *app) conversationContent(c *ui.Context, v conversationPresentation) {
	id := v.id
	// Keep the original conversation key when a refreshed device disappears;
	// its retained draft stays isolated, and the pane receives disabled state.
	ui.Scroll(c).FillWidth().Grow(1).Children(func() {
		if a.filesTab {
			a.files[id].view(c, a.ctx, a.ja, a.demo, v.enabled, a.exchange, a.state.ResourceCatalogProcessID, id, a.state.Transfers)
		} else {
			a.messages[id].view(c, a.ctx, a.ja, a.demo, v.enabled, a.exchange, id, v.target.Name, a.state.Messages)
		}
	})
}
