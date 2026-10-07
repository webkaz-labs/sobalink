//go:build soba_e2e

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

func TestSavedHostBrowserFixtureUsesProductionReviewWithoutNetwork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "synthetic-profile")
	if err := PrepareSavedHostBrowserFixture(dir); err != nil {
		t.Fatal(err)
	}
	store, err := readLANStore(filepath.Join(dir, "lan.json"))
	if err != nil {
		t.Fatal(err)
	}
	state := store.copy()
	if err := validateLANState(state); err != nil {
		t.Fatal(err)
	}
	if !state.DestinationPolicy.Strict() || len(state.Remotes) != 0 || len(state.Trust.Peers) != 0 {
		t.Fatal("fixture seeded a pair, grant or broad policy")
	}
	for _, name := range []string{"lan.json", "sobalink.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal("fixture state is not private")
		}
	}
	before, _ := os.ReadFile(store.path)
	if err := PrepareSavedHostBrowserFixture(dir); err == nil {
		t.Fatal("fixture replaced existing state")
	}
	after, _ := os.ReadFile(store.path)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected fixture rewrote state")
	}
	c, err := Open(context.Background(), Options{Directory: dir, Version: "fixture-test", SkipNetworkStart: true, NodeFactory: func(string, string) (NetworkBackend, error) {
		t.Error("fixture started a backend")
		return nil, errors.New("forbidden")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.lanFactory = func(*lanStore) (lanNetworkBackend, error) {
		t.Error("review started LAN")
		return nil, errors.New("forbidden")
	}
	first := savedHostReview(t, c)
	for range 3 {
		snapshot, err := c.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		lan := snapshot["lan"].(map[string]any)
		review := lan["savedStart"].(*LANStartReview)
		if review.Revision != first.Revision || review.PublicKey != state.Identity.PublicKey() || review.Relay != *state.Selection || review.Hostname != "saved-notebook" || len(review.Policy.Prefixes) != 1 || review.Policy.Prefixes[0] != "192.168.50.0/24" {
			t.Fatal("fixture substituted a review or changed scope")
		}
		if lan["relayReady"] != false || lan["pairingReady"] != false || review.PairedDevices != 0 || review.TrustedDevices != 0 || review.AutomaticReceivers != 0 || len(review.PendingStartup) != 0 || c.nodeCopy() != nil {
			t.Fatal("fixture claimed readiness or seeded authority")
		}
		raw, _ := json.Marshal(snapshot)
		if fixtureSnapshotLeaksPrivateMaterial(t, raw, state) {
			t.Fatal("fixture status leaked private key")
		}
	}
	after, _ = os.ReadFile(store.path)
	if !bytes.Equal(before, after) {
		t.Fatal("review changed private LAN state")
	}
}

func fixturePrivateValues(state lanState) []any {
	return []any{state.Identity.Key, state.Identity.PSK, state.RelayIdentity.Key, state.RelayIdentity.PrivateKeyPEM, string(state.RelayIdentity.PrivateKeyPEM)}
}
func fixtureSnapshotLeaksPrivateMaterial(t *testing.T, raw []byte, state lanState) bool {
	t.Helper()
	for _, value := range fixturePrivateValues(state) {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal("cannot encode fixture privacy sentinel")
		}
		if bytes.Contains(raw, encoded) {
			return true
		}
	}
	return false
}
func TestSavedHostFixturePrivacyCheckDetectsJSONEncodedSecrets(t *testing.T) {
	identity := lanlink.GenerateIdentity()
	state := lanState{Identity: identity, RelayIdentity: &lanlink.RelayIdentity{Key: lanlink.GenerateIdentity().Key, PrivateKeyPEM: []byte("-----BEGIN PRIVATE KEY-----\nfictional-fixture-secret\n-----END PRIVATE KEY-----\n")}}
	for index, value := range fixturePrivateValues(state) {
		raw, err := json.Marshal(map[string]any{"unexpected": value})
		if err != nil {
			t.Fatal("cannot encode injected privacy sentinel")
		}
		if !fixtureSnapshotLeaksPrivateMaterial(t, raw, state) {
			t.Fatalf("privacy assertion missed secret representation %d", index)
		}
	}
	raw, _ := json.Marshal(map[string]any{"publicKey": identity.PublicKey(), "configured": true})
	if fixtureSnapshotLeaksPrivateMaterial(t, raw, state) {
		t.Fatal("public identity was treated as private")
	}
}
