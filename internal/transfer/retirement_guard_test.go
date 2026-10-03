package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type guardFaultStore struct {
	FileReceiveAccountingStore
	acquire func(ReceiveRetirementGuard, AccountingLimits) (ReceiveRetirementLease, error)
	release func(ReceiveRetirementLease, func() error) error
	saves   int
}

func (s *guardFaultStore) WithReceiveAccountingLimits(l AccountingLimits) ReceiveAccountingStore {
	s.Limits = l
	return s
}
func (s *guardFaultStore) SaveReceiveAccounting(a ReceiveAccounting) error {
	s.saves++
	return s.FileReceiveAccountingStore.SaveReceiveAccounting(a)
}
func (s *guardFaultStore) AcquireReceiveRetirementGuard(g ReceiveRetirementGuard, l AccountingLimits) (ReceiveRetirementLease, error) {
	var lease ReceiveRetirementLease
	var err error
	if s.acquire != nil {
		lease, err = s.acquire(g, l)
	} else {
		lease, err = s.FileReceiveAccountingStore.AcquireReceiveRetirementGuard(g, l)
	}
	if err != nil {
		return nil, err
	}
	return &guardFaultLease{ReceiveRetirementLease: lease, store: s}, nil
}

type guardFaultLease struct {
	ReceiveRetirementLease
	store *guardFaultStore
}

func (l *guardFaultLease) Release(v func() error) error {
	if l.store.release != nil {
		return l.store.release(l.ReceiveRetirementLease, v)
	}
	return l.ReceiveRetirementLease.Release(v)
}

func assertGuard(t *testing.T, file FileReceiveAccountingStore) ReceiveRetirementGuard {
	t.Helper()
	g, l, err := file.LoadReceiveRetirementGuard(accountingLimits(AccountingLimits{}))
	if err != nil {
		t.Fatal("durable guard missing", err)
	}
	l.Close()
	return *g
}

func TestRetirementGuardAcquireAndReleaseFailures(t *testing.T) {
	for _, mode := range []string{"acquire-before", "acquire-after", "release-before", "release-after"} {
		t.Run(mode, func(t *testing.T) {
			file := FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}
			store := &guardFaultStore{FileReceiveAccountingStore: file}
			m, peer := testManager(t, Options{AccountingStore: store})
			if _, err := m.Offer(peer, testManifest("fault", testEntry("f", "saved", "x"))); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "acquire") {
				store.acquire = func(g ReceiveRetirementGuard, l AccountingLimits) (ReceiveRetirementLease, error) {
					if mode == "acquire-after" {
						lease, err := file.AcquireReceiveRetirementGuard(g, l)
						if err != nil {
							t.Fatal(err)
						}
						lease.Close()
					}
					return nil, errors.New("acquisition uncertainty")
				}
			} else {
				store.release = func(lease ReceiveRetirementLease, v func() error) error {
					if mode == "release-after" {
						if err := lease.Release(v); err != nil {
							t.Fatal(err)
						}
					}
					return errors.New("release uncertainty")
				}
			}
			b, err := m.Accept("fault", t.TempDir())
			if !errors.Is(err, ErrReceiveRecovery) || b.State != Accepted {
				t.Fatalf("partial truth %+v %v", b, err)
			}
			state, err := file.LoadReceiveAccounting()
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "acquire") && state.Preparation == nil {
				t.Fatal("failed acquire removed index evidence")
			}
			if m.ReceiveRecovery().State != "blocked" {
				t.Fatal("uncertainty did not block")
			}
			before, _ := os.ReadFile(file.Path)
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(file.Path)
			if string(before) != string(after) {
				t.Fatal("Close overwrote uncertain index")
			}
			reopened, err := NewManager(Options{AccountingStore: file, ExistingState: true})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			want := "blocked"
			if mode == "release-after" {
				want = "ready"
			}
			if reopened.ReceiveRecovery().State != want {
				t.Fatalf("reopen=%+v want %s", reopened.ReceiveRecovery(), want)
			}
		})
	}
}

func TestRetirementLastReleaseValidationRejectsReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory rename")
	}
	file := FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}
	store := &guardFaultStore{FileReceiveAccountingStore: file}
	m, peer := testManager(t, Options{AccountingStore: store})
	if _, err := m.Offer(peer, testManifest("release", testEntry("f", "saved", "x"))); err != nil {
		t.Fatal(err)
	}
	store.release = func(lease ReceiveRetirementLease, v func() error) error {
		state, err := file.LoadReceiveAccounting()
		if err != nil {
			t.Fatal(err)
		}
		r := state.Roots[0]
		if err := os.Rename(r.OwnedRoot, r.OwnedRoot+"-original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(r.OwnedRoot, 0700); err != nil {
			t.Fatal(err)
		}
		return lease.Release(v)
	}
	if b, err := m.Accept("release", t.TempDir()); !errors.Is(err, ErrReceiveRecovery) || b.State != Accepted {
		t.Fatalf("release %+v %v", b, err)
	}
	assertGuard(t, file)
	m.Close()
	reopened, err := NewManager(Options{AccountingStore: file, ExistingState: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.ReceiveRecovery().State != "blocked" {
		t.Fatal("release replacement forgotten")
	}
}

func TestRetirementGuardFreezesCompetingCloseAndCleanup(t *testing.T) {
	file := FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}
	store := &prepareFailureStore{FileReceiveAccountingStore: file}
	m, peer := testManager(t, Options{AccountingStore: store})
	defer m.Close()
	for _, id := range []string{"older", "victim"} {
		if _, err := m.Offer(peer, testManifest(id, testEntry("f", "saved", "x"))); err != nil {
			t.Fatal(err)
		}
		if id == "older" {
			if _, err := m.Accept(id, t.TempDir()); err != nil {
				t.Fatal(err)
			}
		}
	}
	store.onSave = func(a ReceiveAccounting) error {
		if err := file.SaveReceiveAccounting(a); err != nil {
			return err
		}
		if a.Preparation == nil && len(a.Roots) == 2 {
			return errors.New("committed then failed")
		}
		return nil
	}
	if _, err := m.Accept("victim", t.TempDir()); !errors.Is(err, ErrReceiveRecovery) {
		t.Fatal(err)
	}
	old := m.batches["older"]
	name := old.stage + "/retained-cleanup"
	if err := old.root.WriteFile(name, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	old.cleanup = map[string]string{name: "f"}
	before, _ := os.ReadFile(file.Path)
	guardBefore, _ := os.ReadFile(file.Path + ".retirement")
	if err := m.cleanupFileLocked(old, "f"); !errors.Is(err, ErrReceiveRecovery) {
		t.Fatal("cleanup not frozen", err)
	}
	if _, err := m.Cancel("older"); err != nil {
		t.Fatal(err)
	}
	if err := m.Forget("older"); !errors.Is(err, ErrState) {
		t.Fatal("cleanup debt forgotten", err)
	}
	store.onSave = func(ReceiveAccounting) error { t.Fatal("stale writer saved"); return nil }
	m.Close()
	after, _ := os.ReadFile(file.Path)
	guardAfter, _ := os.ReadFile(file.Path + ".retirement")
	if string(before) != string(after) || string(guardBefore) != string(guardAfter) {
		t.Fatal("frozen evidence changed")
	}
	if data, err := os.ReadFile(filepath.Join(old.value.Destination, name)); err != nil || string(data) != "x" {
		t.Fatal("cleanup debt removed", err)
	}
	if !old.reserved || !m.batches["victim"].reserved {
		t.Fatal("uncertain ownership released")
	}
}

func TestRetirementGuardRootRetirementFaultMatrix(t *testing.T) {
	for _, path := range []string{"runtime", "startup"} {
		for _, mode := range []string{"before", "after", "replace"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				if mode == "replace" && runtime.GOOS == "windows" {
					t.Skip("open-directory rename")
				}
				dir, state, r := accountingFixture(t)
				file := FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")}
				mustWrite(t, filepath.Join(r.OwnedRoot, receiveOwnerMarker), "saved owner payload")
				store := &prepareFailureStore{FileReceiveAccountingStore: file}
				called := false
				store.onSave = func(a ReceiveAccounting) error {
					called = true
					if mode == "before" {
						return errors.New("before commit")
					}
					if err := file.SaveReceiveAccounting(a); err != nil {
						return err
					}
					if mode == "after" {
						return errors.New("after commit")
					}
					if err := os.Rename(r.OwnedRoot, r.OwnedRoot+"-original"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(r.OwnedRoot, 0700); err != nil {
						t.Fatal(err)
					}
					mustWrite(t, filepath.Join(r.OwnedRoot, "foreign"), "preserved")
					return nil
				}
				var m *Manager
				if path == "startup" {
					var err error
					m, err = NewManager(Options{AccountingStore: store, ExistingState: true})
					if err != nil {
						t.Fatal(err)
					}
				} else {
					// Reconstruct a live terminal root from the real persisted fixture.
					root, err := os.OpenRoot(r.OwnedRoot)
					if err != nil {
						t.Fatal(err)
					}
					info, err := root.Lstat(r.Stage)
					if err != nil {
						t.Fatal(err)
					}
					m = &Manager{accountingStore: store, accounting: state, accountingLimits: accountingLimits(AccountingLimits{}), batches: map[string]*batchState{}, peers: map[string]*peerState{}}
					b := &batchState{root: root, stage: r.Stage, stageInfo: info, ownerToken: r.OwnerToken, value: Batch{ID: "terminal", Destination: r.OwnedRoot, State: Completed}}
					m.batches[b.value.ID] = b
					m.closeRootLocked(b)
				}
				if !called || m.ReceiveRecovery().State != "blocked" {
					t.Fatal("retirement fixture did not fail closed")
				}
				assertGuard(t, file)
				m.Close()
				reopened, err := NewManager(Options{AccountingStore: file, ExistingState: true})
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				want := "ready"
				savedRoot := r.OwnedRoot
				if mode == "replace" {
					want = "blocked"
					savedRoot += "-original"
				}
				if reopened.ReceiveRecovery().State != want {
					t.Fatalf("reopen %+v want %s", reopened.ReceiveRecovery(), want)
				}
				if data, err := os.ReadFile(filepath.Join(savedRoot, receiveOwnerMarker)); err != nil || string(data) != "saved owner payload" {
					t.Fatal("saved owner payload lost", err)
				}
				if mode != "replace" {
					if _, err := os.Stat(filepath.Join(r.OwnedRoot, r.Stage)); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("marker-only private stage abandoned", err)
					}
				}
			})
		}
	}
}

