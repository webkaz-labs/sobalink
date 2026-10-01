package transport

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"
)

const DefaultUDPIdleTimeout = 5 * time.Minute
const defaultMaxUDPSessions = 256
const defaultUDPQueueSize = 64
const maxDatagramSize = 65535

type UDPConfig struct {
	ListenAddress string
	Target        string
	DialTimeout   time.Duration
	// IdleTimeout defaults to five minutes. Shorter values are useful in tests;
	// applications should enforce a minimum of five minutes for normal use.
	IdleTimeout  time.Duration
	WriteTimeout time.Duration
	MaxSessions  int
	QueueSize    int
	// Validate optionally rechecks current destination authorization before
	// forwarding each datagram in either direction. It must be concurrency-safe
	// and honor its context. Any error closes that source's session.
	Validate func(context.Context, string, string) error
}

// udpPacketIO keeps the session engine independent of the socket implementation.
// Production always supplies the loopback *net.UDPConn created by StartUDP.
type udpPacketIO interface {
	ReadFromUDPAddrPort([]byte) (int, netip.AddrPort, error)
	WriteToUDPAddrPort([]byte, netip.AddrPort) (int, error)
}

type udpTable struct {
	mu       sync.Mutex
	sessions map[netip.AddrPort]*udpSession
	server   *Server
	local    udpPacketIO
	cfg      UDPConfig
	dial     Dialer
}

type udpSession struct {
	table      *udpTable
	source     netip.AddrPort
	ctx        context.Context
	cancel     context.CancelFunc
	queue      chan []byte
	mu         sync.Mutex
	lastActive time.Time
	closed     bool
}

// StartUDP keeps one connected upstream socket per local source, preserving
// reverse delivery of delayed or unsolicited replies until the session expires.
// Sessions and queued datagrams are bounded; excess traffic is dropped.
func StartUDP(ctx context.Context, cfg UDPConfig, dial Dialer) (*Server, error) {
	if err := validateListenAddress(cfg.ListenAddress); err != nil {
		return nil, err
	}
	if dial == nil {
		return nil, errors.New("dialer is required")
	}
	if cfg.Target == "" {
		return nil, errors.New("target is required")
	}
	if cfg.IdleTimeout < 0 || cfg.MaxSessions < 0 || cfg.QueueSize < 0 {
		return nil, errors.New("UDP limits must be positive")
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = DefaultUDPIdleTimeout
	}
	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = defaultMaxUDPSessions
	}
	if cfg.QueueSize == 0 {
		cfg.QueueSize = defaultUDPQueueSize
	}
	var err error
	cfg.DialTimeout, err = normalizeTimeout(cfg.DialTimeout)
	if err != nil {
		return nil, err
	}
	cfg.WriteTimeout, err = normalizeTimeout(cfg.WriteTimeout)
	if err != nil {
		return nil, err
	}
	localPacket, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp4", cfg.ListenAddress)
	if err != nil {
		return nil, err
	}
	local := localPacket.(*net.UDPConn)
	return startServer(ctx, local, local.LocalAddr(), func(s *Server) {
		table := &udpTable{sessions: make(map[netip.AddrPort]*udpSession), server: s, local: local, cfg: cfg, dial: dial}
		table.readLocal()
	}), nil
}

func (t *udpTable) readLocal() {
	buffer := make([]byte, maxDatagramSize)
	for {
		n, source, err := t.local.ReadFromUDPAddrPort(buffer)
		if err != nil {
			t.server.fail(err)
			return
		}
		if t.server.ctx.Err() != nil {
			return
		}
		packet := append([]byte{}, buffer[:n]...)
		t.mu.Lock()
		session := t.sessions[source]
		if session == nil && len(t.sessions) < t.cfg.MaxSessions {
			ctx, cancel := context.WithCancel(t.server.ctx)
			session = &udpSession{table: t, source: source, ctx: ctx, cancel: cancel, queue: make(chan []byte, t.cfg.QueueSize), lastActive: time.Now()}
			t.sessions[source] = session
			t.server.wg.Add(1)
			go func() { defer t.server.wg.Done(); session.run() }()
		}
		if session != nil {
			session.offer(packet)
		}
		t.mu.Unlock()
	}
}

func (s *udpSession) offer(packet []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return
	}
	select {
	case s.queue <- packet:
		s.lastActive = time.Now()
	default: // A slow source cannot grow memory without bound.
	}
}

func (s *udpSession) touch() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return false
	}
	s.lastActive = time.Now()
	return true
}

// remaining atomically expires the session, so new traffic cannot revive a
// mapping after its expiry has been committed.
func (s *udpSession) remaining() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return 0
	}
	remaining := s.table.cfg.IdleTimeout - time.Since(s.lastActive)
	if remaining <= 0 {
		s.closed = true
		s.cancel()
		return 0
	}
	return remaining
}

func (s *udpSession) stop() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
}

func (s *udpSession) validate() error {
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if s.table.cfg.Validate == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.table.cfg.DialTimeout)
	defer cancel()
	if err := s.table.cfg.Validate(ctx, "udp", s.table.cfg.Target); err != nil {
		return err
	}
	return ctx.Err()
}

func (s *udpSession) run() {
	t := s.table
	defer func() {
		s.stop()
		t.mu.Lock()
		if t.sessions[s.source] == s {
			delete(t.sessions, s.source)
		}
		t.mu.Unlock()
	}()
	dialCtx, cancel := context.WithTimeout(s.ctx, t.cfg.DialTimeout)
	remote, err := dialTracked(t.server, dialCtx, t.dial, "udp", t.cfg.Target)
	cancel()
	if err != nil {
		return
	}
	readerDone := make(chan struct{})
	watcherDone := make(chan struct{})
	defer func() {
		s.stop()
		t.server.release(remote)
		<-readerDone
		<-watcherDone
	}()
	go func() { defer close(readerDone); s.readRemote(remote) }()
	go func() { defer close(watcherDone); s.watchRemote(remote) }()
	for {
		select {
		case <-s.ctx.Done():
			return
		case packet := <-s.queue:
			if err := s.validate(); err != nil {
				return
			}
			if !s.touch() {
				return
			}
			if err := remote.SetWriteDeadline(time.Now().Add(t.cfg.WriteTimeout)); err != nil {
				return
			}
			n, err := remote.Write(packet)
			if err != nil || n != len(packet) {
				return
			}
		}
	}
}

// A separate lifetime watcher closes the socket even while a write or
// validation call is blocked. Expiry never waits for another datagram.
func (s *udpSession) watchRemote(remote net.Conn) {
	defer remote.Close()
	defer s.stop()
	remaining := s.remaining()
	if remaining <= 0 {
		return
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			remaining := s.remaining()
			if remaining <= 0 {
				return
			}
			timer.Reset(remaining)
		}
	}
}

func (s *udpSession) readRemote(remote net.Conn) {
	defer s.stop()
	buffer := make([]byte, maxDatagramSize)
	for {
		n, err := remote.Read(buffer)
		if err != nil {
			return
		}
		if err := s.validate(); err != nil {
			return
		}
		if !s.touch() {
			return
		}
		written, err := s.table.local.WriteToUDPAddrPort(buffer[:n], s.source)
		if err != nil || written != n {
			return
		}
	}
}
