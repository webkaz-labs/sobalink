package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicOutcomeFirstDestinationMustReserveParentEntry(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < atomicScanEntries-1; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("foreign-%04d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "export.json")
	first := AtomicWritePrivate(path, []byte(`{}`))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	second := AtomicWritePrivate(path, []byte(`{}`))
	t.Logf("initialEntries=%d firstSuccess=%v resultingEntries=%d secondRecovery=%v", atomicScanEntries-1, first == nil, len(entries), errors.Is(second, ErrAtomicRecovery))
	if first == nil && len(entries) > atomicScanEntries {
		t.Errorf("writer admitted itself beyond total parent entry bound and blocked its next save")
	}
}

func TestAtomicParentBoundReservesAllMissingEntriesBeforeAllocation(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		namespace, destination bool
		entries                int
		allowed                bool
	}{
		{"first-use-new-destination-over-bound", false, false, atomicScanEntries - 1, false},
		{"first-use-new-destination-at-bound", false, false, atomicScanEntries - 2, true},
		{"first-use-existing-destination-at-bound", false, true, atomicScanEntries - 1, true},
		{"first-use-existing-destination-over-bound", false, true, atomicScanEntries, false},
		{"existing-namespace-new-destination-over-bound", true, false, atomicScanEntries, false},
		{"existing-namespace-new-destination-at-bound", true, false, atomicScanEntries - 1, true},
		{"existing-namespace-replacement-at-bound", true, true, atomicScanEntries, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "export.json")
			if tc.namespace {
				seed := path
				if !tc.destination {
					seed = filepath.Join(dir, "anchor.json")
				}
				if err := AtomicWritePrivate(seed, []byte("old")); err != nil {
					t.Fatal(err)
				}
			} else if tc.destination {
				if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			foreign := tc.entries - len(entries)
			for i := 0; i < foreign; i++ {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("foreign-%04d", i)), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			allocated := 0
			err = atomicWriteOwned(path, []byte("new"), &atomicHooks{barrier: func(phase string) {
				if phase == "afterCreateTemp" {
					allocated++
				}
			}})
			if tc.allowed {
				if err != nil || allocated != 1 {
					t.Fatalf("replacement: allocations=%d err=%v", allocated, err)
				}
				if err := AtomicWritePrivate(path, []byte("again")); err != nil {
					t.Fatalf("next replacement: %v", err)
				}
			} else {
				if !errors.Is(err, ErrAtomicRecovery) || allocated != 0 {
					t.Fatalf("preflight did not block allocation: %d %v", allocated, err)
				}
				if !tc.namespace {
					if _, err := os.Lstat(filepath.Join(dir, atomicNamespace)); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("preflight allocated metadata: %v", err)
					}
				}
				if !tc.destination {
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("preflight published destination: %v", err)
					}
				} else if data, err := os.ReadFile(path); err != nil || string(data) != "old" {
					t.Fatalf("preflight changed existing destination: %q %v", data, err)
				}
			}
			entries, err = os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			expected := tc.entries
			if tc.allowed {
				expected = atomicScanEntries
			}
			if len(entries) != expected {
				t.Fatalf("entries=%d want=%d", len(entries), expected)
			}
			for i := 0; i < foreign; i++ {
				data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("foreign-%04d", i)))
				if err != nil || string(data) != "preserve" {
					t.Fatalf("foreign object changed: %d %q %v", i, data, err)
				}
			}
		})
	}
}
