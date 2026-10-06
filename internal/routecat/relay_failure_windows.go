package routecat

import (
	"golang.org/x/sys/windows"
	"syscall"
)

func relayPlatformAvailability(cause syscall.Errno) bool {
	switch cause {
	case windows.WSAECONNREFUSED, windows.WSAECONNRESET, windows.WSAENETUNREACH, windows.WSAEHOSTUNREACH, windows.WSAETIMEDOUT,
		windows.ERROR_NETNAME_DELETED, windows.ERROR_NETWORK_UNREACHABLE, windows.ERROR_HOST_UNREACHABLE, windows.ERROR_CONNECTION_REFUSED:
		return true
	}
	return false
}
