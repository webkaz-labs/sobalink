package config_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func peakHash(s transfer.ReceiveAccounting) string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func peakIntentFixture(t *testing.T) (transfer.FileReceiveAccountingStore, transfer.ReceiveRetirementGuard, transfer.AccountingLimits) {
	t.Helper()
	// Short paths retain the reported 1317-byte example on native Unix runs.
	dir, err := os.MkdirTemp("", "soba-peak-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	destination := filepath.Join(dir, "receive")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	identity, err := config.DirectoryIdentityForTest(destination)
	if err != nil {
		t.Fatal(err)
	}
	// Same exported intent as transfer.intentFixture: planned root is absent.
	before := transfer.ReceiveAccounting{Version: 2, Roots: []transfer.ReceiveRoot{}, Preparation: &transfer.ReceivePreparation{
		Destination: destination, DestinationIdentity: identity, Root: "sobalink-" + strings.Repeat("a", 32), Stage: ".incoming-" + strings.Repeat("b", 32), OwnerToken: strings.Repeat("c", 32),
	}}
	after := transfer.ReceiveAccounting{Version: 2, Roots: []transfer.ReceiveRoot{}}
	g := transfer.ReceiveRetirementGuard{Version: 1, ID: strings.Repeat("d", 32), Kind: "retire", Before: before, After: after, BeforeHash: peakHash(before), AfterHash: peakHash(after), Witnesses: []transfer.ReceiveRetirementWitness{}}
	b, _ := json.Marshal(before)
	raw, _ := json.Marshal(g)
	bound := 2*int64(len(b)) + int64(len(raw))
	limit := max(int64(1317), bound+16)
	limits := transfer.AccountingLimits{MaxBytes: limit, MaxEntries: 4}
	file := transfer.FileReceiveAccountingStore{Path: filepath.Join(dir, "index.json"), Limits: limits}
	if err := file.SaveReceiveAccounting(before); err != nil {
		t.Fatal(err)
	}
	t.Logf("B=%d G=%d protected=2B+G=%d admission=%d", len(b), len(raw), bound, limit)
	return file, g, limits
}

// Absence is accepted only when the calling boundary expects ENOENT. Other
// inspection errors and unexpected disappearance never count as zero.
func peakSize(t *testing.T, path string, absent bool) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if absent {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected absent %s: info=%v err=%v", path, info, err)
		}
		return 0
	}
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

func peakChild(t *testing.T, dir, mode string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrossCallerPeakChild$")
	cmd.Env = append(os.Environ(), "SOBALINK_PEAK_CHILD="+dir, "SOBALINK_PEAK_MODE="+mode)
	output, err := cmd.CombinedOutput()
	if mode == "crash" {
		var e *exec.ExitError
		if !errors.As(err, &e) || e.ExitCode() != 89 {
			t.Fatalf("crash child: %v %s", err, output)
		}
	} else if err != nil {
		t.Fatalf("contending child: %v %s", err, output)
	}
}

func TestCrossCallerPeakChild(t *testing.T) {
	dir := os.Getenv("SOBALINK_PEAK_CHILD")
	if dir == "" {
		return
	}
	path := filepath.Join(dir, "caller.json")
	if os.Getenv("SOBALINK_PEAK_MODE") == "crash" {
		err := config.AtomicWriteForTest(path, bytes.Repeat([]byte("p"), 4096), func(phase string) {
			if phase == "afterSync" {
				os.Exit(89)
			}
		})
		t.Fatalf("child failed to crash after real snapshot sync: %v", err)
	}
	if err := config.AtomicWritePrivate(path, []byte("competitor")); !errors.Is(err, config.ErrAtomicBusy) {
		t.Fatalf("competing writer admitted: %v", err)
	}
}

type peakStore struct {
	transfer.FileReceiveAccountingStore
	acquired func()
	before   func()
	after    func()
	release  func()
	saveErr  error
	saves    int
}

