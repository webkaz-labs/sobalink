package directlan

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// These fixtures construct only policy and in-memory lifecycle owners. They
// never call Start, buildContextGeneration, net.Listen, or a successful dial or
// transport preparer. Their listeners and connections have no OS resources.
const controlLifecycleWait = 3 * time.Second

func controlLifecycleIdentity(value byte) Identity {
	var seed [32]byte
	for i := range seed {
		seed[i] = value
	}
	return Identity{Seed: hex.EncodeToString(seed[:])}
}

func controlLifecycleConfig(limit int) ContextControlConfig {
	peer := controlLifecycleIdentity(102)
	return ContextControlConfig{
		Identity:        controlLifecycleIdentity(101),
		Listen:          netip.MustParseAddrPort("127.0.0.1:44101"),
		AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
		Peers: []Peer{{Key: peer.PublicKey(), Name: "synthetic peer",
			Endpoint: netip.MustParseAddrPort("127.0.0.1:44102"), TunnelKey: peer.TunnelKey()}},
		ControlLimit: limit,
	}
}

func controlLifecycleNode(t *testing.T, cfg ContextControlConfig) *Node {
	t.Helper()
	n, err := NewContextControl(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controlLifecycleClose(t, n) })
	return n
}

func controlLifecycleClose(t *testing.T, n *Node) error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- n.Close() }()
	timer := time.NewTimer(controlLifecycleWait)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-timer.C:
		t.Fatal("timed out waiting for Node cleanup")
		return nil
	}
}

// This bypasses only resource construction, never authentication. No proof is
// minted by this fixture. Production supervision and admission run unchanged.
func controlLifecycleGeneration(t *testing.T, n *Node) *runtimeGeneration {
	t.Helper()
	g := newRuntimeGeneration(n, nil, nil)
	g.controlPeers = make(map[string]*contextPeerRegistration)
	g.failedControl = make(map[*controlStream]error)
	for _, peer := range n.cfg.Peers {
		g.controlPeers[peer.Key] = &contextPeerRegistration{g: g, peer: peer}
	}
	n.generation.Store(g)
	n.started = true
	g.controlOpen.Store(true)
	close(g.published)
	go g.supervise()
	// Even an assertion failure before the fake listener is attached must let
	// the real supervisor finish when Node cleanup requests stop.
	t.Cleanup(func() { controlLifecycleCompleteBuild(g) })
	return g
}

func controlLifecycleCompleteBuild(g *runtimeGeneration) {
	// Called only by the test goroutine and its later cleanup, never a builder.
	select {
	case <-g.built:
	default:
		close(g.built)
	}
}

func controlLifecycleAwait(t *testing.T, done <-chan struct{}, timeout time.Duration, what string) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", what)
	}
}

func controlLifecyclePending(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
		t.Fatalf("%s completed before its owned work returned", what)
	default:
	}
}

func controlLifecycleCounts(g *runtimeGeneration) (controls, work, failed int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.controlCount, len(g.work), len(g.failedControl)
}

func controlLifecycleRequireCounts(t *testing.T, g *runtimeGeneration, controls, work, failed int) {
	t.Helper()
	c, w, f := controlLifecycleCounts(g)
	if c != controls || w != work || f != failed {
		t.Fatalf("ownership counts = (%d, %d, %d), want (%d, %d, %d)", c, w, f, controls, work, failed)
	}
}

func controlLifecycleRejectWork(t *testing.T, g *runtimeGeneration, want error) {
	t.Helper()
	work, err := g.acquireWork(nil, true)
	// A regression must fail the assertion without stranding fixture work.
	if work != nil {
		work.finish()
	}
	if work != nil || !errors.Is(err, want) {
		t.Fatalf("control admission = work present %t, error %v; want %v", work != nil, err, want)
	}
}

