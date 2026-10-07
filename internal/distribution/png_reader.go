package distribution

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const pngReaderRoot = "web/vendor/zxing-wasm-3.1.5"
const pngReaderIntegrity = "sha512-jmxXvTCR/qZxMz2XwR6V+nL2BlDTD1aux7ZWSH9BV77TG9u4k0VyHba/NCCHBHJ60Wg6VV0QL/SMlJczKhJI4w=="
const pngReaderWASMHash = "aecc1876de036c62c8419f67a5e1a16b1698a325bcd190aa84810d516e263931"
const pngReaderNoticesHash = "3e7975d38af01a4537dedb6bc1541c6af4e3b8d187af5404809a7351ee2ddcf1"

// This entry identifies an unchanged official prebuilt subset, not a rebuilt or
// modified-source component. Archive integrity and transitive-source limits stay
// in the accompanying inventory. Pin changes require explicit source review.
func pngReaderInventory(root, share string) (*FrontendModule, error) {
	_, vendorErr := os.Lstat(filepath.Join(root, filepath.FromSlash(pngReaderRoot)))
	emitted, err := filepath.Glob(filepath.Join(root, "web/dist/assets/zxing_reader-*.wasm"))
	if err != nil {
		return nil, err
	}
	// Small packaging fixtures without the optional reader remain valid.
	if errors.Is(vendorErr, os.ErrNotExist) && len(emitted) == 0 {
		return nil, nil
	}
	pins := map[string]string{
		"share.js":                 "68b09047a19c17ba74c0bf7e0a4341e77d46b36ae1cae4bdcca31d0c6757f3f2",
		"reader/index.js":          "3c364d8477404a513d7c0629b1cf9e63ae11e01f9746146d3574f8b10a122320",
		"reader/zxing_reader.wasm": pngReaderWASMHash,
		"THIRD_PARTY_NOTICES.txt":  pngReaderNoticesHash,
	}
	for path, hash := range pins {
		raw, err := readSourceFile(root, pngReaderRoot+"/"+path)
		if err != nil || bytesHash(raw) != hash {
			return nil, fmt.Errorf("pinned PNG reader input differs: %s", path)
		}
	}
	if len(emitted) != 1 {
		return nil, errors.New("expected exactly one embedded PNG reader WASM")
	}
	wasmPath, err := filepath.Rel(root, emitted[0])
	if err != nil {
		return nil, err
	}
	raw, err := readSourceFile(root, filepath.ToSlash(wasmPath))
	if err != nil || bytesHash(raw) != pngReaderWASMHash {
		return nil, errors.New("embedded PNG reader WASM differs")
	}
	raw, err = readSourceFile(root, "web/dist/assets/png-reader-notices.txt")
	if err != nil || bytesHash(raw) != pngReaderNoticesHash {
		return nil, errors.New("embedded PNG reader notices missing or different")
	}
	raw, err = readSourceFile(root, "web/dist/png-worker-manifest.json")
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Version int    `json:"version"`
		Path    string `json:"path"`
		SHA256  string `json:"sha256"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.Version != 1 || !regexp.MustCompile(`^assets/device-card-worker-[A-Za-z0-9_-]+\.js$`).MatchString(manifest.Path) {
		return nil, errors.New("invalid PNG worker build manifest")
	}
	raw, err = readSourceFile(root, "web/dist/"+manifest.Path)
	if err != nil || bytesHash(raw) != manifest.SHA256 {
		return nil, errors.New("PNG worker differs from its build manifest")
	}
	module := &FrontendModule{Name: "zxing-wasm", Version: "3.1.5", Integrity: pngReaderIntegrity}
	for _, name := range []string{"LICENSE", "NOTICE_INVENTORY.json", "README.md", "THIRD_PARTY_NOTICES.txt", "UPSTREAM.json"} {
		raw, err := readSourceFile(root, pngReaderRoot+"/"+name)
		if err != nil {
			return nil, err
		}
		destination := "licenses/npm/zxing-wasm@3.1.5/" + name
		if err := os.MkdirAll(filepath.Dir(filepath.Join(share, filepath.FromSlash(destination))), 0755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(share, filepath.FromSlash(destination)), raw, 0644); err != nil {
			return nil, err
		}
		module.Notices = append(module.Notices, Notice{destination, bytesHash(raw)})
	}
	return module, nil
}
