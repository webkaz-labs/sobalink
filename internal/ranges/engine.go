package ranges

import (
	"context"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/deadline"
	"io"
	"net"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	MaxPolicyConnections = 128
	MaxGlobalConnections = 512
	MaxPeerConnections   = 64
)

// Request is the captured numeric flow scope. Authorize must resolve the
// current authenticated source identity and check the exact current permission
// and expiry, including the current source-IP mapping. PeerID is empty before
// first authentication and thereafter pins the identity for the flow's life.
type Request struct {
	PolicyID            string
	Source, Destination netip.AddrPort
	PeerID              string
	ExpiresAt           time.Time
}
type Authorizer func(context.Context, Request) (string, error)
type LoopbackDialer func(context.Context, netip.AddrPort) (net.Conn, error)
type Limits struct{ Global, PerPolicy, PerPeer int }
type Options struct {
	Authorize Authorizer
	// DialLoopback receives only 127.0.0.1 or ::1 and the requested service
	// port or explicitly mapped TargetPort. Nil uses a numeric-only OS
	// loopback dial, never an outward dial.
	DialLoopback LoopbackDialer
	// AdmitTCP integrates with a process-wide budget shared by other TCP
	// transports. It must be nonblocking and return an idempotent release.
	// Nil uses this package's shared process-wide 512-flow gate.
	AdmitTCP           func() (release func(), ok bool)
	Limits             Limits
	DialTimeout        time.Duration
	RevalidateInterval time.Duration
}

var processSlots = make(chan struct{}, MaxGlobalConnections)

func defaultAdmission() (func(), bool) {
	select {
	case processSlots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-processSlots }) }, true
	default:
		return nil, false
	}
}

type permit struct {
	rule    compiledPolicy
	revoked atomic.Bool
}
type snapshot struct {
	selfIPs map[netip.Addr]struct{}
	permits []*permit
}
type flow struct {
	engine              *Engine
	permit              *permit
	source, destination netip.AddrPort
	peerID              string // guarded by engine.mu; immutable after initial authentication
	ctx                 context.Context
	cancel              context.CancelFunc
	client              net.Conn
	mu                  sync.Mutex
	remote              net.Conn
	release             func()
	done                chan struct{}
}

// Engine is a single TCP fallback dispatcher. Handle is safe to call while the
// embedded stack holds its mutex: it only reads an immutable atomic snapshot,
// checks addresses/intervals/lifetime, and returns a closure. It does not call
// WhoIs, State, a dialer, a budget hook, or any other supplied callback.
// Use one Engine per embedded node. Close revokes and cancels synchronously;
// Wait(ctx) optionally waits for callbacks and stream workers to finish.
type Engine struct {
	ctx      context.Context
	cancel   context.CancelFunc
	opts     Options
	current  atomic.Pointer[snapshot]
	mu       sync.Mutex
	closed   bool
	active   map[*flow]struct{}
	byPolicy map[string]int
	byPeer   map[string]int
	bySource map[netip.Addr]int
	wg       sync.WaitGroup
	done     chan struct{}
}

func New(ctx context.Context, opts Options) (*Engine, error) {
	if ctx == nil || opts.Authorize == nil {
		return nil, errors.New("context and current source authorization are required")
	}
	if opts.DialTimeout == 0 {
		opts.DialTimeout = 10 * time.Second
	}
	if opts.DialTimeout < 0 || opts.DialTimeout > 10*time.Second {
		return nil, errors.New("dial timeout must be positive and at most 10 seconds")
	}
	if opts.RevalidateInterval == 0 {
		opts.RevalidateInterval = time.Second
	}
	if opts.RevalidateInterval < 0 || opts.RevalidateInterval > time.Second {
		return nil, errors.New("revalidation interval must be positive and at most one second")
	}
	for _, item := range []struct {
		value   *int
		maximum int
	}{{&opts.Limits.Global, MaxGlobalConnections}, {&opts.Limits.PerPolicy, MaxPolicyConnections}, {&opts.Limits.PerPeer, MaxPeerConnections}} {
		if *item.value == 0 {
			*item.value = item.maximum
		}
		if *item.value < 1 || *item.value > item.maximum {
			return nil, errors.New("connection limit exceeds its allowed bound")
		}
	}
	if opts.DialLoopback == nil {
		opts.DialLoopback = dialLoopback
	}
	if opts.AdmitTCP == nil {
		opts.AdmitTCP = defaultAdmission
	}
	lifetime, cancel := context.WithCancel(ctx)
	e := &Engine{ctx: lifetime, cancel: cancel, opts: opts, active: make(map[*flow]struct{}), byPolicy: make(map[string]int), byPeer: make(map[string]int), bySource: make(map[netip.Addr]int), done: make(chan struct{})}
	e.current.Store(&snapshot{selfIPs: map[netip.Addr]struct{}{}})
	go func() { <-lifetime.Done(); _ = e.Close() }()
	return e, nil
}

