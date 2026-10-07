//go:build directlan_managed_session_native

package directlan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/testfixture"
)

// Constructor-level native composition only. These separately selected tests
// use synthetic initial contexts and fresh, unchanged IPv4 loopback endpoints.
// They do not establish durable publication, context-owner handoff, Core grants,
// endpoint movement, restart, old-flow continuity or revocation acceptance.
type managedNativeFixture struct {
	nodes  [2]*Node // lower signing key first
	ctx    context.Context
	cancel context.CancelFunc
	tasks  sync.WaitGroup
}

func newManagedNativeFixture(t *testing.T, flowLimit int) *managedNativeFixture {
	t.Helper()
	f := &managedNativeFixture{}
	f.ctx, f.cancel = context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(func() {
		for _, n := range f.nodes {
			if n != nil {
				n.RequestClose()
			}
		}
		f.cancel()
		results := make(chan error, 2)
		var closers sync.WaitGroup
		closers.Add(2)
		for _, n := range f.nodes {
			go func(n *Node) {
				defer closers.Done()
				if n == nil {
					results <- nil
				} else {
					results <- n.Close()
				}
			}(n)
		}

		joined := make(chan struct{})
		go func() { f.tasks.Wait(); closers.Wait(); close(joined) }()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-joined:
			for range f.nodes {
				if err := <-results; err != nil {
					t.Errorf("fixed owner close: %v", err)
				}
			}
		case <-timer.C:
			t.Error("native owner/application task cleanup did not join; retained/incomplete")
		}

	})
	var reservations [2]*testfixture.PortReservation
	var endpoints [2]netip.AddrPort
	for i := range reservations {
		var err error
		reservations[i], err = testfixture.ReserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 54543, 54544, 54545)
		if err != nil {
			t.Fatal(err)
		}
		reservation := reservations[i]
		t.Cleanup(func() {
			if err := reservation.Close(); err != nil {
				t.Errorf("reservation close: %v", err)
			}
		})
		endpoints[i] = reservation.Endpoint()
	}
	if endpoints[0] == endpoints[1] {
		t.Fatal("held reservations are not distinct")
	}
	cfg, remote := managedFixtureConfig()
	identities := [2]Identity{cfg.Identity, remote}
	if identities[0].PublicKey() > identities[1].PublicKey() {
		identities[0], identities[1] = identities[1], identities[0]
	}
	scope := endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"127.0.0.1/32"}}
	template := cfg.PairContexts[remote.PublicKey()]
	pair := endpointmeta.PairContext{Version: 1, HostKey: identities[0].PublicKey(), JoinerKey: identities[1].PublicKey(), HostTunnelKey: identities[0].TunnelKey(), JoinerTunnelKey: identities[1].TunnelKey(), HostNonce: template.HostNonce, JoinerNonce: template.JoinerNonce, HostEndpoint: endpoints[0].String(), JoinerEndpoint: endpoints[1].String(), HostScope: scope, JoinerScope: scope}
	for i := range f.nodes {
		other := identities[1-i]
		config := Config{Identity: identities[i], Listen: endpoints[i], AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, FlowLimit: flowLimit, ControlLimit: 4, ListenerLimit: 2, PacketQueueLimit: 2, Peers: []Peer{{Key: other.PublicKey(), TunnelKey: other.TunnelKey(), Endpoint: endpoints[1-i]}}, PairContexts: map[string]endpointmeta.PairContext{other.PublicKey(): pair}}
		var err error
		f.nodes[i], err = NewNode(config)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Reservations are released only after both immutable configurations exist.
	// A failed exact bind is failure: no endpoint reselection or fallback.
	for i, n := range f.nodes {
		if err := reservations[i].Close(); err != nil {
			t.Fatal(err)
		}
		if err := n.Start(f.ctx); err != nil {
			t.Fatal(err)
		}
		g := n.generation.Load()
		actual := g.underlay.listener.Addr().(*net.TCPAddr).AddrPort()
		if n.Endpoint() != endpoints[i] || actual != endpoints[i] {
			t.Fatal("fixed endpoint changed")
		}
		g.bind.mu.Lock()
		socket := g.bind.socket
		var udpEndpoint netip.AddrPort
		if socket != nil {
			udpEndpoint = socket.LocalAddr().(*net.UDPAddr).AddrPort()
		}
		g.bind.mu.Unlock()
		if udpEndpoint != endpoints[i] {
			t.Fatal("fixed UDP endpoint changed or missing")
		}
		p := n.peers[f.nodes[1-i].PublicKey()]
		if p == nil || p.binding == "" || p.session.registration.Load() == 0 || g.applicationPeer(p) {
			t.Fatal("fresh managed registration not closed")
		}
	}
	return f
}
func managedNativeTCPServer(ctx context.Context, n *Node, callerKey string, listener net.Listener, result chan<- error) {
	conn, err := listener.Accept()
	if err != nil {
		result <- err
		return
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		result <- err
		return
	}
	if key, ok := n.PeerKey(conn.RemoteAddr()); !ok || key != callerKey {
		result <- ErrIdentity
		return
	}
	identity, ok := conn.(interface{ PeerIdentity() (string, bool) })
	if !ok {
		result <- ErrIdentity
		return
	}
	if key, ok := identity.PeerIdentity(); !ok || key != callerKey {
		result <- ErrIdentity
		return
	}
	var payload [4]byte
	if _, err := io.ReadFull(conn, payload[:]); err != nil {
		result <- err
		return
	}
	if string(payload[:]) != "ping" {
		result <- ErrUntrusted
		return
	}
	err = writeAll(conn, []byte("pong"))
	result <- errors.Join(err, conn.Close())
}
func managedNativeTCPClient(ctx context.Context, caller, receiver *Node) error {
	conn, err := caller.DialPeer(ctx, receiver.PublicKey(), "tcp", 42001)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	if err := writeAll(conn, []byte("ping")); err != nil {
		return err
	}
	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	if string(reply[:]) != "pong" {
		return ErrUntrusted
	}
	return conn.Close()
}
func managedNativeAwait(t *testing.T, ctx context.Context, results <-chan error, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("fixed native operation did not complete", ctx.Err())
		}
	}
}
func TestManagedNativeColdCallerRoles(t *testing.T) {
	for _, callerIndex := range []int{0, 1} {
		t.Run(fmt.Sprintf("caller-%d", callerIndex), func(t *testing.T) {
			f := newManagedNativeFixture(t, 1)
			caller, receiver := f.nodes[callerIndex], f.nodes[1-callerIndex]
			listener, err := receiver.ListenPeer(f.ctx, "tcp", 42001)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			result := make(chan error, 1)
			f.run(func() { managedNativeTCPServer(f.ctx, receiver, caller.PublicKey(), listener, result) })
			if err := managedNativeTCPClient(f.ctx, caller, receiver); err != nil {
				t.Fatal(err)
			}
			managedNativeAwait(t, f.ctx, result, 1)
			for _, n := range f.nodes {
				p := n.peers[f.nodes[0].PublicKey()]
				if n == f.nodes[0] {
					p = n.peers[f.nodes[1].PublicKey()]
				}
				if !n.generation.Load().applicationPeer(p) || !p.session.ready() {
					t.Fatal("bound exchange or role readiness missing")
				}
				if !p.session.initiator && !p.session.confirmed.Load() {
					t.Fatal("responder readiness bypassed transport confirmation")
				}
			}
		})
	}
}
func TestManagedNativeSimultaneousColdCallers(t *testing.T) {
	f := newManagedNativeFixture(t, 2)
	results := make(chan error, 4)
	for i, n := range f.nodes {
		listener, err := n.ListenPeer(f.ctx, "tcp", 42001)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		f.run(func() { managedNativeTCPServer(f.ctx, n, f.nodes[1-i].PublicKey(), listener, results) })
	}
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	for i, n := range f.nodes {
		f.run(func() {
			ready <- struct{}{}
			select {
			case <-start:
			case <-f.ctx.Done():
				results <- f.ctx.Err()
				return
			}
			results <- managedNativeTCPClient(f.ctx, n, f.nodes[1-i])
		})
	}
	for range f.nodes {
		select {
		case <-ready:
		case <-f.ctx.Done():
			t.Fatal("cold callers did not reach barrier")
		}
	}
	close(start)
	managedNativeAwait(t, f.ctx, results, 4)
}
func TestManagedNativeFreshUDPAndSingleFlowControlCapacity(t *testing.T) {
	for _, callerIndex := range []int{0, 1} {
		t.Run(fmt.Sprintf("caller-%d", callerIndex), func(t *testing.T) {
			f := newManagedNativeFixture(t, 1)
			caller, receiver := f.nodes[callerIndex], f.nodes[1-callerIndex]
			listener, err := receiver.ListenPacket(f.ctx, 42002)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			deadline, _ := f.ctx.Deadline()
			if err := listener.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			f.run(func() {
				buffer := make([]byte, 16)
				count, address, err := listener.ReadFrom(buffer)
				if err != nil {
					result <- err
					return
				}
				if !bytes.Equal(buffer[:count], []byte("datagram")) {
					result <- ErrUntrusted
					return
				}
				if key, ok := receiver.PeerKey(address); !ok || key != caller.PublicKey() {
					result <- ErrIdentity
					return
				}
				count, err = listener.WriteTo(buffer[:count], address)
				if err == nil && count != len("datagram") {
					err = io.ErrShortWrite
				}
				result <- err
			})
			conn, err := caller.DialPacketPeer(f.ctx, receiver.PublicKey(), 42002)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			if count, err := conn.Write([]byte("datagram")); err != nil || count != len("datagram") {
				t.Fatal("datagram write", count, err)
			}
			buffer := make([]byte, 16)
			count, err := conn.Read(buffer)
			if err != nil || !bytes.Equal(buffer[:count], []byte("datagram")) {
				t.Fatal("datagram echo", err)
			}
			managedNativeAwait(t, f.ctx, result, 1)
		})
	}
}

func (f *managedNativeFixture) run(task func()) {
	f.tasks.Add(1)
	go func() { defer f.tasks.Done(); task() }()
}
