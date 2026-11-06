package distribution

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func installNativeUIFixture(t *testing.T, tool *Tool) {
	t.Helper()
	root := tool.Root
	for _, name := range []string{"poc/native/go.mod", "poc/native/go.sum", "poc/native/main.go", "internal/control/client.go"} {
		writeFixture(t, filepath.Join(root, filepath.FromSlash(name)), "fixture "+name)
	}
	deps := filepath.Join(root, "deps", "mygo")
	writeFixture(t, filepath.Join(deps, "LICENSE"), "MyGo license")
	tool.nativeUIRun = func(env []string, args ...string) ([]byte, error) {
		target := Target{}
		for _, v := range env {
			if strings.HasPrefix(v, "GOOS=") {
				target.OS = strings.TrimPrefix(v, "GOOS=")
			}
			if strings.HasPrefix(v, "GOARCH=") {
				target.Arch = strings.TrimPrefix(v, "GOARCH=")
			}
		}
		if !strings.Contains(strings.Join(env, "\n"), "CGO_ENABLED=0") {
			t.Fatal("UI must use CGO=0 independently")
		}
		switch args[0] {
		case "env":
			return []byte(NativeUIGoVersion + "\n" + target.OS + "\n" + target.Arch + "\n" + filepath.Join(root, "toolchain")), nil
		case "mod":
			return nil, nil
		case "list":
			packages := []goPackage{
				{ImportPath: nativeUIProject, Dir: filepath.Join(root, "poc", "native"), GoFiles: []string{"main.go"}, Imports: []string{Project + "/internal/control", "github.com/egoist/mygo"}, Module: &goModule{Path: nativeUIProject, Dir: filepath.Join(root, "poc", "native"), Main: true}},
				{ImportPath: Project + "/internal/control", Dir: filepath.Join(root, "internal", "control"), GoFiles: []string{"client.go"}, Module: &goModule{Path: Project, Version: "v0.0.0", Dir: root, Replace: &goModule{Path: "../..", Dir: root}}},
				{ImportPath: "github.com/egoist/mygo", Dir: deps, Module: &goModule{Path: "github.com/egoist/mygo", Version: "v0.7.4", Sum: "h1:fixture", Dir: deps}},
			}
			var out []byte
			for _, p := range packages {
				data, _ := json.Marshal(p)
				out = append(out, data...)
				out = append(out, '\n')
			}
			return out, nil
		case "build":
			for i, v := range args {
				if v == "-o" {
					writeFixture(t, args[i+1], "compiled native UI")
				}
			}
			return nil, nil
		}
		return nil, errors.New("unexpected UI command")
	}
}

func TestNativeUIInputAdmission(t *testing.T) {
	root := t.TempDir()
	valid := goPackage{ImportPath: Project + "/internal/control", Dir: filepath.Join(root, "internal", "control"), Module: &goModule{Path: Project, Dir: root, Version: "v0.0.0", Replace: &goModule{Path: "../..", Dir: root}}}
	got, err := nativeUIPackages(root, []goPackage{valid})
	if err != nil || !got[0].Module.Main || got[0].Module.Replace != nil {
		t.Fatalf("exact root replacement: %v", err)
	}
	for _, mutate := range []func(*goModule){func(m *goModule) { m.Version = "v1.0.0" }, func(m *goModule) { m.Replace = nil }, func(m *goModule) { m.Replace = &goModule{Dir: filepath.Join(root, "elsewhere")} }, func(m *goModule) { m.Path = "other" }, func(m *goModule) { m.Main = true }} {
		bad := valid
		m := *valid.Module
		bad.Module = &m
		mutate(&m)
		if _, err := nativeUIPackages(root, []goPackage{bad}); err == nil {
			t.Fatal("accepted unreviewed replacement")
		}
	}
}

