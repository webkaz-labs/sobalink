package core

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func messageQueueFixture(t *testing.T) *Core {
	t.Helper()
	c := outboxFixture(t)
	c.profile.Version = 3
	c.startup.Revocations = map[string]string{}
	for i := range c.profile.Peers {
		c.profile.Peers[i].RevocationEpoch = strings.Repeat("f", 32)
		c.startup.Revocations[c.profile.Peers[i].ID] = c.profile.Peers[i].RevocationEpoch
	}
	if err := c.initializeMessageStore(true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c.messageStore != nil && c.messageStore.root != nil {
			_ = c.messageStore.root.Close()
		}
	})
	return c
}
func messageQueuePrepareFixture(t *testing.T, c *Core, peer, text string) TextOutboxRecord {
	t.Helper()
	result, err := c.prepareMessageQueue(context.Background(), newTextRequestID(time.Now()), resourceCatalogProcessToken(c.resourceNonce), peer, text, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return result.(TextOutboxRecord)
}
func messageQueueCommitFixture(t *testing.T, c *Core, review TextOutboxRecord) TextOutboxRecord {
	t.Helper()
	result, err := c.commitMessageQueue(context.Background(), review.OutboxID, review.ProcessID, review.ReviewRevision, true)
	if err != nil {
		t.Fatal(err)
	}
	return result.(TextOutboxRecord)
}
func messageQueueReopenFixture(t *testing.T, c *Core) {
	t.Helper()
	if err := c.messageStore.root.Close(); err != nil {
		t.Fatal(err)
	}
	c.messageStore = nil
	c.messageQueue = nil
	c.resourceNonce = randomID()
	if err := c.initializeMessageStore(false); err != nil {
		t.Fatal(err)
	}
}
func messageQueueLeaseFixture(row *messageOutgoing) transfer.DurableLease {
	horizon, _ := textRequestHorizon(row.Offer.MessageID)
	return transfer.DurableLease{Version: DurableTextVersion, DeliveryID: row.Offer.MessageID, RecipientID: row.Offer.RecipientID, Digest: row.Offer.Digest, ReceiverEpoch: strings.Repeat("d", 32), Token: strings.Repeat("e", 32), ExpiresAt: row.Offer.ExpiresAt, ReceiptUntil: horizon}
}
func messageQueueReceiptFixture(row *messageOutgoing, lease transfer.DurableLease) DurableTextReceipt {
	return DurableTextReceipt{Lease: lease, MessageID: row.Offer.MessageID, TextBytes: row.Offer.TextBytes, TextSHA256: row.Offer.TextSHA256, SavedAt: time.Now().UTC()}
}
func messageQueueRunFixture(c *Core, id string) *messageQueueRun {
	row := c.messageStore.state.Outgoing[id]
	ctx, cancel := context.WithCancel(c.ctx)
	run := &messageQueueRun{id: id, ctx: ctx, cancel: cancel, joined: make(chan struct{}), selection: messageQueueSelection{id: id, identity: row.Identity, senderID: row.SenderID, trust: row.Trust, backend: c.node}}
	c.messageQueueRuntimeLocked().runs[id] = run
	return run
}

func TestMessageQueueOfflineReviewCommitAndOriginalLocatorRestart(t *testing.T) {
	c := messageQueueFixture(t)
	// The fixture has an identity but no online route. Enqueue needs no delivery.
	review := messageQueuePrepareFixture(t, c, "peer-b", "synthetic offline text")
	if review.OutboxID != review.RequestID || review.RequestID != review.MessageID || review.Text == "" || review.ProcessID == "" {
		t.Fatal("review identity or explicit text missing")
	}
	again, err := c.prepareMessageQueue(context.Background(), review.OutboxID, review.ProcessID, review.PeerID, review.Text, time.Time{})
	if err != nil || !reflect.DeepEqual(review, again) {
		t.Fatalf("default expiry or opaque review changed: %v", err)
	}
	for _, change := range []struct {
		peer, text string
		expiry     time.Time
	}{{"peer-c", review.Text, review.ExpiresAt}, {review.PeerID, "different text", review.ExpiresAt}, {review.PeerID, review.Text, review.ExpiresAt.Add(time.Minute)}} {
		if _, err := c.prepareMessageQueue(context.Background(), review.OutboxID, review.ProcessID, change.peer, change.text, change.expiry); networkErrorCode(err) != "message_conflict" {
			t.Fatalf("changed preparation was accepted: %v", err)
		}
	}
	committed := messageQueueCommitFixture(t, c, review)
	if committed.ProcessID != "" || committed.Status != "queued" || committed.Admission != "enqueued" || len(c.messages) != 1 || c.messages[0].Text != review.Text {
		t.Fatal("enqueue and visible projection were not one saved row")
	}
	messageQueueReopenFixture(t, c)
	recovered, err := c.messageQueueCommand(context.Background(), "message.queue.status", "status", json.RawMessage(`{"outboxId":"`+review.OutboxID+`"}`))
	if err != nil || !reflect.DeepEqual(committed, recovered) {
		t.Fatalf("original committed locator changed after restart: %v", err)
	}
	repeated, err := c.commitMessageQueue(context.Background(), review.OutboxID, review.ProcessID, review.ReviewRevision, true)
	if err != nil || !reflect.DeepEqual(committed, repeated) {
		t.Fatalf("exact commit retry after restart lost original record: %v", err)
	}
}
func TestMessageQueueUncommittedReviewDoesNotSurviveRestart(t *testing.T) {
	c := messageQueueFixture(t)
	review := messageQueuePrepareFixture(t, c, "peer-b", "synthetic uncommitted text")
	messageQueueReopenFixture(t, c)
	if _, err := c.commitMessageQueue(context.Background(), review.OutboxID, review.ProcessID, review.ReviewRevision, true); networkErrorCode(err) != "message_stale" {
		t.Fatalf("old process review committed: %v", err)
	}
	if len(c.messageStore.state.Outgoing) != 0 || len(c.messages) != 0 {
		t.Fatal("uncommitted preparation became durable")
	}
}
func TestMessageQueueUncertainCommitKeepsOriginalLocator(t *testing.T) {
	c := messageQueueFixture(t)
	review := messageQueuePrepareFixture(t, c, "peer-b", "synthetic uncertain text")
	c.messageStore.write = func(string, []byte) error { return config.ErrAtomicCommitted }
	if _, err := c.commitMessageQueue(context.Background(), review.OutboxID, review.ProcessID, review.ReviewRevision, true); err == nil {
		t.Fatal("uncertain commit claimed success")
	}
	c.mu.Lock()
	_, err := c.messageQueueStatusLocked(review.OutboxID)
	c.mu.Unlock()
	if err == nil || networkErrorCode(err) == "message_not_found" {
		t.Fatalf("unknown commit was lost: %v", err)
	}
	if c.messageStore.candidate == nil || c.messageStore.candidate.Outgoing[review.OutboxID] == nil || c.messageQueue.prepared[review.OutboxID] == nil {
		t.Fatal("uncertain original evidence/preparation owner disappeared")
	}
}
func TestMessageQueuePreparationCapacityAndReducedBudgetCompletion(t *testing.T) {
	c := messageQueueFixture(t)
	var reviews []TextOutboxRecord
	for i := 0; i < messageActivePeerLimit; i++ {
		reviews = append(reviews, messageQueuePrepareFixture(t, c, "peer-b", "synthetic reserved text"))
	}
	if _, err := c.prepareMessageQueue(context.Background(), newTextRequestID(time.Now()), resourceCatalogProcessToken(c.resourceNonce), "peer-b", "one too many", time.Time{}); networkErrorCode(err) != "message_peer_capacity" && networkErrorCode(err) != "message_active_capacity" {
		t.Fatalf("preparations escaped active-per-peer budget: %v", err)
	}
	first := messageQueueCommitFixture(t, c, reviews[0])
	c.mu.Lock()
	rows, _, active, perPeer := c.messageQueueReservationsLocked(c.messageStore.state.Outgoing)
	if rows != 7 || active != 7 || perPeer["peer-b"] != 7 {
		t.Fatal("promotion double charged preparation")
	}
	row := c.messageStore.state.Outgoing[first.OutboxID]
	lease := messageQueueLeaseFixture(row)
	receipt := messageQueueReceiptFixture(row, lease)
	run := messageQueueRunFixture(c, first.OutboxID)
	c.capacity.Resources["messageStorageBytes"] = capacity.Limited(1)
	c.mu.Unlock()
	if err := c.recordMessageQueueStatus(first.OutboxID, DurableTextStatus{Lease: lease, State: "received", Receipt: &receipt, ReceiverNow: time.Now()}, true); err != nil {
		t.Fatalf("retained admission could not finalize after quota reduction: %v", err)
	}
	c.mu.Lock()
	if !c.messageStore.state.Outgoing[first.OutboxID].Active {
		t.Fatal("receipt released active slot before physical join")
	}
	c.messageQueue.completions[first.OutboxID].joined = true
	close(run.joined)
	if err := c.drainMessageQueueCompletionLocked(first.OutboxID); err != nil {
		t.Fatal(err)
	}
	if c.messageStore.state.Outgoing[first.OutboxID].Active {
		t.Fatal("joined confirmed receipt retained active slot")
	}
	c.mu.Unlock()
	run.cancel()
}
func TestMessageQueueFailedTerminalSaveRetainsActiveAndCompletion(t *testing.T) {
	c := messageQueueFixture(t)
	view := messageQueueCommitFixture(t, c, messageQueuePrepareFixture(t, c, "peer-b", "synthetic terminal fence"))
	c.mu.Lock()
	run := messageQueueRunFixture(c, view.OutboxID)
	completion := &messageQueueCompletion{joined: true, code: "message_permission"}
	c.messageQueue.completions[view.OutboxID] = completion
	write := c.messageStore.write
	c.messageStore.write = func(string, []byte) error { return errors.New("synthetic write denied") }
	err := c.drainMessageQueueCompletionLocked(view.OutboxID)
	if err == nil || !c.messageStore.state.Outgoing[view.OutboxID].Active || c.messageQueue.runs[view.OutboxID] != run || c.messageQueue.completions[view.OutboxID] != completion {
		t.Fatal("failed terminal save freed authority/accounting owner")
	}
	c.messageStore.write = write
	c.mu.Unlock()
	run.cancel()
}
func TestMessageQueueCancelJoinsBeforeRetiringActive(t *testing.T) {
	c := messageQueueFixture(t)
	view := messageQueueCommitFixture(t, c, messageQueuePrepareFixture(t, c, "peer-b", "synthetic cancellable text"))
	c.mu.Lock()
	run := messageQueueRunFixture(c, view.OutboxID)
	c.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := c.cancelMessageQueue(ctx, view.OutboxID)
	if err != nil || result.(TextOutboxRecord).Status != "cancel_requested" {
		t.Fatalf("cancel fence missing: %v", err)
	}
	c.mu.Lock()
	row := c.messageStore.state.Outgoing[view.OutboxID]
	if !row.Active || !row.NoMoreSend || !row.Cancelled || run.ctx.Err() == nil {
		t.Fatal("cancel did not fence and retain physical owner")
	}
	c.messageQueue.completions[view.OutboxID] = &messageQueueCompletion{joined: true}
	close(run.joined)
	if err := c.drainMessageQueueCompletionLocked(view.OutboxID); err != nil {
		t.Fatal(err)
	}
	if c.messageStore.state.Outgoing[view.OutboxID].Active {
		t.Fatal("joined durable cancellation did not retire active admission")
	}
	c.mu.Unlock()
}
func TestMessageQueueSelectionRejectsReplacementAndCancellation(t *testing.T) {
	c := messageQueueFixture(t)
	view := messageQueueCommitFixture(t, c, messageQueuePrepareFixture(t, c, "peer-b", "synthetic authority text"))
	c.mu.Lock()
	run := messageQueueRunFixture(c, view.OutboxID)
	selected := run.selection
	selected.allowSend = true
	if err := c.checkMessageQueueSelectionLocked("peer-b", &selected, c.node); err != nil {
		t.Fatal(err)
	}
	original := c.node
	c.node = &pipeNode{}
	if err := c.checkMessageQueueSelectionLocked("peer-b", &selected, c.node); err == nil {
		t.Fatal("unbound backend replacement accepted")
	}
	c.node = original
	c.profile.Peers[0].RevocationEpoch = "replacement"
	if err := c.checkMessageQueueSelectionLocked("peer-b", &selected, c.node); err == nil {
		t.Fatal("relationship replacement accepted")
	}
	c.profile.Peers[0].RevocationEpoch = strings.Repeat("f", 32)
	if err := c.mutateMessageStoreLocked(func(next *messageEnvelope) error { next.Outgoing[view.OutboxID].NoMoreSend = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := c.checkMessageQueueSelectionLocked("peer-b", &selected, c.node); err == nil {
		t.Fatal("post-cancel send admission accepted")
	}
	selected.allowSend = false
	if err := c.checkMessageQueueSelectionLocked("peer-b", &selected, c.node); err != nil {
		t.Fatalf("current-permission receipt inquiry rejected: %v", err)
	}
	c.mu.Unlock()
	run.cancel()
}

func TestMessageQueueFailedCancelSaveNeverRestartsSend(t *testing.T) {
	c := messageQueueFixture(t)
	view := messageQueueCommitFixture(t, c, messageQueuePrepareFixture(t, c, "peer-b", "synthetic cancellation retry text"))
	c.mu.Lock()
	run := messageQueueRunFixture(c, view.OutboxID)
	originalWrite := c.messageStore.write
	c.messageStore.write = func(string, []byte) error { return errors.New("synthetic prepublication failure") }
	c.mu.Unlock()
	if _, err := c.cancelMessageQueue(context.Background(), view.OutboxID); err == nil {
		t.Fatal("failed cancellation save returned success")
	}
	c.mu.Lock()
	if !c.messageQueue.pendingCancel[view.OutboxID] || run.ctx.Err() == nil {
		t.Fatal("failed cancellation lost its process-local fence")
	}
	selected := run.selection
	selected.allowSend = true
	if err := c.checkMessageQueueSelectionLocked("peer-b", &selected, c.node); networkErrorCode(err) != "message_cancelled" {
		t.Fatalf("old row regained send authority: %v", err)
	}
	c.messageQueue.completions[view.OutboxID] = &messageQueueCompletion{joined: true, code: "message_peer_offline"}
	close(run.joined)
	if err := c.drainMessageQueueCompletionLocked(view.OutboxID); err == nil || c.messageQueue.runs[view.OutboxID] == nil || !c.messageStore.state.Outgoing[view.OutboxID].Active {
		t.Fatal("failed cancellation released its completion/active fence")
	}
	c.messageStore.write = originalWrite
	if err := c.drainMessageQueueCompletionLocked(view.OutboxID); err != nil {
		t.Fatal(err)
	}
	row := c.messageStore.state.Outgoing[view.OutboxID]
	if !row.NoMoreSend || !row.Cancelled || row.Active || c.messageQueue.pendingCancel[view.OutboxID] || c.messageQueue.runs[view.OutboxID] != nil {
		t.Fatal("retry did not certify original cancellation before releasing work")
	}
	c.mu.Unlock()
}
