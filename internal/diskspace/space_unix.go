//go:build linux || darwin

package diskspace

import (
	"errors"
	"math"
	"os"

	"golang.org/x/sys/unix"
)

func available(file *os.File) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 || stat.Bavail > stat.Blocks || stat.Bavail > stat.Bfree {
		return 0, ErrUnknown
	}
	return blockBytes(uint64(stat.Bavail), uint64(stat.Bsize))
}

func blockBytes(blocks, blockSize uint64) (uint64, error) {
	if blockSize == 0 || blocks > math.MaxUint64/blockSize {
		return 0, ErrUnknown
	}
	return blocks * blockSize, nil
}

func nativeNoSpace(err error) bool { return errors.Is(err, unix.ENOSPC) || errors.Is(err, unix.EDQUOT) }
