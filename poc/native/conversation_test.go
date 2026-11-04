package main

import (
	"github.com/egoist/mygo/ui"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestConversationIsolationAndReadOnly(t *testing.T) {
	a := demoApp()
	a.conversation = "demo-alpha"
	tt := ui.NewTester(a.view, 360, 560)
	if !tt.HasText("DEMO · SIMULATED EXCHANGE") {
		t.Fatal("missing demo distinction")
	}
	a.messages["demo-alpha"].draft = "private draft"
	a.conversation = "demo-beta"
	tt.Frame()
	if a.messages["demo-beta"].draft != "" {
		t.Fatal("draft leaked across recipient")
	}
	a.demo = false
	a.sendEnabled = false
	tt.Frame()
	if !tt.HasText("LIVE IPC · READ ONLY") {
		t.Fatal("readonly distinction")
	}
}
func TestConversationReviewRender(t *testing.T) {
	a := demoApp()
	a.ja = true
	a.conversation = "demo-alpha"
	tt := ui.NewTester(a.view, 360, 560)
	tt.SetScale(2)
	p := a.messages["demo-alpha"]
	p.draft = "確認用のメッセージです。"
	p.beginReview("demo-alpha", "Demo laptop")
	tt.Frame()
	if !tt.HasText("確認用のメッセージです。") {
		t.Fatal("review missing")
	}
	out := os.Getenv("SOBA_POC_EXCHANGE_OUTPUT")
	if out == "" {
		return
	}
	save := func(name string) {
		t.Helper()
		f, e := os.Create(filepath.Join(out, name))
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		if e = png.Encode(f, tt.Image()); e != nil {
			t.Fatal(e)
		}
	}
	save("exchange-message-review-ja-2x.png")
	a.filesTab = true
	tt.Frame()
	fp := a.files["demo-alpha"]
	fp.process = "demo-process"
	fp.peer = "demo-alpha"
	fp.preparation = "demo-preparation"
	fp.review = fileReview{Version: 1, ProcessID: fp.process, PreparationID: fp.preparation, ReviewRevision: "demo-review", PeerIDs: []string{fp.peer}, Entries: []fileEntry{{ID: "demo-file", Path: "sample.txt", Kind: "file", Size: 256}}, TotalBytes: 256, Status: "prepared"}
	tt.Frame()
	save("exchange-file-review-ja-2x.png")
}

func TestConversationIncomingReviewAndAnnouncement(t *testing.T) {
	a := demoApp()
	a.ja = true
	a.conversation = "demo-alpha"
	a.filesTab = true
	tt := ui.NewTester(a.view, 360, 560)
	if err := tt.Click("受信を確認"); err != nil {
		t.Fatal(err)
	}
	p := a.files["demo-alpha"]
	if p.incoming == nil || p.incoming.ID != "demo-offer" {
		t.Fatal("review not tied to snapshot offer")
	}
	if !tt.HasText("受信オファーを確認") {
		t.Fatal("incoming review absent")
	}
	p.notice = "uncertain"
	tt.Frame()
	if len(tt.Announcements()) == 0 {
		t.Fatal("status change not announced")
	}
	tt.Frame()
	if len(tt.Announcements()) != 0 {
		t.Fatal("repeated announcement")
	}
}
