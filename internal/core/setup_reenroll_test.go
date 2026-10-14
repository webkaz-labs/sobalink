package core

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
)

func setupRevokedFixture(t *testing.T, basis string) (*Core, *lanStore, string) {
	t.Helper()
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	store := c.lanStoreCopy()
	key := lanlink.GenerateIdentity().PublicKey()
	next := store.copy()
	if err := rememberSetupTrust(&next, key, basis); err != nil {
		t.Fatal(err)
	}
	if err := store.save(next); err != nil {
		t.Fatal(err)
	}
	if err := c.recordSetupRevocation(key); err != nil {
		t.Fatal(err)
	}
	return c, store, key
}
func setupReenroll(t *testing.T, c *Core, key string, generation uint64) SetupReenrollView {
	t.Helper()
	return mustCommand(t, c, "setup.reenroll", SetupReenrollInput{PeerKey: key, ExpectedGeneration: generation, Consent: true}).(SetupReenrollView)
}
func TestSetupReenrollPreservesBasisAndRequiresNewBilateralSetup(t *testing.T) {
	for _, basis := range []string{"tofu", "full-sha256", "unknown"} {
		t.Run(basis, func(t *testing.T) {
			c, store, key := setupRevokedFixture(t, basis)
			before := store.copy()
			v := mustCommand(t, c, "setup.revoked", struct{}{}).(SetupRevokedView)
			if v.Kind != "sobalink-setup-revoked" || v.Version != 1 || len(v.Records) != 1 || v.Records[0] != (SetupRevokedRecord{key, 1, basis}) {
				t.Fatal("incorrect revoked-key view", v)
			}
			r := setupReenroll(t, c, key, 1)
			if r.Kind != "sobalink-setup-reenroll" || r.Version != 1 || r.PeerKey != key || r.Generation != 2 || r.Verification != basis || r.Paired || r.Trusted {
				t.Fatal("reenrollment granted authority or changed basis", r)
			}
			after := store.copy()
			if after.SetupPersistenceEpoch != before.SetupPersistenceEpoch+1 || after.SetupTrust[0].Revoked || after.SetupTrust[0].Generation != 2 || len(after.Trust.Peers) != 0 || len(after.Remotes) != 0 || len(c.profileCopy().Peers) != 0 || c.nodeCopy() != nil {
				t.Fatal("reenrollment changed permission or started an owner")
			}
			after.SetupTrust, after.SetupPersistenceEpoch = before.SetupTrust, before.SetupPersistenceEpoch
			if !reflect.DeepEqual(before, after) {
				t.Fatal("reenrollment changed unrelated LAN state")
			}
			loaded, err := readLANStore(store.path)
			if err != nil || loaded.copy().SetupTrust[0].Generation != 2 || loaded.copy().SetupPersistenceEpoch != 1 {
				t.Fatal("generation did not survive restart", err)
			}
			if len(c.revokedSetupKeys().Records) != 0 {
				t.Fatal("reenrolled record still presented as revoked")
			}
			if _, err := command(c, randomID(), "setup.reenroll", SetupReenrollInput{key, 1, true}); networkErrorCode(err) != "setup_generation_changed" {
				t.Fatal("stale review reused", err)
			}
			if err := c.recordSetupRevocation(key); err != nil {
				t.Fatal(err)
			}
			if store.copy().SetupTrust[0].Generation != 3 || store.copy().SetupPersistenceEpoch != 1 {
				t.Fatal("revocation changed backend epoch or reused generation")
			}
			setupReenroll(t, c, key, 3)
			if store.copy().SetupTrust[0].Generation != 4 || store.copy().SetupPersistenceEpoch != 2 {
				t.Fatal("repeat re-enrollment lost monotonicity")
			}
		})
	}
}
func TestSetupReenrollFencesEveryOldWholeSnapshotIncludingOmissions(t *testing.T) {
	c, store, key := setupRevokedFixture(t, "tofu")
	old := store.copy()
	oldWriter := store.persistAt(old.SetupPersistenceEpoch)
	setupReenroll(t, c, key, 1)
	peer := lanlink.Peer{Key: key, Name: "Synthetic fresh peer"}
	fresh := lanlink.Snapshot{Version: 1, Peers: []lanlink.Peer{peer}}
	remotes := []lanlink.RemotePeer{{Peer: peer, Address: tailcat.Addr("synthetic-role")}}
	writer := store.persistAt(store.copy().SetupPersistenceEpoch)
	if err := writer(fresh, remotes); err != nil {
		t.Fatal(err)
	}
	before := store.copy()
	for _, snapshot := range []lanlink.Snapshot{old.Trust, fresh, {Version: 1, Peers: []lanlink.Peer{}}} {
		if err := oldWriter(snapshot, nil); networkErrorCode(err) != "setup_generation_changed" {
			t.Fatal("old whole snapshot reached persistence", err)
		}
		if !reflect.DeepEqual(before, store.copy()) {
			t.Fatal("old snapshot erased or restored current authority")
		}
	}
	// Ordinary revocation must not retire an otherwise current backend writer.
	if err := c.recordSetupRevocation(key); err != nil {
		t.Fatal(err)
	}
	if err := writer(lanlink.Snapshot{Version: 1, Peers: []lanlink.Peer{}}, nil); err != nil {
		t.Fatal("current backend could not persist removal", err)
	}
	if store.copy().SetupTrust[0].Generation != 3 || store.copy().SetupPersistenceEpoch != 1 {
		t.Fatal("removal double-bumped revocation")
	}
}
func TestSetupReenrollRejectsMissingConsentStaleGenerationAndOwners(t *testing.T) {
	c, store, key := setupRevokedFixture(t, "tofu")
	base := store.copy()
	for _, input := range []SetupReenrollInput{{key, 1, false}, {key, 0, true}, {key, setupMaxGeneration + 1, true}, {"invalid", 1, true}, {key, 2, true}} {
		if _, err := command(c, randomID(), "setup.reenroll", input); err == nil {
			t.Fatal("invalid review accepted")
		}
		if !reflect.DeepEqual(base, store.copy()) {
			t.Fatal("invalid request mutated state")
		}
	}
	backend, _ := testLANBackend()
	c.mu.Lock()
	c.node = backend
	c.mu.Unlock()
	if _, err := command(c, randomID(), "setup.reenroll", SetupReenrollInput{key, 1, true}); networkErrorCode(err) != "setup_reenroll_offline_required" {
		t.Fatal("active node allowed reenroll", err)
	}
	c.mu.Lock()
	c.node = nil
	c.mu.Unlock()
	ctx, cancel := context.WithCancel(c.ctx)
	s := &setupSession{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	c.mu.Lock()
	c.setup = s
	c.mu.Unlock()
	for _, ended := range []bool{false, true} {
		if ended {
			cancel()
		}
		if _, err := command(c, randomID(), "setup.reenroll", SetupReenrollInput{key, 1, true}); networkErrorCode(err) != "setup_busy" {
			t.Fatal("live or unjoined session allowed reenroll", err)
		}
	}
	close(s.done)
	c.mu.Lock()
	c.setup = nil
	c.mu.Unlock()
	for _, component := range []string{"trust", "remote"} {
		store.mu.Lock()
		if component == "trust" {
			store.state.Trust.Peers = []lanlink.Peer{{Key: key, Name: "Synthetic pair"}}
		} else {
			store.state.Remotes = []lanlink.RemotePeer{{Peer: lanlink.Peer{Key: key, Name: "Synthetic pair"}}}
		}
		store.mu.Unlock()
		if _, err := command(c, randomID(), "setup.reenroll", SetupReenrollInput{key, 1, true}); networkErrorCode(err) != "setup_peer_still_paired" {
			t.Fatal("partial saved pair allowed reenroll", component, err)
		}
		store.mu.Lock()
		store.state = cloneLANState(base)
		store.mu.Unlock()
	}
	setupReenroll(t, c, key, 1)
}
func TestSetupReenrollLegacyGenerationAndSafeIntegerBoundary(t *testing.T) {
	c, store, key := setupRevokedFixture(t, "full-sha256")
	next := store.copy()
	next.SetupTrust[0].Generation = 0
	if err := store.save(next); err != nil {
		t.Fatal(err)
	}
	if c.revokedSetupKeys().Records[0].Generation != 1 {
		t.Fatal("legacy tombstone projected as never revoked")
	}
	setupReenroll(t, c, key, 1)
	for _, field := range []string{"key", "epoch"} {
		next = store.copy()
		next.SetupTrust[0].Revoked = true
		next.SetupTrust[0].Generation = 3
		if field == "key" {
			next.SetupTrust[0].Generation = setupMaxGeneration
		} else {
			next.SetupPersistenceEpoch = setupMaxGeneration
		}
		if err := store.save(next); err != nil {
			t.Fatal(err)
		}
		if _, err := command(c, randomID(), "setup.reenroll", SetupReenrollInput{key, next.SetupTrust[0].Generation, true}); networkErrorCode(err) != "setup_generation_exhausted" {
			t.Fatal("generation overflow allowed", err)
		}
		invalid := cloneLANState(next)
		if field == "key" {
			invalid.SetupTrust[0].Generation++
		} else {
			invalid.SetupPersistenceEpoch++
		}
		if validateSetupTrust(invalid) == nil {
			t.Fatal("unsafe JSON integer accepted")
		}
	}
}
func TestSetupReenrollAtomicFailuresNeverClaimSuccess(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unpublished", true: "published-uncertain"}[committed], func(t *testing.T) {
			c, store, key := setupRevokedFixture(t, "tofu")
			oldWriter := store.persistAt(store.copy().SetupPersistenceEpoch)
			store.write = func(path string, data []byte) error {
				if committed {
					if err := config.AtomicWrite(path, data); err != nil {
						return err
					}
					return config.ErrAtomicCommitted
				}
				return os.ErrPermission
			}
			v, err := command(c, randomID(), "setup.reenroll", SetupReenrollInput{key, 1, true})
			if err == nil || v.(SetupReenrollView).Kind != "" {
				t.Fatal("unconfirmed write claimed reset", err)
			}
			if errors.Is(err, config.ErrAtomicCommitted) != committed {
				t.Fatal("atomic outcome hidden", err)
			}
			state := store.copy()
			if state.SetupTrust[0].Revoked == committed || (state.SetupPersistenceEpoch == 1) != committed {
				t.Fatal("memory did not reflect published boundary")
			}
			if committed {
				if err := oldWriter(lanlink.Snapshot{Version: 1}, nil); networkErrorCode(err) != "setup_generation_changed" {
					t.Fatal("uncertain published reset allowed old writer", err)
				}
			}
		})
	}
}
func TestSetupReenrollOldV1AndV2SessionsCannotRegainAuthorityOrSuccess(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "v1", true: "v2"}[v2], func(t *testing.T) {
			var a, b *setupSession
			if v2 {
				a, b, _ = setupRequestTestSessions(t, "full-sha256")
			} else {
				a, b, _ = setupTestSessions(t)
			}
			c := openLANTestCore(t)
			store, err := c.ensureLANIdentity()
			if err != nil {
				t.Fatal(err)
			}
			a.core = c
			a.setupGenerations = setupGenerations(store.copy())
			a.peer = &b.local
			a.paired = true
			a.connected = true
			a.state = "connected"
			if err := c.recordSetupRevocation(b.local.Key); err != nil {
				t.Fatal(err)
			}
			setupReenroll(t, c, b.local.Key, 1)
			if err := a.verifySavedBasis(b.local.Key); networkErrorCode(err) != "setup_generation_changed" {
				t.Fatal("old session reclaimed same key", err)
			}
			v := a.snapshot()
			if v.Paired || v.Connected || v.Trusted || v.State == "connected" || v.ReasonCode != "setup_generation_changed" {
				t.Fatal("historical receipt revived after reenroll", v.State, v.ReasonCode)
			}
			if _, err := a.wireLocked(b.local.Expires.Add(-1)); err == nil {
				t.Fatal("stale session emitted old proofs")
			}
			fresh := &setupSession{core: c, local: a.local, setupGenerations: setupGenerations(store.copy())}
			if err := fresh.verifySavedBasis(b.local.Key); err != nil {
				t.Fatal("fresh session denied current generation", err)
			}
		})
	}
}

