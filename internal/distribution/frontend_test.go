package distribution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrontendInventoryIncludesEmbeddedRuntimeAndBuildHelpers(t *testing.T) {
	tool, _ := mockTool(t)
	lock := `{"lockfileVersion":3,"packages":{"node_modules/react":{"version":"1.0.0","integrity":"sha512-react"},"node_modules/vite":{"version":"2.0.0","integrity":"sha512-vite","dev":true},"node_modules/test-only":{"version":"3.0.0","integrity":"sha512-test","dev":true}}}`
	writeFixture(t, filepath.Join(tool.Root, "web", "package-lock.json"), lock)
	writeFixture(t, filepath.Join(tool.Root, "web", "node_modules", "vite", "package.json"), `{"name":"vite","version":"2.0.0"}`)
	writeFixture(t, filepath.Join(tool.Root, "web", "node_modules", "vite", "LICENSE"), "Vite license")
	share := t.TempDir()
	modules, assets, err := frontendInventory(tool.Root, share)
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 2 || modules[0].Name != "react" || modules[1].Name != "vite" {
		t.Fatalf("runtime inventory: %+v", modules)
	}
	if len(assets) != 1 || assets[0].Path != "index.html" || len(assets[0].SHA256) != 64 {
		t.Fatalf("asset inventory: %+v", assets)
	}
	for _, module := range modules {
		for _, notice := range module.Notices {
			hash, err := fileHash(filepath.Join(share, filepath.FromSlash(notice.Path)))
			if err != nil || hash != notice.SHA256 {
				t.Fatalf("retained notice %s: %v", notice.Path, err)
			}
		}
	}
	bom := makeSBOM(nil, NoticeInventory{Frontend: modules}, "1.2.3", Targets[0], strings.Repeat("a", 40), strings.Repeat("b", 64))
	raw, err := json.Marshal(bom)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pkg:npm/react@1.0.0", "pkg:npm/vite@2.0.0", "sobalink", "npm:integrity"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("SBOM missing %s", want)
		}
	}
}
func TestFrontendInventoryRejectsMismatchedProvenance(t *testing.T) {
	for _, mutate := range []func(*testing.T, string){
		func(t *testing.T, root string) {
			writeFixture(t, filepath.Join(root, "web", "node_modules", "react", "package.json"), `{"name":"react","version":"9.9.9"}`)
		},
		func(t *testing.T, root string) {
			writeFixture(t, filepath.Join(root, "web", "package-lock.json"), `{"lockfileVersion":3,"packages":{"../private":{"version":"1.0.0","integrity":"sha512-fixture"}}}`)
		},
		func(t *testing.T, root string) {
			writeFixture(t, filepath.Join(root, "web", "package-lock.json"), `{"lockfileVersion":3,"packages":{"node_modules/react":{"version":"1.0.0","link":true}}}`)
		},
		func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "web", "node_modules", "react", "LICENSE")); err != nil {
				t.Fatal(err)
			}
		},
		func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "web", "dist", "index.html")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		tool, _ := mockTool(t)
		mutate(t, tool.Root)
		if _, _, err := frontendInventory(tool.Root, t.TempDir()); err == nil {
			t.Fatal("accepted incomplete or untrusted frontend inventory")
		}
	}
}
