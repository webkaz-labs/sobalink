//go:build !windows

package core

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

func outboxSyncDirectory(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func outboxNativeIdentity(f *os.File) (string, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x:%x", stat.Dev, stat.Ino), nil
}
