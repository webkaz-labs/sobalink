package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Native I/O watchdogs detect stuck tests; protocol timing is checked in synctest.
const nativeTestTimeout = 15 * time.Second

func closeServer(t *testing.T, s *Server) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("server close: %v", err)
		}
	case <-time.After(nativeTestTimeout):
		t.Error("server workers did not terminate")
	}
}

func clientTCP(t *testing.T, addr net.Addr) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), nativeTestTimeout)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp4", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(nativeTestTimeout))
	return c
}

func readBytes(t *testing.T, c net.Conn, n int) []byte {
	t.Helper()
	p := make([]byte, n)
	if _, err := io.ReadFull(c, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRejectNonLoopbackListeners(t *testing.T) {
	t.Parallel()
	dial := func(context.Context, string, string) (net.Conn, error) {
		t.Error("unexpected dial")
		return nil, errors.New("unexpected")
	}
	for _, address := range []string{":0", "0.0.0.0:0", "localhost:0", "192.0.2.1:0", "127.0.0.2:0", "[::]:0", "[::ffff:127.0.0.1]:0", "127.0.0.1:-1", "127.0.0.1:65536", "127.0.0.1:http"} {
		t.Run(address, func(t *testing.T) {
			if s, err := StartTCP(context.Background(), TCPConfig{ListenAddress: address, Target: "example.test:80"}, dial); err == nil {
				s.Close()
				t.Error("TCP accepted invalid listener")
			}
			if s, err := StartSOCKS(context.Background(), SOCKSConfig{ListenAddress: address, Username: "u", Password: "p"}, dial); err == nil {
				s.Close()
				t.Error("SOCKS accepted invalid listener")
			}
			if s, err := StartUDP(context.Background(), UDPConfig{ListenAddress: address, Target: "example.test:53"}, dial); err == nil {
				s.Close()
				t.Error("UDP accepted invalid listener")
			}
		})
	}
}

func TestTCPHalfClose(t *testing.T) {
	t.Parallel()
	upstream, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstreamDone := make(chan error, 1)
	go func() {
		c, err := upstream.Accept()
		if err != nil {
			upstreamDone <- err
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(nativeTestTimeout))
		data, err := io.ReadAll(c)
		if err == nil {
			err = writeFull(c, append([]byte("reply:"), data...))
		}
		upstreamDone <- err
	}()
	var d net.Dialer
	s, err := StartTCP(context.Background(), TCPConfig{ListenAddress: "127.0.0.1:0", Target: upstream.Addr().String()}, d.DialContext)
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer(t, s)
	client := clientTCP(t, s.Addr()).(*net.TCPConn)
	if err := writeFull(client, []byte("request")); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(client)
	if err != nil || string(got) != "reply:request" {
		t.Fatalf("reply %q, err %v", got, err)
	}
	if err := <-upstreamDone; err != nil {
		t.Fatal(err)
	}
}

func TestTCPCancelClosesActiveConnections(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remoteSeen := make(chan net.Conn, 1)
	dial := func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		remoteSeen <- b
		return a, nil
	}
	s, err := StartTCP(ctx, TCPConfig{ListenAddress: "127.0.0.1:0", Target: "example.test:443"}, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer(t, s)
	client := clientTCP(t, s.Addr())
	remote := <-remoteSeen
	defer remote.Close()
	cancel()
	closeServer(t, s)
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("client remained open")
	}
	if _, err := remote.Read(make([]byte, 1)); err == nil {
		t.Fatal("remote remained open")
	}
	if c, err := net.DialTimeout("tcp4", s.Addr().String(), nativeTestTimeout); err == nil {
		c.Close()
		t.Fatal("listener remained open")
	}
}

func TestTCPDialCancellationAndTimeout(t *testing.T) {
	for _, cancelServer := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[cancelServer], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const timeout = 30 * time.Millisecond
				started := make(chan struct{})
				finished := make(chan error, 1)
				dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
					close(started)
					<-ctx.Done()
					finished <- ctx.Err()
					return nil, ctx.Err()
				}
				s, client, closed := virtualStreamServer(t, func(s *Server, c net.Conn) { serveTCP(s, c, "example.test:80", timeout, dial) })
				defer closeServer(t, s)
				defer client.Close()
				synctest.Wait()
				select {
				case <-started:
				default:
					t.Fatal("dial did not start")
				}
				time.Sleep(timeout - time.Nanosecond)
				synctest.Wait()
				assertChannelOpen(t, closed, "client closed before cancellation/deadline")
				select {
				case err := <-finished:
					t.Fatalf("dial ended early: %v", err)
				default:
				}
				want := context.DeadlineExceeded
				if cancelServer {
					want = context.Canceled
					closeServer(t, s)
				} else {
					time.Sleep(time.Nanosecond)
				}
				synctest.Wait()
				assertChannelClosed(t, closed, "client remained open after cancellation/deadline")
				select {
				case err := <-finished:
					if !errors.Is(err, want) {
						t.Fatalf("dial error %v, want %v", err, want)
					}
				default:
					t.Fatal("dial did not finish at cancellation/deadline")
				}
			})
		})
	}
}

