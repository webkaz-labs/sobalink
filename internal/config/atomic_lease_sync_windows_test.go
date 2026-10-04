package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newAtomicLeaseSyncNamespace(t *testing.T, parentPath string) *os.File {
	t.Helper()
	parent, err := atomicOpenDirectory(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := parent.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := atomicMkdir(parent, atomicNamespace); err != nil {
		t.Fatal(err)
	}
	owned, err := atomicOpenChild(parent, atomicNamespace, false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owned.Close(); err != nil {
			t.Error(err)
		}
	})
	return owned
}

func TestAtomicSyncLeaseNamespaceWindowsSuccess(t *testing.T) {
	dir := t.TempDir()
	owned := newAtomicLeaseSyncNamespace(t, dir)
	snapshot, err := atomicOpenChild(owned, atomicSnapshot, true, false, true)
	if err != nil {
		t.Fatal(err)
	}
	removeErr := atomicRemove(owned, atomicSnapshot, snapshot)
	closeErr := snapshot.Close()
	if removeErr != nil || closeErr != nil {
		t.Fatal(removeErr, closeErr)
	}
	if _, err := atomicChildMetadata(owned, atomicSnapshot); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("snapshot removal not observed", err)
	}
	if err := atomicSyncLeaseNamespace(dir, owned); err != nil {
		t.Fatal("native namespace durability acknowledgement unavailable", err)
	}
}

func TestAtomicSyncLeaseNamespaceWindowsWrongBound(t *testing.T) {
	dir := t.TempDir()
	owned := newAtomicLeaseSyncNamespace(t, dir)
	wrong := t.TempDir()
	newAtomicLeaseSyncNamespace(t, wrong)
	if err := atomicSyncLeaseNamespace(wrong, owned); !errors.Is(err, ErrAtomicRecovery) {
		t.Fatal("different native namespace identity not rejected before flush", err)
	}
}

func TestAtomicSyncLeaseNamespaceWindowsRebinding(t *testing.T) {
	dir := t.TempDir()
	owned := newAtomicLeaseSyncNamespace(t, dir)
	if err := os.Rename(filepath.Join(dir, atomicNamespace), filepath.Join(dir, "retained")); err != nil {
		t.Fatal(err)
	}
	newAtomicLeaseSyncNamespace(t, dir)
	if err := atomicSyncLeaseNamespace(dir, owned); !errors.Is(err, ErrAtomicRecovery) {
		t.Fatal("replacement binding not rejected before flush", err)
	}
}

func TestAtomicSyncLeaseNamespaceWindowsReparse(t *testing.T) {
	dir := t.TempDir()
	owned := newAtomicLeaseSyncNamespace(t, dir)
	retainedPath := filepath.Join(dir, "retained")
	path := filepath.Join(dir, atomicNamespace)
	if err := os.Rename(path, retainedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(retainedPath, path); err != nil {
		t.Skip("symlink permission unavailable", err)
	}
	if err := atomicSyncLeaseNamespace(dir, owned); err == nil {
		t.Fatal("reparse binding to retained identity admitted")
	}
}
