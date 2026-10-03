package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// Replace a real file using the production writer, then inject the distinct
// post-commit outcome. This cannot pass by returning an error without writing.
func committedWriter(calls *atomic.Int64) func(string, []byte) error {
	return func(path string, data []byte) error {
		calls.Add(1)
		if err := config.AtomicWrite(path, data); err != nil {
			return err
		}
		return fmt.Errorf("%w: injected durability failure", config.ErrAtomicCommitted)
	}
}

func jsonEqual(a, b any) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && string(left) == string(right)
}

func assertPublishedProfile(t *testing.T, c *Core) {
	t.Helper()
	saved, err := ReadProfile(filepath.Join(c.dir, "sobalink.json"))
	if err != nil || !jsonEqual(cloneProfile(saved), c.profileCopy()) {
		t.Fatalf("saved/runtime profile differ: %+v / %+v / %v", saved, c.profileCopy(), err)
	}
}

func TestAtomicCommittedProfileMutationsReconcileAndPreserveError(t *testing.T) {
	for _, operation := range []string{"service.save", "service.delete", "group.save", "profile.import", "settings.update", "rustdesk.save"} {
		t.Run(operation, func(t *testing.T) {
			c := offlineDefinitionCore(t)
			spec := saveDefinitionFixture(t, c, definitionFixture("original", "tcp", "8080"))
			var payload any
			switch operation {
			case "service.save":
				payload = map[string]any{"configuration": definitionFixture("new-service", "tcp", "8081")}
			case "service.delete":
				payload = map[string]any{"id": spec.ID, "expectedRevision": serviceRevision(spec)}
			case "group.save":
				payload = map[string]any{"group": ServiceGroup{Name: "reviewed", ServiceIDs: []string{spec.ID}}}
			case "profile.import":
				bundle := DefinitionBundle{Version: 1, Services: []ServiceSpec{spec}, Groups: []ServiceGroup{{Name: "imported", ServiceIDs: []string{spec.ID}}}}
				preview := mustCommand(t, c, "profile.import.preview", map[string]any{"profile": bundle}).(map[string]any)
				payload = map[string]any{"profile": bundle, "expectedRevision": preview["revision"]}
			case "settings.update":
				payload = map[string]any{"locale": "ja", "theme": "dark"}
			case "rustdesk.save":
				review := rustDeskPreview(t, c, rustDeskFixture())
				payload = RustDeskSetupRequest{Configuration: rustDeskFixture(), ExpectedRevision: review.Revision}
			}
			before := c.profileCopy()
			var calls atomic.Int64
			c.atomicWrite = committedWriter(&calls)
			id := randomID()
			_, err := command(c, id, operation, payload)
			if !errors.Is(err, config.ErrAtomicCommitted) {
				t.Fatal("committed error lost", err)
			}
			assertPublishedProfile(t, c)
			if reflect.DeepEqual(before, c.profileCopy()) || len(c.active) != 0 {
				t.Fatal("committed mutation hidden or listener started")
			}
			_, err = command(c, id, operation, payload)
			if !errors.Is(err, config.ErrAtomicCommitted) || calls.Load() != 1 {
				t.Fatal("idempotent replay retried uncertain save", calls.Load(), err)
			}
		})
	}
}

func TestAtomicUncommittedSettingsDoNotPublish(t *testing.T) {
	c := offlineDefinitionCore(t)
	before := c.profileCopy()
	old, _ := os.ReadFile(filepath.Join(c.dir, "sobalink.json"))
	c.atomicWrite = func(string, []byte) error { return os.ErrPermission }
	_, err := command(c, randomID(), "settings.update", map[string]any{"locale": "ja"})
	now, _ := os.ReadFile(filepath.Join(c.dir, "sobalink.json"))
	if !errors.Is(err, os.ErrPermission) || !reflect.DeepEqual(before, c.profileCopy()) || string(now) != string(old) {
		t.Fatal("uncommitted failure published", err)
	}
}

func TestAtomicCommittedInitializeReportsPublishedMetadata(t *testing.T) {
	var calls atomic.Int64
	dir := t.TempDir()
	result, err := initializeProfile(context.Background(), Options{Directory: dir}, "reviewed-node", committedWriter(&calls))
	if !errors.Is(err, config.ErrAtomicCommitted) || result.State != "initialized" || result.Network != "none" {
		t.Fatal(result, err)
	}
	saved, err := ReadProfile(filepath.Join(dir, "sobalink.json"))
	if err != nil || saved.Settings.Hostname != result.Hostname {
		t.Fatal(saved, err)
	}
	result, err = initializeProfile(context.Background(), Options{Directory: dir}, "reviewed-node", committedWriter(&calls))
	if err != nil || result.State != "exists" || calls.Load() != 1 {
		t.Fatal("repeat initialization rewrote committed profile", result, calls.Load(), err)
	}
}