func socksServer(t *testing.T, dial Dialer, timeout time.Duration) *Server {
	t.Helper()
	s, err := StartSOCKS(context.Background(), SOCKSConfig{ListenAddress: "127.0.0.1:0", Username: "user", Password: "secret", HandshakeTimeout: timeout}, dial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeServer(t, s) })
	return s
}

func authenticateClient(t *testing.T, c net.Conn) {
	t.Helper()
	if err := writeFull(c, []byte{5, 2, 0, 2}); err != nil {
		t.Fatal(err)
	}
	if got := readBytes(t, c, 2); string(got) != string([]byte{5, 2}) {
		t.Fatalf("method %v", got)
	}
	if err := writeFull(c, append([]byte{1, 4}, []byte("user\x06secret")...)); err != nil {
		t.Fatal(err)
	}
	if got := readBytes(t, c, 2); string(got) != string([]byte{1, 0}) {
		t.Fatalf("auth %v", got)
	}
}

func TestSOCKSRequiresAuthentication(t *testing.T) {
	t.Parallel()
	var dials atomic.Int64
	s := socksServer(t, func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("unexpected")
	}, defaultHandshakeTimeout)
	client := clientTCP(t, s.Addr())
	writeFull(client, []byte{5, 1, 0})
	if got := readBytes(t, client, 2); string(got) != string([]byte{5, 255}) {
		t.Fatalf("method %v", got)
	}
	client = clientTCP(t, s.Addr())
	writeFull(client, []byte{5, 1, 2})
	readBytes(t, client, 2)
	writeFull(client, append([]byte{1, 4}, []byte("user\x05wrong")...))
	if got := readBytes(t, client, 2); string(got) != string([]byte{1, 1}) {
		t.Fatalf("auth %v", got)
	}
	if dials.Load() != 0 {
		t.Fatal("unauthenticated remote dial")
	}
}

func TestSOCKSRejectsUnsupportedCommandBeforeReadingAddress(t *testing.T) {
	t.Parallel()
	var dials atomic.Int64
	s := socksServer(t, func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("unexpected")
	}, defaultHandshakeTimeout)
	for _, command := range []byte{2, 3, 255} {
		client := clientTCP(t, s.Addr())
		authenticateClient(t, client)
		// No destination follows: rejection must occur from this header alone.
		writeFull(client, []byte{5, command, 0, 1})
		if got := readBytes(t, client, 10); got[1] != 7 {
			t.Fatalf("command %d reply %v", command, got)
		}
		client.Close()
	}
	if dials.Load() != 0 {
		t.Fatal("unsupported command allocated a remote connection")
	}
}

