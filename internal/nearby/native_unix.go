//go:build linux || darwin

package nearby

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

type nativeTransport struct {
	conn     *net.UDPConn
	packet   *ipv4.PacketConn
	scope    Scope
	ifi      net.Interface
	policy   discoveryPolicy
	once     sync.Once
	closeErr error
}

// NativeFactory opens IPv4 mDNS on exactly one selected interface. It binds
// the multicast group itself, never a wildcard, and never retries with a
// broader bind. IPv6 is currently explicit unsupported; manual setup remains.
// A bind/options failure is not permission to modify OS/network policy.
func NativeFactory(scope Scope) (Transport, error) { return nativeFactory(scope, discoveryPolicy{}) }

func nativeFactory(scope Scope, policy discoveryPolicy) (Transport, error) {
	if err := policy.validateScope(scope); err != nil {
		return nil, err
	}
	if !scope.LocalAddress.Is4() {
		return nil, ErrUnsupported
	}
	ifi, err := currentBindingWithPolicy(scope, nil, policy)
	if err != nil {
		return nil, err
	}
	lc := net.ListenConfig{Control: func(network, address string, raw syscall.RawConn) error {
		if network != "udp4" || address != "224.0.0.251:5353" {
			return ErrInvalidScope
		}
		var optionErr error
		err := raw.Control(func(fd uintptr) {
			if optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); optionErr != nil {
				return
			}
			optionErr = configureNativeSocket(int(fd), scope.InterfaceIndex)
		})
		if err != nil {
			return err
		}
		return optionErr
	}}
	raw, err := lc.ListenPacket(context.Background(), "udp4", "224.0.0.251:5353")
	if err != nil {
		return nil, ErrSocketUnavailable
	}
	conn, ok := raw.(*net.UDPConn)
	if !ok {
		return rollbackTransport(&closeOnlyTransport{close: raw.Close}, ErrSocketUnavailable)
	}
	packet := ipv4.NewPacketConn(conn)
	owner := &nativeTransport{conn: conn, packet: packet, scope: scope, ifi: ifi, policy: policy}
	fail := func() (Transport, error) { return rollbackTransport(owner, ErrSocketUnavailable) }
	if conn.LocalAddr().String() != "224.0.0.251:5353" {
		return fail()
	}
	if conn.SetReadBuffer(16*MaxDatagramBytes) != nil || conn.SetWriteBuffer(4*MaxDatagramBytes) != nil {
		return fail()
	}
	if packet.SetControlMessage(ipv4.FlagTTL|ipv4.FlagDst|ipv4.FlagInterface, true) != nil {
		return fail()
	}
	if packet.SetMulticastInterface(&ifi) != nil || packet.SetMulticastTTL(255) != nil || packet.SetMulticastLoopback(true) != nil {
		return fail()
	}
	if packet.JoinGroup(&ifi, &net.UDPAddr{IP: net.IP(multicast4.AsSlice())}) != nil {
		return fail()
	}
	return owner, nil
}
func (t *nativeTransport) ReadPacket() (Packet, error) {
	data := make([]byte, MaxDatagramBytes+1)
	n, cm, source, err := t.packet.ReadFrom(data)
	if err != nil {
		return Packet{}, ErrSocketUnavailable
	}
	if _, err := currentBindingWithPolicy(t.scope, &t.ifi, t.policy); err != nil {
		return Packet{}, err
	}
	p := Packet{Data: data[:min(n, len(data))]}
	if cm == nil {
		return p, nil
	} // fail closed at Session validation, count input
	from, ok := source.(*net.UDPAddr)
	if !ok {
		return p, nil
	}
	dst, ok := netip.AddrFromSlice(cm.Dst)
	if !ok {
		return p, nil
	}
	p.Source, p.Destination, p.InterfaceIndex, p.HopLimit = from.AddrPort(), dst.Unmap(), cm.IfIndex, cm.TTL
	return p, nil
}
func (t *nativeTransport) WritePacket(data []byte) error {
	if len(data) > MaxDatagramBytes || len(data) < 12 {
		return ErrInvalidPacket
	}
	if _, err := currentBindingWithPolicy(t.scope, &t.ifi, t.policy); err != nil {
		return err
	}
	if t.conn.SetWriteDeadline(time.Now().Add(time.Second)) != nil {
		return ErrSocketUnavailable
	}
	cm := &ipv4.ControlMessage{Src: net.IP(t.scope.LocalAddress.AsSlice()), IfIndex: t.scope.InterfaceIndex}
	n, err := t.packet.WriteTo(data, cm, &net.UDPAddr{IP: net.IP(multicast4.AsSlice()), Port: mdnsPort})
	if err != nil || n != len(data) {
		return ErrSocketUnavailable
	}
	return nil
}
func (t *nativeTransport) Close() error {
	t.once.Do(func() { t.closeErr = normalizedCloseError(t.packet.Close()) })
	return t.closeErr
}
