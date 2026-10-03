package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cancelAfterSourceChecks struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *cancelAfterSourceChecks) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func sourceCheckContext(checks int) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	return &cancelAfterSourceChecks{Context: ctx, cancel: cancel, remaining: checks}
}

func TestSourcePlanningPrecedesPayloadReads(t *testing.T) {
	source := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(source, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanSources(context.Background(), "batch", []string{source}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	manifest := plan.Manifest()
	if len(manifest.Entries) != 1 || manifest.Entries[0].Size != 7 {
		t.Fatalf("incorrect admission metadata: %+v", manifest)
	}
	// Mutating an admission copy must not change the data subsequently hashed.
	manifest.Entries[0].Path = "changed.txt"
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if manifest, sources, err := plan.Hash(context.Background()); !errors.Is(err, os.ErrNotExist) || len(manifest.Entries) != 0 || len(sources) != 0 {
		t.Fatalf("hashing did not defer its payload read: %+v, %+v, %v", manifest, sources, err)
	}
	if plan.Manifest().Entries[0].Path != "note.txt" {
		t.Fatal("admission metadata mutated the source plan")
	}
}

func TestSourcePlanningAndHashingHonorCancellation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "selection")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("note-%d", i)), []byte("payload"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if plan, err := PlanSources(sourceCheckContext(5), "batch", []string{root}, Limits{}); !errors.Is(err, context.Canceled) || plan != nil {
		t.Fatalf("cancelled directory enumeration = %+v, %v", plan, err)
	}
	source := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(source, []byte(strings.Repeat("x", streamBufferBytes*2)), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanSources(context.Background(), "batch", []string{source}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if manifest, sources, err := plan.Hash(sourceCheckContext(4)); !errors.Is(err, context.Canceled) || len(manifest.Entries) != 0 || len(sources) != 0 {
		t.Fatalf("cancelled hashing returned partial data: %+v, %+v, %v", manifest, sources, err)
	}
	if err := os.WriteFile(source, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Hash(context.Background()); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("source changed after planning = %v", err)
	}
}

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
	if _, _, err := BuildManifest("batch", []string{first}, Limits{MaxManifestBytes: 1000}); !errors.Is(err, ErrMetadataLimit) || !errors.Is(err, ErrLimit) {
		t.Fatalf("source metadata budget error = %v", err)
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

func TestSourcePlanningUsesRaisedPathChoicesAndFiniteWalkBudget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "selection")
	leaf := filepath.Join(root, filepath.FromSlash(strings.Repeat("d/", 20)), "note")
	if err := os.MkdirAll(filepath.Dir(leaf), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leaf, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanSources(context.Background(), "default", []string{root}, Limits{}); !errors.Is(err, ErrLimit) {
		t.Fatalf("default depth error = %v", err)
	}
	lim := Limits{MaxDepth: 32, MaxPathBytes: 8192}
	plan, err := PlanSources(context.Background(), "raised", []string{root}, lim)
	if err != nil {
		t.Fatalf("source path limit remained clamped: %v", err)
	}
	manifest, sources, err := plan.Hash(context.Background())
	if err != nil || len(sources) != 1 || manifest.Entries[0].Path != "selection/"+strings.Repeat("d/", 20)+"note" {
		t.Fatalf("raised source hashing = %+v, %+v, %v", manifest, sources, err)
	}
	// The final manifest fits, but the active traversal also needs bounded
	// metadata for ancestor paths, handles and directory-reader frames.
	lim.MaxManifestBytes = metadataSize(manifest) + 1024
	if _, err := PlanSources(context.Background(), "bounded", []string{root}, lim); !errors.Is(err, ErrMetadataLimit) {
		t.Fatalf("source traversal ignored metadata budget: %v", err)
	}
}
