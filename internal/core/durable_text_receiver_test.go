package core

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func durableTextReceiverFixture(t *testing.T) (*Core, Trust, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	trust := Trust{ID: "synthetic-sender", Network: "tailnet", Generation: 1, RevocationEpoch: strings.Repeat("d", 32)}
	c := &Core{dir: dir, ctx: ctx, cancel: cancel, resourceLock: lock, resourceIdentity: strings.Repeat("a", 32), resourceNonce: strings.Repeat("b", 32), capacity: capacity.Defaults(), profile: Profile{Version: currentProfileVersion, DurableFiles: &DurableProfile{Version: 1, Epoch: strings.Repeat("c", 32), Phase: "initializing"}, Settings: Settings{Network: "tailnet"}, Peers: []Trust{trust}}}
	if err = c.initializeMessageStore(true); err != nil {
		t.Fatal(err)
	}
	c.profile.DurableFiles.Phase = "ready"
	c.startup.Revocations = map[string]string{trust.ID: trust.RevocationEpoch}
	now := c.messageStore.state.HighWater
	c.messageStore.now = func() time.Time { return now }
	t.Cleanup(func() { cancel(); c.mu.Lock(); _ = c.closeMessageStoreLocked(); c.mu.Unlock(); _ = lock.Close() })
	return c, trust, &now
}

func durableTextReceiverOffer(t *testing.T, now time.Time, text string) DurableTextOffer {
	t.Helper()
	id := newTextRequestID(now)
	created, _ := textRequestCreated(id)
	offer := DurableTextOffer{Version: 1, MessageID: id, RecipientID: "synthetic-receiver", TextBytes: int64(len(text)), TextSHA256: textSHA256(text), CreatedAt: created, ExpiresAt: created.Add(time.Hour), SentAt: now}
	var err error
	offer.Digest, err = durableTextDigest(offer)
	if err != nil {
		t.Fatal(err)
	}
	return offer
}

func durableTextReceiverCode(t *testing.T, err error, code string) {
	t.Helper()
	var local *localCommandError
	if !errors.As(err, &local) || local.code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}

func reopenDurableTextReceiver(t *testing.T, c *Core, now *time.Time) {
	t.Helper()
	c.mu.Lock()
	if err := c.closeMessageStoreLocked(); err != nil {
		t.Fatal(err)
	}
	c.messageStore = nil
	c.messages = nil
	c.mu.Unlock()
	if err := c.initializeMessageStore(false); err != nil {
		t.Fatal(err)
	}
	*now = c.messageStore.state.HighWater
	c.messageStore.now = func() time.Time { return *now }
}

func TestDurableTextReceiverLostOfferACKPreservesExactLeaseAcrossRestart(t *testing.T) {
	c, trust, now := durableTextReceiverFixture(t)
	offer := durableTextReceiverOffer(t, *now, "synthetic text")
	first, err := c.offerDurableText(context.Background(), trust, offer)
	if err != nil {
		t.Fatal(err)
	}
	horizon := offer.CreatedAt.Add(textMaximumLifetime + textReceiptGrace + textClockSkew)
	if first.State != "offered" || !first.Lease.ReceiptUntil.Equal(horizon) || first.Lease.ReceiptUntil.Equal(offer.ExpiresAt.Add(textReceiptGrace)) {
		t.Fatal("lease lacks immutable key horizon")
	}
	if row := c.messageStore.state.Incoming[offer.MessageID]; row.Text != "" || row.Visible || row.Receipt != nil {
		t.Fatal("offer exposed body or receipt")
	}
	reopenDurableTextReceiver(t, c, now)
	offer.SentAt = now.Add(time.Second)
	second, err := c.offerDurableText(context.Background(), trust, offer)
	if err != nil || !sameTextLease(first.Lease, second.Lease) || second.State != "offered" || len(c.messageStore.state.Incoming) != 1 {
		t.Fatalf("lost ACK reminted reservation: %v", err)
	}
}

