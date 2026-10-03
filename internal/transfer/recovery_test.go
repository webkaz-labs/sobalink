package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func accountingFixture(t *testing.T) (string, ReceiveAccounting, ReceiveRoot) {
	t.Helper()
	dir := t.TempDir()
	destination := filepath.Join(dir, "receive")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(crashOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	peer := Peer{ID: "fixture-peer", Generation: 1}
	if err := m.BindPeer(peer); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Offer(peer, testManifest("fixture", testEntry("file", "payload", "12345678"))); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Accept("fixture", destination); err != nil {
		t.Fatal(err)
	}
	// Close handles without normal Manager.Close cleanup, as after a crash.
	b := m.batches["fixture"]
	if err := b.root.Close(); err != nil {
		t.Fatal(err)
	}
	b.root = nil
	return dir, m.accounting, m.accounting.Roots[0]
}

func addRetained(t *testing.T, record ReceiveRoot, name, contents string) string {
	t.Helper()
	file := filepath.Join(record.OwnedRoot, record.Stage, name)
	if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func restarted(t *testing.T, dir string) *Manager {
	t.Helper()
	options := crashOptions(dir)
	options.ExistingState = true
	m, err := NewManager(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestReceiveAccountingInventoriesUnrecordedFilesAndHardlinksOnce(t *testing.T) {
	dir, _, r := accountingFixture(t)
	part := addRetained(t, r, "unrecorded-name", "1234567")
	if err := os.Link(part, filepath.Join(r.OwnedRoot, r.Stage, "other-link")); err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(r.OwnedRoot, "payload")
	if err := os.Link(part, saved); err != nil {
		t.Fatal(err)
	}
	m := restarted(t, dir)
	view := m.ReceiveRecovery()
	if view.ReservedBytes == nil || *view.ReservedBytes != 7 {
		t.Fatalf("hardlink accounting: %+v", view)
	}
	// No inventory/Close operation touches a saved name or its data.
	m.Close()
	if data, err := os.ReadFile(saved); err != nil || string(data) != "1234567" {
		t.Fatalf("saved output changed: %q %v", data, err)
	}
}

func TestReceiveAccountingUnknownIsNotZero(t *testing.T) {
	cases := map[string]func(*testing.T, string, ReceiveRoot){
		"corrupt": func(t *testing.T, dir string, r ReceiveRoot) {
			mustWrite(t, filepath.Join(dir, "receive-accounting.json"), "{broken")
		},
		"missing_destination": func(t *testing.T, dir string, r ReceiveRoot) {
			if err := os.Rename(r.Destination, r.Destination+"-offline"); err != nil {
				t.Fatal(err)
			}
		},
		"replaced_destination": func(t *testing.T, dir string, r ReceiveRoot) {
			if err := os.Rename(r.Destination, r.Destination+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(r.Destination, 0700); err != nil {
				t.Fatal(err)
			}
		},
		"replaced_owned_root": func(t *testing.T, dir string, r ReceiveRoot) {
			if err := os.Rename(r.OwnedRoot, r.OwnedRoot+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(r.OwnedRoot, 0700); err != nil {
				t.Fatal(err)
			}
		},
		"replaced_stage": func(t *testing.T, dir string, r ReceiveRoot) {
			stage := filepath.Join(r.OwnedRoot, r.Stage)
			if err := os.Rename(stage, stage+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(stage, 0700); err != nil {
				t.Fatal(err)
			}
		},
		"nested_unrecorded": func(t *testing.T, dir string, r ReceiveRoot) {
			if err := os.Mkdir(filepath.Join(r.OwnedRoot, r.Stage, "nested"), 0700); err != nil {
				t.Fatal(err)
			}
		},
		"changed_marker": func(t *testing.T, dir string, r ReceiveRoot) {
			mustWrite(t, filepath.Join(r.OwnedRoot, r.Stage, receiveOwnerMarker), "wrong-token")
		},
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			dir, _, r := accountingFixture(t)
			addRetained(t, r, "part-fixture", "1234567")
			damage(t, dir, r)
			m := restarted(t, dir)
			view := m.ReceiveRecovery()
			if view.State != "blocked" || view.ReservedBytes != nil {
				t.Fatalf("unknown appeared empty: %+v", view)
			}
			peer := Peer{ID: "fixture-peer", Generation: 1}
			if err := m.BindPeer(peer); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Offer(peer, testManifest("new", testEntry("a", "a", "1"))); !errors.Is(err, ErrReceiveRecovery) {
				t.Fatalf("admitted unknown state: %v", err)
			}
			if _, err := m.ConfirmReceiveRecovery(context.Background(), true); !errors.Is(err, ErrReceiveRecovery) {
				t.Fatalf("review discarded damaged accounting: %v", err)
			}
		})
	}
}

func mustWrite(t *testing.T, name, value string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReceiveAccountingDeletedChildVersusUnavailableDestination(t *testing.T) {
	for _, child := range []string{"stage", "root"} {
		t.Run(child, func(t *testing.T) {
			dir, _, r := accountingFixture(t)
			target := r.OwnedRoot
			if child == "stage" {
				target = filepath.Join(target, r.Stage)
			}
			if err := os.RemoveAll(target); err != nil {
				t.Fatal(err)
			}
			view := restarted(t, dir).ReceiveRecovery()
			if view.State != "ready" || view.ReservedBytes == nil || *view.ReservedBytes != 0 {
				t.Fatalf("verified deletion not zero: %+v", view)
			}
			data, err := os.ReadFile(filepath.Join(dir, "receive-accounting.json"))
			if err != nil || string(data) != `{"version":1,"roots":[]}` {
				t.Fatalf("deleted stage/root not durably retired: %s %v", data, err)
			}
		})
	}
}

func TestReceiveAccountingSymlinksFailClosed(t *testing.T) {
	for _, location := range []string{"index", "marker", "stage", "part", "destination"} {
		t.Run(location, func(t *testing.T) {
			dir, _, r := accountingFixture(t)
			outside := filepath.Join(dir, "outside")
			mustWrite(t, outside, r.OwnerToken)
			name := filepath.Join(dir, "receive-accounting.json")
			switch location {
			case "marker":
				name = filepath.Join(r.OwnedRoot, r.Stage, receiveOwnerMarker)
			case "stage":
				name = filepath.Join(r.OwnedRoot, r.Stage)
			case "part":
				name = addRetained(t, r, "part-fixture", "7")
			case "destination":
				name = r.Destination
			}
			if location == "destination" {
				if err := os.Rename(name, name+"-original"); err != nil {
					t.Fatal(err)
				}
			} else if location == "stage" {
				if err := os.Remove(filepath.Join(name, receiveOwnerMarker)); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, name); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			view := restarted(t, dir).ReceiveRecovery()
			if view.State != "blocked" || view.ReservedBytes != nil {
				t.Fatalf("followed %s symlink: %+v", location, view)
			}
			if data, err := os.ReadFile(outside); err != nil || string(data) != r.OwnerToken {
				t.Fatal("modified symlink target")
			}
		})
	}
}

func TestReceiveAccountingBudgetsAndCancellation(t *testing.T) {
	dir, _, r := accountingFixture(t)
	addRetained(t, r, "first", "123")
	addRetained(t, r, "second", "4567")
	for name, budget := range map[string]AccountingLimits{
		"entries": {MaxEntries: 2}, "metadata": {MaxBytes: 1}, "path": {MaxPathBytes: 1}, "depth": {MaxDepth: -1},
	} {
		t.Run(name, func(t *testing.T) {
			options := crashOptions(dir)
			options.AccountingLimits = budget
			m, err := NewManager(options)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if view := m.ReceiveRecovery(); view.State != "blocked" || view.ReservedBytes != nil {
				t.Fatalf("ignored budget: %+v", view)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	options := crashOptions(dir)
	options.Context = ctx
	m, err := NewManager(options)
	if !errors.Is(err, context.Canceled) || m != nil {
		t.Fatalf("cancelled startup published a manager: %v %v", m, err)
	}
	m, err = NewManager(crashOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if view := m.ReceiveRecovery(); view.ReservedBytes == nil || *view.ReservedBytes != 7 {
		t.Fatalf("cancelled inventory changed retained accounting: %+v", view)
	}
}

func TestReceiveAccountingMultipleRootsAndLowerQuota(t *testing.T) {
	dir, state, r := accountingFixture(t)
	addRetained(t, r, "part-first", "123")
	root, actual, stage, token, parentID, err := prepareDestination(r.Destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := receiveRootRecord(r.Destination, actual, stage, token, parentID, root)
	root.Close()
	if err != nil {
		t.Fatal(err)
	}
	addRetained(t, second, "part-second", "4567")
	if err := os.Link(filepath.Join(r.OwnedRoot, r.Stage, "part-first"), filepath.Join(second.OwnedRoot, second.Stage, "part-duplicate")); err != nil {
		t.Fatal(err)
	}
	state.Roots = append(state.Roots, second)
	if err := (FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")}).SaveReceiveAccounting(state); err != nil {
		t.Fatal(err)
	}
	m := restarted(t, dir)
	if view := m.ReceiveRecovery(); view.ReservedBytes == nil || *view.ReservedBytes != 7 {
		t.Fatalf("multiple roots: %+v", view)
	}
	if err := m.UpdateLimits(Limits{MaxReservedBytes: 6}); err != nil {
		t.Fatal(err)
	}
	peer := Peer{ID: "fixture-peer", Generation: 1}
	m.BindPeer(peer)
	if _, err := m.Offer(peer, testManifest("new", testEntry("a", "a", "1"))); !errors.Is(err, ErrLimit) {
		t.Fatalf("lower quota lost retained bytes: %v", err)
	}
}

func TestReceiveAccountingLegacyReviewIsExplicitAndDurable(t *testing.T) {
	dir := t.TempDir()
	options := crashOptions(dir)
	options.ExistingState = true
	m, err := NewManager(options)
	if err != nil {
		t.Fatal(err)
	}
	if m.ReceiveRecovery().Code != "legacy_review_required" {
		t.Fatal("old state admitted without review")
	}
	view, err := m.ConfirmReceiveRecovery(context.Background(), false)
	if err != nil || view.Applied || view.State != "blocked" {
		t.Fatalf("preview applied review: %+v %v", view, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "receive-accounting.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview persisted an empty index")
	}
	view, err = m.ConfirmReceiveRecovery(context.Background(), true)
	if err != nil || !view.Applied || view.State != "ready" {
		t.Fatalf("confirmation: %+v %v", view, err)
	}
	m.Close()
	m, err = NewManager(options)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.ReceiveRecovery().State != "ready" {
		t.Fatal("explicit review did not persist")
	}
}

type cancellationDuringMissingLoadStore struct {
	FileReceiveAccountingStore
	loads          int
	callerCancel   context.CancelFunc
	lifetimeCancel context.CancelFunc
}

func (s *cancellationDuringMissingLoadStore) LoadReceiveAccounting() (ReceiveAccounting, error) {
	s.loads++
	if s.loads == 2 {
		s.callerCancel()
		s.lifetimeCancel()
	}
	return s.FileReceiveAccountingStore.LoadReceiveAccounting()
}

type dualRecoveryContext struct {
	context.Context
	caller, lifetime context.Context
}

func (ctx dualRecoveryContext) Err() error {
	if err := ctx.caller.Err(); err != nil {
		return err
	}
	if err := ctx.lifetime.Err(); err != nil {
		return err
	}
	return ctx.Context.Err()
}

func TestLegacyReviewCancellationDuringMissingLoadDoesNotInitializeIndex(t *testing.T) {
	dir := t.TempDir()
	caller, cancelCaller := context.WithCancel(context.Background())
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	store := &cancellationDuringMissingLoadStore{
		FileReceiveAccountingStore: FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")},
		callerCancel:               cancelCaller, lifetimeCancel: cancelLifetime,
	}
	options := Options{AccountingStore: store, ExistingState: true}
	m, err := NewManager(options)
	if err != nil {
		t.Fatal(err)
	}
	view, err := m.ConfirmReceiveRecovery(dualRecoveryContext{Context: caller, caller: caller, lifetime: lifetime}, true)
	if err == nil || view.Applied {
		t.Fatalf("cancelled review applied: %+v %v", view, err)
	}
	if _, err := os.Stat(store.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled review initialized index: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewManager(options)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if recovery := restarted.ReceiveRecovery(); recovery.Code != "legacy_review_required" || recovery.State != "blocked" {
		t.Fatalf("restart lost legacy review gate: %+v", recovery)
	}
}

type cancelStartupLoadStore struct {
	FileReceiveAccountingStore
	cancel context.CancelFunc
}

func (s cancelStartupLoadStore) LoadReceiveAccounting() (ReceiveAccounting, error) {
	s.cancel()
	return s.FileReceiveAccountingStore.LoadReceiveAccounting()
}

func TestCancelledStartupLoadDoesNotInitializeAccountingIndex(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	store := cancelStartupLoadStore{
		FileReceiveAccountingStore: FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")},
		cancel:                     cancel,
	}
	if _, err := NewManager(Options{Context: ctx, AccountingStore: store}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled startup load error = %v", err)
	}
	if _, err := os.Stat(store.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled startup initialized index: %v", err)
	}
}

func TestReceiveAccountingCrashBoundariesChild(t *testing.T) {
	dir := os.Getenv("SOBALINK_ACCOUNTING_BOUNDARY_DIR")
	if dir == "" {
		t.Skip("private subprocess helper")
	}
	m, err := NewManager(crashOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	peer := Peer{ID: "fixture-peer", Generation: 1}
	m.BindPeer(peer)
	if _, err := m.Offer(peer, testManifest("fixture", testEntry("file", "payload", "1234567"))); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Accept("fixture", filepath.Join(dir, "receive")); err != nil {
		t.Fatal(err)
	}
	b := m.batches["fixture"]
	boundary := os.Getenv("SOBALINK_ACCOUNTING_BOUNDARY")
	if boundary == "accepted" {
		os.Exit(83)
	}
	name := b.stage + "/part-fixture"
	f, err := b.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if boundary == "created" {
		os.Exit(83)
	}
	if _, err := io.WriteString(f, "1234567"); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if boundary == "written" {
		os.Exit(83)
	}
	if err := commitFile(b.root, name, "payload", info); err != nil {
		t.Fatal(err)
	}
	if boundary == "linked" {
		os.Exit(83)
	}
	if err := b.root.Remove(name); err != nil {
		t.Fatal(err)
	}
	if boundary == "unlinked" {
		os.Exit(83)
	}
	if err := b.root.Remove(filepath.Join(b.stage, receiveOwnerMarker)); err != nil {
		t.Fatal(err)
	}
	if err := b.root.Remove(b.stage); err != nil {
		t.Fatal(err)
	}
	if boundary == "stage_removed" {
		os.Exit(83)
	}
	if err := m.accountingStore.SaveReceiveAccounting(ReceiveAccounting{Version: 1}); err != nil {
		t.Fatal(err)
	}
	os.Exit(83)
}

func TestReceiveAccountingCrashBoundaries(t *testing.T) {
	for _, boundary := range []string{"accepted", "created", "written", "linked", "unlinked", "stage_removed", "index_retired"} {
		t.Run(boundary, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "receive"), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestReceiveAccountingCrashBoundariesChild$")
			cmd.Env = append(os.Environ(), "SOBALINK_ACCOUNTING_BOUNDARY_DIR="+dir, "SOBALINK_ACCOUNTING_BOUNDARY="+boundary)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 83 {
				t.Fatalf("crash child: %v %s", err, output)
			}
			m := restarted(t, dir)
			want := int64(0)
			if boundary == "written" || boundary == "linked" {
				want = 7
			}
			if view := m.ReceiveRecovery(); view.ReservedBytes == nil || *view.ReservedBytes != want {
				t.Fatalf("boundary %s: %+v want %d", boundary, view, want)
			}
			if boundary == "linked" || boundary == "unlinked" || boundary == "stage_removed" || boundary == "index_retired" {
				matches, err := filepath.Glob(filepath.Join(dir, "receive", "sobalink-*", "payload"))
				if err != nil || len(matches) != 1 {
					t.Fatalf("saved file lost: %v %v", matches, err)
				}
				if data, err := os.ReadFile(matches[0]); err != nil || string(data) != "1234567" {
					t.Fatal("saved data changed")
				}
			}
		})
	}
}

func TestReceiveRecoveryViewDoesNotExposePrivateAccounting(t *testing.T) {
	dir, _, r := accountingFixture(t)
	addRetained(t, r, "part-fixture", "1234567")
	m := restarted(t, dir)
	data, err := json.Marshal(m.ReceiveRecovery())
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{dir, r.Stage, r.OwnerToken, r.RootIdentity, r.StageIdentity} {
		if strings.Contains(string(data), private) {
			t.Fatal("private accounting exposed")
		}
	}
}

type inventoryActionContext struct {
	context.Context
	calls, after int
	action       func()
}

func (c *inventoryActionContext) Err() error {
	c.calls++
	if c.calls == c.after {
		c.action()
	}
	return c.Context.Err()
}

func TestReceiveInventoryDetectsReplacementDuringTraversal(t *testing.T) {
	dir, _, r := accountingFixture(t)
	addRetained(t, r, "part-fixture", "1234567")
	ctx := &inventoryActionContext{Context: context.Background(), after: 2, action: func() {
		if err := os.Rename(r.Destination, r.Destination+"-original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(r.Destination, 0700); err != nil {
			t.Fatal(err)
		}
	}}
	options := crashOptions(dir)
	options.Context = ctx
	m, err := NewManager(options)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if view := m.ReceiveRecovery(); view.State != "blocked" || view.ReservedBytes != nil {
		t.Fatalf("published stale inventory after replacement: %+v", view)
	}
}

func TestReceiveInventoryCancellationDuringTraversalPublishesNoPartialTotal(t *testing.T) {
	dir, _, r := accountingFixture(t)
	addRetained(t, r, "first", "123")
	addRetained(t, r, "second", "4567")
	lifetime, cancel := context.WithCancel(context.Background())
	ctx := &inventoryActionContext{Context: lifetime, after: 4, action: cancel}
	options := crashOptions(dir)
	options.Context = ctx
	m, err := NewManager(options)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if view := m.ReceiveRecovery(); view.State != "blocked" || view.ReservedBytes != nil {
		t.Fatalf("published partial inventory after cancellation: %+v", view)
	}
}

func TestReceiveMissingIndexWithExistingStateRequiresReview(t *testing.T) {
	dir, _, r := accountingFixture(t)
	retained := addRetained(t, r, "part-fixture", "1234567")
	if err := os.Remove(filepath.Join(dir, "receive-accounting.json")); err != nil {
		t.Fatal(err)
	}
	m := restarted(t, dir)
	view := m.ReceiveRecovery()
	if view.Code != "legacy_review_required" || view.ReservedBytes != nil {
		t.Fatalf("missing index silently reset retained storage: %+v", view)
	}
	if data, err := os.ReadFile(retained); err != nil || string(data) != "1234567" {
		t.Fatal("review gate altered old data")
	}
}

func TestReceiveAccountingRejectsIncompleteAndDuplicateFields(t *testing.T) {
	dir, state, record := accountingFixture(t)
	addRetained(t, record, "part-fixture", "1234567")
	root, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"version1-only":     `{"version":1}`,
		"roots-omitted":     `{"version":1}`,
		"version-omitted":   `{"roots":[]}`,
		"roots-duplicate":   `{"version":1,"roots":[` + string(root) + `],"roots":[]}`,
		"version-duplicate": `{"version":2,"version":1,"roots":[` + string(root) + `]}`,
		"case-alias":        `{"Version":1,"roots":[` + string(root) + `]}`,
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(root, &fields); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		missing := make(map[string]json.RawMessage)
		for k, v := range fields {
			if k != key {
				missing[k] = v
			}
		}
		encoded, err := json.Marshal(missing)
		if err != nil {
			t.Fatal(err)
		}
		cases["missing-"+key] = `{"version":1,"roots":[` + string(encoded) + `]}`
		// Even identical duplicate fields are ambiguous and must be rejected.
		duplicate := strings.TrimSuffix(string(root), "}") + `,"` + key + `":` + string(value) + `}`
		cases["duplicate-"+key] = `{"version":1,"roots":[` + duplicate + `]}`
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			index := filepath.Join(dir, "receive-accounting.json")
			mustWrite(t, index, data)
			m := restarted(t, dir)
			if v := m.ReceiveRecovery(); v.State != "blocked" || v.ReservedBytes != nil {
				t.Fatalf("incomplete/duplicate index appeared empty: %+v", v)
			}
			peer := Peer{ID: "fixture-peer", Generation: 1}
			if err := m.BindPeer(peer); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Offer(peer, testManifest("new", testEntry("file", "payload", "12345678"))); !errors.Is(err, ErrReceiveRecovery) {
				t.Fatalf("admitted eight bytes over retained seven: %v", err)
			}
			if _, err := m.ConfirmReceiveRecovery(context.Background(), true); !errors.Is(err, ErrReceiveRecovery) {
				t.Fatalf("confirmation discarded invalid index: %v", err)
			}
			if after, err := os.ReadFile(index); err != nil || string(after) != data {
				t.Fatal("invalid index was rewritten")
			}
			mustWrite(t, index, string(valid))
		})
	}
}

func TestReceiveAccountingEmptyIndexCanonicalAndNullCompatibility(t *testing.T) {
	dir := t.TempDir()
	store := FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")}
	if err := store.SaveReceiveAccounting(ReceiveAccounting{Version: 1}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.Path)
	if err != nil || string(data) != `{"version":1,"roots":[]}` {
		t.Fatalf("noncanonical empty index: %s %v", data, err)
	}
	for _, raw := range []string{`{"version":1,"roots":[]}`, `{"version":1,"roots":null}`} {
		mustWrite(t, store.Path, raw)
		state, err := store.LoadReceiveAccounting()
		if err != nil || len(state.Roots) != 0 {
			t.Fatalf("explicit empty roots: %+v %v", state, err)
		}
		if err := store.SaveReceiveAccounting(state); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(store.Path)
		if err != nil || string(data) != `{"version":1,"roots":[]}` {
			t.Fatalf("empty roots not canonicalized: %s %v", data, err)
		}
	}
}

func TestReceiveAccountingExcludesOnlyValidatedInternalMarker(t *testing.T) {
	dir, _, r := accountingFixture(t)
	addRetained(t, r, ".sobalink-owner-copy", "123")
	addRetained(t, r, "unfamiliar-name", "4567")
	view := restarted(t, dir).ReceiveRecovery()
	if view.ReservedBytes == nil || *view.ReservedBytes != 7 {
		t.Fatalf("unknown files excluded as metadata: %+v", view)
	}
}

func TestUnavailableStartupPolicyRemovalMustPersistBeforeRecovery(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "missing")
	store := &testPolicyStore{policies: []ReceivePolicy{{Peer: Peer{ID: "fixture-peer", Generation: 1}, Destination: destination, AutoAccept: true}}, saveErr: errors.New("save unavailable")}
	opts := crashOptions(dir)
	opts.PolicyStore = store
	m, err := NewManager(opts)
	if err != nil {
		t.Fatal("receive policy stopped Manager", err)
	}
	defer m.Close()
	if view := m.ReceiveRecovery(); view.State != "blocked" || len(m.ReceivePolicies()) != 0 {
		t.Fatalf("failed removal re-enabled policy: %+v", view)
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if view, err := m.ConfirmReceiveRecovery(context.Background(), true); err == nil || view.Applied {
		t.Fatalf("failed durable removal cleared gate: %+v %v", view, err)
	}
	store.saveErr = nil
	if view, err := m.ConfirmReceiveRecovery(context.Background(), true); err != nil || !view.Applied {
		t.Fatalf("retry removal: %+v %v", view, err)
	}
	if len(store.policies) != 0 || len(m.ReceivePolicies()) != 0 {
		t.Fatal("confirmation revived removed policy")
	}
}

func TestUnavailableLegacyPolicyRemovalPreservesExplicitReview(t *testing.T) {
	dir := t.TempDir()
	store := &testPolicyStore{policies: []ReceivePolicy{{Peer: Peer{ID: "fixture-peer", Generation: 1}, Destination: filepath.Join(dir, "missing"), AutoAccept: true}}, saveErr: errors.New("save unavailable")}
	opts := crashOptions(dir)
	opts.ExistingState = true
	opts.PolicyStore = store
	m, err := NewManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.ReceiveRecovery().Code != "legacy_review_required" {
		t.Fatal("failed policy removal lost legacy gate")
	}
	store.saveErr = nil
	if view, err := m.ConfirmReceiveRecovery(context.Background(), false); err != nil || view.Applied {
		t.Fatalf("preview cleared legacy gate: %+v %v", view, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "receive-accounting.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview initialized legacy index")
	}
	if view, err := m.ConfirmReceiveRecovery(context.Background(), true); err != nil || !view.Applied {
		t.Fatalf("legacy removal retry: %+v %v", view, err)
	}
	if len(store.policies) != 0 {
		t.Fatal("legacy confirmation retained unavailable approval")
	}
}

func TestDeletedStageRetiresIndexWithoutTouchingSavedMarkerName(t *testing.T) {
	dir, _, r := accountingFixture(t)
	saved := filepath.Join(r.OwnedRoot, ".sobalink-owner")
	mustWrite(t, saved, "saved-output")
	if err := os.RemoveAll(filepath.Join(r.OwnedRoot, r.Stage)); err != nil {
		t.Fatal(err)
	}
	m := restarted(t, dir)
	if v := m.ReceiveRecovery(); v.State != "ready" || v.ReservedBytes == nil || *v.ReservedBytes != 0 {
		t.Fatalf("deleted stage: %+v", v)
	}
	m.Close()
	data, err := os.ReadFile(saved)
	if err != nil || string(data) != "saved-output" {
		t.Fatal("retirement removed saved marker-name payload")
	}
	index, err := os.ReadFile(filepath.Join(dir, "receive-accounting.json"))
	if err != nil || string(index) != `{"version":1,"roots":[]}` {
		t.Fatalf("retirement not canonical: %s %v", index, err)
	}
}
