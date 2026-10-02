package ranges

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestEngine(t *testing.T, opts Options) *Engine {
	t.Helper()
	if opts.Authorize == nil {
		opts.Authorize = func(context.Context, Request) (string, error) { return "peer-a", nil }
	}
	e, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = e.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := e.Wait(ctx); err != nil {
			t.Error("engine did not drain:", err)
		}
	})
	return e
}
func replaceTestPlan(t *testing.T, e *Engine, policies ...Policy) {
	t.Helper()
	if err := e.Replace([]netip.Addr{testSelf, testSelf6}, policies); err != nil {
		t.Fatal(err)
	}
}
func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("condition did not become true")
		case <-ticker.C:
		}
	}
}
func await(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not finish")
	}
}
func startFlow(t *testing.T, e *Engine, src, dst netip.AddrPort) (net.Conn, <-chan struct{}) {
	t.Helper()
	handler, intercept := e.Handle(src, dst)
	if !intercept || handler == nil {
		t.Fatalf("flow not selected: %s -> %s", src, dst)
	}
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); handler(server) }()
	t.Cleanup(func() { _ = client.Close() })
	return client, done
}
func endpoint(port uint16) netip.AddrPort { return netip.AddrPortFrom(testSelf, port) }

func TestSelectorIsCompactPureAndCapturesScope(t *testing.T) {
	var auth, dials, admissions atomic.Int32
	e := newTestEngine(t, Options{Authorize: func(context.Context, Request) (string, error) { auth.Add(1); return "peer-a", nil }, DialLoopback: func(context.Context, netip.AddrPort) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("unused")
	}, AdmitTCP: func() (func(), bool) { admissions.Add(1); return func() {}, true }})
	replaceTestPlan(t, e, testPolicy(t, "all", "1-65535"))
	for _, port := range []uint16{1, 22, 54542, 54546, 65535} {
		if handler, intercept := e.Handle(testSource, endpoint(port)); handler == nil || !intercept {
			t.Fatalf("missing port %d", port)
		}
	}
	for _, pair := range [][2]netip.AddrPort{
		{testSource, endpoint(0)}, {testSource, endpoint(DiscoveryPort)}, {testSource, endpoint(PeerTransferPort)}, {testSource, endpoint(PairingPort)},
		{netip.MustParseAddrPort("127.0.0.1:22"), endpoint(22)},
		{netip.MustParseAddrPort("100.100.100.100:22"), endpoint(22)},
		{netip.MustParseAddrPort("100.64.0.2:0"), endpoint(22)},
		{testSource, netip.MustParseAddrPort("100.64.0.99:22")},
		{testSource, netip.MustParseAddrPort("127.0.0.1:22")},
	} {
		if handler, intercept := e.Handle(pair[0], pair[1]); handler != nil || !intercept {
			t.Errorf("unsafe flow selected: %v", pair)
		}
	}
	if auth.Load() != 0 || dials.Load() != 0 || admissions.Load() != 0 {
		t.Fatal("selector called a blocking/admission dependency")
	}
	s := e.Stats()
	if s.Policies != 1 || s.Intervals != 2 || s.Active != 0 {
		t.Fatalf("range allocated per-port resources: %+v", s)
	}
}

