//go:build linux

package nearby

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativePlatformOptionsStaySocketScoped(t *testing.T) {
	calls := 0
	err := configureNativeSocketOptions(31, 7, func(fd, level, name, value int) error {
		calls++
		if fd != 31 || level != unix.IPPROTO_IP || name != unix.IP_MULTICAST_ALL || value != 0 {
			t.Fatal("Linux multicast confinement changed")
		}
		return errSyntheticClose
	})
	if calls != 1 || !errors.Is(err, errSyntheticClose) {
		t.Fatal("Linux option failure was ignored or retried")
	}
}
