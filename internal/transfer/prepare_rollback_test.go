package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/diskspace"
)

type prepareFailureStore struct {
	FileReceiveAccountingStore
	onSave func(ReceiveAccounting, ...ReceiveRetirementLease) error
}

func (s *prepareFailureStore) SaveReceiveAccounting(state ReceiveAccounting, leases ...ReceiveRetirementLease) error {
	if s.onSave != nil {
		return s.onSave(state, leases...)
	}
	return s.FileReceiveAccountingStore.SaveReceiveAccounting(state, leases...)
}

func TestPrepareRollbackRepeatedFailuresPreserveSavedMarker(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			calls, fail := 0, false
			space := diskspace.New(func(*os.File) (uint64, error) {
				calls++
				if fail && calls == 4 {
					if unknown {
						return 0, io.ErrUnexpectedEOF
					}
					return 0, nil
				}
				return 1 << 40, nil
			})
			m, peer := testManager(t, Options{DiskSpace: space, AccountingStore: FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}})
			destination := t.TempDir()
			if _, err := m.Offer(peer, testManifest("saved", testEntry("marker", receiveOwnerMarker, "saved payload"))); err != nil {
				t.Fatal(err)
			}
			saved, err := m.Accept("saved", destination)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.ReceiveFile(context.Background(), peer, "saved", "marker", strings.NewReader("saved payload")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(destination, "original"), []byte("original bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Offer(peer, testManifest("retry", testEntry("file", "a/b/c/file", "payload"))); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 100; i++ {
				calls, fail = 0, true
				want := diskspace.ErrLow
				if unknown {
					want = diskspace.ErrUnknown
				}
				if b, err := m.Accept("retry", destination); !errors.Is(err, want) || b.State != Pending {
					t.Fatalf("attempt %d: %+v %v", i, b, err)
				}
				if len(mustReadDir(t, destination)) != 2 || m.reserved != 7 || !m.batches["retry"].reserved {
					t.Fatal("provisional root accumulated or reservation released")
				}
				for name, want := range map[string]string{filepath.Join(destination, "original"): "original bytes", filepath.Join(saved.Destination, receiveOwnerMarker): "saved payload"} {
					if data, err := os.ReadFile(name); err != nil || string(data) != want {
						t.Fatalf("preserved bytes: %q %v", data, err)
					}
				}
			}
			fail = false
			first, err := m.Accept("retry", destination)
			if err != nil {
				t.Fatal(err)
			}
			if second, err := m.Accept("retry", destination); err != nil || second.Destination != first.Destination {
				t.Fatalf("duplicate allocation: %+v %v", second, err)
			}
			ack, err := m.ReceiveFile(context.Background(), peer, "retry", "file", strings.NewReader("payload"))
			if err != nil {
				t.Fatal(err)
			}
			if again, err := m.ReceiveFile(context.Background(), peer, "retry", "file", unreadableReader{t}); err != nil || again != ack {
				t.Fatalf("completion repeated: %+v %v", again, err)
			}
			if b, _ := m.Get("retry"); b.State != Completed || len(mustReadDir(t, destination)) != 3 {
				t.Fatal("retry did not complete once")
			}
		})
	}
}

func TestPrepareRollbackPersistenceAndUncertainSave(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(fmt.Sprint(saved), func(t *testing.T) {
			store := &prepareFailureStore{FileReceiveAccountingStore: FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}}
			m, peer := testManager(t, Options{AccountingStore: store})
			if _, err := m.Offer(peer, testManifest("failed", testEntry("file", "a/b/file", "payload"))); err != nil {
				t.Fatal(err)
			}
			calls := 0
			store.onSave = func(state ReceiveAccounting, leases ...ReceiveRetirementLease) error {
				calls++
				if saved {
					if err := store.FileReceiveAccountingStore.SaveReceiveAccounting(state, leases...); err != nil {
						t.Fatal(err)
					}
				}
				return errors.New("save failed or response lost")
			}
			destination := t.TempDir()
			for i := 0; i < 100; i++ {
				if b, err := m.Accept("failed", destination); !errors.Is(err, ErrReceiveRecovery) || b.State != Pending {
					t.Fatalf("accept: %+v %v", b, err)
				}
				if len(mustReadDir(t, destination)) != 0 || m.reserved != 7 || m.ReceiveRecovery().Code != "index_unavailable" {
					t.Fatal("uncertain save lost gate/accounting or leaked root")
				}
			}
			if calls != 1 {
				t.Fatal("uncertain save was retried")
			}
			state, err := store.LoadReceiveAccounting()
			if err != nil || len(state.Roots) != 0 || (state.Preparation != nil) != saved {
				t.Fatalf("durable fixture: %+v %v", state, err)
			}
			store.onSave = nil
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := NewManager(Options{AccountingStore: store, ExistingState: true})
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if restarted.ReceiveRecovery().State != "ready" || restarted.reserved != 0 || len(restarted.accounting.Roots) != 0 {
				t.Fatal("indexed but rolled-back missing root did not retire safely")
			}
		})
	}
}

