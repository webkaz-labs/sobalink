//go:build endpoint_following_acceptance

package directlan

import (
	"context"
	"sync"

	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// AcceptanceSessionLog is observation only. Its private records retain no
// callable authorization, setter, publication callback or readiness override.
// Only one opt-in acceptance fixture may install it; tests must not be parallel.
type AcceptanceSessionLog struct {
	mu     sync.Mutex
	births map[*peerSession]bool
}

var acceptanceSessionObserver struct {
	sync.Mutex
	active *AcceptanceSessionLog
}

func ObserveAcceptanceSessions() (*AcceptanceSessionLog, error) {
	acceptanceSessionObserver.Lock()
	defer acceptanceSessionObserver.Unlock()
	if acceptanceSessionObserver.active != nil {
		return nil, ErrUnavailable
	}
	log := &AcceptanceSessionLog{births: make(map[*peerSession]bool)}
	acceptanceSessionObserver.active = log
	return log, nil
}
func (l *AcceptanceSessionLog) Close() {
	acceptanceSessionObserver.Lock()
	defer acceptanceSessionObserver.Unlock()
	if acceptanceSessionObserver.active == l {
		acceptanceSessionObserver.active = nil
	}
}
func observeAcceptanceSessionBirth(p *peerState) {
	acceptanceSessionObserver.Lock()
	defer acceptanceSessionObserver.Unlock()
	l := acceptanceSessionObserver.active
	if l == nil || p == nil || p.session == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// Called before the newly allocated peer is returned or exposed to a worker.
	// This synchronous observation adds mutex timing but never pauses on a test
	// channel, invokes a callback, performs I/O or changes the production owner.
	l.births[p.session] = !p.session.ready() && p.authenticated.Load() == nil
}

// AcceptanceGeneration retains the exact observed owner for identity/cleanup
// assertions. It cannot start, stop, publish, dial or grant application access.
type AcceptanceGeneration struct {
	g                   *runtimeGeneration
	session             *peerSession
	origin              *transportorigin.Token
	bornUnauthenticated bool
}

func (l *AcceptanceSessionLog) Capture(n *Node, key string) (*AcceptanceGeneration, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	g := n.generation.Load()
	p := n.peers[key]
	if g == nil || p == nil || p.g != g || p.session == nil {
		return nil, ErrUnavailable
	}
	l.mu.Lock()
	born, seen := l.births[p.session]
	l.mu.Unlock()
	if !seen {
		return nil, ErrUnavailable
	}
	return &AcceptanceGeneration{g: g, session: p.session, origin: g.origin.Identity(), bornUnauthenticated: born}, nil
}
func (a *AcceptanceGeneration) FreshFrom(old *AcceptanceGeneration) bool {
	return a != nil && old != nil && a.g != old.g && a.session != old.session && a.origin != old.origin && a.bornUnauthenticated
}

// MatchesRetirement compares only immutable attribution and the original
// transport-owned handle identity. It cannot select or stop another owner.
func (a *AcceptanceGeneration) MatchesRetirement(r *TransportRetirement) bool {
	return a != nil && r.valid() && r.g == a.g && r.g.origin.Identity() == a.origin
}
func (a *AcceptanceGeneration) WaitPhysicalJoin(ctx context.Context) error {
	if a == nil || a.g == nil {
		return ErrUnavailable
	}
	return a.g.wait(ctx)
}
func (a *AcceptanceGeneration) SessionReady() bool {
	return a != nil && a.session.ready()
}
