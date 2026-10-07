package core

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func (c *Core) commitIncomingPeerMessage(ctx context.Context, trust Trust, peer, id, text string) (int, map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	refused := func(status int) (int, map[string]string) {
		return status, map[string]string{"error": "request refused"}
	}
	if c.closing || c.ctx.Err() != nil || ctx.Err() != nil {
		return refused(http.StatusForbidden)
	}
	currentTrust := false
	for _, current := range c.profile.Peers {
		if current.ID == trust.ID && current.Network == trust.Network && current.Network == c.profile.Settings.Network && current.Generation == trust.Generation && !current.Paused {
			currentTrust = true
			break
		}
	}
	if !currentTrust {
		return refused(http.StatusForbidden)
	}
	// Core.mu serializes every history writer and the trust check. Acquire
	// takes only the origin's short admission locks. After it returns, disk
	// I/O holds no generation/WG lock; retirement cancels without Core.mu and
	// waits for this transaction's actual return. A cancellation during the
	// atomic replacement cannot undo the write or invent a durable outcome.
	release, err := incomingPeerMessageCommit(ctx)
	if err != nil {
		return refused(http.StatusForbidden)
	}
	defer release()
	for _, message := range c.messages {
		if message.ID != id || message.PeerID != peer || message.Direction != "incoming" {
			continue
		}
		if message.Text != text {
			return refused(http.StatusConflict)
		}
		if c.messageHistoryUncertain {
			if err := c.saveMessageHistoryLocked(c.messages); err != nil {
				return http.StatusInsufficientStorage, map[string]string{"code": "message_history_unavailable", "error": messageHistoryError(err, true).Error()}
			}
		}
		return http.StatusOK, map[string]string{"id": message.ID, "status": "received"}
	}
	message := Message{ID: id, PeerID: peer, Text: text, Direction: "incoming", CreatedAt: time.Now().UTC(), Status: "received"}
	if err := c.appendMessageLocked(message); err != nil {
		return http.StatusInsufficientStorage, map[string]string{"code": "message_history_unavailable", "error": messageHistoryError(err, errors.Is(err, config.ErrAtomicCommitted)).Error()}
	}
	return http.StatusOK, map[string]string{"id": message.ID, "status": "received"}
}

// An outgoing message owns an obligation to record its exact delivery outcome
// before exposing bytes. Request preparation binds this application's actual
// origins before HTTP exposure. Its ordinary leases therefore cover the local
// history transaction even if a request, deadline or generation stops later.
// This completion-only obligation cannot send again or admit another operation.
// It is separate from the bounded memory-copy publication permit.
type peerMessageOperation struct {
	application *peerHTTPApplication
	workDone    func()
}

func (c *Core) beginPeerMessageOperation(ctx context.Context) (context.Context, *peerMessageOperation, error) {
	workDone, err := c.beginWork()
	if err != nil {
		return nil, nil, err
	}
	ctx, application, err := c.peerHTTPApplication(ctx)
	if err != nil {
		workDone()
		return nil, nil, err
	}
	return ctx, &peerMessageOperation{application: application, workDone: workDone}, nil
}

func (operation *peerMessageOperation) commit(c *Core, message Message) error {
	// Serialize with incoming delivery, same-ID replay and explicit cleanup.
	// appendMessageLocked reconciles ErrAtomicCommitted with the in-memory
	// record and keeps durability uncertainty. No completion is claimed on a
	// timeout; this operation and its budgets remain owned until I/O returns.
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.appendMessageLocked(message)
}

func (operation *peerMessageOperation) finish() {
	operation.application.finish()
	operation.workDone()
	operation.application, operation.workDone = nil, nil
}
