package core

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/connectionroute"
)

// Source-only fixtures: no Open, transfer manager, backend, listener, or network
// owner is constructed. Injected writers model per-file outcomes; they are not
// evidence of OS durability or successful installed-binary execution.
func terminalReconcileFixture(t *testing.T) (*Core, []string) {
	t.Helper()
	state, now := pairRecordFixture(t)
	state = pairRecordV4(t, state, now, true)
	c, _ := pairRecordStoreFixture(t, state)
	c.dir = t.TempDir()
	raw := state.Peers[0].Key
	implicit := mixedID("direct-lan", raw)
	public := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	logical, err := connectionroute.StablePeerID(public)
	if err != nil {
		t.Fatal(err)
	}
	mixed := mixedState{Version: 1, Seed: hex.EncodeToString(bytes.Repeat([]byte{8}, ed25519.SeedSize)), Selection: MixedSelection{Backends: []string{"direct-lan", "lan"}}, Bindings: []connectionroute.Binding{{PeerID: logical, PublicKey: public, Identities: []connectionroute.TransportIdentity{{Backend: "direct-lan", ID: raw}, {Backend: "lan", ID: "synthetic-lan-peer"}}}}}
	data, err := json.Marshal(mixed)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(c.dir, "mixed.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ids := []string{raw, implicit, logical}
	c.profile = Profile{Version: 1, Settings: Settings{Network: "mixed", Locale: "en", Theme: "system", Hostname: "synthetic-node"}, Peers: []Trust{{ID: raw, Generation: 1, Network: "direct-lan"}, {ID: implicit, Generation: 2, Network: "mixed"}, {ID: logical, Generation: 3, Network: "mixed"}, {ID: "unrelated-peer", Generation: 4, Network: "tailnet"}}}
	c.startup = startupStore{Version: 1, Revocations: map[string]string{}, Entries: []StartupEntry{}}
	c.savedProxies = savedProxyStore{Version: 1}
	c.startupPending, c.savedProxyPending = map[string]string{}, map[string]string{}
	c.startupStates = map[string]string{}
	c.savedProxyRuns = map[string]savedProxyRun{}
	for i, id := range ids {
		name := []string{"raw", "implicit", "logical"}[i]
		c.startup.Entries = append(c.startup.Entries, StartupEntry{Name: name, Enabled: true, Revision: "saved-" + name, Services: []ServiceSpec{{PeerID: id, Direction: "forward"}}, PeerEpochs: map[string]string{id: ""}})
		c.savedProxies.Entries = append(c.savedProxies.Entries, savedProxy{Scope: ProxyScope{Name: name, Backend: "mixed", Targets: []ProxyTarget{{PeerID: id, Port: 444}}}, Revision: "proxy-" + name, Hostname: "synthetic-node", PeerEpochs: map[string]string{id: ""}})
		c.startupPending[name], c.savedProxyPending[name] = "saved-"+name, "proxy-"+name
	}
	c.atomicWrite = func(path string, data []byte) error { return os.WriteFile(path, data, 0600) }
	return c, ids
}

func TestTerminalAncillaryReconciliationCapturesAllIDsAndIsIdempotent(t *testing.T) {
	c, ids := terminalReconcileFixture(t)
	beforeEvidence := c.directLAN.copy()
	captured, err := c.terminalDirectLANDeniedIDs(ids[:1])
	if err != nil || len(captured) != len(ids) {
		t.Fatal(captured, err)
	}
	for _, id := range ids {
		if !containsPeerID(captured, id) {
			t.Fatal("raw, implicit, or saved mixed denial missing")
		}
	}
	writes := 0
	c.atomicWrite = func(path string, data []byte) error {
		writes++
		return os.WriteFile(path, data, 0600)
	}
	if err := c.reconcileTerminalDirectLAN(); err != nil {
		t.Fatal(err)
	}
	if c.transfers != nil || c.node != nil || c.contextControl != nil || c.managedCleanupPending {
		t.Fatal("reconciliation constructed an owner or left incomplete cleanup")
	}
	if !reflect.DeepEqual(beforeEvidence, c.directLAN.copy()) {
		t.Fatal("ancillary cleanup rewrote retained pair evidence")
	}
	if peers := c.profileCopy().Peers; len(peers) != 1 || peers[0].ID != "unrelated-peer" {
		t.Fatal("profile cleanup changed unrelated grants", peers)
	}
	mixed, err := c.readMixedState()
	if err != nil || len(mixed.Bindings) != 0 {
		t.Fatal("affected binding survived confirmed invalidation", err)
	}
	for _, id := range ids {
		if c.startup.Revocations[id] == "" || !c.managedPeerDenied(id) {
			t.Fatal("terminal authority or usable epoch survived")
		}
	}
	epochs := privateRevision(c.startup.Revocations)
	completedWrites := writes
	if err := c.reconcileTerminalDirectLAN(); err != nil {
		t.Fatal(err)
	}
	if writes != completedWrites || epochs != privateRevision(c.startup.Revocations) {
		t.Fatal("successful repeated reopen rewrote settings or rotated epochs")
	}
}

func TestTerminalCleanupRetainsBindingOnFailedSyntheticJournalAndRetriesSameEpoch(t *testing.T) {
	c, ids := terminalReconcileFixture(t)
	failed := false
	c.atomicWrite = func(path string, data []byte) error {
		base := filepath.Base(path)
		if base == "startup-revocations.json" {
			var epochs map[string]string
			if err := json.Unmarshal(data, &epochs); err != nil {
				return err
			}
			if epochs[ids[2]] != "" {
				failed = true
				return config.ErrAtomicBusy
			}
		}
		if base == "startup.json" && failed {
			return config.ErrAtomicRecovery
		}
		return os.WriteFile(path, data, 0600)
	}
	err := c.reconcileTerminalDirectLAN()
	if err == nil || !errors.Is(err, config.ErrAtomicBusy) || !errors.Is(err, config.ErrAtomicRecovery) || !c.managedCleanupPending || !errors.Is(c.managedCleanupError, config.ErrAtomicBusy) || !errors.Is(c.managedCleanupError, config.ErrAtomicRecovery) {
		t.Fatal("lost independent publication failures", err)
	}
	mixed, err := c.readMixedState()
	if err != nil || len(mixed.Bindings) != 1 {
		t.Fatal("binding erased before its synthetic epoch became durable", err)
	}
	if c.startupPending == nil || len(c.startupPending) != 0 || len(c.savedProxyPending) != 0 || !c.startupSuppressed {
		t.Fatal("failed cleanup retained runnable saved work")
	}
	for _, id := range ids {
		if !c.managedPeerDenied(id) {
			t.Fatal("failed ancillary write restored in-memory authority")
		}
	}
	epochs := privateRevision(c.startup.Revocations)
	c.atomicWrite = func(path string, data []byte) error { return os.WriteFile(path, data, 0600) }
	if err = c.reconcileTerminalDirectLAN(); err != nil {
		t.Fatal(err)
	}
	if epochs != privateRevision(c.startup.Revocations) || c.managedCleanupPending {
		t.Fatal("retry rotated stale epochs or remained incomplete")
	}
	mixed, err = c.readMixedState()
	if err != nil || len(mixed.Bindings) != 0 {
		t.Fatal("confirmed retry did not retire binding", err)
	}
}

func TestTerminalCleanupPreservesGeneralRecoveryAndRejectsOmittedEpochs(t *testing.T) {
	c, ids := terminalReconcileFixture(t)
	c.directLAN.recovery = true
	for i := range c.startup.Entries {
		c.startup.Entries[i].PeerEpochs = nil
		c.savedProxies.Entries[i].PeerEpochs = nil
	}
	// Target bytes are checked directly even before a denial cache is installed.
	for i := range c.startup.Entries {
		if c.startupApprovalValid(c.startup.Entries[i]) || c.savedProxyApprovalValid(c.savedProxies.Entries[i]) {
			t.Fatal("omitted epoch map bypassed retained terminal marker")
		}
	}
	if err := c.reconcileTerminalDirectLAN(); err != nil {
		t.Fatal(err)
	}
	if !c.directLAN.needsRecovery() {
		t.Fatal("ancillary success cleared unrelated evidence recovery")
	}
	for _, id := range ids {
		if _, ok := c.trust(id); ok {
			t.Fatal("terminal profile authority survived")
		}
	}
}

func TestTerminalCleanupUncertainProfileRetryDoesNotRotateAgain(t *testing.T) {
	c, ids := terminalReconcileFixture(t)
	c.atomicWrite = func(path string, data []byte) error {
		if err := os.WriteFile(path, data, 0600); err != nil {
			return err
		}
		if filepath.Base(path) == "sobalink.json" {
			return config.ErrAtomicCommitted
		}
		return nil
	}
	if err := c.reconcileTerminalDirectLAN(); !errors.Is(err, config.ErrAtomicCommitted) || !c.managedCleanupPending {
		t.Fatal("uncertain profile claimed ancillary completion", err)
	}
	epochs := privateRevision(c.startup.Revocations)
	profileWrites := 0
	c.atomicWrite = func(path string, data []byte) error {
		if filepath.Base(path) == "sobalink.json" {
			profileWrites++
		}
		return os.WriteFile(path, data, 0600)
	}
	// Use the captured IDs, including the synthetic identity whose binding has
	// already been removed. Exact re-publication must confirm prior uncertainty.
	if err := c.cleanupTerminalDirectLAN(ids); err != nil {
		t.Fatal(err)
	}
	if profileWrites != 1 || epochs != privateRevision(c.startup.Revocations) || c.managedCleanupPending {
		t.Fatal("uncertain cleanup was not exactly repaired")
	}
}

func TestTerminalReopenUsesJournalAfterBindingAndProfileCleanup(t *testing.T) {
	c, ids := terminalReconcileFixture(t)
	if err := c.writePrivateSettings("startup.json", c.startup); err != nil {
		t.Fatal(err)
	}
	// Full saved-proxy parser requires private credentials. Fixtures remain
	// entirely synthetic and are never returned by a public metadata view.
	for i := range c.savedProxies.Entries {
		c.savedProxies.Entries[i].Username = "synthetic-user"
		c.savedProxies.Entries[i].Password = "synthetic-password"
	}
	if err := c.writePrivateSettings("saved-proxies.json", c.savedProxies); err != nil {
		t.Fatal(err)
	}
	if err := c.reconcileTerminalDirectLAN(); err != nil {
		t.Fatal(err)
	}
	reopened := &Core{dir: c.dir, profile: c.profileCopy(), directLAN: c.directLAN}
	if err := reopened.loadStartupSettings(true, false); err != nil {
		t.Fatal(err)
	}
	writes := 0
	reopened.atomicWrite = func(string, []byte) error { writes++; return errors.New("unexpected repeat write") }
	if err := reopened.reconcileTerminalDirectLAN(); err != nil || writes != 0 {
		t.Fatal("completed cold reopen was not idempotent", err, writes)
	}
	for i := range reopened.startup.Entries {
		if reopened.startupApprovalValid(reopened.startup.Entries[i]) || reopened.savedProxyApprovalValid(reopened.savedProxies.Entries[i]) {
			t.Fatal("old synthetic approval recovered after its binding was retired")
		}
	}
	if reopened.startup.Revocations[ids[2]] == "" {
		t.Fatal("retired synthetic epoch was lost")
	}
}

func TestTerminalReconciliationReportsMalformedMixedStateWithoutConstruction(t *testing.T) {
	c, ids := terminalReconcileFixture(t)
	if err := os.WriteFile(filepath.Join(c.dir, "mixed.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	c.atomicWrite = func(string, []byte) error { t.Fatal("unexpected publication on failed target capture"); return nil }
	if _, err := c.terminalDirectLANDeniedIDs(ids[:1]); err == nil {
		t.Fatal("malformed mixed state was silently ignored")
	}
	if err := c.reconcileTerminalDirectLAN(); err == nil || !c.managedCleanupPending || !c.startupSuppressed {
		t.Fatal("failed reopen did not retain offline repair state", err)
	}
	if c.transfers != nil || c.node != nil || !c.managedPeerDenied(ids[0]) || !c.managedPeerDenied(ids[1]) {
		t.Fatal("unknown ancillary state restored authority")
	}
}
