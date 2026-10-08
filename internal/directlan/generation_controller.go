package directlan

import (
	"context"
	"net"
	"net/netip"
	"sync"

	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// TransportRetirement owns the signal and observed completion of exactly one
// generation. Wait is resource-cleanup evidence only, never durable authority
// or certification that replacement activation is available. Keep the original
// pointer: copied/zero handles cannot select another generation.
type TransportRetirement struct{ g *runtimeGeneration }

func (r *TransportRetirement) valid() bool {
	return r != nil && r.g != nil && r.g.retirement == r
}
func (r *TransportRetirement) Origin() transportorigin.Origin {
	if !r.valid() {
		return nil
	}
	return r.g.origin
}

// RequestStop is signal-only and idempotent. A participant may request stop
// while holding a lease, but must release it before waiting for its own owner.
func (r *TransportRetirement) RequestStop() {
	if r.valid() {
		r.g.requestStop(ErrRecovery)
	}
}

// Wait observes the existing generation supervisor. Timeout changes neither
// owner state nor reservations and cannot authorize construction or publication.
func (r *TransportRetirement) Wait(ctx context.Context) error {
	if !r.valid() || ctx == nil {
		return ErrUnavailable
	}
	return r.g.wait(ctx)
}
func (r *TransportRetirement) completed() error {
	if !r.valid() {
		return ErrUnavailable
	}
	select {
	case <-r.g.done:
		// done publishes the immutable physical cleanup result.
		return r.g.result
	default:
		return ErrRetirementIncomplete
	}
}

// CaptureTransportOrigin captures attribution, not endpoint or application
// authority. Retain this exact facade for the subsequent retirement request.
func (n *Node) CaptureTransportOrigin() (transportorigin.Origin, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.readyLocked(); err != nil {
		return nil, err
	}
	g := n.generation.Load()
	if g == nil || !g.trafficOpen() {
		return nil, ErrUnavailable
	}
	return g.origin, nil
}

// BeginTransportRetirement seals only the exact selected current generation.
// Repeated calls return its one retained handle. Do not take Node.mu before the
// stop signal: an old synchronous send may own that lock and need socket wake.
func (n *Node) BeginTransportRetirement(expected transportorigin.Origin) (*TransportRetirement, error) {
	origin, ok := expected.(*generationOrigin)
	g := n.generation.Load()
	if !ok || origin == nil || g == nil || g.origin != origin || !g.traffic.Load() {
		return nil, ErrUntrusted
	}
	retirement := g.retirement
	retirement.RequestStop()
	return retirement, nil
}

// TransportEndpoints is an inert construction proposal, not a replacement
// grant. Identity, scope, budgets and persistence remain Node-owned. Peers may
// only omit existing peers or change their numeric endpoint within that scope;
// they cannot add identities or change keys/names. Proposals never modify the
// current peer map or grant a route, including for a previously omitted peer.
// Core must later supply exact reviewed durable authority before any activation.
type TransportEndpoints struct {
	Listen netip.AddrPort
	Peers  []Peer
}

// PreparedTransport owns one detached, non-admitting candidate. There is no
// publication method or Origin/peer capability export. Abort and WaitClosed
// manage resources only; this type cannot transfer authorization to a caller.
type PreparedTransport struct {
	mu      sync.Mutex // protects detachable build references against Abort
	self    *PreparedTransport
	node    *Node
	retired *TransportRetirement
	build   *generationBuild
	done    chan struct{}
	result  error // immutable after done
}

// PrepareTransport may bind only after the exact old physical owner succeeded.
// It never waits for retirement while holding a Node/authority mutex. Until a
// separately reviewed Core transaction and publication gate exist, every
// candidate remains staged; RetireTransport still returns its incomplete guard.
// Once registered, a non-nil candidate is returned even on construction failure
// so callers can join cleanup. Node retains the owner if that handle is dropped.
func (n *Node) PrepareTransport(ctx context.Context, retired *TransportRetirement, endpoints TransportEndpoints) (*PreparedTransport, error) {
	if n.contextControl || ctx == nil || n.cfg.protectedPairs() {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n.mu.Lock()
	if n.closed || n.closing.Load() {
		n.mu.Unlock()
		return nil, net.ErrClosed
	}
	if n.nonTransportRecovery {
		n.mu.Unlock()
		return nil, ErrRecovery
	}
	if !retired.valid() || retired.g.n != n || n.generation.Load() != retired.g || !n.started {
		n.mu.Unlock()
		return nil, ErrUntrusted
	}
	if err := retired.completed(); err != nil {
		n.mu.Unlock()
		return nil, err
	}
	if n.building.Load() != nil || n.staged.Load() != nil {
		n.mu.Unlock()
		return nil, ErrCapacity
	}
	cfg, err := n.candidateConfigLocked(retired, endpoints)
	if err != nil {
		n.mu.Unlock()
		return nil, err
	}
	candidate := &PreparedTransport{node: n, retired: retired, build: newGenerationBuild(ctx), done: make(chan struct{})}
	candidate.self = candidate
	build := candidate.build
	n.staged.Store(candidate)
	// The single supervisor exists before any construction resource. It keeps
	// the slot through actual cleanup, even after caller cancellation/timeout.
	go candidate.supervise()
	if n.closing.Load() {
		candidate.Abort()
	}
	go candidate.construct(cfg)
	n.mu.Unlock()

	select {
	case <-ctx.Done():
		candidate.Abort()
		return candidate, ctx.Err()
	case <-build.done:
		return candidate, build.err
	}
}

func (p *PreparedTransport) construct(cfg Config) {
	n := p.node
	var g *runtimeGeneration
	err := localAddressReady(cfg.Listen.Addr())
	if err == nil {
		g, err = n.buildTransportGeneration(p.build, cfg)
	}
	n.mu.Lock()
	if err == nil && (n.closed || n.closing.Load() || n.staged.Load() != p || n.generation.Load() != p.retired.g || n.nonTransportRecovery) {
		err = ErrRecovery
	}
	if err == nil {
		err = p.build.ctx.Err()
	}
	if err == nil && (g == nil || !g.open()) {
		err = ErrRecovery
	}
	p.build.err = err
	if err != nil {
		p.Abort()
	}
	close(p.build.done)
	n.mu.Unlock()
}

func (n *Node) candidateConfigLocked(retired *TransportRetirement, endpoints TransportEndpoints) (Config, error) {
	if n.cfg.protectedPairs() {
		return Config{}, ErrUnavailable
	}
	if !endpoints.Listen.IsValid() || endpoints.Listen.Addr().Is4() != retired.g.cfg.Listen.Addr().Is4() {
		return Config{}, ErrPolicy
	}
	cfg := cloneGenerationConfig(n.cfg)
	cfg.Listen = endpoints.Listen
	cfg.Peers = append([]Peer(nil), endpoints.Peers...)
	for _, peer := range cfg.Peers {
		previous := n.peers[peer.Key]
		if previous == nil || previous.g != retired.g || previous.peer.Key != peer.Key || previous.peer.TunnelKey != peer.TunnelKey || previous.peer.Name != peer.Name {
			return Config{}, ErrUntrusted
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	if cfg.PeerLimitCurrent != nil {
		limit := cfg.PeerLimitCurrent()
		if limit < 0 || limit > 0 && len(cfg.Peers) > limit {
			return Config{}, ErrCapacity
		}
	}
	return cfg, nil
}

// Abort never waits and never reopens the old transport. Resource close owners
// are idempotent; the existing generation supervisor owns device/stack cleanup.
func (p *PreparedTransport) Abort() {
	if p == nil || p.self != p {
		return
	}
	p.mu.Lock()
	build := p.build
	p.mu.Unlock()
	if build != nil {
		build.RequestStop()
	}
}
func (p *PreparedTransport) WaitClosed(ctx context.Context) error {
	if p == nil || p.self != p || p.done == nil || ctx == nil {
		return ErrUnavailable
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return p.result
	}
}

func (p *PreparedTransport) supervise() {
	build := p.build
	select {
	case <-build.ctx.Done():
		build.signalResources()
	case <-build.done:
		if g := build.generation.Load(); g != nil {
			select {
			case <-build.ctx.Done():
			case <-g.stop:
			}
		}
		build.RequestStop()
	}
	// Cancellation may have arrived before either resource pointer was stored.
	// Construction's own checks signal late resources; this final signal and
	// join observe that same exact owner after setup has actually returned.
	<-build.done
	build.signalResources()
	result := build.wait(context.Background())
	n := p.node
	n.mu.Lock()
	p.result = result
	if result == nil {
		n.staged.CompareAndSwap(p, nil)
		// A retained completed handle is inert. It does not accumulate closed
		// devices/configuration/Node graphs across successfully cleaned retries.
		p.mu.Lock()
		p.build, p.node, p.retired = nil, nil, nil
		p.mu.Unlock()
	} else {
		// No next candidate may hide failed physical cleanup. Retain the slot
		// and the distinct non-transport latch; a timeout never clears either.
		n.recovery = true
		n.nonTransportRecovery = true
	}
	close(p.done)
	n.mu.Unlock()
}
