package core

import (
	"context"
	"encoding/json"
	"net/netip"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/ranges"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestCapacityPreviewCountsSuspendedListenerReservations(t *testing.T) {
	ports, err := ranges.Parse("8000-8002")
	if err != nil {
		t.Fatal(err)
	}
	c := &Core{capacity: capacity.Defaults(), active: map[string]*activeService{"retained": {spec: ServiceSpec{Direction: "forward", Network: "tcp"}, effective: ports}}, proxies: map[string]*activeProxy{"proxy": {}}}
	if got := c.capacityUsage()["materializedListeners"]; got != 4 {
		t.Fatalf("preview reported %d reservations, want4", got)
	}
	if c.materializedCount() != 4 {
		t.Fatal("preview and admission disagree")
	}
}

func TestRetainedShareScopeRecoversAfterCapacityReduction(t *testing.T) {
	p := newCorePair(t)
	p.nb.mu.Lock()
	p.nb.state.Snapshot.Peers = append(p.nb.state.Snapshot.Peers, policy.Peer{ID: "peer-c", IPs: []netip.Addr{netip.MustParseAddr("100.64.0.3")}})
	p.nb.mu.Unlock()
	for _, rule := range []map[string]any{
		{"name": "several-intervals", "network": "tcp", "ports": "8080,8082", "peerIds": []string{"peer-a", "peer-c"}, "ttlSeconds": 600},
		{"name": "second-policy", "network": "tcp", "ports": "8090", "peerIds": []string{"peer-a"}, "ttlSeconds": 600},
	} {
		mustCommand(t, p.b, "service.share", rule)
	}
	withServiceOperation(p.b, func() {
		until := map[string]time.Time{}
		for id, a := range p.b.active {
			until[id] = a.expires
		}
		p.b.mu.Lock()
		for _, key := range []string{"rangePolicies", "portIntervals", "sharePeers"} {
			p.b.capacity.Logical[key] = capacity.Limited(1)
		}
		p.b.mu.Unlock()
		p.b.suspendServices()
		state, err := p.nb.State(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		p.b.revalidateServices(state)
		for id, a := range p.b.active {
			if !p.b.serviceTransportReady(a) || !a.expires.Equal(until[id]) {
				t.Fatal("lowered admission stranded or renewed existing permission")
			}
		}
	})
	raw, _ := json.Marshal(map[string]any{"name": "new-policy", "network": "tcp", "ports": "8091", "peerIds": []string{"peer-a"}, "ttlSeconds": 600})
	if _, err := p.b.Command(t.Context(), webui.Command{RequestID: "new-after-reduction", Name: "service.share", Payload: raw}); err == nil {
		t.Fatal("reduced admission accepted another grant")
	}
	if len(p.b.profileCopy().Services) != 2 {
		t.Fatal("rejected admission changed saved definitions")
	}
}

// This native listener regression is exercised by CI. The local executor's
// socket-free suite selects other tests and does not invoke this function.
func TestMaterializedOneListenerUsesItsSingleReservation(t *testing.T) {
	port := availableServicePort(t)
	p := newCorePair(t)
	p.a.mu.Lock()
	p.a.capacity.Resources["materializedListeners"] = capacity.Limited(1)
	p.a.mu.Unlock()
	value := mustCommand(t, p.a, "service.connect", map[string]any{"name": "single", "network": "tcp", "ports": "8080", "localPort": port, "peerId": "peer-b", "lifetime": "until-stopped"}).(map[string]any)
	if value["status"] != "active" {
		t.Fatal("single reservation did not create one listener", value["status"])
	}
	if p.a.materializedCount() != 1 {
		t.Fatal("listener reservation count changed")
	}
}
