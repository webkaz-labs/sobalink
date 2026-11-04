package distribution

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

const NativeUIGoVersion = "go1.27.3"
const nativeUIProject = Project + "/poc/native"

// NativeUIBuild is independent of the daemon's compiler and native provenance.
// Paths never contain a runner directory or a runtime profile.
type NativeUIBuild struct {
	Project            string            `json:"project"`
	Product            string            `json:"product"`
	Version            string            `json:"version"`
	Commit             string            `json:"source_commit"`
	Target             string            `json:"target"`
	GoVersion          string            `json:"go_version"`
	BuildTags          string            `json:"build_tags"`
	CGO                bool              `json:"cgo_enabled"`
	Trimpath           bool              `json:"trimpath"`
	BuildVCS           bool              `json:"buildvcs"`
	LDFlags            string            `json:"ldflags"`
	GoModSHA256        string            `json:"go_mod_sha256"`
	GoSumSHA256        string            `json:"go_sum_sha256"`
	Binary             map[string]string `json:"binary"`
	SourceInputs       []Notice          `json:"source_inputs"`
	SourceInputsSHA256 string            `json:"source_inputs_sha256"`
	SBOMSHA256         string            `json:"sbom_sha256"`
	NoticesSHA256      string            `json:"notices_sha256"`
}

func nativeUITarget(target Target) bool {
	return target == (Target{"darwin", "arm64"}) || target == (Target{"windows", "amd64"})
}

// nativeUIPackages admits only the nested main module and its exact in-tree
// product dependency. All third-party replacements retain the existing reviewed
// engine policy; no arbitrary local replacement is made publishable.
func nativeUIPackages(root string, packages []goPackage) ([]goPackage, error) {
	result := append([]goPackage(nil), packages...)
	for i := range result {
		p := &result[i]
		if p.Module == nil {
			continue
		}
		m := *p.Module
		if m.Main {
			if m.Path != nativeUIProject || filepath.Clean(m.Dir) != filepath.Join(root, "poc", "native") || m.Replace != nil {
				return nil, errors.New("unexpected native UI main module")
			}
		} else if m.Path == Project {
			if m.Version != "v0.0.0" || m.Replace == nil || m.Replace.Version != "" || filepath.Clean(m.Replace.Dir) != root || filepath.Clean(m.Dir) != root {
				return nil, errors.New("native UI product replacement must be the exact source checkout")
			}
			m.Main = true
			m.Replace = nil
		} else if m.Replace != nil && !allowedEngineModule(root, &m) {
			return nil, fmt.Errorf("native UI refuses module replacement: %s", m.Path)
		}
		if m.Main {
			moduleRoot := root
			if m.Path == nativeUIProject {
				moduleRoot = filepath.Join(root, "poc", "native")
			}
			suffix := strings.TrimPrefix(p.ImportPath, m.Path)
			if (suffix != "" && !strings.HasPrefix(suffix, "/")) || !within(moduleRoot, p.Dir) || filepath.Clean(p.Dir) != filepath.Join(moduleRoot, filepath.FromSlash(suffix)) {
				return nil, errors.New("native UI local package is not from its exact source directory")
			}
		}
		p.Module = &m
	}
	return result, nil
}

