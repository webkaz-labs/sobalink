package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagingInventoryStopsAtSelectedWorkBudgets(t *testing.T) {
	t.Run("entries", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"a", "b", "c"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		bytes, entries, err := inspectStaging(root, 2, 8)
		if err == nil || !strings.Contains(err.Error(), "entry budget") || entries != 2 || bytes != 8 {
			t.Fatalf("work was not bounded: bytes=%d entries=%d err=%v", bytes, entries, err)
		}
		for _, name := range []string{"a", "b", "c"} {
			if body, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(body) != "keep" {
				t.Fatal("inventory changed retained payload")
			}
		}
	})
	t.Run("depth", func(t *testing.T) {
		root := t.TempDir()
		deep := filepath.Join(root, "a", "b", "c", "d")
		if err := os.MkdirAll(deep, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(deep, "retained")
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		_, entries, err := inspectStaging(root, 100, 2)
		if err == nil || !strings.Contains(err.Error(), "depth budget") || entries != 3 {
			t.Fatalf("depth was not bounded: entries=%d err=%v", entries, err)
		}
		bytes, entries, err := inspectStaging(root, 100, 8)
		if err != nil || bytes != 4 || entries != 5 {
			t.Fatalf("raised budget did not recover inventory: %d %d %v", bytes, entries, err)
		}
		if body, err := os.ReadFile(path); err != nil || string(body) != "keep" {
			t.Fatal("inspection limit deleted retained data")
		}
	})
}
