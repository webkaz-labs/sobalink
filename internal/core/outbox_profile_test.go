package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func guardProposal(c *Core, phase string) Profile {
	p := c.profileCopy()
	p.Version = currentProfileVersion
	p.DurableFiles = &DurableProfile{Version: 1, Epoch: "0123456789abcdef0123456789abcdef", Phase: phase}
	return p
}

func TestDurableProfileGuardShapes(t *testing.T) {
	good := Profile{Version: currentProfileVersion, DurableFiles: &DurableProfile{Version: 1, Epoch: "0123456789abcdef0123456789abcdef", Phase: "ready"}}
	if !validDurableProfile(good) || validDurableProfile(Profile{Version: 1}) {
		t.Fatal("valid profile rejected")
	}
	for _, mutate := range []func(*Profile){
		func(p *Profile) { p.Version = 1 },
		func(p *Profile) { p.Version = 3 },
		func(p *Profile) { p.DurableFiles = nil },
		func(p *Profile) { p.DurableFiles.Version = 2 },
		func(p *Profile) { p.DurableFiles.Epoch = "0123456789ABCDEF0123456789ABCDEF" },
		func(p *Profile) { p.DurableFiles.Epoch = "1234" },
		func(p *Profile) { p.DurableFiles.Phase = "disabled" },
	} {
		changed := cloneProfile(good)
		mutate(&changed)
		if validDurableProfile(changed) {
			t.Fatal("invalid compatibility guard accepted")
		}
	}
	copied := cloneProfile(good)
	copied.DurableFiles.Phase = "initializing"
	if good.DurableFiles.Phase != "ready" || !validDurableProfile(copied) {
		t.Fatal("guard was not independently cloned")
	}
}

func TestDurableProfileInventoryAndUnsupportedFormatFailReadOnly(t *testing.T) {
	for _, kind := range []string{"durable-v1", "durable-outbox-v1", "old-format", "missing-guard"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "sobalink.json")
			want := "outbox_recovery_required"
			if kind == "old-format" || kind == "missing-guard" {
				if kind == "old-format" {
					want = "profile_format_unsupported"
				}
				version := 1
				if kind == "missing-guard" {
					version = currentProfileVersion
				}
				raw, err := json.Marshal(Profile{Version: version, Settings: Settings{Network: "none", Locale: "auto", Theme: "system", Hostname: "synthetic-node"}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(filepath.Join(dir, kind), 0700); err != nil {
				t.Fatal(err)
			}
			before, beforeErr := os.ReadFile(path)
			_, err := ReadProfile(path)
			if networkErrorCode(err) != want {
				t.Fatal("profile read did not reject exact guard", err)
			}
			_, err = ReadLaunchReview(dir)
			if networkErrorCode(err) != want {
				t.Fatal("launch review did not reject exact guard", err)
			}
			_, err = InitializeProfile(context.Background(), Options{Directory: dir}, "synthetic-node")
			if networkErrorCode(err) != want {
				t.Fatal("init did not reject exact guard", err)
			}
			_, err = OfflineDefinitionCommand(context.Background(), Options{Directory: dir, SkipNetworkStart: true}, webui.Command{RequestID: "synthetic-profile-read", Name: "profile.export", Payload: json.RawMessage(`{}`)})
			if networkErrorCode(err) != want {
				t.Fatal("offline did not reject exact guard", err)
			}
			app, err := Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
			if app != nil {
				_ = app.Close()
			}
			if networkErrorCode(err) != want {
				t.Fatal("Core did not reject exact guard", err)
			}
			after, afterErr := os.ReadFile(path)
			if !reflect.DeepEqual(before, after) || errors.Is(beforeErr, os.ErrNotExist) != errors.Is(afterErr, os.ErrNotExist) {
				t.Fatal("failed admission rewrote existing profile")
			}
		})
	}
}

