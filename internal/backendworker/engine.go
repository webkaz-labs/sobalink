package backendworker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const MaxIO = 64 << 10
const MaxHandles = 128

// Engine is the child-side network contract. Implementations enforce exact
// overlay destinations and authenticated identity; they never use an OS fallback
// to dial application destinations. State is the versioned application snapshot.
type Engine interface {
	Start() error
	State(context.Context) (json.RawMessage, error)
	Login(context.Context) error
	Logout(context.Context) error
	DialIP(context.Context, string, netip.AddrPort) (net.Conn, error)
	Listen(string, string) (net.Listener, error)
	ListenPacket(string, string) (net.PacketConn, error)
	WhoIs(context.Context, netip.AddrPort) (string, error)
	Close() error
}
type engineRequest struct {
	Handle   uint64      `json:"handle,omitempty"`
	Scopes   []TCPPolicy `json:"scopes,omitempty"`
	Network  string      `json:"network,omitempty"`
	Address  string      `json:"address,omitempty"`
	Data     []byte      `json:"data,omitempty"`
	Size     int         `json:"size,omitempty"`
	Deadline time.Time   `json:"deadline,omitempty"`
}
type engineResponse struct {
	Handle   uint64 `json:"handle,omitempty"`
	Network  string `json:"network,omitempty"`
	Local    string `json:"local,omitempty"`
	Remote   string `json:"remote,omitempty"`
	Data     []byte `json:"data,omitempty"`
	N        int    `json:"n,omitempty"`
	EOF      bool   `json:"eof,omitempty"`
	IOError  string `json:"ioError,omitempty"`
	Timeout  bool   `json:"timeout,omitempty"`
	Identity string `json:"identity,omitempty"`
}
type engineHost struct {
	engine   Engine
	mu       sync.Mutex
	next     uint64
	handles  map[uint64]io.Closer
	closed   bool
	fallback *fallbackState
	limits   Limits
}

