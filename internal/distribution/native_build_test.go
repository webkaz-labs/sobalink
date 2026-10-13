package distribution

import (
	"bytes"
	"encoding/binary"
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
)

func nativeProbeFixture(t *testing.T) func() (*NativeBuild, []string, error) {
	return nativeProbeFixtureWith(t, nil)
}

func nativeProbeFixtureWith(t *testing.T, transform func(string, string) string) func() (*NativeBuild, []string, error) {
	t.Helper()
	root := t.TempDir()
	cc := filepath.Join(root, "toolchain", "clang")
	ld := filepath.Join(root, "toolchain", "ld")
	sdk := filepath.Join(root, "SDK")
	writeFixture(t, cc, "synthetic compiler")
	writeFixture(t, ld, "synthetic linker")
	for _, path := range darwinSDKInputs {
		writeFixture(t, filepath.Join(sdk, path), "synthetic SDK input")
	}
	return func() (*NativeBuild, []string, error) {
		return probeDarwinWith(func(command string, args ...string) ([]byte, error) {
			values := map[string]string{
				"/usr/bin/xcrun --sdk macosx --find clang": cc, "/usr/bin/xcrun --sdk macosx --find ld": ld, "/usr/bin/xcrun --sdk macosx --show-sdk-path": sdk,
				"/usr/bin/xcrun --sdk macosx --show-sdk-version": "26.0", "/usr/bin/xcrun --sdk macosx --show-sdk-build-version": "25A1",
				"/usr/bin/xcode-select -p": filepath.Join(root, "Developer"), "/usr/bin/xcodebuild -version": "Xcode 26.0\nBuild version 17A1",
				"/usr/bin/sw_vers -productVersion": "26.0.1", "/usr/bin/sw_vers -buildVersion": "25A1", cc + " --version": "Apple clang version 17.0.0 (clang-1700.1.2)\nInstalledDir: " + root,
			}
			v, ok := values[command+" "+strings.Join(args, " ")]
			if !ok {
				return nil, errors.New("unexpected native query")
			}
			if transform != nil {
				v = transform(command+" "+strings.Join(args, " "), v)
			}
			return []byte(v), nil
		})
	}
}
func TestDarwinProbeFiniteIdentityAndExplicitFlags(t *testing.T) {
	n, env, err := nativeProbeFixture(t)()
	if err != nil {
		t.Fatal(err)
	}
	if n.DeploymentTarget != "13.0" || len(n.SDKInputs) != len(darwinSDKInputs) || !strings.Contains(strings.Join(env, "\n"), "-mmacosx-version-min=13.0") {
		t.Fatal("missing explicit native inputs")
	}
	raw, _ := json.Marshal(n)
	if bytes.Contains(raw, []byte("InstalledDir")) || bytes.Contains(raw, []byte("/tmp/")) {
		t.Fatal("native identity leaked a machine path")
	}
	if len(n.CompilerSHA256) != 64 || len(n.LinkerSHA256) != 64 {
		t.Fatal("missing compiler content identity")
	}
}
func TestDarwinProbeRejectsCrossHost(t *testing.T) {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		t.Skip("cross-host guard only")
	}
	if _, _, err := probeDarwinToolchain(); err == nil {
		t.Fatal("accepted non-native Darwin package")
	}
}
func TestNativeEnvironmentCannotInheritOverrides(t *testing.T) {
	for _, key := range []string{"CC", "CXX", "CGO_CFLAGS", "CGO_LDFLAGS_ALLOW", "CPATH", "C_INCLUDE_PATH", "LIBRARY_PATH", "SDKROOT", "DEVELOPER_DIR", "MACOSX_DEPLOYMENT_TARGET", "DYLD_INSERT_LIBRARIES", "CCC_OVERRIDE_OPTIONS"} {
		t.Setenv(key, "synthetic-unreviewed")
	}
	if strings.Contains(strings.Join(cleanBuildEnvironment(), "\n"), "synthetic-unreviewed") {
		t.Fatal("inherited compiler override")
	}
}

