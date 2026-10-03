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

	"github.com/webkaz-labs/sobalink/internal/diskspace"
)

func TestPreparationIntentCrashChild(t *testing.T) {
	dir := os.Getenv("SOBALINK_INTENT_CRASH_DIR")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	boundary := os.Getenv("SOBALINK_INTENT_CRASH_BOUNDARY")
	store := FileReceiveAccountingStore{Path: filepath.Join(dir, "index.json")}
	p := &preparedDestination{io: defaultPreparationIO()}
	if boundary == "after-mkdir" || boundary == "after-stage-mkdir" || boundary == "after-manifest-mkdir" {
		count := 0
		p.io.mkdir = func(parent *os.Root, name string, mode os.FileMode) error {
			if err := parent.Mkdir(name, mode); err != nil {
				return err
			}
			count++
			if count == 1 && boundary == "after-mkdir" || count == 2 && boundary == "after-stage-mkdir" || count == 3 && boundary == "after-manifest-mkdir" {
				os.Exit(83)
			}
			return nil
		}
	}
	if boundary == "after-marker-create" {
		p.io.write = func(*os.File, []byte) (int, error) { os.Exit(83); return 0, nil }
	}
	if boundary == "after-marker-sync" {
		p.io.sync = func(f *os.File) error {
			if err := f.Sync(); err != nil {
				return err
			}
			os.Exit(83)
			return nil
		}
	}
	if boundary == "before-marker" {
		p.io.openRoot = func(parent *os.Root, name string) (*os.Root, error) {
			if strings.HasPrefix(name, ".incoming-") {
				os.Exit(83)
			}
			return parent.OpenRoot(name)
		}
	}
	var state ReceiveAccounting
	err := p.prepare(context.Background(), filepath.Join(dir, "receive"), []Entry{{ID: "dir", Path: "empty", Kind: Directory}}, diskspace.Process, 1, func(plan ReceivePreparation) error {
		state = ReceiveAccounting{Version: 2, Preparation: &plan}
		if err := store.SaveReceiveAccounting(state); err != nil {
			return err
		}
		if boundary == "before-mkdir" {
			os.Exit(83)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if boundary == "before-promotion" {
		os.Exit(83)
	}
	record, err := receiveRootRecord(state.Preparation.Destination, p.actual, p.stage.name, p.token, p.parentIdentity, p.root)
	if err != nil {
		t.Fatal(err)
	}
	state.Roots = []ReceiveRoot{record}
	if err := store.SaveReceiveAccounting(state); err != nil {
		t.Fatal(err)
	}
	if boundary == "after-promotion" {
		os.Exit(83)
	}
	t.Fatal("unknown boundary")
}

func TestPreparationIntentAbruptBoundaries(t *testing.T) {
	for _, boundary := range []string{"before-mkdir", "after-mkdir", "after-stage-mkdir", "before-marker", "after-marker-create", "after-marker-sync", "after-manifest-mkdir", "before-promotion", "after-promotion"} {
		t.Run(boundary, func(t *testing.T) {
			dir := t.TempDir()
			destination := filepath.Join(dir, "receive")
			if err := os.Mkdir(destination, 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestPreparationIntentCrashChild$")
			cmd.Env = append(os.Environ(), "SOBALINK_INTENT_CRASH_DIR="+dir, "SOBALINK_INTENT_CRASH_BOUNDARY="+boundary)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 83 {
				t.Fatalf("exit %v: %s", err, output)
			}
			store := FileReceiveAccountingStore{Path: filepath.Join(dir, "index.json")}
			state, err := store.LoadReceiveAccounting()
			if err != nil || state.Preparation == nil {
				t.Fatalf("durable intent: %+v %v", state, err)
			}
			m, err := NewManager(Options{AccountingStore: store, ExistingState: true})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if boundary == "before-mkdir" {
				if v := m.ReceiveRecovery(); v.State != "ready" || v.ReservedBytes == nil || *v.ReservedBytes != 0 {
					t.Fatalf("missing root retirement %+v", v)
				}
				state, err = store.LoadReceiveAccounting()
				if err != nil || state.Preparation != nil {
					t.Fatal("intent not durably retired", err)
				}
				return
			}
			if v := m.ReceiveRecovery(); v.State != "blocked" || v.ReservedBytes != nil {
				t.Fatalf("crash escaped gate %+v", v)
			}
			before, _ := os.ReadFile(store.Path)
			if v, err := m.ConfirmReceiveRecovery(context.Background(), true); err == nil || v.Applied {
				t.Fatal("confirmation guessed ownership")
			}
			peer := Peer{ID: "peer", Generation: 1}
			if err := m.BindPeer(peer); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 100; i++ {
				if _, err := m.Offer(peer, testManifest(fmt.Sprint(i), testEntry("file", "file", "x"))); !errors.Is(err, ErrReceiveRecovery) {
					t.Fatal(err)
				}
			}
			if len(mustReadDir(t, destination)) != 1 {
				t.Fatal("additional root allocated")
			}
			after, _ := os.ReadFile(store.Path)
			if string(before) != string(after) {
				t.Fatal("existing intent changed")
			}
		})
	}
}

func TestPreparationIntentUncertainPromotionCleanupAndReopen(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			store := &prepareFailureStore{FileReceiveAccountingStore: FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}}
			m, peer := testManager(t, Options{AccountingStore: store})
			destination := t.TempDir()
			if _, err := m.Offer(peer, testManifest("failed", Entry{ID: "dir", Path: "empty", Kind: Directory})); err != nil {
				t.Fatal(err)
			}
			var foreign string
			store.onSave = func(state ReceiveAccounting, leases ...ReceiveRetirementLease) error {
				if len(state.Roots) == 0 {
					return store.FileReceiveAccountingStore.SaveReceiveAccounting(state, leases...)
				}
				foreign = filepath.Join(state.Roots[0].OwnedRoot, "empty", "foreign")
				mustWrite(t, foreign, "user data")
				if committed {
					if err := store.FileReceiveAccountingStore.SaveReceiveAccounting(state, leases...); err != nil {
						t.Fatal(err)
					}
				}
				return errors.New("uncertain promotion")
			}
			if _, err := m.Accept("failed", destination); !errors.Is(err, ErrReceiveRecovery) {
				t.Fatal(err)
			}
			for i := 0; i < 100; i++ {
				if _, err := m.Accept("failed", destination); !errors.Is(err, ErrReceiveRecovery) {
					t.Fatal(err)
				}
				if len(mustReadDir(t, destination)) != 1 || !m.batches["failed"].reserved || m.metadata == 0 || m.pendingPreparation == nil {
					t.Fatal("uncertain cleanup released accounting or allocated another root")
				}
			}
			if err := m.Close(); !errors.Is(err, ErrReceiveRecovery) {
				t.Fatal("cleanup unexpectedly succeeded", err)
			}
			state, err := store.LoadReceiveAccounting()
			if err != nil || state.Preparation == nil {
				t.Fatal("Close erased intent", err)
			}
			if len(state.Roots) != map[bool]int{false: 0, true: 1}[committed] {
				t.Fatalf("promotion fixture %+v", state)
			}
			store.onSave = nil
			reopened, err := NewManager(Options{AccountingStore: store, ExistingState: true})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if v := reopened.ReceiveRecovery(); v.State != "blocked" || v.ReservedBytes != nil {
				t.Fatal("reopen lost unknown accounting")
			}
			if data, err := os.ReadFile(foreign); err != nil || string(data) != "user data" {
				t.Fatal("unknown data lost")
			}
			if v, err := reopened.ConfirmReceiveRecovery(context.Background(), true); err == nil || v.Applied {
				t.Fatal("unowned cleanup permitted")
			}
			// Empty/markerless is still ambiguous, including after a user resolves payload.
			if err := os.Remove(foreign); err != nil {
				t.Fatal(err)
			}
			if _, err := reopened.ConfirmReceiveRecovery(context.Background(), true); err == nil {
				t.Fatal("empty root guessed owned")
			}
			if len(mustReadDir(t, destination)) != 1 {
				t.Fatal("root deleted")
			}
		})
	}
}

