//go:build resource_process_native && linux && (amd64 || arm64)

package resourceacceptance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestProcessEntryOwnedDirectoryNoFollowNativeFilesystem(t *testing.T) {
	cutoff := time.Now().Add(5 * time.Second)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal("owned directory create failed")
	}
	if err := os.Symlink("child", filepath.Join(root, "alias")); err != nil {
		t.Fatal("owned symlink create failed")
	}
	files := &processEntryFiles{}
	v := &processEntryVerifier{files: files, deadline: cutoff}
	t.Cleanup(func() {
		if !files.closeBefore(cutoff) {
			t.Error("owned metadata handles unjoined")
		}
	})
	owned := v.openAbsolute(filepath.Join(root, "child"), true)
	if _, ok := processEntryPrivateDirectory(owned); !ok {
		t.Fatal("owned directory rejected")
	}
	if link := v.openAbsolute(filepath.Join(root, "alias"), true); link != nil {
		t.Fatal("symlink followed")
	}
}

func TestProcessEntryCurrentImageIdentityNativeFilesystem(t *testing.T) {
	cutoff := time.Now().Add(5 * time.Second)
	files := &processEntryFiles{}
	v := &processEntryVerifier{files: files, deadline: cutoff}
	t.Cleanup(func() {
		if !files.closeBefore(cutoff) {
			t.Error("image handles unjoined")
		}
	})
	path, err := os.Executable()
	if err != nil || os.Getpid() <= 0 || os.Getppid() <= 0 {
		t.Fatal("current-process query failed")
	}
	image := v.open("/proc/self/exe", unix.O_RDONLY|unix.O_CLOEXEC)
	named := v.openAbsolute(path, false)
	if image == nil || named == nil {
		t.Fatal("current image open failed")
	}
	a, okA := processEntryStat(image.file)
	b, okB := processEntryStat(named.file)
	if !okA || !okB || !processEntrySameFile(a, b) {
		t.Fatal("current image identity mismatch")
	}
}