func nativeInventoryFixture(t *testing.T) []goPackage {
	t.Helper()
	root := t.TempDir()
	original := darwinPolicyInputs
	darwinPolicyInputs = map[string]string{}
	t.Cleanup(func() { darwinPolicyInputs = original })
	packages := map[string]goPackage{}
	for path := range original {
		name, file := filepath.ToSlash(filepath.Dir(path)), filepath.Base(path)
		p := packages[name]
		p.ImportPath = name
		p.Dir = filepath.Join(root, filepath.FromSlash(name))
		packages[name] = p
		raw := "// synthetic reviewed boundary\n"
		writeFixture(t, filepath.Join(p.Dir, file), raw)
		darwinPolicyInputs[path] = bytesHash([]byte(raw))
	}
	for _, name := range darwinPolicyImporters {
		p := packages[name]
		p.ImportPath = name
		p.Imports = append(p.Imports, "tailscale.com/util/syspolicy/policyclient")
		packages[name] = p
	}
	p := packages["tailscale.com/control/controlclient"]
	p.Imports = append(p.Imports, certstoreModule)
	packages[p.ImportPath] = p
	p = packages[certstoreModule]
	p.GoFiles = []string{"certstore.go"}
	p.CgoFiles = []string{"certstore_darwin.go"}
	p.CgoCFLAGS = []string{"-x", "objective-c"}
	p.CgoLDFLAGS = []string{"-framework", "CoreFoundation", "-framework", "Security"}
	p.Module = &goModule{Path: certstoreModule, Version: certstoreVersion, Sum: certstoreSum}
	packages[p.ImportPath] = p
	p = goPackage{ImportPath: nearbyPackage, Dir: filepath.Join(root, "nearby"), CgoFiles: []string{"provider_darwin.go"}, CFiles: []string{"dnssd_bridge_darwin.c"}, HFiles: []string{"dnssd_bridge_darwin.h"}, CgoCFLAGS: []string{"-std=c11"}}
	for _, name := range []string{"provider_darwin.go", "dnssd_bridge_darwin.c", "dnssd_bridge_darwin.h"} {
		writeFixture(t, filepath.Join(p.Dir, name), "synthetic native source")
	}
	packages[p.ImportPath] = p
	p = goPackage{ImportPath: "runtime/cgo", Dir: filepath.Join(root, "runtime"), CgoFiles: []string{"cgo.go"}, CgoCFLAGS: []string{"-Wall", "-Werror", "-fno-stack-protector", "-Wdeclaration-after-statement", "-std=gnu90"}}
	writeFixture(t, filepath.Join(p.Dir, "cgo.go"), "synthetic runtime source")
	packages[p.ImportPath] = p
	var out []goPackage
	for _, p := range packages {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ImportPath < out[j].ImportPath })
	return out
}
func TestDarwinNativeClosureIsPositiveAndSourceBound(t *testing.T) {
	packages := nativeInventoryFixture(t)
	records, policy, err := darwinNativeInventory(packages)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || len(policy) != 5 {
		t.Fatal("incomplete native closure")
	}
	again, againPolicy, err := darwinNativeInventory(packages)
	if err != nil || !reflect.DeepEqual(records, again) || !reflect.DeepEqual(policy, againPolicy) {
		t.Fatal("unstable native closure")
	}
	for _, mutate := range []func([]goPackage) []goPackage{
		func(p []goPackage) []goPackage {
			return append(p, goPackage{ImportPath: "tailscale.com/util/syspolicy"})
		},
		func(p []goPackage) []goPackage {
			return append(p, goPackage{ImportPath: "unreviewed/cgo", CgoFiles: []string{"extra.go"}})
		},
		func(p []goPackage) []goPackage {
			return append(p, goPackage{ImportPath: "unreviewed/consumer", Imports: []string{certstoreModule}})
		},
		func(p []goPackage) []goPackage {
			return append(p, goPackage{ImportPath: "unreviewed/policy", Imports: []string{"tailscale.com/util/syspolicy/policyclient"}})
		},
		func(p []goPackage) []goPackage {
			return append(p, goPackage{ImportPath: Project + "/internal/other", Module: &goModule{Main: true}, Imports: []string{"tailscale.com/tsd"}})
		},
		func(p []goPackage) []goPackage {
			for i := range p {
				if p[i].ImportPath == nearbyPackage {
					p[i].CgoLDFLAGS = []string{"-ldns_sd"}
				}
			}
			return p
		},
		func(p []goPackage) []goPackage {
			for i := range p {
				if p[i].ImportPath == certstoreModule {
					m := *p[i].Module
					m.Version = "v0.0.0"
					p[i].Module = &m
				}
			}
			return p
		},
	} {
		if _, _, err := darwinNativeInventory(mutate(append([]goPackage{}, packages...))); err == nil {
			t.Fatal("accepted unreviewed native closure")
		}
	}
	for _, p := range packages {
		if p.ImportPath == certstoreModule {
			writeFixture(t, filepath.Join(p.Dir, "certstore_darwin.go"), "changed constructor")
		}
	}
	if _, _, err := darwinNativeInventory(packages); err == nil {
		t.Fatal("accepted changed credential boundary")
	}
}
func TestDarwinBuildRejectsIdentityDrift(t *testing.T) {
	tool, _ := mockTool(t)
	probe := tool.nativeProbe
	calls := 0
	tool.nativeProbe = func() (*NativeBuild, []string, error) {
		n, e, err := probe()
		calls++
		if calls == 2 {
			n.SDKBuild = "changed"
		}
		return n, e, err
	}
	if err := tool.Build("0.0.0-dev.1", Target{"darwin", "arm64"}, strings.Repeat("a", 40), io.Discard); err == nil {
		t.Fatal("accepted native toolchain drift")
	}
}

