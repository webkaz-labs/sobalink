package transport

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type trackedConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *trackedConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func udpSocket(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func udpClient(t *testing.T, addr net.Addr) *net.UDPConn {
	t.Helper()
	c, err := net.DialUDP("udp4", nil, addr.(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func receiveUDP(t *testing.T, c *net.UDPConn) ([]byte, netip.AddrPort) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 65535)
	n, from, err := c.ReadFromUDPAddrPort(buffer)
	if err != nil {
		t.Fatal(err)
	}
	return buffer[:n], from
}

func TestUDPPersistentMappingAndAsynchronousReply(t *testing.T) {
	t.Parallel()
	upstream := udpSocket(t)
	var dials atomic.Int64
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "udp" || address != upstream.LocalAddr().String() {
			return nil, errors.New("unexpected target")
		}
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	s, err := StartUDP(context.Background(), UDPConfig{ListenAddress: "127.0.0.1:0", Target: upstream.LocalAddr().String()}, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer(t, s)
	client := udpClient(t, s.Addr())
	var mapping netip.AddrPort
	for i, packet := range [][]byte{[]byte("one"), {}, []byte("three")} {
		if _, err := client.Write(packet); err != nil {
			t.Fatal(err)
		}
		got, from := receiveUDP(t, upstream)
		if string(got) != string(packet) {
			t.Fatalf("datagram %q, want %q", got, packet)
		}
		if i == 0 {
			mapping = from
		} else if from != mapping {
			t.Fatalf("upstream mapping changed: %v -> %v", mapping, from)
		}
	}
	if dials.Load() != 1 {
		t.Fatalf("dials %d, want 1", dials.Load())
	}
	// The reply is not tied to an outstanding per-packet request.
	time.Sleep(25 * time.Millisecond)
	if _, err := upstream.WriteToUDPAddrPort([]byte("delayed"), mapping); err != nil {
		t.Fatal(err)
	}
	if got, _ := receiveUDP(t, client); string(got) != "delayed" {
		t.Fatalf("delayed reply %q", got)
	}
	second := udpClient(t, s.Addr())
	second.Write([]byte("second source"))
	_, secondMapping := receiveUDP(t, upstream)
	if secondMapping == mapping {
		t.Fatal("different sources shared one upstream mapping")
	}
	if dials.Load() != 2 {
		t.Fatalf("dials %d, want 2", dials.Load())
	}
}

func TestUDPIdleExpiryAndSessionBound(t *testing.T) {
	t.Parallel()
	upstream := udpSocket(t)
	var dials atomic.Int64
	closed := make(chan struct{}, 8)
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		dials.Add(1)
		tracked := &trackedConn{Conn: c, closed: make(chan struct{})}
		go func() { <-tracked.closed; closed <- struct{}{} }()
		return tracked, nil
	}
	s, err := StartUDP(context.Background(), UDPConfig{ListenAddress: "127.0.0.1:0", Target: upstream.LocalAddr().String(), IdleTimeout: 100 * time.Millisecond, MaxSessions: 1}, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer(t, s)
	first, second := udpClient(t, s.Addr()), udpClient(t, s.Addr())
	first.Write([]byte("first"))
	receiveUDP(t, upstream)
	second.Write([]byte("over capacity"))
	upstream.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if _, _, err := upstream.ReadFromUDPAddrPort(make([]byte, 10)); err == nil {
		t.Fatal("capacity limit admitted a second source")
	}
	if dials.Load() != 1 {
		t.Fatal("capacity limit allocated an upstream socket")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("idle mapping was not closed")
	}
	// Closing the socket precedes table removal; allow the worker to finish.
	time.Sleep(10 * time.Millisecond)
	second.Write([]byte("after expiry"))
	if got, _ := receiveUDP(t, upstream); string(got) != "after expiry" {
		t.Fatalf("replacement packet %q", got)
	}
	if dials.Load() != 2 {
		t.Fatalf("dials %d, want 2", dials.Load())
	}
}

func TestUDPActivityExtendsSession(t *testing.T) {
	t.Parallel()
	upstream := udpSocket(t)
	var dials atomic.Int64
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	s, err := StartUDP(context.Background(), UDPConfig{ListenAddress: "127.0.0.1:0", Target: upstream.LocalAddr().String(), IdleTimeout: 100 * time.Millisecond}, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer(t, s)
	client := udpClient(t, s.Addr())
	client.Write([]byte("start"))
	_, mapping := receiveUDP(t, upstream)
	for i := 0; i < 8; i++ {
		time.Sleep(25 * time.Millisecond)
		upstream.WriteToUDPAddrPort([]byte("keepalive"), mapping)
		if got, _ := receiveUDP(t, client); string(got) != "keepalive" {
			t.Fatalf("reply %q", got)
		}
	}
	client.Write([]byte("still same"))
	_, gotMapping := receiveUDP(t, upstream)
	if gotMapping != mapping || dials.Load() != 1 {
		t.Fatal("active reverse traffic did not retain mapping")
	}
}

func TestUDPRevokedPolicyClosesMapping(t *testing.T) {
	for _, direction := range []string{"outbound", "inbound"} {
		t.Run(direction, func(t *testing.T) {
			upstream := udpSocket(t)
			var allowed atomic.Bool
			allowed.Store(true)
			var validations atomic.Int64
			closed := make(chan struct{})
			dial := func(ctx context.Context, network, address string) (net.Conn, error) {
				c, err := (&net.Dialer{}).DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				return &trackedConn{Conn: c, closed: closed}, nil
			}
			validate := func(ctx context.Context, network, address string) error {
				validations.Add(1)
				if network != "udp" || address != upstream.LocalAddr().String() {
					return errors.New("wrong target")
				}
				if !allowed.Load() {
					return errors.New("policy revoked")
				}
				return nil
			}
			s, err := StartUDP(context.Background(), UDPConfig{ListenAddress: "127.0.0.1:0", Target: upstream.LocalAddr().String(), Validate: validate}, dial)
			if err != nil {
				t.Fatal(err)
			}
			defer closeServer(t, s)
			client := udpClient(t, s.Addr())
			client.Write([]byte("allowed"))
			_, mapping := receiveUDP(t, upstream)
			allowed.Store(false)
			if direction == "outbound" {
				client.Write([]byte("denied"))
			} else {
				upstream.WriteToUDPAddrPort([]byte("denied"), mapping)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("revoked session remained open")
			}
			if validations.Load() < 2 {
				t.Fatal("policy was not revalidated per datagram")
			}
			if direction == "outbound" {
				upstream.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
				if _, _, err := upstream.ReadFromUDPAddrPort(make([]byte, 30)); err == nil {
					t.Fatal("denied outbound packet arrived")
				}
			} else {
				client.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
				if _, err := client.Read(make([]byte, 30)); err == nil {
					t.Fatal("denied inbound packet arrived")
				}
			}
		})
	}
}

func TestUDPExpiryInterruptsBlockedWrite(t *testing.T) {
	t.Parallel()
	closed := make(chan struct{})
	peerSeen := make(chan net.Conn, 1)
	dial := func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		peerSeen <- b
		return &trackedConn{Conn: a, closed: closed}, nil
	}
	s, err := StartUDP(context.Background(), UDPConfig{ListenAddress: "127.0.0.1:0", Target: "example.test:53", IdleTimeout: 40 * time.Millisecond, WriteTimeout: time.Second}, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer(t, s)
	udpClient(t, s.Addr()).Write([]byte("blocked because nobody reads"))
	peer := <-peerSeen
	defer peer.Close()
	select {
	case <-closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("idle expiry waited for a blocked write")
	}
}

func TestUDPShutdownCancelsDialAndValidation(t *testing.T) {
	for _, phase := range []string{"dial", "validate"} {
		t.Run(phase, func(t *testing.T) {
			started, canceled := make(chan struct{}), make(chan struct{})
			var peer net.Conn
			dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
				if phase == "dial" {
					close(started)
					<-ctx.Done()
					close(canceled)
					return nil, ctx.Err()
				}
				a, b := net.Pipe()
				peer = b
				return a, nil
			}
			cfg := UDPConfig{ListenAddress: "127.0.0.1:0", Target: "example.test:53"}
			if phase == "validate" {
				cfg.Validate = func(ctx context.Context, _, _ string) error {
					close(started)
					<-ctx.Done()
					close(canceled)
					return ctx.Err()
				}
			}
			s, err := StartUDP(context.Background(), cfg, dial)
			if err != nil {
				t.Fatal(err)
			}
			defer closeServer(t, s)
			udpClient(t, s.Addr()).Write([]byte("packet"))
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("worker did not start")
			}
			closeServer(t, s)
			select {
			case <-canceled:
			default:
				t.Fatal("worker did not observe cancellation")
			}
			if peer != nil {
				peer.Close()
			}
		})
	}
}

func TestUDPSessionQueueAndTerminalExpiry(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &udpSession{ctx: ctx, cancel: cancel, queue: make(chan []byte, 2), lastActive: time.Now(), table: &udpTable{cfg: UDPConfig{IdleTimeout: time.Minute}}}
	for i := 0; i < 100; i++ {
		s.offer([]byte{byte(i)})
	}
	if len(s.queue) != 2 {
		t.Fatal("queue bound changed")
	}
	s.mu.Lock()
	s.lastActive = time.Now().Add(-2 * time.Minute)
	s.mu.Unlock()
	if s.remaining() != 0 {
		t.Fatal("expired session still live")
	}
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 50; j++ {
				s.offer([]byte("late"))
				s.touch()
				s.remaining()
				s.stop()
			}
		}()
	}
	workers.Wait()
	if s.touch() || s.remaining() != 0 {
		t.Fatal("expired session was revived")
	}
	if DefaultUDPIdleTimeout < 5*time.Minute {
		t.Fatal("default UDP idle window is too short")
	}
}

func TestUDPConcurrentTrafficAndShutdown(t *testing.T) {
	t.Parallel()
	upstream := udpSocket(t)
	var echo sync.WaitGroup
	echo.Add(1)
	go func() {
		defer echo.Done()
		buffer := make([]byte, 1500)
		for {
			n, from, err := upstream.ReadFromUDPAddrPort(buffer)
			if err != nil {
				return
			}
			upstream.WriteToUDPAddrPort(buffer[:n], from)
		}
	}()
	var d net.Dialer
	s, err := StartUDP(context.Background(), UDPConfig{ListenAddress: "127.0.0.1:0", Target: upstream.LocalAddr().String(), IdleTimeout: 10 * time.Millisecond, MaxSessions: 8}, d.DialContext)
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer(t, s)
	var clients sync.WaitGroup
	for i := 0; i < 8; i++ {
		c := udpClient(t, s.Addr())
		clients.Add(1)
		go func() {
			defer clients.Done()
			for j := 0; j < 30; j++ {
				c.Write([]byte("data"))
				time.Sleep(time.Millisecond)
			}
		}()
	}
	time.Sleep(15 * time.Millisecond)
	closeServer(t, s)
	clients.Wait()
	upstream.Close()
	echo.Wait()
}
