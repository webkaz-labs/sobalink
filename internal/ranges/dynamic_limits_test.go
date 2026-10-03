package ranges

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/transport"
)

func TestRangeLiveLimitsPreserveExistingFlows(t *testing.T) {
	var limits atomic.Pointer[Limits]
	initial := Limits{Global: 2, PerPolicy: 2, PerPeer: 2}
	limits.Store(&initial)
	var dials atomic.Int32
	e := newTestEngine(t, Options{CurrentLimits: func() Limits { return *limits.Load() }, DialLoopback: func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
		dials.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	replaceTestPlan(t, e, testPolicy(t, "policy", "8080"))
	_, first := startFlow(t, e, testSource, endpoint(8080))
	eventually(t, func() bool { return dials.Load() == 1 })
	lowered := Limits{Global: 1, PerPolicy: 1, PerPeer: 1}
	limits.Store(&lowered)
	_, denied := startFlow(t, e, testSource, endpoint(8080))
	await(t, denied)
	if err := e.Revalidate(t.Context()); err != nil {
		t.Fatal("resource reduction changed authorization", err)
	}
	select {
	case <-first:
		t.Fatal("resource reduction canceled existing flow")
	default:
	}
	raised := Limits{Global: 1000, PerPolicy: 1000, PerPeer: 1000}
	limits.Store(&raised)
	_, second := startFlow(t, e, testSource, endpoint(8080))
	eventually(t, func() bool { return dials.Load() == 2 })
	e.Close()
	await(t, first)
	await(t, second)
	if e.Stats().Active != 0 {
		t.Fatal("active accounting leaked")
	}
}

func TestRangeSharedControllerIncludesOtherTransports(t *testing.T) {
	controller := transport.NewController(func() transport.Limits { return transport.Limits{TCPConnections: 3, TCPPerPolicy: 2, TCPPerPeer: 1} })
	external, ok := controller.AdmitTCP("forward", "peer-a")
	if !ok {
		t.Fatal("forward reservation unavailable")
	}
	defer external()
	var dials atomic.Int32
	e := newTestEngine(t, Options{
		AdmitPolicyTCP: func(id string) (func(), bool) { return controller.AdmitTCP(id, "") },
		AdmitPeerTCP:   controller.AdmitTCPPeer,
		DialLoopback: func(context.Context, netip.AddrPort) (net.Conn, error) {
			dials.Add(1)
			return nil, errors.New("refused")
		},
	})
	replaceTestPlan(t, e, testPolicy(t, "share", "8080"))
	_, denied := startFlow(t, e, testSource, endpoint(8080))
	await(t, denied)
	if dials.Load() != 0 || controller.Usage().TCPConnections != 1 {
		t.Fatal("authenticated peer gate bypassed or admission leaked")
	}
	external()
	for range 3 {
		_, done := startFlow(t, e, testSource, endpoint(8080))
		await(t, done)
		if controller.Usage() != (transport.Usage{}) {
			t.Fatal("failure retained aggregate, policy or peer reservation")
		}
	}
	if dials.Load() != 3 {
		t.Fatal("released peer capacity was not reusable")
	}
}

func TestRangeConfiguredLimitsAreDefaultsNotMaximums(t *testing.T) {
	e := newTestEngine(t, Options{Limits: Limits{Global: 1000, PerPolicy: 1000, PerPeer: 1000}})
	if e.Stats().Active != 0 || len(e.byPolicy) != 0 || len(e.byPeer) != 0 {
		t.Fatal("limits eagerly materialized state")
	}
}
