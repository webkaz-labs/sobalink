package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// Diagnostic classes only. Never format the underlying OS error, which can
// contain a destination, entry name or another private value.
func outboxInventoryErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, os.ErrPermission):
		return "permission"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	case errors.Is(err, os.ErrExist):
		return "exists"
	default:
		return "other"
	}
}

func outboxInventoryCount(count int) string {
	if count == 0 {
		return "zero"
	}
	if count == 1 {
		return "one"
	}
	return "multiple"
}

func TestOutboxSavedInventoryReadDiagnostics(t *testing.T) {
	const stage = ".sobalink-durable-stage-v1"
	root := t.TempDir()
	if os.WriteFile(filepath.Join(root, "reviewed"), []byte("saved"), 0600) != nil || os.Mkdir(filepath.Join(root, stage), 0700) != nil {
		t.Fatal("synthetic inventory fixture unavailable")
	}
	expected := transfer.Entry{Path: "reviewed", Kind: transfer.File, Size: 5}
	for _, target := range []string{"root", "stage"} {
		for _, tc := range []struct {
			name, class string
			err         error
		}{
			{"permission", "permission", os.ErrPermission},
			{"missing", "not_found", os.ErrNotExist},
			{"unknown", "other", errors.New("synthetic-private-detail")},
		} {
			t.Run(target+"/"+tc.name, func(t *testing.T) {
				calls := 0
				output, err := outboxSavedInventoryReadDir(root, expected, func(path string) ([]os.DirEntry, error) {
					calls++
					if target == "root" && path == root || target == "stage" && path == filepath.Join(root, stage) {
						return nil, &os.PathError{Op: "readdir", Path: "synthetic-private-path", Err: tc.err}
					}
					return os.ReadDir(path)
				})
				// Preserve the exact existing maintenance-race exception, only for
				// a stage that vanished. No other read error becomes success.
				if target == "stage" && tc.name == "missing" {
					if err != nil || output != filepath.Join(root, expected.Path) || calls != 2 {
						t.Fatal("vanished optional stage changed inventory acceptance")
					}
					return
				}
				if err == nil || output != "" || !strings.Contains(err.Error(), "read failed: class="+tc.class) || strings.Contains(err.Error(), "retained unexpected entries") || strings.Contains(err.Error(), "synthetic-private") {
					t.Fatal("inventory read failure was misclassified or exposed private values")
				}
				wantCalls := 1
				if target == "stage" {
					wantCalls = 2
				}
				if calls != wantCalls {
					t.Fatal("diagnostics retried filesystem inspection")
				}
			})
		}
	}
	if os.WriteFile(filepath.Join(root, stage, "synthetic-private-name"), nil, 0600) != nil {
		t.Fatal("synthetic stage entry unavailable")
	}
	if _, err := outboxSavedInventory(root, expected); err == nil || err.Error() != "completed durable stage retained unexpected entries: count=one" {
		t.Fatal("nonempty stage was accepted, misclassified or exposed an entry name")
	}
}

func TestOutboxInventoryDiagnosticClasses(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{{nil, "none"}, {os.ErrPermission, "permission"}, {os.ErrNotExist, "not_found"}, {os.ErrExist, "exists"}, {errors.New("synthetic-private"), "other"}} {
		if outboxInventoryErrorClass(tc.err) != tc.want {
			t.Fatal("inventory error did not use a fixed class")
		}
	}
	for count, want := range map[int]string{0: "zero", 1: "one", 2: "multiple", 1000000: "multiple"} {
		if outboxInventoryCount(count) != want {
			t.Fatal("inventory count escaped bounded categories")
		}
	}
}
