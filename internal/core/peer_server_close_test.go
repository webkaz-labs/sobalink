package core

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/ranges"
)

type sequencedPeerCloseListener struct {
	mu       sync.Mutex
	calls    int
	accepts  int
	called   chan int
	firstErr error
}

func (l *sequencedPeerCloseListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	l.accepts++
	l.mu.Unlock()
	return nil, net.ErrClosed
}
func (l *sequencedPeerCloseListener) Addr() net.Addr { return &net.TCPAddr{} }
func (l *sequencedPeerCloseListener) Close() error {
	l.mu.Lock()
	l.calls++
	n := l.calls
	l.mu.Unlock()
	l.called <- n
	if n > 1 {
		return net.ErrClosed
	}
	return l.firstErr
}
func peerServerSequencedClose(t *testing.T, legacy bool, firstErr error) error {
	t.Helper()
	raw := &sequencedPeerCloseListener{called: make(chan int, 8), firstErr: firstErr}
	owner := &peerServerListener{Listener: raw}
	var served net.Listener = owner
	if legacy {
		served = raw
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	server := &http.Server{BaseContext: func(net.Listener) context.Context { close(entered); <-release; return context.Background() }}
	serving := make(chan error, 1)
	go func() { serving <- server.Serve(served) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("HTTP listener did not register")
	}
	p := &peerServer{http: server, listeners: []*peerServerListener{owner}}
	result := make(chan error, 1)
	go func() {
		if legacy {
			_ = raw.Close()
			result <- server.Close()
			return
		}
		result <- p.Close()
	}()
	expected := 1
	if legacy {
		expected = 2
	}
	// Holding BaseContext keeps the listener registered for the explicit and
	// HTTP close operations. No network socket or timing-dependent sleep is used.
	for want := 1; want <= expected; want++ {
		select {
		case got := <-raw.called:
			if got != want {
				t.Fatal("unexpected physical close order")
			}
		case <-time.After(time.Second):
			t.Fatal("expected close call did not arrive")
		}
	}
	unblock()
	var got error
	select {
	case got = <-result:
	case <-time.After(time.Second):
		t.Fatal("HTTP close did not join")
	}
	select {
	case <-serving:
	case <-time.After(time.Second):
		t.Fatal("HTTP Serve did not join")
	}
	if !legacy {
		again := p.Close()
		if firstErr == nil && again != nil || firstErr != nil && !errors.Is(again, firstErr) {
			t.Fatal("repeated owner close changed the original result")
		}
		raw.mu.Lock()
		calls := raw.calls
		raw.mu.Unlock()
		if calls != 1 {
			t.Fatal("owned listener was physically closed more than once")
		}
	}
	return got
}
func TestPeerServerLegacyCloseReportsBenignRepeatError(t *testing.T) {
	if !errors.Is(peerServerSequencedClose(t, true, nil), net.ErrClosed) {
		t.Fatal("original ownership defect was not reproduced")
	}
}
func TestPeerServerCloseOwnsPhysicalListener(t *testing.T) {
	if peerServerSequencedClose(t, false, nil) != nil {
		t.Fatal("clean owned listener shutdown failed")
	}
}
func TestPeerServerClosePreservesFirstRealError(t *testing.T) {
	sentinel := errors.New("synthetic close failure")
	for _, first := range []error{sentinel, net.ErrClosed, errors.Join(net.ErrClosed, sentinel)} {
		if got := peerServerSequencedClose(t, false, first); !errors.Is(got, first) {
			t.Fatal("first genuine close failure was hidden")
		}
	}
}
func TestPeerServerCloseOwnsUnregisteredAndConcurrentListeners(t *testing.T) {
	sentinel := errors.New("synthetic partial startup close failure")
	raw := &sequencedPeerCloseListener{called: make(chan int, 8), firstErr: sentinel}
	owner := &peerServerListener{Listener: raw}
	p := &peerServer{http: &http.Server{}, listeners: []*peerServerListener{owner}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !errors.Is(p.Close(), sentinel) {
				t.Error("partial startup owner lost its first error")
			}
		}()
	}
	wg.Wait()
	if raw.calls != 1 {
		t.Fatal("partial startup listener closed more than once")
	}
}

type partialPeerStartNode struct {
	*pipeNode
	raw       *sequencedPeerCloseListener
	packets   int
	listens   int
	listenErr error
}

