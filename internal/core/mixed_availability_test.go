package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/backendworker"
	"github.com/webkaz-labs/sobalink/internal/connectionroute"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/identity"
)

// Fault injection belongs only to the explicitly exercised dial, not ordinary
// Core maintenance probes that continue concurrently throughout these tests.
type availabilityDialKey struct{}

func availabilityDialContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, availabilityDialKey{}, true)
}

type availabilityBackend struct {
	NetworkBackend
	startErr, dialErr     error
	starts, dials, closes int
	beforeDial            func()
}

func (b *availabilityBackend) Start() error { b.starts++; return b.startErr }
func (b *availabilityBackend) Close() error {
	b.closes++
	if b.NetworkBackend != nil {
		return b.NetworkBackend.Close()
	}
	return nil
}
func (b *availabilityBackend) DialIP(ctx context.Context, network string, ap netip.AddrPort) (net.Conn, error) {
	if ctx.Value(availabilityDialKey{}) != true {
		return b.NetworkBackend.DialIP(ctx, network, ap)
	}
	b.dials++
	if b.beforeDial != nil {
		b.beforeDial()
	}
	if b.dialErr != nil {
		return nil, b.dialErr
	}
	return b.NetworkBackend.DialIP(ctx, network, ap)
}
func boundAvailabilityPair(t *testing.T) (*mixedBackend, string, *availabilityBackend, *availabilityBackend) {
	t.Helper()
	wrappers := map[string]*availabilityBackend{}
	a, _, n, _ := mixedCorePair(t, func(name string, node *pipeNode) NetworkBackend {
		wrapped := &availabilityBackend{NetworkBackend: node}
		wrappers[name] = wrapped
		return wrapped
	})
	raw, _ := json.Marshal(map[string]any{"peers": []string{mixedID("lan", "peer-b-lan"), mixedID("tailnet", "peer-b-tailnet")}})
	result, e := a.bindMixedPeers(context.Background(), raw)
	if e != nil {
		t.Fatal(e)
	}
	id := result.(map[string]any)["binding"].(connectionroute.Binding).PeerID
	return n, id, wrappers["lan"], wrappers["tailnet"]
}
func TestMixedAvailabilityClassifierRejectsAmbiguousErrors(t *testing.T) {
	for _, e := range []error{net.ErrClosed, backendworker.ErrClosed, connectionroute.ErrUnavailable, directlan.ErrLocalAddressUnavailable, fmt.Errorf("wrapped: %w", net.ErrClosed)} {
		if !mixedUnavailable(e) {
			t.Fatal(e)
		}
	}
	for _, e := range []error{nil, context.Canceled, context.DeadlineExceeded, directlan.ErrLocalAddressUnknown, directlan.ErrUnavailable, directlan.ErrRecovery, directlan.ErrUntrusted, connectionroute.ErrDenied, errors.New("closed"), errors.Join(net.ErrClosed, connectionroute.ErrDenied), errors.Join(net.ErrClosed, context.Canceled)} {
		if mixedUnavailable(e) {
			t.Fatalf("unsafe availability: %v", e)
		}
	}
}
func TestMixedStartupOnlyDegradesForConfirmedAbsence(t *testing.T) {
	for _, e := range []error{directlan.ErrLocalAddressUnavailable, net.ErrClosed, context.Canceled, directlan.ErrLocalAddressUnknown, directlan.ErrRecovery, errors.New("bad saved configuration")} {
		t.Run(e.Error(), func(t *testing.T) {
			first, second := &availabilityBackend{startErr: e}, &availabilityBackend{}
			n := &mixedBackend{ctx: context.Background(), order: []string{"direct-lan", "tailnet"}, nodes: map[string]NetworkBackend{"direct-lan": first, "tailnet": second}}
			got := n.Start()
			if mixedStartupUnavailable(e) {
				if got != nil || second.starts != 1 || second.closes != 0 {
					t.Fatalf("healthy backend was lost: %v", got)
				}
			} else if got == nil || second.starts != 0 || first.closes != 1 {
				t.Fatalf("unsafe startup: %v", got)
			}
		})
	}
}
func TestMixedUnknownReadinessNeverSelectsAnotherRoute(t *testing.T) {
	for _, state := range []string{"Starting", "NoState", "Stopped", "mystery", "", "NeedsLogin", "NeedsMachineAuth"} {
		t.Run(state, func(t *testing.T) {
			n, id, first, second := boundAvailabilityPair(t)
			node := first.NetworkBackend.(*pipeNode)
			node.mu.Lock()
			node.state.Backend = state
			node.state.Snapshot.Running = false
			node.mu.Unlock()
			conn, e := n.DialIP(availabilityDialContext(context.Background()), "tcp", netip.AddrPortFrom(mixedIP(id), PeerPort))
			if conn != nil {
				conn.Close()
			}
			if e == nil || first.dials != 0 || second.dials != 0 {
				t.Fatalf("unconfirmed state selected a route: %v calls %d/%d", e, first.dials, second.dials)
			}
			if state == "NeedsLogin" || state == "NeedsMachineAuth" {
				if !errors.Is(e, connectionroute.ErrDenied) {
					t.Fatal(e)
				}
			} else if !errors.Is(e, errMixedReadinessUnknown) {
				t.Fatal(e)
			}
		})
	}
}
func TestMixedDialAvailabilityRaceRechecksBeforeFallback(t *testing.T) {
	for _, failure := range []error{net.ErrClosed, backendworker.ErrClosed, directlan.ErrLocalAddressUnavailable, connectionroute.ErrDenied, directlan.ErrUntrusted, context.Canceled, context.DeadlineExceeded, errors.New("connection refused")} {
		t.Run(failure.Error(), func(t *testing.T) {
			n, id, first, second := boundAvailabilityPair(t)
			first.dialErr = failure
			conn, e := n.DialIP(availabilityDialContext(context.Background()), "tcp", netip.AddrPortFrom(mixedIP(id), PeerPort))
			if mixedUnavailable(failure) {
				if e != nil || conn == nil || second.dials != 1 {
					t.Fatalf("positive absence did not fall back: %v", e)
				}
				conn.Close()
			} else if e == nil || second.dials != 0 {
				if conn != nil {
					conn.Close()
				}
				t.Fatalf("terminal failure fell back: %v", e)
			}
		})
	}
}
func TestMixedDialRaceCannotBypassNewRevocationOrCancellation(t *testing.T) {
	for _, cancelCall := range []bool{false, true} {
		n, id, first, second := boundAvailabilityPair(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		first.dialErr = net.ErrClosed
		first.beforeDial = func() {
			if cancelCall {
				cancel()
				return
			}
			node := second.NetworkBackend.(*pipeNode)
			node.mu.Lock()
			node.state.Snapshot.Peers[0].Expired = true
			node.mu.Unlock()
		}
		conn, e := n.DialIP(availabilityDialContext(ctx), "tcp", netip.AddrPortFrom(mixedIP(id), PeerPort))
		if conn != nil {
			conn.Close()
		}
		if e == nil || second.dials != 0 {
			t.Fatalf("race escaped retirement/cancellation: %v", e)
		}
	}
}
func TestMixedReadinessStatusDistinguishesAbsence(t *testing.T) {
	s := identity.State{Backend: "unavailable"}
	if mixedReadiness(s) != mixedAbsent {
		t.Fatal(s)
	}
	s.Backend = "Starting"
	if mixedReadiness(s) != mixedUnknown {
		t.Fatal(s)
	}
	s.Backend = "NeedsLogin"
	s.Snapshot.Running = true
	if mixedReadiness(s) != mixedAuthRequired {
		t.Fatal(s)
	}
}

func TestMixedDeadWorkerDoesNotEraseKnownAuthorizationFailure(t *testing.T) {
	n, id, first, second := boundAvailabilityPair(t)
	node := first.NetworkBackend.(*pipeNode)
	node.mu.Lock()
	node.state.Backend = "NeedsLogin"
	node.state.Snapshot.Running = false
	node.mu.Unlock()
	if _, _, e := n.routeSnapshot(context.Background()); e != nil {
		t.Fatal(e)
	}
	node.Close()
	conn, e := n.DialIP(availabilityDialContext(context.Background()), "tcp", netip.AddrPortFrom(mixedIP(id), PeerPort))
	if conn != nil {
		conn.Close()
	}
	if !errors.Is(e, connectionroute.ErrDenied) || second.dials != 0 {
		t.Fatalf("dead worker erased cached authorization: %v", e)
	}
}

func TestMixedCanceledStartupDoesNotStartAnotherBackend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	first, second := &availabilityBackend{}, &availabilityBackend{}
	n := &mixedBackend{ctx: ctx, order: []string{"direct-lan", "tailnet"}, nodes: map[string]NetworkBackend{"direct-lan": first, "tailnet": second}}
	if e := n.Start(); !errors.Is(e, context.Canceled) || first.starts != 0 || second.starts != 0 {
		t.Fatalf("canceled startup contacted a backend: %v", e)
	}
}

