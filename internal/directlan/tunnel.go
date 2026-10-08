package directlan

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"

	"github.com/tailscale/wireguard-go/device"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

func (n *Node) refreshBindLocked() {
	g := n.generation.Load()
	if g != nil {
		g.refreshBindPolicy(n.peers)
	}
}

func (g *runtimeGeneration) refreshBindPolicy(peers map[string]*peerState) {
	p := &bindPolicy{endpoints: map[netip.AddrPort]bool{}, sources: map[netip.Addr]bool{}, generations: map[netip.Addr]*peerState{}, sessions: map[[32]byte]*peerSession{}}
	for _, peer := range peers {
		p.endpoints[peer.peer.Endpoint] = true
		a, _ := OverlayAddress(peer.peer.Key)
		p.sources[a] = true
		p.generations[a] = peer
		raw, _ := hex.DecodeString(peer.peer.TunnelKey)
		var key [32]byte
		copy(key[:], raw)
		p.sessions[key] = peer.session
	}
	g.bind.policy.Store(p)
}
func (n *Node) installPeerLocked(p Peer) error {
	g := n.generation.Load()
	state := n.peers[p.Key]
	if g == nil || state == nil || state.g != g {
		return ErrRecovery
	}
	return g.installPeer(state)
}

// installPeer configures only the captured generation and exact fresh session.
// The builder is exclusive before publication; Node.mu owns later pair edits.
func (g *runtimeGeneration) installPeer(state *peerState) error {
	if state == nil || state.g != g {
		return ErrRecovery
	}
	p := state.peer
	raw, _ := hex.DecodeString(p.TunnelKey)
	var key device.NoisePublicKey
	copy(key[:], raw)
	// The builder or Node.mu serializes configuration. Capture the exact selected
	// session before entering WG; the registration callback never discovers
	// a session from the mutable current-key map.
	target := &peerRegistrationTarget{key: key, session: state.session}
	g.registrationTarget.Store(target)
	defer g.registrationTarget.CompareAndSwap(target, nil)
	engine := g.engine.Load()
	if engine == nil {
		return ErrRecovery
	}
	a, _ := OverlayAddress(p.Key)
	if e := engine.IpcSet(fmt.Sprintf("public_key=%s\nendpoint=%s\nreplace_allowed_ips=true\nallowed_ip=%s/128\npersistent_keepalive_interval=0\n", p.TunnelKey, p.Endpoint, a)); e != nil {
		return e
	}
	handle := engine.LookupPeer(key)
	if handle == nil {
		return ErrUntrusted
	}
	registration := handle.Registration()
	if registration == 0 || state.session.registration.Load() != uint64(registration) {
		return ErrRecovery
	}
	if !g.admit(func() bool {
		g.peerRegistrations[registration] = state
		return true
	}) {
		return ErrRecovery
	}
	state.enginePeer = handle
	return nil
}
func (n *Node) dispatch(g *runtimeGeneration, f func()) bool {
	if n.contextControl || g == nil {
		return false
	}
	lease, e := g.acquireWork(nil, false)
	if e != nil {
		return false
	}
	select {
	case n.dispatchSlots <- struct{}{}:
	default:
		lease.finish()
		return false
	}
	go func() { defer lease.finish(); defer func() { <-n.dispatchSlots }(); f() }()
	return true
}
func addrPort(address tcpip.Address, port uint16) netip.AddrPort {
	a, _ := netip.AddrFromSlice(address.AsSlice())
	return netip.AddrPortFrom(a, port)
}
func (n *Node) incoming(g *runtimeGeneration, id stack.TransportEndpointID, network string, expected *peerState) (*peerState, *listener, func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.readyLocked() != nil || n.generation.Load() != g || !g.open() || n.flowUsageLocked() >= n.cfg.FlowLimit || !validService(network, id.LocalPort) || addrPort(id.LocalAddress, id.LocalPort).Addr() != n.OverlayAddr() {
		return nil, nil, nil
	}
	src := addrPort(id.RemoteAddress, id.RemotePort)
	for _, p := range n.peers {
		a, _ := OverlayAddress(p.peer.Key)
		if a == src.Addr() && p == expected && g.applicationPeer(p) {
			return p, n.listeners[service{network, id.LocalPort}], n.fallback
		}
	}
	return nil, nil, nil
}
func (n *Node) acceptTCP(g *runtimeGeneration, r *tcp.ForwarderRequest, expected *peerState, complete func(bool)) {
	id := r.ID()
	p, ln, fallback := n.incoming(g, id, "tcp", expected)
	if p == nil || (ln == nil && fallback == nil) {
		complete(true)
		return
	}
	var wq waiter.Queue
	ep, e := r.CreateEndpoint(&wq)
	if e != nil {
		complete(false)
		return
	}
	owner := g.endpoint(ep)
	// The owned forwarder installs this owner before returning success.
	// An absent owner is an integration error, never permission to fake cleanup.
	if owner == nil {
		g.requestStop(ErrRecovery)
		return
	}
	owner.attach(gonet.NewTCPConn(&wq, ep))
	complete(false)
	raw := owner
	n.mu.Lock()
	f, err := n.trackFlowLocked(g, raw, p, "tcp", true)
	n.mu.Unlock()
	if err != nil {
		raw.Close()
		return
	}
	if ln != nil {
		if !ln.deliver(f) {
			f.Close()
		}
		return
	}
	defer f.Close()
	handler, ok := fallback(f.remote, f.local)
	if !ok || handler == nil || !f.Valid() {
		return
	}
	handler(f)
}
func (n *Node) acceptUDP(g *runtimeGeneration, creator *endpointCreator, r *udp.ForwarderRequest, expected *peerState) {
	id := r.ID()
	p, ln, _ := n.incoming(g, id, "udp", expected)
	if p == nil || ln == nil {
		return
	}
	if creator.ctx.Err() != nil {
		return
	}
	var wq waiter.Queue
	ep, e := r.CreateEndpoint(&wq)
	if e != nil {
		return
	}
	creator.PublishEndpoint(ep)
	n.mu.Lock()
	valid := n.readyLocked() == nil && n.generation.Load() == g && n.peers[p.peer.Key] == p
	if !valid || !creator.TryHandoff(ep) {
		n.mu.Unlock()
		cleanupCreated(ep)
		return
	}
	raw := creator.live
	raw.attach(gonet.NewUDPConn(&wq, ep))
	f, err := n.trackFlowLocked(g, raw, p, "udp", true)
	n.mu.Unlock()
	if err != nil {
		raw.Close()
		return
	}
	if !ln.deliver(f) {
		f.Close()
	}
}
func (n *Node) DialPeer(ctx context.Context, key, network string, port uint16) (net.Conn, error) {
	capability, e := n.CapturePeer(key)
	if e != nil {
		return nil, e
	}
	return capability.DialPeer(ctx, network, port)
}
func (n *Node) dialCapturedPeer(ctx context.Context, expected *runtimeGeneration, p *peerState, network string, port uint16) (net.Conn, error) {
	if !validService(network, port) {
		return nil, ErrUnavailable
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	n.mu.Lock()
	if e := n.readyLocked(); e != nil {
		n.mu.Unlock()
		return nil, e
	}
	if n.generation.Load() != expected || p == nil || p.g != expected || n.peers[p.peer.Key] != p {
		n.mu.Unlock()
		return nil, ErrUntrusted
	}
	key := p.peer.Key
	if n.flowUsageLocked() >= n.cfg.FlowLimit {
		n.mu.Unlock()
		return nil, ErrCapacity
	}
	g := expected
	run, cancel := context.WithTimeout(ctx, handshakeTimeout)
	creator, e := g.acquireCreator(run, false)
	if e != nil {
		n.mu.Unlock()
		cancel()
		return nil, e
	}
	pending := &pendingDial{key: key, cancel: cancel, g: g}
	n.dials[pending] = struct{}{}
	n.mu.Unlock()
	defer func() { cancel(); n.mu.Lock(); delete(n.dials, pending); n.mu.Unlock(); creator.finishOutgoing() }()
	if e := localAddressReady(expected.bind.cfg.Listen.Addr()); e != nil {
		return nil, e
	}
	if e := n.ensureSession(creator.ctx, p); e != nil {
		return nil, e
	}
	target, _ := OverlayAddress(key)
	ap := netip.AddrPortFrom(target, port)
	raw, ep, e := g.tunnel.dialOwned(creator, network, netip.AddrPortFrom(n.OverlayAddr(), 0), ap)
	if e != nil {
		cleanupCreated(ep)
		return nil, e
	}
	n.mu.Lock()
	valid := n.readyLocked() == nil && n.generation.Load() == g && n.peers[key] == p && creator.ctx.Err() == nil
	if !valid || !creator.TryHandoff(ep) {
		n.mu.Unlock()
		cleanupCreated(ep)
		return nil, net.ErrClosed
	}
	owner := creator.live
	owner.attach(raw)
	delete(n.dials, pending)
	f, e := n.trackFlowLocked(g, owner, p, network, false)
	n.mu.Unlock()
	if e != nil {
		owner.requestClose()
		return nil, e
	}
	return f, nil
}
func (n *Node) DialPacketPeer(ctx context.Context, key string, port uint16) (ConnPacketConn, error) {
	c, e := n.DialPeer(ctx, key, "udp", port)
	if e != nil {
		return nil, e
	}
	return c.(*flow), nil
}

// packetPeer is safe on the synchronous WireGuard receive path. It must never
// acquire Node.mu: revocation holds that lock while joining the peer receiver.
func (g *runtimeGeneration) packetPeer(id stack.TransportEndpointID) *peerState {
	if !g.open() {
		return nil
	}
	p := g.bind.policy.Load()
	if p == nil || addrPort(id.LocalAddress, id.LocalPort).Addr() != g.n.OverlayAddr() {
		return nil
	}
	peer := p.generations[addrPort(id.RemoteAddress, id.RemotePort).Addr()]
	if !g.applicationPeer(peer) {
		return nil
	}
	return peer
}
