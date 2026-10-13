package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

const textQueueCLIOriginal = "mt1_1791766800000_0123456789abcdef0123456789abcdef"

func textQueueCLITestRecord() core.TextOutboxRecord {
	created, _ := textQueueCLIRequestTime(textQueueCLIOriginal)
	body := "Synthetic text / テスト本文\nhttps://example.invalid/note"
	return core.TextOutboxRecord{Version: 1, Kind: "text_outbox", OutboxID: textQueueCLIOriginal, RequestID: textQueueCLIOriginal, MessageID: textQueueCLIOriginal, PeerID: "peer-a", ProcessID: dispatchCLIProcess, ReviewRevision: dispatchCLIRevision, Admission: "prepared", Status: "prepared", DeliveryOutcome: "not_attempted", Text: body, TextBytes: int64(len(body)), SHA256: textQueueCLIHash(body), CreatedAt: created, ExpiresAt: created.Add(24 * time.Hour), UpdatedAt: created}
}
func textQueueCLITestReceipt(r core.TextOutboxRecord) *core.DurableTextReceipt {
	return &core.DurableTextReceipt{Lease: transfer.DurableLease{Version: 1, DeliveryID: r.MessageID, Digest: textQueueCLIOfferDigest(r), ReceiverEpoch: strings.Repeat("a", 32), Token: strings.Repeat("b", 32), RecipientID: r.PeerID, ExpiresAt: r.ExpiresAt, ReceiptUntil: r.CreatedAt.Add(8*24*time.Hour + 5*time.Minute)}, MessageID: r.MessageID, TextBytes: r.TextBytes, TextSHA256: r.SHA256, SavedAt: r.CreatedAt.Add(time.Second)}
}

type textQueueCLIFixture struct {
	t          *testing.T
	calls      []string
	commands   []webui.Command
	record     core.TextOutboxRecord
	fail       string
	failBefore bool
	failErr    error
	mutate     func(string, *core.TextOutboxRecord)
	peers      []guidedPeer
}

