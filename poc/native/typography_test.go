package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Optional diagnostic uses fixed fictional text, never profile contents.
func TestJapaneseTypographyProbe(t *testing.T) {
	out := os.Getenv("SOBA_POC_TYPOGRAPHY_OUTPUT")
	if out == "" {
		t.Skip("optional diagnostic image")
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20).Gap(14).Background(ui.Hex("#ffffff")).TextColor(ui.Hex("#172e34")).Children(func() {
			ui.Text(c, "Default font / no language").FontSize(13)
			ui.Text(c, "確認します。端末、設定。句読点「、。」").FontSize(25)
			ui.Text(c, "Default font / Lang(ja)").FontSize(13)
			ui.Text(c, "確認します。端末、設定。句読点「、。」").FontSize(25).Lang("ja")
			ui.Text(c, "Noto Sans CJK JP / Lang(ja)").FontSize(13)
			ui.Text(c, "確認します。端末、設定。句読点「、。」").FontSize(25).Lang("ja").Font("Noto Sans CJK JP")
		})
	}, 620, 310)
	f, err := os.Create(filepath.Clean(out))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestJapaneseFontSelection(t *testing.T) {
	if uiFont(false) != "" {
		t.Fatal("English must retain its system default")
	}
	if uiFont(true) != "Hiragino Sans, Yu Gothic UI, Meiryo, Noto Sans CJK JP, Noto Sans JP" {
		t.Fatal("Japanese font candidates changed: recheck punctuation rendering")
	}
}
