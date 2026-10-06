package lanlink

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

type fakePeerTransport struct {
	mu       sync.Mutex
	probeErr error
	dialErr  error
	probe    func(context.Context) error
	closed   atomic.Int32
	probes   atomic.Int32
	conns    []*routeTestConn
}

func (f *fakePeerTransport) Ping(context.Context) (tailcat.PingResult, error) {
	return tailcat.PingResult{}, nil
}
func (f *fakePeerTransport) DiscoPing(ctx context.Context) (*ipnstate.PingResult, error) {
	f.probes.Add(1)
	if f.probe != nil {
		if err := f.probe(ctx); err != nil {
			return nil, err
		}
	}
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	return &ipnstate.PingResult{DERPRegionID: 1}, nil
}
func (f *fakePeerTransport) DialTCP(context.Context, netip.AddrPort) (net.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	c := &routeTestConn{}
	f.conns = append(f.conns, c)
	return c, nil
}
func (f *fakePeerTransport) DialUDP(ctx context.Context, a netip.AddrPort) (tailcat.ConnPacketConn, error) {
	c, e := f.DialTCP(ctx, a)
	if e != nil {
		return nil, e
	}
	return c.(*routeTestConn), nil
}
func (f *fakePeerTransport) Close() error {
	f.closed.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		c.Close()
	}
	return nil
}

type routeTestConn struct {
	closed  atomic.Bool
	written atomic.Int32
}

func (c *routeTestConn) Read([]byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	return 0, io.EOF
}
func (c *routeTestConn) Write(b []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	c.written.Add(int32(len(b)))
	return len(b), nil
}
func (c *routeTestConn) Close() error                     { c.closed.Store(true); return nil }
func (c *routeTestConn) LocalAddr() net.Addr              { return &net.TCPAddr{IP: net.IPv6loopback, Port: 1} }
func (c *routeTestConn) RemoteAddr() net.Addr             { return &net.TCPAddr{IP: net.IPv6loopback, Port: 2} }
func (c *routeTestConn) SetDeadline(time.Time) error      { return nil }
func (c *routeTestConn) SetReadDeadline(time.Time) error  { return nil }
func (c *routeTestConn) SetWriteDeadline(time.Time) error { return nil }
func (c *routeTestConn) CloseWrite() error                { return nil }
func (c *routeTestConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, e := c.Read(b)
	return n, c.RemoteAddr(), e
}
func (c *routeTestConn) WriteTo(b []byte, _ net.Addr) (int, error) { return c.Write(b) }

func routeRuntimeFixture(t *testing.T, expiry time.Time) (*remoteClient, TrustedRelay, []RouteCandidate) {
	t.Helper()
	n := testNode()
	remote := testNode()
	role := key.NewNode()
	r := &remoteClient{remote: RemotePeer{Peer: Peer{remote.PublicKey(), "remote"}, Address: remote.Address(), ClientPrivate: role, IncomingClientKey: keyString(key.NewNode().Public())}, address: remote.OverlayAddr()}
	candidates := []RouteCandidate{routeFixture("local", "192.168.50.2:54446"), routeFixture("external", "192.0.2.20:443")}
	t.Cleanup(func() { r.shutdown() })
	return r, n.cfg.Relay, candidates
}

func TestTransportFailoverKeepsKeysAndDoesNotReplayPayload(t *testing.T) {
	r, anchor, candidates := routeRuntimeFixture(t, time.Time{})
	first := &fakePeerTransport{probeErr: syscall.ECONNREFUSED}
	second := &fakePeerTransport{}
	var selected []RouteCandidate
	r.makeClient = func(addr tailcat.Addr) peerTransport {
		ci, e := tailcat.ParseAddr(addr)
		if e != nil {
			t.Fatal(e)
		}
		original, _ := tailcat.ParseAddr(r.remote.Address)
		if ci.ServerPublic != original.ServerPublic || ci.ServerDiscoPublic != original.ServerDiscoPublic || !ci.PresharedKey.Equal(original.PresharedKey) {
			t.Fatal("candidate change altered peer identity")
		}
		if len(selected) == 0 {
			selected = append(selected, candidates[0])
			return first
		}
		selected = append(selected, candidates[1])
		return second
	}
	if e := r.prepare(anchor, false, RouteSnapshot{Permitted: candidates}); e != nil {
		t.Fatal(e)
	}
	c, e := r.dial(context.Background(), "tcp", 8080)
	if e != nil {
		t.Fatal(e)
	}
	if len(selected) != 2 || first.closed.Load() != 1 || r.selected != 1 || r.activeFlows() != 1 {
		t.Fatal("bounded candidate failover failed")
	}
	if second.conns[0].written.Load() != 0 {
		t.Fatal("transport wrote application payload")
	}
	if _, e = c.Write([]byte("once")); e != nil {
		t.Fatal(e)
	}
	second.mu.Lock()
	second.dialErr = errors.New("new dial failed")
	second.mu.Unlock()
	if _, e = r.dial(context.Background(), "tcp", 8080); e == nil {
		t.Fatal("expected new dial failure")
	}
	if len(selected) != 2 || second.closed.Load() != 0 || second.conns[0].written.Load() != 4 {
		t.Fatal("active engine replaced or payload replayed")
	}
	c.Close()
}

