// Package discovery transports a minimal, read-only service catalogue. Callers
// supply a tsnet listener and a policy-checked, identity-pinned netstack dialer;
// this package never creates listeners, resolves names or chooses a route.
package discovery

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

const (
	Version = 1
	Port    = 54543
	Path    = "/.well-known/tsnet-bridge/services/v1"

	MaxServices      = 64
	MaxExpiry        = 24 * time.Hour
	maxConnections   = 16
	maxHeaderBytes   = 4 << 10
	maxResponseBytes = 64 << 10
	queryTimeout     = 2 * time.Second
	serverTimeout    = 3 * time.Second
	versionHeader    = "X-Tsnet-Bridge-Discovery-Version"
)

// ErrUnsupported means an explicit endpoint or version response was received.
// A timeout, refusal, authorization failure or malformed response cannot establish
// that a peer runs an older version and instead returns ErrUnavailable.
var (
	ErrUnsupported = errors.New("service discovery unsupported")
	ErrUnavailable = errors.New("service discovery unavailable")
)

// Service deliberately excludes names, local targets, owners and allowed peers.
// ID is an opaque 16-byte identifier encoded as 32 lowercase hexadecimal digits.
// Application reports no application-level verification.
type Service struct {
	ID          string    `json:"id"`
	Purpose     string    `json:"purpose"`
	Network     string    `json:"network"`
	Port        int       `json:"port"`
	ExpiresAt   time.Time `json:"expires_at"`
	Application string    `json:"application"`
}

type Response struct {
	Version  int       `json:"version"`
	Services []Service `json:"services"`
}

// Services must freshly authenticate source against its current pinned identity
// and live sharing grant on every call, and return only that source's services.
// It must honor context cancellation. Its errors are never transmitted or logged.
type Services func(context.Context, netip.AddrPort) ([]Service, error)

// Serve blocks until cancellation or a listener failure. It takes ownership of
// ln, which must be a listener on the embedded node's explicit tailnet address.
// Cancellation closes connections rather than draining or reusing them. Both
// transport deadlines and the wait for native closure are bounded.
func Serve(ctx context.Context, ln net.Listener, services Services) error {
	if ctx == nil || ln == nil || services == nil {
		return ErrUnavailable
	}
	srv := &http.Server{
		Handler:           handler(services),
		ReadHeaderTimeout: serverTimeout,
		ReadTimeout:       serverTimeout,
		WriteTimeout:      serverTimeout,
		IdleTimeout:       serverTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          log.New(io.Discard, "", 0),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	srv.SetKeepAlivesEnabled(false)
	limited := &boundedListener{Listener: ln, slots: make(chan struct{}, maxConnections)}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(limited) }()
	var result error
	select {
	case err := <-served:
		served = nil
		if !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
			result = ErrUnavailable
		}
	case <-ctx.Done():
	}
	closed := make(chan error, 1)
	go func() { closed <- srv.Close() }()
	timer := time.NewTimer(serverTimeout)
	defer timer.Stop()
	for closed != nil || served != nil {
		select {
		case err := <-closed:
			if err != nil {
				result = ErrUnavailable
			}
			closed = nil
		case <-served:
			served = nil
		case <-timer.C:
			return ErrUnavailable
		}
	}
	return result
}

