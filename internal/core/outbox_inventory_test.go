package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func assertOutboxSavedInventory(t *testing.T, root string, expected transfer.Entry) string {
	t.Helper()
	output, err := outboxSavedInventory(root, expected)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

// Completed receives can still own one empty private stage until maintenance
// succeeds (directory-only /start does not run file-receive cleanup). Its exact
// reserved name is allowed, never arbitrary dotfiles or extra user outputs.
func outboxSavedInventory(root string, expected transfer.Entry) (string, error) {
	const stageName = ".sobalink-durable-stage-v1"
	if expected.Path == "" || filepath.Base(expected.Path) != expected.Path || expected.Path == stageName {
		return "", errors.New("fixture expected output is not one reviewed top-level entry")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("completed batch root is not a real directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) < 1 || len(entries) > 2 {
		return "", errors.New("missing or duplicate actual saved outputs")
	}
	found := false
	for _, entry := range entries {
		info, err := entry.Info()
		// The one optional stage may be removed by maintenance between the
		// directory listing and inspection. No reviewed output gets this exception.
		if entry.Name() == stageName && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("saved inventory contains an unreadable or symbolic entry")
		}
		switch entry.Name() {
		case expected.Path:
			found = true
			if expected.Kind == transfer.Directory {
				if !info.IsDir() {
					return "", errors.New("reviewed directory output has the wrong type")
				}
			} else if expected.Kind != transfer.File || !info.Mode().IsRegular() || info.Size() != expected.Size {
				return "", errors.New("reviewed file output has the wrong type or size")
			}
		case stageName:
			if !info.IsDir() {
				return "", errors.New("reserved durable stage is not a real directory")
			}
			staged, err := os.ReadDir(filepath.Join(root, stageName))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || len(staged) != 0 {
				return "", errors.New("completed durable stage retained unexpected entries")
			}
		default:
			return "", errors.New("completed batch retained an unreviewed output")
		}
	}
	if !found {
		return "", errors.New("reviewed output is missing from completed batch")
	}
	return filepath.Join(root, expected.Path), nil
}

func TestOutboxSavedInventory(t *testing.T) {
	const stage = ".sobalink-durable-stage-v1"
	write := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mkdir := func(t *testing.T, path string) {
		t.Helper()
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	remove := func(t *testing.T, path string) {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name   string
		kind   transfer.Kind
		valid  bool
		mutate func(*testing.T, string)
	}{
		{name: "file", kind: transfer.File, valid: true},
		{name: "directory", kind: transfer.Directory, valid: true},
		{name: "file_with_exact_empty_stage", kind: transfer.File, valid: true, mutate: func(t *testing.T, root string) { mkdir(t, filepath.Join(root, stage)) }},
		{name: "directory_with_exact_empty_stage", kind: transfer.Directory, valid: true, mutate: func(t *testing.T, root string) { mkdir(t, filepath.Join(root, stage)) }},
		{name: "missing_output", kind: transfer.File, mutate: func(t *testing.T, root string) { remove(t, filepath.Join(root, "reviewed")) }},
		{name: "only_reserved_stage", kind: transfer.File, mutate: func(t *testing.T, root string) {
			remove(t, filepath.Join(root, "reviewed"))
			mkdir(t, filepath.Join(root, stage))
		}},
		{name: "unreviewed_output", kind: transfer.File, mutate: func(t *testing.T, root string) { write(t, filepath.Join(root, "another"), "saved") }},
		{name: "arbitrary_dotfile", kind: transfer.File, mutate: func(t *testing.T, root string) { write(t, filepath.Join(root, ".unreviewed"), "") }},
		{name: "duplicate_with_stage", kind: transfer.File, mutate: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, stage))
			write(t, filepath.Join(root, "reviewed-copy"), "saved")
		}},
		{name: "stage_is_file", kind: transfer.File, mutate: func(t *testing.T, root string) { write(t, filepath.Join(root, stage), "") }},
		{name: "stage_not_empty", kind: transfer.File, mutate: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, stage))
			write(t, filepath.Join(root, stage, "unexpected"), "")
		}},
		{name: "file_is_directory", kind: transfer.File, mutate: func(t *testing.T, root string) {
			remove(t, filepath.Join(root, "reviewed"))
			mkdir(t, filepath.Join(root, "reviewed"))
		}},
		{name: "directory_is_file", kind: transfer.Directory, mutate: func(t *testing.T, root string) {
			remove(t, filepath.Join(root, "reviewed"))
			write(t, filepath.Join(root, "reviewed"), "")
		}},
		{name: "file_wrong_size", kind: transfer.File, mutate: func(t *testing.T, root string) { write(t, filepath.Join(root, "reviewed"), "changed-size") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			expected := transfer.Entry{Path: "reviewed", Kind: tc.kind}
			if tc.kind == transfer.File {
				expected.Size = 5
				write(t, filepath.Join(root, "reviewed"), "saved")
			} else {
				mkdir(t, filepath.Join(root, "reviewed"))
			}
			if tc.mutate != nil {
				tc.mutate(t, root)
			}
			output, err := outboxSavedInventory(root, expected)
			if (err == nil) != tc.valid {
				t.Fatalf("inventory validity = %v, want %v", err == nil, tc.valid)
			}
			if tc.valid && output != filepath.Join(root, expected.Path) {
				t.Fatal("inventory retargeted reviewed output")
			}
		})
	}
}

func TestOutboxSavedInventoryRejectsSymlinks(t *testing.T) {
	for _, target := range []string{"root", "output_file", "output_directory", "stage"} {
		t.Run(target, func(t *testing.T) {
			root, external := t.TempDir(), t.TempDir()
			expected := transfer.Entry{Path: "reviewed", Kind: transfer.File, Size: 5}
			if err := os.WriteFile(filepath.Join(root, "reviewed"), []byte("saved"), 0600); err != nil {
				t.Fatal(err)
			}
			link, destination := filepath.Join(root, ".sobalink-durable-stage-v1"), external
			switch target {
			case "root":
				link = filepath.Join(t.TempDir(), "linked-root")
				destination = root
				root = link
			case "output_file", "output_directory":
				link = filepath.Join(root, "reviewed")
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				if target == "output_file" {
					destination = filepath.Join(external, "original")
					if err := os.WriteFile(destination, []byte("saved"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					expected.Kind = transfer.Directory
					expected.Size = 0
				}
			}
			if err := os.Symlink(destination, link); err != nil {
				t.Skip("symlink unavailable")
			}
			if _, err := outboxSavedInventory(root, expected); err == nil {
				t.Fatal("symbolic inventory entry accepted")
			}
		})
	}
}
