// Package backendworker implements bounded owner-pipe RPC for isolated network
// engines. It never opens a socket or interprets shell commands. The application
// owns the child process, typed method dispatch, backend grants and lifecycle.
package backendworker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const MaxFrame = 256 << 10
const MaxPending = 128

var ErrProtocol = errors.New("invalid backend worker protocol")
var ErrClosed = errors.New("backend worker closed")
var ErrBusy = errors.New("backend worker request budget exhausted")

type Message struct {
	ID       uint64          `json:"id"`
	Method   string          `json:"method,omitempty"`
	Body     json.RawMessage `json:"body,omitempty"`
	Error    string          `json:"error,omitempty"`
	Deadline time.Time       `json:"deadline,omitempty"`
}

func writeMessage(w io.Writer, m Message, maximum ...int) error {
	limit := MaxFrame
	if len(maximum) > 0 {
		limit = maximum[0]
	}
	raw, e := json.Marshal(m)
	if e != nil {
		return e
	}
	if len(raw) > limit {
		return ErrProtocol
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
	if e = writeAll(w, header[:]); e != nil {
		return e
	}
	return writeAll(w, raw)
}
func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, e := w.Write(p)
		if n < 0 || n > len(p) {
			return io.ErrShortWrite
		}
		p = p[n:]
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
func readMessage(r io.Reader, maximum ...int) (Message, error) {
	limit := MaxFrame
	if len(maximum) > 0 {
		limit = maximum[0]
	}
	var header [4]byte
	if _, e := io.ReadFull(r, header[:]); e != nil {
		return Message{}, e
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || uint64(size) > uint64(limit) {
		return Message{}, ErrProtocol
	}
	raw := make([]byte, size)
	if _, e := io.ReadFull(r, raw); e != nil {
		return Message{}, e
	}
	var m Message
	if e := json.Unmarshal(raw, &m); e != nil || m.ID == 0 {
		return Message{}, ErrProtocol
	}
	return m, nil
}

// Handler accepts only a fixed typed operation set implemented by the caller.
type Handler func(context.Context, string, json.RawMessage) (json.RawMessage, error)

func Serve(ctx context.Context, in io.ReadCloser, out io.WriteCloser, handler Handler, selected ...Limits) error {
	return serveRequests(ctx, in, out, handler, nil, nil, selected...)
}
func serveRequests(ctx context.Context, in io.ReadCloser, out io.WriteCloser, handler Handler, drop func(uint64), ownerClose func(), selected ...Limits) error {
	limits := DefaultLimits()
	if len(selected) > 0 {
		limits = selected[0]
	}
	if e := limits.Validate(); e != nil {
		return e
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer in.Close()
	defer out.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = in.Close()
			_ = out.Close()
		case <-done:
		}
	}()
	var writers, requests sync.Mutex
	active := map[uint64]context.CancelFunc{}
	var wg sync.WaitGroup
	defer wg.Wait()
	defer func() {
		cancel()
		if ownerClose != nil {
			ownerClose()
		}
	}()
	dataSlots := make(chan struct{}, limits.dataSlots())
	controlSlots := make(chan struct{}, limits.controlSlots())
	send := func(m Message) error {
		writers.Lock()
		defer writers.Unlock()
		return writeMessage(out, m, limits.FrameBytes)
	}
	for {
		m, e := readMessage(in, limits.FrameBytes)
		if e != nil {
			cancel()
			return e
		}
		if m.Method == "" || len(m.Method) > 64 || m.Error != "" {
			cancel()
			return ErrProtocol
		}
		if m.Method == "$cancel" {
			requests.Lock()
			stop := active[m.ID]
			requests.Unlock()
			if stop != nil {
				stop()
			}
			continue
		}
		if m.Method == "$drop" {
			var q struct {
				Handle uint64 `json:"handle"`
			}
			if json.Unmarshal(m.Body, &q) != nil || q.Handle == 0 {
				cancel()
				return ErrProtocol
			}
			if drop != nil {
				drop(q.Handle)
			}
			continue
		}
		slots := dataSlots
		if controlMethod(m.Method) {
			slots = controlSlots
		}
		select {
		case slots <- struct{}{}:
		default:
			if e := send(Message{ID: m.ID, Error: ErrBusy.Error()}); e != nil {
				cancel()
				return e
			}
			continue
		}
		callCtx, stop := context.WithCancel(ctx)
		if !m.Deadline.IsZero() {
			var deadlineCancel context.CancelFunc
			callCtx, deadlineCancel = context.WithDeadline(callCtx, m.Deadline)
			base := stop
			stop = func() { deadlineCancel(); base() }
		} else if boundedMethod(m.Method) {
			var deadlineCancel context.CancelFunc
			callCtx, deadlineCancel = context.WithTimeout(callCtx, 30*time.Second)
			base := stop
			stop = func() { deadlineCancel(); base() }
		}
		requests.Lock()
		if active[m.ID] != nil {
			requests.Unlock()
			stop()
			<-slots
			cancel()
			return ErrProtocol
		}
		active[m.ID] = stop
		requests.Unlock()
		wg.Add(1)
		go func(m Message, callCtx context.Context, stop context.CancelFunc, slots chan struct{}) {
			defer wg.Done()
			defer func() { requests.Lock(); delete(active, m.ID); requests.Unlock(); stop(); <-slots }()
			var body json.RawMessage
			err := callCtx.Err()
			if err == nil {
				body, err = handler(callCtx, m.Method, m.Body)
			}
			response := Message{ID: m.ID, Body: body}
			if err != nil {
				response.Body = nil
				response.Error = err.Error()
				if len(response.Error) > 1024 {
					response.Error = "backend operation failed"
				}
			}
			if e := send(response); e != nil {
				cancel()
				_ = in.Close()
				_ = out.Close()
			}
		}(m, callCtx, stop, slots)
	}
}

type result struct {
	message Message
	err     error
}
type pendingCall struct {
	id              uint64
	method          string
	body            json.RawMessage
	reply           chan result
	sent, cancelled bool
	once            sync.Once
	control         bool
}
type Client struct {
	in                          io.ReadCloser
	out                         io.WriteCloser
	writeMu                     sync.Mutex
	mu                          sync.Mutex
	pending                     map[uint64]*pendingCall
	closed                      bool
	next                        atomic.Uint64
	once                        sync.Once
	limits                      Limits
	controlPending, dataPending int
	abandonedHandle             func(string, json.RawMessage, Message) uint64
}

func NewClient(in io.ReadCloser, out io.WriteCloser, selected ...Limits) *Client {
	limits := DefaultLimits()
	if len(selected) > 0 {
		limits = selected[0]
	}
	c := &Client{in: in, out: out, pending: map[uint64]*pendingCall{}, limits: limits}
	if limits.Validate() != nil {
		c.closed = true
		_ = in.Close()
		_ = out.Close()
		return c
	}
	go c.read()
	return c
}
func deliver(p *pendingCall, r result) {
	select {
	case p.reply <- r:
	default:
	}
}
func (c *Client) release(p *pendingCall) {
	p.once.Do(func() {
		c.mu.Lock()
		delete(c.pending, p.id)
		if p.control {
			c.controlPending--
		} else {
			c.dataPending--
		}
		c.mu.Unlock()
	})
}
func (c *Client) read() {
	for {
		m, e := readMessage(c.in, c.limits.FrameBytes)
		if e != nil || m.Method != "" {
			_ = c.Close()
			return
		}
		c.mu.Lock()
		p := c.pending[m.ID]
		c.mu.Unlock()
		if p != nil {
			deliver(p, result{message: m})
		}
	}
}
func (c *Client) sendControl(m Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if e := writeMessage(c.out, m, c.limits.FrameBytes); e != nil {
		_ = c.Close()
		return e
	}
	return nil
}
func (c *Client) cancelCall(p *pendingCall) {
	c.mu.Lock()
	p.cancelled = true
	sent := p.sent
	c.mu.Unlock()
	if sent {
		go func() { _ = c.sendControl(Message{ID: p.id, Method: "$cancel"}) }()
	}
	// Retain this finite request slot until the late result has been drained and
	// any new child handle dropped. Cancellation cannot accumulate orphan work.
	go func() {
		r := <-p.reply
		if c.abandonedHandle != nil {
			if handle := c.abandonedHandle(p.method, p.body, r.message); handle != 0 {
				body, _ := json.Marshal(struct {
					Handle uint64 `json:"handle"`
				}{handle})
				_ = c.sendControl(Message{ID: p.id, Method: "$drop", Body: body})
			}
		}
		c.release(p)
	}()
}
func (c *Client) Call(ctx context.Context, method string, body, out any) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if method == "" || len(method) > 64 || method[0] == '$' {
		return ErrProtocol
	}
	raw, e := json.Marshal(body)
	if e != nil {
		return e
	}
	id := c.next.Add(1)
	if id == 0 {
		_ = c.Close()
		return ErrClosed
	}
	p := &pendingCall{id: id, method: method, body: raw, reply: make(chan result, 1), control: controlMethod(method)}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	if p.control {
		if c.controlPending >= c.limits.controlSlots() {
			c.mu.Unlock()
			return ErrBusy
		}
		c.controlPending++
	} else {
		if c.dataPending >= c.limits.dataSlots() {
			c.mu.Unlock()
			return ErrBusy
		}
		c.dataPending++
	}
	c.pending[id] = p
	c.mu.Unlock()
	var deadline time.Time
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	written := make(chan error, 1)
	go func() {
		c.writeMu.Lock()
		c.mu.Lock()
		skip := p.cancelled || c.closed
		if !skip {
			p.sent = true
		}
		c.mu.Unlock()
		if skip {
			c.writeMu.Unlock()
			deliver(p, result{err: context.Canceled})
			written <- context.Canceled
			return
		}
		err := writeMessage(c.out, Message{ID: id, Method: method, Body: raw, Deadline: deadline}, c.limits.FrameBytes)
		c.writeMu.Unlock()
		if err != nil {
			_ = c.Close()
		}
		written <- err
	}()
	select {
	case <-ctx.Done():
		c.cancelCall(p)
		return ctx.Err()
	case e = <-written:
		if e != nil {
			c.release(p)
			return e
		}
	}
	select {
	case <-ctx.Done():
		c.cancelCall(p)
		return ctx.Err()
	case r := <-p.reply:
		if r.err != nil {
			c.release(p)
			return r.err
		}
		if r.message.Error != "" {
			c.release(p)
			return errors.New(r.message.Error)
		}
		if out != nil {
			if e = json.Unmarshal(r.message.Body, out); e != nil {
				if c.abandonedHandle != nil {
					if handle := c.abandonedHandle(method, raw, r.message); handle != 0 {
						encoded, _ := json.Marshal(struct {
							Handle uint64 `json:"handle"`
						}{handle})
						_ = c.sendControl(Message{ID: id, Method: "$drop", Body: encoded})
					}
				}
				c.release(p)
				return e
			}
		}
		c.release(p)
		return nil
	}
}
func (c *Client) Close() error {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		pending := c.pending
		c.pending = map[uint64]*pendingCall{}
		c.mu.Unlock()
		_ = c.in.Close()
		_ = c.out.Close()
		for _, p := range pending {
			deliver(p, result{err: ErrClosed})
		}
	})
	return nil
}
func controlMethod(method string) bool {
	switch method {
	case "close", "close-write", "deadline", "read-deadline", "write-deadline", "fallback-scopes", "fallback-disable", "state", "whois", "connection-identity", "logout":
		return true
	}
	return false
}
func boundedMethod(method string) bool {
	switch method {
	case "state", "whois", "connection-identity", "dial", "listen", "listen-packet", "login", "logout":
		return true
	}
	return false
}