func TestSOCKSConnectLoopback(t *testing.T) {
	t.Parallel()
	type target struct{ network, address string }
	dialed := make(chan target, 1)
	echoDone := make(chan struct{})
	s := socksServer(t, func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed <- target{network, address}
		a, b := net.Pipe()
		go func() { defer close(echoDone); defer b.Close(); io.Copy(b, b) }()
		return a, nil
	}, defaultHandshakeTimeout)
	client := clientTCP(t, s.Addr())
	authenticateClient(t, client)
	request := append([]byte{5, 1, 0, 3, byte(len("host.example"))}, []byte("host.example")...)
	writeFull(client, append(request, 1, 187))
	if reply := readBytes(t, client, 10); reply[1] != 0 {
		t.Fatalf("connect reply %v", reply)
	}
	if got := <-dialed; got.network != "tcp" || got.address != "host.example:443" {
		t.Fatalf("dial target %+v", got)
	}
	writeFull(client, []byte("payload"))
	if got := readBytes(t, client, 7); string(got) != "payload" {
		t.Fatalf("echo %q", got)
	}
	closeServer(t, s)
	select {
	case <-echoDone:
	case <-time.After(nativeTestTimeout):
		t.Fatal("echo did not stop")
	}
}

func TestSOCKSConnectAndClearsDeadline(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const handshake = 2 * time.Second
		echoDone := make(chan struct{})
		dialed := make(chan string, 1)
		dial := func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed <- network + " " + address
			a, b := net.Pipe()
			go func() { defer close(echoDone); defer b.Close(); io.Copy(b, b) }()
			return a, nil
		}
		cfg := SOCKSConfig{Username: "user", Password: "secret", HandshakeTimeout: handshake, DialTimeout: defaultDialTimeout}
		s, client, closed := virtualStreamServer(t, func(s *Server, c net.Conn) { serveSOCKS(s, c, cfg, dial) })
		defer closeServer(t, s)
		defer client.Close()
		authenticateClient(t, client)
		request := append([]byte{5, 1, 0, 3, byte(len("host.example"))}, []byte("host.example")...)
		if err := writeFull(client, append(request, 1, 187)); err != nil {
			t.Fatal(err)
		}
		if reply := readBytes(t, client, 10); reply[1] != 0 {
			t.Fatalf("connect reply %v", reply)
		}
		if got := <-dialed; got != "tcp host.example:443" {
			t.Fatalf("dial target %q", got)
		}
		synctest.Wait()
		time.Sleep(2 * handshake)
		synctest.Wait()
		assertChannelOpen(t, closed, "successful CONNECT kept its handshake deadline")
		if err := writeFull(client, []byte("payload")); err != nil {
			t.Fatal(err)
		}
		if got := readBytes(t, client, 7); string(got) != "payload" {
			t.Fatalf("echo %q", got)
		}
		closeServer(t, s)
		synctest.Wait()
		assertChannelClosed(t, echoDone, "echo did not stop with its server")
	})
}

