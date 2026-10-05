package core

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/backendworker"
	"github.com/webkaz-labs/sobalink/internal/connectionroute"
	"net"
	"net/netip"
	"testing"
)

func TestMixedReviewBindingMustObserveExactIdentity(t *testing.T) {
	a, _, na, _ := mixedCorePair(t)
	tn := na.nodes["tailnet"].(*pipeNode)
	tn.mu.Lock()
	tn.who[netip.MustParseAddr("100.64.0.2")] = "different-current-identity"
	tn.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"peers": []string{mixedID("lan", "peer-b-lan"), mixedID("tailnet", "peer-b-tailnet")}})
	if _, err := a.bindMixedPeers(context.Background(), raw); err == nil {
		t.Fatal("binding succeeded even though backend WhoIs disagreed with bound transport identity")
	}
}

type reviewScopedNode struct{ *pipeNode }

func (n *reviewScopedNode) SetTCPScopes(context.Context, []backendworker.TCPPolicy) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return backendworker.ErrClosed
	}
	return nil
}
func TestMixedReviewUnavailableWorkerMustNotCloseHealthyWorker(t *testing.T) {
	a, _, na, _ := mixedCorePair(t)
	for name, node := range na.nodes {
		na.nodes[name] = &reviewScopedNode{node.(*pipeNode)}
	}
	raw, _ := json.Marshal(map[string]any{"peers": []string{mixedID("lan", "peer-b-lan"), mixedID("tailnet", "peer-b-tailnet")}})
	result, err := a.bindMixedPeers(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	b := result.(map[string]any)["binding"].(connectionroute.Binding)
	scopes := []backendworker.TCPPolicy{{Address: na.self, Ports: [][2]uint16{{4567, 4567}}, Peers: []netip.Addr{mixedIP(b.PeerID)}, UntilRevoked: true}}
	if err = na.SetTCPScopes(context.Background(), scopes); err != nil {
		t.Fatal(err)
	}
	na.nodes["lan"].Close()
	err = na.SetTCPScopes(context.Background(), scopes)
	_, healthyErr := na.nodes["tailnet"].State(context.Background())
	if errors.Is(healthyErr, net.ErrClosed) {
		t.Fatalf("healthy backend was closed after other worker unavailable: scope error=%v", err)
	}
}
func TestMixedReviewUnbindMustRetireSavedStartup(t *testing.T) {
	a, _, _, _ := mixedCorePair(t)
	ids := []string{mixedID("lan", "peer-b-lan"), mixedID("tailnet", "peer-b-tailnet")}
	spec := ServiceSpec{ID: "fixture-service", Name: "example", Backend: "mixed", Direction: "forward", Network: "tcp", Ports: "443", LocalPort: 8443, LoopbackHost: "127.0.0.1", Lifetime: "finite", TTLSeconds: 3600, PeerID: ids[0]}
	p := a.profileCopy()
	p.Services = []ServiceSpec{spec}
	if err := a.saveProfile(p); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.profile = p
	a.mu.Unlock()
	saveStartupFixture(t, a, spec)
	raw, _ := json.Marshal(map[string]any{"peers": ids})
	result, err := a.bindMixedPeers(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	b := result.(map[string]any)["binding"].(connectionroute.Binding)
	raw, _ = json.Marshal(map[string]any{"peerId": b.PeerID})
	if _, err = a.unbindMixedPeer(raw); err != nil {
		t.Fatal(err)
	}
	if err = a.loadStartup(false); err != nil {
		t.Fatal(err)
	}
	started := false
	a.runStartupSelections(context.Background(), func(context.Context, string, json.RawMessage) (any, error) { started = true; return nil, nil })
	if started {
		t.Fatal("original route-specific service startup approval revived after bind/unbind and startup reload")
	}
}
func TestMixedReviewFailedUnbindMustPauseAuthority(t *testing.T) {
	a, _, _, _ := mixedCorePair(t)
	ids := []string{mixedID("lan", "peer-b-lan"), mixedID("tailnet", "peer-b-tailnet")}
	raw, _ := json.Marshal(map[string]any{"peers": ids})
	result, err := a.bindMixedPeers(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	b := result.(map[string]any)["binding"].(connectionroute.Binding)
	a.mu.Lock()
	a.profile.Peers = append(a.profile.Peers, Trust{ID: b.PeerID, Network: "mixed", Generation: 1})
	a.mu.Unlock()
	a.atomicWrite = func(string, []byte) error { return errors.New("synthetic persistence failure") }
	raw, _ = json.Marshal(map[string]any{"peerId": b.PeerID})
	if _, err = a.unbindMixedPeer(raw); err == nil {
		t.Fatal("write failure not returned")
	}
	trust, ok := a.trust(b.PeerID)
	if ok && !trust.Paused {
		t.Fatal("logical peer remained authorized after failed unbind persistence")
	}
}
