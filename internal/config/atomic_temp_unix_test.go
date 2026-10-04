//go:build !windows

package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestOwnedAtomicSnapshotSubstitutionPreservesOutside(t *testing.T) {
	for _, kind := range []string{"symlink-before-open", "fifo-before-open", "symlink-before-unlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			p := seedOwnedSnapshot(t, dir)
			outside := filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(outside, []byte("outside sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			phase := "beforeInspectSnapshot"
			if kind == "symlink-before-unlink" {
				phase = "beforeRemoveSnapshot"
			}
			hooks := &atomicHooks{barrier: func(actual string) {
				if actual != phase {
					return
				}
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				var err error
				switch kind {
				case "fifo-before-open":
					err = unix.Mkfifo(p, 0600)
				case "hardlink":
					err = os.Link(outside, p)
				default:
					err = os.Symlink(outside, p)
				}
				if err != nil {
					t.Fatal(err)
				}
			}}
			// Alarm exposes a regular-to-FIFO race that would otherwise deadlock
			// metadata inspection/open while holding the directory lease.
			done := make(chan error, 1)
			go func() { done <- atomicWriteOwned(filepath.Join(dir, "profile.json"), nil, hooks) }()
			select {
			case err := <-done:
				if !errors.Is(err, ErrAtomicRecovery) {
					t.Fatal("substitution admitted", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("FIFO substitution blocked the writer")
			}
			if _, err := os.Lstat(p); err != nil {
				t.Fatal("substitute deleted", err)
			}
			b, err := os.ReadFile(outside)
			if err != nil || string(b) != "outside sentinel" {
				t.Fatal("outside modified", err)
			}
		})
	}
}

func TestOwnedAtomicLegacyUnknownNeverFollowed(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "hardlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			outside := filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, ".write-user-owned")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(outside, p)
			case "fifo":
				err = unix.Mkfifo(p, 0600)
			case "hardlink":
				err = os.Link(outside, p)
			case "directory":
				err = os.Mkdir(p, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = AtomicWritePrivate(filepath.Join(dir, "profile.json"), nil); !errors.Is(err, ErrAtomicRecovery) {
				t.Fatal("unknown legacy admitted", err)
			}
			if _, err = os.Lstat(filepath.Join(dir, atomicNamespace)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("protocol metadata allocated with unknown legacy accounting", err)
			}
			if _, err = os.Lstat(p); err != nil {
				t.Fatal("legacy removed", err)
			}
			if b, err := os.ReadFile(outside); err != nil || string(b) != "outside" {
				t.Fatal("outside touched", err)
			}
			count, _ := atomicSnapshotState(t, dir)
			if count != 0 {
				t.Fatal("temp created with unknown legacy accounting")
			}
		})
	}
}

func TestOwnedAtomicLegacyAccountingDoesNotReadPayload(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".write-no-read")
	if err := os.WriteFile(p, []byte("secret"), 0000); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWritePrivate(filepath.Join(dir, "profile.json"), []byte(`{}`)); err != nil {
		t.Fatal("metadata accounting attempted payload read", err)
	}
	info, err := os.Stat(p)
	if err != nil || info.Size() != 6 || info.Mode().Perm() != 0 {
		t.Fatal("legacy modified", err)
	}
}

func TestOwnedAtomicDirectoryAndLeaseSubstitution(t *testing.T) {
	for _, kind := range []string{"parent", "owned-directory", "lease"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "parent")
			if err := os.Mkdir(dir, 0755); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "profile.json")
			if err := os.WriteFile(sentinel, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			var retained string
			err := atomicWriteOwned(filepath.Join(dir, "profile.json"), []byte(atomicTestSnapshot), &atomicHooks{barrier: func(phase string) {
				if phase != "beforeReplace" {
					return
				}
				switch kind {
				case "parent":
					retained = filepath.Join(base, "retained")
					if err := os.Rename(dir, retained); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, dir); err != nil {
						t.Fatal(err)
					}
					retained = filepath.Join(retained, atomicNamespace, atomicSnapshot)
				case "owned-directory":
					retained = filepath.Join(dir, "retained")
					if err := os.Rename(filepath.Join(dir, atomicNamespace), retained); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, filepath.Join(dir, atomicNamespace)); err != nil {
						t.Fatal(err)
					}
					retained = filepath.Join(retained, atomicSnapshot)
				case "lease":
					lease := filepath.Join(dir, atomicNamespace, atomicLeaseName)
					if err := os.Rename(lease, lease+".retained"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(lease, []byte(atomicOwner), 0600); err != nil {
						t.Fatal(err)
					}
					retained = filepath.Join(dir, atomicNamespace, atomicSnapshot)
				}
			}})
			if !errors.Is(err, ErrAtomicRecovery) {
				t.Fatal("directory substitution admitted", err)
			}
			if _, err := os.Stat(retained); err != nil {
				t.Fatal("unverified temp was removed", err)
			}
			if b, err := os.ReadFile(sentinel); err != nil || string(b) != "outside" {
				t.Fatal("outside touched", err)
			}
		})
	}
}

func TestOwnedAtomicUnknownPrivatePermissionsFailClosed(t *testing.T) {
	for _, component := range []string{atomicNamespace, filepath.Join(atomicNamespace, atomicLeaseName), filepath.Join(atomicNamespace, atomicSnapshot)} {
		t.Run(component, func(t *testing.T) {
			dir := t.TempDir()
			p := seedOwnedSnapshot(t, dir)
			if err := os.Chmod(filepath.Join(dir, component), 0777); err != nil {
				t.Fatal(err)
			}
			if err := AtomicWritePrivate(filepath.Join(dir, "profile.json"), nil); !errors.Is(err, ErrAtomicRecovery) {
				t.Fatal("unprivate namespace admitted", err)
			}
			if _, err := os.Stat(p); err != nil {
				t.Fatal("unverified snapshot deleted", err)
			}
		})
	}
}
