package core

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
)

func outboxSyncDirectory(root *os.Root) (err error) {
	// Open relative to the retained Root, not its potentially rebound pathname.
	pinned, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, pinned.Close()) }()
	var retained windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(pinned.Fd()), &retained); err != nil {
		return err
	}
	if retained.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || retained.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return outboxError("outbox_recovery_required")
	}
	path, err := windows.UTF16PtrFromString(root.Name())
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(h)) }()
	var opened windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &opened); err != nil {
		return err
	}
	if opened.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || opened.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		opened.VolumeSerialNumber != retained.VolumeSerialNumber || opened.FileIndexHigh != retained.FileIndexHigh || opened.FileIndexLow != retained.FileIndexLow {
		return outboxError("outbox_recovery_required")
	}
	return windows.FlushFileBuffers(h)
}

func outboxNativeIdentity(f *os.File) (string, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return "", err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", outboxError("outbox_payload_recovery_required")
	}
	return fmt.Sprintf("%x:%x:%x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}
