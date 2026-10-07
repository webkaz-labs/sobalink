package distribution

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/engineadaptation"
)

func engineModuleFixture(root string) *goModule {
	dir := filepath.Join(root, filepath.FromSlash(engineadaptation.Directory))
	return &goModule{Path: engineadaptation.Module, Version: engineadaptation.Version, Dir: dir, Replace: &goModule{Path: engineadaptation.Replacement, Dir: dir}}
}
func TestEngineReplacementExactAllowlist(t *testing.T) {
	root := t.TempDir()
	if !allowedEngineModule(root, engineModuleFixture(root)) {
		t.Fatal("reviewed replacement refused")
	}
	for _, name := range []string{"module", "version", "sum", "directory", "replacement path", "replacement version", "replacement sum", "replacement directory", "nested replacement", "main"} {
		t.Run(name, func(t *testing.T) {
			m := engineModuleFixture(root)
			switch name {
			case "module":
				m.Path = "example.org/other"
			case "version":
				m.Version = "v1.105.0"
			case "sum":
				m.Sum = "h1:original-mislabeled-as-adapted"
			case "directory":
				m.Dir = filepath.Join(root, "elsewhere")
			case "replacement path":
				m.Replace.Path = "../tailscale"
			case "replacement version":
				m.Replace.Version = "v1.104.0"
			case "replacement sum":
				m.Replace.Sum = "h1:unreviewed"
			case "replacement directory":
				m.Replace.Dir = filepath.Join(root, "elsewhere")
			case "nested replacement":
				m.Replace.Replace = &goModule{}
			case "main":
				m.Main = true
			}
			if allowedEngineModule(root, m) {
				t.Fatal("unreviewed replacement accepted")
			}
		})
	}
}

func TestOwnedEngineReplacementSetAndSourceReferences(t *testing.T) {
	root := t.TempDir()
	for _, pin := range engineadaptation.ModulePins() {
		dir := filepath.Join(root, filepath.FromSlash(pin.Directory))
		m := &goModule{Path: pin.Module, Version: pin.Version, Dir: dir, Replace: &goModule{Path: pin.Replacement, Dir: dir}}
		if !allowedEngineModule(root, m) {
			t.Fatalf("pinned replacement refused: %s", pin.Module)
		}
		if moduleRef(m) != "source:"+engineSourcePath(pin.Module) {
			t.Fatalf("adapted module mislabeled as original: %s", pin.Module)
		}
		m.Replace.Main = true
		if allowedEngineModule(root, m) {
			t.Fatal("replacement main module accepted")
		}
		m.Replace.Main = false
		m.Version = "v0.0.0-unreviewed"
		if allowedEngineModule(root, m) {
			t.Fatal("wrong pinned module version accepted")
		}
	}
}

func TestOwnedEngineSourcesRetainRecipesAndOriginalNotices(t *testing.T) {
	sources, err := engineSources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 {
		t.Fatal("incomplete transport source set")
	}
	for _, source := range sources {
		if source.component.ManifestSHA256 == "" || source.component.AdaptedTreeSHA256 == "" {
			t.Fatal("missing source identity")
		}
		if source.retained["cmd/prepare-engine/main.go"] != "prepare-engine/main.go" {
			t.Fatal("missing preparation recipe")
		}
		if source.pin.Module != engineadaptation.Module {
			if source.component.Upstream.Files["LICENSE"] == "" || source.component.Upstream.Commit == "" {
				t.Fatal("missing original lifecycle license/revision")
			}
			found := false
			for from := range source.retained {
				if strings.Contains(from, "/sources/") {
					found = true
				}
			}
			if !found {
				t.Fatal("missing readable lifecycle overlays")
			}
		}
	}
}
func TestAdaptedEngineSBOMHasSeparateIdentity(t *testing.T) {
	root := t.TempDir()
	m := engineModuleFixture(root)
	packages := []goPackage{{ImportPath: "app", Module: &goModule{Main: true}, Imports: []string{"tailscale.com/net/underlayguard"}}, {ImportPath: "tailscale.com/net/underlayguard", Module: m, Imports: []string{"fmt"}}, {ImportPath: "fmt"}}
	source := SourceComponent{Package: engineadaptation.Module, Path: engineadaptation.SourceDirectory, Upstream: SourceProvenance{Module: engineadaptation.Module, Version: engineadaptation.Version, ModuleSum: engineadaptation.ModuleSum, GoModSum: engineadaptation.GoModSum}, ManifestSHA256: strings.Repeat("a", 64), AdaptedTreeSHA256: strings.Repeat("b", 64)}
	bom := makeSBOM(packages, NoticeInventory{Sources: []SourceComponent{source}}, "1.2.3", Targets[0], strings.Repeat("c", 40), strings.Repeat("d", 64))
	raw, err := json.Marshal(bom)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), root) {
		t.Fatal("local path in source SBOM")
	}
	found := false
	for _, value := range bom["components"].([]any) {
		c := value.(map[string]any)
		if c["name"] == engineadaptation.Module {
			found = true
			if c["bom-ref"] != "source:"+engineadaptation.SourceDirectory {
				t.Fatal("wrong source identity")
			}
			if _, ok := c["purl"]; ok {
				t.Fatal("adapted component claims original purl")
			}
		}
	}
	if !found {
		t.Fatal("adapted component missing")
	}
	for _, value := range bom["dependencies"].([]any) {
		d := value.(map[string]any)
		if d["ref"] == "golang:"+moduleKey(engineadaptation.Module, engineadaptation.Version) {
			t.Fatal("phantom original runtime module")
		}
	}
}
