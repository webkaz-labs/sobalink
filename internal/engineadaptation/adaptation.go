// Package engineadaptation materializes the reviewed Tailscale source adaptation.
// It never edits the downloaded module or trusts an existing generated tree.
package engineadaptation

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const Module = "tailscale.com"
const Version = "v1.104.0"
const ModuleSum = "h1:7LgglawekeutAexqXUzSxP/Kqo3HHwF/SkcbX5HpiU4="
const GoModSum = "h1:Cb1XjScRgSOeRD7NW6uCEznme782WiOU9IxqyTWoRf0="
const Directory = ".sobalink-deps/tailscale"
const SourceDirectory = "internal/engineadaptation"
const Replacement = "./" + Directory

//go:embed manifest.json
var manifestBytes []byte

type Edit struct {
	Offset int    `json:"offset"`
	Old    string `json:"old"`
	New    string `json:"new"`
}
type Change struct {
	Path           string `json:"path"`
	OriginalSHA256 string `json:"original_sha256"`
	AdaptedSHA256  string `json:"adapted_sha256"`
	Edits          []Edit `json:"edits"`
}
type Manifest struct {
	Schema             int      `json:"schema"`
	Module             string   `json:"module"`
	Version            string   `json:"version"`
	ModuleSum          string   `json:"module_sum"`
	GoModSum           string   `json:"go_mod_sum"`
	SourceURL          string   `json:"source_url"`
	UpstreamTreeSHA256 string   `json:"upstream_tree_sha256"`
	AdaptedTreeSHA256  string   `json:"adapted_tree_sha256"`
	Changes            []Change `json:"changes"`
}

func Hash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func ManifestBytes() []byte   { return bytes.Clone(manifestBytes) }
func Load() (Manifest, error) {
	var m Manifest
	if Hash(manifestBytes) != ManifestSHA256 {
		return m, errors.New("adaptation manifest differs from reviewed source pin")
	}
	d := json.NewDecoder(bytes.NewReader(manifestBytes))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, err
	}
	if m.Schema != 1 || m.Module != Module || m.Version != Version || m.ModuleSum != ModuleSum || m.GoModSum != GoModSum || len(m.Changes) == 0 {
		return m, errors.New("invalid adaptation identity")
	}
	return m, nil
}
func regular(root, rel string) ([]byte, error) {
	if !fs.ValidPath(rel) || strings.ContainsAny(rel, "\\:\r\n") {
		return nil, errors.New("unsafe adaptation input path")
	}
	for _, part := range append([]string{""}, strings.Split(rel, "/")...) {
		root = filepath.Join(root, part)
		info, err := os.Lstat(root)
		if err != nil {
			return nil, err
		}
		if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return nil, errors.New("adaptation input contains a link or reparse point")
		}
	}
	info, err := os.Lstat(root)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("adaptation input is not a regular file")
	}
	return os.ReadFile(root)
}
func readTree(root string) (map[string][]byte, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return nil, errors.New("adapted dependency must be a real directory; run go run ./cmd/prepare-engine")
	}
	tree := map[string][]byte{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return errors.New("adapted dependency contains a link or reparse point")
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		data, err := regular(root, rel)
		if err != nil {
			return err
		}
		tree[rel] = data
		return nil
	})
	return tree, err
}
func treeHash(tree map[string][]byte) string {
	keys := make([]string, 0, len(tree))
	for k := range tree {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s  %s\n", Hash(tree[k]), k)
	}
	return Hash([]byte(b.String()))
}
func ValidateInputs(root string) (Manifest, error) {
	return validateInputs(root, false)
}

// ValidateOwnedInputs permits exactly the three pinned transport adaptations.
// The default ValidateInputs still permits only the original Tailscale entry.
func ValidateOwnedInputs(root string) (Manifest, error) {
	return validateInputs(root, true)
}

func validateInputs(root string, owned bool) (Manifest, error) {
	m, err := Load()
	if err != nil {
		return m, err
	}
	raw, err := regular(root, SourceDirectory+"/manifest.json")
	if err != nil {
		return m, err
	}
	if !bytes.Equal(raw, manifestBytes) {
		return m, errors.New("checkout adaptation manifest differs from build-tool pin")
	}
	mod, err := regular(root, "go.mod")
	if err != nil {
		return m, err
	}
	pins := ModulePins()
	if !owned {
		pins = pins[:1]
	}
	replacements := regexp.MustCompile(`(?m)^\s*replace\b`).FindAll(mod, -1)
	if len(replacements) != len(pins) {
		return m, errors.New("module replacements differ from the exact selected adaptation set")
	}
	for _, pin := range pins {
		exact := "replace " + pin.Module + " " + pin.Version + " => " + pin.Replacement
		if strings.Count("\n"+string(mod), "\n"+exact+"\n") != 1 {
			return m, errors.New("missing, duplicate or changed pinned module replacement")
		}
		if owned {
			// Require the selected upstream version as well as its replacement.
			// Standalone and grouped require directives are both accepted.
			count := 0
			for _, line := range strings.Split(string(mod), "\n") {
				line, _, _ = strings.Cut(line, "//")
				fields := strings.Fields(line)
				if len(fields) > 0 && fields[0] == "require" {
					fields = fields[1:]
				}
				if len(fields) > 0 && fields[0] == pin.Module {
					if len(fields) != 2 || fields[1] != pin.Version {
						return m, errors.New("pinned adapted module requirement changed")
					}
					count++
				}
			}
			if count != 1 {
				return m, errors.New("pinned adapted module requirement missing or duplicated")
			}
		}
	}
	sums, err := regular(root, "go.sum")
	if err != nil {
		return m, err
	}
	for _, pin := range pins {
		for _, expected := range []struct{ version, sum string }{{pin.Version, pin.ModuleSum}, {pin.Version + "/go.mod", pin.GoModSum}} {
			matches := 0
			for _, line := range strings.Split(string(sums), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 2 && fields[0] == pin.Module && fields[1] == expected.version {
					if len(fields) != 3 || fields[2] != expected.sum {
						return m, errors.New("original adapted module checksum changed")
					}
					matches++
				}
			}
			if matches != 1 {
				return m, errors.New("original adapted module checksum missing or duplicated")
			}
		}
	}
	return m, nil
}
func Verify(root string) (Manifest, error) {
	return verify(root, false)
}

