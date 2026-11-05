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
	for _, dark := range []bool{false, true} {
		for _, ja := range []bool{false, true} {
			for _, pane := range []string{"devices", "messages", "files"} {
				name := fmt.Sprintf("%s-dark-%t-ja-%t", pane, dark, ja)
				t.Run(name, func(t *testing.T) {
					a := demoApp()
					a.ja = ja
					if pane != "devices" {
						a.conversation, a.filesTab = "demo-alpha", pane == "files"
					}
					tt := ui.NewTester(func(c *ui.Context) {
						theme := ui.LightTheme()
						if dark {
							theme = ui.DarkTheme()
						}
						c.SetTheme(theme)
						a.view(c)
					}, 360, 560)
					tt.SetScale(2)
					for i := 0; i < 3; i++ {
						tt.Frame()
					}
					label := "soba"
					if pane != "devices" {
						label = "Demo laptop"
					}
					r, ok := tt.Find(label)
					if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 360 || r.Y+r.H > 560 {
						t.Fatalf("header not visible: %+v", r)
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
