package distribution

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type FrontendModule struct {
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	Integrity string   `json:"integrity"`
	Notices   []Notice `json:"notices"`
}

// FrontendInventory identifies every locked production package, including packages
// only partly included by the bundler. This deliberately over-approximates the
// embedded runtime instead of omitting licenses after tree shaking. Vite and
// Tailwind notices also cover generated preload helpers and bundled CSS.
func frontendInventory(root, share string) ([]FrontendModule, []Notice, error) {
	var lock struct {
		Version  int `json:"lockfileVersion"`
		Packages map[string]struct {
			Version   string `json:"version"`
			Integrity string `json:"integrity"`
			Dev       bool   `json:"dev"`
			Link      bool   `json:"link"`
		} `json:"packages"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "web", "package-lock.json"))
	if err != nil {
		return nil, nil, err
	}
	if err = json.Unmarshal(raw, &lock); err != nil {
		return nil, nil, err
	}
	if lock.Version != 3 {
		return nil, nil, errors.New("frontend requires npm lockfileVersion 3")
	}
	paths := make([]string, 0, len(lock.Packages))
	for path := range lock.Packages {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	var modules []FrontendModule
	for _, path := range paths {
		pkg := lock.Packages[path]
		if path == "" || (pkg.Dev && path != "node_modules/vite" && path != "node_modules/tailwindcss") {
			continue
		}
		if !strings.HasPrefix(path, "node_modules/") || !fs.ValidPath(path) || strings.ContainsAny(path, "\\:") || pkg.Link || pkg.Version == "" || pkg.Integrity == "" {
			return nil, nil, fmt.Errorf("invalid frontend package provenance: %s", path)
		}
		name := strings.TrimPrefix(path, "node_modules/")
		// npm nesting is represented in the lock path, not in the package name.
		if i := strings.LastIndex(name, "/node_modules/"); i >= 0 {
			name = name[i+len("/node_modules/"):]
		}
		src := filepath.Join(root, "web", filepath.FromSlash(path))
		if info, err := os.Lstat(src); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, fmt.Errorf("frontend dependency must be a real directory: %s", path)
		}
		var installed struct{ Name, Version string }
		raw, err := os.ReadFile(filepath.Join(src, "package.json"))
		if err != nil {
			return nil, nil, err
		}
		if err = json.Unmarshal(raw, &installed); err != nil {
			return nil, nil, err
		}
		if installed.Name != name || installed.Version != pkg.Version {
			return nil, nil, fmt.Errorf("frontend package differs from lock: %s", path)
		}
		key := "npm/" + moduleKey(name, pkg.Version)
		notices, err := collectNotices(src, filepath.Join(share, "licenses", key), false)
		if err != nil {
			return nil, nil, err
		}
		for i := range notices {
			notices[i].Path = "licenses/" + key + "/" + notices[i].Path
		}
		modules = append(modules, FrontendModule{name, pkg.Version, pkg.Integrity, notices})
	}
	if len(modules) == 0 {
		return nil, nil, errors.New("frontend production dependency inventory is empty")
	}
	reader, err := pngReaderInventory(root, share)
	if err != nil {
		return nil, nil, err
	}
	if reader != nil {
		modules = append(modules, *reader)
	}
	var assets []Notice
	dist := filepath.Join(root, "web", "dist")
	if info, err := os.Lstat(filepath.Join(dist, "index.html")); err != nil || !info.Mode().IsRegular() {
		return nil, nil, errors.New("build frontend with npm ci and npm run build before packaging")
	}
	err = fs.WalkDir(os.DirFS(dist), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("frontend assets must be regular files")
		}
		hash, err := fileHash(filepath.Join(dist, filepath.FromSlash(path)))
		if err != nil {
			return err
		}
		assets = append(assets, Notice{path, hash})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	for _, name := range []string{"package.json", "package-lock.json"} {
		if err := copyFile(filepath.Join(root, "web", name), filepath.Join(share, "web", name)); err != nil {
			return nil, nil, err
		}
	}
	return modules, assets, nil
}
func npmPURL(name, version string) string {
	parts := strings.Split(name, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "pkg:npm/" + strings.Join(parts, "/") + "@" + url.PathEscape(version)
}
