package directlan

import (
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"net/netip"
	"testing"
)

func TestQueuedAdmissionCannotAdoptRepairedGeneration(t *testing.T) {
	n := memoryNode(t, 80)
	// This queue-only fixture has no live engine or supervisor. Capture one
	// generation explicitly, then detach it before memoryNode's Close cleanup.
	bind := &lanBind{}
	generation := newRuntimeGeneration(n, bind, nil)
	generation.traffic.Store(true)
	close(generation.published)
	n.generation.Store(generation)
	t.Cleanup(func() { n.generation.CompareAndSwap(generation, nil) })
	key := testIdentity(81).PublicKey()
	old := &peerState{peer: Peer{Key: key}}
	replacement := &peerState{peer: old.peer}
	source, _ := OverlayAddress(key)
	id := stack.TransportEndpointID{LocalAddress: tcpip.AddrFromSlice(n.OverlayAddr().AsSlice()), LocalPort: 42000, RemoteAddress: tcpip.AddrFromSlice(source.AsSlice()), RemotePort: 41000}
	q := newTCPAdmissions(1)
	entry, created := q.begin(id, old)
	if entry == nil || !created {
		t.Fatal("initial admission")
	}
	if again, added := q.begin(id, old); again != entry || added {
		t.Fatal("SYN retry replaced original admission")
	}
	if again, _ := q.begin(id, replacement); again != nil {
		t.Fatal("new generation overwrote queued authorization")
	}
	other := id
	other.RemotePort++
	if extra, _ := q.begin(other, old); extra != nil {
		t.Fatal("pending admission resource cap ignored")
	}
	n.mu.Lock()
	n.peers[key] = replacement
	n.mu.Unlock()
	for _, network := range []string{"tcp", "udp"} {
		if p, _, _ := n.incoming(generation, id, network, entry.peer); p != nil {
			t.Fatal("old queued request adopted new paired generation", network)
		}
	}
	q.retire(id, entry)
	next, created := q.begin(id, replacement)
	if next == nil || !created {
		t.Fatal("fresh generation denied after retirement")
	}
	q.retire(id, entry)
	if q.get(id) != next {
		t.Fatal("stale callback removed new admission")
	}
	bind.policy.Store(&bindPolicy{generations: map[netip.Addr]*peerState{source: replacement}})
	if generation.packetPeer(id) != replacement {
		t.Fatal("current atomic packet generation missing")
	}
}