func (s *peakStore) WithReceiveAccountingLimits(l transfer.AccountingLimits) transfer.ReceiveAccountingStore {
	s.Limits = l
	return s
}
func (s *peakStore) SaveReceiveAccounting(a transfer.ReceiveAccounting, leases ...transfer.ReceiveRetirementLease) error {
	s.saves++
	if len(leases) != 1 {
		return fmt.Errorf("retirement decorator lost explicit lease: %d", len(leases))
	}
	if s.before != nil {
		s.before()
	}
	if s.saveErr != nil {
		return s.saveErr
	}
	if err := s.FileReceiveAccountingStore.SaveReceiveAccounting(a, leases...); err != nil {
		return err
	}
	if s.after != nil {
		s.after()
	}
	return nil
}
func (s *peakStore) AcquireReceiveRetirementGuard(g transfer.ReceiveRetirementGuard, l transfer.AccountingLimits) (transfer.ReceiveRetirementLease, error) {
	lease, err := s.FileReceiveAccountingStore.AcquireReceiveRetirementGuard(g, l)
	if err != nil {
		return nil, err
	}
	if s.acquired != nil {
		s.acquired()
	}
	return &peakLease{ReceiveRetirementLease: lease, release: s.release}, nil
}

type peakLease struct {
	transfer.ReceiveRetirementLease
	release func()
}

func (l *peakLease) Release(v func() error) error {
	if l.release != nil {
		l.release()
	}
	return l.ReceiveRetirementLease.Release(v)
}

func TestCrossCallerInterruptedSnapshotRetirementPeak(t *testing.T) {
	file, _, limits := peakIntentFixture(t)
	dir := filepath.Dir(file.Path)
	guard := file.Path + ".retirement"
	slot := filepath.Join(dir, ".sobalink-atomic-v1", "snapshot")
	peakChild(t, dir, "crash")
	if n := peakSize(t, slot, false); n != 4096 {
		t.Fatal(n)
	}
	counts := map[string]int{}
	var peak int64
	observe := func(phase string, guardAbsent, snapshotAbsent bool) {
		counts[phase]++
		n := peakSize(t, file.Path, false) + peakSize(t, guard, guardAbsent) + peakSize(t, slot, snapshotAbsent)
		if phase == "beforeRemoveSnapshot" {
			if n <= limits.MaxBytes {
				t.Fatalf("prior caller must exceed retirement admission: %d", n)
			}
			return
		}
		peak = max(peak, n)
		if n > limits.MaxBytes {
			t.Fatalf("protected phase %s: actual=%d admitted=%d", phase, n, limits.MaxBytes)
		}
	}
	restore := config.ObserveAtomicAdmissionForTest(func(phase string) {
		switch phase {
		case "beforeRemoveSnapshot":
			observe(phase, true, false)
		case "afterReclaim":
			observe(phase, true, true)
		case "afterCreateTemp", "afterSync", "beforeReplace":
			observe(phase, false, false)
		}
	}, nil)
	defer restore()
	s := &peakStore{FileReceiveAccountingStore: file}
	s.acquired = func() { observe("guard-durable", false, true) }
	s.before = func() { observe("decorated-before", false, true) }
	s.after = func() { observe("decorated-after", false, true) }
	s.release = func() { observe("before-release", false, true) }
	m, err := transfer.NewManager(transfer.Options{AccountingStore: s, ExistingState: true, AccountingLimits: limits})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	observe("released", true, true)
	if m.ReceiveRecovery().State != "ready" || s.saves != 1 {
		t.Fatalf("actual missing-root retirement: %+v saves=%d", m.ReceiveRecovery(), s.saves)
	}
	for _, phase := range []string{"beforeRemoveSnapshot", "afterReclaim", "afterCreateTemp", "afterSync", "beforeReplace", "guard-durable", "decorated-before", "decorated-after", "before-release", "released"} {
		if counts[phase] != 1 {
			t.Fatalf("boundary %s observed %d times", phase, counts[phase])
		}
	}
	t.Logf("actual protected peak=%d admitted=%d; prior snapshot=4096 durably reclaimed before guard", peak, limits.MaxBytes)
}

