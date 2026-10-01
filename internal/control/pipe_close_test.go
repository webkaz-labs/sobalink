package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Model the observed native stack: Accept has returned an unexpected connection
// error, listenerRoutine is waiting again, and Close is still waiting for doneCh.
type lostClosePipe struct {
	closeNotification chan struct{}
	done              chan struct{}
	acceptError       error
	loseNotification  bool
	beforeClose       bool
	retryGate         <-chan struct{}
	calls             atomic.Int32
	doneOnce          sync.Once
}

func newLostClosePipe() *lostClosePipe {
	return &lostClosePipe{closeNotification: make(chan struct{}), done: make(chan struct{}), acceptError: errors.New("fixture native connect aborted"), loseNotification: true}
}
func (l *lostClosePipe) Accept() (net.Conn, error) {
	if !l.beforeClose {
		<-l.closeNotification
	}
	return nil, l.acceptError
}
func (l *lostClosePipe) Close() error {
	n := l.calls.Add(1)
	if n == 1 {
		close(l.closeNotification)
	}
	if n > 1 || !l.loseNotification {
		if n == 2 && l.retryGate != nil {
			<-l.retryGate
		}
		l.doneOnce.Do(func() { close(l.done) })
	}
	<-l.done
	return nil
}
func (l *lostClosePipe) Addr() net.Addr { return memoryAddress("lost-close-pipe") }

func TestPipeCloseLostNotificationReproducer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		raw := newLostClosePipe()
		s := serveListener(t.Context(), raw, func(context.Context, string) (any, error) {
			t.Error("unexpected handler")
			return nil, nil
		})
		start := time.Now()
		err := <-closeAsync(s)
		if !errors.Is(err, ErrShutdownTimeout) || !strings.Contains(err.Error(), "listener pending=true, handlers/accept pending=false, connection closes pending=false") {
			t.Fatalf("fixture did not reproduce native pending stages: %v", err)
		}
		if time.Since(start) != shutdownTimeout || raw.calls.Load() != 1 {
			t.Fatal("fixture did not consume exactly one close notification")
		}
		// Release the intentionally stuck fixture rather than leaking a worker.
		raw.Close()
		synctest.Wait()
	})
}

func TestPipeCloseRecoversLostNotification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		raw := newLostClosePipe()
		ln := &pipeCloseListener{Listener: raw}
		s := serveListener(t.Context(), ln, func(context.Context, string) (any, error) {
			t.Error("unexpected handler")
			return nil, nil
		})
		start := time.Now()
		if err := <-closeAsync(s); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("recovery used a timer instead of the observed accept error: %s", elapsed)
		}
		if raw.calls.Load() != 2 {
			t.Fatalf("close notifications %d, want two", raw.calls.Load())
		}
		if _, err := ln.Accept(); !errors.Is(err, raw.acceptError) || raw.calls.Load() != 2 {
			t.Fatal("recovery repeated or suppressed the accept error", err)
		}
	})
}

func TestPipeCloseDoesNotRetryNormalClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		raw := newLostClosePipe()
		raw.loseNotification = false
		raw.acceptError = net.ErrClosed
		ln := &pipeCloseListener{Listener: raw}
		s := serveListener(t.Context(), ln, func(context.Context, string) (any, error) { return nil, nil })
		if err := <-closeAsync(s); err != nil {
			t.Fatal(err)
		}
		if raw.calls.Load() != 1 {
			t.Fatalf("normal closure retried: %d", raw.calls.Load())
		}
	})
}

func TestPipeCloseRecoversWrappedClosedError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		raw := newLostClosePipe()
		raw.acceptError = fmt.Errorf("fixture wrapped error: %w", net.ErrClosed)
		ln := &pipeCloseListener{Listener: raw}
		s := serveListener(t.Context(), ln, func(context.Context, string) (any, error) { return nil, nil })
		if err := <-closeAsync(s); err != nil || raw.calls.Load() != 2 {
			t.Fatalf("upstream exact-sentinel mismatch was not recovered: %v", err)
		}
	})
}

func TestPipeClosePreservesErrorsBeforeClosing(t *testing.T) {
	raw := newLostClosePipe()
	raw.beforeClose = true
	ln := &pipeCloseListener{Listener: raw}
	if c, err := ln.Accept(); c != nil || !errors.Is(err, raw.acceptError) || raw.calls.Load() != 0 {
		t.Fatal("an ordinary accept error triggered an unsolicited close", c, err)
	}
}

func TestPipeCloseBlockedRetryStillReportsTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		raw := newLostClosePipe()
		release := make(chan struct{})
		raw.retryGate = release
		ln := &pipeCloseListener{Listener: raw}
		s := serveListener(t.Context(), ln, func(context.Context, string) (any, error) { return nil, nil })
		err := <-closeAsync(s)
		if !errors.Is(err, ErrShutdownTimeout) || raw.calls.Load() != 2 {
			t.Fatalf("blocked recovery was reported successful: %v", err)
		}
		close(release)
		synctest.Wait()
	})
}
