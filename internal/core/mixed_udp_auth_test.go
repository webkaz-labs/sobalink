package core

import (
	"context"
	"net/netip"
	"testing"
)

func TestMixedOpaqueUDPSourceReachesCoreIdentityGuard(t *testing.T) {
	c, _, node, _ := mixedCorePair(t)
	alias, e := nextMixedPacketAlias()
	if e != nil {
		t.Fatal(e)
	}
	logical := mixedID("lan", "peer-b-lan")
	node.mu.Lock()
	node.sources[alias] = mixedSource{backend: "lan", remote: netip.MustParseAddrPort("100.64.0.2:32000"), identity: "peer-b-lan", logical: logical}
	node.mu.Unlock()
	id, e := c.authenticated(context.Background(), alias)
	if e != nil || id != logical {
		t.Fatal("authenticated opaque UDP alias rejected by Core", id, e)
	}
	if _, e = node.DialIP(context.Background(), "tcp", netip.AddrPortFrom(alias.Addr(), PeerPort)); e == nil {
		t.Fatal("inbound alias became an outgoing application destination")
	}
	node.mu.Lock()
	delete(node.sources, alias)
	node.mu.Unlock()
	if _, e = c.authenticated(context.Background(), alias); e == nil {
		t.Fatal("retired UDP alias authenticated")
	}
}
