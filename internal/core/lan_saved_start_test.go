package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func savedHostCore(t *testing.T) *Core {
	t.Helper()
	c := openLANTestCore(t)
	if err := c.configureLAN(&LANSelection{Kind: "host", Address: "192.168.50.10:48443"}); err != nil {
		t.Fatal(err)
	}
	return c
}
func savedHostReview(t *testing.T, c *Core) *LANStartReview {
	t.Helper()
	view, ok := c.lanStatus()["savedStart"].(*LANStartReview)
	if !ok || view == nil || len(view.Revision) != 64 {
		t.Fatal("missing saved-host review")
	}
	return view
}
func assertSavedStartRejected(t *testing.T, c *Core, revision string) {
	t.Helper()
	before := privateRevision(c.profileCopy())
	store := c.lanStoreCopy()
	beforeLAN := privateRevision(store.copy())
	c.lanFactory = func(*lanStore) (lanNetworkBackend, error) {
		t.Fatal("rejected review constructed a network")
		return nil, nil
	}
	_, err := command(c, randomID(), "network.configure", map[string]any{"mode": "lan", "expectedLANStartRevision": revision})
	if err == nil || before != privateRevision(c.profileCopy()) || beforeLAN != privateRevision(store.copy()) {
		t.Fatal("stale review mutated saved state", err)
	}
}

func TestSavedLANStartReviewReadOnlyAndPrivate(t *testing.T) {
	c := savedHostCore(t)
	store := c.lanStoreCopy()
	before, _ := os.ReadFile(store.path)
	view := savedHostReview(t, c)
	if view.Relay != *store.copy().Selection || view.Policy.Mode != lanpolicy.TrustedRelay || view.Hostname != c.profileCopy().Settings.Hostname {
		t.Fatal("review differs from saved scope")
	}
	raw, _ := json.Marshal(view)
	state := store.copy()
	identityKey, _ := state.Identity.Key.MarshalText()
	relayKey, _ := state.RelayIdentity.Key.MarshalText()
	psk, _ := json.Marshal(state.Identity.PSK)
	for _, secret := range []string{string(identityKey), string(state.RelayIdentity.PrivateKeyPEM), string(relayKey), string(psk), c.dir, c.lanStartNonce} {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			t.Fatal("review leaked private material")
		}
	}
	for range 3 {
		if savedHostReview(t, c).Revision != view.Revision {
			t.Fatal("read-only review changed")
		}
	}
	after, _ := os.ReadFile(store.path)
	if !bytes.Equal(before, after) || c.nodeCopy() != nil {
		t.Fatal("review saved or started work")
	}
}

func TestSavedLANStartUsesExactExistingIdentityAndPolicy(t *testing.T) {
	c := savedHostCore(t)
	mustCommand(t, c, "lan.policy.set", map[string]any{"mode": lanpolicy.AllowedLANDestinations, "prefixes": []string{"192.168.50.0/24"}})
	store := c.lanStoreCopy()
	before := privateRevision(store.copy())
	view := savedHostReview(t, c)
	starts := 0
	c.lanFactory = func(s *lanStore) (lanNetworkBackend, error) {
		starts++
		if privateRevision(s.copy()) != before {
			t.Fatal("saved-start changed identity, pin, policy or pairs")
		}
		b, _ := testLANBackend()
		return b, nil
	}
	id := randomID()
	payload := map[string]any{"mode": "lan", "expectedLANStartRevision": view.Revision}
	if _, err := command(c, id, "network.configure", payload); err != nil {
		t.Fatal(err)
	}
	if _, err := command(c, id, "network.configure", payload); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || c.lanStatus()["savedStart"] != nil || privateRevision(store.copy()) != before {
		t.Fatal("repeat changed saved host or started twice")
	}
	assertSavedStartRejected(t, c, view.Revision)
}

func TestSavedLANStartRejectsAllEditingAndMalformedReviewPayloads(t *testing.T) {
	for _, field := range []string{"hostname", "lan", "lanPolicy", "rotateCertificate", "mixed", "directLAN", "other-mode", "empty", "null", "number", "empty-name", "null-host", "false-rotation"} {
		t.Run(field, func(t *testing.T) {
			c := savedHostCore(t)
			payload := map[string]any{"mode": "lan", "expectedLANStartRevision": savedHostReview(t, c).Revision}
			switch field {
			case "hostname":
				payload[field] = c.profileCopy().Settings.Hostname
			case "lan":
				payload[field] = LANSelection{Kind: "host", Address: "192.168.50.10:48443"}
			case "lanPolicy":
				payload[field] = lanpolicy.Config{Mode: lanpolicy.TrustedRelay}
			case "rotateCertificate":
				payload[field] = true
			case "mixed":
				payload[field] = MixedSelection{Backends: []string{"lan", "tailnet"}}
			case "directLAN":
				payload[field] = DirectLANSelection{Listen: "192.168.50.10:48444", Prefixes: []string{"192.168.50.0/24"}}
			case "other-mode":
				payload["mode"] = "tailnet"
			case "empty":
				payload["expectedLANStartRevision"] = ""
			case "null":
				payload["expectedLANStartRevision"] = nil
			case "number":
				payload["expectedLANStartRevision"] = 1
			case "empty-name":
				payload["hostname"] = ""
			case "null-host":
				payload["lan"] = nil
			case "false-rotation":
				payload["rotateCertificate"] = false
			}
			c.lanFactory = func(*lanStore) (lanNetworkBackend, error) {
				t.Fatal("malformed guarded request started")
				return nil, nil
			}
			before := privateRevision(c.profileCopy())
			_, err := command(c, randomID(), "network.configure", payload)
			if networkErrorCode(err) != "lan_saved_start_changed" || privateRevision(c.profileCopy()) != before {
				t.Fatal("edit or malformed token escaped guard", err)
			}
		})
	}
}