func (f *textQueueCLIFixture) call(ctx context.Context, dir, wire string, target any) error {
	f.t.Helper()
	if err := ctx.Err(); err != nil {
		return err
	}
	if dir != "profile" {
		f.t.Fatal("selected profile changed")
	}
	if wire == "status" {
		f.calls = append(f.calls, "state")
		if f.fail == "state" {
			return errors.New("synthetic-private-cause")
		}
		raw, _ := json.Marshal(map[string]any{"resourceCatalogProcessId": dispatchCLIProcess, "peers": f.peers})
		return json.Unmarshal(raw, target)
	}
	var cmd webui.Command
	if err := json.Unmarshal([]byte(wire), &cmd); err != nil {
		f.t.Fatal(err)
	}
	if !strings.HasPrefix(cmd.Name, "message.queue.") {
		f.t.Fatal("unexpected route")
	}
	action := strings.TrimPrefix(cmd.Name, "message.queue.")
	f.calls, f.commands = append(f.calls, action), append(f.commands, cmd)
	fail := func() error {
		if f.failErr != nil {
			return f.failErr
		}
		return errors.New("synthetic-private-cause")
	}
	if f.fail == action && f.failBefore {
		return fail()
	}
	var p struct {
		ProcessID      string    `json:"processId"`
		PeerID         string    `json:"peerId"`
		Text           string    `json:"text"`
		ExpiresAt      time.Time `json:"expiresAt"`
		OutboxID       string    `json:"outboxId"`
		ReviewRevision string    `json:"reviewRevision"`
		Confirm        bool      `json:"confirm"`
		Cursor         string    `json:"cursor"`
		Revision       string    `json:"revision"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		f.t.Fatal(err)
	}
	if action == "prepare" {
		created, ok := textQueueCLIRequestTime(cmd.RequestID)
		if !ok || p.ProcessID != dispatchCLIProcess || p.PeerID == "" || p.Text == "" || p.ExpiresAt.IsZero() {
			f.t.Fatal("original preparation binding lost")
		}
		f.record = textQueueCLITestRecord()
		f.record.OutboxID, f.record.RequestID, f.record.MessageID = cmd.RequestID, cmd.RequestID, cmd.RequestID
		f.record.PeerID, f.record.Text, f.record.TextBytes, f.record.SHA256 = p.PeerID, p.Text, int64(len(p.Text)), textQueueCLIHash(p.Text)
		f.record.CreatedAt, f.record.UpdatedAt, f.record.ExpiresAt = created, created, p.ExpiresAt
	} else {
		if _, ok := textQueueCLIRequestTime(cmd.RequestID); ok {
			f.t.Fatal("observation created an admission key")
		}
		if action != "list" && p.OutboxID != f.record.OutboxID {
			f.t.Fatal("original ID lost")
		}
		if action == "commit" || action == "discard" {
			if p.ProcessID != dispatchCLIProcess {
				f.t.Fatal("original process lost")
			}
		} else if p.ProcessID != "" {
			f.t.Fatal("durable operation depends on process")
		}
	}
	switch action {
	case "commit":
		if p.ReviewRevision != dispatchCLIRevision || !p.Confirm {
			f.t.Fatal("explicit original review lost")
		}
		f.record.Admission, f.record.Status, f.record.ProcessID = "enqueued", "queued", ""
	case "discard":
		f.record.Status, f.record.Text = "discarded", ""
	case "cancel":
		if f.record.DeliveryOutcome != "confirmed" {
			f.record.Status = "cancelled"
		}
	}
	if f.fail == action {
		return fail()
	}
	result := f.record
	if f.mutate != nil {
		f.mutate(action, &result)
	}
	var raw []byte
	if action == "list" {
		result.Text = ""
		item := textQueueCLIListItem{TextOutboxRecord: result, ID: result.OutboxID}
		raw, _ = json.Marshal(textQueueCLIPage{Version: 1, Kind: "text_outbox", Status: "ready", Items: []textQueueCLIListItem{item}, Revision: dispatchCLIRevision, Total: 1})
	} else {
		raw, _ = json.Marshal(result)
	}
	return json.Unmarshal(raw, target)
}
func textQueueCLIArgs(options ...string) []string {
	return append(append([]string{"--queue", "--peer", "peer-a"}, options...), "Synthetic text / テスト本文\nhttps://example.invalid/note")
}

func TestTextQueueCLIPrepareConfirmAndDecline(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, tc := range []struct {
			args   []string
			answer string
			calls  []string
			status string
		}{
			{textQueueCLIArgs("--json"), "", []string{"state", "prepare"}, "prepared"},
			{textQueueCLIArgs("--confirm", "--json"), "", []string{"state", "prepare", "commit"}, "queued"},
			{textQueueCLIArgs(), "yes\n", []string{"state", "prepare", "commit"}, "queued"},
			{textQueueCLIArgs(), "はい\n", []string{"state", "prepare", "commit"}, "queued"},
			{textQueueCLIArgs(), "no\n", []string{"state", "prepare", "discard"}, "discarded"},
		} {
			f := &textQueueCLIFixture{t: t}
			var out bytes.Buffer
			err := textQueueSendCLI(context.Background(), tc.args, "profile", ja, false, &out, strings.NewReader(tc.answer), f.call)
			if (err != nil) != (tc.status == "discarded") {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(f.calls, tc.calls) || f.record.Status != tc.status {
				t.Fatalf("calls/status: %v %s", f.calls, f.record.Status)
			}
			if strings.Contains(out.String(), "[y/N]") && strings.Contains(strings.Join(tc.args, " "), "--json") {
				t.Fatal("machine flow prompted")
			}
			if f.record.ExpiresAt.Sub(f.record.CreatedAt) != 24*time.Hour {
				t.Fatal("default deadline changed")
			}
			if !strings.Contains(strings.Join(tc.args, " "), "--json") {
				for _, want := range []string{f.record.OutboxID, "peer-a", "テスト本文", text(ja, "local", "ローカル"), "--state-dir", "profile"} {
					if !strings.Contains(out.String(), want) {
						t.Fatalf("missing %q: %s", want, out.String())
					}
				}
			}
		}
	}
}
func TestTextQueueCLIAmbiguousRepliesKeepOriginal(t *testing.T) {
	for _, phase := range []string{"prepare", "commit"} {
		for _, ja := range []bool{false, true} {
			f := &textQueueCLIFixture{t: t, fail: phase}
			var out bytes.Buffer
			err := textQueueSendCLI(context.Background(), textQueueCLIArgs("--confirm", "--json"), "profile", ja, false, &out, strings.NewReader(""), f.call)
			if err == nil || strings.Contains(err.Error(), "synthetic-private-cause") || strings.Contains(out.String(), "synthetic-private-cause") {
				t.Fatal("lost reply accepted or private cause leaked")
			}
			var got struct {
				RequestID, OutboxID, MessageID string
				Observation, Phase             string
				ProcessID, ReviewRevision      string
				Original                       struct {
					PeerID, Text, SHA256 string
					TextBytes            int64
					CreatedAt, ExpiresAt time.Time
				}
			}
			if json.Unmarshal(out.Bytes(), &got) != nil || got.RequestID != f.record.RequestID || got.OutboxID != got.RequestID || got.MessageID != got.RequestID || got.Observation != "unconfirmed" || got.Phase != phase || got.ProcessID != dispatchCLIProcess || got.Original.PeerID != f.record.PeerID || got.Original.Text != f.record.Text || got.Original.SHA256 != f.record.SHA256 || got.Original.TextBytes != f.record.TextBytes || !got.Original.CreatedAt.Equal(f.record.CreatedAt) || !got.Original.ExpiresAt.Equal(f.record.ExpiresAt) {
				t.Fatalf("original recovery context lost: %s", out.String())
			}
			if phase == "commit" && got.ReviewRevision != dispatchCLIRevision {
				t.Fatal("review retry context lost")
			}
			want := []string{"state", "prepare"}
			if phase == "commit" {
				want = append(want, "commit")
			}
			if !reflect.DeepEqual(f.calls, want) {
				t.Fatal("implicit replay")
			}
			f.fail = ""
			out.Reset()
			if err := textQueueCommandCLI(context.Background(), []string{"show", got.OutboxID, "--json"}, "profile", ja, false, &out, f.call); err != nil {
				t.Fatal(err)
			}
			if f.commands[len(f.commands)-1].Name != "message.queue.status" || strings.Contains(string(f.commands[len(f.commands)-1].Payload), "processId") {
				t.Fatal("status did not use process-independent original locator")
			}
		}
	}
}
func TestTextQueueCLIStorageBusyRecoveryUsesSameCommit(t *testing.T) {
	for _, ja := range []bool{false, true} {
		f := &textQueueCLIFixture{t: t, fail: "commit", failBefore: true, failErr: &control.RemoteError{Code: "message_storage_busy"}}
		var out bytes.Buffer
		if err := textQueueSendCLI(context.Background(), textQueueCLIArgs("--confirm"), "profile", ja, false, &out, strings.NewReader(""), f.call); err == nil {
			t.Fatal("busy accepted")
		}
		original := f.record
		first := f.commands[len(f.commands)-1]
		for _, want := range []string{original.OutboxID, original.ExpiresAt.Format(time.RFC3339Nano), "--process", dispatchCLIProcess, "--revision", dispatchCLIRevision, "--state-dir", "profile"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("missing recovery %q", want)
			}
		}
		f.fail = ""
		if err := textQueueCommandCLI(context.Background(), []string{"show", original.OutboxID, "--json"}, "profile", ja, false, io.Discard, f.call); err != nil {
			t.Fatal(err)
		}
		if err := textQueueCommandCLI(context.Background(), []string{"commit", original.OutboxID, "--process", original.ProcessID, "--revision", original.ReviewRevision, "--confirm", "--json"}, "profile", ja, false, io.Discard, f.call); err != nil {
			t.Fatal(err)
		}
		last := f.commands[len(f.commands)-1]
		if string(first.Payload) != string(last.Payload) || !textQueueCLISameReview(original, f.record) || !reflect.DeepEqual(f.calls, []string{"state", "prepare", "commit", "status", "commit"}) {
			t.Fatal("busy retry changed original authority")
		}
	}
}
func TestTextQueueCLIStrictOriginalReview(t *testing.T) {
	for _, tc := range []struct {
		name, phase string
		change      func(*core.TextOutboxRecord)
	}{
		{"request", "prepare", func(r *core.TextOutboxRecord) { r.RequestID = "other" }},
		{"message", "prepare", func(r *core.TextOutboxRecord) { r.MessageID = "other" }},
		{"process", "prepare", func(r *core.TextOutboxRecord) { r.ProcessID = dispatchCLIRevision }},
		{"recipient", "prepare", func(r *core.TextOutboxRecord) { r.PeerID = "other" }},
		{"body", "prepare", func(r *core.TextOutboxRecord) {
			r.Text = "substituted"
			r.TextBytes = int64(len(r.Text))
			r.SHA256 = textQueueCLIHash(r.Text)
		}},
		{"expiry", "prepare", func(r *core.TextOutboxRecord) { r.ExpiresAt = r.ExpiresAt.Add(time.Hour) }},
		{"review", "commit", func(r *core.TextOutboxRecord) { r.ReviewRevision = dispatchCLIProcess }},
		{"hash", "commit", func(r *core.TextOutboxRecord) { r.SHA256 = dispatchCLIProcess; r.Text = "" }},
		{"recipient", "commit", func(r *core.TextOutboxRecord) { r.PeerID = "other" }},
		{"expiry", "commit", func(r *core.TextOutboxRecord) { r.ExpiresAt = r.ExpiresAt.Add(time.Minute) }},
	} {
		t.Run(tc.phase+"/"+tc.name, func(t *testing.T) {
			f := &textQueueCLIFixture{t: t, mutate: func(phase string, r *core.TextOutboxRecord) {
				if phase == tc.phase {
					tc.change(r)
				}
			}}
			if err := textQueueSendCLI(context.Background(), textQueueCLIArgs("--confirm", "--json"), "profile", false, false, io.Discard, strings.NewReader(""), f.call); err == nil {
				t.Fatal("changed review accepted")
			}
			if tc.phase == "prepare" && len(f.calls) != 2 {
				t.Fatal("committed unreviewed text")
			}
		})
	}
}
func TestTextQueueCLIDryRunZeroIPCAndInvalidInputs(t *testing.T) {
	client := func(context.Context, string, string, any) error { t.Fatal("local-only flow called IPC"); return nil }
	for _, args := range [][]string{textQueueCLIArgs(), textQueueCLIArgs("--expires-in", "1m"), textQueueCLIArgs("--expires-in", "168h"), {"--queue", "--peer-name", "Example device", "--json", "--confirm", "text"}, {"--queue", "--peer", "peer-a", "--", "--queue"}} {
		var out bytes.Buffer
		if err := textQueueSendCLI(context.Background(), args, "profile", false, true, &out, strings.NewReader(""), client); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `"applied":false`) || strings.Contains(out.String(), `"requestId"`) || strings.Contains(out.String(), `"processId"`) {
			t.Fatal(out.String())
		}
	}
	for _, args := range [][]string{{"--peer", "peer-a", "text"}, {"--queue", "text"}, {"--queue=false", "--peer", "peer-a", "text"}, {"--queue", "--peer", "peer-a"}, {"--queue", "--peer", "peer-a", "one", "two"}, {"--queue", "--peer", "peer-a", "  \n"}, {"--queue", "--peer", "peer-a", "\x00text"}, {"--queue", "--peer", "peer-a", string([]byte{255})}, {"--queue", "--peer", "peer-a", "--peer", "peer-b", "text"}, {"--queue", "--peer", "peer-a", "--peer-name", "name", "text"}, textQueueCLIArgs("--expires-in", "59s"), textQueueCLIArgs("--expires-in", "169h"), textQueueCLIArgs("--expires-in", "bad")} {
		if err := textQueueSendCLI(context.Background(), args, "profile", false, false, io.Discard, strings.NewReader(""), client); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, args := range [][]string{{"list", "--json"}, {"show", textQueueCLIOriginal, "--json"}, {"status", "--request", textQueueCLIOriginal}, {"cancel", textQueueCLIOriginal}, {"commit", textQueueCLIOriginal, "--process", dispatchCLIProcess, "--revision", dispatchCLIRevision, "--confirm"}, {"discard", textQueueCLIOriginal, "--process", dispatchCLIProcess}, {"list", "--cursor", textQueueCLIOriginal, "--revision", dispatchCLIRevision}} {
		if err := textQueueCommandCLI(context.Background(), args, "profile", false, true, io.Discard, client); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"show"}, {"show", "legacy"}, {"show", textQueueCLIOriginal, "--request", textQueueCLIOriginal}, {"list", textQueueCLIOriginal}, {"list", "--cursor", textQueueCLIOriginal}, {"list", "--revision", dispatchCLIRevision}, {"cancel", textQueueCLIOriginal, "--process", dispatchCLIProcess}, {"cancel", textQueueCLIOriginal, "--delivery", "other"}, {"commit", textQueueCLIOriginal, "--process", dispatchCLIProcess, "--revision", dispatchCLIRevision}, {"discard", textQueueCLIOriginal}, {"show", "--request="}} {
		if err := textQueueCommandCLI(context.Background(), args, "profile", false, false, io.Discard, client); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestTextQueueCLINamedPeerResolution(t *testing.T) {
	for _, peers := range [][]guidedPeer{
		{{ID: "peer-a", Name: "Example device", Verified: true, Online: false}},
		{{ID: "peer-a", Name: "Example device", Verified: false}},
		{{ID: "peer-a", Name: "Example device", Verified: true}, {ID: "peer-b", Name: "Example device", Verified: true}},
		{},
	} {
		f := &textQueueCLIFixture{t: t, peers: peers}
		err := textQueueSendCLI(context.Background(), []string{"--queue", "--peer-name", "Example device", "--json", "text"}, "profile", false, false, io.Discard, strings.NewReader(""), f.call)
		valid := len(peers) == 1 && peers[0].Verified
		if (err == nil) != valid {
			t.Fatal("missing/ambiguous/unverified name accepted or offline verified name rejected", err)
		}
		if valid {
			if f.record.PeerID != "peer-a" || strings.Contains(string(f.commands[0].Payload), "peerName") {
				t.Fatal("name remained in authority")
			}
		} else if !reflect.DeepEqual(f.calls, []string{"state"}) {
			t.Fatal("invalid name reached prepare")
		}
	}
}
func TestTextQueueCLIStrictDTOs(t *testing.T) {
	r := textQueueCLITestRecord()
	raw, _ := json.Marshal(r)
	valid := string(raw)
	for _, bad := range []string{
		strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(valid, `"version":1`, `"Version":1`, 1),
		strings.Replace(valid, `"version":1`, `"version":1,"unexpected":true`, 1),
		strings.Replace(valid, `"reasonCode":"",`, "", 1),
		strings.Replace(valid, `"textBytes":`+strconvForTest(r.TextBytes)+",", "", 1),
		strings.Replace(valid, `"text":`, `"text":null,"extra":`, 1),
		strings.Replace(valid, `"kind":"text_outbox"`, `"kind":"outbox"`, 1),
		valid + `{}`,
	} {
		if _, err := textQueueCLIDecode([]byte(bad), r.ProcessID, r.OutboxID, false, false); err == nil {
			t.Fatalf("accepted malformed DTO %s", bad)
		}
	}
	for _, change := range []func(*core.TextOutboxRecord){
		func(r *core.TextOutboxRecord) { r.TextBytes++ }, func(r *core.TextOutboxRecord) { r.SHA256 = strings.Repeat("a", 64) }, func(r *core.TextOutboxRecord) { r.Text = "" },
		func(r *core.TextOutboxRecord) { r.Status = "waiting_for_peer" }, func(r *core.TextOutboxRecord) { r.DeliveryOutcome = "partial" }, func(r *core.TextOutboxRecord) { r.CreatedAt = r.CreatedAt.Add(time.Millisecond) },
		func(r *core.TextOutboxRecord) { r.ExpiresAt = r.CreatedAt.Add(7*24*time.Hour + time.Millisecond) }, func(r *core.TextOutboxRecord) { r.ExpiresAt = r.CreatedAt.Add(59 * time.Second) },
		func(r *core.TextOutboxRecord) { r.Admission = "enqueued"; r.Status = "queued" }, func(r *core.TextOutboxRecord) { r.Admission = "enqueued"; r.ProcessID = ""; r.Status = "completed" },
	} {
		r := textQueueCLITestRecord()
		change(&r)
		raw, _ := json.Marshal(r)
		if _, err := textQueueCLIDecode(raw, "", r.OutboxID, false, false); err == nil {
			t.Fatal("inconsistent DTO accepted")
		}
	}
}
func strconvForTest(value int64) string { raw, _ := json.Marshal(value); return string(raw) }
func TestTextQueueCLIExactReceiptAndHistoryCleanup(t *testing.T) {
	r := textQueueCLITestRecord()
	r.Admission, r.Status, r.ProcessID, r.DeliveryOutcome = "enqueued", "completed", "", "confirmed"
	r.Receipt = textQueueCLITestReceipt(r)
	r.Text = ""
	raw, _ := json.Marshal(r)
	if _, err := textQueueCLIDecode(raw, "", r.OutboxID, false, false); err != nil {
		t.Fatal("body-free exact saved evidence rejected", err)
	}
	for _, change := range []func(*core.TextOutboxRecord){
		func(r *core.TextOutboxRecord) { r.Receipt = nil }, func(r *core.TextOutboxRecord) { r.DeliveryOutcome = "unknown" },
		func(r *core.TextOutboxRecord) { r.Receipt.MessageID = "other" }, func(r *core.TextOutboxRecord) { r.Receipt.TextBytes++ }, func(r *core.TextOutboxRecord) { r.Receipt.TextSHA256 = dispatchCLIProcess },
		func(r *core.TextOutboxRecord) { r.Receipt.Lease.RecipientID = "other" }, func(r *core.TextOutboxRecord) { r.Receipt.Lease.Digest = dispatchCLIProcess }, func(r *core.TextOutboxRecord) { r.Receipt.Lease.ReceiverEpoch = "invalid" },
		func(r *core.TextOutboxRecord) { r.Receipt.Lease.ExpiresAt = r.ExpiresAt.Add(time.Second) }, func(r *core.TextOutboxRecord) { r.Receipt.Lease.ReceiptUntil = r.ExpiresAt.Add(24 * time.Hour) }, func(r *core.TextOutboxRecord) { r.Receipt.SavedAt = time.Time{} },
	} {
		candidate := r
		receipt := *r.Receipt
		candidate.Receipt = &receipt
		change(&candidate)
		raw, _ := json.Marshal(candidate)
		if _, err := textQueueCLIDecode(raw, "", r.OutboxID, false, false); err == nil {
			t.Fatal("nonexact receipt accepted")
		}
	}
	raw, _ = json.Marshal(r)
	valid := string(raw)
	for _, bad := range []string{strings.Replace(valid, `"savedAt":`, `"SavedAt":`, 1), strings.Replace(valid, `"token":`, `"unexpected":"value","token":`, 1), strings.Replace(valid, `"textBytes":`, `"textBytes":0,"textBytes":`, 1)} {
		if _, err := textQueueCLIDecode([]byte(bad), "", r.OutboxID, false, false); err == nil {
			t.Fatal("malformed nested receipt accepted")
		}
	}
}
func TestTextQueueCLICancelKeepsOutcomeAndRequiresFence(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, outcome := range []string{"unknown", "confirmed", "not_attempted"} {
			r := textQueueCLITestRecord()
			r.ProcessID, r.Admission, r.Status, r.DeliveryOutcome = "", "enqueued", "waiting_peer", outcome
			if outcome == "confirmed" {
				r.Status = "completed"
				r.Receipt = textQueueCLITestReceipt(r)
			}
			f := &textQueueCLIFixture{t: t, record: r}
			var out bytes.Buffer
			if err := textQueueCommandCLI(context.Background(), []string{"cancel", r.OutboxID}, "profile", ja, false, &out, f.call); err != nil {
				t.Fatal(err)
			}
			if f.record.DeliveryOutcome != outcome || !strings.Contains(out.String(), outboxCLIOutcomeLabel(ja, outcome)) || !strings.Contains(out.String(), text(ja, "does not recall", "回収しません")) {
				t.Fatal("cancellation lost delivery evidence")
			}
			if len(f.calls) != 1 || f.calls[0] != "cancel" {
				t.Fatal("cancel used implicit process/status calls")
			}
		}
	}
	for _, status := range []string{"queued", "waiting_peer", "delivering", "paused", "blocked", "needs_review", "prepared"} {
		r := textQueueCLITestRecord()
		r.ProcessID, r.Admission, r.Status = "", "enqueued", "queued"
		f := &textQueueCLIFixture{t: t, record: r, mutate: func(action string, r *core.TextOutboxRecord) {
			if action == "cancel" {
				r.Status = status
			}
		}}
		if err := textQueueCommandCLI(context.Background(), []string{"cancel", r.OutboxID, "--json"}, "profile", false, false, io.Discard, f.call); err == nil {
			t.Fatal("unfenced cancellation accepted", status)
		}
	}
}
func TestTextQueueCLIListContract(t *testing.T) {
	r := textQueueCLITestRecord()
	r.Text = ""
	page := textQueueCLIPage{Version: 1, Kind: "text_outbox", Status: "ready", Items: []textQueueCLIListItem{{TextOutboxRecord: r, ID: r.OutboxID}}, Revision: dispatchCLIRevision, Total: 1}
	for _, status := range []string{"ready", "blocked"} {
		candidate := page
		candidate.Status = status
		if status == "blocked" {
			candidate.ReasonCode = "message_storage_uncertain"
		}
		raw, _ := json.Marshal(candidate)
		if _, err := textQueueCLIListDecode(raw, "", "", false); err != nil {
			t.Fatal(err)
		}
	}
	for _, change := range []func(*textQueueCLIPage){
		func(p *textQueueCLIPage) { p.Items[0].Text = "body forbidden" }, func(p *textQueueCLIPage) { p.Items[0].ID = "other" }, func(p *textQueueCLIPage) { p.Total = 0 },
		func(p *textQueueCLIPage) { p.NextCursor = "other" }, func(p *textQueueCLIPage) { p.Status = "disabled" }, func(p *textQueueCLIPage) { p.Status = "blocked" }, func(p *textQueueCLIPage) { p.Revision = "" },
		func(p *textQueueCLIPage) { p.Items = nil }, func(p *textQueueCLIPage) { p.Items = append(p.Items, p.Items[0]); p.Total = 2 },
	} {
		candidate := page
		candidate.Items = append([]textQueueCLIListItem(nil), page.Items...)
		change(&candidate)
		raw, _ := json.Marshal(candidate)
		if _, err := textQueueCLIListDecode(raw, "", "", false); err == nil {
			t.Fatal("invalid page accepted")
		}
	}
	page.Total, page.NextCursor = 2, r.OutboxID
	raw, _ := json.Marshal(page)
	if _, err := textQueueCLIListDecode(raw, "", "", false); err != nil {
		t.Fatal("continuation page rejected", err)
	}
	if _, err := textQueueCLIListDecode(raw, r.OutboxID, dispatchCLIRevision, false); err == nil {
		t.Fatal("repeated page admitted")
	}
	if _, err := textQueueCLIListDecode(raw, "", dispatchCLIProcess, false); err == nil {
		t.Fatal("changed page revision admitted")
	}
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		client := func(_ context.Context, _, _ string, target any) error { return json.Unmarshal(raw, target) }
		if err := textQueueCommandCLI(context.Background(), []string{"list"}, "profile", ja, false, &out, client); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"--cursor", r.OutboxID, "--revision", dispatchCLIRevision, "--state-dir", "profile"} {
			if !strings.Contains(out.String(), want) {
				t.Fatal("pagination guidance lost")
			}
		}
		if strings.Contains(out.String(), "--confirm") {
			t.Fatal("body-free list offered commit without review")
		}
	}
}
func TestTextQueueCLIIDsAndLifetimeEdges(t *testing.T) {
	created := time.UnixMilli(1791766800000)
	seen := map[string]bool{}
	for range 50 {
		id, err := newTextQueueCLIRequestID(created)
		parsed, ok := textQueueCLIRequestTime(id)
		if err != nil || !ok || !parsed.Equal(created) || seen[id] {
			t.Fatal("invalid or repeated ID")
		}
		seen[id] = true
	}
	for _, id := range []string{"", "legacy", strings.Replace(textQueueCLIOriginal, "mt1", "mt2", 1), strings.ToUpper(textQueueCLIOriginal), textQueueCLIOriginal + "0", textQueueCLIOriginal[:49], "mt1_0791766800000_0123456789abcdef0123456789abcdef", "mt1_9999999999999_0123456789abcdef0123456789abcdef"} {
		if _, ok := textQueueCLIRequestTime(id); ok {
			t.Fatal("noncanonical ID accepted")
		}
	}
	if _, ok := textQueueCLIRequestTime("mt1_1000000000000_0123456789abcdef0123456789abcdef"); !ok {
		t.Fatal("historical observation rejected")
	}
	for _, duration := range []string{"1m", "168h"} {
		synctest.Test(t, func(t *testing.T) {
			// The default synctest epoch predates canonical 13-digit request IDs.
			time.Sleep(created.Sub(time.Now()))
			f := &textQueueCLIFixture{t: t}
			if err := textQueueSendCLI(context.Background(), textQueueCLIArgs("--expires-in", duration, "--json"), "profile", false, false, io.Discard, strings.NewReader(""), f.call); err != nil {
				t.Fatal(err)
			}
			lifetime, _ := time.ParseDuration(duration)
			keyTime, _ := textQueueCLIRequestTime(f.record.OutboxID)
			if !f.record.CreatedAt.Equal(keyTime) || !f.record.ExpiresAt.Equal(keyTime.Add(lifetime)) {
				t.Fatal("deadline did not bind original key time")
			}
		})
	}
}
func TestTextQueueCLIOutputErrorsAndCanceledContext(t *testing.T) {
	f := &textQueueCLIFixture{t: t}
	if err := textQueueSendCLI(context.Background(), textQueueCLIArgs("--confirm"), "profile", false, false, dispatchCLIRejectWriter{}, strings.NewReader(""), f.call); err == nil || !reflect.DeepEqual(f.calls, []string{"state", "prepare"}) {
		t.Fatal("unseen review committed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f = &textQueueCLIFixture{t: t}
	if err := textQueueSendCLI(ctx, textQueueCLIArgs("--confirm"), "profile", false, false, io.Discard, strings.NewReader(""), f.call); !errors.Is(err, context.Canceled) || len(f.calls) != 0 {
		t.Fatal("cancelled call reached IPC")
	}
	f = &textQueueCLIFixture{t: t, fail: "state"}
	if err := textQueueSendCLI(context.Background(), textQueueCLIArgs("--confirm"), "profile", false, false, io.Discard, strings.NewReader(""), f.call); err == nil || !reflect.DeepEqual(f.calls, []string{"state"}) {
		t.Fatal("stopped agent reached prepare")
	}
}
func TestTextQueueCLILocalesRoutingAndMachineJSON(t *testing.T) {
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "")
	for _, tc := range []struct{ locale, want string }{{"ja_JP.UTF-8", "テキストを今すぐ"}, {"en_US.UTF-8", "Send text now"}, {"zz_UNKNOWN", "Send text now"}} {
		t.Setenv("LC_ALL", tc.locale)
		for _, args := range [][]string{{"message", "--help"}, {"help", "message"}, {"message", "queue", "--help"}, {"message", "--queue", "--help"}} {
			var out bytes.Buffer
			client := func(context.Context, string, string, any) error { t.Fatal("help IPC"); return nil }
			if err := runWith(context.Background(), args, &out, strings.NewReader(""), client); err != nil || !strings.Contains(out.String(), tc.want) {
				t.Fatal(err, out.String())
			}
		}
	}
	var previous string
	for _, locale := range []string{"en", "ja"} {
		f := &textQueueCLIFixture{t: t, record: textQueueCLITestRecord()}
		var out bytes.Buffer
		if err := runWith(context.Background(), []string{"--state-dir", "profile", "--locale", locale, "message", "queue", "show", textQueueCLIOriginal, "--json"}, &out, strings.NewReader(""), f.call); err != nil {
			t.Fatal(err)
		}
		if previous != "" && previous != out.String() {
			t.Fatal("machine JSON localized")
		}
		previous = out.String()
	}
	var out bytes.Buffer
	client := func(_ context.Context, _, wire string, target any) error {
		var command webui.Command
		if json.Unmarshal([]byte(wire), &command) != nil || command.Name != "message.send" || strings.Contains(wire, "message.queue") {
			t.Fatal("immediate send silently queued")
		}
		return json.Unmarshal([]byte(`{"ok":true}`), target)
	}
	if err := runWith(context.Background(), []string{"message", "peer-a", "--queue"}, &out, strings.NewReader(""), client); err != nil {
		t.Fatal(err)
	}
	for _, locale := range []string{"en", "ja"} {
		f := &textQueueCLIFixture{t: t}
		out.Reset()
		args := append([]string{"--state-dir", "profile", "--locale", locale, "message"}, textQueueCLIArgs("--json")...)
		if err := runWith(context.Background(), args, &out, strings.NewReader(""), f.call); err != nil || !reflect.DeepEqual(f.calls, []string{"state", "prepare"}) {
			t.Fatal("queue routing failed", err)
		}
	}
}
