package core

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/connectionroute"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
)

func mixedCorePair(t *testing.T) (*Core, *Core, *mixedBackend, *mixedBackend) {
	t.Helper()
	open := func() (*Core, *mixedBackend) {
		c, e := Open(context.Background(), Options{Directory: t.TempDir(), Version: "test", SkipNetworkStart: true})
		if e != nil {
			t.Fatal(e)
		}
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		id, _ := connectionroute.StablePeerID(pub)
		s := mixedState{Version: 1, Seed: hex.EncodeToString(priv.Seed()), Selection: MixedSelection{Backends: []string{"lan", "tailnet"}}, Bindings: []connectionroute.Binding{}}
		raw, _ := json.Marshal(s)
		if e = c.writeAtomic(filepath.Join(c.dir, "mixed.json"), raw); e != nil {
			t.Fatal(e)
		}
		n := &mixedBackend{ctx: c.ctx, self: mixedIP(id), order: s.Selection.Backends, nodes: map[string]NetworkBackend{}, sources: map[netip.AddrPort]mixedSource{}}
		c.mu.Lock()
		c.profile.Settings.Network = "mixed"
		c.node = n
		c.mu.Unlock()
		t.Cleanup(func() { _ = c.Close() })
		return c, n
	}
	a, na := open()
	b, nb := open()
	for _, name := range []string{"lan", "tailnet"} {
		hub := &pipeNetwork{listeners: map[netip.AddrPort]*pipeListener{}}
		aIP, bIP := netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("100.64.0.2")
		node := func(ip, other netip.Addr, own, peer string) *pipeNode {
			return &pipeNode{hub: hub, ip: ip, who: map[netip.Addr]string{other: peer}, state: identity.State{SelfID: own, IPs: []netip.Addr{ip}, Backend: "Running", Snapshot: policy.Snapshot{Running: true, Peers: []policy.Peer{{ID: peer, IPs: []netip.Addr{other}}}}}}
		}
		na.nodes[name] = node(aIP, bIP, "peer-a-"+name, "peer-b-"+name)
		nb.nodes[name] = node(bIP, aIP, "peer-b-"+name, "peer-a-"+name)
	}
	for _, c := range []*Core{a, b} {
		c.op.Lock()
		if e := c.startPeerServer([]netip.Addr{c.nodeCopy().(*mixedBackend).self}); e != nil {
			t.Fatal(e)
		}
		c.op.Unlock()
	}
	return a, b, na, nb
}
func TestMixedAuthenticatedBindingAndAvailabilityFallback(t *testing.T) {
	a, _, na, _ := mixedCorePair(t)
	old := []string{mixedID("lan", "peer-b-lan"), mixedID("tailnet", "peer-b-tailnet")}
	raw, _ := json.Marshal(map[string]any{"peers": old})
	result, e := a.bindMixedPeers(context.Background(), raw)
	if e != nil {
		t.Fatal(e)
	}
	binding := result.(map[string]any)["binding"].(connectionroute.Binding)
	if _, ok := a.trust(binding.PeerID); ok {
		t.Fatal("binding granted application access")
	}
	state, e := na.State(context.Background())
	if e != nil || len(state.Snapshot.Peers) != 1 || state.Snapshot.Peers[0].ID != binding.PeerID {
		t.Fatal(state, e)
	}
	// After one backend is unavailable, the same logical endpoint selects the
	// other reviewed route. These are independent in-memory engines, not NAT proof.
	_ = na.nodes["lan"].Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, e := na.DialIP(ctx, "tcp", netip.AddrPortFrom(mixedIP(binding.PeerID), PeerPort))
	if e != nil {
		t.Fatal(e)
	}
	_ = conn.Close()
	state, e = na.State(ctx)
	if e != nil || !state.Snapshot.Running {
		t.Fatal("one backend failure disabled unrelated backend", e)
	}
}
func TestMixedOverlappingAddressesRemainSeparate(t *testing.T) {
	a, _, na, _ := mixedCorePair(t)
	state, e := na.State(context.Background())
	if e != nil || len(state.Snapshot.Peers) != 2 {
		t.Fatal(state, e)
	}
	if state.Snapshot.Peers[0].IPs[0] == state.Snapshot.Peers[1].IPs[0] {
		t.Fatal("different authenticated backends merged by IP")
	}
	if _, e = na.WhoIs(context.Background(), netip.AddrPortFrom(state.Snapshot.Peers[0].IPs[0], 40000)); e == nil {
		t.Fatal("invented synthetic endpoint authenticated")
	}
	_ = a
}
func TestMixedBindingDifferentApplicationKeysRejected(t *testing.T) {
	a, b, _, nb := mixedCorePair(t) // A claimed mapping to an identity not owned by the responding remote cannot
	// be signed. The same display/address on both routes supplies no proof.
	nb.nodes["tailnet"].(*pipeNode).mu.Lock()
	nb.nodes["tailnet"].(*pipeNode).state.SelfID = "different-authenticated-id"
	nb.nodes["tailnet"].(*pipeNode).mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"peers": []string{mixedID("lan", "peer-b-lan"), mixedID("tailnet", "peer-b-tailnet")}})
	if _, e := a.bindMixedPeers(context.Background(), raw); e == nil {
		t.Fatal("unproven identity alias accepted")
	}
	s, e := a.readMixedState()
	if e != nil || len(s.Bindings) != 0 {
		t.Fatal("failed proof persisted a binding", e)
	}
	_ = b
}

