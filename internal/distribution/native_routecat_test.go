package distribution

import (
	"encoding/json"
	"go/build"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Select the actual checked-in Darwin source independently of the reviewed
// hash map, without compiling or starting the networking runtime.
func darwinRoutecatFixture(t *testing.T) goPackage {
	t.Helper()
	context := build.Default
	context.GOOS, context.GOARCH, context.CgoEnabled = "darwin", "arm64", true
	context.BuildTags = strings.Split(BuildTags, ",")
	selected, err := context.ImportDir(filepath.Join("..", "routecat"), 0)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	p := goPackage{ImportPath: routecatPackage, Dir: filepath.Join(root, filepath.FromSlash(routecatPath)), Module: &goModule{Path: Project, Main: true, Dir: root}, GoFiles: selected.GoFiles, Imports: selected.Imports}
	if len(selected.CgoFiles)+len(selected.CFiles)+len(selected.CXXFiles)+len(selected.MFiles)+len(selected.HFiles)+len(selected.FFiles)+len(selected.SFiles)+len(selected.SwigFiles)+len(selected.SwigCXXFiles)+len(selected.SysoFiles)+len(selected.EmbedPatterns) != 0 {
		t.Fatal("actual adapted source gained non-Go inputs")
	}
	for _, name := range p.GoFiles {
		writeFixture(t, filepath.Join(p.Dir, name), string(readFixture(t, filepath.Join("..", "routecat", name))))
	}
	return p
}

func TestDarwinNativeClosureIncludesExactAdaptedPolicyContainer(t *testing.T) {
	packages := nativeInventoryFixture(t)
	p := darwinRoutecatFixture(t)
	if !slices.Contains(p.Imports, "tailscale.com/tsd") {
		t.Fatal("actual adapted source no longer exercises the tsd boundary")
	}
	packages = append(packages, p)
	records, policy, err := darwinNativeInventory(packages)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || len(policy) != 5+10 {
		t.Fatal("incomplete native or adapted policy boundary")
	}
	for _, input := range policy {
		if name, ok := strings.CutPrefix(input.Path, routecatPackage+"/"); ok && input.SHA256 != bytesHash(readFixture(t, filepath.Join(p.Dir, name))) {
			t.Fatalf("incorrect adapted source identity: %s", name)
		}
	}
	raw, err := json.Marshal(policy)
	if err != nil || strings.Contains(string(raw), p.Module.Dir) {
		t.Fatal("policy evidence contains a machine path")
	}
	slices.Reverse(p.GoFiles)
	packages[len(packages)-1] = p
	slices.Reverse(packages)
	again, againPolicy, err := darwinNativeInventory(packages)
	if err != nil || !reflect.DeepEqual(records, again) || !reflect.DeepEqual(policy, againPolicy) {
		t.Fatal("adapted policy boundary is not deterministic")
	}
}

func TestDarwinAdaptedPolicyContainerRejectsEveryChangedSource(t *testing.T) {
	for name := range darwinRoutecatPolicyInputs {
		t.Run(name, func(t *testing.T) {
			packages := nativeInventoryFixture(t)
			p := darwinRoutecatFixture(t)
			writeFixture(t, filepath.Join(p.Dir, name), string(readFixture(t, filepath.Join(p.Dir, name)))+"\n// changed source\n")
			if _, _, err := darwinNativeInventory(append(packages, p)); err == nil {
				t.Fatal("accepted changed adapted source")
			}
		})
	}
}

func TestDarwinNativeClosureRejectsAdditionalAdaptedPolicyContainerSource(t *testing.T) {
	packages := nativeInventoryFixture(t)
	p := darwinRoutecatFixture(t)
	writeFixture(t, filepath.Join(p.Dir, "configure.go"), "package routecat\nfunc configure(b *locoBackend) { _ = b.sys }\n")
	p.GoFiles = append(p.GoFiles, "configure.go")
	if _, _, err := darwinNativeInventory(append(packages, p)); err == nil {
		t.Fatal("accepted extra same-package source without a tsd import")
	}
}

func TestDarwinAdaptedPolicyContainerRejectsUnreviewedIdentityAndSourceSet(t *testing.T) {
	tests := map[string]func(*testing.T, *goPackage){
		"wrong package":               func(_ *testing.T, p *goPackage) { p.ImportPath = Project + "/internal/other" },
		"nil module":                  func(_ *testing.T, p *goPackage) { p.Module = nil },
		"external module":             func(_ *testing.T, p *goPackage) { p.Module.Main = false },
		"wrong module":                func(_ *testing.T, p *goPackage) { p.Module.Path = "example.org/substitute" },
		"replacement":                 func(_ *testing.T, p *goPackage) { p.Module.Replace = &goModule{} },
		"module version":              func(_ *testing.T, p *goPackage) { p.Module.Version = "v0.0.0" },
		"module sum":                  func(_ *testing.T, p *goPackage) { p.Module.Sum = "synthetic" },
		"relative root":               func(_ *testing.T, p *goPackage) { p.Module.Dir = "." },
		"different root":              func(t *testing.T, p *goPackage) { p.Module.Dir = t.TempDir() },
		"different package directory": func(t *testing.T, p *goPackage) { p.Dir = t.TempDir() },
		"missing listed source":       func(_ *testing.T, p *goPackage) { p.GoFiles = p.GoFiles[1:] },
		"duplicate listed source":     func(_ *testing.T, p *goPackage) { p.GoFiles[0] = p.GoFiles[1] },
		"unexpected listed source":    func(_ *testing.T, p *goPackage) { p.GoFiles[0] = "unexpected.go" },
		"missing source file": func(t *testing.T, p *goPackage) {
			if err := os.Remove(filepath.Join(p.Dir, p.GoFiles[0])); err != nil {
				t.Fatal(err)
			}
		},
		"additional same-package source without tsd import": func(t *testing.T, p *goPackage) {
			writeFixture(t, filepath.Join(p.Dir, "configure.go"), "package routecat\nfunc configure(b *locoBackend) { _ = b.sys }\n")
			p.GoFiles = append(p.GoFiles, "configure.go")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := darwinRoutecatFixture(t)
			mutate(t, &p)
			if _, err := darwinRoutecatPolicyInventory(p); err == nil {
				t.Fatal("accepted unreviewed adapted source identity or set")
			}
		})
	}
	for _, field := range []string{"CgoFiles", "CFiles", "CXXFiles", "MFiles", "HFiles", "FFiles", "SFiles", "SwigFiles", "SwigCXXFiles", "SysoFiles", "EmbedFiles", "CgoCFLAGS", "CgoCPPFLAGS", "CgoCXXFLAGS", "CgoFFLAGS", "CgoLDFLAGS", "CgoPkgConfig"} {
		t.Run(field, func(t *testing.T) {
			p := darwinRoutecatFixture(t)
			reflect.ValueOf(&p).Elem().FieldByName(field).Set(reflect.ValueOf([]string{"unreviewed"}))
			if _, err := darwinRoutecatPolicyInventory(p); err == nil {
				t.Fatal("accepted unreviewed native/embed input or build mechanism")
			}
		})
	}
}

