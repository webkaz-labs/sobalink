package directlan

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"

	wgconn "github.com/tailscale/wireguard-go/conn"
)

type bindPolicy struct {
	endpoints   map[netip.AddrPort]bool
	sources     map[netip.Addr]bool
	generations map[netip.Addr]*peerState
}
type lanBind struct {
	mu     sync.Mutex
	cfg    Config
	socket *net.UDPConn
	policy atomic.Pointer[bindPolicy]
}
type lanEndpoint struct{ destination, source netip.AddrPort }

func (e *lanEndpoint) ClearSrc()           {}
func (e *lanEndpoint) SrcToString() string { return e.source.String() }
func (e *lanEndpoint) DstToString() string { return e.destination.String() }
func (e *lanEndpoint) DstIP() netip.Addr   { return e.destination.Addr() }
func (e *lanEndpoint) SrcIP() netip.Addr   { return e.source.Addr() }
func (e *lanEndpoint) DstToBytes() []byte  { b, _ := e.destination.MarshalBinary(); return b }
func (b *lanBind) Open(port uint16) ([]wgconn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.socket != nil {
		return nil, 0, wgconn.ErrBindAlreadyOpen
	}
	if port != b.cfg.Listen.Port() {
		return nil, 0, ErrPolicy
	}
	network := "udp6"
	if b.cfg.Listen.Addr().Is4() {
		network = "udp4"
	}
	c, e := net.ListenUDP(network, net.UDPAddrFromAddrPort(b.cfg.Listen))
	if e != nil {
		return nil, 0, e
	}
	b.socket = c
	recv := func(slab []byte, packets []wgconn.ReceivedPacket) (int, error) {
		if len(packets) < 1 {
			return 0, ErrCapacity
		}
		for {
			n, ap, e := c.ReadFromUDPAddrPort(slab)
			if e != nil {
				return 0, e
			}
			p := b.policy.Load()
			if !b.cfg.permits(ap, true) || p == nil || !p.endpoints[ap] {
				continue
			}
			packets[0] = wgconn.ReceivedPacket{Offset: 0, Size: n, Endpoint: &lanEndpoint{destination: ap, source: b.cfg.Listen}}
			return 1, nil
		}
	}
	return []wgconn.ReceiveFunc{recv}, port, nil
}
func (b *lanBind) Close() error {
	b.mu.Lock()
	c := b.socket
	b.socket = nil
	b.mu.Unlock()
	if c != nil {
		return c.Close()
	}
	return nil
}
func (b *lanBind) SetMark(mark uint32) error {
	if mark != 0 {
		return errors.New("OS socket marks are unsupported")
	}
	return nil
}
func (b *lanBind) BatchSize() int { return 1 }
func (b *lanBind) ParseEndpoint(s string) (wgconn.Endpoint, error) {
	ap, e := netip.ParseAddrPort(s)
	if e != nil || s != ap.String() || !b.cfg.permits(ap, true) {
		return nil, ErrPolicy
	}
	p := b.policy.Load()
	if p == nil || !p.endpoints[ap] {
		return nil, ErrUntrusted
	}
	return &lanEndpoint{destination: ap, source: b.cfg.Listen}, nil
}
func (b *lanBind) Send(bufs [][]byte, ep wgconn.Endpoint, offset int) error {
	target, ok := ep.(*lanEndpoint)
	if !ok {
		return wgconn.ErrWrongEndpointType
	}
	p := b.policy.Load()
	if p == nil || !p.endpoints[target.destination] || !b.cfg.permits(target.destination, true) {
		return ErrUntrusted
	}
	b.mu.Lock()
	c := b.socket
	b.mu.Unlock()
	if c == nil {
		return net.ErrClosed
	}
	for _, data := range bufs {
		if offset < 0 || offset > len(data) {
			return ErrPolicy
		}
		if _, e := c.WriteToUDPAddrPort(data[offset:], target.destination); e != nil {
			return e
		}
	}
	return nil
}

var _ wgconn.Bind = (*lanBind)(nil)