func dialLoopback(ctx context.Context, target netip.AddrPort) (net.Conn, error) {
	if !loopbackIP(target.Addr()) || target.Port() == 0 {
		return nil, errors.New("exact numeric loopback service target required")
	}
	network := "tcp6"
	if target.Addr().Is4() {
		network = "tcp4"
	}
	return (&net.Dialer{}).DialContext(ctx, network, target.String())
}

// Replace validates the entire proposal before affecting the current plan.
func (e *Engine) Replace(selfIPs []netip.Addr, policies []Policy) error {
	plan, err := BuildPlan(selfIPs, policies)
	if err != nil {
		return err
	}
	return e.Apply(plan)
}

// Apply atomically publishes a validated plan. Identical policies retain their
// flows; removed or changed permits are all revoked before any drain begins.
func (e *Engine) Apply(plan *Plan) error {
	if plan == nil {
		return errors.New("validated range plan required")
	}
	e.mu.Lock()
	if e.closed || e.ctx.Err() != nil {
		e.mu.Unlock()
		return net.ErrClosed
	}
	old := e.current.Load()
	next := &snapshot{selfIPs: plan.selfIPs, permits: make([]*permit, 0, len(plan.policies))}
	retained := make(map[*permit]bool, len(plan.policies))
	for _, rule := range plan.policies {
		var selected *permit
		for _, previous := range old.permits {
			if !previous.revoked.Load() && equalPolicies(previous.rule, rule) {
				selected = previous
				break
			}
		}
		if selected == nil {
			selected = &permit{rule: rule}
		}
		next.permits = append(next.permits, selected)
		retained[selected] = true
	}
	for _, previous := range old.permits {
		if !retained[previous] {
			previous.revoked.Store(true)
		}
	}
	e.current.Store(next)
	var revoked []*flow
	for f := range e.active {
		if f.permit.revoked.Load() {
			revoked = append(revoked, f)
		}
	}
	e.mu.Unlock()
	for _, f := range revoked {
		f.cancel()
	}
	return nil
}

// RevokeIDs invalidates every matching permit before canceling any active
// flow. An already-selected handler can no longer admit a flow afterwards.
func (e *Engine) RevokeIDs(ids []string) int {
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	e.mu.Lock()
	old := e.current.Load()
	next := &snapshot{selfIPs: old.selfIPs, permits: make([]*permit, 0, len(old.permits))}
	count := 0
	for _, p := range old.permits {
		if wanted[p.rule.policy.ID] {
			p.revoked.Store(true)
			count++
		} else {
			next.permits = append(next.permits, p)
		}
	}
	e.current.Store(next)
	var revoked []*flow
	for f := range e.active {
		if f.permit.revoked.Load() {
			revoked = append(revoked, f)
		}
	}
	e.mu.Unlock()
	for _, f := range revoked {
		f.cancel()
	}
	return count
}

func validFlow(s *snapshot, p *permit, src, dst netip.AddrPort) bool {
	return validFlowAt(time.Now(), s, p, src, dst)
}
func validFlowAt(now time.Time, s *snapshot, p *permit, src, dst netip.AddrPort) bool {
	if s == nil || p.revoked.Load() || src.Port() == 0 || dst.Port() == 0 || !tailnetIP(src.Addr()) || !tailnetIP(dst.Addr()) {
		return false
	}
	if _, own := s.selfIPs[dst.Addr()]; !own {
		return false
	}
	r := p.rule
	return r.policy.Network == "tcp" && r.policy.Address == dst.Addr() && deadline.Active(now, r.policy.ExpiresAt) && r.effective.Contains(dst.Port())
}

