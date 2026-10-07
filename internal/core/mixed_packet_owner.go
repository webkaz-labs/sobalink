package core

import (
	"context"
	"net"
	"net/netip"
	"sync"

	"github.com/webkaz-labs/sobalink/internal/connectionroute"
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// mixedPacketAssociation is an immutable, listener-scoped return capability.
// Cleanup detaches its live owner; the alias, tokens and terminal acknowledgement
// remain valid tombstones and can never resolve a replacement source.
type mixedPacketAssociation struct {
	alias    netip.AddrPort
	identity *transportorigin.Token
	origin   transportorigin.Origin
	done     <-chan struct{}
	mu       sync.Mutex
	packet   *mixedPacket
	source   *mixedPacketSource
}

func (a *mixedPacketAssociation) Network() string { return "udp" }
func (a *mixedPacketAssociation) String() string  { return a.alias.String() }
func (a *mixedPacketAssociation) AssociationIdentity() *transportorigin.Token {
	return a.identity
}
func (a *mixedPacketAssociation) TransportOrigin() transportorigin.Origin { return a.origin }
func (a *mixedPacketAssociation) owner() (*mixedPacket, *mixedPacketSource) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.packet, a.source
}
func (a *mixedPacketAssociation) detach() {
	a.mu.Lock()
	a.packet, a.source = nil, nil
	a.mu.Unlock()
}
func (a *mixedPacketAssociation) Close() error {
	p, source := a.owner()
	if p != nil {
		p.mu.Lock()
		p.retireLocked(source.key, source)
		p.mu.Unlock()
	}
	return nil
}
func (a *mixedPacketAssociation) WaitClosed(ctx context.Context) error {
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (a *mixedPacketAssociation) peerIdentity(raw bool) (string, bool) {
	p, source := a.owner()
	if p == nil {
		return "", false
	}
	p.mu.Lock()
	if !p.sourceOpenLocked(source) {
		p.mu.Unlock()
		return "", false
	}
	p.borrowSourceLocked(source)
	p.mu.Unlock()
	defer p.releaseBorrow(source)
	if err := p.checkSource(p.ctx, source); err != nil {
		p.mu.Lock()
		p.retireLocked(source.key, source)
		p.mu.Unlock()
		return "", false
	}
	p.mu.Lock()
	valid := p.sourceOpenLocked(source)
	p.mu.Unlock()
	if !valid {
		return "", false
	}
	if raw {
		return source.source.identity, true
	}
	return source.source.logical, true
}
func (a *mixedPacketAssociation) PeerIdentity() (string, bool) { return a.peerIdentity(false) }

// mixedBackend.WhoIs must also enter the old source's counted identity gate;
// retaining a raw association in its registry would bypass that gate.
type mixedPacketPeer struct{ association *mixedPacketAssociation }

func (p *mixedPacketPeer) PeerIdentity() (string, bool) { return p.association.peerIdentity(true) }

var _ transportorigin.PacketAssociation = (*mixedPacketAssociation)(nil)

func (p *mixedPacket) sourceOpenLocked(source *mixedPacketSource) bool {
	if p.closed || source.stopped || p.sources[source.alias] != source || p.packets[source.source.backend] != source.packet {
		return false
	}
	if source.origin != nil {
		select {
		case <-source.origin.StopRequested():
			return false
		default:
		}
	}
	return true
}

// The source borrow or admission lease is already held by the caller. Exact
// association evidence is never recreated through the current backend node.
func (p *mixedPacket) checkSource(ctx context.Context, source *mixedPacketSource) error {
	var id string
	if source.association != nil {
		var valid bool
		id, valid = source.association.PeerIdentity()
		if !valid {
			return connectionroute.ErrDenied
		}
	} else {
		identityCtx, cancel := context.WithTimeout(ctx, mixedPacketIdentityTimeout)
		var err error
		id, err = p.owner.nodes[source.source.backend].WhoIs(identityCtx, source.source.remote)
		cancel()
		if err != nil {
			return err
		}
	}
	if id != source.source.identity || p.owner.logical(source.source.backend, id) != source.source.logical {
		return connectionroute.ErrDenied
	}
	return nil
}

func (p *mixedPacket) signalSourceLocked(source *mixedPacketSource) {
	close(source.changed)
	source.changed = make(chan struct{})
}
func (p *mixedPacket) signalReadLocked() {
	close(p.wake)
	p.wake = make(chan struct{})
}
func (p *mixedPacket) borrowSourceLocked(source *mixedPacketSource) {
	source.borrowing++
	p.wg.Add(1)
}
func (p *mixedPacket) releaseBorrow(source *mixedPacketSource) {
	defer p.wg.Done()
	p.mu.Lock()
	source.borrowing--
	p.signalSourceLocked(source)
	if source.stopped && source.association == nil && source.lease == nil && source.borrowing == 0 {
		p.finishSourceLocked(source)
		close(source.done)
	}
	p.mu.Unlock()
}

// Caller holds p.mu. Source capacity is retained until every queue/delivery,
// callback and reverse writer has returned and the underlying association has
// acknowledged its own reader/queue/endpoint cleanup.
func (p *mixedPacket) retireLocked(key mixedPacketKey, source *mixedPacketSource) {
	if source.stopped {
		return
	}
	source.stopped = true
	if p.aliases[key] == source {
		delete(p.aliases, key)
	}
	kept := p.in[:0]
	for _, packet := range p.in {
		if packet.source == source {
			source.queued--
			clear(packet.data)
		} else {
			kept = append(kept, packet)
		}
	}
	clear(p.in[len(kept):])
	p.in = kept
	close(source.stop)
	p.signalSourceLocked(source)
	p.signalReadLocked()
	if source.association == nil && source.lease == nil && source.borrowing == 0 {
		p.finishSourceLocked(source)
		close(source.done)
	}
}

func (p *mixedPacket) finishSourceLocked(source *mixedPacketSource) {
	delete(p.sources, source.alias)
	if p.associations[source.key] == source {
		delete(p.associations, source.key)
	}
	p.owner.mu.Lock()
	delete(p.owner.sources, source.alias)
	p.owner.mu.Unlock()
	source.address.detach()
	source.packet, source.remote, source.association = nil, nil, nil
}

func (p *mixedPacket) ownSource(source *mixedPacketSource) {
	defer p.wg.Done()
	association := source.association
	var terminal chan error
	if association != nil {
		terminal = make(chan error, 1)
		// This single waiter is owned and joined here, including natural flow
		// completion. A retained listener does not signal association closure.
		go func() { terminal <- association.WaitClosed(context.Background()) }()
	}
	var stopped, cancelled <-chan struct{}
	if source.origin != nil {
		stopped = source.origin.StopRequested()
	}
	if source.lease != nil {
		cancelled = source.lease.Context().Done()
	}
	var terminalErr error
	terminalObserved := false
	select {
	case <-source.stop:
	case <-stopped:
	case <-cancelled:
	case <-p.ctx.Done():
	case terminalErr = <-terminal:
		terminalObserved = true
	}
	p.mu.Lock()
	p.retireLocked(source.key, source)
	p.mu.Unlock()
	if association != nil {
		_ = association.Close()
	}
	for {
		p.mu.Lock()
		quiet, changed := source.borrowing == 0 && source.queued == 0, source.changed
		p.mu.Unlock()
		if quiet {
			break
		}
		<-changed
	}
	if terminal != nil && !terminalObserved {
		terminalErr = <-terminal
	}
	if terminalErr != nil {
		// Missing terminal evidence keeps the same owner and charge pending.
		select {}
	}
	p.mu.Lock()
	lease := source.lease
	p.finishSourceLocked(source)
	source.lease = nil
	p.mu.Unlock()
	if lease != nil {
		lease.Release()
	}
	close(source.done)
}

func (p *mixedPacket) packetRecovering(name string) bool {
	backend, ok := p.owner.nodes[name].(interface{ TransportRecovering() bool })
	return ok && backend.TransportRecovering()
}

// Capturing an origin without the association that owns its return address is
// never downgraded to tuple-based routing.
func mixedPacketOrigin(address net.Addr) (transportorigin.PacketAssociation, transportorigin.Origin, error) {
	association, associated := address.(transportorigin.PacketAssociation)
	carrier, carried := address.(transportorigin.Carrier)
	var origin transportorigin.Origin
	if carried {
		origin = carrier.TransportOrigin()
	}
	if origin != nil && (!associated || origin.Identity() == nil) {
		return nil, nil, transportorigin.ErrMissingOrigin
	}
	if associated && association.AssociationIdentity() == nil {
		return nil, nil, transportorigin.ErrMissingOrigin
	}
	return association, origin, nil
}
