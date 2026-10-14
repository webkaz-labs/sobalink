package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// Only peer inventory/transport is synthetic in the shared Core fixture. Both
// contract tests use the real lifecycle lock, command routing, staging and
// admission. Neither claims enrollment, delivery or installed-binary acceptance.
type dispatchIPCBackend struct{}

func (*dispatchIPCBackend) Start() error                 { return nil }
func (*dispatchIPCBackend) Close() error                 { return nil }
func (*dispatchIPCBackend) Login(context.Context) error  { return nil }
func (*dispatchIPCBackend) Logout(context.Context) error { return nil }
func (*dispatchIPCBackend) State(context.Context) (identity.State, error) {
	return identity.State{
		Backend: "Running",
		IPs:     []netip.Addr{netip.MustParseAddr("100.64.0.1")},
		Snapshot: policy.Snapshot{Running: true, Peers: []policy.Peer{
			{ID: "peer-a", Online: true, IPs: []netip.Addr{netip.MustParseAddr("100.64.0.2")}},
			{ID: "peer-b", Online: true, IPs: []netip.Addr{netip.MustParseAddr("100.64.0.3")}},
		}},
	}, nil
}
func (*dispatchIPCBackend) DialIP(context.Context, string, netip.AddrPort) (net.Conn, error) {
	return nil, errors.New("synthetic peer transport unavailable")
}
func (*dispatchIPCBackend) Listen(string, string) (net.Listener, error) {
	return nil, errors.New("synthetic peer listeners unavailable")
}
func (*dispatchIPCBackend) ListenPacket(string, string) (net.PacketConn, error) {
	return nil, errors.New("synthetic peer listeners unavailable")
}
func (*dispatchIPCBackend) WhoIs(context.Context, netip.AddrPort) (string, error) {
	return "", errors.New("synthetic peer identity unavailable")
}

var _ core.NetworkBackend = (*dispatchIPCBackend)(nil)

type dispatchIPCFixtureOption func(*core.Profile) core.NetworkBackend

func dispatchContractCore(t *testing.T, ctx context.Context, options ...dispatchIPCFixtureOption) (string, *core.Core) {
	t.Helper()
	dir := privateStateDir(t) // Keep Unix socket paths below the native limit.
	if err := config.SecureDir(dir); err != nil {
		t.Fatal(err)
	}
	profile := core.Profile{
		Version:      3,
		DurableFiles: syntheticDurableProfile(),
		Settings:     core.Settings{Locale: "auto", Theme: "system", Network: "tailnet", Hostname: "dispatch-fixture"},
		Peers: []core.Trust{
			{ID: "peer-a", Network: "tailnet", Generation: 1},
			{ID: "peer-b", Network: "tailnet", Generation: 2},
		},
		Services: []core.ServiceSpec{},
	}
	var backend core.NetworkBackend = &dispatchIPCBackend{}
	for _, option := range options {
		backend = option(&profile)
	}
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.AtomicWritePrivate(filepath.Join(dir, "sobalink.json"), data); err != nil {
		t.Fatal(err)
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	})
	app, err := core.Open(ctx, core.Options{
		Directory: dir, Version: "test", LifecycleLock: lock,
		NodeFactory: func(string, string) (core.NetworkBackend, error) { return backend, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Error(err)
		}
	})
	return dir, app
}

func dispatchIPCServer(t *testing.T, ctx context.Context, options ...dispatchIPCFixtureOption) string {
	t.Helper()
	dir, app := dispatchContractCore(t, ctx, options...)
	server, err := control.ServeWithLimits(ctx, dir, app.IPC, app.LocalControlLimits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	if runtime.GOOS != "windows" {
		info, err := os.Lstat(filepath.Join(dir, "control.sock"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
			t.Fatal("local control endpoint is not a private native socket")
		}
	}
	return dir
}

func dispatchIPCDecode(t *testing.T, raw []byte, target any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatal("expected exactly one machine JSON result")
	}
}

func dispatchIPCSpools(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "outgoing"))
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Fatal("unexpected non-directory in staging inventory")
		}
		paths = append(paths, filepath.Join(dir, "outgoing", entry.Name()))
	}
	return paths
}