func TestDarwinAdaptedPolicyContainerRejectsSourceLinks(t *testing.T) {
	for _, part := range []string{"file", "package directory", "interior directory", "module root"} {
		t.Run(part, func(t *testing.T) {
			p := darwinRoutecatFixture(t)
			path := filepath.Join(p.Dir, p.GoFiles[0])
			switch part {
			case "package directory":
				path = p.Dir
			case "interior directory":
				path = filepath.Join(p.Module.Dir, "internal")
			case "module root":
				path = p.Module.Dir
			}
			target := filepath.Join(t.TempDir(), "target")
			if err := os.Rename(path, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skip("source-link fixture unavailable on this platform")
			}
			if _, err := darwinRoutecatPolicyInventory(p); err == nil {
				t.Fatal("accepted a linked adapted source input")
			}
		})
	}
}

func TestDarwinAdaptedPolicyContainerDoesNotPermitApplicationPolicyOrCredentials(t *testing.T) {
	for _, importer := range []string{routecatPackage, Project + "/internal/other"} {
		for _, imp := range []string{certstoreModule, "tailscale.com/util/syspolicy", "tailscale.com/util/syspolicy/policyclient", "tailscale.com/util/syspolicy/setting"} {
			t.Run(importer+"/"+imp, func(t *testing.T) {
				packages := nativeInventoryFixture(t)
				p := darwinRoutecatFixture(t)
				if importer == routecatPackage {
					p.Imports = append(p.Imports, imp)
				} else {
					packages = append(packages, goPackage{ImportPath: importer, Module: &goModule{Path: Project, Main: true}, Imports: []string{imp}})
				}
				if _, _, err := darwinNativeInventory(append(packages, p)); err == nil {
					t.Fatal("adapted tsd boundary admitted application policy or credentials")
				}
			})
		}
	}
}
