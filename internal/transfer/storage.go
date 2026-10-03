package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/diskspace"
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
	return prepareDestinationWithSpace(destination, entries, diskspace.Process, diskspace.DefaultReserveBytes)
}

func prepareDestinationWithSpace(destination string, entries []Entry, space *diskspace.Guard, reserve int64) (openedRoot *os.Root, actualDirectory, stagingName string, resultErr error) {
	defer func() { resultErr = diskspace.NormalizeError(resultErr) }()
	parent, err := openDestination(destination)
	if err != nil {
		return nil, "", "", err
	}
	defer parent.Close()
	if err := checkRootSpace(context.Background(), parent, space, reserve); err != nil {
		return nil, "", "", err
	}
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
	if err = checkRootSpace(context.Background(), root, space, reserve); err != nil {
		return nil, "", "", err
	}
	if err = root.Mkdir(stage, 0700); err != nil {
		return nil, "", "", err
	}
	for _, e := range entries {
		if err = ensureDirectoriesWithSpace(context.Background(), root, path.Dir(e.Path), space, reserve); err != nil {
			return nil, "", "", err
		}
		if e.Kind == Directory {
			if err = checkRootSpace(context.Background(), root, space, reserve); err != nil {
				return nil, "", "", err
			}
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
func checkRootSpace(ctx context.Context, root *os.Root, space *diskspace.Guard, reserve int64) error {
	volume, err := root.Open(".")
	if err != nil {
		return diskspace.ErrUnknown
	}
	defer volume.Close()
	return space.Check(ctx, volume, reserve)
}

func ensureDirectories(root *os.Root, name string) error {
	return ensureDirectoriesWithSpace(context.Background(), root, name, diskspace.Process, diskspace.DefaultReserveBytes)
}

func ensureDirectoriesWithSpace(ctx context.Context, root *os.Root, name string, space *diskspace.Guard, reserve int64) error {
	if name == "." {
		return nil
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		part := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(part)
		if errors.Is(err, fs.ErrNotExist) {
			if err = checkRootSpace(ctx, root, space, reserve); err != nil {
				return err
			}
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
	return commitFileWithSpace(context.Background(), root, temp, name, original, diskspace.Process, diskspace.DefaultReserveBytes, nil)
}

func commitFileWithSpace(ctx context.Context, root *os.Root, temp, name string, original fs.FileInfo, space *diskspace.Guard, reserve int64, beforeLink func() error) error {
	if err := ensureDirectoriesWithSpace(ctx, root, path.Dir(name), space, reserve); err != nil {
		return err
	}
	current, err := root.Lstat(temp)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(original, current) {
		return ErrUnsafePath
	}
	if err = checkRootSpace(ctx, root, space, reserve); err != nil {
		return err
	}
	if beforeLink != nil {
		if err := beforeLink(); err != nil {
			return err
		}
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
