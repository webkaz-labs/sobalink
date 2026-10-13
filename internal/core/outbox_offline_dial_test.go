package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
)

type outboxOfflineDialBackend struct {
	NetworkBackend
	dials  atomic.Int32
	denied error
}

func (b *outboxOfflineDialBackend) DialIP(context.Context, string, netip.AddrPort) (net.Conn, error) {
	b.dials.Add(1)
	return nil, b.denied
}

// Invoke production discovery and delivery directly so the causal proof does
// not depend on a timer or caller names. Running remains true throughout; only
// the peer availability flag changes for the final positive control.
func TestOutboxOfflineDeliveryAfterFailedDiscoveryNeverDials(t *testing.T) {
	c := outboxFixture(t) // No background maintenance goroutine in this fixture.
	node := c.node.(*pipeNode)
	node.state = identity.State{
		SelfID: "peer-a", Backend: "Running",
		IPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")},
		Snapshot: policy.Snapshot{Running: true, Peers: []policy.Peer{
			{ID: "peer-b", Online: false, IPs: []netip.Addr{netip.MustParseAddr("100.64.0.2")}},
			{ID: "peer-c", Online: false, IPs: []netip.Addr{netip.MustParseAddr("100.64.0.3")}},
		}},
	}
	backend := &outboxOfflineDialBackend{NetworkBackend: node, denied: errors.New("synthetic transport denied")}
	c.node = backend
	t.Cleanup(func() {
		if c.peerHTTPTransport != nil {
			c.peerHTTPTransport.close()
		}
	})
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	state, err := backend.State(ctx)
	if err != nil || !state.Snapshot.Running {
		t.Fatal("running discovery fixture unavailable")
	}
	// This is the same synchronous refresh pass called by maintain. No Outbox
	// preparation, admission or worker exists yet, so these calls are discovery.
	c.refreshPeers(state)
	if backend.dials.Load() != 2 || len(c.confirmed) != 0 || len(c.outbox.prepared) != 0 || len(c.outbox.state.Intents) != 0 {
		t.Fatal("controlled pre-admission discovery did not reach both denied dials")
	}
	for _, peer := range state.Snapshot.Peers {
		if peer.Online || c.discoveryObservation(peer.ID).State != "unconfirmed" {
			t.Fatal("failed discovery changed offline peer evidence")
		}
	}
	t.Logf("pre-admission discovery dials=%d", backend.dials.Load())
	review := outboxCommitFixture(t, c, outboxPrepareFixture(t, c, "offline-dial"))
	row := c.outbox.state.Intents[review.OutboxID]
	delivery := row.Deliveries[0]
	batch := &outgoingBatch{ID: delivery.Offer.DeliveryID, PeerID: delivery.Trust.ID, Network: delivery.Trust.Network, Generation: delivery.Trust.Generation, Manifest: delivery.Offer.Manifest, backend: backend, outboxID: review.OutboxID}
	before := backend.dials.Load()
	if networkErrorCode(c.deliverOutbox(ctx, batch)) != "outbox_peer_offline" || backend.dials.Load() != before {
		t.Fatal("offline delivery reached transport after failed discovery")
	}
	assertUnattempted := func() {
		t.Helper()
		if row.View.DeliveryOutcome != "not_attempted" {
			t.Fatal("denied transport changed aggregate delivery evidence")
		}
		for i, delivery := range row.Deliveries {
			child := row.View.Children[i]
			if delivery.Attempted || delivery.Exposure || delivery.Lease != nil || child.DeliveryOutcome != "not_attempted" || len(child.Files) != 0 {
				t.Fatal("denied transport acquired delivery or receiver evidence")
			}
		}
	}
	assertUnattempted()
	// The same fresh intent and real delivery method must reach the trap when
	// the selected peer is online, proving the offline zero-dial check is live.
	node.mu.Lock()
	node.state.Snapshot.Peers[0].Online = true
	node.mu.Unlock()
	if !errors.Is(c.deliverOutbox(ctx, batch), backend.denied) || backend.dials.Load() != before+1 {
		t.Fatal("online delivery positive control did not reach the denied dial")
	}
	assertUnattempted()
	t.Logf("delivery dials: offline=0 online=%d", backend.dials.Load()-before)
}
