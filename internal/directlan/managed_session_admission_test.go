package directlan

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// These synthetic records exercise admission and buffered-read revalidation.
// They do not represent WG decryption, netstack construction or a real flow.
type managedAdmissionBuffer struct {
	invalidate    func()
	local, remote net.Addr
}

func (c *managedAdmissionBuffer) Read(b []byte) (int, error) {
	count := copy(b, "buffered")
	if c.invalidate != nil {
		c.invalidate()
	}
	return count, nil
}
func (c *managedAdmissionBuffer) Write(b []byte) (int, error)      { return len(b), nil }
func (c *managedAdmissionBuffer) Close() error                     { return nil }
func (c *managedAdmissionBuffer) LocalAddr() net.Addr              { return c.local }
func (c *managedAdmissionBuffer) RemoteAddr() net.Addr             { return c.remote }
func (c *managedAdmissionBuffer) SetDeadline(time.Time) error      { return nil }
func (c *managedAdmissionBuffer) SetReadDeadline(time.Time) error  { return nil }
func (c *managedAdmissionBuffer) SetWriteDeadline(time.Time) error { return nil }

func TestManagedApplicationAdmissionSurfaces(t *testing.T) {
	n, g, p, _ := managedFixtureOwner(t)
	remote, _ := OverlayAddress(p.peer.Key)
	id := stack.TransportEndpointID{LocalAddress: tcpip.AddrFromSlice(n.OverlayAddr().AsSlice()), LocalPort: 42001, RemoteAddress: tcpip.AddrFromSlice(remote.AsSlice()), RemotePort: 42003}
	for _, network := range []string{"tcp", "udp"} {
		if g.packetPeer(id) != nil {
			t.Fatal("unauthenticated packet dispatched", network)
		}
		if peer, _, _ := n.incoming(g, id, network, p); peer != nil {
			t.Fatal("unauthenticated incoming request admitted", network)
		}
		n.mu.Lock()
		_, err := n.trackFlowLocked(g, nil, p, network, true)
		n.mu.Unlock()
		if !errors.Is(err, ErrUntrusted) {
			t.Fatal("unauthenticated flow creation admitted", network, err)
		}
	}
	if err := n.commitManagedSession(context.Background(), n.captureManagedSession(p)); err != nil {
		t.Fatal(err)
	}
	if g.packetPeer(id) != p {
		t.Fatal("exact current packet identity rejected")
	}
	for _, network := range []string{"tcp", "udp"} {
		if peer, _, _ := n.incoming(g, id, network, p); peer != p {
			t.Fatal("exact incoming identity rejected", network)
		}
	}
	raw := &managedAdmissionBuffer{local: &net.TCPAddr{IP: net.IP(n.OverlayAddr().AsSlice()), Port: 42001}, remote: &net.TCPAddr{IP: net.IP(remote.AsSlice()), Port: 42003}}
	endpoint := newLiveEndpoint(g, nil)
	endpoint.attach(raw)
	n.mu.Lock()
	f, err := n.trackFlowLocked(g, endpoint, p, "tcp", true)
	n.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// Release only synthetic bookkeeping; there is no endpoint supervisor or
	// physical resource whose completion can be manufactured by this fixture.
	t.Cleanup(func() {
		n.mu.Lock()
		delete(n.wires, f.w)
		delete(n.flows[f.remote], f)
		n.mu.Unlock()
		f.work.finish()
	})
	if key, ok := f.PeerIdentity(); !ok || key != p.peer.Key {
		t.Fatal("exact flow identity missing")
	}
	if key, ok := n.PeerKey(raw.RemoteAddr()); !ok || key != p.peer.Key {
		t.Fatal("exact address attribution missing")
	}
	raw.invalidate = func() { p.authenticated.Store(nil) }
	buffer := make([]byte, 16)
	count, readErr := f.Read(buffer)
	if count != 0 || !errors.Is(readErr, net.ErrClosed) {
		t.Fatal("buffered bytes escaped lost authentication", count, readErr)
	}
	for _, value := range buffer[:len("buffered")] {
		if value != 0 {
			t.Fatal("unauthorized buffered bytes not cleared")
		}
	}
	if _, ok := f.PeerIdentity(); ok {
		t.Fatal("stale flow still attributed")
	}
	if _, ok := n.PeerKey(raw.RemoteAddr()); ok {
		t.Fatal("stale address still attributed")
	}
	n.mu.Lock()
	valid := n.validFlowLocked(f)
	n.mu.Unlock()
	if valid || g.packetPeer(id) != nil {
		t.Fatal("established-flow or packet gate retained authentication")
	}
	for _, network := range []string{"tcp", "udp"} {
		if peer, _, _ := n.incoming(g, id, network, p); peer != nil {
			t.Fatal("incoming revalidation retained authentication", network)
		}
	}
}
