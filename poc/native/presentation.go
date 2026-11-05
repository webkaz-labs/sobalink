package main

import (
	"fmt"
	"strings"
)

type translator bool

func (ja translator) text(en, jp string) string {
	if ja {
		return jp
	}
	return en
}

func (a *app) modeLabel(exchange bool) string {
	if a.demo {
		if exchange {
			return a.t("DEMO · SIMULATED EXCHANGE", "デモ · やり取りはシミュレーション")
		}
		return a.t("DEMO · FICTIONAL DATA", "デモ · 架空のデータ")
	}
	if a.sendEnabled {
		return a.t("LIVE IPC · REVIEW BEFORE SENDING", "実IPC · 送信前に確認")
	}
	return a.t("LIVE IPC · READ ONLY", "実IPC · 読み取り専用")
}

type networkPresentation struct {
	status, count, help, warning, refresh string
}

func (a *app) networkPresentation() networkPresentation {
	v := networkPresentation{status: a.t("Not read yet", "未取得"), refresh: a.t("Refresh", "更新")}
	if a.busy {
		v.refresh = a.t("Reading…", "取得中…")
	}
	if a.loaded {
		v.status = a.state.Self.Status
		switch v.status {
		case "running", "ready":
			v.status = a.t("Network running", "ネットワーク稼働中")
		case "offline":
			v.status = a.t("Offline", "オフライン")
		}
		online := 0
		for _, p := range a.state.Peers {
			if p.Online {
				online++
			}
		}
		v.count = fmt.Sprintf(a.t("%d online / %d devices", "オンライン %d / %d 台"), online, len(a.state.Peers))
		if a.state.Self.Status != "running" && a.state.Self.Status != "ready" {
			v.warning = a.t("Review network state in Web UI or CLI. IPC access does not prove connectivity.", "Web UI / CLI で状態を確認してください。IPC接続は通信成功を示しません。")
		}
	}
	if a.failed {
		v.status = a.t("Status unavailable", "取得できません")
		v.help = a.t("Check the profile and running daemon, then retry.", "同じプロファイルで本体の起動を確認して再試行してください。")
	} else if !a.loaded {
		v.help = a.t("Refresh reads the existing daemon. Nothing starts automatically.", "更新で既存の本体を読み取ります。自動起動はしません。")
	}
	return v
}

type devicePresentation struct {
	peer                               peer
	description, state, trust, chevron string
	expanded                           bool
}

func (a *app) devicePresentation(p peer) devicePresentation {
	online := a.t("Offline", "オフライン")
	if p.Online {
		online = a.t("Online", "オンライン")
	}
	route := a.t("Path unknown", "経路不明")
	switch p.Path {
	case "direct":
		route = a.t("Direct", "直接")
	case "relay":
		route = a.t("Relay", "中継")
	}
	v := devicePresentation{peer: p, description: a.peerDescription(p), state: online + " · " + route, expanded: a.selected == p.ID, chevron: "›", trust: a.t("Messages/files: not trusted", "メッセージ・ファイル: 未許可")}
	if v.expanded {
		v.chevron = "⌄"
	}
	if p.Trusted {
		v.trust = a.t("Messages/files: trusted", "メッセージ・ファイル: 許可済み")
	}
	return v
}
func (a *app) filteredPeers() []peer {
	query := strings.ToLower(a.query)
	var peers []peer
	for _, p := range a.state.Peers {
		if strings.Contains(strings.ToLower(p.Name), query) {
			peers = append(peers, p)
		}
	}
	return peers
}
func (a *app) emptyDevicesLabel() string {
	if len(a.state.Peers) == 0 {
		return a.t("No devices yet.", "端末はまだありません。")
	}
	return a.t("No matching devices.", "該当する端末はありません。")
}
func (a *app) aboutLabel() string {
	if a.about {
		return a.t("Close info", "説明を閉じる")
	}
	return a.t("About", "この試作について")
}

type conversationPresentation struct {
	target         peer
	id, name, mode string
	enabled        bool
}

func (a *app) conversationPresentation() conversationPresentation {
	v := conversationPresentation{id: a.conversation, name: a.t("Device unavailable", "端末を取得できません"), mode: a.modeLabel(true)}
	for _, p := range a.state.Peers {
		if p.ID == a.conversation {
			v.target = p
			break
		}
	}
	if v.target.Name != "" {
		v.name = v.target.Name
	}
	v.enabled = (a.demo || a.sendEnabled) && a.loaded && !a.failed && v.target.Trusted && !v.target.Autosave.Paused
	return v
}
