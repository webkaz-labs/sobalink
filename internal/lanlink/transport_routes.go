package lanlink

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
	"tailscale.com/feature/buildfeatures"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/logger"
)

const routeAttemptTimeout = 5 * time.Second
const routeRetryHoldDown = 30 * time.Second

// transportGate serializes one runtime's preparation and network operations.
// Its zero value is usable. Waiting callers select on their own cancellation;
// a single bounded channel, rather than a goroutine per waiter, owns admission.
type transportGate struct {
	once sync.Once
	held chan struct{}
}

func (g *transportGate) LockContext(ctx context.Context) error {
	g.once.Do(func() { g.held = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case g.held <- struct{}{}:
		// Cancellation and admission can become ready together. Never start
		// an operation for a caller whose deadline has already elapsed.
		if err := ctx.Err(); err != nil {
			g.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *transportGate) Lock()   { _ = g.LockContext(context.Background()) }
func (g *transportGate) Unlock() { <-g.held }

type peerTransport interface {
	Ping(context.Context) (tailcat.PingResult, error)
	DiscoPing(context.Context) (*ipnstate.PingResult, error)
	DialTCP(context.Context, netip.AddrPort) (net.Conn, error)
	DialUDP(context.Context, netip.AddrPort) (tailcat.ConnPacketConn, error)
	Close() error
}

func legacyCandidate(relay TrustedRelay) RouteCandidate {
	scope := "external"
	if relay.Address.Addr().IsPrivate() || relay.Address.Addr().IsLoopback() {
		scope = "local"
	}
	return RouteCandidate{Relay: relay, Scope: scope}
}

func configuredRegions(cfg NodeConfig) ([]*tailcfg.DERPRegion, error) {
	candidates := slices.Clone(cfg.Candidates)
	if len(candidates) == 0 {
		candidates = []RouteCandidate{legacyCandidate(cfg.Relay)}
	}
	resources, err := cfg.RelayResources.WithDefaults()
	if err != nil {
		return nil, err
	}
	if err := tailcat.ValidateRelayPresenceBudget(len(candidates), resources.PresenceConnections); err != nil {
		return nil, err
	}
	if cfg.PrivateOnly && buildfeatures.HasUDPTransport {
		return nil, tailcat.ErrPrivateOnlyBuild
	}
	anchor := false
	seen := make(map[netip.AddrPort]bool)
	regions := make([]*tailcfg.DERPRegion, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Validate() != nil || seen[candidate.Relay.Address] || cfg.PrivateOnly && candidate.Scope != "local" {
			return nil, ErrRouteUpdate
		}
		seen[candidate.Relay.Address] = true
		if err := cfg.DestinationPolicy.CheckRelay(candidate.Relay.Address); err != nil {
			return nil, err
		}
		anchor = anchor || candidate.Relay == cfg.Relay
		regions = append(regions, candidate.Relay.region())
	}
	if !anchor {
		return nil, errors.New("configured candidates must include the unchanged pairing relay")
	}
	return regions, nil
}

// prepare freezes a single runtime generation's authority. Route mutations
// replace remoteClient rather than mutating its active transport underneath it.
// The caller copies remote state under n.mu, then releases n.mu before waiting
// for startMu, so unrelated status and trust operations never wait on network I/O.
func (r *remoteClient) prepare(anchor TrustedRelay, privateOnly bool, snapshot RouteSnapshot) error {
	return r.prepareRemote(context.Background(), r.remote, anchor, privateOnly, snapshot)
}
func (r *remoteClient) prepareRemote(ctx context.Context, remote RemotePeer, anchor TrustedRelay, privateOnly bool, snapshot RouteSnapshot) error {
	if err := r.waitForPredecessor(ctx); err != nil {
		return err
	}
	if err := r.startMu.LockContext(ctx); err != nil {
		return err
	}
	defer r.startMu.Unlock()
	if r.retired.Load() || r.closed {
		return net.ErrClosed
	}
	if r.prepared {
		return nil
	}
	candidates := slices.Clone(snapshot.Permitted)
	if snapshot.Legacy {
		candidates = []RouteCandidate{legacyCandidate(anchor)}
	}
	if r.destinationPolicy.Strict() {
		candidates = slices.DeleteFunc(candidates, func(candidate RouteCandidate) bool {
			return r.destinationPolicy.CheckRelay(candidate.Relay.Address) != nil
		})
	}
	if len(candidates) == 0 {
		return ErrRoutePermission
	}
	for _, candidate := range candidates {
		if candidate.Validate() != nil || privateOnly && candidate.Scope != "local" {
			return ErrRoutePermission
		}
	}
	base, err := tailcat.ParseAddr(remote.Address)
	if err != nil {
		return ErrUntrusted
	}
	role := remote.ClientPrivate
	if r.makeClient == nil {
		r.makeClient = func(address tailcat.Addr) peerTransport {
			return &tailcat.Client{Server: address, Key: role, PrivateOnly: privateOnly, DestinationPrefixes: destinationPrefixes(r.destinationPolicy), WANCandidates: r.wanCandidates, Logf: logger.Discard}
		}
	}
	// Keep the original authenticated identity and secret material. Candidate
	// selection only substitutes a previously authenticated, locally approved map.
	r.capability = base
	r.candidates = candidates
	r.managed = !snapshot.Legacy
	r.expires = snapshot.NextExpiry
	r.failures = make(map[string]time.Time)
	r.selected = -1
	if r.client != nil {
		r.selected = 0
		r.selectedAt = time.Now()
		r.generation = &transportGeneration{}
	}
	if r.runCtx == nil {
		r.runCtx, r.cancel = context.WithCancel(context.Background())
	}
	cancelFunc := r.cancel
	r.runCancel.Store(&cancelFunc)
	r.prepared = true
	r.publishObservation("idle", "unknown", time.Time{})
	if !r.expires.IsZero() {
		if !time.Now().Before(r.expires) {
			return ErrRoutePermission
		}
		r.expiryTimer = time.AfterFunc(time.Until(r.expires), func() { r.beginRetirement() })
	}
	return nil
}

func (r *remoteClient) validRoute() bool {
	return !r.retired.Load() && (r.expires.IsZero() || time.Now().Before(r.expires))
}

func (r *remoteClient) newClientLocked(index int) {
	ci := r.capability
	ci.RegionID = 0
	ci.Region = []*tailcfg.DERPRegion{r.candidates[index].Relay.region()}
	r.client = r.makeClient(ci.Addr())
	r.selected, r.started, r.path = index, false, "unknown"
	r.selectedAt = time.Now()
	r.generation = &transportGeneration{}
	r.publishObservation("connecting", "unknown", time.Time{})
}

func (r *remoteClient) closeClientLocked() {
	if r.generation != nil {
		r.generation.retired.Store(true)
	}
	r.publishObservation("reconnecting", "unknown", time.Time{})
	if r.client != nil {
		_ = r.client.Close()
	}
	r.client, r.started, r.path = nil, false, "unknown"
}

// dial proves transport health for each managed new connection. A failed
// proof retires the old generation (including hung flows); payload is never
// replayed. A healthy application-port failure does not condemn its relay.
func (r *remoteClient) dial(ctx context.Context, network string, port uint16) (net.Conn, error) {
	if err := r.waitForPredecessor(ctx); err != nil {
		return nil, err
	}
	if err := r.startMu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer r.startMu.Unlock()
	if !r.prepared || r.closed || !r.validRoute() {
		return nil, ErrRoutePermission
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !r.managed {
		return r.dialLegacyLocked(ctx, network, port)
	}
	attempts := r.candidateAttempts
	if attempts == 0 {
		attempts = DefaultRelayCandidateAttempts
	}
	if attempts < 1 {
		return nil, ErrRoutePermission
	}
	attempts = min(attempts, len(r.candidates))
	bounded, cancel := context.WithTimeout(ctx, time.Duration(attempts)*routeAttemptTimeout)
	stop := context.AfterFunc(r.runCtx, cancel)
	defer stop()
	defer cancel()
	if !r.expires.IsZero() {
		var finish context.CancelFunc
		bounded, finish = context.WithDeadline(bounded, r.expires)
		defer finish()
	}
	if r.client != nil && r.activeFlows() == 0 && r.selected > 0 && time.Since(r.selectedAt) >= routeRetryHoldDown {
		r.closeClientLocked()
		r.nextCandidate = 0
	}
	attempted := make(map[int]bool)
	var lastErr error
	for len(attempted) < attempts {
		if !r.validRoute() {
			return nil, ErrRoutePermission
		}
		if err := bounded.Err(); err != nil {
			return nil, err
		}
		index := r.selected
		if r.client == nil {
			index = -1
			for offset := range r.candidates {
				i := (r.nextCandidate + offset) % len(r.candidates)
				candidate := r.candidates[i]
				if !attempted[i] && !time.Now().Before(r.failures[candidate.ID()]) {
					index = i
					break
				}
			}
			if index < 0 {
				break
			}
			r.newClientLocked(index)
		}
		attempted[index] = true
		proofCtx, finishProof := context.WithTimeout(bounded, routeAttemptTimeout)
		lastErr = r.proveRoute(proofCtx)
		finishProof()
		if lastErr == nil {
			attemptCtx, finish := context.WithTimeout(bounded, routeAttemptTimeout)
			address := netip.AddrPortFrom(r.address, port)
			var conn net.Conn
			if network == "tcp" {
				conn, lastErr = r.client.DialTCP(attemptCtx, address)
			} else {
				conn, lastErr = r.client.DialUDP(attemptCtx, address)
			}
			finish()
			if lastErr == nil && conn != nil {
				if !r.validRoute() {
					conn.Close()
					return nil, ErrRoutePermission
				}
				r.generation.active.Add(1)
				return r.wrapOutgoing(conn), nil
			}
			if conn != nil {
				conn.Close()
			}
			if lastErr == nil {
				lastErr = errors.New("application connection unavailable")
			}
			applicationErr := lastErr
			// A target service denial or unknown application error must never
			// be reinterpreted as permission to select another relay.
			if !relayAvailabilityError(applicationErr) {
				return nil, applicationErr
			}
			if err := bounded.Err(); err != nil {
				return nil, applicationErr
			}
			// A service can refuse its port while the selected transport is healthy.
			healthCtx, finishHealth := context.WithTimeout(bounded, 2*time.Second)
			healthErr := r.proveRoute(healthCtx)
			finishHealth()
			if healthErr == nil {
				return nil, applicationErr
			}
			lastErr = healthErr
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !r.validRoute() {
			return nil, ErrRoutePermission
		}
		if !relayAvailabilityError(lastErr) {
			r.closeClientLocked()
			r.publishObservation("unavailable", "unknown", time.Time{})
			return nil, lastErr
		}
		r.failures[r.candidates[index].ID()] = time.Now().Add(routeRetryHoldDown)
		r.nextCandidate = (index + 1) % len(r.candidates)
		r.closeClientLocked()
	}
	r.publishObservation("unavailable", "unknown", time.Time{})
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("approved relay candidates are unavailable; retry after the recovery hold-down")
}

func (r *remoteClient) proveRoute(ctx context.Context) error {
	observation, err := r.client.DiscoPing(ctx)
	if err != nil {
		return err
	}
	if observation == nil {
		return errors.New("fresh route evidence unavailable")
	}
	path := "unknown"
	if observation.Endpoint != "" {
		path = "direct"
	} else if observation.DERPRegionID != 0 {
		path = "relay"
	}
	r.started = true
	r.path = path
	r.publishObservation("ready", path, time.Now().UTC())
	return nil
}

// Legacy pairs retain the existing singleton startup and dial behavior. They do
// not acquire candidate failover, hold-down, or finite permissions implicitly.
func (r *remoteClient) dialLegacyLocked(ctx context.Context, network string, port uint16) (net.Conn, error) {
	if r.client == nil {
		r.newClientLocked(0)
	}
	activeCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.runCtx, cancel)
	defer stop()
	defer cancel()
	if !r.started {
		bounded, finish := context.WithTimeout(activeCtx, 10*time.Second)
		_, err := r.client.Ping(bounded)
		finish()
		if err != nil {
			return nil, err
		}
		r.started = true
	}
	var conn net.Conn
	var err error
	address := netip.AddrPortFrom(r.address, port)
	if network == "tcp" {
		conn, err = r.client.DialTCP(activeCtx, address)
	} else {
		conn, err = r.client.DialUDP(activeCtx, address)
	}
	if err != nil {
		return nil, err
	}
	if !r.validRoute() {
		conn.Close()
		return nil, ErrRoutePermission
	}
	r.generation.active.Add(1)
	return r.wrapOutgoing(conn), nil
}

// transportRetirement publishes its result by closing done. Every generation
// has at most one cleanup task, shared by timers, mutations and Node.Close.
type transportRetirement struct {
	done chan struct{}
	err  error // read only after done is closed
}

func (r *remoteClient) retirementState() *transportRetirement {
	r.retirementInit.Do(func() { r.retirement = &transportRetirement{done: make(chan struct{})} })
	return r.retirement
}

// replaceRemoteLocked immediately denies old flows and publishes a replacement
// whose setup waits for the old engine to close. The caller holds n.mu and must
// start old's retirement after releasing it; no engine teardown runs under n.mu.
func (n *Node) replaceRemoteLocked(peer string, old *remoteClient, remote RemotePeer) *remoteClient {
	old.retired.Store(true)
	next := &remoteClient{remote: remote, address: old.address, destinationPolicy: n.cfg.DestinationPolicy, wanCandidates: n.cfg.WANCandidates, candidateAttempts: n.cfg.RelayResources.CandidateAttempts, predecessor: old.retirementState()}
	n.clients[peer] = next
	return next
}

func (r *remoteClient) waitForPredecessor(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.predecessor == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.predecessor.done:
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.predecessor.err != nil {
			// A failed Close does not prove the previous engine is gone.
			return errors.Join(ErrRoutePermission, r.predecessor.err)
		}
		return nil
	}
}

func (r *remoteClient) beginRetirement() *transportRetirement {
	r.retired.Store(true)
	if cancel := r.runCancel.Load(); cancel != nil {
		(*cancel)()
	}
	result := r.retirementState()
	r.retirementStart.Do(func() { go r.finishRetirement(result) })
	return result
}

func (r *remoteClient) finishRetirement(result *transportRetirement) {
	// A replacement may itself be revoked before setup. Preserve the entire
	// retirement chain so a third generation cannot bypass the first Close.
	if r.predecessor != nil {
		<-r.predecessor.done
		result.err = r.predecessor.err
	}
	r.startMu.Lock()
	defer close(result.done)
	defer r.startMu.Unlock()
	r.closed = true
	if r.cancel != nil {
		r.cancel()
	}
	if r.generation != nil {
		r.generation.retired.Store(true)
	}
	r.publishObservation("closed", "unknown", time.Time{})
	if r.expiryTimer != nil {
		r.expiryTimer.Stop()
	}
	if r.client != nil {
		result.err = errors.Join(result.err, r.client.Close())
	}
}

func (r *remoteClient) shutdown() error {
	result := r.beginRetirement()
	<-result.done
	return result.err
}

func (n *Node) trackOutgoing(peer string, epoch uint64, r *remoteClient, c net.Conn) (net.Conn, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed || n.pairingRecovery || n.clients[peer] != r || !r.validRoute() || !c.(interface{ routeValid() bool }).routeValid() {
		c.Close()
		return nil, ErrRoutePermission
	}
	tracked, err := n.track(peer, epoch, c)
	if err != nil {
		return nil, err
	}
	switch flow := tracked.(type) {
	case *trackedStream:
		trustValid := flow.valid
		flow.valid = func() bool { return trustValid() && c.(interface{ routeValid() bool }).routeValid() }
	case *trackedPacket:
		trustValid := flow.valid
		flow.valid = func() bool { return trustValid() && c.(interface{ routeValid() bool }).routeValid() }
	}
	return tracked, nil
}

type outgoingStream struct {
	net.Conn
	runtime    *remoteClient
	generation *transportGeneration
	once       sync.Once
}

func (r *remoteClient) wrapOutgoing(c net.Conn) net.Conn {
	s := &outgoingStream{Conn: c, runtime: r, generation: r.generation}
	if pc, ok := c.(ConnPacketConn); ok {
		return &outgoingPacket{outgoingStream: s, packet: pc}
	}
	return s
}
func (c *outgoingStream) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.generation.active.Add(-1) })
	return err
}
func (c *outgoingStream) Read(b []byte) (int, error) {
	if !c.routeValid() {
		return 0, ErrRoutePermission
	}
	n, err := c.Conn.Read(b)
	if !c.routeValid() {
		return 0, ErrRoutePermission
	}
	return n, err
}
func (c *outgoingStream) Write(b []byte) (int, error) {
	if !c.routeValid() {
		return 0, ErrRoutePermission
	}
	return c.Conn.Write(b)
}
func (c *outgoingStream) CloseWrite() error {
	if h, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return h.CloseWrite()
	}
	return errors.New("half-close unsupported")
}