func durableProfileFixture(t *testing.T) *Core {
	t.Helper()
	c, _ := resourceFixture(t)
	if err := c.writeProfile(c.profileCopy()); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDurableProfilePublishedBarrierFailureNeverGrantsReady(t *testing.T) {
	for _, phase := range []string{"initializing", "ready"} {
		for _, failAt := range []int{1, 2} {
			t.Run(phase+string(rune('0'+failAt)), func(t *testing.T) {
				c := durableProfileFixture(t)
				dir := c.dir
				proposal := guardProposal(c, phase)
				calls := 0
				err := c.persistDurableProfile(proposal, func(*os.Root) error {
					calls++
					if calls == failAt {
						return errors.New("synthetic namespace barrier failure")
					}
					return nil
				})
				if !errors.Is(err, config.ErrAtomicCommitted) || calls != failAt {
					t.Fatal("published uncertainty was lost", err, calls)
				}
				if c.durableProfileReady() || !c.lanStartUncertain.Load() {
					t.Fatal("uncertain guard granted authority")
				}
				memory := c.profileCopy()
				saved, readErr := ReadProfile(filepath.Join(dir, "sobalink.json"))
				if readErr != nil || memory.Version != 2 || saved.Version != 2 || memory.DurableFiles.Phase != phase || saved.DurableFiles.Phase != phase {
					t.Fatal("published guard was rolled back", readErr)
				}
			})
		}
	}
}

func TestDurableProfileUnpublishedFailurePreservesInitializing(t *testing.T) {
	c := durableProfileFixture(t)
	dir := c.dir
	path := filepath.Join(dir, "sobalink.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c.atomicWrite = func(string, []byte) error { return errors.New("synthetic pre-publication failure") }
	err = c.persistDurableProfile(guardProposal(c, "initializing"), func(*os.Root) error { t.Fatal("barrier after rejected write"); return nil })
	c.atomicWrite = nil
	if err == nil || errors.Is(err, config.ErrAtomicCommitted) || c.profileCopy().Version != currentProfileVersion || c.profileCopy().DurableFiles.Phase != "initializing" || c.durableProfileReady() {
		t.Fatal("rejected guard changed authority", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("rejected guard changed disk", err)
	}
}

func TestDurableProfileWriteRequiresOriginalLifecycleOwner(t *testing.T) {
	for _, kind := range []string{"profile", "lock"} {
		t.Run(kind, func(t *testing.T) {
			c, owner := resourceFixture(t)
			proposal := guardProposal(c, "initializing")
			replaceResourceBinding(t, c.dir, kind, owner)
			called := false
			c.atomicWrite = func(string, []byte) error { called = true; return nil }
			err := c.persistDurableProfile(proposal, func(*os.Root) error { called = true; return nil })
			if err == nil || called || c.durableProfileReady() {
				t.Fatal("replacement ownership accepted", err)
			}
		})
	}
}

func TestDurableProfileWithoutLifecycleOwnerCannotInitialize(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()
	if c.durableProfileReady() || c.profileCopy().DurableFiles.Phase != "initializing" {
		t.Fatal("unowned Core granted durable authority")
	}
	for _, name := range []string{"durable-v1", "durable-outbox-v1"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unowned Core initialized durable store", err)
		}
	}
}

func TestDurableProfileFreshRoundTripAndMissingStoreFailClosed(t *testing.T) {
	dir, owner := resourceOperationLifecycleOwner(t)
	first, closeFirst := openResourceApplication(t, dir, owner)
	if !first.durableProfileReady() || first.profileCopy().Version != currentProfileVersion {
		t.Fatal("fresh profile not ready")
	}
	saved := first.profileCopy()
	closeFirst()
	second, closeSecond := openResourceApplication(t, dir, owner)
	if !second.durableProfileReady() || second.profileCopy().DurableFiles.Epoch != saved.DurableFiles.Epoch {
		t.Fatal("reopen lost ready epoch")
	}
	closeSecond()
	if err := os.Rename(filepath.Join(dir, "durable-v1", "receiver.json"), filepath.Join(dir, "durable-v1", "receiver-fixture-evidence.json")); err != nil {
		t.Fatal(err)
	}
	third, _ := openResourceApplication(t, dir, owner)
	if third.durableProfileReady() {
		t.Fatal("missing receiver ledger granted authority")
	}
	if err := third.restoreDurableProfile(); err == nil {
		t.Fatal("ready restore reconstructed lost ledger")
	}
	if _, err := os.Lstat(filepath.Join(dir, "durable-v1", "receiver.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing ledger silently recreated", err)
	}
	assertResourceOperationOffline(t, third)
}

func TestDurableFreshMetadataInitializesOnlyAtOwnedOpen(t *testing.T) {
	dir := t.TempDir()
	if _, err := InitializeProfile(context.Background(), Options{Directory: dir}, "synthetic-node"); err != nil {
		t.Fatal(err)
	}
	before, err := ReadProfile(filepath.Join(dir, "sobalink.json"))
	if err != nil {
		t.Fatal(err)
	}
	if before.Version != currentProfileVersion || before.DurableFiles.Phase != "initializing" {
		t.Fatal("inert init granted authority")
	}
	for _, name := range []string{"durable-v1", "durable-outbox-v1"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("inert init allocated durable store", err)
		}
	}
	owner, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	c, _ := openResourceApplication(t, dir, owner)
	if !c.durableProfileReady() || c.profileCopy().DurableFiles.Epoch != before.DurableFiles.Epoch {
		t.Fatal("owned initialization changed epoch or failed")
	}
}
