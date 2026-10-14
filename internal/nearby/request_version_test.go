package nearby

import (
	"bytes"
	"net/netip"
	"testing"
)

func TestRequestAdvertisementVersionsNeverDowngrade(t *testing.T) {
	for _, capability := range []string{RequestCapability, ReceiveCapability} {
		ad := testAd(1)
		ad.Protocol = RequestProtocol
		ad.Capability = capability
		if ad.Validate() != nil {
			t.Fatal("v2 advertisement rejected")
		}
		packet := testPacket(t, ad)
		got, _, _, e := unpackPacket(packet)
		if e != nil || got != ad {
			t.Fatal("wire changed v2 capability", e)
		}
		cfg := Config{Scope: testScope(), Advertisement: ad}
		cfg.Advertisement.Bootstrap = netip.AddrPortFrom(cfg.Scope.LocalAddress, cfg.Advertisement.Bootstrap.Port())
		reg, e := dnssdRegistrationFor(cfg, discoveryPolicy{})
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Contains(reg.Records[2].Data, []byte(RequestProtocol)) || !bytes.Contains(reg.Records[2].Data, []byte(capability)) {
			t.Fatal("DNS-SD downgraded request capability")
		}
		ad.Protocol = Protocol
		if ad.Validate() == nil {
			t.Fatal("v1 accepted v2 capability")
		}
	}
}
