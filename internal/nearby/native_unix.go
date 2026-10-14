//go:build linux || darwin

package nearby

import (
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

type nativeTransport struct {
	conn       *net.UDPConn
	packet     *ipv4.PacketConn
	scope      Scope
	ifi        net.Interface
	policy     discoveryPolicy
	once       sync.Once
	closeErr   error
	sender     nativePacketWriter
	closeOwner Transport
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
	raw, failedOwner, err := openNativeGroupSocket(scope.InterfaceIndex, systemNativeSocketOps())
	if err != nil {
		return failedOwner, err
	}
	conn, ok := raw.(*net.UDPConn)
	if !ok {
		return rollbackTransport(&closeOnlyTransport{close: raw.Close}, socketFailure("socket_type"))
	}
	packet := ipv4.NewPacketConn(conn)
	owner := &nativeTransport{conn: conn, packet: packet, sender: packet, closeOwner: &closeOnlyTransport{close: packet.Close}, scope: scope, ifi: ifi, policy: policy}
	fail := func(stage nativeSocketStage) (Transport, error) {
		return rollbackTransport(owner, socketFailure(stage))
	}
	if conn.LocalAddr().String() != "224.0.0.251:5353" {
		return fail("socket_bound_address")
	}
	for _, step := range []struct {
		stage nativeSocketStage
		apply func() error
	}{
		{"socket_read_buffer", func() error { return conn.SetReadBuffer(16 * MaxDatagramBytes) }},
		{"socket_write_buffer", func() error { return conn.SetWriteBuffer(4 * MaxDatagramBytes) }},
		{"socket_receive_metadata", func() error { return packet.SetControlMessage(ipv4.FlagTTL|ipv4.FlagDst|ipv4.FlagInterface, true) }},
		{"socket_multicast_interface", func() error { return packet.SetMulticastInterface(&ifi) }},
		{"socket_multicast_ttl", func() error { return packet.SetMulticastTTL(255) }},
		{"socket_multicast_loopback", func() error { return packet.SetMulticastLoopback(true) }},
		{"socket_join_group", func() error { return packet.JoinGroup(&ifi, &net.UDPAddr{IP: net.IP(multicast4.AsSlice())}) }},
	} {
		if step.apply() != nil {
			return fail(step.stage)
		}
	}
	return attachNativePlatformSender(owner)
}
func (t *nativeTransport) ReadPacket() (Packet, error) {
	data := make([]byte, MaxDatagramBytes+1)
	n, cm, source, err := t.packet.ReadFrom(data)
	if err != nil {
		return Packet{}, socketFailure("socket_read")
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
	return writeNativePacket(t.sender, data, nativeSendControl(t.scope))
}

// The writer interface permits ownership/control regressions without sockets.
type nativePacketWriter interface {
	SetWriteDeadline(time.Time) error
	WriteTo([]byte, *ipv4.ControlMessage, net.Addr) (int, error)
	Close() error
}

func writeNativePacket(sender nativePacketWriter, data []byte, cm *ipv4.ControlMessage) error {
	if sender.SetWriteDeadline(time.Now().Add(time.Second)) != nil {
		return socketFailure("socket_write_deadline")
	}
	n, err := sender.WriteTo(data, cm, &net.UDPAddr{IP: net.IP(multicast4.AsSlice()), Port: mdnsPort})
	return classifyNativeSend(n, len(data), 0, 0, err)
}

func sourceOnlySendControl(scope Scope) *ipv4.ControlMessage {
	// Darwin honors the source only when ancillary IfIndex is zero. Its
	// socket-local IP_BOUND_IF still enforces the selected interface.
	return &ipv4.ControlMessage{Src: net.IP(scope.LocalAddress.AsSlice())}
}

func adoptNativeSender(owner *nativeTransport, open func() (nativePacketWriter, Transport, error)) (Transport, error) {
	sender, senderOwner, err := open()
	if senderOwner != nil {
		owner.closeOwner = pairedCloseOwner(owner.closeOwner, senderOwner)
	}
	if err != nil {
		return rollbackTransport(owner, err)
	}
	if sender == nil || senderOwner == nil {
		return rollbackTransport(owner, socketFailure("socket_type"))
	}
	owner.sender = sender
	return owner, nil
}

func (t *nativeTransport) Close() error {
	t.once.Do(func() { t.closeErr = normalizedCloseError(t.closeOwner.Close()) })
	return t.closeErr
}
