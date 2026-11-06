package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/webkaz-labs/sobalink/internal/uibrand"
)

func TestNativeThemePreservesSystemAppearanceAndMetrics(t *testing.T) {
	for _, base := range []*ui.Theme{ui.LightTheme(), ui.DarkTheme()} {
		base.Spacing, base.FontSize = 5, 18
		before := *base
		got := nativeTheme(base, ui.Preferences{})
		if got == base || *base != before {
			t.Fatal("shared theme mutated")
		}
		if got.Dark != base.Dark || got.Spacing != 5 || got.FontSize != 18 {
			t.Fatal("system appearance or metrics lost")
		}
		palette := uibrand.Palette(base.Dark)
		if got.Background != ui.Hex(palette.Background) || got.Text != ui.Hex(palette.Text) || got.Accent != ui.Hex(palette.Primary) {
			t.Fatal("shared light/dark palette not applied")
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
		theme.Radius *= 2
		theme.FontSize *= 2
		c.SetTheme(&theme)
		large = nativeStyles(c)
	}, 360, 560)
	// NewTester builds the initial frame; repeated frames must not compound scale.
	if large.caption != 2*small.caption || large.cardRadius != 2*small.cardRadius || large.screenPadding != 2*small.screenPadding || large.itemGap != 2*small.itemGap || large.controlGap != 2*small.controlGap || large.actionGap != 2*small.actionGap {
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

func TestNativeThemeFollowsDesktopPreferences(t *testing.T) {
	a := demoApp()
	var before, after ui.Theme
	var style nativeStyle
	tt := ui.NewTester(func(c *ui.Context) {
		before = *c.Theme()
		a.view(c)
		after = *c.Theme()
		style = nativeStyles(c)
	}, 460, 700)
	for _, dark := range []bool{false, true} {
		tt.SetDark(dark)
		for _, accent := range []ui.Color{{}, ui.Hex("#facc15"), ui.Hex("#db2777")} {
			for _, contrast := range []bool{false, true} {
				prefs := ui.Preferences{Accent: accent, HighContrast: contrast, TextScale: 1.5}
				tt.SetPreferences(prefs)
				for i := 0; i < 3; i++ {
					tt.Frame()
				}
				if after.Dark != dark || after.FontSize != before.FontSize {
					t.Fatal("desktop appearance or text scale lost")
				}
				if contrast {
					if after != before {
						t.Fatal("high-contrast theme changed by companion palette")
					}
					if style.history != before.SurfaceHover || style.status != before.SurfacePressed {
						t.Fatal("component fill overrides high-contrast theme")
					}
					if style.positiveText != before.Text || style.warningText != before.Text {
						t.Fatal("status text bypasses high-contrast foreground")
					}
					if after.Focus.A != 255 {
						t.Fatal("high-contrast focus lost opacity")
					}
				}
				if accent.A != 0 || contrast {
					if after.Accent != before.Accent || after.AccentText != before.AccentText || after.AccentHover != before.AccentHover || after.AccentPressed != before.AccentPressed || after.Selection != before.Selection || after.Focus != before.Focus {
						t.Fatal("desktop accent family was partially replaced")
					}
				} else {
					if after.AccentText != ui.Hex(uibrand.Palette(dark).OnPrimary) || after.Selection != after.Accent.Alpha(.25) || after.Focus != after.Accent {
						t.Fatal("companion accent family is inconsistent")
					}
				}
			}
		}
	}
	tt.SetDark(false)
	tt.SetPreferences(ui.Preferences{})
	palette := uibrand.Palette(false)
	if after.Background != ui.Hex(palette.Background) || after.Accent != ui.Hex(palette.Primary) || after.FontSize != ui.LightTheme().FontSize {
		t.Fatal("clearing preferences did not restore companion defaults")
	}
}

func TestConversationTabsKeyboard(t *testing.T) {
	for _, ja := range []bool{false, true} {
		t.Run(fmt.Sprintf("ja=%v", ja), func(t *testing.T) {
			a := demoApp()
			a.ja, a.conversation = ja, "demo-alpha"
			tt := ui.NewTester(a.view, 360, 560)
			a.messages["demo-alpha"].draft = "fictional retained draft"
			messages, files := a.t("Messages", "メッセージ"), a.t("Files", "ファイル")
			if err := tt.Click(files); err != nil {
				t.Fatal(err)
			}
			if !a.filesTab || !tt.Focused(files) || !tt.HasText(a.t("Choose files…", "ファイルを選択…")) {
				t.Fatal("file tab did not select its content and focus")
			}
			for _, step := range []struct {
				key   ui.Key
				files bool
			}{
				{ui.KeyLeft, false}, {ui.KeyRight, true}, {ui.KeyRight, false},
				{ui.KeyEnd, true}, {ui.KeyHome, false},
			} {
				tt.Key(0, step.key)
				label := messages
				if step.files {
					label = files
				}
				if a.filesTab != step.files || !tt.Focused(label) {
					t.Fatalf("key %v: files=%v, selected focus missing", step.key, a.filesTab)
				}
			}
			// The selected tab is one Tab stop, not two unrelated buttons.
			tt.Key(0, ui.KeyTab)
			if tt.Focused(messages) || tt.Focused(files) {
				t.Fatal("Tab did not leave tab list")
			}
			tt.Key(ui.Shift, ui.KeyTab)
			if !tt.Focused(messages) {
				t.Fatal("reverse Tab did not return to selected tab")
			}
			if a.messages["demo-alpha"].draft != "fictional retained draft" {
				t.Fatal("tab navigation lost draft")
			}
		})
	}
}

func TestDispatchReceiptStatusLocalization(t *testing.T) {
	child := fileChild{TransferID: "fictional-transfer", PeerID: "fictional-peer"}
	for _, ja := range []bool{false, true} {
		tr := fileText(translator(ja).text)
		a := fileActions{transfers: []exchangeTransfer{
			{ID: child.TransferID, PeerID: "other-peer", Direction: "outgoing", Status: "failed"},
			{ID: child.TransferID, PeerID: child.PeerID, Direction: "incoming", Status: "failed"},
			{ID: child.TransferID, PeerID: child.PeerID, Direction: "outgoing", Status: "completed"},
		}}
		if got := dispatchChildStatus(child, a, tr, ja); got != tr("completed", "完了") {
			t.Fatal(got)
		}
		if a.transfers[2].Status != "completed" {
			t.Fatal("protocol status changed")
		}
		a.transfers[2].Status = "future-status"
		if got := dispatchChildStatus(child, a, tr, ja); got != "future-status" {
			t.Fatal("unknown status lost")
		}
		a.transfers = nil
		if got := dispatchChildStatus(child, a, tr, ja); got != tr("Not yet in refreshed history", "更新した履歴に未反映") {
			t.Fatal(got)
		}
		a.demo = true
		if got := dispatchChildStatus(child, a, tr, ja); got != tr("Simulated", "シミュレーション") {
			t.Fatal(got)
		}
	}
}