func TestTransportFreshClientsPreserveRoleAndCapability(t *testing.T) {
	r, anchor, candidates := routeRuntimeFixture(t, time.Time{})
	if e := r.prepare(anchor, false, RouteSnapshot{Permitted: candidates}); e != nil {
		t.Fatal(e)
	}
	base, _ := tailcat.ParseAddr(r.remote.Address)
	for i := range candidates {
		r.newClientLocked(i)
		c := r.client.(*tailcat.Client)
		ci, e := tailcat.ParseAddr(c.Server)
		if e != nil {
			t.Fatal(e)
		}
		if c.Key.Public() != r.remote.ClientPrivate.Public() || ci.ServerPublic != base.ServerPublic || ci.ServerDiscoPublic != base.ServerDiscoPublic || !ci.PresharedKey.Equal(base.PresharedKey) {
			t.Fatal("durable identity changed")
		}
		r.closeClientLocked()
	}
}

func TestTransportExpiryClosesActiveRuntime(t *testing.T) {
	r, anchor, candidates := routeRuntimeFixture(t, time.Time{})
	fake := &fakePeerTransport{}
	r.makeClient = func(tailcat.Addr) peerTransport { return fake }
	if e := r.prepare(anchor, false, RouteSnapshot{Permitted: candidates[:1], NextExpiry: time.Now().Add(50 * time.Millisecond)}); e != nil {
		t.Fatal(e)
	}
	c, e := r.dial(context.Background(), "udp", 8080)
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(time.Second)
	for !r.retired.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !r.retired.Load() {
		t.Fatal("expiry did not retire runtime")
	}
	if _, e = c.Write([]byte("blocked")); !errors.Is(e, ErrRoutePermission) {
		t.Fatal("write survived permission expiry", e)
	}
	if _, e = c.(ConnPacketConn).WriteTo([]byte("blocked"), c.RemoteAddr()); !errors.Is(e, ErrRoutePermission) {
		t.Fatal("datagram survived permission expiry", e)
	}
	c.Close()
}

func TestTransportRetirementCancelsInflightProbe(t *testing.T) {
	r, anchor, candidates := routeRuntimeFixture(t, time.Time{})
	entered := make(chan struct{})
	f := &fakePeerTransport{probe: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }}
	r.makeClient = func(tailcat.Addr) peerTransport { return f }
	if e := r.prepare(anchor, false, RouteSnapshot{Permitted: candidates}); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := r.dial(context.Background(), "tcp", 8080); done <- e }()
	<-entered
	if e := r.shutdown(); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("retired dial succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("probe survived retirement")
	}
}

func TestTransportQueuedDialHonorsCallerDeadline(t *testing.T) {
	for _, entry := range []string{"node-prepare", "runtime-dial"} {
		for _, network := range []string{"tcp", "udp"} {
			t.Run(entry+"/"+network, func(t *testing.T) {
				a, b, now, candidates := routeNodesFixture(t)
				routeApplyFixture(t, a, b, candidates, now)
				r := b.clients[a.PublicKey()]
				entered, release := make(chan struct{}), make(chan struct{})
				var enteredOnce sync.Once
				fake := &fakePeerTransport{probe: func(ctx context.Context) error {
					enteredOnce.Do(func() { close(entered) })
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}}
				r.makeClient = func(tailcat.Addr) peerTransport { return fake }
				firstCtx, cancelFirst := context.WithCancel(context.Background())
				defer cancelFirst()
				defer close(release)
				firstDone := make(chan error, 1)
				go func() {
					conn, err := b.DialPeer(firstCtx, a.PublicKey(), "tcp", 8080)
					if conn != nil {
						conn.Close()
					}
					firstDone <- err
				}()
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("first dial did not enter its held probe")
				}
				queuedCtx, cancelQueued := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancelQueued()
				queuedDone := make(chan error, 1)
				go func() {
					var conn net.Conn
					var err error
					if entry == "node-prepare" {
						conn, err = b.DialPeer(queuedCtx, a.PublicKey(), network, 8080)
					} else {
						conn, err = r.dial(queuedCtx, network, 8080)
					}
					if conn != nil {
						conn.Close()
					}
					queuedDone <- err
				}()
				select {
				case err := <-queuedDone:
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal("queued dial did not return its caller deadline", err)
					}
				case <-time.After(time.Second):
					t.Fatal("queued dial waited for the first probe to release")
				}
				select {
				case <-firstDone:
					t.Fatal("first dial released before queued cancellation was proved")
				default:
				}
				if fake.probes.Load() != 1 {
					t.Fatal("cancelled waiter started another transport operation")
				}
				cancelFirst()
				select {
				case err := <-firstDone:
					if !errors.Is(err, context.Canceled) {
						t.Fatal("first caller cancellation was lost", err)
					}
				case <-time.After(time.Second):
					t.Fatal("first probe survived its caller cancellation")
				}
			})
		}
	}
}