func TestAtomicCommittedStartupStoreAndRevocationDoNotRetry(t *testing.T) {
	c, spec := startupFixture(t)
	review := mustCommand(t, c, "startup.preview", map[string]any{"name": "example", "ids": []string{spec.ID}}).(StartupReview)
	var calls atomic.Int64
	c.atomicWrite = committedWriter(&calls)
	_, err := command(c, randomID(), "startup.save", map[string]any{"name": "example", "ids": []string{spec.ID}, "expectedRevision": review.Revision, "expectedStoreRevision": review.StoreRevision})
	if !errors.Is(err, config.ErrAtomicCommitted) || len(c.startup.Entries) != 1 || len(c.startupPending) != 0 || calls.Load() != 1 {
		t.Fatal("startup publication/retry", calls.Load(), err)
	}
	var saved startupStore
	if err := config.ReadJSON(filepath.Join(c.dir, "startup.json"), &saved); err != nil || !jsonEqual(saved, c.startup) {
		t.Fatal("startup disk/runtime differ", err)
	}
	c.startupPending["example"] = review.Revision
	_, err = command(c, randomID(), "startup.disable", map[string]any{"name": "example", "expectedStoreRevision": privateRevision(c.startup)})
	if !errors.Is(err, config.ErrAtomicCommitted) || c.startup.Entries[0].Enabled || len(c.startupPending) != 0 {
		t.Fatal("committed disable not effective", err)
	}
	count := calls.Load()
	err = c.revokeStartupPeer("peer-b")
	if !errors.Is(err, config.ErrAtomicCommitted) || calls.Load() != count+1 || !c.startupSuppressed || c.startup.Revocations["peer-b"] == "" {
		t.Fatal("uncertain revocation retried or hidden", calls.Load(), err)
	}
	var epochs map[string]string
	if err := config.ReadJSON(filepath.Join(c.dir, "startup-revocations.json"), &epochs); err != nil || !reflect.DeepEqual(epochs, c.startup.Revocations) {
		t.Fatal("revocation disk/runtime differ", err)
	}
}

func TestAtomicCommittedMessageHistoryReconcilesAppendAndCleanup(t *testing.T) {
	c := offlineDefinitionCore(t)
	var calls atomic.Int64
	c.atomicWrite = committedWriter(&calls)
	m := Message{ID: "old-message", PeerID: "peer-a", Direction: "incoming", Text: "retained", CreatedAt: time.Now().Add(-400 * 24 * time.Hour), Status: "received"}
	c.mu.Lock()
	err := c.appendMessageLocked(m)
	c.mu.Unlock()
	if !errors.Is(err, config.ErrAtomicCommitted) || len(c.messages) != 1 {
		t.Fatal("committed append hidden", err)
	}
	var history []Message
	if err := readMessageHistory(filepath.Join(c.dir, "messages.json"), c.limit("resources", "messageStorageBytes"), &history); err != nil || !jsonEqual(history, c.messages) {
		t.Fatal("message disk/runtime differ", err)
	}
	preview := mustCommand(t, c, "message.history.preview", map[string]any{}).(historyCleanup)
	if preview.Remove != 1 {
		t.Fatal("fixture did not select cleanup", preview)
	}
	_, err = command(c, randomID(), "message.history.cleanup", map[string]any{"expectedRevision": preview.Revision})
	if !errors.Is(err, config.ErrAtomicCommitted) || len(c.messages) != 0 || calls.Load() != 2 {
		t.Fatal("committed cleanup hidden/retried", calls.Load(), err)
	}
	if err := readMessageHistory(filepath.Join(c.dir, "messages.json"), c.limit("resources", "messageStorageBytes"), &history); err != nil || len(history) != 0 {
		t.Fatal("cleanup not published", history, err)
	}
}

