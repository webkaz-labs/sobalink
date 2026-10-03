package ranges

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

func TestRaisedLogicalLimitsRemainCompact(t *testing.T) {
	ports, err := ParseWithLimit("1-65535", 4096)
	if err != nil {
		t.Fatal(err)
	}
	rule := testPolicy(t, "all", "8080")
	rule.Ports = ports
	rule.ExpiresAt = time.Time{}
	rule.UntilRevoked = true
	plan, err := BuildPlanWithLimits([]netip.Addr{testSelf}, []Policy{rule}, PlanLimits{Policies: 1024, Intervals: 4096, Peers: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PolicyCount() != 1 || plan.IntervalCount() != 2 || plan.Describe()[0].EffectivePorts != 65532 {
		t.Fatal("broad range materialized or expanded incorrectly")
	}
	list, err := mustPorts(t, "8000-8064").ExpandWithLimit(65)
	if err != nil || len(list) != 65 {
		t.Fatal("explicitly raised listener allowance rejected", err)
	}
	if _, err := ports.ExpandWithLimit(64); err == nil {
		t.Fatal("listener allowance bypassed")
	}
}

func TestUntilRevokedRangeRejectsAmbiguousDeadlineAndStops(t *testing.T) {
	rule := testPolicy(t, "permanent", "8080")
	rule.UntilRevoked = true
	if _, err := BuildPlan([]netip.Addr{testSelf}, []Policy{rule}); err == nil {
		t.Fatal("permanent mode with deadline accepted")
	}
	rule.ExpiresAt = time.Time{}
	e, err := New(context.Background(), Options{Authorize: func(context.Context, Request) (string, error) { return "peer-a", nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err := e.Replace([]netip.Addr{testSelf}, []Policy{rule}); err != nil {
		t.Fatal(err)
	}
	if handler, _ := e.Handle(testSource, netip.AddrPortFrom(testSelf, 8080)); handler == nil {
		t.Fatal("explicit permanent rule unavailable")
	}
	e.RevokeIDs([]string{rule.ID})
	if handler, _ := e.Handle(testSource, netip.AddrPortFrom(testSelf, 8080)); handler != nil {
		t.Fatal("revoked permanent rule admitted flow")
	}
}