func TestMixedBindingCannotBypassKnownRevocation(t *testing.T) {
	a, _, na, _ := mixedCorePair(t)
	raw, _ := json.Marshal(map[string]any{"peers": []string{mixedID("lan", "peer-b-lan"), mixedID("tailnet", "peer-b-tailnet")}})
	result, e := a.bindMixedPeers(context.Background(), raw)
	if e != nil {
		t.Fatal(e)
	}
	binding := result.(map[string]any)["binding"].(connectionroute.Binding)
	lan := na.nodes["lan"].(*pipeNode)
	lan.mu.Lock()
	lan.state.Snapshot.Peers = nil
	lan.mu.Unlock()
	if _, e = na.DialIP(context.Background(), "tcp", netip.AddrPortFrom(mixedIP(binding.PeerID), PeerPort)); e != connectionroute.ErrDenied {
		t.Fatalf("revoked identity fell back: %v", e)
	}
	state, e := na.State(context.Background())
	if e != nil || len(state.Snapshot.Peers) != 1 || !state.Snapshot.Peers[0].Expired {
		t.Fatal("logical permission loss was hidden", state, e)
	}
}
func TestMixedBindingPausesRatherThanMigratesApprovals(t *testing.T) {
	a, _, na, _ := mixedCorePair(t)
	ids := []string{mixedID("lan", "peer-b-lan"), mixedID("tailnet", "peer-b-tailnet")}
	a.mu.Lock()
	for i, id := range ids {
		a.profile.Peers = append(a.profile.Peers, Trust{ID: id, Network: "mixed", Generation: uint64(i + 1)})
	}
	a.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"peers": ids})
	result, e := a.bindMixedPeers(context.Background(), raw)
	if e != nil {
		t.Fatal(e)
	}
	binding := result.(map[string]any)["binding"].(connectionroute.Binding)
	for _, id := range ids {
		trust, ok := a.trust(id)
		if !ok || !trust.Paused || trust.Autosave {
			t.Fatal("old approval was not paused")
		}
	}
	raw, _ = json.Marshal(map[string]any{"peerId": binding.PeerID})
	if _, e = a.unbindMixedPeer(raw); e != nil {
		t.Fatal(e)
	}
	for _, id := range ids {
		trust, _ := a.trust(id)
		if !trust.Paused {
			t.Fatal("unbind resumed an old approval")
		}
	}
	state, e := na.State(context.Background())
	if e != nil || len(state.Snapshot.Peers) != 2 {
		t.Fatal(state, e)
	}
}

func TestMixedRawTransportRevocationRetiresBinding(t *testing.T) {
	a, _, _, _ := mixedCorePair(t)
	raw, _ := json.Marshal(map[string]any{"peers": []string{mixedID("lan", "peer-b-lan"), mixedID("tailnet", "peer-b-tailnet")}})
	value, e := a.bindMixedPeers(context.Background(), raw)
	if e != nil {
		t.Fatal(e)
	}
	binding := value.(map[string]any)["binding"].(connectionroute.Binding)
	a.mu.Lock()
	a.profile.Peers = append(a.profile.Peers, Trust{ID: binding.PeerID, Network: "mixed", Generation: 1})
	a.profile.Settings.Network = "lan"
	a.mu.Unlock()
	if e = a.retireMixedTransport("lan", "peer-b-lan"); e != nil {
		t.Fatal(e)
	}
	state, e := a.readMixedState()
	if e != nil || len(state.Bindings) != 0 {
		t.Fatal(state, e)
	}
	p := a.profileCopy()
	for _, peer := range p.Peers {
		if peer.ID == binding.PeerID && !peer.Paused {
			t.Fatal("raw revocation retained logical application approval")
		}
	}
	if a.reviewPeerEpochs([]string{binding.PeerID})[binding.PeerID] == "" {
		t.Fatal("logical startup epoch was not retired")
	}
}
