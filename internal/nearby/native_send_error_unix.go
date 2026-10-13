//go:build linux || darwin

package nearby

import (
	"errors"
	"golang.org/x/sys/unix"
)

func nativeSendErrorKind(err error) string {
	for _, entry := range []struct {
		kind string
		err  error
	}{
		{"invalid_argument", unix.EINVAL}, {"source_unavailable", unix.EADDRNOTAVAIL},
		{"network_unreachable", unix.ENETUNREACH}, {"host_unreachable", unix.EHOSTUNREACH},
		{"permission_denied", unix.EACCES}, {"permission_denied", unix.EPERM},
		{"unsupported", unix.EOPNOTSUPP}, {"unsupported", unix.ENOPROTOOPT},
		{"timeout", unix.ETIMEDOUT},
	} {
		if errors.Is(err, entry.err) {
			return entry.kind
		}
	}
	return "other"
}
