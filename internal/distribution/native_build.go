package distribution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The pinned Go 1.27.1 linker already targets macOS 13.0. Never inherit the
// runner's newer deployment target. This is not oldest-OS runtime acceptance.
const darwinDeploymentTarget = "13.0"
const certstoreModule = "github.com/tailscale/certstore"
const certstoreVersion = "v0.1.1-0.20260409135935-3638fb84b77d"
const certstoreSum = "h1:JcGKBZAL7ePLwOhUdN8qGQZlP5GueEiIZwY7R62pejE="
const nearbyPackage = Project + "/internal/nearby"

var darwinSystemLibraries = []string{
	"/System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation",
	"/System/Library/Frameworks/Security.framework/Versions/A/Security",
	"/usr/lib/libSystem.B.dylib",
	"/usr/lib/libresolv.9.dylib",
}
var darwinSDKInputs = []string{
	"SDKSettings.json",
	"System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation.tbd",
	"System/Library/Frameworks/Security.framework/Versions/A/Security.tbd",
	"usr/include/dispatch/dispatch.h",
	"usr/include/dns_sd.h",
	"usr/lib/libSystem.tbd",
	"usr/lib/libresolv.9.tbd",
}

// NativeBuild contains bounded public facts, never host paths or raw output.
// SDKInputs identifies reviewed headers/link stubs, not the complete SDK tree.
type NativeBuild struct {
	Schema           int             `json:"schema"`
	HostOS           string          `json:"host_os"`
	HostArch         string          `json:"host_arch"`
	HostVersion      string          `json:"host_version"`
	HostBuild        string          `json:"host_build"`
	CompilerVersion  string          `json:"compiler_version"`
	CompilerSHA256   string          `json:"compiler_sha256"`
	LinkerSHA256     string          `json:"linker_sha256"`
	XcodeVersion     string          `json:"xcode_version"`
	XcodeBuild       string          `json:"xcode_build"`
	SDKVersion       string          `json:"sdk_version"`
	SDKBuild         string          `json:"sdk_build"`
	DeploymentTarget string          `json:"deployment_target"`
	SDKInputs        []Notice        `json:"sdk_inputs"`
	CFlags           []string        `json:"cflags"`
	LDFlags          []string        `json:"ldflags"`
	Packages         []NativePackage `json:"packages"`
	PolicyInputs     []Notice        `json:"policy_inputs"`
	Libraries        []string        `json:"system_libraries"`
}
type NativePackage struct {
	Package  string   `json:"package"`
	Inputs   []Notice `json:"inputs"`
	CFlags   []string `json:"cflags"`
	CPPFlags []string `json:"cppflags"`
	CXXFlags []string `json:"cxxflags"`
	LDFlags  []string `json:"ldflags"`
}

func cleanBuildEnvironment() []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CGO_") || strings.HasPrefix(key, "DYLD_") {
			continue
		}
		switch key {
		case "GOCACHEPROG", "CC", "CXX", "FC", "PKG_CONFIG", "CPATH", "C_INCLUDE_PATH", "CPLUS_INCLUDE_PATH", "OBJC_INCLUDE_PATH", "LIBRARY_PATH", "COMPILER_PATH", "GCC_EXEC_PREFIX", "CCC_OVERRIDE_OPTIONS", "SDKROOT", "DEVELOPER_DIR", "MACOSX_DEPLOYMENT_TARGET":
			continue
		}
		env = append(env, entry)
	}
	return env
}

func nativeCommand(command string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C", "LANG=C"}
	var out limitedNativeOutput
	cmd.Stdout = &out
	cmd.WaitDelay = time.Second
	// Compiler and SDK diagnostics can contain machine paths. Do not forward them.
	if err := cmd.Run(); err != nil || out.over {
		return nil, errors.New("native toolchain query failed")
	}
	return out.Bytes(), nil
}

type limitedNativeOutput struct {
	buffer bytes.Buffer
	over   bool
}

func (b *limitedNativeOutput) Len() int      { return b.buffer.Len() }
func (b *limitedNativeOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *limitedNativeOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len()+n > 16384 {
		b.over = true
		return n, nil
	}
	_, err := b.buffer.Write(p)
	return n, err
}

