package ranges

import (
	"net/netip"
	"testing"
	"time"
)

func TestRangeAdmissionRejectsExactAndMissingExpiry(t *testing.T) {
	until := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	address := netip.MustParseAddr("100.64.0.1")
	ports, err := Parse("8080")
	if err != nil {
		t.Fatal(err)
	}
	p := &permit{rule: compiledPolicy{policy: Policy{Network: "tcp", Address: address, ExpiresAt: until}, effective: ports}}
	s := &snapshot{selfIPs: map[netip.Addr]struct{}{address: {}}}
	source := netip.MustParseAddrPort("100.64.0.2:43210")
	target := netip.AddrPortFrom(address, 8080)
	if !validFlowAt(until.Add(-time.Nanosecond), s, p, source, target) {
		t.Fatal("valid range permission denied")
	}
	if validFlowAt(until, s, p, source, target) {
		t.Fatal("expired range still admitted a new flow")
	}
	p.rule.policy.ExpiresAt = time.Time{}
	if validFlowAt(until, s, p, source, target) {
		t.Fatal("missing expiry admitted an unbounded flow")
	}
}
