//go:build darwin

package nearby

import (
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
	"net"
)

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

func nativeSendControl(scope Scope) *ipv4.ControlMessage { return sourceOnlySendControl(scope) }

func attachNativePlatformSender(owner *nativeTransport) (Transport, error) {
	return adoptNativeSender(owner, func() (nativePacketWriter, Transport, error) {
		raw, retained, err := openNativeLocalSocket(owner.scope, owner.policy, systemNativeSocketOps())
		if err != nil {
			return nil, retained, err
		}
		conn, ok := raw.(*net.UDPConn)
		if !ok {
			return nil, &closeOnlyTransport{close: raw.Close}, socketFailure("socket_type")
		}
		packet := ipv4.NewPacketConn(conn)
		closer := &closeOnlyTransport{close: packet.Close}
		if conn.LocalAddr().String() != net.JoinHostPort(owner.scope.LocalAddress.String(), "5353") {
			return nil, closer, socketFailure("socket_bound_address")
		}
		for _, step := range []struct {
			stage nativeSocketStage
			apply func() error
		}{
			{"socket_write_buffer", func() error { return conn.SetWriteBuffer(4 * MaxDatagramBytes) }},
			{"socket_multicast_interface", func() error { return packet.SetMulticastInterface(&owner.ifi) }},
			{"socket_multicast_ttl", func() error { return packet.SetMulticastTTL(255) }},
			{"socket_multicast_loopback", func() error { return packet.SetMulticastLoopback(true) }},
		} {
			if step.apply() != nil {
				return nil, closer, socketFailure(step.stage)
			}
		}
		return packet, closer, nil
	})
}
