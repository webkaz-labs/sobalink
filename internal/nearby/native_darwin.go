//go:build darwin

package nearby

import "golang.org/x/sys/unix"

// The caller holds syscall.ForkLock until FD_CLOEXEC has been checked.
func newNativeSocket() (int, error) {
	return unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, unix.IPPROTO_UDP)
}

func configureNativeSocket(fd, index int) error {
	return configureNativeSocketOptions(fd, index, unix.SetsockoptInt)
}

func configureNativeSocketOptions(fd, index int, set func(int, int, int, int) error) error {
	// This is socket-local interface confinement, not an OS routing change.
	if err := set(fd, unix.IPPROTO_IP, unix.IP_BOUND_IF, index); err != nil {
		return err
	}
	return set(fd, unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
}
