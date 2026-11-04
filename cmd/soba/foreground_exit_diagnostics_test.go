//go:build product_activation_native && web_activation_native && managed_restart_native && linux

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// Formatting a private error would panic, even before any serialization check.
type privateForegroundError struct{}

func (privateForegroundError) Error() string { panic("raw foreground error formatted") }

func TestProductForegroundExitComponents(t *testing.T) {
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "1")
	previous := observeForegroundExitResult
	defer func() { observeForegroundExitResult = previous }()
	for mask := 0; mask < 16; mask++ {
		var components [4]error
		var want [4]bool
		for i := range components {
			want[i] = mask&(1<<i) != 0
			if want[i] {
				components[i] = privateForegroundError{}
			}
		}
		calls := 0
		observeForegroundExitResult = func(body, owners, lock, ack bool) {
			calls++
			if [4]bool{body, owners, lock, ack} != want {
				t.Fatal("foreground error components were combined or changed")
			}
		}
		observeForegroundExit(components[0], components[1], components[2], components[3])
		if calls != 1 {
			t.Fatal("exit observation was not synchronous and singular")
		}
	}
}

func TestProductForegroundExitRequiresOptIn(t *testing.T) {
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "")
	previous := observeForegroundExitResult
	defer func() { observeForegroundExitResult = previous }()
	observeForegroundExitResult = func(bool, bool, bool, bool) { t.Fatal("unapproved observation") }
	observeForegroundExit(privateForegroundError{}, nil, nil, nil)
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "1")
	observeForegroundExitResult = nil
	observeForegroundExit(privateForegroundError{}, nil, nil, nil)
}

// Reject a malformed synthetic profile after acquiring the real owner lock.
// This exercises the foreground defer without starting network, HTTP or IPC.
func TestProductForegroundExitBodyFailure(t *testing.T) {
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "1")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sobalink.json"), []byte("{"), 0600); err != nil {
		t.Fatal("synthetic profile preparation failed")
	}
	previous := observeForegroundExitResult
	defer func() { observeForegroundExitResult = previous }()
	observed := false
	observeForegroundExitResult = func(body, owners, lock, ack bool) {
		observed = true
		if !body || owners || lock || ack {
			t.Fatal("body failure was attributed to cleanup")
		}
	}
	if runForeground(context.Background(), dir, true, false, io.Discard) == nil || !observed {
		t.Fatal("actual foreground failure was not observed")
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal("foreground failure retained its owner lock")
	}
	if lock.Close() != nil {
		t.Fatal("synthetic owner lock cleanup failed")
	}
}
