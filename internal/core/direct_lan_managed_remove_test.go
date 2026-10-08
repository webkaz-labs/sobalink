package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
)

// These fixtures have no transport/application constructors. Only synthetic
// evidence and injected file writers are used; they do not prove live removal.
func managedRemovalFixture(t *testing.T) (*Core, *directLANStore, time.Time, string) {
	t.Helper()
	state, now := pairRecordFixture(t)
	c, s := pairRecordStoreFixture(t, state)
	c.dir = filepath.Dir(s.path)
	id := state.Peers[0].Key
	c.profile = Profile{Version: 1, Settings: Settings{Network: "direct-lan", Locale: "auto", Theme: "system", Hostname: "synthetic-node"}, Peers: []Trust{{ID: id, Network: "direct-lan", Generation: 1}}}
	c.startup = startupStore{Version: 1, Revocations: map[string]string{}}
	c.startupPending, c.savedProxyPending = map[string]string{}, map[string]string{}
	return c, s, now, id
}

func TestManagedRemovalPublicationOrdering(t *testing.T) {
	c, s, now, id := managedRemovalFixture(t)
	var operations []string
	write := func(path string, data []byte) error {
		name := filepath.Base(path)
		if path == s.path {
			var state directLANState
			if err := json.Unmarshal(data, &state); err != nil {
				t.Fatal(err)
			}
			if state.Metadata.Peers[0].PairRevocation == nil {
				name = "migration"
			} else {
				name = "terminal"
			}
		} else if s.state.Metadata.Peers[0].PairRevocation == nil {
			t.Fatal("ancillary write preceded terminal marker")
		}
		operations = append(operations, name)
		return os.WriteFile(path, data, 0600)
	}
	s.write, c.atomicWrite = write, write
	c.attemptedNetwork, c.attemptedHostname = "direct-lan", "synthetic-node"
	result, err := c.removeManagedDirectLANAtLocked(context.Background(), id, func() time.Time { return now })
	if err != nil || !result.MigrationDurable || !result.TerminalDurable || !result.AncillaryComplete || !result.OwnersJoined {
		t.Fatal(result, err)
	}
	if !reflect.DeepEqual(operations, []string{"migration", "terminal", "startup-revocations.json", "sobalink.json"}) {
		t.Fatal(operations)
	}
	if c.attemptedNetwork != "direct-lan" || !c.managedPeerDenied(id) || c.networkReady.Load() {
		t.Fatal("removal restored activation")
	}
	epoch := c.startup.Revocations[id]
	operations = nil
	again, err := c.removeManagedDirectLANAtLocked(context.Background(), id, func() time.Time { return now })
	if err != nil || !again.TerminalDurable || !again.AncillaryComplete || c.startup.Revocations[id] != epoch || !reflect.DeepEqual(operations, []string{"terminal"}) {
		t.Fatal(again, err, operations)
	}
}

