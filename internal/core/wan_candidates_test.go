package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
	"github.com/webkaz-labs/sobalink/internal/routecat"
)

func wanCommand(c *Core, name string, payload any) (any, error) {
	raw, _ := json.Marshal(payload)
	return c.wanCandidatesCommand(context.Background(), name, raw)
}

func TestWANCandidateStateExplicitPrivateAndMonotonic(t *testing.T) {
	c := openLANTestCore(t)
	got, err := wanCommand(c, "wan.candidates.get", map[string]any{})
	if err != nil || got.(map[string]any)["enabled"] != false || c.lanStoreCopy() != nil {
		t.Fatal("reading WAN config changed state", err)
	}
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	before := c.lanStoreCopy().copy()
	payload := map[string]any{"enabled": true, "stunEndpoints": []string{"[2001:db8::1]:3478", "192.0.2.1:3478"}, "advertiseIPv6": true}
	if _, err := wanCommand(c, "wan.candidates.set", payload); err != nil {
		t.Fatal(err)
	}
	state := c.lanStoreCopy().copy()
	if state.Version != 5 || state.WANCandidates == nil || state.Identity.PublicKey() != before.Identity.PublicKey() || *state.Selection != *before.Selection || !reflect.DeepEqual(state.DestinationPolicy, before.DestinationPolicy) {
		t.Fatal("WAN edit changed unrelated state")
	}
	state.WANCandidates.STUNEndpoints[0] = "198.51.100.9:3478"
	if reflect.DeepEqual(state.WANCandidates, c.lanStoreCopy().copy().WANCandidates) {
		t.Fatal("state copy aliases WAN settings")
	}
	path := filepath.Join(c.dir, "lan.json")
	assertLANStatePrivate(t, path)
	loaded, err := readLANStore(path)
	if err != nil || !reflect.DeepEqual(loaded.copy().WANCandidates, c.lanStoreCopy().copy().WANCandidates) {
		t.Fatal("WAN settings did not reload", err)
	}
	durable, _ := os.ReadFile(path)
	if _, err := wanCommand(c, "wan.candidates.set", payload); err != nil {
		t.Fatal(err)
	}
	repeat, _ := os.ReadFile(path)
	if string(durable) != string(repeat) {
		t.Fatal("repeat mutated durable state")
	}
	if err := c.configureLANWithPolicy(nil, false, &lanpolicy.Config{Mode: lanpolicy.TrustedRelay}); err != nil {
		t.Fatal(err)
	}
	if current := c.lanStoreCopy().copy(); current.Version != 5 || current.WANCandidates == nil {
		t.Fatal("policy write downgraded WAN state")
	}
	if _, err := wanCommand(c, "wan.candidates.set", map[string]any{"enabled": false}); err != nil {
		t.Fatal(err)
	}
	if current := c.lanStoreCopy().copy(); current.Version != 5 || current.WANCandidates != nil {
		t.Fatal("disable failed or downgraded state")
	}
	if err := c.configureLANWithPolicy(nil, false, &lanpolicy.Config{Mode: lanpolicy.AllowedLANDestinations, Prefixes: []string{"192.168.50.0/24"}}); err != nil {
		t.Fatal(err)
	}
	if c.lanStoreCopy().copy().Version != 5 {
		t.Fatal("later strict policy downgraded state")
	}
}

