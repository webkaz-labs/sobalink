package ranges

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mustPorts(t *testing.T, expression string) Set {
	t.Helper()
	ports, err := Parse(expression)
	if err != nil {
		t.Fatal(err)
	}
	return ports
}

func TestParseNormalizesCompactIntervals(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		count       uint32
	}{
		{"1", "1", 1}, {"65535", "65535", 1}, {"443,80,22,80", "22,80,443", 3},
		{" 9-12, 1-4, 5-8 , 10,65535 ", "1-12,65535", 13},
		{"1-65535", "1-65535", 65535},
	} {
		t.Run(tc.input, func(t *testing.T) {
			s := mustPorts(t, tc.input)
			if s.String() != tc.want || s.Count() != tc.count {
				t.Fatalf("got %s (%d), want %s (%d)", s.String(), s.Count(), tc.want, tc.count)
			}
		})
	}
	for _, invalid := range []string{"", "0", "65536", "-1", "+1", "1-0", "2-1", "1-2-3", "1,,2", "1.0", "0x22", "1 2", strings.Repeat("1,", MaxIntervals) + "1"} {
		if _, err := Parse(invalid); err == nil {
			t.Errorf("accepted %q", invalid)
		}
	}
	if _, err := NewSet([]Interval{{0, 1}}); err == nil {
		t.Fatal("accepted port zero")
	}
}

func TestSetCopiesAndBoundarySubtraction(t *testing.T) {
	input := []Interval{{1, 65535}}
	s, err := NewSet(input)
	if err != nil {
		t.Fatal(err)
	}
	input[0] = Interval{5, 5}
	returned := s.Intervals()
	returned[0] = Interval{9, 9}
	if s.String() != "1-65535" {
		t.Fatal("set exposes mutable interval backing storage")
	}
	for _, tc := range []struct {
		excludes, want string
		count          uint32
	}{
		{"1,65535", "2-65534", 65533},
		{"1-65535", "", 0},
		{"54543-54544", "1-54542,54545-65535", 65533},
		{"2-65534", "1,65535", 2},
	} {
		out, err := s.Excluding(mustPorts(t, tc.excludes))
		if err != nil {
			t.Fatal(err)
		}
		if out.String() != tc.want || out.Count() != tc.count {
			t.Fatalf("excluding %s: %s (%d)", tc.excludes, out.String(), out.Count())
		}
	}
	if !s.Contains(1) || !s.Contains(65535) || s.Contains(0) {
		t.Fatal("incorrect inclusive boundaries")
	}
	if !mustPorts(t, "1-5").Overlaps(mustPorts(t, "5-9")) || mustPorts(t, "1-5").Overlaps(mustPorts(t, "6-9")) {
		t.Fatal("incorrect overlap boundaries")
	}
}

func TestMaterializedExpansionIsExplicitAndPreflighted(t *testing.T) {
	full := mustPorts(t, "1-65535")
	for _, limit := range []int{0, 1, 64, 65, 65535} {
		if out, err := full.Expand(limit); err == nil || out != nil {
			t.Fatalf("full range expanded with limit %d", limit)
		}
	}
	ports, err := mustPorts(t, "1,65534-65535").Expand(3)
	if err != nil || !reflect.DeepEqual(ports, []uint16{1, 65534, 65535}) {
		t.Fatalf("boundary expansion %v, %v", ports, err)
	}
}

var testSelf = netip.MustParseAddr("100.64.0.1")
var testSelf6 = netip.MustParseAddr("fd7a:115c:a1e0::1")
var testSource = netip.MustParseAddrPort("100.64.0.2:12345")

func testPolicy(t *testing.T, id, ports string) Policy {
	t.Helper()
	return Policy{ID: id, Network: "tcp", Address: testSelf, Ports: mustPorts(t, ports), Loopback: netip.MustParseAddr("127.0.0.1"), PeerIDs: []string{"peer-a"}, ExpiresAt: time.Now().Add(time.Hour)}
}

func TestFullRangePlanIsOnePolicyAndTwoIntervals(t *testing.T) {
	rule := testPolicy(t, "all", "1-65535")
	plan, err := BuildPlan([]netip.Addr{testSelf}, []Policy{rule})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PolicyCount() != 1 || plan.IntervalCount() != 2 {
		t.Fatalf("full range was not compact: %+v", plan)
	}
	d := plan.Describe()[0]
	if d.EffectivePorts != 65532 || d.Effective != "1-54542,54546-65535" {
		t.Fatalf("reserved exclusions missing: %+v", d)
	}
	rule.PeerIDs[0] = "changed"
	if plan.policies[0].policy.PeerIDs[0] != "peer-a" {
		t.Fatal("plan retained caller's mutable peer list")
	}
	if _, err := plan.ExpandMaterialized([]string{"all"}, 64); err == nil {
		t.Fatal("materialized an unrestricted range")
	}
}

