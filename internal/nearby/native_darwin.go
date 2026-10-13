//go:build darwin

package nearby

import "golang.org/x/sys/unix"

func configureNativeSocket(fd, index int) error {
	// This is socket-local interface confinement, not an OS routing change.
	if err := unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_BOUND_IF, index); err != nil {
		return err
	}
	return unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
}
