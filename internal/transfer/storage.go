package transfer

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func randomName(prefix string) (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(token[:]), nil
}

func openDestination(destination string) (*os.Root, error) {
	if !validDestination(destination) {
		return nil, ErrUnsafePath
	}
	info, err := os.Lstat(destination)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafePath
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		_ = root.Close()
		return nil, ErrUnsafePath
	}
	return root, nil
}

func prepareDestination(destination string, entries []Entry) (*os.Root, string, string, error) {
	parent, err := openDestination(destination)
	if err != nil {
		return nil, "", "", err
	}
	defer parent.Close()
	name, err := randomName("sobalink-")
	if err != nil {
		return nil, "", "", err
	}
	if err = parent.Mkdir(name, 0700); err != nil {
		return nil, "", "", err
	}
	actual := filepath.Join(destination, name)
	if err = protectDirectory(actual); err != nil {
		return nil, "", "", err
	}
	info, err := parent.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "", "", ErrUnsafePath
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, "", "", err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, "", "", ErrUnsafePath
	}
	ok := false
	defer func() {
		if !ok {
			_ = root.Close()
		}
	}()
	stage, err := randomName(".incoming-")
	if err != nil {
		return nil, "", "", err
	}
	if err = root.Mkdir(stage, 0700); err != nil {
		return nil, "", "", err
	}
	for _, e := range entries {
		if err = ensureDirectories(root, path.Dir(e.Path)); err != nil {
			return nil, "", "", err
		}
		if e.Kind == Directory {
			if err = root.Mkdir(e.Path, 0700); err != nil {
				return nil, "", "", err
			}
		}
	}
	ok = true
	return root, actual, stage, nil
}

// Root confines all operations even when a component is exchanged concurrently.
// The checks additionally reject existing symlinks instead of following them.
// An attacker with write access as the same OS user can still modify received
// files after saving; this package is not a sandbox against that user.
func ensureDirectories(root *os.Root, name string) error {
	if name == "." {
		return nil
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		part := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(part)
		if errors.Is(err, fs.ErrNotExist) {
			if err = root.Mkdir(part, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, err = root.Lstat(part)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafePath
		}
	}
	return nil
}

// commitFile uses a same-filesystem hard link as a cross-platform atomic
// no-replace operation. Filesystems without hard-link support fail closed; it
// never falls back to a rename that could overwrite an existing destination.
func commitFile(root *os.Root, temp, name string, original fs.FileInfo) error {
	if err := ensureDirectories(root, path.Dir(name)); err != nil {
		return err
	}
	current, err := root.Lstat(temp)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(original, current) {
		return ErrUnsafePath
	}
	if err = root.Link(temp, name); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: destination exists", ErrConflict)
		}
		return fmt.Errorf("save without overwrite: %w", err)
	}
	final, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !final.Mode().IsRegular() || !os.SameFile(original, final) {
		return ErrUnsafePath
	}
	return nil
}
