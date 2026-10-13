//go:build linux

package nearby

import "golang.org/x/sys/unix"

func newNativeSocket() (int, error) {
	return unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.IPPROTO_UDP)
}

func configureNativeSocket(fd, index int) error {
	return configureNativeSocketOptions(fd, index, unix.SetsockoptInt)
}

func configureNativeSocketOptions(fd, index int, set func(int, int, int, int) error) error {
	// Linux otherwise delivers groups joined by unrelated sockets/interfaces.
	// This socket owns one selected membership, and checks pktinfo on every read.
	return set(fd, unix.IPPROTO_IP, unix.IP_MULTICAST_ALL, 0)
}
