// Package control is a bounded JSON protocol over user-private local IPC.
package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/messageframe"
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
	initialReadTimeout   = 20 * time.Second
	responseWriteTimeout = 20 * time.Second
	maxRequestBytes      = messageframe.ControlRequestBytes
	// Local status includes bounded multi-batch metadata and saved destinations,
	// so it needs more room than a command request. File bodies never use IPC.
	maxResponseBytes = 16 << 20
	// Total cleanup includes response draining and Windows I/O completion,
	// while still refusing an indefinitely stuck native close.
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

// Limits are finite encoded byte budgets, not decoded application admission rules.
// Providers may grow response budgets to retain access to existing history.
type Limits struct {
	CommandBytes  int64 `json:"commandBytes"`
	RequestBytes  int64 `json:"requestBytes"`
	ResponseBytes int64 `json:"responseBytes"`
}

func DefaultLimits() Limits {
	return Limits{int64(messageframe.CommandBytes), int64(maxRequestBytes), maxResponseBytes}
}

func (l Limits) Validate() error {
	for _, value := range []int64{l.CommandBytes, l.RequestBytes, l.ResponseBytes} {
		if value < 1 || value >= math.MaxInt64 {
			return errors.New("local IPC budgets must be finite positive byte counts")
		}
	}
	return nil
}

type LimitsProvider func() Limits

func Serve(parent context.Context, dir string, h Handler) (*Server, error) {
	return ServeWithLimits(parent, dir, h, DefaultLimits)
}

func ServeWithLimits(parent context.Context, dir string, h Handler, limits LimitsProvider) (*Server, error) {
	if limits == nil || limits().Validate() != nil {
		return nil, errors.New("invalid local IPC limits")
	}
	ln, e := listen(dir)
	if e != nil {
		return nil, e
	}
	return serveListenerForDirectory(parent, dir, ln, h, limits), nil
}

// serveListener keeps shutdown policy testable with in-memory I/O while Serve
// continues to use the actual protected Unix socket or Windows named pipe.
func serveListener(parent context.Context, ln net.Listener, h Handler) *Server {
	return serveListenerWithLimits(parent, ln, h, DefaultLimits)
}

func serveListenerWithLimits(parent context.Context, ln net.Listener, h Handler, limits LimitsProvider) *Server {
	return serveListenerForDirectory(parent, "", ln, h, limits)
}

// The production directory is captured before any request goroutine can run.
// The unscoped wrapper above remains available for in-memory protocol tests.
func serveListenerForDirectory(parent context.Context, dir string, ln net.Listener, h Handler, limits LimitsProvider) *Server {
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
				budget := limits()
				if budget.Validate() != nil {
					return
				}
				_ = c.SetReadDeadline(time.Now().Add(initialReadTimeout))
				limited := &io.LimitedReader{R: c, N: budget.RequestBytes + 1}
				reader := bufio.NewReader(limited)
				line, err := reader.ReadBytes('\n')
				if limited.N == 0 || int64(len(line)) > budget.RequestBytes {
					writeResponse(c, Response{Error: requestTooLargeMessage, Code: "request_too_large"}, budget.ResponseBytes)
					return
				}
				if err != nil {
					return
				}
				var r Request
				d := json.NewDecoder(bytes.NewReader(line))
				d.DisallowUnknownFields()
				if err := d.Decode(&r); err != nil {
					return
				}
				if err := d.Decode(new(any)); err != io.EOF {
					return
				}
				if int64(len(r.Command)) > budget.CommandBytes {
					writeResponse(c, Response{Error: requestTooLargeMessage, Code: "request_too_large"}, budget.ResponseBytes)
					return
				}
				// Exactly one newline-delimited request belongs to a connection.
				// Buffered trailing data is rejected before any command is admitted.
				if reader.Buffered() != 0 {
					return
				}
				_ = c.SetReadDeadline(time.Time{})
				callCtx, stop := context.WithCancel(ctx)
				defer stop()
				// Read no more request data; disconnects or a second frame cancel
				// staging promptly without imposing a separate operation timeout.
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					var extra [1]byte
					_, _ = c.Read(extra[:])
					stop()
				}()
				var v any
				var e error
				observeResourceProcessDispatch(dir, r.Command)
				if r.Command == "control.limits" {
					v = budget
				} else {
					v, e = h(callCtx, r.Command)
				}
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
				// A policy command may just have raised or lowered admission.
				// Reevaluate after it finishes so retained data stays readable.
				responseBudget := limits()
				if responseBudget.Validate() == nil {
					writeResponse(c, resp, responseBudget.ResponseBytes)
				}
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

const requestTooLargeMessage = "local command exceeds its JSON envelope limit; shorten the text or reduce the command payload"

func writeResponse(c net.Conn, response Response, limit int64) {
	encoded, err := json.Marshal(response)
	if err != nil || int64(len(encoded))+1 > limit {
		encoded, _ = json.Marshal(Response{Error: "local response is too large; raise the finite response budgets or use paginated list commands", Code: "response_too_large"})
	}
	_ = c.SetWriteDeadline(time.Now().Add(responseWriteTimeout))
	_, _ = c.Write(append(encoded, '\n'))
}

func Call(ctx context.Context, dir, command string, v any) error {
	return CallWithLimits(ctx, dir, command, v, DefaultLimits())
}

func CallWithLimits(ctx context.Context, dir, command string, v any, limits Limits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(Request{command})
	if err != nil {
		return err
	}
	if int64(len(command)) > limits.CommandBytes || int64(len(encoded))+1 > limits.RequestBytes {
		return &RemoteError{Code: "request_too_large", Message: requestTooLargeMessage}
	}
	observeResourceProcessDialAttempt(dir)
	c, err := dial(ctx, dir)
	if err != nil {
		return err
	}
	observeResourceProcessDialCompleted(dir)
	defer c.Close()
	return callConn(ctx, c, encoded, v, limits.ResponseBytes)
}

// callConn is shared by the native client and deterministic in-memory tests.
// The caller's context is the only operation deadline. The server separately
// bounds initial request reads, writes and shutdown.
func callConn(ctx context.Context, c net.Conn, encoded []byte, v any, responseBytes int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = c.SetDeadline(time.Now())
			_ = c.Close()
		case <-done:
		}
	}()
	_, err := c.Write(append(encoded, '\n'))
	if err == nil {
		err = readResponseWithLimit(c, v, responseBytes)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func readResponse(reader io.Reader, v any) error {
	return readResponseWithLimit(reader, v, maxResponseBytes)
}

func readResponseWithLimit(reader io.Reader, v any, limit int64) error {
	if limit < 1 || limit >= math.MaxInt64 {
		return errors.New("invalid response budget")
	}
	var r Response
	limited := &io.LimitedReader{R: reader, N: limit + 1}
	decoder := json.NewDecoder(limited)
	err := decoder.Decode(&r)
	if limited.N == 0 {
		return &RemoteError{Code: "response_too_large", Message: "local response exceeds the selected finite IPC budget"}
	}
	if err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); limited.N == 0 {
		return &RemoteError{Code: "response_too_large", Message: "local response exceeds the selected finite IPC budget"}
	} else if err != io.EOF {
		return errors.New("unexpected data after local response")
	}
	if r.Error != "" {
		return &RemoteError{Code: boundedErrorCode(r.Code), Message: r.Error}
	}
	if v != nil {
		return json.Unmarshal(r.Data, v)
	}
	return nil
}
