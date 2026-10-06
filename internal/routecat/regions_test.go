package routecat

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	stock "github.com/tailscale/tailcat"
	"tailscale.com/feature/buildfeatures"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

func testRegion(host string, port int) *tailcfg.DERPRegion {
	n := &tailcfg.DERPNode{Name: host, RegionID: 1, HostName: host, CertName: "sha256-raw:" + strings.Repeat("a", 64), IPv4: host, IPv6: "none", DERPPort: port, STUNPort: -1}
	return &tailcfg.DERPRegion{RegionID: 1, RegionCode: "1", Nodes: []*tailcfg.DERPNode{n}}
}

func cleanRuntimeEnvironment(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "TS_PROXY", "TS_DEBUG_USE_DERP_ADDR"} {
		t.Setenv(k, "")
	}
}

func TestRegionsCanonicalPrivateFirstAndOwned(t *testing.T) {
	a, b := testRegion("192.0.2.1", 4443), testRegion("10.1.2.3", 54446)
	left, err := validateRegions([]*tailcfg.DERPRegion{a, b}, false)
	if err != nil {
		t.Fatal(err)
	}
	right, err := validateRegions([]*tailcfg.DERPRegion{b, a}, false)
	if err != nil || !reflect.DeepEqual(left, right) {
		t.Fatal("candidate IDs depend on input order")
	}
	if left[0].Nodes[0].HostName != "10.1.2.3" || left[0].RegionID != 1 || left[1].RegionID != 2 {
		t.Fatal("unstable or non-local-first home")
	}
	b.Nodes[0].IPv4 = "198.51.100.2"
	if left[0].Nodes[0].IPv4 != "10.1.2.3" {
		t.Fatal("caller owns live map storage")
	}
	if !regionMap(left).OmitDefaultRegions {
		t.Fatal("default regions not excluded")
	}
}

func TestRegionsRejectUnsafeAndOversizedInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*tailcfg.DERPRegion)
	}{
		{"hostname", func(r *tailcfg.DERPRegion) { r.Nodes[0].HostName = "example.invalid" }},
		{"dns-v6", func(r *tailcfg.DERPRegion) { r.Nodes[0].IPv6 = "" }},
		{"address-mismatch", func(r *tailcfg.DERPRegion) { r.Nodes[0].IPv4 = "192.0.2.2" }},
		{"no-pin", func(r *tailcfg.DERPRegion) { r.Nodes[0].CertName = "" }},
		{"bad-pin", func(r *tailcfg.DERPRegion) { r.Nodes[0].CertName = "sha256-raw:" + strings.Repeat("z", 64) }},
		{"insecure", func(r *tailcfg.DERPRegion) { r.Nodes[0].InsecureForTests = true }},
		{"stun", func(r *tailcfg.DERPRegion) { r.Nodes[0].STUNPort = 3478 }},
		{"stun-override", func(r *tailcfg.DERPRegion) { r.Nodes[0].STUNTestIP = "192.0.2.2" }},
		{"port80", func(r *tailcfg.DERPRegion) { r.Nodes[0].CanPort80 = true }},
		{"port-zero", func(r *tailcfg.DERPRegion) { r.Nodes[0].DERPPort = 0 }},
		{"multiple-nodes", func(r *tailcfg.DERPRegion) { r.Nodes = append(r.Nodes, r.Nodes[0]) }},
		{"nil-node", func(r *tailcfg.DERPRegion) { r.Nodes[0] = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := testRegion("127.0.0.1", 54446)
			tc.mutate(r)
			if _, err := validateRegions([]*tailcfg.DERPRegion{r}, false); err == nil {
				t.Fatal("unsafe relay accepted")
			}
		})
	}
	for _, regs := range [][]*tailcfg.DERPRegion{nil, {nil}, {testRegion("127.0.0.1", 54446), testRegion("127.0.0.1", 54446)}, make([]*tailcfg.DERPRegion, RelayRegionNamespace+1)} {
		if _, err := validateRegions(regs, false); err == nil {
			t.Fatal("invalid set accepted")
		}
	}
}

func TestPrivateOnlyMapAndBuildFailClosed(t *testing.T) {
	cleanRuntimeEnvironment(t)
	if _, err := validateRegions([]*tailcfg.DERPRegion{testRegion("192.0.2.1", 4443)}, true); err == nil {
		t.Fatal("external relay accepted")
	}
	regions, err := validateRegions([]*tailcfg.DERPRegion{testRegion("127.0.0.1", 54446)}, true)
	if err != nil || !regions[0].NoMeasureNoHome {
		t.Fatal("private-only diagnostics were not disabled")
	}
	if !buildfeatures.HasPortMapper && !buildfeatures.HasCaptivePortal && !buildfeatures.HasUseProxy {
		err = validateRuntime(true)
		if buildfeatures.HasUDPTransport && !errors.Is(err, ErrPrivateOnlyBuild) {
			t.Fatalf("direct-enabled build did not fail closed: %v", err)
		}
		if !buildfeatures.HasUDPTransport && err != nil {
			t.Fatal(err)
		}
	}
}

