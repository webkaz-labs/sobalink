package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRetirementSaveErrorAfterCommitKeepsMissingRootEvidenceOnRestart(t *testing.T) {
	file, state := intentFixture(t)
	if err := file.SaveReceiveAccounting(state); err != nil {
		t.Fatal(err)
	}
	store := &prepareFailureStore{FileReceiveAccountingStore: file}
	wroteUnknownRoot := false
	store.onSave = func(next ReceiveAccounting) error {
		if next.Preparation == nil {
			root := filepath.Join(state.Preparation.Destination, state.Preparation.Root)
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "unknown"), []byte("preserve me"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := file.SaveReceiveAccounting(next); err != nil {
				t.Fatal(err)
			}
			wroteUnknownRoot = true
			return errors.New("retirement response lost after commit")
		}
		return file.SaveReceiveAccounting(next)
	}

	m, err := NewManager(Options{AccountingStore: store, ExistingState: true})
	if err != nil {
		t.Fatal(err)
	}
	if !wroteUnknownRoot || m.ReceiveRecovery().State != "blocked" {
		t.Fatal("uncertain retirement did not leave the manager blocked after creating the unknown root")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	store.onSave = nil

	reopened, err := NewManager(Options{AccountingStore: store, ExistingState: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.ReceiveRecovery().State != "blocked" {
		t.Fatal("restart became ready after an uncertain retirement erased its only durable evidence")
	}
	if _, err := os.Stat(filepath.Join(state.Preparation.Destination, state.Preparation.Root, "unknown")); err != nil {
		t.Fatalf("unknown root data was not preserved: %v", err)
	}
}

func TestAcceptFinalRetirementSaveRejectsBindingSubstitution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory rename")
	}
	for _, target := range []string{"root", "destination", "stage", "marker"} {
		t.Run(target, func(t *testing.T) {
			store := &prepareFailureStore{FileReceiveAccountingStore: FileReceiveAccountingStore{Path: filepath.Join(t.TempDir(), "index.json")}}
			m, peer := testManager(t, Options{AccountingStore: store})
			destination := t.TempDir()
			if _, err := m.Offer(peer, testManifest("retire-"+target, testEntry("file", "file", "payload"))); err != nil {
				t.Fatal(err)
			}
			substituted := false
			unknownPath := ""
			store.onSave = func(next ReceiveAccounting) error {
				if next.Preparation != nil || len(next.Roots) == 0 || substituted {
					return store.FileReceiveAccountingStore.SaveReceiveAccounting(next)
				}
				if err := store.FileReceiveAccountingStore.SaveReceiveAccounting(next); err != nil {
					return err
				}
				record := next.Roots[len(next.Roots)-1]
				unknownPath = record.OwnedRoot
				if target == "destination" {
					unknownPath = record.Destination
				}
				if target == "stage" || target == "marker" {
					unknownPath = filepath.Join(record.OwnedRoot, record.Stage)
				}
				movedPath := unknownPath + "-original"
				if err := os.Rename(unknownPath, movedPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(unknownPath, 0700); err != nil {
					t.Fatal(err)
				}
				if target == "marker" {
					if err := os.WriteFile(filepath.Join(unknownPath, receiveOwnerMarker), []byte("replacement marker"), 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(filepath.Join(unknownPath, "unknown"), []byte("preserve me"), 0600); err != nil {
					t.Fatal(err)
				}
				substituted = true
				return nil
			}

			batch, acceptErr := m.Accept("retire-"+target, destination)
			if !substituted {
				t.Fatal("fixture did not substitute the binding during final retirement save")
			}
			if !errors.Is(acceptErr, ErrReceiveRecovery) || batch.State != Accepted {
				t.Fatalf("final retirement accepted a substituted binding: batch=%+v err=%v", batch, acceptErr)
			}
			if m.ReceiveRecovery().State != "blocked" {
				t.Fatal("runtime did not block after final retirement binding changed")
			}
			if _, err := m.Offer(peer, testManifest("after-substitution", testEntry("next", "next", "x"))); !errors.Is(err, ErrReceiveRecovery) {
				t.Fatalf("runtime admitted another offer after substitution: %v", err)
			}
			if _, err := m.ReceiveFile(context.Background(), peer, "retire-"+target, "file", strings.NewReader("payload")); !errors.Is(err, ErrReceiveRecovery) {
				t.Fatalf("runtime received into a substituted binding: %v", err)
			}
			if err := m.Close(); err != nil {
				t.Fatalf("normal Close failed: %v", err)
			}
			store.onSave = nil
			reopened, err := NewManager(Options{AccountingStore: store, ExistingState: true})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if reopened.ReceiveRecovery().State != "blocked" {
				t.Fatal("restart did not retain a block for the substituted binding")
			}
			foreign := filepath.Join(unknownPath, "unknown")
			want := "preserve me"
			if target == "marker" {
				foreign = filepath.Join(unknownPath, receiveOwnerMarker)
				want = "replacement marker"
			}
			if data, err := os.ReadFile(foreign); err != nil || string(data) != want {
				t.Fatalf("unknown data changed or disappeared: %q %v", data, err)
			}
		})
	}
}
