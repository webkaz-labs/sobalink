package main

import (
	"context"
	"flag"
	"fmt"
	"image/png"
	"os"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	demo := flag.Bool("demo", false, "Use fictional data / 架空のデータを使用")
	dir := flag.String("state-dir", "", "Existing profile for read-only IPC / 読取専用IPCの既存プロファイル")
	tray := flag.Bool("tray", false, "Use a menu-bar / tray window")
	enableSend := flag.Bool("enable-send", false, "Enable reviewed messages/files on the selected profile")
	pane := flag.String("render-pane", "", "Demo pane: messages or files")
	lang := flag.String("locale", "auto", "auto, ja, en")
	render := flag.String("render", "", "Render fictional demo to PNG (headless) / 架空デモをPNG描画")
	width := flag.Int("render-width", 460, "Headless image width / 描画画像の幅")
	height := flag.Int("render-height", 560, "Headless image height / 描画画像の高さ")
	scale := flag.Int("render-scale", 1, "Headless scale 1 or 2 / 描画倍率")
	details := flag.Bool("render-details", false, "Expand fictional detail in rendered demo / 架空デモの詳細を表示")
	flag.Parse()
	if *height < 440 || *height > 1600 || *scale < 1 || *scale > 2 {
		return fmt.Errorf("invalid render height or scale")
	}
	if *width < 360 || *width > 2000 {
		return fmt.Errorf("render-width: 360..2000")
	}
	ja, err := locale(*lang, os.Getenv)
	if err != nil {
		return err
	}
	if flag.NArg() != 0 || (*demo) == (*dir != "") {
		return fmt.Errorf("choose exactly one: --demo or --state-dir / --demo または --state-dir を一つ指定してください")
	}
	if *render != "" && !*demo {
		return fmt.Errorf("render requires --demo; live data is never exported / 描画保存は --demo 専用です")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &app{ja: ja, demo: *demo, ctx: ctx, results: make(chan result, 1), sendEnabled: *enableSend, exchange: newExchangeClient(*dir, *enableSend, nil)}
	if *demo {
		a.state = demoSnapshot()
		a.loaded = true
	} else {
		a.load = ipcLoader(*dir)
	}
	if *pane != "" {
		if !*demo || (*pane != "messages" && *pane != "files") {
			return fmt.Errorf("render-pane requires demo and messages/files")
		}
		a.conversation = "demo-alpha"
		a.filesTab = *pane == "files"
	}
	if *render != "" {
		tt := ui.NewTester(a.view, *width, *height)
		tt.SetLocale(a.t("en", "ja"))
		if *details {
			a.selected = "demo-alpha"
			tt.Frame()
		}
		tt.SetScale(float32(*scale))
		f, err := os.Create(*render)
		if err != nil {
			return err
		}
		defer f.Close()
		return png.Encode(f, tt.Image())
	}
	mygo.App.SetLanguages("en", "ja")
	var shell *nativeShell
	mygo.App.WhenReady(func() {
		if *lang == "auto" {
			a.ja = strings.HasPrefix(mygo.App.Locale(), "ja")
		}
		shell = startNativeUI(a, *tray)
	})
	err = mygo.App.Run()
	runtime.KeepAlive(shell)
	return err
}
