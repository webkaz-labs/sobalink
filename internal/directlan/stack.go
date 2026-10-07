/* SPDX-License-Identifier: MIT
 * Copyright (C) 2017-2023 WireGuard LLC. All Rights Reserved.
 * Adapted from github.com/tailscale/wireguard-go/tun/netstack/tun.go.
 * See UPSTREAM.md and WIREGUARD_LICENSE for provenance and changes.
 */
package directlan

import (
	"context"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"

	"github.com/tailscale/wireguard-go/tun"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// userspaceTunnel is an in-memory WireGuard TUN, never a kernel device. The
// stack has a single local /128 and no forwarding, resolver or OS routes.
type userspaceTunnel struct {
	ep          *channel.Endpoint
	stack       *stack.Stack
	events      chan tun.Event
	done        chan struct{}
	notify      chan struct{}
	once        sync.Once
	destroyOnce sync.Once
	owner       *runtimeGeneration
	allow       func(src, dst netip.Addr) bool
}

func newUserspaceTunnel(local netip.Addr, allow func(netip.Addr, netip.Addr) bool) (*userspaceTunnel, error) {
	s := stack.New(stack.Options{NetworkProtocols: []stack.NetworkProtocolFactory{ipv6.NewProtocol}, TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol6}, HandleLocal: false})
	t := &userspaceTunnel{ep: channel.New(256, 1280, ""), stack: s, events: make(chan tun.Event, 1), done: make(chan struct{}), notify: make(chan struct{}, 1), allow: allow}
	t.ep.AddNotify(t)
	if e := s.CreateNIC(1, t.ep); e != nil {
		s.Destroy()
		return nil, io.ErrUnexpectedEOF
	}
	if e := s.AddProtocolAddress(1, tcpip.ProtocolAddress{Protocol: ipv6.ProtocolNumber, AddressWithPrefix: tcpip.AddrFromSlice(local.AsSlice()).WithPrefix()}, stack.AddressProperties{}); e != nil {
		t.Close()
		t.destroySealed()
		return nil, io.ErrUnexpectedEOF
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv6EmptySubnet, NIC: 1}})
	sack := tcpip.TCPSACKEnabled(true)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &sack)
	return t, nil
}
func (t *userspaceTunnel) File() *os.File           { return nil }
func (t *userspaceTunnel) Name() (string, error)    { return "sobalink-userspace-directlan", nil }
func (t *userspaceTunnel) MTU() (int, error)        { return 1280, nil }
func (t *userspaceTunnel) BatchSize() int           { return 1 }
func (t *userspaceTunnel) Events() <-chan tun.Event { return t.events }
func (t *userspaceTunnel) WriteNotify() {
	select {
	case t.notify <- struct{}{}:
	default:
	}
}
func (t *userspaceTunnel) Read(slab []byte, packets []tun.ReadPacket) (int, error) {
	for {
		select {
		case <-t.done:
			return 0, os.ErrClosed
		default:
		}
		pkt := t.ep.Read()
		if pkt != nil {
			if t.owner != nil && !t.owner.trafficOpen() {
				pkt.DecRef()
				continue
			}
			view := pkt.ToView()
			pkt.DecRef()
			defer view.Release()
			if len(packets) < 1 || len(slab) < 2*tun.ReadPacketSpacing+view.Size() {
				return 0, io.ErrShortBuffer
			}
			n, e := view.Read(slab[tun.ReadPacketSpacing : len(slab)-tun.ReadPacketSpacing])
			packets[0] = tun.ReadPacket{Offset: tun.ReadPacketSpacing, Size: n}
			return 1, e
		}
		select {
		case <-t.done:
			return 0, os.ErrClosed
		case <-t.notify:
		}
	}
}
func (t *userspaceTunnel) Write(bufs [][]byte, offset int) (int, error) {
	if t.owner != nil {
		if !t.owner.trafficOpen() {
			return 0, os.ErrClosed
		}
		lease, err := t.owner.acquireWork(nil, false)
		if err != nil {
			return 0, os.ErrClosed
		}
		defer lease.finish()
	}
	select {
	case <-t.done:
		return 0, os.ErrClosed
	default:
	}
	for _, b := range bufs {
		if offset < 0 || offset > len(b) {
			return 0, io.ErrShortBuffer
		}
		b = b[offset:]
		if len(b) < 40 || b[0]>>4 != 6 {
			continue
		}
		src, _ := netip.AddrFromSlice(b[8:24])
		dst, _ := netip.AddrFromSlice(b[24:40])
		if t.allow == nil || !t.allow(src, dst) {
			continue
		}
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(b)})
		t.ep.InjectInbound(ipv6.ProtocolNumber, pkt)
		pkt.DecRef()
	}
	return len(bufs), nil
}
func (t *userspaceTunnel) Close() error {
	t.once.Do(func() {
		close(t.done)
		t.ep.Close()
		close(t.events)
	})
	if t.owner != nil {
		t.owner.seal(net.ErrClosed)
	} else {
		t.destroySealed()
	}
	return nil
}

// destroySealed is supervisor-only after creator/ingress fences and WG join.
// TUN.Close is deliberately only a permanent stop/wake signal.
func (t *userspaceTunnel) destroySealed() {
	t.destroyOnce.Do(func() { t.stack.Close(); t.stack.Wait(); t.stack.Destroy() })
}
func fullAddress(ap netip.AddrPort) tcpip.FullAddress {
	return tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFromSlice(ap.Addr().AsSlice()), Port: ap.Port()}
}
func (t *userspaceTunnel) dialTCP(ctx context.Context, ap netip.AddrPort) (*gonet.TCPConn, error) {
	if t.owner != nil {
		return nil, ErrUnavailable
	}
	return gonet.DialContextTCP(ctx, t.stack, fullAddress(ap), ipv6.ProtocolNumber)
}
func (t *userspaceTunnel) listenTCP(ap netip.AddrPort) (*gonet.TCPListener, error) {
	if t.owner != nil {
		return nil, ErrUnavailable
	}
	return gonet.ListenTCP(t.stack, fullAddress(ap), ipv6.ProtocolNumber)
}
func (t *userspaceTunnel) dialUDP(local, remote netip.AddrPort) (*gonet.UDPConn, error) {
	if t.owner != nil {
		return nil, ErrUnavailable
	}
	l, r := fullAddress(local), fullAddress(remote)
	return gonet.DialUDP(t.stack, &l, &r, ipv6.ProtocolNumber)
}

var _ tun.Device = (*userspaceTunnel)(nil)
