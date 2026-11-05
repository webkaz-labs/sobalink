package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/messageframe"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func waitMessageEvent(t *testing.T, p *messagePane) {
	t.Helper()
	select {
	case e := <-p.events:
		p.events <- e
		p.drain()
	case <-time.After(5 * time.Second):
		t.Fatal("message result timed out")
	}
}
func TestMessagePaneImmutableSingleFlight(t *testing.T) {
	p := &messagePane{draft: "reviewed message"}
	p.beginReview("fictional-peer", "Fictional device")
	id := p.review.requestID
	p.draft = "later draft change"
	release := make(chan struct{})
	var calls atomic.Int32
	commands := make(chan webui.Command, 1)
	c := newExchangeClient("fictional-profile", true, func(_ context.Context, _, wire string, out any) error {
		calls.Add(1)
		var cmd webui.Command
		_ = json.Unmarshal([]byte(wire), &cmd)
		commands <- cmd
		<-release
		b, _ := json.Marshal(exchangeMessage{ID: "message", PeerID: "fictional-peer", Direction: "outgoing", Text: "reviewed message", Status: "sent"})
		return json.Unmarshal(b, out)
	})
	p.send(t.Context(), nil, false, true, c, "fictional-peer")
	p.send(t.Context(), nil, false, true, c, "fictional-peer")
	cmd := <-commands
	close(release)
	waitMessageEvent(t, p)
	var payload map[string]string
	_ = json.Unmarshal(cmd.Payload, &payload)
	if calls.Load() != 1 || cmd.RequestID != id || payload["text"] != "reviewed message" || p.notice != "submitted" {
		t.Fatal("review changed or duplicated")
	}
}
func TestMessagePaneUncertainPreservesRequest(t *testing.T) {
	p := &messagePane{draft: "retained draft"}
	p.beginReview("fictional-peer", "Device")
	id := p.review.requestID
	c, calls := exchangeFixture(t, true, nil, errors.New("private failure detail"))
	p.send(t.Context(), nil, false, true, c, "fictional-peer")
	waitMessageEvent(t, p)
	p.send(t.Context(), nil, false, true, c, "fictional-peer")
	if !p.uncertain || p.busy || p.draft != "retained draft" || p.review.requestID != id || len(*calls) != 1 {
		t.Fatal("uncertainty lost or retried")
	}
	tt := ui.NewTester(func(c *ui.Context) { p.view(c, t.Context(), false, false, true, nil, "fictional-peer", "Device", nil) }, 420, 600)
	if tt.HasText("private failure detail") {
		t.Fatal("raw error displayed")
	}
}
func TestMessagePaneGuardsAndDemo(t *testing.T) {
	p := &messagePane{draft: "hello"}
	p.beginReview("fictional-peer", "Device")
	c, calls := exchangeFixture(t, true, nil, errors.New("must not call"))
	p.send(t.Context(), nil, false, false, c, "fictional-peer")
	p.send(t.Context(), nil, false, true, c, "other-peer")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p.send(ctx, nil, false, true, c, "fictional-peer")
	if p.busy || p.review.attempted || len(*calls) != 0 {
		t.Fatal("guard failed")
	}
	p.send(t.Context(), nil, true, true, c, "fictional-peer")
	waitMessageEvent(t, p)
	if len(*calls) != 0 || p.notice != "simulated" || p.review != nil {
		t.Fatal("demo invoked transport")
	}
}
func TestMessagePaneHistoryAndReviewUI(t *testing.T) {
	p := &messagePane{draft: "確認用の架空メッセージ"}
	messages := []exchangeMessage{{PeerID: "fictional-peer", Direction: "incoming", Text: "架空の受信履歴です", Status: "received"}, {PeerID: "other-peer", Text: "other conversation"}}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(12).Lang("ja").Font(uiFont(true)).Children(func() { p.view(c, t.Context(), true, true, true, nil, "fictional-peer", "架空の端末", messages) })
	}, 420, 600)
	if !tt.HasText("架空の受信履歴です") || tt.HasText("other conversation") {
		t.Fatal(tt.Texts())
	}
	if err := tt.Click("送信内容を確認"); err != nil {
		t.Fatal(err)
	}
	if p.review == nil || p.review.text != "確認用の架空メッセージ" || !tt.HasText("送信前の確認") {
		t.Fatal(tt.Texts())
	}
	if err := tt.Click("下書きに戻る"); err != nil {
		t.Fatal(err)
	}
	if p.review != nil || p.draft != "確認用の架空メッセージ" {
		t.Fatal("edit lost draft")
	}
}

