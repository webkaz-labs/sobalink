package transport

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"
	"unsafe"

	"github.com/webkaz-labs/sobalink/internal/transportorigin"
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
	// MaxSessions and QueueSize optionally further restrict the live budget.
	// Zero follows the current budget, including later increases.
	MaxSessions int
	QueueSize   int
	// Budget shares current session and queued-storage limits across every
	// materialized port belonging to one policy. Reuse the same pointer for
	// those ports. Nil selects a policy budget on the default controller.
	Budget *UDPBudget
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
	bytesMu      sync.Mutex
	queuedBytes  int64
	budgetOnce   sync.Once
	budget       *UDPBudget
	authorize    SourceAuthorizer
	guard        func() error
	mu           sync.Mutex
	sessions     map[netip.AddrPort]*udpSession
	associations map[udpAssociationKey]*udpSession
	server       *Server
	local        udpPacketIO
	cfg          UDPConfig
	dial         Dialer
}

type udpAssociationKey struct {
	source   netip.AddrPort
	identity *transportorigin.Token
}

type udpSession struct {
	owner         *Session
	association   transportorigin.PacketAssociation
	sourceAddr    net.Addr
	sharedBudget  *UDPBudget
	table         *udpTable
	source        netip.AddrPort
	ctx           context.Context
	cancel        context.CancelFunc
	queue, tail   *queuedPacket
	queuedPackets int64
	ready         chan struct{}
	mu            sync.Mutex
	lastActive    time.Time
	closed        bool
}

// Queue storage grows only with admitted datagrams. Charging metadata also
// bounds empty datagrams when a caller selects a very large packet budget.
type queuedPacket struct {
	data []byte
	next *queuedPacket
}

const udpPacketOverhead = int(unsafe.Sizeof(queuedPacket{}))

func packetStorage(n int) int { return n + udpPacketOverhead }

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
	if err := normalizeUDPConfig(&cfg); err != nil {
		return nil, err
	}
	localPacket, err := (&net.ListenConfig{}).ListenPacket(ctx, loopbackNetwork("udp", cfg.ListenAddress), cfg.ListenAddress)
	if err != nil {
		return nil, err
	}
	local := localPacket.(*net.UDPConn)
	return startServer(ctx, local, local.LocalAddr(), func(s *Server) {
		table := &udpTable{sessions: make(map[netip.AddrPort]*udpSession), server: s, local: local, cfg: cfg, dial: dial}
		table.readLocal()
	}), nil
}

