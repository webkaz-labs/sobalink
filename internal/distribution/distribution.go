// Package distribution builds auditable, deterministic distribution archives.
// It has no dependency on the networking runtime or any external packaging tool.
package distribution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

const Project = "github.com/webkaz-labs/sobalink"
const Product = "sobalink"
const Executable = "soba"
const BuildTags = "ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy"
const GoVersion = "go1.27.1"

type Target struct{ OS, Arch string }

var Targets = []Target{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}, {"windows", "amd64"}}

func (t Target) String() string { return t.OS + "-" + t.Arch }
func (t Target) Binary() string {
	if t.OS == "windows" {
		return Executable + ".exe"
	}
	return Executable
}
func (t Target) Extension() string {
	if t.OS == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}
func (t Target) valid() bool {
	for _, allowed := range Targets {
		if t == allowed {
			return true
		}
	}
	return false
}
func stem(version string, target Target) string {
	return Product + "-" + version + "-" + target.String()
}

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func ValidateVersion(version string) error {
	if !versionPattern.MatchString(version) {
		return fmt.Errorf("invalid version %q (expected X.Y.Z or X.Y.Z-prerelease)", version)
	}
	if _, pre, ok := strings.Cut(version, "-"); ok {
		for _, id := range strings.Split(pre, ".") {
			if len(id) > 1 && id[0] == '0' && strings.Trim(id, "0123456789") == "" {
				return errors.New("numeric prerelease identifiers cannot have leading zeros")
			}
		}
	}
	return nil
}

type Tool struct {
	Root string
	run  func(env []string, args ...string) ([]byte, error)
}

