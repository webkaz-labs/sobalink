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
	w             *wire
	c             net.Conn
	network       string
	local, remote netip.AddrPort
	inbound       bool
	listener      *listener
	once          sync.Once
}

func (n *Node) trackFlowLocked(c net.Conn, p *peerState, network string, inbound bool) (*flow, error) {
	if n.readyLocked() != nil || p == nil || n.peers[p.peer.Key] != p {
		return nil, ErrUntrusted
	}
	if len(n.wires)+len(n.dials) >= n.cfg.FlowLimit {
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
	w := &wire{raw: c, key: p.peer.Key, peer: p}
	f := &flow{n: n, w: w, c: c, network: network, local: local, remote: remote, inbound: inbound}
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
	return !n.closed && !n.recovery && present && n.peers[f.w.key] == f.w.peer && (!f.inbound || flowPresent(n.flows[f.remote], f))
}
func (f *flow) Valid() bool { f.n.mu.Lock(); defer f.n.mu.Unlock(); return f.n.validFlowLocked(f) }
func (f *flow) Read(b []byte) (int, error) {
	if !f.Valid() {
		return 0, net.ErrClosed
	}
	n, e := f.c.Read(b)
	if !f.Valid() {
		return 0, net.ErrClosed
	}
	return n, e
}
func (f *flow) Write(b []byte) (int, error) {
	if !f.Valid() {
		return 0, net.ErrClosed
	}
	if f.network == "udp" && len(b) > MaxDatagram {
		return 0, errFrame
	}
	return f.c.Write(b)
}
func (f *flow) Close() error {
	var e error
	f.once.Do(func() {
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
		if f.listener != nil {
			f.listener.mu.Lock()
			delete(f.listener.flows, f)
			f.listener.mu.Unlock()
		}
	})
	return e
}
func (f *flow) CloseWrite() error {
	if f.network != "tcp" {
		return errors.New("half-close is available only for TCP streams")
	}
	if !f.Valid() {
		return net.ErrClosed
	}
	c, ok := f.c.(interface{ CloseWrite() error })
	if !ok {
		return ErrUnavailable
	}
	return c.CloseWrite()
}
func (f *flow) LocalAddr() net.Addr                { return f.c.LocalAddr() }
func (f *flow) RemoteAddr() net.Addr               { return f.c.RemoteAddr() }
func (f *flow) SetDeadline(t time.Time) error      { return f.c.SetDeadline(t) }
func (f *flow) SetReadDeadline(t time.Time) error  { return f.c.SetReadDeadline(t) }
func (f *flow) SetWriteDeadline(t time.Time) error { return f.c.SetWriteDeadline(t) }
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
	f.n.mu.Lock()
	defer f.n.mu.Unlock()
	if !f.n.validFlowLocked(f) {
		return "", false
	}
	return f.w.key, true
}