func TestCrossCallerRetirementExclusionAtCriticalPhases(t *testing.T) {
	file, _, limits := peakIntentFixture(t)
	dir := filepath.Dir(file.Path)
	aliasRoot := dir + "-alias"
	alias := filepath.Join(aliasRoot, filepath.Base(dir))
	if err := os.Symlink(filepath.Dir(dir), aliasRoot); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(aliasRoot)
	phases := 0
	contender := func(phase string) {
		phases++
		result := make(chan error, 1)
		go func() { result <- config.AtomicWritePrivate(filepath.Join(alias, "goroutine.json"), []byte(phase)) }()
		if err := <-result; !errors.Is(err, config.ErrAtomicBusy) {
			t.Fatalf("%s goroutine writer: %v", phase, err)
		}
		peakChild(t, alias, "busy")
		peakSize(t, filepath.Join(dir, ".sobalink-atomic-v1", "snapshot"), true)
	}
	restore := config.ObserveAtomicAdmissionForTest(func(phase string) {
		if phase == "afterReclaim" {
			contender(phase)
		}
	}, nil)
	defer restore()
	s := &peakStore{FileReceiveAccountingStore: file}
	s.acquired = func() { contender("guard-durable") }
	s.before = func() { contender("before-save") }
	s.after = func() { contender("index-replaced") }
	s.release = func() { contender("before-release") }
	m, err := transfer.NewManager(transfer.Options{AccountingStore: s, ExistingState: true, AccountingLimits: limits})
	restore()
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if phases != 5 || m.ReceiveRecovery().State != "ready" {
		t.Fatalf("phases=%d recovery=%+v", phases, m.ReceiveRecovery())
	}
	if err := config.AtomicWritePrivate(filepath.Join(alias, "caller.json"), bytes.Repeat([]byte("p"), 4096)); err != nil {
		t.Fatal("writer unavailable after release", err)
	}
}

func TestCrossCallerFailedRetirementRetainsGuardAndOtherWriterLimits(t *testing.T) {
	file, g, limits := peakIntentFixture(t)
	s := &peakStore{FileReceiveAccountingStore: file, saveErr: errors.New("decorator unavailable")}
	m, err := transfer.NewManager(transfer.Options{AccountingStore: s, ExistingState: true, AccountingLimits: limits})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.ReceiveRecovery().State != "blocked" || s.saves != 1 {
		t.Fatalf("failure: %+v saves=%d", m.ReceiveRecovery(), s.saves)
	}
	guard := file.Path + ".retirement"
	raw, err := os.ReadFile(guard)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(file.Path)
	legacy := filepath.Join(dir, ".write-unknown-legacy")
	if err := os.WriteFile(legacy, []byte("preserved legacy"), 0600); err != nil {
		t.Fatal(err)
	}
	// Management remains writable, including a subsequent real child crash with
	// a larger caller allocation. The guard is blocking evidence, not a global cap.
	if err := config.AtomicWritePrivate(filepath.Join(dir, "manage.json"), bytes.Repeat([]byte("m"), 8192)); err != nil {
		t.Fatal(err)
	}
	peakChild(t, dir, "crash")
	actual := peakSize(t, file.Path, false) + peakSize(t, guard, false) + peakSize(t, filepath.Join(dir, ".sobalink-atomic-v1", "snapshot"), false)
	b, _ := json.Marshal(g.Before)
	envelope := int64(len(b)+len(raw)) + max(int64(len(b)), int64(4096))
	if actual != envelope || actual <= limits.MaxBytes {
		t.Fatalf("composed bound actual=%d envelope=%d admission=%d", actual, envelope, limits.MaxBytes)
	}
	kept, err := os.ReadFile(guard)
	if err != nil || !bytes.Equal(kept, raw) {
		t.Fatal("guard changed", err)
	}
	if kept, err := os.ReadFile(legacy); err != nil || string(kept) != "preserved legacy" {
		t.Fatal("legacy changed", err)
	}
	if err := config.AtomicWritePrivate(filepath.Join(dir, "manage.json"), []byte("still available")); err != nil {
		t.Fatal(err)
	}
	kept, err = os.ReadFile(guard)
	if err != nil || !bytes.Equal(kept, raw) {
		t.Fatal("guard changed after reclaim", err)
	}
	if _, err := m.ConfirmReceiveRecovery(context.Background(), true); err == nil {
		t.Fatal("decorator failure unblocked receive")
	}
	t.Logf("failed retirement: actual=%d = B+G+max(B,4096); guard unchanged; management available", actual)
}

