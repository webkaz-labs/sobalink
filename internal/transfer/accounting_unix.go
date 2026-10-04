//go:build !windows

package transfer

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

func accountingOpen(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, sourceReadFlags(), 0)
}

func accountingIdentity(f *os.File) (string, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x:%x", stat.Dev, stat.Ino), nil
}

func accountingPrivate(_ *os.File, info os.FileInfo) bool { return info.Mode().Perm()&0077 == 0 }
