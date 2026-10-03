package diskspace

import (
	"golang.org/x/sys/windows"
	"testing"
)

func TestNativeDiskFullErrors(t *testing.T) {
	testNativeDiskErrors(t, []error{windows.ERROR_DISK_FULL, windows.ERROR_HANDLE_DISK_FULL, windows.ERROR_DISK_QUOTA_EXCEEDED}, []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_NOT_ENOUGH_QUOTA})
}
