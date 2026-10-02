package discovery

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

const testAddress = "100.64.0.10:54543"
const testSource = "100.64.0.20:42000"

type testAddr string

func (a testAddr) Network() string { return "tcp" }
func (a testAddr) String() string  { return string(a) }

type addressedConn struct {
	net.Conn
	local, remote net.Addr
}

func (c *addressedConn) LocalAddr() net.Addr  { return c.local }
func (c *addressedConn) RemoteAddr() net.Addr { return c.remote }

type pipeListener struct {
	queue chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{queue: make(chan net.Conn, 64), done: make(chan struct{})}
}
func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case c := <-l.queue:
		return c, nil
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *pipeListener) Addr() net.Addr { return testAddr(testAddress) }
func (l *pipeListener) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" || address != testAddress {
		return nil, errors.New("unexpected endpoint")
	}
	client, server := net.Pipe()
	select {
	case <-ctx.Done():
		client.Close()
		server.Close()
		return nil, ctx.Err()
	case <-l.done:
		client.Close()
		server.Close()
		return nil, net.ErrClosed
	case l.queue <- &addressedConn{Conn: server, local: testAddr(testAddress), remote: testAddr(testSource)}:
		return &addressedConn{Conn: client, local: testAddr(testSource), remote: testAddr(testAddress)}, nil
	}
}

func serviceAt(now time.Time) Service {
	return Service{ID: "00112233445566778899aabbccddeeff", Purpose: "web", Network: "tcp", Port: 8080, ExpiresAt: now.Add(time.Hour), Application: "unverified"}
}

func runServer(t *testing.T, services Services) (*pipeListener, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	ln := newPipeListener()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, services) }()
	return ln, cancel, done
}

func finishServer(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve shutdown: %v", err)
	}
}

func TestQueryRoundTripAndFreshAuthorization(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		var allowed atomic.Bool
		allowed.Store(true)
		service := serviceAt(time.Now())
		ln, cancel, done := runServer(t, func(ctx context.Context, source netip.AddrPort) ([]Service, error) {
			calls.Add(1)
			if source.String() != testSource {
				t.Errorf("source = %v", source)
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Error("missing callback deadline")
			}
			if !allowed.Load() {
				return nil, errors.New("private authorization reason")
			}
			return []Service{service}, nil
		})
		defer finishServer(t, cancel, done)
		response, err := Query(t.Context(), ln.dial, testAddress)
		if err != nil || response.Version != Version || len(response.Services) != 1 || response.Services[0].ID != service.ID {
			t.Fatalf("query = %#v, %v", response, err)
		}
		allowed.Store(false)
		response, err = Query(t.Context(), ln.dial, testAddress)
		if !errors.Is(err, ErrUnavailable) || len(response.Services) != 0 {
			t.Fatalf("revoked query = %#v, %v", response, err)
		}
		if calls.Load() != 2 {
			t.Fatalf("authorization calls = %d", calls.Load())
		}
	})
}

func TestServerRequestsAreReadOnlyAndHostBound(t *testing.T) {
	for _, tc := range []struct {
		name, request string
		status        int
	}{
		{"valid", "GET " + Path + " HTTP/1.1\r\nHost: " + testAddress + "\r\n\r\n", 200},
		{"method", "POST " + Path + " HTTP/1.1\r\nHost: " + testAddress + "\r\nContent-Length: 0\r\n\r\n", 405},
		{"query", "GET " + Path + "?extra=1 HTTP/1.1\r\nHost: " + testAddress + "\r\n\r\n", 404},
		{"trailing slash", "GET " + Path + "/ HTTP/1.1\r\nHost: " + testAddress + "\r\n\r\n", 404},
		{"host", "GET " + Path + " HTTP/1.1\r\nHost: example.invalid\r\n\r\n", 404},
		{"proxy", "GET http://example.invalid" + Path + " HTTP/1.1\r\nHost: " + testAddress + "\r\n\r\n", 404},
		{"body", "GET " + Path + " HTTP/1.1\r\nHost: " + testAddress + "\r\nContent-Length: 1\r\n\r\nx", 400},
		{"upgrade", "GET " + Path + " HTTP/1.1\r\nHost: " + testAddress + "\r\nUpgrade: websocket\r\n\r\n", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				ln, cancel, done := runServer(t, func(context.Context, netip.AddrPort) ([]Service, error) { calls.Add(1); return nil, nil })
				defer finishServer(t, cancel, done)
				c, err := ln.dial(t.Context(), "tcp", testAddress)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				go func() { _, _ = io.WriteString(c, tc.request) }()
				response, err := http.ReadResponse(bufio.NewReader(c), nil)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != tc.status {
					t.Fatalf("status = %d, err = %v", response.StatusCode, err)
				}
				if tc.status != 200 && (len(body) != 0 || response.Header.Get(versionHeader) != "") {
					t.Fatalf("failure leaked metadata: %s %v", body, response.Header)
				}
				if calls.Load() != 1 {
					t.Fatalf("authorization calls = %d", calls.Load())
				}
				if !response.Close || response.Header.Get("Cache-Control") != "no-store" {
					t.Fatal("response reusable or cacheable")
				}
			})
		})
	}
}