func (e *Engine) Handle(src, dst netip.AddrPort) (func(net.Conn), bool) {
	if e.ctx.Err() != nil {
		return nil, true
	}
	s := e.current.Load()
	for _, p := range s.permits {
		if validFlow(s, p, src, dst) {
			return func(conn net.Conn) {
				if conn != nil {
					e.serve(p, src, dst, conn)
				}
			}, true
		}
	}
	return nil, true // explicitly reject; never forward an unmatched port elsewhere
}

func (e *Engine) admit(p *permit, src, dst netip.AddrPort, client net.Conn) *flow {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.ctx.Err() != nil || !validFlow(e.current.Load(), p, src, dst) || len(e.active) >= e.opts.Limits.Global || e.byPolicy[p.rule.policy.ID] >= e.opts.Limits.PerPolicy || e.bySource[src.Addr()] >= e.opts.Limits.PerPeer {
		return nil
	}
	release, ok := e.opts.AdmitTCP()
	if !ok {
		return nil
	}
	if release == nil {
		return nil
	}
	ctx, cancel := context.WithDeadline(e.ctx, p.rule.policy.ExpiresAt)
	f := &flow{engine: e, permit: p, source: src, destination: dst, ctx: ctx, cancel: cancel, client: client, release: release, done: make(chan struct{})}
	e.active[f] = struct{}{}
	e.byPolicy[p.rule.policy.ID]++
	e.bySource[src.Addr()]++
	e.wg.Add(1)
	return f
}

func (f *flow) permitted() error {
	if err := f.ctx.Err(); err != nil {
		return err
	}
	if !validFlow(f.engine.current.Load(), f.permit, f.source, f.destination) {
		return errors.New("range flow is no longer permitted")
	}
	return nil
}

func (e *Engine) authorize(ctx context.Context, f *flow) (string, error) {
	if err := f.permitted(); err != nil {
		return "", err
	}
	e.mu.Lock()
	expected := f.peerID
	e.mu.Unlock()
	r := f.permit.rule.policy
	check, cancel := context.WithTimeout(ctx, e.opts.DialTimeout)
	defer cancel()
	peer, err := e.opts.Authorize(check, Request{PolicyID: r.ID, Source: f.source, Destination: f.destination, PeerID: expected, ExpiresAt: r.ExpiresAt})
	if err != nil {
		return "", err
	}
	if err := check.Err(); err != nil {
		return "", err
	}
	if err := f.permitted(); err != nil {
		return "", err
	}
	i := sort.SearchStrings(r.PeerIDs, peer)
	if peer == "" || i == len(r.PeerIDs) || r.PeerIDs[i] != peer || (expected != "" && peer != expected) {
		return "", errors.New("current source does not match the pinned peer permission")
	}
	return peer, nil
}

func (e *Engine) bindPeer(f *flow, peer string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if f.permitted() != nil || e.byPeer[peer] >= e.opts.Limits.PerPeer {
		return false
	}
	f.peerID = peer
	e.byPeer[peer]++
	return true
}

func (f *flow) closeSockets() {
	_ = f.client.Close()
	f.mu.Lock()
	remote := f.remote
	f.mu.Unlock()
	if remote != nil {
		_ = remote.Close()
	}
}
func (f *flow) install(remote net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.permitted() != nil {
		_ = remote.Close()
		return false
	}
	f.remote = remote
	return true
}
func decrement[K comparable](m map[K]int, key K) {
	if m[key] <= 1 {
		delete(m, key)
	} else {
		m[key]--
	}
}
func (e *Engine) release(f *flow) {
	f.cancel()
	f.closeSockets()
	e.mu.Lock()
	delete(e.active, f)
	decrement(e.byPolicy, f.permit.rule.policy.ID)
	decrement(e.bySource, f.source.Addr())
	if f.peerID != "" {
		decrement(e.byPeer, f.peerID)
	}
	e.mu.Unlock()
	f.release()
	close(f.done)
	e.wg.Done()
}

