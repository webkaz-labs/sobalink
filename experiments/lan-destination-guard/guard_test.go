package guard

import (
	"errors"
	"net/netip"
	"sync"
	"testing"
)

type fake struct {
	mu    sync.Mutex
	calls []netip.AddrPort
	err   error
}

func (f *fake) record(ap netip.AddrPort) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ap)
	return f.err
}
func (f *fake) Write(_ []byte, ap netip.AddrPort) error   { return f.record(ap) }
func (f *fake) Batch(_ [][]byte, ap netip.AddrPort) error { return f.record(ap) }
func (f *fake) Connect(ap netip.AddrPort) error           { return f.record(ap) }
func ap(s string) netip.AddrPort                          { return netip.MustParseAddrPort(s) }
func policy() Policy {
	return Policy{[]netip.Prefix{netip.MustParsePrefix("192.168.40.0/24"), netip.MustParsePrefix("fd00:40::/64")}, ap("192.168.40.2:4443")}
}
func allow(netip.AddrPort) bool { return true }
func TestDestinationsAtBothWriteBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, dest string
		want       bool
	}{
		{"selected-v4", "192.168.40.3:3000", true}, {"selected-ula", "[fd00:40::3]:3000", true},
		{"global-v4", "8.8.8.8:443", false}, {"other-private", "10.20.0.3:3000", false}, {"adjacent-private", "192.168.41.3:3000", false},
		{"other-ula", "[fd00:41::3]:3000", false}, {"global-v6", "[2001:db8::3]:3000", false}, {"linklocal-zoned", "[fe80::3%eth0]:3000", false},
		{"mapped-v4", "[::ffff:192.168.40.3]:3000", false}, {"zero-port", "192.168.40.3:0", false}, {"multicast", "224.0.0.1:3000", false},
		{"unspecified", "0.0.0.0:3000", false}, {"loopback", "127.0.0.1:3000", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, batch := range []bool{false, true} {
				f := &fake{}
				g := New(policy(), f, allow)
				var e error
				if batch {
					e = g.Batch([][]byte{{1}, {2}}, ap(tc.dest))
				} else {
					e = g.Write([]byte{1}, ap(tc.dest))
				}
				if (e == nil) != tc.want || (len(f.calls) == 1) != tc.want {
					t.Fatalf("batch=%v error=%v calls=%d", batch, e, len(f.calls))
				}
			}
		})
	}
}
func TestInvalidPolicyAndZeroDestinationFailClosed(t *testing.T) {
	for _, p := range []Policy{{}, {Relay: policy().Relay}, {[]netip.Prefix{{}}, policy().Relay}, {[]netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}, policy().Relay}, {[]netip.Prefix{netip.MustParsePrefix("192.168.40.1/24")}, policy().Relay}, {[]netip.Prefix{netip.MustParsePrefix("192.168.41.0/24")}, policy().Relay}} {
		f := &fake{}
		g := New(p, f, allow)
		if g.Write(nil, policy().Relay) != ErrDenied || g.Batch([][]byte{{1}}, policy().Relay) != ErrDenied || g.Connect(policy().Relay) != ErrDenied || len(f.calls) != 0 {
			t.Fatal("not fail closed")
		}
	}
	g := New(policy(), &fake{}, allow)
	if g.Write(nil, netip.AddrPort{}) != ErrDenied {
		t.Fatal("zero destination allowed")
	}
}
func TestEndpointUpdateCannotReuseAdmission(t *testing.T) {
	f := &fake{}
	g := New(policy(), f, allow)
	if g.Write(nil, ap("192.168.40.3:3000")) != nil {
		t.Fatal("allowed")
	}
	if g.Write(nil, ap("8.8.8.8:3000")) != ErrDenied || len(f.calls) != 1 {
		t.Fatal("cached admission")
	}
}
func TestPolicyReplacementAndCallerMutation(t *testing.T) {
	f := &fake{}
	p := policy()
	g := New(p, f, allow)
	p.Prefixes[0] = netip.MustParsePrefix("10.0.0.0/8")
	if g.Write(nil, ap("10.0.0.3:3000")) != ErrDenied {
		t.Fatal("mutable policy")
	}
	g.Replace(Policy{})
	if g.Write(nil, policy().Relay) != ErrDenied || len(f.calls) != 0 {
		t.Fatal("old policy survived invalid update")
	}
}
func TestVPNAndUncertainRouteDenied(t *testing.T) {
	for _, proof := range []RouteProof{nil, func(netip.AddrPort) bool { return false }} {
		f := &fake{}
		g := New(policy(), f, proof)
		if g.Write(nil, policy().Relay) != ErrDenied || g.Batch([][]byte{{1}}, policy().Relay) != ErrDenied || g.Connect(policy().Relay) != ErrDenied || len(f.calls) != 0 {
			t.Fatal("route uncertainty accepted")
		}
	}
}
func TestCIDRAloneCannotDistinguishVPN(t *testing.T) {
	if !policy().permits(ap("192.168.40.3:3000")) {
		t.Fatal("same address routed through VPN still satisfies CIDR; this is an explicit limitation")
	}
}
func TestEmptyBatchAndBackendErrors(t *testing.T) {
	f := &fake{err: errors.New("fake write failed")}
	g := New(policy(), f, allow)
	if g.Batch(nil, policy().Relay) != nil || len(f.calls) != 0 {
		t.Fatal("empty batch wrote")
	}
	if g.Batch(nil, ap("8.8.8.8:443")) != ErrDenied {
		t.Fatal("empty batch bypassed policy")
	}
	if g.Write(nil, policy().Relay) != f.err || g.Batch([][]byte{{1}}, policy().Relay) != f.err {
		t.Fatal("lost backend error")
	}
}
func TestTCPOnlyExactSelectedRelay(t *testing.T) {
	f := &fake{}
	g := New(policy(), f, allow)
	for _, dest := range []string{"192.168.40.3:4443", "192.168.40.2:443", "8.8.8.8:443"} {
		if g.Connect(ap(dest)) != ErrDenied {
			t.Fatal("unexpected TCP destination")
		}
	}
	if g.Connect(policy().Relay) != nil || len(f.calls) != 1 {
		t.Fatal("selected relay rejected")
	}
}
func TestPolicyUpdatesRaceSafe(t *testing.T) {
	f := &fake{}
	g := New(policy(), f, allow)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				g.Write(nil, policy().Relay)
				g.Batch([][]byte{{1}}, policy().Relay)
				g.Replace(policy())
				g.Replace(Policy{})
			}
		}()
	}
	wg.Wait()
	g.Replace(Policy{})
	if g.Write(nil, policy().Relay) != ErrDenied {
		t.Fatal("revocation failed")
	}
}
