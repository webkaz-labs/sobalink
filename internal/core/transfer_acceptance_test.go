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

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/diskspace"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func transferAcceptanceFixture(t *testing.T) (*Core, string, string) {
	t.Helper()
	c := outboxFixture(t) // No background maintenance or real network.
	var err error
	c.transfers, err = transfer.NewManager(transfer.Options{Limits: transfer.Limits{DiskReserveBytes: 1}})
	if err != nil {
		t.Fatal("synthetic transfer manager unavailable")
	}
	t.Cleanup(func() { _ = c.transfers.Close() })
	if c.initializeDurableReceiver(true) != nil {
		t.Fatal("synthetic durable receiver unavailable")
	}
	peer := transfer.Peer{ID: "peer-b", Generation: 1}
	if c.transfers.BindPeer(peer) != nil || c.transfers.BindDurableRelationship(peer, "synthetic", "epoch-one") != nil {
		t.Fatal("synthetic relationship unavailable")
	}
	now := time.Now().UTC()
	id := "accept-original"
	offer := transfer.DurableOffer{Version: 1, DeliveryID: id, RecipientID: "synthetic-receiver", Manifest: coreCrashManifest(id, "synthetic payload"), CreatedAt: now, SentAt: now, ExpiresAt: now.Add(time.Hour)}
	offer.Digest, err = transfer.DurableDigest(offer)
	if err != nil {
		t.Fatal("synthetic offer unavailable")
	}
	if _, err := c.transfers.OfferDurable(peer, offer.RecipientID, offer); err != nil {
		t.Fatal("synthetic durable offer unavailable")
	}
	return c, id, t.TempDir()
}

func TestTransferAcceptBusyCacheAndExplicitRetry(t *testing.T) {
	c, id, destination := transferAcceptanceFixture(t)
	payload := map[string]string{"transferId": id, "destination": destination}
	ledger := filepath.Join(c.dir, "durable-v1", "receiver.json")
	read := func() []byte {
		t.Helper()
		raw, err := os.ReadFile(ledger)
		if err != nil {
			t.Fatal("synthetic ledger unavailable")
		}
		return raw
	}
	before := read()
	release := holdOutboxLifecycle(t, c)
	defer func() {
		if release != nil {
			release()
		}
	}()
	value, firstErr := command(c, "original-invocation", "transfer.accept", payload)
	if networkErrorCode(firstErr) != "transfer_accept_busy" || !errors.Is(firstErr, transfer.ErrAcceptBusy) || !errors.Is(firstErr, transfer.ErrBusy) || !bytes.Equal(before, read()) {
		t.Fatal("held owner did not expose the exact prepublication busy contract")
	}
	first, err := json.Marshal(value)
	if err != nil || value.(transfer.Batch).State != transfer.Pending {
		t.Fatal("busy command changed the initial consent")
	}
	changed := map[string]string{"transferId": id, "destination": t.TempDir()}
	if _, err := command(c, "original-invocation", "transfer.accept", changed); err == nil || networkErrorCode(err) == "transfer_accept_busy" || !bytes.Equal(before, read()) {
		t.Fatal("changed destination reused the cached original invocation")
	}
	release()
	release = nil
	value, err = command(c, "original-invocation", "transfer.accept", payload)
	if err != firstErr || !bytes.Equal(before, read()) {
		t.Fatal("same invocation reran acceptance after ownership was released")
	}
	// A settings change must not retarget an explicitly selected destination.
	c.mu.Lock()
	c.profile.Settings.ReceiveDirectory = changed["destination"]
	c.mu.Unlock()
	value, err = command(c, "explicit-next-invocation", "transfer.accept", payload)
	if err != nil || value.(transfer.Batch).State != transfer.Accepted || value.(transfer.Batch).Destination != destination {
		t.Fatal("fresh explicit invocation lost the original destination")
	}
	accepted := read()
	if _, err := command(c, "different-destination", "transfer.accept", changed); networkErrorCode(err) != "transfer_accept_unconfirmed" || !errors.Is(err, transfer.ErrState) || !bytes.Equal(accepted, read()) {
		t.Fatal("accepted destination mutation was not rejected")
	}
	value, err = command(c, "original-invocation", "transfer.accept", payload)
	previous, _ := json.Marshal(value)
	if err != firstErr || !bytes.Equal(first, previous) || !bytes.Equal(accepted, read()) {
		t.Fatal("later consent replaced the original invocation's cached refusal")
	}
	// Busy is about this attempt, not proof that no earlier consent exists.
	release = holdOutboxLifecycle(t, c)
	value, err = command(c, "prior-consent-busy", "transfer.accept", payload)
	if networkErrorCode(err) != "transfer_accept_busy" || value.(transfer.Batch).State != transfer.Accepted || value.(transfer.Batch).Destination != destination || !bytes.Equal(accepted, read()) {
		t.Fatal("busy acceptance erased or republished earlier consent")
	}
	release()
	release = nil
	if _, err := command(c, "prior-consent-explicit", "transfer.accept", payload); err != nil || !bytes.Equal(accepted, read()) {
		t.Fatal("same-target explicit acceptance was not idempotent")
	}
	if entries, err := os.ReadDir(destination); err != nil || len(entries) != 0 {
		t.Fatal("acceptance published output before authenticated lease start")
	}
}

