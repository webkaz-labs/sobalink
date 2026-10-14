//go:build linux || darwin

package nearby

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
)

type nativeSenderFixture struct {
	calls       atomic.Int32
	closed      chan struct{}
	started     chan struct{}
	holdClose   chan struct{}
	closeErr    error
	control     *ipv4.ControlMessage
	destination net.Addr
}

func (f *nativeSenderFixture) Close() error {
	f.calls.Add(1)
	if f.holdClose != nil {
		<-f.holdClose
	}
	if f.closed != nil {
		close(f.closed)
	}
	return f.closeErr
}
func (f *nativeSenderFixture) SetWriteDeadline(time.Time) error { return nil }
func (f *nativeSenderFixture) WriteTo(_ []byte, cm *ipv4.ControlMessage, destination net.Addr) (int, error) {
	f.control, f.destination = cm, destination
	close(f.started)
	<-f.closed
	return 0, context.Canceled
}
func TestNativeLocalSenderNeverBroadensBind(t *testing.T) {
	for _, scope := range []Scope{testScope(), {InterfaceIndex: 7, LocalAddress: netip.MustParseAddr("127.0.0.1"), Prefix: netip.MustParsePrefix("127.0.0.1/32")}} {
		policy := discoveryPolicy{}
		if scope.LocalAddress.IsLoopback() {
			policy = isolatedLoopbackPolicy()
		}
		fixture := &nativeSocketScenario{}
		packet, owner, err := openNativeLocalSocket(scope, policy, fixture.ops(t))
		if err != nil || owner != nil || packet == nil || fixture.bound == nil || fixture.bound.Addr != scope.LocalAddress.As4() || fixture.bound.Port != mdnsPort || fixture.index != scope.InterfaceIndex {
			t.Fatal("sender widened selected bind")
		}
		_ = packet.Close()
	}
	for _, address := range []string{"0.0.0.0", "224.0.0.251", "127.0.0.1", "203.0.113.1"} {
		fixture := &nativeSocketScenario{}
		scope := testScope()
		scope.LocalAddress = netip.MustParseAddr(address)
		if _, _, err := openNativeLocalSocket(scope, discoveryPolicy{}, fixture.ops(t)); err == nil || len(fixture.calls) != 0 {
			t.Fatal("invalid sender scope opened socket")
		}
	}
}
func TestNativeSenderPartialConstructorClosesBothOwners(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		first, second := &nativeSenderFixture{}, &nativeSenderFixture{}
		if uncertain {
			second.closeErr = errSyntheticClose
		}
		receiver := &closeOnlyTransport{close: first.Close}
		sender := &closeOnlyTransport{close: second.Close}
		owner := &nativeTransport{closeOwner: receiver}
		returned, err := adoptNativeSender(owner, func() (nativePacketWriter, Transport, error) {
			return nil, sender, socketFailure("socket_write_buffer")
		})
		if !errors.Is(err, ErrSocketUnavailable) || first.calls.Load() != 1 || second.calls.Load() != 1 {
			t.Fatal("partial sender did not close both owners")
		}
		if uncertain {
			if returned != owner || !errors.Is(err, ErrCloseUncertain) || IsUnavailable(err) {
				t.Fatal("partial constructor lost uncertain sender")
			}
			_ = returned.Close()
			if first.calls.Load() != 1 || second.calls.Load() != 1 {
				t.Fatal("partial constructor repeated close")
			}
		} else if returned != nil {
			t.Fatal("confirmed rollback retained unnecessary owner")
		}
	}
}
func TestNativeSenderMissingSecondOwnerClosesReceiver(t *testing.T) {
	first := &nativeSenderFixture{}
	owner := &nativeTransport{closeOwner: &closeOnlyTransport{close: first.Close}}
	returned, err := adoptNativeSender(owner, func() (nativePacketWriter, Transport, error) { return nil, nil, socketFailure("socket_create") })
	if returned != nil || !errors.Is(err, ErrSocketUnavailable) || first.calls.Load() != 1 {
		t.Fatal("failed second open leaked first owner")
	}
}
func TestNativeSenderCloseUnblocksWriteAndRetainsErrors(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		receiver := &nativeSenderFixture{}
		sender := &nativeSenderFixture{closed: make(chan struct{}), started: make(chan struct{})}
		if uncertain {
			receiver.closeErr = errSyntheticClose
		}
		owner := &nativeTransport{closeOwner: &closeOnlyTransport{close: receiver.Close}}
		if _, err := adoptNativeSender(owner, func() (nativePacketWriter, Transport, error) {
			return sender, &closeOnlyTransport{close: sender.Close}, nil
		}); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- writeNativePacket(owner.sender, packQuery(), sourceOnlySendControl(testScope())) }()
		<-sender.started
		err := owner.Close()
		select {
		case <-done:
		case <-time.After(closeJoinTimeout):
			t.Fatal("sender close did not unblock write")
		}
		if receiver.calls.Load() != 1 || sender.calls.Load() != 1 {
			t.Fatal("duplex owner did not close both")
		}
		if uncertain != errors.Is(err, ErrCloseUncertain) {
			t.Fatal("close uncertainty changed")
		}
		_ = owner.Close()
		if receiver.calls.Load() != 1 || sender.calls.Load() != 1 {
			t.Fatal("duplex close repeated")
		}
		if !sender.control.Src.Equal(net.IP(testScope().LocalAddress.AsSlice())) || sender.control.IfIndex != 0 || sender.destination.String() != "224.0.0.251:5353" {
			t.Fatal("Darwin source-only control or destination changed")
		}
	}
}
func TestNativePairedCloseStartsBothBeforeWaiting(t *testing.T) {
	release := make(chan struct{})
	first := &nativeSenderFixture{holdClose: release}
	second := &nativeSenderFixture{}
	owner := pairedCloseOwner(first, second)
	done := make(chan error, 1)
	go func() { done <- owner.Close() }()
	eventually(t, func() bool { return first.calls.Load() == 1 && second.calls.Load() == 1 })
	select {
	case <-done:
		t.Fatal("paired close claimed join while held")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