func nativeUISourceInputs(root string, packages []goPackage, destination string) ([]Notice, error) {
	paths := map[string]bool{"go.mod": true, "go.sum": true, "LICENSE": true, "poc/native/go.mod": true, "poc/native/go.sum": true}
	for _, p := range packages {
		if p.Module == nil || !p.Module.Main {
			continue
		}
		for _, files := range [][]string{p.GoFiles, p.CgoFiles, p.CFiles, p.CXXFiles, p.MFiles, p.HFiles, p.FFiles, p.SFiles, p.SwigFiles, p.SwigCXXFiles, p.SysoFiles, p.EmbedFiles} {
			for _, name := range files {
				if filepath.IsAbs(name) || !within(p.Dir, filepath.Join(p.Dir, name)) {
					return nil, errors.New("unsafe native UI package input")
				}
				rel, err := filepath.Rel(root, filepath.Join(p.Dir, name))
				if err != nil {
					return nil, err
				}
				paths[filepath.ToSlash(rel)] = true
			}
		}
	}
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]Notice, 0, len(names))
	for _, name := range names {
		data, err := readSourceFile(root, name)
		if err != nil {
			return nil, err
		}
		result = append(result, Notice{name, bytesHash(data)})
		if destination != "" {
			path := filepath.Join(destination, filepath.FromSlash(name))
			if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return nil, err
			}
			if err = os.WriteFile(path, data, 0644); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func nativeUIRun(root, tool string, env []string, args ...string) ([]byte, error) {
	if !filepath.IsAbs(tool) {
		return nil, errors.New("SOBALINK_NATIVE_GO must name an absolute Go 1.27.3 executable")
	}
	cmd := exec.Command(tool, args...)
	cmd.Dir = filepath.Join(root, "poc", "native")
	clean := []string{}
	for _, entry := range cleanBuildEnvironment() {
		if !strings.HasPrefix(entry, "GOROOT=") {
			clean = append(clean, entry)
		}
	}
	cmd.Env = append(clean, env...)
	// Compiler diagnostics can contain runner directories or source snippets.
	// Keep bounded diagnostics private; publication logs get only this failure.
	var diagnostics limitedNativeOutput
	cmd.Stderr = &diagnostics
	data, err := cmd.Output()
	if err != nil || diagnostics.over {
		return nil, errors.New("native UI Go command failed; diagnostics withheld")
	}
	return data, nil
}

func (t *Tool) buildNativeUI(version string, target Target, commit, stage string) (*NativeUIBuild, *NoticeInventory, map[string]any, error) {
	if !nativeUITarget(target) {
		return nil, nil, nil, nil
	}
	tool := os.Getenv("SOBALINK_NATIVE_GO")
	env := []string{"GOTOOLCHAIN=local", "GOWORK=off", "GOENV=off", "GOFLAGS=", "GOEXPERIMENT=", "GOFIPS140=off", "CGO_ENABLED=0", "GOOS=" + target.OS, "GOARCH=" + target.Arch, "GOAMD64=v1", "GOARM64=v8.0"}
	run := func(args ...string) ([]byte, error) {
		if t.nativeUIRun != nil {
			return t.nativeUIRun(env, args...)
		}
		return nativeUIRun(t.Root, tool, env, args...)
	}
	identity, err := run("env", "GOVERSION", "GOHOSTOS", "GOHOSTARCH", "GOROOT")
	if err != nil {
		return nil, nil, nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(identity)), "\n")
	if len(lines) != 4 || strings.TrimSpace(lines[0]) != NativeUIGoVersion || strings.TrimSpace(lines[1]) != target.OS || strings.TrimSpace(lines[2]) != target.Arch {
		return nil, nil, nil, errors.New("native UI requires exact Go 1.27.3 on the native target")
	}
	if _, err = run("mod", "verify"); err != nil {
		return nil, nil, nil, err
	}
	list := func() ([]goPackage, error) {
		data, err := run("list", "-tags", BuildTags, "-mod=readonly", "-buildvcs=false", "-deps", "-json", ".")
		if err != nil {
			return nil, err
		}
		packages, err := parsePackages(data)
		if err != nil {
			return nil, err
		}
		return nativeUIPackages(t.Root, packages)
	}
	packages, err := list()
	if err != nil {
		return nil, nil, nil, err
	}
	share := filepath.Join(stage, "share", Product, "native-ui")
	if err = os.MkdirAll(share, 0755); err != nil {
		return nil, nil, nil, err
	}
	inputs, err := nativeUISourceInputs(t.Root, packages, filepath.Join(share, "source"))
	if err != nil {
		return nil, nil, nil, err
	}
	inventory, err := collectInventory(packages, strings.TrimSpace(lines[3]), filepath.Join(share, "licenses"), t.Root)
	if err != nil {
		return nil, nil, nil, err
	}
	inventory.Go.Version = NativeUIGoVersion
	inventory.ArchiveBasePath = "share/sobalink/native-ui"
	inventory.Scope = "Target-filtered native UI Go package/module inventory (CGO_ENABLED=0), excluding test dependencies. OS-provided graphical libraries are dynamically loaded and are not redistributed. Local product source inputs and both module locks are retained separately."
	inventory.Sources, err = sourceInventory(t.Root, packages, share)
	if err != nil {
		return nil, nil, nil, err
	}
	engine, err := engineInventory(t.Root, packages, share)
	if err != nil {
		return nil, nil, nil, err
	}
	inventory.Sources = append(inventory.Sources, engine...)
	name := "soba-native"
	if target.OS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(stage, "bin", name)
	flags := "-s -w -buildid= -X main.version=" + version + " -X main.sourceCommit=" + commit
	if target.OS == "windows" {
		flags += " -H=windowsgui"
	}
	if _, err = run("build", "-tags", BuildTags, "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags", flags, "-o", binary, "."); err != nil {
		return nil, nil, nil, err
	}
	if err = os.Chmod(binary, 0755); err != nil {
		return nil, nil, nil, err
	}
	after, err := list()
	if err != nil {
		return nil, nil, nil, err
	}
	verifiedIdentity, err := run("env", "GOVERSION", "GOHOSTOS", "GOHOSTARCH", "GOROOT")
	if err != nil || string(identity) != string(verifiedIdentity) {
		return nil, nil, nil, errors.New("native UI toolchain identity changed during build")
	}
	if _, err = run("mod", "verify"); err != nil {
		return nil, nil, nil, err
	}

	verifiedInputs, err := nativeUISourceInputs(t.Root, after, "")
	if err != nil {
		return nil, nil, nil, err
	}
	if !reflect.DeepEqual(packages, after) || !reflect.DeepEqual(inputs, verifiedInputs) {
		return nil, nil, nil, errors.New("native UI source or dependency inputs changed during build")
	}
	verifiedSources, err := sourceInventory(t.Root, after, share)
	if err != nil {
		return nil, nil, nil, err
	}
	verifiedEngine, err := engineInventory(t.Root, after, share)
	if err != nil {
		return nil, nil, nil, err
	}
	if !reflect.DeepEqual(inventory.Sources, append(verifiedSources, verifiedEngine...)) {
		return nil, nil, nil, errors.New("native UI adapted inputs changed during build")
	}
	for _, pair := range [][2]string{{"poc/native/go.mod", "go.mod"}, {"poc/native/go.sum", "go.sum"}, {"LICENSE", "LICENSE"}} {
		if err = copyFile(filepath.Join(t.Root, filepath.FromSlash(pair[0])), filepath.Join(share, pair[1])); err != nil {
			return nil, nil, nil, err
		}
	}
	hash, err := fileHash(binary)
	if err != nil {
		return nil, nil, nil, err
	}
	bom := makeSBOM(packages, inventory, version, target, commit, hash)
	namespaceNativeUISBOM(bom)
	if err = writeJSON(filepath.Join(share, "bom.cdx.json"), bom); err != nil {
		return nil, nil, nil, err
	}
	if err = writeJSON(filepath.Join(share, "third-party-notices.json"), inventory); err != nil {
		return nil, nil, nil, err
	}
	modHash, err := fileHash(filepath.Join(share, "go.mod"))
	if err != nil {
		return nil, nil, nil, err
	}
	sumHash, err := fileHash(filepath.Join(share, "go.sum"))
	if err != nil {
		return nil, nil, nil, err
	}
	bomHash, err := fileHash(filepath.Join(share, "bom.cdx.json"))
	if err != nil {
		return nil, nil, nil, err
	}
	noticeHash, err := fileHash(filepath.Join(share, "third-party-notices.json"))
	if err != nil {
		return nil, nil, nil, err
	}
	rawInputs, err := json.Marshal(inputs)
	if err != nil {
		return nil, nil, nil, err
	}
	build := &NativeUIBuild{Project: nativeUIProject, Product: "soba-native", Version: version, Commit: commit, Target: target.String(), GoVersion: NativeUIGoVersion, BuildTags: BuildTags, Trimpath: true, LDFlags: flags, GoModSHA256: modHash, GoSumSHA256: sumHash, Binary: map[string]string{"path": "bin/" + name, "sha256": hash}, SourceInputs: inputs, SourceInputsSHA256: bytesHash(rawInputs), SBOMSHA256: bomHash, NoticesSHA256: noticeHash}
	if err = writeJSON(filepath.Join(share, "build.json"), build); err != nil {
		return nil, nil, nil, err
	}
	return build, &inventory, bom, nil
}

