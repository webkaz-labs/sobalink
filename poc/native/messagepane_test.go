package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
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