func TestPreparationIntentRetirementNeverRollsBackPromotion(t *testing.T) {
	for _, mode := range []string{"fail-before", "fail-after", "cancel-before", "cancel-after"} {
		t.Run(mode, func(t *testing.T) {
			store := &prepareFailureStore{FileReceiveAccountingStore: FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}}
			m, peer := testManager(t, Options{AccountingStore: store})
			destination := t.TempDir()
			if _, err := m.Offer(peer, testManifest("promote", Entry{ID: "dir", Path: "empty", Kind: Directory})); err != nil {
				t.Fatal(err)
			}
			store.onSave = func(state ReceiveAccounting, leases ...ReceiveRetirementLease) error {
				if state.Preparation != nil {
					err := store.FileReceiveAccountingStore.SaveReceiveAccounting(state, leases...)
					if len(state.Roots) != 0 && mode == "cancel-before" {
						m.batches["promote"].cancel()
					}
					return err
				}
				if mode == "fail-before" {
					return errors.New("retirement failed")
				}
				err := store.FileReceiveAccountingStore.SaveReceiveAccounting(state, leases...)
				if mode == "cancel-after" {
					m.batches["promote"].cancel()
				}
				if mode == "fail-after" {
					return errors.New("retirement uncertain")
				}
				return err
			}
			b, err := m.Accept("promote", destination)
			if mode == "cancel-after" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrReceiveRecovery) {
				t.Fatal(err)
			}
			if b.Destination == "" || b.State == Pending || m.pendingPreparation != nil {
				t.Fatalf("committed promotion reverted: %+v", b)
			}
			if _, err := os.Stat(filepath.Join(b.Destination, "empty")); err != nil {
				t.Fatal("promoted directory rolled back", err)
			}
			if mode != "cancel-after" && m.ReceiveRecovery().State != "blocked" {
				t.Fatal("failed retirement not blocked")
			}
			state, loadErr := store.LoadReceiveAccounting()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if (state.Preparation != nil) != (mode == "fail-before" || mode == "cancel-before") {
				t.Fatalf("retirement fixture %+v", state)
			}
			store.onSave = nil
			// An unrelated batch's close must retain this outstanding intent.
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			state, loadErr = store.LoadReceiveAccounting()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if (state.Preparation != nil) != (mode == "fail-before" || mode == "cancel-before") {
				t.Fatal("Close changed the committed index")
			}
			if mode == "fail-after" {
				g, l, e := store.LoadReceiveRetirementGuard(accountingLimits(AccountingLimits{}))
				if e != nil || g.Before.Preparation == nil {
					t.Fatal("Close erased independent preparation evidence", e)
				}
				l.Close()
			}
			reopened, err := NewManager(Options{AccountingStore: store, ExistingState: true})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if mode != "cancel-after" && reopened.ReceiveRecovery().State != "blocked" {
				t.Fatal("intent escaped reopen")
			}
		})
	}
}