func (e *Engine) serve(p *permit, src, dst netip.AddrPort, client net.Conn) {
	f := e.admit(p, src, dst, client)
	if f == nil {
		_ = client.Close()
		return
	}
	defer e.release(f)
	watched := make(chan struct{})
	go func() { defer close(watched); <-f.ctx.Done(); f.closeSockets() }()
	defer func() { f.cancel(); <-watched }()
	peer, err := e.authorize(f.ctx, f)
	if err != nil || !e.bindPeer(f, peer) {
		return
	}
	targetPort := dst.Port()
	if p.rule.policy.TargetPort != 0 {
		targetPort = p.rule.policy.TargetPort
	}
	target := netip.AddrPortFrom(p.rule.policy.Loopback, targetPort)
	if f.permitted() != nil {
		return
	}
	dialCtx, cancel := context.WithTimeout(f.ctx, e.opts.DialTimeout)
	remote, err := e.opts.DialLoopback(dialCtx, target)
	dialErr := dialCtx.Err()
	cancel()
	if err != nil || dialErr != nil || remote == nil {
		if remote != nil {
			_ = remote.Close()
		}
		return
	}
	if !f.install(remote) {
		return
	}
	if _, err := e.authorize(f.ctx, f); err != nil {
		return
	}
	validated := make(chan struct{})
	go func() {
		defer close(validated)
		ticker := time.NewTicker(e.opts.RevalidateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-f.ctx.Done():
				return
			case <-ticker.C:
				if _, err := e.authorize(f.ctx, f); err != nil {
					f.cancel()
					return
				}
			}
		}
	}()
	bridge(&guardedConn{Conn: client, guard: f.permitted}, &guardedConn{Conn: remote, guard: f.permitted})
	f.cancel()
	<-validated
}

// Revalidate checks already-authenticated active flows against current identity
// and permission data. Errors cancel the affected flow; untrusted partial data
// never keeps it alive. The callback must support concurrent calls.
func (e *Engine) Revalidate(ctx context.Context) error {
	e.mu.Lock()
	var active []*flow
	for f := range e.active {
		if f.peerID != "" {
			active = append(active, f)
		}
	}
	e.mu.Unlock()
	var result error
	for _, f := range active {
		if _, err := e.authorize(ctx, f); err != nil {
			f.cancel()
			result = err
		}
	}
	return result
}

// Close disables admission and revokes all permits before cancellation starts.
// It never waits on user callbacks or dials; use Wait for a bounded drain.
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	for _, p := range e.current.Load().permits {
		p.revoked.Store(true)
	}
	e.current.Store(&snapshot{selfIPs: map[netip.Addr]struct{}{}})
	e.cancel()
	for f := range e.active {
		f.cancel()
	}
	e.mu.Unlock()
	go func() { e.wg.Wait(); close(e.done) }()
	return nil
}
func (e *Engine) Done() <-chan struct{} { return e.done }
func (e *Engine) Wait(ctx context.Context) error {
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type Stats struct {
	Policies, Intervals, Active int
	PerPolicy, PerPeer          map[string]int
}

func (e *Engine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := Stats{Active: len(e.active), PerPolicy: make(map[string]int, len(e.byPolicy)), PerPeer: make(map[string]int, len(e.byPeer))}
	for _, p := range e.current.Load().permits {
		out.Policies++
		out.Intervals += p.rule.effective.IntervalCount()
	}
	for id, n := range e.byPolicy {
		out.PerPolicy[id] = n
	}
	for id, n := range e.byPeer {
		out.PerPeer[id] = n
	}
	return out
}

type guardedConn struct {
	net.Conn
	guard func() error
}

func (c *guardedConn) Read(b []byte) (int, error) {
	if err := c.guard(); err != nil {
		return 0, err
	}
	n, err := c.Conn.Read(b)
	if current := c.guard(); current != nil {
		clear(b[:n])
		return 0, current
	}
	return n, err
}
func (c *guardedConn) Write(b []byte) (int, error) {
	if err := c.guard(); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}
func (c *guardedConn) CloseWrite() error {
	if conn, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return c.Conn.Close()
}

// bridge preserves TCP half-close so a request-side EOF can still receive its
// response. A canceled permit closes both directions via the flow watcher.
func bridge(a, b net.Conn) {
	done := make(chan struct{})
	copyOne := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if err != nil {
			_ = a.Close()
			_ = b.Close()
			return
		}
		if conn, ok := dst.(interface{ CloseWrite() error }); ok {
			if conn.CloseWrite() != nil {
				_ = a.Close()
				_ = b.Close()
			}
		} else {
			_ = dst.Close()
		}
	}
	go func() { defer close(done); copyOne(a, b) }()
	copyOne(b, a)
	<-done
}
