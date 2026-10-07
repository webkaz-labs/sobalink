package core

import "golang.org/x/sys/windows"

func fixtureListenerConflict() error { return windows.WSAEADDRINUSE }
func fixtureListenerErrors() []struct {
	err  error
	code string
} {
	return []struct {
		err  error
		code string
	}{{windows.WSAEACCES, "listener_permission_denied"}, {windows.ERROR_ACCESS_DENIED, "listener_permission_denied"}, {windows.WSAEMFILE, "listener_capacity"}, {windows.WSAENOBUFS, "listener_capacity"}, {windows.ERROR_NOT_ENOUGH_MEMORY, "listener_capacity"}, {windows.WSAEADDRNOTAVAIL, "listener_address_unavailable"}, {windows.WSAEAFNOSUPPORT, "listener_address_unavailable"}, {windows.WSAEPROTONOSUPPORT, "listener_address_unavailable"}}
}
