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

	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

const dispatchCLIProcess = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const dispatchCLIRevision = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type dispatchCLIFixture struct {
	t           *testing.T
	calls       []string
	preparation string
	peers       []string
	fail        string
	mutate      func(string, *dispatchCLIReceipt)
}

func (f *dispatchCLIFixture) call(ctx context.Context, dir, wire string, target any) error {
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
	action := strings.TrimPrefix(cmd.Name, "transfer.dispatch.")
	f.calls = append(f.calls, action)
	var payload struct {
		ProcessID      string   `json:"processId"`
		PreparationID  string   `json:"preparationId"`
		ReviewRevision string   `json:"reviewRevision"`
		PeerIDs        []string `json:"peerIds"`
		Paths          []string `json:"paths"`
		Confirm        bool     `json:"confirm"`
	}
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		f.t.Fatal(err)
	}
	if payload.ProcessID != dispatchCLIProcess {
		f.t.Fatal("process lost")
	}
	if action == "prepare" {
		f.preparation = cmd.RequestID
		f.peers = payload.PeerIDs
		if len(payload.Paths) != 1 || !filepath.IsAbs(payload.Paths[0]) {
			f.t.Fatal("paths changed")
		}
	} else {
		if payload.PreparationID != f.preparation {
			f.t.Fatal("original ID lost")
		}
		if action == "commit" && (!payload.Confirm || payload.ReviewRevision != dispatchCLIRevision) {
			f.t.Fatal("review lost")
		}
	}
	if f.fail == action {
		return errors.New("private/fixture-secret")
	}
	r := dispatchCLIReceipt{Version: 1, ProcessID: dispatchCLIProcess, PreparationID: f.preparation, Status: "prepared", ReviewRevision: dispatchCLIRevision, PeerIDs: f.peers, Entries: []transfer.Entry{{ID: "file", Path: "example.txt", Kind: transfer.File, Size: 3, SHA256: dispatchCLIRevision}}, TotalBytes: 3}
	if action == "commit" {
		r = dispatchCLIReceipt{Version: 1, ProcessID: dispatchCLIProcess, PreparationID: f.preparation, DispatchID: f.preparation, Status: "committed", Children: []dispatchCLIChild{}}
		for _, peer := range f.peers {
			r.Children = append(r.Children, dispatchCLIChild{PeerID: peer, TransferID: "batch-" + peer})
		}
	}
	if action == "discard" {
		r = dispatchCLIReceipt{Version: 1, ProcessID: dispatchCLIProcess, PreparationID: f.preparation, Status: "discarded"}
	}
	if f.mutate != nil {
		f.mutate(action, &r)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		f.t.Fatal(err)
	}
	return json.Unmarshal(raw, target)
}
func dispatchCLIArgs(options ...string) []string {
	args := []string{"--peer", "peer-b", "--peer", "peer-a"}
	args = append(args, options...)
	return append(args, "example.txt")
}
func TestTransferDispatchCLIConfirmAndCancellation(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, tc := range []struct {
			name, answer string
			confirm      bool
			want         []string
		}{{"yes", "y\n", false, []string{"state", "prepare", "commit"}}, {"no", "n\n", false, []string{"state", "prepare", "discard"}}, {"cancel", "q\n", false, []string{"state", "prepare", "discard"}}, {"eof", "", false, []string{"state", "prepare", "discard"}}, {"explicit", "", true, []string{"state", "prepare", "commit"}}} {
			t.Run(tc.name+text(ja, "en", "ja"), func(t *testing.T) {
				fixture := &dispatchCLIFixture{t: t}
				var out bytes.Buffer
				args := dispatchCLIArgs()
				if tc.confirm {
					args = dispatchCLIArgs("--confirm")
				}
				err := transferDispatchCLI(context.Background(), args, "profile", ja, false, &out, strings.NewReader(tc.answer), fixture.call)
				if !reflect.DeepEqual(fixture.calls, tc.want) {
					t.Fatalf("calls %v want %v", fixture.calls, tc.want)
				}
				if tc.name == "yes" || tc.confirm {
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(out.String(), text(ja, "not delivered", "配達完了ではありません")) {
						t.Fatal("admission mislabeled")
					}
				} else if !errors.Is(err, errPromptCanceled) {
					t.Fatalf("cancel error %v", err)
				}
				if !strings.Contains(out.String(), "example.txt") || !strings.Contains(out.String(), "--state-dir profile send --status "+fixture.preparation) {
					t.Fatal("review or recovery omitted")
				}
			})
		}
	}
}
func TestTransferDispatchCLIJSONPrepareAndCommit(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, confirmed := range []bool{false, true} {
			fixture := &dispatchCLIFixture{t: t}
			var out bytes.Buffer
			flags := []string{"--json"}
			if confirmed {
				flags = append(flags, "--confirm")
			}
			if err := transferDispatchCLI(context.Background(), dispatchCLIArgs(flags...), "profile", ja, false, &out, strings.NewReader("y\n"), fixture.call); err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(&out)
			var r dispatchCLIReceipt
			if err := decoder.Decode(&r); err != nil {
				t.Fatal(err)
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				t.Fatal("more than one JSON result")
			}
			want := []string{"state", "prepare"}
			if confirmed {
				want = append(want, "commit")
			}
			if !reflect.DeepEqual(fixture.calls, want) {
				t.Fatal(fixture.calls)
			}
			if confirmed && r.Status != "committed" || !confirmed && r.Status != "prepared" {
				t.Fatal(r.Status)
			}
		}
	}
}
func TestTransferDispatchCLILostResponsesNeverResend(t *testing.T) {
	for _, phase := range []string{"prepare", "commit", "discard"} {
		for _, machine := range []bool{false, true} {
			if phase == "discard" && machine {
				continue
			}
			fixture := &dispatchCLIFixture{t: t, fail: phase}
			var out bytes.Buffer
			opts := []string{"--confirm"}
			answer := "y\n"
			if phase == "discard" {
				opts = nil
				answer = "n\n"
			}
			if machine {
				opts = append(opts, "--json")
			}
			err := transferDispatchCLI(context.Background(), dispatchCLIArgs(opts...), "profile", false, false, &out, strings.NewReader(answer), fixture.call)
			if err == nil || strings.Contains(err.Error(), "fixture-secret") || strings.Contains(out.String(), "fixture-secret") {
				t.Fatal("missing or unsafe error")
			}
			expected := []string{"state", "prepare"}
			if phase != "prepare" {
				expected = append(expected, phase)
			}
			if !reflect.DeepEqual(fixture.calls, expected) || !strings.Contains(out.String(), fixture.preparation) {
				t.Fatalf("replay or no recovery: %v", fixture.calls)
			}
		}
	}
}
func TestTransferDispatchCLISelectionAndResponseBinding(t *testing.T) {
	for _, change := range []func(string, *dispatchCLIReceipt){
		func(action string, r *dispatchCLIReceipt) {
			if action == "prepare" {
				r.PeerIDs = []string{"other"}
			}
		},
		func(action string, r *dispatchCLIReceipt) {
			if action == "prepare" {
				r.ProcessID = dispatchCLIRevision
			}
		},
		func(action string, r *dispatchCLIReceipt) {
			if action == "prepare" {
				r.PreparationID = "other"
			}
		},
		func(action string, r *dispatchCLIReceipt) {
			if action == "prepare" {
				r.TotalBytes++
			}
		},
		func(action string, r *dispatchCLIReceipt) {
			if action == "commit" {
				r.Children[0].PeerID = "other"
			}
		},
	} {
		fixture := &dispatchCLIFixture{t: t, mutate: change}
		var out bytes.Buffer
		err := transferDispatchCLI(context.Background(), dispatchCLIArgs("--confirm"), "profile", false, false, &out, strings.NewReader(""), fixture.call)
		if err == nil {
			t.Fatal("mismatch accepted")
		}
		if len(fixture.calls) > 3 {
			t.Fatal("retried")
		}
	}
}
func TestTransferDispatchCLIDryRunAndInvalidInputs(t *testing.T) {
	calls := 0
	client := func(context.Context, string, string, any) error { calls++; return errors.New("unexpected call") }
	for _, args := range [][]string{dispatchCLIArgs("--confirm"), {"--status", "original", "--process", dispatchCLIProcess}, {"--discard", "original", "--process", dispatchCLIProcess}, {"--commit", "original", "--process", dispatchCLIProcess, "--revision", dispatchCLIRevision, "--confirm"}} {
		var out bytes.Buffer
		if err := transferDispatchCLI(context.Background(), args, "profile", false, true, &out, strings.NewReader(""), client); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `"applied":false`) {
			t.Fatal(out.String())
		}
	}
	for _, args := range [][]string{{"--peer", "peer", "--peer", "peer", "file"}, {"--peer", "", "file"}, {"--peer", "peer"}, {"--peer", "peer", "--process", dispatchCLIProcess, "file"}, {"--status", "original"}, {"--status", "", "file"}, {"--status", "original", "--process", dispatchCLIProcess, "--confirm"}, {"--commit", "original", "--process", dispatchCLIProcess, "--revision", dispatchCLIRevision}, {"--commit", "original", "--status", "original", "--process", dispatchCLIProcess, "--confirm"}} {
		var out bytes.Buffer
		if err := transferDispatchCLI(context.Background(), args, "profile", false, false, &out, strings.NewReader(""), client); err == nil {
			t.Fatalf("invalid accepted: %v", args)
		}
	}
	if calls != 0 {
		t.Fatalf("called Core %d times", calls)
	}
}
func TestTransferDispatchCLIExplicitRecovery(t *testing.T) {
	for _, action := range []string{"status", "commit", "discard"} {
		fixture := &dispatchCLIFixture{t: t, preparation: "original", peers: []string{"peer"}}
		var out bytes.Buffer
		args := []string{"--" + action, "original", "--process", dispatchCLIProcess, "--json"}
		if action == "commit" {
			args = append(args, "--revision", dispatchCLIRevision, "--confirm")
		}
		if err := transferDispatchCLI(context.Background(), args, "profile", false, false, &out, strings.NewReader(""), fixture.call); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fixture.calls, []string{action}) {
			t.Fatal(fixture.calls)
		}
	}
}