func TestWANCandidatesNeverWidenStrictDestinationPolicy(t *testing.T) {
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	on := map[string]any{"enabled": true, "advertiseIPv6": true}
	if _, err := wanCommand(c, "wan.candidates.set", on); err != nil {
		t.Fatal(err)
	}
	policy := &lanpolicy.Config{Mode: lanpolicy.AllowedLANDestinations, Prefixes: []string{"192.168.50.0/24"}}
	if err := c.configureLANWithPolicy(nil, false, policy); networkErrorCode(err) != "wan_candidates_restricted" {
		t.Fatal("strict setup accepted WAN config", err)
	}
	raw, _ := json.Marshal(policy)
	if _, err := c.lanPolicyCommand(context.Background(), "lan.policy.set", raw); networkErrorCode(err) != "wan_candidates_restricted" {
		t.Fatal("strict policy accepted WAN config", err)
	}
	if c.lanStoreCopy().copy().DestinationPolicy.Strict() {
		t.Fatal("failed strict transition applied")
	}
	if _, err := wanCommand(c, "wan.candidates.set", map[string]any{"enabled": false}); err != nil {
		t.Fatal(err)
	}
	if err := c.configureLANWithPolicy(nil, false, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := wanCommand(c, "wan.candidates.set", on); networkErrorCode(err) != "wan_candidates_restricted" {
		t.Fatal("WAN config widened strict policy", err)
	}
	state := c.lanStoreCopy().copy()
	if state.WANCandidates != nil || !state.DestinationPolicy.Strict() {
		t.Fatal("failed WAN edit applied")
	}
}

func TestWANCandidatesRejectMalformedAndLegacySmuggling(t *testing.T) {
	for _, eps := range [][]string{{"stun.example.invalid:3478"}, {"192.0.2.1"}, {"192.0.2.1:0"}, {" 192.0.2.1:3478"}, {"192.0.2.1:03478"}, {"[2001:DB8::1]:3478"}, {"[2001:db8::1%fixture]:3478"}, {"[::ffff:192.0.2.1]:3478"}, {"224.0.0.1:3478"}, {"192.0.2.1:3478", "192.0.2.1:3478"}, make([]string, 5)} {
		if _, err := (WANCandidateConfig{STUNEndpoints: eps}).Canonical(); networkErrorCode(err) != "wan_candidates_invalid" {
			t.Fatal("invalid numeric configuration admitted", err)
		}
	}
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []any{map[string]any{}, map[string]any{"enabled": true}, map[string]any{"enabled": false, "advertiseIPv6": true}, map[string]any{"enabled": false, "stunEndpoints": []string{"192.0.2.1:3478"}}, map[string]any{"enabled": true, "advertiseIPv6": true, "defaultSTUN": true}} {
		if _, err := wanCommand(c, "wan.candidates.set", payload); err == nil {
			t.Fatal("ambiguous config accepted")
		}
	}
	state := c.lanStoreCopy().copy()
	state.WANCandidates = &WANCandidateConfig{AdvertiseIPv6: true}
	for version := 1; version <= 4; version++ {
		state.Version = version
		if validateLANState(state) == nil {
			t.Fatal("legacy state silently admitted WAN authority")
		}
	}
	state.Version = 5
	if err := validateLANState(state); err != nil {
		t.Fatal(err)
	}
}

func TestWANCandidatesFailedSaveRequiresRecovery(t *testing.T) {
	for _, writeErr := range []error{errors.New("synthetic save failure"), config.ErrAtomicCommitted} {
		c := openLANTestCore(t)
		if err := c.configureLAN(testLANSelection()); err != nil {
			t.Fatal(err)
		}
		store := c.lanStoreCopy()
		store.write = func(string, []byte) error { return writeErr }
		if _, err := wanCommand(c, "wan.candidates.set", map[string]any{"enabled": true, "advertiseIPv6": true}); !errors.Is(err, config.ErrAtomicRecovery) {
			t.Fatal("save failure reported success", err)
		}
		if !store.routesNeedRecovery() {
			t.Fatal("save failure left engine start eligible")
		}
		if _, err := wanCommand(c, "wan.candidates.set", map[string]any{"enabled": false}); !errors.Is(err, config.ErrAtomicRecovery) {
			t.Fatal("subsequent write escaped recovery latch", err)
		}
	}
}

func TestWANCandidatesRejectActiveEngine(t *testing.T) {
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	b, _ := testLANBackend()
	c.mu.Lock()
	c.node = b
	c.mu.Unlock()
	if _, err := wanCommand(c, "wan.candidates.set", map[string]any{"enabled": true, "advertiseIPv6": true}); networkErrorCode(err) != "network_restart_required" {
		t.Fatal("active engine changed WAN authority", err)
	}
}

func TestWANCandidatesMetadataUsesConfigurableStorageAndProbeBudgets(t *testing.T) {
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	var endpoints []string
	for port := 30000; port < 30016; port++ {
		endpoints = append(endpoints, fmt.Sprintf("192.0.2.1:%d", port))
	}
	payload := map[string]any{"enabled": true, "stunEndpoints": endpoints, "probeBudget": 2}
	got, err := wanCommand(c, "wan.candidates.set", payload)
	if err != nil {
		t.Fatal("metadata retained arbitrary four-endpoint cap", err)
	}
	view := got.(map[string]any)
	if view["probeBudget"] != 2 || len(view["stunEndpoints"].([]string)) != len(endpoints) {
		t.Fatal("resource budget or metadata lost")
	}
	store := c.lanStoreCopy()
	saved := store.copy()
	transport, err := wanTransportConfig(saved.WANCandidates)
	if err != nil || transport.ProbeBudget != 2 || len(transport.STUNEndpoints) != len(endpoints) {
		t.Fatal("saved probe budget did not reach transport")
	}
	loaded, err := readLANStore(filepath.Join(c.dir, "lan.json"))
	if err != nil || loaded.copy().WANCandidates.ProbeBudget != 2 {
		t.Fatal("probe budget failed private reload", err)
	}
	originalLimits := store.currentLimits()
	tight := *originalLimits
	raw, _ := json.MarshalIndent(saved, "", "  ")
	tight.bytes = int64(len(raw) + 1)
	store.limits.Store(&tight)
	for port := 30016; port < 30064; port++ {
		endpoints = append(endpoints, fmt.Sprintf("192.0.2.1:%d", port))
	}
	payload["stunEndpoints"] = endpoints
	payload["probeBudget"] = 7
	if _, err := wanCommand(c, "wan.candidates.set", payload); networkErrorCode(err) != "lan_state_capacity" {
		t.Fatal("metadata escaped configurable state byte budget", err)
	}
	if store.routesNeedRecovery() || len(store.copy().WANCandidates.STUNEndpoints) != 16 {
		t.Fatal("capacity rejection mutated state or required recovery without a write")
	}
	store.limits.Store(originalLimits)
	if _, err := wanCommand(c, "wan.candidates.set", payload); err != nil {
		t.Fatal("raising storage budget did not admit metadata", err)
	}
	if store.copy().WANCandidates.ProbeBudget != 7 {
		t.Fatal("adjusted resource budget was not saved")
	}
}

func TestWANCandidatesProbeBudgetDefaultsAndValidation(t *testing.T) {
	canonical, err := (WANCandidateConfig{AdvertiseIPv6: true}).Canonical()
	if err != nil || canonical.ProbeBudget != routecat.DefaultWANProbeBudget {
		t.Fatal("legacy omitted budget lost default", err)
	}
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	for _, budget := range []int{-1, 0, routecat.STUNRegionNamespaceSize + 1} {
		if _, err := wanCommand(c, "wan.candidates.set", map[string]any{"enabled": true, "advertiseIPv6": true, "probeBudget": budget}); networkErrorCode(err) != "wan_candidates_invalid" {
			t.Fatal("invalid explicit resource budget admitted", err)
		}
	}
	if _, err := wanCommand(c, "wan.candidates.set", map[string]any{"enabled": false, "probeBudget": 4}); err == nil {
		t.Fatal("disabled configuration retained probe options")
	}
}
