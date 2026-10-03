package config

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func atomicOpenDirectory(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	s, err := atomicFileMetadata(f)
	if err != nil || !s.directory {
		_ = f.Close()
		return nil, errors.New("destination must be a non-reparse directory")
	}
	return f, nil
}

// Native relative opens pin every operation to the verified directory handle.
// OPEN_REPARSE_POINT prevents following junctions/symlinks; attributes and
// link count are inspected on the resulting handle before any payload I/O.
func atomicWindowsOpen(dir *os.File, name string, create, directory bool, access uint32) (*os.File, error) {
	n, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(dir.Fd()), ObjectName: n, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(oa))
	disposition := uint32(windows.FILE_OPEN)
	if create {
		disposition = windows.FILE_CREATE
		sid, e := UserSID()
		if e != nil {
			return nil, e
		}
		inherit := ""
		if directory {
			inherit = "OICI"
		}
		oa.SecurityDescriptor, err = windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;" + inherit + ";FA;;;" + sid + ")")
		if err != nil {
			return nil, err
		}
	}
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT | windows.FILE_OPEN_FOR_BACKUP_INTENT)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, access|windows.SYNCHRONIZE, &oa, &iosb, nil, windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, disposition, options, 0, 0)
	if status, ok := err.(windows.NTStatus); ok {
		err = status.Errno()
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), name), nil
}

func atomicOpenChild(dir *os.File, name string, create, directory, writable bool) (*os.File, error) {
	access := uint32(windows.FILE_GENERIC_READ | windows.READ_CONTROL)
	if writable {
		access |= windows.FILE_GENERIC_WRITE | windows.DELETE
	}
	return atomicWindowsOpen(dir, name, create, directory, access)
}

func atomicPrivateDACL(h windows.Handle) (bool, error) {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	control, _, err := sd.Control()
	if err != nil {
		return false, err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return false, err
	}
	if control&windows.SE_DACL_PROTECTED == 0 || acl == nil || acl.AceCount != 1 {
		return false, nil
	}
	sidText, err := UserSID()
	if err != nil {
		return false, err
	}
	sid, err := windows.StringToSid(sidText)
	if err != nil {
		return false, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return false, err
	}
	if owner == nil || !owner.Equals(sid) {
		return false, nil
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err = windows.GetAce(acl, 0, &ace); err != nil {
		return false, err
	}
	actual := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	return ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE && ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 && actual.Equals(sid) &&
		(ace.Mask&windows.GENERIC_ALL != 0 || ace.Mask&0x001f01ff == 0x001f01ff), nil
}

func atomicFileMetadata(f *os.File) (atomicMetadata, error) {
	h := windows.Handle(f.Fd())
	var s windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &s); err != nil {
		return atomicMetadata{}, err
	}
	typ, err := windows.GetFileType(h)
	if err != nil {
		return atomicMetadata{}, err
	}
	m := atomicMetadata{id: [3]uint64{uint64(s.VolumeSerialNumber), uint64(s.FileIndexHigh), uint64(s.FileIndexLow)},
		size: int64(uint64(s.FileSizeHigh)<<32 | uint64(s.FileSizeLow)), singleLink: s.NumberOfLinks == 1}
	if typ != windows.FILE_TYPE_DISK || s.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return m, nil
	}
	m.directory = s.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	m.regular = !m.directory && s.FileAttributes&windows.FILE_ATTRIBUTE_DEVICE == 0
	m.private, err = atomicPrivateDACL(h)
	return m, err
}

func atomicChildMetadata(dir *os.File, name string) (atomicMetadata, error) {
	// Metadata opens admit files and directories, without FILE_NON_DIRECTORY_FILE.
	n, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return atomicMetadata{}, err
	}
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(dir.Fd()), ObjectName: n, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE, &oa, &iosb, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_FOR_BACKUP_INTENT, 0, 0)
	if status, ok := err.(windows.NTStatus); ok {
		err = status.Errno()
	}
	if err != nil {
		return atomicMetadata{}, err
	}
	f := os.NewFile(uintptr(h), name)
	defer f.Close()
	return atomicFileMetadata(f)
}

func atomicMkdir(dir *os.File, name string) error {
	f, err := atomicOpenChild(dir, name, true, true, false)
	if err != nil {
		return err
	}
	return f.Close()
}

func atomicTryLease(f *os.File) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrAtomicBusy
	}
	return err
}

func atomicRemove(_ *os.File, _ string, f *os.File) error {
	// Delete the checked object itself, not whatever its pathname now names.
	deleteFile := byte(1)
	return windows.SetFileInformationByHandle(windows.Handle(f.Fd()), windows.FileDispositionInfo, &deleteFile, 1)
}

func atomicReplace(_ *os.File, _ string, f *os.File, to *os.File, target string) error {
	target16, err := windows.UTF16FromString(target)
	if err != nil {
		return err
	}
	var info struct {
		ReplaceIfExists byte
		RootDirectory   windows.Handle
		FileNameLength  uint32
		FileName        [260]uint16
	}
	if len(target16) > len(info.FileName) {
		return errors.New("destination name too long")
	}
	info.ReplaceIfExists = 1
	info.RootDirectory = windows.Handle(to.Fd())
	info.FileNameLength = uint32((len(target16) - 1) * 2)
	copy(info.FileName[:], target16)
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtSetInformationFile(windows.Handle(f.Fd()), &iosb, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), windows.FileRenameInformation)
	if status, ok := err.(windows.NTStatus); ok {
		err = status.Errno()
	}
	return err
}

// Windows does not support FlushFileBuffers on directory handles. Flush the
// already-synced snapshot handle after its handle-relative rename instead.
func atomicSyncDirectory(_ *os.File) error         { return nil }
func atomicSyncReplacement(f, _, _ *os.File) error { return f.Sync() }
