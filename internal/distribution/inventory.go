package distribution

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type goModule struct {
	Path, Version, Dir, Sum string
	Main                    bool
	Replace                 *goModule
}
type goPackage struct {
	ImportPath   string
	Dir          string
	GoFiles      []string
	CgoFiles     []string
	CFiles       []string
	CXXFiles     []string
	MFiles       []string
	HFiles       []string
	FFiles       []string
	SFiles       []string
	SwigFiles    []string
	SwigCXXFiles []string
	SysoFiles    []string
	EmbedFiles   []string
	Imports      []string
	Module       *goModule
	Error        *struct{ Err string }
}

func parsePackages(data []byte) ([]goPackage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var packages []goPackage
	for {
		var p goPackage
		err := decoder.Decode(&p)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if p.Error != nil {
			return nil, errors.New(p.Error.Err)
		}
		if p.ImportPath == "" {
			return nil, errors.New("package inventory is missing an import path")
		}
		packages = append(packages, p)
	}
	if len(packages) == 0 {
		return nil, errors.New("empty package inventory")
	}
	return packages, nil
}

type Notice struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type ModuleNotices struct {
	Module  string   `json:"module"`
	Version string   `json:"version"`
	Sum     string   `json:"go_module_sum,omitempty"`
	Notices []Notice `json:"notices"`
}
type NoticeInventory struct {
	ArchiveBasePath string            `json:"archive_base_path"`
	Scope           string            `json:"scope"`
	Modules         []ModuleNotices   `json:"modules"`
	Go              ModuleNotices     `json:"go_standard_library"`
	Frontend        []FrontendModule  `json:"frontend_modules"`
	Sources         []SourceComponent `json:"source_components"`
}

