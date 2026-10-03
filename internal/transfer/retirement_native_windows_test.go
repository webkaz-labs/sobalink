package transfer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func TestWindowsRetirementDirectoryBarrierNative(t *testing.T) {
	path := t.TempDir()
	if err := config.Protect(path, true); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()
	ok, releasePins := probeRemovalShape(t, root)
	defer func() {
		if err := releasePins(); err != nil {
			t.Error(err)
		}
	}()
	if !ok {
		t.Fatal("required retained-handle deletion shape failed")
	}
	if err := retirementSyncDirectory(root); err != nil {
		t.Fatalf("CHOSEN production barrier: %v", err)
	}
}

// Parent-relative Root opens share deletion; this exercises a stale Root.Name
// without closing its identity pin. No rename fixture is skipped on Windows.
func TestWindowsRetirementDirectoryBarrierRejectsRebinding(t *testing.T) {
	for _, replacement := range []string{"directory", "file", "junction"} {
		t.Run(replacement, func(t *testing.T) {
			base := t.TempDir()
			parent, err := os.OpenRoot(base)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			if err := parent.Mkdir("target", 0700); err != nil {
				t.Fatal(err)
			}
			root, err := parent.OpenRoot("target")
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			identity, err := rootIdentity(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := parent.Rename("target", "retained"); err != nil {
				t.Fatal("required parent-relative rename fixture", err)
			}
			switch replacement {
			case "directory":
				if err := parent.Mkdir("target", 0700); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := parent.WriteFile("target", []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			case "junction":
				// mklink /J requires no symbolic-link privilege and touches only
				// finite test-owned paths. The target is the retained original:
				// following it would pass identity but violate no-reparse policy.
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				output, err := exec.CommandContext(ctx, "cmd", "/c", "mklink", "/J", filepath.Join(base, "target"), filepath.Join(base, "retained")).CombinedOutput()
				if err != nil {
					t.Fatalf("required junction fixture: %v: %s", err, output)
				}
				defer func() {
					if err := os.Remove(filepath.Join(base, "target")); err != nil {
						t.Error(err)
					}
				}()
			}
			if err := retirementSyncDirectory(root); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("acknowledged rebound %s: %v", replacement, err)
			}
			if retained, err := rootIdentity(root); err != nil || retained != identity {
				t.Fatal("retained object changed", retained, err)
			}
		})
	}
}

func TestWindowsRetirementSavedPartialCleanup(t *testing.T) {
	file := FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}
	destination := t.TempDir()
	if err := config.Protect(filepath.Dir(file.Path), true); err != nil {
		t.Fatal(err)
	}
	var savedPaths []string
	var controlNames string
	for i := 0; i < 2; i++ {
		m, peer := testManager(t, Options{AccountingStore: file, ExistingState: i != 0})
		id := []string{"receive", "cancel"}[i]
		manifest := testManifest(id, testEntry("saved", receiveOwnerMarker, "saved marker bytes"), testEntry("partial", "partial-output", "complete payload"))
		if _, err := m.Offer(peer, manifest); err != nil {
			t.Fatal(err)
		}
		b, err := m.Accept(id, destination)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err := m.ReceiveFile(context.Background(), peer, id, "saved", strings.NewReader("saved marker bytes")); err != nil {
				t.Fatal(err)
			}
			saved := filepath.Join(b.Destination, receiveOwnerMarker)
			link := filepath.Join(b.Destination, "saved-hardlink")
			if err := os.Link(saved, link); err != nil {
				t.Fatal(err)
			}
			savedPaths = append(savedPaths, saved, link)
		}
		// A real synchronous receive writes nonempty incomplete bytes, removes
		// its partial, and leaves a marker-only private stage before cancel.
		if _, err := m.ReceiveFile(context.Background(), peer, id, "partial", strings.NewReader("complete")); !errors.Is(err, ErrIntegrity) {
			t.Fatal("incomplete receive did not reach the integrity check", err)
		}
		view, err := m.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		wrotePartial := false
		for _, f := range view.Files {
			if f.ID == "partial" && f.State == FileFailed && f.CompletedBytes == int64(len("complete")) {
				wrotePartial = true
			}
		}
		if !wrotePartial {
			t.Fatal("fixture did not write nonempty partial bytes before removal")
		}
		t.Logf("prior partial actually written: bytes=%d integrity failure then checked empty-stage inventory", len("complete"))
		r := m.accounting.Roots[len(m.accounting.Roots)-1]
		stage, err := os.Open(filepath.Join(r.OwnedRoot, r.Stage))
		if err != nil {
			t.Fatal(err)
		}
		names, readErr := stage.Readdirnames(-1)
		if !accountingPrivate(stage, nil) {
			t.Error("private stage lacks actual protected same-user DACL")
		}
		if err := stage.Close(); err != nil {
			t.Error(err)
		}
		if readErr != nil || len(names) != 1 || names[0] != receiveOwnerMarker {
			t.Fatalf("prior partial not removed before marker-only stage: %v %v", names, readErr)
		}
		if _, err := m.Cancel(id); err != nil {
			t.Fatal(err)
		}
		if err := m.Forget(id); err != nil {
			t.Fatal(err)
		}
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(r.OwnedRoot, r.Stage)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stage deletion not visible: %v", err)
		}
		if _, err := os.Lstat(file.Path + ".retirement"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("guard survived successful retirement: %v", err)
		}
		// Compare the actual control namespace, without assuming a combined
		// owned-writer implementation has only one permanent control file.
		entries := mustReadDir(t, filepath.Dir(file.Path))
		var namesNow []string
		for _, e := range entries {
			namesNow = append(namesNow, e.Name())
		}
		current := strings.Join(namesNow, "\n")
		if i == 0 {
			controlNames = current
		} else if current != controlNames {
			t.Fatalf("control history grew: before=%q after=%q", controlNames, current)
		}
		t.Logf("CHOSEN cycle=%d receive/cancel/Close controls=%q prior partial removed", i, current)
		for _, saved := range savedPaths {
			data, err := os.ReadFile(saved)
			if err != nil || string(data) != "saved marker bytes" {
				t.Fatalf("saved output/hardlink changed: %q %v", data, err)
			}
		}
		original, err := os.Stat(savedPaths[0])
		linked, linkErr := os.Stat(savedPaths[1])
		if err != nil || linkErr != nil || !os.SameFile(original, linked) {
			t.Fatal("saved hardlink identity lost", err, linkErr)
		}
	}
	reopened, _ := testManager(t, Options{AccountingStore: file, ExistingState: true})
	if reopened.ReceiveRecovery().State != "ready" {
		t.Fatalf("reopen: %+v", reopened.ReceiveRecovery())
	}
}