func TestSOCKSHandshakeAndDialHaveHardDeadline(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"greeting", "authentication", "request", "dial"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const handshake = 2 * time.Second
				start := time.Now()
				dialStarted := make(chan time.Time, 1)
				dialDone := make(chan error, 1)
				dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
					d, _ := ctx.Deadline()
					dialStarted <- d
					<-ctx.Done()
					dialDone <- ctx.Err()
					return nil, ctx.Err()
				}
				cfg := SOCKSConfig{Username: "user", Password: "secret", HandshakeTimeout: handshake, DialTimeout: defaultDialTimeout}
				s, client, closed := virtualStreamServer(t, func(s *Server, c net.Conn) { serveSOCKS(s, c, cfg, dial) })
				defer closeServer(t, s)
				defer client.Close()
				synctest.Wait() // The original absolute handshake deadline is now installed.
				time.Sleep(handshake / 4)
				switch phase {
				case "greeting":
					writeFull(client, []byte{5})
				case "authentication":
					writeFull(client, []byte{5, 1, 2})
					readBytes(t, client, 2)
					time.Sleep(handshake / 4)
					writeFull(client, []byte{1, 10})
				case "request":
					authenticateClient(t, client)
					time.Sleep(handshake / 4)
					writeFull(client, []byte{5, 1})
				case "dial":
					authenticateClient(t, client)
					time.Sleep(handshake / 4)
					writeFull(client, []byte{5, 1, 0, 1, 192, 0, 2, 1, 0, 80})
				}
				synctest.Wait()
				if phase == "dial" {
					select {
					case deadline := <-dialStarted:
						if !deadline.Equal(start.Add(handshake)) {
							t.Fatalf("dial deadline %v, want original handshake deadline", deadline)
						}
					default:
						t.Fatal("request never reached the dial phase")
					}
				}
				remaining := start.Add(handshake).Sub(time.Now())
				if remaining <= 0 {
					t.Fatal("phase setup crossed the virtual handshake deadline")
				}
				time.Sleep(remaining - time.Nanosecond)
				synctest.Wait()
				assertChannelOpen(t, closed, "handshake ended before its original deadline")
				if phase == "dial" {
					select {
					case err := <-dialDone:
						t.Fatalf("dial ended early: %v", err)
					default:
					}
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				assertChannelClosed(t, closed, "handshake remained open at its original deadline")
				if phase == "dial" {
					select {
					case err := <-dialDone:
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Fatalf("dial cancellation %v", err)
						}
					default:
						t.Fatal("dial ignored handshake deadline")
					}
				}
			})
		})
	}
}

func TestSOCKSConfigurationRejectsEmptyAndLongCredentials(t *testing.T) {
	t.Parallel()
	dial := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unexpected") }
	for _, credentials := range [][2]string{{"", "p"}, {"u", ""}, {strings.Repeat("u", 256), "p"}, {"u", strings.Repeat("p", 256)}} {
		if s, err := StartSOCKS(context.Background(), SOCKSConfig{ListenAddress: "127.0.0.1:0", Username: credentials[0], Password: credentials[1]}, dial); err == nil {
			s.Close()
			t.Fatal("accepted invalid credentials")
		}
	}
}

func TestSOCKSInvalidRequestsDoNotDial(t *testing.T) {
	t.Parallel()
	var dials atomic.Int64
	s := socksServer(t, func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("unexpected")
	}, defaultHandshakeTimeout)
	for _, request := range [][]byte{{5, 1, 1, 1}, {4, 1, 0, 1}, {5, 1, 0, 99}, {5, 1, 0, 3, 0}, {5, 1, 0, 1, 192, 0, 2, 1, 0, 0}} {
		client := clientTCP(t, s.Addr())
		authenticateClient(t, client)
		writeFull(client, request)
		if reply := readBytes(t, client, 10); reply[1] == 0 {
			t.Fatalf("invalid request accepted: %v", request)
		}
		client.Close()
	}
	if dials.Load() != 0 {
		t.Fatal("invalid request dialed")
	}
}

func TestTimeoutBounds(t *testing.T) {
	for _, timeout := range []time.Duration{-time.Second, 11 * time.Second} {
		if _, err := normalizeTimeout(timeout); err == nil {
			t.Fatalf("accepted timeout %s", timeout)
		}
	}
	if got, err := normalizeTimeout(0); err != nil || got != 10*time.Second {
		t.Fatalf("default timeout %s: %v", got, err)
	}
}

