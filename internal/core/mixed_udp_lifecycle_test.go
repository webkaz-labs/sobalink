package core

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestReviewOpaqueUDPCoreAdmissionLifecycle(t *testing.T) {
	nodes := map[string]*mixedTestPacketNode{}
	c, _, owner, _ := mixedCorePair(t, func(name string, node *pipeNode) NetworkBackend {
		p := &mixedTestPacketNode{pipeNode: node}
		nodes[name] = p
		return p
	})
	conn, err := owner.ListenPacket("udp", netip.AddrPortFrom(owner.self, 7000).String())
	if err != nil {
		t.Fatal(err)
	}
	p := conn.(*mixedPacket)
	defer p.Close()
	logical := mixedID("lan", "peer-b-lan")
	p.SetPeerFilter(func(id string) bool { return id == logical })
	mixedPacketSend(nodes["lan"].latest(t), 32000, []byte("approved"))
	alias := mixedPacketReceive(t, p, []byte("approved"))
	ap, err := netip.ParseAddrPort(alias.String())
	if err != nil {
		t.Fatal(err)
	}
	if id, e := c.authenticated(context.Background(), ap); e != nil || id != logical {
		t.Fatalf("actual admitted alias rejected: %s %v", id, e)
	}
	if _, e := c.authenticated(context.Background(), netip.AddrPortFrom(ap.Addr(), 2)); e == nil {
		t.Fatal("alias address authorized a forged source port")
	}
	if _, e := owner.DialIP(context.Background(), "udp", ap); e == nil {
		t.Fatal("opaque alias permitted as outbound destination")
	}
	packet := nodes["lan"].latest(t)
	if _, e := p.WriteTo([]byte("reply"), alias); e != nil {
		t.Fatal(e)
	}
	reply := <-packet.outgoing
	if reply.remote != netip.MustParseAddrPort("100.64.0.2:32000") {
		t.Fatalf("reply was retargeted: %v", reply.remote)
	}
	nodes["lan"].mu.Lock()
	nodes["lan"].who[netip.MustParseAddr("100.64.0.2")] = "changed"
	nodes["lan"].mu.Unlock()
	if _, e := c.authenticated(context.Background(), ap); e == nil {
		t.Fatal("changed transport identity authorized old alias")
	}
	nodes["lan"].mu.Lock()
	nodes["lan"].who[netip.MustParseAddr("100.64.0.2")] = "peer-b-lan"
	nodes["lan"].state.Snapshot.Peers[0].Expired = true
	nodes["lan"].mu.Unlock()
	if _, e := c.authenticated(context.Background(), ap); e == nil {
		t.Fatal("expired transport identity authorized old alias")
	}
	nodes["lan"].mu.Lock()
	nodes["lan"].state.Snapshot.Peers[0].Expired = false
	nodes["lan"].mu.Unlock()
	p.SetPeerFilter(nil)
	if _, e := c.authenticated(context.Background(), ap); e == nil {
		t.Fatal("retired listener filter authorized old alias")
	}
	if _, e := p.WriteTo([]byte("retired"), alias); e == nil {
		t.Fatal("retired alias routed reply")
	}
	p.SetPeerFilter(func(id string) bool { return id == logical })
	mixedPacketSend(packet, 32000, []byte("fresh"))
	fresh := mixedPacketReceive(t, p, []byte("fresh"))
	if fresh.String() == alias.String() {
		t.Fatal("re-enabled filter reused retired alias")
	}
	if _, e := p.WriteTo([]byte("wrong-port"), net.UDPAddrFromAddrPort(netip.AddrPortFrom(ap.Addr(), 2))); e == nil {
		t.Fatal("forged alias port routed reply")
	}
	// Delivery can precede the producer's deferred borrow release. Join that
	// counted work before testing idle expiration, without a wall-clock sleep.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := reviewRetireIdleUDPSource(ctx, p, fresh, nil); err != nil {
		t.Fatal(err)
	}
	freshAP, _ := netip.ParseAddrPort(fresh.String())
	if _, e := c.authenticated(context.Background(), freshAP); e == nil {
		t.Fatal("idle retired alias authenticated")
	}
	if _, e := p.WriteTo([]byte("idle-retired"), fresh); e == nil {
		t.Fatal("idle retired alias routed reply")
	}
}
