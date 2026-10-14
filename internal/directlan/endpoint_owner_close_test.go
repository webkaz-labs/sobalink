package directlan

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

// This fixture uses real pinned userspace TCP with in-memory packet delivery.
// It exercises the production creator and endpoint owner, not WireGuard,
// native processes, real enrollment, or physical-device acceptance.
func newEndpointCloseFixture(t *testing.T) (*runtimeGeneration, *liveEndpoint, *tcp.Endpoint, net.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	var tunnels [2]*userspaceTunnel
	for i, address := range []string{"fd00::1", "fd00::2"} {
		var err error
		tunnels[i], err = newUserspaceTunnel(netip.MustParseAddr(address), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tunnels[i].Close() })
	}
	joined := make(chan struct{}, 2)
	pump := func(from, to *userspaceTunnel) {
		defer func() { joined <- struct{}{} }()
		for {
			packet := from.ep.ReadContext(ctx)
			if packet == nil {
				return
			}
			view := packet.ToView()
			packet.DecRef()
			incoming := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithView(view)})
			to.ep.InjectInbound(ipv6.ProtocolNumber, incoming)
			incoming.DecRef()
		}
	}
	go pump(tunnels[0], tunnels[1])
	go pump(tunnels[1], tunnels[0])
	t.Cleanup(func() { cancel(); <-joined; <-joined })
	listener, err := tunnels[1].listenTCP(netip.MustParseAddrPort("[fd00::2]:55123"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	g := &runtimeGeneration{changed: make(chan struct{}), stop: make(chan struct{}), live: make(map[tcpip.Endpoint]*liveEndpoint), creators: make(map[*endpointCreator]struct{}), endpointLimit: 1}
	creator, err := g.acquireCreator(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, ep, err := tunnels[0].dialOwned(creator, "tcp", netip.AddrPort{}, netip.MustParseAddrPort("[fd00::2]:55123"))
	if err != nil {
		cleanupCreated(ep)
		creator.finishOutgoing()
		t.Fatal(err)
	}
	if !creator.TryHandoff(ep) {
		cleanupCreated(ep)
		creator.finishOutgoing()
		t.Fatal("endpoint handoff rejected")
	}
	owner := creator.live
	owner.attach(raw)
	creator.finishOutgoing()
	t.Cleanup(func() {
		g.seal(net.ErrClosed)
		select {
		case <-owner.done:
		case <-time.After(time.Second):
			t.Error("retirement did not join the endpoint owner")
		}
	})
	remote, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	if err := remote.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := owner.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Write([]byte("accepted")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 8)
	if _, err := io.ReadFull(owner, data); err != nil || string(data) != "accepted" {
		t.Fatalf("response: %q, %v", data, err)
	}
	return g, owner, ep.(*tcp.Endpoint), remote
}

func waitEndpointState(t *testing.T, endpoint *tcp.Endpoint, want tcp.EndpointState) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for endpoint.EndpointState() != want {
		if time.Now().After(deadline) {
			t.Fatalf("TCP state = %s; want %s", endpoint.EndpointState(), want)
		}
		time.Sleep(time.Millisecond)
	}
	// Join the state transition's dispatcher critical section, including HUp.
	endpoint.LockUser()
	endpoint.UnlockUser()
}

func TestEndpointOwnerCloseAfterTimeWait(t *testing.T) {
	g, owner, endpoint, remote := newEndpointCloseFixture(t)
	// A supported half-close drives active-close ordering without a test hook:
	// the owner must observe hangup even if both FINs precede its final Close.
	if err := owner.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("remote EOF: %v", err)
	}
	if err := remote.Close(); err != nil {
		t.Fatal(err)
	}
	waitEndpointState(t, endpoint, tcp.StateTimeWait)
	if _, err := g.acquireCreator(context.Background(), false); !errors.Is(err, ErrCapacity) {
		t.Fatalf("live endpoint did not retain its reservation: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-owner.done:
	case <-time.After(time.Second):
		t.Fatalf("owner missed completed TCP hangup: state=%s", endpoint.EndpointState())
	}
	if endpoint.EndpointState() != tcp.StateTimeWait {
		t.Fatalf("normal close changed TCP TIME_WAIT: %s", endpoint.EndpointState())
	}
	g.mu.Lock()
	retained := len(g.live) != 0 || owner.raw != nil || owner.ep != nil
	g.mu.Unlock()
	if retained {
		t.Fatal("completed owner retained live endpoint references")
	}
	if _, err := owner.Write([]byte("stale")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed owner admitted stale traffic: %v", err)
	}
	creator, err := g.acquireCreator(context.Background(), false)
	if err != nil {
		t.Fatalf("completed owner did not release its reservation: %v", err)
	}
	creator.finishOutgoing()
}

func TestEndpointOwnerCloseWaitsForPeerFIN(t *testing.T) {
	g, owner, endpoint, remote := newEndpointCloseFixture(t)
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("remote EOF: %v", err)
	}
	// The peer has received our FIN but has not sent its own. A close request
	// is insufficient to release the physical owner or admit another endpoint.
	select {
	case <-owner.done:
		t.Fatal("owner completed before the peer FIN")
	default:
	}
	if _, err := g.acquireCreator(context.Background(), false); !errors.Is(err, ErrCapacity) {
		t.Fatalf("unfinished close released its reservation: %v", err)
	}
	if err := remote.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-owner.done:
	case <-time.After(time.Second):
		t.Fatalf("owner did not observe peer FIN: state=%s", endpoint.EndpointState())
	}
	if endpoint.EndpointState() != tcp.StateTimeWait {
		t.Fatalf("normal active close changed TIME_WAIT: %s", endpoint.EndpointState())
	}
}

func TestEndpointOwnerRetirementJoinsUnfinishedClose(t *testing.T) {
	g, owner, endpoint, remote := newEndpointCloseFixture(t)
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("remote EOF: %v", err)
	}
	select {
	case <-owner.done:
		t.Fatal("owner completed before retirement or the peer FIN")
	default:
	}
	g.seal(net.ErrClosed)
	select {
	case <-owner.done:
	case <-time.After(time.Second):
		t.Fatal("retirement did not abort and join the unfinished close")
	}
	if endpoint.EndpointState() != tcp.StateError {
		t.Fatalf("retirement did not terminate the endpoint: %s", endpoint.EndpointState())
	}
	if _, err := g.acquireCreator(context.Background(), false); !errors.Is(err, ErrRecovery) {
		t.Fatalf("retirement reopened endpoint admission: %v", err)
	}
}
