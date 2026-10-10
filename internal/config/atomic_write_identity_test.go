package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteIdentityMatchesCommittedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "group-evidence.json")
	lease, err := AcquireAtomicWriteLease(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	info, err := lease.WriteWithIdentity(path, []byte("synthetic evidence"))
	if err != nil || info == nil {
		t.Fatalf("missing committed identity: %v", err)
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		t.Fatalf("wrong committed identity: %v", err)
	}
}

func TestAtomicWriteIdentityAbsentBeforeCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "group-evidence.json")
	hooks := &atomicHooks{barrier: func(phase string) {
		if phase == "beforeReplace" {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		}
	}}
	lease, err := acquireAtomicWriteLease(path, hooks)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	info, err := lease.WriteWithIdentity(path, []byte("synthetic evidence"))
	if err == nil || errors.Is(err, ErrAtomicCommitted) || info != nil {
		t.Fatalf("precommit failure claimed identity: %v", err)
	}
	current, statErr := os.Lstat(path)
	if statErr != nil || !current.IsDir() {
		t.Fatalf("foreign destination changed: %v", statErr)
	}
}

func TestAtomicWriteIdentityRetainedOnCommittedUncertainty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "group-evidence.json")
	hooks := &atomicHooks{syncReplacement: func(f, parent, owned *os.File) error {
		if err := atomicSyncReplacement(f, parent, owned); err != nil {
			return err
		}
		return os.ErrPermission
	}}
	lease, err := acquireAtomicWriteLease(path, hooks)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	info, err := lease.WriteWithIdentity(path, []byte("synthetic evidence"))
	if info == nil || !errors.Is(err, ErrAtomicCommitted) || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("uncertain publication classification lost: %v", err)
	}
	current, statErr := os.Lstat(path)
	if statErr != nil || !os.SameFile(info, current) {
		t.Fatalf("uncertain publication identity lost: %v", statErr)
	}
}

func TestAtomicWriteIdentityRejectsSameByteReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "group-evidence.json")
	data := []byte("synthetic evidence")
	lease, err := AcquireAtomicWriteLease(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	info, err := lease.WriteWithIdentity(path, data)
	if err != nil || info == nil {
		t.Fatal(err)
	}
	// The independently created foreign inode contains exactly the same bytes.
	// The retained writer identity must still distinguish it after path replacement.
	foreign := filepath.Join(dir, "foreign.json")
	if err := os.WriteFile(foreign, data, 0600); err != nil {
		t.Fatal(err)
	}
	foreignInfo, err := os.Lstat(foreign)
	if err != nil || os.SameFile(info, foreignInfo) {
		t.Fatalf("fixture did not create independent inode: %v", err)
	}
	if err := os.Rename(foreign, path); err != nil {
		t.Fatal(err)
	}
	current, err := os.Lstat(path)
	if err != nil || os.SameFile(info, current) || !os.SameFile(foreignInfo, current) {
		t.Fatalf("foreign inode accepted as writer publication: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(data) {
		t.Fatalf("same-byte fixture changed: %v", err)
	}
}