func TestFlowAuthenticatesCapturedEndpointAndReassignmentCloses(t *testing.T) {
	var identity atomic.Value
	identity.Store("peer-a")
	requests := make(chan Request, 16)
	service := make(chan net.Conn, 1)
	targets := make(chan netip.AddrPort, 1)
	e := newTestEngine(t, Options{Authorize: func(_ context.Context, r Request) (string, error) {
		requests <- r
		return identity.Load().(string), nil
	}, DialLoopback: func(_ context.Context, target netip.AddrPort) (net.Conn, error) {
		targets <- target
		a, b := net.Pipe()
		service <- b
		return a, nil
	}})
	rule := testPolicy(t, "shared", "443,65535")
	rule.PeerIDs = []string{"peer-a", "peer-b"}
	replaceTestPlan(t, e, rule)
	client, done := startFlow(t, e, testSource, endpoint(65535))
	var app net.Conn
	select {
	case app = <-service:
	case <-time.After(3 * time.Second):
		t.Fatal("missing dial")
	}
	defer app.Close()
	if got := <-targets; got != netip.MustParseAddrPort("127.0.0.1:65535") {
		t.Fatalf("wrong same-port loopback target %s", got)
	}
	for i := 0; i < 2; i++ {
		select {
		case r := <-requests:
			if r.PolicyID != "shared" || r.Source != testSource || r.Destination != endpoint(65535) || !r.ExpiresAt.Equal(rule.ExpiresAt) {
				t.Fatalf("lost captured scope: %+v", r)
			}
			if i == 1 && r.PeerID != "peer-a" {
				t.Fatalf("identity not pinned after first auth: %+v", r)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("missing authorization")
		}
	}
	written := make(chan struct{})
	go func() { defer close(written); _, _ = client.Write([]byte("ping")) }()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(app, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("forwarding failed: %q %v", buf, err)
	}
	await(t, written)
	identity.Store("peer-b")
	if err := e.Revalidate(context.Background()); err == nil {
		t.Fatal("accepted reassigned source even though another allowed peer")
	}
	await(t, done)
	if e.Stats().Active != 0 {
		t.Fatal("revoked flow kept budget")
	}
}

func TestSnapshotReplacementAndStaleHandler(t *testing.T) {
	var authorizations atomic.Int32
	e := newTestEngine(t, Options{Authorize: func(context.Context, Request) (string, error) { authorizations.Add(1); return "peer-a", nil }})
	rule := testPolicy(t, "share", "8000-9000")
	replaceTestPlan(t, e, rule)
	handler, _ := e.Handle(testSource, endpoint(8500))
	ambiguous := rule
	ambiguous.ID = "overlap"
	if err := e.Replace([]netip.Addr{testSelf}, []Policy{rule, ambiguous}); err == nil {
		t.Fatal("accepted ambiguous plan")
	}
	if selected, _ := e.Handle(testSource, endpoint(8500)); selected == nil {
		t.Fatal("invalid replace damaged old snapshot")
	}
	if got := e.RevokeIDs([]string{"share"}); got != 1 {
		t.Fatalf("revoked %d policies", got)
	}
	a, b := net.Pipe()
	defer b.Close()
	handler(a)
	if authorizations.Load() != 0 || e.Stats().Active != 0 {
		t.Fatal("stale selected handler admitted after revocation")
	}
	if err := e.Replace([]netip.Addr{testSelf6}, []Policy{rule}); err == nil {
		t.Fatal("accepted no-longer-current self IP")
	}
	if err := e.Replace([]netip.Addr{testSelf6}, nil); err != nil {
		t.Fatal(err)
	}
	if selected, _ := e.Handle(testSource, endpoint(8500)); selected != nil {
		t.Fatal("stale own address still selected")
	}
}

func TestSamePlanPreservesFlowChangedPlanRevokes(t *testing.T) {
	entered := make(chan struct{}, 1)
	e := newTestEngine(t, Options{DialLoopback: func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	rule := testPolicy(t, "share", "8080")
	replaceTestPlan(t, e, rule)
	_, done := startFlow(t, e, testSource, endpoint(8080))
	await(t, entered)
	replaceTestPlan(t, e, rule)
	select {
	case <-done:
		t.Fatal("identical publication canceled a live permit")
	default:
	}
	rule.Loopback = netip.IPv6Loopback()
	replaceTestPlan(t, e, rule)
	await(t, done)
	if e.Stats().Active != 0 {
		t.Fatal("changed policy leaked admission")
	}
}

func TestExpiryAndCloseCancelAuthentication(t *testing.T) {
	for _, mode := range []string{"expiry", "close"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{})
			var calls atomic.Int32
			e := newTestEngine(t, Options{Authorize: func(ctx context.Context, _ Request) (string, error) {
				close(entered)
				<-ctx.Done()
				return "", ctx.Err()
			}, DialLoopback: func(context.Context, netip.AddrPort) (net.Conn, error) {
				calls.Add(1)
				return nil, errors.New("must not dial")
			}})
			rule := testPolicy(t, "share", "8080")
			if mode == "expiry" {
				rule.ExpiresAt = time.Now().Add(60 * time.Millisecond)
			}
			replaceTestPlan(t, e, rule)
			_, done := startFlow(t, e, testSource, endpoint(8080))
			await(t, entered)
			if mode == "close" {
				_ = e.Close()
			}
			await(t, done)
			if calls.Load() != 0 || e.Stats().Active != 0 {
				t.Fatal("expired/pending flow reached service or kept budget")
			}
			if handler, _ := e.Handle(testSource, endpoint(8080)); handler != nil {
				t.Fatal("expired/closed policy selected")
			}
		})
	}
}

func TestCancellationClosesLateDialResultAndReleasesBudget(t *testing.T) {
	entered := make(chan struct{})
	resume := make(chan struct{})
	lateClient, lateService := net.Pipe()
	defer lateService.Close()
	var acquired, released atomic.Int32
	e := newTestEngine(t, Options{AdmitTCP: func() (func(), bool) { acquired.Add(1); return func() { released.Add(1) }, true }, DialLoopback: func(context.Context, netip.AddrPort) (net.Conn, error) {
		close(entered)
		<-resume
		return lateClient, nil
	}})
	replaceTestPlan(t, e, testPolicy(t, "share", "8080"))
	_, done := startFlow(t, e, testSource, endpoint(8080))
	await(t, entered)
	closed := make(chan struct{})
	go func() { _ = e.Close(); close(closed) }()
	await(t, closed)
	if handler, _ := e.Handle(testSource, endpoint(8080)); handler != nil {
		t.Fatal("close did not synchronously revoke")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := e.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait should honor drain deadline: %v", err)
	}
	close(resume)
	await(t, done)
	if acquired.Load() != 1 || released.Load() != 1 || e.Stats().Active != 0 {
		t.Fatalf("admission leak: acquired %d released %d stats %+v", acquired.Load(), released.Load(), e.Stats())
	}
	if _, err := lateService.Write([]byte("x")); err == nil {
		t.Fatal("late dial result not closed")
	}
}

