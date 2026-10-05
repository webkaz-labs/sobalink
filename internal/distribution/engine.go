package distribution

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/engineadaptation"
)

func allowedEngineModule(root string, m *goModule) bool {
	if m == nil || m.Replace == nil {
		return false
	}
	expected := filepath.Join(root, filepath.FromSlash(engineadaptation.Directory))
	r := m.Replace
	return m.Path == engineadaptation.Module && m.Version == engineadaptation.Version && !m.Main && m.Sum == "" && filepath.Clean(m.Dir) == expected && r.Path == engineadaptation.Replacement && r.Version == "" && r.Sum == "" && !r.Main && filepath.Clean(r.Dir) == expected && r.Replace == nil
}

// engineInventory is the sole release exception to the replacement ban. Every
// dependency package must resolve to the exact generated, whole-tree-verified
// module; no replacement path, version, parent link or unnoticed extra file is
// accepted. The original upstream module is an ancestor, not runtime identity.
func engineInventory(root string, packages []goPackage, share string) ([]SourceComponent, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var linked []goPackage
	for _, p := range packages {
		if p.Module != nil && p.Module.Path == engineadaptation.Module {
			if !allowedEngineModule(root, p.Module) {
				return nil, errors.New("Tailscale runtime must use only the reviewed local adaptation")
			}
			suffix := strings.TrimPrefix(p.ImportPath, engineadaptation.Module)
			if (suffix != "" && !strings.HasPrefix(suffix, "/")) || filepath.Clean(p.Dir) != filepath.Join(root, filepath.FromSlash(engineadaptation.Directory+suffix)) {
				return nil, errors.New("engine package directory is outside its exact reviewed module")
			}
			linked = append(linked, p)
		} else if p.Module != nil && p.Module.Replace != nil {
			return nil, fmt.Errorf("release packaging refuses module replacement: %s", p.Module.Path)
		}
	}
	if len(linked) == 0 {
		return nil, nil
	}
	m, err := engineadaptation.Verify(root)
	if err != nil {
		return nil, err
	}
	upstream := SourceProvenance{Module: m.Module, Version: m.Version, SourceURL: m.SourceURL, License: "BSD-3-Clause", ModuleSum: m.ModuleSum, GoModSum: m.GoModSum, TreeSHA256: m.UpstreamTreeSHA256, Files: map[string]string{}}
	for _, c := range m.Changes {
		if c.OriginalSHA256 != "" {
			upstream.Files[c.Path] = c.OriginalSHA256
		}
	}
	component := SourceComponent{Package: engineadaptation.Module, Path: engineadaptation.SourceDirectory, Modification: "Reviewed local Tailscale source adaptation for per-engine underlay admission. Not the unmodified upstream module. Original module checksums, exact changes and complete adapted source digest are retained.", Upstream: upstream, AdaptedTreeSHA256: m.AdaptedTreeSHA256, ManifestSHA256: engineadaptation.ManifestSHA256}
	inputs := map[string]string{}
	for _, rel := range []string{"go.mod", "go.sum", engineadaptation.SourceDirectory + "/manifest.json", engineadaptation.SourceDirectory + "/UPSTREAM.md"} {
		data, err := readSourceFile(root, rel)
		if err != nil {
			return nil, err
		}
		inputs[rel] = bytesHash(data)
	}
	for _, p := range linked {
		for _, list := range [][]string{p.GoFiles, p.CgoFiles, p.CFiles, p.CXXFiles, p.MFiles, p.HFiles, p.FFiles, p.SFiles, p.SwigFiles, p.SwigCXXFiles, p.SysoFiles, p.EmbedFiles} {
			for _, name := range list {
				rel, err := filepath.Rel(root, filepath.Join(p.Dir, filepath.FromSlash(name)))
				if err != nil {
					return nil, err
				}
				rel = filepath.ToSlash(rel)
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
	destination := "licenses/source/" + engineadaptation.SourceDirectory
	notices, err := collectNotices(filepath.Join(root, filepath.FromSlash(engineadaptation.Directory)), filepath.Join(share, filepath.FromSlash(destination)), false)
	if err != nil {
		return nil, err
	}
	for _, n := range notices {
		n.Path = destination + "/" + n.Path
		component.Notices = append(component.Notices, n)
	}
	for _, name := range []string{"manifest.json", "UPSTREAM.md"} {
		from := engineadaptation.SourceDirectory + "/" + name
		to := destination + "/" + name
		if err := copyFile(filepath.Join(root, filepath.FromSlash(from)), filepath.Join(share, filepath.FromSlash(to))); err != nil {
			return nil, err
		}
		component.Notices = append(component.Notices, Notice{to, inputs[from]})
	}
	return []SourceComponent{component}, nil
}
