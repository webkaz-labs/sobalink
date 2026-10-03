package control

import (
	"context"
	"net"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// Native IPC checks exercise actual named pipes/Unix sockets. This watchdog is
// diagnostic scheduling margin, not the response-drain policy; synctest covers
// the exact 200ms grace and five-second total cleanup budget separately.
const nativeIPCWatchdog = 2 * shutdownTimeout

type observingListener struct {
	net.Listener
	accepted chan *observedConn
}

func (l *observingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	observed := observeConn(c)
	l.accepted <- observed
	return observed, nil
}
func goroutineDump() string {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return string(buf[:n])
}
func nativeAwait[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	timer := time.NewTimer(nativeIPCWatchdog)
	defer timer.Stop()
	select {
	case value := <-ch:
		return value
	case <-timer.C:
		t.Fatalf("native IPC watchdog while %s (policy budget %s):\n%s", what, shutdownTimeout, goroutineDump())
		var zero T
		return zero
	}
}
func nativeServer(t *testing.T) (*Server, *observingListener, string) {
	t.Helper()
	dir := shortDir(t)
	if err := config.SecureDir(dir); err != nil {
		t.Fatal(err)
	}
	ln, err := listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	observed := &observingListener{Listener: ln, accepted: make(chan *observedConn, 16)}
	s := serveListener(context.Background(), observed, func(_ context.Context, name string) (any, error) { return map[string]string{"command": name}, nil })
	t.Cleanup(func() {
		if err := nativeAwait(t, "cleanup", closeAsync(s)); err != nil {
			t.Errorf("native IPC cleanup: %v\n%s", err, goroutineDump())
		}
	})
	return s, observed, dir
}

func TestRoundTripAndShutdown(t *testing.T) {
	s, ln, dir := nativeServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), nativeIPCWatchdog)
	defer cancel()
	var out map[string]string
	if err := Call(ctx, dir, "status", &out); err != nil || out["command"] != "status" {
		t.Fatal(out, err)
	}
	first := nativeAwait(t, "first accepted connection", ln.accepted)
	nativeAwait(t, "first request read", first.readStarted)
	slow, err := dial(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	accepted := nativeAwait(t, "slow connection accepted", ln.accepted)
	nativeAwait(t, "slow connection waiting for request", accepted.readStarted)
	if err := nativeAwait(t, "shutdown with an accepted client that sent no request", closeAsync(s)); err != nil {
		t.Fatalf("native shutdown failed: %v\n%s", err, goroutineDump())
	}
}

// Keep a native test for the accept/connect overlap too: client dial completion
// is deliberately not treated as proof that the server registered its handle.
func TestNativeShutdownDuringConnect(t *testing.T) {
	s, _, dir := nativeServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), nativeIPCWatchdog)
	defer cancel()
	slow, err := dial(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	if err := nativeAwait(t, "shutdown overlapping a native connect", closeAsync(s)); err != nil {
		t.Fatalf("native connect/shutdown failed: %v\n%s", err, goroutineDump())
	}
}

func TestUnavailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), nativeIPCWatchdog)
	defer cancel()
	err := Call(ctx, shortDir(t), "status", nil)
	if err == nil || !Unavailable(err) {
		t.Fatal(err)
	}
}

func TestNativeCommandErrorCode(t *testing.T) {
	dir := shortDir(t)
	if err := config.SecureDir(dir); err != nil {
		t.Fatal(err)
	}
	s, err := Serve(t.Context(), dir, func(context.Context, string) (any, error) {
		return nil, fixtureCommandError{}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	})
	err = Call(t.Context(), dir, "fixture", nil)
	assertRemoteCode(t, err, "service_revision_conflict", "reload the saved configuration")
}

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tb-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