func TestDarwinProbeRejectsMalformedIdentityAndMissingSDK(t *testing.T) {
	for _, command := range []string{"/usr/bin/xcodebuild -version", "/usr/bin/sw_vers -productVersion", "/usr/bin/sw_vers -buildVersion", "/usr/bin/xcrun --sdk macosx --show-sdk-version", "/usr/bin/xcrun --sdk macosx --show-sdk-build-version"} {
		probe := nativeProbeFixtureWith(t, func(key, value string) string {
			if key == command {
				return "unexpected/private-value"
			}
			return value
		})
		if _, _, err := probe(); err == nil {
			t.Fatalf("accepted malformed %s", command)
		}
	}
	probe := nativeProbeFixtureWith(t, func(key, value string) string {
		if strings.HasSuffix(key, "clang --version") {
			return "unreviewed clang"
		}
		return value
	})
	if _, _, err := probe(); err == nil {
		t.Fatal("accepted non-Apple compiler identity")
	}
	probe = nativeProbeFixture(t)
	_, env, err := probe()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, "SDKROOT=") {
			if err := os.Remove(filepath.Join(strings.TrimPrefix(entry, "SDKROOT="), "usr/include/dns_sd.h")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, _, err := probe(); err == nil {
		t.Fatal("accepted missing SDK header")
	}
}
func TestNativeOutputCaptureIsBounded(t *testing.T) {
	var out limitedNativeOutput
	for range 4 {
		if _, err := out.Write(make([]byte, 8192)); err != nil {
			t.Fatal(err)
		}
	}
	if !out.over || out.Len() > 16384 {
		t.Fatal("unbounded native tool output")
	}
}
func writeMachOFixture(t *testing.T, path string, libraries []string, minimum uint32, extra uint32) {
	t.Helper()
	var commands bytes.Buffer
	appendWords := func(words ...uint32) {
		for _, word := range words {
			_ = binary.Write(&commands, binary.LittleEndian, word)
		}
	}
	appendWords(0x32, 24, 1, minimum<<16, 26<<16, 0)
	count := uint32(1)
	for _, name := range libraries {
		raw := append([]byte(name), 0)
		size := (24 + len(raw) + 7) / 8 * 8
		appendWords(0xc, uint32(size), 24, 0, 0, 0)
		commands.Write(raw)
		commands.Write(make([]byte, size-24-len(raw)))
		count++
	}
	if extra != 0 {
		appendWords(extra, 24, 24, 0, 0, 0)
		count++
	}
	var header bytes.Buffer
	for _, word := range []uint32{0xfeedfacf, 0x100000c, 0, 2, count, uint32(commands.Len()), 0, 0} {
		_ = binary.Write(&header, binary.LittleEndian, word)
	}
	if err := os.WriteFile(path, append(header.Bytes(), commands.Bytes()...), 0o600); err != nil {
		t.Fatal(err)
	}
}
func TestDarwinBinaryInspectionRejectsChangedLinkageAndMinimum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic-macho")
	writeMachOFixture(t, path, darwinSystemLibraries, 13, 0)
	got, err := inspectDarwinLibraries(path)
	if err != nil || !reflect.DeepEqual(got, darwinSystemLibraries) {
		t.Fatalf("valid native inspection failed: %v", err)
	}
	for _, change := range []struct {
		libraries      []string
		minimum, extra uint32
	}{
		{darwinSystemLibraries, 26, 0}, {darwinSystemLibraries, 13, 0x8000001c}, {darwinSystemLibraries, 13, 0x1b},
		{append(append([]string{}, darwinSystemLibraries...), "/tmp/unreviewed.dylib"), 13, 0},
		{append(append([]string{}, darwinSystemLibraries...), "/usr/lib/libdns_sd.dylib"), 13, 0},
		{darwinSystemLibraries[:len(darwinSystemLibraries)-1], 13, 0},
	} {
		writeMachOFixture(t, path, change.libraries, change.minimum, change.extra)
		if _, err := inspectDarwinLibraries(path); err == nil {
			t.Fatal("accepted unreviewed Mach-O boundary")
		}
	}
}

