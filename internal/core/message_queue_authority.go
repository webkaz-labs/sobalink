package core

import (
	"context"
	"time"
)

type messageQueueSelectionKey struct{}
type messageQueueSelection struct {
	id        string
	identity  string
	senderID  string
	trust     Trust
	backend   NetworkBackend
	allowSend bool
}

// capturePeerHTTPRoute calls this with Core.mu held before selecting any route.
// The context carries the exact reviewed relationship and backend; peerJSON may
// not recapture a replacement backend after an earlier permission precheck.
func (c *Core) checkMessageQueueSelectionLocked(id string, selected *messageQueueSelection, node NetworkBackend) error {
	if selected == nil || selected.id == "" || id != selected.trust.ID || selected.backend == nil || selected.backend != node || c.node != selected.backend {
		return messageStoreError("message_permission")
	}
	if err := c.messageQueueGuardLocked(); err != nil {
		return err
	}
	row := c.messageStore.state.Outgoing[selected.id]
	if row == nil || row.Identity != selected.identity || c.resourceIdentity != selected.identity || row.SenderID != selected.senderID || row.Trust != selected.trust || row.Network != selected.trust.Network || !c.outboxTrustCurrentLocked(selected.trust) {
		return messageStoreError("message_permission")
	}
	if selected.allowSend && c.messageQueue != nil && c.messageQueue.pendingCancel[selected.id] {
		return messageStoreError("message_cancelled")
	}
	if selected.allowSend && (row.NoMoreSend || row.Cancelled || !c.messageQueueNowLocked().Before(row.View.ExpiresAt)) {
		return messageStoreError("message_expired")
	}
	if horizon, ok := textRequestHorizon(selected.id); !ok || !c.messageQueueNowLocked().Before(horizon) {
		return messageStoreError("message_expired")
	}
	return nil
}
func (c *Core) messageQueueJSON(ctx context.Context, run *messageQueueRun, allowSend bool, method, path string, input, out any) error {
	selected := run.selection
	selected.allowSend = allowSend
	// Current sender identity is checked for every request, including receipt-only
	// inquiry. Mixed backends without a canonical SelfID fail closed here.
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	state, err := selected.backend.State(call)
	cancel()
	if err != nil {
		return messageStoreError("message_peer_offline")
	}
	if state.SelfID == "" || state.SelfID != selected.senderID {
		return messageStoreError("message_identity_changed")
	}
	c.mu.RLock()
	err = c.checkMessageQueueSelectionLocked(selected.trust.ID, &selected, c.node)
	c.mu.RUnlock()
	if err != nil {
		return err
	}
	return c.peerJSON(context.WithValue(ctx, messageQueueSelectionKey{}, &selected), selected.trust.ID, method, path, input, out)
}