func VerifyOwned(root string) (Manifest, error) { return verify(root, true) }

func verify(root string, owned bool) (Manifest, error) {
	m, err := validateInputs(root, owned)
	if err != nil {
		return m, err
	}
	return m, VerifyPinnedTree(root, Directory, m.AdaptedTreeSHA256)
}

// moduleHash implements Go's documented dirhash Hash1 format. The archive's
// names include module@version/, exactly as used for its go.sum h1 value.
func moduleHash(tree map[string][]byte) string {
	keys := make([]string, 0, len(tree))
	for k := range tree {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s  %s\n", Hash(tree[k]), k)
	}
	return "h1:" + base64.StdEncoding.EncodeToString(h.Sum(nil))
}
func readZip(path string, m Manifest) (map[string][]byte, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	full := map[string][]byte{}
	tree := map[string][]byte{}
	prefix := m.Module + "@" + m.Version + "/"
	for _, f := range z.File {
		if !strings.HasPrefix(f.Name, prefix) {
			return nil, errors.New("unexpected upstream archive path")
		}
		rel := strings.TrimPrefix(f.Name, prefix)
		if f.FileInfo().IsDir() {
			continue
		}
		if !fs.ValidPath(rel) || strings.ContainsAny(rel, "\\:\r\n") || !f.Mode().IsRegular() {
			return nil, errors.New("unsafe upstream archive entry")
		}
		if _, ok := tree[rel]; ok {
			return nil, errors.New("duplicate upstream archive entry")
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			return nil, err
		}
		full[f.Name] = data
		tree[rel] = data
	}
	if moduleHash(full) != m.ModuleSum || moduleHash(map[string][]byte{"go.mod": tree["go.mod"]}) != m.GoModSum || treeHash(tree) != m.UpstreamTreeSHA256 {
		return nil, errors.New("upstream module archive checksum or source digest mismatch")
	}
	return tree, nil
}
func apply(tree map[string][]byte, m Manifest) error {
	seen := map[string]bool{}
	for _, c := range m.Changes {
		if !fs.ValidPath(c.Path) || strings.ContainsAny(c.Path, "\\:\r\n") || seen[c.Path] || c.Path == "go.mod" || c.Path == "go.sum" {
			return errors.New("invalid or duplicate adaptation target")
		}
		seen[c.Path] = true
		data, exists := tree[c.Path]
		if c.OriginalSHA256 == "" {
			if exists {
				return errors.New("new adaptation target already exists")
			}
		} else if !exists || Hash(data) != c.OriginalSHA256 {
			return errors.New("original adaptation source hash mismatch")
		}
		end := len(data)
		for i := len(c.Edits) - 1; i >= 0; i-- {
			e := c.Edits[i]
			if e.Offset < 0 || e.Offset+len(e.Old) > end || string(data[e.Offset:e.Offset+len(e.Old)]) != e.Old {
				return errors.New("adaptation hunk does not exactly match source")
			}
			data = append(append(append([]byte{}, data[:e.Offset]...), []byte(e.New)...), data[e.Offset+len(e.Old):]...)
			end = e.Offset
		}
		if Hash(data) != c.AdaptedSHA256 {
			return errors.New("adapted source hash mismatch")
		}
		tree[c.Path] = data
	}
	if treeHash(tree) != m.AdaptedTreeSHA256 {
		return errors.New("adapted tree digest mismatch")
	}
	return nil
}

// Prepare is idempotent but never silently repairs changed generated source.
// Downloads populate only Go's original module cache. Source is read from the
// verified zip, never from a potentially modified extracted cache directory.
func Prepare(root string) (Manifest, error) {
	return prepare(root, false)
}

func PrepareOwned(root string) (Manifest, error) { return prepare(root, true) }

