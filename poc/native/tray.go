package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// nativeShell owns the tray and its reusable window for the whole UI lifetime.
// All state transitions run on MyGo's main thread. It never starts or stops the daemon.
type nativeShell struct {
	tray     *mygo.Tray
	window   *mygo.Window
	quitting bool
}

// startNativeUI must run inside App.WhenReady. Keep its result alive until App.Run returns.
func startNativeUI(a *app, useTray bool) *nativeShell {
	s := &nativeShell{}
	if !useTray {
		s.window = normalNativeWindow(ui.View(a.view), 560)
		return s
	}
	if !nativeTraySupported(runtime.GOOS) {
		s.openFallback(a)
		return s
	}
	icon, err := nativeTrayIcon()
	if err == nil {
		s.tray, err = mygo.NewTray(mygo.TrayOptions{
			ID: "soba.native-poc", Icon: icon, IconIsTemplate: true,
			ToolTip: "soba · Native UI PoC",
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, a.t("Tray unavailable; using a normal window:", "トレイを利用できないため通常のウィンドウで開きます:"), err)
		s.openFallback(a)
		return s
	}
	// Do not hide the Dock icon until tray creation succeeds. This is process-only;
	// no login items, persistent OS settings, or daemon configuration are changed.
	if runtime.GOOS == "darwin" {
		mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	}
	// A keyboard-capable Panel deliberately replaces the transient Flyout.
	// Focusable flyouts / macOS popovers dismiss natively on focus loss,
	// including native pickers and IME interactions, even without OnBlur.
	// Keep review and composing state visible until an explicit dismissal.
	// Native picker ownership, IME and multi-display placement still require
	// real-device acceptance; headless tests cannot establish those behaviors.
	s.window = mygo.NewWindow(nativeTrayPanelOptions(ui.View(func(c *ui.Context) { s.view(a, c) })))
	s.window.OnClose(func(e *mygo.CloseEvent) {
		if s.hideOnClose() {
			e.PreventDefault()
			s.window.Hide()
		}
	})
	s.tray.OnClick(func() {
		if s.window.IsVisible() {
			s.window.Hide()
		} else {
			s.show()
		}
	})
	// Right-click also exposes the panel and its explicit Quit UI action.
	s.tray.OnRightClick(s.show)
	// Allow native app-menu Quit and OS quit to bypass close-to-hide as well.
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { s.quitting = true })
	mygo.App.OnQuit(func() { s.tray.Destroy() })
	return s
}

// Panel handles an unconsumed Escape as Close, but does not automatically
// dismiss on blur. Never attach an unconditional hide-on-blur listener here.
func nativeTrayPanelOptions(content mygo.Content) mygo.WindowOptions {
	return mygo.WindowOptions{
		Title: "soba · Native UI PoC", Panel: true, Hidden: true,
		Width: 460, Height: 620, UseContentSize: true,
		Frameless: true, DisableResize: true, CornerRadius: 12,
		Content: content,
	}
}

func (s *nativeShell) show() {
	anchor := s.tray.Bounds()
	point := mygo.Point{X: anchor.X + anchor.Width/2, Y: anchor.Y + anchor.Height/2}
	if anchor.Width <= 0 || anchor.Height <= 0 {
		point = mygo.Screen.CursorScreenPoint()
	}
	work := mygo.Screen.DisplayNearestPoint(point).WorkArea
	r := nativeTrayPanelBounds(anchor, work)
	s.window.SetSize(r.Width, r.Height)
	s.window.SetPosition(r.X, r.Y)
	s.window.Show()
}

// nativeTrayPanelBounds clamps the panel to the clicked icon's display. A
// missing icon rectangle (such as overflow transitions) uses the work-area center.
func nativeTrayPanelBounds(anchor, work mygo.Rectangle) mygo.Rectangle {
	w, h := min(460, max(1, work.Width)), min(620, max(1, work.Height))
	x, y := work.X+(work.Width-w)/2, work.Y+(work.Height-h)/2
	if anchor.Width > 0 && anchor.Height > 0 {
		x = anchor.X + (anchor.Width-w)/2
		y = anchor.Y + anchor.Height + 6
		if y+h > work.Y+work.Height {
			y = anchor.Y - h - 6
		}
	}
	x = max(work.X, min(x, work.X+work.Width-w))
	y = max(work.Y, min(y, work.Y+work.Height-h))
	return mygo.Rectangle{X: x, Y: y, Width: w, Height: h}
}

func nativeTraySupported(goos string) bool { return goos == "darwin" || goos == "windows" }

func normalNativeWindow(content mygo.Content, height int) *mygo.Window {
	return mygo.NewWindow(mygo.WindowOptions{
		Title: "soba · Native UI PoC", Width: 460, Height: height,
		MinWidth: 360, MinHeight: 440, Content: content,
	})
}

func (s *nativeShell) hideOnClose() bool { return s.tray != nil && !s.quitting }

func (s *nativeShell) openFallback(a *app) {
	s.window = normalNativeWindow(ui.View(func(c *ui.Context) { s.fallbackView(a, c) }), 640)
}
func (s *nativeShell) hideWindow() { s.window.Hide() }
func (s *nativeShell) quitUI() {
	s.quitting = true
	mygo.App.Quit()
}

// nativeTrayIcon creates a neutral, original two-link mark. It contains no
// profile or environment information. Template rendering adapts its black ink
// to the macOS menu bar and Windows taskbar appearance.
func nativeTrayIcon() ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			left := (x-11)*(x-11) + (y-16)*(y-16)
			right := (x-21)*(x-21) + (y-16)*(y-16)
			if (left >= 25 && left <= 64) || (right >= 25 && right <= 64) {
				img.SetNRGBA(x, y, color.NRGBA{A: 255})
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
