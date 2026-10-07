// Package lifecycleadaptation prepares and verifies the separately pinned
// WireGuard and gVisor ownership adaptations without editing upstream source.
package lifecycleadaptation

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/engineadaptation"
)

// The complete replacement source is retained as text so upstream
// packages are not accidentally compiled as part of this module.
//
//go:embed *.json sources licenses
var inputs embed.FS

const SourceDirectory = "internal/lifecycleadaptation"

type Dependency string

const (
	GVisor    Dependency = "gvisor"
	WireGuard Dependency = "wireguard"
)

// Change retains the original upstream anchor and a readable source overlay.
// Empty OriginalSHA256 denotes an added path that must not exist upstream.
type Change struct {
	Path           string `json:"path"`
	OriginalSHA256 string `json:"original_sha256"`
	AdaptedSHA256  string `json:"adapted_sha256"`
	SourcePath     string `json:"source_path"`
}

type Notice struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Schema             int      `json:"schema"`
	Module             string   `json:"module"`
	Version            string   `json:"version"`
	ModuleSum          string   `json:"module_sum"`
	GoModSum           string   `json:"go_mod_sum"`
	SourceURL          string   `json:"source_url"`
	Revision           string   `json:"revision"`
	License            string   `json:"license"`
	UpstreamTreeSHA256 string   `json:"upstream_tree_sha256"`
	AdaptedTreeSHA256  string   `json:"adapted_tree_sha256"`
	Notices            []Notice `json:"notices"`
	Changes            []Change `json:"changes"`
}

// Load authenticates the embedded manifest before archive or source processing.
func Load(dependency Dependency) (Manifest, error) {
	var m Manifest
	pin, ok := manifestPins[dependency]
	if !ok {
		return m, errors.New("unknown lifecycle dependency")
	}
	raw, err := inputs.ReadFile(string(dependency) + ".json")
	if err != nil {
		return m, err
	}
	if engineadaptation.Hash(raw) != pin {
		return m, errors.New("lifecycle manifest differs from source pin")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return m, errors.New("trailing lifecycle manifest content")
	}
	if m.Schema != 1 || m.Module == "" || m.Version == "" || m.Revision == "" || len(m.Changes) == 0 || len(m.Notices) == 0 {
		return m, errors.New("incomplete lifecycle manifest")
	}
	return m, nil
}

// PrepareArchive constructs the candidate entirely in memory from an already
// present original archive. It never invokes Go, a generator, a downloader, or
// a subprocess; never writes the module cache or a generated dependency; and
// never changes go.mod. The caller owns the returned tree only on success.
//
// Prepare persists and re-verifies this whole tree outside the module cache.
func PrepareArchive(dependency Dependency, archive string) (Manifest, map[string][]byte, error) {
	m, err := Load(dependency)
	if err != nil {
		return m, nil, err
	}
	base := engineadaptation.Manifest{
		Schema: 1, Module: m.Module, Version: m.Version,
		ModuleSum: m.ModuleSum, GoModSum: m.GoModSum, SourceURL: m.SourceURL,
		UpstreamTreeSHA256: m.UpstreamTreeSHA256, AdaptedTreeSHA256: m.AdaptedTreeSHA256,
	}
	tree, err := engineadaptation.ReadPinnedArchive(archive, base)
	if err != nil {
		return m, nil, err
	}
	for _, notice := range m.Notices {
		source, exists := tree[notice.Path]
		if !exists || !safePath(notice.Path) || engineadaptation.Hash(source) != notice.SHA256 {
			return m, nil, errors.New("upstream lifecycle license or notice differs")
		}
	}
	for _, change := range m.Changes {
		if !safePath(change.Path) || !safePath(change.SourcePath) || change.SourcePath != "sources/"+string(dependency)+"/"+change.Path+".txt" {
			return m, nil, errors.New("invalid lifecycle source path")
		}
		// Licenses and notices are inventory, never adaptation targets.
		for _, notice := range m.Notices {
			if change.Path == notice.Path {
				return m, nil, errors.New("lifecycle adaptation cannot replace an upstream notice")
			}
		}
		source, err := inputs.ReadFile(change.SourcePath)
		if err != nil {
			return m, nil, err
		}
		if engineadaptation.Hash(source) != change.AdaptedSHA256 {
			return m, nil, fmt.Errorf("lifecycle source pin mismatch: %s", change.Path)
		}
		base.Changes = append(base.Changes, engineadaptation.Change{
			Path: change.Path, OriginalSHA256: change.OriginalSHA256, AdaptedSHA256: change.AdaptedSHA256,
			Edits: []engineadaptation.Edit{{Offset: 0, Old: string(tree[change.Path]), New: string(source)}},
		})
	}
	if err := engineadaptation.ApplyPinnedChanges(tree, base); err != nil {
		return m, nil, err
	}
	return m, tree, nil
}

func safePath(path string) bool {
	return fs.ValidPath(path) && !strings.ContainsAny(path, "\\:\r\n")
}
