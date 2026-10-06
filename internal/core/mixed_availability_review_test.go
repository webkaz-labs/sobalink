package core

import (
	"context"
	"encoding/json"
	"github.com/webkaz-labs/sobalink/internal/connectionroute"
	"net"
	"net/netip"
	"testing"
)

func reviewBoundCore(t *testing.T, decorate ...func(string, *pipeNode) NetworkBackend) (*Core, *mixedBackend, string) {
	t.Helper()
	c, _, n, _ := mixedCorePair(t, decorate...)
	raw, _ := json.Marshal(map[string]any{"peers": []string{mixedID("lan", "peer-b-lan"), mixedID("tailnet", "peer-b-tailnet")}})
	result, e := c.bindMixedPeers(context.Background(), raw)
	if e != nil {
		t.Fatal(e)
	}
	return c, n, result.(map[string]any)["binding"].(connectionroute.Binding).PeerID
}
func reviewReadinessChange(n *mixedBackend) {
	p := n.nodes["lan"]
	var node *pipeNode
	switch p := p.(type) {
	case *pipeNode:
		node = p
	case *mixedTestPacketNode:
		node = p.pipeNode
	}
	node.mu.Lock()
	node.state.Backend = "Starting"
	node.state.Snapshot.Running = false
	node.mu.Unlock()
}
func TestMixedReviewAvailabilityAdmissionUnknownBlocksNewTCPPreservesAdmittedIdentity(t *testing.T) {
	c, n, id := reviewBoundCore(t)
	makeConn := func() net.Conn {
		a, b := net.Pipe()
		t.Cleanup(func() { a.Close(); b.Close() })
		return &pipeConn{Conn: a, local: netip.MustParseAddrPort("100.64.0.1:7000"), remote: netip.MustParseAddrPort("100.64.0.2:32000"), hub: &pipeNetwork{}}
	}
	admitted, e := n.wrap("tailnet", makeConn())
	if e != nil {
		t.Fatal(e)
	}
	defer admitted.Close()
	ap, e := netip.ParseAddrPort(admitted.RemoteAddr().String())
	if e != nil {
		t.Fatal(e)
	}
	reviewReadinessChange(n)
	if actual, e := c.authenticated(context.Background(), ap); e != nil || actual != id {
		t.Fatalf("companion Starting retired an already admitted source identity: %s %v", actual, e)
	}
	fresh, e := n.wrap("tailnet", makeConn())
	if fresh != nil {
		fresh.Close()
	}
	if e == nil {
		t.Fatal("new incoming TCP accepted while bound companion readiness unconfirmed")
	}
}
func TestMixedReviewAvailabilityAdmissionUnknownBlocksNewUDPAlias(t *testing.T) {
	nodes := map[string]*mixedTestPacketNode{}
	_, n, id := reviewBoundCore(t, func(name string, node *pipeNode) NetworkBackend {
		wrapped := &mixedTestPacketNode{pipeNode: node}
		nodes[name] = wrapped
		return wrapped
	})
	conn, e := n.ListenPacket("udp", netip.AddrPortFrom(n.self, 7000).String())
	if e != nil {
		t.Fatal(e)
	}
	p := conn.(*mixedPacket)
	defer p.Close()
	p.SetPeerFilter(func(got string) bool { return got == id })
	reviewReadinessChange(n)
	p.enqueue("tailnet", nodes["tailnet"].latest(t), netip.MustParseAddrPort("100.64.0.2:32100"), []byte("new"))
	p.mu.Lock()
	count := len(p.sources)
	queued := len(p.in)
	p.mu.Unlock()
	if count != 0 || queued != 0 {
		t.Fatalf("unknown companion allowed new UDP alias/data: sources=%d queued=%d", count, queued)
	}
}
func TestMixedReviewAvailabilityAdmissionRetryStopsAfterBindingRemoved(t *testing.T) {
	n, id, first, second := boundAvailabilityPair(t)
	first.dialErr = net.ErrClosed
	first.beforeDial = func() { n.mu.Lock(); n.bindings = nil; n.mu.Unlock() }
	conn, e := n.DialIP(availabilityDialContext(context.Background()), "tcp", netip.AddrPortFrom(mixedIP(id), PeerPort))
	if conn != nil {
		conn.Close()
	}
	if e == nil || second.dials != 0 {
		t.Fatal("retry escaped removed binding", e, second.dials)
	}
}
