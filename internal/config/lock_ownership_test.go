package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResourceLifecycleOwnership(t *testing.T) {
	dir := t.TempDir()
	owner, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	called := false
	callback := func() error { called = true; return nil }
	if err := owner.WithOwnership(dir, callback); err != nil || !called {
		t.Fatal("owner rejected", err)
	}
	called = false
	if err := owner.WithOwnership(t.TempDir(), callback); err == nil || called {
		t.Fatal("wrong profile accepted")
	}
	if err := (*Lock)(nil).WithOwnership(dir, callback); err == nil || called {
		t.Fatal("nil lock accepted")
	}
	if err := owner.WithOwnership(dir, nil); err == nil {
		t.Fatal("nil callback accepted")
	}
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Even the same process.lock inode cannot authorize a replaced profile root.
	if err := os.Link(filepath.Join(moved, "process.lock"), filepath.Join(dir, "process.lock")); err == nil {
		if err := owner.WithOwnership(dir, callback); err == nil || called {
			t.Fatal("replaced directory accepted")
		}
	}
	if err := owner.WithOwnership(moved, callback); err != nil {
		t.Fatal("renamed directory rejected", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	called = false
	if err := owner.WithOwnership(moved, callback); err == nil || called {
		t.Fatal("closed owner accepted")
	}
}
func TestResourceLifecycleCloseWaitsForCallback(t *testing.T) {
	dir := t.TempDir()
	owner, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() { finished <- owner.WithOwnership(dir, func() error { close(entered); <-release; return nil }) }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- owner.Close() }()
	select {
	case <-closed:
		t.Fatal("Close released owned callback")
	default:
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}
func TestResourcePrivateDirectoryDurability(t *testing.T) {
	dir := t.TempDir()
	if err := SecureDir(dir); err != nil {
		t.Fatal(err)
	}
	var synced []string
	sync := func(path string, _ *os.File) error { synced = append(synced, path); return nil }
	if err := secureChildDirectory(dir, "resources", sync); err != nil {
		t.Fatal(err)
	}
	if len(synced) != 2 || synced[0] != filepath.Join(dir, "resources") || synced[1] != dir {
		t.Fatal("parent binding not certified", synced)
	}
	sentinel := errors.New("synthetic sync failure")
	for _, failAt := range []int{1, 2} {
		n := 0
		err := secureChildDirectory(dir, "resources", func(string, *os.File) error {
			n++
			if n == failAt {
				return sentinel
			}
			return nil
		})
		if !errors.Is(err, sentinel) {
			t.Fatal("sync failure hidden", err)
		}
	}
	for _, name := range []string{"", ".", "..", "nested/child", "nested\\child"} {
		if err := SecureChildDirectory(dir, name); err == nil {
			t.Fatal("unsafe name accepted")
		}
	}
}

func TestResourcePrivateDirectoryRejectsUnsafeChild(t *testing.T) {
	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := SecureDir(dir); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "resources")
			if kind == "file" {
				if err := os.WriteFile(path, []byte("synthetic"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Skip("symlink unavailable")
				}
			}
			called := false
			err := secureChildDirectory(dir, "resources", func(string, *os.File) error { called = true; return nil })
			if err == nil || called {
				t.Fatal("unsafe child certified")
			}
		})
	}
}
