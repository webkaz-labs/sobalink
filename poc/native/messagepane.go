package main

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/messageframe"
)

// A pane belongs to one peer. Reviewed values are immutable until explicitly
// edited, and a possibly delivered request cannot be retried from this pane.
type messagePane struct {
	announced       string
	draft           string
	review          *messageReview
	busy, uncertain bool
	notice          string
	events          chan messagePaneEvent
}
type messageReview struct {
	requestID, peerID, peerName, text string
	attempted                         bool
}
type messagePaneEvent struct {
	requestID string
	simulated bool
	err       error
}

func (p *messagePane) beginReview(peerID, peerName string) {
	if p.busy || p.uncertain || p.review != nil || peerID == "" || strings.TrimSpace(p.draft) == "" || !utf8.ValidString(p.draft) {
		return
	}
	if len(p.draft) > messageframe.TextBytes {
		p.notice = "too_large"
		return
	}
	p.review = &messageReview{requestID: newExchangeRequestID(), peerID: peerID, peerName: peerName, text: p.draft}
	p.notice = ""
}
func (p *messagePane) send(ctx context.Context, wake func(), demo, enabled bool, client *exchangeClient, peerID string) {
	if !enabled || p.busy || p.uncertain || p.review == nil || p.review.attempted || p.review.peerID != peerID || ctx.Err() != nil || !demo && client == nil {
		return
	}
	// Reject locally before marking the immutable review as attempted. All
	// other errors after dispatch remain uncertain unless Core explicitly
	// confirms its pre-dispatch text-budget rejection.
	if len(p.review.text) > messageframe.TextBytes {
		p.notice = "too_large"
		return
	}
	r := *p.review
	p.review.attempted = true
	p.busy = true
	if p.events == nil {
		p.events = make(chan messagePaneEvent, 1)
	}
	events := p.events
	go func() {
		e := messagePaneEvent{requestID: r.requestID, simulated: demo}
		if !demo {
			_, e.err = client.SendMessage(ctx, r.requestID, r.peerID, r.text)
		}
		// The bounded channel preserves completion even when the window is hidden.
		events <- e
		if ctx.Err() == nil && wake != nil {
			wake()
		}
	}()
}
func (p *messagePane) drain() {
	if p.events == nil {
		return
	}
	select {
	case e := <-p.events:
		p.busy = false
		if p.review == nil || p.review.requestID != e.requestID {
			p.uncertain = true
			p.notice = "uncertain"
			return
		}
		// For immediate message.send, Core emits this code before creating an
		// operation or contacting the peer. Peer-side size failures use a
		// different code. Do not generalize to request/response size errors,
		// wrapped errors, or other remote failures with uncertain outcomes.
		if remote, ok := e.err.(*control.RemoteError); ok && remote.Code == "message_too_large" {
			p.review.attempted = false
			p.notice = "configured_too_large"
			return
		}
		if e.err != nil {
			p.uncertain = true
			p.notice = "uncertain"
			return
		}
		p.notice = "submitted"
		if e.simulated {
			p.notice = "simulated"
		}
		p.draft = ""
		p.review = nil
	default:
	}
}

func (p *messagePane) canEdit() bool {
	return !p.busy && !p.uncertain && (p.review == nil || !p.review.attempted)
}
func (p *messagePane) editDraft() {
	if p.canEdit() {
		p.review = nil
	}
}
func (p *messagePane) canReview() bool {
	return p.canEdit() && p.review == nil && strings.TrimSpace(p.draft) != "" && utf8.ValidString(p.draft)
}
func (p *messagePane) canSend(peerID string) bool {
	return p.canEdit() && p.review != nil && p.review.peerID == peerID
}
func (p *messagePane) takeAnnouncement() bool {
	status := p.notice
	if p.busy {
		status = "busy"
	}
	if status == p.announced {
		return false
	}
	p.announced = status
	return status != ""
}
