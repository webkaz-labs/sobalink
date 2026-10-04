package config

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Lease admission requires a real namespace durability acknowledgement. A
// Windows cross-build does not establish native filesystem support for it.
func atomicSyncLeaseNamespace(parentPath string, owned *os.File) (err error) {
	if owned == nil {
		return ErrAtomicRecovery
	}
	retained, err := atomicFileMetadata(owned)
	if err != nil {
		return err
	}
	if !retained.directory || !retained.private {
		return ErrAtomicRecovery
	}
	path, err := filepath.Abs(filepath.Join(parentPath, atomicNamespace))
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), path)
	defer func() { err = errors.Join(err, f.Close()) }()
	// Each check reads native metadata, including the protected owner DACL.
	check := func(file *os.File) error {
		m, e := atomicFileMetadata(file)
		if e != nil {
			return e
		}
		if !m.directory || !m.private || m.id != retained.id {
			return ErrAtomicRecovery
		}
		return nil
	}
	checkBinding := func() (result error) {
		bound, e := atomicOpenDirectory(path)
		if e != nil {
			return e
		}
		defer func() { result = errors.Join(result, bound.Close()) }()
		return check(bound)
	}
	if err = check(f); err != nil {
		return err
	}
	if err = checkBinding(); err != nil {
		return err
	}
	if err = check(owned); err != nil {
		return err
	}
	if err = windows.FlushFileBuffers(h); err != nil {
		return err
	}
	if err = check(f); err != nil {
		return err
	}
	if err = check(owned); err != nil {
		return err
	}
	return checkBinding()
}