func handler(services Services) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		w.Header().Set("Cache-Control", "no-store")
		r.Close = true
		fail := func(status int) {
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(status)
		}
		source, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil || source.Addr().Zone() != "" || source.Port() == 0 {
			fail(http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
		defer cancel()
		// Authenticate even malformed requests; no untrusted header can supply
		// identity or select a different source or upstream destination.
		list, err := services(ctx, source)
		if err != nil || ctx.Err() != nil {
			fail(http.StatusForbidden)
			return
		}
		local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		host, hostErr := netip.ParseAddrPort(r.Host)
		var localAddress netip.AddrPort
		if ok && local != nil {
			localAddress, err = netip.ParseAddrPort(local.String())
		}
		if !ok || err != nil || hostErr != nil || host != localAddress ||
			r.URL.IsAbs() || r.RequestURI != Path || r.URL.RawQuery != "" || r.URL.ForceQuery {
			fail(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet {
			fail(http.StatusMethodNotAllowed)
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.Header.Get("Upgrade") != "" {
			fail(http.StatusBadRequest)
			return
		}
		if list == nil {
			list = []Service{}
		}
		response := Response{Version: Version, Services: list}
		if validate(response, time.Now()) != nil {
			fail(http.StatusServiceUnavailable)
			return
		}
		body, err := json.Marshal(response)
		if err != nil || len(body) > maxResponseBytes-maxHeaderBytes {
			fail(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set(versionHeader, strconv.Itoa(Version))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}

// Query performs one request to a numeric discovery endpoint, using only dial.
// The caller must validate and pin the peer before and during that netstack dial.
// There is no default transport, proxy, DNS lookup, redirect or connection reuse.
func Query(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), address string) (Response, error) {
	if ctx == nil || dial == nil {
		return Response{}, ErrUnavailable
	}
	endpoint, err := netip.ParseAddrPort(address)
	if err != nil || endpoint.Port() != Port || !config.TailnetIP(endpoint.Addr()) {
		return Response{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	conn, err := dial(ctx, "tcp", endpoint.String())
	if err != nil || conn == nil {
		if conn != nil {
			_ = conn.Close()
		}
		return Response{}, ErrUnavailable
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil || ctx.Err() != nil {
		return Response{}, ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+endpoint.String()+Path, nil)
	if err != nil {
		return Response{}, ErrUnavailable
	}
	req.Close = true
	req.Header.Set("Accept", "application/json")
	if err = req.Write(conn); err != nil {
		return Response{}, ErrUnavailable
	}
	// Bound the entire response, including headers and chunk framing. HTTP is
	// parsed directly so response handling can never trigger another dial.
	limited := &io.LimitedReader{R: conn, N: maxResponseBytes + 1}
	resp, err := http.ReadResponse(bufio.NewReader(limited), req)
	if err != nil {
		return Response{}, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return Response{}, ErrUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, ErrUnavailable
	}
	if versions := resp.Header.Values(versionHeader); len(versions) != 0 {
		if len(versions) != 1 {
			return Response{}, ErrUnavailable
		}
		v, err := strconv.Atoi(versions[0])
		if err != nil || v < 1 {
			return Response{}, ErrUnavailable
		}
		if v != Version {
			return Response{}, ErrUnsupported
		}
	}
	if resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Content-Encoding") != "" || resp.ContentLength > maxResponseBytes {
		return Response{}, ErrUnavailable
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil || limited.N <= 0 || len(resp.Trailer) != 0 || ctx.Err() != nil {
		return Response{}, ErrUnavailable
	}
	var response Response
	if err = decode(body, &response); err != nil {
		return Response{}, ErrUnavailable
	}
	if response.Version > 0 && response.Version != Version {
		return Response{}, ErrUnsupported
	}
	if err = validate(response, time.Now()); err != nil {
		return Response{}, ErrUnavailable
	}
	return response, nil
}

func validate(response Response, now time.Time) error {
	if response.Version != Version || response.Services == nil || len(response.Services) > MaxServices {
		return ErrUnavailable
	}
	seen := make(map[string]bool, len(response.Services))
	for _, service := range response.Services {
		if len(service.ID) != 32 || seen[service.ID] {
			return ErrUnavailable
		}
		for _, r := range service.ID {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return ErrUnavailable
			}
		}
		seen[service.ID] = true
		switch service.Purpose {
		case "web", "ssh", "db", "ai", "custom", "rustdesk":
		default:
			return ErrUnavailable
		}
		if (service.Network != "tcp" && service.Network != "udp") || service.Port < 1 || service.Port > 65535 ||
			service.Application != "unverified" || !service.ExpiresAt.After(now) || service.ExpiresAt.After(now.Add(MaxExpiry)) {
			return ErrUnavailable
		}
	}
	return nil
}

// decode rejects unknown, duplicate and case-variant keys, null/missing fields,
// and any non-whitespace content after the single JSON object.
func decode(body []byte, response *Response) error {
	var outer map[string]json.RawMessage
	if err := strictObject(body, []string{"version", "services"}, &outer); err != nil {
		return err
	}
	var services []json.RawMessage
	if err := json.Unmarshal(outer["services"], &services); err != nil || services == nil || len(services) > MaxServices {
		return ErrUnavailable
	}
	for _, raw := range services {
		if err := strictObject(raw, []string{"id", "purpose", "network", "port", "expires_at", "application"}, nil); err != nil {
			return err
		}
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	return d.Decode(response)
}

func strictObject(body []byte, fields []string, result *map[string]json.RawMessage) error {
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return ErrUnavailable
	}
	values := make(map[string]json.RawMessage, len(fields))
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || values[key] != nil {
			return ErrUnavailable
		}
		allowed := false
		for _, field := range fields {
			allowed = allowed || key == field
		}
		var value json.RawMessage
		if !allowed || d.Decode(&value) != nil || bytes.Equal(value, []byte("null")) {
			return ErrUnavailable
		}
		values[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || len(values) != len(fields) {
		return ErrUnavailable
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return ErrUnavailable
	}
	if result != nil {
		*result = values
	}
	return nil
}

type boundedListener struct {
	net.Listener
	slots chan struct{}
}

func (l *boundedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &boundedConn{Conn: conn, release: func() { <-l.slots }}, nil
		default:
			_ = conn.Close()
		}
	}
}

type boundedConn struct {
	net.Conn
	release     func()
	once        sync.Once
	closeErr    error
	headerBytes int
	headerEnd   uint32
	headersDone bool
}

func (c *boundedConn) Close() error {
	c.once.Do(func() {
		c.closeErr = c.Conn.Close()
		c.release()
	})
	return c.closeErr
}

// net/http reserves an extra read buffer above MaxHeaderBytes. Enforce the
// actual first-request wire-header limit as well; connections are never reused.
func (c *boundedConn) Read(p []byte) (int, error) {
	if !c.headersDone {
		remaining := maxHeaderBytes - c.headerBytes
		if remaining == 0 {
			return 0, io.ErrUnexpectedEOF
		}
		if len(p) > remaining {
			p = p[:remaining]
		}
	}
	n, err := c.Conn.Read(p)
	if !c.headersDone {
		for _, b := range p[:n] {
			c.headerBytes++
			c.headerEnd = c.headerEnd<<8 | uint32(b)
			if c.headerEnd == 0x0d0a0d0a {
				c.headersDone = true
				break
			}
		}
	}
	return n, err
}
