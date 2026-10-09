package config

import (
	"errors"
	"os"
	"path/filepath"
)

// WithOwnership runs a bounded operation while this live lifecycle lock owns
// exactly dir. It cannot mint ownership from a path or a caller-supplied flag.
// Close waits for the operation; callers must not call Close from the callback.
func (l *Lock) WithOwnership(dir string, operation func() error) error {
	if operation == nil {
		return errors.New("profile lifecycle ownership required")
	}
	return l.WithOwnershipInfo(dir, func(os.FileInfo, os.FileInfo) error { return operation() })
}

// WithOwnershipInfo supplies the actual held directory and process-lock
// identities to a bounded publication callback, under the same lifecycle mutex.
// The immutable metadata is not a stand-alone ownership capability; the callback
// must finish before Close can release ownership.
func (l *Lock) WithOwnershipInfo(dir string, operation func(directory, lock os.FileInfo) error) error {
	if l == nil || operation == nil {
		return errors.New("profile lifecycle ownership required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil || l.directory == nil {
		return errors.New("profile lifecycle ownership required")
	}
	directory, err := os.Lstat(dir)
	if err != nil || !directory.IsDir() || !os.SameFile(l.directory, directory) {
		return errors.New("profile lifecycle ownership mismatch")
	}
	opened, err := l.f.Stat()
	if err != nil {
		return errors.New("profile lifecycle ownership unavailable")
	}
	current, err := os.Lstat(filepath.Join(dir, "process.lock"))
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return errors.New("profile lifecycle ownership changed")
	}
	return operation(l.directory, opened)
}