func TestManagedRemovalPartialPublication(t *testing.T) {
	for _, failAt := range []int{1, 2, 3, 4} {
		for _, committed := range []bool{false, true} {
			t.Run(string(rune('0'+failAt))+map[bool]string{false: "-absent", true: "-uncertain"}[committed], func(t *testing.T) {
				c, s, now, id := managedRemovalFixture(t)
				sentinel := errors.New("synthetic publication failure")
				writes := 0
				failedPath := ""
				write := func(path string, data []byte) error {
					writes++
					if writes == failAt {
						failedPath = path
						if committed {
							if err := os.WriteFile(path, data, 0600); err != nil {
								return err
							}
							return errors.Join(config.ErrAtomicCommitted, sentinel)
						}
						return sentinel
					}
					return os.WriteFile(path, data, 0600)
				}
				s.write, c.atomicWrite = write, write
				result, err := c.removeManagedDirectLANAtLocked(context.Background(), id, func() time.Time { return now })
				wantPath := s.path
				wantWrites := failAt
				if failAt >= 3 {
					wantWrites = 4
					wantPath = filepath.Join(c.dir, "startup-revocations.json")
					if failAt == 3 && !committed {
						wantWrites = 5
					} // failed journal, fallback, profile
					if failAt == 4 {
						wantPath = filepath.Join(c.dir, "sobalink.json")
					}
				}
				if failedPath != wantPath || writes != wantWrites {
					t.Fatalf("wrong injected publication stage: path=%q writes=%d; want %q/%d", failedPath, writes, wantPath, wantWrites)
				}
				// The evidence/settings boundaries intentionally sanitize raw
				// writer causes while preserving contractual atomic outcomes.
				if failAt <= 2 {
					if !errors.Is(err, directlan.ErrRecovery) || errors.Is(err, sentinel) || networkErrorCode(err) != "" {
						t.Fatal("wrong evidence failure category", err)
					}
				} else if networkErrorCode(err) != "direct_lan_cleanup_required" {
					t.Fatal("wrong ancillary failure category", err)
				}
				if failAt == 3 && errors.Is(err, sentinel) {
					t.Fatal("settings boundary exposed raw cause", err)
				}
				if failAt == 4 && !errors.Is(err, sentinel) {
					t.Fatal("profile cleanup lost its supplied failure", err)
				}
				if errors.Is(err, config.ErrAtomicCommitted) != committed {
					t.Fatal("wrong atomic outcome", err)
				}
				want := managedRemovalResult{OwnersJoined: true}
				if failAt == 1 {
					want.Migration = pairRecordSaveResult{changed: committed, published: committed}
				} else {
					want.Migration = pairRecordSaveResult{changed: true, published: true, durable: true}
					want.Terminal = pairRecordSaveResult{changed: committed, published: committed}
					if failAt >= 3 {
						want.Terminal = pairRecordSaveResult{changed: true, published: true, durable: true}
					}
				}
				want.MigrationChanged, want.MigrationPublished, want.MigrationDurable = want.Migration.changed, want.Migration.published, want.Migration.durable
				want.TerminalChanged, want.TerminalPublished, want.TerminalDurable = want.Terminal.changed, want.Terminal.published, want.Terminal.durable
				if result != want {
					t.Fatal("wrong phase outcome", result, want)
				}
				markerPublished := failAt > 2 || failAt == 2 && committed
				if (s.state.Metadata.Peers[0].PairRevocation != nil) != markerPublished || s.recovery != (failAt <= 2) || c.managedCleanupPending != (failAt >= 3) || !c.managedPeerDenied(id) || c.networkReady.Load() || !c.startupSuppressed || c.networkState != "error" {
					t.Fatal("wrong retained publication/denial/recovery state", result, err)
				}
				wantCode := "direct_lan_removal_unconfirmed"
				if failAt >= 3 {
					wantCode = "direct_lan_cleanup_required"
				}
				if c.networkErrorCode != wantCode {
					t.Fatal("wrong public removal status", c.networkErrorCode)
				}
				// A fresh explicit reduction retry does not clear general store recovery.
				oldRecovery := s.recovery
				writeOK := func(path string, data []byte) error { return os.WriteFile(path, data, 0600) }
				s.write, c.atomicWrite = writeOK, writeOK
				retry, err := c.removeManagedDirectLANAtLocked(context.Background(), id, func() time.Time { return now })
				if err != nil || !retry.TerminalDurable || !retry.AncillaryComplete {
					t.Fatal(retry, err)
				}
				if oldRecovery && !s.recovery {
					t.Fatal("cleanup cleared general recovery")
				}
			})
		}
	}
}