// A failed physical Close is terminal recovery, not reusable capacity pressure.
// The retained wrapper and the rejection slot can reference the same raw socket;
// neither reference authorizes another physical Close or owner detachment.
func controlLifecycleRequireRetainedFailure(t *testing.T, g *runtimeGeneration, raw *controlLifecycleConn, want error) {
	t.Helper()
	controlLifecycleRequireCounts(t, g, 1, 0, 1)
	g.mu.Lock()
	sealed := g.sealed
	var retained *controlStream
	var retainedErr error
	for stream, err := range g.failedControl {
		retained, retainedErr = stream, err
	}
	g.mu.Unlock()
	if !sealed || g.open() || g.controlOpen.Load() || retained == nil || retained.g != g || retained.raw != raw || !errors.Is(retainedErr, want) || raw.closeCalls.Load() != 1 {
		t.Fatal("failed Close lost its exact owner/error, retried physical cleanup, or left admission open")
	}
	controlLifecycleRejectWork(t, g, ErrRecovery)
	// Even repeating Close on the exact retained owner must return its original
	// error without increasing the retained charge or touching the raw socket.
	if err := retained.Close(); !errors.Is(err, want) {
		t.Fatalf("retained Close lost error: %v", err)
	}
	controlLifecycleRequireCounts(t, g, 1, 0, 1)
	if raw.closeCalls.Load() != 1 {
		t.Fatal("retained Close retried the raw socket")
	}
	g.origin.mu.Lock()
	attached := g.origin.g == g
	g.origin.mu.Unlock()
	if !attached {
		t.Fatal("failed cleanup detached its generation origin")
	}
}

func controlLifecycleArm(t *testing.T, n *Node) *ContextAttempt {
	t.Helper()
	a, err := n.ArmContextAttempt(n.cfg.Peers[0].Key, endpointmeta.BoundRequest{
		Version: 2, Operation: "pair-context-status", PairBinding: strings.Repeat("ab", 32),
	}, NewContextEpoch())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

type controlLifecycleAddr string

func (a controlLifecycleAddr) Network() string { return "tcp" }
func (a controlLifecycleAddr) String() string  { return string(a) }

// Read waits for the real deadline or Close. Close wakes Read before waiting
// on a bounded cleanup gate, so a held Close never invents a held TLS callback.
type controlLifecycleConn struct {
	remote       net.Addr
	closed       chan struct{}
	readEntered  chan struct{}
	closeEntered chan struct{}
	deadlineSet  chan time.Time
	closeRelease <-chan struct{}
	closeErr     error
	closeCalls   atomic.Int32
	readOnce     sync.Once
	closeOnce    sync.Once
	mu           sync.Mutex
	deadline     time.Time
}

func controlLifecycleConnection(remote string) *controlLifecycleConn {
	return &controlLifecycleConn{remote: controlLifecycleAddr(remote), closed: make(chan struct{}),
		readEntered: make(chan struct{}), closeEntered: make(chan struct{}), deadlineSet: make(chan time.Time, 1)}
}

func (c *controlLifecycleConn) Read([]byte) (int, error) {
	c.readOnce.Do(func() { close(c.readEntered) })
	c.mu.Lock()
	deadline := c.deadline
	c.mu.Unlock()
	if deadline.IsZero() {
		deadline = time.Now().Add(3 * handshakeTimeout)
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	case <-timer.C:
		return 0, os.ErrDeadlineExceeded
	}
}

func (c *controlLifecycleConn) Write(b []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
		return len(b), nil
	}
}

func (c *controlLifecycleConn) Close() error {
	c.closeCalls.Add(1)
	c.closeOnce.Do(func() { close(c.closed); close(c.closeEntered) })
	if c.closeRelease != nil {
		timer := time.NewTimer(controlLifecycleWait)
		defer timer.Stop()
		select {
		case <-c.closeRelease:
		case <-timer.C:
			return errors.New("synthetic Close release timed out")
		}
	}
	return c.closeErr
}