func (h *engineHost) close() {
	h.closeFallback()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	handles := h.handles
	h.handles = map[uint64]io.Closer{}
	h.mu.Unlock()
	for _, c := range handles {
		_ = c.Close()
	}
	_ = h.engine.Close()
}
func (h *engineHost) add(c io.Closer) (uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		_ = c.Close()
		return 0, ErrClosed
	}
	if len(h.handles) >= h.limits.Handles {
		_ = c.Close()
		return 0, ErrBusy
	}
	h.next++
	if h.next == 0 {
		_ = c.Close()
		return 0, ErrClosed
	}
	h.handles[h.next] = c
	return h.next, nil
}
func (h *engineHost) get(id uint64) io.Closer { h.mu.Lock(); defer h.mu.Unlock(); return h.handles[id] }
func (h *engineHost) remove(id uint64) error {
	h.mu.Lock()
	c := h.handles[id]
	delete(h.handles, id)
	f := h.fallback
	h.mu.Unlock()
	if f != nil {
		f.mu.Lock()
		delete(f.handles, id)
		f.mu.Unlock()
	}
	if c == nil {
		return nil
	}
	return c.Close()
}
func (h *engineHost) handle(ctx context.Context, method string, raw json.RawMessage) (json.RawMessage, error) {
	var q engineRequest
	if e := json.Unmarshal(raw, &q); e != nil {
		return nil, ErrProtocol
	}
	if strings.HasPrefix(method, "fallback-") {
		return h.fallbackHandle(ctx, method, q)
	}
	var out engineResponse
	var err error
	switch method {
	case "state":
		return h.engine.State(ctx)
	case "login":
		err = h.engine.Login(ctx)
	case "logout":
		err = h.engine.Logout(ctx)
	case "whois":
		var ap netip.AddrPort
		ap, err = netip.ParseAddrPort(q.Address)
		if err == nil {
			out.Identity, err = h.engine.WhoIs(ctx, ap)
		}
	case "dial":
		if q.Network != "tcp" && q.Network != "udp" {
			return nil, ErrProtocol
		}
		ap, e := netip.ParseAddrPort(q.Address)
		if e != nil {
			return nil, e
		}
		conn, e := h.engine.DialIP(ctx, q.Network, ap)
		if e != nil {
			return nil, e
		}
		out.Handle, err = h.add(conn)
		if err == nil {
			out.Network = q.Network
			out.Local = conn.LocalAddr().String()
			out.Remote = conn.RemoteAddr().String()
		}
	case "listen":
		if q.Network != "tcp" {
			return nil, ErrProtocol
		}
		l, e := h.engine.Listen(q.Network, q.Address)
		if e != nil {
			return nil, e
		}
		out.Handle, err = h.add(l)
		if err == nil {
			out.Network = q.Network
			out.Local = l.Addr().String()
		}
	case "listen-packet":
		if q.Network != "udp" {
			return nil, ErrProtocol
		}
		p, e := h.engine.ListenPacket(q.Network, q.Address)
		if e != nil {
			return nil, e
		}
		out.Handle, err = h.add(p)
		if err == nil {
			out.Network = q.Network
			out.Local = p.LocalAddr().String()
		}
	case "accept":
		l, ok := h.get(q.Handle).(net.Listener)
		if !ok {
			return nil, ErrClosed
		}
		conn, e := l.Accept()
		if e != nil {
			return nil, e
		}
		out.Handle, err = h.add(conn)
		if err == nil {
			out.Network = "tcp"
			out.Local = conn.LocalAddr().String()
			out.Remote = conn.RemoteAddr().String()
		}
	case "read":
		c, ok := h.get(q.Handle).(net.Conn)
		if !ok {
			return nil, ErrClosed
		}
		if q.Size < 1 || q.Size > MaxIO {
			return nil, ErrProtocol
		}
		out.Data = make([]byte, q.Size)
		out.N, err = c.Read(out.Data)
		out.Data = out.Data[:out.N]
		if errors.Is(err, io.EOF) {
			out.EOF = true
			err = nil
		}
	case "write":
		c, ok := h.get(q.Handle).(net.Conn)
		if !ok {
			return nil, ErrClosed
		}
		if len(q.Data) > MaxIO {
			return nil, ErrProtocol
		}
		out.N, err = c.Write(q.Data)
	case "read-packet":
		c, ok := h.get(q.Handle).(net.PacketConn)
		if !ok {
			return nil, ErrClosed
		}
		if q.Size < 1 || q.Size > MaxIO {
			return nil, ErrProtocol
		}
		out.Data = make([]byte, q.Size)
		var remote net.Addr
		out.N, remote, err = c.ReadFrom(out.Data)
		out.Data = out.Data[:out.N]
		if remote != nil {
			out.Remote = remote.String()
		}
	case "write-packet":
		c, ok := h.get(q.Handle).(net.PacketConn)
		if !ok {
			return nil, ErrClosed
		}
		if len(q.Data) > MaxIO {
			return nil, ErrProtocol
		}
		ap, e := netip.ParseAddrPort(q.Address)
		if e != nil {
			return nil, e
		}
		out.N, err = c.WriteTo(q.Data, net.UDPAddrFromAddrPort(ap))
	case "deadline", "read-deadline", "write-deadline":
		d, ok := h.get(q.Handle).(interface {
			SetDeadline(time.Time) error
			SetReadDeadline(time.Time) error
			SetWriteDeadline(time.Time) error
		})
		if !ok {
			return nil, ErrClosed
		}
		switch method {
		case "deadline":
			err = d.SetDeadline(q.Deadline)
		case "read-deadline":
			err = d.SetReadDeadline(q.Deadline)
		case "write-deadline":
			err = d.SetWriteDeadline(q.Deadline)
		}
	case "close-write":
		c, ok := h.get(q.Handle).(interface{ CloseWrite() error })
		if !ok {
			return nil, ErrProtocol
		}
		err = c.CloseWrite()
	case "close":
		err = h.remove(q.Handle)
	default:
		return nil, ErrProtocol
	}
	if err != nil {
		switch method {
		case "read", "write", "read-packet", "write-packet":
			out.IOError = err.Error()
			if len(out.IOError) > 1024 {
				out.IOError = "worker I/O failed"
			}
			var ne net.Error
			if errors.As(err, &ne) {
				out.Timeout = ne.Timeout()
			}
		default:
			return nil, err
		}
	}
	return json.Marshal(out)
}

