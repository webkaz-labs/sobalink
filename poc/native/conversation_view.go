package main

import "github.com/egoist/mygo/ui"

func (a *app) exchangeView(c *ui.Context) {
	v := a.conversationPresentation()
	theme, style := c.Theme(), nativeStyles(c)
	nativeScreen(c, style.paneGap, func() {
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
	nativeHeader(c, v.name, c.Theme().FontSize, func() {
		if nativeIconButton(c, "back", a.t("Back", "戻る")).Clicked() {
			a.closeConversation()
		}
	}, func() {
		if !a.demo && nativeIconButton(c, "refresh", a.t("Refresh", "更新")).Disabled(a.busy).Clicked() {
			a.refresh(c.Services().Invalidate)
		}
	})
}
func (a *app) conversationTabs(c *ui.Context) {
	selected := a.conversationTab()
	if ui.Tabs(c, &selected, a.t("Messages", "メッセージ"), a.t("Files", "ファイル")).Changed() {
		a.selectConversationTab(selected)
	}
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