func intentFixture(t *testing.T) (FileReceiveAccountingStore, ReceiveAccounting) {
	t.Helper()
	destination := t.TempDir()
	parent, err := openDestination(destination)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := rootIdentity(parent)
	parent.Close()
	if err != nil {
		t.Fatal(err)
	}
	plan := &ReceivePreparation{Destination: destination, DestinationIdentity: identity, Root: "sobalink-" + strings.Repeat("a", 32), Stage: ".incoming-" + strings.Repeat("b", 32), OwnerToken: strings.Repeat("c", 32)}
	return FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}, ReceiveAccounting{Version: 2, Roots: []ReceiveRoot{}, Preparation: plan}
}

func TestPreparationIntentStrictFieldsAndBudgets(t *testing.T) {
	store, state := intentFixture(t)
	data, _ := json.Marshal(state)
	plan, _ := json.Marshal(state.Preparation)
	if err := store.SaveReceiveAccounting(state); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadReceiveAccounting(); err != nil {
		t.Fatal(err)
	}
	cases := []string{`{"version":2,"roots":[]}`, `{"version":2,"roots":[],"Preparation":null}`, `{"version":2,"roots":[],"preparation":null,"preparation":null}`, `{"version":1,"roots":[],"preparation":null}`, `{"version":3,"roots":[],"preparation":null}`, `{"Version":2,"roots":[],"preparation":null}`, `{"version":2,"version":2,"roots":[],"preparation":null}`, `{"version":2,"roots":[],"roots":[],"preparation":null}`, `{"version":2,"preparation":null}`}
	var fields map[string]json.RawMessage
	json.Unmarshal(plan, &fields)
	for key, value := range fields {
		omitted := map[string]json.RawMessage{}
		for k, v := range fields {
			if k != key {
				omitted[k] = v
			}
		}
		raw, _ := json.Marshal(omitted)
		cases = append(cases, strings.Replace(string(data), string(plan), string(raw), 1))
		duplicated := string(plan[:len(plan)-1]) + fmt.Sprintf(",%q:%s}", key, value)
		cases = append(cases, strings.Replace(string(data), string(plan), duplicated, 1))
		alias := strings.Replace(string(plan), fmt.Sprintf("%q:", key), fmt.Sprintf("%q:", strings.ToUpper(key)), 1)
		cases = append(cases, strings.Replace(string(data), string(plan), alias, 1))
	}
	for i, raw := range cases {
		mustWrite(t, store.Path, raw)
		if _, err := store.LoadReceiveAccounting(); err == nil {
			t.Fatalf("accepted strict case %d %s", i, raw)
		}
	}
	for _, budget := range []AccountingLimits{{MaxEntries: 1}, {MaxBytes: 1}, {MaxPathBytes: 1}} {
		m, peer := testManager(t, Options{AccountingStore: FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}})
		m.accountingLimits = accountingLimits(budget)
		destination := t.TempDir()
		if _, err := m.Offer(peer, testManifest("budget", Entry{ID: "dir", Path: "empty", Kind: Directory})); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Accept("budget", destination); err == nil {
			t.Fatal("budget accepted overlap")
		}
		if len(mustReadDir(t, destination)) != 0 {
			t.Fatal("allocated before metadata budget")
		}
	}
}

