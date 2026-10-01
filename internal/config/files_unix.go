//go:build !windows

package config

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func Protect(path string, dir bool) error {
	mode := os.FileMode(0600)
	if dir {
		mode = 0700
	}
	return os.Chmod(path, mode)
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
	if e = Protect(path, true); e != nil {
		return e
	}
	return nil
}
func replace(from, to string) error { return os.Rename(from, to) }

type Lock struct{ f *os.File }

func AcquireLock(dir string) (*Lock, error) {
	if e := SecureDir(dir); e != nil {
		return nil, e
	}
	p := dir + "/process.lock"
	fd, e := unix.Open(p, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), p)
	if e = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("another tsnet-bridge process owns this profile")
	}
	return &Lock{f}, nil
}
func (l *Lock) Close() error { _ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN); return l.f.Close() }