func TestMessagePaneOversizeRemainsEditable(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", 200000), strings.Repeat("界", messageframe.TextBytes/3+1)} {
		p := &messagePane{draft: text}
		p.beginReview("fictional-peer", "Device")
		if p.review != nil || !p.canEdit() || p.notice != "too_large" || p.draft != text {
			t.Fatal("oversize review was not rejected in place")
		}
		for _, ja := range []bool{false, true} {
			if !strings.Contains(p.noticeText(ja), "16384") {
				t.Fatal("missing localized limit")
			}
		}
		// Also protect dispatch if a reviewed value came from an older UI state.
		p.review = &messageReview{requestID: "retained-id", peerID: "fictional-peer", text: text}
		c, calls := exchangeFixture(t, true, nil, errors.New("must not call"))
		p.send(t.Context(), nil, false, true, c, "fictional-peer")
		if len(*calls) != 0 || p.events != nil || p.busy || p.uncertain || p.review.attempted || !p.canEdit() || p.review.requestID != "retained-id" {
			t.Fatal("local rejection dispatched or locked the review")
		}
		p.editDraft()
		p.draft = "shortened draft"
		p.beginReview("fictional-peer", "Device")
		if p.review == nil || p.review.text != p.draft || p.notice != "" || !p.canSend("fictional-peer") {
			t.Fatal("cannot review the corrected draft")
		}
	}
}

func TestMessagePaneRemoteSizeErrorRemainsUncertain(t *testing.T) {
	for _, code := range []string{"request_too_large", "response_too_large", "message_peer_too_large", "wrapped_local_rejection"} {
		t.Run(code, func(t *testing.T) {
			p := &messagePane{draft: "retained draft"}
			p.beginReview("fictional-peer", "Device")
			id := p.review.requestID
			var failure error = &control.RemoteError{Code: code, Message: "untrusted remote detail"}
			if code == "wrapped_local_rejection" {
				failure = errors.Join(&control.RemoteError{Code: "message_too_large"}, errors.New("another uncertain outcome"))
			}
			c, calls := exchangeFixture(t, true, nil, failure)
			p.send(t.Context(), nil, false, true, c, "fictional-peer")
			waitMessageEvent(t, p)
			p.editDraft()
			p.send(t.Context(), nil, false, true, c, "fictional-peer")
			if !p.uncertain || p.canEdit() || p.review.requestID != id || p.review.text != p.draft || len(*calls) != 1 {
				t.Fatal("post-dispatch rejection discarded request identity or permitted retry")
			}
		})
	}
}

func TestMessagePaneConfiguredSizeRejectionAllowsEditing(t *testing.T) {
	p := &messagePane{draft: "retained draft"}
	p.beginReview("fictional-peer", "Device")
	id := p.review.requestID
	c, calls := exchangeFixture(t, true, nil, &control.RemoteError{Code: "message_too_large", Message: "private detail"})
	p.send(t.Context(), nil, false, true, c, "fictional-peer")
	waitMessageEvent(t, p)
	if p.uncertain || p.busy || !p.canEdit() || p.review.attempted || p.review.requestID != id || p.notice != "configured_too_large" || len(*calls) != 1 {
		t.Fatal("Core pre-dispatch rejection did not retain editable review")
	}
	for _, ja := range []bool{false, true} {
		if p.noticeText(ja) == "" || strings.Contains(p.noticeText(ja), "private detail") {
			t.Fatal("unsafe or missing rejection notice")
		}
	}
	p.editDraft()
	p.draft = "short"
	p.beginReview("fictional-peer", "Device")
	if p.review == nil || p.review.text != "short" || p.review.requestID == id {
		t.Fatal("edited draft did not get a fresh review")
	}
}
