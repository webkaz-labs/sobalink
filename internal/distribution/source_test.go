package distribution

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func sourcePackageFixture(root string) goPackage {
	return goPackage{
		ImportPath: routecatPackage, Dir: filepath.Join(root, "internal", "routecat"),
		Module:  &goModule{Path: Project, Main: true, Dir: root},
		GoFiles: []string{"disco.go", "listen.go", "regions.go", "tailcat.go", "wire.go"},
		Imports: []string{"fmt"},
	}
}

func writeSourceFixture(t *testing.T, root string) {
	t.Helper()
	// Read the checked-in notices, never fetch legal text during a test.
	for _, name := range []string{"LICENSE", "UPSTREAM.json", "UPSTREAM.md"} {
		writeFixture(t, filepath.Join(root, "internal", "routecat", name), string(readFixture(t, filepath.Join("..", "routecat", name))))
	}
	for name := range routecatUpstream().Files {
		if name != "LICENSE" {
			writeFixture(t, filepath.Join(root, "internal", "routecat", name), "// Copyright (c) Tailscale Inc & contributors\n// SPDX-License-Identifier: BSD-3-Clause\n\npackage routecat\n// adapted fixture\n")
		}
	}
	writeFixture(t, filepath.Join(root, "internal", "routecat", "regions.go"), "// SPDX-License-Identifier: MIT\npackage routecat\n")
	writeFixture(t, filepath.Join(root, "internal", "routecat", "unused_test.go"), "not a runtime input")
}

func sourceFixture(t *testing.T) (string, []goPackage) {
	t.Helper()
	root := t.TempDir()
	writeSourceFixture(t, root)
	writeFixture(t, filepath.Join(root, "go.mod"), "module "+Project+"\n")
	writeFixture(t, filepath.Join(root, "go.sum"), "module checksum fixture\n")
	return root, []goPackage{sourcePackageFixture(root)}
}

func TestSourceInventoryRetainsExactProvenanceAndBuildInputs(t *testing.T) {
	root, packages := sourceFixture(t)
	share := t.TempDir()
	components, err := sourceInventory(root, packages, share)
	if err != nil {
		t.Fatal(err)
	}
	if len(components) != 1 {
		t.Fatalf("source components = %+v", components)
	}
	component := components[0]
	if !reflect.DeepEqual(component.Upstream, routecatUpstream()) || component.Package != routecatPackage || component.Path != routecatPath || !strings.Contains(component.Modification, "Adapted main-module source") {
		t.Fatalf("source provenance = %+v", component)
	}
	if len(component.Notices) != 3 || len(component.BuildInputs) != 10 {
		t.Fatalf("source inputs = %+v", component)
	}
	for _, notice := range component.Notices {
		retained := readFixture(t, filepath.Join(share, filepath.FromSlash(notice.Path)))
		original := readFixture(t, filepath.Join(root, "internal", "routecat", filepath.Base(notice.Path)))
		if !bytes.Equal(retained, original) || bytesHash(retained) != notice.SHA256 {
			t.Fatalf("retained notice changed: %s", notice.Path)
		}
	}
	for _, input := range component.BuildInputs {
		if input.SHA256 != bytesHash(readFixture(t, filepath.Join(root, filepath.FromSlash(input.Path)))) || strings.Contains(input.Path, "_test.go") {
			t.Fatalf("incorrect source input: %+v", input)
		}
	}
	raw, _ := json.Marshal(component.BuildInputs)
	if component.BuildInputsSHA256 != bytesHash(raw) {
		t.Fatal("input manifest digest mismatch")
	}
	inventory, _ := json.Marshal(NoticeInventory{Sources: components})
	if !bytes.Contains(inventory, []byte(`"source_components"`)) || bytes.Contains(inventory, []byte(root)) {
		t.Fatal("missing source inventory or leaked local path")
	}
	// The upstream source hashes intentionally differ from these adapted bytes.
	if component.Upstream.Files["tailcat.go"] == bytesHash(readFixture(t, filepath.Join(root, "internal", "routecat", "tailcat.go"))) {
		t.Fatal("test must distinguish adapted and original upstream source")
	}
}

