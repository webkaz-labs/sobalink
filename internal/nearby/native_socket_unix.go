//go:build linux || darwin

package nearby

import (
	"errors"
	"io"
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// The Go UDP listener changes a multicast bind to a wildcard before its Control
// hook. Bind the exact group ourselves, then use the supported FilePacketConn
// bridge for Go poller/deadline ownership. Never retry another bind address.
// Dependencies are passed by value solely to check construction and ownership
// without opening sockets in the ordinary suite.
type nativeSocketOps struct {
	create           func() (int, error)
	close            func(int) error
	closeOnExec      func(int) error
	nonblock         func(int) error
	reuseAddress     func(int) error
	interfaceOptions func(int, int) error
	bind             func(int, *unix.SockaddrInet4) error
	wrap             func(int) (net.PacketConn, io.Closer, error)
}

func systemNativeSocketOps() nativeSocketOps {
	return nativeSocketOps{
		create: newNativeSocket,
		close:  unix.Close,
		closeOnExec: func(fd int) error {
			flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
			if err != nil {
				return err
			}
			_, err = unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags|unix.FD_CLOEXEC)
			return err
		},
		nonblock: func(fd int) error { return unix.SetNonblock(fd, true) },
		reuseAddress: func(fd int) error {
			return unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
		},
		interfaceOptions: configureNativeSocket,
		bind:             func(fd int, address *unix.SockaddrInet4) error { return unix.Bind(fd, address) },
		wrap: func(fd int) (net.PacketConn, io.Closer, error) {
			// NewFile takes ownership of fd, even when the duplicate fails.
			file := os.NewFile(uintptr(fd), "nearby-mdns")
			packet, err := net.FilePacketConn(file)
			return packet, file, err
		},
	}
}

// On failure only, failedOwner retains any original/duplicate whose closure is
// uncertain. A successful return owns only packet; the original was closed.
func openNativeGroupSocket(index int, ops nativeSocketOps) (net.PacketConn, Transport, error) {
	return openNativeBoundSocket(index, multicast4.As4(), "group_receive", ops)
}

func openNativeLocalSocket(scope Scope, policy discoveryPolicy, ops nativeSocketOps) (net.PacketConn, Transport, error) {
	if err := policy.validateScope(scope); err != nil {
		return nil, nil, err
	}
	if !scope.LocalAddress.Is4() {
		return nil, nil, ErrUnsupported
	}
	return openNativeBoundSocket(scope.InterfaceIndex, scope.LocalAddress.As4(), "local_sender", ops)
}

func openNativeBoundSocket(index int, address [4]byte, role string, ops nativeSocketOps) (packet net.PacketConn, failedOwner Transport, err error) {
	// Hold Go's fork lock until FD_CLOEXEC is set on platforms without atomic
	// socket flags. Do not silently ignore close-on-exec failures.
	syscall.ForkLock.RLock()
	fd, createErr := ops.create()
	if createErr != nil {
		syscall.ForkLock.RUnlock()
		return nil, nil, socketFailure("socket_create")
	}
	owner := &closeOnlyTransport{close: func() error { return ops.close(fd) }}
	cloexecErr := ops.closeOnExec(fd)
	syscall.ForkLock.RUnlock()
	fail := func(stage nativeSocketStage) (net.PacketConn, Transport, error) {
		retained, err := rollbackTransport(owner, socketFailure(stage))
		return nil, retained, err
	}
	if cloexecErr != nil {
		return fail("socket_cloexec")
	}
	for _, step := range []struct {
		stage nativeSocketStage
		apply func(int) error
	}{
		{"socket_nonblock", ops.nonblock},
		{"socket_reuse_address", ops.reuseAddress},
		{"socket_interface_options", func(fd int) error { return ops.interfaceOptions(fd, index) }},
		{"socket_bind", func(fd int) error {
			return ops.bind(fd, &unix.SockaddrInet4{Port: mdnsPort, Addr: address})
		}},
	} {
		if err := step.apply(fd); err != nil {
			if step.stage == "socket_bind" {
				retained, failure := rollbackTransport(owner, nativeBindFailure(role, err))
				return nil, retained, failure
			}
			return fail(step.stage)
		}
	}
	packet, file, wrapErr := ops.wrap(fd)
	if file != nil {
		// The os.File now owns the original; never close its numeric descriptor
		// separately, or retry it after the descriptor might have been reused.
		owner = &closeOnlyTransport{close: file.Close}
	}
	if packet != nil {
		original := owner
		duplicate := &closeOnlyTransport{close: packet.Close}
		combined := &closeOnlyTransport{close: func() error { return errors.Join(original.Close(), duplicate.Close()) }}
		if wrapErr != nil {
			owner = combined
			return fail("socket_wrap")
		}
		if original.Close() != nil {
			owner = combined
			return fail("socket_original_close")
		}
		return packet, nil, nil
	}
	return fail("socket_wrap")
}