func TestServerDeniesWithoutMetadata(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ln, cancel, done := runServer(t, func(context.Context, netip.AddrPort) ([]Service, error) {
			return []Service{serviceAt(time.Now())}, errors.New("secret rule and target")
		})
		defer finishServer(t, cancel, done)
		c, _ := ln.dial(t.Context(), "tcp", testAddress)
		defer c.Close()
		go func() { fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\n\r\n", Path, testAddress) }()
		response, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 403 || len(body) != 0 || response.Header.Get(versionHeader) != "" {
			t.Fatalf("failure = %#v, %q", response, body)
		}
	})
}

// respondOnce never uses a socket, resolver, proxy or real tailnet.
func respondOnce(t *testing.T, wire string, requests *atomic.Int32) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != testAddress {
			t.Errorf("unexpected dial %s %s", network, address)
		}
		requests.Add(1)
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			req, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				return
			}
			if req.Method != "GET" || req.RequestURI != Path || req.Host != testAddress || !req.Close {
				t.Error("unexpected request")
			}
			req.Body.Close()
			_, _ = io.WriteString(server, wire)
		}()
		return client, nil
	}
}

func responseWire(status int, headers, body string) string {
	return fmt.Sprintf("HTTP/1.1 %d status\r\nContent-Type: application/json\r\nContent-Length: %d\r\n%s\r\n%s", status, len(body), headers, body)
}

func TestQueryClassifiesOnlyExplicitUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		headers, body string
		want          error
	}{
		{"404", 404, "", "", ErrUnsupported},
		{"405", 405, "", "", ErrUnsupported},
		{"version header", 200, versionHeader + ": 2\r\n", `{"version":1,"services":[]}`, ErrUnsupported},
		{"version body", 200, "", `{"version":2,"services":[]}`, ErrUnsupported},
		{"forbidden", 403, "", "", ErrUnavailable},
		{"redirect", 302, "Location: http://example.invalid/\r\n", "", ErrUnavailable},
		{"server failure", 503, "", "", ErrUnavailable},
		{"bad version", 200, versionHeader + ": invalid\r\n", `{"version":1,"services":[]}`, ErrUnavailable},
		{"duplicate version header", 200, versionHeader + ": 1\r\n" + versionHeader + ": 1\r\n", `{"version":1,"services":[]}`, ErrUnavailable},
		{"gzip", 200, "Content-Encoding: gzip\r\n", `{"version":1,"services":[]}`, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var requests atomic.Int32
				_, err := Query(t.Context(), respondOnce(t, responseWire(tc.status, tc.headers, tc.body), &requests), testAddress)
				if !errors.Is(err, tc.want) {
					t.Fatalf("error = %v, want %v", err, tc.want)
				}
				if requests.Load() != 1 {
					t.Fatalf("dials = %d", requests.Load())
				}
			})
		})
	}
}

func TestQueryRejectsInvalidDocuments(t *testing.T) {
	valid := Response{Version: Version, Services: []Service{serviceAt(time.Now())}}
	encoded, _ := json.Marshal(valid)
	body := string(encoded)
	cases := map[string]string{
		"unknown":               strings.Replace(body, `"version":1`, `"version":1,"name":"private"`, 1),
		"duplicate":             strings.Replace(body, `"version":1`, `"version":1,"version":1`, 1),
		"case variant":          strings.Replace(body, `"version"`, `"Version"`, 1),
		"trailing":              body + `{}`,
		"null list":             `{"version":1,"services":null}`,
		"missing list":          `{"version":1}`,
		"missing version":       `{"services":[]}`,
		"null version":          `{"version":null,"services":[]}`,
		"zero version":          `{"version":0,"services":[]}`,
		"null service":          `{"version":1,"services":[null]}`,
		"unknown service":       strings.Replace(body, `"id":`, `"target":"127.0.0.1","id":`, 1),
		"duplicate service key": strings.Replace(body, `"port":8080`, `"port":8080,"port":22`, 1),
		"missing purpose":       strings.Replace(body, `"purpose":"web",`, "", 1),
		"null application":      strings.Replace(body, `"application":"unverified"`, `"application":null`, 1),
		"truncated":             body[:len(body)-1],
		"too large":             `{"version":1,"services":[]}` + strings.Repeat(" ", maxResponseBytes),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var requests atomic.Int32
				_, err := Query(t.Context(), respondOnce(t, responseWire(200, "", body), &requests), testAddress)
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("error = %v", err)
				}
			})
		})
	}
}