func New(root string) (*Tool, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	t := &Tool{Root: root}
	t.run = func(env []string, args ...string) ([]byte, error) {
		cmd := exec.Command("go", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), env...)
		cmd.Stderr = os.Stderr
		data, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
		}
		return data, nil
	}
	return t, nil
}
func (t *Tool) Run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: package-tool build VERSION GOOS GOARCH COMMIT | manifest VERSION | checksums | archive SOURCE DESTINATION")
	}
	switch args[0] {
	case "build":
		if len(args) == 5 {
			return t.Build(args[1], Target{args[2], args[3]}, args[4], out)
		}
	case "manifest":
		if len(args) == 2 {
			return t.Manifest(args[1])
		}
	case "checksums":
		if len(args) == 1 {
			return t.Checksums()
		}
	case "archive":
		if len(args) == 3 {
			return Archive(args[1], args[2])
		}
	}
	return errors.New("unknown command or incorrect arguments")
}
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
func copyFile(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", src)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
func (t *Tool) Build(version string, target Target, commit string, out io.Writer) error {
	if err := ValidateVersion(version); err != nil {
		return err
	}
	if !target.valid() {
		return fmt.Errorf("unsupported target %s", target)
	}
	if !commitPattern.MatchString(commit) {
		return errors.New("source commit must be 40 lowercase hexadecimal characters")
	}
	env := []string{"GOTOOLCHAIN=local", "GOWORK=off", "GOENV=off", "GOFLAGS=", "GOEXPERIMENT=", "GOFIPS140=off", "CGO_ENABLED=0", "GOOS=" + target.OS, "GOARCH=" + target.Arch, "GOAMD64=v1", "GOARM64=v8.0"}
	gv, err := t.run(env, "env", "GOVERSION")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(gv)) != GoVersion {
		return fmt.Errorf("packaging requires %s, got %s", GoVersion, strings.TrimSpace(string(gv)))
	}
	if err := os.MkdirAll(filepath.Join(t.Root, ".build"), 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Join(t.Root, ".build"), "package-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	share := filepath.Join(stage, "share", Product)
	if err = os.MkdirAll(share, 0o755); err != nil {
		return err
	}
	bin := filepath.Join(stage, "bin", target.Binary())
	if err = os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	flags := "-s -w -buildid= -X main.version=" + version
	packagesJSON, err := t.run(env, "list", "-tags", BuildTags, "-mod=readonly", "-buildvcs=false", "-deps", "-json", "./cmd/soba")
	if err != nil {
		return err
	}
	packages, err := parsePackages(packagesJSON)
	if err != nil {
		return err
	}
	goRoot, err := t.run(env, "env", "GOROOT")
	if err != nil {
		return err
	}
	licenses, err := collectInventory(packages, strings.TrimSpace(string(goRoot)), filepath.Join(share, "licenses"), t.Root)
	if err != nil {
		return err
	}
	licenses.Sources, err = sourceInventory(t.Root, packages, share)
	if err != nil {
		return err
	}
	engineSources, err := engineInventory(t.Root, packages, share)
	if err != nil {
		return err
	}
	licenses.Sources = append(licenses.Sources, engineSources...)
	// Snapshot the adapted source before compilation and re-list afterwards.
	// Refuse to publish provenance for bytes or target inputs changed mid-build.
	if _, err = t.run(env, "build", "-tags", BuildTags, "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags", flags, "-o", bin, "./cmd/soba"); err != nil {
		return err
	}
	if err = os.Chmod(bin, 0o755); err != nil {
		return err
	}
	packagesJSON, err = t.run(env, "list", "-tags", BuildTags, "-mod=readonly", "-buildvcs=false", "-deps", "-json", "./cmd/soba")
	if err != nil {
		return err
	}
	packages, err = parsePackages(packagesJSON)
	if err != nil {
		return err
	}
	verifiedSources, err := sourceInventory(t.Root, packages, share)
	if err != nil {
		return err
	}
	verifiedEngine, err := engineInventory(t.Root, packages, share)
	if err != nil {
		return err
	}
	verifiedSources = append(verifiedSources, verifiedEngine...)
	if !reflect.DeepEqual(licenses.Sources, verifiedSources) {
		return errors.New("adapted source inputs changed during package build")
	}
	frontend, assets, err := frontendInventory(t.Root, share)
	if err != nil {
		return err
	}
	licenses.Frontend = frontend
	licenses.Scope += " Embedded frontend inventory includes all locked npm production packages, including tree-shaken source, plus Vite preload helpers and Tailwind CSS."
	frontendLockHash, err := fileHash(filepath.Join(t.Root, "web", "package-lock.json"))
	if err != nil {
		return err
	}
	if err = writeJSON(filepath.Join(share, "third-party-notices.json"), licenses); err != nil {
		return err
	}
	for _, name := range []string{"LICENSE", "README.md", "README.en.md", "SECURITY.md", "go.mod", "go.sum"} {
		if err = copyFile(filepath.Join(t.Root, name), filepath.Join(share, name)); err != nil {
			return err
		}
	}
	// Preserve the relative layout used by guides, including their Web API
	// and browser-acceptance references. Never collect runtime state or caches.
	for _, directory := range []string{"docs", "web"} {
		docs, err := filepath.Glob(filepath.Join(t.Root, directory, "*.md"))
		if err != nil {
			return err
		}
		for _, doc := range docs {
			if err = copyFile(doc, filepath.Join(share, directory, filepath.Base(doc))); err != nil {
				return err
			}
		}
	}
	binaryHash, err := fileHash(bin)
	if err != nil {
		return err
	}
	sumHash, err := fileHash(filepath.Join(t.Root, "go.sum"))
	if err != nil {
		return err
	}
	metadata := map[string]any{"project": Project, "product": Product, "version": version, "source_commit": commit, "target": target.String(), "go_version": GoVersion, "build_tags": BuildTags, "cgo_enabled": false, "trimpath": true, "buildvcs": false, "ldflags": flags, "go_sum_sha256": sumHash, "source_components": licenses.Sources, "frontend": map[string]any{"node_version": "24.19.0", "npm_version": "11.9.0", "lock_sha256": frontendLockHash, "assets": assets}, "binary": map[string]string{"path": "bin/" + target.Binary(), "sha256": binaryHash}}
	if err = writeJSON(filepath.Join(share, "build.json"), metadata); err != nil {
		return err
	}
	if err = writeJSON(filepath.Join(share, "bom.cdx.json"), makeSBOM(packages, licenses, version, target, commit, binaryHash)); err != nil {
		return err
	}
	dist := filepath.Join(t.Root, "dist")
	if err = os.MkdirAll(dist, 0o755); err != nil {
		return err
	}
	basename := stem(version, target)
	if err = Archive(stage, filepath.Join(dist, basename+target.Extension())); err != nil {
		return err
	}
	for _, pair := range [][2]string{{"bom.cdx.json", ".cdx.json"}, {"build.json", ".build.json"}, {"third-party-notices.json", ".notices.json"}} {
		if err = copyFile(filepath.Join(share, pair[0]), filepath.Join(dist, basename+pair[1])); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(out, filepath.Join("dist", basename+target.Extension()))
	return err
}

// Manifest refuses incomplete target sets and binds every SBOM to its archive.
// This is signing input, not a signed manifest or an installable public release.
func (t *Tool) Manifest(version string) error {
	if err := ValidateVersion(version); err != nil {
		return err
	}
	dist := filepath.Join(t.Root, "dist")
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated signing input. No release is published by this command.\nproject = %q\nversion = %q\nurl_base = %q\n\n", Project, version, "https://"+Project+"/releases/download/v"+version)
	var commit string
	for _, target := range Targets {
		base := stem(version, target)
		for _, suffix := range []string{target.Extension(), ".cdx.json", ".build.json", ".notices.json"} {
			info, err := os.Lstat(filepath.Join(dist, base+suffix))
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("all manifest inputs must be regular files")
			}
		}
		raw, err := os.ReadFile(filepath.Join(dist, base+".build.json"))
		if err != nil {
			return err
		}
		var meta struct {
			Project, Version string
			Commit           string `json:"source_commit"`
			Target           string
		}
		if err = json.Unmarshal(raw, &meta); err != nil {
			return err
		}
		if meta.Project != Project || meta.Version != version || meta.Target != target.String() || !commitPattern.MatchString(meta.Commit) {
			return fmt.Errorf("invalid build metadata for %s", target)
		}
		if commit == "" {
			commit = meta.Commit
		}
		if commit != meta.Commit {
			return errors.New("manifest inputs refer to different source commits")
		}
		arch := "x86_64"
		if target.Arch == "arm64" {
			arch = "aarch64"
		}
		format := strings.TrimPrefix(target.Extension(), ".")
		fmt.Fprintf(&b, "[[artifact]]\npath = %q\nos = %q\narch = %q\nformat = %q\nbin = [{ name = \"soba\", path = %q }]\n", "dist/"+base+target.Extension(), target.OS, arch, format, "bin/"+target.Binary())
		if target.OS == "linux" {
			b.WriteString("libc = \"any\"\n")
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "[[resource]]\nkind = \"sbom\"\nformat = \"cyclonedx\"\nartifact = %q\nasset = %q\n\n", base+target.Extension(), "dist/"+base+".cdx.json")
	}
	fmt.Fprintf(&b, "[source]\nrepo = %q\ncommit = %q\ntag = %q\n", "https://"+Project, commit, "v"+version)
	return os.WriteFile(filepath.Join(dist, "packslip.toml"), []byte(b.String()), 0o644)
}
func (t *Tool) Checksums() error {
	dist := filepath.Join(t.Root, "dist")
	entries, err := os.ReadDir(dist)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, entry := range entries {
		if entry.Name() == "SHA256SUMS" {
			continue
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected non-file in dist: %s", entry.Name())
		}
		if strings.ContainsAny(entry.Name(), "\r\n\\") {
			return errors.New("unsafe checksum filename")
		}
		hash, err := fileHash(filepath.Join(dist, entry.Name()))
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s  %s\n", hash, entry.Name())
	}
	if b.Len() == 0 {
		return errors.New("no artifacts to checksum")
	}
	return os.WriteFile(filepath.Join(dist, "SHA256SUMS"), []byte(b.String()), 0o644)
}
