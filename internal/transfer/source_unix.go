//go:build !windows

package transfer

import (
	"golang.org/x/sys/unix"
	"os"
)

// Nonblocking open avoids hanging if a regular source is exchanged for a FIFO
// between Lstat and OpenFile; no-follow rejects a substituted final symlink.
func sourceReadFlags() int { return os.O_RDONLY | unix.O_NONBLOCK | unix.O_NOFOLLOW }
