package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
)

// Only peer inventory and peer transport are synthetic. These tests exercise
// the CLI against the production Core DTO/storage commands; they do not prove
// receiver delivery, real ACK loss, power-loss durability or physical devices.
// Text exercises a first-ever production approval, not the older shared
// dispatch fixture's already-trusted seed or a revoke-before-trust workaround.
func textQueueIPCUntrustedProfile(backend *outboxIPCBackend) dispatchIPCFixtureOption {
	return func(profile *core.Profile) core.NetworkBackend {
		profile.Peers = []core.Trust{}
		return backend.configure(profile)
	}
}
func textQueueIPCAssertFirstApproval(t *testing.T, directory string, approved bool) {
	t.Helper()
	profileBytes, err := os.ReadFile(filepath.Join(directory, "sobalink.json"))
	var profile core.Profile
	if err != nil || json.Unmarshal(profileBytes, &profile) != nil {
		t.Fatal("fixture profile unavailable")
	}
	journalBytes, journalErr := os.ReadFile(filepath.Join(directory, "startup-revocations.json"))
	journal := map[string]string{}
	if journalErr != nil && !errors.Is(journalErr, os.ErrNotExist) || journalErr == nil && json.Unmarshal(journalBytes, &journal) != nil {
		t.Fatal("fixture authority journal unavailable")
	}
	if !approved {
		if len(profile.Peers) != 0 || len(journal) != 0 {
			t.Fatal("text fixture did not start with first-ever unapproved authority")
		}
		return
	}
	if len(profile.Peers) != 1 || profile.Peers[0].ID != "peer-a" || profile.Peers[0].RevocationEpoch == "" || journal["peer-a"] != profile.Peers[0].RevocationEpoch {
		t.Fatal("production approval did not persist the exact initial peer epoch")
	}
}
func TestTextQueueCLIProtectedIPC(t *testing.T) {
	testTextQueueCLICoreContract(t, func(t *testing.T, ctx context.Context, backend *outboxIPCBackend) (string, controlCaller) {
		return dispatchIPCServer(t, ctx, textQueueIPCUntrustedProfile(backend)), control.Call
	})
}
func TestTextQueueCLIInProcessCoreContract(t *testing.T) {
	testTextQueueCLICoreContract(t, func(t *testing.T, ctx context.Context, backend *outboxIPCBackend) (string, controlCaller) {
		dir, app := dispatchContractCore(t, ctx, textQueueIPCUntrustedProfile(backend))
		return dir, func(ctx context.Context, selectedDir, wire string, target any) error {
			if dir != selectedDir {
				return errors.New("unexpected selected profile")
			}
			value, err := app.IPC(ctx, wire)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(value)
			if err != nil {
				return err
			}
			return json.Unmarshal(raw, target)
		}
	})
}
func testTextQueueCLICoreContract(t *testing.T, setup func(*testing.T, context.Context, *outboxIPCBackend) (string, controlCaller)) {
	for _, locale := range []string{"en", "ja"} {
		t.Run(locale, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			t.Cleanup(cancel)
			backend := &outboxIPCBackend{}
			// Core closes before this observation. Any sender/unknown transport owner
			// would invalidate the offline-recipient fixture, even after the last reply.
			t.Cleanup(func() { backend.dials.requireDiscoveryOnly(t) })
			dir, client := setup(t, ctx, backend)
			call := func(args ...string) ([]byte, error) {
				var out bytes.Buffer
				argv := append([]string{"--locale", locale, "--state-dir", dir}, args...)
				err := runWith(ctx, argv, &out, strings.NewReader(""), client)
				return out.Bytes(), err
			}
			decode := func(raw []byte) core.TextOutboxRecord {
				t.Helper()
				r, err := textQueueCLIDecode(raw, "", "", locale == "ja", false)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			textQueueIPCAssertFirstApproval(t, dir, false)
			if _, err := call("trust", "peer-a"); err != nil {
				t.Fatal("first-ever production trust failed", err)
			}
			textQueueIPCAssertFirstApproval(t, dir, true)
			raw, err := call("message", "--queue", "--peer", "peer-a", "--json", "Synthetic IPC text / 合成テキスト")
			if err != nil {
				t.Fatal(err)
			}
			review := decode(raw)
			if review.Status != "prepared" || review.ProcessID == "" {
				t.Fatal("preparation unexpectedly enqueued")
			}
			raw, err = call("message", "queue", "list", "--json")
			if err != nil {
				t.Fatal(err)
			}
			page, err := textQueueCLIListDecode(raw, "", "", locale == "ja")
			if err != nil || len(page.Items) != 1 || page.Items[0].OutboxID != review.OutboxID || page.Items[0].Text != "" {
				t.Fatal("production list shape differs from CLI", err)
			}
			commit := []string{"message", "queue", "commit", review.OutboxID, "--process", review.ProcessID, "--revision", review.ReviewRevision, "--confirm", "--json"}
			// Test-only bounded recovery of lifecycle contention: show the original
			// first, then invoke identical commit arguments. Never repeat preparation.
			for {
				raw, err = call(commit...)
				var coded interface{ ErrorCode() string }
				if !errors.As(err, &coded) || coded.ErrorCode() != "message_storage_busy" {
					break
				}
				status, statusErr := call("message", "queue", "show", review.OutboxID, "--json")
				if statusErr != nil {
					t.Fatal(statusErr)
				}
				observed := decode(status)
				if !textQueueCLISameReview(review, observed) {
					t.Fatal("busy status changed review")
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			queued := decode(raw)
			if queued.Admission != "enqueued" || queued.ProcessID != "" || !textQueueCLISameReview(review, queued) {
				t.Fatal("production commit changed authority")
			}
			// Losing a status reply cannot turn the accepted request into a new send.
			lost := func(ctx context.Context, dir, wire string, target any) error {
				if err := client(ctx, dir, wire, target); err != nil {
					return err
				}
				return io.ErrUnexpectedEOF
			}
			var ambiguous bytes.Buffer
			err = textQueueCommandCLI(ctx, []string{"show", review.OutboxID, "--json"}, dir, locale == "ja", false, &ambiguous, lost)
			if err == nil || !strings.Contains(ambiguous.String(), review.OutboxID) || !strings.Contains(ambiguous.String(), `"observation":"unconfirmed"`) {
				t.Fatal("lost status did not preserve locator")
			}
			for _, action := range []string{"show", "cancel", "show"} {
				for {
					raw, err = call("message", "queue", action, review.OutboxID, "--json")
					var coded interface{ ErrorCode() string }
					if !errors.As(err, &coded) || coded.ErrorCode() != "message_storage_busy" {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(10 * time.Millisecond):
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				result := decode(raw)
				if !textQueueCLISameReview(review, result) || result.ProcessID != "" {
					t.Fatal("durable observation lost original identity")
				}
				if action == "cancel" && result.Status != "cancelled" && result.Status != "cancel_requested" {
					t.Fatal("cancel lacks stopped-work fence")
				}
			}
		})
	}
}
