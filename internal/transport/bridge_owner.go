package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// Session owns one finite admitted bridge or UDP association, independently of
// its retained listener. Finish is called by the enclosing worker only after
// its copies, validation callbacks and queued/in-flight deliveries have joined.
// An origin is bound once, before an origin-selected dial or inbound callback.
type Session struct {
	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
	origin       transportorigin.Origin
	lease        transportorigin.Lease
	originDone   chan struct{}
	closing      bool
	finishing    bool
	releases     []func()
	connHolds    []func()
	conns        []*bridgeConn
	associations []transportorigin.PacketAssociation
	stopped      chan struct{}
	done         chan struct{}
	finish       sync.Once
}
type sessionContextKey struct{}

func NewSession(ctx context.Context) *Session {
	run, cancel := context.WithCancel(ctx)
	s := &Session{cancel: cancel, stopped: make(chan struct{}), done: make(chan struct{})}
	s.ctx = transportorigin.WithBinder(context.WithValue(run, sessionContextKey{}, s), s)
	go func() {
		<-s.ctx.Done()
		s.mu.Lock()
		s.closing = true
		conns := append([]*bridgeConn(nil), s.conns...)
		associations := append([]transportorigin.PacketAssociation(nil), s.associations...)
		s.mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
		// Association Close is required to be a stop request, never a join.
		for _, a := range associations {
			_ = a.Close()
		}
		close(s.stopped)
	}()
	return s
}
func (s *Session) Context() context.Context { return s.ctx }
func (s *Session) Stop()                    { s.cancel() }
func (s *Session) Done() <-chan struct{}    { return s.done }
func (s *Session) BoundOrigin() transportorigin.Origin {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.origin
}
func (s *Session) BindOrigin(origin transportorigin.Origin) error {
	if origin == nil || origin.Identity() == nil {
		return transportorigin.ErrMissingOrigin
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.finishing || s.ctx.Err() != nil {
		return net.ErrClosed
	}
	if s.origin != nil {
		if s.origin.Identity() != origin.Identity() || s.lease == nil {
			return transportorigin.ErrStopped
		}
		select {
		case <-s.origin.StopRequested():
			return transportorigin.ErrStopped
		default:
			return nil
		}
	}
	s.origin = origin // selection remains committed even if terminal admission loses
	lease, err := origin.Acquire(s.ctx)
	if err != nil {
		return err
	}
	s.lease = lease
	s.originDone = make(chan struct{})
	// This watcher is joined by Finish, through the session stop acknowledgement.
	// It has no callbacks and cannot release the participant itself.
	go func() {
		defer close(s.originDone)
		select {
		case <-lease.Context().Done():
			s.Stop()
		case <-s.stopped:
		}
	}()
	return nil
}
func (s *Session) openLocked() bool {
	if s.closing || s.ctx.Err() != nil {
		return false
	}
	if s.origin != nil {
		select {
		case <-s.origin.StopRequested():
			return false
		default:
		}
	}
	return true
}

// Adopt takes sole physical-close ownership even when admission has stopped.
// A rejected late dial stays charged until Finish observes its real completion.
func (s *Session) Adopt(raw net.Conn) (net.Conn, error) {
	if raw == nil {
		return nil, errors.New("nil bridge connection")
	}
	if owned, ok := raw.(*bridgeConn); ok && owned.session == s {
		return owned, nil
	}
	var bindErr error
	if carrier, ok := raw.(transportorigin.Carrier); ok && carrier.TransportOrigin() != nil {
		bindErr = s.BindOrigin(carrier.TransportOrigin())
	}
	var identityHold func()
	var holdErr error
	if lifetime, ok := raw.(transportorigin.ConnectionLifetime); ok {
		identityHold, holdErr = lifetime.HoldConnection()
		if holdErr == nil && identityHold == nil {
			holdErr = transportorigin.ErrMissingOrigin
		}
	}
	c := newBridgeConn(s, raw)
	s.mu.Lock()
	s.conns = append(s.conns, c)
	if identityHold != nil {
		s.connHolds = append(s.connHolds, identityHold)
	}
	admitted := bindErr == nil && holdErr == nil && s.openLocked()
	s.mu.Unlock()
	if !admitted {
		_ = c.Close()
		if bindErr != nil {
			return c, bindErr
		}
		if holdErr != nil {
			return c, holdErr
		}
		return c, net.ErrClosed
	}
	return c, nil
}
func (s *Session) adoptAssociation(a transportorigin.PacketAssociation) error {
	var err error
	if a.TransportOrigin() != nil {
		err = s.BindOrigin(a.TransportOrigin())
	}
	s.mu.Lock()
	s.associations = append(s.associations, a)
	admitted := err == nil && s.openLocked()
	s.mu.Unlock()
	if !admitted {
		_ = a.Close()
		if err != nil {
			return err
		}
		return net.ErrClosed
	}
	return nil
}
func (s *Session) HoldRelease(release func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finishing {
		return false
	}
	s.releases = append(s.releases, release)
	return true
}
func (s *Session) Finish() {
	s.finish.Do(func() {
		s.mu.Lock()
		s.finishing = true
		s.mu.Unlock()
		s.Stop()
		<-s.stopped
		s.mu.Lock()
		conns, associations, lease, originDone := s.conns, s.associations, s.lease, s.originDone
		connHolds := s.connHolds
		s.connHolds = nil
		s.mu.Unlock()
		// Finish's caller has joined all callbacks/copies first. Keep their
		// exact identity mappings until here, then release before waiting on
		// raw terminal owners which themselves join these holds.
		for _, release := range connHolds {
			release()
		}
		for _, c := range conns {
			_ = c.Close()
			<-c.done
		}
		for _, a := range associations {
			_ = a.Close()
			if a.WaitClosed(context.Background()) != nil {
				return
			}
		}
		s.mu.Lock()
		releases := s.releases
		s.conns, s.associations, s.origin, s.lease, s.releases = nil, nil, nil, nil, nil
		s.mu.Unlock()
		if originDone != nil {
			<-originDone
		}
		for _, release := range releases {
			release()
		}
		if lease != nil {
			lease.Release()
		}
		close(s.done)
	})
	<-s.done // failed terminal observation retains the owner and its reservation
}
func sessionContext(c net.Conn, fallback context.Context) context.Context {
	if owned, ok := c.(*bridgeConn); ok {
		return owned.session.ctx
	}
	return fallback
}
func sessionFor(ctx context.Context) *Session {
	s, _ := ctx.Value(sessionContextKey{}).(*Session)
	return s
}

// bridgeConn deliberately does not embed net.Conn: io.Copy cannot bypass the
// counted Read/Write gates via a promoted WriterTo or ReaderFrom implementation.
type bridgeConn struct {
	mu            sync.Mutex
	changed       *sync.Cond
	raw           net.Conn
	session       *Session
	origin        transportorigin.Origin
	local, remote net.Addr
	borrows       int
	closing       bool
	stop          chan struct{}
	done          chan struct{}
}

func newBridgeConn(s *Session, raw net.Conn) *bridgeConn {
	c := &bridgeConn{raw: raw, session: s, local: raw.LocalAddr(), remote: raw.RemoteAddr(), stop: make(chan struct{}), done: make(chan struct{})}
	if carrier, ok := raw.(transportorigin.Carrier); ok {
		c.origin = carrier.TransportOrigin()
	}
	c.changed = sync.NewCond(&c.mu)
	go func() {
		<-c.stop
		closeErr := raw.Close()
		if terminal, ok := raw.(interface{ WaitClosed(context.Context) error }); ok {
			if terminal.WaitClosed(context.Background()) != nil {
				return
			}
		} else if c.origin != nil || (closeErr != nil && !errors.Is(closeErr, net.ErrClosed)) {
			return // no positive physical cleanup acknowledgement
		}
		c.mu.Lock()
		for c.borrows != 0 {
			c.changed.Wait()
		}
		c.raw = nil
		c.mu.Unlock()
		close(c.done)
	}()
	return c
}
func (c *bridgeConn) borrow() (net.Conn, transportorigin.Lease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.raw == nil || c.session.ctx.Err() != nil {
		return nil, nil, net.ErrClosed
	}
	origin := c.session.BoundOrigin()
	var lease transportorigin.Lease
	var err error
	if origin != nil {
		lease, err = origin.Acquire(c.session.ctx)
		if err != nil {
			return nil, nil, err
		}
	}
	c.borrows++
	return c.raw, lease, nil
}
func (c *bridgeConn) returned(lease transportorigin.Lease) bool {
	c.mu.Lock()
	valid := !c.closing && c.session.ctx.Err() == nil
	c.borrows--
	if c.borrows == 0 {
		c.changed.Broadcast()
	}
	c.mu.Unlock()
	if lease != nil {
		if lease.Context().Err() != nil {
			valid = false
		}
		lease.Release()
	}
	return valid
}
func (c *bridgeConn) Read(b []byte) (int, error) {
	raw, lease, err := c.borrow()
	if err != nil {
		return 0, err
	}
	n, err := raw.Read(b)
	if !c.returned(lease) {
		clear(b[:n])
		return 0, net.ErrClosed
	}
	return n, err
}
func (c *bridgeConn) Write(b []byte) (int, error) {
	raw, lease, err := c.borrow()
	if err != nil {
		return 0, err
	}
	n, err := raw.Write(b)
	if !c.returned(lease) {
		return n, net.ErrClosed
	}
	return n, err
}
func (c *bridgeConn) Close() error {
	c.mu.Lock()
	if !c.closing {
		c.closing = true
		close(c.stop)
	}
	c.mu.Unlock()
	return nil
}
func (c *bridgeConn) WaitClosed(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return nil
	}
}
func (c *bridgeConn) TransportOrigin() transportorigin.Origin { return c.origin }
func (c *bridgeConn) AuthenticatedPeer(ctx context.Context) (string, error) {
	raw, lease, err := c.borrow()
	if err != nil {
		return "", err
	}
	var id string
	if peer, ok := raw.(interface {
		AuthenticatedPeer(context.Context) (string, error)
	}); ok {
		id, err = peer.AuthenticatedPeer(ctx)
	} else if peer, ok := raw.(interface{ PeerIdentity() (string, bool) }); ok {
		var valid bool
		id, valid = peer.PeerIdentity()
		if !valid {
			err = net.ErrClosed
		}
	} else {
		err = transportorigin.ErrMissingOrigin
	}
	if !c.returned(lease) {
		return "", net.ErrClosed
	}
	return id, err
}
func (c *bridgeConn) PeerIdentity() (string, bool) {
	raw, lease, err := c.borrow()
	if err != nil {
		return "", false
	}
	var id string
	var valid bool
	if peer, ok := raw.(interface{ PeerIdentity() (string, bool) }); ok {
		id, valid = peer.PeerIdentity()
	}
	if !c.returned(lease) {
		return "", false
	}
	return id, valid
}

