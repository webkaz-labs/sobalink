package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func retrySelectionFixture(t *testing.T) (*Core, *outgoingBatch, transferRetryExpectation) {
	t.Helper()
	manager, err := transfer.NewManager(transfer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	peer := Trust{ID: "example-peer", Network: "tailnet", Generation: 1}
	b := &outgoingBatch{ID: "example-batch", PeerID: peer.ID, Network: peer.Network, Generation: peer.Generation, State: "failed", Spool: t.TempDir(), Created: time.Now()}
	c := &Core{ctx: context.Background(), resourceLock: &config.Lock{}, resourceIdentity: strings.Repeat("1", 32), resourceNonce: strings.Repeat("2", 32), transfers: manager, outgoing: map[string]*outgoingBatch{b.ID: b}, profile: Profile{Settings: Settings{Network: "tailnet"}, Peers: []Trust{peer}}}
	expected := transferRetryExpectation{ProcessID: c.resourceCatalogProcessID(), PeerID: peer.ID, Direction: "outgoing"}
	return c, b, expected
}
func selectedRetry(t *testing.T, c *Core, id string, expected *transferRetryExpectation) error {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"transferId": id, "expected": expected})
	c.op.Lock()
	defer c.op.Unlock()
	_, err := c.transferCommand(context.Background(), "transfer.retry", payload)
	return err
}
func TestSelectedTransferRetryRejectsChangedSelectionWithoutStarting(t *testing.T) {
	for _, field := range []string{"process", "peer", "direction", "empty"} {
		t.Run(field, func(t *testing.T) {
			c, b, e := retrySelectionFixture(t)
			switch field {
			case "process":
				e.ProcessID = resourceCatalogProcessToken(strings.Repeat("3", 32))
			case "peer":
				e.PeerID = "another-peer"
			case "direction":
				e.Direction = "incoming"
			case "empty":
				e.ProcessID = ""
			}
			if err := selectedRetry(t, c, b.ID, &e); networkErrorCode(err) != "transfer_retry_stale" {
				t.Fatalf("selection = %v", err)
			}
			if b.running || b.cancel != nil || b.State != "failed" {
				t.Fatal("stale selection changed transfer")
			}
		})
	}
}
func TestSelectedTransferRetryRejectsTerminalBusyAndRepairedPeer(t *testing.T) {
	for _, state := range []string{"completed", "cancelled", "declined", "transferring", "running", "staging", "generation", "paused", "spool"} {
		t.Run(state, func(t *testing.T) {
			c, b, e := retrySelectionFixture(t)
			switch state {
			case "running":
				b.running = true
			case "staging":
				b.staging = true
			case "generation":
				c.profile.Peers[0].Generation++
			case "paused":
				c.profile.Peers[0].Paused = true
			case "spool":
				b.Spool = ""
			default:
				b.State = state
			}
			if err := selectedRetry(t, c, b.ID, &e); err == nil {
				t.Fatal("unavailable retry admitted")
			}
			if b.cancel != nil {
				t.Fatal("rejected retry started a run")
			}
			row := c.transferViews()[0]
			if row["retryable"] != false || row["retryBlockedReason"] == "" {
				t.Fatalf("bad eligibility projection: %v", row)
			}
		})
	}
}
func TestTransferRetryEligibilityProjectionKeepsOriginalIdentity(t *testing.T) {
	c, b, _ := retrySelectionFixture(t)
	row := c.transferViews()[0]
	if row["retryable"] != true || row["retryBlockedReason"] != "" || row["peerId"] != b.PeerID || row["id"] != b.ID || row["direction"] != "outgoing" {
		t.Fatalf("projection = %v", row)
	}
}
func TestSelectedIncomingTransferRetryRejectsCompletedWhileLegacyRemainsCompatible(t *testing.T) {
	c, b, e := retrySelectionFixture(t)
	delete(c.outgoing, b.ID)
	peer := transfer.Peer{ID: e.PeerID, Generation: 1}
	if err := c.transfers.BindPeer(peer); err != nil {
		t.Fatal(err)
	}
	if _, err := c.transfers.Offer(peer, transfer.Manifest{ID: b.ID, Entries: []transfer.Entry{{ID: "folder", Path: "example", Kind: transfer.Directory}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.transfers.Accept(b.ID, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	e.Direction = "incoming"
	if err := selectedRetry(t, c, b.ID, &e); networkErrorCode(err) != "transfer_retry_unavailable" {
		t.Fatalf("terminal incoming retry=%v", err)
	}
	if err := selectedRetry(t, c, b.ID, nil); err != nil {
		t.Fatalf("legacy no-op changed=%v", err)
	}
}
