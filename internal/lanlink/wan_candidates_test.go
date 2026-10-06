package lanlink

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
	"tailscale.com/feature/buildfeatures"
)

func TestWANCandidatesReachServerWithoutChangingCapability(t *testing.T) {
	clearRouteEnvironment(t)
	if !buildfeatures.HasUDPTransport || !buildfeatures.HasNATTraversal {
		t.Skip("WAN requires UDP build")
	}
	cfg := testNode().cfg
	base, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	cfg.WANCandidates = &tailcat.WANConfig{STUNEndpoints: []netip.AddrPort{netip.MustParseAddrPort("192.0.2.1:3478")}, AdvertiseIPv6: true, ProbeBudget: tailcat.DefaultWANProbeBudget}
	node, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	if !reflect.DeepEqual(node.server.WANCandidates, cfg.WANCandidates) || node.Address() != base.Address() {
		t.Fatal("WAN config lost or capability changed")
	}
	cfg.WANCandidates.STUNEndpoints[0] = netip.AddrPort{}
	if !node.server.WANCandidates.STUNEndpoints[0].IsValid() {
		t.Fatal("caller mutated server config")
	}
	cfg = node.cfg
	cfg.DestinationPolicy = lanpolicy.Config{Mode: lanpolicy.AllowedLANDestinations, Prefixes: []string{"127.0.0.1/32"}}
	if _, err := NewNode(cfg); !errors.Is(err, tailcat.ErrWANRestricted) {
		t.Fatal("strict node admitted WAN authority", err)
	}
	cfg.DestinationPolicy = lanpolicy.Config{}
	cfg.PrivateOnly = true
	if _, err := NewNode(cfg); !errors.Is(err, tailcat.ErrWANRestricted) {
		t.Fatal("private-only node admitted WAN authority", err)
	}
}

func TestWANCandidatesReachEveryFreshRouteClient(t *testing.T) {
	r, anchor, candidates := routeRuntimeFixture(t, time.Time{})
	r.wanCandidates = &tailcat.WANConfig{STUNEndpoints: []netip.AddrPort{netip.MustParseAddrPort("192.0.2.1:3478")}, AdvertiseIPv6: true, ProbeBudget: tailcat.DefaultWANProbeBudget}
	if err := r.prepare(anchor, false, RouteSnapshot{Permitted: candidates}); err != nil {
		t.Fatal(err)
	}
	for i := range candidates {
		r.newClientLocked(i)
		client := r.client.(*tailcat.Client)
		if !reflect.DeepEqual(client.WANCandidates, r.wanCandidates) {
			t.Fatal("route fallback lost explicit WAN settings")
		}
		ci, err := tailcat.ParseAddr(client.Server)
		if err != nil || len(ci.Region) != 1 || ci.Region[0].Nodes[0].STUNPort != -1 {
			t.Fatal("discovery map entered pinned capability")
		}
		r.closeClientLocked()
	}
	node := testNode()
	node.cfg.WANCandidates = r.wanCandidates
	replacement := node.replaceRemoteLocked("synthetic", r, r.remote)
	if !reflect.DeepEqual(replacement.wanCandidates, r.wanCandidates) {
		t.Fatal("retirement replacement lost WAN settings")
	}
}