func TestPrepareRollbackRecordAndValidationFailures(t *testing.T) {
	for _, failure := range []string{"record", "validation"} {
		t.Run(failure, func(t *testing.T) {
			if failure == "record" && runtime.GOOS == "windows" {
				t.Skip("open-directory rename exercises Unix handles")
			}
			destination := filepath.Join(t.TempDir(), "receive")
			if err := os.Mkdir(destination, 0700); err != nil {
				t.Fatal(err)
			}
			calls := 0
			space := diskspace.New(func(*os.File) (uint64, error) {
				calls++
				if failure == "record" && calls == 3 {
					if err := os.Rename(destination, destination+"-original"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(destination, 0700); err != nil {
						t.Fatal(err)
					}
				}
				return 1 << 40, nil
			})
			store := &prepareFailureStore{FileReceiveAccountingStore: FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}}
			m, peer := testManager(t, Options{AccountingStore: store, DiskSpace: space})
			if failure == "validation" {
				m.accountingLimits.MaxPathBytes = 1
			}
			store.onSave = func(state ReceiveAccounting, leases ...ReceiveRetirementLease) error {
				if failure == "validation" {
					t.Fatal("budget failure must precede persistence")
				}
				if len(state.Roots) != 0 {
					t.Fatal("record failure must precede promotion")
				}
				return store.FileReceiveAccountingStore.SaveReceiveAccounting(state, leases...)
			}
			if _, err := m.Offer(peer, testManifest("failed", testEntry("file", "a/file", "payload"))); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Accept("failed", destination); !errors.Is(err, ErrReceiveRecovery) {
				t.Fatal(err)
			}
			if len(mustReadDir(t, destination)) != 0 || m.reserved != 7 || m.pendingPreparation != nil {
				t.Fatal("record/validation rollback failed")
			}
			if failure == "record" && len(mustReadDir(t, destination+"-original")) != 0 {
				t.Fatal("retained parent handle leaked provisional root")
			}
		})
	}
}

func TestPrepareRollbackCleanupDeniedKeepsOneRoot(t *testing.T) {
	store := &prepareFailureStore{FileReceiveAccountingStore: FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}}
	m, peer := testManager(t, Options{AccountingStore: store})
	destination := t.TempDir()
	if _, err := m.Offer(peer, testManifest("failed", Entry{ID: "dir", Path: "empty", Kind: Directory})); err != nil {
		t.Fatal(err)
	}
	var obstruction string
	store.onSave = func(state ReceiveAccounting, leases ...ReceiveRetirementLease) error {
		if len(state.Roots) == 0 {
			return store.FileReceiveAccountingStore.SaveReceiveAccounting(state, leases...)
		}
		obstruction = filepath.Join(state.Roots[0].OwnedRoot, "empty", "foreign")
		if err := os.WriteFile(obstruction, []byte("user data"), 0600); err != nil {
			t.Fatal(err)
		}
		return errors.New("save failed")
	}
	for i := 0; i < 100; i++ {
		if _, err := m.Accept("failed", destination); !errors.Is(err, ErrReceiveRecovery) {
			t.Fatal(err)
		}
		if len(mustReadDir(t, destination)) != 1 || m.pendingPreparation == nil || !m.batches["failed"].reserved || m.metadata == 0 {
			t.Fatal("pending cleanup grew or lost zero-byte entry charge")
		}
	}
	if _, err := m.Cancel("failed"); err != nil {
		t.Fatal(err)
	}
	if err := m.Forget("failed"); !errors.Is(err, ErrState) {
		t.Fatal("forgot unindexed zero-byte cleanup")
	}
	if _, err := m.Offer(peer, testManifest("new", testEntry("file", "file", "x"))); !errors.Is(err, ErrReceiveRecovery) {
		t.Fatal("new receiving escaped cleanup block")
	}
	if data, err := os.ReadFile(obstruction); err != nil || string(data) != "user data" {
		t.Fatal("removed unknown user data")
	}
	if err := os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if len(mustReadDir(t, destination)) != 0 || m.pendingPreparation != nil || m.batches["failed"].reserved || m.ReceiveRecovery().Code != "index_unavailable" {
		t.Fatal("cleanup retry did not retire handle and preserve uncertain-index gate")
	}
}