var nativeVersion = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3}){1,2}$`)
var nativeBuildID = regexp.MustCompile(`^[0-9A-Za-z.]{1,32}$`)
var appleCompiler = regexp.MustCompile(`^Apple clang version [0-9.]{3,24} \(clang-[0-9.]{3,40}\)$`)

func probeDarwinToolchain() (*NativeBuild, []string, error) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return nil, nil, errors.New("Darwin packaging requires a native darwin-arm64 host")
	}
	return probeDarwinWith(nativeCommand)
}
func probeDarwinWith(run func(string, ...string) ([]byte, error)) (*NativeBuild, []string, error) {
	query := func(command string, args ...string) (string, error) {
		b, e := run(command, args...)
		return strings.TrimSpace(string(b)), e
	}
	get := func(command string, args ...string) string {
		v, e := query(command, args...)
		if e != nil {
			return ""
		}
		return v
	}
	cc := get("/usr/bin/xcrun", "--sdk", "macosx", "--find", "clang")
	ld := get("/usr/bin/xcrun", "--sdk", "macosx", "--find", "ld")
	sdk := get("/usr/bin/xcrun", "--sdk", "macosx", "--show-sdk-path")
	developer := get("/usr/bin/xcode-select", "-p")
	for _, path := range []string{cc, ld, sdk, developer} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
			return nil, nil, errors.New("invalid native toolchain location")
		}
	}
	compiler := strings.Split(get(cc, "--version"), "\n")[0]
	xcode := strings.Split(get("/usr/bin/xcodebuild", "-version"), "\n")
	if !appleCompiler.MatchString(compiler) || len(xcode) != 2 || !strings.HasPrefix(xcode[0], "Xcode ") || !strings.HasPrefix(xcode[1], "Build version ") {
		return nil, nil, errors.New("Apple clang and Xcode identities are required")
	}
	n := &NativeBuild{Schema: 1, HostOS: "darwin", HostArch: "arm64", HostVersion: get("/usr/bin/sw_vers", "-productVersion"), HostBuild: get("/usr/bin/sw_vers", "-buildVersion"), CompilerVersion: compiler, XcodeVersion: strings.TrimPrefix(xcode[0], "Xcode "), XcodeBuild: strings.TrimPrefix(xcode[1], "Build version "), SDKVersion: get("/usr/bin/xcrun", "--sdk", "macosx", "--show-sdk-version"), SDKBuild: get("/usr/bin/xcrun", "--sdk", "macosx", "--show-sdk-build-version"), DeploymentTarget: darwinDeploymentTarget, CFlags: []string{"-O2", "-g0", "-isysroot", "${SDKROOT}", "-mmacosx-version-min=13.0"}, LDFlags: []string{"-isysroot", "${SDKROOT}", "-mmacosx-version-min=13.0"}}
	for _, v := range []string{n.HostVersion, n.XcodeVersion, n.SDKVersion} {
		if !nativeVersion.MatchString(v) {
			return nil, nil, errors.New("invalid native version identity")
		}
	}
	for _, v := range []string{n.HostBuild, n.XcodeBuild, n.SDKBuild} {
		if !nativeBuildID.MatchString(v) {
			return nil, nil, errors.New("invalid native build identity")
		}
	}
	var err error
	if n.CompilerSHA256, err = fileHash(cc); err != nil {
		return nil, nil, errors.New("cannot identify native compiler")
	}
	if n.LinkerSHA256, err = fileHash(ld); err != nil {
		return nil, nil, errors.New("cannot identify native linker")
	}
	for _, rel := range darwinSDKInputs {
		hash, e := sdkInputHash(sdk, rel)
		if e != nil {
			return nil, nil, fmt.Errorf("missing reviewed SDK input: %s", rel)
		}
		n.SDKInputs = append(n.SDKInputs, Notice{rel, hash})
	}
	cflags := "-O2 -g0 -isysroot " + strconv.Quote(sdk) + " -mmacosx-version-min=13.0"
	ldflags := "-isysroot " + strconv.Quote(sdk) + " -mmacosx-version-min=13.0"
	env := []string{"CGO_ENABLED=1", "CC=" + strconv.Quote(cc), "CXX=" + strconv.Quote(cc), "SDKROOT=" + sdk, "DEVELOPER_DIR=" + developer, "MACOSX_DEPLOYMENT_TARGET=13.0", "CGO_CFLAGS=" + cflags, "CGO_CPPFLAGS=", "CGO_CXXFLAGS=" + cflags, "CGO_FFLAGS=", "CGO_LDFLAGS=" + ldflags}
	return n, env, nil
}

// SDK aliases are permitted only within the selected SDK. Record the public
// input name and resolved content hash without exposing installation paths.
func sdkInputHash(sdk, name string) (string, error) {
	root, err := filepath.EvalSymlinks(sdk)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("SDK input escapes selected SDK")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("SDK input is not a regular file")
	}
	return fileHash(path)
}

// These exact source identities keep the audited no-policy/default-empty
// credential boundary tied to its review. Dependency upgrades require review.
var darwinPolicyInputs = map[string]string{
	"tailscale.com/control/controlclient/sign_supported.go":     "2319d740882481d8cf4f1dfd1f1ac853b678971928fd176ecfe5be1ffda9082b",
	"tailscale.com/util/syspolicy/policyclient/policyclient.go": "e7fa6ea83699de60241f0f96c018c66ec1fb1061860435c7a325097ce5ebbaba",
	"tailscale.com/tsd/tsd.go":                                  "255dee50953d2a19b3dd8e57a21d9bc2c3d10095a4828fb6d1126776a645383d",
	"github.com/tailscale/certstore/certstore.go":               "4c63ea6a06aeac9f027de208d1113997867b1ad77bf75d91f12740ba3df5b977",
	"github.com/tailscale/certstore/certstore_darwin.go":        "4e2698e090e5cec688c4d7d3d5178d4233b070c47c60fbb9e73cf1430179c117",
}
var darwinPolicyImporters = []string{"tailscale.com/client/web", "tailscale.com/control/controlclient", "tailscale.com/ipn", "tailscale.com/ipn/ipnauth", "tailscale.com/ipn/ipnlocal", "tailscale.com/net/dns", "tailscale.com/tsd", "tailscale.com/util/syspolicy/rsop"}

func darwinNativeInventory(packages []goPackage) ([]NativePackage, []Notice, error) {
	byName := map[string]goPackage{}
	var cgoNames, importers []string
	var policy []Notice
	for _, p := range packages {
		if _, ok := byName[p.ImportPath]; ok {
			return nil, nil, errors.New("duplicate native package")
		}
		byName[p.ImportPath] = p
		reviewedRoutecat := false
		if p.ImportPath == routecatPackage {
			inputs, err := darwinRoutecatPolicyInventory(p)
			if err != nil {
				return nil, nil, err
			}
			policy = append(policy, inputs...)
			reviewedRoutecat = true
		}
		if len(p.CgoFiles) > 0 {
			cgoNames = append(cgoNames, p.ImportPath)
		}
		switch p.ImportPath {
		case "tailscale.com/util/syspolicy", "tailscale.com/feature/syspolicy", "tailscale.com/feature/posture", "tailscale.com/posture", "tailscale.com/ipn/desktop":
			return nil, nil, errors.New("unreviewed platform policy provider")
		}
		for _, imp := range p.Imports {
			if imp == certstoreModule && p.ImportPath != "tailscale.com/control/controlclient" {
				return nil, nil, errors.New("unreviewed certificate store importer")
			}
			if imp == "tailscale.com/util/syspolicy/policyclient" {
				importers = append(importers, p.ImportPath)
			}
			if p.Module != nil && p.Module.Main && (imp == certstoreModule || imp == "tailscale.com/tsd" || strings.HasPrefix(imp, "tailscale.com/util/syspolicy")) {
				// Only the exact reviewed adapted source can use the zero-value
				// subsystem container. This never permits a policy/store import.
				if reviewedRoutecat && imp == "tailscale.com/tsd" {
					continue
				}
				return nil, nil, errors.New("application must not configure platform policy or credentials")
			}
		}
	}
	sort.Strings(cgoNames)
	sort.Strings(importers)
	if !reflect.DeepEqual(cgoNames, []string{certstoreModule, nearbyPackage, "runtime/cgo"}) || !reflect.DeepEqual(importers, darwinPolicyImporters) {
		return nil, nil, errors.New("Darwin cgo or policy-client closure differs from review")
	}
	cert := byName[certstoreModule]
	if cert.Module == nil || cert.Module.Path != certstoreModule || cert.Module.Version != certstoreVersion || cert.Module.Sum != certstoreSum || cert.Module.Replace != nil {
		return nil, nil, errors.New("unreviewed certificate store source")
	}
	for path, want := range darwinPolicyInputs {
		pkg, name := filepath.ToSlash(filepath.Dir(path)), filepath.Base(path)
		p, ok := byName[pkg]
		if !ok {
			return nil, nil, errors.New("missing reviewed credential boundary")
		}
		data, e := readSourceFile(p.Dir, name)
		if e != nil || bytesHash(data) != want {
			return nil, nil, errors.New("credential boundary source differs from review")
		}
		policy = append(policy, Notice{path, want})
	}
	sort.Slice(policy, func(i, j int) bool { return policy[i].Path < policy[j].Path })
	var records []NativePackage
	for _, name := range cgoNames {
		p := byName[name]
		r := NativePackage{Package: name, CFlags: append([]string{}, p.CgoCFLAGS...), CPPFlags: append([]string{}, p.CgoCPPFLAGS...), CXXFlags: append([]string{}, p.CgoCXXFLAGS...), LDFlags: append([]string{}, p.CgoLDFLAGS...)}
		if len(p.CgoCPPFLAGS) > 0 || len(p.CgoCXXFLAGS) > 0 || len(p.CgoPkgConfig) > 0 || len(p.CgoFFLAGS) > 0 || len(p.FFiles) > 0 || len(p.SwigFiles) > 0 || len(p.SwigCXXFiles) > 0 {
			return nil, nil, errors.New("unreviewed native build mechanism")
		}
		if name == nearbyPackage && (!reflect.DeepEqual(p.CgoFiles, []string{"provider_darwin.go"}) || !reflect.DeepEqual(p.CFiles, []string{"dnssd_bridge_darwin.c"}) || !reflect.DeepEqual(p.HFiles, []string{"dnssd_bridge_darwin.h"}) || !reflect.DeepEqual(p.CgoCFLAGS, []string{"-std=c11"}) || len(p.CgoLDFLAGS)+len(p.CgoCPPFLAGS)+len(p.CgoCXXFLAGS)+len(p.CXXFiles)+len(p.MFiles) > 0) {
			return nil, nil, errors.New("unreviewed DNS-SD native inputs")
		}
		if name == certstoreModule && (len(p.CFiles)+len(p.CXXFiles)+len(p.MFiles)+len(p.HFiles)+len(p.SFiles)+len(p.SysoFiles)+len(p.EmbedFiles) > 0 || !reflect.DeepEqual(p.GoFiles, []string{"certstore.go"}) || !reflect.DeepEqual(p.CgoFiles, []string{"certstore_darwin.go"}) || !reflect.DeepEqual(p.CgoCFLAGS, []string{"-x", "objective-c"}) || !reflect.DeepEqual(p.CgoLDFLAGS, []string{"-framework", "CoreFoundation", "-framework", "Security"})) {
			return nil, nil, errors.New("unreviewed certificate store native flags")
		}
		if name == "runtime/cgo" && (!reflect.DeepEqual(p.CgoCFLAGS, []string{"-Wall", "-Werror", "-fno-stack-protector", "-Wdeclaration-after-statement", "-std=gnu90"}) || len(p.CgoLDFLAGS) > 0) {
			return nil, nil, errors.New("unreviewed Go runtime native flags")
		}
		names := map[string]bool{}
		for _, list := range [][]string{p.GoFiles, p.CgoFiles, p.CFiles, p.CXXFiles, p.MFiles, p.HFiles, p.SFiles, p.SysoFiles, p.EmbedFiles} {
			for _, file := range list {
				if names[file] {
					return nil, nil, errors.New("duplicate native source input")
				}
				names[file] = true
			}
		}
		for file := range names {
			data, e := readSourceFile(p.Dir, file)
			if e != nil {
				return nil, nil, e
			}
			r.Inputs = append(r.Inputs, Notice{file, bytesHash(data)})
		}
		sort.Slice(r.Inputs, func(i, j int) bool { return r.Inputs[i].Path < r.Inputs[j].Path })
		records = append(records, r)
	}
	return records, policy, nil
}

// darwinBuildUUID is a public build identity, not an integrity digest. The
// separate full binary SHA256 remains authoritative for content provenance.
// Build validates the commit and version before this ASCII-only derivation.
func darwinBuildUUID(commit, version, nativeIdentity string) string {
	identity := "sobalink-darwin-uuid-v1\n" + commit + "\n" + version + "\ndarwin-arm64\n" + GoVersion + "\n" + BuildTags + "\n" + nativeIdentity + "\n"
	hash := sha256.Sum256([]byte(identity))
	uuid := hash[:16]
	uuid[6] = (uuid[6] & 0x0f) | 0x80 // Custom UUID version 8.
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // RFC variant.
	return hex.EncodeToString(uuid)
}

// Derive before linking without mutating the inventory. Final inspection must
// independently prove these exact libraries and the expected UUID in the binary.
func darwinUUIDNativeDigest(native *NativeBuild) string {
	normalized := *native
	normalized.Libraries = append([]string{}, darwinSystemLibraries...)
	return nativeDigest(&normalized)
}

var darwinUUIDPattern = regexp.MustCompile(`^[0-9a-f]{12}8[0-9a-f]{3}[89ab][0-9a-f]{15}$`)

func inspectDarwinLibraries(path, expectedUUID string) ([]string, error) {
	if !darwinUUIDPattern.MatchString(expectedUUID) {
		return nil, errors.New("invalid expected Darwin UUID")
	}
	uuid, _ := hex.DecodeString(expectedUUID)
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read Darwin binary")
	}
	defer f.Close()
	// Read and bound the command region before allocating or parsing it. This
	// inspector does not load symbols, sections, or other file-sized structures.
	var header [32]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return nil, errors.New("incomplete Mach-O header")
	}
	order := binary.LittleEndian
	if order.Uint32(header[:4]) != 0xfeedfacf || order.Uint32(header[4:8]) != 0x100000c || order.Uint32(header[12:16]) != 2 {
		return nil, errors.New("unexpected Darwin binary target")
	}
	count, size := order.Uint32(header[16:20]), order.Uint32(header[20:24])
	if count == 0 || count > 1024 || size == 0 || size > 1024*1024 || count*8 > size {
		return nil, errors.New("unbounded Mach-O load commands")
	}
	commands := make([]byte, int(size))
	if _, err := io.ReadFull(f, commands); err != nil {
		return nil, errors.New("incomplete Mach-O load commands")
	}
	var libraries []string
	var deployment, foundUUID bool
	offset := 0
	for range count {
		if len(commands)-offset < 8 {
			return nil, errors.New("short Mach-O load command")
		}
		command := order.Uint32(commands[offset : offset+4])
		length := order.Uint32(commands[offset+4 : offset+8])
		if length < 8 || length%8 != 0 || uint64(length) > uint64(len(commands)-offset) {
			return nil, errors.New("invalid Mach-O load command length")
		}
		raw := commands[offset : offset+int(length)]
		offset += int(length)
		switch command {
		case 0x8000001c:
			return nil, errors.New("Darwin binary has an unreviewed runtime search path")
		case 0x1b:
			if foundUUID || len(raw) != 24 || !bytes.Equal(raw[8:], uuid) {
				return nil, errors.New("invalid or unexpected Darwin UUID")
			}
			foundUUID = true
		case 0xc, 0x80000018, 0x8000001f, 0x20, 0x80000023:
			if len(raw) < 24 {
				return nil, errors.New("invalid Mach-O library command")
			}
			start := order.Uint32(raw[8:12])
			if start < 24 || uint64(start) >= uint64(len(raw)) {
				return nil, errors.New("invalid Mach-O library name")
			}
			end := bytes.IndexByte(raw[start:], 0)
			if end < 0 {
				return nil, errors.New("unterminated Mach-O library name")
			}
			libraries = append(libraries, string(raw[start:int(start)+end]))
		case 0x32:
			if deployment || len(raw) < 24 || order.Uint32(raw[8:12]) != 1 || order.Uint32(raw[12:16]) != 13<<16 || uint64(order.Uint32(raw[20:24]))*8+24 != uint64(len(raw)) {
				return nil, errors.New("unexpected Darwin deployment target")
			}
			deployment = true
		case 0x24, 0x25, 0x2f, 0x30:
			return nil, errors.New("unreviewed Mach-O deployment command")
		}
	}
	if offset != len(commands) || !deployment || !foundUUID {
		return nil, errors.New("incomplete Mach-O identity or load commands")
	}
	sort.Strings(libraries)
	if !reflect.DeepEqual(libraries, darwinSystemLibraries) {
		return nil, errors.New("Darwin system linkage differs from review")
	}
	return libraries, nil
}
func nativeDigest(n *NativeBuild) string {
	b, _ := json.Marshal(n)
	var value any
	_ = json.Unmarshal(b, &value)
	b, _ = json.Marshal(value)
	return bytesHash(b)
}
