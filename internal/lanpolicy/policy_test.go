package lanpolicy

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestCanonical(t *testing.T) {
	for _, raw := range []string{"0.0.0.0/0", "10.0.0.0/7", "172.0.0.0/8", "192.168.1.2/24", "2001:db8::/32", "fe80::/64", "::ffff:192.168.1.0/120", "::/0", "fc00::/6", "192.168.1.1", "localhost", " 10.0.0.0/8"} {
		if _, err := (Config{Mode: AllowedLANDestinations, Prefixes: []string{raw}}).Canonical(); err == nil {
			t.Errorf("accepted invalid prefix %q", raw)
		}
	}
	for _, c := range []Config{{Mode: "invalid"}, {Mode: AllowedLANDestinations}, {Mode: TrustedRelay, Prefixes: []string{"10.0.0.0/8"}}} {
		if _, err := c.Canonical(); err == nil {
			t.Errorf("accepted invalid policy %+v", c)
		}
	}
	old, err := (Config{}).Canonical()
	if err != nil || old.Mode != TrustedRelay {
		t.Fatal("legacy behavior changed")
	}
	original := Config{Mode: AllowedLANDestinations, Prefixes: []string{"fd01::/64", "192.168.50.0/24", "192.168.50.0/24"}}
	got, err := original.Canonical()
	if err != nil || !reflect.DeepEqual(got.Prefixes, []string{"192.168.50.0/24", "fd01::/64"}) {
		t.Fatalf("canonical: %+v %v", got, err)
	}
	got.Prefixes[0] = "changed"
	if original.Prefixes[0] != "fd01::/64" {
		t.Fatal("policy aliases input")
	}
}
func TestRelayAdmission(t *testing.T) {
	c := Config{Mode: AllowedLANDestinations, Prefixes: []string{"192.168.50.0/24", "fd01::/64"}}
	for _, raw := range []string{"192.168.50.2:8443", "[fd01::2]:8443"} {
		if err := c.CheckRelay(netip.MustParseAddrPort(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"192.168.51.2:8443", "[fd02::2]:8443", "192.168.50.2:0", "[::ffff:192.168.50.2]:8443", "[fd01::2%example]:8443", "203.0.113.2:8443"} {
		if err := c.CheckRelay(netip.MustParseAddrPort(raw)); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}
