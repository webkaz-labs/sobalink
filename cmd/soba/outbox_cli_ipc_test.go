package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// Only the peer inventory and transport are synthetic. Real local control,
// Core staging, reviewed admission and cancellation are exercised below. Offline
// peers and a non-network dial stub prevent any claim of remote delivery.
type outboxIPCBackend struct {
	dispatchIPCBackend
	dials outboxIPCDialLog
}

func (b *outboxIPCBackend) State(ctx context.Context) (identity.State, error) {
	state, err := b.dispatchIPCBackend.State(ctx)
	state.SelfID = "synthetic-outbox-sender"
	for i := range state.Snapshot.Peers {
		state.Snapshot.Peers[i].Online = false
	}
	return state, err
}
func (b *outboxIPCBackend) DialIP(context.Context, string, netip.AddrPort) (net.Conn, error) {
	b.dials.record(outboxIPCCurrentDialCategory())
	return nil, errors.New("synthetic offline peer transport unavailable")
}
func (b *outboxIPCBackend) configure(profile *core.Profile) core.NetworkBackend {
	profile.Settings.Hostname = "outbox-fixture"
	return b
}

func TestOutboxCLIProtectedIPC(t *testing.T) {
	testOutboxCLIContract(t, func(t *testing.T, ctx context.Context, backend *outboxIPCBackend) (string, controlCaller) {
		return dispatchIPCServer(t, ctx, backend.configure), control.Call
	})
}

// This deliberately separate, weaker companion serializes the production local
// request/response around Core.IPC in process. It proves no socket/pipe boundary,
// and is never selected automatically when the protected IPC test is blocked.
func TestOutboxCLIInProcessCoreContract(t *testing.T) {
	testOutboxCLIContract(t, func(t *testing.T, ctx context.Context, backend *outboxIPCBackend) (string, controlCaller) {
		dir, app := dispatchContractCore(t, ctx, backend.configure)
		return dir, func(ctx context.Context, selectedDir, command string, target any) error {
			if selectedDir != dir {
				return errors.New("unexpected in-process profile selection")
			}
			requestBytes, err := json.Marshal(control.Request{Command: command})
			if err != nil {
				return err
			}
			var request control.Request
			if err := json.Unmarshal(requestBytes, &request); err != nil {
				return err
			}
			value, callErr := app.IPC(ctx, request.Command)
			var reply control.Response
			if callErr != nil {
				reply.Error = callErr.Error()
				var coded interface{ ErrorCode() string }
				if errors.As(callErr, &coded) {
					reply.Code = coded.ErrorCode()
				}
			} else {
				reply.Data, err = json.Marshal(value)
				if err != nil {
					return err
				}
			}
			responseBytes, err := json.Marshal(reply)
			if err != nil {
				return err
			}
			var response control.Response
			if err := json.Unmarshal(responseBytes, &response); err != nil {
				return err
			}
			if response.Error != "" {
				return &control.RemoteError{Code: response.Code, Message: response.Error}
			}
			return json.Unmarshal(response.Data, target)
		}
	})
}

