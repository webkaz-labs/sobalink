package main

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// A pane belongs to one peer. Reviewed values are immutable until explicitly
// edited, and a possibly delivered request cannot be retried from this pane.
type messagePane struct {
	announced       string
	draft           string
	review          *messageReview
	busy, uncertain bool
	notice          string
	events          chan messagePaneEvent
}
type messageReview struct {
	requestID, peerID, peerName, text string
	attempted                         bool
}
type messagePaneEvent struct {
	requestID string
	simulated bool
	err       error
}

func (p *messagePane) beginReview(peerID, peerName string) {
	if p.busy || p.uncertain || p.review != nil || peerID == "" || strings.TrimSpace(p.draft) == "" || !utf8.ValidString(p.draft) {
		return
	}
	p.review = &messageReview{requestID: newExchangeRequestID(), peerID: peerID, peerName: peerName, text: p.draft}
	p.notice = ""
}
func (p *messagePane) send(ctx context.Context, wake func(), demo, enabled bool, client *exchangeClient, peerID string) {
	if !enabled || p.busy || p.uncertain || p.review == nil || p.review.attempted || p.review.peerID != peerID || ctx.Err() != nil || !demo && client == nil {
		return
	}
	r := *p.review
	p.review.attempted = true
	p.busy = true
	if p.events == nil {
		p.events = make(chan messagePaneEvent, 1)
	}
	events := p.events
	go func() {
		e := messagePaneEvent{requestID: r.requestID, simulated: demo}
		if !demo {
			_, e.err = client.SendMessage(ctx, r.requestID, r.peerID, r.text)
		}
		// The bounded channel preserves completion even when the window is hidden.
		events <- e
		if ctx.Err() == nil && wake != nil {
			wake()
		}
	}()
}
func (p *messagePane) drain() {
	if p.events == nil {
		return
	}
	select {
	case e := <-p.events:
		p.busy = false
		if p.review == nil || p.review.requestID != e.requestID {
			p.uncertain = true
			p.notice = "uncertain"
			return
		}
		if e.err != nil {
			p.uncertain = true
			p.notice = "uncertain"
			return
		}
		p.notice = "submitted"
		if e.simulated {
			p.notice = "simulated"
		}
		p.draft = ""
		p.review = nil
	default:
	}
}
func (p *messagePane) view(c *ui.Context, ctx context.Context, ja, demo, enabled bool, client *exchangeClient, peerID, peerName string, messages []exchangeMessage) {
	p.drain()
	t := func(en, jp string) string {
		if ja {
			return jp
		}
		return en
	}
	announcement := p.notice
	if p.busy {
		announcement = "busy"
	}
	if announcement != p.announced {
		p.announced = announcement
		if announcement != "" {
			c.Announce(t("Action status changed. Review the status below.", "操作状況が変わりました。下の結果を確認してください。"))
		}
	}
	allowed := enabled && peerID != "" && (demo || client != nil)
	ui.Column(c).FillWidth().Gap(7).Children(func() {
		ui.Text(c, t("Messages", "メッセージ")).Bold()
		if demo {
			ui.Text(c, t("SIMULATED · No message is sent", "送信シミュレーション · 実際には送信しません")).FontSize(12)
		}
		count := 0
		for _, m := range messages {
			if m.PeerID == peerID {
				count++
			}
		}
		if count == 0 {
			ui.Text(c, t("No message history yet.", "メッセージ履歴はありません。")).FontSize(12)
		}
		if count > 6 {
			ui.Text(c, t("Showing the latest 6 messages.", "最新6件を表示しています。")).FontSize(12)
		}
		skip := max(0, count-6)
		for _, m := range messages {
			if m.PeerID != peerID {
				continue
			}
			if skip > 0 {
				skip--
				continue
			}
			who := t("Received", "受信")
			if m.Direction == "outgoing" {
				who = t("You", "自分")
			}
			ui.Column(c).FillWidth().Padding(8).Gap(3).Background(ui.Hex("#edf3f0")).Radius(6).Children(func() {
				ui.Text(c, who+" · "+exchangeStatus(m.Status, ja)).FontSize(11)
				ui.Text(c, m.Text).FillWidth().FontSize(13)
			})
		}
		if !enabled {
			ui.Text(c, t("Read only. Sending is disabled.", "読み取り専用です。送信は無効です。")).FontSize(12)
		}
		if p.review == nil {
			ui.TextArea(c, &p.draft).Label(t("Message draft", "メッセージ下書き")).Placeholder(t("Write a message…", "メッセージを入力…")).Lines(2, 4).FillWidth().ReadOnly(!allowed || p.busy || p.uncertain)
			if ui.Button(c, t("Review message", "送信内容を確認")).Disabled(!allowed || p.busy || p.uncertain || strings.TrimSpace(p.draft) == "").Clicked() {
				p.beginReview(peerID, peerName)
			}
		} else {
			r := p.review
			ui.Text(c, t("Review before sending", "送信前の確認")).Bold()
			ui.Text(c, t("To: ", "送信先: ")+r.peerName).FontSize(12)
			ui.Text(c, r.peerID).FontSize(11).FillWidth()
			ui.Text(c, r.text).FillWidth().FontSize(13)
			ui.Row(c).Gap(6).Children(func() {
				if ui.Button(c, t("Confirm message send", "メッセージ送信を確定")).Disabled(!allowed || p.busy || p.uncertain || r.attempted || r.peerID != peerID).Clicked() {
					p.send(ctx, c.Services().Invalidate, demo, enabled, client, peerID)
				}
				if ui.Button(c, t("Edit draft", "下書きに戻る")).Disabled(p.busy || p.uncertain || r.attempted).Clicked() {
					p.review = nil
				}
			})
		}
		if p.busy {
			ui.Text(c, t("Submitting…", "送信要求中…")).FontSize(12)
		}
		switch p.notice {
		case "uncertain":
			ui.Text(c, t("Outcome unknown. Do not resend. Refresh history and check with the recipient. The draft and request are retained.", "結果を確認できません。再送せず、履歴を更新して相手に確認してください。下書きと要求を保持しています。")).FillWidth().FontSize(12)
		case "submitted":
			ui.Text(c, t("Request completed. Refresh history to check its recorded status.", "要求が完了しました。履歴を更新して記録された状態を確認してください。")).FillWidth().FontSize(12)
		case "simulated":
			ui.Text(c, t("Simulation complete. Nothing was sent.", "シミュレーション完了。実際には送信していません。")).FontSize(12)
		}
	})
}
