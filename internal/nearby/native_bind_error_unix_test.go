//go:build linux || darwin

package nearby

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativeBindEvidencePreservesRoleAndRollback(t *testing.T) {
	for _, role := range []string{"group_receive", "local_sender"} {
		for _, tc := range []struct {
			err  error
			kind string
		}{
			{unix.EADDRINUSE, "address_in_use"}, {unix.EADDRNOTAVAIL, "source_unavailable"},
			{unix.EACCES, "permission_denied"}, {unix.EPERM, "permission_denied"},
			{unix.EINVAL, "invalid_argument"}, {unix.EOPNOTSUPP, "unsupported"},
			{errors.New("synthetic private errno"), "other"},
		} {
			for _, uncertain := range []bool{false, true} {
				fixture := &nativeSocketScenario{}
				if uncertain {
					fixture.raw.err = errSyntheticClose
				}
				ops := fixture.ops(t)
				bind := ops.bind
				ops.bind = func(fd int, address *unix.SockaddrInet4) error {
					_ = bind(fd, address)
					return errors.Join(errors.New("synthetic private context"), tc.err)
				}
				open := func() (Transport, error) { _, owner, err := openNativeGroupSocket(7, ops); return owner, err }
				want := multicast4.As4()
				if role == "local_sender" {
					want = testScope().LocalAddress.As4()
					open = func() (Transport, error) {
						_, owner, err := openNativeLocalSocket(testScope(), discoveryPolicy{}, ops)
						return owner, err
					}
				}
				owner, err := open()
				if nativeBindSocketRole(err) != role || nativeBindFailureKind(err) != tc.kind || socketFailureStage(err) != "socket_bind" || nativeSendFailureKind(err) != "none" {
					t.Fatal("bind role/category lost")
				}
				if fixture.bound == nil || fixture.bound.Addr != want || fixture.bound.Port != mdnsPort || fixture.raw.calls != 1 || fixture.duplicate.calls != 0 || fixture.original.calls != 0 {
					t.Fatal("bind failure changed scope or ownership")
				}
				if uncertain {
					if owner == nil || IsUnavailable(err) || !errors.Is(err, errSyntheticClose) {
						t.Fatal("bind failure erased uncertain rollback")
					}
					_ = owner.Close()
				} else if owner != nil || err.Error() != ErrSocketUnavailable.Error() {
					t.Fatal("bind failure leaked private errno or retained confirmed owner")
				}
				if fixture.raw.calls != 1 {
					t.Fatal("bind failure repeated close")
				}
			}
		}
	}
	for _, err := range []error{nil, ErrScopeChanged, errors.New("synthetic private error")} {
		if nativeBindFailureKind(err) != "none" || nativeBindSocketRole(err) != "none" {
			t.Fatal("untyped error became bind evidence")
		}
	}
}
