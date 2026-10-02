package transfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildManifestTraversesDirectoriesAndKeepsSourcePathsPrivate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "selection")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	// Cross the directory page boundary and preserve executable sources only as data.
	for i := 0; i < 130; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%03d", i)), []byte("payload"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	manifest, sources, err := BuildManifest("batch", []string{root}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 131 || len(sources) != 130 {
		t.Fatalf("entries=%d sources=%d", len(manifest.Entries), len(sources))
	}
	if err := ValidateManifest(manifest, Limits{}); err != nil {
		t.Fatal(err)
	}
	byID := map[string]Entry{}
	for _, entry := range manifest.Entries {
		byID[entry.ID] = entry
	}
	for _, source := range sources {
		entry, ok := byID[source.EntryID]
		if !ok || entry.Kind != File || entry.SHA256 != testEntry("unused", "unused", "payload").SHA256 {
			t.Fatalf("source and hashed entry differ: %+v, %+v", source, entry)
		}
		if !filepath.IsAbs(source.LocalPath) {
			t.Fatalf("source is not local absolute path: %+v", source)
		}
	}
	wire, err := json.Marshal(struct {
		Manifest Manifest
		Sources  []Source
	}{manifest, sources})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), root) || strings.Contains(string(wire), "LocalPath") {
		t.Fatalf("wire encoding disclosed local source path: %s", wire)
	}
	if _, _, err = BuildManifest("bounded", []string{root}, Limits{MaxEntries: 128}); !errors.Is(err, ErrLimit) {
		t.Fatalf("source enumeration bound = %v", err)
	}
}

func TestBuildManifestRejectsSelectedAndNestedSymlinks(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "selected"
		if nested {
			name = "nested"
		}
		t.Run(name, func(t *testing.T) {
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("private bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			link := filepath.Join(root, "link")
			testSymlink(t, outside, link)
			selected := link
			if nested {
				selected = root
			}
			manifest, sources, err := BuildManifest("batch", []string{selected}, Limits{})
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("symlink source = %v", err)
			}
			if len(manifest.Entries) != 0 || len(sources) != 0 {
				t.Fatal("failed selection returned partial source data")
			}
		})
	}
}

func TestBuildManifestRejectsCollidingSelectionsAndBounds(t *testing.T) {
	first, second := filepath.Join(t.TempDir(), "same.txt"), filepath.Join(t.TempDir(), "same.txt")
	for _, name := range []string{first, second} {
		if err := os.WriteFile(name, []byte("payload"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := BuildManifest("batch", []string{first, second}, Limits{}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("colliding selections = %v", err)
	}
	if _, _, err := BuildManifest("batch", []string{first}, Limits{MaxFileBytes: 6}); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversized source = %v", err)
	}
	if _, _, err := BuildManifest("batch", nil, Limits{}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("empty selection = %v", err)
	}
	root := filepath.Join(t.TempDir(), "selection")
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := BuildManifest("batch", []string{root}, Limits{MaxDepth: 2}); !errors.Is(err, ErrLimit) {
		t.Fatalf("deep source = %v", err)
	}
}