func TestDarwinBinaryInspectionIncludesWeakLibraries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic-weak-library")
	libraries := append(append([]string{}, darwinSystemLibraries...), "/tmp/unreviewed.dylib")
	writeMachOFixture(t, path, libraries, 13, 0)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	offset, last := 32, 0
	for offset < len(raw) {
		last = offset
		offset += int(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
	}
	binary.LittleEndian.PutUint32(raw[last:last+4], 0x80000018)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectDarwinLibraries(path); err == nil {
		t.Fatal("ignored an unreviewed weak library")
	}
}

func TestDarwinSDKUmbrellaAliasIsContainedAndContentBound(t *testing.T) {
	root := t.TempDir()
	sdk := filepath.Join(root, "SDK")
	name := "usr/lib/libSystem.tbd"
	target := filepath.Join(sdk, "usr/lib/libSystem.B.tbd")
	writeFixture(t, target, "synthetic system umbrella")
	alias := filepath.Join(sdk, filepath.FromSlash(name))
	if err := os.Symlink("libSystem.B.tbd", alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("fixture requires symlink creation privilege")
		}
		t.Fatal(err)
	}
	hash, err := sdkInputHash(sdk, name)
	if err != nil || hash != bytesHash([]byte("synthetic system umbrella")) {
		t.Fatalf("in-SDK alias did not hash resolved content: %v", err)
	}
	writeFixture(t, target, "changed system umbrella")
	changed, err := sdkInputHash(sdk, name)
	if err != nil || changed == hash {
		t.Fatal("SDK alias concealed changed content")
	}
	outside := filepath.Join(root, "outside.tbd")
	writeFixture(t, outside, "synthetic outside SDK")
	for _, bad := range []string{outside, "missing.tbd", "."} {
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(bad, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := sdkInputHash(sdk, name); err == nil {
			t.Fatal("accepted outside, broken or directory SDK alias")
		}
	}
}

func TestDarwinSDKRequiresUmbrellaWithoutStandaloneDNSStub(t *testing.T) {
	probe := nativeProbeFixture(t)
	n, env, err := probe()
	if err != nil {
		t.Fatal(err)
	}
	var sdk string
	for _, entry := range env {
		if strings.HasPrefix(entry, "SDKROOT=") {
			sdk = strings.TrimPrefix(entry, "SDKROOT=")
		}
	}
	if sdk == "" {
		t.Fatal("missing selected SDK")
	}
	if _, err := os.Stat(filepath.Join(sdk, "usr/lib/libdns_sd.tbd")); !os.IsNotExist(err) {
		t.Fatal("fixture must not depend on a standalone DNS-SD stub")
	}
	for _, input := range n.SDKInputs {
		if input.Path == "usr/lib/libdns_sd.tbd" {
			t.Fatal("recorded nonexistent standalone DNS-SD input")
		}
	}
	if err := os.Remove(filepath.Join(sdk, "usr/lib/libSystem.tbd")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := probe(); err == nil {
		t.Fatal("accepted missing system umbrella")
	}
}