func (n *partialPeerStartNode) Listen(string, string) (net.Listener, error) {
	n.listens++
	if n.listens == 1 {
		return n.raw, nil
	}
	return nil, n.listenErr
}
func TestPeerServerPartialStartRetainsFailureAndBlocksReplacement(t *testing.T) {
	first := errors.Join(net.ErrClosed, errors.New("synthetic genuine close failure"))
	raw := &sequencedPeerCloseListener{called: make(chan int, 8), firstErr: first}
	listenErr := errors.New("synthetic second listen failure")
	node := &partialPeerStartNode{raw: raw, listenErr: listenErr}
	c := &Core{ctx: context.Background(), node: node}
	c.networkReady.Store(true)
	err := c.startPeerServer([]netip.Addr{netip.MustParseAddr("100.64.0.1")})
	if !errors.Is(err, first) || !errors.Is(err, listenErr) || networkErrorCode(err) != "network_restart_required" {
		t.Fatal("partial-start failure lost original causes or restart classification")
	}
	retained := c.peerServer
	if retained == nil || retained.startFailure == nil || c.networkReady.Load() || c.networkState != "error" || c.networkFatal == "" {
		t.Fatal("uncertain partial owner was discarded or reported ready")
	}
	// These are the same status/admission boundaries used by maintenance,
	// Snapshot, service.start, reconnect, peer identity and outgoing peer HTTP.
	for i := 0; i < 3; i++ {
		if _, err := c.current(context.Background()); !errors.Is(err, first) {
			t.Fatal("healthy backend status could bypass retained failure")
		}
		if err := c.startNetwork(context.Background()); !errors.Is(err, first) {
			t.Fatal("network start replaced uncertain owner")
		}
		if err := c.startPeerServer([]netip.Addr{netip.MustParseAddr("100.64.0.1")}); !errors.Is(err, first) {
			t.Fatal("peer start replaced uncertain owner")
		}
	}
	if node.listens != 2 || c.peerServer != retained || raw.calls != 1 || raw.accepts != 0 {
		t.Fatal("uncertain owner reopened or physically reclosed")
	}
	if !errors.Is(retained.Close(), first) {
		t.Fatal("final cleanup lost first genuine failure")
	}
	if c.setupProcess().State != "needs_restart" {
		t.Fatal("uncertain peer owner permitted setup")
	}
}

func TestPeerServerPartialFailureRemainsUnconfirmedThroughLogout(t *testing.T) {
	pair := newCorePair(t)
	c := pair.a
	c.op.Lock()
	defer c.op.Unlock()
	original := c.peerServer
	if err := original.Close(); err != nil {
		t.Fatal("initial owned peer server did not close")
	}
	first := errors.Join(net.ErrClosed, errors.New("synthetic genuine close failure"))
	raw := &sequencedPeerCloseListener{called: make(chan int, 8), firstErr: first}
	retained := &peerServer{listeners: []*peerServerListener{{Listener: raw}}, startFailure: first}
	c.mu.Lock()
	c.peerServer = retained
	c.networkFatal = "network_restart_required"
	c.mu.Unlock()
	// The synthetic failing listener has no physical socket. Restore the already
	// closed real fixture owner after asserting retention, so fixture teardown
	// does not intentionally report the injected error a second time.
	defer func() { c.mu.Lock(); c.peerServer = original; c.mu.Unlock() }()
	result, err := c.logoutTailnet(context.Background())
	if err == nil || networkErrorCode(err) != "logout_cleanup_unconfirmed" || result.LocalTrafficStopped || !result.LogoutConfirmed {
		t.Fatal("logout normalized genuine retained close failure")
	}
	if c.peerServer != retained || !c.closing || raw.calls != 1 {
		t.Fatal("logout lost uncertain owner or reopened traffic")
	}
}

func (n *partialPeerStartNode) ListenPacket(string, string) (net.PacketConn, error) {
	n.packets++
	return nil, errors.New("synthetic packet listener failure")
}

func TestPeerServerPartialFailureSuspendsSameMaintenanceTick(t *testing.T) {
	address := netip.MustParseAddr("100.64.0.1")
	ports, err := ranges.Parse("12000")
	if err != nil {
		t.Fatal(err)
	}
	st := identity.State{IPs: []netip.Addr{address}}
	st.Snapshot.Running = true
	fixture := func() (*Core, *partialPeerStartNode, *activeService) {
		n := &partialPeerStartNode{
			raw:       &sequencedPeerCloseListener{called: make(chan int, 8), firstErr: errors.New("synthetic close failure")},
			listenErr: errors.New("synthetic later listen failure"),
		}
		a := &activeService{ctx: context.Background(), address: address, effective: ports,
			spec: ServiceSpec{ID: "synthetic", Direction: "share", Network: "udp", Lifetime: "until-revoked"}}
		c := &Core{ctx: context.Background(), node: n, active: map[string]*activeService{"synthetic": a}}
		c.networkReady.Store(true)
		return c, n, a
	}
	// Establish that this healthy snapshot and admitted grant really reach the
	// UDP listener path if the old same-tick recovery callbacks are invoked.
	control, controlNode, _ := fixture()
	control.revalidateServices(st)
	if controlNode.packets != 1 {
		t.Fatal("control did not reach stale-snapshot listener recovery")
	}
	c, n, a := fixture()
	c.op.Lock()
	defer c.op.Unlock()
	c.maintainPeerApplicationsLocked(st)
	if c.peerServer == nil || c.peerServer.startFailure == nil || c.networkReady.Load() || a.ready.Load() || n.listens != 2 || n.packets != 0 || n.raw.accepts != 0 {
		t.Fatal("same-tick failure did not suspend before application recovery")
	}
	// Even a previously captured healthy snapshot cannot revive the owner on
	// repeated maintenance or open a replacement peer/application listener.
	c.maintainPeerApplicationsLocked(st)
	if n.listens != 2 || n.packets != 0 || c.networkReady.Load() || a.ready.Load() {
		t.Fatal("retained failure admitted later recovery")
	}
}