func TestAddressAndProtectedJSONStayStockCompatible(t *testing.T) {
	k := key.NewNode()
	psk := stock.NewPresharedKey()
	stockInfo := stock.ConnInfo{ServerPublic: stock.NodePublic{NodePublic: k.Public()}, ServerDiscoPublic: stock.DiscoPublicForNode(k), PresharedKey: psk, Region: []*tailcfg.DERPRegion{testRegion("127.0.0.1", 54446)}}
	parsed, err := ParseAddr(Addr(stockInfo.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	if string(parsed.Addr()) != string(stockInfo.Addr()) {
		t.Fatal("capability wire format changed")
	}
	if parsed.ServerPublic.NodePublic != k.Public() || parsed.PresharedKey != PresharedKey(psk) || parsed.ServerDiscoPublic != DiscoPublicForNode(k) {
		t.Fatal("stable identity material changed")
	}
	a, _ := json.Marshal(psk)
	b, _ := json.Marshal(parsed.PresharedKey)
	if string(a) != string(b) {
		t.Fatal("protected state JSON changed")
	}
	if _, err := stock.ParseAddr(stock.Addr(parsed.Addr())); err != nil {
		t.Fatal("stock cannot parse adapted address")
	}
}

func TestExpandNeverBootstrapsImplicitMaps(t *testing.T) {
	for _, id := range []tailcfg.DERPRegionID{0, 1, -1} {
		ci := ConnInfo{RegionID: id}
		if !errors.Is(ci.Expand(context.Background()), ErrExplicitRegions) {
			t.Fatal("implicit map accepted")
		}
	}
	ci := ConnInfo{Region: []*tailcfg.DERPRegion{testRegion("127.0.0.1", 54446)}}
	if err := ci.Expand(context.Background(), &tailcfg.DERPMap{}); !errors.Is(err, ErrExplicitRegions) {
		t.Fatal("alternate map accepted")
	}
}

func TestPresenceAllRegionsAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var sent []tailcfg.DERPRegionID
	done := make(chan struct{})
	go func() {
		defer close(done)
		runPresence(ctx, time.Hour, []tailcfg.DERPRegionID{1, 2, 3}, func(id tailcfg.DERPRegionID) {
			mu.Lock()
			sent = append(sent, id)
			mu.Unlock()
			if id == 3 {
				cancel()
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("presence did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(sent, []tailcfg.DERPRegionID{1, 2, 3}) {
		t.Fatal("presence omitted or repeated candidate")
	}
}

func TestMeowRejectsUnknownRegionOrChangedDiscoWithoutEngine(t *testing.T) {
	k := key.NewNode()
	disco := DiscoPublicForNode(k).DiscoPublic
	regs, _ := validateRegions([]*tailcfg.DERPRegion{testRegion("127.0.0.1", 54446)}, false)
	b := &locoBackend{logf: logger.Discard, dm: regionMap(regs), homeRegion: 1, clients: map[key.NodePublic]*tailcfg.Node{k.Public(): {Key: k.Public(), DiscoKey: disco, HomeDERP: 1}}}
	if b.onMeow(2, k.Public(), disco) {
		t.Fatal("unknown region accepted")
	}
	if b.onMeow(1, k.Public(), DiscoPublicForNode(key.NewNode()).DiscoPublic) {
		t.Fatal("discovery identity changed without admission")
	}
	if !b.onMeow(1, k.Public(), disco) {
		t.Fatal("idempotent announcement rejected")
	}
}

func TestRelayCandidatesAreCopies(t *testing.T) {
	regs, _ := validateRegions([]*tailcfg.DERPRegion{testRegion("127.0.0.1", 54446)}, false)
	s := &Server{lb: &locoBackend{dm: regionMap(regs)}}
	got := s.RelayCandidates()
	got[0].Nodes[0].IPv4 = "192.0.2.1"
	if s.RelayCandidates()[0].Nodes[0].IPv4 != "127.0.0.1" {
		t.Fatal("status exposes live configuration")
	}
}

func TestRegionMigrationKeepsPublishedNodeViewsImmutable(t *testing.T) {
	peer := &tailcfg.Node{ID: 2, Key: key.NewNode().Public(), HomeDERP: 1}
	published := peer.View()
	updated := peerWithHomeDERP(peer, 2)
	if published.HomeDERP() != 1 || peer.HomeDERP != 1 {
		t.Fatal("migration mutated the previously published network map")
	}
	if updated.HomeDERP != 2 || updated.Key != peer.Key || updated.ID != peer.ID {
		t.Fatal("migration lost stable peer identity or new relay")
	}
	if published.Equal(updated.View()) {
		t.Fatal("magicsock would skip the relay change as an unchanged NodeView")
	}
}