func TestAtomicCommittedCapacityReconcilesAllAdmissionBudgets(t *testing.T) {
	c := offlineDefinitionCore(t)
	if _, err := c.ensureLANIdentity(); err != nil {
		t.Fatal(err)
	}
	p := c.capacityPolicy()
	p.Resources["lanStateBytes"] = capacity.Limited(4 << 20)
	p.Logical["fileBytes"] = capacity.Limited(3 << 20)
	preview := mustCommand(t, c, "policy.preview", map[string]any{"policy": p}).(map[string]any)
	var calls atomic.Int64
	c.atomicWrite = committedWriter(&calls)
	_, err := command(c, randomID(), "policy.apply", map[string]any{"policy": p, "expectedRevision": preview["revision"]})
	if !errors.Is(err, config.ErrAtomicCommitted) || !capacityJSONEqual(c.capacityPolicy(), p) || c.lan.currentLimits().bytes != 4<<20 || calls.Load() != 1 {
		t.Fatal("committed policy hidden or admission budgets diverged", calls.Load(), err)
	}
	peer := transfer.Peer{ID: "budget-peer", Generation: 1}
	if err := c.transfers.BindPeer(peer); err != nil {
		t.Fatal(err)
	}
	manifest := fileManifest("over-budget", "payload")
	manifest.Entries[0].Size = (3 << 20) + 1
	if _, err := c.transfers.Offer(peer, manifest); !errors.Is(err, transfer.ErrLimit) {
		t.Fatal("manager retained previous admission budget", err)
	}
	saved, err := readCapacityPolicy(c.dir)
	if err != nil || !capacityJSONEqual(saved, p) {
		t.Fatal("capacity disk/runtime differ", err)
	}
}

func TestAtomicCommittedReceiveSettingsReconcileManagerAndPause(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	var calls atomic.Int64
	p.b.atomicWrite = committedWriter(&calls)
	for _, enabled := range []bool{true, false} {
		_, err := command(p.b, randomID(), "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": enabled, "paused": true, "directory": t.TempDir()})
		peer, _ := p.b.trust("peer-a")
		policies := p.b.transfers.ReceivePolicies()
		expectedPolicies := 0
		if enabled {
			expectedPolicies = 1
		}
		if !errors.Is(err, config.ErrAtomicCommitted) || !peer.Paused || peer.Autosave != enabled || len(policies) != expectedPolicies {
			t.Fatal("committed settings/manager mismatch", peer, policies, err)
		}
		_, offerErr := p.b.transfers.Offer(transfer.Peer{ID: peer.ID, Generation: peer.Generation}, fileManifest("paused-offer", "payload"))
		if !errors.Is(offerErr, transfer.ErrPeerPaused) {
			t.Fatal("committed pause not effective", offerErr)
		}
		assertPublishedProfile(t, p.b)
	}
	if calls.Load() != 2 {
		t.Fatal("receive save retried", calls.Load())
	}
}

func TestAtomicCommittedMultiServiceStartDoesNotRollbackOrActivate(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	first := definitionFixture("first", "tcp", "8080")
	first.PeerIDs = []string{"peer-b"}
	second := first
	second.Name = "second"
	second.Ports = "8081"
	one := saveDefinitionFixture(t, p.a, first)
	two := saveDefinitionFixture(t, p.a, second)
	review := mustCommand(t, p.a, "service.selection", map[string]any{"ids": []string{one.ID, two.ID}}).(map[string]any)
	var calls atomic.Int64
	p.a.atomicWrite = committedWriter(&calls)
	_, err := command(p.a, randomID(), "services.start", map[string]any{"ids": []string{one.ID, two.ID}, "expectedRevision": review["revision"]})
	if !errors.Is(err, config.ErrAtomicCommitted) || calls.Load() != 1 || len(p.a.active) != 0 {
		t.Fatal("uncertain group save retried/rolled back/activated", calls.Load(), err)
	}
	assertPublishedProfile(t, p.a)
}

func TestAtomicCommittedSavedProxyDeletionClosesLiveSession(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	_, _, count := installProxyStarter(t, p.a)
	saved := saveProxyFixture(t, p.a, false, true)
	started := mustCommand(t, p.a, "proxy.saved.start", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision}).(map[string]any)
	var calls atomic.Int64
	p.a.atomicWrite = committedWriter(&calls)
	_, err := command(p.a, randomID(), "proxy.saved.delete", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision})
	if !errors.Is(err, config.ErrAtomicCommitted) || len(p.a.savedProxies.Entries) != 0 || p.a.proxies[started["id"].(string)] != nil || count.Load() != 1 || calls.Load() != 1 {
		t.Fatal("committed deletion retained session or retried", err)
	}
	var store savedProxyStore
	if err := config.ReadJSON(filepath.Join(p.a.dir, "saved-proxies.json"), &store); err != nil || len(store.Entries) != 0 {
		t.Fatal("private proxy deletion not published", err)
	}
}

