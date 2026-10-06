//go:build !windows

package core

import "syscall"

func fixtureListenerConflict() error { return syscall.EADDRINUSE }
func fixtureListenerErrors() []struct {
	err  error
	code string
} {
	return []struct {
		err  error
		code string
	}{{syscall.EACCES, "listener_permission_denied"}, {syscall.EPERM, "listener_permission_denied"}, {syscall.EMFILE, "listener_capacity"}, {syscall.ENFILE, "listener_capacity"}, {syscall.ENOBUFS, "listener_capacity"}, {syscall.ENOMEM, "listener_capacity"}, {syscall.EADDRNOTAVAIL, "listener_address_unavailable"}, {syscall.EAFNOSUPPORT, "listener_address_unavailable"}, {syscall.EPROTONOSUPPORT, "listener_address_unavailable"}}
}