func TestTransferDispatchCLIProtectedIPC(t *testing.T) {
	testTransferDispatchCLIContract(t, func(t *testing.T, ctx context.Context) (string, controlCaller) {
		return dispatchIPCServer(t, ctx), control.Call
	})
}

// This separate, weaker test serializes production request/response DTOs around
// Core.IPC in process. It does not use control.Call, exercise native IPC framing
// or prove socket/pipe security. It is never selected as a socket-failure fallback.
func TestTransferDispatchCLIInProcessCoreContract(t *testing.T) {
	testTransferDispatchCLIContract(t, func(t *testing.T, ctx context.Context) (string, controlCaller) {
		dir, app := dispatchContractCore(t, ctx)
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

func testTransferDispatchCLIContract(t *testing.T, setup func(*testing.T, context.Context) (string, controlCaller)) {
	t.Helper()
	for _, ja := range []bool{false, true} {
		t.Run(text(ja, "en", "ja"), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			t.Cleanup(cancel)
			dir, client := setup(t, ctx)
			content := []byte("synthetic staged contents\n")
			source := filepath.Join(t.TempDir(), "example.txt")
			if err := os.WriteFile(source, content, 0600); err != nil {
				t.Fatal(err)
			}
			call := func(args ...string) ([]byte, error) {
				var out bytes.Buffer
				err := transferDispatchCLI(ctx, args, dir, ja, false, &out, strings.NewReader(""), client)
				return out.Bytes(), err
			}
			mustCall := func(args ...string) []byte {
				t.Helper()
				raw, err := call(args...)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			assertChildren := func(want []core.TransferDispatchChild, dispatchID string) {
				t.Helper()
				var state struct {
					Transfers []struct {
						ID, PeerID, Direction, DispatchID string
					} `json:"transfers"`
				}
				if err := client(ctx, dir, "status", &state); err != nil {
					t.Fatal(err)
				}
				if len(state.Transfers) != len(want) {
					t.Fatalf("child history count = %d, want %d", len(state.Transfers), len(want))
				}
				for _, child := range want {
					found := false
					for _, batch := range state.Transfers {
						if batch.ID == child.TransferID && batch.PeerID == child.PeerID && batch.Direction == "outgoing" && batch.DispatchID == dispatchID {
							found = true
						}
					}
					if !found {
						t.Fatal("receipt child missing from real Core history")
					}
				}
			}
			assertErrorCode := func(want string, args ...string) {
				t.Helper()
				raw, err := call(args...)
				var remote *control.RemoteError
				if !errors.As(err, &remote) || remote.ErrorCode() != want {
					t.Fatalf("IPC error code = %v, want %s", err, want)
				}
				var result struct {
					Version       int    `json:"version"`
					ProcessID     string `json:"processId"`
					PreparationID string `json:"preparationId"`
					Status        string `json:"status"`
					Phase         string `json:"phase"`
					NextAction    string `json:"nextAction"`
				}
				dispatchIPCDecode(t, raw, &result)
				if result.Version != 1 || result.Status != "unconfirmed" || result.NextAction != "status" || result.PreparationID != args[1] || result.ProcessID != args[3] || result.Phase != strings.TrimPrefix(args[0], "--") {
					t.Fatal("CLI lost the original preparation recovery identity")
				}
			}

			prepare := []string{"--peer", "peer-b", "--peer", "peer-a", "--json", source}
			var review core.TransferDispatchReview
			dispatchIPCDecode(t, mustCall(prepare...), &review)
			digest := sha256.Sum256(content)
			if review.Version != 1 || review.Status != "prepared" || !reflect.DeepEqual(review.PeerIDs, []string{"peer-b", "peer-a"}) || review.TotalBytes != int64(len(content)) || len(review.Entries) != 1 {
				t.Fatal("Core review did not preserve exact selected peers and content")
			}
			entry := review.Entries[0]
			if entry.Kind != transfer.File || entry.Path != "example.txt" || entry.Size != int64(len(content)) || entry.SHA256 != hex.EncodeToString(digest[:]) {
				t.Fatal("Core review did not describe the actual staged file")
			}
			assertChildren(nil, "")
			spools := dispatchIPCSpools(t, dir)
			if len(spools) != 1 {
				t.Fatal("multi-recipient preparation did not stage exactly once")
			}
			stagedPath := filepath.Join(spools[0], entry.ID)
			if data, err := os.ReadFile(stagedPath); err != nil || !bytes.Equal(data, content) {
				t.Fatal("actual staged bytes differ from the reviewed contents")
			}
			status := []string{"--status", review.PreparationID, "--process", review.ProcessID, "--json"}
			var observed core.TransferDispatchReview
			dispatchIPCDecode(t, mustCall(status...), &observed)
			if !reflect.DeepEqual(observed, review) {
				t.Fatal("status did not return the original Core review")
			}
			if err := os.WriteFile(source, []byte("changed source\n"), 0600); err != nil {
				t.Fatal(err)
			}
			commit := []string{"--commit", review.PreparationID, "--process", review.ProcessID, "--revision", review.ReviewRevision, "--confirm", "--json"}
			invalidCommit := append([]string(nil), commit...)
			invalidCommit[5] = strings.Repeat("0", 64)
			assertErrorCode("transfer_dispatch_stale", invalidCommit...)
			assertChildren(nil, "")
			var receipt core.TransferDispatchReceipt
			dispatchIPCDecode(t, mustCall(commit...), &receipt)
			if receipt.Version != 1 || receipt.Status != "committed" || receipt.ProcessID != review.ProcessID || receipt.PreparationID != review.PreparationID || receipt.DispatchID != review.PreparationID || len(receipt.Children) != 2 || receipt.Children[0].TransferID == receipt.Children[1].TransferID {
				t.Fatal("Core commit receipt did not bind two distinct admitted children")
			}
			if !dispatchCLISamePeers([]string{receipt.Children[0].PeerID, receipt.Children[1].PeerID}, review.PeerIDs) {
				t.Fatal("commit changed the selected recipients")
			}
			for _, args := range [][]string{status, commit} {
				var recovered core.TransferDispatchReceipt
				dispatchIPCDecode(t, mustCall(args...), &recovered)
				if !reflect.DeepEqual(recovered, receipt) {
					t.Fatal("status or repeat commit changed the original receipt")
				}
			}
			assertChildren(receipt.Children, receipt.DispatchID)
			if data, err := os.ReadFile(stagedPath); err != nil || !bytes.Equal(data, content) || len(dispatchIPCSpools(t, dir)) != 1 {
				t.Fatal("commit copied again or changed the frozen staged payload")
			}
			assertErrorCode("transfer_dispatch_committed", "--discard", review.PreparationID, "--process", review.ProcessID, "--json")

			var discardReview core.TransferDispatchReview
			dispatchIPCDecode(t, mustCall(prepare...), &discardReview)
			if discardReview.PreparationID == review.PreparationID || len(dispatchIPCSpools(t, dir)) != 2 {
				t.Fatal("second preparation did not have a separate identity and staged copy")
			}
			for range 2 {
				var discarded dispatchCLIReceipt
				dispatchIPCDecode(t, mustCall("--discard", discardReview.PreparationID, "--process", discardReview.ProcessID, "--json"), &discarded)
				if discarded.Version != 1 || discarded.Status != "discarded" || discarded.ProcessID != discardReview.ProcessID || discarded.PreparationID != discardReview.PreparationID {
					t.Fatal("discard did not preserve the preparation identity")
				}
			}
			if !reflect.DeepEqual(dispatchIPCSpools(t, dir), spools) {
				t.Fatal("discard did not remove only its uncommitted staged copy")
			}
			assertErrorCode("transfer_dispatch_stale", "--status", discardReview.PreparationID, "--process", discardReview.ProcessID, "--json")
			assertChildren(receipt.Children, receipt.DispatchID)
		})
	}
}
