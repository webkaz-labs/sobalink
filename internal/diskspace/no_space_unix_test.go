//go:build linux || darwin

package diskspace

import (
	"golang.org/x/sys/unix"
	"testing"
)

func TestNativeDiskFullErrors(t *testing.T) {
	testNativeDiskErrors(t, []error{unix.ENOSPC, unix.EDQUOT}, []error{unix.EACCES, unix.EIO})
}