// ServeEngine owns exactly one engine per process. Owner-pipe failure closes all
// listeners and flows before return. No other engine may start in this process.
func ServeEngine(ctx context.Context, in io.ReadCloser, out io.WriteCloser, e Engine, selected ...Limits) error {
	limits := DefaultLimits()
	if len(selected) > 0 {
		limits = selected[0]
	}
	if e := limits.Validate(); e != nil {
		return e
	}
	if e == nil {
		return ErrProtocol
	}
	h := &engineHost{engine: e, handles: map[uint64]io.Closer{}, limits: limits}
	defer h.close()
	if err := e.Start(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			h.close()
		case <-done:
		}
	}()
	var watch sync.Once
	return Serve(ctx, in, out, func(ctx context.Context, m string, b json.RawMessage) (json.RawMessage, error) {
		watch.Do(func() { go func() { <-ctx.Done(); h.close() }() })
		return h.handle(ctx, m, b)
	}, limits)
}

type RemoteEngine struct {
	client *Client
	ctx    context.Context
}

func NewRemoteEngine(ctx context.Context, in io.ReadCloser, out io.WriteCloser, selected ...Limits) *RemoteEngine {
	return &RemoteEngine{client: NewClient(in, out, selected...), ctx: ctx}
}
func (e *RemoteEngine) Start() error { return nil }
func (e *RemoteEngine) State(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	err := e.client.Call(ctx, "state", engineRequest{}, &out)
	return out, err
}
func (e *RemoteEngine) Login(ctx context.Context) error {
	return e.client.Call(ctx, "login", engineRequest{}, nil)
}
func (e *RemoteEngine) Logout(ctx context.Context) error {
	return e.client.Call(ctx, "logout", engineRequest{}, nil)
}
func (e *RemoteEngine) WhoIs(ctx context.Context, ap netip.AddrPort) (string, error) {
	var out engineResponse
	err := e.client.Call(ctx, "whois", engineRequest{Address: ap.String()}, &out)
	return out.Identity, err
}
func (e *RemoteEngine) DialIP(ctx context.Context, network string, ap netip.AddrPort) (net.Conn, error) {
	var out engineResponse
	err := e.client.Call(ctx, "dial", engineRequest{Network: network, Address: ap.String()}, &out)
	if err != nil {
		return nil, err
	}
	if network == "udp" {
		return &remotePacket{remoteConn: *e.conn(out)}, nil
	}
	return e.conn(out), nil
}
func (e *RemoteEngine) Listen(network, address string) (net.Listener, error) {
	var out engineResponse
	err := e.client.Call(e.ctx, "listen", engineRequest{Network: network, Address: address}, &out)
	if err != nil {
		return nil, err
	}
	return &remoteListener{engine: e, id: out.Handle, addr: addressValue{network, out.Local}}, nil
}
func (e *RemoteEngine) ListenPacket(network, address string) (net.PacketConn, error) {
	var out engineResponse
	err := e.client.Call(e.ctx, "listen-packet", engineRequest{Network: network, Address: address}, &out)
	if err != nil {
		return nil, err
	}
	return &remotePacket{remoteConn: *e.conn(out)}, nil
}
func (e *RemoteEngine) Close() error { return e.client.Close() }
func (e *RemoteEngine) conn(out engineResponse) *remoteConn {
	return &remoteConn{engine: e, id: out.Handle, local: addressValue{out.Network, out.Local}, remote: addressValue{out.Network, out.Remote}}
}

type addressValue struct{ network, address string }

func (a addressValue) Network() string { return a.network }
func (a addressValue) String() string  { return a.address }

type remoteConn struct {
	engine        *RemoteEngine
	id            uint64
	local, remote net.Addr
}

