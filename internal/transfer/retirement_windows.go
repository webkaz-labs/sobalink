package transfer

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/webkaz-labs/sobalink/internal/config"
	"golang.org/x/sys/windows"
)

func retirementCreate(root *os.Root, name string) (*os.File, error) {
	path, err := windows.UTF16PtrFromString(filepath.Join(root.Name(), name))
	if err != nil {
		return nil, err
	}
	descriptor, err := config.SecurityDescriptor(false)
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString(descriptor)
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	h, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, &sa, windows.CREATE_NEW, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_WRITE_THROUGH, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), name), nil
}

// A filesystem that cannot acknowledge directory durability fails closed. A
// Windows cross-build is not native proof of support for this operation.
func retirementSyncDirectory(root *os.Root) (err error) {
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
		return ErrUnsafePath
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
		return ErrUnsafePath
	}
	return windows.FlushFileBuffers(h)
}
