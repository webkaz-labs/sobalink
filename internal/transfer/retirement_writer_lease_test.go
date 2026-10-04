package transfer

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func TestRetirementCloseErrorStillReleasesWriter(t *testing.T) {
	file, before := intentFixture(t)
	if err := file.SaveReceiveAccounting(before); err != nil {
		t.Fatal(err)
	}
	after := ReceiveAccounting{Version: 2, Roots: []ReceiveRoot{}}
	g, err := retirementGuard(before, after, false)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := file.AcquireReceiveRetirementGuard(g, accountingLimits(AccountingLimits{}))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file.Path + ".retirement")
	if err != nil {
		t.Fatal(err)
	}
	l := lease.(*fileRetirementLease)
	// The guard handle close error cannot retain the parent writer exclusion.
	if err := l.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("expected guard close failure: %v", err)
	}
	if err := config.AtomicWritePrivate(filepath.Join(filepath.Dir(file.Path), "manage.json"), []byte("available")); err != nil {
		t.Fatal("writer exclusion leaked", err)
	}
	kept, err := os.ReadFile(file.Path + ".retirement")
	if err != nil || !bytes.Equal(kept, raw) {
		t.Fatal("failed close removed evidence", err)
	}
}

func TestRetirementNativeGuardParentMustMatchWriter(t *testing.T) {
	file, _ := intentFixture(t)
	writer, err := config.AcquireAtomicWriteLease(file.Path, file.retirementName())
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := openDestination(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := rootIdentity(wrong)
	if err != nil {
		t.Fatal(err)
	}
	l := &fileRetirementLease{parent: wrong, parentIdentity: identity, writer: writer}
	defer l.Close()
	if err := l.checkWriterParent(); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("native guard parent mismatch accepted: %v", err)
	}
}

func TestRetirementTypedNilLeaseRejectsSaveAndClose(t *testing.T) {
	file, _ := intentFixture(t)
	after := ReceiveAccounting{Version: 2, Roots: []ReceiveRoot{}}
	var lease *fileRetirementLease
	if err := file.SaveReceiveAccounting(after, lease); err == nil {
		t.Fatal("typed nil lease accepted")
	}
	if err := lease.Close(); !errors.Is(err, ErrState) {
		t.Fatal(err)
	}
	if err := lease.Release(func() error { return nil }); !errors.Is(err, ErrState) {
		t.Fatal(err)
	}
}