func TestAtomicCommittedLANMetadataReconcilesWithoutRetry(t *testing.T) {
	c := offlineDefinitionCore(t)
	var calls atomic.Int64
	c.atomicWrite = committedWriter(&calls)
	store, err := c.ensureLANIdentity()
	if !errors.Is(err, config.ErrAtomicCommitted) || store == nil || store != c.lanStoreCopy() || calls.Load() != 1 {
		t.Fatal("committed identity forgotten", err)
	}
	var saved lanState
	if err := readBoundedPrivateJSON(filepath.Join(c.dir, "lan.json"), c.limit("resources", "lanStateBytes"), &saved); err != nil || !jsonEqual(cloneLANState(saved), store.copy()) {
		t.Fatal("LAN saved snapshot differs", err)
	}
	// A subsequent read must reuse the identity rather than issue a replacement.
	again, err := c.ensureLANIdentity()
	if err != nil || again != store || calls.Load() != 1 {
		t.Fatal("identity regenerated/retried", err)
	}
}

func TestAtomicMetadataDoesNotEnterExportsOrPeerResponses(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	if _, err := os.Stat(filepath.Join(p.b.dir, ".sobalink-atomic-v1", "owner.lock")); err != nil {
		t.Fatal("fixture lacks actual metadata", err)
	}
	export := mustCommand(t, p.b, "profile.export", map[string]any{})
	data, err := json.Marshal(export)
	if err != nil {
		t.Fatal(err)
	}
	responses := [][]byte{data}
	for _, path := range []string{"/v1/hello", "/.well-known/sobalink/services/v3"} {
		status, body := peerCall(t, p.na, p.nb, "GET", path, nil)
		if status != http.StatusOK {
			t.Fatal(path, status, string(body))
		}
		responses = append(responses, body)
	}
	for _, response := range responses {
		for _, private := range []string{".sobalink-atomic-v1", "owner.lock", "sobalink atomic persistence v1", p.b.dir} {
			if strings.Contains(string(response), private) {
				t.Fatal("owned metadata entered public JSON", private)
			}
		}
	}
}

func TestAtomicCommittedProfileRevocationStopsLivePermissionWithoutRewrite(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	spec := definitionFixture("live-share", "tcp", "8080")
	spec.PeerIDs = []string{"peer-a"}
	saved := saveDefinitionFixture(t, p.b, spec)
	review := mustCommand(t, p.b, "service.selection", map[string]any{"ids": []string{saved.ID}}).(map[string]any)
	mustCommand(t, p.b, "services.start", map[string]any{"ids": []string{saved.ID}, "expectedRevision": review["revision"]})
	var writes atomic.Int64
	p.b.atomicWrite = func(path string, data []byte) error {
		writes.Add(1)
		if err := config.AtomicWrite(path, data); err != nil {
			return err
		}
		if filepath.Base(path) == "sobalink.json" {
			return config.ErrAtomicCommitted
		}
		return nil
	}
	_, err := command(p.b, randomID(), "peer.trust", map[string]any{"peerId": "peer-a", "trusted": false})
	if !errors.Is(err, config.ErrAtomicCommitted) || writes.Load() != 2 || len(p.b.active) != 0 {
		t.Fatal("revocation retained live permission or rewrote profile", writes.Load(), err)
	}
	if _, ok := p.b.trust("peer-a"); ok {
		t.Fatal("committed revocation hidden")
	}
	assertPublishedProfile(t, p.b)
	if _, err := p.b.transfers.Offer(transfer.Peer{ID: "peer-a", Generation: 1}, fileManifest("revoked-offer", "payload")); !errors.Is(err, transfer.ErrPeerChanged) {
		t.Fatal("receiver was not revoked", err)
	}
}

func TestAtomicCommittedLANOutcomeRedactsOwnedMetadata(t *testing.T) {
	c := offlineDefinitionCore(t)
	c.atomicWrite = func(path string, data []byte) error {
		if err := config.AtomicWrite(path, data); err != nil {
			return err
		}
		return fmt.Errorf("%w: %s/.sobalink-atomic-v1: private-key-detail", config.ErrAtomicCommitted, c.dir)
	}
	store, err := c.ensureLANIdentity()
	if store == nil || !errors.Is(err, config.ErrAtomicCommitted) {
		t.Fatal("committed LAN state hidden", err)
	}
	for _, private := range []string{c.dir, ".sobalink-atomic-v1", "private-key-detail"} {
		if strings.Contains(err.Error(), private) {
			t.Fatal("LAN persistence exposed private detail to its transport callback", err)
		}
	}
}
