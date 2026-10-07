package engineadaptation

import (
	"errors"
	"path/filepath"
)

// ModulePin is a named, fixed upstream-to-local replacement. ModulePins returns
// a new slice so callers cannot mutate the process-wide selection.
type ModulePin struct {
	Module, Version, ModuleSum, GoModSum, Directory, Replacement string
}

func ModulePins() []ModulePin {
	return []ModulePin{
		{Module, Version, ModuleSum, GoModSum, Directory, Replacement},
		{"github.com/tailscale/wireguard-go", "v0.0.0-20260928213032-417aef361226", "h1:v3Lpj2iHPWQDqeCwemQPz4fWweIEMLqBkwJqCjRyJQc=", "h1:rUelGmuK4UnSJYM5gl5Mknp6YbwwcL8+VAPMhNYe+jg=", ".sobalink-deps/wireguard", "./.sobalink-deps/wireguard"},
		{"gvisor.dev/gvisor", "v0.0.0-20260915211658-a6f909f08a72", "h1:EytKYr5WrVs+Aah/0xDO8ZAZZzS/iTEVC8ln4ipDbL0=", "h1:8aLQqUBHDH8fY5y60lzmwDpMMbQCcT3EBfoSwhfaGCY=", ".sobalink-deps/gvisor", "./.sobalink-deps/gvisor"},
	}
}

func validOutputDirectory(directory string) bool {
	for _, pin := range ModulePins() {
		if directory == pin.Directory {
			return true
		}
	}
	return false
}

// ReadSourceFile refuses links and reparse points in every checkout-relative
// path component, including the supplied root.
func ReadSourceFile(root, relative string) ([]byte, error) { return regular(root, relative) }

// VerifyPinnedTree includes all files, rather than only target-linked sources.
// Existing generated trees are never repaired implicitly.
func VerifyPinnedTree(root, directory, expected string) error {
	if !validOutputDirectory(directory) {
		return errors.New("unrecognized adapted dependency output")
	}
	if _, err := regular(root, directory+"/go.mod"); err != nil {
		return err
	}
	tree, err := readTree(filepath.Join(root, filepath.FromSlash(directory)))
	if err != nil {
		return err
	}
	if treeHash(tree) != expected {
		return errors.New("adapted dependency drift detected; inspect and explicitly remove the generated tree before preparing again")
	}
	return nil
}

// ReadPinnedArchive reads an original module archive only after checking its
// module checksum, go.mod checksum and complete source-tree digest against m.
// It neither downloads dependencies nor writes a generated tree. Callers must
// authenticate their own manifest before calling this lower-level helper.
// Existing Tailscale preparation continues to use its independently pinned
// Load/ValidateInputs path.
func ReadPinnedArchive(path string, m Manifest) (map[string][]byte, error) {
	return readZip(path, m)
}

// ApplyPinnedChanges applies only the exact edits and original/adapted digests
// in m, including its complete output-tree digest. The map is caller-owned and
// must be discarded on error; partially applied changes are not usable output.
func ApplyPinnedChanges(tree map[string][]byte, m Manifest) error {
	return apply(tree, m)
}
