package core

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/connectionroute"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/transport"
)

const mixedPacketWatchInterval = 2 * time.Second

// UDP aliases are opaque local handles, never network destinations. A distinct
// namespace and a non-wrapping process counter prevent an old application
// session from addressing a new mapping after idle retirement or listener close.
var mixedPacketSequence atomic.Uint64

func nextMixedPacketAlias() (netip.AddrPort, error) {
	for {
		n := mixedPacketSequence.Load()
		if n == ^uint64(0) {
			return netip.AddrPort{}, connectionroute.ErrCapacity
		}
		if !mixedPacketSequence.CompareAndSwap(n, n+1) {
			continue
		}
		raw := [16]byte{0xfd, 0x7a, 0x11, 0x5c, 0xa1, 0xe1}
		binary.BigEndian.PutUint64(raw[8:], n+1)
		return netip.AddrPortFrom(netip.AddrFrom16(raw), 1), nil
	}
}

type mixedDatagram struct {
	data   []byte
	source netip.AddrPort
}

type mixedPacketSource struct {
	alias      netip.AddrPort
	source     mixedSource
	lastActive time.Time
	queued     int
	writing    int
}

type mixedPacket struct {
	owner         *mixedBackend
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	packets       map[string]net.PacketConn
	aliases       map[string]*mixedPacketSource
	sources       map[netip.AddrPort]*mixedPacketSource
	peerFilter    func(string) bool
	filterVersion uint64
	in            chan mixedDatagram
	done          chan struct{}
	once          sync.Once
	wg            sync.WaitGroup
	addr          net.Addr
	closed        bool
	terminal      error
	readDeadline  time.Time
	writeDeadline time.Time
	wake          chan struct{}
}

func mixedPacketUnavailable(err error) bool {
	return mixedUnavailable(err)
}

func (n *mixedBackend) ListenPacket(network, address string) (net.PacketConn, error) {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || network != "udp" || ap.Addr() != n.self || ap.Port() == 0 {
		return nil, connectionroute.ErrDenied
	}
	n.mu.Lock()
	closed := n.closed
	n.mu.Unlock()
	if closed {
		return nil, net.ErrClosed
	}
	ctx, cancel := context.WithCancel(n.ctx)
	p := &mixedPacket{owner: n, ctx: ctx, cancel: cancel, packets: map[string]net.PacketConn{}, aliases: map[string]*mixedPacketSource{}, sources: map[netip.AddrPort]*mixedPacketSource{}, in: make(chan mixedDatagram, max(1, n.packetQueueLimit)), done: make(chan struct{}), wake: make(chan struct{}), addr: net.UDPAddrFromAddrPort(ap)}
	if err = p.refresh(network, ap.Port()); err != nil {
		_ = p.Close()
		return nil, err
	}
	p.mu.Lock()
	if p.closed {
		err = p.closedErrorLocked()
		p.mu.Unlock()
		_ = p.Close()
		return nil, err
	}
	p.wg.Add(1)
	go p.watch(network, ap.Port())
	p.mu.Unlock()
	return p, nil
}

// SetPeerFilter installs application admission before allocating source handles.
// Without a filter, authenticated transport peers receive no application access.
// Transport-level WhoIs and the service authorizer still recheck every mapping.
func (p *mixedPacket) SetPeerFilter(filter func(string) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.peerFilter = filter
	p.filterVersion++
	for key, source := range p.aliases {
		p.retireLocked(key, source)
	}
}

func (p *mixedPacket) refresh(network string, port uint16) error {
	p.owner.mu.Lock()
	closed := p.owner.closed
	p.owner.mu.Unlock()
	if closed {
		return net.ErrClosed
	}
	_, states, err := p.owner.routeSnapshot(p.ctx)
	if err != nil {
		return err
	}
	for _, name := range p.owner.order {
		state := states[name]
		if mixedReadiness(state) == mixedAuthRequired {
			return connectionroute.ErrDenied
		}
		if mixedReadiness(state) == mixedUnknown || (mixedReadiness(state) == mixedReady && len(state.IPs) == 0) {
			return errMixedReadinessUnknown
		}
		if mixedReadiness(state) == mixedAbsent {
			p.mu.Lock()
			packet := p.packets[name]
			p.mu.Unlock()
			if packet != nil {
				p.detach(name, packet)
			}
			continue
		}
		if err := p.attach(name, network, port, state); err != nil && !mixedPacketUnavailable(err) {
			return err
		}
	}
	return nil
}

