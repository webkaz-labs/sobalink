package directlan

import (
	"encoding/base64"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// These tests use copied synthetic models and pure reducers only. They create
// no Node, listener, generation, receipt, physical owner or application traffic.
func currentLocalProjectionBase(base Config, model endpointmeta.Snapshot) Config {
	base = currentProjectionBase(base, model)
	base.Listen = netip.MustParseAddrPort(model.LocalPeer.Endpoint)
	return base
}

func TestCurrentEndpointProjectionRetainsLocalMovementBinding(t *testing.T) {
	for _, test := range []struct {
		name    string
		version int
		joiner  bool
	}{{"v3-host", 3, false}, {"v4-host", 4, false}, {"v4-joiner", 4, true}} {
		t.Run(test.name, func(t *testing.T) {
			base, model, remote, now := currentProjectionFixture(t)
			model.Version = test.version
			if test.joiner {
				local := model.LocalPeer
				model.LocalPeer, model.Peers[0].Peer = model.Peers[0].Peer, local
				model.LocalScope = model.Peers[0].PairContext.JoinerScope
				model.PreviousLocalEndpoint = model.LocalPeer.Endpoint
				base.Identity = remote
				state, err := endpointmeta.InitialState(*model.Peers[0].PairContext, remote.PublicKey())
				if err != nil {
					t.Fatal(err)
				}
				model.Peers[0].EndpointState = &state
			}
			original := currentProjectionCopy(model)
			for _, endpoint := range []string{"127.0.0.8:45108", "127.0.0.9:45109", original.LocalPeer.Endpoint} {
				previous := model.LocalPeer.Endpoint
				model = currentProjectionFinish(t, model, endpointmeta.Mutation{Kind: "local-endpoint", LocalEndpoint: endpoint}, now)
				before := currentProjectionCopy(model)
				cfg, err := ProjectCurrentEndpointConfig(currentLocalProjectionBase(base, model), model, now)
				if err != nil || len(cfg.Peers) != 1 || cfg.Listen.String() != endpoint || cfg.Validate() != nil {
					t.Fatal("retained local projection", err)
				}
				authority := cfg.currentEndpoints
				key := model.Peers[0].Peer.Key
				if authority == nil || authority.localEndpoint != endpoint || authority.previousLocalEndpoint != previous || !reflect.DeepEqual(cfg.PairContexts[key], *original.Peers[0].PairContext) || !reflect.DeepEqual(model.Peers, original.Peers) || !reflect.DeepEqual(model, before) {
					t.Fatal("local projection changed immutable binding or retained evidence")
				}
				if endpoint != original.LocalPeer.Endpoint {
					ordinary := cloneGenerationConfig(cfg)
					ordinary.currentEndpoints = nil
					if ordinary.Validate() == nil {
						t.Fatal("local movement bypassed ordinary fixed binding checks")
					}
				}
				copy := cloneGenerationConfig(cfg)
				cfg.currentEndpoints.previousLocalEndpoint = "127.0.0.7:45107"
				if cfg.Validate() == nil || copy.Validate() != nil || copy.currentEndpoints.previousLocalEndpoint != previous {
					t.Fatal("local authority was mutable or aliased across copies")
				}
			}
		})
	}
}

func TestCurrentEndpointProjectionLocalMovementRetainsRemoteAuthority(t *testing.T) {
	base, model, remote, now := currentProjectionFixture(t)
	model = currentProjectionSet(t, model, remote, "7", "127.0.0.2:45107", now, false)
	before := currentProjectionCopy(model)
	moved := currentProjectionFinish(t, model, endpointmeta.Mutation{Kind: "local-endpoint", LocalEndpoint: "127.0.0.8:45108"}, now)
	if !reflect.DeepEqual(moved.Peers, before.Peers) {
		t.Fatal("local movement changed remote proof, approval or counters")
	}
	current, err := ProjectCurrentEndpointConfig(currentLocalProjectionBase(base, moved), moved, now)
	if err != nil || len(current.Peers) != 1 {
		t.Fatal(err)
	}
	remoteAuthority := current.currentEndpoints.peers[remote.PublicKey()]
	if remoteAuthority.highwater != "7" || remoteAuthority.endpoint != "127.0.0.2:45107" || !remoteAuthority.deadline.Equal(now.Add(time.Hour)) {
		t.Fatal("local movement renewed or replaced remote authority")
	}
	expired, err := ProjectCurrentEndpointConfig(currentLocalProjectionBase(base, moved), moved, now.Add(time.Hour))
	if err != nil || len(expired.Peers) != 0 || len(expired.PairContexts) != 0 || !reflect.DeepEqual(expired.InactiveEndpointPeerKeys(), []string{remote.PublicKey()}) {
		t.Fatal("local movement restored expired remote authority", err)
	}
	terminal, changed, err := endpointmeta.RevokeManagedPairV4(moved, moved.Peers[0], now, 1<<20)
	if err != nil || !changed {
		t.Fatal(err)
	}
	denied, err := ProjectCurrentEndpointConfig(currentLocalProjectionBase(base, terminal), terminal, now)
	if err != nil || len(denied.Peers) != 0 || len(denied.PairContexts) != 0 || !reflect.DeepEqual(denied.DeniedPeerKeys, []string{remote.PublicKey()}) {
		t.Fatal("local movement restored terminal pair", err)
	}
	legacy := currentProjectionCopy(moved)
	legacy.Peers[0].PairContext, legacy.Peers[0].EndpointState = nil, nil
	legacy.Peers[0].ContextConfirmed = false
	legacyConfig, err := ProjectCurrentEndpointConfig(currentLocalProjectionBase(base, legacy), legacy, now)
	if err != nil || len(legacyConfig.Peers) != 1 || legacyConfig.currentEndpoints == nil || len(legacyConfig.PairContexts) != 0 {
		t.Fatal("local movement discarded legacy membership or its projection seal", err)
	}
}

func TestCurrentEndpointProjectionLocalMovementGuards(t *testing.T) {
	base, initial, _, now := currentProjectionFixture(t)
	mutation := endpointmeta.Mutation{Kind: "local-endpoint", LocalEndpoint: "127.0.0.8:45108"}
	moved := currentProjectionFinish(t, initial, mutation, now)
	if _, err := ProjectCurrentEndpointConfig(base, moved, now); err == nil {
		t.Fatal("stale local listener accepted")
	}
	review, err := endpointmeta.PreviewMutation(initial, mutation)
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := endpointmeta.Fence(initial, mutation, review, base64.RawURLEncoding.EncodeToString(make([]byte, 32)), now, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectCurrentEndpointConfig(base, fenced, now); !errors.Is(err, ErrRecovery) {
		t.Fatal("pending local movement became current", err)
	}
	for _, previous := range []bool{false, true} {
		bad := currentProjectionCopy(moved)
		pair := bad.Peers[0].PairContext
		pair.JoinerScope = endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/24"}}
		state, err := endpointmeta.InitialState(*pair, bad.LocalPeer.Key)
		if err != nil {
			t.Fatal(err)
		}
		bad.Peers[0].EndpointState = &state
		if previous {
			bad.PreviousLocalEndpoint = "127.0.1.8:45108"
		} else {
			bad.LocalPeer.Endpoint = "127.0.1.8:45108"
		}
		if err := bad.ValidateAt(now); err != nil {
			t.Fatal("scope fixture must remain a structurally valid model", err)
		}
		if _, err := ProjectCurrentEndpointConfig(currentLocalProjectionBase(base, bad), bad, now); !errors.Is(err, ErrPolicy) {
			t.Fatal("retained local endpoint escaped immutable remote scope", err)
		}
	}
	for name, change := range map[string]func(*endpointmeta.Snapshot){
		"local-scope": func(m *endpointmeta.Snapshot) {
			m.LocalScope.Prefixes = []string{"127.0.0.0/9"}
		},
		"local-family": func(m *endpointmeta.Snapshot) {
			m.LocalPeer.Endpoint, m.PreviousLocalEndpoint = "[::1]:45108", "[::1]:45101"
			m.LocalScope = endpointmeta.Scope{Family: "ipv6", Prefixes: []string{"::1/128"}}
		},
		"unconfirmed": func(m *endpointmeta.Snapshot) { m.Peers[0].ContextConfirmed = false },
		"wrong-binding": func(m *endpointmeta.Snapshot) {
			m.Peers[0].EndpointState.PairBinding = m.LocalPeer.Key
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := currentProjectionCopy(moved)
			change(&bad)
			cfg := currentLocalProjectionBase(base, bad)
			cfg.AllowedPrefixes = nil
			for _, prefix := range bad.LocalScope.Prefixes {
				cfg.AllowedPrefixes = append(cfg.AllowedPrefixes, netip.MustParsePrefix(prefix))
			}
			if got, err := ProjectCurrentEndpointConfig(cfg, bad, now); err == nil || len(got.Peers) != 0 || got.currentEndpoints != nil {
				t.Fatal("invalid local projection leaked authority", err)
			}
		})
	}
}
