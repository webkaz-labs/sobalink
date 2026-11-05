package main

import (
	"fmt"
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
