//go:build !windows

package transfer

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
)

func TestBuildManifestRejectsFIFOWithoutOpeningIt(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := BuildManifest("batch", []string{fifo}, Limits{}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("FIFO source = %v", err)
	}
}
