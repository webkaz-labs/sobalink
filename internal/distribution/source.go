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
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// SourceProvenance records the original, unmodified upstream inputs. Its hashes
// must never be interpreted as the hashes of our adapted runtime sources.
type SourceProvenance struct {
	Module     string            `json:"module"`
	Version    string            `json:"version"`
	Commit     string            `json:"commit"`
	SourceURL  string            `json:"source_url"`
	License    string            `json:"license"`
	Files      map[string]string `json:"files"`
	ModuleSum  string            `json:"module_sum,omitempty"`
	GoModSum   string            `json:"go_mod_sum,omitempty"`
	TreeSHA256 string            `json:"tree_sha256,omitempty"`
}

type SourceComponent struct {
	Package           string           `json:"package"`
	Path              string           `json:"source_path"`
	Modification      string           `json:"modification"`
	Upstream          SourceProvenance `json:"upstream"`
	Notices           []Notice         `json:"notices"`
	BuildInputs       []Notice         `json:"build_inputs"`
	BuildInputsSHA256 string           `json:"build_inputs_sha256"`
	AdaptedTreeSHA256 string           `json:"adapted_tree_sha256,omitempty"`
	ManifestSHA256    string           `json:"manifest_sha256,omitempty"`
}

const routecatPath = "internal/routecat"
const routecatPackage = Project + "/" + routecatPath

// This reviewed pin is intentionally independent of UPSTREAM.json: changing a
// provenance file and its license together must not silently substitute another
// component. Updating the upstream pin requires reviewing this inventory too.
// These original file hashes were checked against the pinned official module.
func routecatUpstream() SourceProvenance {
	return SourceProvenance{
		Module:    "github.com/tailscale/tailcat",
		Version:   "v0.7.1-0.20260929145319-b4dc28e8aa89",
		Commit:    "b4dc28e8aa89",
		SourceURL: "https://github.com/tailscale/tailcat/tree/b4dc28e8aa89",
		License:   "BSD-3-Clause",
		Files: map[string]string{
			"tailcat.go": "c93dc45dfe47f3a35bd026322ceb8bc011364641632b11134c74b7bf600ee6eb",
			"wire.go":    "24dfea08aef53fa49a5a3797a5243e52e8c9f5948ecef88c8329d6889d8b27b8",
			"disco.go":   "b5b970d0c76a6126504b9a1e130676dd97e2e88d0f460d0646c1daf023de2892",
			"listen.go":  "24e12e05c10b1dfd443b0a5cb8148e11c093b158ea2ed24e5cb43b73aa41652d",
			"LICENSE":    "a7ca6186a7963a0a60740f6047760eecd7a0234e8c38bd7e1e0bbcb324bda45b",
		},
	}
}

// readSourceFile rejects every interior link/reparse point, not just the final
// filename. The checkout root may itself be reached through an OS-managed parent
// path, but unlike a toolchain notice root it must be a real directory.
func readSourceFile(root, rel string) ([]byte, error) {
	if !fs.ValidPath(rel) || strings.ContainsAny(rel, "\\:") {
		return nil, fmt.Errorf("invalid source input path: %q", rel)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return nil, errors.New("source inventory root must be a real directory")
	}
	parts := strings.Split(rel, "/")
	path := root
	for i, part := range parts {
		path = filepath.Join(path, part)
		info, err = os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("source input %s: %w", rel, err)
		}
		if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return nil, fmt.Errorf("link or reparse entry in source input: %s", rel)
		}
		if i < len(parts)-1 && !info.IsDir() || i == len(parts)-1 && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("non-regular source input: %s", rel)
		}
	}
	return os.ReadFile(path)
}

func bytesHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// uniqueSourceObject rejects ambiguous duplicate keys before standard decoding.
// Provenance is shared verbatim with other consumers, so last-key-wins JSON is
// not an acceptable way to resolve conflicting upstream identities or hashes.
func uniqueSourceObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("source provenance must be a JSON object")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid source provenance key")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate source provenance key: %s", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("source provenance contains trailing data")
	}
	return fields, nil
}