func TestServiceValidation(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		edit func(*Service)
	}{
		{"short ID", func(s *Service) { s.ID = "abcd" }},
		{"uppercase ID", func(s *Service) { s.ID = strings.ToUpper(s.ID) }},
		{"control ID", func(s *Service) { s.ID = "\n" + s.ID[1:] }},
		{"purpose", func(s *Service) { s.Purpose = "personal" }},
		{"network", func(s *Service) { s.Network = "http" }},
		{"zero port", func(s *Service) { s.Port = 0 }},
		{"large port", func(s *Service) { s.Port = 65536 }},
		{"expired", func(s *Service) { s.ExpiresAt = now }},
		{"long expiry", func(s *Service) { s.ExpiresAt = now.Add(MaxExpiry + time.Nanosecond) }},
		{"application", func(s *Service) { s.Application = "verified" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := serviceAt(now)
			tc.edit(&service)
			if validate(Response{Version: Version, Services: []Service{service}}, now) == nil {
				t.Fatal("invalid service accepted")
			}
		})
	}
	for _, purpose := range []string{"web", "ssh", "db", "ai", "custom", "rustdesk"} {
		for _, network := range []string{"tcp", "udp"} {
			service := serviceAt(now)
			service.Purpose = purpose
			service.Network = network
			if err := validate(Response{Version: Version, Services: []Service{service}}, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	if validate(Response{Version: Version, Services: []Service{serviceAt(now), serviceAt(now)}}, now) == nil {
		t.Fatal("duplicate ID accepted")
	}
	if validate(Response{Version: Version, Services: make([]Service, MaxServices+1)}, now) == nil {
		t.Fatal("excess services accepted")
	}
}

func TestQueryNeverResolvesOrRewritesDestination(t *testing.T) {
	for _, address := range []string{"example.invalid:54543", "http://100.64.0.10:54543", "100.64.0.10:80", "100.64.0.10:54543/path", "0.0.0.0:54543", "127.0.0.1:54543", "192.168.1.1:54543", "1.1.1.1:54543", "100.100.100.100:54543", "[fe80::1%zone]:54543", "100.64.0.10:54543\r\nHost: example.invalid"} {
		called := false
		_, err := Query(t.Context(), func(context.Context, string, string) (net.Conn, error) {
			called = true
			return nil, errors.New("unexpected")
		}, address)
		if called || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("address %q: dial=%v error=%v", address, called, err)
		}
	}
	_, err := Query(t.Context(), func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("connection refused") }, testAddress)
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrUnsupported) {
		t.Fatalf("refusal = %v", err)
	}
}

func TestQueryDeadlineAndCancellation(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client, server := net.Pipe()
			defer server.Close()
			if cancelEarly {
				go func() { time.Sleep(100 * time.Millisecond); cancel() }()
			}
			started := time.Now()
			_, err := Query(ctx, func(context.Context, string, string) (net.Conn, error) { return client, nil }, testAddress)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error = %v", err)
			}
			want := queryTimeout
			if cancelEarly {
				want = 100 * time.Millisecond
			}
			if elapsed := time.Since(started); elapsed != want {
				t.Fatalf("deadline = %v, want %v", elapsed, want)
			}
		})
	}
}

func TestServeCancellationClosesActiveConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ln, cancel, done := runServer(t, func(context.Context, netip.AddrPort) ([]Service, error) { return nil, nil })
		c, _ := ln.dial(t.Context(), "tcp", testAddress)
		defer c.Close()
		synctest.Wait()
		finishServer(t, cancel, done)
		var b [1]byte
		if _, err := c.Read(b[:]); err == nil {
			t.Fatal("active connection remains open")
		}
	})
}

func TestServeHeaderLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		ln, cancel, done := runServer(t, func(context.Context, netip.AddrPort) ([]Service, error) { calls.Add(1); return nil, nil })
		defer finishServer(t, cancel, done)
		c, _ := ln.dial(t.Context(), "tcp", testAddress)
		defer c.Close()
		go func() {
			fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nX-Large: %s\r\n\r\n", Path, testAddress, strings.Repeat("x", maxHeaderBytes))
		}()
		wire, _ := io.ReadAll(c)
		if strings.Contains(string(wire), "200 OK") || calls.Load() != 0 {
			t.Fatalf("oversized header accepted: %s", wire)
		}
	})
}

func TestConnectionLimitAndSlotRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := newPipeListener()
		defer base.Close()
		ln := &boundedListener{Listener: base, slots: make(chan struct{}, maxConnections)}
		var accepted []net.Conn
		var clients []net.Conn
		defer func() {
			for _, c := range accepted {
				c.Close()
			}
			for _, c := range clients {
				c.Close()
			}
		}()
		for range maxConnections {
			client, _ := base.dial(t.Context(), "tcp", testAddress)
			clients = append(clients, client)
			c, err := ln.Accept()
			if err != nil {
				t.Fatal(err)
			}
			accepted = append(accepted, c)
		}
		extra, _ := base.dial(t.Context(), "tcp", testAddress)
		defer extra.Close()
		next := make(chan net.Conn, 1)
		go func() { c, _ := ln.Accept(); next <- c }()
		var b [1]byte
		if _, err := extra.Read(b[:]); err == nil {
			t.Fatal("excess connection admitted")
		}
		accepted[0].Close()
		client, _ := base.dial(t.Context(), "tcp", testAddress)
		clients = append(clients, client)
		accepted = append(accepted, <-next)
		if len(ln.slots) != maxConnections {
			t.Fatalf("slots = %d", len(ln.slots))
		}
	})
}

func TestServerRejectsInvalidServiceResults(t *testing.T) {
	for _, kind := range []string{"expired", "too many", "duplicate", "invalid application"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				service := serviceAt(time.Now())
				list := []Service{service}
				switch kind {
				case "expired":
					list[0].ExpiresAt = time.Now()
				case "too many":
					list = make([]Service, MaxServices+1)
				case "duplicate":
					list = append(list, service)
				case "invalid application":
					list[0].Application = "verified"
				}
				ln, cancel, done := runServer(t, func(context.Context, netip.AddrPort) ([]Service, error) { return list, nil })
				defer finishServer(t, cancel, done)
				response, err := Query(t.Context(), ln.dial, testAddress)
				if !errors.Is(err, ErrUnavailable) || len(response.Services) != 0 {
					t.Fatalf("invalid result = %#v %v", response, err)
				}
			})
		})
	}
}

func TestServerReadAndAuthorizationDeadlines(t *testing.T) {
	for _, request := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			var calls atomic.Int32
			ln, cancel, done := runServer(t, func(ctx context.Context, _ netip.AddrPort) ([]Service, error) {
				calls.Add(1)
				<-ctx.Done()
				return nil, ctx.Err()
			})
			defer finishServer(t, cancel, done)
			c, _ := ln.dial(t.Context(), "tcp", testAddress)
			defer c.Close()
			started := time.Now()
			if request {
				go func() { fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\n\r\n", Path, testAddress) }()
			}
			body, _ := io.ReadAll(c)
			want := serverTimeout
			if request {
				want = queryTimeout
			}
			if elapsed := time.Since(started); elapsed != want {
				t.Fatalf("deadline = %v, want %v", elapsed, want)
			}
			if request && (!strings.Contains(string(body), "403 Forbidden") || calls.Load() != 1) {
				t.Fatalf("authorization timeout = %s", body)
			}
			if !request && calls.Load() != 0 {
				t.Fatal("incomplete request reached authorization")
			}
		})
	}
}

func TestServerDoesNotReuseConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		ln, cancel, done := runServer(t, func(context.Context, netip.AddrPort) ([]Service, error) { calls.Add(1); return nil, nil })
		defer finishServer(t, cancel, done)
		c, _ := ln.dial(t.Context(), "tcp", testAddress)
		defer c.Close()
		request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\n\r\n", Path, testAddress)
		go func() { _, _ = io.WriteString(c, request+request) }()
		body, _ := io.ReadAll(c)
		if strings.Count(string(body), "200 OK") != 1 || calls.Load() != 1 {
			t.Fatalf("connection reused: %s", body)
		}
	})
}

type blockedCloseListener struct {
	*pipeListener
	unblock chan struct{}
}

func (l *blockedCloseListener) Close() error {
	<-l.unblock
	return l.pipeListener.Close()
}

func TestServeBoundsNativeCloseWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		ln := &blockedCloseListener{pipeListener: newPipeListener(), unblock: make(chan struct{})}
		done := make(chan error, 1)
		go func() {
			done <- Serve(ctx, ln, func(context.Context, netip.AddrPort) ([]Service, error) { return nil, nil })
		}()
		synctest.Wait()
		started := time.Now()
		cancel()
		err := <-done
		close(ln.unblock)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("blocked close = %v", err)
		}
		if elapsed := time.Since(started); elapsed != serverTimeout {
			t.Fatalf("close wait = %v, want %v", elapsed, serverTimeout)
		}
		synctest.Wait()
	})
}