func TestManagedRemovalStoppedOwnerAdmission(t *testing.T) {
	c, s, now, id := managedRemovalFixture(t)
	c.attemptedNetwork = "direct-lan"
	in := pairRecordInputs{operation: pairRecordMigrate}
	if _, err := c.capturePairRecordAdmissionLocked(context.Background(), s, c.lanStartNonce, in, now); err == nil {
		t.Fatal("general admission widened")
	}
	o := &managedRemovalOwner{core: c, store: s, process: c.lanStartNonce, attemptedNetwork: "direct-lan", joined: true}
	if _, err := c.capturePairRecordRemovalAdmissionLocked(context.Background(), s, c.lanStartNonce, in, now, o); err == nil {
		t.Fatal("uninstalled removal owner accepted")
	}
	c.managedRemoval = o
	if _, err := c.capturePairRecordRemovalAdmissionLocked(context.Background(), s, c.lanStartNonce, in, now, o); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(){func() { o.joined = false }, func() { o.process = "stale" }, func() { o.store = &directLANStore{} }, func() { o.attemptedNetwork = "mixed" }} {
		saved := *o
		mutate()
		if _, err := c.capturePairRecordRemovalAdmissionLocked(context.Background(), s, c.lanStartNonce, in, now, o); err == nil {
			t.Fatal("stale stopped owner accepted")
		}
		*o = saved
	}
	if c.managedPeerDenied(id) {
		t.Fatal("admission itself minted denial")
	}
}

func TestManagedRemovalRetainsFailedCloseOwner(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		c, s, now, id := managedRemovalFixture(t)
		failure := errors.New("synthetic prior close failure")
		child := &directLANBackend{store: s, closed: true, closeErr: failure}
		child.closeOnce.Do(func() {}) // inert representation of a previously failed join
		var owner NetworkBackend = child
		if mixed {
			owner = &mixedBackend{nodes: map[string]NetworkBackend{"direct-lan": child}}
			c.attemptedNetwork = "mixed"
		} else {
			c.attemptedNetwork = "direct-lan"
		}
		c.node = owner
		s.write = func(string, []byte) error { t.Fatal("failed join published marker"); return nil }
		for attempt := 0; attempt < 2; attempt++ {
			result, err := c.removeManagedDirectLANAtLocked(context.Background(), id, func() time.Time { return now })
			if !errors.Is(err, failure) || result.OwnersJoined || result.TerminalDurable || c.node != owner || !c.managedPeerDenied(id) {
				t.Fatal(result, err)
			}
			if err := c.startNetwork(context.Background()); err == nil {
				t.Fatal("failed-close owner allowed reconnect success")
			}
		}
	}
}

func TestManagedLocalReviewRemovalNoLegacyFallback(t *testing.T) {
	c, s, now, id := managedRemovalFixture(t)
	s.state.Metadata.Peers[0].PairContext = nil
	s.state.Metadata.Peers[0].EndpointState = nil
	s.state.Metadata.Peers[0].ContextConfirmed = false
	raw, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err = os.WriteFile(s.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s.fileDigest = directLANFileDigest(raw)
	before := cloneDirectLANState(s.state)
	write := func(path string, data []byte) error { return os.WriteFile(path, data, 0600) }
	s.write, c.atomicWrite = write, write
	result, err := c.removeManagedDirectLANAtLocked(context.Background(), id, func() time.Time { return now })
	if err != nil || !result.TerminalDurable || !result.AncillaryComplete {
		t.Fatal(result, err)
	}
	terminal := s.state.Metadata.Peers[0]
	if terminal.PairRevocation.Kind != "local-record" || terminal.PairRevocation.PairBinding != "" {
		t.Fatal("not a local review denial")
	}
	terminal.PairRevocation = nil
	if !reflect.DeepEqual(terminal, before.Metadata.Peers[0]) {
		t.Fatal("local transcript changed")
	}
	projection, err := projectManagedFixedEndpoint(s.state)
	if err != nil || len(projection.Peers) != 0 || len(projection.PairContexts) != 0 || len(projection.DeniedPeerKeys) != 1 || projection.DeniedPeerKeys[0] != id {
		t.Fatal("local denial fell back to legacy", projection, err)
	}
	if _, err = s.runtimeConfig(); err == nil {
		t.Fatal("phase C activation guard opened")
	}
}
