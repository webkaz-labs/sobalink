package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// Synthetic owned profile; this helper grants no ownership through a test flag.
func messageStoreFixture(t *testing.T) *Core {
	t.Helper()
	dir := t.TempDir()
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	c := &Core{dir: dir, ctx: ctx, cancel: cancel, resourceLock: lock, resourceIdentity: strings.Repeat("a", 32), resourceNonce: strings.Repeat("b", 32), capacity: capacity.Defaults(), profile: Profile{Version: currentProfileVersion, DurableFiles: &DurableProfile{Version: 1, Epoch: strings.Repeat("c", 32), Phase: "initializing"}}}
	t.Cleanup(func() { cancel(); c.mu.Lock(); _ = c.closeMessageStoreLocked(); c.mu.Unlock(); _ = lock.Close() })
	if err = c.initializeMessageStore(true); err != nil {
		t.Fatal(err)
	}
	c.profile.DurableFiles.Phase = "ready"
	return c
}
func storedTextOffer(t *testing.T, now time.Time, recipient, text string) DurableTextOffer {
	t.Helper()
	id := newTextRequestID(now)
	created, _ := textRequestCreated(id)
	offer := DurableTextOffer{Version: 1, MessageID: id, RecipientID: recipient, TextBytes: int64(len(text)), TextSHA256: textSHA256(text), CreatedAt: created, ExpiresAt: created.Add(time.Hour), SentAt: now.UTC()}
	var err error
	offer.Digest, err = durableTextDigest(offer)
	if err != nil {
		t.Fatal(err)
	}
	return offer
}
func storedTextLease(offer DurableTextOffer) transfer.DurableLease {
	until, _ := textRequestHorizon(offer.MessageID)
	return transfer.DurableLease{Version: 1, DeliveryID: offer.MessageID, RecipientID: offer.RecipientID, Digest: offer.Digest, ReceiverEpoch: strings.Repeat("d", 32), Token: strings.Repeat("e", 32), ExpiresAt: offer.ExpiresAt, ReceiptUntil: until}
}
func storedIncoming(t *testing.T, now time.Time, peer, text string) *messageIncoming {
	t.Helper()
	offer := storedTextOffer(t, now, "receiver", text)
	lease := storedTextLease(offer)
	lease.ReceiverEpoch = strings.Repeat("c", 32)
	return &messageIncoming{PeerID: peer, Network: "tailnet", TrustGeneration: 1, RevocationEpoch: strings.Repeat("f", 32), Offer: offer, Lease: lease, State: "offered"}
}
func storedOutgoing(t *testing.T, c *Core, now time.Time, peer, text string) *messageOutgoing {
	t.Helper()
	offer := storedTextOffer(t, now, peer, text)
	return &messageOutgoing{View: TextOutboxRecord{Version: 1, Kind: "text_outbox", OutboxID: offer.MessageID, RequestID: offer.MessageID, MessageID: offer.MessageID, PeerID: peer, ReviewRevision: "synthetic-review", Admission: "enqueued", Status: "queued", DeliveryOutcome: "not_attempted", TextBytes: offer.TextBytes, SHA256: offer.TextSHA256, CreatedAt: offer.CreatedAt, ExpiresAt: offer.ExpiresAt, UpdatedAt: offer.CreatedAt}, Identity: c.resourceIdentity, Network: "tailnet", SenderID: "sender", Trust: Trust{ID: peer, Network: "tailnet", Generation: 1, RevocationEpoch: strings.Repeat("f", 32)}, Signature: "synthetic-signature", Offer: offer, Text: text, BodyRetained: true, Visible: true, Active: true}
}
func mutateStoredMessages(t *testing.T, c *Core, change func(*messageEnvelope) error) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.mutateMessageStoreLocked(change); err != nil {
		t.Fatal(err)
	}
}
func messageErrorCode(err error) string {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return ""
}

func TestTextIdentityDigestAndLeaseHardHorizon(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 678000000, time.UTC)
	offer := storedTextOffer(t, now, "receiver", "synthetic text")
	if created, ok := textRequestCreated(offer.MessageID); !ok || !created.Equal(now) {
		t.Fatal(created, ok)
	}
	for _, bad := range []string{strings.Replace(offer.MessageID, "mt1_", "ob1_", 1), strings.ToUpper(offer.MessageID), offer.MessageID + "0", "mt1_123_a"} {
		if _, ok := textRequestCreated(bad); ok {
			t.Fatal("invalid key admitted")
		}
	}
	if !textRequestFresh(offer.MessageID, now.Add(textClockSkew)) || textRequestFresh(offer.MessageID, now.Add(textClockSkew+time.Millisecond)) {
		t.Fatal("creation window differs")
	}
	original := offer.Digest
	offer.SentAt = now.Add(time.Hour)
	again, err := durableTextDigest(offer)
	if err != nil || again != original {
		t.Fatal("fresh transport time changed immutable digest")
	}
	lease := storedTextLease(offer)
	if !textLeaseMatches(offer, lease) {
		t.Fatal("valid text lease refused")
	}
	lease.ReceiptUntil = offer.ExpiresAt.Add(textReceiptGrace)
	if textLeaseMatches(offer, lease) {
		t.Fatal("file horizon was accepted for text")
	}
	changed := offer
	changed.ExpiresAt = offer.ExpiresAt.Add(time.Second)
	digest, err := durableTextDigest(changed)
	if err != nil || digest == original {
		t.Fatal("deadline omitted from digest")
	}
}

