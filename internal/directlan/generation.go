package directlan

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/tailscale/wireguard-go/device"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

// ErrRetirementIncomplete means local transport cleanup alone cannot certify
// the retained application/session participants required for replacement.
var ErrRetirementIncomplete = errors.New("direct-LAN retirement participants incomplete")

// runtimeGeneration is never reopened or rebound. Node owns the stable service
// entrances; this object owns exactly one disposable transport. No replacement
// publication path is provided here.
type runtimeGeneration struct {
	n                           *Node
	cfg                         Config
	peers                       map[string]*peerState
	underlay                    *generationUnderlay
	published                   chan struct{}
	traffic                     atomic.Bool
	controlOpen                 atomic.Bool
	controlPeers                map[string]*contextPeerRegistration
	failedControl               map[*controlStream]error
	bind                        *lanBind
	tunnel                      *userspaceTunnel
	engine                      atomic.Pointer[device.OwnedDevice]
	registrationTarget          atomic.Pointer[peerRegistrationTarget]
	origin                      *generationOrigin
	retirement                  *TransportRetirement
	mu                          sync.Mutex
	sealed                      bool
	cause                       error
	changed                     chan struct{}
	stop                        chan struct{}
	built                       chan struct{}
	done                        chan struct{}
	result                      error
	work                        map[*generationWork]struct{}
	creators                    map[*endpointCreator]struct{}
	live                        map[tcpip.Endpoint]*liveEndpoint
	endpointLimit, controlLimit int
	controlCount                int
	incoming                    *tcpAdmissions
	// The owned WG registry bounds these exact old registrations by MaxPeers.
	peerRegistrations map[device.PeerRegistration]*peerState
}

type peerRegistrationTarget struct {
	key     device.NoisePublicKey
	session *peerSession
}

type generationWork struct {
	g       *runtimeGeneration
	cancel  context.CancelFunc
	control bool
	once    sync.Once
}

func newRuntimeGeneration(n *Node, b *lanBind, t *userspaceTunnel) *runtimeGeneration {
	cfg := cloneGenerationConfig(n.cfg)
	g := &runtimeGeneration{n: n, cfg: cfg, peers: make(map[string]*peerState), published: make(chan struct{}), bind: b, tunnel: t, changed: make(chan struct{}), stop: make(chan struct{}), built: make(chan struct{}), done: make(chan struct{}), work: make(map[*generationWork]struct{}), creators: make(map[*endpointCreator]struct{}), live: make(map[tcpip.Endpoint]*liveEndpoint), endpointLimit: n.cfg.FlowLimit, controlLimit: n.cfg.ControlLimit, incoming: newTCPAdmissions(n.cfg.FlowLimit), peerRegistrations: make(map[device.PeerRegistration]*peerState)}
	g.origin = newGenerationOrigin(g)
	g.retirement = &TransportRetirement{g: g}
	return g
}

// admit serializes every registration/handoff with the WG terminal gate. The
// callback performs bookkeeping only, with lock order WG owner -> generation.
func (g *runtimeGeneration) admit(f func() bool) bool {
	run := func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return !g.sealed && f()
	}
	if engine := g.engine.Load(); engine != nil {
		return engine.Admit(run)
	}
	// The immutable TCP-only mode intentionally has no WG owner. Ordinary
	// generations reach this branch only before engine construction.
	return run()
}
func (g *runtimeGeneration) trafficOpen() bool { return g != nil && g.traffic.Load() && g.open() }
func (g *runtimeGeneration) open() bool        { return g != nil && g.admit(func() bool { return true }) }
func (g *runtimeGeneration) changedLocked()    { close(g.changed); g.changed = make(chan struct{}) }
func (g *runtimeGeneration) seal(cause error) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if !g.sealed {
		g.sealed = true
		g.controlOpen.Store(false)
		g.cause = cause
		close(g.stop)
		g.changedLocked()
	}
	g.mu.Unlock()
}
func (g *runtimeGeneration) requestStop(cause error) {
	if g == nil {
		return
	}
	g.seal(cause)
	if engine := g.engine.Load(); engine != nil {
		engine.RequestStop(cause)
	}
}
func (g *runtimeGeneration) acquireWork(cancel context.CancelFunc, control bool) (*generationWork, error) {
	w := &generationWork{g: g, cancel: cancel, control: control}
	capacity := false
	if !g.admit(func() bool {
		if control && g.controlCount >= g.controlLimit {
			capacity = true
			return false
		}
		if control {
			g.controlCount++
		}
		g.work[w] = struct{}{}
		return true
	}) {
		if capacity {
			return nil, ErrCapacity
		}
		return nil, ErrRecovery
	}
	return w, nil
}
func (w *generationWork) finish() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
		w.g.mu.Lock()
		delete(w.g.work, w)
		if w.control {
			w.g.controlCount--
		}
		w.g.changedLocked()
		w.g.mu.Unlock()
	})
}