// routecatInventory augments the Go module inventory only when the adapted
// main-module package is actually in the target's non-test dependency closure.
func routecatInventory(root string, packages []goPackage, share string) ([]SourceComponent, error) {
	var linked *goPackage
	for i := range packages {
		if packages[i].ImportPath == routecatPackage {
			if linked != nil {
				return nil, errors.New("duplicate adapted source package")
			}
			linked = &packages[i]
		}
	}
	if linked == nil {
		return []SourceComponent{}, nil
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if linked.Module == nil || !linked.Module.Main || linked.Module.Replace != nil || linked.Module.Path != Project || filepath.Clean(linked.Module.Dir) != root || filepath.Clean(linked.Dir) != filepath.Join(root, filepath.FromSlash(routecatPath)) {
		return nil, errors.New("adapted source package is not from the expected main-module directory")
	}
	raw, err := readSourceFile(root, routecatPath+"/UPSTREAM.json")
	if err != nil {
		return nil, err
	}
	fields, err := uniqueSourceObject(raw)
	if err != nil {
		return nil, err
	}
	for field := range fields {
		switch field {
		case "module", "version", "commit", "source_url", "license", "files":
		default:
			return nil, fmt.Errorf("unknown source provenance key: %s", field)
		}
	}
	if _, err := uniqueSourceObject(fields["files"]); err != nil {
		return nil, err
	}
	var upstream SourceProvenance
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&upstream); err != nil {
		return nil, fmt.Errorf("adapted source provenance: %w", err)
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("adapted source provenance contains trailing data")
	}
	if !reflect.DeepEqual(upstream, routecatUpstream()) {
		return nil, errors.New("adapted source provenance differs from the reviewed upstream pin")
	}
	component := SourceComponent{
		Package: routecatPackage, Path: routecatPath,
		Modification: "Adapted main-module source, not the unmodified upstream Go module. BSD-3-Clause applies to the explicitly marked upstream-derived files; other local inputs retain their own notices.",
		Upstream:     upstream,
	}
	// Hash selected compile/embed inputs, the module graph, and the exact
	// retained provenance. Tests and ignored target files are not runtime inputs.
	inputs := map[string][]byte{routecatPath + "/UPSTREAM.json": raw}
	selected := map[string]bool{}
	for _, list := range [][]string{linked.GoFiles, linked.CgoFiles, linked.CFiles, linked.CXXFiles, linked.MFiles, linked.HFiles, linked.FFiles, linked.SFiles, linked.SwigFiles, linked.SwigCXXFiles, linked.SysoFiles, linked.EmbedFiles} {
		for _, rel := range list {
			if !fs.ValidPath(rel) || strings.ContainsAny(rel, "\\:") || strings.HasSuffix(rel, "_test.go") {
				return nil, fmt.Errorf("invalid adapted source build input: %q", rel)
			}
			path := routecatPath + "/" + rel
			data, err := readSourceFile(root, path)
			if err != nil {
				return nil, err
			}
			inputs[path] = data
			selected[rel] = true
		}
	}
	for name := range upstream.Files {
		if name == "LICENSE" {
			continue
		}
		if !selected[name] {
			return nil, fmt.Errorf("adapted source build inventory is missing %s", name)
		}
		data := inputs[routecatPath+"/"+name]
		if !bytes.HasPrefix(data, []byte("// Copyright (c) Tailscale Inc & contributors\n// SPDX-License-Identifier: BSD-3-Clause\n")) {
			return nil, fmt.Errorf("adapted source is missing its explicit upstream notice: %s", name)
		}
	}
	for _, rel := range []string{"go.mod", "go.sum", routecatPath + "/LICENSE", routecatPath + "/UPSTREAM.md"} {
		data, err := readSourceFile(root, rel)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return nil, fmt.Errorf("empty adapted source build input: %s", rel)
		}
		inputs[rel] = data
	}
	if bytesHash(inputs[routecatPath+"/LICENSE"]) != upstream.Files["LICENSE"] {
		return nil, errors.New("adapted source LICENSE differs from the reviewed upstream license")
	}
	paths := make([]string, 0, len(inputs))
	for path := range inputs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		component.BuildInputs = append(component.BuildInputs, Notice{path, bytesHash(inputs[path])})
	}
	manifest, err := json.Marshal(component.BuildInputs)
	if err != nil {
		return nil, err
	}
	component.BuildInputsSHA256 = bytesHash(manifest)
	for _, name := range []string{"LICENSE", "UPSTREAM.json", "UPSTREAM.md"} {
		path := "licenses/source/" + routecatPath + "/" + name
		target := filepath.Join(share, filepath.FromSlash(path))
		if err = os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		data := inputs[routecatPath+"/"+name]
		if err = os.WriteFile(target, data, 0o644); err != nil {
			return nil, err
		}
		component.Notices = append(component.Notices, Notice{path, bytesHash(data)})
	}
	return []SourceComponent{component}, nil
}

func sourceRef(component SourceComponent) string { return "source:" + component.Path }

func sourceSBOMComponent(component SourceComponent, version string) map[string]any {
	// Do not attach the upstream purl/version to our modified component. Its
	// ancestor is provenance only and is not asserted to be a runtime module.
	upstream := component.Upstream
	originalHashes, _ := json.Marshal(upstream.Files)
	inputs, _ := json.Marshal(component.BuildInputs)
	return map[string]any{
		"type": "library", "bom-ref": sourceRef(component), "name": component.Package, "version": version,
		"description": component.Modification,
		"pedigree": map[string]any{"ancestors": []any{map[string]any{
			"type": "library", "name": upstream.Module, "version": upstream.Version, "purl": purl(upstream.Module, upstream.Version),
			"licenses":           []any{map[string]any{"license": map[string]string{"id": upstream.License}}},
			"externalReferences": []any{map[string]string{"type": "vcs", "url": upstream.SourceURL}},
			"properties":         []any{map[string]string{"name": "source:commit", "value": upstream.Commit}, map[string]string{"name": "source:original-files:sha256", "value": string(originalHashes)}},
		}}},
		"licenses": []any{map[string]any{"license": map[string]string{"name": "See retained source licenses and per-file notices; adapted source inputs have separate provenance"}}},
		"properties": []any{
			map[string]string{"name": "source:path", "value": component.Path},
			map[string]string{"name": "source:modified", "value": "true"},
			map[string]string{"name": "source:adapted-tree:sha256", "value": component.AdaptedTreeSHA256},
			map[string]string{"name": "source:manifest:sha256", "value": component.ManifestSHA256},
			map[string]string{"name": "source:upstream-module:sum", "value": upstream.ModuleSum},
			map[string]string{"name": "source:upstream-go-mod:sum", "value": upstream.GoModSum},
			map[string]string{"name": "source:upstream-tree:sha256", "value": upstream.TreeSHA256},
			map[string]string{"name": "source:build-inputs:sha256", "value": component.BuildInputsSHA256},
			map[string]string{"name": "source:build-inputs", "value": string(inputs)},
			map[string]string{"name": "source:notices", "value": "See third-party-notices.json and licenses/source/" + component.Path + "/"},
		},
	}
}