func TestTransferAcceptExplicitRetryRechecksAuthority(t *testing.T) {
	c, id, destination := transferAcceptanceFixture(t)
	payload := map[string]string{"transferId": id, "destination": destination}
	release := holdOutboxLifecycle(t, c)
	_, firstErr := command(c, "before-change", "transfer.accept", payload)
	release()
	if networkErrorCode(firstErr) != "transfer_accept_busy" {
		t.Fatal("held owner did not refuse acceptance")
	}
	if c.transfers.BindDurableRelationship(transfer.Peer{ID: "peer-b", Generation: 1}, "synthetic", "epoch-two") != nil {
		t.Fatal("synthetic relationship change failed")
	}
	if _, err := command(c, "after-change", "transfer.accept", payload); networkErrorCode(err) != "transfer_accept_unconfirmed" || !errors.Is(err, transfer.ErrDurablePermission) {
		t.Fatal("fresh invocation bypassed current relationship authority")
	}
	if _, err := command(c, "before-change", "transfer.accept", payload); err != firstErr {
		t.Fatal("cached earlier result was changed by later authority")
	}
}

func TestTransferAcceptErrorBoundaryExcludesUncertainty(t *testing.T) {
	for _, cause := range []error{
		transfer.ErrBusy, transfer.ErrDurableRecovery, transfer.ErrReceiveRecovery,
		transfer.ErrDurablePermission, transfer.ErrState,
		fmt.Errorf("synthetic-private: %w", transfer.ErrAcceptBusy),
		errors.Join(transfer.ErrAcceptBusy, config.ErrAtomicCommitted),
		errors.Join(transfer.ErrAcceptBusy, transfer.ErrDurableRecovery),
		&os.PathError{Op: "open", Path: "/synthetic/private", Err: os.ErrPermission},
		errors.Join(diskspace.ErrLow, config.ErrAtomicCommitted),
		errors.Join(diskspace.ErrUnknown, transfer.ErrDurableRecovery),
		fmt.Errorf("synthetic-private: %w", errors.Join(diskspace.ErrLow, transfer.ErrReceiveRecovery)),
		&localCommandError{"disk_space_low", "synthetic-private"},
		errors.New("synthetic-private busy"),
	} {
		err := transferAcceptCommandError(cause)
		if networkErrorCode(err) != "transfer_accept_unconfirmed" || !errors.Is(err, cause) || strings.Contains(err.Error(), "synthetic-private") || strings.Contains(err.Error(), "/synthetic/") || strings.Contains(err.Error(), "then explicitly accept") {
			t.Fatal("non-busy acceptance gained a retry contract or lost its cause")
		}
	}
	if transferAcceptCommandError(nil) != nil {
		t.Fatal("successful acceptance became an error")
	}
	for _, cause := range []error{diskspace.ErrLow, diskspace.ErrUnknown} {
		if transferAcceptCommandError(cause) != cause {
			t.Fatal("existing exact preflight disk-space error was replaced")
		}
	}
	c, _, _ := transferAcceptanceFixture(t)
	if _, err := c.transferCommand(context.Background(), "transfer.accept", json.RawMessage(`{"unknown":true}`)); err == nil || networkErrorCode(err) == "transfer_accept_unconfirmed" {
		t.Fatal("earlier payload validation changed its error contract")
	}
}
