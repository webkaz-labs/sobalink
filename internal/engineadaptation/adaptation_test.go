package engineadaptation

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureManifest() (map[string][]byte, Manifest) {
	tree := map[string][]byte{"go.mod": []byte("module example.org/fixture\n"), "original.go": []byte("before\n")}
	m := Manifest{Module: "example.org/fixture", Version: "v1.0.0", UpstreamTreeSHA256: treeHash(tree), GoModSum: moduleHash(map[string][]byte{"go.mod": tree["go.mod"]})}
	full := map[string][]byte{}
	for p, b := range tree {
		full[m.Module+"@"+m.Version+"/"+p] = b
	}
	m.ModuleSum = moduleHash(full)
	m.Changes = []Change{{Path: "original.go", OriginalSHA256: Hash(tree["original.go"]), AdaptedSHA256: Hash([]byte("after\n")), Edits: []Edit{{Offset: 0, Old: "before", New: "after"}}}, {Path: "new.go", AdaptedSHA256: Hash([]byte("new\n")), Edits: []Edit{{Offset: 0, New: "new\n"}}}}
	final := map[string][]byte{"go.mod": tree["go.mod"], "original.go": []byte("after\n"), "new.go": []byte("new\n")}
	m.AdaptedTreeSHA256 = treeHash(final)
	return tree, m
}
func TestExactAdaptation(t *testing.T) {
	tree, m := fixtureManifest()
	if err := apply(tree, m); err != nil {
		t.Fatal(err)
	}
	if string(tree["original.go"]) != "after\n" || string(tree["new.go"]) != "new\n" {
		t.Fatal("incorrect adaptation")
	}
	if err := apply(tree, m); err == nil {
		t.Fatal("double patch accepted")
	}
}
func TestAdaptationRejectsDrift(t *testing.T) {
	for _, kind := range []string{"original", "adapted", "tree", "duplicate", "path", "mod", "offset", "hunk", "added collision"} {
		t.Run(kind, func(t *testing.T) {
			tree, m := fixtureManifest()
			switch kind {
			case "original":
				tree["original.go"] = []byte("changed")
			case "adapted":
				m.Changes[0].AdaptedSHA256 = strings.Repeat("0", 64)
			case "tree":
				m.AdaptedTreeSHA256 = strings.Repeat("0", 64)
			case "duplicate":
				m.Changes = append(m.Changes, m.Changes[0])
			case "path":
				m.Changes[0].Path = "../escape"
			case "mod":
				m.Changes[0].Path = "go.mod"
			case "offset":
				m.Changes[0].Edits[0].Offset = -1
			case "hunk":
				m.Changes[0].Edits[0].Old = "BEFORE"
			case "added collision":
				tree["new.go"] = []byte("existing")
			}
			if err := apply(tree, m); err == nil {
				t.Fatal("accepted drift")
			}
		})
	}
}
func TestUpstreamZipVerifiedIndependently(t *testing.T) {
	tree, m := fixtureManifest()
	name := filepath.Join(t.TempDir(), "source.zip")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for p, b := range tree {
		w, err := z.Create(m.Module + "@" + m.Version + "/" + p)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(b)
	}
	z.Close()
	f.Close()
	got, err := readZip(name, m)
	if err != nil {
		t.Fatal(err)
	}
	if treeHash(got) != m.UpstreamTreeSHA256 {
		t.Fatal("incorrect zip")
	}
	m.ModuleSum = "h1:wrong"
	if _, err := readZip(name, m); err == nil {
		t.Fatal("accepted wrong archive checksum")
	}
}
func TestGeneratedTreeDriftAndLinks(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "source.go")
	if err := os.WriteFile(p, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := readTree(root)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte("drift"), 0o644)
	after, err := readTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if treeHash(before) == treeHash(after) {
		t.Fatal("drift missed")
	}
	if err := os.Symlink(p, filepath.Join(root, "link")); err != nil {
		t.Skip("symlink unavailable")
	}
	if _, err := readTree(root); err == nil {
		t.Fatal("accepted symlink")
	}
}
func TestModuleHashKnownFormat(t *testing.T) {
	// A hash of Go's canonical per-file digest list, not a raw zip-byte hash.
	tree := map[string][]byte{"go.mod": []byte("module fixture\n")}
	if got := moduleHash(tree); got != "h1:Imo/be5D7AjuFGHlTMc+U8cgnbry+LAgDWXabfa5Nk4=" {
		t.Fatalf("canonical fixture hash: %s", got)
	}
}

func TestCacheAndGeneratedTreesCannotOverlap(t *testing.T) {
	root := t.TempDir()
	for _, pair := range [][2]string{{root, root}, {root, filepath.Join(root, "child")}, {filepath.Join(root, "child"), root}} {
		if !overlaps(pair[0], pair[1]) {
			t.Fatal("overlap missed")
		}
	}
	if overlaps(filepath.Join(root, "cache"), filepath.Join(root, "cache-adapted")) {
		t.Fatal("separate tree rejected")
	}
}

func TestPinnedInputsRejectManifestAndChecksumChanges(t *testing.T) {
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"valid", "manifest", "checksum", "duplicate checksum", "replacement", "extra replacement", "missing replacement"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			os.MkdirAll(filepath.Join(root, SourceDirectory), 0o755)
			manifest := ManifestBytes()
			mod := "module fixture\n\nreplace " + Module + " " + Version + " => " + Replacement + "\n"
			sums := Module + " " + Version + " " + ModuleSum + "\n" + Module + " " + Version + "/go.mod " + GoModSum + "\n"
			switch kind {
			case "manifest":
				manifest = append(manifest, ' ')
			case "checksum":
				sums = strings.ReplaceAll(sums, ModuleSum, "h1:changed")
			case "duplicate checksum":
				sums += Module + " " + Version + " " + ModuleSum + "\n"
			case "replacement":
				mod = strings.ReplaceAll(mod, Replacement, "../elsewhere")
			case "extra replacement":
				mod += "replace example.org/other => ../other\n"
			case "missing replacement":
				mod = "module fixture\n"
			}
			for p, b := range map[string][]byte{SourceDirectory + "/manifest.json": manifest, "go.mod": []byte(mod), "go.sum": []byte(sums)} {
				if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(p)), b, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := ValidateInputs(root)
			if (err == nil) != (kind == "valid") {
				t.Fatalf("validation = %v", err)
			}
		})
	}
}
