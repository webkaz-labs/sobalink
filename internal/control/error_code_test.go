package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

type fixtureCommandError struct{}

func (fixtureCommandError) Error() string     { return "reload the saved configuration" }
func (fixtureCommandError) ErrorCode() string { return "service_revision_conflict" }

func assertRemoteCode(t *testing.T, err error, code, message string) {
	t.Helper()
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.ErrorCode() != code || remote.Error() != message {
		t.Fatalf("remote error = %#v, want code %q and message %q", err, code, message)
	}
}

func TestCommandErrorCodeMemoryRoundTrip(t *testing.T) {
	s, _, client, _ := memoryServer(func(context.Context, string) (any, error) {
		return nil, fmt.Errorf("command rejected: %w", fixtureCommandError{})
	})
	t.Cleanup(func() {
		client.Close()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	})
	if err := json.NewEncoder(client).Encode(Request{Command: "fixture"}); err != nil {
		t.Fatal(err)
	}
	err := readResponse(client, nil)
	assertRemoteCode(t, fmt.Errorf("caller context: %w", err), "service_revision_conflict", "command rejected: reload the saved configuration")
}

func TestResponseCompatibility(t *testing.T) {
	assertRemoteCode(t, readResponse(strings.NewReader(`{"error":"legacy failure"}`), nil), "", "legacy failure")
	assertRemoteCode(t, readResponse(strings.NewReader(`{"error":"failure","code":"arbitrary payload"}`), nil), "", "failure")
	var out struct {
		Ready bool `json:"ready"`
	}
	if err := readResponse(strings.NewReader(`{"data":{"ready":true}}`), &out); err != nil || !out.Ready {
		t.Fatalf("success response changed: %#v, %v", out, err)
	}
	if err := readResponse(strings.NewReader(`{"error":`), nil); err == nil {
		t.Fatal("malformed response accepted")
	}
}

func TestLargeTransferStatusMemoryRoundTrip(t *testing.T) {
	// Valid multi-file histories can exceed 256 KiB without containing any file
	// bodies. Keep status and commands that consult it usable at that size.
	// Six batches of 128 entries fit the entry/depth/manifest limits; their
	// combined path metadata alone exceeds the old IPC allowance.
	paths := make([]string, 6*128)
	for i := range paths {
		paths[i] = fmt.Sprintf("folder-%d/%s", i, strings.Repeat(strings.Repeat("s", 48)+"/", 7)+"file.txt")
	}
	metadata, _ := json.Marshal(paths)
	if len(metadata) <= 256<<10 {
		t.Fatal("fixture no longer exceeds the old IPC limit")
	}
	for batch := 0; batch < 6; batch++ {
		manifest := transfer.Manifest{ID: fmt.Sprintf("batch-%d", batch)}
		for i, path := range paths[batch*128 : (batch+1)*128] {
			manifest.Entries = append(manifest.Entries, transfer.Entry{ID: fmt.Sprintf("file-%d", i), Path: path, Kind: transfer.File, SHA256: strings.Repeat("a", 64)})
		}
		if err := transfer.ValidateManifest(manifest, transfer.Limits{MaxEntries: 256, MaxManifestBytes: 128 << 10}); err != nil {
			t.Fatal("fixture must fit actual transfer validation", err)
		}
	}
	s, _, client, _ := memoryServer(func(context.Context, string) (any, error) {
		return map[string]any{"paths": paths}, nil
	})
	t.Cleanup(func() { client.Close(); _ = s.Close() })
	if err := json.NewEncoder(client).Encode(Request{Command: "status"}); err != nil {
		t.Fatal(err)
	}
	var output struct{ Paths []string }
	if err := readResponse(client, &output); err != nil || len(output.Paths) != len(paths) || output.Paths[len(paths)-1] != paths[len(paths)-1] {
		t.Fatalf("valid transfer metadata did not survive IPC: entries=%d, err=%v", len(output.Paths), err)
	}
}

func TestOversizedStatusReturnsStructuredRecovery(t *testing.T) {
	s, _, client, _ := memoryServer(func(context.Context, string) (any, error) {
		return strings.Repeat("x", maxResponseBytes), nil
	})
	t.Cleanup(func() { client.Close(); _ = s.Close() })
	if err := json.NewEncoder(client).Encode(Request{Command: "status"}); err != nil {
		t.Fatal(err)
	}
	var remote *RemoteError
	if err := readResponse(client, nil); !errors.As(err, &remote) || remote.ErrorCode() != "response_too_large" {
		t.Fatal("server did not return a bounded recovery error", err)
	}
	oversized := io.MultiReader(strings.NewReader(`{"data":"`), io.LimitReader(repeatingReader{}, maxResponseBytes), strings.NewReader(`"}`))
	if err := readResponse(oversized, nil); !errors.As(err, &remote) || remote.ErrorCode() != "response_too_large" {
		t.Fatal("client accepted an oversized older-server response", err)
	}
}

type repeatingReader struct{}

func (repeatingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
