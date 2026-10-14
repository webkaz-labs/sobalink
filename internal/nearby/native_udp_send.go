package nearby

import (
	"net"
	"time"
)

// Windows already binds the exact selected local source and fixes multicast
// egress on that socket. Ordinary UDP sends preserve that contract without the
// pinned Go WSASendMsg synchronous byte-count ambiguity. Never retry a send.
type nativeUDPWriter interface {
	SetWriteDeadline(time.Time) error
	WriteToUDP([]byte, *net.UDPAddr) (int, error)
}

func writeScopedUDPPacket(sender nativeUDPWriter, data []byte, bindingCurrent func() error) error {
	if len(data) > MaxDatagramBytes || len(data) < 12 {
		return ErrInvalidPacket
	}
	if err := bindingCurrent(); err != nil {
		return err
	}
	if sender.SetWriteDeadline(time.Now().Add(time.Second)) != nil {
		return socketFailure("socket_write_deadline")
	}
	n, err := sender.WriteToUDP(data, &net.UDPAddr{IP: net.IP(multicast4.AsSlice()), Port: mdnsPort})
	return classifyNativeSend(n, len(data), 0, 0, err)
}
