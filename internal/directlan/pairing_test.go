package directlan

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func memoryNode(t *testing.T, n byte) *Node {
	t.Helper()
	node, e := NewNode(testConfig(n))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(attachMemoryGeneration(node))
	return node
}

// attachMemoryGeneration supplies explicit generation/config metadata only.
// It creates no engine, tunnel, sockets or retirement supervisor. Detach this
// exact synthetic owner before Node.Close, which must not wait for an unstarted
// supervisor. Do not synthesize a completed retirement signal for the fixture.
func attachMemoryGeneration(node *Node) func() {
	g := newRuntimeGeneration(node, &lanBind{cfg: cloneGenerationConfig(node.cfg)}, nil)
	g.peers = node.peers
	g.traffic.Store(true)
	close(g.published)
	node.generation.Store(g)
	node.started = true
	return func() {
		node.generation.CompareAndSwap(g, nil)
		node.Close()
	}
}

func memoryClient(t *testing.T, host, client *Node) (*tls.Conn, func()) {
	t.Helper()
	a, b := net.Pipe()
	host.mu.Lock()
	w := &wire{raw: a, g: host.generation.Load()}
	host.wires[w] = struct{}{}
	host.wg.Add(1)
	host.mu.Unlock()
	go func() { defer host.wg.Done(); host.handle(w) }()
	c := tls.Client(b, tlsConfig(client.cert, host.PublicKey(), false))
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if e := c.Handshake(); e != nil {
		t.Fatal(e)
	}
	return c, func() { b.Close() }
}
func memoryPair(t *testing.T, host, client *Node, inv Invitation) response {
	t.Helper()
	c, close := memoryClient(t, host, client)
	defer close()
	p := Peer{Key: client.PublicKey(), Endpoint: client.Endpoint(), TunnelKey: client.cfg.Identity.TunnelKey()}
	if e := writeJSON(c, request{Version: 1, Operation: "pair", Token: inv.Token, Peer: &p}); e != nil {
		t.Fatal(e)
	}
	var r response
	if e := readJSON(c, &r); e != nil {
		t.Fatal(e)
	}
	return r
}
func TestInvitationBoundedOneUseAuthenticated(t *testing.T) {
	host, client, wrong := memoryNode(t, 1), memoryNode(t, 2), memoryNode(t, 3)
	for _, ttl := range []time.Duration{0, time.Millisecond, MaxInvitationTTL + time.Second} {
		if _, e := host.IssueInvitation(context.Background(), Peer{Key: client.PublicKey()}, ttl); e == nil {
			t.Fatal("invalid lifetime")
		}
	}
	inv, e := host.IssueNamedInvitation(context.Background(), Peer{Key: client.PublicKey(), Name: "Synthetic client"}, "Synthetic host", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	text, e := inv.Encode()
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := ParseInvitation(text)
	if e != nil || parsed.Token != inv.Token {
		t.Fatal(e)
	}
	if inv.ValidateFor(wrong.PublicKey(), time.Now()) == nil || inv.ValidateFor(client.PublicKey(), inv.Expires) == nil {
		t.Fatal("wrong recipient or expired")
	}
	if r := memoryPair(t, host, wrong, inv); r.OK {
		t.Fatal("wrong identity accepted")
	}
	if r := memoryPair(t, host, client, inv); !r.OK {
		t.Fatal("pairing failed", r)
	}
	if r := memoryPair(t, host, client, inv); r.OK {
		t.Fatal("invitation replay accepted")
	}
	if len(host.Peers()) != 1 || host.Peers()[0].Key != client.PublicKey() || host.Peers()[0].Name != "Synthetic client" {
		t.Fatal(host.Peers())
	}
}
func TestInvitationCancelExpiredAndEndpointScope(t *testing.T) {
	host, client := memoryNode(t, 4), memoryNode(t, 5)
	inv, e := host.IssueInvitation(context.Background(), Peer{Key: client.PublicKey()}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	host.CancelInvitation(inv.Token)
	if memoryPair(t, host, client, inv).OK {
		t.Fatal("cancelled token")
	}
	inv, e = host.IssueInvitation(context.Background(), Peer{Key: client.PublicKey()}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	host.mu.Lock()
	p := host.invites[tokenHash(inv.Token)]
	p.deadline = time.Now().Add(-time.Second)
	host.invites[tokenHash(inv.Token)] = p
	host.mu.Unlock()
	if memoryPair(t, host, client, inv).OK {
		t.Fatal("expired token")
	}
	if _, e = host.IssueInvitation(context.Background(), Peer{Key: client.PublicKey(), Endpoint: netip.MustParseAddrPort("8.8.8.8:40000")}, time.Minute); e == nil {
		t.Fatal("public recipient endpoint")
	}
}
func TestPersistenceFailureDoesNotActivate(t *testing.T) {
	// Persistence is captured by the generation. Configure the failure before
	// constructing that immutable owner rather than changing the node template.
	var saveCalled atomic.Bool
	cfg := testConfig(6)
	cfg.Persist = func([]Peer) error {
		saveCalled.Store(true)
		return errors.New("synthetic save failure")
	}
	host, e := NewNode(cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(attachMemoryGeneration(host))
	client := memoryNode(t, 7)
	inv, e := host.IssueInvitation(context.Background(), Peer{Key: client.PublicKey()}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	c, close := memoryClient(t, host, client)
	defer close()
	p := Peer{Key: client.PublicKey(), Endpoint: client.Endpoint(), TunnelKey: client.cfg.Identity.TunnelKey()}
	if e = writeJSON(c, request{Version: 1, Operation: "pair", Token: inv.Token, Peer: &p}); e != nil {
		t.Fatal(e)
	}
	var r response
	if e = readJSON(c, &r); e == nil && r.OK {
		t.Fatal("failed save activated pairing")
	}
	host.mu.Lock()
	recovery := host.recovery
	host.mu.Unlock()
	if !saveCalled.Load() {
		t.Fatal("persistence failure was not exercised")
	}
	if !recovery || len(host.Peers()) != 0 {
		t.Fatal("missing fail closed latch")
	}
	if _, e = host.IssueInvitation(context.Background(), Peer{Key: client.PublicKey()}, time.Minute); !errors.Is(e, ErrRecovery) {
		t.Fatal(e)
	}
}
func TestConcurrentTokenConsumption(t *testing.T) {
	host, client := memoryNode(t, 8), memoryNode(t, 9)
	inv, e := host.IssueInvitation(context.Background(), Peer{Key: client.PublicKey()}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	success := 0
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := memoryPair(t, host, client, inv)
			if r.OK {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if success != 1 {
		t.Fatal("one-use count", success)
	}
}
