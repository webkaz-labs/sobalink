package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func TestExistingProfileAdmissionRejectsMissingWithoutInitialization(t *testing.T) {
	for _, exists := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "profile")
		if exists {
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := CheckExistingProfile(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("missing state admitted", err)
		}
		app, err := Open(t.Context(), Options{Directory: dir, SkipNetworkStart: true, RequireExistingProfile: true})
		if app != nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatal("missing profile opened", err)
		}
		if exists {
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatal("missing profile produced files", err)
			}
		} else if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("missing directory created", err)
		}
	}
}

func TestExistingProfileAdmissionRejectsUnsafeOrInvalidFiles(t *testing.T) {
	for _, scenario := range []string{"malformed", "semantic", "unsupported", "directory", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "sobalink.json")
			switch scenario {
			case "malformed":
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "semantic", "unsupported":
				if _, err := InitializeProfile(t.Context(), Options{Directory: dir}, "example-node"); err != nil {
					t.Fatal(err)
				}
				data, _ := os.ReadFile(path)
				if scenario == "semantic" {
					data = bytes.Replace(data, []byte(`"network": "none"`), []byte(`"network": "invalid"`), 1)
				} else {
					data = bytes.Replace(data, []byte(`"version": 3`), []byte(`"version": 2`), 1)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skip("symlink creation unavailable")
				}
			}
			if err := CheckExistingProfile(dir); err == nil {
				t.Fatal("unsafe profile admitted")
			}
		})
	}
}

func TestExistingProfileOfflineLaunchKeepsSavedNetworkAndNeverStartsIt(t *testing.T) {
	dir := t.TempDir()
	if _, err := InitializeProfile(t.Context(), Options{Directory: dir}, "example-node"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sobalink.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before = bytes.Replace(before, []byte(`"network": "none"`), []byte(`"network": "tailnet"`), 1)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	owner, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	app, err := Open(t.Context(), Options{Directory: dir, SkipNetworkStart: true, RequireExistingProfile: true, LifecycleLock: owner, NodeFactory: func(string, string) (NetworkBackend, error) {
		t.Fatal("UI recovery started a network engine")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := app.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	self := snapshot["self"].(map[string]any)
	if self["status"] != "idle" || snapshot["settings"].(Settings).Network != "tailnet" || self["guidance"].(*DiagnosticGuidance).Code != "network_stopped" {
		t.Fatal("offline saved state or visible network guidance changed")
	}
	if snapshot["startup"].(map[string]any)["suppressed"] != true || snapshot["savedProxies"].(map[string]any)["suppressed"] != true {
		t.Fatal("offline recovery did not suppress saved startup approvals")
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	var savedBefore, savedAfter Profile
	if json.Unmarshal(before, &savedBefore) != nil || json.Unmarshal(after, &savedAfter) != nil || !reflect.DeepEqual(savedBefore.Settings, savedAfter.Settings) {
		t.Fatal("ordinary offline recovery changed saved network settings")
	}
}

func TestExistingProfileRequiresLiveMatchingOwnerAndOfflineMode(t *testing.T) {
	dir := t.TempDir()
	if _, err := InitializeProfile(t.Context(), Options{Directory: dir}, "example-node"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), Options{Directory: dir, RequireExistingProfile: true}); err == nil {
		t.Fatal("online recovery accepted")
	}
	if _, err := Open(t.Context(), Options{Directory: dir, SkipNetworkStart: true, RequireExistingProfile: true}); err == nil {
		t.Fatal("unowned recovery accepted")
	}
	owner, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), Options{Directory: dir, SkipNetworkStart: true, RequireExistingProfile: true, LifecycleLock: owner}); err == nil {
		t.Fatal("closed owner accepted")
	}
}

func TestExistingProfileOwnedReadDoesNotRecreateRemovedData(t *testing.T) {
	dir := t.TempDir()
	if _, err := InitializeProfile(t.Context(), Options{Directory: dir}, "example-node"); err != nil {
		t.Fatal(err)
	}
	if err := CheckExistingProfile(dir); err != nil {
		t.Fatal(err)
	}
	owner, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	path := filepath.Join(dir, "sobalink.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var profile Profile
	if err := readExistingProfileOwned(dir, owner, 1<<20, &profile); err == nil {
		t.Fatal("deleted profile read succeeded")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deleted profile recreated")
	}
}
