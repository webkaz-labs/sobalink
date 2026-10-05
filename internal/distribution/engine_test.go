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
