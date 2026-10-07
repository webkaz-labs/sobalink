package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/connectionroute"
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// mixedConn owns raw from authentication through physical close, queue removal,
// every admitted call and exact alias removal. A retained old wrapper keeps only
// inert address/origin/terminal evidence after cleanup; it cannot look up a new
// connection through a reused numeric alias.
type mixedConn struct {
	mu           sync.Mutex
	raw          net.Conn
	owner        *mixedBackend
	origin       transportorigin.Origin
	lease        transportorigin.Lease
	ctx          context.Context
	alias        netip.AddrPort
	local        net.Addr
	backend      string
	identity     string
	logical      string
	peer         *mixedTCPPeer
	published    bool
	closing      bool
	borrows      int
	changed      chan struct{}
	stop         chan struct{}
	physicalDone chan struct{}
	queueDone    <-chan struct{}
	done         chan struct{}
}

func (c *mixedConn) openLocked() bool {
	if c.closing || c.raw == nil || c.ctx == nil || c.ctx.Err() != nil {
		return false
	}
	if c.origin != nil {
		select {
		case <-c.origin.StopRequested():
			return false
		default:
		}
	}
	return true
}
func (c *mixedConn) borrow() (net.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.published || !c.openLocked() {
		return nil, net.ErrClosed
	}
	c.borrows++
	return c.raw, nil
}
func (c *mixedConn) returned() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	valid := c.openLocked()
	c.borrows--
	close(c.changed)
	c.changed = make(chan struct{})
	return valid
}

// HoldConnection keeps the old alias assigned to this exact gate until the
// enclosing session/request has joined all callbacks that can use that alias.
// Closing still rejects identity/I/O immediately and starts physical cleanup.
func (c *mixedConn) HoldConnection() (func(), error) {
	if _, err := c.borrow(); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { c.returned() }) }, nil
}

func (c *mixedConn) Close() error {
	c.mu.Lock()
	if !c.closing {
		c.closing = true
		close(c.stop)
	}
	c.mu.Unlock()
	return nil
}
func (c *mixedConn) WaitClosed(ctx context.Context) error {
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *mixedConn) TransportOrigin() transportorigin.Origin { return c.origin }
func (c *mixedConn) LocalAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.local
}
func (c *mixedConn) RemoteAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	return net.TCPAddrFromAddrPort(c.alias)
}
func (c *mixedConn) Read(buf []byte) (int, error) {
	raw, err := c.borrow()
	if err != nil {
		return 0, err
	}
	n, err := raw.Read(buf)
	if !c.returned() {
		clear(buf[:n])
		return 0, net.ErrClosed
	}
	return n, err
}
func (c *mixedConn) Write(buf []byte) (int, error) {
	raw, err := c.borrow()
	if err != nil {
		return 0, err
	}
	n, err := raw.Write(buf)
	if !c.returned() {
		return n, net.ErrClosed
	}
	return n, err
}
func (c *mixedConn) CloseWrite() error {
	raw, err := c.borrow()
	if err != nil {
		return err
	}
	if half, ok := raw.(interface{ CloseWrite() error }); ok {
		err = half.CloseWrite()
	} else {
		err = errors.ErrUnsupported
	}
	if !c.returned() {
		return net.ErrClosed
	}
	return err
}
func (c *mixedConn) SetDeadline(t time.Time) error      { return c.deadline(t, 0) }
func (c *mixedConn) SetReadDeadline(t time.Time) error  { return c.deadline(t, 1) }
func (c *mixedConn) SetWriteDeadline(t time.Time) error { return c.deadline(t, 2) }
func (c *mixedConn) deadline(t time.Time, which int) error {
	raw, err := c.borrow()
	if err != nil {
		return err
	}
	switch which {
	case 0:
		err = raw.SetDeadline(t)
	case 1:
		err = raw.SetReadDeadline(t)
	case 2:
		err = raw.SetWriteDeadline(t)
	}
	if !c.returned() {
		return net.ErrClosed
	}
	return err
}

// The registry captures this exact gate, never a raw peer or an alias lookup.
// Its result is the backend identity expected by mixedBackend.WhoIs.
type mixedTCPPeer struct{ conn *mixedConn }