func (c *controlLifecycleConn) LocalAddr() net.Addr {
	return controlLifecycleAddr("127.0.0.1:44101")
}
func (c *controlLifecycleConn) RemoteAddr() net.Addr { return c.remote }
func (c *controlLifecycleConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadline = deadline
	c.mu.Unlock()
	select {
	case c.deadlineSet <- deadline:
	default:
	}
	return nil
}
func (c *controlLifecycleConn) SetReadDeadline(deadline time.Time) error {
	return c.SetDeadline(deadline)
}
func (c *controlLifecycleConn) SetWriteDeadline(time.Time) error { return nil }

type controlLifecycleListener struct {
	mu           sync.Mutex
	queue        []net.Conn
	calls        int
	beforeReturn func(int)
	closed       chan struct{}
	closeOnce    sync.Once
}

func controlLifecycleListenerFor(connections ...net.Conn) *controlLifecycleListener {
	return &controlLifecycleListener{queue: connections, closed: make(chan struct{})}
}

func (l *controlLifecycleListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	l.calls++
	call := l.calls
	var c net.Conn
	if len(l.queue) != 0 {
		c, l.queue = l.queue[0], l.queue[1:]
	}
	l.mu.Unlock()
	if c != nil {
		if l.beforeReturn != nil {
			l.beforeReturn(call)
		}
		return c, nil
	}
	timer := time.NewTimer(3 * handshakeTimeout)
	defer timer.Stop()
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case <-timer.C:
		return nil, os.ErrDeadlineExceeded
	}
}

func (l *controlLifecycleListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}
func (l *controlLifecycleListener) Addr() net.Addr {
	return controlLifecycleAddr("127.0.0.1:44101")
}

func controlLifecycleUnderlay(g *runtimeGeneration, l *controlLifecycleListener) *generationUnderlay {
	u := newGenerationUnderlay(l)
	g.underlay = u
	u.start(g.n, g)
	return u
}

func TestContextControlModeExcludesApplicationAdmission(t *testing.T) {
	cfg := controlLifecycleConfig(1)
	n := controlLifecycleNode(t, cfg)
	if !n.contextControl || n.bind != nil || n.tunnel != nil || n.engine != nil || n.underlay != nil ||
		n.generation.Load() != nil || len(n.peers) != 0 || n.listeners != nil || n.flows != nil || n.dispatchSlots != nil {
		t.Fatal("policy construction created application or transport resources")
	}
	savedPeer, savedPrefix := cfg.Peers[0], cfg.AllowedPrefixes[0]
	cfg.Peers[0].Name = "changed synthetic input"
	cfg.AllowedPrefixes[0] = netip.MustParsePrefix("10.0.0.0/8")
	if n.cfg.Peers[0] != savedPeer || n.cfg.AllowedPrefixes[0] != savedPrefix {
		t.Fatal("constructor retained mutable policy slices")
	}
	if err := n.ControlReady(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unstarted control readiness = %v", err)
	}

	for _, state := range []string{"unstarted", "control-ready"} {
		if state == "control-ready" {
			g := controlLifecycleGeneration(t, n)
			controlLifecycleCompleteBuild(g)
			if err := n.ControlReady(); err != nil {
				t.Fatal(err)
			}
			if g.bind != nil || g.tunnel != nil || g.engine.Load() != nil || len(g.peers) != 0 ||
				len(g.peerRegistrations) != 0 || g.traffic.Load() || len(g.controlPeers) != 1 {
				t.Fatal("control generation gained WG or application admission")
			}
		}
		t.Run(state, func(t *testing.T) {
			// Every API below rejects immutable control mode before constructing a
			// listener, dialing, creating a candidate, or invoking a dispatcher.
			if err := n.Ready(); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("ordinary readiness = %v", err)
			}
			if l, err := n.ListenPeer(context.Background(), "tcp", 41000); l != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("application listener = %v, %v", l, err)
			}
			if c, err := n.DialPeer(context.Background(), savedPeer.Key, "tcp", 41000); c != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("application dial = %v, %v", c, err)
			}
			if p, err := n.CapturePeer(savedPeer.Key); p != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("peer capture = %v, %v", p, err)
			}
			if p, err := n.CaptureDial(savedPeer.Key, "tcp", 41000); p != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("dial capture = %v, %v", p, err)
			}
			if origin, err := n.CaptureTransportOrigin(); origin != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("transport capture = %v, %v", origin, err)
			}
			var called atomic.Bool
			if cancel, err := n.RegisterTCPFallback(func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool) {
				called.Store(true)
				return nil, false
			}); cancel != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("fallback registration = %v", err)
			}
			if n.dispatch(n.generation.Load(), func() { called.Store(true) }) || called.Load() {
				t.Fatal("control mode dispatched application work")
			}
			if candidate, err := n.PrepareTransport(context.Background(), nil, TransportEndpoints{}); candidate != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("transport preparation = %v, %v", candidate, err)
			}
			if n.staged.Load() != nil || n.building.Load() != nil || n.fallback != nil || len(n.wires) != 0 || len(n.dials) != 0 {
				t.Fatal("rejected application API retained resources")
			}
		})
	}
}

