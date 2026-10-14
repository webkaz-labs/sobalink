package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// Hold the production command lock across each deterministic scenario so the
// maintenance timer cannot dispatch queued work. Commands still enter the real
// command handlers; owned Open/reopen and all successful writes are native.
type peerTrustEpochFixture struct {
	t    *testing.T
	c    *Core
	held bool
}

func newPeerTrustEpochFixture(t *testing.T) *peerTrustEpochFixture {
	t.Helper()
	c, err := openOwnedTestCore(t, context.Background(), Options{Directory: t.TempDir(), SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	f := &peerTrustEpochFixture{t: t, c: c}
	f.hold()
	p := c.profileCopy()
	p.Settings.Network = "tailnet"
	if err := c.saveProfile(p); err != nil {
		t.Fatal(err)
	}
	c.profile = p
	f.installNode()
	return f
}

func (f *peerTrustEpochFixture) hold() {
	f.c.op.Lock()
	f.held = true
	f.t.Cleanup(f.release)
}

func (f *peerTrustEpochFixture) release() {
	if f.held {
		f.c.op.Unlock()
		f.held = false
	}
}

func (f *peerTrustEpochFixture) installNode() {
	f.c.node = &pipeNode{hub: &pipeNetwork{listeners: map[netip.AddrPort]*pipeListener{}}, state: identity.State{
		SelfID: "peer-a", Backend: "Running", Snapshot: policy.Snapshot{Running: true, Peers: []policy.Peer{
			{ID: "peer-b", DNSName: "peer-b.invalid", IPs: []netip.Addr{netip.MustParseAddr("100.64.0.2")}},
			{ID: "peer-c", DNSName: "peer-c.invalid", IPs: []netip.Addr{netip.MustParseAddr("100.64.0.3")}},
		}},
	}}
}

func (f *peerTrustEpochFixture) reopen() {
	f.t.Helper()
	old := f.c
	f.release()
	if err := old.Close(); err != nil {
		f.t.Fatal(err)
	}
	c, err := openOwnedTestCore(f.t, context.Background(), Options{Directory: old.dir, LifecycleLock: old.resourceLock, SkipNetworkStart: true})
	if err != nil {
		f.t.Fatal(err)
	}
	f.c = c
	f.hold()
	f.installNode()
	if c.durableProfileError != "" {
		f.t.Fatal("owned reopen did not certify durable stores", c.durableProfileError)
	}
}

func (f *peerTrustEpochFixture) command(name string, payload any) (any, error) {
	f.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	id := randomID()
	if name == "message.queue.prepare" {
		id = newTextRequestID(time.Now())
	}
	cmd := webui.Command{RequestID: id, Name: name, Payload: raw}
	if strings.HasPrefix(name, "message.queue.") {
		// Durable text has its own short-lock dispatch before the generic handler.
		return f.c.Command(context.Background(), cmd)
	}
	return f.c.command(context.Background(), cmd)
}

func (f *peerTrustEpochFixture) must(name string, payload any) any {
	f.t.Helper()
	value, err := f.command(name, payload)
	if err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
	return value
}

func (f *peerTrustEpochFixture) approve(peer string) Trust {
	f.t.Helper()
	f.must("peer.trust", map[string]any{"peerId": peer, "trusted": true})
	trust, ok := f.c.trust(peer)
	if !ok || trust.RevocationEpoch == "" {
		f.t.Fatal("explicit approval did not establish a nonempty epoch")
	}
	return trust
}

func (f *peerTrustEpochFixture) journal() map[string]string {
	f.t.Helper()
	var saved map[string]string
	if err := readBoundedPrivateJSON(filepath.Join(f.c.dir, "startup-revocations.json"), f.c.limit("resources", "profileBytes"), &saved); err != nil {
		f.t.Fatal(err)
	}
	return saved
}

func (f *peerTrustEpochFixture) prepare() (any, error) {
	return f.command("message.queue.prepare", map[string]any{"peerId": "peer-b", "text": "synthetic first-approval text", "processId": resourceCatalogProcessToken(f.c.resourceNonce)})
}

func (f *peerTrustEpochFixture) assertDenied(peer string) {
	f.t.Helper()
	if _, ok := f.c.trust(peer); ok {
		f.t.Fatal("failed or revoked approval published live Trust")
	}
	if _, err := f.c.transfers.Offer(transfer.Peer{ID: peer, Generation: f.c.trustGeneration}, fileManifest("denied-epoch", "synthetic")); !errors.Is(err, transfer.ErrUnknownPeer) && !errors.Is(err, transfer.ErrPeerChanged) {
		f.t.Fatal("failed approval retained a live transfer binding", err)
	}
	if peer == "peer-b" {
		if _, err := f.prepare(); networkErrorCode(err) != "message_permission" {
			f.t.Fatal("failed approval authorized text preparation", err)
		}
	}
}

func (f *peerTrustEpochFixture) queuedTrust(id string) (Trust, error) {
	f.t.Helper()
	c := f.c
	c.mu.Lock()
	defer c.mu.Unlock()
	row := c.messageStore.state.Outgoing[id]
	if row == nil {
		f.t.Fatal("restart lost the original queued locator")
	}
	selected := &messageQueueSelection{id: id, identity: row.Identity, senderID: row.SenderID, trust: row.Trust, backend: c.node, allowSend: true}
	return row.Trust, c.checkMessageQueueSelectionLocked(row.Trust.ID, selected, c.node)
}

func TestInitialPeerTrustEpochPrepareRestartAndReapproval(t *testing.T) {
	f := newPeerTrustEpochFixture(t)
	if len(f.c.profile.Peers) != 0 || len(f.c.startup.Revocations) != 0 {
		t.Fatal("fixture did not begin without approvals or epochs")
	}
	if _, err := os.Stat(filepath.Join(f.c.dir, "startup-revocations.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("first approval was accidentally seeded", err)
	}
	f.assertDenied("peer-b")
	approved := f.approve("peer-b")
	if f.journal()[approved.ID] != approved.RevocationEpoch || !strings.HasPrefix(approved.RevocationEpoch, "0000000000000001:") {
		t.Fatal("initial exact epoch was not persisted with the nonreuse counter")
	}
	assertPublishedProfile(t, f.c)
	f.c.atomicWrite = func(string, []byte) error {
		t.Fatal("repeat approval rewrote current authority")
		return os.ErrPermission
	}
	if repeated := f.approve("peer-b"); repeated != approved {
		t.Fatal("repeat approval changed current authority")
	}
	f.c.atomicWrite = nil
	review, err := f.prepare()
	if err != nil {
		t.Fatal("first approval did not permit text preparation", err)
	}
	r := review.(TextOutboxRecord)
	f.must("message.queue.commit", map[string]any{"outboxId": r.OutboxID, "processId": r.ProcessID, "reviewRevision": r.ReviewRevision, "confirm": true})
	f.reopen()
	current, ok := f.c.trust("peer-b")
	if !ok || current != approved || f.journal()["peer-b"] != approved.RevocationEpoch {
		t.Fatal("reopen changed the certified approval")
	}
	if trust, err := f.queuedTrust(r.OutboxID); err != nil || trust.RevocationEpoch != approved.RevocationEpoch {
		t.Fatal("reopen did not preserve the queued relationship", err)
	}
	f.must("peer.trust", map[string]any{"peerId": "peer-b", "trusted": false})
	rotated := f.journal()["peer-b"]
	if rotated <= approved.RevocationEpoch {
		t.Fatal("revocation did not advance the persistent epoch")
	}
	// With no saved peers, the process-local transfer generation can restart at
	// the same number. The distinct persistent epoch must still deny old work.
	f.reopen()
	f.assertDenied("peer-b")
	renewed := f.approve("peer-b")
	if renewed.RevocationEpoch != rotated || renewed.Generation != approved.Generation {
		t.Fatal("fixture did not distinguish a reused numeric generation from the new durable epoch")
	}
	for restart := 0; restart < 2; restart++ {
		if trust, err := f.queuedTrust(r.OutboxID); networkErrorCode(err) != "message_permission" || trust.RevocationEpoch != approved.RevocationEpoch {
			t.Fatal("old queued job gained new approval", err)
		}
		if _, err := f.prepare(); err != nil {
			t.Fatal("new approval did not permit a fresh review", err)
		}
		if restart == 0 {
			f.reopen()
		}
	}
}

func TestInitialPeerTrustEpochWriteFailuresAndExactRetry(t *testing.T) {
	for _, target := range []string{"startup-revocations.json", "sobalink.json"} {
		for _, committed := range []bool{false, true} {
			name := target + "/unpublished"
			if committed {
				name = target + "/committed"
			}
			t.Run(name, func(t *testing.T) {
				f := newPeerTrustEpochFixture(t)
				var writes []string
				f.c.atomicWrite = func(path string, b []byte) error {
					base := filepath.Base(path)
					writes = append(writes, base)
					if base == target {
						if !committed {
							return os.ErrPermission
						}
						if err := config.AtomicWrite(path, b); err != nil {
							return err
						}
						return config.ErrAtomicCommitted
					}
					return config.AtomicWrite(path, b)
				}
				var epoch string
				for attempt := 0; attempt < 2; attempt++ {
					_, err := f.command("peer.trust", map[string]any{"peerId": "peer-b", "trusted": true})
					if err == nil || errors.Is(err, config.ErrAtomicCommitted) != committed {
						t.Fatal("approval did not preserve the write outcome", err)
					}
					f.assertDenied("peer-b")
					if current := f.c.startup.Revocations["peer-b"]; current == "" || epoch != "" && current != epoch {
						t.Fatal("retry did not retain the exact attempted epoch")
					} else {
						epoch = current
					}
				}
				want := []string{"startup-revocations.json", "startup-revocations.json"}
				if target == "sobalink.json" {
					want = []string{"startup-revocations.json", "sobalink.json", "startup-revocations.json", "sobalink.json"}
				}
				if !reflect.DeepEqual(writes, want) {
					t.Fatal("profile publication preceded exact journal certification", writes)
				}
				before := f.c.trustGeneration
				f.c.atomicWrite = nil
				approved := f.approve("peer-b")
				if approved.RevocationEpoch != epoch || approved.Generation <= before || f.journal()["peer-b"] != epoch {
					t.Fatal("explicit retry changed epoch or reused an attempted transfer generation")
				}
				if _, err := f.prepare(); err != nil {
					t.Fatal("confirmed retry could not prepare text", err)
				}
			})
		}
	}
}

func TestInitialPeerTrustEpochUncertaintyReopenAndRevoke(t *testing.T) {
	for _, target := range []string{"startup-revocations.json", "sobalink.json"} {
		for _, revoke := range []bool{false, true} {
			name := target + "/reopen"
			if revoke {
				name = target + "/revoke-before-reopen"
			}
			t.Run(name, func(t *testing.T) {
				f := newPeerTrustEpochFixture(t)
				f.c.atomicWrite = func(path string, b []byte) error {
					if err := config.AtomicWrite(path, b); err != nil {
						return err
					}
					if filepath.Base(path) == target {
						return config.ErrAtomicCommitted
					}
					return nil
				}
				if _, err := f.command("peer.trust", map[string]any{"peerId": "peer-b", "trusted": true}); !errors.Is(err, config.ErrAtomicCommitted) {
					t.Fatal(err)
				}
				f.assertDenied("peer-b")
				epoch := f.journal()["peer-b"]
				f.c.atomicWrite = nil
				if revoke {
					f.must("peer.trust", map[string]any{"peerId": "peer-b", "trusted": false})
					if f.journal()["peer-b"] <= epoch {
						t.Fatal("revoke skipped an uncertain unpublished runtime grant")
					}
				}
				f.reopen()
				if target == "sobalink.json" && !revoke {
					trust, ok := f.c.trust("peer-b")
					if !ok || trust.RevocationEpoch != epoch {
						t.Fatal("owned reopen lost the explicitly approved published candidate")
					}
					if _, err := f.prepare(); err != nil {
						t.Fatal("certified reopened grant did not permit preparation", err)
					}
				} else {
					f.assertDenied("peer-b")
				}
			})
		}
	}
}

func TestInitialPeerTrustEpochUnrelatedApprovalDoesNotAdoptUncertainty(t *testing.T) {
	f := newPeerTrustEpochFixture(t)
	f.c.atomicWrite = func(path string, b []byte) error {
		if err := config.AtomicWrite(path, b); err != nil {
			return err
		}
		if filepath.Base(path) == "sobalink.json" {
			return config.ErrAtomicCommitted
		}
		return nil
	}
	if _, err := f.command("peer.trust", map[string]any{"peerId": "peer-b", "trusted": true}); !errors.Is(err, config.ErrAtomicCommitted) {
		t.Fatal(err)
	}
	epoch := f.journal()["peer-b"]
	f.c.atomicWrite = nil
	other := f.approve("peer-c")
	f.assertDenied("peer-b")
	if other.ID != "peer-c" || f.journal()["peer-b"] != epoch {
		t.Fatal("unrelated approval changed an uncertain epoch")
	}
	f.reopen()
	f.assertDenied("peer-b")
	if current, ok := f.c.trust("peer-c"); !ok || current != other {
		t.Fatal("unrelated confirmed approval did not survive reopen")
	}
}

func TestInitialPeerTrustEpochFailedRevocationCannotAuthorizeOldQueue(t *testing.T) {
	f := newPeerTrustEpochFixture(t)
	old := f.approve("peer-b")
	review, err := f.prepare()
	if err != nil {
		t.Fatal(err)
	}
	r := review.(TextOutboxRecord)
	f.must("message.queue.commit", map[string]any{"outboxId": r.OutboxID, "processId": r.ProcessID, "reviewRevision": r.ReviewRevision, "confirm": true})
	f.c.atomicWrite = func(string, []byte) error { return os.ErrPermission }
	if _, err := f.command("peer.trust", map[string]any{"peerId": "peer-b", "trusted": false}); networkErrorCode(err) != "startup_revocation_unconfirmed" {
		t.Fatal("failed revocation did not require repair", err)
	}
	deniedEpoch := f.c.startup.Revocations["peer-b"]
	if deniedEpoch <= old.RevocationEpoch || !f.c.startupSuppressed {
		t.Fatal("failed revocation lost its in-memory denial")
	}
	if _, err := f.command("peer.trust", map[string]any{"peerId": "peer-b", "trusted": true}); err == nil {
		t.Fatal("uncertified nonempty revocation authorized a new grant")
	}
	f.assertDenied("peer-b")
	f.c.atomicWrite = nil
	f.approve("peer-c")
	if f.journal()["peer-b"] != deniedEpoch || !f.c.startupSuppressed {
		t.Fatal("unrelated approval cleared failed-revocation suppression")
	}
	f.assertDenied("peer-b")
	renewed := f.approve("peer-b")
	if renewed.RevocationEpoch != deniedEpoch || !f.c.startupSuppressed {
		t.Fatal("explicit retry changed denial or restored suppressed startup")
	}
	if _, err := f.queuedTrust(r.OutboxID); networkErrorCode(err) != "message_permission" {
		t.Fatal("old job regained permission under recertified denial", err)
	}
	f.reopen()
	if _, err := f.queuedTrust(r.OutboxID); networkErrorCode(err) != "message_permission" {
		t.Fatal("reopen restored an old denied job", err)
	}
}

func TestInitialPeerTrustEpochDoesNotRefreshSeparateSavedApprovals(t *testing.T) {
	for _, failure := range []string{"none", "journal-unpublished", "journal-committed", "profile-committed"} {
		t.Run(failure, func(t *testing.T) {
			f := newPeerTrustEpochFixture(t)
			other := f.approve("peer-c")
			reviews := map[string]StartupReview{}
			for i, peer := range []string{"peer-b", "peer-c"} {
				spec := ServiceSpec{Name: "startup-" + peer, Backend: "tailnet", Direction: "forward", Network: "tcp", Ports: "443", LocalPort: 8443 + i, LoopbackHost: "127.0.0.1", Lifetime: "finite", TTLSeconds: 3600, PeerID: peer}
				spec = f.must("service.save", map[string]any{"configuration": spec}).(SavedServiceConfiguration).Configuration
				review := f.must("startup.preview", map[string]any{"name": spec.Name, "ids": []string{spec.ID}}).(StartupReview)
				reviews[peer] = review
				f.must("startup.save", map[string]any{"name": spec.Name, "ids": []string{spec.ID}, "expectedRevision": review.Revision, "expectedStoreRevision": review.StoreRevision})
				scope := ProxyScope{Name: "proxy-" + peer, Backend: "tailnet", LoopbackHost: "127.0.0.1", LocalPort: 1080 + i, Lifetime: "finite", TTLSeconds: 3600, Targets: []ProxyTarget{{PeerID: peer, Port: 443}}}
				proxy := f.must("proxy.preview", map[string]any{"scope": scope}).(proxyReview)
				f.must("proxy.save", map[string]any{"scope": proxy.Scope, "expectedRevision": proxy.Revision, "expectedStoreRevision": f.c.savedProxyView()["revision"], "username": "synthetic-user", "password": "synthetic-password", "startOnLaunch": true})
			}
			if reviews["peer-b"].PeerEpochs["peer-b"] != "" || reviews["peer-c"].PeerEpochs["peer-c"] != other.RevocationEpoch {
				t.Fatal("fresh-profile saved approvals did not exercise empty and current epochs")
			}
			var stored [][]byte
			for _, name := range []string{"startup.json", "saved-proxies.json"} {
				b, err := os.ReadFile(filepath.Join(f.c.dir, name))
				if err != nil {
					t.Fatal(err)
				}
				stored = append(stored, b)
			}
			if failure != "none" {
				f.c.atomicWrite = func(path string, b []byte) error {
					base := filepath.Base(path)
					if failure == "journal-unpublished" && base == "startup-revocations.json" {
						return os.ErrPermission
					}
					if err := config.AtomicWrite(path, b); err != nil {
						return err
					}
					if failure == "journal-committed" && base == "startup-revocations.json" || failure == "profile-committed" && base == "sobalink.json" {
						return config.ErrAtomicCommitted
					}
					return nil
				}
			}
			_, err := f.command("peer.trust", map[string]any{"peerId": "peer-b", "trusted": true})
			if (err != nil) != (failure != "none") {
				t.Fatal("unexpected first-approval outcome", err)
			}
			if failure != "none" {
				f.assertDenied("peer-b")
				f.c.atomicWrite = nil
				f.approve("peer-b")
			}
			assertScopes := func() {
				t.Helper()
				for _, entry := range f.c.startup.Entries {
					peer := startupEntryPeerIDs(entry)[0]
					if f.c.startupApprovalValid(entry) != (peer == "peer-c") || entry.PeerEpochs[peer] != reviews[peer].PeerEpochs[peer] {
						t.Fatal("first app approval silently refreshed or broadly revoked startup authority")
					}
				}
				for _, entry := range f.c.savedProxies.Entries {
					peer := savedProxyPeerIDs(entry)[0]
					if f.c.savedProxyApprovalValid(entry) != (peer == "peer-c") || entry.PeerEpochs[peer] != reviews[peer].PeerEpochs[peer] {
						t.Fatal("first app approval silently refreshed or broadly revoked proxy authority")
					}
				}
				for i, name := range []string{"startup.json", "saved-proxies.json"} {
					b, err := os.ReadFile(filepath.Join(f.c.dir, name))
					if err != nil || !reflect.DeepEqual(b, stored[i]) {
						t.Fatal("app approval rewrote a separately saved scope", name, err)
					}
				}
				if current, ok := f.c.trust("peer-c"); !ok || current != other {
					t.Fatal("unrelated current Trust changed")
				}
			}
			assertScopes()
			old := reviews["peer-b"]
			if _, err := f.command("startup.save", map[string]any{"name": old.Name, "ids": old.IDs, "expectedRevision": old.Revision, "expectedStoreRevision": old.StoreRevision}); networkErrorCode(err) != "startup_revision_conflict" {
				t.Fatal("old empty-epoch startup review was reusable", err)
			}
			for _, entry := range f.c.savedProxies.Entries {
				if entry.Scope.Name == "proxy-peer-b" {
					if _, err := f.command("proxy.saved.start", map[string]any{"name": entry.Scope.Name, "expectedRevision": entry.Revision}); networkErrorCode(err) != "proxy_saved_revision_conflict" {
						t.Fatal("old empty-epoch proxy could start without renewed review", err)
					}
				}
			}
			f.reopen()
			assertScopes()
		})
	}
}
