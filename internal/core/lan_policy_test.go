package core

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
)

func TestLANDestinationPolicyExplicitPersistedAndDetached(t *testing.T) {
	c := openLANTestCore(t)
	view := mustCommand(t, c, "lan.policy.get", map[string]any{}).(map[string]any)
	if view["mode"] != lanpolicy.TrustedRelay || c.lanStoreCopy() != nil {
		t.Fatal("read created identity or changed default")
	}
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"mode": lanpolicy.AllowedLANDestinations, "prefixes": []string{"192.168.50.0/24"}}
	got := mustCommand(t, c, "lan.policy.set", payload).(map[string]any)
	if got["mode"] != lanpolicy.AllowedLANDestinations || got["restartRequired"] != true {
		t.Fatal(got)
	}
	state := c.lanStoreCopy().copy()
	if state.Version != 4 || !state.DestinationPolicy.Strict() {
		t.Fatal("not versioned")
	}
	state.DestinationPolicy.Prefixes[0] = "10.0.0.0/8"
	if c.lanStoreCopy().copy().DestinationPolicy.Prefixes[0] != "192.168.50.0/24" {
		t.Fatal("copy aliases policy")
	}
	loaded, err := readLANStore(filepath.Join(c.dir, "lan.json"))
	if err != nil || !reflect.DeepEqual(loaded.copy().DestinationPolicy, c.lanStoreCopy().copy().DestinationPolicy) {
		t.Fatal("policy failed reload", err)
	}
	before, _ := os.ReadFile(filepath.Join(c.dir, "lan.json"))
	mustCommand(t, c, "lan.policy.set", payload)
	after, _ := os.ReadFile(filepath.Join(c.dir, "lan.json"))
	if string(before) != string(after) {
		t.Fatal("repeat changed durable state")
	}
}

func TestLANDestinationPolicyRejectsUnselectedRelayAndMissingChoice(t *testing.T) {
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []map[string]any{
		{"prefixes": []string{"192.168.50.0/24"}},
		{"mode": lanpolicy.AllowedLANDestinations, "prefixes": []string{"0.0.0.0/0"}},
		{"mode": lanpolicy.AllowedLANDestinations, "prefixes": []string{"192.168.51.0/24"}},
		{"mode": lanpolicy.TrustedRelay, "prefixes": []string{"192.168.50.0/24"}},
	} {
		if _, err := command(c, randomID(), "lan.policy.set", payload); err == nil {
			t.Fatal("invalid policy accepted", payload)
		}
	}
	if c.lanStoreCopy().copy().DestinationPolicy.Strict() {
		t.Fatal("failed policy applied")
	}
}

func TestLANDestinationPolicyFailedSaveDoesNotClaimDurableChange(t *testing.T) {
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	store := c.lanStoreCopy()
	store.write = func(string, []byte) error { return errors.New("synthetic disk failure") }
	_, err := command(c, randomID(), "lan.policy.set", map[string]any{"mode": lanpolicy.AllowedLANDestinations, "prefixes": []string{"192.168.50.0/24"}})
	if !errors.Is(err, config.ErrAtomicRecovery) || !store.routesNeedRecovery() {
		t.Fatal("failed edit did not require review", err)
	}
	if store.copy().DestinationPolicy.Strict() {
		t.Fatal("failed write changed policy")
	}
	loaded, err := readLANStore(filepath.Join(c.dir, "lan.json"))
	if err != nil || loaded.copy().DestinationPolicy.Strict() {
		t.Fatal("fresh process must observe actual old disk state", err)
	}
}
