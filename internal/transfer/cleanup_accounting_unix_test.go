//go:build !windows

package transfer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type permissionFailureReader struct {
	stage string
	read  bool
	t     *testing.T
}

func (r *permissionFailureReader) Read(p []byte) (int, error) {
	if !r.read {
		r.read = true
		return copy(p, "1234567"), nil
	}
	if err := os.Chmod(r.stage, 0500); err != nil {
		r.t.Fatal(err)
	}
	return 0, io.ErrUnexpectedEOF
}

func TestReceiveRetryCleansFailedRemovalBeforeWriting(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	m, peer := testManager(t, Options{Limits: Limits{MaxReservedBytes: 8}})
	if _, err := m.Offer(peer, testManifest("fixture", testEntry("file", "payload", "12345678"))); err != nil {
		t.Fatal(err)
	}
	accepted, err := m.Accept("fixture", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(accepted.Destination, m.batches["fixture"].stage)
	t.Cleanup(func() { os.Chmod(stage, 0700) })
	if _, err := m.ReceiveFile(context.Background(), peer, "fixture", "file", &permissionFailureReader{stage: stage, t: t}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	b := m.batches["fixture"]
	if m.reserved != 8 || len(b.cleanup) != 1 {
		t.Fatal("failed removal released storage")
	}
	if _, err := m.RetryFile("fixture", "file"); !errors.Is(err, ErrReceiveRecovery) {
		t.Fatalf("retry ignored failed cleanup: %v", err)
	}
	if err := os.Chmod(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RetryFile("fixture", "file"); err != nil {
		t.Fatal(err)
	}
	names, err := os.ReadDir(stage)
	if err != nil || len(names) != 0 {
		t.Fatalf("retry left old partial: %v %v", names, err)
	}
	if _, err := m.ReceiveFile(context.Background(), peer, "fixture", "file", strings.NewReader("12345678")); err != nil {
		t.Fatal(err)
	}
	if m.reserved != 0 {
		t.Fatal("successful cleanup did not release reservation")
	}
}
