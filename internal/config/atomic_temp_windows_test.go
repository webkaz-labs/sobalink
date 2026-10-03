package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOwnedAtomicWindowsPrivateHandles(t *testing.T) {
	dir := t.TempDir()
	if err := AtomicWritePrivate(filepath.Join(dir, "profile.json"), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{atomicNamespace, filepath.Join(atomicNamespace, atomicLeaseName), "profile.json"} {
		path := filepath.Join(dir, relative)
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		private, err := atomicPrivateDACL(windows.Handle(f.Fd()))
		_ = f.Close()
		if err != nil || !private {
			t.Fatal("owned object lacks protected current-user DACL", relative, err)
		}
	}
}

func TestOwnedAtomicWindowsReparseSnapshotPreserved(t *testing.T) {
	dir := t.TempDir()
	p := seedOwnedSnapshot(t, dir)
	outside := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, p); err != nil {
		t.Skip("symlink permission unavailable", err)
	}
	if err := AtomicWritePrivate(filepath.Join(dir, "profile.json"), nil); !errors.Is(err, ErrAtomicRecovery) {
		t.Fatal("reparse snapshot admitted", err)
	}
	if _, err := os.Lstat(p); err != nil {
		t.Fatal("reparse entry removed", err)
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "outside" {
		t.Fatal("outside touched", err)
	}
}

func TestOwnedAtomicWindowsUnknownDACLRetainsCharge(t *testing.T) {
	dir := t.TempDir()
	p := seedOwnedSnapshot(t, dir)
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if err = AtomicWritePrivate(filepath.Join(dir, "profile.json"), nil); !errors.Is(err, ErrAtomicRecovery) {
		t.Fatal("unknown DACL admitted", err)
	}
	if _, err = os.Stat(p); err != nil {
		t.Fatal("unverified snapshot removed", err)
	}
}