func TestPrepareRollbackPreservesSubstitutions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory rename exercises Unix handles")
	}
	for _, target := range []string{"root", "stage", "directory", "marker"} {
		for _, symlink := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/link=%v", target, symlink), func(t *testing.T) {
				p := &preparedDestination{}
				defer p.close(false)
				space := diskspace.New(func(*os.File) (uint64, error) { return 1 << 40, nil })
				if err := p.prepare(context.Background(), t.TempDir(), []Entry{testEntry("file", "a/b/file", "")}, space, 1, nil); err != nil {
					t.Fatal(err)
				}
				name := p.actual
				switch target {
				case "stage":
					name = filepath.Join(p.actual, p.stage.name)
				case "directory":
					name = filepath.Join(p.actual, "a")
				case "marker":
					name = filepath.Join(p.actual, p.stage.name, receiveOwnerMarker)
				}
				if err := os.Rename(name, name+"-original"); err != nil {
					t.Fatal(err)
				}
				outside := t.TempDir()
				keep := filepath.Join(outside, "sentinel")
				if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				if symlink {
					linkTarget := outside
					if target == "marker" {
						linkTarget = keep
					}
					testSymlink(t, linkTarget, name)
				} else if target == "marker" {
					if err := os.WriteFile(name, []byte(p.token), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(name, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(name, "user"), []byte("replacement"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				for i := 0; i < 3; i++ {
					if err := p.rollback(); err == nil {
						t.Fatal("trusted substituted creation")
					}
					if _, err := os.Lstat(name); err != nil {
						t.Fatal("removed replacement")
					}
					if data, err := os.ReadFile(keep); err != nil || string(data) != "keep" {
						t.Fatal("followed symlink target")
					}
				}
			})
		}
	}
}

func TestPrepareRollbackPartialMarkerIdentity(t *testing.T) {
	p := &preparedDestination{}
	defer p.close(false)
	space := diskspace.New(func(*os.File) (uint64, error) { return 1 << 40, nil })
	destination := t.TempDir()
	if err := p.prepare(context.Background(), destination, nil, space, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filepath.Join(p.actual, p.stage.name, receiveOwnerMarker), 3); err != nil {
		t.Fatal(err)
	}
	if err := p.rollback(); err != nil {
		t.Fatal(err)
	}
	if err := p.rollback(); err != nil || len(mustReadDir(t, destination)) != 0 {
		t.Fatal("partial marker identity did not permit idempotent rollback")
	}
}

func TestPrepareRollbackCreationFailures(t *testing.T) {
	for _, failure := range []string{"protect", "open-root", "open-stage", "partial-write", "short-write", "sync", "close"} {
		t.Run(failure, func(t *testing.T) {
			ops := defaultPreparationIO()
			injected := errors.New("injected preparation I/O failure")
			switch failure {
			case "protect":
				ops.protect = func(string) error { return injected }
			case "open-root", "open-stage":
				calls := 0
				ops.openRoot = func(parent *os.Root, name string) (*os.Root, error) {
					calls++
					if calls == 1 && failure == "open-root" || calls == 2 && failure == "open-stage" {
						return nil, injected
					}
					return parent.OpenRoot(name)
				}
			case "partial-write", "short-write":
				ops.write = func(file *os.File, data []byte) (int, error) {
					n, err := file.Write(data[:3])
					if err != nil {
						t.Fatal(err)
					}
					if failure == "short-write" {
						return n, nil
					}
					return n, injected
				}
			case "sync":
				ops.sync = func(*os.File) error { return injected }
			case "close":
				ops.close = func(file *os.File) error {
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
					return injected
				}
			}
			p := &preparedDestination{io: ops}
			defer p.close(false)
			destination := t.TempDir()
			space := diskspace.New(func(*os.File) (uint64, error) { return 1 << 40, nil })
			err := p.prepare(context.Background(), destination, []Entry{testEntry("file", "a/b/file", "")}, space, 1, nil)
			want := injected
			if failure == "short-write" {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) {
				t.Fatalf("failure fixture did not run: %v", err)
			}
			if err := p.rollback(); err != nil || len(mustReadDir(t, destination)) != 0 {
				t.Fatalf("failed %s retained creations: %v", failure, err)
			}
		})
	}
}

