package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type memoryListener struct {
	incoming     chan net.Conn
	stopped      chan struct{}
	closeStarted chan struct{}
	closeGate    <-chan struct{}
	once         sync.Once
}

func newMemoryListener() *memoryListener {
	return &memoryListener{incoming: make(chan net.Conn, 1), stopped: make(chan struct{}), closeStarted: make(chan struct{})}
}
func (l *memoryListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.incoming:
		return c, nil
	case <-l.stopped:
		return nil, net.ErrClosed
	}
}
func (l *memoryListener) Close() error {
	l.once.Do(func() { close(l.closeStarted); close(l.stopped) })
	if l.closeGate != nil {
		<-l.closeGate
	}
	return nil
}
func (l *memoryListener) Addr() net.Addr { return memoryAddress("memory") }

type memoryAddress string

func (a memoryAddress) Network() string { return "memory" }
func (a memoryAddress) String() string  { return string(a) }

type observedConn struct {
	net.Conn
	readStarted chan struct{}
	once        sync.Once
}

func observeConn(c net.Conn) *observedConn {
	return &observedConn{Conn: c, readStarted: make(chan struct{})}
}
func (c *observedConn) Read(p []byte) (int, error) {
	c.once.Do(func() { close(c.readStarted) })
	return c.Conn.Read(p)
}

func closeAsync(s *Server) <-chan error {
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	return done
}
func assertPending(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("shutdown returned before drain policy: %v", err)
	default:
	}
}
func memoryServer(h Handler) (*Server, *memoryListener, net.Conn, *observedConn) {
	ln := newMemoryListener()
	server, client := net.Pipe()
	observed := observeConn(server)
	ln.incoming <- observed
	return serveListener(context.Background(), ln, h), ln, client, observed
}

func TestShutdownSlowClientDrainPolicy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, client, server := memoryServer(func(context.Context, string) (any, error) { t.Error("handler called without request"); return nil, nil })
		defer client.Close()
		<-server.readStarted
		synctest.Wait()
		start := time.Now()
		done := closeAsync(s)
		synctest.Wait()
		assertPending(t, done)
		time.Sleep(responseDrainTimeout - time.Nanosecond)
		synctest.Wait()
		assertPending(t, done)
		time.Sleep(time.Nanosecond)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != responseDrainTimeout {
			t.Fatalf("shutdown took %s, want policy %s", elapsed, responseDrainTimeout)
		}
		s.mu.Lock()
		remaining := len(s.conns)
		s.mu.Unlock()
		if remaining != 0 {
			t.Fatalf("connections remaining: %d", remaining)
		}
	})
}

func TestShutdownPreservesInFlightResponse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		s, _, client, _ := memoryServer(func(ctx context.Context, name string) (any, error) {
			close(entered)
			select {
			case <-release:
				return map[string]string{"command": name}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
		defer client.Close()
		result := make(chan Response, 1)
		requestErr := make(chan error, 1)
		go func() {
			if err := json.NewEncoder(client).Encode(Request{"stop"}); err != nil {
				requestErr <- err
				return
			}
			var response Response
			if err := json.NewDecoder(client).Decode(&response); err != nil {
				requestErr <- err
				return
			}
			result <- response
		}()
		<-entered
		start := time.Now()
		done := closeAsync(s)
		synctest.Wait()
		assertPending(t, done)
		time.Sleep(responseDrainTimeout / 2)
		close(release)
		select {
		case response := <-result:
			if response.Error != "" || !strings.Contains(string(response.Data), "stop") {
				t.Fatalf("response lost: %+v", response)
			}
		case err := <-requestErr:
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if time.Since(start) != responseDrainTimeout/2 {
			t.Fatalf("completed response unnecessarily waited full grace: %s", time.Since(start))
		}
	})
}

func TestShutdownCancelsHandlerAfterGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		canceled := make(chan struct{})
		s, _, client, _ := memoryServer(func(ctx context.Context, _ string) (any, error) {
			close(entered)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		})
		defer client.Close()
		if err := json.NewEncoder(client).Encode(Request{"doctor"}); err != nil {
			t.Fatal(err)
		}
		<-entered
		start := time.Now()
		done := closeAsync(s)
		synctest.Wait()
		select {
		case <-canceled:
			t.Fatal("canceled handler before response grace")
		default:
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		select {
		case <-canceled:
		default:
			t.Fatal("handler was not canceled")
		}
		if time.Since(start) != responseDrainTimeout {
			t.Fatalf("canceled at %s", time.Since(start))
		}
	})
}

func TestShutdownBoundsBlockedListenerClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, ln, client, server := memoryServer(func(context.Context, string) (any, error) { return nil, nil })
		defer client.Close()
		<-server.readStarted
		synctest.Wait()
		release := make(chan struct{})
		ln.closeGate = release
		start := time.Now()
		done := closeAsync(s)
		<-ln.closeStarted
		synctest.Wait()
		time.Sleep(responseDrainTimeout)
		synctest.Wait()
		s.mu.Lock()
		remaining := len(s.conns)
		s.mu.Unlock()
		if remaining != 0 {
			t.Fatalf("blocked listener prevented connection cancellation: %d", remaining)
		}
		err := <-done
		if !errors.Is(err, ErrShutdownTimeout) || !strings.Contains(err.Error(), "listener pending=true") {
			t.Fatalf("wrong timeout: %v", err)
		}
		if time.Since(start) != shutdownTimeout {
			t.Fatalf("deadline = %s, want %s", time.Since(start), shutdownTimeout)
		}
		close(release)
		synctest.Wait()
		if !errors.Is(s.Close(), ErrShutdownTimeout) {
			t.Fatal("later Close hid incomplete shutdown")
		}
	})
}

