package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/diskspace"
)

// Only the directory barrier is intercepted. All identity checks, removals,
// index/guard writes and Manager.Close/reopen use the actual file-backed store.
func interceptRetirementSync(t *testing.T, hook func(*os.Root) error) {
	t.Helper()
	original := retirementDirectorySync
	retirementDirectorySync = func(root *os.Root) error {
		if err := hook(root); err != nil {
			return err
		}
		return original(root)
	}
	t.Cleanup(func() { retirementDirectorySync = original })
}

func TestRetirementMissingStageRetriesDirectorySync(t *testing.T) {
	for _, missing := range []string{"removed-stage", "inventory-stage", "inventory-root", "inventory-plan"} {
		t.Run(missing, func(t *testing.T) {
			dir, state, r := accountingFixture(t)
			file := FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")}
			barrier := r.OwnedRoot
			if missing == "inventory-root" || missing == "inventory-plan" {
				barrier = r.Destination
				if err := os.RemoveAll(r.OwnedRoot); err != nil {
					t.Fatal(err)
				}
			} else if missing == "inventory-stage" {
				if err := os.RemoveAll(filepath.Join(r.OwnedRoot, r.Stage)); err != nil {
					t.Fatal(err)
				}
			}
			if missing == "inventory-plan" {
				state.Version, state.Roots = 2, nil
				state.Preparation = &ReceivePreparation{Destination: r.Destination, DestinationIdentity: r.DestinationIdentity, Root: filepath.Base(r.OwnedRoot), Stage: r.Stage, OwnerToken: r.OwnerToken}
				if err := file.SaveReceiveAccounting(state); err != nil {
					t.Fatal(err)
				}
			}
			preserved := filepath.Join(r.Destination, "foreign")
			if barrier == r.OwnedRoot {
				preserved = filepath.Join(r.OwnedRoot, receiveOwnerMarker)
			}
			if err := os.WriteFile(preserved, []byte("saved output"), 0600); err != nil {
				t.Fatal(err)
			}
			store := &guardFaultStore{FileReceiveAccountingStore: file}
			attempts, unavailable := 0, true
			interceptRetirementSync(t, func(root *os.Root) error {
				if root.Name() == barrier {
					attempts++
					if unavailable {
						return errors.New("directory durability unavailable")
					}
				}
				return nil
			})
			open := func() *Manager {
				t.Helper()
				m, err := NewManager(Options{AccountingStore: store, ExistingState: true})
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			blocked := func(m *Manager, wantAttempts int) {
				t.Helper()
				if m.ReceiveRecovery().State != "blocked" || attempts != wantAttempts || store.saves != 0 {
					t.Fatalf("barrier failure bypassed: recovery=%+v attempts=%d saves=%d", m.ReceiveRecovery(), attempts, store.saves)
				}
				g := assertGuard(t, file)
				if g.BeforeHash != accountingHash(state) || g.BeforeHash != accountingHash(g.Before) || g.AfterHash != accountingHash(g.After) {
					t.Fatal("guard snapshot hashes changed")
				}
				current, err := file.LoadReceiveAccounting()
				if err != nil || accountingHash(current) != g.BeforeHash {
					t.Fatal("failed barrier published the after index", err)
				}
			}
			first := open()
			blocked(first, 1)
			if _, err := os.Lstat(filepath.Join(r.OwnedRoot, r.Stage)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("stage must be absent after removal", err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			second := open()
			blocked(second, 2)
			if view, err := second.ConfirmReceiveRecovery(context.Background(), true); !errors.Is(err, ErrReceiveRecovery) || view.Applied || view.State != "blocked" {
				t.Fatal("local review bypassed failed barrier", view, err)
			}
			blocked(second, 3)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if view, err := second.ConfirmReceiveRecovery(ctx, true); !errors.Is(err, context.Canceled) || view.Applied {
				t.Fatal("cancelled review bypassed guard", view, err)
			}
			blocked(second, 3)
			if err := second.Close(); err != nil {
				t.Fatal(err)
			}
			assertGuard(t, file)
			unavailable = false
			third := open()
			defer third.Close()
			if third.ReceiveRecovery().State != "ready" || attempts != 6 || store.saves != 1 {
				t.Fatalf("successful retry: %+v attempts=%d saves=%d", third.ReceiveRecovery(), attempts, store.saves)
			}
			if _, err := os.Lstat(file.Path + ".retirement"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("successful barrier did not release guard", err)
			}
			current, err := file.LoadReceiveAccounting()
			if err != nil || len(current.Roots) != 0 || current.Preparation != nil {
				t.Fatal("successful retry did not retire evidence", current, err)
			}
			if data, err := os.ReadFile(preserved); err != nil || string(data) != "saved output" {
				t.Fatal("saved/foreign bytes changed", err)
			}
			t.Logf("failure -> Close/reopen -> failing local review -> actual successful barrier: attempts=%d saves=%d", attempts, store.saves)
		})
	}
}

func TestRetirementRollbackSyncsMissingRootParent(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "successful-barrier"
		if fail {
			name = "failed-barrier"
		}
		t.Run(name, func(t *testing.T) {
			file := FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}
			destination := t.TempDir()
			attempts := 0
			interceptRetirementSync(t, func(root *os.Root) error {
				if root.Name() == destination {
					attempts++
					if len(mustReadDir(t, destination)) != 0 {
						t.Fatal("destination barrier must observe the already removed provisional root")
					}
					g := assertGuard(t, file)
					if g.Before.Preparation == nil || g.After.Preparation != nil {
						t.Fatal("barrier did not retain plan-to-missing-root guard")
					}
					if fail {
						return errors.New("destination durability unavailable")
					}
				}
				return nil
			})
			calls := 0
			space := diskspace.New(func(*os.File) (uint64, error) {
				calls++
				if calls == 4 {
					return 0, nil
				}
				return 1 << 40, nil
			})
			m, peer := testManager(t, Options{AccountingStore: file, DiskSpace: space})
			if _, err := m.Offer(peer, testManifest("rollback-sync", testEntry("f", "a/b/file", "x"))); err != nil {
				t.Fatal(err)
			}
			b, err := m.Accept("rollback-sync", destination)
			var want error = diskspace.ErrLow
			if fail {
				want = ErrReceiveRecovery
			}
			if !errors.Is(err, want) || b.State != Pending || len(mustReadDir(t, destination)) != 0 {
				t.Fatal("expected post-create rollback", b, err)
			}
			state, err := file.LoadReceiveAccounting()
			if err != nil || (state.Preparation != nil) != fail || len(state.Roots) != 0 {
				t.Fatal("rollback evidence", state, err)
			}
			if fail {
				g := assertGuard(t, file)
				if attempts != 1 {
					t.Fatal("failed barrier retried without caller", attempts)
				}
				if _, err := m.Accept("rollback-sync", destination); !errors.Is(err, ErrReceiveRecovery) || attempts != 1 {
					t.Fatal("Accept bypassed/spun on guard", err, attempts)
				}
				if cancelled, err := m.Cancel("rollback-sync"); err != nil || cancelled.State != Cancelled {
					t.Fatal("cancellation truth", cancelled, err)
				}
				if after, err := file.LoadReceiveAccounting(); err != nil || accountingHash(after) != g.BeforeHash || attempts != 1 {
					t.Fatal("Cancel discarded unresolved evidence", err, attempts)
				}
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := NewManager(Options{AccountingStore: file, ExistingState: true})
				if err != nil {
					t.Fatal(err)
				}
				if reopened.ReceiveRecovery().State != "blocked" || attempts != 2 {
					t.Fatal("restart bypassed failed destination barrier", attempts, reopened.ReceiveRecovery())
				}
				assertGuard(t, file)
				fail = false
				view, err := reopened.ConfirmReceiveRecovery(context.Background(), true)
				if err != nil || view.State != "ready" || !view.Applied || attempts != 5 {
					t.Fatal("successful actual barrier did not recover", view, err, attempts)
				}
				defer reopened.Close()
			} else {
				if attempts != 3 {
					t.Fatal("publication/release must verify destination barrier", attempts)
				}
				defer m.Close()
			}
			if _, err := os.Lstat(file.Path + ".retirement"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("successful retry retained guard", err)
			}
			t.Logf("rollback destination barriers=%d; each observed removed root with durable guard", attempts)
		})
	}
}

func TestRetirementDirectorySyncRevalidatesPublicBindings(t *testing.T) {
	for _, mutation := range []string{"stage-reappears", "root-reappears", "plan-reappears", "root-replaced", "destination-replaced"} {
		t.Run(mutation, func(t *testing.T) {
			if runtime.GOOS == "windows" && (mutation == "root-replaced" || mutation == "destination-replaced") {
				t.Skip("retained-directory rename requires native platform acceptance")
			}
			dir, state, r := accountingFixture(t)
			file := FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")}
			barrier, replacement := r.OwnedRoot, r.OwnedRoot
			if mutation == "stage-reappears" {
				replacement = filepath.Join(r.OwnedRoot, r.Stage)
			} else if mutation != "root-replaced" {
				barrier = r.Destination
				if err := os.RemoveAll(r.OwnedRoot); err != nil {
					t.Fatal(err)
				}
			}
			if mutation == "plan-reappears" || mutation == "destination-replaced" {
				state.Version, state.Roots = 2, nil
				state.Preparation = &ReceivePreparation{Destination: r.Destination, DestinationIdentity: r.DestinationIdentity, Root: filepath.Base(r.OwnedRoot), Stage: r.Stage, OwnerToken: r.OwnerToken}
				if err := file.SaveReceiveAccounting(state); err != nil {
					t.Fatal(err)
				}
			}
			store := &guardFaultStore{FileReceiveAccountingStore: file}
			attempts := 0
			var unknown string
			interceptRetirementSync(t, func(root *os.Root) error {
				if root.Name() != barrier || attempts != 0 {
					return nil
				}
				attempts++
				assertGuard(t, file)
				if mutation == "destination-replaced" {
					replacement = r.Destination
				}
				if mutation == "root-replaced" || mutation == "destination-replaced" {
					if err := os.Rename(replacement, replacement+"-held"); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Mkdir(replacement, 0700); err != nil {
					t.Fatal(err)
				}
				unknown = filepath.Join(replacement, "foreign")
				if err := os.WriteFile(unknown, []byte("unknown output"), 0600); err != nil {
					t.Fatal(err)
				}
				// The real barrier still runs on the retained original handle.
				return nil
			})
			for reopen := 0; reopen < 2; reopen++ {
				m, err := NewManager(Options{AccountingStore: store, ExistingState: true})
				if err != nil {
					t.Fatal(err)
				}
				if m.ReceiveRecovery().State != "blocked" || attempts != 1 || store.saves != 0 {
					t.Fatal("successful Sync hid changed public binding/name", m.ReceiveRecovery(), attempts, store.saves)
				}
				assertGuard(t, file)
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}
				current, err := file.LoadReceiveAccounting()
				if err != nil || accountingHash(current) != accountingHash(state) {
					t.Fatal("binding failure published/discarded index evidence", err)
				}
				if data, err := os.ReadFile(unknown); err != nil || string(data) != "unknown output" {
					t.Fatal("unknown replacement was inferred owned/deleted", err)
				}
			}
		})
	}
}