func TestDurableTextReceiverSavedReceiptSurvivesRestartCleanupAndReplay(t *testing.T) {
	c, trust, now := durableTextReceiverFixture(t)
	text := "synthetic saved text"
	offer := durableTextReceiverOffer(t, *now, text)
	reserved, err := c.offerDurableText(context.Background(), trust, offer)
	if err != nil {
		t.Fatal(err)
	}
	request := DurableTextDeliver{Lease: reserved.Lease, Text: text}
	first, err := c.deliverDurableText(context.Background(), trust, request)
	if err != nil || first.Receipt == nil || len(c.messages) != 1 {
		t.Fatalf("receive failed: %v", err)
	}
	receipt := *first.Receipt
	reopenDurableTextReceiver(t, c, now)
	c.capacity.Logical["messageHistoryBytes"] = capacity.Limited(1)
	view, err := c.messageHistoryCommand("message.history.preview", json.RawMessage(`{}`))
	if err != nil || view.(historyCleanup).Remove != 1 {
		t.Fatalf("cleanup preview failed: %v", err)
	}
	input, _ := json.Marshal(map[string]string{"expectedRevision": view.(historyCleanup).Revision})
	if _, err = c.messageHistoryCommand("message.history.cleanup", input); err != nil {
		t.Fatal(err)
	}
	reopenDurableTextReceiver(t, c, now)
	*now = now.Add(2 * time.Hour)
	for _, method := range []string{"offer", "deliver", "status"} {
		var result DurableTextStatus
		switch method {
		case "offer":
			offer.SentAt = *now
			result, err = c.offerDurableText(context.Background(), trust, offer)
		case "deliver":
			result, err = c.deliverDurableText(context.Background(), trust, request)
		case "status":
			result, err = c.statusDurableText(context.Background(), trust, reserved.Lease)
		}
		if err != nil || result.State != "received" || result.Receipt == nil || !result.Receipt.SavedAt.Equal(receipt.SavedAt) || !sameTextLease(result.Receipt.Lease, receipt.Lease) {
			t.Fatalf("%s lost immutable receipt: %v", method, err)
		}
		row := c.messageStore.state.Incoming[offer.MessageID]
		if row.Visible || row.BodyRetained || row.Text != "" || len(c.messages) != 0 {
			t.Fatalf("%s resurrected hidden history", method)
		}
	}
}

func TestDurableTextReceiverImmutableOfferAndBodyConflicts(t *testing.T) {
	c, trust, now := durableTextReceiverFixture(t)
	offer := durableTextReceiverOffer(t, *now, "exact body")
	reserved, err := c.offerDurableText(context.Background(), trust, offer)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*DurableTextOffer){func(o *DurableTextOffer) { o.ExpiresAt = o.ExpiresAt.Add(time.Minute) }, func(o *DurableTextOffer) { o.TextSHA256 = textSHA256("other body") }} {
		changed := offer
		change(&changed)
		changed.Digest, _ = durableTextDigest(changed)
		_, err = c.offerDurableText(context.Background(), trust, changed)
		durableTextReceiverCode(t, err, "message_conflict")
	}
	_, err = c.deliverDurableText(context.Background(), trust, DurableTextDeliver{Lease: reserved.Lease, Text: "wrong body"})
	durableTextReceiverCode(t, err, "message_conflict")
	for _, change := range []func(*transfer.DurableLease){func(l *transfer.DurableLease) { l.Token = strings.Repeat("e", 32) }, func(l *transfer.DurableLease) { l.ReceiverEpoch = strings.Repeat("f", 32) }, func(l *transfer.DurableLease) { l.ReceiptUntil = l.ReceiptUntil.Add(-time.Hour) }} {
		lease := reserved.Lease
		change(&lease)
		_, err = c.statusDurableText(context.Background(), trust, lease)
		durableTextReceiverCode(t, err, "message_unknown_lease")
	}
	if c.messageStore.state.Incoming[offer.MessageID].State != "offered" || len(c.messages) != 0 {
		t.Fatal("conflict mutated receiver")
	}
}

func TestDurableTextReceiverCurrentAuthorityFences(t *testing.T) {
	for _, mode := range []string{"pause", "generation", "revocation", "persistent_epoch", "managed_denied", "network", "backend", "origin", "cancel", "closing"} {
		t.Run(mode, func(t *testing.T) {
			c, trust, now := durableTextReceiverFixture(t)
			offer := durableTextReceiverOffer(t, *now, "bound text")
			reserved, err := c.offerDurableText(context.Background(), trust, offer)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			switch mode {
			case "pause":
				c.profile.Peers[0].Paused = true
			case "generation":
				c.profile.Peers[0].Generation++
			case "revocation":
				c.profile.Peers[0].RevocationEpoch = "changed-epoch"
			case "persistent_epoch":
				c.startup.Revocations[trust.ID] = "new-persistent-epoch"
			case "managed_denied":
				c.managedDenied = map[string]bool{trust.ID: true}
			case "network":
				c.profile.Settings.Network = "none"
			case "backend":
				ctx = context.WithValue(ctx, durableTextTransportKey{}, durableTextTransport{backend: &pipeNode{}, recipient: offer.RecipientID})
			case "origin":
				stopped := make(chan struct{})
				close(stopped)
				c.node = &pipeNode{}
				ctx = context.WithValue(ctx, durableTextTransportKey{}, durableTextTransport{backend: c.node, recipient: offer.RecipientID})
				ctx = context.WithValue(ctx, peerIncomingRequestKey{}, &peerIncomingRequest{authority: &peerHTTPAuthority{stopped: stopped}})
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "closing":
				c.closing = true
			}
			_, err = c.deliverDurableText(ctx, trust, DurableTextDeliver{Lease: reserved.Lease, Text: "bound text"})
			durableTextReceiverCode(t, err, "message_permission")
			if c.messageStore.state.Incoming[offer.MessageID].State != "offered" || len(c.messages) != 0 {
				t.Fatal("stale authority committed body")
			}
		})
	}
}

