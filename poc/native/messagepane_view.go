package main

import (
	"context"
	"github.com/egoist/mygo/ui"
)

func (p *messagePane) view(c *ui.Context, ctx context.Context, ja, demo, enabled bool, client *exchangeClient, peerID, peerName string, messages []exchangeMessage) {
	p.drain()
	t := translator(ja).text
	if p.takeAnnouncement() {
		c.Announce(t("Action status changed. Review the status below.", "操作状況が変わりました。下の結果を確認してください。"))
	}
	allowed := enabled && peerID != "" && (demo || client != nil)
	ui.Column(c).FillWidth().Gap(nativeStyles(c).itemGap).Children(func() {
		ui.Text(c, t("Messages", "メッセージ")).Bold()
		if demo {
			ui.Text(c, t("SIMULATED · No message is sent", "送信シミュレーション · 実際には送信しません")).FontSize(nativeStyles(c).caption)
		}
		p.historyView(c, ja, recentMessages(messages, peerID))
		if !enabled {
			ui.Text(c, t("Read only. Sending is disabled.", "読み取り専用です。送信は無効です。")).FontSize(nativeStyles(c).caption)
		}
		if p.review == nil {
			p.draftView(c, ja, allowed, peerID, peerName)
		} else {
			p.reviewView(c, ja, allowed, peerID, func() { p.send(ctx, c.Services().Invalidate, demo, enabled, client, peerID) })
		}
		p.statusView(c, ja)
	})
}
func (p *messagePane) historyView(c *ui.Context, ja bool, v messagePresentation) {
	t, style := translator(ja).text, nativeStyles(c)
	if len(v.entries) == 0 {
		ui.Text(c, t("No message history yet.", "メッセージ履歴はありません。")).FontSize(style.caption)
	}
	if v.truncated {
		ui.Text(c, t("Showing the latest 6 messages.", "最新6件を表示しています。")).FontSize(style.caption)
	}
	for _, m := range v.entries {
		messageRow(c, ja, m)
	}
}
func messageRow(c *ui.Context, ja bool, m exchangeMessage) {
	theme, style := c.Theme(), nativeStyles(c)
	ui.Column(c.Key(m.Direction + ":" + m.ID)).FillWidth().Padding(theme.Space(2)).Gap(theme.Space(.75)).Background(style.history).Radius(style.historyRadius).Children(func() {
		ui.Text(c, messageAuthor(m, ja)+" · "+exchangeStatus(m.Status, ja)).FontSize(style.metadata)
		ui.Text(c, m.Text).FillWidth().FontSize(style.body)
	})
}
func (p *messagePane) draftView(c *ui.Context, ja, allowed bool, peerID, peerName string) {
	t := translator(ja).text
	ui.TextArea(c, &p.draft).Label(t("Message draft", "メッセージ下書き")).Placeholder(t("Write a message…", "メッセージを入力…")).Lines(2, 4).FillWidth().ReadOnly(!allowed || !p.canEdit())
	if ui.Button(c, t("Review message", "送信内容を確認")).Disabled(!allowed || !p.canReview()).Clicked() {
		p.beginReview(peerID, peerName)
	}
}
func (p *messagePane) reviewView(c *ui.Context, ja, allowed bool, peerID string, confirm func()) {
	t, style := translator(ja).text, nativeStyles(c)
	r := p.review
	ui.Text(c, t("Review before sending", "送信前の確認")).Bold()
	ui.Text(c, t("To: ", "送信先: ")+r.peerName).FontSize(style.caption)
	ui.Text(c, r.peerID).FontSize(style.metadata).FillWidth()
	ui.Text(c, r.text).FillWidth().FontSize(style.body)
	ui.Row(c).Gap(style.actionGap).Children(func() {
		if ui.Button(c, t("Confirm message send", "メッセージ送信を確定")).Disabled(!allowed || !p.canSend(peerID)).Clicked() {
			confirm()
		}
		if ui.Button(c, t("Edit draft", "下書きに戻る")).Disabled(!p.canEdit()).Clicked() {
			p.editDraft()
		}
	})
}
func (p *messagePane) statusView(c *ui.Context, ja bool) {
	t, style := translator(ja).text, nativeStyles(c)
	if p.busy {
		ui.Text(c, t("Submitting…", "送信要求中…")).FontSize(style.caption)
	}
	if notice := p.noticeText(ja); notice != "" {
		ui.Text(c, notice).FillWidth().FontSize(style.caption)
	}
}