func TestPlanScopeOverlapAndAggregateBounds(t *testing.T) {
	a := testPolicy(t, "a", "1-100")
	b := testPolicy(t, "b", "100-200")
	if _, err := BuildPlan([]netip.Addr{testSelf}, []Policy{a, b}); err == nil {
		t.Fatal("accepted overlapping policies")
	}
	b.Exclude = mustPorts(t, "100")
	if _, err := BuildPlan([]netip.Addr{testSelf}, []Policy{a, b}); err != nil {
		t.Fatal(err)
	}
	b.Exclude = Set{}
	b.Network = "udp"
	if _, err := BuildPlan([]netip.Addr{testSelf}, []Policy{a, b}); err != nil {
		t.Fatal("different protocol overlaps should be independent:", err)
	}
	b.Network = "tcp"
	b.Address = testSelf6
	if _, err := BuildPlan([]netip.Addr{testSelf, testSelf6}, []Policy{a, b}); err != nil {
		t.Fatal("different address overlaps should be independent:", err)
	}
	for _, change := range []func(*Policy){
		func(p *Policy) { p.Address = netip.MustParseAddr("100.64.0.99") },
		func(p *Policy) { p.Address = netip.MustParseAddr("127.0.0.1") },
		func(p *Policy) { p.Loopback = netip.MustParseAddr("127.0.0.2") },
		func(p *Policy) { p.Loopback = netip.MustParseAddr("192.168.1.1") },
		func(p *Policy) { p.PeerIDs = nil }, func(p *Policy) { p.ExpiresAt = time.Time{} },
		func(p *Policy) {
			p.PeerIDs = make([]string, MaxPolicyPeers+1)
			for i := range p.PeerIDs {
				p.PeerIDs[i] = strings.Repeat("p", i+1)
			}
		},
		func(p *Policy) { p.Ports = mustPorts(t, "54543-54544") },
	} {
		invalid := a
		change(&invalid)
		if _, err := BuildPlan([]netip.Addr{testSelf}, []Policy{invalid}); err == nil {
			t.Fatalf("accepted invalid scope: %+v", invalid)
		}
	}
	policies := make([]Policy, 65)
	for i := range policies {
		policies[i] = a
		policies[i].ID = strings.Repeat("x", i+1)
		policies[i].Ports, _ = NewSet([]Interval{{uint16(i + 1), uint16(i + 1)}})
	}
	if _, err := BuildPlan([]netip.Addr{testSelf}, policies[:64]); err != nil {
		t.Fatal("64 policies rejected:", err)
	}
	if _, err := BuildPlan([]netip.Addr{testSelf}, policies); err == nil {
		t.Fatal("65 policies accepted")
	}
	var many []Interval
	for i := 0; i < MaxIntervals; i++ {
		many = append(many, Interval{uint16(2*i + 1), uint16(2*i + 1)})
	}
	a.Ports, _ = NewSet(many)
	b = a
	b.ID = "b"
	b.Ports = mustPorts(t, "1000")
	if _, err := BuildPlan([]netip.Addr{testSelf}, []Policy{a}); err != nil {
		t.Fatal("256 intervals rejected:", err)
	}
	if _, err := BuildPlan([]netip.Addr{testSelf}, []Policy{a, b}); err == nil {
		t.Fatal("257 intervals accepted")
	}
}

func TestMaterializedPlanCountsAcrossPolicies(t *testing.T) {
	a, b := testPolicy(t, "a", "1-32"), testPolicy(t, "b", "100-131")
	plan, err := BuildPlan([]netip.Addr{testSelf}, []Policy{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.ExpandMaterialized([]string{"a", "b"}, 63); err == nil {
		t.Fatal("ignored aggregate limit")
	}
	out, err := plan.ExpandMaterialized([]string{"a", "b"}, 64)
	if err != nil || len(out["a"])+len(out["b"]) != 64 {
		t.Fatalf("expected 64 explicit listeners: %v", err)
	}
	if _, err := plan.ExpandMaterialized([]string{"unknown"}, 64); err == nil {
		t.Fatal("accepted unknown policy")
	}
}
