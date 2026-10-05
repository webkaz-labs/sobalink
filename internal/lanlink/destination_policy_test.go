package lanlink

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
)

func TestDestinationPolicyReachesServerAndRejectsExternalCandidates(t *testing.T) {
	clearRouteEnvironment(t)
	cfg := testNode().cfg
	cfg.DestinationPolicy = lanpolicy.Config{Mode: lanpolicy.AllowedLANDestinations, Prefixes: []string{"127.0.0.1/32"}}
	node, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	want := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	if !reflect.DeepEqual(node.server.DestinationPrefixes, want) {
		t.Fatal("server constructor lost destination policy")
	}
	cfg.DestinationPolicy.Prefixes[0] = "192.168.50.0/24"
	if !reflect.DeepEqual(node.server.DestinationPrefixes, want) || node.cfg.DestinationPolicy.Prefixes[0] != "127.0.0.1/32" {
		t.Fatal("caller mutation changed runtime authority")
	}
	cfg = node.cfg
	cfg.Candidates = []RouteCandidate{legacyCandidate(cfg.Relay), routeFixture("external", "192.0.2.20:443")}
	if _, err := NewNode(cfg); err == nil {
		t.Fatal("outside prepared relay reached engine construction")
	}
	cfg.Candidates = nil
	cfg.DestinationPolicy = lanpolicy.Config{}
	normal, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer normal.Close()
	if normal.server.DestinationPrefixes != nil {
		t.Fatal("legacy trusted relay unexpectedly restricted")
	}
}

func TestDestinationPolicyIntersectsRemoteRoutesWithoutExternalFallback(t *testing.T) {
	r, anchor, candidates := routeRuntimeFixture(t, time.Time{})
	r.destinationPolicy = lanpolicy.Config{Mode: lanpolicy.AllowedLANDestinations, Prefixes: []string{"127.0.0.1/32", "192.168.50.0/24"}}
	made := 0
	r.makeClient = func(address tailcat.Addr) peerTransport {
		made++
		ci, err := tailcat.ParseAddr(address)
		if err != nil || len(ci.Region) != 1 || ci.Region[0].Nodes[0].HostName != "192.168.50.2" {
			t.Fatal("outside route selected")
		}
		return &fakePeerTransport{probeErr: errors.New("synthetic permitted relay unavailable")}
	}
	if err := r.prepare(anchor, false, RouteSnapshot{Permitted: candidates}); err != nil {
		t.Fatal(err)
	}
	if len(r.candidates) != 1 || r.candidates[0] != candidates[0] {
		t.Fatal("destination policy did not intersect approved routes")
	}
	if _, err := r.dial(context.Background(), "tcp", 8080); err == nil {
		t.Fatal("unavailable selected route unexpectedly succeeded")
	}
	if made != 1 {
		t.Fatal("fallback attempted outside destination policy")
	}
	denied, anchor, candidates := routeRuntimeFixture(t, time.Time{})
	denied.destinationPolicy = r.destinationPolicy
	if err := denied.prepare(anchor, false, RouteSnapshot{Permitted: candidates[1:]}); !errors.Is(err, ErrRoutePermission) {
		t.Fatal("outside-only routes accepted", err)
	}
}
