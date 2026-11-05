package main

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
