package directlan

import (
	"context"
	"net"
	"net/netip"
	"sync"
)

type listener struct {
	n            *Node
	service      service
	pending      []*flow
	pendingLimit int
	changed      chan struct{}
	done         chan struct{}
	once         sync.Once
	mu           sync.Mutex
	flows        map[*flow]struct{}
	stop         func() bool
	packet       *packetListener
}

func (n *Node) ListenPeer(ctx context.Context, network string, port uint16) (net.Listener, error) {
	return n.listenPeer(ctx, network, port, ErrUnavailable)
}

func (n *Node) listenPeer(ctx context.Context, network string, port uint16, conflict error) (net.Listener, error) {
	if !validService(network, port) {
		return nil, ErrUnavailable
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if e := n.readyLocked(); e != nil {
		return nil, e
	}
	s := service{network, port}
	if n.listeners[s] != nil {
		return nil, conflict
	}
	if len(n.listeners) >= n.cfg.ListenerLimit {
		return nil, ErrCapacity
	}
	l := &listener{n: n, service: s, pendingLimit: n.cfg.PacketQueueLimit, changed: make(chan struct{}), done: make(chan struct{}), flows: map[*flow]struct{}{}}
	n.listeners[s] = l
	l.mu.Lock()
	l.stop = context.AfterFunc(ctx, func() { l.Close() })
	l.mu.Unlock()
	return l, nil
}
func (l *listener) notifyLocked() { close(l.changed); l.changed = make(chan struct{}) }
func (l *listener) deliver(f *flow) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.done:
		return false
	default:
	}
	if len(l.pending) >= l.pendingLimit {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || !f.g.open() {
		return false
	}
	f.listener = l
	l.flows[f] = struct{}{}
	l.pending = append(l.pending, f)
	l.notifyLocked()
	return true
}
func (l *listener) Accept() (net.Conn, error) {
	for {
		l.mu.Lock()
		select {
		case <-l.done:
			l.mu.Unlock()
			return nil, net.ErrClosed
		default:
		}
		if len(l.pending) != 0 {
			f := l.pending[0]
			l.pending[0] = nil
			l.pending = l.pending[1:]
			work, e := f.g.acquireWork(nil, false)
			l.mu.Unlock()
			if e == nil && f.Valid() {
				defer work.finish()
				return f, nil
			}
			f.Close()
			work.finish()
			continue
		}
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-l.done:
			return nil, net.ErrClosed
		case <-changed:
		}
	}
}
func (l *listener) retireGeneration(g *runtimeGeneration) {
	l.mu.Lock()
	var retired []*flow
	kept := l.pending[:0]
	for _, f := range l.pending {
		if f.g == g {
			retired = append(retired, f)
		} else {
			kept = append(kept, f)
		}
	}
	clear(l.pending[len(kept):])
	l.pending = kept
	packet := l.packet
	l.notifyLocked()
	l.mu.Unlock()
	if packet != nil {
		packet.retireGeneration(g)
	}
	for _, f := range retired {
		f.Close()
	}
}
func (l *listener) Close() error {
	l.once.Do(func() {
		close(l.done)
		l.n.mu.Lock()
		if l.n.listeners[l.service] == l {
			delete(l.n.listeners, l.service)
		}
		l.n.mu.Unlock()
		l.mu.Lock()
		if l.stop != nil {
			l.stop()
		}
		fs := make([]*flow, 0, len(l.flows))
		for f := range l.flows {
			fs = append(fs, f)
		}
		clear(l.pending)
		l.pending = nil
		l.mu.Unlock()
		for _, f := range fs {
			f.Close()
		}
	})
	return nil
}
func (l *listener) Addr() net.Addr {
	ap := netip.AddrPortFrom(l.n.OverlayAddr(), l.service.port)
	if l.service.network == "udp" {
		return net.UDPAddrFromAddrPort(ap)
	}
	return net.TCPAddrFromAddrPort(ap)
}
