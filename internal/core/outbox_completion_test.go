package core

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func outboxCompletionFixture(t *testing.T) (*Core, OutboxRecord, *outgoingBatch) {
	t.Helper()
	c := outboxFixture(t)
	r := outboxCommitFixture(t, c, outboxPrepareFixture(t, c, "completion"))
	o := c.outbox
	d := o.state.Intents[r.OutboxID].Deliveries[0]
	_, cancel := context.WithCancel(c.ctx)
	t.Cleanup(cancel)
	b := &outgoingBatch{ID: d.Offer.DeliveryID, PeerID: d.Trust.ID, Network: d.Trust.Network, Generation: d.Trust.Generation, Manifest: d.Offer.Manifest, backend: c.nodeCopy(), outboxID: r.OutboxID, cancel: cancel}
	o.runs[b.ID] = b
	// Isolate completion drainage from the next ordinary retry admission.
	for _, child := range r.Children {
		o.next[child.DeliveryID] = time.Now().Add(time.Hour)
	}
	return c, r, b
}

func waitOutboxCompletion(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("physical worker did not join while lifecycle ownership was held")
	}
}

func TestOutboxCompletionBusyRetainsJoinedDisposition(t *testing.T) {
	c, r, b := outboxCompletionFixture(t)
	var requests atomic.Int64
	offered := make(chan string, 1)
	outboxPipePeer(t, c, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		switch req.URL.Path {
		case "/v1/hello":
			reply(w, http.StatusOK, map[string]any{"protocol": 1, "product": "sobalink", "capabilities": []string{transfer.DurableCapability}})
		case transfer.DurableOfferPath:
			var offer transfer.DurableOffer
			if json.NewDecoder(req.Body).Decode(&offer) != nil {
				http.Error(w, "invalid offer", http.StatusBadRequest)
				return
			}
			select {
			case offered <- offer.DeliveryID:
			default:
			}
			lease := transfer.DurableLease{Version: 1, DeliveryID: offer.DeliveryID, Digest: offer.Digest, RecipientID: offer.RecipientID, ReceiverEpoch: strings.Repeat("d", 32), Token: strings.Repeat("e", 32), ExpiresAt: offer.ExpiresAt, ReceiptUntil: offer.ExpiresAt.Add(transfer.DurableReceiptGrace)}
			reply(w, http.StatusOK, transfer.DurableStatus{Lease: lease, State: transfer.Pending, ReceiverNow: time.Now()})
		default:
			http.Error(w, "unexpected request", http.StatusConflict)
		}
	}))
	o := c.outbox
	before := append([]byte(nil), o.confirmedDisk...)
	releaseOwner := holdOutboxLifecycle(t, c)
	var once sync.Once
	release := func() { once.Do(releaseOwner) }
	defer release()
	if err := c.runOutgoing(c.ctx, b); err != nil {
		t.Fatal(err)
	}
	joined := make(chan struct{})
	go func() { c.wg.Wait(); close(joined) }()
	waitOutboxCompletion(t, joined)
	o.mu.Lock()
	completion, pending := o.completions[b.ID]
	retained := o.runs[b.ID] == b
	view := o.viewLocked(o.state.Intents[r.OutboxID])
	unchanged := reflect.DeepEqual(before, o.confirmedDisk)
	o.mu.Unlock()
	if !pending || !completion.joined || completion.code != "outbox_storage_busy" || !retained || !unchanged || requests.Load() != 0 {
		t.Fatal("busy initial admission lost its joined completion or exposed network work")
	}
	if view.Children[0].Status != "queued" || view.Children[0].DeliveryOutcome != "not_attempted" {
		t.Fatal("unpersisted completion changed certified evidence")
	}
	c.mu.RLock()
	active, queued := c.outgoingActive, len(c.outgoingQueue)
	c.mu.RUnlock()
	if active != 0 || queued != 0 {
		t.Fatal("pending persistence retained a global network worker slot")
	}
	c.pumpOutbox() // Busy again; neither disposition nor fence may disappear.
	release()
	writes := 0
	o.afterWrite = func() error { writes++; return nil }
	c.pumpOutbox()
	if len(o.completions) != 0 || o.runs[b.ID] != nil || writes != 1 {
		t.Fatal("admitted maintenance did not drain the original completion exactly once")
	}
	child := o.state.Intents[r.OutboxID].View.Children[0]
	if child.Status != "waiting_for_peer" || child.ReasonCode != "outbox_storage_busy" || child.DeliveryOutcome != "not_attempted" {
		t.Fatal("durable completion lost the closed refusal reason")
	}
	c.pumpOutbox()
	if writes != 1 || requests.Load() != 0 {
		t.Fatal("completion drainage duplicated a save or prematurely replanned")
	}
	// The next ordinary attempt uses the same delivery, reaches the actual
	// offer path, and waits for receiver consent after contention ends.
	delete(o.next, b.ID)
	c.pumpOutbox()
	retried := make(chan struct{})
	go func() { c.wg.Wait(); close(retried) }()
	waitOutboxCompletion(t, retried)
	select {
	case id := <-offered:
		if id != b.ID {
			t.Fatal("retry changed the original delivery identity")
		}
	default:
		t.Fatal("ordinary retry did not reach the original offer")
	}
	child = o.state.Intents[r.OutboxID].View.Children[0]
	if requests.Load() != 2 || child.Status != "awaiting_receipt" || child.DeliveryOutcome != "unknown" || o.state.Intents[r.OutboxID].Deliveries[0].Lease == nil || len(o.completions) != 0 {
		t.Fatal("retried original delivery did not persist its lease and approval wait")
	}
}

