package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

type app struct {
	sendEnabled, filesTab                 bool
	conversation                          string
	exchange                              *exchangeClient
	messages                              map[string]*messagePane
	files                                 map[string]*filePane
	updated                               time.Time
	ja, demo, loaded, busy, failed, about bool
	query, selected                       string
	state                                 snapshot
	ctx                                   context.Context
	load                                  loader
	results                               chan result
}

func (a *app) t(en, ja string) string {
	if a.ja {
		return ja
	}
	return en
}
func (a *app) refresh(wake func()) {
	if a.demo || a.busy || a.load == nil {
		return
	}
	a.busy = true
	a.failed = false
	go func() {
		s, err := a.load(a.ctx)
		select {
		case a.results <- result{s, err}:
			wake()
		case <-a.ctx.Done():
		}
	}()
}
func (a *app) drain() {
	select {
	case r := <-a.results:
		a.busy = false
		a.failed = r.err != nil
		a.loaded = r.err == nil
		if r.err == nil {
			a.state = r.state
			a.updated = time.Now()
		} else {
			a.state = snapshot{}
			a.selected = ""
		}
	default:
	}
}
func (a *app) view(c *ui.Context) {
	a.drain()
	if a.conversation != "" {
		a.exchangeView(c)
		return
	}
	ink, muted := ui.Hex("#172e34"), ui.Hex("#52666a")
	line, accent := ui.Hex("#d8e3e0"), ui.Hex("#26745b")
	ui.Column(c).Fill().Lang(a.t("en", "ja")).Font(uiFont(a.ja)).Background(ui.Hex("#f4f7f5")).Padding(16).Gap(12).TextColor(ink).Children(func() {
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			ui.Text(c, "soba").FontSize(25).Bold().Grow(1)
			if ui.Button(c, a.t("日本語", "English")).Clicked() {
				a.ja = !a.ja
			}
		})
		mode := a.t("LIVE IPC · READ ONLY", "実IPC · 読み取り専用")
		if a.sendEnabled {
			mode = a.t("LIVE IPC · REVIEW BEFORE SENDING", "実IPC · 送信前に確認")
		}
		if a.demo {
			mode = a.t("DEMO · FICTIONAL DATA", "デモ · 架空のデータ")
		}
		ui.Text(c, mode).FontSize(12).Bold().TextColor(muted)
		ui.Column(c).FillWidth().Padding(12).Gap(5).Background(ui.Hex("#e2efea")).Radius(9).Children(func() {
			status := a.t("Not read yet", "未取得")
			if a.loaded {
				status = a.state.Self.Status
				switch status {
				case "running", "ready":
					status = a.t("Network running", "ネットワーク稼働中")
				case "offline":
					status = a.t("Offline", "オフライン")
				}
			}
			if a.failed {
				status = a.t("Status unavailable", "取得できません")
			}
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				ui.Text(c, status).Bold().Grow(1)
				if !a.demo {
					label := a.t("Refresh", "更新")
					if a.busy {
						label = a.t("Reading…", "取得中…")
					}
					if ui.Button(c, label).Disabled(a.busy).Clicked() {
						a.refresh(c.Services().Invalidate)
					}
				}
			})
			if a.loaded {
				online := 0
				for _, p := range a.state.Peers {
					if p.Online {
						online++
					}
				}
				ui.Text(c, fmt.Sprintf(a.t("%d online / %d devices", "オンライン %d / %d 台"), online, len(a.state.Peers))).FontSize(13).TextColor(muted)
			}
			if a.failed {
				ui.Text(c, a.t("Check the profile and running daemon, then retry.", "同じプロファイルで本体の起動を確認して再試行してください。")).FontSize(13)
			} else if !a.loaded {
				ui.Text(c, a.t("Refresh reads the existing daemon. Nothing starts automatically.", "更新で既存の本体を読み取ります。自動起動はしません。")).FontSize(13)
			}
			if a.loaded && a.state.Self.Status != "running" && a.state.Self.Status != "ready" {
				ui.Text(c, a.t("Review network state in Web UI or CLI. IPC access does not prove connectivity.", "Web UI / CLI で状態を確認してください。IPC接続は通信成功を示しません。")).FontSize(13).TextColor(ui.Hex("#9a4427"))
			}
		})
		ui.TextInput(c, &a.query).Placeholder(a.t("Filter devices", "端末を絞り込む")).Label(a.t("Device filter", "端末を絞り込む")).FillWidth()
		ui.Scroll(c).FillWidth().Grow(1).Children(func() {
			ui.Column(c).FillWidth().Gap(7).Children(func() {
				n := 0
				for _, p := range a.state.Peers {
					if !strings.Contains(strings.ToLower(p.Name), strings.ToLower(a.query)) {
						continue
					}
					n++
					row := ui.ButtonBase(c.Key(p.ID)).FillWidth().Padding(11).Background(ui.Hex("#ffffff")).Border(1.5, line).Radius(9).Cursor(ui.CursorPointer).Role(ui.RoleButton).Label(p.Name).Description(a.peerDescription(p)).Expanded(a.selected == p.ID)
					if row.Hovered() {
						row.Background(ui.Hex("#edf5f1"))
					}
					if row.Focused() {
						row.BorderColor(accent)
					}
					if row.Clicked() {
						if a.selected == p.ID {
							a.selected = ""
						} else {
							a.selected = p.ID
						}
					}
					row.Children(func() {
						ui.Column(c).FillWidth().Gap(7).Children(func() {
							ui.Row(c).FillWidth().Gap(10).Children(func() {
								ui.Column(c).Grow(1).Gap(4).Children(func() {
									ui.Text(c, p.Name).FontSize(16).Bold()
									online := a.t("Offline", "オフライン")
									color := muted
									if p.Online {
										online = a.t("Online", "オンライン")
										color = accent
									}
									path := a.t("Path unknown", "経路不明")
									if p.Path == "direct" {
										path = a.t("Direct", "直接")
									}
									if p.Path == "relay" {
										path = a.t("Relay", "中継")
									}
									ui.Text(c, online+" · "+path).FontSize(13).TextColor(color)
								})
								chevron := "›"
								if a.selected == p.ID {
									chevron = "⌄"
								}
								ui.Text(c, chevron).FontSize(22).TextColor(muted).Role(ui.RoleNone)
							})
							if a.selected == p.ID {
								trust := a.t("Messages/files: not trusted", "メッセージ・ファイル: 未許可")
								if p.Trusted {
									trust = a.t("Messages/files: trusted", "メッセージ・ファイル: 許可済み")
								}
								ui.Text(c, trust).FontSize(13).TextColor(muted)
								ui.Text(c, a.t("Service permission is separate. Application health is unverified.", "サービスの許可は別です。アプリの動作は未確認です。")).FontSize(13).TextColor(muted)
							}
						})
					})
				}
				if a.loaded && n == 0 {
					label := a.t("No matching devices.", "該当する端末はありません。")
					if len(a.state.Peers) == 0 {
						label = a.t("No devices yet.", "端末はまだありません。")
					}
					ui.Text(c, label)
				}
			})
		})
		if a.selected != "" {
			if ui.Button(c, a.t("Open messages / files", "メッセージ・ファイルを開く")).Clicked() {
				a.conversation = a.selected
			}
		}
		if !a.updated.IsZero() {
			ui.Text(c, a.t("Last read: ", "最終取得: ")+a.updated.Format("15:04:05")).FontSize(12)
		}
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			ui.Text(c, a.t("Native UI prototype", "Native UI 試作")).FontSize(12).TextColor(muted).Grow(1)
			label := a.t("About", "この試作について")
			if a.about {
				label = a.t("Close info", "説明を閉じる")
			}
			if ui.Button(c, label).Clicked() {
				a.about = !a.about
			}
		})
		if a.about {
			ui.Text(c, a.t("No connection or permission changes. Sending requires --enable-send and review. Demo never sends.", "接続・許可は変更しません。実送信には --enable-send と確認が必要です。デモでは送信しません。")).FontSize(13).TextColor(muted)
		}
	})
}

// Use Japanese faces for the entire Japanese text run, including punctuation.
// A locale tag alone does not constrain a system's generic-font fallback.
// These are installed system-font candidates, not bundled font assets.
func uiFont(japanese bool) string {
	if !japanese {
		return ""
	}
	return "Hiragino Sans, Yu Gothic UI, Meiryo, Noto Sans CJK JP, Noto Sans JP"
}
