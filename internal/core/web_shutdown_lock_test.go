package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/webkaz-labs/sobalink/internal/webui"
)

// Only scheduling is controlled. The authenticated HTTP request still executes
// the real Core Snapshot and the real HTTP/Core shutdown paths.
type scheduledSnapshotBackend struct {
	*Core
	entered, resume, finished chan struct{}
}

func (b *scheduledSnapshotBackend) Snapshot(ctx context.Context) (map[string]any, error) {
	close(b.entered)
	<-b.resume
	defer close(b.finished)
	return b.Core.Snapshot(ctx)
}

type shutdownPeerBarrier struct {
	entered, resume chan struct{}
	once            sync.Once
	err             error
}

func (l *shutdownPeerBarrier) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (l *shutdownPeerBarrier) Addr() net.Addr            { return &net.TCPAddr{} }
func (l *shutdownPeerBarrier) Close() error {
	l.once.Do(func() { close(l.entered); <-l.resume })
	return l.err
}

func awaitShutdownProbe(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not arrive", label)
	}
}

func TestCoreCloseDrainsDisconnectedSnapshotWithoutMutationLock(t *testing.T) {
	coreCloseDrainsDisconnectedSnapshot(t, nil)
}

func TestCoreCloseDrainsDisconnectedSnapshotPreservesFailure(t *testing.T) {
	coreCloseDrainsDisconnectedSnapshot(t, errors.New("synthetic peer close failure"))
}

func coreCloseDrainsDisconnectedSnapshot(t *testing.T, closeFailure error) {
	t.Helper()
	c, err := openOwnedTestCore(t, context.Background(), Options{Directory: t.TempDir(), SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b := &scheduledSnapshotBackend{Core: c, entered: make(chan struct{}), resume: make(chan struct{}), finished: make(chan struct{})}
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(b.resume) }) }
	defer resume()
	server, err := webui.Start(c.ctx, fstest.MapFS{"index.html": {Data: []byte("synthetic UI")}}, b)
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.web = server
	c.mu.Unlock()
	code, err := server.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	req, err := http.NewRequest("POST", server.URL()+"/api/session", strings.NewReader(`{"code":"`+code+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", server.URL())
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cookies := response.Cookies()
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || len(cookies) != 1 {
		t.Fatal("synthetic login failed")
	}
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	requestDone := make(chan struct{})
	request, _ := http.NewRequestWithContext(requestCtx, "GET", server.URL()+"/api/state", nil)
	request.AddCookie(cookies[0])
	go func() {
		defer close(requestDone)
		result, e := client.Do(request)
		if e == nil {
			_, _ = io.Copy(io.Discard, result.Body)
			_ = result.Body.Close()
		}
	}()
	awaitShutdownProbe(t, b.entered, "authenticated snapshot admission")
	// Model context.close(): its request has gone away, but the server-side
	// handler remains admitted until its actual Snapshot returns.
	cancelRequest()
	awaitShutdownProbe(t, requestDone, "client disconnection")
	barrier := &shutdownPeerBarrier{entered: make(chan struct{}), resume: make(chan struct{}), err: closeFailure}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(barrier.resume) }) }
	defer release()
	c.mu.Lock()
	c.peerServer = &peerServer{listeners: []*peerServerListener{{Listener: barrier}}}
	c.mu.Unlock()
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	// This closer is called under Core.op, before the Web close. Schedule the
	// already-admitted Snapshot to need that same mutex before releasing it.
	awaitShutdownProbe(t, barrier.entered, "Core mutation-lock ownership")
	concurrent := make(chan error, 3)
	for range 3 {
		go func() { concurrent <- c.Close() }()
	}
	resume()
	release()
	var result error
	select {
	case result = <-closed:
		if errors.Is(result, context.DeadlineExceeded) || !errors.Is(result, closeFailure) {
			t.Fatalf("Core shutdown result changed: deadline=%t expected-failure=%t", errors.Is(result, context.DeadlineExceeded), errors.Is(result, closeFailure))
		}
	case <-time.After(7 * time.Second):
		t.Fatal("Core shutdown did not finish")
	}
	awaitShutdownProbe(t, b.finished, "server-side snapshot completion")
	for range 3 {
		select {
		case got := <-concurrent:
			if got != result {
				t.Fatal("concurrent Close changed original result")
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent Close did not join")
		}
	}
	if c.Close() != result {
		t.Fatal("repeated Close changed original result")
	}
}

func TestStartWebRejectsStoppingCore(t *testing.T) {
	for _, state := range []string{"canceled-new", "canceled-existing", "closed", "closing"} {
		t.Run(state, func(t *testing.T) {
			c, err := openOwnedTestCore(t, context.Background(), Options{Directory: t.TempDir(), SkipNetworkStart: true})
			if err != nil {
				t.Fatal(err)
			}
			assets := fstest.MapFS{"index.html": {Data: []byte("synthetic UI")}}
			if state == "canceled-existing" {
				if _, _, err := c.StartWeb(assets); err != nil {
					t.Fatal(err)
				}
			}
			var closed chan error
			var barrier *shutdownPeerBarrier
			release := func() {}
			defer func() { release() }()
			if state == "closed" {
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
			} else if state == "closing" {
				barrier = &shutdownPeerBarrier{entered: make(chan struct{}), resume: make(chan struct{})}
				var releaseOnce sync.Once
				release = func() { releaseOnce.Do(func() { close(barrier.resume) }) }
				c.mu.Lock()
				c.peerServer = &peerServer{listeners: []*peerServerListener{{Listener: barrier}}}
				c.mu.Unlock()
				closed = make(chan error, 1)
				go func() { closed <- c.Close() }()
				awaitShutdownProbe(t, barrier.entered, "closing Core")
			} else {
				c.cancel()
			}
			type result struct {
				address, code string
				err           error
			}
			started, done := make(chan struct{}), make(chan result, 1)
			go func() { close(started); address, code, err := c.StartWeb(assets); done <- result{address, code, err} }()
			<-started
			release()
			select {
			case got := <-done:
				if got.err == nil || got.address != "" || got.code != "" {
					// The original implementation can publish a replacement
					// after Close returned. Contain that failing-test resource.
					c.mu.RLock()
					web := c.web
					c.mu.RUnlock()
					if web != nil {
						ctx, cancel := context.WithTimeout(context.Background(), time.Second)
						_ = web.Close(ctx)
						cancel()
					}
					t.Error("stopping Core published a Web server or login code")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("StartWeb did not finish")
			}
			if closed != nil {
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("Core close did not finish")
				}
			}
		})
	}
}
