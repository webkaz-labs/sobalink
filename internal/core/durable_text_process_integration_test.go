//go:build lanlink_integration && directlan_integration

package core

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
)

// This is native process/loopback acceptance. It does not qualify physical
// devices, power-loss durability, enrollment, OS login, suspend or WAN paths.
func TestDurableTextProcessRestartACKLossAndHiddenReplay(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_DIRECT_INTEGRATION") != "1" {
		t.Skip("explicit native loopback execution required")
	}
	if !directLANNetstackReady {
		t.Skip("userspace netstack activation remains gated")
	}
	t.Log("SOBA_TEXT_PHASE start")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dirs, peers, keys := startDispatchProcessPeers(t, ctx)
	// The third process belongs to the existing paired fixture; no text is
	// selected for it. All identities and reviewed text are synthetic.
	const body = "  synthetic durable text: \"exact\"\n合成テキスト\n"
	id := newTextRequestID(time.Now())
	evidence := func(index int) durableTextProcessEvidence {
		return catalogProcessDecode[durableTextProcessEvidence](t, peers[index].invoke(t, "fixture.text-evidence", map[string]string{"messageId": id, "text": body}))
	}
	process := func(index int) string {
		return catalogProcessDecode[struct {
			ProcessID string `json:"resourceCatalogProcessId"`
		}](t, peers[index].invoke(t, "fixture.snapshot", struct{}{})).ProcessID
	}
	await := func(label string, check func() bool) {
		t.Helper()
		until := time.Now().Add(30 * time.Second)
		for !check() {
			if ctx.Err() != nil || time.Now().After(until) {
				t.Fatal("native durable text condition unavailable:", label)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	start := func(index int) {
		peers[index] = startRouteCoreProcessWithEnvironment(t, ctx, dirs[index], false, []string{"SOBALINK_ROUTE_PROCESS_CATALOG=1"})
		await("native peer listener", func() bool { return evidence(index).PeerReady })
	}
	status := func() TextOutboxRecord {
		return catalogProcessDecode[TextOutboxRecord](t, peers[0].invoke(t, "message.queue.status", map[string]string{"outboxId": id}))
	}
	// Only an exact lifecycle-busy result is retryable. Preserve the complete
	// reviewed payload and original request ID; all other errors fail closed.
	messageCommand := func(index int, name, requestID string, payload any) json.RawMessage {
		t.Helper()
		until := time.Now().Add(10 * time.Second)
		for {
			reply := peers[index].invokeResultID(t, name, requestID, payload)
			if reply.Error == "" {
				return reply.Value
			}
			if reply.Error != name+": message_storage_busy" || ctx.Err() != nil || time.Now().After(until) {
				t.Fatal("native durable text command failed:", name)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	command := func(name, requestID string, payload any) json.RawMessage {
		return messageCommand(0, name, requestID, payload)
	}
	t.Log("SOBA_TEXT_PHASE prepare")
	oldProcess := process(0)
	if oldProcess == "" {
		t.Fatal("owned sender process identity unavailable")
	}
	peers[1].stop(t)
	prepare := map[string]any{"processId": oldProcess, "peerId": keys[1], "text": body, "expiresAt": time.Now().UTC().Add(time.Hour)}
	review := catalogProcessDecode[TextOutboxRecord](t, command("message.queue.prepare", id, prepare))
	if review.Version != 1 || review.Kind != "text_outbox" || review.Admission != "prepared" || review.Status != "prepared" || review.ProcessID != oldProcess || review.OutboxID != id || review.RequestID != id || review.MessageID != id || review.PeerID != keys[1] || review.Text != body || review.TextBytes != int64(len(body)) || review.SHA256 != textSHA256(body) || review.ReviewRevision == "" {
		t.Fatal("native text review lost exact immutable content or identity")
	}
	repeated := catalogProcessDecode[TextOutboxRecord](t, command("message.queue.prepare", id, prepare))
	if !reflect.DeepEqual(review, repeated) {
		t.Fatal("repeated native text review changed")
	}
	for _, field := range []string{"text", "expiresAt"} {
		changed := map[string]any{"processId": oldProcess, "peerId": keys[1], "text": body, "expiresAt": review.ExpiresAt}
		if field == "text" {
			changed[field] = "synthetic changed text"
		} else {
			changed[field] = review.ExpiresAt.Add(time.Minute)
		}
		if reply := peers[0].invokeResultID(t, "message.queue.prepare", id, changed); reply.Error != "message.queue.prepare: message_conflict" {
			t.Fatal("native text immutable review conflict was not rejected")
		}
	}
	commitPayload := map[string]any{"outboxId": id, "processId": oldProcess, "reviewRevision": review.ReviewRevision, "confirm": true}
	commit := func() TextOutboxRecord {
		return catalogProcessDecode[TextOutboxRecord](t, command("message.queue.commit", "", commitPayload))
	}
	immutable := func(record TextOutboxRecord) {
		t.Helper()
		if record.Admission != "enqueued" || record.OutboxID != id || record.RequestID != id || record.MessageID != id || record.PeerID != review.PeerID || record.ReviewRevision != review.ReviewRevision || !record.CreatedAt.Equal(review.CreatedAt) || !record.ExpiresAt.Equal(review.ExpiresAt) || record.TextBytes != review.TextBytes || record.SHA256 != review.SHA256 || record.Text != body {
			t.Fatal("native text restart changed original review or locator")
		}
	}
	t.Log("SOBA_TEXT_PHASE commit")
	immutable(commit())
	assertOffline := func() {
		t.Helper()
		value := status()
		immutable(value)
		observed := evidence(0)
		if value.DeliveryOutcome != "not_attempted" || value.Receipt != nil || observed.OutgoingRows != 1 || observed.VisibleRows != 1 || !observed.BodyExact || !observed.BodyRetained || observed.ExposurePossible || observed.Lease != nil {
			t.Fatal("offline native text enqueue fabricated delivery or lost visibility")
		}
	}
	assertOffline()
	t.Log("SOBA_TEXT_PHASE sender-restart")
	peers[0].stop(t)
	start(0)
	if process(0) == oldProcess {
		t.Fatal("native sender process did not restart")
	}
	assertOffline()
	immutable(commit()) // exact old process/review retry resolves the durable row
	t.Log("SOBA_TEXT_PHASE saved-ack-loss")
	// Stop the sender before installing the receiver-only ACK fault so there
	// is no unprotected race between network readiness and interceptor setup.
	peers[0].stop(t)
	start(1)
	peers[1].invoke(t, "fixture.text-fault", map[string]string{"messageId": id, "operation": "drop-saved-ack"})
	start(0)
	await("successful saved ACK loss", func() bool { return evidence(1).Fault.SavedACKDrops == 1 })
	lost := evidence(1)
	if !lost.Fault.Installed || lost.Fault.InvalidCapture || lost.Fault.OfferRequests < 1 || lost.Fault.DeliverRequests < 1 || lost.IncomingRows != 1 || lost.VisibleRows != 1 || !lost.BodyExact || !lost.BodyRetained || lost.Receipt == nil || lost.Lease == nil {
		t.Fatal("lost ACK was not an actual admitted single receiver save")
	}
	receipt := *lost.Receipt
	unknown := status()
	immutable(unknown)
	sender := evidence(0)
	if unknown.DeliveryOutcome != "unknown" || unknown.Receipt != nil || !sender.ExposurePossible || sender.Lease == nil || !sameTextLease(*sender.Lease, receipt.Lease) || receipt.MessageID != id || receipt.TextBytes != review.TextBytes || receipt.TextSHA256 != review.SHA256 {
		t.Fatal("saved ACK loss did not preserve original lease and unknown sender evidence")
	}
	t.Log("SOBA_TEXT_PHASE receiver-restart")
	peers[0].stop(t)
	if settled := evidence(1); settled.Fault.SavedACKDrops != 1 || settled.Fault.InvalidCapture || settled.Receipt == nil || !reflect.DeepEqual(receipt, *settled.Receipt) {
		t.Fatal("sender stop changed the single lost saved ACK or original receipt")
	}
	peers[1].stop(t)
	// Reopen the receiver first. Its receipt and visible row must survive before
	// a sender is available to query or retransmit anything.
	start(1)
	reopened := evidence(1)
	if reopened.IncomingRows != 1 || reopened.VisibleRows != 1 || !reopened.BodyExact || reopened.Receipt == nil || !reflect.DeepEqual(receipt, *reopened.Receipt) {
		t.Fatal("receiver process restart lost exact saved text or receipt")
	}
	t.Log("SOBA_TEXT_PHASE receipt-recovery")
	peers[1].invoke(t, "fixture.text-fault", map[string]string{"messageId": id, "operation": "observe"})
	start(0)
	await("original saved receipt reconciliation", func() bool { return status().DeliveryOutcome == "confirmed" })
	confirmed := status()
	immutable(confirmed)
	if confirmed.Status != "completed" || confirmed.Receipt == nil || !reflect.DeepEqual(receipt, *confirmed.Receipt) {
		t.Fatal("sender restart did not reconcile original exact receiver receipt")
	}
	immutable(commit())
	if current := evidence(1); current.VisibleRows != 1 || current.IncomingRows != 1 || !current.BodyExact || !current.Fault.Installed || current.Fault.InvalidCapture || current.Fault.StatusRequests < 1 || current.Fault.OfferRequests != 0 || current.Fault.DeliverRequests != 0 || current.Fault.SavedACKDrops != 0 {
		t.Fatal("reconciliation duplicated text or replaced original status inquiry")
	}
	t.Log("SOBA_TEXT_PHASE history-hide")
	// Use the actual policy review/apply and history preview/cleanup commands.
	// Only the synthetic visible message is selected, and evidence must remain.
	policy := catalogProcessDecode[struct {
		Requested capacity.Policy `json:"requested"`
	}](t, peers[1].invoke(t, "policy.config", struct{}{})).Requested
	policy.Logical["messageHistoryBytes"] = capacity.Limited(1)
	policyReview := catalogProcessDecode[struct {
		Revision string `json:"revision"`
	}](t, peers[1].invoke(t, "policy.preview", map[string]any{"policy": policy}))
	peers[1].invoke(t, "policy.apply", map[string]any{"policy": policy, "expectedRevision": policyReview.Revision})
	cleanup := catalogProcessDecode[historyCleanup](t, peers[1].invoke(t, "message.history.preview", struct{}{}))
	if cleanup.Remove != 1 || cleanup.Retained != 0 || len(cleanup.MessageIDs) != 1 || cleanup.MessageIDs[0] != "incoming:"+keys[0]+":"+id {
		t.Fatal("native cleanup review did not select only original received text")
	}
	messageCommand(1, "message.history.cleanup", "", map[string]string{"expectedRevision": cleanup.Revision})
	peers[1].stop(t)
	start(1)
	assertHidden := func() {
		t.Helper()
		value := evidence(1)
		if value.IncomingRows != 1 || value.VisibleRows != 0 || value.BodyRetained || value.RetainedTextBytes != 0 || value.Receipt == nil || !reflect.DeepEqual(receipt, *value.Receipt) {
			t.Fatal("native hidden message reappeared or retained receipt changed")
		}
	}
	assertHidden()
	t.Log("SOBA_TEXT_PHASE hidden-replay")
	for index, operation := range []string{"status", "deliver", "offer", "status"} {
		t.Logf("SOBA_TEXT_REPLAY index=%d operation=%s stage=begin", index, operation)
		value := catalogProcessDecode[DurableTextStatus](t, peers[0].invoke(t, "fixture.text-wire", map[string]string{"messageId": id, "text": body, "operation": operation}))
		if value.State != "received" || value.Receipt == nil || !sameTextLease(value.Lease, receipt.Lease) || !reflect.DeepEqual(receipt, *value.Receipt) {
			t.Fatal("native original-lease replay lost retained receipt")
		}
		assertHidden()
	}
	for _, test := range []struct{ operation, text, code string }{
		{"deliver", "synthetic changed text", "message_conflict"},
		{"wrong-epoch", body, "message_unknown_lease"},
	} {
		reply := peers[0].invokeResult(t, "fixture.text-wire", map[string]string{"messageId": id, "text": test.text, "operation": test.operation})
		if reply.Error != "fixture.text-wire: "+test.code {
			t.Fatal("native invalid text replay was not rejected")
		}
		assertHidden()
	}
	if other := evidence(2); other.IncomingRows != 0 || other.VisibleRows != 0 {
		t.Fatal("unselected native peer received durable text")
	}
	immutable(commit())
	if current := evidence(0); current.OutgoingRows != 1 || current.VisibleRows != 1 || current.Receipt == nil || !reflect.DeepEqual(receipt, *current.Receipt) {
		t.Fatal("native replay changed sender history or saved evidence")
	}
	t.Log("SOBA_TEXT_PHASE child-join")
	for _, p := range peers {
		p.stop(t)
	}
	t.Log("SOBA_TEXT_PHASE complete")
	t.Log("durable_text_native offline_restart=true saved_ack_drops=1 receiver_rows=1 visible_after_cleanup=0 receipt_retained=true replay_rejected=2")
}
