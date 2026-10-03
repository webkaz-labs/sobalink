package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func TestAtomicOutcomeJournalCommittedRevocationMustCloseTrust(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
	old, _ := p.b.trust("peer-a")
	var calls atomic.Int64
	p.b.atomicWrite = func(path string, data []byte) error {
		calls.Add(1)
		if err := config.AtomicWrite(path, data); err != nil {
			return err
		}
		if filepath.Base(path) == "startup-revocations.json" {
			return config.ErrAtomicCommitted
		}
		return nil
	}
	_, err := command(p.b, randomID(), "peer.trust", map[string]any{"peerId": "peer-a", "trusted": false})
	revocationWrites := calls.Load()
	_, trusted := p.b.trust("peer-a")
	_, offerErr := p.b.transfers.Offer(transfer.Peer{ID: "peer-a", Generation: old.Generation}, fileManifest("after-revoke", "payload"))
	status, _ := peerCall(t, p.na, p.nb, "POST", "/v1/messages", map[string]any{"id": "after-revoke-message", "text": "still allowed"})
	t.Logf("committed=%v revocationWrites=%d trust=%v policies=%d offerAllowed=%v messageHTTP=%d", errors.Is(err, config.ErrAtomicCommitted), revocationWrites, trusted, len(p.b.transfers.ReceivePolicies()), offerErr == nil, status)
	if !errors.Is(err, config.ErrAtomicCommitted) || trusted || offerErr == nil || status != 403 {
		t.Errorf("revocation left incoming authorization live")
	}
}
func TestAtomicOutcomeCommittedPrivateSettingsMustRedactPath(t *testing.T) {
	c := offlineDefinitionCore(t)
	c.atomicWrite = func(path string, b []byte) error {
		if err := config.AtomicWrite(path, b); err != nil {
			return err
		}
		return fmt.Errorf("%w: %w", config.ErrAtomicCommitted, &os.PathError{Op: "sync", Path: filepath.Join(c.dir, ".sobalink-atomic-v1"), Err: os.ErrPermission})
	}
	err := c.writePrivateSettings("startup.json", startupStore{Version: 1, Entries: []StartupEntry{}})
	t.Logf("typed=%v internalPathLeaked=%v namespaceLeaked=%v", errors.Is(err, config.ErrAtomicCommitted), strings.Contains(err.Error(), c.dir), strings.Contains(err.Error(), ".sobalink-atomic-v1"))
	if !errors.Is(err, config.ErrAtomicCommitted) || strings.Contains(err.Error(), c.dir) || strings.Contains(err.Error(), ".sobalink-atomic-v1") {
		t.Errorf("private settings returns raw internal persistence detail")
	}
	for _, sentinel := range []error{config.ErrAtomicBusy, config.ErrAtomicRecovery} {
		c.atomicWrite = func(string, []byte) error { return sentinel }
		got := c.writePrivateSettings("startup.json", startupStore{Version: 1, Entries: []StartupEntry{}})
		t.Logf("sentinel=%q typePreserved=%v", sentinel.Error(), errors.Is(got, sentinel))
		if !errors.Is(got, sentinel) {
			t.Errorf("private settings loses typed inventory/admission outcome")
		}
	}
}
func TestAtomicOutcomeCommittedIncomingMessageMustReportUncertainty(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	var calls atomic.Int64
	p.b.atomicWrite = committedWriter(&calls)
	payload := map[string]any{"id": "committed-incoming", "text": "retained message"}
	status, body := peerCall(t, p.na, p.nb, "POST", "/v1/messages", payload)
	p.b.mu.RLock()
	count := len(p.b.messages)
	p.b.mu.RUnlock()
	nextStatus, nextBody := peerCall(t, p.na, p.nb, "POST", "/v1/messages", payload)
	t.Logf("firstHTTP=%d firstBody=%s retainedMessages=%d replayHTTP=%d replayBody=%s writes=%d", status, body, count, nextStatus, nextBody, calls.Load())
	if count != 1 || strings.Contains(string(body), "message could not be saved") || nextStatus == 200 {
		t.Errorf("committed uncertainty reported as failed save then as durable receipt")
	}
}