func TestPreparationIntentMissingRootRequiresSameParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory rename")
	}
	for _, replacement := range []string{"before-inventory", "during-retirement", "root-during-retirement"} {
		t.Run(replacement, func(t *testing.T) {
			file, state := intentFixture(t)
			if err := file.SaveReceiveAccounting(state); err != nil {
				t.Fatal(err)
			}
			store := &prepareFailureStore{FileReceiveAccountingStore: file}
			replace := func() {
				if err := os.Rename(state.Preparation.Destination, state.Preparation.Destination+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(state.Preparation.Destination, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if replacement == "before-inventory" {
				replace()
			} else {
				once := true
				store.onSave = func(next ReceiveAccounting, leases ...ReceiveRetirementLease) error {
					if next.Preparation == nil && once {
						once = false
						if replacement == "root-during-retirement" {
							if err := os.Mkdir(filepath.Join(state.Preparation.Destination, state.Preparation.Root), 0700); err != nil {
								t.Fatal(err)
							}
						} else {
							replace()
						}
					}
					return file.SaveReceiveAccounting(next, leases...)
				}
			}
			m, err := NewManager(Options{AccountingStore: store, ExistingState: true})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if v := m.ReceiveRecovery(); v.State != "blocked" || v.ReservedBytes != nil {
				t.Fatal("replacement retired intent")
			}
			durable, err := file.LoadReceiveAccounting()
			if err != nil {
				t.Fatal(err)
			}
			if durable.Preparation == nil {
				g, l, e := file.LoadReceiveRetirementGuard(accountingLimits(AccountingLimits{}))
				if e != nil || g.Before.Preparation == nil {
					t.Fatal("replacement erased evidence", e)
				}
				l.Close()
			}
			if _, err := m.ConfirmReceiveRecovery(context.Background(), true); err == nil {
				t.Fatal("replacement confirmed")
			}
		})
	}
}

func TestPreparationIntentOtherRootWriterPreservesOutstandingPlan(t *testing.T) {
	store := &prepareFailureStore{FileReceiveAccountingStore: FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}}
	m, peer := testManager(t, Options{AccountingStore: store})
	if _, err := m.Offer(peer, testManifest("older", testEntry("file", "file", "x"))); err != nil {
		t.Fatal(err)
	}
	older, err := m.Accept("older", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if _, err := m.Offer(peer, testManifest("failed", Entry{ID: "dir", Path: "empty", Kind: Directory})); err != nil {
		t.Fatal(err)
	}
	store.onSave = func(state ReceiveAccounting, leases ...ReceiveRetirementLease) error {
		if len(state.Roots) == 2 {
			record := state.Roots[1]
			mustWrite(t, filepath.Join(record.OwnedRoot, "empty", "foreign"), "user data")
			return errors.New("promotion unavailable")
		}
		return store.FileReceiveAccountingStore.SaveReceiveAccounting(state, leases...)
	}
	if _, err := m.Accept("failed", destination); !errors.Is(err, ErrReceiveRecovery) {
		t.Fatal(err)
	}
	expected := *m.accounting.Preparation
	if _, err := os.Stat(filepath.Join(destination, expected.Root, expected.Stage)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rollback did not remove stage before cleanup denial", err)
	}
	store.onSave = nil
	if _, err := m.Cancel("older"); err != nil {
		t.Fatal(err)
	}
	state, err := store.LoadReceiveAccounting()
	if err != nil || state.Preparation == nil || *state.Preparation != expected {
		t.Fatal("unrelated root retirement erased intent", err)
	}
	if _, err := os.Stat(older.Destination); err != nil {
		t.Fatal("saved root removed", err)
	}
	if err := m.Close(); !errors.Is(err, ErrReceiveRecovery) {
		t.Fatal(err)
	}
	state, err = store.LoadReceiveAccounting()
	if err != nil || state.Preparation == nil || *state.Preparation != expected {
		t.Fatal("normal Close erased intent", err)
	}
	reopened, err := NewManager(Options{AccountingStore: store, ExistingState: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.ReceiveRecovery().State != "blocked" {
		t.Fatal("outstanding intent not recovered")
	}
}

func TestPreparationIntentCancelledRetirementIsRetryable(t *testing.T) {
	for _, phase := range []string{"before-save", "during-save", "after-save"} {
		t.Run(phase, func(t *testing.T) {
			file, state := intentFixture(t)
			if err := file.SaveReceiveAccounting(state); err != nil {
				t.Fatal(err)
			}
			store := &prepareFailureStore{FileReceiveAccountingStore: file, onSave: func(_ ReceiveAccounting, _ ...ReceiveRetirementLease) error { return errors.New("offline index") }}
			m, err := NewManager(Options{AccountingStore: store, ExistingState: true})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if m.ReceiveRecovery().State != "blocked" {
				t.Fatal("fixture not blocked")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "before-save" {
				cancel()
				store.onSave = func(_ ReceiveAccounting, _ ...ReceiveRetirementLease) error {
					t.Fatal("cancelled retirement saved")
					return nil
				}
			} else if phase == "during-save" {
				store.onSave = func(_ ReceiveAccounting, _ ...ReceiveRetirementLease) error { cancel(); return ctx.Err() }
			} else {
				store.onSave = func(next ReceiveAccounting, leases ...ReceiveRetirementLease) error {
					err := file.SaveReceiveAccounting(next, leases...)
					cancel()
					return err
				}
			}
			view, err := m.ConfirmReceiveRecovery(ctx, true)
			if phase != "after-save" {
				if err == nil || view.Applied {
					t.Fatalf("cancelled retirement %+v %v", view, err)
				}
				durable, err := file.LoadReceiveAccounting()
				if err != nil || durable.Preparation == nil {
					t.Fatal("cancelled intent lost", err)
				}
				store.onSave = nil
				if v, err := m.ConfirmReceiveRecovery(context.Background(), true); err != nil || !v.Applied {
					t.Fatalf("retry %+v %v", v, err)
				}
			} else if err != nil || !view.Applied || view.State != "ready" {
				t.Fatalf("durable retirement reverted on cancellation %+v %v", view, err)
			}
			durable, err := file.LoadReceiveAccounting()
			if err != nil || durable.Preparation != nil {
				t.Fatal("retirement not durable", err)
			}
		})
	}
}

func TestPreparationIntentPromotionReplacementDisarmsRollback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory rename")
	}
	for _, target := range []string{"destination", "root"} {
		t.Run(target, func(t *testing.T) {
			store := &prepareFailureStore{FileReceiveAccountingStore: FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}}
			m, peer := testManager(t, Options{AccountingStore: store})
			destination := t.TempDir()
			if _, err := m.Offer(peer, testManifest("replace", Entry{ID: "dir", Path: "empty", Kind: Directory})); err != nil {
				t.Fatal(err)
			}
			var original, substitute string
			store.onSave = func(state ReceiveAccounting, leases ...ReceiveRetirementLease) error {
				err := store.FileReceiveAccountingStore.SaveReceiveAccounting(state, leases...)
				if len(state.Roots) != 0 && state.Preparation != nil {
					substitute = state.Roots[0].OwnedRoot
					if target == "destination" {
						substitute = destination
					}
					original = substitute + "-original"
					if err := os.Rename(substitute, original); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(substitute, 0700); err != nil {
						t.Fatal(err)
					}
					mustWrite(t, filepath.Join(substitute, "foreign"), "substitute data")
				}
				return err
			}
			b, err := m.Accept("replace", destination)
			if !errors.Is(err, ErrReceiveRecovery) || b.State != Accepted || m.pendingPreparation != nil {
				t.Fatalf("promotion rollback armed %+v %v", b, err)
			}
			if data, err := os.ReadFile(filepath.Join(substitute, "foreign")); err != nil || string(data) != "substitute data" {
				t.Fatal("substitute modified")
			}
			root := original
			if target == "destination" {
				root = filepath.Join(original, filepath.Base(b.Destination))
			}
			if _, err := os.Stat(filepath.Join(root, "empty")); err != nil {
				t.Fatal("successful promotion rolled back", err)
			}
			store.onSave = nil
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			state, err := store.LoadReceiveAccounting()
			if err != nil || state.Preparation == nil {
				t.Fatal("replacement erased intent", err)
			}
		})
	}
}

func TestPreparationParentReplacementCannotProtectWrongPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory rename")
	}
	destination := t.TempDir()
	p := &preparedDestination{io: defaultPreparationIO()}
	p.io.openRoot = func(parent *os.Root, name string) (*os.Root, error) {
		root, err := parent.OpenRoot(name)
		if strings.HasPrefix(name, "sobalink-") {
			if err := os.Rename(destination, destination+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(destination, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(destination, name), 0700); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(destination, name, "foreign"), "preserved")
		}
		return root, err
	}
	p.io.protect = func(string) error { t.Fatal("absolute protection reached substituted parent"); return nil }
	err := p.prepare(context.Background(), destination, nil, diskspace.Process, 1, nil)
	if err == nil {
		t.Fatal("parent replacement accepted")
	}
	if err := p.rollback(); err != nil {
		t.Fatal(err)
	}
	entries := mustReadDir(t, destination)
	if len(entries) != 1 {
		t.Fatal("substitute removed")
	}
	if data, err := os.ReadFile(filepath.Join(destination, entries[0].Name(), "foreign")); err != nil || string(data) != "preserved" {
		t.Fatal("substitute payload modified")
	}
}

