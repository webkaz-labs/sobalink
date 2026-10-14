//go:build windows

package nearby

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsBindFailureCategories(t *testing.T) {
	for _, tc := range []struct {
		err  error
		kind string
	}{
		{windows.WSAEADDRINUSE, "address_in_use"}, {windows.WSAEADDRNOTAVAIL, "source_unavailable"},
		{windows.WSAEACCES, "permission_denied"}, {windows.WSAEINVAL, "invalid_argument"},
		{windows.WSAEOPNOTSUPP, "unsupported"}, {errors.New("synthetic private error"), "other"},
	} {
		err := nativeBindFailure("local_duplex", tc.err)
		if nativeBindFailureKind(err) != tc.kind || nativeBindSocketRole(err) != "local_duplex" || err.Error() != ErrSocketUnavailable.Error() {
			t.Fatal("Windows bind evidence changed or leaked")
		}
	}
}