func noticeName(name string) bool {
	name = strings.ToUpper(name)
	for _, prefix := range []string{"LICENSE", "LICENCE", "COPYING", "NOTICE", "COPYRIGHT", "UNLICENSE", "PATENTS", "AUTHORS"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
func collectNotices(root, destination string, skipToolSources bool) ([]Notice, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var notices []Notice
	// fs.WalkDir Stat-follows only the explicitly supplied root. This matters
	// for Windows toolcache junctions: since Go 1.23 they are ModeIrregular,
	// so EvalSymlinks leaves a final-component junction in place and
	// filepath.WalkDir's Lstat would silently visit only that non-directory.
	// Descendants still come from ReadDir and are never link-followed.
	err = fs.WalkDir(os.DirFS(root), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if rel == "." {
			if !entry.IsDir() {
				return errors.New("notice source must be a directory")
			}
			return nil
		}
		if entry.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return fmt.Errorf("link or reparse entry in notice source: %s (%s)", rel, entry.Type())
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "testdata" || (skipToolSources && rel == "src/cmd") {
				return filepath.SkipDir
			}
			return nil
		}
		// Reject interior links/reparse points even when their names do not
		// look like licenses: skipping one could omit an entire notice subtree.
		// The explicitly supplied root is the only link-following exception.
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular entry in notice source: %s (%s)", rel, entry.Type())
		}
		if !noticeName(entry.Name()) {
			return nil
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		if !fs.ValidPath(rel) || !within(root, path) {
			return errors.New("notice escapes source root")
		}
		target := filepath.Join(destination, filepath.FromSlash(rel))
		if err = copyFile(path, target); err != nil {
			return err
		}
		hash, err := fileHash(target)
		if err != nil {
			return err
		}
		notices = append(notices, Notice{filepath.ToSlash(rel), hash})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(notices) == 0 {
		rootType := "unavailable"
		if info, err := os.Lstat(root); err == nil {
			rootType = info.Mode().String()
		}
		licenseType := "unavailable"
		if info, err := os.Lstat(filepath.Join(root, "LICENSE")); err == nil {
			licenseType = info.Mode().String()
		} else if os.IsNotExist(err) {
			licenseType = "missing"
		}
		return nil, fmt.Errorf("no license or notice files found (root Lstat mode: %s; root LICENSE: %s)", rootType, licenseType)
	}
	return notices, nil
}

func collectInventory(packages []goPackage, goRoot, out string) (NoticeInventory, error) {
	inventory := NoticeInventory{ArchiveBasePath: "share/sobalink", Scope: "Target-filtered Go package/module inventory (CGO_ENABLED=0), excluding test dependencies. Module-level notice files are preserved without license classification. Explicit adapted main-module source components are inventoried separately from upstream Go modules; other embedded source-only licenses still require release review."}
	modules := map[string]goModule{}
	for _, p := range packages {
		if p.Module == nil || p.Module.Main {
			continue
		}
		m := *p.Module
		if m.Replace != nil {
			return inventory, fmt.Errorf("release packaging refuses module replacements: %s", m.Path)
		}
		if m.Path == "" || m.Version == "" || m.Dir == "" || m.Sum == "" {
			return inventory, fmt.Errorf("incomplete module provenance for %s", m.Path)
		}
		if prior, ok := modules[m.Path]; ok && (prior.Version != m.Version || prior.Sum != m.Sum) {
			return inventory, fmt.Errorf("conflicting module %s", m.Path)
		}
		modules[m.Path] = m
	}
	names := make([]string, 0, len(modules))
	for name := range modules {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		m := modules[name]
		key := moduleKey(m.Path, m.Version)
		notices, err := collectNotices(m.Dir, filepath.Join(out, key), false)
		if err != nil {
			return inventory, fmt.Errorf("%s@%s notices: %w", m.Path, m.Version, err)
		}
		for i := range notices {
			notices[i].Path = "licenses/" + key + "/" + notices[i].Path
		}
		inventory.Modules = append(inventory.Modules, ModuleNotices{m.Path, m.Version, m.Sum, notices})
	}
	goDest := filepath.Join(out, "go")
	goNotices, err := collectNotices(goRoot, goDest, true)
	if err != nil {
		return inventory, fmt.Errorf("Go notices: %w", err)
	}
	// Several standard-library files carry extra Sun, Cephes or Fiat-crypto
	// notices embedded in source. Preserve the full files rather than guessing
	// legal text boundaries. Missing files across toolchain versions are skipped.
	for _, rel := range []string{"src/math/acosh.go", "src/math/asinh.go", "src/math/atanh.go", "src/math/cbrt.go", "src/math/erf.go", "src/math/exp.go", "src/math/expm1.go", "src/math/j0.go", "src/math/j1.go", "src/math/jn.go", "src/math/lgamma.go", "src/math/log.go", "src/math/log1p.go", "src/math/remainder.go", "src/math/sqrt.go", "src/math/atan.go", "src/math/gamma.go", "src/math/sin.go", "src/math/tan.go", "src/math/tanh.go", "src/crypto/internal/fips140/edwards25519/scalar.go", "src/crypto/internal/fips140/aes/aes_generic.go"} {
		path := filepath.Join(goRoot, filepath.FromSlash(rel))
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return inventory, err
		}
		target := filepath.Join(goDest, filepath.FromSlash(rel))
		if err := copyFile(path, target); err != nil {
			return inventory, err
		}
		hash, err := fileHash(target)
		if err != nil {
			return inventory, err
		}
		goNotices = append(goNotices, Notice{rel, hash})
	}
	if _, err := os.Stat(filepath.Join(goDest, "LICENSE")); err != nil {
		return inventory, fmt.Errorf("mandatory Go LICENSE: %w", err)
	}
	for i := range goNotices {
		goNotices[i].Path = "licenses/go/" + goNotices[i].Path
	}
	sort.Slice(goNotices, func(i, j int) bool { return goNotices[i].Path < goNotices[j].Path })
	inventory.Go = ModuleNotices{Module: "Go standard library", Version: GoVersion, Notices: goNotices}
	return inventory, nil
}
func moduleKey(path, version string) string {
	sum := sha256.Sum256([]byte(path + "@" + version))
	return hex.EncodeToString(sum[:])
}
func moduleRef(m *goModule) string {
	if m == nil {
		return "golang:stdlib"
	}
	if m.Main {
		return "application:sobalink"
	}
	return "golang:" + moduleKey(m.Path, m.Version)
}
func purl(path, version string) string {
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "pkg:golang/" + strings.Join(parts, "/") + "@" + url.PathEscape(version)
}
func makeSBOM(packages []goPackage, notices NoticeInventory, version string, target Target, commit, binaryHash string) map[string]any {
	root := "application:sobalink"
	goRef := "golang:stdlib"
	components := []any{map[string]any{"type": "library", "bom-ref": goRef, "name": "Go standard library", "version": GoVersion, "licenses": []any{map[string]any{"license": map[string]string{"name": "See licenses/go/ and third-party-notices.json"}}}}}
	for _, m := range notices.Modules {
		components = append(components, map[string]any{"type": "library", "bom-ref": "golang:" + moduleKey(m.Module, m.Version), "name": m.Module, "version": m.Version, "purl": purl(m.Module, m.Version), "properties": []any{map[string]string{"name": "go:module:sum", "value": m.Sum}}, "licenses": []any{map[string]any{"license": map[string]string{"name": "See third-party-notices.json for original notices (not classified)"}}}})
	}
	for _, component := range notices.Sources {
		components = append(components, sourceSBOMComponent(component, version))
	}
	for _, m := range notices.Frontend {
		components = append(components, map[string]any{"type": "library", "bom-ref": "npm:" + moduleKey(m.Name, m.Version), "name": m.Name, "version": m.Version, "purl": npmPURL(m.Name, m.Version), "properties": []any{map[string]string{"name": "npm:integrity", "value": m.Integrity}}, "licenses": []any{map[string]any{"license": map[string]string{"name": "See third-party-notices.json for original notices (not classified)"}}}})
	}
	refs := map[string]string{}
	edges := map[string]map[string]bool{root: {}, goRef: {}}
	for _, m := range notices.Frontend {
		ref := "npm:" + moduleKey(m.Name, m.Version)
		edges[root][ref] = true
		edges[ref] = map[string]bool{}
	}
	sourceRefs := map[string]string{}
	for _, component := range notices.Sources {
		ref := sourceRef(component)
		sourceRefs[component.Package] = ref
		edges[root][ref] = true
		edges[ref] = map[string]bool{}
	}
	for _, p := range packages {
		ref := moduleRef(p.Module)
		if source, ok := sourceRefs[p.ImportPath]; ok {
			ref = source
		}
		refs[p.ImportPath] = ref
		if edges[ref] == nil {
			edges[ref] = map[string]bool{}
		}
	}
	for _, p := range packages {
		ref := refs[p.ImportPath]
		for _, imp := range p.Imports {
			dep, ok := refs[imp]
			if ok && dep != ref {
				edges[ref][dep] = true
			}
		}
	}
	keys := make([]string, 0, len(edges))
	for ref := range edges {
		keys = append(keys, ref)
	}
	sort.Strings(keys)
	dependencies := []any{}
	for _, ref := range keys {
		deps := []string{}
		for dep := range edges[ref] {
			deps = append(deps, dep)
		}
		sort.Strings(deps)
		dependencies = append(dependencies, map[string]any{"ref": ref, "dependsOn": deps})
	}
	return map[string]any{"bomFormat": "CycloneDX", "specVersion": "1.5", "version": 1, "metadata": map[string]any{"component": map[string]any{"type": "application", "bom-ref": root, "name": Product, "version": version, "purl": purl(Project, "v"+version), "hashes": []any{map[string]string{"alg": "SHA-256", "content": binaryHash}}}, "properties": []any{map[string]string{"name": "inventory:scope", "value": notices.Scope}, map[string]string{"name": "build:target", "value": target.String()}, map[string]string{"name": "source:commit", "value": commit}, map[string]string{"name": "build:cgo", "value": "0"}}}, "components": components, "dependencies": dependencies}
}