type outgoingPacket struct {
	*outgoingStream
	packet ConnPacketConn
}

func (c *outgoingPacket) ReadFrom(b []byte) (int, net.Addr, error) {
	if !c.routeValid() {
		return 0, nil, ErrRoutePermission
	}
	n, a, e := c.packet.ReadFrom(b)
	if !c.routeValid() {
		return 0, nil, ErrRoutePermission
	}
	return n, a, e
}
func (c *outgoingPacket) WriteTo(b []byte, a net.Addr) (int, error) {
	if !c.routeValid() {
		return 0, ErrRoutePermission
	}
	return c.packet.WriteTo(b, a)
}

func (c *outgoingStream) routeValid() bool {
	return c.runtime.validRoute() && !c.generation.retired.Load()
}

type transportGeneration struct {
	retired atomic.Bool
	active  atomic.Int64
}

func (r *remoteClient) activeFlows() int {
	if r.generation == nil || r.generation.retired.Load() {
		return 0
	}
	return int(r.generation.active.Load())
}

// PeerTransportSnapshot reports selection and a previous fresh probe; it does
// not promise that a later application call or existing TCP session will work.
type PeerTransportSnapshot struct {
	CandidateID string    `json:"candidate_id,omitempty"`
	Scope       string    `json:"scope,omitempty"`
	Path        string    `json:"path"`
	ActiveFlows int       `json:"active_flows"`
	Expires     time.Time `json:"expires,omitempty"`
	Retired     bool      `json:"retired"`
}