// The after-hash branch must retry the real destination barrier. These fixtures
// need no Windows open-directory rename and use the real file-backed store.
func TestWindowsRetirementPostSaveBarrierFailure(t *testing.T) {
	for _, boundary := range []string{"post-save", "release-callback"} {
		t.Run(boundary, func(t *testing.T) {
			file, before := intentFixture(t)
			if err := file.SaveReceiveAccounting(before); err != nil {
				t.Fatal(err)
			}
			store := &prepareFailureStore{FileReceiveAccountingStore: file}
			committed, unavailable, calls := false, true, 0
			store.onSave = func(next ReceiveAccounting) error {
				if err := file.SaveReceiveAccounting(next); err != nil {
					return err
				}
				if next.Preparation == nil {
					committed = true
				}
				return nil
			}
			interceptRetirementSync(t, func(root *os.Root) error {
				if committed && root.Name() == before.Preparation.Destination {
					calls++
					cut := 1
					if boundary == "release-callback" {
						cut = 2
					}
					if unavailable && calls >= cut {
						return errors.New("post-commit destination barrier uncertainty")
					}
				}
				return nil
			})
			open := func() *Manager {
				m, err := NewManager(Options{AccountingStore: store, ExistingState: true})
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			first := open()
			if !committed || first.ReceiveRecovery().State != "blocked" {
				t.Fatal("after-save barrier failure bypassed")
			}
			g := assertGuard(t, file)
			current, err := file.LoadReceiveAccounting()
			if err != nil || accountingHash(current) != g.AfterHash {
				t.Fatal("did not reach actual after-hash commit", err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := open()
			if reopened.ReceiveRecovery().State != "blocked" {
				t.Fatal("restart forgot real barrier obligation")
			}
			assertGuard(t, file)
			if view, err := reopened.ConfirmReceiveRecovery(context.Background(), true); !errors.Is(err, ErrReceiveRecovery) || view.Applied {
				t.Fatal("confirmation bypassed failed barrier", view, err)
			}
			unavailable = false
			if view, err := reopened.ConfirmReceiveRecovery(context.Background(), true); err != nil || !view.Applied || view.State != "ready" {
				t.Fatal("actual successful retry failed", view, err)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(file.Path + ".retirement"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("guard not released after actual successful barrier", err)
			}
			t.Logf("post-commit cut=%s actual barrier attempts=%d", boundary, calls)
		})
	}
}

func TestWindowsRetirementPostSaveGuardMutation(t *testing.T) {
	file, before := intentFixture(t)
	if err := file.SaveReceiveAccounting(before); err != nil {
		t.Fatal(err)
	}
	store := &prepareFailureStore{FileReceiveAccountingStore: file}
	mutated := false
	store.onSave = func(next ReceiveAccounting) error {
		if err := file.SaveReceiveAccounting(next); err != nil {
			return err
		}
		if next.Preparation == nil {
			// Mutate bytes in place, preserving the held identity and ACL. The
			// release byte check must observe this after a successful Save.
			if err := os.WriteFile(file.Path+".retirement", []byte("{corrupt}"), 0600); err != nil {
				t.Fatal(err)
			}
			mutated = true
		}
		return nil
	}
	m, err := NewManager(Options{AccountingStore: store, ExistingState: true})
	if err != nil {
		t.Fatal(err)
	}
	if !mutated || m.ReceiveRecovery().State != "blocked" {
		t.Fatal("pre-release guard validation did not reject mutation")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	store.onSave = nil
	reopened, err := NewManager(Options{AccountingStore: store, ExistingState: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.ReceiveRecovery().State != "blocked" {
		t.Fatal("restart accepted mutated guard")
	}
	if data, err := os.ReadFile(file.Path + ".retirement"); err != nil || string(data) != "{corrupt}" {
		t.Fatal("mutated blocking evidence disappeared", err)
	}
}

func TestWindowsRetirementPostSaveUnexpectedStageChild(t *testing.T) {
	dir, _, record := accountingFixture(t)
	file := FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")}
	store := &prepareFailureStore{FileReceiveAccountingStore: file}
	var unknown string
	store.onSave = func(next ReceiveAccounting) error {
		if err := file.SaveReceiveAccounting(next); err != nil {
			return err
		}
		if len(next.Roots) == 0 {
			stage := filepath.Join(record.OwnedRoot, record.Stage)
			if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("fixture did not reach deleted stage before Save", err)
			}
			if err := config.SecureDir(stage); err != nil {
				t.Fatal(err)
			}
			unknown = filepath.Join(stage, "unknown")
			if err := os.WriteFile(unknown, []byte("preserve unknown"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	m, err := NewManager(Options{AccountingStore: store, ExistingState: true})
	if err != nil {
		t.Fatal(err)
	}
	if unknown == "" || m.ReceiveRecovery().State != "blocked" {
		t.Fatal("post-Save unexpected stage child bypassed verification")
	}
	g := assertGuard(t, file)
	current, err := file.LoadReceiveAccounting()
	if err != nil || accountingHash(current) != g.AfterHash {
		t.Fatal("did not reach committed after-hash", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	store.onSave = nil
	reopened, err := NewManager(Options{AccountingStore: store, ExistingState: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.ReceiveRecovery().State != "blocked" {
		t.Fatal("reopen forgot unexpected stage child")
	}
	assertGuard(t, file)
	if data, err := os.ReadFile(unknown); err != nil || string(data) != "preserve unknown" {
		t.Fatal("unknown bytes changed", err)
	}
}
