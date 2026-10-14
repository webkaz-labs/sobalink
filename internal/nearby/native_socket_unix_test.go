//go:build linux || darwin

package nearby

import (
	"errors"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type nativeTestCloser struct {
	calls int
	err   error
}

func (c *nativeTestCloser) Close() error { c.calls++; return c.err }

type nativeTestPacketConn struct{ nativeTestCloser }

func (*nativeTestPacketConn) ReadFrom([]byte) (int, net.Addr, error) { panic("not used") }
func (*nativeTestPacketConn) WriteTo([]byte, net.Addr) (int, error)  { panic("not used") }
func (*nativeTestPacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
}
func (*nativeTestPacketConn) SetDeadline(time.Time) error      { panic("not used") }
func (*nativeTestPacketConn) SetReadDeadline(time.Time) error  { panic("not used") }
func (*nativeTestPacketConn) SetWriteDeadline(time.Time) error { panic("not used") }

type nativeSocketScenario struct {
	fail                 string
	calls                []string
	bound                *unix.SockaddrInet4
	index                int
	original, raw        nativeTestCloser
	duplicate            nativeTestPacketConn
	wrapReturnsDuplicate bool
}

func (s *nativeSocketScenario) operation(name string) error {
	s.calls = append(s.calls, name)
	if s.fail == name {
		return errors.New("synthetic setup failure")
	}
	return nil
}
func (s *nativeSocketScenario) ops(t *testing.T) nativeSocketOps {
	t.Helper()
	const descriptor = 31
	fdStep := func(name string) func(int) error {
		return func(fd int) error {
			if fd != descriptor {
				t.Fatal("descriptor changed")
			}
			return s.operation(name)
		}
	}
	return nativeSocketOps{
		create: func() (int, error) { return descriptor, s.operation("socket_create") },
		close: func(fd int) error {
			if fd != descriptor {
				t.Fatal("closed wrong descriptor")
			}
			return s.raw.Close()
		},
		closeOnExec: fdStep("socket_cloexec"), nonblock: fdStep("socket_nonblock"),
		reuseAddress:     fdStep("socket_reuse_address"),
		interfaceOptions: func(fd, index int) error { s.index = index; return fdStep("socket_interface_options")(fd) },
		bind: func(fd int, address *unix.SockaddrInet4) error {
			copy := *address
			s.bound = &copy
			return fdStep("socket_bind")(fd)
		},
		wrap: func(fd int) (net.PacketConn, io.Closer, error) {
			err := fdStep("socket_wrap")(fd)
			if err != nil && !s.wrapReturnsDuplicate {
				return nil, &s.original, err
			}
			return &s.duplicate, &s.original, err
		},
	}
}

func TestNativeSocketConstructionBindsExactGroup(t *testing.T) {
	s := &nativeSocketScenario{}
	packet, owner, err := openNativeGroupSocket(7, s.ops(t))
	if err != nil || owner != nil || packet != &s.duplicate {
		t.Fatal("construction did not transfer duplicate ownership")
	}
	if s.bound == nil || s.bound.Addr != [4]byte{224, 0, 0, 251} || s.bound.Port != 5353 || s.index != 7 {
		t.Fatal("construction widened or changed exact selected multicast scope")
	}
	want := []string{"socket_create", "socket_cloexec", "socket_nonblock", "socket_reuse_address", "socket_interface_options", "socket_bind", "socket_wrap"}
	if !reflect.DeepEqual(s.calls, want) {
		t.Fatalf("wrong construction order: %v", s.calls)
	}
	if s.raw.calls != 0 || s.original.calls != 1 || s.duplicate.calls != 0 {
		t.Fatal("incorrect success ownership")
	}
	if err := packet.Close(); err != nil || s.duplicate.calls != 1 {
		t.Fatal("caller did not own duplicate")
	}
}

func TestNativeSocketConstructionFailuresCloseWithoutFallback(t *testing.T) {
	stages := []string{"socket_create", "socket_cloexec", "socket_nonblock", "socket_reuse_address", "socket_interface_options", "socket_bind", "socket_wrap"}
	for position, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			s := &nativeSocketScenario{fail: stage}
			packet, owner, err := openNativeGroupSocket(7, s.ops(t))
			if packet != nil || owner != nil || !errors.Is(err, ErrSocketUnavailable) || socketFailureStage(err) != stage {
				t.Fatal("wrong failure/owner classification")
			}
			if !reflect.DeepEqual(s.calls, stages[:position+1]) {
				t.Fatal("continued after failure or attempted a fallback")
			}
			wantRaw, wantFile := 1, 0
			if stage == "socket_create" {
				wantRaw = 0
			}
			if stage == "socket_wrap" {
				wantRaw, wantFile = 0, 1
			}
			if s.raw.calls != wantRaw || s.original.calls != wantFile || s.duplicate.calls != 0 {
				t.Fatal("failed construction leaked or double-closed descriptor")
			}
			if s.bound != nil && (s.bound.Addr != [4]byte{224, 0, 0, 251} || s.bound.Port != 5353) {
				t.Fatal("failure attempted a wider bind")
			}
		})
	}
}

func TestNativeSocketRollbackRetainsEachUncertainOwner(t *testing.T) {
	for _, which := range []string{"raw", "original_after_wrap_failure", "original_after_wrap_success", "duplicate_after_wrap_failure"} {
		t.Run(which, func(t *testing.T) {
			s := &nativeSocketScenario{}
			stage := "socket_wrap"
			switch which {
			case "raw":
				s.fail, stage, s.raw.err = "socket_bind", "socket_bind", errSyntheticClose
			case "original_after_wrap_failure":
				s.fail, s.original.err = "socket_wrap", errSyntheticClose
			case "original_after_wrap_success":
				s.original.err, stage = errSyntheticClose, "socket_original_close"
			case "duplicate_after_wrap_failure":
				s.fail, s.wrapReturnsDuplicate, s.duplicate.err = "socket_wrap", true, errSyntheticClose
			}
			packet, owner, err := openNativeGroupSocket(7, s.ops(t))
			if packet != nil || owner == nil || socketFailureStage(err) != stage || !errors.Is(err, ErrCloseUncertain) || !errors.Is(err, errSyntheticClose) || IsUnavailable(err) {
				t.Fatal("uncertain constructor lost owner or admitted fallback")
			}
			before := []int{s.raw.calls, s.original.calls, s.duplicate.calls}
			for i := 0; i < 3; i++ {
				if closeErr := owner.Close(); !errors.Is(closeErr, ErrCloseUncertain) || !errors.Is(closeErr, errSyntheticClose) {
					t.Fatal("retained owner erased closure uncertainty")
				}
			}
			if !reflect.DeepEqual(before, []int{s.raw.calls, s.original.calls, s.duplicate.calls}) {
				t.Fatal("retained owner retried a possibly reused descriptor")
			}
			if which == "original_after_wrap_success" && (s.original.calls != 1 || s.duplicate.calls != 1) {
				t.Fatal("original-close failure left duplicate live")
			}
		})
	}
}

func TestNativeSocketWrapFailureClosesBothDescriptors(t *testing.T) {
	s := &nativeSocketScenario{fail: "socket_wrap", wrapReturnsDuplicate: true}
	packet, owner, err := openNativeGroupSocket(7, s.ops(t))
	if packet != nil || owner != nil || socketFailureStage(err) != "socket_wrap" || s.original.calls != 1 || s.duplicate.calls != 1 || s.raw.calls != 0 {
		t.Fatal("wrap failure did not close both separately owned descriptors")
	}
}
