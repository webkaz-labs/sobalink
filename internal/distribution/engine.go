package distribution

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/webkaz-labs/sobalink/internal/engineadaptation"
	"github.com/webkaz-labs/sobalink/internal/lifecycleadaptation"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

func engineModulePin(module string) (engineadaptation.ModulePin, bool) {
	for _, pin := range engineadaptation.ModulePins() {
		if pin.Module == module {
			return pin, true
		}
	}
	return engineadaptation.ModulePin{}, false
}
func engineSourcePath(module string) string {
	switch module {
	case engineadaptation.Module:
		return engineadaptation.SourceDirectory
	case "github.com/tailscale/wireguard-go":
		return lifecycleadaptation.ComponentPath(lifecycleadaptation.WireGuard)
	case "gvisor.dev/gvisor":
		return lifecycleadaptation.ComponentPath(lifecycleadaptation.GVisor)
	default:
		return ""
	}
}
func allowedEngineModule(root string, m *goModule) bool {
	if m == nil || m.Replace == nil {
		return false
	}
	pin, ok := engineModulePin(m.Path)
	if !ok {
		return false
	}
	expected := filepath.Join(root, filepath.FromSlash(pin.Directory))
	r := m.Replace
	return m.Version == pin.Version && !m.Main && m.Sum == "" && filepath.Clean(m.Dir) == expected && r.Path == pin.Replacement && r.Version == "" && r.Sum == "" && !r.Main && filepath.Clean(r.Dir) == expected && r.Replace == nil
}

type engineSource struct {
	pin       engineadaptation.ModulePin
	component SourceComponent
	// Original repository input path -> retained path beneath this component.
	retained map[string]string
}

func engineSources() ([]engineSource, error) {
	m, err := engineadaptation.Load()
	if err != nil {
		return nil, err
	}
	pin, _ := engineModulePin(m.Module)
	upstream := SourceProvenance{Module: m.Module, Version: m.Version, SourceURL: m.SourceURL, License: "BSD-3-Clause", ModuleSum: m.ModuleSum, GoModSum: m.GoModSum, TreeSHA256: m.UpstreamTreeSHA256, Files: map[string]string{}}
	for _, c := range m.Changes {
		if c.OriginalSHA256 != "" {
			upstream.Files[c.Path] = c.OriginalSHA256
		}
	}
	retained := map[string]string{"cmd/prepare-engine/main.go": "prepare-engine/main.go"}
	for _, name := range []string{"manifest.json", "UPSTREAM.md", "pin.go", "adaptation.go", "archive.go"} {
		retained[engineadaptation.SourceDirectory+"/"+name] = name
	}
	sources := []engineSource{{pin, SourceComponent{Package: m.Module, Path: engineadaptation.SourceDirectory, Modification: "Local Tailscale source adaptation for per-engine underlay admission and bounded WAN candidate discovery. Original module checksums, source changes and complete adapted tree are retained.", Upstream: upstream, AdaptedTreeSHA256: m.AdaptedTreeSHA256, ManifestSHA256: engineadaptation.ManifestSHA256}, retained}}
	for _, dependency := range lifecycleadaptation.Dependencies() {
		m, err := lifecycleadaptation.Load(dependency)
		if err != nil {
			return nil, err
		}
		pin, ok := engineModulePin(m.Module)
		if !ok {
			return nil, errors.New("unrecognized lifecycle provenance")
		}
		manifestHash, err := lifecycleadaptation.ManifestSHA256(dependency)
		if err != nil {
			return nil, err
		}
		upstream := SourceProvenance{Module: m.Module, Version: m.Version, Commit: m.Revision, SourceURL: m.SourceURL, License: m.License, ModuleSum: m.ModuleSum, GoModSum: m.GoModSum, TreeSHA256: m.UpstreamTreeSHA256, Files: map[string]string{}}
		for _, n := range m.Notices {
			upstream.Files[n.Path] = n.SHA256
		}
		for _, c := range m.Changes {
			if c.OriginalSHA256 != "" {
				if prior, ok := upstream.Files[c.Path]; ok && prior != c.OriginalSHA256 {
					return nil, errors.New("conflicting lifecycle original source identity")
				}
				upstream.Files[c.Path] = c.OriginalSHA256
			}
		}
		paths, err := lifecycleadaptation.SourceInputs(dependency)
		if err != nil {
			return nil, err
		}
		retained := map[string]string{}
		for _, path := range paths {
			switch {
			case path == lifecycleadaptation.SourceDirectory+"/"+string(dependency)+".json":
				retained[path] = "manifest.json"
			case strings.HasPrefix(path, lifecycleadaptation.SourceDirectory+"/"):
				retained[path] = strings.TrimPrefix(path, lifecycleadaptation.SourceDirectory+"/")
			case strings.HasPrefix(path, engineadaptation.SourceDirectory+"/"):
				retained[path] = "engineadaptation/" + strings.TrimPrefix(path, engineadaptation.SourceDirectory+"/")
			case path == "cmd/prepare-engine/main.go":
				retained[path] = "prepare-engine/main.go"
			default:
				return nil, errors.New("unrecognized lifecycle recipe input")
			}
		}
		modification := "Local WireGuard source adaptation for generation-bound admission, peer registration identity and joined lifecycle ownership. Original module checksums, readable overlays and complete adapted tree are retained."
		if dependency == lifecycleadaptation.GVisor {
			modification = "Local gVisor source adaptation for cancellable forwarder creation, pre-wait endpoint publication and owned handoff. Original module checksums, readable overlays and complete adapted tree are retained."
		}
		sources = append(sources, engineSource{pin, SourceComponent{Package: m.Module, Path: lifecycleadaptation.ComponentPath(dependency), Modification: modification, Upstream: upstream, AdaptedTreeSHA256: m.AdaptedTreeSHA256, ManifestSHA256: manifestHash}, retained})
	}
	return sources, nil
}