func TestSavedLANStartRejectsChangedAndRevertedPolicyAndProfile(t *testing.T) {
	for _, kind := range []string{"policy", "profile"} {
		t.Run(kind, func(t *testing.T) {
			c := savedHostCore(t)
			view := savedHostReview(t, c)
			if kind == "policy" {
				mustCommand(t, c, "lan.policy.set", map[string]any{"mode": lanpolicy.AllowedLANDestinations, "prefixes": []string{"192.168.50.0/24"}})
				assertSavedStartRejected(t, c, view.Revision)
				mustCommand(t, c, "lan.policy.set", map[string]any{"mode": lanpolicy.TrustedRelay, "prefixes": []string{}})
			} else {
				p := c.profileCopy()
				original := p.Settings.Hostname
				p.Settings.Hostname = "changed-fixture"
				if err := c.saveProfile(p); err != nil {
					t.Fatal(err)
				}
				c.mu.Lock()
				c.profile = p
				c.mu.Unlock()
				assertSavedStartRejected(t, c, view.Revision)
				p.Settings.Hostname = original
				if err := c.saveProfile(p); err != nil {
					t.Fatal(err)
				}
				c.mu.Lock()
				c.profile = p
				c.mu.Unlock()
			}
			assertSavedStartRejected(t, c, view.Revision)
			if savedHostReview(t, c).Revision == view.Revision {
				t.Fatal("reverted state resurrected old review")
			}
		})
	}
}

func TestSavedLANStartBindsWholeScope(t *testing.T) {
	for _, scope := range []string{"endpoint", "identity", "certificate", "pair-generation", "routes", "wan", "resource", "application", "startup", "proxies", "profile-binding", "process"} {
		t.Run(scope, func(t *testing.T) {
			c := savedHostCore(t)
			view := savedHostReview(t, c)
			store := c.lanStoreCopy()
			switch scope {
			case "endpoint":
				if err := c.configureLAN(&LANSelection{Kind: "host", Address: "192.168.50.10:48444"}); err != nil {
					t.Fatal(err)
				}
			case "identity":
				store.mu.Lock()
				store.state.Identity = lanlink.GenerateIdentity()
				store.mu.Unlock()
			case "certificate":
				if err := c.configureLANWithOptions(&LANSelection{Kind: "host", Address: "192.168.50.10:48443"}, true); err != nil {
					t.Fatal(err)
				}
			case "pair-generation":
				store.mu.Lock()
				store.state.Trust.Peers = append(store.state.Trust.Peers, lanlink.Peer{Key: strings.Repeat("a", 64), Name: "fixture"})
				store.mu.Unlock()
			case "routes":
				store.mu.Lock()
				store.state.RouteCandidates = append(store.state.RouteCandidates, lanlink.RouteCandidate{})
				store.mu.Unlock()
			case "wan":
				mustCommand(t, c, "wan.candidates.set", map[string]any{"enabled": true, "advertiseIPv6": true})
			case "resource":
				c.mu.Lock()
				c.capacity.Resources["relayTLSConnections"] = capacity.Limited(32)
				c.mu.Unlock()
			case "application":
				c.mu.Lock()
				c.profile.Peers = append(c.profile.Peers, Trust{ID: strings.Repeat("a", 64), Network: "lan", Generation: 1, Autosave: true})
				c.mu.Unlock()
			case "startup":
				c.mu.Lock()
				c.startupPending["fixture"] = "approved"
				c.mu.Unlock()
			case "proxies":
				c.mu.Lock()
				c.savedProxyPending["fixture"] = "approved"
				c.mu.Unlock()
			case "profile-binding":
				c.dir = filepath.Join(c.dir, "different-profile")
			case "process":
				c.lanStartNonce = randomID()
			}
			assertSavedStartRejected(t, c, view.Revision)
		})
	}
}

