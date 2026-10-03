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
	err      error
	wg       sync.WaitGroup
	done     chan struct{}
}

func startServer(ctx context.Context, listener io.Closer, addr net.Addr, run func(*Server)) *Server {
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{ctx: ctx, cancel: cancel, listener: listener, addr: addr, active: make(map[net.Conn]struct{}), done: make(chan struct{})}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); run(s) }()
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		s.closed = true
		_ = s.listener.Close()
		for c := range s.active {
			_ = c.Close()
		}
		s.mu.Unlock()
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
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		_ = c.Close()
		return false
	}
	s.active[c] = struct{}{}
	return true
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
		if !s.track(c) {
			release()
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer release()
			defer s.release(c)
			handle(c)
		}()
	}
}

func dialTracked(s *Server, ctx context.Context, dial Dialer, network, address string) (net.Conn, error) {
	c, err := dial(ctx, network, address)
	if err != nil {
		if c != nil {
			_ = c.Close()
		}
		return nil, err
	}
	if c == nil {
		return nil, errors.New("dialer returned a nil connection")
	}
	if ctx.Err() != nil {
		_ = c.Close()
		return nil, ctx.Err()
	}
	if !s.track(c) {
		return nil, context.Canceled
	}
	return c, nil
}

// bridge preserves TCP half-close semantics, allowing a response after a
// client finishes writing. Errors and shutdown close both copy directions.
func bridge(a, b net.Conn) {
	done := make(chan struct{})
	copyOne := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
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
