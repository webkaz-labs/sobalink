package main

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestNativeTraySupported(t *testing.T) {
	for _, os := range []string{"darwin", "windows", "linux", "freebsd", ""} {
		if got, want := nativeTraySupported(os), os == "darwin" || os == "windows"; got != want {
			t.Errorf("nativeTraySupported(%q) = %v, want %v", os, got, want)
		}
	}
}

func TestNativeTrayClosePolicy(t *testing.T) {
	s := &nativeShell{}
	if s.hideOnClose() {
		t.Fatal("normal fallback must close, not hide")
	}
	s.tray = &mygo.Tray{}
	for range 3 {
		if !s.hideOnClose() {
			t.Fatal("tray close/Escape must preserve reusable window")
		}
	}
	s.quitting = true
	if s.hideOnClose() {
		t.Fatal("explicit UI quit must bypass hide-on-close")
	}
}

func TestNativeTrayIcon(t *testing.T) {
	data, err := nativeTrayIcon()
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 32 || img.Bounds().Dy() != 32 {
		t.Fatal("tray icon must be 32x32")
	}
	ink, transparent := 0, 0
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if r != 0 || g != 0 || b != 0 {
				t.Fatal("template icon must have black ink only")
			}
			if a == 0 {
				transparent++
			} else {
				ink++
			}
		}
	}
	if ink == 0 || transparent == 0 {
		t.Fatal("template icon needs both ink and transparent pixels")
	}
}

func TestNativeTrayFooterFits(t *testing.T) {
	for _, ja := range []bool{false, true} {
		a := demoApp()
		a.ja = ja
		shell := &nativeShell{}
		tt := ui.NewTester(func(c *ui.Context) { shell.view(a, c) }, 460, 620)
		for _, label := range []string{a.t("Hide", "隠す"), a.t("Quit UI (daemon continues)", "UIを終了（本体は継続）")} {
			rect, ok := tt.Find(label)
			if !ok || rect.X < 0 || rect.Y < 0 || rect.X+rect.W > 460 || rect.Y+rect.H > 620 {
				t.Errorf("visible footer %q: rect=%+v found=%v", label, rect, ok)
			}
		}
	}
}

func TestNativeTrayUsesPersistentPanel(t *testing.T) {
	opts := nativeTrayPanelOptions(nil)
	if !opts.Panel || !opts.Hidden || !opts.UseContentSize {
		t.Fatal("tray must use a reusable keyboard-capable panel, not a transient focus-dismissed flyout")
	}
}

func TestNativeTrayPanelBounds(t *testing.T) {
	cases := []struct {
		name         string
		anchor, work mygo.Rectangle
	}{
		{"top", mygo.Rectangle{X: 900, Y: 0, Width: 24, Height: 24}, mygo.Rectangle{X: 0, Y: 24, Width: 1024, Height: 744}},
		{"bottom", mygo.Rectangle{X: 900, Y: 744, Width: 24, Height: 24}, mygo.Rectangle{X: 0, Y: 0, Width: 1024, Height: 744}},
		{"negative display", mygo.Rectangle{X: -900, Y: 0, Width: 24, Height: 24}, mygo.Rectangle{X: -1024, Y: 24, Width: 1024, Height: 744}},
		{"small display", mygo.Rectangle{X: 290, Y: 0, Width: 24, Height: 24}, mygo.Rectangle{X: 0, Y: 24, Width: 320, Height: 400}},
		{"unknown icon", mygo.Rectangle{}, mygo.Rectangle{X: 100, Y: 24, Width: 1024, Height: 744}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := nativeTrayPanelBounds(tc.anchor, tc.work)
			if r.Width <= 0 || r.Height <= 0 || r.X < tc.work.X || r.Y < tc.work.Y || r.X+r.Width > tc.work.X+tc.work.Width || r.Y+r.Height > tc.work.Y+tc.work.Height {
				t.Fatalf("panel outside work area: %+v, work %+v", r, tc.work)
			}
		})
	}
}