func TestSavedLANStartRejectsUncertainStateAndOtherEngines(t *testing.T) {
	for _, kind := range []string{"recovery", "uncertain-profile", "uncertain-host", "other-network", "attempted-other", "running"} {
		t.Run(kind, func(t *testing.T) {
			c := savedHostCore(t)
			view := savedHostReview(t, c)
			store := c.lanStoreCopy()
			switch kind {
			case "recovery":
				store.requireRouteRecovery()
			case "uncertain-profile":
				c.atomicWrite = func(string, []byte) error { return config.ErrAtomicCommitted }
				if err := c.saveProfile(c.profileCopy()); !errors.Is(err, config.ErrAtomicCommitted) {
					t.Fatal(err)
				}
			case "uncertain-host":
				store.write = func(string, []byte) error { return config.ErrAtomicCommitted }
				if err := store.save(store.copy()); !errors.Is(err, config.ErrAtomicCommitted) {
					t.Fatal(err)
				}
			case "other-network":
				c.mu.Lock()
				c.profile.Settings.Network = "tailnet"
				c.mu.Unlock()
			case "attempted-other":
				c.mu.Lock()
				c.attemptedNetwork = "tailnet"
				c.mu.Unlock()
			case "running":
				b, _ := testLANBackend()
				c.mu.Lock()
				c.node = b
				c.mu.Unlock()
			}
			if c.lanStatus()["savedStart"] != nil {
				t.Fatal("unsafe state offered saved start")
			}
			assertSavedStartRejected(t, c, view.Revision)
		})
	}
}

func TestSavedLANStartQueuedAfterConfigurationChangeDoesNotStart(t *testing.T) {
	c := savedHostCore(t)
	view := savedHostReview(t, c)
	c.op.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := command(c, randomID(), "network.configure", map[string]any{"mode": "lan", "expectedLANStartRevision": view.Revision})
		done <- err
	}()
	// Model an edit already holding the authoritative operation lock. The
	// request may only compare its review once this complete edit releases it.
	if err := c.configureLANWithPolicy(nil, false, &lanpolicy.Config{Mode: lanpolicy.AllowedLANDestinations, Prefixes: []string{"192.168.50.0/24"}}); err != nil {
		c.op.Unlock()
		t.Fatal(err)
	}
	c.op.Unlock()
	if err := <-done; networkErrorCode(err) != "lan_saved_start_changed" || c.nodeCopy() != nil {
		t.Fatal("queued stale review started", err)
	}
}

func TestSavedLANStartCancelledRequestCannotStart(t *testing.T) {
	c := savedHostCore(t)
	view := savedHostReview(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, _ := json.Marshal(map[string]any{"mode": "lan", "expectedLANStartRevision": view.Revision})
	_, err := c.Command(ctx, webui.Command{RequestID: randomID(), Name: "network.configure", Payload: raw})
	if !errors.Is(err, context.Canceled) || c.nodeCopy() != nil {
		t.Fatal("cancelled saved start executed", err)
	}
}

func TestSavedLANStartRejectsMissingStateAndAnotherProcess(t *testing.T) {
	missing := openLANTestCore(t)
	_, err := command(missing, randomID(), "network.configure", map[string]any{"mode": "lan", "expectedLANStartRevision": strings.Repeat("a", 64)})
	if networkErrorCode(err) != "lan_saved_start_changed" || missing.lanStoreCopy() != nil || missing.nodeCopy() != nil {
		t.Fatal("guard initialized missing saved state", err)
	}
	c := savedHostCore(t)
	view := savedHostReview(t, c)
	dir := c.dir
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Options{Directory: dir, Version: "test", SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertSavedStartRejected(t, reopened, view.Revision)
	if savedHostReview(t, reopened).Revision == view.Revision {
		t.Fatal("another process inherited review")
	}
}

func TestSavedLANStartCannotRepairAnExistingFailedHostEngine(t *testing.T) {
	c := savedHostCore(t)
	view := savedHostReview(t, c)
	b, _ := testLANBackend()
	relay := &trackedRelayClose{done: make(chan struct{})}
	b.start = func() (hostedLANRelay, error) { return relay, nil }
	c.lanFactory = func(*lanStore) (lanNetworkBackend, error) { return b, nil }
	mustCommand(t, c, "network.configure", map[string]any{"mode": "lan", "expectedLANStartRevision": view.Revision})
	close(relay.done)
	if status := c.lanStatus(); status["relayReady"] != false || status["savedStart"] != nil {
		t.Fatal("failed existing engine offered in-process saved restart")
	}
	assertSavedStartRejected(t, c, view.Revision)
}

func TestSavedLANStartWriteFailureDoesNotConstructEngine(t *testing.T) {
	for _, failure := range []error{errors.New("synthetic write failure"), config.ErrAtomicCommitted} {
		t.Run(failure.Error(), func(t *testing.T) {
			c := savedHostCore(t)
			view := savedHostReview(t, c)
			c.atomicWrite = func(string, []byte) error { return failure }
			c.lanFactory = func(*lanStore) (lanNetworkBackend, error) { t.Fatal("failed save started an engine"); return nil, nil }
			_, err := command(c, randomID(), "network.configure", map[string]any{"mode": "lan", "expectedLANStartRevision": view.Revision})
			if err == nil || c.nodeCopy() != nil {
				t.Fatal("failed save reported startup", err)
			}
			assertSavedStartRejected(t, c, view.Revision)
		})
	}
}
