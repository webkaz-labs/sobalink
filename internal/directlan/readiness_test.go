package directlan

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func TestSelectedAddressReadinessIsExactAndPassive(t *testing.T) {
	address := netip.MustParseAddr("192.168.50.10")
	cases := []struct {
		observed []interfaceObservation
		want     bool
	}{{nil, false}, {[]interfaceObservation{{up: false, addresses: []netip.Addr{address}}}, false}, {[]interfaceObservation{{up: true, addresses: []netip.Addr{netip.MustParseAddr("192.168.50.11")}}}, false}, {[]interfaceObservation{{up: true, addresses: []netip.Addr{address}}}, true}}
	for _, c := range cases {
		if got := observedAddressAvailable(address, c.observed); got != c.want {
			t.Fatal("readiness must require the exact assigned address on an up interface")
		}
	}
	if e := localAddressReady(netip.MustParseAddr("127.0.0.1")); e != nil {
		t.Fatal(e)
	}
	if errors.Is(ErrLocalAddressUnknown, ErrLocalAddressUnavailable) {
		t.Fatal("unknown was classified as safe unavailable")
	}
}

func TestStartupChecksAddressBeforeOpeningSockets(t *testing.T) {
	for _, want := range []error{ErrLocalAddressUnavailable, ErrLocalAddressUnknown} {
		n, e := NewNode(testConfig(81))
		if e != nil {
			t.Fatal(e)
		}
		observed := false
		e = n.start(context.Background(), func(ip netip.Addr) error { observed = true; return want })
		if e != want || !observed || n.started || n.underlay != nil || n.engine != nil {
			t.Fatalf("address classification occurred after startup: %v", e)
		}
		n.Close()
	}
}