func TestPrepareRollbackRetryBeforeAllocation(t *testing.T) {
	destination := t.TempDir()
	calls, fail := 0, true
	var obstruction string
	space := diskspace.New(func(*os.File) (uint64, error) {
		calls++
		if fail && calls == 4 {
			entries := mustReadDir(t, destination)
			if len(entries) != 1 {
				t.Fatal("unexpected provisional root count")
			}
			obstruction = filepath.Join(destination, entries[0].Name(), "a", "foreign")
			if err := os.WriteFile(obstruction, []byte("user data"), 0600); err != nil {
				t.Fatal(err)
			}
			return 0, nil
		}
		return 1 << 40, nil
	})
	m, peer := testManager(t, Options{DiskSpace: space})
	if _, err := m.Offer(peer, testManifest("retry", testEntry("file", "a/b/file", "payload"))); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := m.Accept("retry", destination); !errors.Is(err, ErrReceiveRecovery) {
			t.Fatal(err)
		}
		if calls != 4 || len(mustReadDir(t, destination)) != 1 || m.reserved != 7 || m.ReceiveRecovery().Code != "prepare_cleanup_unavailable" {
			t.Fatal("allocated before pending cleanup succeeded")
		}
	}
	if err := os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	fail = false
	if _, err := m.Accept("retry", destination); err != nil {
		t.Fatal(err)
	}
	if m.pendingPreparation != nil || len(mustReadDir(t, destination)) != 1 || m.ReceiveRecovery().State != "ready" {
		t.Fatal("retry did not remove the old root before allocation")
	}
	if _, err := m.ReceiveFile(context.Background(), peer, "retry", "file", strings.NewReader("payload")); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRollbackPermissionDeniedRetainsReservation(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("Unix permission denial needs an unprivileged native process")
	}
	destination := t.TempDir()
	calls, deny := 0, true
	space := diskspace.New(func(*os.File) (uint64, error) {
		calls++
		if deny && calls == 4 {
			if err := os.Chmod(destination, 0500); err != nil {
				t.Fatal(err)
			}
			return 0, nil
		}
		return 1 << 40, nil
	})
	m, peer := testManager(t, Options{DiskSpace: space})
	// Restore before the manager's cleanup even when an assertion fails.
	t.Cleanup(func() { _ = os.Chmod(destination, 0700) })
	if _, err := m.Offer(peer, testManifest("denied", testEntry("file", "a/b/file", "payload"))); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := m.Accept("denied", destination); !errors.Is(err, ErrReceiveRecovery) {
			t.Fatal(err)
		}
		if calls != 4 || len(mustReadDir(t, destination)) != 1 || m.pendingPreparation == nil || m.reserved != 7 {
			t.Fatal("permission-denied cleanup allocated or released capacity")
		}
	}
	if _, err := m.Cancel("denied"); err != nil || !m.batches["denied"].reserved || m.reserved != 7 {
		t.Fatal("cancel released pending cleanup bytes")
	}
	if err := m.Forget("denied"); !errors.Is(err, ErrState) {
		t.Fatal("forgot pending cleanup")
	}
	if err := os.Chmod(destination, 0700); err != nil {
		t.Fatal(err)
	}
	deny = false
	// The prior reservation is released only after retrying cleanup.
	if _, err := m.Offer(peer, testManifest("next", testEntry("file", "file", "x"))); !errors.Is(err, ErrReceiveRecovery) {
		t.Fatal("offer escaped pending cleanup")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if len(mustReadDir(t, destination)) != 0 || m.pendingPreparation != nil || m.reserved != 0 || m.batches["denied"].reserved {
		t.Fatal("retired preparation retained capacity")
	}
}

func (s *prepareFailureStore) WithReceiveAccountingLimits(l AccountingLimits) ReceiveAccountingStore {
	s.Limits = l
	return s
}
