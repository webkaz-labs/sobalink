package config

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"sync"
)

func UserSID() (string, error) {
	t, e := windows.OpenCurrentProcessToken()
	if e != nil {
		return "", e
	}
	defer t.Close()
	u, e := t.GetTokenUser()
	if e != nil {
		return "", e
	}
	return u.User.Sid.String(), nil
}
func SecurityDescriptor(dir bool) (string, error) {
	sid, e := UserSID()
	if e != nil {
		return "", e
	}
	inherit := ""
	if dir {
		inherit = "OICI"
	}
	return "D:P(A;" + inherit + ";FA;;;" + sid + ")", nil
}
func Protect(path string, dir bool) error {
	s, e := SecurityDescriptor(dir)
	if e != nil {
		return e
	}
	sd, e := windows.SecurityDescriptorFromString(s)
	if e != nil {
		return e
	}
	dacl, _, e := sd.DACL()
	if e != nil {
		return e
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
func SecureDir(path string) error {
	if e := os.MkdirAll(path, 0700); e != nil {
		return e
	}
	s, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !s.IsDir() || s.Mode()&os.ModeSymlink != 0 {
		return errors.New("state path must be a real directory")
	}
	return Protect(path, true)
}
func replace(from, to string) error {
	f, e := windows.UTF16PtrFromString(from)
	if e != nil {
		return e
	}
	t, e := windows.UTF16PtrFromString(to)
	if e != nil {
		return e
	}
	return windows.MoveFileEx(f, t, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

type Lock struct {
	mu        sync.Mutex
	directory os.FileInfo
	f         *os.File
	ov        windows.Overlapped
}

func AcquireLock(dir string) (*Lock, error) {
	if e := SecureDir(dir); e != nil {
		return nil, e
	}
	p := filepath.Join(dir, "process.lock")
	if s, e := os.Lstat(p); e == nil && s.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("lock must not be a symlink")
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = Protect(p, false); e != nil {
		f.Close()
		return nil, e
	}
	directory, err := os.Stat(dir)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	l := &Lock{f: f, directory: directory}
	if e = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &l.ov); e != nil {
		f.Close()
		return nil, errors.New("another sobalink process owns this profile")
	}
	return l, nil
}
func (l *Lock) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	_ = windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, &l.ov)
	err := l.f.Close()
	l.f = nil
	return err
}