func TestAtomicIncomingReplayReconcilesExactlyOncePerUncertainAttempt(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	var calls atomic.Int64
	p.b.atomicWrite = func(path string, data []byte) error {
		n := calls.Add(1)
		if n == 3 {
			return &os.PathError{Op: "write", Path: path, Err: os.ErrPermission}
		}
		if err := config.AtomicWrite(path, data); err != nil {
			return err
		}
		if n <= 2 {
			return fmt.Errorf("%w: %w", config.ErrAtomicCommitted, &os.PathError{Op: "sync", Path: path, Err: os.ErrPermission})
		}
		return nil
	}
	payload := map[string]any{"id": "uncertain-replay", "text": "retain only once"}
	for attempt, want := range []int{507, 507, 507, 200, 200} {
		status, body := peerCall(t, p.na, p.nb, "POST", "/v1/messages", payload)
		if status != want {
			t.Fatalf("attempt %d: %d %s", attempt, status, body)
		}
		var envelope map[string]string
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		if want == 507 {
			if len(envelope) != 2 || envelope["code"] != "message_history_unavailable" || !strings.Contains(envelope["error"], "message was received") || !strings.Contains(envelope["error"], "same message ID") || strings.Contains(envelope["error"], p.b.dir) || strings.Contains(envelope["error"], "message could not be saved") {
				t.Fatalf("untruthful/private envelope: %s", body)
			}
		} else if len(envelope) != 2 || envelope["id"] != payload["id"] || envelope["status"] != "received" {
			t.Fatalf("ACK changed: %s", body)
		}
		p.b.mu.RLock()
		count, uncertain := len(p.b.messages), p.b.messageHistoryUncertain
		p.b.mu.RUnlock()
		expectedWrites := int64(attempt + 1)
		if expectedWrites > 4 {
			expectedWrites = 4
		}
		if count != 1 || uncertain != (attempt < 3) || calls.Load() != expectedWrites {
			t.Fatalf("attempt %d: messages=%d uncertain=%v writes=%d", attempt, count, uncertain, calls.Load())
		}
	}
	var history []Message
	if err := config.ReadJSON(filepath.Join(p.b.dir, "messages.json"), &history); err != nil || len(history) != 1 {
		t.Fatal(history, err)
	}
	status, _ := peerCall(t, p.na, p.nb, "POST", "/v1/messages", map[string]string{"id": "uncertain-replay", "text": "changed"})
	if status != 409 || calls.Load() != 4 {
		t.Fatal("conflicting duplicate wrote history", status, calls.Load())
	}
}

