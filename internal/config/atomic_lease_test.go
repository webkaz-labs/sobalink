package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteLeaseParentIdentityAndTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	lease, err := AcquireAtomicWriteLease(path, "index.json.retirement")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	parent, err := atomicOpenDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	wrong, err := atomicOpenDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	if err := lease.CheckParent(wrong); err == nil {
		t.Fatal("wrong retained parent admitted")
	}
	if err := lease.CheckParent(parent); err != nil {
		t.Fatal(err)
	}
	if err := lease.Write(filepath.Join(dir, "other.json"), []byte("bad")); !errors.Is(err, ErrAtomicRecovery) {
		t.Fatal("wrong target", err)
	}
	if _, err := os.Stat(filepath.Join(dir, atomicNamespace, atomicSnapshot)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid write allocated slot", err)
	}
	if err := lease.Write(path, []byte("good")); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.CheckParent(parent); err == nil {
		t.Fatal("closed lease parent check accepted")
	}
	if err := lease.Write(path, []byte("bad")); err == nil {
		t.Fatal("closed lease write accepted")
	}
	if err := lease.Close(); err == nil {
		t.Fatal("double close accepted")
	}
	if err := AtomicWritePrivate(path, []byte("next")); err != nil {
		t.Fatal("writer exclusion leaked", err)
	}
}