func TestRetirementGuardCrashChild(t *testing.T) {
	dir := os.Getenv("SOBALINK_RETIREMENT_DIR")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	phase := os.Getenv("SOBALINK_RETIREMENT_PHASE")
	file := FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")}
	before, err := file.LoadReceiveAccounting()
	if err != nil {
		t.Fatal(err)
	}
	after := ReceiveAccounting{Version: before.Version, Roots: []ReceiveRoot{}}
	g, err := retirementGuard(before, after, false)
	if err != nil {
		t.Fatal(err)
	}
	if phase == "create" || phase == "partial" {
		root, err := openDestination(dir)
		if err != nil {
			t.Fatal(err)
		}
		f, err := retirementCreate(root, file.retirementName())
		if err != nil {
			t.Fatal(err)
		}
		if phase == "partial" {
			if _, err := f.Write([]byte(`{"version":1,`)); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
		if err := retirementSyncDirectory(root); err != nil {
			t.Fatal(err)
		}
		os.Exit(87)
	}
	lease, err := file.AcquireReceiveRetirementGuard(g, accountingLimits(AccountingLimits{}))
	if err != nil {
		t.Fatal(err)
	}
	if phase == "acquired" {
		os.Exit(87)
	}
	if phase == "marker-removed" {
		stage, err := os.OpenRoot(filepath.Join(before.Roots[0].OwnedRoot, before.Roots[0].Stage))
		if err != nil {
			t.Fatal(err)
		}
		if err := stage.Remove(receiveOwnerMarker); err != nil {
			t.Fatal(err)
		}
		if err := retirementSyncDirectory(stage); err != nil {
			t.Fatal(err)
		}
		os.Exit(87)
	}
	if err := verifyRetirement(g, true); err != nil {
		t.Fatal(err)
	}
	if phase == "stage-removed" {
		os.Exit(87)
	}
	if err := file.SaveReceiveAccounting(after); err != nil {
		t.Fatal(err)
	}
	if phase == "index-replaced" {
		os.Exit(87)
	}
	if err := verifyRetirement(g, false); err != nil {
		t.Fatal(err)
	}
	if phase == "postverified" {
		os.Exit(87)
	}
	if phase == "unlink-before-dirsync" {
		if err := os.Remove(file.Path + ".retirement"); err != nil {
			t.Fatal(err)
		}
		os.Exit(87)
	}
	if err := lease.Release(func() error { return verifyRetirement(g, false) }); err != nil {
		t.Fatal(err)
	}
	os.Exit(87)
}

func TestRetirementGuardProcessCrashCuts(t *testing.T) {
	for _, phase := range []string{"create", "partial", "acquired", "marker-removed", "stage-removed", "index-replaced", "postverified", "unlink-before-dirsync", "released"} {
		t.Run(phase, func(t *testing.T) {
			dir, _, r := accountingFixture(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestRetirementGuardCrashChild$")
			cmd.Env = append(os.Environ(), "SOBALINK_RETIREMENT_DIR="+dir, "SOBALINK_RETIREMENT_PHASE="+phase)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 87 {
				t.Fatalf("cut %s: %v %s", phase, err, out)
			}
			file := FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")}
			m, err := NewManager(Options{AccountingStore: file, ExistingState: true})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			want := "ready"
			if phase == "create" || phase == "partial" {
				want = "blocked"
			}
			if m.ReceiveRecovery().State != want {
				t.Fatalf("cut %s: %+v", phase, m.ReceiveRecovery())
			}
			if want == "ready" {
				if _, err := os.Stat(filepath.Join(r.OwnedRoot, r.Stage)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("forgotten private stage", err)
				}
			}
		})
	}
}

func TestRetirementRepeatedTransfersCancelAndRestartConstantFiles(t *testing.T) {
	file := FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}
	destination := t.TempDir()
	for i := 0; i < 40; i++ {
		m, peer := testManager(t, Options{AccountingStore: file, ExistingState: i != 0})
		id := fmt.Sprintf("repeat-%d", i)
		if _, err := m.Offer(peer, testManifest(id, testEntry("f", receiveOwnerMarker, "payload"))); err != nil {
			t.Fatal(err)
		}
		b, err := m.Accept(id, destination)
		if err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			if _, err := m.ReceiveFile(context.Background(), peer, id, "f", strings.NewReader("payload")); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := m.Cancel(id); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.Forget(id); err != nil {
			t.Fatal("unresolved normal ownership", err)
		}
		m.Close()
		if len(mustReadDir(t, filepath.Dir(file.Path))) != 1 {
			t.Fatal("control file history accumulated")
		}
		entries, err := os.ReadDir(b.Destination)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".incoming-") {
				t.Fatal("private stage left behind")
			}
		}
		if i%2 == 0 {
			if data, err := os.ReadFile(filepath.Join(b.Destination, receiveOwnerMarker)); err != nil || string(data) != "payload" {
				t.Fatal("saved marker payload changed", err)
			}
		}
	}
}