type blockedCloseConn struct {
	net.Conn
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (c *blockedCloseConn) Close() error {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return c.Conn.Close()
}

func TestShutdownBoundsBlockedConnectionClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ln := newMemoryListener()
		server, client := net.Pipe()
		defer client.Close()
		release := make(chan struct{})
		blocked := &blockedCloseConn{Conn: server, started: make(chan struct{}), release: release}
		observed := observeConn(blocked)
		ln.incoming <- observed
		s := serveListener(context.Background(), ln, func(context.Context, string) (any, error) { return nil, nil })
		<-observed.readStarted
		synctest.Wait()
		start := time.Now()
		done := closeAsync(s)
		<-blocked.started
		synctest.Wait()
		// This lock must remain available even while the transport's Close blocks.
		locked := make(chan struct{})
		go func() { s.mu.Lock(); s.mu.Unlock(); close(locked) }()
		<-locked
		err := <-done
		if !errors.Is(err, ErrShutdownTimeout) || !strings.Contains(err.Error(), "connection closes pending=true") {
			t.Fatalf("wrong timeout: %v", err)
		}
		if time.Since(start) != shutdownTimeout {
			t.Fatalf("deadline = %s", time.Since(start))
		}
		close(release)
		synctest.Wait()
	})
}

func TestShutdownBoundsUncooperativeHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		s, _, client, _ := memoryServer(func(context.Context, string) (any, error) { close(entered); <-release; return nil, nil })
		defer client.Close()
		if err := json.NewEncoder(client).Encode(Request{"status"}); err != nil {
			t.Fatal(err)
		}
		<-entered
		start := time.Now()
		err := <-closeAsync(s)
		if !errors.Is(err, ErrShutdownTimeout) || !strings.Contains(err.Error(), "handlers/accept pending=true") {
			t.Fatalf("wrong timeout: %v", err)
		}
		if time.Since(start) != shutdownTimeout {
			t.Fatalf("deadline = %s", time.Since(start))
		}
		close(release)
		synctest.Wait()
	})
}

// sync.Once waiters block on a mutex, which is intentionally not a durably
// blocked operation in synctest. Test concurrent callers on the real clock;
// the response-grace and deadline policies above use virtual time.
func TestShutdownConcurrentCloseIsIdempotent(t *testing.T) {
	s, _, client, server := memoryServer(func(context.Context, string) (any, error) { return nil, nil })
	defer client.Close()
	<-server.readStarted
	done := make(chan error, 8)
	for range 8 {
		go func() { done <- s.Close() }()
	}
	watchdog := time.NewTimer(2 * shutdownTimeout)
	defer watchdog.Stop()
	for range 8 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-watchdog.C:
			t.Fatal("concurrent Close callers did not finish")
		}
	}
}

func TestShutdownCompletionWinsExpiredBudget(t *testing.T) {
	for _, expired := range []bool{false, true} {
		listener := make(chan error, 1)
		listener <- nil
		joined := make(chan struct{})
		close(joined)
		connections := make(chan struct{})
		close(connections)
		deadline := make(chan time.Time, 1)
		deadline <- time.Now()
		if err := awaitShutdown(deadline, expired, listener, joined, connections); err != nil {
			t.Fatalf("completed shutdown reported a timeout (expired=%t): %v", expired, err)
		}
	}
	listener := make(chan error, 1)
	expected := errors.New("listener close failure")
	listener <- expected
	joined := make(chan struct{})
	close(joined)
	connections := make(chan struct{})
	close(connections)
	if err := awaitShutdown(nil, true, listener, joined, connections); !errors.Is(err, expected) || errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("lost listener error or invented timeout: %v", err)
	}
}

type delayedAcceptListener struct {
	*memoryListener
	seen, release chan struct{}
}

func (l *delayedAcceptListener) Accept() (net.Conn, error) {
	c, err := l.memoryListener.Accept()
	if err != nil {
		return nil, err
	}
	close(l.seen)
	<-l.release
	return c, nil
}
func TestShutdownRejectsLateAcceptedConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := newMemoryListener()
		ln := &delayedAcceptListener{memoryListener: base, seen: make(chan struct{}), release: make(chan struct{})}
		server, client := net.Pipe()
		defer client.Close()
		observed := observeConn(server)
		base.incoming <- observed
		s := serveListener(context.Background(), ln, func(context.Context, string) (any, error) {
			t.Error("late accepted connection reached handler")
			return nil, nil
		})
		<-ln.seen
		done := closeAsync(s)
		<-base.closeStarted
		close(ln.release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		select {
		case <-observed.readStarted:
			t.Fatal("late connection was read")
		default:
		}
		if _, err := client.Read(make([]byte, 1)); err == nil {
			t.Fatal("late connection remained open")
		}
	})
}
