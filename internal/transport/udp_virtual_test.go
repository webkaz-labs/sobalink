package transport

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This socket is used only inside a synctest bubble. Channel-backed packet I/O
// and net.Pipe let the real session workers become durably blocked; an OS
// socket read would instead prevent the virtual clock from advancing.
type virtualDatagram struct {
	data    []byte
	address netip.AddrPort
}

type virtualPacketSocket struct {
	incoming chan virtualDatagram
	outgoing chan virtualDatagram
	closed   chan struct{}
	once     sync.Once
}

func (c *virtualPacketSocket) ReadFromUDPAddrPort(buffer []byte) (int, netip.AddrPort, error) {
	select {
	case packet := <-c.incoming:
		return copy(buffer, packet.data), packet.address, nil
	case <-c.closed:
		return 0, netip.AddrPort{}, net.ErrClosed
	}
}

func (c *virtualPacketSocket) WriteToUDPAddrPort(buffer []byte, address netip.AddrPort) (int, error) {
	packet := virtualDatagram{data: append([]byte(nil), buffer...), address: address}
	select {
	case c.outgoing <- packet:
		return len(buffer), nil
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *virtualPacketSocket) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *virtualPacketSocket) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12344}
}

type virtualMapping struct {
	peer   net.Conn
	closed <-chan struct{}
	writes *atomic.Int64
}

type countedVirtualConn struct {
	net.Conn
	writes *atomic.Int64
}

func (c *countedVirtualConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return c.Conn.Write(p)
}

func (m virtualMapping) assertOpen(t *testing.T) {
	t.Helper()
	select {
	case <-m.closed:
		t.Fatal("active mapping closed before its idle deadline")
	default:
	}
}

func (m virtualMapping) assertClosed(t *testing.T) {
	t.Helper()
	select {
	case <-m.closed:
	default:
		t.Fatal("mapping remained open past its idle deadline")
	}
}

type virtualForwarder struct {
	server *Server
	local  *virtualPacketSocket
	table  *udpTable
	dialed chan virtualMapping
}

func virtualUDPServer(t *testing.T, idle time.Duration, maxSessions int) *virtualForwarder {
	t.Helper()
	local := &virtualPacketSocket{incoming: make(chan virtualDatagram), outgoing: make(chan virtualDatagram), closed: make(chan struct{})}
	dialed := make(chan virtualMapping, maxSessions+1)
	tableReady := make(chan *udpTable, 1)
	server := startServer(context.Background(), local, local.LocalAddr(), func(s *Server) {
		table := &udpTable{
			sessions: make(map[netip.AddrPort]*udpSession), server: s, local: local,
			cfg: UDPConfig{Target: "server.example:21116", IdleTimeout: idle, MaxSessions: maxSessions, QueueSize: defaultUDPQueueSize, DialTimeout: defaultDialTimeout, WriteTimeout: defaultDialTimeout},
			dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				remote, peer := net.Pipe()
				closed := make(chan struct{})
				writes := new(atomic.Int64)
				dialed <- virtualMapping{peer: peer, closed: closed, writes: writes}
				return &trackedConn{Conn: &countedVirtualConn{Conn: remote, writes: writes}, closed: closed}, nil
			},
		}
		tableReady <- table
		table.readLocal()
	})
	return &virtualForwarder{server: server, local: local, table: <-tableReady, dialed: dialed}
}

func (v *virtualForwarder) send(source netip.AddrPort, data string) {
	v.local.incoming <- virtualDatagram{data: []byte(data), address: source}
}

func (v *virtualForwarder) receive(t *testing.T) virtualDatagram {
	t.Helper()
	select {
	case packet := <-v.local.outgoing:
		return packet
	case <-time.After(time.Second):
		t.Fatal("upstream reply was not delivered to its local source")
		return virtualDatagram{}
	}
}

func (v *virtualForwarder) mapping(t *testing.T) virtualMapping {
	t.Helper()
	select {
	case mapping := <-v.dialed:
		return mapping
	case <-time.After(time.Second):
		t.Fatal("source did not create an upstream mapping")
		return virtualMapping{}
	}
}

func (v *virtualForwarder) noAdditionalMapping(t *testing.T) {
	t.Helper()
	select {
	case m := <-v.dialed:
		m.peer.Close()
		t.Fatal("unexpected extra upstream mapping")
	default:
	}
}

func (v *virtualForwarder) sessionCount() int {
	v.table.mu.Lock()
	defer v.table.mu.Unlock()
	return len(v.table.sessions)
}
