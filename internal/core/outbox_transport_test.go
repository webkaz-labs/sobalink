package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// Real HTTP framing and request ownership over the existing in-memory transport.
// No OS listener, enrollment, or external network is used.
func outboxPipePeer(t *testing.T, c *Core, handler http.Handler) {
	t.Helper()
	a, b := netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("100.64.0.2")
	hub := &pipeNetwork{listeners: map[netip.AddrPort]*pipeListener{}}
	node := c.node.(*pipeNode)
	node.mu.Lock()
	node.hub, node.ip = hub, a
	node.state = identity.State{SelfID: "peer-a", IPs: []netip.Addr{a}, Backend: "Running", Snapshot: policy.Snapshot{Running: true, Peers: []policy.Peer{{ID: "peer-b", DNSName: "peer-b.invalid", IPs: []netip.Addr{b}, Online: true}}}}
	node.mu.Unlock()
	remote := &pipeNode{hub: hub, ip: b}
	listener, err := remote.Listen("tcp", netip.AddrPortFrom(b, PeerPort).String())
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-done
		if c.peerHTTPTransport != nil {
			c.peerHTTPTransport.close()
		}
	})
}

func TestOutboxFileLeaseHeaderSurvivesPrivateRequestContext(t *testing.T) {
	c := outboxFixture(t)
	header := make(chan string, 1)
	outboxPipePeer(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header <- r.Header.Get("X-Sobalink-Durable-Lease")
		reply(w, http.StatusOK, map[string]any{})
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, outboxLeaseHeaderKey{}, "synthetic-bounded-lease")
	var result map[string]any
	if err := c.peerRequest(ctx, "peer-b", "POST", transfer.DurableFilePath+"?fileID=1", nil, "application/octet-stream", &result); err != nil {
		t.Fatal(err)
	}
	if got := <-header; got != "synthetic-bounded-lease" {
		t.Fatalf("lease header lost at private context boundary: %q", got)
	}
}

func TestOutboxPolicyReductionQueriesRetainedLeaseButRefusesNewExposure(t *testing.T) {
	for _, state := range []transfer.BatchState{transfer.Completed, transfer.Accepted} {
		t.Run(string(state), func(t *testing.T) {
			c := outboxFixture(t)
			review := outboxPrepareFixture(t, c, "retained-transport")
			outboxCommitFixture(t, c, review)
			row := c.outbox.state.Intents[review.OutboxID]
			d := &row.Deliveries[0]
			d.Attempted = true
			d.Lease = &transfer.DurableLease{Version: 1, DeliveryID: d.Offer.DeliveryID, Digest: d.Offer.Digest, RecipientID: d.Trust.ID, ReceiverEpoch: strings.Repeat("d", 32), Token: strings.Repeat("e", 32), ExpiresAt: review.ExpiresAt, ReceiptUntil: review.ExpiresAt.Add(transfer.DurableReceiptGrace)}
			row.View.Children[0].DeliveryOutcome = "unknown"
			status := transfer.DurableStatus{Lease: *d.Lease, State: state}
			if state == transfer.Completed {
				e := review.Entries[0]
				status.Receipt = &transfer.DurableReceipt{Lease: *d.Lease, State: state, SavedAt: time.Now(), Files: []transfer.FileAck{{BatchID: d.Offer.DeliveryID, FileID: e.ID, StoredName: e.Path, Size: e.Size, SHA256: e.SHA256}}}
			}
			var mu sync.Mutex
			var paths []string
			outboxPipePeer(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				if r.URL.Path != transfer.DurableStatusPath {
					http.Error(w, "unexpected mutation", http.StatusConflict)
					return
				}
				var request transfer.DurableRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.Lease != *d.Lease {
					http.Error(w, "wrong lease", http.StatusConflict)
					return
				}
				reply(w, http.StatusOK, status)
			}))
			c.mu.Lock()
			c.capacity.Resources["transferManifestBytes"] = capacity.Limited(1)
			c.mu.Unlock()
			batch := &outgoingBatch{ID: d.Offer.DeliveryID, PeerID: d.Trust.ID, Network: d.Trust.Network, Generation: d.Trust.Generation, Manifest: d.Offer.Manifest, backend: c.nodeCopy(), outboxID: review.OutboxID}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := c.deliverOutbox(ctx, batch)
			if state == transfer.Completed && (err != nil || row.View.Children[0].DeliveryOutcome != "confirmed") {
				t.Fatalf("retained receipt was not reconciled: %v", err)
			}
			if state == transfer.Accepted && (networkErrorCode(err) != "outbox_capacity" || d.Exposure) {
				t.Fatalf("reduced policy authorized new exposure: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(paths) != 1 || paths[0] != transfer.DurableStatusPath {
				t.Fatalf("wrong retained-lease requests: %v", paths)
			}
		})
	}
}