func TestAdmissionLimitsBeforeAuthAndByAuthenticatedPeer(t *testing.T) {
	for _, test := range []struct {
		name     string
		limits   Limits
		ports    []uint16
		sources  []netip.AddrPort
		wantAuth int32
	}{
		{"policy", Limits{PerPolicy: 1}, []uint16{8000, 8000}, []netip.AddrPort{testSource, netip.MustParseAddrPort("100.64.0.3:1")}, 1},
		{"global", Limits{Global: 1}, []uint16{8000, 9000}, []netip.AddrPort{testSource, netip.MustParseAddrPort("100.64.0.3:1")}, 1},
		{"source", Limits{PerPeer: 1}, []uint16{8000, 9000}, []netip.AddrPort{testSource, testSource}, 1},
		{"peer", Limits{PerPeer: 1}, []uint16{8000, 9000}, []netip.AddrPort{testSource, netip.MustParseAddrPort("[fd7a:115c:a1e0::2]:1")}, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var auth, dials atomic.Int32
			e := newTestEngine(t, Options{Limits: test.limits, Authorize: func(context.Context, Request) (string, error) { auth.Add(1); return "peer-a", nil }, DialLoopback: func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
				dials.Add(1)
				<-ctx.Done()
				return nil, ctx.Err()
			}})
			replaceTestPlan(t, e, testPolicy(t, "a", "8000"), testPolicy(t, "b", "9000"))
			_, first := startFlow(t, e, test.sources[0], endpoint(test.ports[0]))
			eventually(t, func() bool { return dials.Load() == 1 })
			_, second := startFlow(t, e, test.sources[1], endpoint(test.ports[1]))
			await(t, second)
			if auth.Load() != test.wantAuth || dials.Load() != 1 || e.Stats().Active != 1 {
				t.Fatalf("bad admission counts auth=%d dials=%d stats=%+v", auth.Load(), dials.Load(), e.Stats())
			}
			e.RevokeIDs([]string{"a", "b"})
			await(t, first)
			if len(e.Stats().PerPeer) != 0 || len(e.Stats().PerPolicy) != 0 {
				t.Fatal("empty budget entries retained")
			}
		})
	}
}

func TestBudgetReleasedOnAuthAndDialFailure(t *testing.T) {
	for _, failure := range []string{"auth", "dial-error", "nil-dial", "unlisted-peer"} {
		t.Run(failure, func(t *testing.T) {
			var held atomic.Int32
			e := newTestEngine(t, Options{Limits: Limits{Global: 1}, AdmitTCP: func() (func(), bool) { held.Add(1); return func() { held.Add(-1) }, true }, Authorize: func(context.Context, Request) (string, error) {
				if failure == "auth" {
					return "", errors.New("denied")
				}
				if failure == "unlisted-peer" {
					return "peer-b", nil
				}
				return "peer-a", nil
			}, DialLoopback: func(context.Context, netip.AddrPort) (net.Conn, error) {
				if failure == "dial-error" {
					return nil, errors.New("refused")
				}
				return nil, nil
			}})
			replaceTestPlan(t, e, testPolicy(t, "share", "8080"))
			for i := 0; i < 3; i++ {
				_, done := startFlow(t, e, testSource, endpoint(8080))
				await(t, done)
				if held.Load() != 0 || e.Stats().Active != 0 {
					t.Fatal("failed flow leaked a slot")
				}
			}
		})
	}
}

func TestRevokeAllPermitsBeforeDrainAndConcurrentSelection(t *testing.T) {
	e := newTestEngine(t, Options{DialLoopback: func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }})
	policies := []Policy{testPolicy(t, "a", "8000"), testPolicy(t, "b", "9000")}
	replaceTestPlan(t, e, policies...)
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 50; j++ {
				handler, _ := e.Handle(testSource, endpoint(8000))
				if handler != nil {
					a, b := net.Pipe()
					b.Close()
					handler(a)
				}
			}
		}()
	}
	e.RevokeIDs([]string{"a", "b"})
	if a, _ := e.Handle(testSource, endpoint(8000)); a != nil {
		t.Fatal("first revoked policy admitted")
	}
	if b, _ := e.Handle(testSource, endpoint(9000)); b != nil {
		t.Fatal("second revoked policy admitted")
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	await(t, done)
	if e.Stats().Active != 0 {
		t.Fatal("revocation/selection race leaked a flow")
	}
}
