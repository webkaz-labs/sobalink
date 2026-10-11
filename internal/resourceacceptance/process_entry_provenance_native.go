//go:build resource_process_native

package resourceacceptance

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"io/fs"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
	assets "github.com/webkaz-labs/sobalink/web"
)

// These are linker-only immutable claims bound by the separate parent build
// receipt. Empty defaults fail closed. No runtime setter or environment fallback.
var (
	processEntrySourceCommit             string
	processEntrySourceTree               string
	processEntrySourceManifestSHA256     string
	processEntryDependencyManifestSHA256 string
	processEntryToolchainSHA256          string
	processEntryAssetSHA256              string
	processEntryBuildConfigurationSHA256 string
)

const processEntryTags = "resource_process_native,directlan_activation_native,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy"

type processEntryBuild struct {
	commit, tree                                           [20]byte
	source, dependencies, toolchain, assets, configuration [32]byte
}

func processEntryDecodeHex(value string, out []byte) bool {
	if !processEntryHex(value, len(out)*2) {
		return false
	}
	n, err := hex.Decode(out, []byte(value))
	if err != nil || n != len(out) {
		return false
	}
	for _, b := range out {
		if b != 0 {
			return true
		}
	}
	return false
}

func processEntryPinnedBuild() (processEntryBuild, bool) {
	var b processEntryBuild
	ok := processEntryDecodeHex(processEntrySourceCommit, b.commit[:]) && processEntryDecodeHex(processEntrySourceTree, b.tree[:]) && processEntryDecodeHex(processEntrySourceManifestSHA256, b.source[:]) && processEntryDecodeHex(processEntryDependencyManifestSHA256, b.dependencies[:]) && processEntryDecodeHex(processEntryToolchainSHA256, b.toolchain[:]) && processEntryDecodeHex(processEntryAssetSHA256, b.assets[:]) && processEntryDecodeHex(processEntryBuildConfigurationSHA256, b.configuration[:])
	if !ok {
		return b, false
	}
	info, ok := debug.ReadBuildInfo()
	return b, ok && processEntryBuildInfo(info, processEntrySourceCommit, runtime.GOARCH)
}

func processEntryBuildInfo(info *debug.BuildInfo, commit, arch string) bool {
	if info == nil || info.GoVersion != "go1.27.1" || info.Path != "github.com/webkaz-labs/sobalink/cmd/soba" || info.Main.Path != "github.com/webkaz-labs/sobalink" || !processEntryHex(commit, 40) || (arch != "amd64" && arch != "arm64") || len(info.Settings) > 32 {
		return false
	}
	keys := [...]string{"-buildmode", "-compiler", "-tags", "-trimpath", "CGO_ENABLED", "GOARCH", "GOOS", "vcs", "vcs.revision", "vcs.time", "vcs.modified"}
	want := [...]string{"exe", "gc", processEntryTags, "true", "0", arch, "linux", "git", commit, "", "false"}
	var seen [11]bool
	feature, pgo := false, false
	for _, setting := range info.Settings {
		found := false
		for i, key := range keys {
			if setting.Key != key {
				continue
			}
			if seen[i] {
				return false
			}
			seen[i], found = true, true
			if key == "vcs.time" {
				value, err := time.Parse(time.RFC3339, setting.Value)
				if err != nil || value.UTC().Format(time.RFC3339) != setting.Value {
					return false
				}
			} else if setting.Value != want[i] {
				return false
			}
			break
		}
		if found {
			continue
		}
		if !feature && (arch == "amd64" && setting.Key == "GOAMD64" && setting.Value == "v1" || arch == "arm64" && setting.Key == "GOARM64" && setting.Value == "v8.0") {
			feature = true
			continue
		}
		// PGO must be explicitly disabled in the frozen build; absence is also
		// valid when the toolchain does not record the default disabled setting.
		if !pgo && setting.Key == "-pgo" && setting.Value == "off" {
			pgo = true
			continue
		}
		return false
	}
	for _, value := range seen {
		if !value {
			return false
		}
	}
	return feature
}

