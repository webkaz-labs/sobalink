package directlan

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"
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
	// The accept loop owns at most one rejected socket outside admitted work.
	// A failed rejection close stops acceptance and remains retained here.
	rejected     net.Conn
	rejectionErr error
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
	return errors.Join(u.closeErr, u.rejectionErr)
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
			if raw != nil {
				n.rejectAcceptedContext(g, u, raw)
			}
			return
		}
		ap, err := netip.ParseAddrPort(raw.RemoteAddr().String())
		if err != nil || !g.cfg.permits(ap, false) {
			if !n.rejectAcceptedContext(g, u, raw) {
				return
			}
			continue
		}
		n.mu.Lock()
		if n.startReadyLocked() != nil || n.generation.Load() != g || n.controlUsageLocked() >= g.cfg.ControlLimit {
			n.mu.Unlock()
			if !n.rejectAcceptedContext(g, u, raw) {
				return
			}
			continue
		}
		work, err := g.acquireWork(nil, true)
		if err != nil {
			n.mu.Unlock()
			if !n.rejectAcceptedContext(g, u, raw) {
				return
			}
			continue
		}
		w := &wire{raw: newControlStream(g, raw), control: true, g: g, work: work}
		if n.contextControl || g.cfg.protectedPairs() {
			w.contextDeadline = time.Now().Add(handshakeTimeout)
			w.contextArmCutoff = n.contextArmRevision
		}
		n.wires[w] = struct{}{}
		n.wg.Add(1)
		n.mu.Unlock()
		go func() {
			defer n.wg.Done()
			if n.contextControl {
				n.handleContext(w)
			} else {
				n.handle(w)
			}
		}()
	}
}

// The listener's one rejection slot bounds cleanup even when ControlLimit is
// already exhausted. It never becomes an authenticated work admission. A failed
// close seals the owner immediately; no further raw socket can accumulate.
func (n *Node) rejectAcceptedContext(g *runtimeGeneration, u *generationUnderlay, raw net.Conn) bool {
	if !n.contextControl && !g.cfg.protectedPairs() {
		_ = raw.Close()
		return true
	}
	u.rejected = raw
	// Even an unidentified rejected socket belongs to this managed owner.
	// Its retained close failure consumes a control slot until owner disposal.
	if err := closeUnadmittedControl(g, raw, nil); err != nil {
		u.rejectionErr = err
		u.RequestClose()
		return false
	}
	u.rejected = nil
	return true
}
