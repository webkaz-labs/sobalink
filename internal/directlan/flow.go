package directlan

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"
)

type flow struct {
	n             *Node
	g             *runtimeGeneration
	work          *generationWork
	w             *wire
	c             *liveEndpoint
	network       string
	local, remote netip.AddrPort
	inbound       bool
	listener      *listener
	mu            sync.Mutex
	closed        bool
	once          sync.Once

	resourceInspectionCaptured bool // Node.mu; one inspection capability per flow
}

func (n *Node) trackFlowLocked(g *runtimeGeneration, c *liveEndpoint, p *peerState, network string, inbound bool) (*flow, error) {
	if n.readyLocked() != nil || n.generation.Load() != g || !g.open() || p == nil || p.g != g || n.peers[p.peer.Key] != p || !g.applicationPeer(p) {
		return nil, ErrUntrusted
	}
	if n.flowUsageLocked() >= n.cfg.FlowLimit {
		return nil, ErrCapacity
	}
	local, e := netip.ParseAddrPort(c.LocalAddr().String())
	if e != nil {
		return nil, e
	}
	remote, e := netip.ParseAddrPort(c.RemoteAddr().String())
	if e != nil {
		return nil, e
	}
	work, e := g.acquireWork(nil, false)
	if e != nil {
		return nil, e
	}
	w := &wire{raw: c, key: p.peer.Key, peer: p, g: g}
	f := &flow{n: n, g: g, work: work, w: w, c: c, network: network, local: local, remote: remote, inbound: inbound}
	w.flow = f
	n.wires[w] = struct{}{}
	if inbound {
		if n.flows[remote] == nil {
			n.flows[remote] = map[*flow]struct{}{}
		}
		n.flows[remote][f] = struct{}{}
	}
	return f, nil
}
func (n *Node) validFlowLocked(f *flow) bool {
	_, present := n.wires[f.w]
	if !f.c.valid() {
		return false
	}
	return !n.closed && !n.recovery && f.g == n.generation.Load() && f.g.open() && f.g.applicationPeer(f.w.peer) && present && n.peers[f.w.key] == f.w.peer && (!f.inbound || flowPresent(n.flows[f.remote], f))
}
func (f *flow) Valid() bool {
	work, e := f.g.acquireWork(nil, false)
	if e != nil {
		return false
	}
	defer work.finish()
	f.n.mu.Lock()
	defer f.n.mu.Unlock()
	return f.n.validFlowLocked(f)
}

// borrow pins current peer authority and the old endpoint under Node.mu ->
// WG admission -> generation. Revocation cannot slip between validation and
// registering the borrowed call. No I/O runs under those locks.
func (f *flow) borrow(deadline bool) (net.Conn, error) {
	f.n.mu.Lock()
	defer f.n.mu.Unlock()
	if !f.n.validFlowLocked(f) {
		return nil, net.ErrClosed
	}
	raw, ok := f.c.borrow(deadline)
	if !ok {
		return nil, net.ErrClosed
	}
	return raw, nil
}
func (f *flow) Read(b []byte) (int, error) {
	raw, e := f.borrow(false)
	if e != nil {
		return 0, e
	}
	defer f.c.release(false)
	n, e := raw.Read(b)
	if !f.Valid() {
		clear(b[:n])
		return 0, net.ErrClosed
	}
	return n, e
}
func (f *flow) Write(b []byte) (int, error) {
	if f.network == "udp" && len(b) > MaxDatagram {
		return 0, errFrame
	}
	raw, e := f.borrow(false)
	if e != nil {
		return 0, e
	}
	defer f.c.release(false)
	return raw.Write(b)
}
func (f *flow) Close() error {
	var e error
	f.once.Do(func() {
		defer f.work.finish()
		f.mu.Lock()
		f.closed = true
		listener := f.listener
		f.mu.Unlock()
		e = f.c.Close()
		f.n.mu.Lock()
		delete(f.n.wires, f.w)
		if f.inbound && flowPresent(f.n.flows[f.remote], f) {
			delete(f.n.flows[f.remote], f)
			if len(f.n.flows[f.remote]) == 0 {
				delete(f.n.flows, f.remote)
			}
		}
		f.n.mu.Unlock()
		if listener != nil {
			listener.mu.Lock()
			delete(listener.flows, f)
			listener.mu.Unlock()
		}
	})
	return e
}
func (f *flow) CloseWrite() error {
	if f.network != "tcp" {
		return errors.New("half-close is available only for TCP streams")
	}
	raw, e := f.borrow(false)
	if e != nil {
		return e
	}
	defer f.c.release(false)
	c, ok := raw.(interface{ CloseWrite() error })
	if !ok {
		return ErrUnavailable
	}
	return c.CloseWrite()
}
func (f *flow) LocalAddr() net.Addr  { return f.c.LocalAddr() }
func (f *flow) RemoteAddr() net.Addr { return f.c.RemoteAddr() }
func (f *flow) SetDeadline(t time.Time) error {
	raw, e := f.borrow(true)
	if e != nil {
		return e
	}
	defer f.c.release(true)
	return raw.SetDeadline(t)
}
func (f *flow) SetReadDeadline(t time.Time) error {
	raw, e := f.borrow(true)
	if e != nil {
		return e
	}
	defer f.c.release(true)
	return raw.SetReadDeadline(t)
}
func (f *flow) SetWriteDeadline(t time.Time) error {
	raw, e := f.borrow(true)
	if e != nil {
		return e
	}
	defer f.c.release(true)
	return raw.SetWriteDeadline(t)
}
func (f *flow) ReadFrom(b []byte) (int, net.Addr, error) {
	if f.network != "udp" {
		return 0, nil, ErrUnavailable
	}
	n, e := f.Read(b)
	return n, f.RemoteAddr(), e
}
func (f *flow) WriteTo(b []byte, a net.Addr) (int, error) {
	if f.network != "udp" || a == nil || a.String() != f.RemoteAddr().String() {
		return 0, ErrUntrusted
	}
	return f.Write(b)
}

var _ ConnPacketConn = (*flow)(nil)

func flowPresent(flows map[*flow]struct{}, f *flow) bool { _, ok := flows[f]; return ok }

// PeerIdentity exposes the cryptographic identity pinned to this connection,
// rather than treating an address or route snapshot as authentication evidence.
// TCP completion and incoming packets are carried by the paired WireGuard key.
// It is invalid immediately on revocation, recovery or connection retirement.
func (f *flow) PeerIdentity() (string, bool) {
	work, e := f.g.acquireWork(nil, false)
	if e != nil {
		return "", false
	}
	defer work.finish()
	f.n.mu.Lock()
	defer f.n.mu.Unlock()
	if !f.n.validFlowLocked(f) {
		return "", false
	}
	return f.w.key, true
}