func TestTransportGateRejectsCancelledCallerWithoutConsumingAdmission(t *testing.T) {
	var gate transportGate
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.LockContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled caller acquired admission", err)
	}
	deadline, finish := context.WithTimeout(context.Background(), time.Second)
	defer finish()
	if err := gate.LockContext(deadline); err != nil {
		t.Fatal("cancelled caller consumed the gate token", err)
	}
	gate.Unlock()
}

func TestTransportLocalRetryOnlyWithoutActiveFlows(t *testing.T) {
	r, anchor, candidates := routeRuntimeFixture(t, time.Time{})
	first := &fakePeerTransport{probeErr: syscall.ECONNREFUSED}
	external := &fakePeerTransport{}
	local := &fakePeerTransport{}
	calls := 0
	r.makeClient = func(tailcat.Addr) peerTransport {
		calls++
		switch calls {
		case 1:
			return first
		case 2:
			return external
		default:
			return local
		}
	}
	if e := r.prepare(anchor, false, RouteSnapshot{Permitted: candidates}); e != nil {
		t.Fatal(e)
	}
	c, e := r.dial(context.Background(), "tcp", 8080)
	if e != nil {
		t.Fatal(e)
	}
	r.selectedAt = time.Now().Add(-time.Hour)
	r.failures[candidates[0].ID()] = time.Time{}
	c2, e := r.dial(context.Background(), "tcp", 8080)
	if e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatal("probed by replacing active engine")
	}
	c2.Close()
	c.Close()
	c3, e := r.dial(context.Background(), "tcp", 8080)
	if e != nil {
		t.Fatal(e)
	}
	defer c3.Close()
	if calls != 3 || r.selected != 0 || external.closed.Load() != 1 {
		t.Fatal("local preference did not recover after idle hold-down")
	}
}

func TestTransportConfigurationRequiresLegacyAnchor(t *testing.T) {
	anchor := routeFixture("local", "192.168.50.2:54446")
	other := routeFixture("external", "192.0.2.20:443")
	if _, e := configuredRegions(NodeConfig{Relay: anchor.Relay, Candidates: []RouteCandidate{other}}); e == nil {
		t.Fatal("missing anchor accepted")
	}
	if _, e := configuredRegions(NodeConfig{Relay: anchor.Relay, Candidates: []RouteCandidate{anchor, anchor}}); e == nil {
		t.Fatal("duplicate endpoint accepted")
	}
	if regions, e := configuredRegions(NodeConfig{Relay: anchor.Relay, Candidates: []RouteCandidate{anchor, other}}); e != nil || len(regions) != 2 {
		t.Fatal("explicit candidate set rejected", e)
	}
}

