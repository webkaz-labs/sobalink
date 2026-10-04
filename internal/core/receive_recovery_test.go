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
	"time"

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

func TestCoreUnavailableAutosaveStopsOnlyReceiveAndRequiresReenable(t *testing.T) {
	for _, kind := range []string{"legacy", "indexed", "empty-index"} {
		t.Run(kind, func(t *testing.T) {
			p := newCorePair(t)
			trustPair(t, p)
			mustCommand(t, p.a, "peer.autosave", map[string]any{"peerId": "peer-b", "enabled": true, "directory": t.TempDir()})
			destination := t.TempDir()
			mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": destination})
			if kind == "indexed" {
				peer, _ := p.b.trust("peer-a")
				if _, err := p.b.transfers.Offer(transfer.Peer{ID: peer.ID, Generation: peer.Generation}, coreCrashManifest("retained", "12345678")); err != nil {
					t.Fatal(err)
				}
				var state transfer.ReceiveAccounting
				raw, err := os.ReadFile(filepath.Join(p.b.dir, "receive-accounting.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &state); err != nil || len(state.Roots) != 1 {
					t.Fatalf("accounting fixture: %v", err)
				}
				r := state.Roots[0]
				if err := os.WriteFile(filepath.Join(r.OwnedRoot, r.Stage, "unknown-retained"), []byte("1234567"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.b.Close(); err != nil {
				t.Fatal(err)
			}
			if kind == "legacy" {
				if err := os.Remove(filepath.Join(p.b.dir, "receive-accounting.json")); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Rename(destination, destination+"-offline"); err != nil {
				t.Fatal(err)
			}
			nb := &pipeNode{hub: p.hub, ip: p.nb.ip, who: map[netip.Addr]string{p.na.ip: "peer-a"}, state: p.nb.state}
			c, err := Open(context.Background(), Options{Directory: p.b.dir, NodeFactory: func(string, string) (NetworkBackend, error) { return nb, nil }})
			if err != nil {
				t.Fatalf("unavailable autosave stopped Core: %v", err)
			}
			defer c.Close()
			c.op.Lock()
			err = c.startPeerServer([]netip.Addr{nb.ip})
			c.op.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			wantCode := "inventory_unavailable"
			if kind == "legacy" {
				wantCode = "legacy_review_required"
			}
			if v := c.transfers.ReceiveRecovery(); v.Code != wantCode || v.ReservedBytes != nil {
				t.Fatalf("receive not blocked: %+v", v)
			}
			mustCommand(t, c, "settings.update", map[string]any{"theme": "dark"})
			if _, err := c.Snapshot(context.Background()); err != nil {
				t.Fatal("management unavailable", err)
			}
			mustCommand(t, c, "message.send", map[string]any{"peerId": "peer-a", "text": "send while receive blocked"})
			source := filepath.Join(t.TempDir(), "outgoing.txt")
			if err := os.WriteFile(source, []byte("1"), 0600); err != nil {
				t.Fatal(err)
			}
			sent, err := c.SendPaths(context.Background(), "peer-a", []string{source})
			if err != nil {
				t.Fatal("file send unavailable", err)
			}
			id := sent.(map[string]string)["id"]
			c.mu.RLock()
			outgoing := c.outgoing[id]
			c.mu.RUnlock()
			outgoing.mu.Lock()
			done := outgoing.runDone
			outgoing.mu.Unlock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("file send stalled while receive blocked")
			}
			if received, err := p.a.transfers.Get(id); err != nil || received.State != transfer.Completed {
				t.Fatalf("send did not reach receiving peer: %+v %v", received, err)
			}
			peer, _ := c.trust("peer-a")
			if peer.Autosave || peer.Directory != destination || len(c.transfers.ReceivePolicies()) != 0 {
				t.Fatal("unavailable autosave not durably disabled")
			}
			if err := os.Rename(destination+"-offline", destination); err != nil {
				t.Fatal(err)
			}
			view := mustCommand(t, c, "receive.recovery.confirm", map[string]bool{"reviewed": true}).(transfer.ReceiveRecoveryView)
			if !view.Applied || view.State != "ready" {
				t.Fatalf("restored destination: %+v", view)
			}
			offered, err := c.transfers.Offer(transfer.Peer{ID: peer.ID, Generation: peer.Generation}, coreCrashManifest("after-recovery", "1"))
			if err != nil || offered.State != transfer.Pending {
				t.Fatalf("recovery resurrected autosave: %+v %v", offered, err)
			}
			if _, err := c.transfers.Cancel(offered.ID); err != nil {
				t.Fatal(err)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(context.Background(), Options{Directory: c.dir, SkipNetworkStart: true})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			peer, _ = reopened.trust("peer-a")
			if peer.Autosave || len(reopened.transfers.ReceivePolicies()) != 0 {
				t.Fatal("restart resurrected autosave")
			}
		})
	}
}

func TestCoreUnavailableDefaultWithoutAutosaveDoesNotGrantOrBlockReceive(t *testing.T) {
	dir := t.TempDir()
	destination := t.TempDir()
	c, err := Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	mustCommand(t, c, "settings.update", map[string]any{"receiveDirectory": destination})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(destination, destination+"-offline"); err != nil {
		t.Fatal(err)
	}
	c, err = Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if v := c.transfers.ReceiveRecovery(); v.State != "ready" || len(c.transfers.ReceivePolicies()) != 0 {
		t.Fatalf("default folder became a receive grant: %+v", v)
	}
	peer := transfer.Peer{ID: "fixture-peer", Generation: 1}
	if err := c.transfers.BindPeer(peer); err != nil {
		t.Fatal(err)
	}
	b, err := c.transfers.Offer(peer, coreCrashManifest("manual", "1"))
	if err != nil || b.State != transfer.Pending {
		t.Fatalf("default folder autoaccepted: %+v %v", b, err)
	}
	if _, err := c.transfers.Accept(b.ID, t.TempDir()); err != nil {
		t.Fatal("manual destination unavailable", err)
	}
}

type recoveryCancelContext struct {
	context.Context
	calls  int
	action func()
}

func (c *recoveryCancelContext) Err() error {
	c.calls++
	if c.calls == 5 {
		c.action()
	}
	return c.Context.Err()
}

func TestCoreRecoveryInventoryStopsOnCallerOrLifetimeCancellation(t *testing.T) {
	for _, source := range []string{"caller", "lifetime-close"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			c, err := Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			peer := transfer.Peer{ID: "fixture-peer", Generation: 1}
			if err := c.transfers.BindPeer(peer); err != nil {
				t.Fatal(err)
			}
			if _, err := c.transfers.Offer(peer, coreCrashManifest("retained", "12345678")); err != nil {
				t.Fatal(err)
			}
			b, err := c.transfers.Accept("retained", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			store := transfer.FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")}
			state, err := store.LoadReceiveAccounting()
			if err != nil {
				t.Fatal(err)
			}
			r := state.Roots[0]
			retained := filepath.Join(b.Destination, r.Stage, "unknown-retained")
			if err := os.WriteFile(retained, []byte("1234567"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(r.Destination, r.Destination+"-offline"); err != nil {
				t.Fatal(err)
			}
			c, err = Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if c.transfers.ReceiveRecovery().State != "blocked" {
				t.Fatal("fixture did not block inventory")
			}
			if err := os.Rename(r.Destination+"-offline", r.Destination); err != nil {
				t.Fatal(err)
			}
			call, cancel := context.WithCancel(context.Background())
			defer cancel()
			closed := make(chan error, 1)
			ctx := &recoveryCancelContext{Context: call, action: func() {
				if source == "caller" {
					cancel()
				} else {
					c.cancel()
					go func() { closed <- c.Close() }()
				}
			}}
			value, err := c.Command(ctx, testCommand(t, "inventory-cancel", "receive.recovery.confirm", map[string]bool{"reviewed": true}))
			view, ok := value.(transfer.ReceiveRecoveryView)
			if err == nil || !ok || view.Applied || view.State != "blocked" || view.ReservedBytes != nil {
				t.Fatalf("cancelled inventory applied partial total: %+v %v", value, err)
			}
			if ctx.calls < 5 {
				t.Fatal("cancellation did not occur during inventory")
			}
			if source == "lifetime-close" {
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("Close waited for cancelled inventory")
				}
			}
			if data, err := os.ReadFile(retained); err != nil || string(data) != "1234567" {
				t.Fatal("cancelled inventory altered retained payload")
			}
			after, err := store.LoadReceiveAccounting()
			if err != nil || len(after.Roots) != 1 {
				t.Fatal("cancelled inventory retired accounting", err)
			}
		})
	}
}
