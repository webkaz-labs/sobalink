//go:build !windows

package config

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func atomicOpenDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func atomicOpenChild(dir *os.File, name string, create, directory, writable bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	if writable {
		flags |= unix.O_RDWR
	}
	if directory {
		flags |= unix.O_DIRECTORY
	}
	if create {
		flags |= unix.O_CREAT | unix.O_EXCL
	}
	fd, err := unix.Openat(int(dir.Fd()), name, flags, 0600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func atomicUnixMetadata(s *unix.Stat_t) atomicMetadata {
	return atomicMetadata{
		id: [3]uint64{uint64(s.Dev), uint64(s.Ino)}, size: s.Size,
		regular:    s.Mode&unix.S_IFMT == unix.S_IFREG,
		directory:  s.Mode&unix.S_IFMT == unix.S_IFDIR,
		singleLink: s.Nlink == 1,
		private:    s.Uid == uint32(os.Geteuid()) && s.Mode&0077 == 0,
	}
}

func atomicFileMetadata(f *os.File) (atomicMetadata, error) {
	var s unix.Stat_t
	err := unix.Fstat(int(f.Fd()), &s)
	return atomicUnixMetadata(&s), err
}

func atomicChildMetadata(dir *os.File, name string) (atomicMetadata, error) {
	var s unix.Stat_t
	err := unix.Fstatat(int(dir.Fd()), name, &s, unix.AT_SYMLINK_NOFOLLOW)
	return atomicUnixMetadata(&s), err
}

func atomicMkdir(dir *os.File, name string) error { return unix.Mkdirat(int(dir.Fd()), name, 0700) }
func atomicTryLease(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrAtomicBusy
	}
	return err
}
func atomicRemove(dir *os.File, name string, _ *os.File) error {
	return unix.Unlinkat(int(dir.Fd()), name, 0)
}
func atomicReplace(from *os.File, name string, _ *os.File, to *os.File, target string) error {
	return unix.Renameat(int(from.Fd()), name, int(to.Fd()), target)
}
func atomicSyncDirectory(dir *os.File) error { return dir.Sync() }
func atomicSyncReplacement(_ *os.File, parent, owned *os.File) error {
	if err := parent.Sync(); err != nil {
		return err
	}
	return owned.Sync()
}
