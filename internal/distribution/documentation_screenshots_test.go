package distribution

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstalledScreenshotsAreExactClosedSetForAllTargets(t *testing.T) {
	if len(documentationScreenshots) != 30 {
		t.Fatal("review screenshot inventory changes explicitly")
	}
	seen := map[string]bool{}
	for _, rel := range documentationScreenshots {
		if seen[rel] || filepath.ToSlash(filepath.Dir(rel)) != "docs/assets/screenshots" || filepath.Ext(rel) != ".png" {
			t.Fatalf("invalid screenshot inventory path: %s", rel)
		}
		seen[rel] = true
	}
	for _, target := range Targets {
		t.Run(target.String(), func(t *testing.T) {
			tool, _ := mockTool(t)
			for _, rel := range documentationScreenshots {
				writeFixture(t, filepath.Join(tool.Root, filepath.FromSlash(rel)), string(readFixture(t, filepath.Join("..", "..", filepath.FromSlash(rel)))))
			}
			writeFixture(t, filepath.Join(tool.Root, "docs/assets/visuals/guide.svg"), "<svg/>")
			for _, rel := range []string{"docs/assets/screenshots/unreviewed.png", "docs/assets/screenshots/nested/preview.png", "docs/assets/screenshots/capture.json", "docs/assets/screenshots/build.py", "docs/assets/visuals/render-preview.png"} {
				writeFixture(t, filepath.Join(tool.Root, filepath.FromSlash(rel)), "excluded synthetic fixture")
			}
			if err := tool.Build("0.0.0-dev.1", target, strings.Repeat("a", 40), io.Discard); err != nil {
				t.Fatal(err)
			}
			entries := readArchiveFixture(t, filepath.Join(tool.Root, "dist", stem("0.0.0-dev.1", target)+target.Extension()))
			for _, rel := range documentationScreenshots {
				if !bytes.Equal(entries["share/sobalink/"+rel], readFixture(t, filepath.Join(tool.Root, filepath.FromSlash(rel)))) {
					t.Fatalf("missing or changed screenshot: %s", rel)
				}
			}
			if string(entries["share/sobalink/docs/assets/visuals/guide.svg"]) != "<svg/>" {
				t.Fatal("SVG no longer shipped")
			}
			for name := range entries {
				if strings.HasPrefix(name, "share/sobalink/docs/assets/") && name != "share/sobalink/docs/assets/visuals/guide.svg" && !seen[strings.TrimPrefix(name, "share/sobalink/")] {
					t.Fatalf("unexpected asset: %s", name)
				}
			}
		})
	}
}

func TestDocumentationScreenshotsRejectMissingEmptyAndNonregular(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "directory", "file symlink", "directory symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			for _, rel := range documentationScreenshots {
				writeFixture(t, filepath.Join(root, filepath.FromSlash(rel)), "fixture")
			}
			first := filepath.Join(root, filepath.FromSlash(documentationScreenshots[0]))
			if err := os.Remove(first); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "empty":
				writeFixture(t, first, "")
			case "directory":
				if err := os.Mkdir(first, 0755); err != nil {
					t.Fatal(err)
				}
			case "file symlink":
				target := filepath.Join(t.TempDir(), "outside.png")
				writeFixture(t, target, "outside fixture")
				if err := os.Symlink(target, first); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			case "directory symlink":
				dir := filepath.Join(root, "docs/assets/screenshots")
				target := filepath.Join(t.TempDir(), "captures")
				if err := os.Rename(dir, target); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, filepath.Join(target, filepath.Base(first)), "outside fixture")
				if err := os.Symlink(target, dir); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			if err := copyDocumentationScreenshots(root, t.TempDir()); err == nil {
				t.Fatalf("accepted %s", kind)
			}
		})
	}
}
