// Package control is a bounded JSON protocol over user-private local IPC.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

type Request struct {
	Command string `json:"command"`
}
type Response struct {
	Error string          `json:"error,omitempty"`
	Code  string          `json:"code,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// RemoteError preserves a stable command error code across the local IPC
// boundary. Older servers without codes remain readable with an empty Code.
type RemoteError struct {
	Code    string
	Message string
}

func (e *RemoteError) Error() string     { return e.Message }
func (e *RemoteError) ErrorCode() string { return e.Code }

func boundedErrorCode(code string) string {
	if len(code) == 0 || len(code) > 64 {
		return ""
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && r != '_' {
			return ""
		}
	}
	return code
}

// Handler must return when its context is canceled. Close reports a timeout
// rather than waiting indefinitely if a handler or native IPC close gets stuck.
type Handler func(context.Context, string) (any, error)

const (
	responseDrainTimeout = 200 * time.Millisecond
	maxRequestBytes      = 64 << 10
	// Local status includes bounded multi-batch metadata and saved destinations,
	// so it needs more room than a command request. File bodies never use IPC.
	maxResponseBytes = 16 << 20
	// Total cleanup includes response draining. This five-second budget leaves
	// ample margin within the 20-second control-call deadline for Windows I/O
	// completion scheduling, while still refusing an indefinitely stuck close.
	shutdownTimeout = 5 * time.Second
)

// ErrShutdownTimeout means admission is stopped and cancellation/close has been
// requested, but at least one resource has not finished shutting down. Cleanup
// continues in the background; callers must not treat this as successful cleanup.
var ErrShutdownTimeout = errors.New("control shutdown deadline exceeded")

type Server struct {
	ln        net.Listener
	wg        sync.WaitGroup
	cancel    context.CancelFunc
	mu        sync.Mutex
	conns     map[net.Conn]bool
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func Serve(parent context.Context, dir string, h Handler) (*Server, error) {
	ln, e := listen(dir)
	if e != nil {
		return nil, e
	}
	return serveListener(parent, ln, h), nil
}

// serveListener keeps shutdown policy testable with in-memory I/O while Serve
// continues to use the actual protected Unix socket or Windows named pipe.
func serveListener(parent context.Context, ln net.Listener, h Handler) *Server {
	ctx, cancel := context.WithCancel(parent)
	s := &Server{ln: ln, cancel: cancel, conns: make(map[net.Conn]bool)}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		slots := make(chan struct{}, 16)
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				c.Close()
				continue
			}
			s.mu.Lock()
			if s.closed || ctx.Err() != nil {
				s.mu.Unlock()
				c.Close()
				<-slots
				return
			}
			s.conns[c] = true
			s.mu.Unlock()
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer func() { <-slots; s.mu.Lock(); delete(s.conns, c); s.mu.Unlock() }()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(20 * time.Second))
				var r Request
				d := json.NewDecoder(io.LimitReader(c, maxRequestBytes))
				d.DisallowUnknownFields()
				if e := d.Decode(&r); e != nil {
					return
				}
				callCtx, stop := context.WithTimeout(ctx, 15*time.Second)
				defer stop()
				v, e := h(callCtx, r.Command)
				resp := Response{}
				if e != nil {
					resp.Error = e.Error()
					var coded interface{ ErrorCode() string }
					if errors.As(e, &coded) {
						resp.Code = boundedErrorCode(coded.ErrorCode())
					}
				} else {
					resp.Data, e = json.Marshal(v)
					if e != nil {
						resp.Error = "could not encode response"
					}
				}
				encoded, err := json.Marshal(resp)
				if err != nil || len(encoded)+1 > maxResponseBytes {
					encoded, _ = json.Marshal(Response{Error: "local response is too large; use the local UI to inspect and clear completed transfer history", Code: "response_too_large"})
				}
				_, _ = c.Write(append(encoded, '\n'))
			}()
		}
	}()
	return s
}
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		deadline := time.NewTimer(shutdownTimeout)
		defer deadline.Stop()
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()

		// go-winio's listener.Close waits for ConnectNamedPipe completion and
		// connection.Close waits for overlapped I/O cancellation. Neither may
		// block cancellation of the other resources or hold the registry lock.
		listenerClosed := make(chan error, 1)
		go func() { listenerClosed <- s.ln.Close() }()
		joined := make(chan struct{})
		go func() { s.wg.Wait(); close(joined) }()

		// Preserve a short opportunity for an in-flight stop/logout response.
		// The total deadline starts before closing the listener, not afterward.
		grace := time.NewTimer(responseDrainTimeout)
		expired := false
		select {
		case <-joined:
		case <-grace.C:
		case <-deadline.C:
			expired = true
		}
		grace.Stop()
		s.cancel()

		s.mu.Lock()
		conns := make([]net.Conn, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
		s.mu.Unlock()
		connectionsClosed := make(chan struct{})
		var closers sync.WaitGroup
		for _, c := range conns {
			closers.Add(1)
			go func() {
				defer closers.Done()
				// A deadline also prevents new reads/writes on a connection whose
				// platform close is still awaiting native completion.
				_ = c.SetDeadline(time.Now())
				_ = c.Close()
			}()
		}
		go func() { closers.Wait(); close(connectionsClosed) }()

		s.closeErr = awaitShutdown(deadline.C, expired, listenerClosed, joined, connectionsClosed)
	})
	return s.closeErr
}

// awaitShutdown observes ready completions before acting on an expired budget.
// Keeping the channel values local also lets deterministic tests exercise the
// timer/completion tie without depending on OS scheduling.
func awaitShutdown(deadline <-chan time.Time, expired bool, listenerClosed <-chan error, joined, connectionsClosed <-chan struct{}) error {
	var closeErr error
	// Each channel is set to nil after observation. This bounds all three
	// joins without reporting successful shutdown while any remains pending.
	for listenerClosed != nil || joined != nil || connectionsClosed != nil {
		// A deadline and a completion can become ready together. Observe all
		// already-completed work before classifying anything as timed out.
		select {
		case err := <-listenerClosed:
			closeErr = err
			listenerClosed = nil
		default:
		}
		select {
		case <-joined:
			joined = nil
		default:
		}
		select {
		case <-connectionsClosed:
			connectionsClosed = nil
		default:
		}
		if listenerClosed == nil && joined == nil && connectionsClosed == nil {
			break
		}
		if expired {
			closeErr = errors.Join(closeErr, fmt.Errorf("%w: listener pending=%t, handlers/accept pending=%t, connection closes pending=%t",
				ErrShutdownTimeout, listenerClosed != nil, joined != nil, connectionsClosed != nil))
			return closeErr
		}
		select {
		case err := <-listenerClosed:
			closeErr = err
			listenerClosed = nil
		case <-joined:
			joined = nil
		case <-connectionsClosed:
			connectionsClosed = nil
		case <-deadline:
			expired = true
		}
	}
	return closeErr
}

func Call(ctx context.Context, dir, command string, v any) error {
	encoded, err := json.Marshal(Request{command})
	if err != nil {
		return err
	}
	if len(encoded)+1 > maxRequestBytes {
		return errors.New("control request exceeds 64 KiB")
	}
	c, e := dial(ctx, dir)
	if e != nil {
		return e
	}
	defer c.Close()
	deadline := time.Now().Add(20 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.SetDeadline(deadline)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		case <-done:
		}
	}()
	if e = json.NewEncoder(c).Encode(Request{command}); e != nil {
		return e
	}
	return readResponse(c, v)
}

func readResponse(reader io.Reader, v any) error {
	var r Response
	limited := &io.LimitedReader{R: reader, N: maxResponseBytes + 1}
	e := json.NewDecoder(limited).Decode(&r)
	if limited.N == 0 {
		return &RemoteError{Code: "response_too_large", Message: "local response exceeds the 16 MiB IPC limit"}
	}
	if e != nil {
		return e
	}
	if r.Error != "" {
		return &RemoteError{Code: boundedErrorCode(r.Code), Message: r.Error}
	}
	if v != nil {
		return json.Unmarshal(r.Data, v)
	}
	return nil
}
