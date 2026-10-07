package directlan

import (
	"net"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

// liveEndpoint is the sole physical close/abort owner. Public Close signals it;
// every endpoint-touching call borrows through the generation's terminal gate.
// All fields other than immutable channels/g are protected by g.mu.
type liveEndpoint struct {
	g                        *runtimeGeneration
	ep                       tcpip.Endpoint
	raw                      net.Conn
	local, remote            net.Addr
	attached                 chan struct{}
	closeRequested           chan struct{}
	done                     chan struct{}
	closing                  bool
	borrows, deadlineSetters int
}

func newLiveEndpoint(g *runtimeGeneration, ep tcpip.Endpoint) *liveEndpoint {
	return &liveEndpoint{g: g, ep: ep, attached: make(chan struct{}), closeRequested: make(chan struct{}), done: make(chan struct{})}
}
func (e *liveEndpoint) attach(raw net.Conn) {
	// The creator retains its ticket until this handoff's wrapper is installed.
	// The supervisor cannot mutate the endpoint until attached is closed.
	local, remote := raw.LocalAddr(), raw.RemoteAddr()
	e.g.mu.Lock()
	e.raw, e.local, e.remote = raw, local, remote
	close(e.attached)
	e.g.mu.Unlock()
}
func (e *liveEndpoint) requestClose() {
	e.g.mu.Lock()
	if !e.closing {
		e.closing = true
		close(e.closeRequested)
		e.g.changedLocked()
	}
	e.g.mu.Unlock()
}
func (e *liveEndpoint) borrow(deadline bool) (net.Conn, bool) {
	var raw net.Conn
	ok := e.g.admit(func() bool {
		if e.closing || e.raw == nil {
			return false
		}
		e.borrows++
		if deadline {
			e.deadlineSetters++
		}
		raw = e.raw
		return true
	})
	return raw, ok
}
func (e *liveEndpoint) release(deadline bool) {
	e.g.mu.Lock()
	e.borrows--
	if deadline {
		e.deadlineSetters--
	}
	e.g.changedLocked()
	e.g.mu.Unlock()
}
func (e *liveEndpoint) valid() bool {
	return e.g.admit(func() bool { return !e.closing && e.raw != nil })
}
func (e *liveEndpoint) waitBorrows(deadlineOnly bool) {
	for {
		e.g.mu.Lock()
		count := e.borrows
		if deadlineOnly {
			count = e.deadlineSetters
		}
		changed := e.g.changed
		e.g.mu.Unlock()
		if count == 0 {
			return
		}
		<-changed
	}
}
func (e *liveEndpoint) supervise() {
	select {
	case <-e.closeRequested:
	case <-e.g.stop:
		e.requestClose()
	}
	<-e.attached
	e.waitBorrows(true)
	e.g.mu.Lock()
	raw, ep := e.raw, e.ep
	e.g.mu.Unlock()
	// Late public deadline resets have already been sealed. These private
	// deadline setters wake old reads/writes before their joins.
	_ = raw.SetDeadline(time.Now())
	e.waitBorrows(false)
	ep.Close()
	if transport, ok := ep.(*tcp.Endpoint); ok {
		// This terminal observer does not mutate the endpoint. The one mutator
		// remains here, including a retirement Abort after an earlier normal Close.
		terminal := make(chan struct{})
		go func() { transport.Wait(); close(terminal) }()
		select {
		case <-terminal:
		case <-e.g.stop:
			transport.Abort()
			<-terminal
		}
	}
	e.g.mu.Lock()
	e.raw, e.ep = nil, nil
	close(e.done)
	delete(e.g.live, ep)
	e.g.changedLocked()
	e.g.mu.Unlock()
}
func (e *liveEndpoint) Read(b []byte) (int, error) {
	raw, ok := e.borrow(false)
	if !ok {
		return 0, net.ErrClosed
	}
	defer e.release(false)
	n, err := raw.Read(b)
	if !e.valid() {
		clear(b[:n])
		return 0, net.ErrClosed
	}
	return n, err
}
func (e *liveEndpoint) Write(b []byte) (int, error) {
	raw, ok := e.borrow(false)
	if !ok {
		return 0, net.ErrClosed
	}
	defer e.release(false)
	return raw.Write(b)
}
func (e *liveEndpoint) Close() error { e.requestClose(); return nil }
func (e *liveEndpoint) CloseWrite() error {
	raw, ok := e.borrow(false)
	if !ok {
		return net.ErrClosed
	}
	defer e.release(false)
	c, ok := raw.(interface{ CloseWrite() error })
	if !ok {
		return ErrUnavailable
	}
	return c.CloseWrite()
}
func (e *liveEndpoint) LocalAddr() net.Addr  { e.g.mu.Lock(); defer e.g.mu.Unlock(); return e.local }
func (e *liveEndpoint) RemoteAddr() net.Addr { e.g.mu.Lock(); defer e.g.mu.Unlock(); return e.remote }
func (e *liveEndpoint) SetDeadline(t time.Time) error {
	raw, ok := e.borrow(true)
	if !ok {
		return net.ErrClosed
	}
	defer e.release(true)
	return raw.SetDeadline(t)
}
func (e *liveEndpoint) SetReadDeadline(t time.Time) error {
	raw, ok := e.borrow(true)
	if !ok {
		return net.ErrClosed
	}
	defer e.release(true)
	return raw.SetReadDeadline(t)
}
func (e *liveEndpoint) SetWriteDeadline(t time.Time) error {
	raw, ok := e.borrow(true)
	if !ok {
		return net.ErrClosed
	}
	defer e.release(true)
	return raw.SetWriteDeadline(t)
}

var _ net.Conn = (*liveEndpoint)(nil)