func TestCrossCallerRetirementAdmissionFailureAllocatesNoGuard(t *testing.T) {
	for _, mode := range []string{"unknown-slot", "hardlinked-slot", "reclaim-sync"} {
		t.Run(mode, func(t *testing.T) {
			file, g, limits := peakIntentFixture(t)
			dir := filepath.Dir(file.Path)
			owned := filepath.Join(dir, ".sobalink-atomic-v1")
			slot := filepath.Join(owned, "snapshot")
			failure := errors.New("reclaim directory sync unavailable")
			var preserved string
			switch mode {
			case "unknown-slot":
				preserved = filepath.Join(owned, "unknown")
				if err := os.WriteFile(preserved, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "hardlinked-slot":
				preserved = filepath.Join(dir, "saved-output")
				if err := os.WriteFile(preserved, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(preserved, slot); err != nil {
					t.Fatal(err)
				}
			case "reclaim-sync":
				peakChild(t, dir, "crash")
			}
			restore := func() {}
			if mode == "reclaim-sync" {
				restore = config.ObserveAtomicAdmissionForTest(nil, func() error { return failure })
			}
			lease, err := file.AcquireReceiveRetirementGuard(g, limits)
			restore()
			if err == nil || lease != nil {
				t.Fatalf("unsafe admission: lease=%v err=%v", lease, err)
			}
			peakSize(t, file.Path+".retirement", true)
			if mode == "reclaim-sync" {
				// A failed real unlink barrier releases exclusion. Next admission must
				// acknowledge the directory even though the removed slot is now absent.
				called := 0
				restore = config.ObserveAtomicAdmissionForTest(nil, func() error { called++; return failure })
				lease, err = file.AcquireReceiveRetirementGuard(g, limits)
				restore()
				if err == nil || lease != nil || called != 1 {
					t.Fatalf("absent-slot barrier skipped: calls=%d err=%v", called, err)
				}
				peakSize(t, file.Path+".retirement", true)
				if err := config.AtomicWritePrivate(filepath.Join(dir, "manage.json"), []byte("available")); err != nil {
					t.Fatal("exclusion leaked", err)
				}
				lease, err = file.AcquireReceiveRetirementGuard(g, limits)
				if err != nil {
					t.Fatal("admission after acknowledged sync", err)
				}
				lease.Close()
			} else {
				if b, err := os.ReadFile(preserved); err != nil || string(b) != "preserve" {
					t.Fatal("unknown/hardlinked data lost", err)
				}
				if mode == "hardlinked-slot" {
					if b, err := os.ReadFile(slot); err != nil || string(b) != "preserve" {
						t.Fatal("hardlink removed", err)
					}
				}
			}
		})
	}
}

func TestCrossCallerGuardReservesMissingParentEntry(t *testing.T) {
	for _, entries := range []int{4095, 4096} {
		t.Run(fmt.Sprint(entries), func(t *testing.T) {
			file, g, limits := peakIntentFixture(t)
			dir := filepath.Dir(file.Path)
			initial, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for i := len(initial); i < entries; i++ {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("foreign-%04d", i)), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			lease, err := file.AcquireReceiveRetirementGuard(g, limits)
			if entries == 4096 {
				if err == nil || lease != nil {
					t.Fatal("guard exceeded parent ceiling", err)
				}
				peakSize(t, file.Path+".retirement", true)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := file.SaveReceiveAccounting(g.After, lease); err != nil {
					t.Fatal("reserved slot save failed", err)
				}
				if err := lease.Release(func() error { return nil }); err != nil {
					t.Fatal(err)
				}
				if err := lease.Close(); err != nil {
					t.Fatal(err)
				}
				if err := file.SaveReceiveAccounting(g.After); err != nil {
					t.Fatal("next ordinary save blocked", err)
				}
			}
			all, err := os.ReadDir(dir)
			if err != nil || len(all) != entries {
				t.Fatalf("parent entries=%d expected=%d err=%v", len(all), entries, err)
			}
		})
	}
}

func TestCrossCallerRetirementLeaseRejectsUnauthorizedSave(t *testing.T) {
	file, g, limits := peakIntentFixture(t)
	lease, err := file.AcquireReceiveRetirementGuard(g, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	raw, err := os.ReadFile(file.Path + ".retirement")
	if err != nil {
		t.Fatal(err)
	}
	other := file
	other.Path = filepath.Join(filepath.Dir(file.Path), "other-index.json")
	for name, save := range map[string]func() error{
		"nil":              func() error { return file.SaveReceiveAccounting(g.After, nil) },
		"multiple":         func() error { return file.SaveReceiveAccounting(g.After, lease, lease) },
		"wrong-target":     func() error { return other.SaveReceiveAccounting(g.After, lease) },
		"wrong-after":      func() error { return file.SaveReceiveAccounting(g.Before, lease) },
		"wrong-bytes":      func() error { return lease.LeaseWrite(file.Path, []byte("{}")) },
		"typed-nil-writer": func() error { var l *config.AtomicWriteLease; return l.Write(file.Path, []byte("{}")) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := save(); err == nil {
				t.Fatal("unauthorized save accepted")
			}
		})
	}
	disk, err := file.LoadReceiveAccounting()
	if err != nil || peakHash(disk) != g.BeforeHash {
		t.Fatal("invalid save changed index", err)
	}
	if err := file.SaveReceiveAccounting(g.After, lease); err != nil {
		t.Fatal("authorized save rejected", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.SaveReceiveAccounting(g.After, lease); err == nil {
		t.Fatal("closed lease accepted")
	}
	if err := lease.Release(func() error { return nil }); err == nil {
		t.Fatal("closed release accepted")
	}
	if err := lease.Close(); err == nil {
		t.Fatal("double close accepted")
	}
	kept, err := os.ReadFile(file.Path + ".retirement")
	if err != nil || !bytes.Equal(kept, raw) {
		t.Fatal("Close removed evidence", err)
	}
	// Recovery reclaims a subsequent caller's real interrupted snapshot under
	// the same exclusion while retaining the already-present guard unchanged.
	peakChild(t, filepath.Dir(file.Path), "crash")
	recoveredReclaim := false
	restore := config.ObserveAtomicAdmissionForTest(func(phase string) {
		if phase == "beforeRemoveSnapshot" {
			peakSize(t, file.Path+".retirement", false)
			if peakSize(t, filepath.Join(filepath.Dir(file.Path), ".sobalink-atomic-v1", "snapshot"), false) != 4096 {
				t.Fatal("missing interrupted recovery slot")
			}
		}
		if phase == "afterReclaim" {
			recoveredReclaim = true
			peakSize(t, file.Path+".retirement", false)
			peakSize(t, filepath.Join(filepath.Dir(file.Path), ".sobalink-atomic-v1", "snapshot"), true)
		}
	}, nil)
	defer restore()
	recovered, live, err := file.LoadReceiveRetirementGuard(limits)
	restore()
	if !recoveredReclaim {
		t.Fatal("present guard recovery did not use leased reclamation")
	}
	if err != nil {
		t.Fatal(err)
	}
	if recovered.AfterHash != g.AfterHash {
		t.Fatal("recovered binding changed")
	}
	dir := filepath.Dir(file.Path)
	aliasRoot := dir + "-alias"
	alias := filepath.Join(aliasRoot, filepath.Base(dir))
	if err := os.Symlink(filepath.Dir(dir), aliasRoot); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(aliasRoot)
	peakChild(t, alias, "busy")
	if err := file.SaveReceiveAccounting(g.After, live); err != nil {
		t.Fatal(err)
	}
	if err := live.Release(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := live.LeaseWrite(file.Path, []byte("{}")); err == nil {
		t.Fatal("released lease accepted")
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	if err := config.AtomicWritePrivate(filepath.Join(alias, "caller.json"), []byte("after-close")); err != nil {
		t.Fatal(err)
	}
}

func TestCrossCallerPresentGuardMissingWriterOwnerBlocks(t *testing.T) {
	file, g, limits := peakIntentFixture(t)
	lease, err := file.AcquireReceiveRetirementGuard(g, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	if err := file.SaveReceiveAccounting(g.After, lease); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	guard, err := os.ReadFile(file.Path + ".retirement")
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(file.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(filepath.Dir(file.Path), ".sobalink-atomic-v1", "owner.lock")); err != nil {
		t.Fatal(err)
	}
	loaded, held, err := file.LoadReceiveRetirementGuard(limits)
	if held != nil {
		_ = held.Close()
	}
	if !errors.Is(err, transfer.ErrReceiveRecovery) || errors.Is(err, os.ErrNotExist) || loaded != nil || held != nil {
		t.Fatalf("present guard admission must require recovery: guard=%v lease=%v err=%v", loaded, held, err)
	}
	m, err := transfer.NewManager(transfer.Options{AccountingStore: file, ExistingState: true, AccountingLimits: limits})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if view := m.ReceiveRecovery(); view.State != "blocked" || view.Applied {
		t.Fatalf("startup probe bypassed present guard: %+v", view)
	}
	if view, err := m.ConfirmReceiveRecovery(context.Background(), true); !errors.Is(err, transfer.ErrReceiveRecovery) || view.State != "blocked" || view.Applied {
		t.Fatalf("recovery probe bypassed present guard: %+v err=%v", view, err)
	}
	for path, want := range map[string][]byte{file.Path: index, file.Path + ".retirement": guard} {
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("evidence changed: %s err=%v", path, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(file.Path), ".sobalink-atomic-v1", "owner.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing owner recreated: %v", err)
	}
}

func TestCrossCallerGuardDisappearsAfterPositiveLookup(t *testing.T) {
	file, g, limits := peakIntentFixture(t)
	lease, err := file.AcquireReceiveRetirementGuard(g, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	removed := false
	restore := config.ObserveAtomicAdmissionForTest(func(phase string) {
		if phase == "afterReclaim" {
			if err := os.Remove(file.Path + ".retirement"); err != nil {
				t.Fatal(err)
			}
			removed = true
		}
	}, nil)
	t.Cleanup(restore)
	_, held, err := file.LoadReceiveRetirementGuard(limits)
	if held != nil {
		_ = held.Close()
	}
	restore()
	if !removed || !errors.Is(err, transfer.ErrReceiveRecovery) || errors.Is(err, os.ErrNotExist) || held != nil {
		t.Fatalf("later guard disappearance classified absent: removed=%v lease=%v err=%v", removed, held, err)
	}
	// A successful ordinary write proves the failed load closed its writer lease.
	if err := config.AtomicWritePrivate(filepath.Join(filepath.Dir(file.Path), "manage.json"), []byte("after failed load")); err != nil {
		t.Fatalf("load leaked exclusion: %v", err)
	}
}

func TestCrossCallerAbsentGuardDoesNotCreateNamespace(t *testing.T) {
	dir := t.TempDir()
	file := transfer.FileReceiveAccountingStore{Path: filepath.Join(dir, "index.json")}
	_, held, err := file.LoadReceiveRetirementGuard(transfer.AccountingLimits{})
	if held != nil {
		_ = held.Close()
	}
	if !errors.Is(err, os.ErrNotExist) || held != nil {
		t.Fatalf("genuinely absent guard: lease=%v err=%v", held, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("absent guard created namespace: %v err=%v", entries, err)
	}
}
