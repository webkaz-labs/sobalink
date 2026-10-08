package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// SecureChildDirectory creates or validates one private child directory and
// certifies both its contents and its binding in the parent before returning.
// Callers provide lifecycle serialization. A failed sync is never success,
// including on filesystems without native directory durability support.
func SecureChildDirectory(parentPath, name string) error {
	return secureChildDirectory(parentPath, name, atomicSyncBoundDirectory)
}

func secureChildDirectory(parentPath, name string, syncDirectory func(string, *os.File) error) (err error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return errors.New("invalid private directory name")
	}
	parent, err := atomicOpenDirectory(parentPath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	before, err := atomicFileMetadata(parent)
	if err != nil {
		return err
	}
	if !before.directory || !before.private {
		return errors.New("private parent directory required")
	}
	childInfo, err := atomicChildMetadata(parent, name)
	if errors.Is(err, os.ErrNotExist) {
		if err = atomicMkdir(parent, name); err != nil {
			return err
		}
		childInfo, err = atomicChildMetadata(parent, name)
	}
	if err != nil {
		return err
	}
	if !childInfo.directory || !childInfo.private {
		return errors.New("private child directory required")
	}
	child, err := atomicOpenChild(parent, name, false, true, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, child.Close()) }()
	opened, err := atomicFileMetadata(child)
	if err != nil {
		return err
	}
	if opened.id != childInfo.id {
		return errors.New("private directory changed")
	}
	if err = syncDirectory(filepath.Join(parentPath, name), child); err != nil {
		return err
	}
	if err = syncDirectory(parentPath, parent); err != nil {
		return err
	}
	current, err := atomicChildMetadata(parent, name)
	if err != nil {
		return err
	}
	bound, err := atomicOpenDirectory(parentPath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, bound.Close()) }()
	binding, err := atomicFileMetadata(bound)
	if err != nil {
		return err
	}
	if binding.id != before.id || current.id != opened.id {
		return errors.New("private directory binding changed")
	}
	return nil
}