func TestSetupReenrollProductionBackendWritersCaptureWholeEpoch(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || strings.HasPrefix(upper, "TS_") && upper != "TS_NO_LOGS_NO_SUPPORT" {
			t.Setenv(name, "")
		}
	}
	c, store, key := setupRevokedFixture(t, "tofu")
	backend, err := c.newLANBackend(store)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	routeNode, closeRoute, err := c.routeControl()
	if err != nil {
		t.Fatal(err)
	}
	defer closeRoute()
	setupReenroll(t, c, key, 1)
	peer := lanlink.Peer{Key: key, Name: "Synthetic fresh epoch"}
	if err := store.persistAt(store.copy().SetupPersistenceEpoch)(lanlink.Snapshot{Version: 1, Peers: []lanlink.Peer{peer}}, []lanlink.RemotePeer{{Peer: peer, Address: tailcat.Addr("synthetic-role")}}); err != nil {
		t.Fatal(err)
	}
	before := store.copy()
	for _, writer := range []interface{ Revoke(string) error }{backend, routeNode} {
		if err := writer.Revoke(lanlink.GenerateIdentity().PublicKey()); networkErrorCode(err) != "setup_generation_changed" {
			t.Fatal("production stale empty snapshot was not fenced", err)
		}
		if !reflect.DeepEqual(before, store.copy()) {
			t.Fatal("production stale writer erased current pair")
		}
	}
}