func TestMixedIncomingAdmissionBlocksUnknownWithoutRetiringExistingTCP(t *testing.T) {
	n, id, first, _ := boundAvailabilityPair(t)
	makeIncoming := func() net.Conn {
		a, b := net.Pipe()
		t.Cleanup(func() { a.Close(); b.Close() })
		return &pipeConn{Conn: a, local: netip.MustParseAddrPort("100.64.0.1:41001"), remote: netip.MustParseAddrPort("100.64.0.2:41000"), hub: &pipeNetwork{}}
	}
	admitted, e := n.wrap("tailnet", makeIncoming())
	if e != nil {
		t.Fatal(e)
	}
	defer admitted.Close()
	alias, e := netip.ParseAddrPort(admitted.RemoteAddr().String())
	if e != nil {
		t.Fatal(e)
	}
	node := first.NetworkBackend.(*pipeNode)
	node.mu.Lock()
	node.state.Backend = "Starting"
	node.state.Snapshot.Running = false
	node.mu.Unlock()
	if c, e := n.wrap("tailnet", makeIncoming()); !errors.Is(e, errMixedReadinessUnknown) {
		if c != nil {
			c.Close()
		}
		t.Fatalf("unconfirmed binding admitted incoming connection: %v", e)
	}
	if got, e := n.WhoIs(context.Background(), alias); e != nil || got != id {
		t.Fatalf("companion startup retired existing exact-source identity: %s %v", got, e)
	}
	node.mu.Lock()
	node.state.Backend = "unavailable"
	node.mu.Unlock()
	c, e := n.wrap("tailnet", makeIncoming())
	if e != nil {
		t.Fatalf("positive absence blocked approved source: %v", e)
	}
	c.Close()
}

