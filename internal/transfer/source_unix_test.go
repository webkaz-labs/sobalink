//go:build !windows

package transfer

import (
	"errors"
	"os"
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

func TestSourceOpenRejectsFIFOReplacementWithoutBlocking(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(selected, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := os.Lstat(selected)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(selected); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(selected, 0600); err != nil {
		t.Fatal(err)
	}
	// Simulate substitution after Lstat, before the checked nonblocking open.
	if file, err := openSource(selected, original); !errors.Is(err, ErrUnsafePath) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("substituted FIFO = %v", err)
	}
}