// Read the capability-bearing address when the packet front supplies one.
// The legacy numeric interface remains for ordinary OS sockets and test fronts.
func (t *udpTable) readPacket(buffer []byte) (int, netip.AddrPort, net.Addr, error) {
	if packet, ok := t.local.(net.PacketConn); ok {
		n, addr, err := packet.ReadFrom(buffer)
		if err != nil {
			return 0, netip.AddrPort{}, nil, err
		}
		source, err := sourceAddress(addr)
		return n, source, addr, err
	}
	n, source, err := t.local.ReadFromUDPAddrPort(buffer)
	return n, source, net.UDPAddrFromAddrPort(source), err
}
func (t *udpTable) readLocal() {
	buffer := make([]byte, maxDatagramSize)
	defer clear(buffer)
	for {
		n, source, addr, err := t.readPacket(buffer)
		if err != nil {
			t.server.fail(err)
			return
		}
		if t.server.ctx.Err() != nil {
			return
		}
		if n < 0 || n > len(buffer) {
			t.server.fail(errors.New("invalid datagram length"))
			return
		}
		t.acceptPacket(source, addr, buffer[:n])
	}
}
func (t *udpTable) acceptPacket(source netip.AddrPort, addr net.Addr, data []byte) {
	association, associated := addr.(transportorigin.PacketAssociation)
	var key udpAssociationKey
	ctx := t.server.ctx
	var delivery transportorigin.Lease
	if associated {
		key = udpAssociationKey{source, association.AssociationIdentity()}
		if key.identity == nil {
			return
		}
		if origin := association.TransportOrigin(); origin != nil {
			var err error
			delivery, err = origin.Acquire(ctx)
			if err != nil {
				return
			}
			defer delivery.Release()
			ctx = delivery.Context()
		}
	} else if carrier, ok := addr.(transportorigin.Carrier); ok && carrier.TransportOrigin() != nil {
		return // never degrade an origin-bearing address into a numeric tuple
	}
	if t.authorize != nil {
		if err := authorizeInbound(ctx, source, t.authorize, t.guard); err != nil {
			t.stopSource(source, key, associated)
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	if associated {
		if _, valid := association.PeerIdentity(); !valid {
			t.stopSource(source, key, true)
			return
		}
	}
	t.mu.Lock()
	var session *udpSession
	if associated {
		session = t.associations[key]
	} else {
		session = t.sessions[source]
	}
	if session == nil && (t.cfg.MaxSessions == 0 || len(t.sessions)+len(t.associations) < t.cfg.MaxSessions) {
		budget := t.resourceBudget()
		if !budget.reserveSession() {
			t.mu.Unlock()
			return
		}
		owner := NewSession(t.server.ctx)
		run, cancel := context.WithCancel(owner.Context())
		session = &udpSession{sharedBudget: budget, table: t, source: source, sourceAddr: addr, ctx: run, cancel: cancel, owner: owner, association: association, ready: make(chan struct{}, 1), lastActive: time.Now()}
		if associated {
			if t.associations == nil {
				t.associations = make(map[udpAssociationKey]*udpSession)
			}
			t.associations[key] = session
		} else {
			t.sessions[source] = session
		}
		t.server.mu.Lock()
		if t.server.sessions == nil {
			t.server.sessions = make(map[*Session]struct{})
		}
		t.server.sessions[owner] = struct{}{}
		t.server.mu.Unlock()
		owner.HoldRelease(func() {
			t.server.mu.Lock()
			delete(t.server.sessions, owner)
			t.server.mu.Unlock()
			t.mu.Lock()
			if associated {
				if t.associations[key] == session {
					delete(t.associations, key)
				}
			} else if t.sessions[source] == session {
				delete(t.sessions, source)
			}
			t.mu.Unlock()
			budget.releaseSession()
		})
		t.server.wg.Add(1)
		// Adoption and first publication occur before the worker starts. The table
		// entry retains capacity until its cleanup path returns even on rejection.
		t.mu.Unlock()
		var adoptErr error
		if associated {
			adoptErr = owner.adoptAssociation(association)
		}
		if adoptErr == nil {
			session.offer(data)
		}
		go func() {
			defer t.server.wg.Done()
			if adoptErr != nil {
				session.cleanup()
				return
			}
			session.run()
		}()
		return
	}
	t.mu.Unlock()
	if session != nil {
		session.offer(data)
	}
}
func (t *udpTable) stopSource(source netip.AddrPort, key udpAssociationKey, associated bool) {
	t.mu.Lock()
	session := t.sessions[source]
	if associated {
		session = t.associations[key]
	}
	t.mu.Unlock()
	if session != nil {
		session.stop()
	}
}
func (s *udpSession) cleanup() {
	s.stop()
	if s.owner != nil {
		s.owner.Finish()
		return
	}
	t := s.table
	t.mu.Lock()
	if s.association != nil {
		key := udpAssociationKey{s.source, s.association.AssociationIdentity()}
		if t.associations[key] == s {
			delete(t.associations, key)
		}
	} else if t.sessions[s.source] == s {
		delete(t.sessions, s.source)
	}
	t.mu.Unlock()
	if s.sharedBudget != nil {
		s.sharedBudget.releaseSession()
	}
}

func (s *udpSession) offer(packet []byte) {
	limit := s.table.resourceBudget().resources().limits().UDPQueuePackets
	if configured := s.table.cfg.QueueSize; configured > 0 && int64(configured) < limit {
		limit = int64(configured)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil || s.queuedPackets >= limit {
		return
	}
	if !s.table.reserveBytes(packetStorage(len(packet))) {
		return
	}
	stored := &queuedPacket{data: make([]byte, len(packet))}
	copy(stored.data, packet)
	if s.tail == nil {
		s.queue = stored
	} else {
		s.tail.next = stored
	}
	s.tail = stored
	s.queuedPackets++
	s.lastActive = time.Now()
	if s.ready == nil {
		s.ready = make(chan struct{}, 1)
	}
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

func (s *udpSession) takePacket() ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil || s.queue == nil {
		return nil, false
	}
	packet := s.queue
	s.queue = packet.next
	if s.queue == nil {
		s.tail = nil
	}
	s.queuedPackets--
	return packet.data, true
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
	defer s.mu.Unlock()
	s.closed = true
	s.cancel()
	for s.queue != nil {
		packet := s.queue
		s.queue = packet.next
		clear(packet.data)
		s.table.releaseBytes(packetStorage(len(packet.data)))
	}
	s.tail = nil
	s.queuedPackets = 0
}

func (s *udpSession) validate() error {
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if s.association != nil {
		if _, valid := s.association.PeerIdentity(); !valid {
			return net.ErrClosed
		}
	}
	if s.table.authorize != nil {
		if err := authorizeInbound(s.ctx, s.source, s.table.authorize, s.table.guard); err != nil {
			return err
		}
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
	defer s.cleanup()
	if err := s.validate(); err != nil {
		return
	}
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
		if packet, ok := s.takePacket(); ok {
			if err := s.sendPacket(remote, packet); err != nil {
				return
			}
			continue
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.ready:
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
	defer clear(buffer)
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
		var delivery transportorigin.Lease
		if s.owner != nil {
			if origin := s.owner.BoundOrigin(); origin != nil {
				delivery, err = origin.Acquire(s.ctx)
				if err != nil {
					return
				}
			}
		}
		var written int
		if packet, ok := s.table.local.(net.PacketConn); ok && s.sourceAddr != nil {
			written, err = packet.WriteTo(buffer[:n], s.sourceAddr)
		} else {
			written, err = s.table.local.WriteToUDPAddrPort(buffer[:n], s.source)
		}
		if delivery != nil {
			delivery.Release()
		}
		if err != nil || written != n {
			return
		}
	}
}

func normalizeUDPConfig(cfg *UDPConfig) error {
	if cfg.IdleTimeout < 0 || cfg.MaxSessions < 0 || cfg.QueueSize < 0 {
		return errors.New("UDP limits must be positive")
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = DefaultUDPIdleTimeout
	}
	var err error
	cfg.DialTimeout, err = normalizeTimeout(cfg.DialTimeout)
	if err != nil {
		return err
	}
	cfg.WriteTimeout, err = normalizeTimeout(cfg.WriteTimeout)
	return err
}

func (s *udpSession) sendPacket(remote net.Conn, packet []byte) error {
	defer func() { clear(packet); s.table.releaseBytes(packetStorage(len(packet))) }()
	if err := s.validate(); err != nil {
		return err
	}
	if !s.touch() {
		return context.Canceled
	}
	if err := remote.SetWriteDeadline(time.Now().Add(s.table.cfg.WriteTimeout)); err != nil {
		return err
	}
	n, err := remote.Write(packet)
	if err != nil {
		return err
	}
	if n != len(packet) {
		return errors.New("incomplete datagram write")
	}
	return nil
}
