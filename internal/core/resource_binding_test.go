package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// Only owned temporary state is substituted. A platform that refuses renaming
// an open directory/lock already prevents that fixture's substitution boundary.
func replaceResourceBinding(t *testing.T, dir, kind string, owner *config.Lock) {
	t.Helper()
	moved := filepath.Join(t.TempDir(), "moved")
	before := map[string]os.FileInfo{}
	for _, path := range []string{dir, filepath.Join(dir, "resource-state"), filepath.Join(dir, "process.lock")} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = info
	}
	blocked := func(err error) {
		if !resourceOpenHandleRenameDenied(err) {
			t.Fatal(err)
		}
		for path, original := range before {
			current, err := os.Lstat(path)
			if err != nil || !os.SameFile(original, current) {
				t.Fatal("blocked substitution changed a retained identity", err)
			}
		}
		if _, err := os.Lstat(moved); !os.IsNotExist(err) {
			t.Fatal("blocked substitution created a destination", err)
		}
		// Some callers hold the lifecycle callback mutex. Cleanup runs after
		// that callback unwinds, before the fixture releases its process lock.
		t.Cleanup(func() {
			if err := owner.WithOwnership(dir, func() error { return nil }); err != nil {
				t.Error("blocked substitution changed lifecycle ownership", err)
			}
		})
		t.Skip("Windows denied live-handle substitution; retained identities unchanged; ownership rechecked during cleanup")
	}
	switch kind {
	case "profile":
		if err := os.Rename(dir, moved); err != nil {
			blocked(err)
		}
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "resource-state"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "process.lock"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	case "journal":
		path := filepath.Join(dir, "resource-state")
		if err := os.Rename(path, moved); err != nil {
			blocked(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	case "lock":
		path := filepath.Join(dir, "process.lock")
		if err := os.Rename(path, moved); err != nil {
			blocked(err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("invalid replacement fixture")
	}
}
func TestResourceBindingStartupRejectsPostWriteSubstitution(t *testing.T) {
	for _, kind := range []string{"profile", "journal", "lock"} {
		t.Run(kind, func(t *testing.T) {
			c, owner := resourceFixture(t)
			writes := 0
			c.atomicWrite = func(path string, data []byte) error {
				writes++
				if err := config.AtomicWrite(path, data); err != nil {
					return err
				}
				replaceResourceBinding(t, c.dir, kind, owner)
				return nil
			}
			c.initializeResourceIdentity(owner)
			if writes != 1 || c.resourceIdentity != "" || c.resourceDirectoryIdentity != nil {
				t.Fatal("substituted startup acknowledged")
			}
			if kind != "lock" {
				if _, err := os.Stat(resourceStatePath(c.dir)); !os.IsNotExist(err) {
					t.Fatal("state appeared in replacement", err)
				}
			}
		})
	}
}
func TestResourceBindingUsesActualOwnerIdentity(t *testing.T) {
	for _, kind := range []string{"profile", "lock"} {
		t.Run(kind, func(t *testing.T) {
			c, owner := resourceFixture(t)
			err := owner.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
				replaceResourceBinding(t, c.dir, kind, owner)
				binding, err := openResourcePathBinding(c.dir, directory, lock)
				if binding != nil {
					binding.close()
				}
				if err == nil {
					t.Fatal("replacement became baseline")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestResourceBindingNativeWriterRejectsReplacedParent(t *testing.T) {
	for _, kind := range []string{"profile", "journal", "lock"} {
		t.Run(kind, func(t *testing.T) {
			c, owner := resourceFixture(t)
			err := owner.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
				binding, err := openResourcePathBinding(c.dir, directory, lock)
				if err != nil {
					return err
				}
				defer binding.close()
				replaceResourceBinding(t, c.dir, kind, owner)
				if err := binding.write(resourceStatePath(c.dir), []byte(`{}`), nil); err == nil {
					t.Fatal("native write accepted replacement")
				}
				if kind != "lock" {
					if _, err := os.Stat(resourceStatePath(c.dir)); !os.IsNotExist(err) {
						t.Fatal("replacement state written", err)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestResourceBindingPrivateChildCreationUsesOwnedParent(t *testing.T) {
	c, owner := resourceFixture(t)
	err := owner.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
		replaceResourceBinding(t, c.dir, "profile", owner)
		if err := config.SecureChildDirectoryBound(c.dir, "new-private-child", directory); err == nil {
			t.Fatal("unowned parent accepted")
		}
		if _, err := os.Stat(filepath.Join(c.dir, "new-private-child")); !os.IsNotExist(err) {
			t.Fatal("created child in replacement", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestResourceBindingReadRejectsSubstitutedIdentity(t *testing.T) {
	for _, kind := range []string{"profile", "journal", "lock"} {
		t.Run(kind, func(t *testing.T) {
			c, owner := resourceFixture(t)
			replaceResourceBinding(t, c.dir, kind, owner)
			result, err := c.Command(context.Background(), webui.Command{RequestID: "read-substitution", Name: "resource.list", Payload: json.RawMessage(`{}`)})
			if err == nil || result != nil {
				t.Fatal("resource identity acknowledged after replacement")
			}
			if len(c.requests) != 0 {
				t.Fatal("resource failure cached")
			}
		})
	}
}

func TestResourceBindingReadRejectsReplacedJournal(t *testing.T) {
	c, owner := resourceFixture(t)
	id := c.resourceIdentity
	replaceResourceBinding(t, c.dir, "journal", owner)
	result, err := c.resourceCommand("resource.list", []byte(`{}`))
	coded, ok := err.(*localCommandError)
	if !ok || coded.ErrorCode() != "resource_unavailable" || result != nil || c.resourceIdentity != id {
		t.Fatal("replacement journal accepted for cached identity", result, err)
	}
}
