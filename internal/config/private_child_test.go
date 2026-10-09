package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBoundPrivateChildExclusiveCreationAndOpen(t *testing.T) {
	parent := t.TempDir()
	if err := SecureDir(parent); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	child, err := CreatePrivateChildDirectoryBound(parent, "grants", expected)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	identity, err := child.Stat()
	if err != nil {
		t.Fatal(err)
	}
	other, err := CreatePrivateChildDirectoryBound(parent, "grants", expected)
	if other != nil {
		other.Close()
		t.Fatal("existing directory adopted")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing child did not fail EEXIST: %v", err)
	}
	opened, err := OpenPrivateChildDirectoryBound(parent, "grants", expected)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := opened.Stat()
	closeErr := opened.Close()
	if err != nil || closeErr != nil || !os.SameFile(identity, observed) {
		t.Fatal("certified child identity changed")
	}
	path := filepath.Join(parent, "grants", "state.json")
	if err := AtomicWritePrivate(path, []byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	file, err := OpenPrivateChildFileBound(filepath.Dir(path), "state.json", identity)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	closeErr = file.Close()
	if err != nil || closeErr != nil || !info.Mode().IsRegular() {
		t.Fatal("private regular file not retained")
	}
}

func TestBoundPrivateChildMissingAndWrongParentDoNotCreate(t *testing.T) {
	parent := t.TempDir()
	if err := SecureDir(parent); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []bool{true, false} {
		child, err := openBoundPrivateChild(parent, "absent", expected, directory, false)
		if child != nil {
			child.Close()
			t.Fatal("missing child opened")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected missing-child result: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(parent, "absent")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("read-only open created child")
		}
	}
	wrong, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	child, err := CreatePrivateChildDirectoryBound(parent, "absent", wrong)
	if child != nil {
		child.Close()
		t.Fatal("wrong owner admitted")
	}
	if err == nil {
		t.Fatal("wrong owner accepted")
	}
	if _, err := os.Lstat(filepath.Join(parent, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrong owner created child")
	}
	for _, name := range []string{"", ".", "..", "nested/child", "nested\\child"} {
		child, err := CreatePrivateChildDirectoryBound(parent, name, expected)
		if child != nil {
			child.Close()
			t.Fatal("unsafe name created")
		}
		if err == nil {
			t.Fatal("unsafe name accepted")
		}
	}
}
