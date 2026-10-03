package transfer

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAdvisorMissingIntentRetirement(t *testing.T) {
	for _, mode := range []string{"commit-then-error", "restore-fails"} {
		t.Run(mode, func(t *testing.T) {
			file, state := intentFixture(t)
			if err := file.SaveReceiveAccounting(state); err != nil {
				t.Fatal(err)
			}
			store := &prepareFailureStore{FileReceiveAccountingStore: file}
			injected := false
			store.onSave = func(next ReceiveAccounting, leases ...ReceiveRetirementLease) error {
				if next.Preparation != nil {
					return errors.New("restoration unavailable")
				}
				if err := file.SaveReceiveAccounting(next, leases...); err != nil {
					return err
				}
				if !injected {
					injected = true
					root := filepath.Join(state.Preparation.Destination, state.Preparation.Root)
					if err := os.Mkdir(root, 0700); err != nil {
						t.Fatal(err)
					}
					mustWrite(t, filepath.Join(root, "unknown"), "preserve")
				}
				if mode == "commit-then-error" {
					return errors.New("response lost after commit")
				}
				return nil
			}
			m, err := NewManager(Options{AccountingStore: store, ExistingState: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("initial=%s", m.ReceiveRecovery().State)
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			disk, err := file.LoadReceiveAccounting()
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("durable intent=%t roots=%d", disk.Preparation != nil, len(disk.Roots))
			reopened, err := NewManager(Options{AccountingStore: file, ExistingState: true})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if got := reopened.ReceiveRecovery().State; got != "blocked" {
				t.Errorf("SAFETY: reopen=%s despite unknown root after %s", got, mode)
			}
			if data, err := os.ReadFile(filepath.Join(state.Preparation.Destination, state.Preparation.Root, "unknown")); err != nil || string(data) != "preserve" {
				t.Fatal("unknown changed", err)
			}
		})
	}
}

func TestAdvisorFinalPromotionRetirementSubstitution(t *testing.T) {
	for _, target := range []string{"root", "destination", "stage", "marker"} {
		t.Run(target, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("open-directory rename")
			}
			file := FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}
			store := &prepareFailureStore{FileReceiveAccountingStore: file}
			m, peer := testManager(t, Options{AccountingStore: store})
			if _, err := m.Offer(peer, testManifest("fixture", testEntry("f", "file", "x"))); err != nil {
				t.Fatal(err)
			}
			once := false
			store.onSave = func(next ReceiveAccounting, leases ...ReceiveRetirementLease) error {
				if err := file.SaveReceiveAccounting(next, leases...); err != nil {
					return err
				}
				if !once && next.Preparation == nil && len(next.Roots) > 0 {
					once = true
					r := next.Roots[0]
					name := r.OwnedRoot
					switch target {
					case "destination":
						name = r.Destination
					case "stage":
						name = filepath.Join(name, r.Stage)
					case "marker":
						name = filepath.Join(name, r.Stage, receiveOwnerMarker)
					}
					if err := os.Rename(name, name+"-original"); err != nil {
						t.Fatal(err)
					}
					if target == "marker" {
						mustWrite(t, name, r.OwnerToken)
					} else {
						if err := os.Mkdir(name, 0700); err != nil {
							t.Fatal(err)
						}
					}
				}
				return nil
			}
			_, err := m.Accept("fixture", t.TempDir())
			if !errors.Is(err, ErrReceiveRecovery) || m.ReceiveRecovery().State != "blocked" {
				t.Errorf("SAFETY: final-save substitution accepted: err=%v state=%s", err, m.ReceiveRecovery().State)
			}
			store.onSave = nil
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			disk, err := file.LoadReceiveAccounting()
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := NewManager(Options{AccountingStore: file, ExistingState: true})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			t.Logf("after Close: intent=%t roots=%d reopen=%s", disk.Preparation != nil, len(disk.Roots), reopened.ReceiveRecovery().State)
			if reopened.ReceiveRecovery().State != "blocked" {
				t.Errorf("SAFETY: Close erased replacement evidence")
			}
		})
	}
}

func TestAdvisorRuntimeRootRetirementSubstitution(t *testing.T) {
	file := FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}
	store := &prepareFailureStore{FileReceiveAccountingStore: file}
	m, peer := testManager(t, Options{AccountingStore: store})
	if _, err := m.Offer(peer, testManifest("fixture", testEntry("f", "file", "x"))); err != nil {
		t.Fatal(err)
	}
	b, err := m.Accept("fixture", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	once := false
	store.onSave = func(next ReceiveAccounting, leases ...ReceiveRetirementLease) error {
		if err := file.SaveReceiveAccounting(next, leases...); err != nil {
			return err
		}
		if !once && len(next.Roots) == 0 {
			once = true
			if err := os.Rename(b.Destination, b.Destination+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(b.Destination, 0700); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(b.Destination, "unknown"), "preserve")
		}
		return nil
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("after Close: recovery=%s", m.ReceiveRecovery().State)
	reopened, err := NewManager(Options{AccountingStore: file, ExistingState: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.ReceiveRecovery().State != "blocked" {
		t.Errorf("SAFETY: runtime retirement reopened ready with unknown replacement")
	}
}

func TestAdvisorStartupRootRetirementSubstitution(t *testing.T) {
	dir, _, record := accountingFixture(t)
	file := FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")}
	store := &prepareFailureStore{FileReceiveAccountingStore: file}
	once := false
	store.onSave = func(next ReceiveAccounting, leases ...ReceiveRetirementLease) error {
		if err := file.SaveReceiveAccounting(next, leases...); err != nil {
			return err
		}
		if !once && len(next.Roots) == 0 {
			once = true
			if err := os.Rename(record.OwnedRoot, record.OwnedRoot+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(record.OwnedRoot, 0700); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(record.OwnedRoot, "unknown"), "preserve")
		}
		return nil
	}
	m, err := NewManager(Options{AccountingStore: store, ExistingState: true})
	if err != nil {
		t.Fatal(err)
	}
	if m.ReceiveRecovery().State != "blocked" {
		t.Errorf("SAFETY: startup retired substituted marker-only root and became ready")
	}
	m.Close()
	reopened, err := NewManager(Options{AccountingStore: file, ExistingState: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.ReceiveRecovery().State != "blocked" {
		t.Errorf("SAFETY: startup retirement lost durable replacement evidence")
	}
}