func TestSourceInventoryReflectsChangedInputs(t *testing.T) {
	for _, path := range []string{"internal/routecat/tailcat.go", "internal/routecat/regions.go", "internal/routecat/UPSTREAM.md", "go.mod", "go.sum"} {
		t.Run(path, func(t *testing.T) {
			root, packages := sourceFixture(t)
			before, err := sourceInventory(root, packages, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(root, filepath.FromSlash(path))
			writeFixture(t, name, string(readFixture(t, name))+"\n// changed input\n")
			after, err := sourceInventory(root, packages, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if before[0].BuildInputsSHA256 == after[0].BuildInputsSHA256 || !reflect.DeepEqual(before[0].Upstream, after[0].Upstream) {
				t.Fatal("changed build input was lost or original upstream provenance changed")
			}
			beforeBOM, _ := json.Marshal(makeSBOM(packages, NoticeInventory{Sources: before}, "1.2.3", Targets[0], strings.Repeat("a", 40), strings.Repeat("b", 64)))
			afterBOM, _ := json.Marshal(makeSBOM(packages, NoticeInventory{Sources: after}, "1.2.3", Targets[0], strings.Repeat("a", 40), strings.Repeat("b", 64)))
			if bytes.Equal(beforeBOM, afterBOM) {
				t.Fatal("changed inputs absent from SBOM")
			}
		})
	}
}

func TestSourceInventoryFailClosed(t *testing.T) {
	tests := map[string]func(*testing.T, string, []goPackage){
		"missing provenance": func(t *testing.T, root string, _ []goPackage) {
			if err := os.Remove(filepath.Join(root, routecatPath, "UPSTREAM.json")); err != nil {
				t.Fatal(err)
			}
		},
		"missing license": func(t *testing.T, root string, _ []goPackage) {
			if err := os.Remove(filepath.Join(root, routecatPath, "LICENSE")); err != nil {
				t.Fatal(err)
			}
		},
		"missing description": func(t *testing.T, root string, _ []goPackage) {
			if err := os.Remove(filepath.Join(root, routecatPath, "UPSTREAM.md")); err != nil {
				t.Fatal(err)
			}
		},
		"empty description": func(t *testing.T, root string, _ []goPackage) {
			writeFixture(t, filepath.Join(root, routecatPath, "UPSTREAM.md"), "\n")
		},
		"substituted license": func(t *testing.T, root string, _ []goPackage) {
			writeFixture(t, filepath.Join(root, routecatPath, "LICENSE"), "different legal terms")
		},
		"license changed together with provenance": func(t *testing.T, root string, _ []goPackage) {
			writeFixture(t, filepath.Join(root, routecatPath, "LICENSE"), "different legal terms")
			pin := routecatUpstream()
			pin.Files["LICENSE"] = bytesHash([]byte("different legal terms"))
			if err := writeJSON(filepath.Join(root, routecatPath, "UPSTREAM.json"), pin); err != nil {
				t.Fatal(err)
			}
		},
		"malformed provenance": func(t *testing.T, root string, _ []goPackage) {
			writeFixture(t, filepath.Join(root, routecatPath, "UPSTREAM.json"), "{")
		},
		"trailing provenance": func(t *testing.T, root string, _ []goPackage) {
			p := filepath.Join(root, routecatPath, "UPSTREAM.json")
			writeFixture(t, p, string(readFixture(t, p))+"{}")
		},
		"unknown provenance": func(t *testing.T, root string, _ []goPackage) {
			p := filepath.Join(root, routecatPath, "UPSTREAM.json")
			writeFixture(t, p, strings.Replace(string(readFixture(t, p)), "{", `{"unknown": true,`, 1))
		},
		"duplicate provenance key": func(t *testing.T, root string, _ []goPackage) {
			p := filepath.Join(root, routecatPath, "UPSTREAM.json")
			writeFixture(t, p, strings.Replace(string(readFixture(t, p)), "{", `{"license":"unreviewed",`, 1))
		},
		"duplicate upstream digest": func(t *testing.T, root string, _ []goPackage) {
			p := filepath.Join(root, routecatPath, "UPSTREAM.json")
			writeFixture(t, p, strings.Replace(string(readFixture(t, p)), `"files": {`, `"files": {"LICENSE":"unreviewed",`, 1))
		},
		"case-folded provenance key": func(t *testing.T, root string, _ []goPackage) {
			p := filepath.Join(root, routecatPath, "UPSTREAM.json")
			writeFixture(t, p, strings.Replace(string(readFixture(t, p)), `"license":`, `"License":`, 1))
		},
		"dropped copyright": func(t *testing.T, root string, _ []goPackage) {
			writeFixture(t, filepath.Join(root, routecatPath, "tailcat.go"), "package routecat\n")
		},
		"missing source": func(t *testing.T, root string, _ []goPackage) {
			if err := os.Remove(filepath.Join(root, routecatPath, "wire.go")); err != nil {
				t.Fatal(err)
			}
		},
		"missing source inventory": func(_ *testing.T, _ string, packages []goPackage) { packages[0].GoFiles = nil },
		"external module":          func(_ *testing.T, _ string, packages []goPackage) { packages[0].Module.Main = false },
		"substituted module":       func(_ *testing.T, _ string, packages []goPackage) { packages[0].Module.Path = "example.org/substitute" },
		"replaced module": func(_ *testing.T, _ string, packages []goPackage) {
			packages[0].Module.Replace = &goModule{Path: "example.org/replaced"}
		},
		"outside package": func(_ *testing.T, root string, packages []goPackage) {
			packages[0].Dir = filepath.Join(root, "outside")
		},
		"outside module": func(_ *testing.T, root string, packages []goPackage) {
			packages[0].Module.Dir = filepath.Join(root, "outside")
		},
		"traversal": func(_ *testing.T, _ string, packages []goPackage) {
			packages[0].GoFiles = append(packages[0].GoFiles, "../private.go")
		},
		"windows path": func(_ *testing.T, _ string, packages []goPackage) {
			packages[0].GoFiles = append(packages[0].GoFiles, `C:\private.go`)
		},
		"test input": func(_ *testing.T, _ string, packages []goPackage) {
			packages[0].GoFiles = append(packages[0].GoFiles, "unused_test.go")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root, packages := sourceFixture(t)
			mutate(t, root, packages)
			if _, err := sourceInventory(root, packages, t.TempDir()); err == nil {
				t.Fatal("accepted untrusted source inventory")
			}
		})
	}
	for _, field := range []string{"module", "version", "commit", "source_url", "license", "files"} {
		t.Run("changed upstream "+field, func(t *testing.T) {
			root, packages := sourceFixture(t)
			var pin map[string]any
			if err := json.Unmarshal(readFixture(t, filepath.Join(root, routecatPath, "UPSTREAM.json")), &pin); err != nil {
				t.Fatal(err)
			}
			delete(pin, field)
			if err := writeJSON(filepath.Join(root, routecatPath, "UPSTREAM.json"), pin); err != nil {
				t.Fatal(err)
			}
			if _, err := sourceInventory(root, packages, t.TempDir()); err == nil {
				t.Fatal("accepted changed upstream metadata")
			}
		})
	}
}

func TestSourceInventoryRejectsLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("junction coverage uses native Windows test")
	}
	for _, path := range []string{"internal", routecatPath, routecatPath + "/LICENSE", routecatPath + "/UPSTREAM.json", routecatPath + "/UPSTREAM.md", routecatPath + "/regions.go", "go.mod"} {
		t.Run(path, func(t *testing.T) {
			root, packages := sourceFixture(t)
			original := filepath.Join(root, filepath.FromSlash(path))
			target := filepath.Join(t.TempDir(), "original")
			if err := os.Rename(original, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, original); err != nil {
				t.Fatal(err)
			}
			if _, err := sourceInventory(root, packages, t.TempDir()); err == nil {
				t.Fatal("followed source input link")
			}
		})
	}
	root, packages := sourceFixture(t)
	link := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	packages[0] = sourcePackageFixture(link)
	if _, err := sourceInventory(link, packages, t.TempDir()); err == nil {
		t.Fatal("followed checkout root link")
	}
}

