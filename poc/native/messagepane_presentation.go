package main

import (
	"fmt"

	"github.com/webkaz-labs/sobalink/internal/messageframe"
)

type messagePresentation struct {
	entries   []exchangeMessage
	truncated bool
}

func recentMessages(messages []exchangeMessage, peerID string) messagePresentation {
	var v messagePresentation
	for _, m := range messages {
		if m.PeerID == peerID {
			v.entries = append(v.entries, m)
		}
	}
	v.truncated = len(v.entries) > 6
	if v.truncated {
		v.entries = v.entries[len(v.entries)-6:]
	}
	return v
}
func messageAuthor(m exchangeMessage, ja bool) string {
	t := translator(ja).text
	if m.Direction == "outgoing" {
		return t("You", "自分")
	}
	return t("Received", "受信")
}
func (p *messagePane) noticeText(ja bool) string {
	t := translator(ja).text
	switch p.notice {
	case "too_large":
		return fmt.Sprintf(t("Nothing was sent. Shorten the draft to this UI’s limit of %d UTF-8 bytes, then review it again.", "送信していません。この画面の上限（UTF-8で%dバイト）以下に下書きを短くして、もう一度確認してください。"), messageframe.TextBytes)
	case "configured_too_large":
		return t("Core rejected the message before sending because it exceeds the configured byte limit. Edit and shorten the draft, then review it again.", "設定されたバイト数の上限を超えたため、Coreが送信前に拒否しました。下書きに戻って短くし、もう一度確認してください。")
	case "uncertain":
		return t("Outcome unknown. Do not resend. Refresh history and check with the recipient. The draft and request are retained.", "結果を確認できません。再送せず、履歴を更新して相手に確認してください。下書きと要求を保持しています。")
	case "submitted":
		return t("Request completed. Refresh history to check its recorded status.", "要求が完了しました。履歴を更新して記録された状態を確認してください。")
	case "simulated":
		return t("Simulation complete. Nothing was sent.", "シミュレーション完了。実際には送信していません。")
	default:
		return ""
	}
}