func (c *bridgeConn) LocalAddr() net.Addr  { return c.local }
func (c *bridgeConn) RemoteAddr() net.Addr { return c.remote }
func (c *bridgeConn) CloseWrite() error {
	raw, lease, err := c.borrow()
	if err != nil {
		return err
	}
	if half, ok := raw.(interface{ CloseWrite() error }); ok {
		err = half.CloseWrite()
	} else {
		err = errors.ErrUnsupported
	}
	if !c.returned(lease) {
		return net.ErrClosed
	}
	return err
}
func (c *bridgeConn) SetDeadline(t time.Time) error      { return c.deadline(t, 0) }
func (c *bridgeConn) SetReadDeadline(t time.Time) error  { return c.deadline(t, 1) }
func (c *bridgeConn) SetWriteDeadline(t time.Time) error { return c.deadline(t, 2) }
func (c *bridgeConn) deadline(t time.Time, which int) error {
	raw, lease, err := c.borrow()
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
	if !c.returned(lease) {
		return net.ErrClosed
	}
	return err
}

// Bridge joins both counted copy directions. Each copy retains its buffer until
// its final write or discard; method gates re-admit buffered delivery against
// the captured origin. Already admitted OS writes cannot be retracted.
func Bridge(a, b net.Conn) { bridge(a, b) }

var _ io.ReadWriteCloser = (*bridgeConn)(nil)
var _ transportorigin.TerminalConnection = (*bridgeConn)(nil)