func TestSourceSBOMSeparatesAdaptationAndUpstreamAncestor(t *testing.T) {
	root, packages := sourceFixture(t)
	packages = append(packages, goPackage{ImportPath: "fmt"})
	components, err := sourceInventory(root, packages, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bom := makeSBOM(packages, NoticeInventory{Sources: components}, "1.2.3", Targets[0], strings.Repeat("a", 40), strings.Repeat("b", 64))
	found := false
	for _, c := range bom["components"].([]any) {
		component := c.(map[string]any)
		if component["name"] == routecatUpstream().Module {
			t.Fatal("upstream module incorrectly presented as an unmodified runtime component")
		}
		if component["name"] != routecatPackage {
			continue
		}
		found = true
		if _, exists := component["purl"]; exists {
			t.Fatal("adaptation inherited upstream purl")
		}
		if component["version"] != "1.2.3" {
			t.Fatal("adaptation inherited upstream version")
		}
		ancestor := component["pedigree"].(map[string]any)["ancestors"].([]any)[0].(map[string]any)
		if ancestor["version"] != routecatUpstream().Version || ancestor["purl"] != purl(routecatUpstream().Module, routecatUpstream().Version) {
			t.Fatal("upstream ancestry missing")
		}
	}
	if !found {
		t.Fatal("adapted source missing from SBOM")
	}
	for _, value := range bom["dependencies"].([]any) {
		dep := value.(map[string]any)
		if dep["ref"] == sourceRef(components[0]) && !reflect.DeepEqual(dep["dependsOn"], []string{"golang:stdlib"}) {
			t.Fatal("adapted source dependencies lost")
		}
		if dep["ref"] == "application:sobalink" && !reflect.DeepEqual(dep["dependsOn"], []string{sourceRef(components[0])}) {
			t.Fatal("application source dependency lost")
		}
	}
}

func TestSourceInventoryOnlyIncludesLinkedPackage(t *testing.T) {
	components, err := sourceInventory(t.TempDir(), nil, t.TempDir())
	if err != nil || len(components) != 0 {
		t.Fatalf("unlinked source inventoried: %+v %v", components, err)
	}
	root, packages := sourceFixture(t)
	packages = append(packages, packages[0])
	if _, err := sourceInventory(root, packages, t.TempDir()); err == nil {
		t.Fatal("accepted duplicate source package")
	}
}

func readArchiveFixture(t *testing.T, name string) map[string][]byte {
	t.Helper()
	data := readFixture(t, name)
	files := map[string][]byte{}
	if strings.HasSuffix(name, ".zip") {
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range archive.File {
			if file.FileInfo().IsDir() {
				continue
			}
			reader, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if err = reader.Close(); err != nil {
				t.Fatal(err)
			}
			files[file.Name] = content
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer gz.Close()
		archive := tar.NewReader(gz)
		for {
			h, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if h.Typeflag == tar.TypeDir {
				continue
			}
			content, err := io.ReadAll(archive)
			if err != nil {
				t.Fatal(err)
			}
			files[h.Name] = content
		}
	}
	return files
}

func TestBuildIncludesSourceNoticesMetadataAndSBOMForAllTargets(t *testing.T) {
	tool, _ := mockTool(t)
	for _, target := range Targets {
		t.Run(target.String(), func(t *testing.T) {
			if err := tool.Build("1.2.3", target, strings.Repeat("a", 40), io.Discard); err != nil {
				t.Fatal(err)
			}
			base := filepath.Join(tool.Root, "dist", stem("1.2.3", target))
			archive := readArchiveFixture(t, base+target.Extension())
			var notices NoticeInventory
			if err := json.Unmarshal(archive["share/sobalink/third-party-notices.json"], &notices); err != nil {
				t.Fatal(err)
			}
			if len(notices.Sources) != 1 {
				t.Fatal("release omitted adapted source")
			}
			for _, notice := range notices.Sources[0].Notices {
				content, ok := archive["share/sobalink/"+notice.Path]
				if !ok || bytesHash(content) != notice.SHA256 || !bytes.Equal(content, readFixture(t, filepath.Join(tool.Root, routecatPath, filepath.Base(notice.Path)))) {
					t.Fatalf("archived notice missing/changed: %s", notice.Path)
				}
			}
			var metadata struct {
				Sources []SourceComponent `json:"source_components"`
			}
			if err := json.Unmarshal(archive["share/sobalink/build.json"], &metadata); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(metadata.Sources, notices.Sources) {
				t.Fatal("build metadata lost source provenance")
			}
			for _, pair := range [][2]string{{"build.json", ".build.json"}, {"bom.cdx.json", ".cdx.json"}, {"third-party-notices.json", ".notices.json"}} {
				if !bytes.Equal(archive["share/sobalink/"+pair[0]], readFixture(t, base+pair[1])) {
					t.Fatal("detached metadata differs from archive")
				}
				if bytes.Contains(archive["share/sobalink/"+pair[0]], []byte(tool.Root)) {
					t.Fatal("local checkout path leaked")
				}
			}
			binaryCount := 0
			for name := range archive {
				if strings.HasPrefix(name, "bin/") {
					binaryCount++
					if name != "bin/"+target.Binary() {
						t.Fatal("unexpected binary")
					}
				}
			}
			if binaryCount != 1 {
				t.Fatal("single executable layout changed")
			}
		})
	}
}

func TestBuildRefusesMissingSourceNotice(t *testing.T) {
	tool, _ := mockTool(t)
	if err := os.Remove(filepath.Join(tool.Root, routecatPath, "LICENSE")); err != nil {
		t.Fatal(err)
	}
	if err := tool.Build("1.2.3", Targets[0], strings.Repeat("a", 40), io.Discard); err == nil {
		t.Fatal("packaged source without required notice")
	}
	if _, err := os.Stat(filepath.Join(tool.Root, "dist")); !os.IsNotExist(err) {
		t.Fatal("failed source inventory published artifacts")
	}
}

func TestBuildRejectsSourceInputsChangedDuringCompilation(t *testing.T) {
	for _, change := range []string{"changed file", "new selected file", "removed linked package"} {
		t.Run(change, func(t *testing.T) {
			tool, _ := mockTool(t)
			originalRun := tool.run
			built := false
			tool.run = func(env []string, args ...string) ([]byte, error) {
				result, err := originalRun(env, args...)
				if err != nil {
					return nil, err
				}
				if args[0] == "build" {
					built = true
					if change == "changed file" {
						name := filepath.Join(tool.Root, routecatPath, "regions.go")
						writeFixture(t, name, string(readFixture(t, name))+"\n// modified during compilation\n")
					}
				}
				if args[0] == "list" && built && change != "changed file" {
					packages, err := parsePackages(result)
					if err != nil {
						t.Fatal(err)
					}
					var out bytes.Buffer
					encoder := json.NewEncoder(&out)
					for _, pkg := range packages {
						if pkg.ImportPath == routecatPackage {
							if change == "removed linked package" {
								continue
							}
							writeFixture(t, filepath.Join(tool.Root, routecatPath, "added.go"), "package routecat\n")
							pkg.GoFiles = append(pkg.GoFiles, "added.go")
						}
						if err := encoder.Encode(pkg); err != nil {
							t.Fatal(err)
						}
					}
					result = out.Bytes()
				}
				return result, nil
			}
			if err := tool.Build("1.2.3", Targets[0], strings.Repeat("a", 40), io.Discard); err == nil || !strings.Contains(err.Error(), "changed during package build") {
				t.Fatalf("accepted changed source inputs: %v", err)
			}
			if _, err := os.Stat(filepath.Join(tool.Root, "dist")); !os.IsNotExist(err) {
				t.Fatal("changed source input build published artifacts")
			}
		})
	}
}
