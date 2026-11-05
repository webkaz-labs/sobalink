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

func TestInstalledHistoricalGuidesKeepRelativeLinksForAllTargets(t *testing.T) {
	guides := map[string]string{
		"docs/GUIDE.en.md":         "[History](history/GUIDE.en.md)",
		"docs/GUIDE.ja.md":         "[履歴](history/GUIDE.ja.md)",
		"docs/history/GUIDE.en.md": "[日本語](GUIDE.ja.md) · [Current](../GUIDE.en.md) · [API](../../web/API.md)",
		"docs/history/GUIDE.ja.md": "[English](GUIDE.en.md) · [現在](../GUIDE.ja.md) · [API](../../web/API.md)",
		"web/API.md":               "[History](../docs/history/GUIDE.en.md)",
	}
	for _, target := range Targets {
		t.Run(target.String(), func(t *testing.T) {
			tool, _ := mockTool(t)
			for rel, body := range guides {
				writeFixture(t, filepath.Join(tool.Root, filepath.FromSlash(rel)), body)
			}
			for _, rel := range []string{"build.py", "session.json", "preview.png", "nested/unreviewed.md", ".cache/cached.md"} {
				writeFixture(t, filepath.Join(tool.Root, "docs", "history", filepath.FromSlash(rel)), "not a distribution input")
			}
			version := "0.0.0-dev.1"
			if err := tool.Build(version, target, strings.Repeat("a", 40), io.Discard); err != nil {
				t.Fatal(err)
			}
			entries := readArchiveFixture(t, filepath.Join(tool.Root, "dist", stem(version, target)+target.Extension()))
			for rel, body := range guides {
				if !bytes.Equal(entries["share/sobalink/"+rel], []byte(body)) {
					t.Fatalf("missing or changed offline guide: %s", rel)
				}
			}
			for path := range entries {
				if strings.HasPrefix(path, "share/sobalink/docs/history/") {
					if _, ok := guides[strings.TrimPrefix(path, "share/sobalink/")]; !ok {
						t.Fatalf("unexpected historical documentation input: %s", path)
					}
				}
			}
		})
	}
}

func TestDocumentationMarkdownWithoutHistory(t *testing.T) {
	root, share := t.TempDir(), t.TempDir()
	for _, rel := range []string{"docs/GUIDE.md", "web/API.md"} {
		writeFixture(t, filepath.Join(root, filepath.FromSlash(rel)), rel)
	}
	if err := copyDocumentationMarkdown(root, share); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"docs/GUIDE.md", "web/API.md"} {
		if string(readFixture(t, filepath.Join(share, filepath.FromSlash(rel)))) != rel {
			t.Fatalf("missing or changed guide: %s", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(share, "docs", "history")); !os.IsNotExist(err) {
		t.Fatalf("unexpected history directory: %v", err)
	}
}

func TestDocumentationMarkdownRejectLinksAndSpecialFiles(t *testing.T) {
	for _, linked := range []string{"docs", "docs/history", "docs/history/GUIDE.md", "docs/GUIDE.md", "web", "web/API.md"} {
		t.Run(linked, func(t *testing.T) {
			root, outside, share := t.TempDir(), t.TempDir(), t.TempDir()
			rel := "docs/history/GUIDE.md"
			if linked == "docs/GUIDE.md" {
				rel = linked
			} else if strings.HasPrefix(linked, "web") {
				rel = "web/API.md"
			}
			target := filepath.Join(outside, "target")
			writeFixture(t, target+filepath.FromSlash(strings.TrimPrefix(rel, linked)), "must not package linked bytes")
			link := filepath.Join(root, filepath.FromSlash(linked))
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlink not supported: %v", err)
			}
			if err := copyDocumentationMarkdown(root, share); err == nil {
				t.Fatal("accepted linked Markdown input")
			}
		})
	}
	t.Run("directory with Markdown extension", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "docs", "history", "GUIDE.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := copyDocumentationMarkdown(root, t.TempDir()); err == nil {
			t.Fatal("accepted non-regular Markdown input")
		}
	})
}

func TestRepositoryDocumentationMarkdownRemainByteExact(t *testing.T) {
	root := filepath.Join("..", "..")
	share := t.TempDir()
	if err := copyDocumentationMarkdown(root, share); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, directory := range []string{"docs", "docs/history", "web"} {
		files, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(directory), "*.md"))
		if err != nil || len(files) == 0 {
			t.Fatalf("missing repository Markdown in %s: %v", directory, err)
		}
		for _, source := range files {
			actual := readFixture(t, filepath.Join(share, filepath.FromSlash(directory), filepath.Base(source)))
			if !bytes.Equal(actual, readFixture(t, source)) {
				t.Fatalf("Markdown changed: %s", source)
			}
			count++
		}
	}
	t.Logf("verified %d repository Markdown files", count)
}