func (p *mixedTCPPeer) PeerIdentity() (string, bool) {
	id, err := p.conn.peerIdentity(context.Background(), true)
	return id, err == nil
}
func (c *mixedConn) AuthenticatedPeer(ctx context.Context) (string, error) {
	return c.peerIdentity(ctx, false)
}
func (c *mixedConn) peerIdentity(ctx context.Context, backendIdentity bool) (string, error) {
	raw, err := c.borrow()
	if err != nil {
		return "", err
	}
	// The borrow keeps owner and all immutable identity evidence attached.
	owner := c.owner
	identityCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	id, err := mixedTCPIdentity(identityCtx, owner.nodes[c.backend], raw, c.origin)
	if err == nil {
		err = identityCtx.Err()
	}
	cancel()
	owner.mu.Lock()
	closed := owner.closed
	owner.mu.Unlock()
	if err == nil && (closed || id != c.identity || owner.logical(c.backend, id) != c.logical) {
		err = connectionroute.ErrDenied
	}
	logical := c.logical
	if !c.returned() {
		return "", net.ErrClosed
	}
	if err != nil {
		return "", err
	}
	if backendIdentity {
		return id, nil
	}
	return logical, nil
}

func mixedTCPIdentity(ctx context.Context, node NetworkBackend, raw net.Conn, origin transportorigin.Origin) (string, error) {
	if origin == nil {
		return observedBackendPeer(ctx, node, raw)
	}
	// Captured origin-bearing evidence cannot fall back to a current node.
	if peer, ok := raw.(interface{ PeerIdentity() (string, bool) }); ok {
		if id, valid := peer.PeerIdentity(); valid && id != "" {
			return id, nil
		}
	}
	return "", connectionroute.ErrDenied
}

func (c *mixedConn) own() {
	raw := c.raw
	terminal, hasTerminal := raw.(interface{ WaitClosed(context.Context) error })
	var natural chan error
	// Non-generation compatibility wrappers may acknowledge WaitClosed before
	// Close, so only an origin-bearing endpoint supplies natural-close evidence.
	if c.origin != nil && hasTerminal {
		natural = make(chan error, 1)
		go func() { natural <- terminal.WaitClosed(context.Background()) }()
	}
	var stopped <-chan struct{}
	if c.origin != nil {
		stopped = c.origin.StopRequested()
	}
	var terminalErr error
	observed := false
	select {
	case <-c.stop:
	case <-c.ctx.Done():
	case <-stopped:
	case terminalErr = <-natural:
		observed = true
	}
	_ = c.Close()
	closeErr := raw.Close()
	if natural != nil {
		if !observed {
			terminalErr = <-natural
		}
	} else if hasTerminal {
		terminalErr = terminal.WaitClosed(context.Background())
	} else if c.origin != nil || (closeErr != nil && !errors.Is(closeErr, net.ErrClosed)) {
		return // no positive terminal evidence: keep the owner and its charge
	}
	if terminalErr != nil {
		return
	}
	c.mu.Lock()
	for c.borrows != 0 {
		changed := c.changed
		c.mu.Unlock()
		<-changed
		c.mu.Lock()
	}
	queueDone := c.queueDone
	c.mu.Unlock()
	close(c.physicalDone)
	if queueDone != nil {
		<-queueDone // queue capacity and references detach before lease release
	}
	owner := c.owner
	owner.mu.Lock()
	if c.published {
		if source := owner.sources[c.alias]; source.capturedPeer == c.peer {
			delete(owner.sources, c.alias)
		}
	} else {
		delete(owner.tcpPending, c)
	}
	owner.mu.Unlock()
	c.mu.Lock()
	lease := c.lease
	c.raw, c.owner, c.lease, c.ctx, c.peer = nil, nil, nil, nil, nil
	c.mu.Unlock()
	if lease != nil {
		lease.Release()
	}
	close(c.done)
}

// A rejected raw accept is still charged to its underlying listener/flow and
// this accepting goroutine. Join it synchronously before accepting another;
// lack of terminal evidence never permits an unbounded chain of close workers.
func closeMixedUnadmitted(raw net.Conn, origin transportorigin.Origin) {
	err := raw.Close()
	if terminal, ok := raw.(interface{ WaitClosed(context.Context) error }); ok {
		if terminal.WaitClosed(context.Background()) == nil {
			return
		}
	} else if origin == nil && (err == nil || errors.Is(err, net.ErrClosed)) {
		return
	}
	<-make(chan struct{}) // unverified cleanup keeps the exact ingress owner
	runtime.KeepAlive(raw)
}

