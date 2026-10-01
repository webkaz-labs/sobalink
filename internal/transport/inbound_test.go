package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func inboundListener(t *testing.T) net.Listener {
	t.Helper()
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { l.Close() })
	return l
}
func TestInboundRejectsNonNumericLoopback(t *testing.T) {
	for _, target := range []string{"localhost:80", "127.0.0.2:80", "[::ffff:127.0.0.1]:80", "192.168.1.1:80", "8.8.8.8:80", "100.64.1.2:80", "127.0.0.1:0", "[::1%lo]:80", "example.invalid:443"} {
		if _, e := loopbackTarget(target); e == nil {
			t.Fatal("accepted", target)
		}
	}
	for _, target := range []string{"127.0.0.1:1", "[::1]:65535"} {
		if _, e := loopbackTarget(target); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := StartInboundTCP(t.Context(), TCPConfig{Target: "127.0.0.1:80"}, nil, nil, nil); e == nil {
		t.Fatal("missing authorization")
	}
}
func TestInboundTCPPreservesHalfCloseAndStop(t *testing.T) {
	service := inboundListener(t)
	serviceDone := make(chan error, 1)
	go func() {
		c, e := service.Accept()
		if e != nil {
			serviceDone <- e
			return
		}
		defer c.Close()
		b, e := io.ReadAll(c)
		if e == nil {
			_, e = c.Write(append([]byte("reply:"), b...))
		}
		serviceDone <- e
	}()
	incoming := inboundListener(t)
	var authCalls atomic.Int64
	s, e := StartInboundTCP(t.Context(), TCPConfig{Target: service.Addr().String()}, incoming, func(context.Context, netip.AddrPort) error { authCalls.Add(1); return nil }, func() error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	defer closeServer(t, s)
	c, e := net.DialTCP("tcp4", nil, incoming.Addr().(*net.TCPAddr))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(nativeTestTimeout))
	c.Write([]byte("hello"))
	c.CloseWrite()
	b, e := io.ReadAll(c)
	if e != nil || string(b) != "reply:hello" {
		t.Fatal(string(b), e)
	}
	if e = <-serviceDone; e != nil {
		t.Fatal(e)
	}
	if authCalls.Load() < 3 {
		t.Fatal("source not revalidated")
	}
}
func TestInboundDeniedSourceNeverDialsService(t *testing.T) {
	service := inboundListener(t)
	incoming := inboundListener(t)
	denied := make(chan struct{})
	var once sync.Once
	s, e := StartInboundTCP(t.Context(), TCPConfig{Target: service.Addr().String()}, incoming, func(context.Context, netip.AddrPort) error {
		once.Do(func() { close(denied) })
		return errors.New("not selected")
	}, func() error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	client, e := net.Dial("tcp", incoming.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	select {
	case <-denied:
	case <-time.After(nativeTestTimeout):
		t.Fatal("source not checked")
	}
	closeServer(t, s)
	service.(*net.TCPListener).SetDeadline(time.Now())
	if c, e := service.Accept(); e == nil {
		c.Close()
		t.Fatal("denied source reached local service")
	}
}
func TestInboundTCPRevocationClosesExistingFlow(t *testing.T) {
	service := inboundListener(t)
	incoming := inboundListener(t)
	accepted := make(chan net.Conn, 1)
	go func() {
		c, e := service.Accept()
		if e == nil {
			accepted <- c
		}
	}()
	var revoked atomic.Bool
	authorize := func(context.Context, netip.AddrPort) error {
		if revoked.Load() {
			return errors.New("revoked")
		}
		return nil
	}
	s, e := StartInboundTCP(t.Context(), TCPConfig{Target: service.Addr().String()}, incoming, authorize, func() error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	defer closeServer(t, s)
	client, e := net.Dial("tcp", incoming.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	var local net.Conn
	select {
	case local = <-accepted:
	case <-time.After(nativeTestTimeout):
		t.Fatal("local connection unavailable")
	}
	defer local.Close()
	revoked.Store(true)
	local.SetDeadline(time.Now().Add(nativeTestTimeout))
	local.Write([]byte("must-not-deliver"))
	client.SetReadDeadline(time.Now().Add(nativeTestTimeout))
	b := make([]byte, 32)
	if n, e := client.Read(b); n != 0 || e == nil {
		t.Fatal("revoked reply delivered", n, e)
	}
}
func TestInboundUDPSourcesAndDelayedReplies(t *testing.T) {
	service := udpSocket(t)
	incoming := udpSocket(t)
	s, e := StartInboundUDP(t.Context(), UDPConfig{Target: service.LocalAddr().String()}, incoming, func(context.Context, netip.AddrPort) error { return nil }, func() error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	defer closeServer(t, s)
	a, b := udpClient(t, s.Addr()), udpClient(t, s.Addr())
	a.Write([]byte("a"))
	got, as := receiveUDP(t, service)
	if string(got) != "a" {
		t.Fatal(string(got))
	}
	b.Write([]byte("b"))
	got, bs := receiveUDP(t, service)
	if string(got) != "b" || as == bs {
		t.Fatal("sources not isolated", as, bs)
	}
	service.WriteToUDPAddrPort([]byte("second-first"), bs)
	service.WriteToUDPAddrPort([]byte("first-later"), as)
	if got, _ = receiveUDP(t, a); string(got) != "first-later" {
		t.Fatal(string(got))
	}
	if got, _ = receiveUDP(t, b); string(got) != "second-first" {
		t.Fatal(string(got))
	}
	service.WriteToUDPAddrPort([]byte("async"), as)
	if got, _ = receiveUDP(t, a); string(got) != "async" {
		t.Fatal(string(got))
	}
}
func TestInboundUDPRevokedReplyNotDelivered(t *testing.T) {
	service := udpSocket(t)
	incoming := udpSocket(t)
	var revoked atomic.Bool
	denied := make(chan struct{})
	var once sync.Once
	authorize := func(context.Context, netip.AddrPort) error {
		if revoked.Load() {
			once.Do(func() { close(denied) })
			return errors.New("revoked")
		}
		return nil
	}
	s, e := StartInboundUDP(t.Context(), UDPConfig{Target: service.LocalAddr().String()}, incoming, authorize, func() error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	client := udpClient(t, s.Addr())
	client.Write([]byte("setup"))
	_, mapping := receiveUDP(t, service)
	revoked.Store(true)
	service.WriteToUDPAddrPort([]byte("secret"), mapping)
	select {
	case <-denied:
	case <-time.After(nativeTestTimeout):
		t.Fatal("reply not revalidated")
	}
	closeServer(t, s)
	client.SetReadDeadline(time.Now())
	buf := make([]byte, 32)
	if n, e := client.Read(buf); n != 0 || e == nil {
		t.Fatal("revoked reply delivered", n, e)
	}
}
func TestInboundUDPGuardPreventsNewLocalMapping(t *testing.T) {
	service := udpSocket(t)
	incoming := udpSocket(t)
	guarded := make(chan struct{})
	var once sync.Once
	s, e := StartInboundUDP(t.Context(), UDPConfig{Target: service.LocalAddr().String()}, incoming, func(context.Context, netip.AddrPort) error { return nil }, func() error { once.Do(func() { close(guarded) }); return errors.New("expired") })
	if e != nil {
		t.Fatal(e)
	}
	client := udpClient(t, s.Addr())
	client.Write([]byte("no"))
	select {
	case <-guarded:
	case <-time.After(nativeTestTimeout):
		t.Fatal("guard not consulted")
	}
	closeServer(t, s)
	service.SetReadDeadline(time.Now())
	b := make([]byte, 10)
	if n, _, e := service.ReadFrom(b); n != 0 || e == nil {
		t.Fatal("expired packet forwarded")
	}
}
func TestInboundLifetimeChecksBothSidesOfRead(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	var calls atomic.Int64
	c := &inboundConn{Conn: a, validate: func() error {
		if calls.Add(1) > 1 {
			return errors.New("expired during read")
		}
		return nil
	}}
	go func() { b.Write([]byte("private")) }()
	buf := make([]byte, 16)
	if n, e := c.Read(buf); n != 0 || e == nil {
		t.Fatal("post-expiry read visible", n, e)
	}
	for _, v := range buf {
		if v != 0 {
			t.Fatal("expired buffer not cleared")
		}
	}
}
