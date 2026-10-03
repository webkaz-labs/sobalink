package ranges

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestSinglePortMappingCapturesIPv6TargetAndRevokesChanges(t *testing.T) {
	targets := make(chan netip.AddrPort, 2)
	apps := make(chan net.Conn, 2)
	engine := newTestEngine(t, Options{DialLoopback: func(_ context.Context, target netip.AddrPort) (net.Conn, error) {
		targets <- target
		left, right := net.Pipe()
		apps <- right
		return left, nil
	}})
	rule := testPolicy(t, "mapped", "8080")
	rule.Loopback, rule.TargetPort = netip.IPv6Loopback(), 9000
	replaceTestPlan(t, engine, rule)
	_, done := startFlow(t, engine, testSource, endpoint(8080))
	var app net.Conn
	select {
	case app = <-apps:
	case <-time.After(3 * time.Second):
		t.Fatal("mapped target was never dialed")
	}
	defer app.Close()
	if got := <-targets; got != netip.MustParseAddrPort("[::1]:9000") {
		t.Fatalf("wrong mapped target: %v", got)
	}
	rule.TargetPort = 9001
	replaceTestPlan(t, engine, rule)
	await(t, done)
	_, changed := startFlow(t, engine, testSource, endpoint(8080))
	select {
	case app = <-apps:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement mapping was never dialed")
	}
	defer app.Close()
	if got := <-targets; got != netip.MustParseAddrPort("[::1]:9001") {
		t.Fatalf("replacement used previous target: %v", got)
	}
	engine.RevokeIDs([]string{rule.ID})
	await(t, changed)
}

func TestMappingRejectsRangeAmbiguityAndReservedTargets(t *testing.T) {
	for _, test := range []struct {
		ports  string
		target uint16
	}{
		{"8080-8081", 9000}, {"8080", DiscoveryPort}, {"8080", PeerTransferPort}, {"8080", PairingPort},
	} {
		rule := testPolicy(t, "invalid", test.ports)
		rule.TargetPort = test.target
		if _, err := BuildPlan([]netip.Addr{testSelf}, []Policy{rule}); err == nil {
			t.Fatalf("invalid mapping accepted: %+v", test)
		}
	}
	// The default remains a compact same-port range with no materialized sockets.
	engine := newTestEngine(t, Options{DialLoopback: func(context.Context, netip.AddrPort) (net.Conn, error) { return nil, errors.New("unused") }})
	replaceTestPlan(t, engine, testPolicy(t, "compact", "1-65535"))
	if stats := engine.Stats(); stats.Policies != 1 || stats.Intervals != 2 || stats.Active != 0 {
		t.Fatalf("same-port default lost compact representation: %+v", stats)
	}
}