// supervise is registered once before WG construction. Socket/TUN wake always
// precedes Node.mu: an admitted handshake send may be holding that mutex.
func (g *runtimeGeneration) supervise() {
	<-g.stop
	if g.n.contextControl {
		// TCP-only construction never waits on an engine. Publish optional
		// resources before reading them, including a cancelled late listen.
		<-g.built
	}
	if g.underlay != nil {
		g.underlay.RequestClose()
	}
	var bindErr error
	if g.bind != nil {
		bindErr = g.bind.SealAndClose()
	}
	if g.tunnel != nil {
		_ = g.tunnel.Close()
	}
	<-g.built
	g.mu.Lock()
	var cancel []context.CancelFunc
	for w := range g.work {
		if w.cancel != nil {
			cancel = append(cancel, w.cancel)
		}
	}
	for c := range g.creators {
		cancel = append(cancel, c.cancel)
	}
	var endpoints []*liveEndpoint
	for _, e := range g.live {
		endpoints = append(endpoints, e)
	}
	g.mu.Unlock()
	for _, f := range cancel {
		f()
	}
	for _, e := range endpoints {
		e.requestClose()
	}
	g.n.mu.Lock()
	if g.n.generation.Load() == g {
		g.n.recovery = true
	}
	var wires []*wire
	for w := range g.n.wires {
		if w.g == g {
			wires = append(wires, w)
		}
	}
	var listeners []*listener
	for _, l := range g.n.listeners {
		listeners = append(listeners, l)
	}
	g.n.mu.Unlock()
	for _, w := range wires {
		if w.flow != nil {
			_ = w.flow.Close()
		} else {
			_ = w.raw.Close()
		}
	}
	for _, l := range listeners {
		l.retireGeneration(g)
	}

	// External work, creator cleanup, ingress and borrowed calls all retain their
	// registrations until their actual return. A caller timeout never changes
	// this predicate, clears a registry or starts another supervisor.
	for {
		g.mu.Lock()
		quiet := len(g.work) == 0 && len(g.creators) == 0 && len(g.live) == 0
		changed := g.changed
		g.mu.Unlock()
		if quiet {
			break
		}
		<-changed
	}
	var underlayErr error
	if g.underlay != nil {
		underlayErr = g.underlay.WaitClosed(context.Background())
	}
	var engineErr error
	if engine := g.engine.Load(); engine != nil {
		engine.RequestStop(g.cause)
		engineErr = engine.WaitStopped(context.Background())
	}
	if bindErr == nil && engineErr == nil && underlayErr == nil {
		// Admission is permanently sealed and all creators/ingress have exited.
		// The adapter remains allocated for late inert references.
		if g.tunnel != nil {
			g.tunnel.destroySealed()
		}
	}
	g.mu.Lock()
	var controlErr error
	for _, err := range g.failedControl {
		controlErr = errors.Join(controlErr, err)
	}
	g.result = errors.Join(bindErr, engineErr, underlayErr, controlErr)
	g.mu.Unlock()
	if g.result == nil {
		g.origin.detach()
	}
	close(g.done)
}
func (g *runtimeGeneration) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return errors.Join(ErrRecovery, ctx.Err())
	case <-g.done:
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.result
	}
}

