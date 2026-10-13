//go:build windows

package nearby

import (
	"context"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

type nativeTransport struct {
	conn     *net.UDPConn
	scope    Scope
	ifi      net.Interface
	policy   discoveryPolicy
	once     sync.Once
	closeErr error
}

// NativeFactory uses supported Winsock message I/O through net.UDPConn.
// IP_IFLIST restricts this socket's ingress to one selected interface on
// Windows 10 1803+. It binds only the selected numeric local address; there is no wildcard retry.
// Native multicast behavior must be tested separately on each target OS.
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
	bind := net.JoinHostPort(scope.LocalAddress.String(), "5353")
	var controlFailure error
	lc := net.ListenConfig{Control: func(network, address string, raw syscall.RawConn) error {
		if network != "udp4" || address != bind {
			controlFailure = socketFailure("socket_bound_address")
			return controlFailure
		}
		var optionErr error
		err := raw.Control(func(fd uintptr) {
			socket := windows.Handle(fd)
			if optionErr = windows.SetsockoptInt(socket, windows.SOL_SOCKET, windows.SO_REUSEADDR, 1); optionErr != nil {
				return
			}
			for _, opt := range []struct{ name, value int }{{winIPInterfaceList, 1}, {winIPAddInterface, scope.InterfaceIndex}, {windows.IP_PKTINFO, 1}, {winIPRecvTTL, 1}, {windows.IP_MULTICAST_TTL, 255}, {windows.IP_MULTICAST_LOOP, 1}} {
				if optionErr = windows.SetsockoptInt(socket, windows.IPPROTO_IP, opt.name, opt.value); optionErr != nil {
					return
				}
			}
			local := scope.LocalAddress.As4()
			if optionErr = windows.SetsockoptInet4Addr(socket, windows.IPPROTO_IP, windows.IP_MULTICAST_IF, local); optionErr != nil {
				return
			}
			membership := windows.IPMreq{Multiaddr: multicast4.As4(), Interface: local}
			optionErr = windows.SetsockoptIPMreq(socket, windows.IPPROTO_IP, windows.IP_ADD_MEMBERSHIP, &membership)
		})
		if err != nil || optionErr != nil {
			controlFailure = socketFailure("socket_interface_options")
			return controlFailure
		}
		return nil
	}}
	raw, err := lc.ListenPacket(context.Background(), "udp4", bind)
	if err != nil {
		if controlFailure != nil {
			return nil, controlFailure
		}
		return nil, socketFailure("socket_bind")
	}
	conn, ok := raw.(*net.UDPConn)
	if !ok {
		return rollbackTransport(&closeOnlyTransport{close: raw.Close}, socketFailure("socket_type"))
	}
	owner := &nativeTransport{conn: conn, scope: scope, ifi: ifi, policy: policy}
	if conn.LocalAddr().String() != bind {
		return rollbackTransport(owner, socketFailure("socket_bound_address"))
	}
	if conn.SetReadBuffer(16*MaxDatagramBytes) != nil {
		return rollbackTransport(owner, socketFailure("socket_read_buffer"))
	}
	if conn.SetWriteBuffer(4*MaxDatagramBytes) != nil {
		return rollbackTransport(owner, socketFailure("socket_write_buffer"))
	}
	return owner, nil
}
func (t *nativeTransport) ReadPacket() (Packet, error) {
	data, oob := make([]byte, MaxDatagramBytes+1), make([]byte, 128)
	n, nn, flags, source, err := t.conn.ReadMsgUDP(data, oob)
	if err != nil {
		return Packet{}, socketFailure("socket_read")
	}
	if _, err := currentBindingWithPolicy(t.scope, &t.ifi, t.policy); err != nil {
		return Packet{}, err
	}
	p := Packet{Data: data[:min(n, len(data))]}
	if source == nil || flags&(windows.MSG_TRUNC|windows.MSG_CTRUNC) != 0 {
		return p, nil
	}
	dst, index, ttl, ok := winReceiveControl(oob[:nn], strconv.IntSize/8)
	if !ok {
		return p, nil
	}
	p.Source, p.Destination, p.InterfaceIndex, p.HopLimit = source.AddrPort(), dst, index, ttl
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
		return socketFailure("socket_write_deadline")
	}
	oob := winSendControl(t.scope, strconv.IntSize/8)
	n, nn, err := t.conn.WriteMsgUDP(data, oob, &net.UDPAddr{IP: net.IP(multicast4.AsSlice()), Port: mdnsPort})
	return classifyNativeSend(n, len(data), nn, len(oob), err)
}
func (t *nativeTransport) Close() error {
	t.once.Do(func() { t.closeErr = normalizedCloseError(t.conn.Close()) })
	return t.closeErr
}
