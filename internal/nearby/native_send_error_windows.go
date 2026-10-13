//go:build windows

package nearby

import (
	"errors"
	"golang.org/x/sys/windows"
)

func nativeSendErrorKind(err error) string {
	for _, entry := range []struct {
		kind string
		err  error
	}{
		{"invalid_argument", windows.WSAEINVAL}, {"source_unavailable", windows.WSAEADDRNOTAVAIL},
		{"network_unreachable", windows.WSAENETUNREACH}, {"host_unreachable", windows.WSAEHOSTUNREACH},
		{"permission_denied", windows.WSAEACCES}, {"unsupported", windows.WSAEOPNOTSUPP},
		{"unsupported", windows.WSAENOPROTOOPT}, {"timeout", windows.WSAETIMEDOUT},
	} {
		if errors.Is(err, entry.err) {
			return entry.kind
		}
	}
	return "other"
}