func TestDurableTextReceiverUncertainPublicationNeverACKsOrReportsUnknown(t *testing.T) {
	for _, stage := range []string{"offer", "deliver"} {
		t.Run(stage, func(t *testing.T) {
			c, trust, now := durableTextReceiverFixture(t)
			offer := durableTextReceiverOffer(t, *now, "uncertain text")
			var reserved DurableTextStatus
			var err error
			if stage == "deliver" {
				reserved, err = c.offerDurableText(context.Background(), trust, offer)
				if err != nil {
					t.Fatal(err)
				}
			}
			c.messageStore.afterWrite = func() error { return errors.New("synthetic directory barrier failure") }
			if stage == "offer" {
				_, err = c.offerDurableText(context.Background(), trust, offer)
			} else {
				_, err = c.deliverDurableText(context.Background(), trust, DurableTextDeliver{Lease: reserved.Lease, Text: "uncertain text"})
			}
			durableTextReceiverCode(t, err, "message_storage_uncertain")
			candidate := c.messageStore.candidate.Incoming[offer.MessageID]
			if candidate == nil || len(c.messages) != 0 {
				t.Fatal("candidate was lost or prematurely visible")
			}
			_, err = c.statusDurableText(context.Background(), trust, candidate.Lease)
			durableTextReceiverCode(t, err, "message_storage_uncertain")
			c.messageStore.afterWrite = nil
			c.mu.Lock()
			err = c.reconcileMessageStoreLocked()
			c.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			replayed, err := c.offerDurableText(context.Background(), trust, offer)
			if err != nil || !sameTextLease(candidate.Lease, replayed.Lease) {
				t.Fatalf("exact reconciliation failed: %v", err)
			}
			if stage == "deliver" && (replayed.Receipt == nil || !replayed.Receipt.SavedAt.Equal(candidate.Receipt.SavedAt) || len(c.messages) != 1) {
				t.Fatal("saved receipt did not survive uncertainty")
			}
		})
	}
}

func TestDurableTextReceiverExpiryRetirementAndRollback(t *testing.T) {
	c, trust, now := durableTextReceiverFixture(t)
	offer := durableTextReceiverOffer(t, *now, "expired text")
	reserved, err := c.offerDurableText(context.Background(), trust, offer)
	if err != nil {
		t.Fatal(err)
	}
	*now = offer.ExpiresAt
	_, err = c.deliverDurableText(context.Background(), trust, DurableTextDeliver{Lease: reserved.Lease, Text: "expired text"})
	durableTextReceiverCode(t, err, "message_expired")
	if c.messageStore.state.Incoming[offer.MessageID].State != "expired" {
		t.Fatal("expiry was not durably fenced")
	}
	*now = reserved.Lease.ReceiptUntil.Add(-time.Nanosecond)
	c.mu.Lock()
	err = c.maintainDurableTextReceiverLocked(*now)
	c.mu.Unlock()
	if err != nil || c.messageStore.state.Incoming[offer.MessageID] == nil {
		t.Fatal("evidence retired early")
	}
	*now = reserved.Lease.ReceiptUntil
	c.mu.Lock()
	err = c.maintainDurableTextReceiverLocked(*now)
	c.mu.Unlock()
	if err != nil || c.messageStore.state.Incoming[offer.MessageID] != nil {
		t.Fatalf("terminal evidence retirement failed: %v", err)
	}
	offer.SentAt = *now
	_, err = c.offerDurableText(context.Background(), trust, offer)
	durableTextReceiverCode(t, err, "message_expired")
	*now = now.Add(-time.Hour)
	offer.SentAt = *now
	_, err = c.offerDurableText(context.Background(), trust, offer)
	durableTextReceiverCode(t, err, "message_clock_rollback")
	_, err = c.statusDurableText(context.Background(), trust, reserved.Lease)
	durableTextReceiverCode(t, err, "message_clock_rollback")
}