const mixedTCPQueueLimit = 16

type mixedAccept struct {
	conn      *mixedConn
	origin    *transportorigin.Token
	handedOff bool
	retired   bool
	handoff   chan struct{}
	detached  chan struct{}
}

func (l *mixedListener) signalLocked() {
	close(l.wake)
	l.wake = make(chan struct{})
}
func (l *mixedListener) enqueue(c *mixedConn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if l.closed || len(l.owned) >= mixedTCPQueueLimit || !c.openLocked() {
		return false
	}
	entry := &mixedAccept{conn: c, handoff: make(chan struct{}), detached: make(chan struct{})}
	if c.origin != nil {
		entry.origin = c.origin.Identity()
	}
	c.queueDone = entry.detached
	l.owned[c] = entry
	l.pending = append(l.pending, entry)
	l.wg.Add(1)
	go l.ownQueued(entry)
	l.signalLocked()
	return true
}
func (l *mixedListener) retireLocked(entry *mixedAccept) {
	if entry.handedOff || entry.retired {
		return
	}
	entry.retired = true
	kept := l.pending[:0]
	for _, queued := range l.pending {
		if queued != entry {
			kept = append(kept, queued)
		}
	}
	clear(l.pending[len(kept):])
	l.pending = kept
	_ = entry.conn.Close()
	l.signalLocked()
}
func (l *mixedListener) ownQueued(entry *mixedAccept) {
	defer l.wg.Done()
	select {
	case <-entry.handoff:
		close(entry.detached)
		return
	case <-entry.conn.stop:
	}
	l.mu.Lock()
	if entry.handedOff {
		l.mu.Unlock()
		close(entry.detached)
		return
	}
	retiringOrigin := false
	if entry.origin != nil {
		select {
		case <-entry.conn.origin.StopRequested():
			retiringOrigin = true
		default:
		}
	}
	if retiringOrigin {
		for _, owned := range l.owned {
			if owned.origin == entry.origin {
				l.retireLocked(owned)
			}
		}
	} else {
		l.retireLocked(entry)
	}
	l.mu.Unlock()
	<-entry.conn.physicalDone
	l.mu.Lock()
	if l.owned[entry.conn] == entry {
		delete(l.owned, entry.conn)
	}
	l.mu.Unlock()
	close(entry.detached)
	<-entry.conn.done // Close joins alias detachment and lease release as well
}

func (l *mixedListener) Accept() (net.Conn, error) {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return nil, net.ErrClosed
		}
		if len(l.pending) == 0 {
			wake := l.wake
			l.mu.Unlock()
			select {
			case <-l.done:
			case <-wake:
			}
			continue
		}
		entry := l.pending[0]
		copy(l.pending, l.pending[1:])
		l.pending[len(l.pending)-1] = nil
		l.pending = l.pending[:len(l.pending)-1]
		c := entry.conn
		if _, err := c.borrow(); err != nil {
			l.retireLocked(entry)
			l.mu.Unlock()
			continue
		}
		l.wg.Add(1) // dequeued handoff remains owned through the return boundary
		l.mu.Unlock()
		conn, err := l.handoff(entry)
		if err == nil {
			defer l.wg.Done()
			defer c.returned()
			defer l.finishHandoff(entry)
			return conn, nil
		}
		_ = c.Close()
		c.returned()
		l.wg.Done()
	}
}
func (l *mixedListener) handoff(entry *mixedAccept) (net.Conn, error) {
	c := entry.conn
	if _, err := c.AuthenticatedPeer(l.owner.ctx); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if l.closed || entry.retired || l.owned[c] != entry || !c.openLocked() {
		return nil, net.ErrClosed
	}
	var publication transportorigin.Lease
	if c.origin != nil {
		var err error
		publication, err = c.origin.AcquirePublication(c.ctx)
		if err != nil {
			return nil, err
		}
		defer publication.Release()
	}
	// Publication is the bounded ownership transfer. Stop either wins first or
	// observes a committed old-origin connection whose return is still counted.
	entry.handedOff = true
	return c, nil
}
func (l *mixedListener) finishHandoff(entry *mixedAccept) {
	l.mu.Lock()
	delete(l.owned, entry.conn)
	close(entry.handoff)
	l.mu.Unlock()
}

var _ transportorigin.ConnectionLifetime = (*mixedConn)(nil)
