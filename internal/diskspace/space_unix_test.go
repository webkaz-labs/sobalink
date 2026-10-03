//go:build linux || darwin

package diskspace

import (
	"errors"
	"math"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativeSpaceProbeUsesAvailableBlocks(t *testing.T) {
	file := fixtureFile(t)
	var stat unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Bavail > stat.Bfree {
		t.Fatal("invalid filesystem availability")
	}
	// An independent descriptor identifies the same filesystem; no path lookup
	// is necessary even after a file is renamed.
	name := file.Name()
	if err := os.Rename(name, name+"-renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := available(file); err != nil {
		t.Fatal(err)
	}
}

func TestFilesystemBlockArithmeticFailsClosedOnOverflow(t *testing.T) {
	for _, v := range [][2]uint64{{math.MaxUint64, 2}, {1, 0}} {
		if _, err := blockBytes(v[0], v[1]); !errors.Is(err, ErrUnknown) {
			t.Fatal(err)
		}
	}
	if n, err := blockBytes(math.MaxUint64/4096, 4096); err != nil || n > math.MaxUint64-4095 {
		t.Fatalf("boundary %d %v", n, err)
	}
}