func TestDialTrackedClosesUnusableConnections(t *testing.T) {
	for _, failure := range []string{"dial error", "canceled", "server closed", "nil connection"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := &Server{ctx: context.Background(), active: make(map[net.Conn]struct{})}
			if failure == "server closed" {
				s.closed = true
			}
			a, b := net.Pipe()
			defer b.Close()
			defer a.Close()
			closed := make(chan struct{})
			conn := &trackedConn{Conn: a, closed: closed}
			dial := func(context.Context, string, string) (net.Conn, error) {
				switch failure {
				case "dial error":
					return conn, errors.New("dial failed")
				case "canceled":
					cancel()
					return conn, nil
				case "server closed":
					return conn, nil
				default:
					return nil, nil
				}
			}
			if _, err := dialTracked(s, ctx, dial, "tcp", "example.test:443"); err == nil {
				t.Fatal("accepted unusable connection")
			}
			if failure != "nil connection" {
				select {
				case <-closed:
				default:
					t.Fatal("unusable connection leaked")
				}
			}
		})
	}
}

func TestUnexpectedListenerErrorStopsServer(t *testing.T) {
	var d net.Dialer
	s, err := StartTCP(context.Background(), TCPConfig{ListenAddress: "127.0.0.1:0", Target: "127.0.0.1:1"}, d.DialContext)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an unexpected local listener failure, rather than cancellation.
	s.listener.Close()
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("listener failure was lost")
		}
	case <-time.After(nativeTestTimeout):
		t.Fatal("listener failure did not stop server")
	}
}

func TestReadSOCKSAddressTypes(t *testing.T) {
	for _, test := range []struct {
		atyp  byte
		bytes []byte
		want  string
	}{
		{1, []byte{192, 0, 2, 1}, "192.0.2.1"},
		{4, net.ParseIP("2001:db8::1").To16(), "2001:db8::1"},
		{3, append([]byte{12}, []byte("host.example")...), "host.example"},
	} {
		host, err := readSOCKSHost(strings.NewReader(string(test.bytes)), test.atyp)
		if err != nil || host != test.want {
			t.Fatalf("host %q, want %q; err %v", host, test.want, err)
		}
		for n := 0; n < len(test.bytes); n++ {
			if _, err := readSOCKSHost(strings.NewReader(string(test.bytes[:n])), test.atyp); err == nil {
				t.Fatalf("accepted truncated address: type %d, bytes %d", test.atyp, n)
			}
		}
	}
}

// A pipe-backed listener exercises the real accept limit without assuming a
// high host file-descriptor limit (some supported desktops default to 256).
type pipeListener struct {
	queue chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.queue:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345} }
func (l *pipeListener) client(t *testing.T) net.Conn {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { b.Close() })
	l.queue <- a
	return b
}

func TestTCPAndSOCKSBoundLocalConnections(t *testing.T) {
	for _, mode := range []string{"tcp", "socks"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var handlers atomic.Int64
				listener := &pipeListener{queue: make(chan net.Conn, 1), done: make(chan struct{})}
				server := startServer(context.Background(), listener, listener.Addr(), func(s *Server) {
					acceptConnections(s, listener, func(c net.Conn) {
						handlers.Add(1)
						if mode == "tcp" {
							<-s.ctx.Done()
							return
						}
						serveSOCKS(s, c, SOCKSConfig{Username: "user", Password: "secret", HandshakeTimeout: defaultHandshakeTimeout}, func(context.Context, string, string) (net.Conn, error) {
							return nil, errors.New("unauthenticated connection must not dial")
						})
					})
				})
				defer closeServer(t, server)
				for i := 0; i < defaultTCPPerPolicy; i++ {
					listener.client(t)
				}
				synctest.Wait()
				if got := handlers.Load(); got != defaultTCPPerPolicy {
					t.Fatalf("handlers %d, want %d", got, defaultTCPPerPolicy)
				}
				overflow, closed := virtualStreamClient(t, listener)
				defer overflow.Close()
				synctest.Wait()
				assertChannelClosed(t, closed, "overflow connection was not immediately rejected")
				if _, err := overflow.Read(make([]byte, 1)); err == nil {
					t.Fatal("overflow connection stayed open")
				}
				if got := handlers.Load(); got != defaultTCPPerPolicy {
					t.Fatalf("handlers %d, want %d", got, defaultTCPPerPolicy)
				}
			})
		})
	}
}
