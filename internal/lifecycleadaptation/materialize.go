package lifecycleadaptation

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/engineadaptation"
)

func Dependencies() []Dependency { return []Dependency{WireGuard, GVisor} }

func Directory(dependency Dependency) string {
	switch dependency {
	case WireGuard:
		return ".sobalink-deps/wireguard"
	case GVisor:
		return ".sobalink-deps/gvisor"
	default:
		return ""
	}
}

func ComponentPath(dependency Dependency) string { return SourceDirectory + "/" + string(dependency) }

func ManifestSHA256(dependency Dependency) (string, error) {
	if _, err := Load(dependency); err != nil {
		return "", err
	}
	return manifestPins[dependency], nil
}

func archivePin(m Manifest) engineadaptation.Manifest {
	return engineadaptation.Manifest{Schema: 1, Module: m.Module, Version: m.Version,
		ModuleSum: m.ModuleSum, GoModSum: m.GoModSum, SourceURL: m.SourceURL,
		UpstreamTreeSHA256: m.UpstreamTreeSHA256, AdaptedTreeSHA256: m.AdaptedTreeSHA256}
}

// SourceInputs lists this component's checked-in recipe for distribution
// retention. The shared engine manifest/pin belongs to the separate engine
// component; target-linked generated source is inventoried separately.
func SourceInputs(dependency Dependency) ([]string, error) {
	m, err := Load(dependency)
	if err != nil {
		return nil, err
	}
	paths := []string{SourceDirectory + "/" + string(dependency) + ".json", SourceDirectory + "/UPSTREAM.md",
		SourceDirectory + "/pin.go", SourceDirectory + "/adaptation.go", SourceDirectory + "/materialize.go",
		engineadaptation.SourceDirectory + "/adaptation.go", engineadaptation.SourceDirectory + "/archive.go", "cmd/prepare-engine/main.go"}
	for _, change := range m.Changes {
		paths = append(paths, SourceDirectory+"/"+change.SourcePath)
	}
	if dependency == GVisor {
		paths = append(paths, SourceDirectory+"/licenses/gvisor/LICENSE")
	}
	sort.Strings(paths)
	return paths, nil
}

// ValidateInputs checks the exact selected replacement set and authenticates the
// checkout against embedded manifests/overlays. Additional overlay files, links
// and stale embedded manifests or overlays are rejected.
func ValidateInputs(root string, dependency Dependency) (Manifest, error) {
	m, err := Load(dependency)
	if err != nil {
		return m, err
	}
	if _, err := engineadaptation.ValidateOwnedInputs(root); err != nil {
		return m, err
	}
	matched := false
	for _, pin := range engineadaptation.ModulePins() {
		if pin.Module == m.Module && pin.Version == m.Version && pin.ModuleSum == m.ModuleSum && pin.GoModSum == m.GoModSum && pin.Directory == Directory(dependency) {
			matched = true
		}
	}
	if !matched {
		return m, errors.New("lifecycle identity differs from replacement pin")
	}
	paths, err := SourceInputs(dependency)
	if err != nil {
		return m, err
	}
	for _, path := range paths {
		raw, err := engineadaptation.ReadSourceFile(root, path)
		if err != nil {
			return m, err
		}
		rel, local := strings.CutPrefix(path, SourceDirectory+"/")
		if !local {
			continue
		}
		embedded, err := inputs.ReadFile(rel)
		if err == nil && !bytes.Equal(raw, embedded) {
			return m, errors.New("checkout lifecycle input differs from compiled source pin")
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return m, err
		}
	}
	if dependency == GVisor {
		raw, err := engineadaptation.ReadSourceFile(root, SourceDirectory+"/licenses/gvisor/LICENSE")
		if err != nil {
			return m, err
		}
		found := false
		for _, notice := range m.Notices {
			if notice.Path == "LICENSE" && engineadaptation.Hash(raw) == notice.SHA256 {
				found = true
			}
		}
		if !found {
			return m, errors.New("retained gVisor license differs from upstream pin")
		}
	}
	wanted := map[string]bool{}
	for _, change := range m.Changes {
		if !safePath(change.SourcePath) || change.SourcePath != "sources/"+string(dependency)+"/"+change.Path+".txt" {
			return m, errors.New("invalid lifecycle source path")
		}
		embedded, err := inputs.ReadFile(change.SourcePath)
		if err != nil {
			return m, err
		}
		checkout, err := engineadaptation.ReadSourceFile(root, SourceDirectory+"/"+change.SourcePath)
		if err != nil {
			return m, err
		}
		// Rebuilding the preparer after an overlay-only edit can make these
		// copies equal while both differ from the independently pinned manifest.
		// Authenticate the recipe even when a valid output tree already exists.
		if err := validateOverlayInput(change, checkout, embedded); err != nil {
			return m, err
		}
		wanted[SourceDirectory+"/"+change.SourcePath] = true
	}
	err = fs.WalkDir(os.DirFS(root), SourceDirectory+"/sources/"+string(dependency), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return errors.New("linked lifecycle overlay input")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() || !wanted[path] {
			return errors.New("unlisted lifecycle overlay input")
		}
		delete(wanted, path)
		return nil
	})
	if err != nil {
		return m, err
	}
	if len(wanted) != 0 {
		return m, errors.New("missing lifecycle overlay input")
	}
	return m, nil
}

func validateOverlayInput(change Change, checkout, embedded []byte) error {
	if engineadaptation.Hash(checkout) != change.AdaptedSHA256 || engineadaptation.Hash(embedded) != change.AdaptedSHA256 {
		return errors.New("lifecycle overlay differs from manifest digest")
	}
	if !bytes.Equal(checkout, embedded) {
		return errors.New("checkout lifecycle overlay differs from compiled source")
	}
	return nil
}

func Verify(root string, dependency Dependency) (Manifest, error) {
	m, err := ValidateInputs(root, dependency)
	if err != nil {
		return m, err
	}
	return m, engineadaptation.VerifyPinnedTree(root, Directory(dependency), m.AdaptedTreeSHA256)
}

func Prepare(root string, dependency Dependency) (Manifest, error) {
	m, err := ValidateInputs(root, dependency)
	if err != nil {
		return m, err
	}
	err = engineadaptation.MaterializePinned(root, Directory(dependency), archivePin(m), func(archive string) (map[string][]byte, error) {
		_, tree, err := PrepareArchive(dependency, archive)
		return tree, err
	})
	if err != nil {
		return m, err
	}
	return Verify(root, dependency)
}

// PrepareAll is the single product preparation path. Only local, standard-
// library-based preparation packages are imported, so absent replacement
// directories cannot create a bootstrap dependency on the adapted modules.
func PrepareAll(root string) error {
	// Check every selected input before writing any generated dependency.
	for _, dependency := range Dependencies() {
		if _, err := ValidateInputs(root, dependency); err != nil {
			return err
		}
	}
	if _, err := engineadaptation.PrepareOwned(root); err != nil {
		return err
	}
	for _, dependency := range Dependencies() {
		if _, err := Prepare(root, dependency); err != nil {
			return err
		}
	}
	return VerifyAll(root)
}

func VerifyAll(root string) error {
	if _, err := engineadaptation.VerifyOwned(root); err != nil {
		return err
	}
	for _, dependency := range Dependencies() {
		if _, err := Verify(root, dependency); err != nil {
			return err
		}
	}
	return nil
}
