package directlan

import (
	"context"
	"net"
	"net/netip"
	"sync"
)

// generationUnderlay owns one exact TCP listener and its accept loop. Closing
// it never closes Node's logical listeners or Core's local service entrances.
type generationUnderlay struct {
	listener   net.Listener
	stop       chan struct{}
	closed     chan struct{}
	accepted   chan struct{}
	stopOnce   sync.Once
	acceptOnce sync.Once
	closeErr   error
}

func newGenerationUnderlay(ln net.Listener) *generationUnderlay {
	u := &generationUnderlay{listener: ln, stop: make(chan struct{}), closed: make(chan struct{}), accepted: make(chan struct{})}
	go func() {
		<-u.stop
		u.closeErr = u.listener.Close()
		close(u.closed)
	}()
	return u
}
func (u *generationUnderlay) start(n *Node, g *runtimeGeneration) {
	u.acceptOnce.Do(func() {
		go func() { defer close(u.accepted); n.accept(g, u) }()
	})
}

// RequestClose is signal-only, including when a constructor has not launched
// the accept loop. The physical close has exactly one pre-registered owner.
func (u *generationUnderlay) RequestClose() {
	u.stopOnce.Do(func() { close(u.stop) })
	u.acceptOnce.Do(func() { close(u.accepted) })
}
func (u *generationUnderlay) WaitClosed(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-u.closed:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-u.accepted:
	}
	return u.closeErr
}
func (n *Node) accept(g *runtimeGeneration, u *generationUnderlay) {
	select {
	case <-u.stop:
		return
	case <-g.published:
	}
	for {
		raw, err := u.listener.Accept()
		if err != nil {
			return
		}
		ap, err := netip.ParseAddrPort(raw.RemoteAddr().String())
		if err != nil || !g.cfg.permits(ap, false) {
			raw.Close()
			continue
		}
		n.mu.Lock()
		if n.readyLocked() != nil || n.generation.Load() != g || !g.trafficOpen() || n.controlUsageLocked() >= g.cfg.ControlLimit {
			n.mu.Unlock()
			raw.Close()
			continue
		}
		work, err := g.acquireWork(nil, true)
		if err != nil {
			n.mu.Unlock()
			raw.Close()
			continue
		}
		w := &wire{raw: newControlStream(g, raw), control: true, g: g, work: work}
		n.wires[w] = struct{}{}
		n.wg.Add(1)
		n.mu.Unlock()
		go func() { defer n.wg.Done(); n.handle(w) }()
	}
}
