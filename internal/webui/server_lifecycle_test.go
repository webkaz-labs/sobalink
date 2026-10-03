package webui

import (
	"context"
	"net"
	"net/http"
	"testing"
	"testing/fstest"
	"time"
)

type lifecycleBackend struct{}

func (lifecycleBackend) Snapshot(context.Context) (map[string]any, error) {
	return map[string]any{}, nil
}
func (lifecycleBackend) Command(context.Context, Command) (any, error) { return nil, nil }
func (lifecycleBackend) Upload(http.ResponseWriter, *http.Request)     {}

func startLifecycleServer(t *testing.T, ctx context.Context) *Server {
	t.Helper()
	s, err := Start(ctx, fstest.MapFS{}, lifecycleBackend{})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	conn, err := net.DialTimeout("tcp4", s.host, time.Second)
	if err != nil {
		t.Fatalf("loopback listener %q is not accepting connections: %v", s.host, err)
	}
	_ = conn.Close()
	return s
}

func waitForWatcher(t *testing.T, s *Server) {
	t.Helper()
	select {
	case <-s.watcherDone:
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle watcher did not terminate")
	}
}

func TestServerCloseStopsLifecycleWatcher(t *testing.T) {
	for range 3 {
		s := startLifecycleServer(t, context.Background())
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := s.Close(ctx); err != nil {
			cancel()
			t.Fatalf("Close() error = %v", err)
		}
		cancel()
		if err := s.Close(context.Background()); err != nil {
			t.Fatalf("repeated Close() error = %v", err)
		}
		waitForWatcher(t, s)
	}
}

func TestServerCloseConcurrentWithParentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	s := startLifecycleServer(t, parent)
	closeResult := make(chan error, 1)
	closeStarted := make(chan struct{})
	go func() {
		close(closeStarted)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		closeResult <- s.Close(ctx)
	}()
	<-closeStarted
	cancelParent()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("concurrent Close() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close() did not return after concurrent parent cancellation")
	}
	waitForWatcher(t, s)
}

func TestServerServeFailureStopsLifecycleWatcher(t *testing.T) {
	s := startLifecycleServer(t, context.Background())
	if err := s.listener.Close(); err != nil {
		t.Fatal(err)
	}
	waitForWatcher(t, s)
	select {
	case err := <-s.done:
		if err == nil {
			t.Fatal("unexpected Serve termination lost its error")
		}
	case <-time.After(time.Second):
		t.Fatal("Serve completion not reported")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