func (c *remoteConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n := len(p)
	if n > c.engine.client.limits.ioBytes() {
		n = c.engine.client.limits.ioBytes()
	}
	var out engineResponse
	if err := c.engine.client.Call(c.engine.ctx, "read", engineRequest{Handle: c.id, Size: n}, &out); err != nil {
		return 0, err
	}
	if out.N != len(out.Data) || out.N > n {
		return 0, ErrProtocol
	}
	copy(p, out.Data)
	if out.IOError != "" {
		return out.N, remoteIOError{out.IOError, out.Timeout}
	}
	if out.EOF {
		return out.N, io.EOF
	}
	return out.N, nil
}
func (c *remoteConn) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > c.engine.client.limits.ioBytes() {
			n = c.engine.client.limits.ioBytes()
		}
		var out engineResponse
		if err := c.engine.client.Call(c.engine.ctx, "write", engineRequest{Handle: c.id, Data: p[:n]}, &out); err != nil {
			return total, err
		}
		if out.N < 0 || out.N > n {
			return total, ErrProtocol
		}
		total += out.N
		if out.IOError != "" {
			return total, remoteIOError{out.IOError, out.Timeout}
		}
		p = p[out.N:]
		if out.N != n {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
func (c *remoteConn) Close() error {
	return c.engine.client.Call(c.engine.ctx, "close", engineRequest{Handle: c.id}, nil)
}
func (c *remoteConn) LocalAddr() net.Addr  { return c.local }
func (c *remoteConn) RemoteAddr() net.Addr { return c.remote }
func (c *remoteConn) SetDeadline(t time.Time) error {
	return c.engine.client.Call(c.engine.ctx, "deadline", engineRequest{Handle: c.id, Deadline: t}, nil)
}
func (c *remoteConn) SetReadDeadline(t time.Time) error {
	return c.engine.client.Call(c.engine.ctx, "read-deadline", engineRequest{Handle: c.id, Deadline: t}, nil)
}
func (c *remoteConn) SetWriteDeadline(t time.Time) error {
	return c.engine.client.Call(c.engine.ctx, "write-deadline", engineRequest{Handle: c.id, Deadline: t}, nil)
}

type remoteListener struct {
	engine *RemoteEngine
	id     uint64
	addr   net.Addr
}

func (l *remoteListener) Accept() (net.Conn, error) {
	var out engineResponse
	err := l.engine.client.Call(l.engine.ctx, "accept", engineRequest{Handle: l.id}, &out)
	if err != nil {
		return nil, err
	}
	return l.engine.conn(out), nil
}
func (l *remoteListener) Close() error {
	return l.engine.client.Call(l.engine.ctx, "close", engineRequest{Handle: l.id}, nil)
}
func (l *remoteListener) Addr() net.Addr { return l.addr }

type remotePacket struct{ remoteConn }

func (c *remotePacket) ReadFrom(p []byte) (int, net.Addr, error) {
	if len(p) == 0 {
		return 0, nil, ErrProtocol
	}
	n := len(p)
	if n > MaxIO {
		n = MaxIO
	}
	var out engineResponse
	err := c.engine.client.Call(c.engine.ctx, "read-packet", engineRequest{Handle: c.id, Size: n}, &out)
	if err != nil {
		return 0, nil, err
	}
	if out.N != len(out.Data) || out.N > n {
		return 0, nil, ErrProtocol
	}
	ap, err := netip.ParseAddrPort(out.Remote)
	if err != nil {
		return 0, nil, ErrProtocol
	}
	copy(p, out.Data)
	if out.IOError != "" {
		return out.N, net.UDPAddrFromAddrPort(ap), remoteIOError{out.IOError, out.Timeout}
	}
	return out.N, net.UDPAddrFromAddrPort(ap), nil
}
func (c *remotePacket) WriteTo(p []byte, to net.Addr) (int, error) {
	if len(p) > 65507 || to == nil {
		return 0, ErrProtocol
	}
	var out engineResponse
	err := c.engine.client.Call(c.engine.ctx, "write-packet", engineRequest{Handle: c.id, Address: to.String(), Data: p}, &out)
	if out.N < 0 || out.N > len(p) {
		return 0, ErrProtocol
	}
	if err == nil && out.IOError != "" {
		err = remoteIOError{out.IOError, out.Timeout}
	}
	return out.N, err
}

type remoteIOError struct {
	message string
	timeout bool
}

func (e remoteIOError) Error() string   { return e.message }
func (e remoteIOError) Timeout() bool   { return e.timeout }
func (e remoteIOError) Temporary() bool { return e.timeout }

func (c *remoteConn) CloseWrite() error {
	return c.engine.client.Call(c.engine.ctx, "close-write", engineRequest{Handle: c.id}, nil)
}

func (c *remotePacket) Write(p []byte) (int, error) {
	if len(p) > 65507 {
		return 0, ErrProtocol
	}
	return c.remoteConn.Write(p)
}