func TestNativeUIIncludesSharedEmbeddedArtwork(t *testing.T) {
	root, destination := t.TempDir(), t.TempDir()
	for _, name := range []string{"go.mod", "go.sum", "LICENSE", "poc/native/go.mod", "poc/native/go.sum", "internal/uibrand/brand.go", "internal/uibrand/palette.json", "internal/uibrand/icons.json", "internal/uibrand/mark.svg"} {
		writeFixture(t, filepath.Join(root, filepath.FromSlash(name)), "fictional source "+name)
	}
	packages := []goPackage{{ImportPath: Project + "/internal/uibrand", Dir: filepath.Join(root, "internal", "uibrand"), GoFiles: []string{"brand.go"}, EmbedFiles: []string{"palette.json", "icons.json", "mark.svg"}, Module: &goModule{Path: Project, Dir: root, Main: true}}}
	inputs, err := nativeUISourceInputs(root, packages, destination)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"palette.json", "icons.json", "mark.svg"} {
		path := "internal/uibrand/" + name
		found := false
		for _, input := range inputs {
			if input.Path == path {
				got, err := fileHash(filepath.Join(destination, filepath.FromSlash(path)))
				if err != nil || got != input.SHA256 {
					t.Fatal("shared artwork missing from retained source")
				}
				found = true
			}
		}
		if !found {
			t.Fatal("shared artwork omitted from input identity")
		}
	}
}
func TestNativeUIRequiresExplicitCompiler(t *testing.T) {
	t.Setenv("SOBALINK_NATIVE_GO", "")
	tool, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []Target{{"darwin", "arm64"}, {"windows", "amd64"}} {
		if _, _, _, err := tool.buildNativeUI("1.0.0", target, strings.Repeat("a", 40), t.TempDir()); err == nil {
			t.Fatal("accepted missing compiler")
		}
	}
	if b, n, bom, err := tool.buildNativeUI("1.0.0", Targets[0], strings.Repeat("a", 40), t.TempDir()); err != nil || b != nil || n != nil || bom != nil {
		t.Fatal("Linux must remain unaffected")
	}
}
func TestNativeUIBundleIdentity(t *testing.T) {
	tool, _ := mockTool(t)
	for _, target := range []Target{{"darwin", "arm64"}, {"windows", "amd64"}} {
		stage := t.TempDir()
		os.MkdirAll(filepath.Join(stage, "bin"), 0755)
		build, notices, bom, err := tool.buildNativeUI("1.2.3-alpha.1", target, strings.Repeat("a", 40), stage)
		if err != nil {
			t.Fatal(err)
		}
		if build.GoVersion != NativeUIGoVersion || build.CGO || build.BuildVCS || !build.Trimpath || notices.Go.Version != NativeUIGoVersion {
			t.Fatal("UI toolchain policy changed")
		}
		share := filepath.Join(stage, "share", Product, "native-ui")
		for _, input := range build.SourceInputs {
			hash, err := fileHash(filepath.Join(share, "source", filepath.FromSlash(input.Path)))
			if err != nil || hash != input.SHA256 {
				t.Fatalf("source mismatch: %s", input.Path)
			}
		}
		if bom["metadata"].(map[string]any)["component"].(map[string]any)["bom-ref"] != "native-ui:application:sobalink" {
			t.Fatal("UI identity collision")
		}
		for _, raw := range bom["components"].([]any) {
			if !strings.HasPrefix(raw.(map[string]any)["bom-ref"].(string), "native-ui:") {
				t.Fatal("unscoped UI component")
			}
		}
		top := makeSBOM(nil, NoticeInventory{Go: ModuleNotices{Version: GoVersion}}, "1.2.3-alpha.1", target, strings.Repeat("a", 40), strings.Repeat("b", 64))
		mergeNativeUISBOM(top, bom)
		if len(top["components"].([]any)) != len(bom["components"].([]any))+2 {
			t.Fatal("combined SBOM omitted UI")
		}
		again, againNotices, againBOM, err := tool.buildNativeUI("1.2.3-alpha.1", target, strings.Repeat("a", 40), stage)
		if err != nil || !reflect.DeepEqual(build, again) || !reflect.DeepEqual(notices, againNotices) || !reflect.DeepEqual(bom, againBOM) {
			t.Fatal("UI package not deterministic", err)
		}
	}
}
func TestNativeUIRejectsChangedLocalInputs(t *testing.T) {
	tool, _ := mockTool(t)
	base := tool.nativeUIRun
	tool.nativeUIRun = func(env []string, args ...string) ([]byte, error) {
		data, err := base(env, args...)
		if args[0] == "build" {
			writeFixture(t, filepath.Join(tool.Root, "poc", "native", "main.go"), "changed")
		}
		return data, err
	}
	if err := tool.Build("1.0.0", Target{"windows", "amd64"}, strings.Repeat("a", 40), io.Discard); err == nil || !strings.Contains(err.Error(), "inputs changed") {
		t.Fatal("changed source accepted", err)
	}
}
func TestNativeUIRejectsWrongToolchain(t *testing.T) {
	tool, _ := mockTool(t)
	base := tool.nativeUIRun
	tool.nativeUIRun = func(env []string, args ...string) ([]byte, error) {
		data, err := base(env, args...)
		if args[0] == "env" {
			data = []byte(strings.Replace(string(data), NativeUIGoVersion, GoVersion, 1))
		}
		return data, err
	}
	if err := tool.Build("1.0.0", Target{"windows", "amd64"}, strings.Repeat("a", 40), io.Discard); err == nil {
		t.Fatal("wrong UI compiler accepted")
	}
}

func TestNativeUISourceInputsRejectLinksAndEscapes(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"go.mod", "go.sum", "LICENSE", "poc/native/go.mod", "poc/native/go.sum"} {
		writeFixture(t, filepath.Join(root, filepath.FromSlash(name)), "fixture")
	}
	pkg := goPackage{ImportPath: nativeUIProject, Dir: filepath.Join(root, "poc", "native"), Module: &goModule{Main: true}, GoFiles: []string{"../../../outside.go"}}
	if _, err := nativeUISourceInputs(root, []goPackage{pkg}, ""); err == nil {
		t.Fatal("accepted escaping source input")
	}
	outside := filepath.Join(t.TempDir(), "outside.go")
	writeFixture(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(root, "poc", "native", "linked.go")); err != nil {
		t.Skip("symlinks unavailable")
	}
	pkg.GoFiles = []string{"linked.go"}
	if _, err := nativeUISourceInputs(root, []goPackage{pkg}, ""); err == nil {
		t.Fatal("accepted linked source input")
	}
}
func TestNativeUIRejectsToolchainChange(t *testing.T) {
	tool, _ := mockTool(t)
	base := tool.nativeUIRun
	queries := 0
	tool.nativeUIRun = func(env []string, args ...string) ([]byte, error) {
		data, err := base(env, args...)
		if args[0] == "env" {
			queries++
			if queries > 1 {
				data = append(data, 'x')
			}
		}
		return data, err
	}
	if err := tool.Build("1.0.0", Target{"windows", "amd64"}, strings.Repeat("a", 40), io.Discard); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatal("changed toolchain accepted", err)
	}
}