func namespaceNativeUISBOM(bom map[string]any) {
	metadata := bom["metadata"].(map[string]any)
	app := metadata["component"].(map[string]any)
	app["name"] = "soba-native"
	app["purl"] = purl(nativeUIProject, "v"+app["version"].(string))
	app["bom-ref"] = "native-ui:" + app["bom-ref"].(string)
	for _, raw := range bom["components"].([]any) {
		component := raw.(map[string]any)
		component["bom-ref"] = "native-ui:" + component["bom-ref"].(string)
	}
	for _, raw := range bom["dependencies"].([]any) {
		dep := raw.(map[string]any)
		dep["ref"] = "native-ui:" + dep["ref"].(string)
		refs := dep["dependsOn"].([]string)
		for i := range refs {
			refs[i] = "native-ui:" + refs[i]
		}
	}
}
func mergeNativeUISBOM(bom, ui map[string]any) {
	if ui == nil {
		return
	}
	components := bom["components"].([]any)
	components = append(components, ui["metadata"].(map[string]any)["component"])
	bom["components"] = append(components, ui["components"].([]any)...)
	deps := bom["dependencies"].([]any)
	for _, raw := range deps {
		dep := raw.(map[string]any)
		if dep["ref"] == "application:sobalink" {
			refs := append(dep["dependsOn"].([]string), "native-ui:application:sobalink")
			sort.Strings(refs)
			dep["dependsOn"] = refs
		}
	}
	bom["dependencies"] = append(deps, ui["dependencies"].([]any)...)
}
