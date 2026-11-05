package main

import (
	"path"
	"strings"
)

type fileText func(en, ja string) string

func fileLabel(p string) string { return path.Base(strings.ReplaceAll(p, "\\", "/")) }
func (p *filePane) noticeText(demo bool, t fileText) string {
	switch p.notice {
	case "uncertain":
		return t("Outcome unknown. Do not resend. Check status or refresh history.", "結果が不明です。再送せず、状態または履歴を確認してください。")
	case "failed":
		return t("Could not complete this step. No automatic retry.", "この操作を完了できませんでした。自動再試行はしません。")
	case "submitted":
		if demo {
			return t("Simulation complete. No file was sent or received.", "シミュレーション完了です。実際のファイル送受信は行っていません。")
		}
		return t("Command accepted. Refresh history to verify progress; delivery is not confirmed.", "操作を受け付けました。履歴を更新して進行状況を確認してください。配信完了は未確認です。")
	case "prepared":
		return t("Staging is prepared. An earlier uncertain action is still locked; inspect in Web UI or CLI.", "準備済みです。結果不明の操作はロックしたままです。Web UI / CLI で確認してください。")
	case "discarded":
		return t("Staging discarded.", "送信準備を破棄しました。")
	}
	return ""
}
func dispatchChildStatus(child fileChild, a fileActions, t fileText, ja bool) string {
	status := t("Not yet in refreshed history", "更新した履歴に未反映")
	if a.demo {
		status = t("Simulated", "シミュレーション")
	}
	for _, v := range a.transfers {
		if v.Direction == "outgoing" && v.ID == child.TransferID && v.PeerID == child.PeerID {
			return exchangeStatus(v.Status, ja)
		}
	}
	return status
}
func visibleFileTransfers(a fileActions) []exchangeTransfer {
	var visible []exchangeTransfer
	for _, v := range a.transfers {
		if a.peer == "" || v.PeerID == a.peer {
			visible = append(visible, v)
		}
	}
	return visible
}