func TestOutboxCompletionWaitsForApplicationRelease(t *testing.T) {
	c, r, b := outboxCompletionFixture(t)
	o := c.outbox
	row := o.state.Intents[r.OutboxID]
	for i := range row.Deliveries {
		row.Deliveries[i].Cancelled = true
		row.View.Children[i].Status = "cancelled"
	}
	outboxSummarize(row, o.now())
	if err := o.saveLocked(); err != nil {
		t.Fatal(err)
	}
	b.running, b.runDone = true, make(chan struct{})
	atRelease, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	ctx, cancel := context.WithCancel(c.ctx)
	application := &peerHTTPApplication{ctx: ctx, cancel: cancel, release: func() { close(atRelease); <-release }}
	run := &outgoingRun{ctx: c.ctx, batch: b, stop: func() bool { return true }, done: func() { close(finished) }}
	go c.finishOutboxRun(run, application, outboxError("outbox_receiver_declined"), false)
	waitOutboxCompletion(t, atRelease)
	// The terminal callback has already run, but the enclosing physical owner
	// has not returned. This is the distinction running/runDone cannot supply.
	if !channelClosed(b.runDone) {
		t.Fatal("fixture did not reach the post-terminal application release")
	}
	c.pumpOutbox()
	o.mu.Lock()
	pending := o.completions[b.ID]
	retained := o.runs[b.ID] == b
	released := o.state.Payloads[row.PayloadID].Released
	o.mu.Unlock()
	if pending.joined || !retained || released {
		t.Fatal("post-terminal physical owner lost its payload or replanning fence")
	}
	unblock()
	waitOutboxCompletion(t, finished)
	if len(o.completions) != 0 || o.runs[b.ID] != nil || !o.state.Payloads[row.PayloadID].Released {
		t.Fatal("joined cancelled owner did not complete safe cleanup")
	}
}

