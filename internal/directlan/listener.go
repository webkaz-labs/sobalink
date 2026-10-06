package directlan

import (
	"context"
	"net"
	"net/netip"
	"sync"
)

type listener struct {
	n       *Node
	service service
	pending chan *flow
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	flows   map[*flow]struct{}
	stop    func() bool
}

func (n *Node) ListenPeer(ctx context.Context, network string, port uint16) (net.Listener, error) {
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
		return nil, ErrUnavailable
	}
	if len(n.listeners) >= n.cfg.ListenerLimit {
		return nil, ErrCapacity
	}
	l := &listener{n: n, service: s, pending: make(chan *flow, n.cfg.PacketQueueLimit), done: make(chan struct{}), flows: map[*flow]struct{}{}}
	n.listeners[s] = l
	l.mu.Lock()
	l.stop = context.AfterFunc(ctx, func() { l.Close() })
	l.mu.Unlock()
	return l, nil
}
func (l *listener) deliver(f *flow) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.done:
		return false
	default:
	}
	f.listener = l
	l.flows[f] = struct{}{}
	select {
	case l.pending <- f:
		return true
	default:
		delete(l.flows, f)
		f.listener = nil
		return false
	}
}
func (l *listener) Accept() (net.Conn, error) {
	for {
		select {
		case <-l.done:
			return nil, net.ErrClosed
		case f := <-l.pending:
			select {
			case <-l.done:
				f.Close()
				return nil, net.ErrClosed
			default:
			}
			if f.Valid() {
				return f, nil
			}
			f.Close()
		}
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