func (p *mixedPacket) attach(name, network string, port uint16, state identity.State) error {
	p.mu.Lock()
	if p.closed || p.packets[name] != nil {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()
	packet, err := p.owner.nodes[name].ListenPacket(network, netip.AddrPortFrom(state.IPs[0], port).String())
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = packet.Close()
		return net.ErrClosed
	}
	if err = packet.SetWriteDeadline(p.writeDeadline); err != nil {
		p.mu.Unlock()
		_ = packet.Close()
		return err
	}
	p.packets[name] = packet
	p.wg.Add(1)
	go p.read(name, packet)
	p.mu.Unlock()
	return nil
}

func (p *mixedPacket) detach(name string, packet net.PacketConn) {
	p.mu.Lock()
	if p.packets[name] != packet {
		p.mu.Unlock()
		return
	}
	delete(p.packets, name)
	for key, source := range p.aliases {
		if source.source.backend == name {
			p.retireLocked(key, source)
		}
	}
	p.mu.Unlock()
	_ = packet.Close()
}

func (p *mixedPacket) watch(network string, port uint16) {
	defer p.wg.Done()
	ticker := time.NewTicker(mixedPacketWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-p.ctx.Done():
			p.stop(p.ctx.Err())
			return
		case now := <-ticker.C:
			p.mu.Lock()
			p.retireIdleLocked(now)
			p.mu.Unlock()
			if err := p.refresh(network, port); err != nil {
				p.stop(err)
				return
			}
		}
	}
}

func mixedPacketSourceKey(name string, remote netip.AddrPort) string {
	return name + "\x00" + remote.String()
}

// Caller holds p.mu; lock ordering is always packet, then owner.
func (p *mixedPacket) retireLocked(key string, source *mixedPacketSource) {
	delete(p.aliases, key)
	delete(p.sources, source.alias)
	p.owner.mu.Lock()
	delete(p.owner.sources, source.alias)
	p.owner.mu.Unlock()
}

func (p *mixedPacket) retireIdleLocked(now time.Time) {
	for key, source := range p.aliases {
		if source.queued == 0 && source.writing == 0 && !now.Before(source.lastActive.Add(transport.DefaultUDPIdleTimeout)) {
			p.retireLocked(key, source)
		}
	}
}

func (p *mixedPacket) enqueue(name string, packet net.PacketConn, remote netip.AddrPort, data []byte) {
	// A full selected queue cannot consume more aliases or authentication work.
	if len(p.in) == cap(p.in) {
		return
	}
	identityCtx, cancel := context.WithTimeout(p.ctx, 3*time.Second)
	id, err := p.owner.nodes[name].WhoIs(identityCtx, remote)
	logical := p.owner.logical(name, id)
	if err == nil {
		err = p.owner.admitMixedRoute(identityCtx, logical)
	}
	cancel()
	p.mu.Lock()
	filter, version := p.peerFilter, p.filterVersion
	p.mu.Unlock()
	allowed := err == nil && id != "" && filter != nil && filter(logical)
	key := mixedPacketSourceKey(name, remote)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.packets[name] != packet || version != p.filterVersion {
		return
	}
	if !allowed {
		if source := p.aliases[key]; source != nil {
			p.retireLocked(key, source)
		}
		return
	}
	if len(p.in) == cap(p.in) {
		return
	}
	now := time.Now()
	source := p.aliases[key]
	if source != nil && (source.source.identity != id || source.source.logical != logical) {
		p.retireLocked(key, source)
		source = nil
	}
	if source == nil {
		p.retireIdleLocked(now)
		p.owner.mu.Lock()
		defer p.owner.mu.Unlock()
		if p.owner.closed || len(p.owner.sources) >= p.owner.sourceBudget() {
			return
		}
		alias, err := nextMixedPacketAlias()
		if err != nil {
			return
		}
		source = &mixedPacketSource{alias: alias, source: mixedSource{name, remote, id, logical}}
		p.owner.sources[alias] = source.source
		p.aliases[key] = source
		p.sources[alias] = source
	}
	source.lastActive = now
	source.queued++
	p.in <- mixedDatagram{data: append([]byte(nil), data...), source: source.alias}
}

func (p *mixedPacket) read(name string, packet net.PacketConn) {
	defer p.wg.Done()
	buf := make([]byte, 65535)
	for {
		n, remote, err := packet.ReadFrom(buf)
		if err != nil {
			if mixedPacketUnavailable(err) {
				p.detach(name, packet)
			} else {
				p.stop(err)
			}
			return
		}
		if remote == nil || n < 0 || n > len(buf) {
			p.stop(connectionroute.ErrDenied)
			return
		}
		ap, err := netip.ParseAddrPort(remote.String())
		if err == nil {
			p.enqueue(name, packet, ap, buf[:n])
		}
	}
}

