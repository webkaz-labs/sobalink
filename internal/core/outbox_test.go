package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func outboxFixture(t *testing.T) *Core {
	t.Helper()
	dir := t.TempDir()
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Core{dir: dir, ctx: ctx, cancel: cancel, resourceLock: lock, resourceIdentity: strings.Repeat("a", 32), resourceNonce: strings.Repeat("b", 32), capacity: capacity.Defaults(), outgoing: map[string]*outgoingBatch{}, profile: Profile{Version: 2, DurableFiles: &DurableProfile{Version: 1, Epoch: strings.Repeat("c", 32), Phase: "ready"}, Settings: Settings{Network: "tailnet"}, Peers: []Trust{{ID: "peer-b", Network: "tailnet", Generation: 1}, {ID: "peer-c", Network: "tailnet", Generation: 1}}}, node: &pipeNode{state: identity.State{SelfID: "peer-a"}}}
	if err = c.initializeOutbox(true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		c.wg.Wait()
		if o := c.outboxCopy(); o != nil && o.root != nil {
			o.root.Close()
		}
		lock.Close()
	})
	return c
}
func outboxPrepareFixture(t *testing.T, c *Core, id string) OutboxRecord {
	t.Helper()
	id = newOutboxRequestID(time.Now())
	source := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(source, []byte("synthetic payload"), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := c.prepareOutboxPaths(context.Background(), id, resourceCatalogProcessToken(c.resourceNonce), []string{"peer-b", "peer-c"}, []string{source}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return v.(OutboxRecord)
}
func outboxCommitFixture(t *testing.T, c *Core, r OutboxRecord) OutboxRecord {
	t.Helper()
	v, err := c.commitOutbox(context.Background(), r.OutboxID, r.ProcessID, r.ReviewRevision, true)
	if err != nil {
		t.Fatal(err)
	}
	return v.(OutboxRecord)
}
func outboxReopenFixture(t *testing.T, c *Core) {
	t.Helper()
	o := c.outboxCopy()
	for _, p := range o.prepared {
		p.cancel()
	}
	o.root.Close()
	c.mu.Lock()
	c.outbox = nil // A simulated new process owns no previous coordinator.
	c.mu.Unlock()
	c.resourceNonce = randomID()
	if err := c.initializeOutbox(false); err != nil {
		t.Fatal(err)
	}
}
func TestOutboxPreparedRestartNeverEnqueues(t *testing.T) {
	c := outboxFixture(t)
	r := outboxPrepareFixture(t, c, "prepare-only")
	p := c.outbox.state.Payloads[c.outbox.prepared[r.OutboxID].intent.PayloadID]
	outboxReopenFixture(t, c)
	if len(c.outbox.state.Intents) != 0 || len(c.outbox.prepared) != 0 || !c.outbox.state.Payloads[p.ID].Released {
		t.Fatal("uncommitted preparation gained authority or retained abandoned bytes")
	}
	if _, err := c.commitOutbox(context.Background(), r.OutboxID, r.ProcessID, r.ReviewRevision, true); err == nil {
		t.Fatal("old review committed after restart")
	}
}
func TestOutboxEnqueueRestartPreservesExactIDsAndCancellation(t *testing.T) {
	c := outboxFixture(t)
	review := outboxPrepareFixture(t, c, "durable-request")
	first := outboxCommitFixture(t, c, review)
	outboxReopenFixture(t, c)
	old := c.outbox.state.Intents[first.OutboxID]
	if !reflect.DeepEqual(first.Children, old.View.Children) || !first.ExpiresAt.Equal(old.View.ExpiresAt) {
		t.Fatal("durable identity or absolute deadline changed")
	}
	repeated, err := c.commitOutbox(context.Background(), review.OutboxID, review.ProcessID, review.ReviewRevision, true)
	if err != nil || !reflect.DeepEqual(first.Children, repeated.(OutboxRecord).Children) {
		t.Fatal("same commit did not recover original admission")
	}
	v, err := c.cancelOutbox(context.Background(), first.OutboxID, first.Children[0].DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := v.(OutboxRecord)
	if cancelled.Children[0].Status != "cancelled" || cancelled.Children[1].Status != "queued" || c.outbox.state.Payloads[old.PayloadID].Released {
		t.Fatal("one cancellation lost other recipient payload ownership")
	}
	if _, err = c.cancelOutbox(context.Background(), first.OutboxID, ""); err != nil {
		t.Fatal(err)
	}
	outboxReopenFixture(t, c)
	for _, d := range c.outbox.state.Intents[first.OutboxID].Deliveries {
		if !d.Cancelled {
			t.Fatal("cancel fence lost on restart")
		}
	}
	if !c.outbox.state.Payloads[old.PayloadID].Released {
		t.Fatal("joined cancellation retained payload bytes")
	}
}
func TestOutboxUncertainAdmissionNeverReturnsEnqueuedSuccess(t *testing.T) {
	c := outboxFixture(t)
	r := outboxPrepareFixture(t, c, "uncertain-request")
	o := c.outbox
	o.write = func(string, []byte) error { return config.ErrAtomicCommitted }
	if _, err := c.commitOutbox(context.Background(), r.OutboxID, r.ProcessID, r.ReviewRevision, true); err == nil {
		t.Fatal("uncertain publication returned success")
	}
	if _, err := c.commitOutbox(context.Background(), r.OutboxID, r.ProcessID, r.ReviewRevision, true); err == nil {
		t.Fatal("retry certified uncertain admission")
	}
	result, err := c.outboxCommand(context.Background(), "outbox.list", "list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.(map[string]any)["items"].([]OutboxRecord)) != 0 {
		t.Fatal("uncertain new admission escaped last-certified view")
	}
}
func TestOutboxClockRollbackAndRelationshipReuseBlock(t *testing.T) {
	t.Run("clock", func(t *testing.T) {
		c := outboxFixture(t)
		r := outboxPrepareFixture(t, c, "clock-request")
		outboxCommitFixture(t, c, r)
		o := c.outbox
		o.now = func() time.Time { return o.state.HighWater.Add(-time.Second) }
		c.pumpOutbox()
		if o.frozen != "outbox_clock_rollback" || len(o.runs) != 0 {
			t.Fatal("clock rollback scheduled payload")
		}
	})
	t.Run("relationship", func(t *testing.T) {
		c := outboxFixture(t)
		r := outboxPrepareFixture(t, c, "epoch-request")
		outboxCommitFixture(t, c, r)
		c.profile.Peers[0].RevocationEpoch = "new-relationship"
		c.pumpOutbox()
		row := c.outbox.state.Intents[r.OutboxID]
		if row.View.Children[0].Status != "blocked_permission" || row.Deliveries[0].Exposure {
			t.Fatal("reused numeric generation authorized an old relationship")
		}
		c.cancel()
		c.wg.Wait()
	})
}
func TestOutboxCorruptStoreFailsClosed(t *testing.T) {
	for _, body := range []string{`{"version":1,"version":1}`, `{"version":1,"intents":{"broken":null}}`} {
		t.Run(body, func(t *testing.T) {
			c := outboxFixture(t)
			c.outbox.root.Close()
			c.mu.Lock()
			c.outbox = nil
			c.mu.Unlock()
			if err := os.WriteFile(filepath.Join(c.dir, outboxNamespace, "state.json"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if err := c.initializeOutbox(false); err == nil {
				t.Fatal("corrupt store became empty runnable store")
			}
		})
	}
}
func TestOutboxCleanupPendingRestartReconcilesMissingOwnedFiles(t *testing.T) {
	c := outboxFixture(t)
	r := outboxPrepareFixture(t, c, "cleanup-request")
	outboxCommitFixture(t, c, r)
	o := c.outbox
	row := o.state.Intents[r.OutboxID]
	p := o.state.Payloads[row.PayloadID]
	for i := range row.Deliveries {
		row.Deliveries[i].Cancelled = true
		row.View.Children[i].Status = "cancelled"
	}
	outboxSummarize(row, o.now())
	p.CleanupPending = true
	if err := o.saveLocked(); err != nil {
		t.Fatal(err)
	}
	root, err := o.payloadRootLocked(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range p.Manifest.Entries {
		if entry.Kind == transfer.File {
			if err = root.Remove(entry.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	root.Close()
	outboxReopenFixture(t, c)
	if !c.outbox.state.Payloads[p.ID].Released {
		t.Fatal("committed cleanup intent was not reconciled")
	}
}
func TestOutboxRetirementKeepsExactKeyFence(t *testing.T) {
	c := outboxFixture(t)
	r := outboxPrepareFixture(t, c, "retire-request")
	outboxCommitFixture(t, c, r)
	if _, err := c.cancelOutbox(context.Background(), r.OutboxID, ""); err != nil {
		t.Fatal(err)
	}
	o := c.outbox
	horizon, _ := outboxRequestHorizon(r.OutboxID)
	o.now = func() time.Time { return horizon }
	if err := o.retireLocked(); err != nil {
		t.Fatal(err)
	}
	if o.state.Intents[r.OutboxID] != nil {
		t.Fatal("eligible terminal record was not compacted")
	}
	raw, _ := json.Marshal(map[string]string{"outboxId": r.OutboxID})
	_, err := c.outboxCommand(context.Background(), "outbox.status", "status", raw)
	if networkErrorCode(err) != "outbox_retired" {
		t.Fatalf("old key status = %v", err)
	}
}
func TestOutboxEvidenceRejectsMismatchedSavedPath(t *testing.T) {
	c := outboxFixture(t)
	r := outboxPrepareFixture(t, c, "receipt-request")
	outboxCommitFixture(t, c, r)
	row := c.outbox.state.Intents[r.OutboxID]
	d := &row.Deliveries[0]
	lease := transfer.DurableLease{Version: 1, DeliveryID: d.Offer.DeliveryID, Digest: d.Offer.Digest, RecipientID: d.Trust.ID, ReceiverEpoch: strings.Repeat("d", 32), Token: strings.Repeat("e", 32), ExpiresAt: r.ExpiresAt, ReceiptUntil: r.ExpiresAt.Add(transfer.DurableReceiptGrace)}
	d.Lease = &lease
	entry := r.Entries[0]
	receipt := transfer.DurableReceipt{Lease: lease, State: transfer.Completed, SavedAt: time.Now(), Files: []transfer.FileAck{{BatchID: d.Offer.DeliveryID, FileID: entry.ID, StoredName: "different.txt", Size: entry.Size, SHA256: entry.SHA256}}}
	err := c.recordOutboxStatus(r.OutboxID, 0, transfer.DurableStatus{Lease: lease, State: transfer.Completed, Receipt: &receipt})
	if err == nil || row.View.Children[0].DeliveryOutcome == "confirmed" {
		t.Fatal("mismatched destination became confirmed")
	}
}

func holdOutboxLifecycle(t *testing.T, c *Core) func() {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	joined := make(chan error, 1)
	go func() {
		joined <- c.resourceLock.WithOwnershipInfo(c.dir, func(os.FileInfo, os.FileInfo) error { close(entered); <-release; return nil })
	}()
	<-entered
	return func() {
		close(release)
		if err := <-joined; err != nil {
			t.Error(err)
		}
	}
}
func TestOutboxLifecycleBusyDoesNotMutateAdmissionOrCancellation(t *testing.T) {
	c := outboxFixture(t)
	review := outboxPrepareFixture(t, c, "busy-request")
	before, _ := json.Marshal(c.outbox.state)
	release := holdOutboxLifecycle(t, c)
	_, err := c.commitOutbox(context.Background(), review.OutboxID, review.ProcessID, review.ReviewRevision, true)
	after, _ := json.Marshal(c.outbox.state)
	release()
	if networkErrorCode(err) != "outbox_storage_busy" || string(before) != string(after) || c.outbox.frozen != "" {
		t.Fatalf("busy changed admission: %v", err)
	}
	admitted := outboxCommitFixture(t, c, review)
	before, _ = json.Marshal(c.outbox.state)
	release = holdOutboxLifecycle(t, c)
	_, err = c.cancelOutbox(context.Background(), admitted.OutboxID, "")
	after, _ = json.Marshal(c.outbox.state)
	release()
	if networkErrorCode(err) != "outbox_storage_busy" || string(before) != string(after) || c.outbox.frozen != "" {
		t.Fatalf("busy changed cancellation: %v", err)
	}
	outboxReopenFixture(t, c)
	row := c.outbox.state.Intents[admitted.OutboxID]
	if !reflect.DeepEqual(row.View.Children, admitted.Children) {
		t.Fatal("busy restart changed original admitted identities")
	}
}
func TestOutboxPublishedUncertaintyReopensSameIntent(t *testing.T) {
	c := outboxFixture(t)
	r := outboxPrepareFixture(t, c, "published-request")
	c.outbox.afterWrite = func() error { return config.ErrAtomicCommitted }
	if _, err := c.commitOutbox(context.Background(), r.OutboxID, r.ProcessID, r.ReviewRevision, true); err == nil {
		t.Fatal("uncertain publication returned success")
	}
	ids := append([]OutboxChild{}, r.Children...)
	outboxReopenFixture(t, c)
	row := c.outbox.state.Intents[r.OutboxID]
	if row == nil || row.View.ReviewRevision != r.ReviewRevision {
		t.Fatal("published operation was lost or reidentified")
	}
	for i, child := range ids {
		if row.View.Children[i].DeliveryID != child.DeliveryID {
			t.Fatal("reopen allocated another child")
		}
	}
}
func TestOutboxRuntimeLedgerLossNeverRecreatesState(t *testing.T) {
	c := outboxFixture(t)
	r := outboxPrepareFixture(t, c, "lost-ledger")
	path := filepath.Join(c.dir, outboxNamespace, "state.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := c.commitOutbox(context.Background(), r.OutboxID, r.ProcessID, r.ReviewRevision, true); err == nil {
		t.Fatal("missing ledger was accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("missing enabled ledger was silently recreated")
	}
}
func TestOutboxCleanupRefusesReplacedOwnedFile(t *testing.T) {
	c := outboxFixture(t)
	r := outboxPrepareFixture(t, c, "replaced-payload")
	outboxCommitFixture(t, c, r)
	row := c.outbox.state.Intents[r.OutboxID]
	p := c.outbox.state.Payloads[row.PayloadID]
	path := filepath.Join(c.dir, outboxNamespace, "payloads", p.ID, r.Entries[0].ID)
	old := path + "-old"
	if err := os.Rename(path, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement must remain"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.cancelOutbox(context.Background(), r.OutboxID, ""); err == nil {
		t.Fatal("unowned replacement was reclaimed")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "replacement must remain" {
		t.Fatal("cleanup removed a substituted file")
	}
	if p.Released {
		t.Fatal("failed ownership check released byte accounting")
	}
}

func TestOutboxInterruptedCopyReclaimsOnlyRecordedOwnedFiles(t *testing.T) {
	c := outboxFixture(t)
	id := newOutboxRequestID(time.Now())
	entry := transfer.Entry{ID: "1", Path: "file.txt", Kind: transfer.File, Size: 32, SHA256: strings.Repeat("0", 64)}
	p, _, err := c.reserveOutbox(context.Background(), id, resourceCatalogProcessToken(c.resourceNonce), []string{"peer-b"}, transfer.Manifest{ID: id, Entries: []transfer.Entry{entry}}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	payload := c.outbox.state.Payloads[p.intent.PayloadID]
	root, err := c.outbox.payloadRootLocked(payload)
	if err != nil {
		t.Fatal(err)
	}
	entry.SHA256 = ""
	if err = c.stageOutboxFile(context.Background(), root, payload, entry, strings.NewReader("cut")); err == nil {
		t.Fatal("short source unexpectedly staged")
	}
	root.Close()
	// The process disappears before its ordinary abort handler. Reopen uses the
	// persisted native ownership records, never a path-only recursive removal.
	outboxReopenFixture(t, c)
	retained := c.outbox.state.Payloads[payload.ID]
	if retained != nil && !retained.Released {
		t.Fatal("uncommitted short copy retained payload bytes")
	}
}
