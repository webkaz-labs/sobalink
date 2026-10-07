package core

import (
	"context"
	"net"
	"sync"

	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

type peerHTTPApplicationKey struct{}

// An enclosing operation keeps its origins until its own publication and
// callbacks finish. Sequential pages cannot silently migrate to a new G.
// Discovery uses bounded publication permits; message/binding transactions and
// transfer runs separately own their durable/terminal completion obligations.
type peerHTTPApplication struct {
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	origins        []peerHTTPApplicationOrigin
	release        func()
	finished       bool
	cleanupDone    bool
	requestsSealed bool
	requests       map[*peerHTTPRequest]struct{}
	requestChanged chan struct{}
}

type peerHTTPApplicationOrigin struct {
	backend string
	origin  transportorigin.Origin
	lease   transportorigin.Lease
	stop    func() bool
	joined  <-chan struct{}
}

func (c *Core) peerHTTPApplication(ctx context.Context) (context.Context, *peerHTTPApplication, error) {
	front, err := c.peerHTTPFront()
	if err != nil {
		return nil, nil, err
	}
	release, err := front.reserve(peerHTTPApplicationLease, c.limit("resources", "tcpConnections"), 0, "")
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	a := &peerHTTPApplication{ctx: ctx, cancel: cancel, release: release, requests: make(map[*peerHTTPRequest]struct{}), requestChanged: make(chan struct{})}
	return context.WithValue(ctx, peerHTTPApplicationKey{}, a), a, nil
}

func (a *peerHTTPApplication) bind(route *peerHTTPRoute) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.checkLocked(route); err != nil {
		return err
	}
	for _, current := range a.origins {
		if current.backend == route.backend && route.origin != nil {
			return nil
		}
	}
	if route.origin == nil {
		return nil
	}
	// There are only three supported actual backends. The application never
	// keeps an unbounded history of superseded generations or routes.
	if len(a.origins) >= 3 {
		return errPeerHTTPCapacity
	}
	lease, err := route.origin.Acquire(a.ctx)
	if err != nil {
		return err
	}
	joined := make(chan struct{})
	cancel := a.cancel
	stop := context.AfterFunc(lease.Context(), func() { cancel(); close(joined) })
	a.origins = append(a.origins, peerHTTPApplicationOrigin{route.backend, route.origin, lease, stop, joined})
	return nil
}

func (a *peerHTTPApplication) check(route *peerHTTPRoute) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.checkLocked(route)
}

func (a *peerHTTPApplication) checkLocked(route *peerHTTPRoute) error {
	if a.finished || a.requestsSealed || a.ctx.Err() != nil {
		return net.ErrClosed
	}
	for _, current := range a.origins {
		if channelClosed(current.origin.StopRequested()) {
			return net.ErrClosed
		}
		if current.backend == route.backend {
			if route.origin == nil || current.origin.Identity() != route.origin.Identity() {
				return net.ErrClosed
			}
		}
	}
	return nil
}

// Register before a request can dial or escape to a cleanup worker. Its front
// request reservation is already held. Completed entries are removed before
// that reservation becomes reusable, so this is a finite active set, not a
// history of submissions or timed-out requests.
func (a *peerHTTPApplication) registerRequest(h *peerHTTPRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished || a.requestsSealed || a.ctx.Err() != nil {
		return net.ErrClosed
	}
	a.requests[h] = struct{}{}
	return nil
}

// Called after source/body close, connection acknowledgement, origin release
// and H's raw-reference detachment, with no H/connection/origin mutex held.
// Only inert front/Core accounting remains in the request cleanup tail.
func (a *peerHTTPApplication) requestCleaned(h *peerHTTPRequest) {
	a.mu.Lock()
	if _, present := a.requests[h]; !present {
		a.mu.Unlock()
		return
	}
	delete(a.requests, h)
	close(a.requestChanged)
	a.requestChanged = make(chan struct{})
	var release func()
	if a.cleanupDone && len(a.requests) == 0 {
		release, a.release = a.release, nil
	}
	a.mu.Unlock()
	if release != nil {
		release()
	}
}

// A transfer must join its physical source owners before removing its spool.
// Seal/cancel first, then wait without application, batch, Core or origin locks.
// A stalled cleanup retains both the run and its capacity; time cannot certify
// source closure. This join is distinct from a caller's bounded request wait.
func (a *peerHTTPApplication) waitRequests() {
	a.mu.Lock()
	a.requestsSealed = true
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for {
		a.mu.Lock()
		quiet, changed := len(a.requests) == 0, a.requestChanged
		a.mu.Unlock()
		if quiet {
			return
		}
		<-changed
	}
}

// admitCommit admits one precomputed durable operation while the caller holds
// its application authority mutex. Ordinary leases own the I/O and outcome
// reconciliation; they do not hold any generation mutex across that work.
// Cancellation after admission cannot undo a committed atomic replacement.
func (a *peerHTTPApplication) admitCommit() (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished || a.requestsSealed || a.ctx.Err() != nil {
		return nil, net.ErrClosed
	}
	var leases []transportorigin.Lease
	release := func() {
		for _, lease := range leases {
			lease.Release()
		}
	}
	for _, current := range a.origins {
		lease, err := current.origin.Acquire(a.ctx)
		if err != nil {
			release()
			return nil, err
		}
		leases = append(leases, lease)
	}
	return release, nil
}

// publication is called with Core.mu held. Each permit starts at the same
// generation fence as seal and covers only the following precomputed map copy.
func (a *peerHTTPApplication) publication() (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished || a.ctx.Err() != nil {
		return nil, net.ErrClosed
	}
	var permits []transportorigin.Lease
	release := func() {
		for _, permit := range permits {
			permit.Release()
		}
	}
	for _, current := range a.origins {
		permit, err := current.origin.AcquirePublication(a.ctx)
		if err != nil {
			release()
			return nil, err
		}
		permits = append(permits, permit)
	}
	return release, nil
}

func (a *peerHTTPApplication) finish() {
	a.finishWithTerminal(nil)
}

// terminal is an enclosing run's final bounded in-memory cleanup publication.
// It runs after cancellation callbacks join, while origin leases still account
// for the operation. It must not start I/O or another operation. No application,
// origin or generation lock is held while it takes the run's authority mutex.
func (a *peerHTTPApplication) finishWithTerminal(terminal func()) {
	a.mu.Lock()
	if a.finished {
		a.mu.Unlock()
		return
	}
	a.finished = true
	a.requestsSealed = true
	origins := a.origins
	a.origins = nil
	cancel := a.cancel
	a.mu.Unlock()
	cancel()
	for _, origin := range origins {
		if !origin.stop() {
			<-origin.joined
		}
	}
	if terminal != nil {
		terminal()
	}
	for _, origin := range origins {
		origin.lease.Release()
	}
	a.mu.Lock()
	a.cleanupDone = true
	a.ctx, a.cancel = nil, nil
	var release func()
	if len(a.requests) == 0 {
		release, a.release = a.release, nil
	}
	a.mu.Unlock()
	if release != nil {
		release()
	}
}
