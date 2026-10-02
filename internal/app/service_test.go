package app

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/control"
	"github.com/webkaz-labs/tsnet-bridge/internal/identity"
	"github.com/webkaz-labs/tsnet-bridge/internal/policy"
	"io"
	"net"
	"net/netip"
	"os"
	"runtime/pprof"
	"sync"
	"testing"
	"time"
)

type fakeNode struct {
	mu        sync.Mutex
	state     identity.State
	logoutErr error
	closed    bool
}

func (n *fakeNode) Start() error { return nil }
func (n *fakeNode) State(context.Context) (identity.State, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state, nil
}
func (n *fakeNode) Login(context.Context) error  { return nil }
func (n *fakeNode) Logout(context.Context) error { return n.logoutErr }
func (n *fakeNode) Close() error                 { n.closed = true; return nil }
func (n *fakeNode) DialIP(ctx context.Context, network string, a netip.AddrPort) (net.Conn, error) {
	a1, b := net.Pipe()
	go func() { defer b.Close(); _, _ = io.Copy(b, b) }()
	return a1, nil
}
func appFixture(t *testing.T) (*Service, *fakeNode, context.CancelFunc) {
	t.Helper()
	c, e := config.New("server.example.ts.net", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	c.LocalIDPort, c.LocalRelayPort, e = allocateFixturePorts(100, bindFixturePort)
	if e != nil {
		t.Fatal(e)
	}
	if e = Preflight(c); e != nil {
		t.Fatalf("allocated fixture ports ID=%d relay=%d: %v", c.LocalIDPort, c.LocalRelayPort, e)
	}
	n := &fakeNode{state: identity.State{Backend: "Running", Snapshot: policy.Snapshot{Running: true, Peers: []policy.Peer{{ID: "peer-1", DNSName: "server.example.ts.net", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.2")}}}}}}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{Dir: t.TempDir(), Config: c, Node: n, runCtx: ctx}
	s.p = &policy.Policy{Rules: []policy.Rule{{Host: c.IDHost, Port: c.IDPort - 1, Network: "tcp"}, {Host: c.IDHost, Port: c.IDPort, Network: "tcp"}, {Host: c.RelayHost, Port: c.RelayPort, Network: "tcp"}, {Host: c.IDHost, Port: c.IDPort, Network: "udp"}}, Source: func(ctx context.Context) (policy.Snapshot, error) { st, e := n.State(ctx); return st.Snapshot, e }, DialIP: n.DialIP}
	t.Cleanup(func() { cancel(); s.closeForwards() })
	return s, n, cancel
}
func TestDoctorRequestDoesNotOwnListeners(t *testing.T) {
	s, _, _ := appFixture(t)
	request, cancel := context.WithCancel(context.Background())
	_, e, _ := s.execute(request, "doctor")
	if e != nil || s.Status().State != "ready" {
		t.Fatal(s.Status(), e)
	}
	cancel()
	for _, f := range s.forwards {
		select {
		case <-f.Done():
			t.Fatal("listener inherited short request context")
		default:
		}
	}
	// Native socket liveness uses a bounded diagnostic watchdog, not a latency
	// assertion. The property here is that request cancellation keeps the listener.
	ctx, stop := context.WithTimeout(t.Context(), 15*time.Second)
	defer stop()
	var dialer net.Dialer
	c, e := dialer.DialContext(ctx, "tcp", config.Loopback(s.Config.LocalIDPort))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	c.SetDeadline(deadline)
	if _, e = c.Write([]byte("echo")); e != nil {
		t.Fatal(e)
	}
	b := make([]byte, 4)
	if _, e = io.ReadFull(c, b); e != nil || string(b) != "echo" {
		t.Fatal(string(b), e)
	}
}
func TestRevokedNodeClosesListeners(t *testing.T) {
	s, n, _ := appFixture(t)
	if !s.check(context.Background()) {
		t.Fatal(s.Status())
	}
	n.mu.Lock()
	n.state.Snapshot.Peers = nil
	n.mu.Unlock()
	if s.check(context.Background()) {
		t.Fatal("revoked peer remained ready")
	}
	if len(s.forwards) != 0 {
		t.Fatal("listeners remain")
	}
	if s.Status().State != "blocked" {
		t.Fatal(s.Status())
	}
}
func TestUnexpectedListenerStopRestarts(t *testing.T) {
	s, _, _ := appFixture(t)
	if !s.check(context.Background()) {
		t.Fatal(s.Status())
	}
	old := s.forwards[0]
	old.Close()
	if !s.check(context.Background()) {
		t.Fatal(s.Status())
	}
	if s.forwards[0] == old {
		t.Fatal("stopped listener reported ready")
	}
}
func TestPartialStartRollsBack(t *testing.T) {
	s, _, _ := appFixture(t)
	conflict, e := net.Listen("tcp4", config.Loopback(s.Config.LocalRelayPort))
	if e != nil {
		t.Fatal(e)
	}
	defer conflict.Close()
	if s.check(context.Background()) {
		t.Fatal("accepted conflict")
	}
	if len(s.forwards) > 0 {
		t.Fatal("partially started listeners remain")
	}
	l, e := net.Listen("tcp4", config.Loopback(s.Config.LocalIDPort))
	if e != nil {
		t.Fatal("rollback failed", e)
	}
	l.Close()
}
func TestLogoutFailureStillClosesForwarding(t *testing.T) {
	s, n, _ := appFixture(t)
	if !s.check(context.Background()) {
		t.Fatal(s.Status())
	}
	n.logoutErr = errors.New("network unavailable")
	_, e, exit := s.execute(context.Background(), "logout")
	if e == nil || !exit || len(s.forwards) != 0 || s.Status().State != "stopped" {
		t.Fatal(e, exit, s.Status())
	}
}
func TestReadinessHasNoAuthSecret(t *testing.T) {
	s, n, _ := appFixture(t)
	n.mu.Lock()
	n.state = identity.State{Backend: "NeedsLogin", AuthURL: "https://login.tailscale.com/a/private-test"}
	n.mu.Unlock()
	s.check(context.Background())
	st := s.Status()
	if st.State != "needs-login" || len(st.Listeners) > 0 || st.RustDesk != "unverified" {
		t.Fatal(st)
	}
}

// This test uses real user-private IPC but a fake backend, never a tailnet.
func TestServiceIPCStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	s, _, _ := appFixture(t)
	s.runCtx = ctx
	d, e := os.MkdirTemp("", "tb-app-")
	if e != nil {
		t.Fatal(e)
	}
	s.Dir = d
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() { defer close(exited); done <- s.Run(s.runCtx) }()
	defer func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
			t.Error("native service cleanup did not finish")
		}
		os.RemoveAll(d)
	}()
	// This native watchdog is a hang diagnostic, not a shutdown latency claim.
	for {
		var st Status
		e = control.Call(ctx, d, "status", &st)
		if e == nil {
			break
		}
		select {
		case e := <-done:
			t.Fatalf("service startup: %v", e)
		default:
		}
		select {
		case <-ctx.Done():
			pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
			t.Fatal("IPC not ready", e)
		case <-time.After(10 * time.Millisecond):
		}
	}
	var st Status
	if e = control.Call(ctx, d, "stop", &st); e != nil {
		t.Fatal("stop response lost", e)
	}
	if st.State != "stopped" {
		t.Fatal(st)
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-ctx.Done():
		pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
		t.Fatal("service did not stop before native test watchdog")
	}
}
