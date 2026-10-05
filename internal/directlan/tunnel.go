package directlan

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/tailscale/wireguard-go/device"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

func (n *Node) refreshBindLocked() {
	p := &bindPolicy{endpoints: map[netip.AddrPort]bool{}, sources: map[netip.Addr]bool{}, generations: map[netip.Addr]*peerState{}}
	for _, peer := range n.peers {
		p.endpoints[peer.peer.Endpoint] = true
		a, _ := OverlayAddress(peer.peer.Key)
		p.sources[a] = true
		p.generations[a] = peer
	}
	n.bind.policy.Store(p)
}
func (n *Node) installPeerLocked(p Peer) error {
	a, _ := OverlayAddress(p.Key)
	return n.engine.IpcSet(fmt.Sprintf("public_key=%s\nendpoint=%s\nreplace_allowed_ips=true\nallowed_ip=%s/128\npersistent_keepalive_interval=0\n", p.TunnelKey, p.Endpoint, a))
}
func (n *Node) startTunnelLocked() error {
	b := &lanBind{cfg: n.cfg}
	n.bind = b
	n.refreshBindLocked()
	local := n.OverlayAddr()
	t, e := newUserspaceTunnel(local, func(src, dst netip.Addr) bool {
		p := b.policy.Load()
		return p != nil && dst == local && p.sources[src]
	})
	if e != nil {
		return e
	}
	n.tunnel = t
	n.tcpIncoming = newTCPAdmissions(n.cfg.FlowLimit)
	tf := tcp.NewForwarder(t.stack, 0, n.cfg.FlowLimit, func(r *tcp.ForwarderRequest) {
		id := r.ID()
		entry := n.tcpIncoming.get(id)
		if entry == nil {
			r.Complete(true)
			return
		}
		retire := func() { n.tcpIncoming.retire(id, entry) }
		if !n.dispatch(func() { n.acceptTCP(r, entry.peer, retire) }) {
			r.Complete(true)
			retire()
		}
	})
	t.stack.SetTransportProtocolHandler(tcp.ProtocolNumber, func(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
		peer := n.packetPeer(id)
		if peer == nil || !validService("tcp", id.LocalPort) {
			return false
		}
		entry, added := n.tcpIncoming.begin(id, peer)
		if entry == nil {
			return true
		}
		handled := tf.HandlePacket(id, pkt)
		if !handled && added {
			n.tcpIncoming.retire(id, entry)
		}
		return handled
	})
	t.stack.SetTransportProtocolHandler(udp.ProtocolNumber, func(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
		peer := n.packetPeer(id)
		if peer == nil || !validService("udp", id.LocalPort) {
			return false
		}
		held := pkt.Clone()
		if !n.dispatch(func() { defer held.DecRef(); n.acceptUDP(udp.NewForwarderRequest(t.stack, id, held), peer) }) {
			held.DecRef()
			return false
		}
		return true
	})
	n.engine = device.NewDevice(t, b, device.NewLogger(device.LogLevelSilent, ""), device.WithQueueStagedSize(64), device.WithQueueInboundSize(128), device.WithQueueOutboundSize(128), device.WithQueueHandshakeSize(64))
	priv, e := n.cfg.Identity.tunnelPrivate()
	if e == nil {
		e = n.engine.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=%d\n", hex.EncodeToString(priv), n.cfg.Listen.Port()))
	}
	if e == nil {
		for _, p := range n.peers {
			if e = n.installPeerLocked(p.peer); e != nil {
				break
			}
		}
	}
	if e == nil {
		e = n.engine.Up()
	}
	if e != nil {
		n.engine.Close()
		n.engine = nil
		n.tunnel = nil
		return e
	}
	return nil
}
func (n *Node) dispatch(f func()) bool {
	n.dispatchMu.Lock()
	defer n.dispatchMu.Unlock()
	if n.dispatchClosed {
		return false
	}
	select {
	case n.dispatchSlots <- struct{}{}:
	default:
		return false
	}
	n.wg.Add(1)
	go func() { defer n.wg.Done(); defer func() { <-n.dispatchSlots }(); f() }()
	return true
}
func addrPort(address tcpip.Address, port uint16) netip.AddrPort {
	a, _ := netip.AddrFromSlice(address.AsSlice())
	return netip.AddrPortFrom(a, port)
}
func (n *Node) incoming(id stack.TransportEndpointID, network string, expected *peerState) (*peerState, *listener, func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.readyLocked() != nil || len(n.wires) >= n.cfg.FlowLimit || !validService(network, id.LocalPort) || addrPort(id.LocalAddress, id.LocalPort).Addr() != n.OverlayAddr() {
		return nil, nil, nil
	}
	src := addrPort(id.RemoteAddress, id.RemotePort)
	for _, p := range n.peers {
		a, _ := OverlayAddress(p.peer.Key)
		if a == src.Addr() && p == expected {
			return p, n.listeners[service{network, id.LocalPort}], n.fallback
		}
	}
	return nil, nil, nil
}
func (n *Node) acceptTCP(r *tcp.ForwarderRequest, expected *peerState, retire func()) {
	defer retire()
	id := r.ID()
	p, ln, fallback := n.incoming(id, "tcp", expected)
	if p == nil || (ln == nil && fallback == nil) {
		r.Complete(true)
		return
	}
	var wq waiter.Queue
	ep, e := r.CreateEndpoint(&wq)
	if e != nil {
		r.Complete(true)
		return
	}
	r.Complete(false)
	retire()
	raw := gonet.NewTCPConn(&wq, ep)
	n.mu.Lock()
	f, err := n.trackFlowLocked(raw, p, "tcp", true)
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
func (n *Node) acceptUDP(r *udp.ForwarderRequest, expected *peerState) {
	id := r.ID()
	p, ln, _ := n.incoming(id, "udp", expected)
	if p == nil || ln == nil {
		return
	}
	var wq waiter.Queue
	ep, e := r.CreateEndpoint(&wq)
	if e != nil {
		return
	}
	raw := gonet.NewUDPConn(&wq, ep)
	n.mu.Lock()
	f, err := n.trackFlowLocked(raw, p, "udp", true)
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
	if e := n.Ready(); e != nil {
		return nil, e
	}
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
	p := n.peers[key]
	if p == nil {
		n.mu.Unlock()
		return nil, ErrUntrusted
	}
	if len(n.wires)+len(n.dials) >= n.cfg.FlowLimit {
		n.mu.Unlock()
		return nil, ErrCapacity
	}
	run, cancel := context.WithTimeout(ctx, 10*time.Second)
	pending := &pendingDial{key: key, cancel: cancel}
	n.dials[pending] = struct{}{}
	t := n.tunnel
	n.mu.Unlock()
	defer func() { cancel(); n.mu.Lock(); delete(n.dials, pending); n.mu.Unlock() }()
	target, _ := OverlayAddress(key)
	ap := netip.AddrPortFrom(target, port)
	var raw net.Conn
	var e error
	if network == "tcp" {
		var c *gonet.TCPConn
		c, e = t.dialTCP(run, ap)
		if e == nil {
			raw = c
		}
	} else {
		var c *gonet.UDPConn
		c, e = t.dialUDP(netip.AddrPortFrom(n.OverlayAddr(), 0), ap)
		if e == nil {
			raw = c
		}
	}
	if e != nil {
		return nil, e
	}
	if e = run.Err(); e != nil {
		raw.Close()
		return nil, e
	}
	n.mu.Lock()
	delete(n.dials, pending)
	f, e := n.trackFlowLocked(raw, p, network, false)
	n.mu.Unlock()
	if e != nil {
		raw.Close()
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
func (n *Node) packetPeer(id stack.TransportEndpointID) *peerState {
	p := n.bind.policy.Load()
	if p == nil || addrPort(id.LocalAddress, id.LocalPort).Addr() != n.OverlayAddr() {
		return nil
	}
	return p.generations[addrPort(id.RemoteAddress, id.RemotePort).Addr()]
}
