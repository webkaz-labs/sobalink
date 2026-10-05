package routecat

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestSelectedEndpointsDoNotAdvertiseOtherDestinations(t *testing.T) {
	selected := []netip.Prefix{netip.MustParsePrefix("192.168.50.0/24"), netip.MustParsePrefix("fd01::/64")}
	endpoints := []netip.AddrPort{netip.MustParseAddrPort("192.168.50.2:40000"), netip.MustParseAddrPort("192.168.51.2:40000"), netip.MustParseAddrPort("[fd01::2]:40000"), netip.MustParseAddrPort("203.0.113.2:40000"), netip.MustParseAddrPort("[::ffff:192.168.50.2]:40000")}
	got := selectedEndpoints(endpoints, selected)
	if !reflect.DeepEqual(got, []netip.AddrPort{endpoints[0], endpoints[2]}) {
		t.Fatal("endpoint advertisement was not filtered")
	}
	normal := selectedEndpoints(endpoints, nil)
	if !reflect.DeepEqual(normal, endpoints) {
		t.Fatal("normal mode changed")
	}
	normal[0] = netip.AddrPort{}
	if endpoints[0].IsValid() == false {
		t.Fatal("endpoint result aliases source")
	}
	if len(selectedEndpoints(endpoints, []netip.Prefix{})) != 0 {
		t.Fatal("empty selected policy advertised endpoints")
	}
}
