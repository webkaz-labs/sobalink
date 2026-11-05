package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNativeThemePreservesSystemAppearanceAndMetrics(t *testing.T) {
	for _, base := range []*ui.Theme{ui.LightTheme(), ui.DarkTheme()} {
		base.Spacing, base.FontSize = 5, 18
		before := *base
		got := nativeTheme(base)
		if got == base || *base != before {
			t.Fatal("shared theme mutated")
		}
		if got.Dark != base.Dark || got.Spacing != 5 || got.FontSize != 18 {
			t.Fatal("system appearance or metrics lost")
		}
		if base.Dark && *got != before {
			t.Fatal("light palette applied to dark appearance")
		}
		if !base.Dark && (got.Background != ui.Hex("#f4f7f5") || got.Text != ui.Hex("#172e34")) {
			t.Fatal("light palette changed")
		}
	}
}
func TestNativeStylesScaleWithTheme(t *testing.T) {
	var small, large nativeStyle
	tt := ui.NewTester(func(c *ui.Context) { small = nativeStyles(c) }, 360, 560)
	tt.Frame()
	tt = ui.NewTester(func(c *ui.Context) {
		theme := *c.Theme()
		theme.Spacing *= 2
		theme.FontSize *= 2
		c.SetTheme(&theme)
		large = nativeStyles(c)
	}, 360, 560)
	// NewTester builds the initial frame; repeated frames must not compound scale.
	if large.caption != 2*small.caption || large.cardRadius != 2*small.cardRadius {
		t.Fatal("tokens do not follow theme metrics")
	}
}
func TestRecentMessagesSelectsPeerAndNewestSix(t *testing.T) {
	var input []exchangeMessage
	for i := 0; i < 9; i++ {
		input = append(input, exchangeMessage{ID: fmt.Sprint(i), PeerID: "fictional-peer"}, exchangeMessage{PeerID: "other-peer"})
	}
	got := recentMessages(input, "fictional-peer")
	if !got.truncated || len(got.entries) != 6 || got.entries[0].ID != "3" || got.entries[5].ID != "8" {
		t.Fatalf("wrong history: %+v", got)
	}
	if len(recentMessages(input, "missing-peer").entries) != 0 {
		t.Fatal("other conversation exposed")
	}
	if len(input) != 18 {
		t.Fatal("input mutated")
	}
}
func TestConversationBackAndReopenRetainsDraft(t *testing.T) {
	a := demoApp()
	a.selected = "demo-alpha"
	a.openConversation()
	tt := ui.NewTester(a.view, 420, 600)
	a.messages["demo-alpha"].draft = "fictional draft"
	if err := tt.Click("Back"); err != nil {
		t.Fatal(err)
	}
	if a.conversation != "" || !tt.HasText("DEMO · FICTIONAL DATA") {
		t.Fatal("did not return to devices")
	}
	if err := tt.Click("Open messages / files"); err != nil {
		t.Fatal(err)
	}
	if a.messages["demo-alpha"].draft != "fictional draft" {
		t.Fatal("back lost draft")
	}
}
func TestMissingConversationDeviceRemainsReadOnly(t *testing.T) {
	a := demoApp()
	a.conversation = "demo-alpha"
	tt := ui.NewTester(a.view, 420, 700)
	a.messages["demo-alpha"].draft = "fictional draft"
	a.state.Peers = nil
	tt.Frame()
	if !tt.HasText("Device unavailable") || !tt.HasText("Read only. Sending is disabled.") {
		t.Fatal(tt.Texts())
	}
	if a.messages["demo-alpha"].draft != "fictional draft" || a.conversation != "demo-alpha" {
		t.Fatal("lost isolated conversation")
	}
}
func TestMessageEditCannotUnlockAttemptedReview(t *testing.T) {
	for _, state := range []string{"busy", "uncertain", "attempted"} {
		p := &messagePane{draft: "fictional draft"}
		p.beginReview("fictional-peer", "Fictional device")
		switch state {
		case "busy":
			p.busy = true
		case "uncertain":
			p.uncertain = true
		case "attempted":
			p.review.attempted = true
		}
		review := p.review
		p.editDraft()
		if p.review != review || p.canEdit() {
			t.Fatalf("edit unlocked %s", state)
		}
	}
}
func TestConversationEligibility(t *testing.T) {
	a := demoApp()
	a.demo, a.sendEnabled, a.conversation = false, true, "demo-alpha"
	if !a.conversationPresentation().enabled {
		t.Fatal("eligible trusted device disabled")
	}
	for _, change := range []func(*app){
		func(a *app) { a.loaded = false },
		func(a *app) { a.failed = true },
		func(a *app) { a.sendEnabled = false },
		func(a *app) { a.state.Peers[0].Trusted = false },
		func(a *app) { a.state.Peers[0].Autosave.Paused = true },
		func(a *app) { a.conversation = "missing" },
	} {
		b := &app{ctx: context.Background(), state: demoSnapshot(), loaded: true, sendEnabled: true, conversation: "demo-alpha"}
		change(b)
		if b.conversationPresentation().enabled {
			t.Fatal("unsafe eligibility")
		}
	}
}