func TestPreparationIntentPromotionMarkerReplacementBlocksRetirement(t *testing.T) {
	store := &prepareFailureStore{FileReceiveAccountingStore: FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}}
	m, peer := testManager(t, Options{AccountingStore: store})
	if _, err := m.Offer(peer, testManifest("marker", Entry{ID: "dir", Path: "empty", Kind: Directory})); err != nil {
		t.Fatal(err)
	}
	var marker string
	store.onSave = func(state ReceiveAccounting, leases ...ReceiveRetirementLease) error {
		err := store.FileReceiveAccountingStore.SaveReceiveAccounting(state, leases...)
		if len(state.Roots) > 0 && state.Preparation != nil {
			r := state.Roots[0]
			marker = filepath.Join(r.OwnedRoot, r.Stage, receiveOwnerMarker)
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, marker, "unknown payload")
		}
		return err
	}
	b, err := m.Accept("marker", t.TempDir())
	if !errors.Is(err, ErrReceiveRecovery) || b.State != Accepted || m.pendingPreparation != nil {
		t.Fatalf("marker substitution %+v %v", b, err)
	}
	state, err := store.LoadReceiveAccounting()
	if err != nil || state.Preparation == nil {
		t.Fatal("replacement retired intent", err)
	}
	store.onSave = nil
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "unknown payload" {
		t.Fatal("unknown marker replacement removed")
	}
}

func TestPreparationV1CanonicalByteBudgetCompatibility(t *testing.T) {
	const canonical = `{"version":1,"roots":[]}`
	store := FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json"), Limits: AccountingLimits{MaxBytes: int64(len(canonical))}}
	if err := store.SaveReceiveAccounting(ReceiveAccounting{Version: 1}); err != nil {
		t.Fatal("v1 empty snapshot no longer fits its canonical byte budget", err)
	}
	data, err := os.ReadFile(store.Path)
	if err != nil || string(data) != canonical {
		t.Fatalf("v1 changed %s %v", data, err)
	}
	if _, err := store.LoadReceiveAccounting(); err != nil {
		t.Fatal("v1 snapshot could not be read", err)
	}
}
