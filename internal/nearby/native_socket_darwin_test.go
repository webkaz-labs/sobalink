//go:build darwin

package nearby

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativePlatformOptionsStaySocketScoped(t *testing.T) {
	for failAt := 0; failAt <= 2; failAt++ {
		calls := 0
		err := configureNativeSocketOptions(31, 7, func(fd, level, name, value int) error {
			calls++
			if fd != 31 {
				t.Fatal("wrong socket")
			}
			if calls == 1 && (level != unix.IPPROTO_IP || name != unix.IP_BOUND_IF || value != 7) {
				t.Fatal("Darwin interface confinement changed")
			}
			if calls == 2 && (level != unix.SOL_SOCKET || name != unix.SO_REUSEPORT || value != 1) {
				t.Fatal("Darwin reuse option changed")
			}
			if calls == failAt {
				return errSyntheticClose
			}
			return nil
		})
		if failAt == 0 && (err != nil || calls != 2) || failAt != 0 && (!errors.Is(err, errSyntheticClose) || calls != failAt) {
			t.Fatal("Darwin option failure was ignored or retried")
		}
	}
}
