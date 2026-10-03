package diskspace

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

func available(file *os.File) (uint64, error) {
	// Resolve the open handle to its volume GUID so a renamed file, replaced
	// parent, or mount-point path cannot redirect the space probe elsewhere.
	// Volumes without GUID resolution fail closed rather than guessing a disk.
	buffer := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 1) // VOLUME_NAME_GUID
	if err != nil {
		return 0, err
	}
	if n == 0 || n >= uint32(len(buffer)) {
		return 0, ErrUnknown
	}
	name := windows.UTF16ToString(buffer[:n])
	if !strings.HasPrefix(name, `\\?\Volume{`) {
		return 0, ErrUnknown
	}
	end := strings.Index(name, `}\`)
	if end < 0 {
		return 0, ErrUnknown
	}
	volume, err := windows.UTF16PtrFromString(name[:end+2])
	if err != nil {
		return 0, err
	}
	var free, total, allFree uint64
	if err := windows.GetDiskFreeSpaceEx(volume, &free, &total, &allFree); err != nil {
		return 0, err
	}
	if free > total || free > allFree {
		return 0, ErrUnknown
	}
	return free, nil
}

func nativeNoSpace(err error) bool {
	return errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL) || errors.Is(err, windows.ERROR_DISK_QUOTA_EXCEEDED)
}