func TestContextControlNilResourceSupervisionJoinsWork(t *testing.T) {
	// This is the socket-free subset of proposed inventory case 2.
	// buildContextGeneration calls net.Listen directly and has no injectable
	// listener factory. This tests its nil-resource supervisor state and real
	// built/work barriers, not a simulated successful execution of that builder.
	for _, cause := range []error{errors.New("synthetic listen failure"), context.Canceled} {
		t.Run(cause.Error(), func(t *testing.T) {
			n := controlLifecycleNode(t, controlLifecycleConfig(1))
			g := controlLifecycleGeneration(t, n)
			var builtOnce sync.Once
			finishBuild := func() { builtOnce.Do(func() { close(g.built) }) }
			t.Cleanup(finishBuild)
			cancelled := make(chan struct{})
			var cancelOnce sync.Once
			work, err := g.acquireWork(func() { cancelOnce.Do(func() { close(cancelled) }) }, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(work.finish)
			g.requestStop(cause)
			controlLifecyclePending(t, g.done, "nil-resource generation")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := g.wait(ctx); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrRecovery) {
				t.Fatalf("unjoined generation wait = %v", err)
			}
			finishBuild()
			controlLifecycleAwait(t, cancelled, controlLifecycleWait, "owned work cancellation")
			controlLifecyclePending(t, g.done, "cancelled but unreturned work")
			controlLifecycleRequireCounts(t, g, 1, 1, 0)
			work.finish()
			controlLifecycleAwait(t, g.done, controlLifecycleWait, "nil-resource supervision")
			if err := g.wait(context.Background()); err != nil {
				t.Fatalf("nil-resource cleanup = %v", err)
			}
			if g.bind != nil || g.tunnel != nil || g.underlay != nil || g.engine.Load() != nil || g.open() || g.controlOpen.Load() || !errors.Is(g.cause, cause) {
				t.Fatal("nil-resource cleanup changed ownership or reopened admission")
			}
			controlLifecycleRejectWork(t, g, ErrRecovery)
			controlLifecycleRequireCounts(t, g, 0, 0, 0)
		})
	}
}

