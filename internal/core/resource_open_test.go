package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

// This fixture exercises the production Core lifecycle with an empty
// network:none profile. It never starts Web/IPC/peer listeners and rejects any
// unexpected backend construction. Close joins owners before releasing lock.
func openResourceApplication(t *testing.T, dir string, owner *config.Lock) (*Core, func()) {
	t.Helper()
	return openResourceApplicationWithInspection(t, dir, owner, false)
}
func openResourceApplicationWithInspection(t *testing.T, dir string, owner *config.Lock, enabled bool) (*Core, func()) {
	t.Helper()
	var backendCalls atomic.Int64
	app, err := Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true, LifecycleLock: owner, EnableResourceInspection: enabled, NodeFactory: func(string, string) (NetworkBackend, error) {
		backendCalls.Add(1)
		return nil, errors.New("unexpected synthetic backend construction")
	}})
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	closeApp := func() {
		once.Do(func() {
			if err := app.Close(); err != nil {
				t.Error(err)
			}
			if backendCalls.Load() != 0 {
				t.Error("offline fixture attempted backend construction")
			}
		})
	}
	t.Cleanup(closeApp)
	return app, closeApp
}
func TestResourceOwnedOpenLifecycle(t *testing.T) {
	dir := t.TempDir()
	owner, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Registered first so every application's cleanup precedes the lock release.
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	first, closeFirst := openResourceApplication(t, dir, owner)
	one := resourceCall(t, first, "resource.list", struct{}{}).(resource.Catalog).Resources[0]
	closeFirst()
	if err := owner.WithOwnership(dir, func() error { return nil }); err != nil {
		t.Fatal("Core closed its caller's lifecycle lock", err)
	}
	second, closeSecond := openResourceApplication(t, dir, owner)
	two := resourceCall(t, second, "resource.list", struct{}{}).(resource.Catalog).Resources[0]
	if one.ResourceID != two.ResourceID || one.Revision == two.Revision {
		t.Fatal("reopen identity/review contract changed")
	}
	closeSecond()
	// Invalid resource evidence disables resources, without failing legacy Open.
	if err := os.WriteFile(resourceStatePath(dir), []byte(`{"schemaVersion":999}`), 0600); err != nil {
		t.Fatal(err)
	}
	malformed, closeMalformed := openResourceApplication(t, dir, owner)
	if _, err := malformed.resourceCommand("resource.list", json.RawMessage(`{}`)); err == nil {
		t.Fatal("invalid identity available")
	}
	if _, err := malformed.capacityCommand("policy.config", json.RawMessage(`{}`)); err != nil {
		t.Fatal("legacy capacity disabled", err)
	}
	closeMalformed()
	got, err := os.ReadFile(resourceStatePath(dir))
	if err != nil || string(got) != `{"schemaVersion":999}` {
		t.Fatal("invalid identity regenerated", err)
	}
}
func TestResourceUnownedOpenPreservesLegacy(t *testing.T) {
	wrong, err := config.AcquireLock(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrong.Close() })
	for _, owner := range []*config.Lock{nil, wrong} {
		dir := t.TempDir()
		app, closeApp := openResourceApplication(t, dir, owner)
		if _, err := app.resourceCommand("resource.list", json.RawMessage(`{}`)); err == nil {
			t.Fatal("unowned resource available")
		}
		if _, err := app.capacityCommand("policy.config", json.RawMessage(`{}`)); err != nil {
			t.Fatal("legacy capacity disabled", err)
		}
		if _, err := os.Lstat(resourceStatePath(dir)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unowned resource sidecar created", err)
		}
		closeApp()
	}
}

func TestResourceInspectionEnabledOpenWithoutGrantDoesNotListen(t *testing.T) {
	dir, owner := resourceOperationLifecycleOwner(t)
	for i := 0; i < 2; i++ {
		app, closeApp := openResourceApplicationWithInspection(t, dir, owner, true)
		assertResourceOperationOffline(t, app)
		app.op.Lock()
		g := app.resourceGrants
		valid := g != nil && !g.frozen && g.firstUse && g.fence == nil && g.runtime == nil && g.retiring == nil && len(g.state.Records) == 0
		app.op.Unlock()
		if !valid {
			t.Fatal("enabled startup created or activated unconfirmed permission")
		}
		catalog := resourceCall(t, app, "resource.list", struct{}{}).(resource.Catalog)
		if len(catalog.Resources) != 1 {
			t.Fatal("inspection implementation disabled existing local resource")
		}
		closeApp()
		if _, err := os.Lstat(filepath.Dir(resourceGrantStatePath(dir))); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("enabled startup initialized saved grant state", err)
		}
	}
}