func TestDurableTextReceiverReservationsSurviveLoweredLimits(t *testing.T) {
	c, trust, now := durableTextReceiverFixture(t)
	text := strings.Repeat("\t", 100) + "escaped synthetic text"
	offer := durableTextReceiverOffer(t, *now, text)
	reserved, err := c.offerDurableText(context.Background(), trust, offer)
	if err != nil {
		t.Fatal(err)
	}
	c.capacity.Resources["messageStorageBytes"] = capacity.Limited(1)
	c.capacity.Resources["messageTextBytes"] = capacity.Limited(1)
	result, err := c.deliverDurableText(context.Background(), trust, DurableTextDeliver{Lease: reserved.Lease, Text: text})
	if err != nil || result.Receipt == nil {
		t.Fatalf("lowered admission limit stranded reservation: %v", err)
	}
	_, err = c.offerDurableText(context.Background(), trust, durableTextReceiverOffer(t, *now, "new text"))
	durableTextReceiverCode(t, err, "message_too_large")
	c.capacity.Resources["messageTextBytes"] = capacity.Limited(16384)
	_, err = c.offerDurableText(context.Background(), trust, durableTextReceiverOffer(t, *now, "new text"))
	durableTextReceiverCode(t, err, "message_evidence_capacity")
	if _, err = c.statusDurableText(context.Background(), trust, reserved.Lease); err != nil {
		t.Fatal(err)
	}
}

func TestDurableTextReceiverActiveCapacityAndFailedTerminalSave(t *testing.T) {
	c, trust, now := durableTextReceiverFixture(t)
	var leases []transfer.DurableLease
	for i := 0; i < messageActivePeerLimit; i++ {
		result, err := c.offerDurableText(context.Background(), trust, durableTextReceiverOffer(t, *now, fmt.Sprintf("text %d", i)))
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, result.Lease)
	}
	_, err := c.offerDurableText(context.Background(), trust, durableTextReceiverOffer(t, *now, "over active peer limit"))
	durableTextReceiverCode(t, err, "message_peer_capacity")
	if _, err = c.statusDurableText(context.Background(), trust, leases[0]); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Hour)
	c.messageStore.write = func(string, []byte) error { return errors.New("synthetic write failed before publication") }
	_, err = c.statusDurableText(context.Background(), trust, leases[0])
	if err == nil || c.messageStore.state.Incoming[leases[0].DeliveryID].State != "offered" {
		t.Fatal("failed terminal save released active reservation")
	}
	c.messageStore.write = nil
	c.mu.Lock()
	err = c.maintainDurableTextReceiverLocked(*now)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.offerDurableText(context.Background(), trust, durableTextReceiverOffer(t, *now, "new active admission")); err != nil {
		t.Fatalf("terminal evidence consumed active pool: %v", err)
	}
}

func TestDurableTextReceiverBoundedHTTPFailures(t *testing.T) {
	c, trust, _ := durableTextReceiverFixture(t)
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", DurableTextOfferPath, "", http.StatusMethodNotAllowed},
		{"POST", "/v1/durable-messages/other", `{}`, http.StatusNotFound},
		{"POST", DurableTextOfferPath, `{"unexpected":true}`, http.StatusBadRequest},
		{"POST", DurableTextStatusPath, `{} {}`, http.StatusBadRequest},
		{"POST", DurableTextOfferPath, strings.Repeat(" ", 8193), http.StatusRequestEntityTooLarge},
	} {
		r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		if !c.serveDurableTextHTTP(w, r, trust) || w.Code != test.status {
			t.Fatalf("%s %s: got %d: %s", test.method, test.path, w.Code, w.Body.String())
		}
	}
}

