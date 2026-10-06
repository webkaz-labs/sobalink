package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
)

func TestLANInitialPolicyIsSavedBeforeAnyNetworkStart(t *testing.T) {
	c := openLANTestCore(t)
	starts := 0
	c.lanFactory = func(store *lanStore) (lanNetworkBackend, error) {
		persisted, err := readLANStore(store.path)
		if err != nil || persisted == nil || !persisted.copy().DestinationPolicy.Strict() {
			t.Fatal("engine created before strict policy persisted", err)
		}
		b, _ := testLANBackend()
		starts++
		return b, nil
	}
	policy := map[string]any{"mode": lanpolicy.AllowedLANDestinations, "prefixes": []string{"192.168.50.0/24"}}
	payload := map[string]any{"mode": "lan", "lan": LANSelection{Kind: "host", Address: "192.168.50.10:48443"}, "lanPolicy": policy}
	mustCommand(t, c, "network.configure", payload)
	mustCommand(t, c, "network.configure", payload)
	if starts != 1 || !c.lanStoreCopy().copy().DestinationPolicy.Strict() {
		t.Fatal("atomic repeat recreated engine or dropped policy")
	}
	_, err := command(c, randomID(), "network.configure", map[string]any{"mode": "lan", "lanPolicy": map[string]any{"mode": lanpolicy.TrustedRelay, "prefixes": []string{}}})
	if networkErrorCode(err) != "network_restart_required" || !c.lanStoreCopy().copy().DestinationPolicy.Strict() {
		t.Fatal("active engine accepted policy widening", err)
	}
}

func TestLANInitialPolicyRejectsInvalidScopeWithoutSavedIdentity(t *testing.T) {
	for _, prefix := range []string{"192.168.51.0/24", "0.0.0.0/0", "192.168.50.10/24"} {
		t.Run(prefix, func(t *testing.T) {
			c := openLANTestCore(t)
			c.lanFactory = func(*lanStore) (lanNetworkBackend, error) { t.Fatal("invalid setup started engine"); return nil, nil }
			_, err := command(c, randomID(), "network.configure", map[string]any{"mode": "lan", "lan": LANSelection{Kind: "host", Address: "192.168.50.10:48443"}, "lanPolicy": map[string]any{"mode": lanpolicy.AllowedLANDestinations, "prefixes": []string{prefix}}})
			if err == nil || c.lanStoreCopy() != nil || c.nodeCopy() != nil {
				t.Fatal("invalid initial policy saved or started", err)
			}
			if _, err := os.Stat(filepath.Join(c.dir, "lan.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid initial scope published an identity")
			}
		})
	}
}

func TestLANSetupSameRelayAppliesChangedPolicyWhileOffline(t *testing.T) {
	c := openLANTestCore(t)
	host := &LANSelection{Kind: "host", Address: "192.168.50.10:48443"}
	if err := c.configureLAN(host); err != nil {
		t.Fatal(err)
	}
	before := c.lanStoreCopy().copy()
	policy := &lanpolicy.Config{Mode: lanpolicy.AllowedLANDestinations, Prefixes: []string{"192.168.50.0/24"}}
	if err := c.configureLANWithPolicy(nil, false, policy); err != nil {
		t.Fatal(err)
	}
	after := c.lanStoreCopy().copy()
	if !after.DestinationPolicy.Strict() || after.Selection.CertificateSHA256 != before.Selection.CertificateSHA256 || c.nodeCopy() != nil {
		t.Fatal("same relay setup discarded policy or changed identity")
	}
	if err := c.configureLANWithPolicy(host, false, &lanpolicy.Config{Mode: lanpolicy.TrustedRelay}); err != nil {
		t.Fatal(err)
	}
	if c.lanStoreCopy().copy().DestinationPolicy.Strict() {
		t.Fatal("explicit widening ignored")
	}
}

func TestLANAtomicPolicySaveFailureBlocksSameProcessRestart(t *testing.T) {
	for _, changeRelay := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-relay", true: "changed-relay"}[changeRelay], func(t *testing.T) {
			c := openLANTestCore(t)
			host := &LANSelection{Kind: "host", Address: "192.168.50.10:48443"}
			if err := c.configureLAN(host); err != nil {
				t.Fatal(err)
			}
			store := c.lanStoreCopy()
			store.write = func(string, []byte) error { return errors.New("synthetic policy save failure") }
			if changeRelay {
				host = &LANSelection{Kind: "host", Address: "192.168.50.10:48444"}
			}
			err := c.configureLANWithPolicy(host, false, &lanpolicy.Config{Mode: lanpolicy.AllowedLANDestinations, Prefixes: []string{"192.168.50.0/24"}})
			if !errors.Is(err, config.ErrAtomicRecovery) || !store.routesNeedRecovery() {
				t.Fatal("failed atomic policy did not latch recovery", err)
			}
			if err := c.configureLAN(nil); !errors.Is(err, config.ErrAtomicRecovery) {
				t.Fatal("same process silently resumed old policy", err)
			}
			loaded, err := readLANStore(store.path)
			if err != nil || loaded.copy().DestinationPolicy.Strict() {
				t.Fatal("failed save was claimed durable", err)
			}
		})
	}
}
