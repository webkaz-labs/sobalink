package core

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// The peer server needs a gate which preserves the actual connection origin.
// An embedded net.Conn alone erases that capability. Both listeners share the
// existing sixteen physical slots; Close does not release a slot until the
// physical close acknowledgement and all admitted connection borrows finish.
type peerIncomingListener struct {
	net.Listener
	core          *Core
	slots         chan struct{}
	requireOrigin bool
}

func (l *peerIncomingListener) Accept() (net.Conn, error) {
	for {
		raw, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
		default:
			// The accepting worker retains this rejected connection until its
			// real cleanup. It cannot launch an unbounded tail of close workers.
			closeRejectedPeerIncoming(raw)
			continue
		}
		conn, err := l.core.ownPeerIncomingConn(raw, l.requireOrigin, func() { <-l.slots })
		if err != nil {
			// Failed adoption has its own retained physical cleanup owner.
			continue
		}
		return conn, nil
	}
}

func closeRejectedPeerIncoming(raw net.Conn) {
	_ = raw.Close()
	if terminal, ok := raw.(transportorigin.TerminalConnection); ok {
		if terminal.WaitClosed(context.Background()) == nil {
			return
		}
		// Failed observation is not completion. Keep this accepting worker
		// and its one rejected connection charged rather than accepting more.
		select {}
	}
	if carrier, ok := raw.(transportorigin.Carrier); ok && carrier.TransportOrigin() != nil {
		select {} // no terminal acknowledgement for a generation-owned stream
	}
}

type peerIncomingConn struct {
	*peerHTTPConn
	mu        sync.Mutex
	origin    transportorigin.Origin
	authority *peerHTTPAuthority
}

func (c *Core) ownPeerIncomingConn(raw net.Conn, requireOrigin bool, release func()) (*peerIncomingConn, error) {
	var origin transportorigin.Origin
	if carrier, ok := raw.(transportorigin.Carrier); ok {
		origin = carrier.TransportOrigin()
	}
	workDone, err := c.beginWork()
	if err != nil {
		closeRejectedPeerIncoming(raw)
		release()
		return nil, err
	}
	var lease transportorigin.Lease
	var stopped <-chan struct{}
	if requireOrigin && origin == nil {
		err = transportorigin.ErrMissingOrigin
	}
	if origin != nil {
		stopped = origin.StopRequested()
		if origin.Identity() == nil {
			err = transportorigin.ErrMissingOrigin
		} else {
			lease, err = origin.Acquire(c.ctx)
		}
	}
	authority := &peerHTTPAuthority{front: c.ctx.Done(), origin: stopped}
	conn := &peerIncomingConn{origin: origin, authority: authority}
	// The socket owner's acknowledgement wakes the enclosing participant.
	// Only that participant releases the shared slot, after detachment and
	// lease release, so delayed cleanup workers cannot accumulate behind it.
	conn.peerHTTPConn = ownPeerHTTPConn(authority, raw, origin != nil || requireOrigin, func() {})
	if err != nil {
		_ = conn.Close()
	}
	go func() {
		select {
		case <-authority.front:
		case <-authority.origin:
		case <-conn.done:
		}
		_ = conn.Close()
		<-conn.done
		conn.mu.Lock()
		conn.origin, conn.authority = nil, nil
		conn.mu.Unlock()
		if lease != nil {
			lease.Release()
		}
		workDone()
		release()
	}()
	return conn, err
}

func (c *peerIncomingConn) TransportOrigin() transportorigin.Origin {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.origin
}

func (c *peerIncomingConn) WaitClosed(ctx context.Context) error {
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// holdConnection pins both this library wrapper and any exact identity owner
// beneath it. The request releases the hold only after its last handler/body/
// response callback; neither raw WaitClosed nor this wrapper's done may precede
// that release, so request cleanup must not wait on either terminal channel.
func (c *peerIncomingConn) holdConnection() (func(), error) {
	raw, err := c.peerHTTPConn.borrow()
	if err != nil {
		return nil, err
	}
	var inner func()
	if lifetime, ok := raw.(transportorigin.ConnectionLifetime); ok {
		inner, err = lifetime.HoldConnection()
		if err == nil && inner == nil {
			err = transportorigin.ErrMissingOrigin
		}
		if err != nil {
			if inner != nil {
				inner()
			}
			c.peerHTTPConn.returned()
			return nil, err
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if inner != nil {
				inner()
			}
			c.peerHTTPConn.returned()
		})
	}, nil
}

type peerIncomingRequestKey struct{}

// A new request must enter the origin gate even if net/http has already read
// all its bytes into a private buffer. The request participant outlives the
// handler, raw body Close, response borrows and its cancellation worker.
type peerIncomingRequest struct {
	mu        sync.Mutex
	origin    transportorigin.Origin
	authority *peerHTTPAuthority
	body      *peerHTTPBody
	response  *peerIncomingResponse
}

