//go:build product_activation_native && web_activation_native && managed_restart_native && linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
)

// Formatting a private error would panic, even before any serialization check.
type privateForegroundError struct{ marker byte }

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

func TestProductForegroundExitOwnersCloseDetails(t *testing.T) {
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "1")
	previous := observeForegroundOwnersCloseResult
	defer func() { observeForegroundOwnersCloseResult = previous }()
	for mask := 0; mask < 8; mask++ {
		var order []string
		failures := [3]error{}
		for i := range failures {
			if mask&(1<<i) != 0 {
				failures[i] = &privateForegroundError{marker: byte(i + 1)}
			}
		}
		ipc, app, lock := upgradeTestCloser{"ipc", &order, failures[0]}, upgradeTestCloser{"core", &order, failures[1]}, upgradeTestCloser{"lock", &order, failures[2]}
		calls := 0
		observeForegroundOwnersCloseResult = func(ipcFailed, coreFailed bool, ipcClass, component, class string) {
			calls++
			if !reflect.DeepEqual(order, []string{"ipc", "core", "lock"}) {
				t.Fatal("observation preceded ordered closure")
			}
			if ipcFailed != (failures[0] != nil) || coreFailed != (failures[1] != nil) {
				t.Fatal("owner failures combined")
			}
			wantIPC, wantCore := "none", "none"
			if ipcFailed {
				wantIPC = "other"
			}
			if coreFailed {
				wantCore = "other"
			}
			if ipcClass != wantIPC || component != wantCore || class != wantCore {
				t.Fatal("private error escaped or misclassified")
			}
		}
		closed, released := shutdownManagedOwners(ipc, app, lock)
		if calls != 1 || released != failures[2] {
			t.Fatal("close observation or lock result changed")
		}
		for _, failure := range failures[:2] {
			if failure != nil && !errors.Is(closed, failure) {
				t.Fatal("underlying close error lost")
			}
		}
		if (closed != nil) != (failures[0] != nil || failures[1] != nil) {
			t.Fatal("owner close gate changed")
		}
	}
}

func TestProductForegroundExitOwnersRequireOptIn(t *testing.T) {
	previous := observeForegroundOwnersCloseResult
	defer func() { observeForegroundOwnersCloseResult = previous }()
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "")
	observeForegroundOwnersCloseResult = func(bool, bool, string, string, string) { t.Fatal("unapproved owner observation") }
	observeForegroundOwnersClose(privateForegroundError{}, privateForegroundError{}, nil)
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "1")
	observeForegroundOwnersCloseResult = nil
	observeForegroundOwnersClose(privateForegroundError{}, privateForegroundError{}, nil)
}

type foregroundCloseBackend struct {
	dispatchIPCBackend
	failure error
	calls   int
}

func (b *foregroundCloseBackend) Close() error { b.calls++; return b.failure }

// A real Core and owner lock carry the fixed snapshot through the foreground
// seam. Only the backend is fake; this does not establish network acceptance.
func TestProductForegroundExitActualCoreCloseDetails(t *testing.T) {
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "1")
	previous := observeForegroundOwnersCloseResult
	defer func() { observeForegroundOwnersCloseResult = previous }()
	dir := t.TempDir()
	profile := core.Profile{Version: 3, DurableFiles: syntheticDurableProfile(), Settings: core.Settings{Locale: "auto", Theme: "system", Network: "tailnet", Hostname: "close-fixture"}, Peers: []core.Trust{}, Services: []core.ServiceSpec{}}
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal("synthetic profile encoding failed")
	}
	if err := config.AtomicWritePrivate(filepath.Join(dir, "sobalink.json"), raw); err != nil {
		t.Fatal("synthetic profile preparation failed")
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal("synthetic lock acquisition failed")
	}
	defer lock.Close()
	failure := &privateForegroundError{marker: 1}
	backend := &foregroundCloseBackend{failure: errors.Join(failure, context.DeadlineExceeded)}
	app, err := core.Open(context.Background(), core.Options{Directory: dir, LifecycleLock: lock, NodeFactory: func(string, string) (core.NetworkBackend, error) { return backend, nil }})
	if err != nil {
		t.Fatal("synthetic Core preparation failed")
	}
	defer app.Close()
	calls := 0
	observeForegroundOwnersCloseResult = func(ipcFailed, coreFailed bool, ipcClass, component, class string) {
		calls++
		if ipcFailed || !coreFailed || ipcClass != "none" || component != "node" || class != "context-deadline" {
			t.Fatal("actual Core detail did not cross fixed foreground seam")
		}
	}
	closed, released := shutdownManagedOwners(nil, app, lock)
	if calls != 1 || released != nil || !errors.Is(closed, failure) || !errors.Is(closed, context.DeadlineExceeded) || backend.calls != 1 {
		t.Fatal("actual Core cleanup or original error changed")
	}
}