func TestMessageStoreLifecycleBusyPrecedesMutation(t *testing.T) {
	c := messageStoreFixture(t)
	before := append([]byte(nil), c.messageStore.confirmedDisk...)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- c.resourceLock.WithOwnership(c.dir, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	called := false
	c.mu.Lock()
	err := c.mutateMessageStoreLocked(func(*messageEnvelope) error { called = true; return nil })
	c.mu.Unlock()
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if messageErrorCode(err) != "message_storage_busy" || called || c.messageStore.frozen != "" || !bytes.Equal(before, c.messageStore.confirmedDisk) {
		t.Fatal("busy changed store state", err, called)
	}
}

func TestMessageStoreUncertainCandidateRetainsLocatorAndReconcilesExactly(t *testing.T) {
	c := messageStoreFixture(t)
	row := storedOutgoing(t, c, time.Now(), "recipient", "retained synthetic payload")
	id := row.View.OutboxID
	c.messageStore.afterWrite = func() error { return os.ErrPermission }
	c.mu.Lock()
	err := c.mutateMessageStoreLocked(func(next *messageEnvelope) error { next.Outgoing[id] = row; return nil })
	view, viewErr := c.messageQueueViewLocked(id)
	c.mu.Unlock()
	if !errors.Is(err, config.ErrAtomicCommitted) || view.OutboxID != id || messageErrorCode(viewErr) != "message_storage_uncertain" || c.messageStore.state.Outgoing[id] != nil || len(c.messages) != 0 {
		t.Fatal("uncertain write was certified or locator lost", err, view, viewErr)
	}
	called := false
	c.mu.Lock()
	err = c.mutateMessageStoreLocked(func(*messageEnvelope) error { called = true; return nil })
	c.mu.Unlock()
	if called || messageErrorCode(err) != "message_storage_uncertain" {
		t.Fatal("uncertainty allowed new authority")
	}
	c.messageStore.afterWrite = nil
	c.mu.Lock()
	err = c.reconcileMessageStoreLocked()
	c.mu.Unlock()
	if err != nil || c.messageStore.candidate != nil || len(c.messages) != 1 {
		t.Fatal("exact publication did not reconcile", err)
	}
}

func TestMessageStoreUncertainCandidateRefusesReplacement(t *testing.T) {
	c := messageStoreFixture(t)
	c.messageStore.afterWrite = func() error { return os.ErrPermission }
	c.mu.Lock()
	_ = c.appendMessageLocked(Message{ID: "retained", PeerID: "peer", Direction: "incoming", Status: "received", Text: "synthetic", CreatedAt: time.Now()})
	c.mu.Unlock()
	c.messageStore.afterWrite = nil
	if err := config.AtomicWrite(filepath.Join(c.dir, "messages.json"), c.messageStore.confirmedDisk); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	err := c.reconcileMessageStoreLocked()
	c.mu.Unlock()
	if err == nil || c.messageStore.candidate == nil || c.messageStore.frozen == "" {
		t.Fatal("replacement erased retained uncertainty")
	}
}

func TestMessageStoreMandatoryReadyFormatAndIdentity(t *testing.T) {
	for _, mode := range []string{"missing", "array", "changed-version", "changed-identity"} {
		t.Run(mode, func(t *testing.T) {
			c := messageStoreFixture(t)
			c.mu.Lock()
			_ = c.closeMessageStoreLocked()
			c.messageStore = nil
			c.mu.Unlock()
			path := filepath.Join(c.dir, "messages.json")
			switch mode {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "array":
				if err := config.AtomicWrite(path, []byte("[]")); err != nil {
					t.Fatal(err)
				}
			default:
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var state messageEnvelope
				if err = json.Unmarshal(data, &state); err != nil {
					t.Fatal(err)
				}
				if mode == "changed-version" {
					state.Version++
				} else {
					state.Identity = "different"
				}
				data, _ = json.Marshal(state)
				if err = config.AtomicWrite(path, data); err != nil {
					t.Fatal(err)
				}
			}
			before, beforeErr := os.ReadFile(path)
			err := c.initializeMessageStore(false)
			after, afterErr := os.ReadFile(path)
			if err == nil || !bytes.Equal(before, after) || errors.Is(beforeErr, os.ErrNotExist) != errors.Is(afterErr, os.ErrNotExist) {
				t.Fatal("invalid mandatory store recreated or adopted", err)
			}
		})
	}
	if err := checkDurableProfileInventory(t.TempDir(), Profile{Version: 2}); messageErrorCode(err) != "profile_format_unsupported" {
		t.Fatal("old profile accepted", err)
	}
}

func TestMessageStoreBodyReceiptCleanupAndLoweredLimit(t *testing.T) {
	c := messageStoreFixture(t)
	text := "body" + strings.Repeat("&", 12000)
	row := storedIncoming(t, time.Now(), "sender", text)
	id := row.Offer.MessageID
	mutateStoredMessages(t, c, func(next *messageEnvelope) error { next.Incoming[id] = row; return nil })
	c.capacity.Resources["messageStorageBytes"] = capacity.Limited(256)
	saved := time.Now().UTC()
	mutateStoredMessages(t, c, func(next *messageEnvelope) error {
		r := next.Incoming[id]
		r.State = "received"
		r.Text = text
		r.BodyRetained = true
		r.Visible = true
		r.Receipt = &DurableTextReceipt{Lease: r.Lease, MessageID: id, TextBytes: r.Offer.TextBytes, TextSHA256: r.Offer.TextSHA256, SavedAt: saved}
		return nil
	})
	if len(c.messages) != 1 || c.messageStore.state.Incoming[id].Receipt == nil {
		t.Fatal("body/visibility/receipt did not commit together")
	}
	c.mu.Lock()
	err := c.saveMessageHistoryLocked([]Message{})
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	retained := c.messageStore.state.Incoming[id]
	if len(c.messages) != 0 || retained.BodyRetained || retained.Text != "" || retained.Receipt == nil || !retained.Receipt.SavedAt.Equal(saved) {
		t.Fatal("cleanup erased evidence or retained body")
	}
	c.mu.Lock()
	err = c.mutateMessageStoreLocked(func(next *messageEnvelope) error {
		r := next.Incoming[id]
		r.Text = text
		r.BodyRetained = true
		r.Visible = true
		return nil
	})
	c.mu.Unlock()
	if err == nil {
		t.Fatal("replay restored hidden received body")
	}
}

func TestMessageStoreActiveOutgoingCleanupKeepsOnlySendBody(t *testing.T) {
	c := messageStoreFixture(t)
	row := storedOutgoing(t, c, time.Now(), "recipient", "only retained payload")
	id := row.View.OutboxID
	mutateStoredMessages(t, c, func(next *messageEnvelope) error { next.Outgoing[id] = row; return nil })
	c.mu.Lock()
	err := c.saveMessageHistoryLocked([]Message{})
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	r := c.messageStore.state.Outgoing[id]
	if r.Visible || !r.BodyRetained || r.Text != row.Text || len(c.messages) != 0 {
		t.Fatal("visible cleanup removed active send body")
	}
	c.mu.Lock()
	err = c.mutateMessageStoreLocked(func(next *messageEnvelope) error { next.Outgoing[id].Active = false; return nil })
	c.mu.Unlock()
	if err == nil {
		t.Fatal("active admission released before durable terminal fence")
	}
	mutateStoredMessages(t, c, func(next *messageEnvelope) error {
		r := next.Outgoing[id]
		r.NoMoreSend = true
		r.Cancelled = true
		r.View.Status = "cancelled"
		return nil
	})
	mutateStoredMessages(t, c, func(next *messageEnvelope) error {
		r := next.Outgoing[id]
		r.Active = false
		r.Text = ""
		r.BodyRetained = false
		return nil
	})
}

func TestMessageStoreAdmissionLimitsAndFailedTerminalSave(t *testing.T) {
	c := messageStoreFixture(t)
	for range messageActivePeerLimit {
		row := storedIncoming(t, time.Now(), "sender", "reserved")
		mutateStoredMessages(t, c, func(next *messageEnvelope) error { next.Incoming[row.Offer.MessageID] = row; return nil })
	}
	extra := storedIncoming(t, time.Now(), "sender", "extra")
	c.mu.Lock()
	err := c.mutateMessageStoreLocked(func(next *messageEnvelope) error { next.Incoming[extra.Offer.MessageID] = extra; return nil })
	c.mu.Unlock()
	if messageErrorCode(err) != "message_peer_capacity" {
		t.Fatal("per-peer active bound not enforced", err)
	}
	c.messageStore.write = func(string, []byte) error { return os.ErrPermission }
	c.mu.Lock()
	err = c.mutateMessageStoreLocked(func(next *messageEnvelope) error {
		for _, r := range next.Incoming {
			r.State = "expired"
			break
		}
		return nil
	})
	c.mu.Unlock()
	if err == nil {
		t.Fatal("fixture terminal save unexpectedly succeeded")
	}
	c.messageStore.write = nil
	c.mu.Lock()
	err = c.mutateMessageStoreLocked(func(next *messageEnvelope) error { next.Incoming[extra.Offer.MessageID] = extra; return nil })
	c.mu.Unlock()
	if messageErrorCode(err) != "message_peer_capacity" {
		t.Fatal("failed terminal save released active capacity", err)
	}
}

func TestMessageStoreFullRetainedEvidenceRefusesAdmission(t *testing.T) {
	c := messageStoreFixture(t)
	mutateStoredMessages(t, c, func(next *messageEnvelope) error {
		for range messageStoreMaxRows {
			row := storedIncoming(t, time.Now(), "sender", "tiny")
			row.State = "expired"
			next.Incoming[row.Offer.MessageID] = row
		}
		return nil
	})
	extra := storedIncoming(t, time.Now(), "sender", "extra")
	c.mu.Lock()
	err := c.mutateMessageStoreLocked(func(next *messageEnvelope) error { next.Incoming[extra.Offer.MessageID] = extra; return nil })
	c.mu.Unlock()
	if err == nil || len(c.messageStore.state.Incoming) != messageStoreMaxRows {
		t.Fatal("full evidence pool discarded or admitted", err)
	}
	mutateStoredMessages(t, c, func(*messageEnvelope) error { return nil })
}

func TestMessageStoreClockRollbackAndExactLeaseNeverRegress(t *testing.T) {
	c := messageStoreFixture(t)
	row := storedOutgoing(t, c, time.Now(), "recipient", "exact lease")
	id := row.View.OutboxID
	mutateStoredMessages(t, c, func(next *messageEnvelope) error { next.Outgoing[id] = row; return nil })
	lease := storedTextLease(row.Offer)
	mutateStoredMessages(t, c, func(next *messageEnvelope) error { next.Outgoing[id].Lease = &lease; return nil })
	c.mu.Lock()
	err := c.mutateMessageStoreLocked(func(next *messageEnvelope) error { next.Outgoing[id].Lease = nil; return nil })
	c.mu.Unlock()
	if err == nil {
		t.Fatal("persisted lease cleared for re-offer")
	}
	c.messageStore.now = func() time.Time { return c.messageStore.state.HighWater.Add(-time.Second) }
	called := false
	c.mu.Lock()
	err = c.mutateMessageStoreLocked(func(*messageEnvelope) error { called = true; return nil })
	c.mu.Unlock()
	if called || messageErrorCode(err) != "message_clock_rollback" {
		t.Fatal("rollback admitted mutation", err)
	}
}

func TestMessageStoreDetachesCallerRowsAndBoundsGlobalActive(t *testing.T) {
	c := messageStoreFixture(t)
	row := storedIncoming(t, time.Now(), "sender", "synthetic")
	id := row.Offer.MessageID
	mutateStoredMessages(t, c, func(next *messageEnvelope) error { next.Incoming[id] = row; return nil })
	row.State = "cancelled"
	if c.messageStore.state.Incoming[id].State != "offered" {
		t.Fatal("caller alias changed certified state without a transaction")
	}
	for i := 1; i < messageActiveLimit; i++ {
		row := storedIncoming(t, time.Now(), fmt.Sprintf("sender-%d", i), "reserved")
		mutateStoredMessages(t, c, func(next *messageEnvelope) error { next.Incoming[row.Offer.MessageID] = row; return nil })
	}
	extra := storedIncoming(t, time.Now(), "another-sender", "extra")
	c.mu.Lock()
	err := c.mutateMessageStoreLocked(func(next *messageEnvelope) error { next.Incoming[extra.Offer.MessageID] = extra; return nil })
	c.mu.Unlock()
	if messageErrorCode(err) != "message_active_capacity" {
		t.Fatal("global active limit was not enforced", err)
	}
}

func TestTextIdentityOriginalLifetimeBoundary(t *testing.T) {
	now := time.Now().UTC()
	offer := storedTextOffer(t, now, "synthetic-recipient", "synthetic text")
	for _, lifetime := range []time.Duration{time.Minute - time.Nanosecond, time.Minute, textMaximumLifetime, textMaximumLifetime + time.Nanosecond} {
		offer.ExpiresAt = offer.CreatedAt.Add(lifetime)
		_, err := durableTextDigest(offer)
		valid := lifetime >= time.Minute && lifetime <= textMaximumLifetime
		if (err == nil) != valid {
			t.Fatal("original lifetime boundary differs", lifetime, err)
		}
	}
}