func (c *Core) serveOwnedPeerHTTP(p *peerServer, w http.ResponseWriter, r *http.Request) {
	conn, ok := r.Context().Value(connectionKey{}).(*peerIncomingConn)
	if !ok {
		peerFailure(w, http.StatusServiceUnavailable)
		return
	}
	select {
	case p.slots <- struct{}{}:
		// Keep the handler slot through body/writer cleanup, even if the
		// physical connection completes before the handler has returned.
		defer func() { <-p.slots }()
	default:
		// No application scope was admitted. This bounded fixed response and
		// net/http's residual draining use only the owned connection wrapper.
		w.Header().Set("Connection", "close")
		peerFailure(w, http.StatusServiceUnavailable)
		return
	}
	workDone, err := c.beginWork()
	if err != nil {
		_ = conn.Close()
		return
	}
	defer workDone()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	identityHold, err := conn.holdConnection()
	if err != nil {
		_ = conn.Close()
		return
	}
	// Registered before the request cleanup defer below: every handler, body,
	// response and cancellation worker finishes before the exact alias hold.
	defer identityHold()
	conn.mu.Lock()
	origin := conn.origin
	var lease transportorigin.Lease
	if !conn.authority.open() || channelClosed(conn.done) {
		err = net.ErrClosed
	} else if origin != nil {
		lease, err = origin.Acquire(ctx)
	}
	conn.mu.Unlock()
	if err != nil {
		_ = conn.Close()
		return
	}
	var stopped <-chan struct{}
	if origin != nil {
		stopped = origin.StopRequested()
	}
	authority := &peerHTTPAuthority{caller: ctx.Done(), front: c.ctx.Done(), origin: stopped}
	h := &peerIncomingRequest{origin: origin, authority: authority}
	h.body = ownPeerHTTPBody(authority, r.Body, func() {})
	h.response = newPeerIncomingResponse(authority, w)
	returned, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-authority.caller:
		case <-authority.front:
		case <-authority.origin:
		case <-returned:
			return
		}
		cancel()
		_ = conn.Close() // wake network I/O before the handler/body joins
	}()
	defer func() {
		h.response.finish()
		_ = h.body.Close()
		close(returned)
		<-joined
		cancel()
		h.mu.Lock()
		h.origin, h.authority, h.body, h.response = nil, nil, nil, nil
		h.mu.Unlock()
		if lease != nil {
			lease.Release()
		}
	}()
	r = r.WithContext(context.WithValue(ctx, peerIncomingRequestKey{}, h))
	r.Body = h.body
	c.peerHTTP(p, h.response, r)
}

// Called only after the caller holds Core.mu and has rechecked current trust.
// This is ordinary operation admission, not a publication permit: it owns one
// already validated durable history transaction, including I/O and final
// reconciliation after cancellation. No origin/generation mutex spans I/O.
func incomingPeerMessageCommit(ctx context.Context) (func(), error) {
	h, ok := ctx.Value(peerIncomingRequestKey{}).(*peerIncomingRequest)
	if !ok {
		return func() {}, ctx.Err() // non-network direct callers have no origin
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.authority.open() {
		return nil, net.ErrClosed
	}
	if h.origin == nil {
		return func() {}, nil
	}
	lease, err := h.origin.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return lease.Release, nil
}

// The response exposes no Unwrap/Hijack route to a raw writer. Header is a
// private map; writes/deadline changes are counted and forbidden after stop.
// net/http's final buffered flush still uses the independently owned connection.
type peerIncomingResponse struct {
	mu        sync.Mutex
	changed   *sync.Cond
	raw       http.ResponseWriter
	authority *peerHTTPAuthority
	header    http.Header
	started   bool
	closing   bool
	borrows   int
}

func newPeerIncomingResponse(authority *peerHTTPAuthority, raw http.ResponseWriter) *peerIncomingResponse {
	w := &peerIncomingResponse{raw: raw, authority: authority, header: raw.Header().Clone()}
	w.changed = sync.NewCond(&w.mu)
	return w
}

func (w *peerIncomingResponse) Header() http.Header { return w.header }

func (w *peerIncomingResponse) borrow(headers bool) (http.ResponseWriter, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing || w.raw == nil || !w.authority.open() {
		return nil, net.ErrClosed
	}
	if headers && !w.started {
		for key, values := range w.header {
			w.raw.Header()[key] = append([]string(nil), values...)
		}
		w.started = true
	}
	w.borrows++
	return w.raw, nil
}

func (w *peerIncomingResponse) returned() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	valid := !w.closing && w.authority.open()
	w.borrows--
	if w.borrows == 0 {
		w.changed.Broadcast()
	}
	return valid
}

func (w *peerIncomingResponse) WriteHeader(status int) {
	raw, err := w.borrow(true)
	if err == nil {
		defer w.returned()
		raw.WriteHeader(status)
	}
}

func (w *peerIncomingResponse) Write(p []byte) (int, error) {
	raw, err := w.borrow(true)
	if err != nil {
		return 0, err
	}
	n, err := raw.Write(p)
	if !w.returned() {
		return 0, net.ErrClosed
	}
	return n, err
}

func (w *peerIncomingResponse) SetReadDeadline(t time.Time) error { return w.deadline(t, true) }
func (w *peerIncomingResponse) SetWriteDeadline(t time.Time) error {
	return w.deadline(t, false)
}
func (w *peerIncomingResponse) deadline(t time.Time, read bool) error {
	raw, err := w.borrow(false)
	if err != nil {
		return err
	}
	controller := http.NewResponseController(raw)
	if read {
		err = controller.SetReadDeadline(t)
	} else {
		err = controller.SetWriteDeadline(t)
	}
	if !w.returned() {
		return net.ErrClosed
	}
	return err
}

func (w *peerIncomingResponse) finish() {
	w.mu.Lock()
	w.closing = true
	for w.borrows != 0 {
		w.changed.Wait()
	}
	w.raw, w.authority = nil, nil
	w.mu.Unlock()
}

var _ transportorigin.TerminalConnection = (*peerIncomingConn)(nil)