func TestAtomicIncomingUnpublishedFailureRetainsNothingAndNormalDuplicatesSkipIO(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	var calls atomic.Int64
	p.b.atomicWrite = func(path string, data []byte) error {
		if calls.Add(1) == 1 {
			return os.ErrPermission
		}
		return config.AtomicWrite(path, data)
	}
	payload := map[string]any{"id": "not-yet-written", "text": "save later"}
	status, body := peerCall(t, p.na, p.nb, "POST", "/v1/messages", payload)
	if status != 507 || !strings.Contains(string(body), "message could not be saved") {
		t.Fatal(status, string(body))
	}
	p.b.mu.RLock()
	count, uncertain := len(p.b.messages), p.b.messageHistoryUncertain
	p.b.mu.RUnlock()
	if count != 0 || uncertain || calls.Load() != 1 {
		t.Fatal("unpublished failure retained a receipt", count, uncertain, calls.Load())
	}
	if _, err := os.Stat(filepath.Join(p.b.dir, "messages.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unpublished failure wrote history", err)
	}
	for i := 0; i < 2; i++ {
		status, _ := peerCall(t, p.na, p.nb, "POST", "/v1/messages", payload)
		if status != 200 {
			t.Fatal(status)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("normal duplicate caused IO", calls.Load())
	}
}

func TestAtomicHistoryCleanupSharesUncertaintyWithoutCountingReplayAsMessage(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	p.b.mu.Lock()
	for _, m := range []Message{
		{ID: "old", PeerID: "peer-a", Direction: "incoming", Text: "old", Status: "received", CreatedAt: time.Now().Add(-400 * 24 * time.Hour)},
		{ID: "recent", PeerID: "peer-a", Direction: "incoming", Text: "recent", Status: "received", CreatedAt: time.Now()},
	} {
		if err := p.b.appendMessageLocked(m); err != nil {
			p.b.mu.Unlock()
			t.Fatal(err)
		}
	}
	p.b.mu.Unlock()
	var calls atomic.Int64
	p.b.atomicWrite = committedWriter(&calls)
	preview := mustCommand(t, p.b, "message.history.preview", map[string]any{}).(historyCleanup)
	result, err := command(p.b, randomID(), "message.history.cleanup", map[string]any{"expectedRevision": preview.Revision})
	if !errors.Is(err, config.ErrAtomicCommitted) || result.(historyCleanup).Remove != 1 || calls.Load() != 1 {
		t.Fatal(result, err, calls.Load())
	}
	payload := map[string]string{"id": "recent", "text": "recent"}
	status, _ := peerCall(t, p.na, p.nb, "POST", "/v1/messages", payload)
	if status != 507 || calls.Load() != 2 {
		t.Fatal(status, calls.Load())
	}
	preview = mustCommand(t, p.b, "message.history.preview", map[string]any{}).(historyCleanup)
	if preview.Remove != 0 || preview.Retained != 1 {
		t.Fatal("replay overcounted cleanup", preview)
	}
	p.b.atomicWrite = func(string, []byte) error { calls.Add(1); return os.ErrPermission }
	_, err = command(p.b, randomID(), "message.history.cleanup", map[string]any{"expectedRevision": preview.Revision})
	p.b.mu.RLock()
	stillUncertain := p.b.messageHistoryUncertain
	p.b.mu.RUnlock()
	if !errors.Is(err, os.ErrPermission) || !stillUncertain || calls.Load() != 3 {
		t.Fatal("unpublished cleanup cleared uncertainty", err, stillUncertain, calls.Load())
	}
	p.b.atomicWrite = func(path string, data []byte) error { calls.Add(1); return config.AtomicWrite(path, data) }
	mustCommand(t, p.b, "message.history.cleanup", map[string]any{"expectedRevision": preview.Revision})
	status, _ = peerCall(t, p.na, p.nb, "POST", "/v1/messages", payload)
	p.b.mu.RLock()
	count, uncertain := len(p.b.messages), p.b.messageHistoryUncertain
	p.b.mu.RUnlock()
	if status != 200 || calls.Load() != 4 || count != 1 || uncertain {
		t.Fatal("successful cleanup did not reconcile", status, calls.Load(), count, uncertain)
	}
}

func TestAtomicMessageHistoryErrorsPreserveSentinelsAndRedactCauses(t *testing.T) {
	for _, sentinel := range []error{config.ErrAtomicCommitted, config.ErrAtomicBusy, config.ErrAtomicRecovery} {
		err := messageHistoryError(fmt.Errorf("%w: %w", sentinel, &os.PathError{Op: "sync", Path: "/private/secret/.sobalink-atomic-v1", Err: os.ErrPermission}), true)
		if !errors.Is(err, sentinel) || !errors.Is(err, config.ErrAtomicCommitted) || networkErrorCode(err) != "message_history_unavailable" || strings.Contains(err.Error(), "/private/secret") {
			t.Fatal(err)
		}
	}
}

func TestAtomicJournalUncertaintyCancelsLiveReceiveAndTrackedConnection(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
	mustCommand(t, p.b, "service.share", map[string]any{"name": "live-share", "network": "tcp", "ports": "8080", "localPort": 9000, "peerIds": []string{"peer-a"}, "ttlSeconds": 60})
	old, _ := p.b.trust("peer-a")
	peer := transfer.Peer{ID: old.ID, Generation: old.Generation}
	batch, err := p.b.transfers.Offer(peer, fileManifest("live-receive", "payload"))
	if err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		_, err := p.b.transfers.ReceiveFile(context.Background(), peer, batch.ID, "file", reader)
		done <- err
	}()
	if _, err := writer.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	// A live peer-server connection is tracked under exactly this peer identity.
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ps := p.b.peerServer
	ps.mu.Lock()
	ps.connections[server] = peer.ID
	ps.mu.Unlock()
	defer func() { ps.mu.Lock(); delete(ps.connections, server); ps.mu.Unlock() }()
	var writes atomic.Int64
	p.b.atomicWrite = func(path string, data []byte) error {
		writes.Add(1)
		if err := config.AtomicWrite(path, data); err != nil {
			return err
		}
		if filepath.Base(path) == "startup-revocations.json" {
			return config.ErrAtomicCommitted
		}
		return nil
	}
	id := randomID()
	_, err = command(p.b, id, "peer.trust", map[string]any{"peerId": peer.ID, "trusted": false})
	if !errors.Is(err, config.ErrAtomicCommitted) || networkErrorCode(err) != "startup_revocation_unconfirmed" || writes.Load() != 2 {
		t.Fatal("journal/profile outcome", writes.Load(), err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, transfer.ErrCancelled) {
			t.Fatal("receive did not cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("live receive remained blocked")
	}
	client.SetReadDeadline(time.Now().Add(time.Second))
	if n, err := client.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatal("tracked connection stayed live", n, err)
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("tracked connection was not closed")
	}
	if _, err := os.Stat(filepath.Join(batch.Destination, "folder", "note.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("revoked receive became visible", err)
	}
	p.b.mu.RLock()
	active, suppressed, pending, proxyPending := len(p.b.active), p.b.startupSuppressed, len(p.b.startupPending), len(p.b.savedProxyPending)
	p.b.mu.RUnlock()
	if active != 0 || !suppressed || pending != 0 || proxyPending != 0 || len(p.b.transfers.ReceivePolicies()) != 0 {
		t.Fatal("teardown incomplete", active, suppressed, pending, proxyPending)
	}
	_, err = command(p.b, id, "peer.trust", map[string]any{"peerId": peer.ID, "trusted": false})
	if !errors.Is(err, config.ErrAtomicCommitted) || writes.Load() != 2 {
		t.Fatal("command replay retried uncertain journal", err, writes.Load())
	}
}

func TestAtomicJournalAuthorityOnReopenSurvivesUnpublishedProfileFailureAndFreshApproval(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
	original := p.b.profileCopy()
	var writes atomic.Int64
	p.b.atomicWrite = func(path string, data []byte) error {
		writes.Add(1)
		if filepath.Base(path) == "sobalink.json" {
			return os.ErrPermission
		}
		if err := config.AtomicWrite(path, data); err != nil {
			return err
		}
		return config.ErrAtomicCommitted
	}
	_, err := command(p.b, randomID(), "peer.trust", map[string]any{"peerId": "peer-a", "trusted": false})
	if !errors.Is(err, config.ErrAtomicCommitted) || writes.Load() != 2 {
		t.Fatal(err, writes.Load())
	}
	if _, trusted := p.b.trust("peer-a"); trusted {
		t.Fatal("failed profile save restored runtime authority")
	}
	var disk Profile
	if err := config.ReadJSON(filepath.Join(p.b.dir, "sobalink.json"), &disk); err != nil || !jsonEqual(original, disk) {
		t.Fatal("fixture lost stale disk grant", disk, err)
	}
	if err := p.b.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Options{Directory: p.b.dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, trusted := reopened.trust("peer-a"); trusted || len(reopened.transfers.ReceivePolicies()) != 0 {
		t.Fatal("journal did not close stale authority on reopen")
	}
	old := original.Peers[0]
	if _, err := reopened.transfers.Offer(transfer.Peer{ID: old.ID, Generation: old.Generation}, fileManifest("stale-reopen", "payload")); err == nil {
		t.Fatal("old approval was rebound")
	}
	reopened.mu.Lock()
	reopened.node = p.nb
	reopened.mu.Unlock()
	reopened.networkReady.Store(true)
	p.nb.mu.Lock()
	p.nb.closed = false
	p.nb.mu.Unlock()
	mustCommand(t, reopened, "peer.trust", map[string]any{"peerId": "peer-a", "trusted": true})
	approved, ok := reopened.trust("peer-a")
	if !ok || approved.Generation <= old.Generation || approved.Autosave || approved.RevocationEpoch != reopened.startup.Revocations[old.ID] {
		t.Fatal("fresh approval did not use current epoch", approved)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(context.Background(), Options{Directory: p.b.dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if trust, ok := again.trust("peer-a"); !ok || trust.RevocationEpoch != approved.RevocationEpoch {
		t.Fatal("explicit fresh approval did not survive reopen", trust)
	}
}

func TestAtomicJournalUncertaintyStopsLANTransportDespiteLaterSuccessfulWrites(t *testing.T) {
	c := openLANTestCore(t)
	backend, fake := testLANBackend()
	if err := backend.Start(); err != nil {
		t.Fatal(err)
	}
	id := fake.peers[0].Key
	p := c.profileCopy()
	p.Settings.Network = "lan"
	p.Peers = []Trust{{ID: id, Network: "lan", Generation: 9, Autosave: true, Directory: t.TempDir()}}
	if err := c.saveProfile(p); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.profile = p
	c.node = backend
	c.mu.Unlock()
	c.networkReady.Store(true)
	if err := c.bindTransferPeer(p.Peers[0]); err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int64
	c.atomicWrite = func(path string, data []byte) error {
		writes.Add(1)
		if err := config.AtomicWrite(path, data); err != nil {
			return err
		}
		if filepath.Base(path) == "startup-revocations.json" {
			return config.ErrAtomicCommitted
		}
		return nil
	}
	c.op.Lock()
	err := c.revokeLANPeer(backend, id)
	c.op.Unlock()
	if !errors.Is(err, config.ErrAtomicCommitted) || networkErrorCode(err) != "lan_revoke_not_persisted" || writes.Load() != 2 {
		t.Fatal(err, writes.Load())
	}
	fake.mu.Lock()
	closed, revocations := fake.closed, len(fake.revoked)
	fake.mu.Unlock()
	if _, ok := c.trust(id); ok || !closed || revocations != 1 || c.networkReady.Load() || len(c.transfers.ReceivePolicies()) != 0 {
		t.Fatal("LAN journal uncertainty retained authority", ok, closed, revocations)
	}
}

func TestAtomicPrivateSettingsCallersRetainAllSentinelsAndMachineCodes(t *testing.T) {
	for _, sentinel := range []error{config.ErrAtomicCommitted, config.ErrAtomicBusy, config.ErrAtomicRecovery} {
		for _, caller := range []string{"startup", "proxy", "revocation"} {
			t.Run(caller+"/"+sentinel.Error(), func(t *testing.T) {
				var c *Core
				var action string
				var payload any
				switch caller {
				case "startup":
					var spec ServiceSpec
					c, spec = startupFixture(t)
					saveStartupFixture(t, c, spec)
					action = "startup.disable"
					payload = map[string]any{"name": "example", "expectedStoreRevision": privateRevision(c.startup)}
				case "proxy":
					pair := newCorePair(t)
					c = pair.a
					save := saveProxyFixture(t, c, false, false)
					action = "proxy.saved.disable"
					payload = map[string]any{"name": save.Name, "expectedRevision": save.Revision}
				case "revocation":
					pair := newCorePair(t)
					trustPair(t, pair)
					c = pair.b
					action = "peer.trust"
					payload = map[string]any{"peerId": "peer-a", "trusted": false}
				}
				c.atomicWrite = func(path string, data []byte) error {
					if sentinel == config.ErrAtomicCommitted {
						if err := config.AtomicWrite(path, data); err != nil {
							return err
						}
					}
					return fmt.Errorf("%w: %w", sentinel, &os.PathError{Op: "sync", Path: filepath.Join(c.dir, ".sobalink-atomic-v1", "private-key-detail"), Err: os.ErrPermission})
				}
				_, err := command(c, randomID(), action, payload)
				wantCode := "private_settings_unavailable"
				if caller == "revocation" {
					wantCode = "startup_revocation_unconfirmed"
				}
				if !errors.Is(err, sentinel) || networkErrorCode(err) != wantCode {
					t.Fatal("atomic contract lost", err, networkErrorCode(err))
				}
				var raw *os.PathError
				if errors.As(err, &raw) || errors.Is(err, os.ErrPermission) {
					t.Fatal("private OS cause escaped", err)
				}
				for _, private := range []string{c.dir, ".sobalink-atomic-v1", "private-key-detail"} {
					if strings.Contains(err.Error(), private) {
						t.Fatal("private cause escaped", err)
					}
				}
			})
		}
	}
}

func TestAtomicJournalUncertaintyCannotRestartSavedStartupOrProxyApproval(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	_, _, starts := installProxyStarter(t, p.a)
	savedProxy := saveProxyFixture(t, p.a, false, true)
	mustCommand(t, p.a, "proxy.saved.start", map[string]any{"name": savedProxy.Name, "expectedRevision": savedProxy.Revision})
	spec := ServiceSpec{Name: "saved-forward", Backend: "tailnet", Direction: "forward", Network: "tcp", Ports: "443", LocalPort: 8443, LoopbackHost: "127.0.0.1", Lifetime: "finite", TTLSeconds: 3600, PeerID: "peer-b"}
	spec = saveDefinitionFixture(t, p.a, spec)
	saveStartupFixture(t, p.a, spec)
	var writes atomic.Int64
	p.a.atomicWrite = func(path string, data []byte) error {
		writes.Add(1)
		if filepath.Base(path) == "sobalink.json" {
			return os.ErrPermission
		}
		if err := config.AtomicWrite(path, data); err != nil {
			return err
		}
		return config.ErrAtomicCommitted
	}
	_, err := command(p.a, randomID(), "peer.trust", map[string]any{"peerId": "peer-b", "trusted": false})
	if !errors.Is(err, config.ErrAtomicCommitted) || writes.Load() != 2 {
		t.Fatal(err, writes.Load())
	}
	if len(p.a.proxies) != 0 || !p.a.startupSuppressed || len(p.a.savedProxyPending) != 0 || len(p.a.startupPending) != 0 {
		t.Fatal("uncertain revoke retained startup work")
	}
	if err := p.a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Options{Directory: p.a.dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.loadStartup(false); err != nil {
		t.Fatal(err)
	}
	_, _, restarts := installProxyStarter(t, reopened)
	withServiceOperation(reopened, func() {
		reopened.runStartupSelections(context.Background(), func(context.Context, string, json.RawMessage) (any, error) {
			t.Fatal("stale startup approval restarted")
			return nil, nil
		})
		reopened.runSavedProxyStartup(context.Background())
	})
	if restarts.Load() != 0 || starts.Load() != 1 || reopened.startupStates["example"] != "stale" {
		t.Fatal("revoked saved authority reopened", restarts.Load(), starts.Load())
	}
	_, err = command(reopened, randomID(), "proxy.saved.start", map[string]any{"name": savedProxy.Name, "expectedRevision": savedProxy.Revision})
	if networkErrorCode(err) != "proxy_saved_revision_conflict" {
		t.Fatal("stale proxy approval accepted", err)
	}
}

func TestAtomicConfirmedJournalStillClosesAuthorityWhenProfileSaveFails(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
	old, _ := p.b.trust("peer-a")
	var writes atomic.Int64
	p.b.atomicWrite = func(path string, data []byte) error {
		writes.Add(1)
		if filepath.Base(path) == "sobalink.json" {
			return os.ErrPermission
		}
		return config.AtomicWrite(path, data)
	}
	_, err := command(p.b, randomID(), "peer.trust", map[string]any{"peerId": old.ID, "trusted": false})
	if !errors.Is(err, os.ErrPermission) || writes.Load() != 2 {
		t.Fatal(err, writes.Load())
	}
	if _, ok := p.b.trust(old.ID); ok || len(p.b.transfers.ReceivePolicies()) != 0 {
		t.Fatal("confirmed journal retained authority after profile failure")
	}
	if _, err := p.b.transfers.Offer(transfer.Peer{ID: old.ID, Generation: old.Generation}, fileManifest("confirmed-journal-offer", "payload")); err == nil {
		t.Fatal("old transfer authority survived")
	}
	status, _ := peerCall(t, p.na, p.nb, "POST", "/v1/messages", map[string]string{"id": "confirmed-journal-message", "text": "reject"})
	if status != 403 {
		t.Fatal("old message authority survived", status)
	}
}
