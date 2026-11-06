package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNativeScreensAppearance(t *testing.T) {
	for _, preset := range []struct {
		name        string
		preferences ui.Preferences
	}{
		{"default", ui.Preferences{}},
		{"high-contrast", ui.Preferences{HighContrast: true}},
		{"large-text", ui.Preferences{TextScale: 1.5}},
		{"desktop-accent", ui.Preferences{Accent: ui.Hex("#facc15")}},
	} {
		for _, dark := range []bool{false, true} {
			for _, ja := range []bool{false, true} {
				for _, pane := range []string{"devices", "messages", "files"} {
					name := fmt.Sprintf("%s-%s-dark-%t-ja-%t", pane, preset.name, dark, ja)
					t.Run(name, func(t *testing.T) {
						a := demoApp()
						a.ja = ja
						if pane != "devices" {
							a.conversation, a.filesTab = "demo-alpha", pane == "files"
						}
						tt := ui.NewTester(a.view, 360, 560)
						tt.SetDark(dark)
						tt.SetPreferences(preset.preferences)
						tt.SetScale(2)
						for i := 0; i < 3; i++ {
							tt.Frame()
						}
						labels := []string{"sobalink.", a.t("Device filter", "端末を絞り込む"), a.t("About", "この試作について")}
						if pane != "devices" {
							labels = []string{"Demo laptop", a.t("Back", "戻る"), a.t("Messages", "メッセージ"), a.t("Files", "ファイル")}
						}
						for _, label := range labels {
							assertNativeControlVisible(t, tt, label, 360, 560)
						}
						img := tt.Image()
						if img.Bounds().Dx() != 720 || img.Bounds().Dy() != 1120 {
							t.Fatal("scale not retained")
						}
						if out := os.Getenv("SOBA_POC_THEME_OUTPUT"); out != "" {
							f, err := os.Create(filepath.Join(out, name+".png"))
							if err != nil {
								t.Fatal(err)
							}
							defer f.Close()
							if err := png.Encode(f, img); err != nil {
								t.Fatal(err)
							}
						}
					})
				}
			}
		}
	}
}

func assertNativeControlVisible(t *testing.T, tt *ui.Tester, label string, width, height float32) {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > width || r.Y+r.H > height {
		t.Fatalf("control %q not visible: %+v", label, r)
	}
}
func TestMessageReviewLargeTextReachability(t *testing.T) {
	for _, scale := range []float32{1.5, 2} {
		for _, ja := range []bool{false, true} {
			a := demoApp()
			a.ja, a.conversation = ja, "demo-alpha"
			tt := ui.NewTester(a.view, 360, 560)
			tt.SetPreferences(ui.Preferences{TextScale: scale})
			p := a.messages[a.conversation]
			p.draft = "Fictional review text"
			tt.Frame()
			tt.Scroll(180, 320, 0, 2000)
			assertNativeControlVisible(t, tt, a.t("Review message", "送信内容を確認"), 360, 560)
			if err := tt.Click(a.t("Review message", "送信内容を確認")); err != nil {
				t.Fatal(err)
			}
			tt.Scroll(180, 320, 0, 2000)
			assertNativeControlVisible(t, tt, a.t("Confirm message send", "メッセージ送信を確定"), 360, 560)
			assertNativeControlVisible(t, tt, a.t("Edit draft", "下書きに戻る"), 360, 560)
			if out := os.Getenv("SOBA_POC_THEME_OUTPUT"); out != "" {
				f, err := os.Create(filepath.Join(out, fmt.Sprintf("message-review-scale-%.1f-ja-%t.png", scale, ja)))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(f, tt.Image())
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := tt.Click(a.t("Edit draft", "下書きに戻る")); err != nil {
				t.Fatal(err)
			}
			if p.review != nil || p.draft != "Fictional review text" {
				t.Fatal("enlarged review cannot return to draft")
			}
		}
	}
}

func TestNativeRefreshLargeTextReachability(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, conversation := range []string{"", "demo-alpha"} {
			a := demoApp()
			// The live-mode controls use fictional state and have no IPC loader.
			a.ja, a.demo, a.conversation = ja, false, conversation
			tt := ui.NewTester(a.view, 360, 560)
			tt.SetPreferences(ui.Preferences{TextScale: 1.5})
			assertNativeControlVisible(t, tt, a.t("Refresh", "更新"), 360, 560)
			if conversation != "" {
				assertNativeControlVisible(t, tt, a.t("Back", "戻る"), 360, 560)
			}
		}
	}
}
