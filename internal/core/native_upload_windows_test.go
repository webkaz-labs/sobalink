package core

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

func init() {
	blockNativeUploadCleanup = func(t *testing.T, spool string) (string, func()) {
		t.Helper()
		path := filepath.Join(spool, "cleanup-blocker")
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			t.Fatal(err)
		}
		// Only this fixture-owned file denies FILE_SHARE_DELETE. The HTTP
		// handler can still read/write and close its actual payload file.
		handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			t.Fatal(err)
		}
		release := sync.OnceFunc(func() {
			if err := windows.CloseHandle(handle); err != nil {
				t.Error(err)
			}
		})
		t.Cleanup(release)
		if err := os.Remove(path); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			t.Fatalf("fixture did not deny file deletion: %v", err)
		}
		return spool, release
	}
}
