// Package transport provides loopback-only forwarding listeners. Every remote
// connection is made through the supplied policy-enforcing Dialer.
package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// Dialer must honor cancellation and return a connected stream (tcp) or
// datagram (udp) connection. It must enforce destination policy before dialing.
type Dialer func(context.Context, string, string) (net.Conn, error)

const defaultDialTimeout = 10 * time.Second
const defaultTCPPerPolicy = 128
const defaultTCPConnections = 512

// AdmitTCP shares the default aggregate budget with virtual range handlers.
// Applications with their own configurable budget use Controller.AdmitTCP.
func AdmitTCP() (func(), bool) { return defaultController.AdmitTCP("", "") }

// Server owns its listener, accepted connections, and remote connections.
// Close and context cancellation shut down all of them; Wait joins all workers.
type Server struct {
	ctx      context.Context
	cancel   context.CancelFunc
	listener io.Closer
	addr     net.Addr
	mu       sync.Mutex
	closed   bool
	active   map[net.Conn]struct{}
	sessions map[*Session]struct{}
	err      error
	wg       sync.WaitGroup
	done     chan struct{}
}

func startServer(ctx context.Context, listener io.Closer, addr net.Addr, run func(*Server)) *Server {
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{ctx: ctx, cancel: cancel, listener: listener, addr: addr, active: make(map[net.Conn]struct{}), sessions: make(map[*Session]struct{}), done: make(chan struct{})}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); run(s) }()
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		s.closed = true

		active := make([]net.Conn, 0, len(s.active))
		for c := range s.active {
			active = append(active, c)
		}
		s.mu.Unlock()
		_ = s.listener.Close()
		for _, c := range active {
			_ = c.Close()
		}
		s.wg.Wait()
		close(s.done)
	}()
	return s
}

func (s *Server) Addr() net.Addr { return s.addr }

// Done closes once the listener and every worker have stopped.
func (s *Server) Done() <-chan struct{} { return s.done }

// Close is idempotent and waits for all workers to finish.
func (s *Server) Close() error { s.cancel(); return s.Wait() }

// Wait returns after shutdown, including all active connection workers.
func (s *Server) Wait() error {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	admitted := !s.closed && s.ctx.Err() == nil
	if admitted {
		s.active[c] = struct{}{}
	}
	s.mu.Unlock()
	if !admitted {
		_ = c.Close()
	}
	return admitted
}

func (s *Server) release(c net.Conn) {
	_ = c.Close()
	s.mu.Lock()
	delete(s.active, c)
	s.mu.Unlock()
}

func (s *Server) fail(err error) {
	if s.ctx.Err() == nil {
		s.mu.Lock()
		if s.err == nil {
			s.err = err
		}
		s.mu.Unlock()
	}
	s.cancel()
}

func validateListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listener address: %w", err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || (ip != netip.MustParseAddr("127.0.0.1") && ip != netip.IPv6Loopback()) {
		return errors.New("listener must use exactly 127.0.0.1 or ::1")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 0 || p > 65535 {
		return errors.New("invalid listener port")
	}
	return nil
}

// loopbackNetwork is used only after exact numeric listener validation.
func loopbackNetwork(network, address string) string {
	host, _, _ := net.SplitHostPort(address)
	if host == "::1" {
		return network + "6"
	}
	return network + "4"
}

func normalizeTimeout(t time.Duration) (time.Duration, error) {
	if t < 0 {
		return 0, errors.New("timeout must be positive")
	}
	if t == 0 {
		return defaultDialTimeout, nil
	}
	if t > defaultDialTimeout {
		return 0, errors.New("timeout must not exceed 10 seconds")
	}
	return t, nil
}

func acceptConnections(s *Server, l net.Listener, handle func(net.Conn), configs ...TCPConfig) {
	var cfg TCPConfig
	if len(configs) != 0 {
		cfg = configs[0]
	}
	controller := controllerOrDefault(cfg.Controller)
	policy := cfg.PolicyID
	if policy == "" {
		policy = fmt.Sprintf("listener:%p", s)
	}
	for {
		c, err := l.Accept()
		if err != nil {
			s.fail(err)
			return
		}
		// Pending dials and unauthenticated SOCKS handshakes hold their slots.
		release, ok := controller.AdmitTCP(policy, cfg.PeerID)
		if !ok {
			_ = c.Close()
			continue
		}

		owner := NewSession(s.ctx)
		owned, ownErr := owner.Adopt(c)
		s.mu.Lock()
		if s.sessions == nil {
			s.sessions = make(map[*Session]struct{})
		}
		s.sessions[owner] = struct{}{}
		s.mu.Unlock()
		owner.HoldRelease(func() { s.mu.Lock(); delete(s.sessions, owner); s.mu.Unlock(); release() })
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer owner.Finish()
			if ownErr != nil || !s.track(owned) {
				return
			}
			defer s.release(owned)
			handle(owned)
		}()
	}
}

func dialTracked(s *Server, ctx context.Context, dial Dialer, network, address string) (net.Conn, error) {
	c, err := dial(ctx, network, address)
	if c != nil {
		if owner := sessionFor(ctx); owner != nil {
			owned, adoptErr := owner.Adopt(c)
			c = owned
			if err == nil {
				err = adoptErr
			}
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		if c != nil {
			_ = c.Close()
		}
		return nil, err
	}
	if c == nil {
		return nil, errors.New("dialer returned a nil connection")
	}
	if !s.track(c) {
		return nil, context.Canceled
	}
	return c, nil
}

// BeginRetire stops only sessions already bound to this immutable origin.
// The accept/read loops and unrelated Tailnet sessions retain their lifetimes.
func (s *Server) BeginRetire(origin transportorigin.Origin) {
	if origin == nil {
		return
	}
	for _, owner := range s.originSessions(origin) {
		owner.Stop()
	}
}
func (s *Server) originSessions(origin transportorigin.Origin) []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var owners []*Session
	for owner := range s.sessions {
		selected := owner.BoundOrigin()
		if selected != nil && selected.Identity() == origin.Identity() {
			owners = append(owners, owner)
		}
	}
	return owners
}

// WaitRetired never releases capacity on timeout or waits for retained fronts.
// The generation's terminal fence must already be sealed before this snapshot.
func (s *Server) WaitRetired(ctx context.Context, origin transportorigin.Origin) error {
	if origin == nil {
		return transportorigin.ErrMissingOrigin
	}
	for _, owner := range s.originSessions(origin) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-owner.Done():
		}
	}
	return nil
}

// bridge preserves TCP half-close semantics, allowing a response after a
// client finishes writing. Errors and shutdown close both copy directions.
func bridge(a, b net.Conn) {
	done := make(chan struct{})
	copyOne := func(dst, src net.Conn) {
		err := copyBridge(dst, src)
		if err != nil {
			_ = a.Close()
			_ = b.Close()
			return
		}
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			if err := cw.CloseWrite(); err != nil {
				_ = a.Close()
				_ = b.Close()
			}
		} else {
			_ = dst.Close()
		}
	}
	go func() { defer close(done); copyOne(a, b) }()
	copyOne(b, a)
	<-done
}

// Use explicit Read/Write calls so an embedded ReaderFrom/WriterTo cannot
// bypass origin admission for bytes buffered by the other copy direction.
func copyBridge(dst, src net.Conn) error {
	buffer := make([]byte, 32*1024)
	defer clear(buffer)
	for {
		n, err := src.Read(buffer)
		if n > 0 {
			if writeErr := writeFull(dst, buffer[:n]); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
