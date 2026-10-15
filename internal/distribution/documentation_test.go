package distribution

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstalledDocumentationVisualsForAllTargets(t *testing.T) {
	for _, target := range Targets {
		t.Run(target.String(), func(t *testing.T) {
			tool, _ := mockTool(t)
			const rel = "docs/assets/visuals/guide.svg"
			const svg = `<svg xmlns="http://www.w3.org/2000/svg"><title>Guide</title></svg>`
			writeFixture(t, filepath.Join(tool.Root, filepath.FromSlash(rel)), svg)
			writeFixture(t, filepath.Join(tool.Root, "docs", "GENERIC.en.md"), `<picture><source srcset="assets/visuals/guide.svg"><img src="assets/visuals/guide.svg" alt="Guide"></picture>`)
			for _, excluded := range []string{"build.py", "README.md", "preview.png", "nested/unused.svg"} {
				writeFixture(t, filepath.Join(tool.Root, "docs", "assets", "visuals", filepath.FromSlash(excluded)), "not a distribution input")
			}
			version := "0.0.0-dev.1"
			if err := tool.Build(version, target, strings.Repeat("a", 40), io.Discard); err != nil {
				t.Fatal(err)
			}
			entries := readArchiveFixture(t, filepath.Join(tool.Root, "dist", stem(version, target)+target.Extension()))
			if !bytes.Equal(entries["share/sobalink/"+rel], []byte(svg)) {
				t.Fatal("missing or changed offline SVG")
			}
			for path := range entries {
				if strings.HasPrefix(path, "share/sobalink/docs/assets/visuals/") && path != "share/sobalink/"+rel {
					t.Fatalf("unexpected documentation asset: %s", path)
				}
			}
		})
	}
}

func TestDocumentationVisualsRejectLinksAndSpecialFiles(t *testing.T) {
	for _, linked := range []string{"docs", "docs/assets", "docs/assets/visuals", "docs/assets/visuals/guide.svg"} {
		t.Run(linked, func(t *testing.T) {
			root, outside, share := t.TempDir(), t.TempDir(), t.TempDir()
			suffix := strings.TrimPrefix("docs/assets/visuals/guide.svg", linked)
			target := filepath.Join(outside, "target")
			writeFixture(t, target+filepath.FromSlash(suffix), "must not package linked bytes")
			link := filepath.Join(root, filepath.FromSlash(linked))
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlink not supported: %v", err)
			}
			if err := copyDocumentationVisuals(root, share); err == nil {
				t.Fatal("accepted linked documentation visual")
			}
		})
	}
	t.Run("directory with SVG extension", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "docs", "assets", "visuals", "guide.svg"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := copyDocumentationVisuals(root, t.TempDir()); err == nil {
			t.Fatal("accepted non-regular SVG")
		}
	})
}

func TestRepositoryDocumentationVisualsRemainByteExact(t *testing.T) {
	root := filepath.Join("..", "..")
	files, err := filepath.Glob(filepath.Join(root, "docs", "assets", "visuals", "*.svg"))
	if err != nil || len(files) == 0 {
		t.Fatalf("missing repository visuals: %v", err)
	}
	share := t.TempDir()
	if err := copyDocumentationVisuals(root, share); err != nil {
		t.Fatal(err)
	}
	for _, source := range files {
		actual := readFixture(t, filepath.Join(share, "docs", "assets", "visuals", filepath.Base(source)))
		if !bytes.Equal(actual, readFixture(t, source)) {
			t.Fatalf("SVG changed: %s", source)
		}
	}
	t.Logf("verified %d repository SVGs", len(files))
}