func (n *Node) TransportSnapshot(peer string) (PeerTransportSnapshot, error) {
	n.mu.Lock()
	r := n.clients[peer]
	n.mu.Unlock()
	if r == nil {
		return PeerTransportSnapshot{}, ErrUntrusted
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()
	out := PeerTransportSnapshot{Path: r.path, ActiveFlows: r.activeFlows(), Expires: r.expires, Retired: r.retired.Load()}
	if out.Path == "" {
		out.Path = "unknown"
	}
	if r.prepared && r.selected >= 0 && r.selected < len(r.candidates) {
		out.CandidateID = r.candidates[r.selected].ID()
		out.Scope = r.candidates[r.selected].Scope
	}
	return out, nil
}

// RouteObservation is immutable, bounded-age transport evidence. Relay scope
// describes the approved bootstrap destination, separately from the observed
// direct/relay packet path. It contains no capability or detailed endpoint error.
type RouteObservation struct {
	State       string    `json:"state"`
	CandidateID string    `json:"candidate_id,omitempty"`
	Scope       string    `json:"scope,omitempty"`
	Path        string    `json:"path"`
	ObservedAt  time.Time `json:"observed_at,omitempty"`
	Expires     time.Time `json:"expires,omitempty"`
}

func (r *remoteClient) publishObservation(state, path string, observed time.Time) {
	out := &RouteObservation{State: state, Path: path, ObservedAt: observed, Expires: r.expires}
	if r.selected >= 0 && r.selected < len(r.candidates) {
		out.CandidateID = r.candidates[r.selected].ID()
		out.Scope = r.candidates[r.selected].Scope
	}
	r.observation.Store(out)
}
func (n *Node) RouteObservation(peer string) (RouteObservation, error) {
	n.mu.Lock()
	r := n.clients[peer]
	n.mu.Unlock()
	if r == nil {
		return RouteObservation{}, ErrUntrusted
	}
	out := RouteObservation{State: "idle", Path: "unknown"}
	if current := r.observation.Load(); current != nil {
		out = *current
	}
	now := time.Now()
	switch {
	case !out.Expires.IsZero() && !now.Before(out.Expires):
		out.State, out.Path = "expired", "unknown"
	case r.retired.Load():
		out.State, out.Path = "closed", "unknown"
	case out.State == "ready" && (out.ObservedAt.IsZero() || out.ObservedAt.After(now) || now.Sub(out.ObservedAt) > routeRetryHoldDown):
		out.State, out.Path = "unknown", "unknown"
	}
	return out, nil
}