type dispatchCLIRejectWriter struct{}

func (dispatchCLIRejectWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestTransferDispatchCLIOutputFailureBlocksCommit(t *testing.T) {
	fixture := &dispatchCLIFixture{t: t}
	if err := transferDispatchCLI(context.Background(), dispatchCLIArgs("--confirm"), "profile", false, false, dispatchCLIRejectWriter{}, strings.NewReader(""), fixture.call); err == nil {
		t.Fatal("write failure lost")
	}
	if !reflect.DeepEqual(fixture.calls, []string{"state", "prepare"}) {
		t.Fatal(fixture.calls)
	}
}
func TestTransferDispatchCLICancelledContextNoCalls(t *testing.T) {
	fixture := &dispatchCLIFixture{t: t}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := transferDispatchCLI(ctx, dispatchCLIArgs("--confirm"), "profile", false, false, &out, strings.NewReader(""), fixture.call); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(fixture.calls) != 0 {
		t.Fatal(fixture.calls)
	}
}
func TestTransferDispatchCLIRoutingAndHelp(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		fixture := &dispatchCLIFixture{t: t}
		var out bytes.Buffer
		args := []string{"--locale", locale, "--state-dir", "profile", "send"}
		args = append(args, dispatchCLIArgs("--json")...)
		if err := runWith(context.Background(), args, &out, strings.NewReader(""), fixture.call); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fixture.calls, []string{"state", "prepare"}) {
			t.Fatal(fixture.calls)
		}
		for _, help := range [][]string{{"send", "--help"}, {"help", "send"}} {
			out.Reset()
			client := func(context.Context, string, string, any) error { t.Fatal("help called Core"); return nil }
			if err := runWith(context.Background(), append([]string{"--locale", locale}, help...), &out, strings.NewReader(""), client); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "--json") || !strings.Contains(out.String(), "--peer") || !strings.Contains(out.String(), "--status") {
				t.Fatal(out.String())
			}
		}
	}
}

