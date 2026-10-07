package lifecycleadaptation

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/engineadaptation"
)

// These fixtures exercise input acceptance without materializing dependencies,
// invoking Go/downloads, or executing transport code.
func inputFixture(t *testing.T, dependency Dependency) string {
	t.Helper()
	root := t.TempDir()
	write := func(path string, data []byte) {
		t.Helper()
		path = filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var mod, sums strings.Builder
	mod.WriteString("module fixture\n")
	for _, pin := range engineadaptation.ModulePins() {
		mod.WriteString("require " + pin.Module + " " + pin.Version + "\n")
		mod.WriteString("replace " + pin.Module + " " + pin.Version + " => " + pin.Replacement + "\n")
		sums.WriteString(pin.Module + " " + pin.Version + " " + pin.ModuleSum + "\n" + pin.Module + " " + pin.Version + "/go.mod " + pin.GoModSum + "\n")
	}
	write("go.mod", []byte(mod.String()))
	write("go.sum", []byte(sums.String()))
	write(engineadaptation.SourceDirectory+"/manifest.json", engineadaptation.ManifestBytes())
	paths, err := SourceInputs(dependency)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		write(path, raw)
	}
	return root
}

func TestLifecycleInputsRejectChangedRecipesAndSelections(t *testing.T) {
	for _, dependency := range Dependencies() {
		for _, mutation := range []string{"valid", "overlay", "missing", "extra", "manifest", "replacement", "extra replacement", "requirement", "duplicate requirement", "checksum"} {
			t.Run(string(dependency)+"/"+mutation, func(t *testing.T) {
				root := inputFixture(t, dependency)
				m, err := Load(dependency)
				if err != nil {
					t.Fatal(err)
				}
				overlay := filepath.Join(root, SourceDirectory, filepath.FromSlash(m.Changes[0].SourcePath))
				path, replacement := "", ""
				switch mutation {
				case "overlay":
					path, replacement = overlay, "changed overlay"
				case "missing":
					if err := os.Remove(overlay); err != nil {
						t.Fatal(err)
					}
				case "extra":
					path, replacement = filepath.Join(filepath.Dir(overlay), "unlisted.go.txt"), "extra source"
				case "manifest":
					path, replacement = filepath.Join(root, SourceDirectory, string(dependency)+".json"), "{}"
				case "replacement", "extra replacement", "requirement", "duplicate requirement":
					path = filepath.Join(root, "go.mod")
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					replacement = string(data)
					switch mutation {
					case "replacement":
						replacement = strings.ReplaceAll(replacement, "./"+Directory(dependency), "../unreviewed")
					case "extra replacement":
						replacement += "replace example.invalid/extra v1.0.0 => ../extra\n"
					case "requirement":
						replacement = strings.ReplaceAll(replacement, "require "+m.Module+" "+m.Version, "require "+m.Module+" v0.0.1")
					case "duplicate requirement":
						replacement += "require " + m.Module + " " + m.Version + "\n"
					}
				case "checksum":
					path, replacement = filepath.Join(root, "go.sum"), "missing original checksums\n"
				}
				if path != "" {
					if err := os.WriteFile(path, []byte(replacement), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				_, err = ValidateInputs(root, dependency)
				if (err == nil) != (mutation == "valid") {
					t.Fatalf("acceptance = %v", err)
				}
			})
		}
	}
}

func TestLifecycleInputsRejectLinksAndLicenseDrift(t *testing.T) {
	root := inputFixture(t, GVisor)
	license := filepath.Join(root, SourceDirectory, "licenses", "gvisor", "LICENSE")
	if err := os.WriteFile(license, []byte("changed notice"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateInputs(root, GVisor); err == nil {
		t.Fatal("changed complete license accepted")
	}
	root = inputFixture(t, WireGuard)
	m, err := Load(WireGuard)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, SourceDirectory, filepath.FromSlash(m.Changes[0].SourcePath))
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".original", path); err != nil {
		t.Skip("symlink unavailable")
	}
	if _, err := ValidateInputs(root, WireGuard); err == nil {
		t.Fatal("linked overlay accepted")
	}
}

func TestExistingOutputDoesNotAuthenticateChangedOverlayRecipe(t *testing.T) {
	root := t.TempDir()
	directory := Directory(WireGuard)
	output := filepath.Join(root, filepath.FromSlash(directory))
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	mod := []byte("module fixture\n")
	original := []byte("package fixture\n")
	changed := []byte("package stale_recipe\n")
	for name, data := range map[string][]byte{"go.mod": mod, "source.go": original} {
		if err := os.WriteFile(filepath.Join(output, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// This tiny existing output has a valid complete tree digest throughout.
	// It cannot establish that the checked-in/embedded recipe is still pinned.
	wantTree := engineadaptation.Hash([]byte(engineadaptation.Hash(mod) + "  go.mod\n" + engineadaptation.Hash(original) + "  source.go\n"))
	change := Change{Path: "source.go", SourcePath: "sources/wireguard/source.go.txt", AdaptedSHA256: engineadaptation.Hash(original)}
	for _, fixture := range []struct {
		name               string
		checkout, embedded []byte
		valid              bool
	}{
		{"unchanged recipe", original, original, true},
		{"changed checkout", changed, original, false},
		{"changed embedded source", original, changed, false},
		{"rebuilt preparer with stale manifest", changed, changed, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if err := engineadaptation.VerifyPinnedTree(root, directory, wantTree); err != nil {
				t.Fatalf("existing output must remain valid: %v", err)
			}
			err := validateOverlayInput(change, fixture.checkout, fixture.embedded)
			if (err == nil) != fixture.valid {
				t.Fatalf("overlay recipe acceptance = %v", err)
			}
		})
	}
}

func TestPreparationBootstrapHasNoAdaptedModuleImports(t *testing.T) {
	for _, directory := range []string{"cmd/prepare-engine", engineadaptation.SourceDirectory, SourceDirectory} {
		entries, err := os.ReadDir(filepath.Join("..", "..", directory))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join("..", "..", directory, entry.Name())
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range file.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(path, "github.com/webkaz-labs/sobalink/internal/") {
					if path != "github.com/webkaz-labs/sobalink/internal/engineadaptation" && path != "github.com/webkaz-labs/sobalink/internal/lifecycleadaptation" {
						t.Fatalf("unexpected bootstrap package: %s", path)
					}
				} else if strings.Contains(strings.Split(path, "/")[0], ".") {
					t.Fatalf("bootstrap depends on external module: %s", path)
				}
			}
		}
	}
}
