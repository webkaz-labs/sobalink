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
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

const mixedPacketWatchInterval = 2 * time.Second
const mixedPacketIdentityTimeout = 3 * time.Second

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
	source *mixedPacketSource
}

type mixedPacketKey struct {
	backend     string
	remote      netip.AddrPort
	association *transportorigin.Token
}

type mixedPacketSource struct {
	alias       netip.AddrPort
	key         mixedPacketKey
	source      mixedSource
	packet      net.PacketConn
	remote      net.Addr
	association transportorigin.PacketAssociation
	origin      transportorigin.Origin
	lease       transportorigin.Lease
	address     *mixedPacketAssociation
	lastActive  time.Time
	queued      int
	borrowing   int
	stopped     bool
	stop        chan struct{}
	changed     chan struct{}
	done        chan struct{}
}

type mixedPacket struct {
	owner         *mixedBackend
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	packets       map[string]net.PacketConn
	aliases       map[mixedPacketKey]*mixedPacketSource
	associations  map[mixedPacketKey]*mixedPacketSource
	sources       map[netip.AddrPort]*mixedPacketSource
	peerFilter    func(string) bool
	filterVersion uint64
	in            []mixedDatagram
	queueLimit    int
	done          chan struct{}
	wg            sync.WaitGroup
	addr          net.Addr
	closed        bool
	terminal      error
	readDeadline  time.Time
	writeDeadline time.Time
	writeVersion  uint64
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
	p := &mixedPacket{owner: n, ctx: ctx, cancel: cancel, packets: map[string]net.PacketConn{}, aliases: map[mixedPacketKey]*mixedPacketSource{}, associations: map[mixedPacketKey]*mixedPacketSource{}, sources: map[netip.AddrPort]*mixedPacketSource{}, in: make([]mixedDatagram, 0, max(1, n.packetQueueLimit)), queueLimit: max(1, n.packetQueueLimit), done: make(chan struct{}), wake: make(chan struct{}), addr: net.UDPAddrFromAddrPort(ap)}
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
			// A recovering direct-LAN generation retires associations separately;
			// its retained logical listener and independent backends stay alive.
			if p.packetRecovering(name) {
				continue
			}
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
	for {
		p.mu.Lock()
		if p.closed || p.packets[name] != nil {
			closed := p.closed
			p.mu.Unlock()
			_ = packet.Close()
			if closed {
				return net.ErrClosed
			}
			return nil
		}
		deadline, version := p.writeDeadline, p.writeVersion
		p.mu.Unlock()
		if err := packet.SetWriteDeadline(deadline); err != nil {
			_ = packet.Close()
			return err
		}
		p.mu.Lock()
		if p.closed || p.packets[name] != nil || version != p.writeVersion {
			p.mu.Unlock()
			continue
		}
		p.packets[name] = packet
		p.wg.Add(1)
		go p.read(name, packet)
		p.mu.Unlock()
		return nil
	}
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

func (p *mixedPacket) retireIdleLocked(now time.Time) {
	for key, source := range p.aliases {
		if source.queued == 0 && source.borrowing == 0 && !now.Before(source.lastActive.Add(transport.DefaultUDPIdleTimeout)) {
			p.retireLocked(key, source)
		}
	}
}

// Kept for plain-address callers; transport readers use enqueueAddress so an
// immutable association can never disappear through address formatting.
func (p *mixedPacket) enqueue(name string, packet net.PacketConn, remote netip.AddrPort, data []byte) {
	p.enqueueAddress(name, packet, net.UDPAddrFromAddrPort(remote), data)
}

func (p *mixedPacket) enqueueAddress(name string, packet net.PacketConn, remote net.Addr, data []byte) {
	association, origin, err := mixedPacketOrigin(remote)
	if err != nil {
		return
	}
	p.mu.Lock()
	full := p.closed || p.packets[name] != packet || len(p.in) >= p.queueLimit
	p.mu.Unlock()
	if full {
		return
	}
	// Capture and count the exact old origin before address/identity callbacks.
	ctx := p.ctx
	var lease transportorigin.Lease
	if origin != nil {
		lease, err = origin.Acquire(ctx)
		if err != nil {
			return
		}
		defer func() {
			if lease != nil {
				lease.Release()
			}
		}()
		ctx = lease.Context()
	}
	ap, err := netip.ParseAddrPort(remote.String())
	if err != nil {
		return
	}
	key := mixedPacketKey{backend: name, remote: ap}
	if association != nil {
		key.association = association.AssociationIdentity()
	}
	p.mu.Lock()
	filter, version := p.peerFilter, p.filterVersion
	candidate := p.aliases[key]
	if association != nil {
		candidate = p.associations[key]
	}
	if candidate != nil {
		if !p.sourceOpenLocked(candidate) {
			p.mu.Unlock()
			return
		}
		p.borrowSourceLocked(candidate)
	}
	p.mu.Unlock()
	var borrowed []*mixedPacketSource
	if candidate != nil {
		borrowed = append(borrowed, candidate)
	}
	defer func() {
		for _, source := range borrowed {
			p.releaseBorrow(source)
		}
	}()
	identityCtx, cancel := context.WithTimeout(ctx, mixedPacketIdentityTimeout)
	var id string
	if association != nil {
		var valid bool
		id, valid = association.PeerIdentity()
		if !valid {
			err = connectionroute.ErrDenied
		}
	} else {
		id, err = p.owner.nodes[name].WhoIs(identityCtx, ap)
	}
	logical := p.owner.logical(name, id)
	if err == nil {
		err = p.owner.admitMixedRoute(identityCtx, logical)
	}
	cancel()
	allowed := err == nil && ctx.Err() == nil && id != "" && filter != nil && filter(logical)
	p.mu.Lock()
	var publication transportorigin.Lease
	defer func() {
		p.mu.Unlock()
		if publication != nil {
			publication.Release()
		}
	}()
	if p.closed || p.packets[name] != packet || version != p.filterVersion || ctx.Err() != nil || (candidate != nil && p.aliases[key] != candidate) {
		return
	}
	if !allowed {
		if source := p.aliases[key]; source != nil {
			p.retireLocked(key, source)
		}
		return
	}
	if len(p.in) >= p.queueLimit {
		return
	}
	now := time.Now()
	source := p.aliases[key]
	if source != nil && (source.source.identity != id || source.source.logical != logical || source.packet != packet) {
		p.retireLocked(key, source)
		if association != nil {
			return
		}
		source = nil
	}
	if source != nil && source != candidate {
		p.borrowSourceLocked(source)
		borrowed = append(borrowed, source)
	}
	if source == nil {
		p.retireIdleLocked(now)
		p.owner.mu.Lock()
		if p.owner.closed || len(p.owner.sources) >= p.owner.sourceBudget() {
			p.owner.mu.Unlock()
			return
		}
		alias, err := nextMixedPacketAlias()
		if err != nil {
			p.owner.mu.Unlock()
			return
		}
		source = &mixedPacketSource{alias: alias, key: key, source: mixedSource{backend: name, remote: ap, identity: id, logical: logical}, packet: packet, remote: remote, association: association, origin: origin, lease: lease, stop: make(chan struct{}), changed: make(chan struct{}), done: make(chan struct{})}
		identity := key.association
		if identity == nil {
			identity = transportorigin.NewToken()
		}
		source.address = &mixedPacketAssociation{alias: alias, identity: identity, origin: origin, done: source.done, packet: p, source: source}
		source.source.capturedPeer = &mixedPacketPeer{association: source.address}
		p.owner.sources[alias] = source.source
		p.owner.mu.Unlock()
		p.aliases[key], p.sources[alias] = source, source
		if association != nil {
			p.associations[key] = source
		}
		p.borrowSourceLocked(source)
		borrowed = append(borrowed, source)
		lease = nil // source owns the admission until its actual cleanup
		if association != nil || origin != nil {
			p.wg.Add(1)
			go p.ownSource(source)
		}
	}
	if !p.sourceOpenLocked(source) {
		p.retireLocked(key, source)
		return
	}
	if origin != nil {
		publication, err = origin.AcquirePublication(ctx)
		if err != nil {
			p.retireLocked(key, source)
			return
		}
	}
	source.lastActive = now
	source.queued++
	p.in = append(p.in, mixedDatagram{data: append([]byte(nil), data...), source: source})
	p.signalReadLocked()
}

func (p *mixedPacket) read(name string, packet net.PacketConn) {
	defer p.wg.Done()
	buf := make([]byte, 65535)
	defer clear(buf)
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
		p.enqueueAddress(name, packet, remote, buf[:n])
		clear(buf[:n])
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
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			p.mu.Unlock()
			return 0, nil, os.ErrDeadlineExceeded
		}
		if len(p.in) != 0 {
			packet := p.in[0]
			copy(p.in, p.in[1:])
			p.in[len(p.in)-1] = mixedDatagram{}
			p.in = p.in[:len(p.in)-1]
			source := packet.source
			source.queued--
			p.borrowSourceLocked(source)
			p.mu.Unlock()
			n, address, err := p.deliver(source, packet.data, buf)
			clear(packet.data)
			p.releaseBorrow(source)
			if err != nil {
				continue
			}
			return n, address, nil
		}
		p.mu.Unlock()
		var timer *time.Timer
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
		select {
		case <-p.done:
		case <-wake:
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func (p *mixedPacket) deliver(source *mixedPacketSource, data, buf []byte) (int, net.Addr, error) {
	if err := p.checkSource(p.ctx, source); err != nil {
		p.mu.Lock()
		p.retireLocked(source.key, source)
		p.mu.Unlock()
		return 0, nil, err
	}
	p.mu.Lock()
	if !p.sourceOpenLocked(source) {
		p.mu.Unlock()
		return 0, nil, net.ErrClosed
	}
	var permit transportorigin.Lease
	if source.origin != nil {
		var err error
		permit, err = source.origin.AcquirePublication(p.ctx)
		if err != nil {
			p.mu.Unlock()
			return 0, nil, err
		}
	}
	// This bounded copy is the delivery commit. Seal cannot win between its
	// final admission and copy; a dequeued buffer stays charged through return.
	n := copy(buf, data)
	source.lastActive = time.Now()
	p.mu.Unlock()
	if permit != nil {
		permit.Release()
	}
	return n, source.address, nil
}

func (p *mixedPacket) WriteTo(buf []byte, remote net.Addr) (int, error) {
	address, ok := remote.(*mixedPacketAssociation)
	if !ok || address == nil {
		return 0, connectionroute.ErrDenied
	}
	owner, source := address.owner()
	if owner != p {
		return 0, connectionroute.ErrDenied
	}
	p.mu.Lock()
	if p.closed {
		err := p.closedErrorLocked()
		p.mu.Unlock()
		return 0, err
	}
	p.retireIdleLocked(time.Now())
	if !p.sourceOpenLocked(source) || p.peerFilter == nil {
		p.mu.Unlock()
		return 0, connectionroute.ErrDenied
	}
	packet, exact, associated := source.packet, source.remote, source.association != nil
	filter, version := p.peerFilter, p.filterVersion
	p.borrowSourceLocked(source)
	p.mu.Unlock()
	defer p.releaseBorrow(source)
	if !filter(source.source.logical) {
		return 0, connectionroute.ErrDenied
	}
	if err := p.checkSource(p.ctx, source); err != nil {
		p.mu.Lock()
		p.retireLocked(source.key, source)
		p.mu.Unlock()
		return 0, err
	}
	p.mu.Lock()
	valid := p.sourceOpenLocked(source) && version == p.filterVersion
	p.mu.Unlock()
	if !valid {
		return 0, connectionroute.ErrDenied
	}
	// The captured PacketConn receives the exact original association. A
	// formatted alias/tuple is never sufficient to select a new direct-LAN flow.
	n, err := packet.WriteTo(buf, exact)
	p.mu.Lock()
	if err == nil {
		source.lastActive = time.Now()
	} else if associated && mixedPacketUnavailable(err) {
		p.retireLocked(source.key, source)
	}
	p.mu.Unlock()
	if err != nil && !associated && mixedPacketUnavailable(err) {
		p.detach(source.source.backend, packet)
	}
	return n, err
}

func (p *mixedPacket) LocalAddr() net.Addr { return p.addr }
func (p *mixedPacket) stop(err error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.wg.Add(1) // every concurrent Close also joins physical listener closes
	p.closed, p.terminal = true, err
	packets := p.packets
	p.packets = map[string]net.PacketConn{}
	for key, source := range p.aliases {
		p.retireLocked(key, source)
	}
	close(p.done)
	p.mu.Unlock()
	defer p.wg.Done()
	p.cancel()
	for _, packet := range packets {
		_ = packet.Close()
	}
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
	if p.closed {
		err := p.closedErrorLocked()
		p.mu.Unlock()
		return err
	}
	p.wg.Add(1)
	defer p.wg.Done()
	p.writeDeadline = t
	p.writeVersion++
	packets := make(map[string]net.PacketConn, len(p.packets))
	for name, packet := range p.packets {
		packets[name] = packet
	}
	p.mu.Unlock()
	for name, packet := range packets {
		for {
			p.mu.Lock()
			deadline, version := p.writeDeadline, p.writeVersion
			attached := !p.closed && p.packets[name] == packet
			p.mu.Unlock()
			if !attached {
				break
			}
			if err := packet.SetWriteDeadline(deadline); err != nil {
				return err
			}
			p.mu.Lock()
			current := version == p.writeVersion
			p.mu.Unlock()
			if current {
				break
			}
		}
	}
	return nil
}
