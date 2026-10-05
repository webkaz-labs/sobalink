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
)

const MaxFrame = 256 << 10
const MaxPending = 128

var ErrProtocol = errors.New("invalid backend worker protocol")
var ErrClosed = errors.New("backend worker closed")
var ErrBusy = errors.New("backend worker request budget exhausted")

type Message struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
	Error  string          `json:"error,omitempty"`
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
// Requests carry no code, filesystem paths or generic management operations.
type Handler func(context.Context, string, json.RawMessage) (json.RawMessage, error)

// Serve supports concurrent blocking stream reads while maintaining a finite
// request budget. Cancellation of the owner closes all worker-owned resources
// through the caller's lifecycle; a transport error is terminal for this worker.
func Serve(ctx context.Context, in io.ReadCloser, out io.WriteCloser, handler Handler, selected ...Limits) error {
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
	var writers sync.Mutex
	var wg sync.WaitGroup
	defer wg.Wait()
	dataSlots := make(chan struct{}, limits.Requests)
	controlSlots := make(chan struct{}, 8)
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
		slots := dataSlots
		if controlMethod(m.Method) {
			slots = controlSlots
		}
		select {
		case slots <- struct{}{}:
		default:
			cancel()
			return ErrBusy
		}
		wg.Add(1)
		go func(m Message, slots chan struct{}) {
			defer wg.Done()
			defer func() { <-slots }()
			body, e := handler(ctx, m.Method, m.Body)
			response := Message{ID: m.ID, Body: body}
			if e != nil {
				response.Body = nil
				response.Error = e.Error()
				if len(response.Error) > 1024 {
					response.Error = "backend operation failed"
				}
			}
			writers.Lock()
			e = writeMessage(out, response, limits.FrameBytes)
			writers.Unlock()
			if e != nil {
				cancel()
				_ = in.Close()
				_ = out.Close()
			}
		}(m, slots)
	}
}

type result struct {
	message Message
	err     error
}
type Client struct {
	in      io.ReadCloser
	out     io.WriteCloser
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[uint64]chan result
	closed  bool
	next    atomic.Uint64
	once    sync.Once
	limits  Limits
}

func NewClient(in io.ReadCloser, out io.WriteCloser, selected ...Limits) *Client {
	limits := DefaultLimits()
	if len(selected) > 0 {
		limits = selected[0]
	}
	c := &Client{in: in, out: out, pending: make(map[uint64]chan result), limits: limits}
	if limits.Validate() != nil {
		c.closed = true
		_ = in.Close()
		_ = out.Close()
		return c
	}
	go c.read()
	return c
}
func (c *Client) read() {
	for {
		m, e := readMessage(c.in, c.limits.FrameBytes)
		if e != nil {
			_ = c.Close()
			return
		}
		if m.Method != "" {
			_ = c.Close()
			return
		}
		c.mu.Lock()
		ch := c.pending[m.ID]
		delete(c.pending, m.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- result{message: m}
		}
	}
}
func (c *Client) Call(ctx context.Context, method string, body, out any) error {
	if method == "" || len(method) > 64 {
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
	ch := make(chan result, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	limit := c.limits.Requests
	if controlMethod(method) {
		limit += 8
	}
	if len(c.pending) >= limit {
		c.mu.Unlock()
		return ErrBusy
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	// The pipe writer is bounded by the owning process lifecycle. If any caller
	// cancels during a blocked operation, terminate this generation rather than
	// leave an unbounded abandoned stream request or replay application bytes.
	done := make(chan struct{})
	var completed atomic.Uint32
	defer func() { completed.CompareAndSwap(0, 1); close(done) }()
	go func() {
		select {
		case <-ctx.Done():
			if completed.CompareAndSwap(0, 2) {
				_ = c.Close()
			}
		case <-done:
		}
	}()
	c.writeMu.Lock()
	e = writeMessage(c.out, Message{ID: id, Method: method, Body: raw}, c.limits.FrameBytes)
	c.writeMu.Unlock()
	if e != nil {
		_ = c.Close()
		return e
	}
	select {
	case <-ctx.Done():
		completed.CompareAndSwap(0, 2)
		_ = c.Close()
		return ctx.Err()
	case r := <-ch:
		if !completed.CompareAndSwap(0, 1) {
			return ctx.Err()
		}
		if r.err != nil {
			return r.err
		}
		if r.message.Error != "" {
			return errors.New(r.message.Error)
		}
		if out != nil {
			return json.Unmarshal(r.message.Body, out)
		}
		return nil
	}
}
func (c *Client) Close() error {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		pending := c.pending
		c.pending = make(map[uint64]chan result)
		c.mu.Unlock()
		_ = c.in.Close()
		_ = c.out.Close()
		for _, ch := range pending {
			ch <- result{err: ErrClosed}
		}
	})
	return nil
}

// Reserve control capacity so full data reads cannot block revocation, close or
// identity checks. Excess control work still fails closed at a finite bound.
func controlMethod(method string) bool {
	switch method {
	case "close", "close-write", "deadline", "read-deadline", "write-deadline", "fallback-scopes", "fallback-disable", "state", "whois", "logout":
		return true
	}
	return false
}
