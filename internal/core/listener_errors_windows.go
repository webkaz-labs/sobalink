package core

import (
	"errors"
	"golang.org/x/sys/windows"
)

func listenerSystemErrorCode(err error) string {
	switch {
	case errors.Is(err, windows.WSAEADDRINUSE):
		return "listener_conflict"
	case errors.Is(err, windows.WSAEACCES), errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return "listener_permission_denied"
	case errors.Is(err, windows.WSAEMFILE), errors.Is(err, windows.WSAENOBUFS), errors.Is(err, windows.ERROR_NOT_ENOUGH_MEMORY):
		return "listener_capacity"
	case errors.Is(err, windows.WSAEADDRNOTAVAIL), errors.Is(err, windows.WSAEAFNOSUPPORT), errors.Is(err, windows.WSAEPROTONOSUPPORT):
		return "listener_address_unavailable"
	default:
		return "listener_unavailable"
	}
}