func TestTransferDispatchCLIReceiptRejectsAmbiguousJSON(t *testing.T) {
	valid := `{"version":1,"processId":"` + dispatchCLIProcess + `","preparationId":"original","reviewRevision":"` + dispatchCLIRevision + `","peerIds":["peer"],"entries":[{"id":"file","path":"example.txt","kind":"file","size":0,"sha256":"` + dispatchCLIRevision + `"}],"totalBytes":0,"status":"prepared"}`
	if _, err := dispatchCLIDecode([]byte(valid), dispatchCLIProcess, "original", false); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(valid, `"version":1`, `"Version":1`, 1),
		strings.Replace(valid, `"peerIds":["peer"]`, `"peerIds":null`, 1),
		strings.Replace(valid, `"peerIds":["peer"]`, `"peerIds":["peer","peer"]`, 1),
		strings.Replace(valid, `"version":1`, `"version":1,"command":"anything"`, 1),
		strings.Replace(valid, `"status":"prepared"`, `"status":"completed"`, 1),
		valid + `{}`,
	} {
		if _, err := dispatchCLIDecode([]byte(bad), dispatchCLIProcess, "original", false); err == nil {
			t.Fatal("ambiguous response accepted")
		}
	}
}
func TestTransferDispatchCLIPrepareAllowsSortedReceipt(t *testing.T) {
	fixture := &dispatchCLIFixture{t: t, mutate: func(action string, r *dispatchCLIReceipt) {
		if action == "prepare" {
			r.PeerIDs = []string{"peer-a", "peer-b"}
		}
	}}
	var out bytes.Buffer
	if err := transferDispatchCLI(context.Background(), dispatchCLIArgs("--confirm"), "profile", false, false, &out, strings.NewReader(""), fixture.call); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixture.calls, []string{"state", "prepare", "commit"}) {
		t.Fatal(fixture.calls)
	}
}
func TestTransferDispatchCLIGlobalDryRunAndLegacy(t *testing.T) {
	for _, args := range [][]string{{"send", "peer", "example.txt"}, {"send", "--peer", "peer-a", "--peer", "peer-b", "--confirm", "example.txt"}, {"send", "--status", "original", "--process", dispatchCLIProcess}} {
		calls := 0
		client := func(context.Context, string, string, any) error { calls++; return errors.New("unexpected call") }
		var out bytes.Buffer
		if err := runWith(context.Background(), append([]string{"--dry-run", "--state-dir", "profile"}, args...), &out, strings.NewReader(""), client); err != nil {
			t.Fatal(err)
		}
		if calls != 0 {
			t.Fatal("dry run called Core")
		}
	}
	var out bytes.Buffer
	calls := 0
	client := func(_ context.Context, _ string, wire string, target any) error {
		calls++
		var command webui.Command
		if err := json.Unmarshal([]byte(wire), &command); err != nil {
			t.Fatal(err)
		}
		if command.Name != "transfer.send" {
			t.Fatalf("legacy changed to %s", command.Name)
		}
		return json.Unmarshal([]byte(`{"id":"legacy-batch"}`), target)
	}
	if err := runWith(context.Background(), []string{"--state-dir", "profile", "send", "peer", "example.txt"}, &out, strings.NewReader(""), client); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("legacy call count")
	}
}

func TestTransferDispatchCLIMissingProcessStopsBeforePreparation(t *testing.T) {
	for _, failure := range []bool{false, true} {
		calls := 0
		client := func(_ context.Context, _ string, wire string, target any) error {
			calls++
			if wire != "status" {
				t.Fatal("prepared without process")
			}
			if failure {
				return errors.New("private/fixture-secret")
			}
			return json.Unmarshal([]byte(`{}`), target)
		}
		var out bytes.Buffer
		err := transferDispatchCLI(context.Background(), dispatchCLIArgs("--confirm"), "profile", false, false, &out, strings.NewReader(""), client)
		if err == nil || calls != 1 || out.Len() != 0 || strings.Contains(err.Error(), "fixture-secret") || !strings.Contains(strings.ToLower(err.Error()), "no send preparation") {
			t.Fatalf("unexpected state error: %v", err)
		}
	}
}
