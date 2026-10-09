package core

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/directlan"
)

// Constructor fixtures use synthetic state in owned temporary files or an
// injected writer. They never open Core or construct a node, socket, provider,
// or application.
func directLANCapacityLoadState() directLANState {
	return directLANState{
		Version:   directLANStateVersion,
		Identity:  directlan.Identity{Seed: strings.Repeat("0", 64)},
		Selection: DirectLANSelection{Listen: "127.0.0.1:55446", Prefixes: []string{"127.0.0.1/32"}},
	}
}

func TestDirectLANStoreLoadInitializesStableCapacityIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits lanStoreLimits
	}{
		{name: "policy-defaults", limits: *selectedLANLimits(capacity.Defaults())},
		{name: "supplied-budgets", limits: lanStoreLimits{peers: 16, bytes: 1 << 20}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(directLANCapacityLoadState())
			if err != nil {
				t.Fatal("synthetic state encoding failed", err)
			}
			path := filepath.Join(t.TempDir(), "direct-lan.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal("synthetic state write failed", err)
			}
			store, err := readDirectLANStore(path, tc.limits.bytes, tc.limits.peers)
			if err != nil || store == nil {
				t.Fatal("valid state load failed", err)
			}
			identity := store.limits.Load()
			if identity == nil || *identity != tc.limits || store.bytes != tc.limits.bytes || store.peers != tc.limits.peers {
				t.Fatal("loaded capacity does not retain the supplied budgets")
			}
			for range 3 {
				if store.currentCapacity() != identity || store.limits.Load() != identity {
					t.Fatal("loaded capacity identity is not stable")
				}
			}
			if store.contextPublication != nil || store.contextEpoch != nil || store.reviewRevision != 0 || store.write != nil || store.recovery {
				t.Fatal("loading capacity minted authority or changed legacy state")
			}
			reloaded, err := readDirectLANStore(path, tc.limits.bytes, tc.limits.peers)
			if err != nil || reloaded == nil {
				t.Fatal("second valid state load failed", err)
			}
			other := reloaded.limits.Load()
			if other == nil || other == identity || *other != *identity || store.limits.Load() != identity {
				t.Fatal("separate loaded stores did not retain distinct capacity identities")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, data) {
				t.Fatal("loading capacity changed private state bytes", err)
			}
		})
	}
}

func TestDirectLANStoreConfigureInitializesStableCapacityIdentity(t *testing.T) {
	for _, name := range []string{"policy-defaults", "selected-budgets"} {
		t.Run(name, func(t *testing.T) {
			policy := capacity.Defaults()
			if name == "selected-budgets" {
				policy.Logical["trustedPeers"] = capacity.Limited(16)
				policy.Resources["lanStateBytes"] = capacity.Limited(1 << 20)
			}
			c := &Core{dir: t.TempDir(), capacity: policy}
			writes := 0
			var published []byte
			c.atomicWrite = func(path string, data []byte) error {
				if path != filepath.Join(c.dir, "direct-lan.json") {
					t.Fatal("configuration attempted an unexpected write")
				}
				writes++
				published = append([]byte(nil), data...)
				return nil
			}
			selection := directLANCapacityLoadState().Selection
			if err := c.configureDirectLAN(&selection); err != nil {
				t.Fatal("synthetic offline configuration failed", err)
			}
			store := c.directLANStoreCopy()
			if store == nil || writes != 1 {
				t.Fatal("configuration did not publish exactly one store")
			}
			identity := store.limits.Load()
			want := selectedLANLimits(policy)
			if identity == nil || *identity != *want || store.currentCapacity() != identity || store.peers != want.peers || store.bytes != want.bytes {
				t.Fatal("configured capacity does not retain the selected budgets")
			}
			var saved directLANState
			if json.Unmarshal(published, &saved) != nil || validateDirectLANState(saved) != nil || privateRevision(saved) != privateRevision(store.state) {
				t.Fatal("configured capacity changed the ordinary state publication")
			}
			if store.contextPublication != nil || store.contextEpoch != nil || store.reviewRevision != 1 || store.recovery {
				t.Fatal("configuration minted authority or changed ordinary publication state")
			}
			if err := c.configureDirectLAN(&selection); err != nil || writes != 1 || c.directLANStoreCopy() != store || store.limits.Load() != identity {
				t.Fatal("unchanged configuration replaced its store or capacity identity", err)
			}
		})
	}
}

func TestDirectLANStoreLoadCapacityInitializationPreservesFailures(t *testing.T) {
	for _, kind := range []string{"missing", "malformed-json", "invalid-state", "over-budget", "nonregular-file"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "direct-lan.json")
			budget := int64(1 << 20)
			state := directLANCapacityLoadState()
			if kind == "invalid-state" {
				state.Identity.Seed = "invalid-synthetic-identity"
			}
			data, err := json.Marshal(state)
			if err != nil {
				t.Fatal("synthetic state encoding failed", err)
			}
			switch kind {
			case "missing":
			case "nonregular-file":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal("synthetic directory creation failed", err)
				}
			default:
				if kind == "malformed-json" {
					data = []byte("{")
				}
				if kind == "over-budget" {
					budget = int64(len(data) - 1)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal("synthetic state write failed", err)
				}
			}
			store, err := readDirectLANStore(path, budget, 16)
			if store != nil {
				t.Fatal("unsuccessful load returned a store")
			}
			if kind == "missing" {
				if err != nil {
					t.Fatal("missing-state load changed its result", err)
				}
				return
			}
			if networkErrorCode(err) != "direct_lan_state_invalid" {
				t.Fatal("invalid-state load changed its public error", err)
			}
		})
	}
}
