package main

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/webkaz-labs/sobalink/internal/control"
)

func TestLocale(t *testing.T) {
	for _, tc := range []struct {
		override, lang string
		ja             bool
	}{{"auto", "ja_JP.UTF-8", true}, {"auto", "en_US", false}, {"auto", "xx", false}, {"en", "ja_JP", false}, {"ja", "en_US", true}} {
		got, err := locale(tc.override, func(k string) string {
			if k == "LANG" {
				return tc.lang
			}
			return ""
		})
		if err != nil || got != tc.ja {
			t.Fatal(tc, got, err)
		}
	}
	if _, err := locale("invalid", os.Getenv); err == nil {
		t.Fatal("accepted invalid locale")
	}
}
func TestNativeIPCReadOnly(t *testing.T) {
	dir, err := os.MkdirTemp("", "soba-ui-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	var calls atomic.Int32
	server, err := control.Serve(t.Context(), dir, func(_ context.Context, command string) (any, error) {
		calls.Add(1)
		if command != "status" {
			t.Errorf("unexpected command %q", command)
			return nil, errors.New("mutation refused")
		}
		return demoSnapshot(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	s, err := ipcLoader(dir)(t.Context())
	if err != nil || len(s.Peers) != 3 || calls.Load() != 1 {
		t.Fatal(s, err, calls.Load())
	}
}
func TestRefreshFailureClearsStaleData(t *testing.T) {
	a := &app{state: demoSnapshot(), loaded: true, selected: "demo-alpha", busy: true, results: make(chan result, 1)}
	a.results <- result{err: errors.New("private error not displayed")}
	a.drain()
	if a.loaded || a.busy || !a.failed || len(a.state.Peers) != 0 || a.selected != "" {
		t.Fatalf("stale state: %+v", a)
	}
}
func TestRepeatedRefreshSingleFlight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	woke := make(chan struct{}, 1)
	var calls atomic.Int32
	a := &app{ctx: ctx, results: make(chan result, 1), load: func(context.Context) (snapshot, error) { calls.Add(1); <-release; return demoSnapshot(), nil }}
	a.refresh(func() { woke <- struct{}{} })
	a.refresh(func() { t.Error("second refresh") })
	close(release)
	select {
	case <-woke:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh timeout")
	}
	a.drain()
	if calls.Load() != 1 || !a.loaded || a.busy {
		t.Fatal("single-flight failed")
	}
}
func demoApp() *app {
	return &app{ctx: context.Background(), demo: true, loaded: true, state: demoSnapshot(), results: make(chan result, 1)}
}
func TestViewInteractions(t *testing.T) {
	a := demoApp()
	tt := ui.NewTester(a.view, 960, 680)
	if !tt.HasText("DEMO · FICTIONAL DATA") {
		t.Fatal(tt.Texts())
	}
	if err := tt.Click("Device filter"); err != nil {
		t.Fatal(err)
	}
	tt.Type("laptop")
	if !tt.HasText("Demo laptop") || tt.HasText("Demo tablet") {
		t.Fatal(tt.Texts())
	}
	if err := tt.Click("Demo laptop"); err != nil {
		t.Fatal(err)
	}
	if a.selected != "demo-alpha" {
		t.Fatal(a.selected)
	}
	if err := tt.Click("Demo laptop"); err != nil {
		t.Fatal(err)
	}
	if a.selected != "" {
		t.Fatal(a.selected)
	}
	if err := tt.Click("日本語"); err != nil {
		t.Fatal(err)
	}
	if !a.ja || !tt.HasText("デモ · 架空のデータ") {
		t.Fatal(tt.Texts())
	}
}
func TestNarrowEmptyState(t *testing.T) {
	a := demoApp()
	a.query = "not-present"
	tt := ui.NewTester(a.view, 420, 680)
	if !tt.HasText("No matching devices.") {
		t.Fatal(tt.Texts())
	}
	tt.SetScale(2)
	tt.Frame()
}

func TestNetworkFailureVisible(t *testing.T) {
	a := demoApp()
	a.demo = false
	a.state.Peers = nil
	a.state.Self.Status = "error"
	tt := ui.NewTester(a.view, 960, 760)
	if !tt.HasText("error") || !tt.HasText("Review network state in Web UI or CLI. IPC access does not prove connectivity.") {
		t.Fatal(tt.Texts())
	}
}

func TestCompactPeerBoundsAndDetails(t *testing.T) {
	for _, width := range []int{360, 420, 640} {
		a := demoApp()
		a.ja = true
		tt := ui.NewTester(a.view, width, 560)
		for _, name := range []string{"Demo laptop", "Demo desktop", "Demo tablet", "この試作について"} {
			r, ok := tt.Find(name)
			if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(width) || r.Y+r.H > 560 {
				t.Fatalf("width %d: %s out of bounds: %+v (%v)", width, name, r, ok)
			}
		}
		a.selected = "demo-alpha"
		tt.Frame()
		tt.SetScale(2)
		for _, name := range []string{"Demo laptop", "⌄", "サービスの許可は別です。アプリの動作は未確認です。"} {
			r, ok := tt.Find(name)
			if !ok || r.X < 0 || r.X+r.W > float32(width) || r.Y+r.H > 500 {
				t.Fatalf("details width %d: %s out of bounds: %+v (%v)", width, name, r, ok)
			}
		}
		if err := tt.Click("Demo laptop"); err != nil {
			t.Fatal(err)
		}
		if a.selected != "" {
			t.Fatal("details did not close")
		}
	}
}
func TestCompactAboutDisclosure(t *testing.T) {
	a := demoApp()
	a.ja = true
	tt := ui.NewTester(a.view, 420, 560)
	text := "接続・許可は変更しません。実送信には --enable-send と確認が必要です。デモでは送信しません。"
	if tt.HasText(text) {
		t.Fatal("explanation not collapsed")
	}
	if err := tt.Click("この試作について"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText(text) {
		t.Fatal("explanation did not open")
	}
	if err := tt.Click("説明を閉じる"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText(text) {
		t.Fatal("explanation did not close")
	}
}

func TestDeviceRowKeyboardActivation(t *testing.T) {
	a := demoApp()
	tt := ui.NewTester(a.view, 420, 560)
	if err := tt.Click("Demo laptop"); err != nil {
		t.Fatal(err)
	}
	if a.selected != "demo-alpha" || !tt.Focused("Demo laptop") {
		t.Fatal("row click/focus failed")
	}
	tt.Key(0, ui.KeyEnter)
	if a.selected != "" {
		t.Fatal("Enter did not collapse")
	}
	tt.Key(0, ui.KeySpace)
	if a.selected != "demo-alpha" {
		t.Fatal("Space did not expand")
	}
	tt.Key(0, ui.KeyTab)
	if !tt.Focused("Demo desktop") {
		t.Fatal("Tab did not move to next row")
	}
	tt.Key(0, ui.KeyEnter)
	if a.selected != "demo-beta" {
		t.Fatal("second row keyboard activation failed")
	}
	if tt.HasText("Details") || tt.HasText("Close") {
		t.Fatal("redundant nested buttons remain")
	}
}

func TestDeviceRowWholeAreaAndHover(t *testing.T) {
	a := demoApp()
	tt := ui.NewTester(a.view, 420, 560)
	r, ok := tt.Find("Demo laptop")
	if !ok {
		t.Fatal("row not found")
	}
	tt.Move(r.X+r.W-10, r.Y+r.H/2)
	if tt.Cursor() != ui.CursorPointer {
		t.Fatal("row does not advertise clickability")
	}
	tt.ClickAt(r.X+r.W-10, r.Y+r.H/2)
	if a.selected != "demo-alpha" {
		t.Fatal("row edge did not activate")
	}
}