func TestRetirementGuardStrictFormatBudgetsAndLimitUpdates(t *testing.T) {
	file, before := intentFixture(t)
	after := before
	after.Preparation = nil
	g, err := retirementGuard(before, after, false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	budget := accountingLimits(AccountingLimits{})
	if _, err := parseRetirementGuard(data, budget); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string][]byte{
		"duplicate": []byte(strings.Replace(string(data), `"version":1,`, `"version":1,"version":1,`, 1)),
		"unknown":   []byte(strings.Replace(string(data), `"version":1,`, `"extra":1,"version":1,`, 1)),
		"case":      []byte(strings.Replace(string(data), `"version":1,`, `"Version":1,`, 1)),
		"version":   []byte(strings.Replace(string(data), `"version":1,`, `"version":99,`, 1)),
		"hash":      []byte(strings.Replace(string(data), g.BeforeHash, strings.Repeat("0", 64), 1)),
		"omission":  []byte(strings.Replace(string(data), `"witnesses":[]`, `"other":[]`, 1)),
		"trailing":  append(append([]byte(nil), data...), []byte(`{}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRetirementGuard(bad, budget); err == nil {
				t.Fatal("damaged guard accepted")
			}
		})
	}
	tiny := budget
	tiny.MaxBytes = int64(len(data))
	if err := validateRetirementGuard(g, tiny); !errors.Is(err, ErrLimit) {
		t.Fatal("independent per-file budget bypassed peak", err)
	}
	if err := file.SaveReceiveAccounting(before); err != nil {
		t.Fatal(err)
	}
	lease, err := file.AcquireReceiveRetirementGuard(g, budget)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	m, err := NewManager(Options{AccountingStore: &prepareFailureStore{FileReceiveAccountingStore: file, onSave: func(ReceiveAccounting) error { return errors.New("offline") }}, ExistingState: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.UpdateAccountingLimits(AccountingLimits{MaxBytes: 1}); err != nil {
		t.Fatal(err)
	}
	if v, err := m.ConfirmReceiveRecovery(context.Background(), true); err == nil || v.Applied {
		t.Fatal("lowered guard limit bypassed", err)
	}
	if err := m.UpdateAccountingLimits(budget); err != nil {
		t.Fatal(err)
	}
	m.accountingStore = file.WithReceiveAccountingLimits(budget)
	if v, err := m.ConfirmReceiveRecovery(context.Background(), true); err != nil || !v.Applied {
		t.Fatal("raised limits failed to reach adapter", err)
	}
}

func TestRetirementGuardMissingCorruptIndexAndUnexpectedSlotBlocksReview(t *testing.T) {
	for _, index := range []string{"missing", "corrupt"} {
		for _, slot := range []string{"valid", "partial", "directory", "symlink"} {
			t.Run(index+"/"+slot, func(t *testing.T) {
				file, before := intentFixture(t)
				after := before
				after.Preparation = nil
				guard := file.Path + ".retirement"
				switch slot {
				case "valid":
					g, err := retirementGuard(before, after, false)
					if err != nil {
						t.Fatal(err)
					}
					lease, err := file.AcquireReceiveRetirementGuard(g, accountingLimits(AccountingLimits{}))
					if err != nil {
						t.Fatal(err)
					}
					lease.Close()
				case "partial":
					mustWrite(t, guard, `{"version":1,`)
				case "directory":
					if err := os.Mkdir(guard, 0700); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					if err := os.Symlink(file.Path, guard); err != nil {
						t.Skip("symlink unavailable")
					}
				}
				if index == "corrupt" {
					mustWrite(t, file.Path, `{"version":1,"roots":`)
				}
				for _, existing := range []bool{false, true} {
					m, err := NewManager(Options{AccountingStore: file, ExistingState: existing})
					if err != nil {
						t.Fatal(err)
					}
					if m.ReceiveRecovery().State != "blocked" {
						t.Fatal("guard ignored without usable index")
					}
					if v, err := m.ConfirmReceiveRecovery(context.Background(), true); err == nil || v.Applied {
						t.Fatal("legacy review discarded guard")
					}
					m.Close()
				}
				if index == "missing" {
					if _, err := os.Lstat(file.Path); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("guard allowed legacy initialization", err)
					}
				}
			})
		}
	}
}

func TestRetirementPeakBudgetRejectsBeforeDestinationCreation(t *testing.T) {
	for _, limits := range []AccountingLimits{{MaxBytes: 1600}, {MaxEntries: 3}} {
		t.Run(fmt.Sprint(limits), func(t *testing.T) {
			file := FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}
			m, peer := testManager(t, Options{AccountingStore: file, AccountingLimits: limits})
			defer m.Close()
			if _, err := m.Offer(peer, testManifest("budget", testEntry("f", "file", "x"))); err != nil {
				t.Fatal(err)
			}
			destination := t.TempDir()
			if _, err := m.Accept("budget", destination); !errors.Is(err, ErrReceiveRecovery) {
				t.Fatal("peak budget accepted", err)
			}
			if len(mustReadDir(t, destination)) != 0 {
				t.Fatal("objects allocated before reserving guard peak")
			}
		})
	}
}

func TestRetirementGuardAcquireSubstitutionAndBatchInventory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory rename")
	}
	for _, target := range []string{"destination", "root", "stage", "marker"} {
		t.Run(target, func(t *testing.T) {
			file := FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}
			store := &guardFaultStore{FileReceiveAccountingStore: file}
			m, peer := testManager(t, Options{AccountingStore: store})
			if _, err := m.Offer(peer, testManifest("acquire", testEntry("f", "file", "x"))); err != nil {
				t.Fatal(err)
			}
			store.acquire = func(g ReceiveRetirementGuard, l AccountingLimits) (ReceiveRetirementLease, error) {
				lease, err := file.AcquireReceiveRetirementGuard(g, l)
				if err != nil {
					return nil, err
				}
				r := g.Witnesses[0].Root
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
				return lease, nil
			}
			if b, err := m.Accept("acquire", t.TempDir()); !errors.Is(err, ErrReceiveRecovery) || b.State != Accepted {
				t.Fatalf("acquisition mutation %+v %v", b, err)
			}
			if store.saves != 3 {
				t.Fatalf("index was cleared after acquisition mutation: %d saves", store.saves)
			}
			assertGuard(t, file)
			m.Close()
			reopened, err := NewManager(Options{AccountingStore: file, ExistingState: true})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if reopened.ReceiveRecovery().State != "blocked" {
				t.Fatal("acquisition substitution disappeared")
			}
		})
	}
	t.Run("batch-startup", func(t *testing.T) {
		file := FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}
		m, peer := testManager(t, Options{AccountingStore: file})
		for i := 0; i < 8; i++ {
			id := fmt.Sprint(i)
			if _, err := m.Offer(peer, testManifest(id, testEntry("f", "file", "x"))); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Accept(id, t.TempDir()); err != nil {
				t.Fatal(err)
			}
		}
		for _, b := range m.batches {
			b.root.Close()
			b.root = nil
		}
		store := &guardFaultStore{FileReceiveAccountingStore: file}
		reopened, err := NewManager(Options{AccountingStore: store, ExistingState: true})
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		if reopened.ReceiveRecovery().State != "ready" || store.saves != 1 {
			t.Fatalf("startup did not batch removal: %d saves %+v", store.saves, reopened.ReceiveRecovery())
		}
		if len(mustReadDir(t, filepath.Dir(file.Path))) != 1 {
			t.Fatal("guard history accumulated")
		}
	})
}

func TestRetirementGuardIndexHashMismatchBlocks(t *testing.T) {
	file, before := intentFixture(t)
	after := before
	after.Preparation = nil
	g, err := retirementGuard(before, after, false)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := file.AcquireReceiveRetirementGuard(g, accountingLimits(AccountingLimits{}))
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	// Canonically valid but unrelated accounting must not be overwritten by the
	// singleton's target. The known v2 operation cannot apply to a v1 index.
	if err := file.SaveReceiveAccounting(ReceiveAccounting{Version: 1}); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(Options{AccountingStore: file, ExistingState: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.ReceiveRecovery().State != "blocked" {
		t.Fatal("hash mismatch accepted")
	}
	if v, err := m.ConfirmReceiveRecovery(context.Background(), true); err == nil || v.Applied {
		t.Fatal("review overwrote unrelated index")
	}
	assertGuard(t, file)
	disk, err := file.LoadReceiveAccounting()
	if err != nil || disk.Version != 1 {
		t.Fatal("mismatched index changed", err)
	}
}
