package core

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func TestGroupBindingRejectsSameBytesAtReplacedFile(t *testing.T) {
	c, s := groupOwnedStoreFixture(t)
	groupOwnedAcceptedFixture(t, c, s)
	path := resourceGroupStatePath(c.dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.AtomicWritePrivate(path, data); err != nil {
		t.Fatal(err)
	}
	if s.current(c) == nil || !s.frozen {
		t.Fatal("same-byte external inode replacement was adopted")
	}
}

func TestGroupBindingRejectsSameInodeDifferentBytes(t *testing.T) {
	c, s := groupOwnedStoreFixture(t)
	groupOwnedAcceptedFixture(t, c, s)
	path := resourceGroupStatePath(c.dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n') // Same decoded envelope, different exact bytes.
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if s.current(c) == nil || !s.frozen {
		t.Fatal("same-inode semantic-equal byte mutation was adopted")
	}
}

func TestGroupBindingRejectsDirectoryReplacement(t *testing.T) {
	c, s := groupOwnedStoreFixture(t)
	groupOwnedAcceptedFixture(t, c, s)
	path := resourceGroupStatePath(c.dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(path)
	if err := os.Rename(directory, filepath.Join(t.TempDir(), "retained")); err != nil {
		t.Fatal(err)
	}
	if err := config.SecureDir(directory); err != nil {
		t.Fatal(err)
	}
	if err := config.AtomicWritePrivate(path, data); err != nil {
		t.Fatal(err)
	}
	if s.current(c) == nil || !s.frozen {
		t.Fatal("replaced private directory was adopted")
	}
}

func TestGroupBindingRejectsMissingObservedState(t *testing.T) {
	for _, kind := range []string{"file", "directory"} {
		t.Run(kind, func(t *testing.T) {
			c, s := groupOwnedStoreFixture(t)
			groupOwnedAcceptedFixture(t, c, s)
			path := resourceGroupStatePath(c.dir)
			if kind == "directory" {
				path = filepath.Dir(path)
			}
			if err := os.Rename(path, filepath.Join(t.TempDir(), "retained")); err != nil {
				t.Fatal(err)
			}
			if s.current(c) == nil || !s.frozen || s.firstUse {
				t.Fatal("missing observed state became first use")
			}
		})
	}
}

func TestGroupBindingRejectsSymlinkAndHardlink(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			c, s := groupOwnedStoreFixture(t)
			groupOwnedAcceptedFixture(t, c, s)
			path := resourceGroupStatePath(c.dir)
			extra := filepath.Join(t.TempDir(), "retained-state")
			if kind == "hardlink" {
				if err := os.Link(path, extra); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(path, extra); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(extra, path); err != nil {
					if runtime.GOOS == "windows" && errors.Is(err, os.ErrPermission) {
						t.Skip("Windows refused creating this synthetic symlink; no rejection claim")
					}
					t.Fatal(err)
				}
			}
			if s.current(c) == nil || !s.frozen {
				t.Fatal("unsafe state link accepted")
			}
		})
	}
}

func TestGroupBindingRejectsUnsafePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode-bit fixture does not establish Windows ACL coverage")
	}
	for _, kind := range []string{"profile", "directory", "file", "process-lock"} {
		t.Run(kind, func(t *testing.T) {
			c, s := groupOwnedStoreFixture(t)
			groupOwnedAcceptedFixture(t, c, s)
			path, mode := resourceGroupStatePath(c.dir), os.FileMode(0644)
			switch kind {
			case "profile":
				path, mode = c.dir, 0755
			case "directory":
				path, mode = filepath.Dir(path), 0755
			case "process-lock":
				path = filepath.Join(c.dir, "process.lock")
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			if s.current(c) == nil || !s.frozen {
				t.Fatal("unsafe private-state permissions accepted")
			}
		})
	}
}

func TestGroupBindingRequiresActualLifecycleOwnership(t *testing.T) {
	for _, kind := range []string{"nil", "closed", "other-profile"} {
		t.Run(kind, func(t *testing.T) {
			c, s := groupOwnedStoreFixture(t)
			groupOwnedAcceptedFixture(t, c, s)
			switch kind {
			case "nil":
				c.resourceLock = nil
			case "closed":
				if err := c.resourceLock.Close(); err != nil {
					t.Fatal(err)
				}
			case "other-profile":
				other, err := config.AcquireLock(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := other.Close(); err != nil {
						t.Errorf("release alternate synthetic lifecycle owner: %v", err)
					}
				})
				c.resourceLock = other
			}
			if s.current(c) == nil || !s.frozen {
				t.Fatal("invalid actual lifecycle owner accepted")
			}
		})
	}
}

func TestGroupBindingRejectsProfileAndLockReplacement(t *testing.T) {
	for _, kind := range []string{"profile", "process-lock"} {
		t.Run(kind, func(t *testing.T) {
			c, s := groupOwnedStoreFixture(t)
			groupOwnedAcceptedFixture(t, c, s)
			path := c.dir
			if kind == "process-lock" {
				path = filepath.Join(c.dir, "process.lock")
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			moved := filepath.Join(t.TempDir(), "retained")
			if err := os.Rename(path, moved); err != nil {
				if resourceOpenHandleRenameDenied(err) {
					after, statErr := os.Lstat(path)
					if statErr != nil || !os.SameFile(before, after) || s.current(c) != nil {
						t.Fatal("blocked substitution changed owned evidence")
					}
					t.Skip("Windows denied synthetic live-handle replacement; owned evidence unchanged")
				}
				t.Fatal(err)
			}
			if kind == "profile" {
				if err := config.SecureDir(path); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(path, "process.lock")
			}
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if s.current(c) == nil || !s.frozen {
				t.Fatal("replaced lifecycle identity accepted")
			}
		})
	}
}