// engineInventory permits only the three named, versioned and whole-tree-verified
// adaptations. Every target-linked source must resolve inside its exact module.
// Their upstream identities are ancestors, never replacement runtime identities.
func engineInventory(root string, packages []goPackage, share string) ([]SourceComponent, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	groups := map[string][]goPackage{}
	for _, p := range packages {
		if p.Module == nil {
			continue
		}
		pin, known := engineModulePin(p.Module.Path)
		if known {
			if !allowedEngineModule(root, p.Module) {
				return nil, fmt.Errorf("adapted runtime must use its exact local source: %s", p.Module.Path)
			}
			suffix := strings.TrimPrefix(p.ImportPath, p.Module.Path)
			if (suffix != "" && !strings.HasPrefix(suffix, "/")) || filepath.Clean(p.Dir) != filepath.Join(root, filepath.FromSlash(pin.Directory+suffix)) {
				return nil, errors.New("engine package directory is outside its exact module")
			}
			groups[p.Module.Path] = append(groups[p.Module.Path], p)
		} else if p.Module.Replace != nil {
			return nil, fmt.Errorf("release packaging refuses module replacement: %s", p.Module.Path)
		}
	}
	if len(groups) == 0 {
		return nil, nil
	}
	for _, pin := range engineadaptation.ModulePins() {
		if len(groups[pin.Module]) == 0 {
			return nil, errors.New("incomplete adapted transport dependency inventory")
		}
	}
	if err := lifecycleadaptation.VerifyAll(root); err != nil {
		return nil, err
	}
	sources, err := engineSources()
	if err != nil {
		return nil, err
	}
	var result []SourceComponent
	for _, source := range sources {
		component := source.component
		inputs := map[string]string{}
		for _, rel := range []string{"go.mod", "go.sum"} {
			data, err := readSourceFile(root, rel)
			if err != nil {
				return nil, err
			}
			inputs[rel] = bytesHash(data)
		}
		for rel := range source.retained {
			data, err := readSourceFile(root, rel)
			if err != nil {
				return nil, err
			}
			inputs[rel] = bytesHash(data)
		}
		for _, p := range groups[source.pin.Module] {
			for _, list := range [][]string{p.GoFiles, p.CgoFiles, p.CFiles, p.CXXFiles, p.MFiles, p.HFiles, p.FFiles, p.SFiles, p.SwigFiles, p.SwigCXXFiles, p.SysoFiles, p.EmbedFiles} {
				for _, name := range list {
					if !fs.ValidPath(name) || strings.ContainsAny(name, "\\:\r\n") {
						return nil, errors.New("unsafe generated package source input")
					}
					rel, err := filepath.Rel(root, filepath.Join(p.Dir, filepath.FromSlash(name)))
					if err != nil {
						return nil, err
					}
					rel = filepath.ToSlash(rel)
					if !strings.HasPrefix(rel, source.pin.Directory+"/") {
						return nil, errors.New("generated build input escaped its module")
					}
					data, err := readSourceFile(root, rel)
					if err != nil {
						return nil, err
					}
					inputs[rel] = bytesHash(data)
				}
			}
		}
		names := make([]string, 0, len(inputs))
		for name := range inputs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			component.BuildInputs = append(component.BuildInputs, Notice{name, inputs[name]})
		}
		raw, err := json.Marshal(component.BuildInputs)
		if err != nil {
			return nil, err
		}
		component.BuildInputsSHA256 = bytesHash(raw)
		destination := "licenses/source/" + component.Path
		notices, err := collectNotices(filepath.Join(root, filepath.FromSlash(source.pin.Directory)), filepath.Join(share, filepath.FromSlash(destination)), false)
		if err != nil {
			return nil, err
		}
		for _, notice := range notices {
			notice.Path = destination + "/" + notice.Path
			component.Notices = append(component.Notices, notice)
		}
		for _, from := range names {
			rel, retain := source.retained[from]
			if !retain {
				continue
			}
			to := destination + "/" + rel
			if err := copyFile(filepath.Join(root, filepath.FromSlash(from)), filepath.Join(share, filepath.FromSlash(to))); err != nil {
				return nil, err
			}
			component.Notices = append(component.Notices, Notice{to, inputs[from]})
		}
		sort.Slice(component.Notices, func(i, j int) bool { return component.Notices[i].Path < component.Notices[j].Path })
		result = append(result, component)
	}
	return result, nil
}
