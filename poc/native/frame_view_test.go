package main

import (
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func nativeTestFrame(a *app, mode string) func(*ui.Context) {
	s := &nativeShell{}
	switch mode {
	case "tray":
		return func(c *ui.Context) { s.view(a, c) }
	case "fallback":
		return func(c *ui.Context) { s.fallbackView(a, c) }
	default:
		return a.view
	}
}

func TestNativeFrameNavigationRetainsReview(t *testing.T) {
	for _, mode := range []string{"normal", "tray", "fallback"} {
		for _, ja := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-ja-%t", mode, ja), func(t *testing.T) {
				a := demoApp()
				a.ja, a.selected = ja, "demo-alpha"
				tt := ui.NewTester(nativeTestFrame(a, mode), 460, 640)
				click := func(label string) {
					t.Helper()
					if err := tt.Click(label); err != nil {
						t.Fatal(err)
					}
				}
				click(a.t("日本語", "English"))
				if a.ja == ja {
					t.Fatal("device header language action lost")
				}
				click(a.t("Open messages / files", "メッセージ・ファイルを開く"))
				p := a.messages["demo-alpha"]
				p.draft = "Fictional pending review"
				p.beginReview("demo-alpha", "Demo laptop")
				review := p.review
				for range 2 {
					click(a.t("Files", "ファイル"))
					click(a.t("Back", "戻る"))
					if a.conversation != "" || !tt.HasText("soba") {
						t.Fatal("conversation header did not return to devices")
					}
					click(a.t("Open messages / files", "メッセージ・ファイルを開く"))
					click(a.t("Messages", "メッセージ"))
					if p.review != review || p.draft != "Fictional pending review" || !tt.HasText(p.draft) {
						t.Fatal("navigation lost the pending review")
					}
				}
				if tt.HasText(a.t("Hide", "隠す")) != (mode == "tray") || tt.HasText(a.t("Quit UI (daemon continues)", "UIを終了（本体は継続）")) != (mode == "tray") {
					t.Fatal("window actions leaked into a different frame")
				}
			})
		}
	}
}

func TestNativeFrameDrainsStatusResults(t *testing.T) {
	for _, mode := range []string{"normal", "tray", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			a := demoApp()
			a.demo, a.busy = false, true
			a.results = make(chan result, 1)
			s := demoSnapshot()
			s.Peers[0].Name = "Updated fictional device"
			a.results <- result{state: s}
			tt := ui.NewTester(nativeTestFrame(a, mode), 460, 640)
			if a.busy || !a.loaded || !tt.HasText("Updated fictional device") {
				t.Fatal("status completion not applied by frame")
			}
			a.results <- result{err: errors.New("fixture unavailable")}
			tt.Frame()
			if !a.failed || a.loaded || tt.HasText("Updated fictional device") {
				t.Fatal("failed status retained stale screen contents")
			}
		})
	}
}

func TestNativeShellFrameAppearance(t *testing.T) {
	for _, mode := range []string{"tray", "fallback"} {
		for _, ja := range []bool{false, true} {
			for _, dark := range []bool{false, true} {
				for _, pane := range []string{"devices", "messages", "files"} {
					name := fmt.Sprintf("%s-%s-dark-%t-ja-%t", mode, pane, dark, ja)
					t.Run(name, func(t *testing.T) {
						a := demoApp()
						a.ja = ja
						if pane != "devices" {
							a.conversation, a.filesTab = "demo-alpha", pane == "files"
						}
						tt := ui.NewTester(nativeTestFrame(a, mode), 460, 640)
						tt.SetDark(dark)
						tt.SetPreferences(ui.Preferences{TextScale: 1.5})
						labels := []string{"soba", a.t("日本語", "English")}
						if pane != "devices" {
							labels = []string{"Demo laptop", a.t("Back", "戻る"), a.t("Messages", "メッセージ"), a.t("Files", "ファイル")}
						}
						if mode == "tray" {
							labels = append(labels, a.t("Hide", "隠す"), a.t("Quit UI (daemon continues)", "UIを終了（本体は継続）"))
						} else {
							labels = append(labels, a.t("Tray unavailable. Using a normal window; close it to quit the UI. The daemon is unchanged.", "トレイを利用できないため通常表示です。ウィンドウを閉じるとUIを終了します。本体の状態は変わりません。"))
						}
						for _, label := range labels {
							assertNativeControlVisible(t, tt, label, 460, 640)
						}
						if out := os.Getenv("SOBA_POC_THEME_OUTPUT"); out != "" {
							f, err := os.Create(filepath.Join(out, name+".png"))
							if err != nil {
								t.Fatal(err)
							}
							defer f.Close()
							if err := png.Encode(f, tt.Image()); err != nil {
								t.Fatal(err)
							}
						}
					})
				}
			}
		}
	}
}