func TestOutboxCompletionCurrentFencesDominate(t *testing.T) {
	for _, change := range []string{"cancel", "expire", "revoke", "pause", "saved"} {
		t.Run(change, func(t *testing.T) {
			c, r, b := outboxCompletionFixture(t)
			o := c.outbox
			row := o.state.Intents[r.OutboxID]
			o.completions[b.ID] = outboxCompletion{code: "outbox_receiver_approval", joined: true}
			want := ""
			switch change {
			case "cancel":
				if _, err := c.cancelOutbox(c.ctx, r.OutboxID, b.ID); err != nil {
					t.Fatal(err)
				}
				want = "cancelled"
			case "expire":
				now := r.ExpiresAt.Add(time.Second)
				o.now = func() time.Time { return now }
				for _, child := range r.Children {
					o.next[child.DeliveryID] = now.Add(time.Hour)
				}
				want = "expired"
			case "revoke":
				c.mu.Lock()
				c.profile.Peers = c.profile.Peers[1:]
				c.mu.Unlock()
				want = "blocked_permission"
			case "pause":
				c.mu.Lock()
				c.profile.Peers[0].Paused = true
				c.mu.Unlock()
				want = "paused"
			case "saved":
				d := &row.Deliveries[0]
				d.Attempted = true
				d.Lease = &transfer.DurableLease{Version: 1, DeliveryID: b.ID, Digest: d.Offer.Digest, RecipientID: d.Trust.ID, ReceiverEpoch: strings.Repeat("d", 32), Token: strings.Repeat("e", 32), ExpiresAt: r.ExpiresAt, ReceiptUntil: r.ExpiresAt.Add(transfer.DurableReceiptGrace)}
				e := r.Entries[0]
				receipt := transfer.DurableReceipt{Lease: *d.Lease, State: transfer.Completed, SavedAt: time.Now(), Files: []transfer.FileAck{{BatchID: b.ID, FileID: e.ID, StoredName: e.Path, Size: e.Size, SHA256: e.SHA256}}}
				if err := c.recordOutboxStatus(r.OutboxID, 0, transfer.DurableStatus{Lease: *d.Lease, State: transfer.Completed, Receipt: &receipt}); err != nil {
					t.Fatal(err)
				}
				want = "completed"
			}
			c.pumpOutbox()
			if row.View.Children[0].Status != want || o.runs[b.ID] != nil || len(o.completions) != 0 {
				t.Fatalf("pending completion overrode current %s fence", change)
			}
		})
	}
}

func TestOutboxCompletionUncertainSaveRetainsFence(t *testing.T) {
	c, r, b := outboxCompletionFixture(t)
	o := c.outbox
	o.completions[b.ID] = outboxCompletion{code: "outbox_receiver_approval", joined: true}
	before := cloneOutboxRecord(o.certified[r.OutboxID])
	o.write = func(string, []byte) error { return config.ErrAtomicCommitted }
	c.pumpOutbox()
	if len(o.completions) != 1 || o.runs[b.ID] != b || o.frozen == "" || !reflect.DeepEqual(before, o.certified[r.OutboxID]) {
		t.Fatal("uncertain completion save forgot its fence or certified evidence")
	}
	view := o.viewLocked(o.state.Intents[r.OutboxID])
	if view.Status != "blocked_recovery" || view.Children[0].DeliveryOutcome != "not_attempted" {
		t.Fatal("uncertain save exposed an uncertified completion")
	}
}

func TestOutboxCompletionRestartKeepsOriginalLease(t *testing.T) {
	c, r, b := outboxCompletionFixture(t)
	o := c.outbox
	row := o.state.Intents[r.OutboxID]
	d := &row.Deliveries[0]
	d.Attempted = true
	d.Lease = &transfer.DurableLease{Version: 1, DeliveryID: b.ID, Digest: d.Offer.Digest, RecipientID: d.Trust.ID, ReceiverEpoch: strings.Repeat("d", 32), Token: strings.Repeat("e", 32), ExpiresAt: r.ExpiresAt, ReceiptUntil: r.ExpiresAt.Add(transfer.DurableReceiptGrace)}
	row.View.Children[0].DeliveryOutcome = "unknown"
	outboxSummarize(row, o.now())
	if err := o.saveLocked(); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(row)
	o.completions[b.ID] = outboxCompletion{code: "outbox_storage_busy", joined: true}
	outboxReopenFixture(t, c)
	after, _ := json.Marshal(c.outbox.state.Intents[r.OutboxID])
	if string(before) != string(after) || len(c.outbox.completions) != 0 || len(c.outbox.runs) != 0 {
		t.Fatal("reopen replaced the original request, lease or retained evidence")
	}
}

func TestOutboxCompletionCloseDoesNotWaitForLifecycle(t *testing.T) {
	c, _, b := outboxCompletionFixture(t)
	outboxPipePeer(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected request", http.StatusConflict)
	}))
	var err error
	c.transfers, err = transfer.NewManager(transfer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	release := holdOutboxLifecycle(t, c)
	defer release()
	if err := c.runOutgoing(c.ctx, b); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { _ = c.Close(); close(closed) }()
	waitOutboxCompletion(t, closed)
}

func TestOutboxCompletionCodeIsClosed(t *testing.T) {
	if outboxCompletionCode(outboxError("outbox_storage_busy")) != "outbox_storage_busy" || outboxCompletionCode(outboxError("untrusted arbitrary text")) != "" {
		t.Fatal("completion retained a dynamic error string")
	}
}
