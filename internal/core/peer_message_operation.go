package core

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// Retry only a certified pre-callback admission refusal. Each attempt releases
// both Core.mu and its incoming commit lease before waiting, then revalidates
// cancellation, trust, generation, duplicates and storage from scratch. This
// local admission budget does not extend the request or any publication lease.
func (c *Core) commitIncomingPeerMessage(ctx context.Context, trust Trust, peer, id, text string) (int, map[string]string) {
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		status, body, retry := c.tryCommitIncomingPeerMessage(ctx, trust, peer, id, text)
		if !retry || !time.Now().Before(deadline) {
			return status, body
		}
		timer := time.NewTimer(min(20*time.Millisecond, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return http.StatusForbidden, map[string]string{"error": "request refused"}
		case <-c.ctx.Done():
			timer.Stop()
			return http.StatusForbidden, map[string]string{"error": "request refused"}
		case <-timer.C:
		}
		if !time.Now().Before(deadline) {
			return status, body
		}
	}
}

func (c *Core) tryCommitIncomingPeerMessage(ctx context.Context, trust Trust, peer, id, text string) (int, map[string]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	refused := func(status int) (int, map[string]string, bool) {
		return status, map[string]string{"error": "request refused"}, false
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
	// An uncertain incoming publication retains its original locator separately
	// from the certified projection. Exact replay may certify only that snapshot.
	if c.messageStore != nil && c.messageStore.candidate != nil {
		for _, message := range c.messageStore.candidate.Immediate {
			if message.ID == id && message.PeerID == peer && message.Direction == "incoming" {
				if message.Text != text {
					return refused(http.StatusConflict)
				}
				if err := c.reconcileMessageStoreLocked(); err != nil {
					return http.StatusInsufficientStorage, map[string]string{"code": "message_history_unavailable", "error": messageHistoryError(err, true).Error()}, false
				}
				return http.StatusOK, map[string]string{"id": message.ID, "status": "received"}, false
			}
		}
	}
	for _, message := range c.messages {
		if message.ID != id || message.PeerID != peer || message.Direction != "incoming" {
			continue
		}
		if message.Text != text {
			return refused(http.StatusConflict)
		}
		if c.messageStore != nil && c.messageStore.frozen != "" {
			return http.StatusInsufficientStorage, map[string]string{"code": "message_history_unavailable", "error": messageHistoryError(messageStoreError(c.messageStore.frozen), true).Error()}, false
		}
		return http.StatusOK, map[string]string{"id": message.ID, "status": "received"}, false
	}

	message := Message{ID: id, PeerID: peer, Text: text, Direction: "incoming", CreatedAt: time.Now().UTC(), Status: "received"}
	if err := c.appendMessageLocked(message); err != nil {
		_, retry := err.(*messageStoreAdmissionBusy)
		return http.StatusInsufficientStorage, map[string]string{"code": "message_history_unavailable", "error": messageHistoryError(err, errors.Is(err, config.ErrAtomicCommitted)).Error()}, retry
	}
	return http.StatusOK, map[string]string{"id": message.ID, "status": "received"}, false
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
