package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
)

func (a *app) peerDescription(p peer) string {
	online := a.t("Offline", "オフライン")
	if p.Online {
		online = a.t("Online", "オンライン")
	}
	trust := a.t("Not trusted", "未許可")
	if p.Trusted {
		trust = a.t("Trusted", "許可済み")
	}
	if p.Autosave.Paused {
		trust = a.t("Paused", "一時停止")
	}
	return fmt.Sprintf("%s, %s, %s. %s", online, p.Path, trust, a.t("Show or hide details", "詳細を開閉"))
}
func (a *app) exchangeView(c *ui.Context) {
	a.drain()
	id := a.conversation
	var target peer
	for _, p := range a.state.Peers {
		if p.ID == id {
			target = p
			break
		}
	}
	enabled := (a.demo || a.sendEnabled) && a.loaded && !a.failed && target.Trusted && !target.Autosave.Paused
	if a.messages == nil {
		a.messages = map[string]*messagePane{}
	}
	if a.files == nil {
		a.files = map[string]*filePane{}
	}
	if a.messages[id] == nil {
		a.messages[id] = &messagePane{}
	}
	if a.files[id] == nil {
		a.files[id] = &filePane{}
	}
	ui.Column(c).Fill().Lang(a.t("en", "ja")).Font(uiFont(a.ja)).Background(ui.Hex("#f4f7f5")).TextColor(ui.Hex("#172e34")).Padding(16).Gap(10).Children(func() {
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			if ui.Button(c, a.t("Back", "戻る")).Clicked() {
				a.conversation = ""
			}
			name := target.Name
			if name == "" {
				name = a.t("Device unavailable", "端末を取得できません")
			}
			ui.Text(c, name).Bold().Grow(1)
			if !a.demo && ui.Button(c, a.t("Refresh", "更新")).Disabled(a.busy).Clicked() {
				a.refresh(c.Services().Invalidate)
			}
		})
		mode := a.t("LIVE IPC · READ ONLY", "実IPC · 読み取り専用")
		if a.sendEnabled {
			mode = a.t("LIVE IPC · REVIEW BEFORE SENDING", "実IPC · 送信前に確認")
		}
		if a.demo {
			mode = a.t("DEMO · SIMULATED EXCHANGE", "デモ · やり取りはシミュレーション")
		}
		ui.Text(c, mode).FontSize(12).Bold()
		if !enabled {
			ui.Text(c, a.t("Sending is disabled. A trusted, unpaused device and --enable-send are required.", "送信は無効です。許可済みで一時停止していない端末と --enable-send が必要です。")).FontSize(12)
		}
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, a.t("Messages", "メッセージ")).Clicked() {
				a.filesTab = false
			}
			if ui.Button(c, a.t("Files", "ファイル")).Clicked() {
				a.filesTab = true
			}
		})
		ui.Scroll(c).FillWidth().Grow(1).Children(func() {
			if a.filesTab {
				a.files[id].view(c, a.ctx, a.ja, a.demo, enabled, a.exchange, a.state.ResourceCatalogProcessID, id, a.state.Transfers)
			} else {
				a.messages[id].view(c, a.ctx, a.ja, a.demo, enabled, a.exchange, id, target.Name, a.state.Messages)
			}
		})
		ui.Text(c, a.t("Refresh history to check the latest result. Acceptance is not proof of delivery.", "最新の結果は履歴を更新して確認してください。受付は到達の証明ではありません。")).FontSize(12).TextColor(ui.Hex("#52666a"))
	})
}

func exchangeStatus(s string, ja bool) string {
	if !ja {
		return s
	}
	switch s {
	case "awaiting-acceptance":
		return "受信確認待ち"
	case "received":
		return "受信済み"
	case "sent":
		return "送信済み"
	case "sending":
		return "送信中"
	case "receiving":
		return "受信中"
	case "completed":
		return "完了"
	case "failed":
		return "失敗"
	case "declined":
		return "辞退"
	case "cancelled":
		return "キャンセル"
	default:
		return s
	}
}