func testOutboxCLIContract(t *testing.T, setup func(*testing.T, context.Context, *outboxIPCBackend) (string, controlCaller)) {
	t.Helper()
	for _, locale := range []string{"en", "ja"} {
		t.Run(locale, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			t.Cleanup(cancel)
			backend := &outboxIPCBackend{}
			backend.dials.changed = make(chan struct{}, 1)
			// Registered before setup so the final observation follows Core.Close,
			// including any worker that was still joining at the last CLI reply.
			t.Cleanup(func() { backend.dials.requireDiscoveryOnly(t) })
			dir, actualCall := setup(t, ctx, backend)
			state, err := backend.State(ctx)
			if err != nil || !state.Snapshot.Running || len(state.Snapshot.Peers) != 2 {
				t.Fatal("offline fixture lost its running peer inventory")
			}
			for _, peer := range state.Snapshot.Peers {
				if peer.Online {
					t.Fatal("offline fixture advertised an online peer")
				}
			}
			// Observe the real maintenance loop before any Outbox command. The
			// stub denies every transport, so discovery cannot confirm either peer.
			backend.dials.wait(t, ctx, func(counts outboxIPCDialCounts) bool {
				return counts.Discovery >= 2 || !counts.discoveryOnly()
			})
			backend.dials.requireDiscoveryOnly(t)
			t.Logf("maintenance before outbox: %+v", backend.dials.snapshot())
			calls := map[string]int{}
			var originalRequest string
			client := func(ctx context.Context, selectedDir, wire string, target any) error {
				if selectedDir != dir {
					t.Fatal("CLI lost the selected profile")
				}
				if wire == "status" {
					calls["process"]++
				} else {
					var command webui.Command
					if err := json.Unmarshal([]byte(wire), &command); err != nil {
						t.Fatal(err)
					}
					calls[command.Name]++
					if command.Name == "outbox.prepare" {
						if originalRequest != "" || !outboxCLIValidRequestID(command.RequestID) {
							t.Fatal("CLI replaced the original request or used a noncanonical key")
						}
						originalRequest = command.RequestID
					}
					if command.Name == "outbox.status" || command.Name == "outbox.cancel" {
						var payload map[string]json.RawMessage
						if err := json.Unmarshal(command.Payload, &payload); err != nil {
							t.Fatal(err)
						}
						if _, present := payload["processId"]; present {
							t.Fatal("durable observation/cancellation depends on an old process")
						}
					}
				}
				return actualCall(ctx, selectedDir, wire, target)
			}
			call := func(args ...string) ([]byte, error) {
				var out bytes.Buffer
				argv := append([]string{"--locale", locale, "--state-dir", dir}, args...)
				err := runWith(ctx, argv, &out, strings.NewReader(""), client)
				return append([]byte(nil), out.Bytes()...), err
			}
			mustCall := func(args ...string) []byte {
				t.Helper()
				raw, err := call(args...)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			decode := func(raw []byte, process, id string) outboxCLIRecord {
				t.Helper()
				var record outboxCLIRecord
				dispatchIPCDecode(t, raw, &record)
				if _, err := outboxCLIDecode(raw, process, id, locale == "ja"); err != nil {
					t.Fatal(err)
				}
				return record
			}
			list := func() outboxCLIList {
				t.Helper()
				raw := mustCall("outbox", "list", "--json")
				var result outboxCLIList
				dispatchIPCDecode(t, raw, &result)
				if _, err := outboxCLIListDecode(raw, locale == "ja"); err != nil || result.Status != "ready" {
					t.Fatalf("outbox list is not ready: %+v, %v", result, err)
				}
				return result
			}
			if len(list().Items) != 0 {
				t.Fatal("fresh profile contains an admitted outbox request")
			}
			content := []byte("synthetic immutable outbox contents\n")
			source := filepath.Join(t.TempDir(), "example.txt")
			if err := os.WriteFile(source, content, 0600); err != nil {
				t.Fatal(err)
			}
			review := decode(mustCall("send", "--queue", "--peer", "peer-b", "--peer", "peer-a", "--expires-in", "24h", "--json", source), "", "")
			digest := sha256.Sum256(content)
			if review.Admission != "prepared" || review.Status != "prepared" || review.OutboxID != originalRequest || review.RequestID != originalRequest || !reflect.DeepEqual(review.PeerIDs, []string{"peer-b", "peer-a"}) || len(review.Entries) != 1 || len(review.Children) != 2 || review.TotalBytes != int64(len(content)) {
				t.Fatal("real preparation lost exact identities, content or prepare-only semantics")
			}
			entry := review.Entries[0]
			if entry.Path != "example.txt" || entry.Kind != transfer.File || entry.Size != int64(len(content)) || entry.SHA256 != hex.EncodeToString(digest[:]) {
				t.Fatal("real review does not describe the staged file")
			}
			if len(list().Items) != 0 {
				t.Fatal("prepare-only CLI admitted durable work")
			}
			staged := func() string {
				t.Helper()
				matches, err := filepath.Glob(filepath.Join(dir, "durable-outbox-v1", "payloads", "*", entry.ID))
				if err != nil || len(matches) != 1 {
					t.Fatalf("expected exactly one shared staged file, found %d: %v", len(matches), err)
				}
				data, err := os.ReadFile(matches[0])
				if err != nil || !bytes.Equal(data, content) {
					t.Fatal("reviewed local copy changed")
				}
				return matches[0]
			}
			stagedPath := staged()
			observed := decode(mustCall("outbox", "show", "--request", originalRequest, "--json"), review.ProcessID, originalRequest)
			if !reflect.DeepEqual(observed, review) {
				t.Fatal("prepared status did not return the exact original review")
			}
			commit := []string{"outbox", "commit", originalRequest, "--process", review.ProcessID, "--revision", review.ReviewRevision, "--confirm", "--json"}
			invalidCommit := append([]string(nil), commit...)
			invalidCommit[6] = strings.Repeat("0", 64)
			raw, err := call(invalidCommit...)
			var remote *control.RemoteError
			if !errors.As(err, &remote) || remote.ErrorCode() != "outbox_stale" {
				t.Fatalf("invalid review lost stable control error: %v", err)
			}
			var uncertain struct {
				Version     int    `json:"version"`
				Kind        string `json:"kind"`
				RequestID   string `json:"requestId"`
				OutboxID    string `json:"outboxId"`
				Observation string `json:"observation"`
				Phase       string `json:"phase"`
				Code        string `json:"code"`
				NextAction  string `json:"nextAction"`
			}
			dispatchIPCDecode(t, raw, &uncertain)
			if uncertain.Version != 1 || uncertain.Kind != "outbox" || uncertain.Phase != "commit" || uncertain.OutboxID != originalRequest || uncertain.RequestID != originalRequest || uncertain.Observation != "unconfirmed" || uncertain.Code != "outbox_stale" || uncertain.NextAction != "status" {
				t.Fatal("invalid commit lost the original recovery locator")
			}
			if len(list().Items) != 0 {
				t.Fatal("invalid confirmation admitted work")
			}
			changed := []byte("changed original source\n")
			if err := os.WriteFile(source, changed, 0600); err != nil {
				t.Fatal(err)
			}
			assertOriginal := func(record outboxCLIRecord) {
				t.Helper()
				if record.Admission != "enqueued" || record.ProcessID != "" || !outboxCLISameReview(review, record) || record.DeliveryOutcome != "not_attempted" {
					t.Fatal("durable receipt changed consent or claimed an unperformed delivery")
				}
				for _, child := range record.Children {
					if child.DeliveryOutcome != "not_attempted" || len(child.Files) != 0 {
						t.Fatal("offline fixture claimed receiver save evidence")
					}
				}
			}
			for _, args := range [][]string{commit, commit, {"outbox", "show", originalRequest, "--json"}} {
				assertOriginal(decode(mustCall(args...), "", originalRequest))
			}
			if staged() != stagedPath {
				t.Fatal("commit replaced the shared staged copy")
			}
			items := list().Items
			if len(items) != 1 {
				t.Fatal("repeat commit created another outbox item")
			}
			assertOriginal(items[0])
			partialCancel := decode(mustCall("outbox", "cancel", originalRequest, "--delivery", review.Children[0].DeliveryID, "--json"), "", originalRequest)
			assertOriginal(partialCancel)
			if partialCancel.Children[0].Status != "cancelled" && partialCancel.Children[0].Status != "cancel_requested" || partialCancel.Children[1].Status == "cancelled" || partialCancel.Children[1].Status == "cancel_requested" || staged() != stagedPath {
				t.Fatal("child cancellation lost the other recipient's payload ownership")
			}
			for range 2 {
				stopped := decode(mustCall("outbox", "cancel", originalRequest, "--json"), "", originalRequest)
				assertOriginal(stopped)
				for _, child := range stopped.Children {
					if child.Status != "cancelled" && child.Status != "cancel_requested" {
						t.Fatal("cancel did not fence further sends")
					}
				}
			}
			assertOriginal(decode(mustCall("outbox", "show", "--request", originalRequest, "--json"), "", originalRequest))
			if data, err := os.ReadFile(source); err != nil || !bytes.Equal(data, changed) {
				t.Fatal("cancellation changed or deleted original input")
			}
			if calls["process"] != 1 || calls["outbox.prepare"] != 1 || calls["outbox.commit"] != 3 || calls["outbox.cancel"] != 3 {
				t.Fatalf("unexpected admission/replay activity: calls=%v", calls)
			}
			backend.dials.requireDiscoveryOnly(t)
		})
	}
}