func TestContextControlRepeatedCloseRetainsError(t *testing.T) {
	n := controlLifecycleNode(t, controlLifecycleConfig(1))
	g := controlLifecycleGeneration(t, n)
	controlLifecycleCompleteBuild(g)
	work, err := g.acquireWork(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(work.finish)
	want := errors.New("synthetic raw Close failure")
	raw := controlLifecycleConnection("127.0.0.1:44102")
	raw.closeErr = want
	stream := newControlStream(g, raw)
	for i := 0; i < 3; i++ {
		if err := stream.Close(); !errors.Is(err, want) {
			t.Fatalf("Close %d = %v", i, err)
		}
	}
	if raw.closeCalls.Load() != 1 {
		t.Fatalf("raw Close calls = %d, want one", raw.closeCalls.Load())
	}
	controlLifecycleRequireCounts(t, g, 2, 1, 1)
	work.finish()
	controlLifecycleRequireRetainedFailure(t, g, raw, want)
	g.mu.Lock()
	cause := g.cause
	g.mu.Unlock()
	if !errors.Is(cause, ErrRecovery) || !errors.Is(cause, want) {
		t.Fatalf("failed Close did not seal with recovery and original cause: %v", cause)
	}
	for i := 0; i < 2; i++ {
		if err := controlLifecycleClose(t, n); !errors.Is(err, want) {
			t.Fatalf("Node.Close %d lost terminal error: %v", i, err)
		}
	}
	controlLifecycleRequireCounts(t, g, 1, 0, 1)
	g.origin.mu.Lock()
	retained := g.origin.g == g
	g.origin.mu.Unlock()
	if !retained || g.open() {
		t.Fatal("failed physical close detached its owner or reopened admission")
	}
}

func TestContextControlRejectedCloseRetainsOneSlot(t *testing.T) {
	for _, branch := range []string{"policy", "wire-capacity", "work-capacity", "concurrent-stop"} {
		t.Run(branch, func(t *testing.T) {
			n := controlLifecycleNode(t, controlLifecycleConfig(1))
			g := controlLifecycleGeneration(t, n)
			first := controlLifecycleConnection("127.0.0.1:44102")
			second := controlLifecycleConnection("127.0.0.1:44103")
			want := errors.New("synthetic rejected Close failure")
			first.closeErr = want
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			first.closeRelease = release
			l := controlLifecycleListenerFor(first, second)
			var work *generationWork
			switch branch {
			case "policy":
				first.remote = controlLifecycleAddr("192.0.2.10:44102")
			case "wire-capacity":
				n.dials[&pendingDial{g: g, control: true, cancel: func() {}}] = struct{}{}
			case "work-capacity":
				var err error
				work, err = g.acquireWork(nil, true)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(work.finish)
			case "concurrent-stop":
				l.beforeReturn = func(int) { n.RequestClose() }
			}
			u := controlLifecycleUnderlay(g, l)
			controlLifecycleCompleteBuild(g)
			controlLifecycleAwait(t, first.closeEntered, controlLifecycleWait, "rejected Close entry")
			controlLifecyclePending(t, u.accepted, "accept loop with held rejection")
			controlLifecyclePending(t, g.done, "held rejection cleanup")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := u.WaitClosed(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("unjoined rejected close = %v", err)
			}
			unblock()
			controlLifecycleAwait(t, u.accepted, controlLifecycleWait, "rejection accept-loop return")
			if u.rejected != first || !errors.Is(u.rejectionErr, want) || first.closeCalls.Load() != 1 || second.closeCalls.Load() != 0 {
				t.Fatal("failed rejection lost its exact socket/error or consumed another socket")
			}
			l.mu.Lock()
			calls, queued := l.calls, len(l.queue)
			l.mu.Unlock()
			if calls != 1 || queued != 1 {
				t.Fatalf("failed rejection accepted %d sockets with %d queued", calls, queued)
			}
			if work != nil {
				work.finish()
			}
			controlLifecycleAwait(t, g.done, controlLifecycleWait, "failed rejection supervision")
			controlLifecycleRequireRetainedFailure(t, g, first, want)
			g.mu.Lock()
			cause := g.cause
			g.mu.Unlock()
			if branch == "concurrent-stop" {
				if !errors.Is(cause, net.ErrClosed) {
					t.Fatalf("cleanup replaced the earlier stop cause: %v", cause)
				}
			} else if !errors.Is(cause, ErrRecovery) || !errors.Is(cause, want) {
				t.Fatalf("rejection failure did not seal with original cause: %v", cause)
			}
			if err := controlLifecycleClose(t, n); !errors.Is(err, want) {
				t.Fatalf("rejection error missing from terminal owner: %v", err)
			}
			controlLifecycleRequireRetainedFailure(t, g, first, want)
			l.mu.Lock()
			finalCalls, finalQueued := l.calls, len(l.queue)
			l.mu.Unlock()
			if finalCalls != 1 || finalQueued != 1 || second.closeCalls.Load() != 0 {
				t.Fatal("terminal cleanup resumed rejected acceptance")
			}
		})
	}
	t.Run("successful-rejections-release-slot", func(t *testing.T) {
		n := controlLifecycleNode(t, controlLifecycleConfig(1))
		g := controlLifecycleGeneration(t, n)
		first := controlLifecycleConnection("192.0.2.10:44102")
		second := controlLifecycleConnection("192.0.2.11:44103")
		u := controlLifecycleUnderlay(g, controlLifecycleListenerFor(first, second))
		controlLifecycleCompleteBuild(g)
		controlLifecycleAwait(t, second.closeEntered, controlLifecycleWait, "second successful rejection")
		u.RequestClose()
		controlLifecycleAwait(t, u.accepted, controlLifecycleWait, "successful rejection loop")
		if u.rejected != nil || u.rejectionErr != nil || first.closeCalls.Load() != 1 || second.closeCalls.Load() != 1 {
			t.Fatal("successful rejection retained a slot or skipped a close")
		}
		controlLifecycleRequireCounts(t, g, 0, 0, 0)
		if !g.open() || !g.controlOpen.Load() {
			t.Fatal("successful rejected cleanup sealed the generation")
		}
		work, err := g.acquireWork(nil, true)
		if err != nil {
			t.Fatalf("successful rejection did not release capacity: %v", err)
		}
		work.finish()
		if err := controlLifecycleClose(t, n); err != nil {
			t.Fatal(err)
		}
		controlLifecycleRequireCounts(t, g, 0, 0, 0)
		g.origin.mu.Lock()
		detached := g.origin.g == nil
		g.origin.mu.Unlock()
		if !detached {
			t.Fatal("successful terminal cleanup retained its origin")
		}
	})
}

func TestContextControlHandshakeCapacityThroughCleanup(t *testing.T) {
	// This is the inbound failed-handshake subset of inventory case 5.
	// This covers actual inbound handshake failure and joined cancellation
	// cleanup. Successful proof/callback/response ownership is exercised by the
	// separate authenticated in-memory exchange fixtures, not fabricated here.
	for _, failed := range []bool{false, true} {
		name := "successful-close"
		if failed {
			name = "failed-close"
		}
		t.Run(name, func(t *testing.T) {
			n := controlLifecycleNode(t, controlLifecycleConfig(1))
			g := controlLifecycleGeneration(t, n)
			a := controlLifecycleArm(t, n)
			controlLifecycleRequireCounts(t, g, 0, 0, 0)
			raw := controlLifecycleConnection("127.0.0.1:44102")
			want := errors.New("synthetic cleanup failure")
			if failed {
				raw.closeErr = want
			}
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			raw.closeRelease = release
			controlLifecycleUnderlay(g, controlLifecycleListenerFor(raw))
			controlLifecycleCompleteBuild(g)
			controlLifecycleAwait(t, raw.readEntered, controlLifecycleWait, "TLS handshake Read")
			if controls, _, _ := controlLifecycleCounts(g); controls != 1 {
				t.Fatalf("accepted handshake reservations = %d", controls)
			}
			controlLifecycleRejectWork(t, g, ErrCapacity)
			n.cancel()
			controlLifecycleAwait(t, raw.closeEntered, controlLifecycleWait, "cancellation Close")
			joined := make(chan struct{})
			go func() { n.wg.Wait(); close(joined) }()
			controlLifecyclePending(t, joined, "handler with Close watcher still running")
			if controls, _, failures := controlLifecycleCounts(g); controls != 1 || failures != 0 {
				t.Fatalf("held Close ownership = controls %d, failures %d", controls, failures)
			}
			controlLifecycleRejectWork(t, g, ErrCapacity)
			unblock()
			controlLifecycleAwait(t, joined, controlLifecycleWait, "handler and cancellation watcher join")
			if raw.closeCalls.Load() != 1 || a.cell.started.Load() {
				t.Fatal("failed handshake retried physical Close or selected an unauthenticated arm")
			}
			n.mu.Lock()
			wires := len(n.wires)
			n.mu.Unlock()
			if wires != 0 {
				t.Fatal("returned handler retained a wire")
			}
			if failed {
				controlLifecycleRequireRetainedFailure(t, g, raw, want)
				g.mu.Lock()
				cause := g.cause
				g.mu.Unlock()
				if !errors.Is(cause, ErrRecovery) || !errors.Is(cause, want) {
					t.Fatalf("handshake cleanup did not seal with original failure: %v", cause)
				}
				if err := controlLifecycleClose(t, n); !errors.Is(err, want) {
					t.Fatalf("terminal failed Close = %v", err)
				}
				controlLifecycleRequireRetainedFailure(t, g, raw, want)
			} else {
				controlLifecycleRequireCounts(t, g, 0, 0, 0)
				work, err := g.acquireWork(nil, true)
				if err != nil {
					t.Fatalf("joined cleanup did not return capacity: %v", err)
				}
				work.finish()
			}
		})
	}
}

func TestContextControlInboundDeadlineStartsAfterIdleArm(t *testing.T) {
	// This is the inbound admission/deadline subset of inventory case 6.
	n := controlLifecycleNode(t, controlLifecycleConfig(1))
	g := controlLifecycleGeneration(t, n)
	a := controlLifecycleArm(t, n)
	controlLifecycleRequireCounts(t, g, 0, 0, 0)
	// Intentionally outlive one complete production handshake window while
	// owning metadata only. This takes just over 20 seconds including expiry.
	armedAt := time.Now()
	timer := time.NewTimer(handshakeTimeout + 20*time.Millisecond)
	<-timer.C
	if a.data() == nil || a.Cancelled() || a.cell.started.Load() {
		t.Fatal("idle arm consumed an exchange deadline")
	}
	controlLifecycleRequireCounts(t, g, 0, 0, 0)
	raw := controlLifecycleConnection("127.0.0.1:44102")
	admittedAfter := time.Now()
	controlLifecycleUnderlay(g, controlLifecycleListenerFor(raw))
	controlLifecycleCompleteBuild(g)
	controlLifecycleAwait(t, raw.readEntered, controlLifecycleWait, "admitted TLS Read")
	var deadline time.Time
	select {
	case deadline = <-raw.deadlineSet:
	default:
		t.Fatal("handshake started without a transport deadline")
	}
	if deadline.Before(admittedAfter.Add(handshakeTimeout)) || deadline.After(time.Now().Add(handshakeTimeout)) ||
		!deadline.After(armedAt.Add(handshakeTimeout)) {
		t.Fatal("exchange deadline was not assigned by actual work admission")
	}
	if controls, _, _ := controlLifecycleCounts(g); controls != 1 {
		t.Fatalf("deadline-bound admission reservations = %d", controls)
	}
	controlLifecycleAwait(t, raw.closeEntered, handshakeTimeout+controlLifecycleWait, "deadline cancellation cleanup")
	joined := make(chan struct{})
	go func() { n.wg.Wait(); close(joined) }()
	controlLifecycleAwait(t, joined, controlLifecycleWait, "expired handler and watcher join")
	controlLifecycleRequireCounts(t, g, 0, 0, 0)
	if time.Now().Before(deadline) || raw.closeCalls.Load() != 1 || a.cell.started.Load() {
		t.Fatal("deadline cleanup fired early, retried Close, or authenticated a stalled handshake")
	}
}
