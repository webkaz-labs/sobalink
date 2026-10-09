package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// OpenPrivateChildDirectoryBound validates and opens an existing private child
// without creating, repairing or changing permissions. The caller owns Close.
func OpenPrivateChildDirectoryBound(parent, name string, expected os.FileInfo) (*os.File, error) {
	return openBoundPrivateChild(parent, name, expected, true, false)
}

// OpenPrivateChildFileBound opens an existing private single-link regular file
// under the certified parent. It never follows a symlink or changes authority.
// The caller owns Close and must bound reads and recheck its lifecycle binding.
func OpenPrivateChildFileBound(parent, name string, expected os.FileInfo) (*os.File, error) {
	return openBoundPrivateChild(parent, name, expected, false, false)
}

// CreatePrivateChildDirectoryBound creates only an absent child. EEXIST,
// including a raced creation, is failure rather than adoption. The retained
// returned handle carries the identity certified by child and parent syncs.
func CreatePrivateChildDirectoryBound(parent, name string, expected os.FileInfo) (*os.File, error) {
	return openBoundPrivateChild(parent, name, expected, true, true)
}

// Reuse the private writer's cross-platform handle and metadata primitives;
// no writer lease or second ownership authority is introduced here.
func openBoundPrivateChild(parentPath, name string, expected os.FileInfo, directory, create bool) (result *os.File, err error) {
	if expected == nil || name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return nil, errors.New("invalid bound private child")
	}
	parent, err := atomicOpenDirectory(parentPath)
	if err != nil {
		return nil, err
	}
	var child *os.File
	defer func() {
		err = errors.Join(err, parent.Close())
		if err != nil && child != nil {
			err = errors.Join(err, child.Close())
			result = nil
		}
	}()
	parentInfo, err := parent.Stat()
	if err != nil || !os.SameFile(parentInfo, expected) {
		return nil, errors.New("private parent identity changed")
	}
	before, err := atomicFileMetadata(parent)
	if err != nil {
		return nil, err
	}
	if !before.directory || !before.private {
		return nil, errors.New("private parent directory required")
	}
	if create {
		if !directory {
			return nil, errors.New("unsupported private child creation")
		}
		if err := atomicMkdir(parent, name); err != nil {
			return nil, err
		}
	}
	metadata, err := atomicChildMetadata(parent, name)
	if err != nil {
		return nil, err
	}
	valid := func(m atomicMetadata) bool {
		return m.private && (directory && m.directory || !directory && m.regular && m.singleLink)
	}
	if !valid(metadata) {
		return nil, errors.New("private child type or permissions invalid")
	}
	child, err = atomicOpenChild(parent, name, false, directory, false)
	if err != nil {
		return nil, err
	}
	opened, err := atomicFileMetadata(child)
	if err != nil {
		return nil, err
	}
	if !valid(opened) || opened.id != metadata.id {
		return nil, errors.New("private child identity changed")
	}
	if create {
		if err := atomicSyncBoundDirectory(filepath.Join(parentPath, name), child); err != nil {
			return nil, err
		}
		if err := atomicSyncBoundDirectory(parentPath, parent); err != nil {
			return nil, err
		}
	}
	current, err := atomicChildMetadata(parent, name)
	if err != nil {
		return nil, err
	}
	if !valid(current) || current.id != opened.id {
		return nil, errors.New("private child changed")
	}
	bound, err := atomicOpenDirectory(parentPath)
	if err != nil {
		return nil, err
	}
	binding, metadataErr := atomicFileMetadata(bound)
	closeErr := bound.Close()
	if metadataErr != nil || closeErr != nil {
		return nil, errors.Join(metadataErr, closeErr)
	}
	if !binding.directory || !binding.private || binding.id != before.id {
		return nil, errors.New("private parent binding changed")
	}
	return child, nil
}