func TestTransportDeadRouteRecoversUDPWithHungOldFlow(t *testing.T) {
	r, anchor, candidates := routeRuntimeFixture(t, time.Time{})
	first, second := &fakePeerTransport{}, &fakePeerTransport{}
	calls := 0
	r.makeClient = func(tailcat.Addr) peerTransport {
		calls++
		if calls == 1 {
			return first
		}
		return second
	}
	if err := r.prepare(anchor, false, RouteSnapshot{Permitted: candidates}); err != nil {
		t.Fatal(err)
	}
	old, err := r.dial(context.Background(), "tcp", 8080)
	if err != nil {
		t.Fatal(err)
	}
	first.probeErr = syscall.ECONNREFUSED
	current, err := r.dial(context.Background(), "udp", 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if calls != 2 || first.probes.Load() != 2 || first.closed.Load() != 1 || r.activeFlows() != 1 {
		t.Fatal("new UDP dial did not replace proved-dead transport")
	}
	if _, err := old.Write([]byte("must not replay")); !errors.Is(err, ErrRoutePermission) {
		t.Fatal("old generation regained authority", err)
	}
	old.Close()
	old.Close()
	if r.activeFlows() != 1 {
		t.Fatal("closing old flow corrupted the new generation count")
	}
}

func TestTransportServiceFailureKeepsHealthyRelay(t *testing.T) {
	r, anchor, candidates := routeRuntimeFixture(t, time.Time{})
	serviceErr := syscall.ECONNREFUSED
	fake := &fakePeerTransport{dialErr: serviceErr}
	calls := 0
	r.makeClient = func(tailcat.Addr) peerTransport { calls++; return fake }
	if err := r.prepare(anchor, false, RouteSnapshot{Permitted: candidates}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.dial(context.Background(), "tcp", 8080); !errors.Is(err, serviceErr) {
		t.Fatal(err)
	}
	if fake.probes.Load() != 2 || calls != 1 || fake.closed.Load() != 0 || r.selected != 0 {
		t.Fatal("application failure was treated as a dead relay")
	}
}

func TestTransportObservationNeverWaitsForDialLock(t *testing.T) {
	n := testNode()
	peer := "fixture"
	r := &remoteClient{}
	r.observation.Store(&RouteObservation{State: "ready", CandidateID: "candidate", Scope: "external", Path: "direct", ObservedAt: time.Now()})
	n.clients[peer] = r
	r.startMu.Lock()
	done := make(chan RouteObservation, 1)
	go func() { observation, _ := n.RouteObservation(peer); done <- observation }()
	select {
	case got := <-done:
		if got.Path != "direct" || got.Scope != "external" {
			t.Fatal("relay scope conflated with packet path")
		}
	case <-time.After(time.Second):
		r.startMu.Unlock()
		t.Fatal("status waited for active dial")
	}
	r.startMu.Unlock()
	r.observation.Store(&RouteObservation{State: "ready", Path: "relay", ObservedAt: time.Now().Add(-time.Minute)})
	got, _ := n.RouteObservation(peer)
	if got.State != "unknown" || got.Path != "unknown" {
		t.Fatal("stale evidence claimed ready")
	}
}

func TestTransportExpiryReDerivesRemainingApproval(t *testing.T) {
	a, b, now, candidates := routeNodesFixture(t)
	routeApplyFixture(t, a, b, candidates, now)
	b.mu.Lock()
	r := b.clients[a.PublicKey()]
	for i := range r.remote.Routes.Approvals {
		if r.remote.Routes.Approvals[i].CandidateID == candidates[1].ID() {
			r.remote.Routes.Approvals[i].Expires = time.Now().Add(50 * time.Millisecond)
		}
	}
	b.mu.Unlock()
	old, err := b.client(context.Background(), a.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !old.retired.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !old.retired.Load() {
		t.Fatal("earliest approval expiry did not retire runtime")
	}
	fresh, err := b.client(context.Background(), a.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if fresh == old || len(fresh.candidates) != 1 || fresh.candidates[0] != candidates[0] {
		t.Fatal("expiry removed still-approved alternative or reused stale runtime")
	}
}

func TestTransportRepeatedRecoveryRetiresExactlyOldGenerations(t *testing.T) {
	r, anchor, candidates := routeRuntimeFixture(t, time.Time{})
	var transports []*fakePeerTransport
	r.makeClient = func(tailcat.Addr) peerTransport {
		f := &fakePeerTransport{}
		transports = append(transports, f)
		return f
	}
	if err := r.prepare(anchor, false, RouteSnapshot{Permitted: candidates}); err != nil {
		t.Fatal(err)
	}
	var flows []net.Conn
	for turn := range 4 {
		if turn > 0 {
			transports[turn-1].probeErr = syscall.ECONNREFUSED
			for _, candidate := range candidates {
				r.failures[candidate.ID()] = time.Time{}
			}
		}
		c, err := r.dial(context.Background(), "tcp", 8080)
		if err != nil {
			t.Fatal(err)
		}
		flows = append(flows, c)
		if _, err := c.Write([]byte("one request")); err != nil {
			t.Fatal(err)
		}
		if r.activeFlows() != 1 || len(transports) != turn+1 {
			t.Fatal("recovery retained multiple live generations")
		}
		for _, old := range transports[:turn] {
			if old.closed.Load() != 1 {
				t.Fatal("superseded engine did not close exactly once")
			}
		}
	}
	for _, flow := range flows {
		flow.Close()
		flow.Close()
	}
	if r.activeFlows() != 0 {
		t.Fatal("old generation closes corrupted final flow count")
	}
	for _, transport := range transports {
		if len(transport.conns) != 1 || transport.conns[0].written.Load() != 11 {
			t.Fatal("application bytes were repeated during recovery")
		}
	}
}
