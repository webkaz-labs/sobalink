package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func TestCoreLegacyReceiveReviewKeepsCoreAvailableAndPersists(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.transfers.ReceiveRecovery().State != "ready" {
		t.Fatal("new state was not initialized")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "receive-accounting.json")); err != nil {
		t.Fatal(err)
	}
	c, err = Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatalf("receive migration stopped all Core functions: %v", err)
	}
	view := c.transfers.ReceiveRecovery()
	if view.Code != "legacy_review_required" || view.ReservedBytes != nil {
		t.Fatalf("legacy state: %+v", view)
	}
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatal("Core unavailable", err)
	}
	preview := mustCommand(t, c, "receive.recovery.confirm", map[string]bool{}).(transfer.ReceiveRecoveryView)
	if preview.Applied || preview.State != "blocked" {
		t.Fatalf("preview acknowledged legacy state: %+v", preview)
	}
	if _, err := os.Stat(filepath.Join(dir, "receive-accounting.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview wrote an index")
	}
	applied := mustCommand(t, c, "receive.recovery.confirm", map[string]bool{"reviewed": true}).(transfer.ReceiveRecoveryView)
	if !applied.Applied || applied.State != "ready" {
		t.Fatalf("review: %+v", applied)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.transfers.ReceiveRecovery().State != "ready" {
		t.Fatal("review was not durable")
	}
}

func TestCoreDamagedIndexStopsOnlyReceiveAndCannotBeDiscarded(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	damaged := []byte("{private-fixture-token malformed}")
	if err := os.WriteFile(filepath.Join(dir, "receive-accounting.json"), damaged, 0600); err != nil {
		t.Fatal(err)
	}
	c, err = Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal("damaged accounting stopped Core", err)
	}
	defer c.Close()
	if _, err := command(c, randomID(), "receive.recovery.confirm", map[string]bool{"reviewed": true}); err == nil || strings.Contains(err.Error(), "private-fixture-token") || strings.Contains(err.Error(), dir) {
		t.Fatalf("unsafe recovery error: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "receive-accounting.json")); err != nil || string(data) != string(damaged) {
		t.Fatal("confirmation destroyed damaged accounting")
	}
	snapshot, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-fixture-token") || strings.Contains(string(data), "receive-accounting.json") {
		t.Fatal("private accounting appeared in API view")
	}
	exported := mustCommand(t, c, "profile.export", map[string]any{})
	data, err = json.Marshal(exported)
	if err != nil || strings.Contains(string(data), "private-fixture-token") || strings.Contains(string(data), "receive-accounting.json") {
		t.Fatal("private accounting appeared in export")
	}
}

func TestReceiveRecoveryCannotBeConfirmedByPeer(t *testing.T) {
	p := newCorePair(t)
	mustCommand(t, p.a, "peer.trust", map[string]any{"peerId": "peer-b", "trusted": true})
	mustCommand(t, p.b, "peer.trust", map[string]any{"peerId": "peer-a", "trusted": true})
	// Restart the receiver through Core.Open with the same legacy profile.
	dir := p.b.dir
	if err := p.b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "receive-accounting.json")); err != nil {
		t.Fatal(err)
	}
	nb := &pipeNode{hub: p.hub, ip: p.nb.ip, who: map[netip.Addr]string{p.na.ip: "peer-a"}, state: p.nb.state}
	receiver, err := Open(context.Background(), Options{Directory: dir, NodeFactory: func(string, string) (NetworkBackend, error) { return nb, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	receiver.op.Lock()
	err = receiver.startPeerServer([]netip.Addr{nb.ip})
	receiver.op.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	manager := receiver.transfers
	for _, path := range []string{"/v1/receive/recovery/confirm", "/v1/command", "/v1/offers"} {
		raw := map[string]any{"name": "receive.recovery.confirm", "reviewed": true}
		if path == "/v1/offers" {
			raw = map[string]any{"id": "blocked-offer", "entries": fileManifest("blocked-offer", "1").Entries}
		}
		var result any
		if err := p.a.peerJSON(context.Background(), "peer-b", http.MethodPost, path, raw, &result); err == nil {
			t.Fatalf("peer gained recovery authority through %s", path)
		}
		if manager.ReceiveRecovery().Code != "legacy_review_required" {
			t.Fatal("peer cleared recovery gate")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "receive-accounting.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("peer initialized private accounting")
	}
}

func TestCoreCloseAfterContextCancellationPreservesSavedFiles(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	c, err := Open(ctx, Options{Directory: dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	peer := transfer.Peer{ID: "fixture-peer", Generation: 1}
	if err := c.transfers.BindPeer(peer); err != nil {
		t.Fatal(err)
	}
	manifest := coreCrashManifest("saved-before-close", "1234567")
	if _, err := c.transfers.Offer(peer, manifest); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	accepted, err := c.transfers.Accept(manifest.ID, destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.transfers.ReceiveFile(ctx, peer, manifest.ID, "file", strings.NewReader("1234567")); err != nil {
		t.Fatal(err)
	}
	pending := coreCrashManifest("accepted-before-close", "1")
	if _, err := c.transfers.Offer(peer, pending); err != nil {
		t.Fatal(err)
	}
	if _, err := c.transfers.Accept(pending.ID, destination); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if view := c.transfers.ReceiveRecovery(); view.State != "ready" || view.ReservedBytes == nil || *view.ReservedBytes != 0 {
		t.Fatalf("cancelled shutdown failed accounting: %+v", view)
	}
	if data, err := os.ReadFile(filepath.Join(accepted.Destination, "payload")); err != nil || string(data) != "1234567" {
		t.Fatal("shutdown removed saved output")
	}
}

func TestReceiveAccountingPrivatePathsUseResourceBudget(t *testing.T) {
	p := capacity.Defaults()
	p.Logical["pathBytes"] = capacity.Limited(1)
	budget := receiveAccountingLimits(p)
	if budget.MaxPathBytes != p.Number("resources", "transferMetadataBytes") {
		t.Fatal("sender path admission restricted private absolute storage paths")
	}
	if budget.MaxEntries != p.Number("resources", "stagingInventoryEntries") || budget.MaxDepth != p.Number("resources", "stagingInventoryDepth") || budget.MaxBytes != p.Number("resources", "transferMetadataBytes") {
		t.Fatal("inventory did not use finite resource policy")
	}
}