// RetireTransport permanently stops this Node's current transport while keeping
// logical listeners/fallback registration. It does not publish a replacement,
// certify external application/bridge completion, renew authority, or close the
// stable fronts. Concurrent callers observe the same retirement owner. Until
// retained outer-session participation is integrated, it deliberately returns
// ErrRetirementIncomplete even when this local transport has finished cleanup.
func (n *Node) RetireTransport(ctx context.Context) error {
	g := n.generation.Load()
	if g == nil {
		return ErrUnavailable
	}
	g.retirement.RequestStop()
	return errors.Join(ErrRecovery, ErrRetirementIncomplete, g.retirement.Wait(ctx))
}

// endpointCreator reserves both a creator and its potential endpoint before
// allocation. Only the creator cleans an unhanded endpoint. The live owner is
// installed atomically before a successful handoff can return.
type endpointCreator struct {
	g                        *runtimeGeneration
	ctx                      context.Context
	done                     <-chan struct{}
	cancel                   context.CancelFunc
	ep                       tcpip.Endpoint
	live                     *liveEndpoint
	requestDone, handlerDone bool
	forwarder                bool
}

func (g *runtimeGeneration) acquireCreator(ctx context.Context, forwarder bool) (*endpointCreator, error) {
	run, cancel := context.WithTimeout(ctx, handshakeTimeout)
	c := &endpointCreator{g: g, ctx: run, done: run.Done(), cancel: cancel, forwarder: forwarder}
	capacity := false
	if !g.admit(func() bool {
		// Request/creator slots are distinct from transferred endpoint slots.
		// A returned endpoint cannot release a still-running handler's ticket.
		if len(g.creators) >= g.endpointLimit {
			capacity = true
			return false
		}
		reserved := len(g.live)
		for existing := range g.creators {
			if existing.live == nil {
				reserved++
			}
		}
		if reserved >= g.endpointLimit {
			capacity = true
			return false
		}
		g.creators[c] = struct{}{}
		return true
	}) {
		cancel()
		if capacity {
			return nil, ErrCapacity
		}
		return nil, ErrRecovery
	}
	return c, nil
}
func (c *endpointCreator) Context() context.Context { return c.ctx }
func (c *endpointCreator) PublishEndpoint(ep tcpip.Endpoint) {
	c.g.mu.Lock()
	defer c.g.mu.Unlock()
	// The upstream owned creator calls once after allocation. Publication is
	// bookkeeping even after sealing, and never grants endpoint authority.
	c.ep = ep
}
func (c *endpointCreator) TryHandoff(ep tcpip.Endpoint) bool {
	ok := c.g.admit(func() bool {
		// Done was captured before admission. Context methods may acquire a
		// context mutex or wait, so none are called under WG owner.mu -> G.mu.
		select {
		case <-c.done:
			return false
		default:
		}
		if ep == nil || c.ep != ep || c.live != nil {
			return false
		}
		c.live = newLiveEndpoint(c.g, ep)
		c.g.live[ep] = c.live
		return true
	})
	if ok {
		go c.live.supervise()
	}
	return ok
}
func (c *endpointCreator) completeLocked() {
	if !c.requestDone || !c.handlerDone {
		return
	}
	delete(c.g.creators, c)
	c.ep = nil
	c.g.changedLocked()
}
func (c *endpointCreator) RequestCompleted() {
	c.cancel()
	c.g.mu.Lock()
	c.requestDone = true
	c.completeLocked()
	c.g.mu.Unlock()
}
func (c *endpointCreator) HandlerReturned() {
	c.g.mu.Lock()
	c.handlerDone = true
	c.completeLocked()
	c.g.mu.Unlock()
}
func (c *endpointCreator) finishOutgoing() {
	// Called only after cleanup returned, or the exact live owner took over.
	c.RequestCompleted()
	c.HandlerReturned()
}
func (g *runtimeGeneration) endpoint(ep tcpip.Endpoint) *liveEndpoint {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.live[ep]
}

var _ tcp.ForwarderRequestLease = (*endpointCreator)(nil)