func TestDurableTextReceiverRevocationDoesNotEraseSavedEvidence(t *testing.T) {
	c, trust, now := durableTextReceiverFixture(t)
	offer := durableTextReceiverOffer(t, *now, "saved before revocation")
	reserved, err := c.offerDurableText(context.Background(), trust, offer)
	if err != nil {
		t.Fatal(err)
	}
	received, err := c.deliverDurableText(context.Background(), trust, DurableTextDeliver{Lease: reserved.Lease, Text: "saved before revocation"})
	if err != nil {
		t.Fatal(err)
	}
	other := durableTextReceiverOffer(t, *now, "not received")
	if _, err = c.offerDurableText(context.Background(), trust, other); err != nil {
		t.Fatal(err)
	}
	c.profile.Peers = nil
	c.mu.Lock()
	err = c.maintainDurableTextReceiverLocked(*now)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if c.messageStore.state.Incoming[other.MessageID].State != "cancelled" {
		t.Fatal("revocation did not fence outstanding offer")
	}
	row := c.messageStore.state.Incoming[offer.MessageID]
	if row.State != "received" || row.Receipt == nil || !row.Receipt.SavedAt.Equal(received.Receipt.SavedAt) || !row.Visible {
		t.Fatal("revocation erased saved evidence")
	}
	*now = reserved.Lease.ReceiptUntil.Add(time.Hour)
	c.mu.Lock()
	err = c.maintainDurableTextReceiverLocked(*now)
	c.mu.Unlock()
	if err != nil || c.messageStore.state.Incoming[offer.MessageID] == nil || len(c.messages) != 1 {
		t.Fatal("evidence retirement silently erased visible text")
	}
	_, err = c.statusDurableText(context.Background(), trust, reserved.Lease)
	durableTextReceiverCode(t, err, "message_permission")
}

func TestDurableTextReceiverUnknownOldKeyAndSentClockBoundaries(t *testing.T) {
	c, trust, now := durableTextReceiverFixture(t)
	offer := durableTextReceiverOffer(t, *now, "clock boundary")
	for _, sent := range []time.Time{now.Add(-textClockSkew - time.Nanosecond), now.Add(textClockSkew + time.Nanosecond), {}} {
		invalid := offer
		invalid.SentAt = sent
		_, err := c.offerDurableText(context.Background(), trust, invalid)
		durableTextReceiverCode(t, err, "message_clock")
	}
	*now = offer.CreatedAt.Add(textMaximumLifetime + textReceiptGrace + textClockSkew)
	offer.SentAt = *now
	_, err := c.offerDurableText(context.Background(), trust, offer)
	durableTextReceiverCode(t, err, "message_expired")
	if len(c.messageStore.state.Incoming) != 0 {
		t.Fatal("old timestamped key gained a new reservation")
	}
}

func TestDurableTextReceiverAdmittedSaveRetainsReceiptAfterCancellation(t *testing.T) {
	c, trust, now := durableTextReceiverFixture(t)
	offer := durableTextReceiverOffer(t, *now, "cancelled after publication")
	reserved, err := c.offerDurableText(context.Background(), trust, offer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.messageStore.afterWrite = func() error { cancel(); return nil }
	result, err := c.deliverDurableText(ctx, trust, DurableTextDeliver{Lease: reserved.Lease, Text: "cancelled after publication"})
	if err != nil || result.Receipt == nil || ctx.Err() == nil || len(c.messages) != 1 {
		t.Fatalf("admitted transaction lost its actual outcome after cancellation: %v", err)
	}
	c.messageStore.afterWrite = nil
	recovered, err := c.statusDurableText(context.Background(), trust, reserved.Lease)
	if err != nil || recovered.Receipt == nil || !recovered.Receipt.SavedAt.Equal(result.Receipt.SavedAt) {
		t.Fatalf("receipt-only reconciliation failed: %v", err)
	}
}

func TestDurableTextReceiverMinimumOriginalLifetimeAllowsLateDelivery(t *testing.T) {
	c, trust, now := durableTextReceiverFixture(t)
	offer := durableTextReceiverOffer(t, *now, "minimum lifetime")
	short := offer
	short.ExpiresAt = short.CreatedAt.Add(time.Minute - time.Nanosecond)
	// A remote peer can hash a structurally valid but disallowed short offer.
	// Do not rely on our own digest constructor to sanitize that peer's input.
	unsigned := short
	unsigned.Digest, unsigned.SentAt = "", time.Time{}
	encoded, _ := json.Marshal(unsigned)
	short.Digest = fmt.Sprintf("%x", sha256.Sum256(append([]byte("sobalink/durable-text/v1\x00"), encoded...)))
	_, err := c.offerDurableText(context.Background(), trust, short)
	durableTextReceiverCode(t, err, "message_invalid")
	offer.ExpiresAt = offer.CreatedAt.Add(time.Minute)
	offer.Digest, err = durableTextDigest(offer)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(45 * time.Second)
	offer.SentAt = *now
	reserved, err := c.offerDurableText(context.Background(), trust, offer)
	if err != nil {
		t.Fatalf("valid original lifetime rejected merely for late arrival: %v", err)
	}
	if _, err = c.deliverDurableText(context.Background(), trust, DurableTextDeliver{Lease: reserved.Lease, Text: "minimum lifetime"}); err != nil {
		t.Fatalf("remaining lifetime below one minute stranded admitted delivery: %v", err)
	}
}