func TestMixedPacketDoesNotSkipUnknownReadiness(t *testing.T) {
	n, nodes := newMixedPacketFixture(t)
	nodes["lan"].mu.Lock()
	nodes["lan"].state.Backend = "NoState"
	nodes["lan"].state.Snapshot.Running = false
	nodes["lan"].mu.Unlock()
	p, e := n.ListenPacket("udp", netip.AddrPortFrom(n.self, 7000).String())
	if p != nil {
		p.Close()
	}
	if !errors.Is(e, errMixedReadinessUnknown) {
		t.Fatalf("unknown UDP backend skipped: %v", e)
	}
	n.Close()
}

func TestMixedRetryStopsAfterBindingRemoved(t *testing.T) {
	n, id, first, second := boundAvailabilityPair(t)
	first.dialErr = net.ErrClosed
	first.beforeDial = func() { n.mu.Lock(); n.bindings = nil; n.mu.Unlock() }
	conn, e := n.DialIP(availabilityDialContext(context.Background()), "tcp", netip.AddrPortFrom(mixedIP(id), PeerPort))
	if conn != nil {
		conn.Close()
	}
	if e == nil || second.dials != 0 {
		t.Fatalf("retry escaped removed binding: %v", e)
	}
}

func TestMixedDirectStartupStatusIsNotAbsenceWithoutObservation(t *testing.T) {
	c := openLANTestCore(t)
	store := configureDirectLANTest(t, c)
	node, e := c.newDirectLANBackend(store)
	if e != nil {
		t.Fatal(e)
	}
	defer node.Close()
	direct := node.(*directLANBackend)
	state, e := direct.State(context.Background())
	if e != nil || mixedReadiness(state) != mixedUnknown || direct.restartRequired() {
		t.Fatalf("unstarted direct backend claimed absence: %v %v", state, e)
	}
	direct.mu.Lock()
	direct.startAbsent = true
	direct.mu.Unlock()
	state, e = direct.State(context.Background())
	if e != nil || mixedReadiness(state) != mixedAbsent || !direct.restartRequired() {
		t.Fatalf("confirmed failed startup lost restart status: %v %v", state, e)
	}
	_, e = direct.DialIP(context.Background(), "tcp", netip.MustParseAddrPort("[fd00::1]:7000"))
	if e != directlan.ErrLocalAddressUnavailable {
		t.Fatalf("absence race lost exact classification: %v", e)
	}
}
