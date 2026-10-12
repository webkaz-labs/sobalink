package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

const outboxCLIOriginal = "ob1_1791766800000_0123456789abcdef0123456789abcdef"

type outboxCLIFixture struct {
	t        *testing.T
	calls    []string
	record   outboxCLIRecord
	fail     string
	mutate   func(string, *outboxCLIRecord)
	commands []webui.Command
}

func outboxCLITestRecord() outboxCLIRecord {
	return outboxCLIRecord{Version: 1, Kind: "outbox", OutboxID: outboxCLIOriginal, RequestID: outboxCLIOriginal, ReviewRevision: dispatchCLIRevision, ProcessID: dispatchCLIProcess, Admission: "prepared", Status: "prepared", DeliveryOutcome: "not_attempted", PeerIDs: []string{"peer-a", "peer-b"}, Entries: []transfer.Entry{{ID: "file", Path: "example.txt", Kind: transfer.File, Size: 3, SHA256: dispatchCLIRevision}}, TotalBytes: 3, CreatedAt: "2026-10-12T01:00:00Z", ExpiresAt: "2026-10-13T01:00:00Z", UpdatedAt: "2026-10-12T01:00:00Z", Children: []outboxCLIChild{{PeerID: "peer-a", DeliveryID: "delivery-a", Status: "prepared", DeliveryOutcome: "not_attempted", Files: []outboxCLIFile{}}, {PeerID: "peer-b", DeliveryID: "delivery-b", Status: "prepared", DeliveryOutcome: "not_attempted", Files: []outboxCLIFile{}}}}
}
func (f *outboxCLIFixture) call(ctx context.Context, dir, wire string, target any) error {
	f.t.Helper()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if dir != "profile" {
		f.t.Fatalf("profile lost: %q", dir)
	}
	if wire == "status" {
		f.calls = append(f.calls, "state")
		return json.Unmarshal([]byte(`{"resourceCatalogProcessId":"`+dispatchCLIProcess+`"}`), target)
	}
	var cmd webui.Command
	if err := json.Unmarshal([]byte(wire), &cmd); err != nil {
		f.t.Fatal(err)
	}
	f.commands = append(f.commands, cmd)
	action := strings.TrimPrefix(cmd.Name, "outbox.")
	f.calls = append(f.calls, action)
	if action != "prepare" && outboxCLIValidRequestID(cmd.RequestID) {
		f.t.Fatal("non-prepare command generated an admission key")
	}
	var p struct {
		ProcessID      string   `json:"processId"`
		OutboxID       string   `json:"outboxId"`
		RequestID      string   `json:"requestId"`
		ReviewRevision string   `json:"reviewRevision"`
		PeerIDs        []string `json:"peerIds"`
		Paths          []string `json:"paths"`
		ExpiresAt      string   `json:"expiresAt"`
		DeliveryID     string   `json:"deliveryId"`
		Confirm        bool     `json:"confirm"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		f.t.Fatal(err)
	}
	if action == "prepare" {
		if !outboxCLIValidRequestID(cmd.RequestID) {
			f.t.Fatal("prepare request is not canonical")
		}
		if p.ProcessID != dispatchCLIProcess || len(p.Paths) != 1 || !filepath.IsAbs(p.Paths[0]) {
			f.t.Fatal("preparation binding lost")
		}
		f.record = outboxCLITestRecord()
		f.record.OutboxID, f.record.RequestID = cmd.RequestID, cmd.RequestID
		f.record.PeerIDs = p.PeerIDs
		f.record.ExpiresAt = p.ExpiresAt
		f.record.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		f.record.UpdatedAt = f.record.CreatedAt
		f.record.Children = []outboxCLIChild{}
		for _, peer := range p.PeerIDs {
			f.record.Children = append(f.record.Children, outboxCLIChild{PeerID: peer, DeliveryID: "delivery-" + peer, Status: "prepared", DeliveryOutcome: "not_attempted", Files: []outboxCLIFile{}})
		}
	} else if action != "list" {
		if p.OutboxID != f.record.OutboxID && p.RequestID != f.record.RequestID {
			f.t.Fatal("original locator lost")
		}
		if action == "commit" || action == "discard" {
			if p.ProcessID != dispatchCLIProcess {
				f.t.Fatal("process lost")
			}
		} else if p.ProcessID != "" {
			f.t.Fatal("durable observation requires process")
		}
		if action == "commit" && (!p.Confirm || p.ReviewRevision != dispatchCLIRevision) {
			f.t.Fatal("review approval lost")
		}
	}
	if action == "commit" {
		f.record.Admission, f.record.Status, f.record.ProcessID = "enqueued", "queued", ""
		for i := range f.record.Children {
			f.record.Children[i].Status = "waiting_for_peer"
		}
	}
	if action == "discard" {
		f.record.Status = "discarded"
		for i := range f.record.Children {
			f.record.Children[i].Status = "discarded"
		}
	}
	if action == "cancel" {
		f.record.Status = "cancelled"
		for i := range f.record.Children {
			if p.DeliveryID == "" || p.DeliveryID == f.record.Children[i].DeliveryID {
				f.record.Children[i].Status = "cancelled"
			}
		}
	}
	if f.fail == action {
		return errors.New("private-fixture-path-must-not-leak")
	}
	var r outboxCLIRecord
	raw, _ := json.Marshal(f.record)
	_ = json.Unmarshal(raw, &r)
	if f.mutate != nil {
		f.mutate(action, &r)
	}
	if action == "list" {
		raw, _ = json.Marshal(outboxCLIList{Version: 1, Kind: "outbox", Status: "ready", Items: []outboxCLIRecord{r}})
	} else {
		raw, _ = json.Marshal(r)
	}
	return json.Unmarshal(raw, target)
}
func outboxCLIArgs(options ...string) []string {
	args := []string{"--queue", "--peer", "peer-b", "--peer", "peer-a"}
	args = append(args, options...)
	return append(args, "example.txt")
}
func TestOutboxCLIConsentAndJSON(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, tc := range []struct {
			name, answer string
			options      []string
			want         []string
			cancel       bool
		}{
			{"yes", "y\n", nil, []string{"state", "prepare", "commit"}, false},
			{"decline", "n\n", nil, []string{"state", "prepare", "discard"}, true},
			{"eof", "", nil, []string{"state", "prepare", "discard"}, true},
			{"explicit", "", []string{"--confirm"}, []string{"state", "prepare", "commit"}, false},
			{"json-prepare", "y\n", []string{"--json"}, []string{"state", "prepare"}, false},
			{"json-confirm", "", []string{"--json", "--confirm"}, []string{"state", "prepare", "commit"}, false},
		} {
			t.Run(tc.name+text(ja, "-en", "-ja"), func(t *testing.T) {
				f := &outboxCLIFixture{t: t}
				var out bytes.Buffer
				err := outboxSendCLI(context.Background(), outboxCLIArgs(tc.options...), "profile", ja, false, &out, strings.NewReader(tc.answer), f.call)
				if !reflect.DeepEqual(f.calls, tc.want) {
					t.Fatalf("calls %v want %v", f.calls, tc.want)
				}
				if tc.cancel {
					if !errors.Is(err, errPromptCanceled) {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if hasFlag(tc.options, "json") {
					var r outboxCLIRecord
					d := json.NewDecoder(&out)
					if d.Decode(&r) != nil {
						t.Fatal("invalid JSON")
					}
					if d.Decode(new(any)) != io.EOF {
						t.Fatal("extra output/prompt")
					}
					if r.OutboxID != f.record.OutboxID || r.ExpiresAt != f.record.ExpiresAt {
						t.Fatal("review lost")
					}
				} else {
					for _, want := range []string{"example.txt", "peer-a", "peer-b", "--state-dir profile outbox show " + f.record.OutboxID, text(ja, "deadline", "期限"), text(ja, "after restart", "再起動後")} {
						if !strings.Contains(out.String(), want) {
							t.Fatalf("missing %q: %s", want, out.String())
						}
					}
				}
			})
		}
	}
}
func TestOutboxCLIUncertaintyKeepsOriginalRequest(t *testing.T) {
	for _, phase := range []string{"prepare", "commit", "discard", "cancel"} {
		for _, ja := range []bool{false, true} {
			f := &outboxCLIFixture{t: t, fail: phase, record: outboxCLITestRecord()}
			var out bytes.Buffer
			var err error
			if phase == "discard" {
				err = outboxCommandCLI(context.Background(), []string{"discard", outboxCLIOriginal, "--process", dispatchCLIProcess, "--json"}, "profile", ja, false, &out, f.call)
			} else if phase == "cancel" {
				f.record.Admission, f.record.Status, f.record.ProcessID = "enqueued", "queued", ""
				err = outboxCommandCLI(context.Background(), []string{"cancel", outboxCLIOriginal, "--json"}, "profile", ja, false, &out, f.call)
			} else {
				err = outboxSendCLI(context.Background(), outboxCLIArgs("--json", "--confirm"), "profile", ja, false, &out, strings.NewReader(""), f.call)
			}
			if err == nil || strings.Contains(err.Error(), "private-fixture") || strings.Contains(out.String(), "private-fixture") {
				t.Fatal("unsafe or missing error")
			}
			var observation struct{ OutboxID, RequestID, Observation, Code, Phase string }
			if json.Unmarshal(out.Bytes(), &observation) != nil || observation.OutboxID != f.record.OutboxID || observation.RequestID != f.record.RequestID || observation.Observation != "unconfirmed" || observation.Code != "outbox_control_unavailable" || observation.Phase != phase {
				t.Fatal(out.String())
			}
			if len(f.calls) > 3 {
				t.Fatal("implicit replay")
			}
		}
	}
}
func TestOutboxCLIFullReviewBinding(t *testing.T) {
	changes := []struct {
		name, phase string
		change      func(*outboxCLIRecord)
	}{
		{"wrong-request", "prepare", func(r *outboxCLIRecord) { r.RequestID = "other" }},
		{"wrong-process", "prepare", func(r *outboxCLIRecord) { r.ProcessID = dispatchCLIRevision }},
		{"wrong-peers", "prepare", func(r *outboxCLIRecord) { r.PeerIDs = []string{"other"} }},
		{"wrong-deadline", "prepare", func(r *outboxCLIRecord) { r.ExpiresAt = "2027-01-01T00:00:00Z" }},
		{"wrong-bytes", "prepare", func(r *outboxCLIRecord) { r.TotalBytes++ }},
		{"changed-revision", "commit", func(r *outboxCLIRecord) { r.ReviewRevision = dispatchCLIProcess }},
		{"changed-content", "commit", func(r *outboxCLIRecord) { r.Entries[0].SHA256 = dispatchCLIProcess }},
		{"changed-path", "commit", func(r *outboxCLIRecord) { r.Entries[0].Path = "other.txt" }},
		{"changed-deadline", "commit", func(r *outboxCLIRecord) { r.ExpiresAt = "2027-01-01T00:00:00Z" }},
		{"changed-delivery", "commit", func(r *outboxCLIRecord) { r.Children[0].DeliveryID = "new-delivery" }},
		{"changed-peers", "commit", func(r *outboxCLIRecord) { r.PeerIDs[0] = "other"; r.Children[0].PeerID = "other" }},
	}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			f := &outboxCLIFixture{t: t, mutate: func(action string, r *outboxCLIRecord) {
				if action == tc.phase {
					tc.change(r)
				}
			}}
			var out bytes.Buffer
			if err := outboxSendCLI(context.Background(), outboxCLIArgs("--confirm", "--json"), "profile", false, false, &out, strings.NewReader(""), f.call); err == nil {
				t.Fatal("mismatch accepted")
			}
			if tc.phase == "prepare" && len(f.calls) != 2 {
				t.Fatal("committed unreviewed data")
			}
		})
	}
}
func TestOutboxCLIDryRunAndInvalidInputs(t *testing.T) {
	client := func(context.Context, string, string, any) error { t.Fatal("local validation called IPC"); return nil }
	for _, opts := range [][]string{nil, {"--expires-in", "1m"}, {"--expires-in", "168h"}, {"--json", "--confirm"}} {
		var out bytes.Buffer
		if err := outboxSendCLI(context.Background(), outboxCLIArgs(opts...), "profile", false, true, &out, strings.NewReader(""), client); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `"applied":false`) {
			t.Fatal(out.String())
		}
	}
	for _, args := range [][]string{{"--queue", "--peer", "same", "--peer", "same", "file"}, {"--queue", "file"}, {"--queue", "--peer", "peer"}, {"--queue=false", "--peer", "peer", "file"}, outboxCLIArgs("--expires-in", "59s"), outboxCLIArgs("--expires-in", "169h"), outboxCLIArgs("--expires-in", "wrong")} {
		var out bytes.Buffer
		if err := outboxSendCLI(context.Background(), args, "profile", false, false, &out, strings.NewReader(""), client); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, args := range [][]string{{"list", "--json"}, {"show", outboxCLIOriginal, "--json"}, {"show", "--request", outboxCLIOriginal, "--json"}, {"cancel", outboxCLIOriginal, "--delivery", "child"}, {"commit", outboxCLIOriginal, "--process", dispatchCLIProcess, "--revision", dispatchCLIRevision, "--confirm"}, {"discard", outboxCLIOriginal, "--process", dispatchCLIProcess}} {
		var out bytes.Buffer
		if err := outboxCommandCLI(context.Background(), args, "profile", false, true, &out, client); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"show"}, {"show", outboxCLIOriginal, "--request", "other"}, {"show", "--request="}, {"list", outboxCLIOriginal}, {"cancel", outboxCLIOriginal, "--process", dispatchCLIProcess}, {"commit", outboxCLIOriginal, "--process", dispatchCLIProcess, "--revision", dispatchCLIRevision}, {"discard", outboxCLIOriginal}, {"cancel", outboxCLIOriginal, "--delivery="}} {
		var out bytes.Buffer
		if err := outboxCommandCLI(context.Background(), args, "profile", false, false, &out, client); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestOutboxCLIDurableStatusCancelAndRecovery(t *testing.T) {
	for _, args := range [][]string{{"show", outboxCLIOriginal, "--json"}, {"show", "--request", outboxCLIOriginal, "--json"}, {"list", "--json"}, {"cancel", outboxCLIOriginal, "--json"}, {"cancel", outboxCLIOriginal, "--delivery", "delivery-a", "--json"}, {"commit", outboxCLIOriginal, "--process", dispatchCLIProcess, "--revision", dispatchCLIRevision, "--confirm", "--json"}, {"discard", outboxCLIOriginal, "--process", dispatchCLIProcess, "--json"}} {
		f := &outboxCLIFixture{t: t, record: outboxCLITestRecord()}
		var out bytes.Buffer
		if args[0] != "commit" && args[0] != "discard" {
			f.record.ProcessID, f.record.Admission, f.record.Status = "", "enqueued", "waiting_for_peer"
			for i := range f.record.Children {
				f.record.Children[i].Status = "waiting_for_peer"
			}
		}
		if err := outboxCommandCLI(context.Background(), args, "profile", false, false, &out, f.call); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if len(f.calls) != 1 {
			t.Fatal("process lookup or repeat")
		}
	}
}
func TestOutboxCLIRejectsAmbiguousReceipts(t *testing.T) {
	r := outboxCLITestRecord()
	raw, _ := json.Marshal(r)
	if _, err := outboxCLIDecode(raw, dispatchCLIProcess, outboxCLIOriginal, false); err != nil {
		t.Fatal(err)
	}
	valid := string(raw)
	for _, bad := range []string{
		strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(valid, `"version":1`, `"Version":1`, 1),
		strings.Replace(valid, `"version":1`, `"version":1,"unexpected":true`, 1),
		strings.Replace(valid, `"totalBytes":3,`, "", 1),
		strings.Replace(valid, `"children":[`, `"children":null,"extra":[`, 1),
		strings.Replace(valid, `"kind":"outbox"`, `"kind":"dispatch"`, 1),
		valid + `{}`,
	} {
		if _, err := outboxCLIDecode([]byte(bad), dispatchCLIProcess, outboxCLIOriginal, false); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
func TestOutboxCLIOutputFailureAndCancelledContext(t *testing.T) {
	f := &outboxCLIFixture{t: t}
	if err := outboxSendCLI(context.Background(), outboxCLIArgs("--confirm"), "profile", false, false, dispatchCLIRejectWriter{}, strings.NewReader(""), f.call); err == nil {
		t.Fatal("lost output failure")
	}
	if !reflect.DeepEqual(f.calls, []string{"state", "prepare"}) {
		t.Fatal("committed unseen review")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f = &outboxCLIFixture{t: t}
	if err := outboxSendCLI(ctx, outboxCLIArgs("--confirm"), "profile", false, false, io.Discard, strings.NewReader(""), f.call); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatal("called after cancellation")
	}
}
func TestOutboxCLIRoutingHelpAndMachineLocale(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		for _, args := range [][]string{{"outbox", "--help"}, {"help", "outbox"}, {"send", "--queue", "--help"}, {"send", "--help"}} {
			var out bytes.Buffer
			client := func(context.Context, string, string, any) error { t.Fatal("help IPC"); return nil }
			if err := runWith(context.Background(), append([]string{"--locale", locale}, args...), &out, strings.NewReader(""), client); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "--queue") {
				t.Fatal(out.String())
			}
		}
		f := &outboxCLIFixture{t: t}
		var out bytes.Buffer
		args := append([]string{"--locale", locale, "--state-dir", "profile", "send"}, outboxCLIArgs("--json")...)
		if err := runWith(context.Background(), args, &out, strings.NewReader(""), f.call); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.calls, []string{"state", "prepare"}) {
			t.Fatal(f.calls)
		}
	}
	for _, args := range [][]string{{"peer", "--queue"}, {"--peer", "peer", "--", "--queue"}, {"--peer", "--queue", "file"}, {"--peer", "peer", "file", "--queue"}} {
		if outboxCLIWantsQueue(args) {
			t.Fatalf("path/value selected queue: %v", args)
		}
	}
}
func TestOutboxCLICancellationPreservesDeliveryEvidence(t *testing.T) {
	for _, ja := range []bool{false, true} {
		r := outboxCLITestRecord()
		r.Admission, r.Status, r.ProcessID, r.DeliveryOutcome = "enqueued", "cancelled", "", "partial"
		r.Children[0].Status, r.Children[0].DeliveryOutcome = "completed", "confirmed"
		r.Children[0].Files = []outboxCLIFile{{BatchID: r.Children[0].DeliveryID, FileID: "file", Size: 3, SHA256: dispatchCLIRevision}}
		r.Children[1].Status, r.Children[1].DeliveryOutcome = "cancelled", "unknown"
		raw, _ := json.Marshal(r)
		if _, err := outboxCLIDecode(raw, "", outboxCLIOriginal, ja); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := outboxCLIShow(&out, "profile", ja, r); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{text(ja, "Further sends stopped", "追加の送信を停止済み"), text(ja, "May already have arrived", "すでに届いている可能性があります"), text(ja, "Saved file", "保存済みファイル"), text(ja, "remain", "残ります")} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("missing %q", want)
			}
		}
	}
}

func TestOutboxCLIListStatesAndErrors(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, value := range []string{
			`{"version":1,"kind":"outbox","status":"ready","reasonCode":"","items":[]}`,
			`{"version":1,"kind":"outbox","status":"blocked_recovery","reasonCode":"outbox_recovery_required","items":[]}`,
		} {
			var out bytes.Buffer
			client := func(_ context.Context, _, _ string, target any) error { return json.Unmarshal([]byte(value), target) }
			if err := outboxCommandCLI(context.Background(), []string{"list"}, "profile", ja, false, &out, client); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, bad := range []string{
		`{"version":1,"kind":"outbox","status":"disabled","reasonCode":"","items":[]}`,
		`{"version":1,"kind":"outbox","enabled":false,"status":"ready","reasonCode":"","items":[]}`,
		`{"version":1,"kind":"outbox","status":"ready","reasonCode":"","items":null}`,
	} {
		if _, err := outboxCLIListDecode([]byte(bad), false); err == nil {
			t.Fatal("invalid list accepted")
		}
	}
}

func TestOutboxCLIRejectsUnfencedCancelAndInconsistentEvidence(t *testing.T) {
	f := &outboxCLIFixture{t: t, record: outboxCLITestRecord(), mutate: func(action string, r *outboxCLIRecord) {
		if action == "cancel" {
			r.Children[0].Status = "sending"
		}
	}}
	f.record.Admission, f.record.Status, f.record.ProcessID = "enqueued", "queued", ""
	for i := range f.record.Children {
		f.record.Children[i].Status = "queued"
	}
	if err := outboxCommandCLI(context.Background(), []string{"cancel", outboxCLIOriginal, "--json"}, "profile", false, false, io.Discard, f.call); err == nil {
		t.Fatal("unfenced cancellation accepted")
	}
	for _, change := range []func(*outboxCLIRecord){
		func(r *outboxCLIRecord) { r.Children[0].DeliveryID = r.Children[1].DeliveryID },
		func(r *outboxCLIRecord) { r.Children[0].PeerID = "other" },
		func(r *outboxCLIRecord) { r.Entries[0].Path = "../outside.txt" },
		func(r *outboxCLIRecord) { r.Children[0].DeliveryOutcome = "confirmed" },
		func(r *outboxCLIRecord) { r.Admission = "enqueued"; r.Status = "completed"; r.ProcessID = "" },
	} {
		r := outboxCLITestRecord()
		change(&r)
		raw, _ := json.Marshal(r)
		if _, err := outboxCLIDecode(raw, "", outboxCLIOriginal, false); err == nil {
			t.Fatal("inconsistent evidence accepted")
		}
	}
}
func TestOutboxCLIAutomaticLocaleAndStableJSON(t *testing.T) {
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "")
	for _, tc := range []struct{ locale, want string }{{"ja_JP.UTF-8", "正確な宛先"}, {"en_US.UTF-8", "Queue files"}, {"zz_UNKNOWN", "Queue files"}} {
		t.Setenv("LC_ALL", tc.locale)
		var out bytes.Buffer
		client := func(context.Context, string, string, any) error { t.Fatal("help IPC"); return nil }
		if err := runWith(context.Background(), []string{"--state-dir", "profile", "outbox", "--help"}, &out, strings.NewReader(""), client); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Fatal(out.String())
		}
	}
	var previous string
	for _, locale := range []string{"en", "ja"} {
		var out bytes.Buffer
		f := &outboxCLIFixture{t: t, record: outboxCLITestRecord()}
		if err := runWith(context.Background(), []string{"--locale", locale, "--state-dir", "profile", "outbox", "list", "--json"}, &out, strings.NewReader(""), f.call); err != nil {
			t.Fatal(err)
		}
		if previous != "" && previous != out.String() {
			t.Fatal("machine schema localized")
		}
		previous = out.String()
	}
}

func TestOutboxCLIRepeatedOperationsKeepDeliveryIdentities(t *testing.T) {
	f := &outboxCLIFixture{t: t, record: outboxCLITestRecord()}
	original := append([]outboxCLIChild(nil), f.record.Children...)
	for _, args := range [][]string{
		{"commit", outboxCLIOriginal, "--process", dispatchCLIProcess, "--revision", dispatchCLIRevision, "--confirm", "--json"},
		{"commit", outboxCLIOriginal, "--process", dispatchCLIProcess, "--revision", dispatchCLIRevision, "--confirm", "--json"},
		{"cancel", outboxCLIOriginal, "--json"},
		{"cancel", outboxCLIOriginal, "--json"},
		{"show", "--request", outboxCLIOriginal, "--json"},
	} {
		var out bytes.Buffer
		if err := outboxCommandCLI(context.Background(), args, "profile", false, false, &out, f.call); err != nil {
			t.Fatal(err)
		}
		var result outboxCLIRecord
		if json.Unmarshal(out.Bytes(), &result) != nil {
			t.Fatal(out.String())
		}
		if result.OutboxID != outboxCLIOriginal || result.RequestID != outboxCLIOriginal {
			t.Fatal("new request created")
		}
		for i, child := range result.Children {
			if child.DeliveryID != original[i].DeliveryID || child.PeerID != original[i].PeerID {
				t.Fatal("delivery replaced")
			}
		}
	}
	if !reflect.DeepEqual(f.calls, []string{"commit", "commit", "cancel", "cancel", "status"}) {
		t.Fatal(f.calls)
	}
}

func TestOutboxCLIConfirmedEvidenceIncludesEveryEntry(t *testing.T) {
	r := outboxCLITestRecord()
	r.Admission, r.Status, r.ProcessID, r.DeliveryOutcome = "enqueued", "completed", "", "confirmed"
	r.Entries = append(r.Entries, transfer.Entry{ID: "directory", Kind: transfer.Directory, Path: "empty-folder", Size: 0})
	for i := range r.Children {
		r.Children[i].Status, r.Children[i].DeliveryOutcome = "completed", "confirmed"
		r.Children[i].Files = []outboxCLIFile{{BatchID: r.Children[i].DeliveryID, FileID: "file", Size: 3, SHA256: dispatchCLIRevision}, {BatchID: r.Children[i].DeliveryID, FileID: "directory", Size: 0, SHA256: ""}}
	}
	raw, _ := json.Marshal(r)
	if _, err := outboxCLIDecode(raw, "", outboxCLIOriginal, false); err != nil {
		t.Fatal(err)
	}
	r.Children[0].Files = r.Children[0].Files[:1]
	raw, _ = json.Marshal(r)
	if _, err := outboxCLIDecode(raw, "", outboxCLIOriginal, false); err == nil {
		t.Fatal("missing directory save evidence accepted")
	}
	r.Children[0].Files = nil
	raw, _ = json.Marshal(r)
	if _, err := outboxCLIDecode(raw, "", outboxCLIOriginal, false); err == nil {
		t.Fatal("nil confirmed receipts accepted")
	}
}

func TestOutboxCLICanonicalRequestIDs(t *testing.T) {
	created := time.Date(2026, 10, 12, 1, 0, 0, 987654321, time.UTC)
	seen := map[string]bool{}
	for range 100 {
		id, err := newOutboxCLIRequestID(created)
		if err != nil {
			t.Fatal(err)
		}
		stamp, ok := outboxCLIRequestTime(id)
		if !ok || !stamp.Equal(created.Truncate(time.Millisecond)) || seen[id] {
			t.Fatalf("invalid or repeated creation key: %q", id)
		}
		seen[id] = true
	}
	for _, invalid := range []string{
		"original", "cli-outbox-original", "", strings.Replace(outboxCLIOriginal, "ob1_", "ob2_", 1),
		strings.ToUpper(outboxCLIOriginal), outboxCLIOriginal + "0", outboxCLIOriginal[:49],
		"ob1_0791766800000_0123456789abcdef0123456789abcdef",
		"ob1_9999999999999_0123456789abcdef0123456789abcdef",
		"ob1_179176680000_0123456789abcdef0123456789abcdef",
		"ob1_1791766800000_0123456789abcdef0123456789abcdeg",
		"ob1_1791766800000-0123456789abcdef0123456789abcdef",
	} {
		if outboxCLIValidRequestID(invalid) {
			t.Fatalf("noncanonical ID accepted: %q", invalid)
		}
	}
	// Syntax validation does not retire a retained record or impose freshness.
	if !outboxCLIValidRequestID("ob1_1000000000000_0123456789abcdef0123456789abcdef") {
		t.Fatal("historical request can no longer be queried")
	}
	if _, err := newOutboxCLIRequestID(time.UnixMilli(999999999999)); err == nil {
		t.Fatal("unsupported clock range generated a request")
	}
	lastAllowed := time.UnixMilli(9_999_999_999_999).Add(-8*24*time.Hour - 5*time.Minute)
	if _, err := newOutboxCLIRequestID(lastAllowed); err != nil {
		t.Fatal("last supported retirement horizon rejected", err)
	}
	if _, err := newOutboxCLIRequestID(lastAllowed.Add(time.Millisecond)); err == nil {
		t.Fatal("unrepresentable retirement horizon accepted")
	}
}

func TestOutboxCLISelectorsRequireCanonicalIDs(t *testing.T) {
	client := func(context.Context, string, string, any) error {
		t.Fatal("invalid selector contacted the agent")
		return nil
	}
	for _, args := range [][]string{
		{"show", "legacy-id"}, {"show", "--request", "legacy-id"},
		{"cancel", "legacy-id"}, {"discard", "legacy-id", "--process", dispatchCLIProcess},
		{"commit", "legacy-id", "--process", dispatchCLIProcess, "--revision", dispatchCLIRevision, "--confirm"},
	} {
		if err := outboxCommandCLI(context.Background(), args, "profile", false, false, io.Discard, client); err == nil {
			t.Fatalf("invalid selector accepted: %v", args)
		}
	}
}

func TestOutboxCLIMaxDeadlineBoundToRequestEpoch(t *testing.T) {
	fixture := &outboxCLIFixture{t: t}
	var out bytes.Buffer
	if err := outboxSendCLI(context.Background(), outboxCLIArgs("--expires-in", "168h", "--json"), "profile", false, false, &out, strings.NewReader(""), fixture.call); err != nil {
		t.Fatal(err)
	}
	created, ok := outboxCLIRequestTime(fixture.record.OutboxID)
	expires, err := time.Parse(time.RFC3339Nano, fixture.record.ExpiresAt)
	if !ok || err != nil || !expires.Equal(created.Add(7*24*time.Hour)) {
		t.Fatal("deadline extended beyond request-epoch authority")
	}
	if !reflect.DeepEqual(fixture.calls, []string{"state", "prepare"}) {
		t.Fatal("prepare-only request sent or created replacement")
	}
	record := outboxCLITestRecord()
	record.CreatedAt = "2026-10-12T01:01:00Z"
	record.UpdatedAt = record.CreatedAt
	record.ExpiresAt = "2026-10-19T01:00:00.001Z"
	raw, _ := json.Marshal(record)
	if _, err := outboxCLIDecode(raw, dispatchCLIProcess, outboxCLIOriginal, false); err == nil {
		t.Fatal("record deadline exceeded original request's seven-day bound")
	}
}