func (p *mixedPacket) closedErrorLocked() error {
	if p.terminal != nil {
		return p.terminal
	}
	return net.ErrClosed
}

func (p *mixedPacket) ReadFrom(buf []byte) (int, net.Addr, error) {
	for {
		p.mu.Lock()
		if p.closed {
			err := p.closedErrorLocked()
			p.mu.Unlock()
			return 0, nil, err
		}
		deadline, wake := p.readDeadline, p.wake
		p.mu.Unlock()
		var timer *time.Timer
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			if !time.Now().Before(deadline) {
				return 0, nil, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
		select {
		case <-p.done:
			if timer != nil {
				timer.Stop()
			}
			continue
		case <-wake:
			if timer != nil {
				timer.Stop()
			}
			continue
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		case packet := <-p.in:
			if timer != nil {
				timer.Stop()
			}
			p.mu.Lock()
			source := p.sources[packet.source]
			if p.closed || source == nil {
				p.mu.Unlock()
				continue
			}
			source.queued--
			source.lastActive = time.Now()
			p.mu.Unlock()
			return copy(buf, packet.data), net.UDPAddrFromAddrPort(packet.source), nil
		}
	}
}

func (p *mixedPacket) WriteTo(buf []byte, remote net.Addr) (int, error) {
	if remote == nil {
		return 0, connectionroute.ErrDenied
	}
	ap, err := netip.ParseAddrPort(remote.String())
	if err != nil {
		return 0, err
	}
	p.mu.Lock()
	if p.closed {
		err := p.closedErrorLocked()
		p.mu.Unlock()
		return 0, err
	}
	p.retireIdleLocked(time.Now())
	source := p.sources[ap]
	if source == nil || p.peerFilter == nil {
		p.mu.Unlock()
		return 0, connectionroute.ErrDenied
	}
	packet := p.packets[source.source.backend]
	filter, version := p.peerFilter, p.filterVersion
	source.writing++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		source.writing--
		p.mu.Unlock()
	}()
	if packet == nil || !filter(source.source.logical) {
		return 0, connectionroute.ErrDenied
	}
	identityCtx, cancel := context.WithTimeout(p.ctx, 3*time.Second)
	_, err = p.owner.WhoIs(identityCtx, ap)
	cancel()
	if err != nil {
		return 0, err
	}
	p.mu.Lock()
	valid := !p.closed && p.sources[ap] == source && version == p.filterVersion && p.packets[source.source.backend] == packet
	p.mu.Unlock()
	if !valid {
		return 0, connectionroute.ErrDenied
	}
	// Send exactly once to the authenticated origin. No replay or other route is
	// attempted for a failed, short, zero-length, or oversized datagram.
	n, err := packet.WriteTo(buf, net.UDPAddrFromAddrPort(source.source.remote))
	if err == nil {
		p.mu.Lock()
		source.lastActive = time.Now()
		p.mu.Unlock()
	} else if mixedPacketUnavailable(err) {
		p.detach(source.source.backend, packet)
	}
	return n, err
}

func (p *mixedPacket) LocalAddr() net.Addr { return p.addr }
func (p *mixedPacket) stop(err error) {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed, p.terminal = true, err
		packets := p.packets
		p.packets = map[string]net.PacketConn{}
		for key, source := range p.aliases {
			p.retireLocked(key, source)
		}
		close(p.done)
		p.cancel()
		p.mu.Unlock()
		for _, packet := range packets {
			_ = packet.Close()
		}
	})
}
func (p *mixedPacket) Close() error {
	p.stop(nil)
	p.wg.Wait()
	return nil
}
func (p *mixedPacket) SetDeadline(t time.Time) error {
	if err := p.SetReadDeadline(t); err != nil {
		return err
	}
	return p.SetWriteDeadline(t)
}
func (p *mixedPacket) SetReadDeadline(t time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closedErrorLocked()
	}
	p.readDeadline = t
	close(p.wake)
	p.wake = make(chan struct{})
	return nil
}
func (p *mixedPacket) SetWriteDeadline(t time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closedErrorLocked()
	}
	p.writeDeadline = t
	for _, packet := range p.packets {
		if err := packet.SetWriteDeadline(t); err != nil {
			return err
		}
	}
	return nil
}
