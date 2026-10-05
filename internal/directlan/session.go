package directlan

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/tailscale/wireguard-go/device"
)

// A single deterministic initiator avoids simultaneous application-triggered
// fresh WireGuard handshakes. The responder waits for an
// authenticated transport packet, not merely a derived next keypair.
type peerSession struct {
	initiator bool
	state     atomic.Uint32
	confirmed atomic.Bool
	changed   chan struct{}
	gate      chan struct{}
}

func newPeerSession(initiator bool) *peerSession {
	return &peerSession{initiator: initiator, changed: make(chan struct{}, 1), gate: make(chan struct{}, 1)}
}
func (s *peerSession) notify() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}
func (s *peerSession) transition(state device.PeerSessionState) {
	s.confirmed.Store(false)
	s.state.Store(uint32(state))
	s.notify()
}
func (s *peerSession) confirm() { s.confirmed.Store(true); s.notify() }
func (s *peerSession) ready() bool {
	return s != nil && device.PeerSessionState(s.state.Load()) == device.PeerSessionEstablished && (s.initiator || s.confirmed.Load())
}
func (n *Node) newPeerState(p Peer) *peerState {
	return &peerState{peer: p, session: newPeerSession(n.PublicKey() < p.Key)}
}
func (n *Node) currentSessionPeer(p *peerState) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.readyLocked() == nil && p != nil && n.peers[p.peer.Key] == p
}
func (n *Node) initiateSession(p *peerState) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.readyLocked() != nil || p == nil || n.peers[p.peer.Key] != p || p.session == nil || !p.session.initiator {
		return ErrUntrusted
	}
	if p.enginePeer == nil {
		return ErrUnavailable
	}
	// An authenticated responder may have restarted while our old key is still
	// valid. Request a fresh handshake even then; the engine's rate limit stays
	// intact. The current generation is held until this synchronous send ends.
	// Session callbacks only touch atomics and never acquire Node.mu.
	return p.enginePeer.SendHandshakeInitiation(false)
}
func (n *Node) requestSession(ctx context.Context, p *peerState) error {
	c, w, e := n.connect(ctx, p.peer, p)
	if e != nil {
		return e
	}
	defer n.removeWire(w)
	stop := context.AfterFunc(ctx, func() { w.raw.Close() })
	defer stop()
	if e = writeJSON(c, request{Version: 1, Operation: "session"}); e != nil {
		return e
	}
	var reply response
	if e = readJSON(c, &reply); e != nil {
		return e
	}
	if reply.Version != 1 || !reply.OK || reply.Code != "" || reply.Peer != nil {
		return ErrUntrusted
	}
	return nil
}
func (n *Node) ensureSession(ctx context.Context, p *peerState) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if p == nil || p.session == nil {
		return ErrUntrusted
	}
	s := p.session
	if !n.currentSessionPeer(p) {
		return ErrUntrusted
	}
	if s.ready() {
		return nil
	}
	// Concurrent calls share the same bounded activation; a caller timeout does
	// not discard the WireGuard session or cancel another caller's work.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.gate <- struct{}{}:
	}
	defer func() { <-s.gate }()
	if e := ctx.Err(); e != nil {
		return e
	}
	if !n.currentSessionPeer(p) {
		return ErrUntrusted
	}
	if s.ready() {
		return nil
	}
	var e error
	if s.initiator {
		e = n.initiateSession(p)
	} else {
		e = n.requestSession(ctx, p)
	}
	if e != nil {
		return e
	}
	var retry <-chan time.Time
	var timer *time.Ticker
	if !s.initiator {
		// An immediate remote restart can hit WireGuard's existing five-second
		// send rate limit. A bounded control retry shares this caller's deadline.
		timer = time.NewTicker(device.RekeyTimeout)
		defer timer.Stop()
		retry = timer.C
	}
	for {
		if !n.currentSessionPeer(p) {
			return ErrUntrusted
		}
		if s.ready() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.changed:
		case <-retry:
			if e := n.requestSession(ctx, p); e != nil {
				return e
			}
		}
	}
}