func processEntryBuildMatches(b processEntryBuild, bootstrap processmodel.Bootstrap) bool {
	return b.commit == bootstrap.SourceCommit && b.tree == bootstrap.SourceTree && b.source == bootstrap.SourceManifestSHA256 && b.assets == bootstrap.AssetSHA256
}

type processEntryAsset struct {
	name   string
	size   uint64
	digest string
}

var processEntryAssets = [...]processEntryAsset{
	{"assets/device-card-worker-B6z6pjDa.js", 42457, "0ff8e4671d58b310d51e1bcac017ac8e7f91a11e346c8c5a1a678f409bf3531c"},
	{"assets/index-CYtXKyG9.css", 84855, "3922251d93352a936096d4af8c9f55392b7079269b48b4d06e1d4532efa9498b"},
	{"assets/index-CiC27tac.js", 1006346, "a1f2d14ed40b0b7b72893dfcc682788c02305bea61e477eec93280674cb154ab"},
	{"assets/png-reader-notices.txt", 178208, "3e7975d38af01a4537dedb6bc1541c6af4e3b8d187af5404809a7351ee2ddcf1"},
	{"assets/zxing_reader-_HcWiliU.wasm", 966895, "aecc1876de036c62c8419f67a5e1a16b1698a325bcd190aa84810d516e263931"},
	{"index.html", 490, "6fac486866b7861a1d860008a5423fdc0390407f790375c3be2125cf498bcebb"},
	{"png-worker-manifest.json", 137, "c0e5b7b9ef490e5b0565348471b0d15ee964eb6ba3cfd3599cd5e5836b0e644f"},
}

func processEntryAssetDigest() ([32]byte, bool) {
	var result [32]byte
	files, err := assets.Assets()
	if err != nil {
		return result, false
	}
	root, err := fs.ReadDir(files, ".")
	if err != nil || len(root) != 3 || root[0].Name() != "assets" || !root[0].IsDir() || root[1].Name() != "index.html" || root[1].IsDir() || root[2].Name() != "png-worker-manifest.json" || root[2].IsDir() {
		return result, false
	}
	children, err := fs.ReadDir(files, "assets")
	if err != nil || len(children) != 5 {
		return result, false
	}
	for i, child := range children {
		if child.IsDir() || "assets/"+child.Name() != processEntryAssets[i].name {
			return result, false
		}
	}
	aggregate := sha256.New()
	_, _ = aggregate.Write([]byte("sobalink-p1-assets-v1\x00"))
	var scalar [8]byte
	binary.BigEndian.PutUint16(scalar[:2], uint16(len(processEntryAssets)))
	_, _ = aggregate.Write(scalar[:2])
	var buffer [32768]byte
	for _, asset := range processEntryAssets {
		if asset.size > 2<<20 {
			return result, false
		}
		file, openErr := files.Open(asset.name)
		if openErr != nil {
			return result, false
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() != int64(asset.size) {
			_ = file.Close()
			return result, false
		}
		h := sha256.New()
		remaining := asset.size
		valid := true
		for remaining > 0 {
			size := uint64(len(buffer))
			if remaining < size {
				size = remaining
			}
			n, readErr := io.ReadFull(file, buffer[:size])
			if readErr != nil || uint64(n) != size {
				valid = false
				break
			}
			_, _ = h.Write(buffer[:n])
			remaining -= uint64(n)
		}
		if closeErr := file.Close(); closeErr != nil {
			valid = false
		}
		var expected [32]byte
		if !valid || !processEntryDecodeHex(asset.digest, expected[:]) {
			return result, false
		}
		actual := h.Sum(nil)
		if string(actual) != string(expected[:]) {
			return result, false
		}
		binary.BigEndian.PutUint16(scalar[:2], uint16(len(asset.name)))
		_, _ = aggregate.Write(scalar[:2])
		_, _ = aggregate.Write([]byte(asset.name))
		binary.BigEndian.PutUint64(scalar[:], asset.size)
		_, _ = aggregate.Write(scalar[:])
		_, _ = aggregate.Write(expected[:])
	}
	copy(result[:], aggregate.Sum(nil))
	return result, true
}
