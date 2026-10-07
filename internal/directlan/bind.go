package directlan

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"

	wgconn "github.com/tailscale/wireguard-go/conn"
	"github.com/tailscale/wireguard-go/device"
)

type bindPolicy struct {
	endpoints   map[netip.AddrPort]bool
	sources     map[netip.Addr]bool
	generations map[netip.Addr]*peerState
	sessions    map[[32]byte]*peerSession
}
type lanBind struct {
	mu         sync.Mutex
	cfg        Config
	socket     *net.UDPConn
	closeState *bindSocketClose
	sealed     bool
	owner      *runtimeGeneration
	policy     atomic.Pointer[bindPolicy]
}

// bindSocketClose is the one physical-close owner. Closing done publishes the
// immutable result to every ordinary/terminal caller. A failed close retains
// the exact socket reference; detaching b.socket is never completion evidence.
type bindSocketClose struct {
	done   chan struct{}
	socket *net.UDPConn
	err    error
}
type lanEndpoint struct {
	destination, source netip.AddrPort
	bind                *lanBind
	policy              *bindPolicy
}

func (e *lanEndpoint) ClearSrc()           {}
func (e *lanEndpoint) SrcToString() string { return e.source.String() }
func (e *lanEndpoint) DstToString() string { return e.destination.String() }
func (e *lanEndpoint) DstIP() netip.Addr   { return e.destination.Addr() }
func (e *lanEndpoint) SrcIP() netip.Addr   { return e.source.Addr() }
func (e *lanEndpoint) DstToBytes() []byte  { b, _ := e.destination.MarshalBinary(); return b }
func (b *lanBind) Open(port uint16) ([]wgconn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// This generation's socket is single-use. Initial empty Close calls do
	// not create closeState, but a real close can never be reset by Open.
	if b.sealed || b.closeState != nil {
		return nil, 0, net.ErrClosed
	}
	if b.socket != nil {
		return nil, 0, wgconn.ErrBindAlreadyOpen
	}
	if port != b.cfg.Listen.Port() {
		return nil, 0, ErrPolicy
	}
	network := "udp6"
	if b.cfg.Listen.Addr().Is4() {
		network = "udp4"
	}
	c, e := net.ListenUDP(network, net.UDPAddrFromAddrPort(b.cfg.Listen))
	if e != nil {
		return nil, 0, e
	}
	b.socket = c
	recv := func(slab []byte, packets []wgconn.ReceivedPacket) (int, error) {
		if len(packets) < 1 {
			return 0, ErrCapacity
		}
		for {
			n, ap, e := c.ReadFromUDPAddrPort(slab)
			if e != nil {
				return 0, e
			}
			if b.owner != nil && !b.owner.trafficOpen() {
				continue
			}
			p := b.policy.Load()
			if !b.cfg.permits(ap, true) || p == nil || !p.endpoints[ap] {
				continue
			}
			packets[0] = wgconn.ReceivedPacket{Offset: 0, Size: n, Endpoint: &lanEndpoint{destination: ap, source: b.cfg.Listen, bind: b, policy: p}}
			return 1, nil
		}
	}
	return []wgconn.ReceiveFunc{recv}, port, nil
}
func (b *lanBind) Close() error { return b.closeSocket(false) }

// SealAndClose is terminal; ordinary Close is also used by initial WG setup
// and must never create or clear this irreversible seal. Both paths preserve
// and await the first physical close, including a close already in flight.
func (b *lanBind) SealAndClose() error { return b.closeSocket(true) }
func (b *lanBind) closeSocket(terminal bool) error {
	b.mu.Lock()
	if terminal {
		b.sealed = true
	}
	state := b.closeState
	ownsClose := false
	if state == nil && b.socket != nil {
		state = &bindSocketClose{done: make(chan struct{}), socket: b.socket}
		b.socket = nil
		b.closeState = state
		ownsClose = true
	}
	b.mu.Unlock()
	if state == nil {
		// Initial empty Close remains a no-op.
		return nil
	}
	if ownsClose {
		// No bind lock is held during physical I/O. No other
		// caller owns this Close, even if terminal sealing races with it.
		state.err = state.socket.Close()
		if state.err == nil {
			state.socket = nil
		}
		close(state.done)
	} else {
		// A detached socket can still have an incomplete or failed Close.
		// Wait outside the mutex, then return exactly its published result.
		<-state.done
	}
	return state.err
}
func (b *lanBind) SetMark(mark uint32) error {
	if mark != 0 {
		return errors.New("OS socket marks are unsupported")
	}
	return nil
}
func (b *lanBind) BatchSize() int { return 1 }
func (b *lanBind) ParseEndpoint(s string) (wgconn.Endpoint, error) {
	ap, e := netip.ParseAddrPort(s)
	if e != nil || s != ap.String() || !b.cfg.permits(ap, true) {
		return nil, ErrPolicy
	}
	p := b.policy.Load()
	if p == nil || !p.endpoints[ap] {
		return nil, ErrUntrusted
	}
	return &lanEndpoint{destination: ap, source: b.cfg.Listen, bind: b, policy: p}, nil
}
func (b *lanBind) Send(bufs [][]byte, ep wgconn.Endpoint, offset int) error {
	if b.owner != nil && !b.owner.trafficOpen() {
		return net.ErrClosed
	}
	target, ok := ep.(*lanEndpoint)
	if !ok || target.bind != b {
		return wgconn.ErrWrongEndpointType
	}
	p := b.policy.Load()
	if p == nil || !p.endpoints[target.destination] || !b.cfg.permits(target.destination, true) {
		return ErrUntrusted
	}
	b.mu.Lock()
	c := b.socket
	sealed := b.sealed
	b.mu.Unlock()
	if c == nil || sealed {
		return net.ErrClosed
	}
	for _, data := range bufs {
		if offset < 0 || offset > len(data) {
			return ErrPolicy
		}
		if _, e := c.WriteToUDPAddrPort(data[offset:], target.destination); e != nil {
			return e
		}
	}
	return nil
}

var _ wgconn.Bind = (*lanBind)(nil)

// FromPeer is a notification after successful WireGuard decryption and keypair
// confirmation. It supplies cryptographic key evidence, never source-IP trust.
func (e *lanEndpoint) FromPeer(key [32]byte) {
	// Legacy unowned engines keep their existing callback. An owned engine
	// must carry the immutable registration and may not use key-only fallback.
	if e.bind == nil || e.bind.owner != nil {
		return
	}
	p := e.policy
	if p == nil {
		return
	}
	if session := p.sessions[key]; session != nil {
		session.confirm()
	}
}

func (e *lanEndpoint) FromPeerRegistration(key [32]byte, registration device.PeerRegistration) {
	if e.bind == nil || e.bind.owner == nil || e.policy == nil || registration == 0 {
		return
	}
	if session := e.policy.sessions[key]; session != nil && session.registration.Load() == uint64(registration) {
		session.confirm()
	}
}

var _ device.OwnedEndpoint = (*lanEndpoint)(nil)
