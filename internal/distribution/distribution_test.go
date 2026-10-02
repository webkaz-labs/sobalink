package distribution

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestVersions(t *testing.T) {
	for _, v := range []string{"0.1.0", "1.2.3-rc.1", "0.0.0-dev.42"} {
		if err := ValidateVersion(v); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{"", "v1.2.3", "01.2.3", "1.2.3-01", "1.2.3\n", "../1.2.3", "1.2.3+path", "1.2.3-foo..bar"} {
		if err := ValidateVersion(v); err == nil {
			t.Fatalf("accepted %q", v)
		}
	}
}
func TestArchiveReproducibleAndPortable(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	for _, suffix := range []string{".tar.gz", ".zip"} {
		t.Run(suffix, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "stage")
			writeFixture(t, filepath.Join(source, "bin", "tool"), "executable")
			writeFixture(t, filepath.Join(source, "share", "LICENSE"), "notice")
			archive := filepath.Join(root, "artifact"+suffix)
			if err := Archive(source, archive); err != nil {
				t.Fatal(err)
			}
			first := readFixture(t, archive)
			if err := os.Chtimes(filepath.Join(source, "bin", "tool"), time.Now(), time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := Archive(source, archive); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first, readFixture(t, archive)) {
				t.Fatal("non-deterministic archive")
			}
			var names []string
			if suffix == ".tar.gz" {
				gz, err := gzip.NewReader(bytes.NewReader(first))
				if err != nil {
					t.Fatal(err)
				}
				defer gz.Close()
				r := tar.NewReader(gz)
				for {
					h, err := r.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					names = append(names, h.Name)
					if h.Uid != 0 || h.Gid != 0 || !h.ModTime.Equal(time.Unix(1700000000, 0)) {
						t.Fatalf("unnormalized header %+v", h)
					}
					if h.Name == "bin/tool" && h.Mode != 0o755 {
						t.Fatalf("binary mode = %o", h.Mode)
					}
				}
			} else {
				zr, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range zr.File {
					names = append(names, f.Name)
					if f.Name == "bin/tool" && f.Mode().Perm() != 0o755 {
						t.Fatalf("binary ZIP mode %v", f.Mode())
					}
				}
			}
			if !sort.StringsAreSorted(names) || len(names) != 4 {
				t.Fatalf("entries = %v", names)
			}
		})
	}
}
func TestArchiveRejectsUnsafeInputs(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeFixture(t, filepath.Join(source, "file"), "data")
	if err := Archive(source, filepath.Join(source, "self.tar.gz")); err == nil {
		t.Fatal("accepted recursive archive")
	}
	if err := Archive(source, filepath.Join(root, "invalid.tar")); err == nil {
		t.Fatal("accepted unsupported format")
	}
	for _, v := range []string{"-1", "bad", "4294967296"} {
		t.Setenv("SOURCE_DATE_EPOCH", v)
		if err := Archive(source, filepath.Join(root, "out.tar.gz")); err == nil {
			t.Fatalf("accepted epoch %q", v)
		}
	}
	t.Setenv("SOURCE_DATE_EPOCH", "0")
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires optional Windows privilege")
	}
	if err := os.Symlink(source, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := Archive(filepath.Join(root, "link"), filepath.Join(root, "out.tar.gz")); err == nil {
		t.Fatal("accepted source link")
	}
	if err := os.Symlink("file", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if err := Archive(source, filepath.Join(root, "out.tar.gz")); err == nil {
		t.Fatal("accepted nested link")
	}
	if _, err := os.Stat(filepath.Join(root, "out.tar.gz")); !os.IsNotExist(err) {
		t.Fatal("failed archive was published")
	}
}
func TestPackageParseFailures(t *testing.T) {
	for _, value := range []string{"", "{}", "broken", `{"ImportPath":"example.org/x","Error":{"Err":"unresolved"}}`} {
		if _, err := parsePackages([]byte(value)); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
func TestInventoryNoticesAndSBOM(t *testing.T) {
	root := t.TempDir()
	modRoot := filepath.Join(root, "mod")
	goRoot := filepath.Join(root, "go")
	writeFixture(t, filepath.Join(modRoot, "LICENSE"), "module license")
	writeFixture(t, filepath.Join(modRoot, "nested", "NOTICE.txt"), "attribution")
	writeFixture(t, filepath.Join(modRoot, "testdata", "LICENSE"), "exclude")
	writeFixture(t, filepath.Join(goRoot, "LICENSE"), "Go license")
	writeFixture(t, filepath.Join(goRoot, "src", "vendor", "nested", "LICENSE"), "vendored Go")
	writeFixture(t, filepath.Join(goRoot, "src", "math", "log1p.go"), "embedded notice")
	mod := &goModule{Path: "example.org/mod", Version: "v1.2.3", Sum: "h1:test", Dir: modRoot}
	packages := []goPackage{{ImportPath: "app", Module: &goModule{Main: true}, Imports: []string{"example.org/mod/a"}}, {ImportPath: "example.org/mod/a", Module: mod, Imports: []string{"fmt"}}, {ImportPath: "fmt"}}
	inv, err := collectInventory(packages, goRoot, filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Modules) != 1 || len(inv.Modules[0].Notices) != 2 || len(inv.Go.Notices) != 3 {
		t.Fatalf("inventory %+v", inv)
	}
	b, err := json.Marshal(makeSBOM(packages, inv, "1.0.0", Target{"windows", "amd64"}, strings.Repeat("1", 40), strings.Repeat("2", 64)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CycloneDX", "pkg:golang/example.org/mod@v1.2.3", "windows-amd64", "h1:test", "dependsOn"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("missing %s", want)
		}
	}
	if bytes.Contains(b, []byte(root)) {
		t.Fatal("local path leaked into SBOM")
	}
	mod.Replace = &goModule{Path: "example.org/other"}
	if _, err = collectInventory(packages, goRoot, filepath.Join(root, "other")); err == nil {
		t.Fatal("accepted replacement")
	}
	mod.Replace = nil
	mod.Sum = ""
	if _, err = collectInventory(packages, goRoot, filepath.Join(root, "other")); err == nil {
		t.Fatal("accepted missing module sum")
	}
}
func TestNoticeCollectionFailClosed(t *testing.T) {
	root := t.TempDir()
	if _, err := collectNotices(root, t.TempDir(), false); err == nil {
		t.Fatal("empty inventory accepted")
	}
	if runtime.GOOS == "windows" {
		return
	}
	outside := filepath.Join(t.TempDir(), "secret")
	writeFixture(t, outside, "private")
	if err := os.Symlink(outside, filepath.Join(root, "LICENSE")); err != nil {
		t.Fatal(err)
	}
	if _, err := collectNotices(root, t.TempDir(), false); err == nil {
		t.Fatal("symlink notice accepted")
	}
}
func mockTool(t *testing.T) (*Tool, *int) {
	t.Helper()
	root := t.TempDir()
	tool, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"LICENSE", "README.md", "README.en.md", "SECURITY.md", "go.mod", "go.sum"} {
		writeFixture(t, filepath.Join(root, name), name)
	}
	writeFixture(t, filepath.Join(root, "web", "package.json"), `{"name":"fixture","dependencies":{"react":"1.0.0"}}`)
	writeFixture(t, filepath.Join(root, "web", "package-lock.json"), `{"lockfileVersion":3,"packages":{"node_modules/react":{"version":"1.0.0","integrity":"sha512-fixture"}}}`)
	writeFixture(t, filepath.Join(root, "web", "node_modules", "react", "package.json"), `{"name":"react","version":"1.0.0"}`)
	writeFixture(t, filepath.Join(root, "web", "node_modules", "react", "LICENSE"), "React license")
	writeFixture(t, filepath.Join(root, "web", "dist", "index.html"), "<!doctype html><html></html>")
	goRoot := filepath.Join(root, "toolchain")
	writeFixture(t, filepath.Join(goRoot, "LICENSE"), "Go license")
	builds := new(int)
	tool.run = func(env []string, args ...string) ([]byte, error) {
		for _, want := range []string{"GOTOOLCHAIN=local", "GOWORK=off", "GOENV=off", "CGO_ENABLED=0", "GOFLAGS=", "GOEXPERIMENT=", "GOFIPS140=off"} {
			if !strings.Contains(strings.Join(env, "\n"), want) {
				t.Fatalf("missing reproducible env %s", want)
			}
		}
		if reflect.DeepEqual(args, []string{"env", "GOVERSION"}) {
			return []byte(GoVersion), nil
		}
		if reflect.DeepEqual(args, []string{"env", "GOROOT"}) {
			return []byte(goRoot), nil
		}
		if args[0] == "build" || args[0] == "list" {
			if len(args) < 3 || args[1] != "-tags" || args[2] != BuildTags || args[len(args)-1] != "./cmd/soba" {
				t.Fatalf("unsafe or wrong product build: %v", args)
			}
		}
		if args[0] == "build" {
			*builds++
			for i, arg := range args {
				if arg == "-o" {
					writeFixture(t, args[i+1], "compiled binary")
				}
			}
			return nil, nil
		}
		if args[0] == "list" {
			return []byte(`{"ImportPath":"app","Module":{"Main":true},"Imports":["fmt"]}{"ImportPath":"fmt"}`), nil
		}
		return nil, errors.New("unexpected command")
	}
	return tool, builds
}
func TestBuildDeterministicAndManifestComplete(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	tool, count := mockTool(t)
	version := "0.0.0-dev.1"
	commit := strings.Repeat("a", 40)
	if err := tool.Manifest(version); err == nil {
		t.Fatal("accepted absent matrix")
	}
	for _, target := range Targets {
		if err := tool.Build(version, target, commit, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if *count != len(Targets) {
		t.Fatal("not all targets built")
	}
	if err := tool.Manifest(version); err != nil {
		t.Fatal(err)
	}
	manifest := string(readFixture(t, filepath.Join(tool.Root, "dist", "packslip.toml")))
	for _, want := range []string{"arch = \"x86_64\"", "arch = \"aarch64\"", "bin/soba.exe", "libc = \"any\"", "format = \"zip\"", commit} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("manifest missing %s", want)
		}
	}
	if strings.Count(manifest, "[[artifact]]") != len(Targets) || strings.Count(manifest, "[[resource]]") != len(Targets) {
		t.Fatal("manifest targets/resources incomplete")
	}
	if err := tool.Checksums(); err != nil {
		t.Fatal(err)
	}
	first := readFixture(t, filepath.Join(tool.Root, "dist", "SHA256SUMS"))
	for _, target := range Targets {
		if err := tool.Build(version, target, commit, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if err := tool.Checksums(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, readFixture(t, filepath.Join(tool.Root, "dist", "SHA256SUMS"))) {
		t.Fatal("repeated package build differs")
	}
	if bytes.Contains(first, []byte("SHA256SUMS")) {
		t.Fatal("checksums included itself")
	}
	// A valid-looking file set with mismatched commits must not be signed.
	path := filepath.Join(tool.Root, "dist", stem(version, Targets[0])+".build.json")
	writeFixture(t, path, strings.ReplaceAll(string(readFixture(t, path)), commit, strings.Repeat("b", 40)))
	if err := tool.Manifest(version); err == nil {
		t.Fatal("accepted mixed commits")
	}
}
func TestBuildAndCLIRejectInvalidInputs(t *testing.T) {
	tool, count := mockTool(t)
	for _, args := range [][]string{nil, {"unknown"}, {"build"}, {"manifest", "../bad"}, {"checksums", "extra"}, {"build", "1.2.3", "windows", "arm64", strings.Repeat("a", 40)}, {"build", "1.2.3", "linux", "amd64", "bad"}} {
		if err := tool.Run(args, io.Discard); err == nil {
			t.Fatalf("accepted args %v", args)
		}
	}
	if *count != 0 {
		t.Fatal("invalid input started build")
	}
	tool.run = func([]string, ...string) ([]byte, error) { return []byte("go1.99.0"), nil }
	if err := tool.Build("1.2.3", Targets[0], strings.Repeat("a", 40), io.Discard); err == nil {
		t.Fatal("accepted unexpected toolchain")
	}
}

func TestChecksumsFailClosedAndSorted(t *testing.T) {
	tool, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(tool.Root, "dist")
	if err = os.Mkdir(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = tool.Checksums(); err == nil {
		t.Fatal("empty checksum set accepted")
	}
	writeFixture(t, filepath.Join(dist, "z.zip"), "z")
	writeFixture(t, filepath.Join(dist, "a.tar.gz"), "a")
	if err = tool.Checksums(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(readFixture(t, filepath.Join(dist, "SHA256SUMS")))), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "  a.tar.gz") || !strings.HasSuffix(lines[1], "  z.zip") {
		t.Fatalf("checksum order %v", lines)
	}
	if err = os.Mkdir(filepath.Join(dist, "unexpected"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = tool.Checksums(); err == nil {
		t.Fatal("directory accepted in checksum set")
	}
}

func TestManifestRejectsMismatchedPlatformMetadata(t *testing.T) {
	tool, _ := mockTool(t)
	version := "1.2.3"
	commit := strings.Repeat("a", 40)
	for _, target := range Targets {
		if err := tool.Build(version, target, commit, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	name := filepath.Join(tool.Root, "dist", stem(version, Targets[0])+".build.json")
	original := readFixture(t, name)
	for _, replacement := range []struct{ from, to string }{{Project, "example.com/other"}, {version, "1.2.4"}, {Targets[0].String(), "windows-amd64"}, {commit, "bad"}} {
		writeFixture(t, name, strings.ReplaceAll(string(original), replacement.from, replacement.to))
		if err := tool.Manifest(version); err == nil {
			t.Fatalf("accepted invalid metadata: %+v", replacement)
		}
	}
}

func TestNoticeCollectionFollowsOnlySuppliedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native junction case is covered separately")
	}
	root := t.TempDir()
	target := filepath.Join(root, "actual")
	writeFixture(t, filepath.Join(target, "LICENSE"), "root license")
	writeFixture(t, filepath.Join(target, "nested", "NOTICE"), "nested notice")
	outside := t.TempDir()
	writeFixture(t, filepath.Join(outside, "NOTICE"), "must not copy")
	link := filepath.Join(root, "toolchain-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	notices, err := collectNotices(link, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 2 || notices[0].Path != "LICENSE" || notices[1].Path != "nested/NOTICE" {
		t.Fatalf("notices = %+v", notices)
	}
	if err := os.Symlink(outside, filepath.Join(target, "nested-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := collectNotices(link, t.TempDir(), true); err == nil {
		t.Fatal("accepted nested directory link")
	}
	// A regular file is never accepted as a notice inventory root.
	if _, err = collectNotices(filepath.Join(target, "LICENSE"), t.TempDir(), false); err == nil {
		t.Fatal("accepted non-directory root")
	}
}

func TestSupportedDistributionTargets(t *testing.T) {
	want := []Target{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}, {"windows", "amd64"}}
	if !reflect.DeepEqual(Targets, want) {
		t.Fatalf("targets = %v, want %v", Targets, want)
	}
	if (Target{"darwin", "amd64"}).valid() {
		t.Fatal("Intel macOS is not a distribution target")
	}
}
