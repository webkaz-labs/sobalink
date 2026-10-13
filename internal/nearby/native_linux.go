//go:build linux

package nearby

import "golang.org/x/sys/unix"

func configureNativeSocket(fd, index int) error {
	// Linux otherwise delivers groups joined by unrelated sockets/interfaces.
	// This socket owns one selected membership, and checks pktinfo on every read.
	return unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_MULTICAST_ALL, 0)
}