func prepare(root string, owned bool) (Manifest, error) {
	m, err := validateInputs(root, owned)
	if err != nil {
		return m, err
	}
	err = MaterializePinned(root, Directory, m, func(archive string) (map[string][]byte, error) {
		tree, err := readZip(archive, m)
		if err != nil {
			return nil, err
		}
		if err := apply(tree, m); err != nil {
			return nil, err
		}
		return tree, nil
	})
	if err != nil {
		return m, err
	}
	return verify(root, owned)
}

// MaterializePinned obtains only the exact original archive, builds a separate
// verified tree, and never changes extracted module-cache source. The assembler
// must authenticate its inputs; its complete output digest is checked here too.
func MaterializePinned(root, directory string, m Manifest, assemble func(string) (map[string][]byte, error)) error {
	if !validOutputDirectory(directory) {
		return errors.New("unrecognized adapted dependency output")
	}
	matched := false
	for _, pin := range ModulePins() {
		if directory == pin.Directory && m.Module == pin.Module && m.Version == pin.Version && m.ModuleSum == pin.ModuleSum && m.GoModSum == pin.GoModSum {
			matched = true
		}
	}
	if !matched {
		return errors.New("unrecognized adapted dependency identity")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if _, err := regular(root, "go.mod"); err != nil {
		return err
	}
	cacheCommand := exec.Command("go", "env", "GOMODCACHE")
	cacheCommand.Env = append(os.Environ(), "GOWORK=off", "GOENV=off", "GOFLAGS=", "GOTOOLCHAIN=local")
	cacheRaw, err := cacheCommand.Output()
	if err != nil {
		return err
	}
	cache, err := canonicalPath(strings.TrimSpace(string(cacheRaw)))
	if err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	output := filepath.Join(resolvedRoot, filepath.FromSlash(directory))
	if overlaps(cache, output) {
		return errors.New("generated dependency must be outside the Go module cache")
	}
	parent := filepath.Join(root, ".sobalink-deps")
	dest := filepath.Join(root, filepath.FromSlash(directory))
	if err := os.Mkdir(parent, 0o755); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return errors.New("dependency output parent must be a real directory")
	}
	// An atomic directory lock prevents concurrent preparers from replacing work.
	lock := filepath.Join(parent, ".prepare-lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		return errors.New("engine preparation already active, or stale .sobalink-deps/.prepare-lock; inspect before retrying")
	}
	defer os.Remove(lock)
	if _, err := os.Lstat(dest); err == nil {
		return VerifyPinnedTree(root, directory, m.AdaptedTreeSHA256)
	} else if !os.IsNotExist(err) {
		return err
	}
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(directory)+"-")
	if err != nil {
		return err
	}
	defer removeStage(stage)
	cmd := exec.Command("go", "mod", "download", "-json", m.Module+"@"+m.Version)
	cmd.Dir = stage
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOENV=off", "GOFLAGS=", "GOTOOLCHAIN=local")
	raw, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("download original pinned module (warm cache supports GOPROXY=off): %w", err)
	}
	var download struct {
		Path, Version, Zip, Sum, GoModSum string
		Error                             any
	}
	if err := json.Unmarshal(raw, &download); err != nil {
		return err
	}
	if download.Path != m.Module || download.Version != m.Version || download.Sum != m.ModuleSum || download.GoModSum != m.GoModSum || download.Zip == "" || download.Error != nil {
		return errors.New("downloaded module identity or checksum differs from reviewed pin")
	}
	tree, err := assemble(download.Zip)
	if err != nil {
		return err
	}
	if treeHash(tree) != m.AdaptedTreeSHA256 {
		return errors.New("assembled dependency differs from pinned complete tree")
	}
	for path, data := range tree {
		if !fs.ValidPath(path) || strings.ContainsAny(path, "\\:\r\n") {
			return errors.New("unsafe assembled source path")
		}
		p := filepath.Join(stage, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o444); err != nil {
			return err
		}
	}
	// Source files are read-only, matching Go's extracted module convention.
	// Directories remain traversable/removable for an explicit reviewed reset;
	// every prepare/package build still rechecks the complete tree.
	if err := filepath.WalkDir(stage, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o755)
		}
		return nil
	}); err != nil {
		return err
	}
	// Re-read staging before publication. Reject a changed or linked entry.
	staged, err := readTree(stage)
	if err != nil {
		return err
	}
	if treeHash(staged) != m.AdaptedTreeSHA256 {
		return errors.New("staged dependency drift")
	}
	if err := os.Rename(stage, dest); err != nil {
		return err
	}
	return VerifyPinnedTree(root, directory, m.AdaptedTreeSHA256)
}

func overlaps(a, b string) bool {
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Only used for a freshly allocated private staging directory, never an
// existing generated dependency or an upstream cache path.
func removeStage(root string) {
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type()&(fs.ModeSymlink|fs.ModeIrregular) == 0 {
			return os.Chmod(path, 0o700)
		}
		return err
	})
	_ = os.RemoveAll(root)
}

// Resolve the nearest existing ancestor too, so an as-yet-uncreated cache
// beneath a symlink cannot alias the generated source output.
func canonicalPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	for {
		if _, err := os.Lstat(path); err == nil {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", errors.New("cannot resolve cache path")
		}
		suffix = append(suffix, filepath.Base(path))
		path = parent
	}
}
