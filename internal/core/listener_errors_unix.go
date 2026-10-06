//go:build !windows

package core

import (
	"errors"
	"syscall"
)

func listenerSystemErrorCode(err error) string {
	switch {
	case errors.Is(err, syscall.EADDRINUSE):
		return "listener_conflict"
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return "listener_permission_denied"
	case errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE), errors.Is(err, syscall.ENOBUFS), errors.Is(err, syscall.ENOMEM):
		return "listener_capacity"
	case errors.Is(err, syscall.EADDRNOTAVAIL), errors.Is(err, syscall.EAFNOSUPPORT), errors.Is(err, syscall.EPROTONOSUPPORT):
		return "listener_address_unavailable"
	default:
		return "listener_unavailable"
	}
}
