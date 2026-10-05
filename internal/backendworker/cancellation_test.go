package backendworker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

type delayedDialEngine struct {
	pipeEngine
	entered, release chan struct{}
	peer             chan net.Conn
}

func (e *delayedDialEngine) DialIP(ctx context.Context, network string, ap netip.AddrPort) (net.Conn, error) {
	if ap.Port() != 9001 {
		return e.pipeEngine.DialIP(ctx, network, ap)
	}
	a, b := net.Pipe()
	e.peer <- b
	close(e.entered)
	<-e.release
	return a, nil
}
func TestCanceledDialRetiresOnlyItsLateHandle(t *testing.T) {
	in, parentOut := io.Pipe()
	parentIn, out := io.Pipe()
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	e := &delayedDialEngine{entered: make(chan struct{}), release: make(chan struct{}), peer: make(chan net.Conn, 1)}
	done := make(chan error, 1)
	go func() { done <- ServeEngine(owner, in, out, e) }()
	remote := NewRemoteEngine(owner, parentIn, parentOut)
	defer remote.Close()
	healthy, err := remote.DialIP(owner, "tcp", netip.MustParseAddrPort("100.64.0.2:1234"))
	if err != nil {
		t.Fatal(err)
	}
	defer healthy.Close()
	ctx, cancel := context.WithCancel(owner)
	result := make(chan error, 1)
	go func() { _, err := remote.DialIP(ctx, "tcp", netip.MustParseAddrPort("100.64.0.2:9001")); result <- err }()
	<-e.entered
	cancel()
	if err = <-result; err == nil {
		t.Fatal("canceled dial succeeded")
	}
	late := <-e.peer
	defer late.Close()
	close(e.release)
	_ = late.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = late.Read(make([]byte, 1)); err != io.EOF {
		t.Fatal("abandoned child handle survived", err)
	}
	written := make(chan error, 1)
	go func() { _, err := healthy.Write([]byte("alive")); written <- err }()
	got := make([]byte, 5)
	if _, err = io.ReadFull(healthy, got); err != nil || string(got) != "alive" {
		t.Fatal("unrelated stream was retired", string(got), err)
	}
	if err = <-written; err != nil {
		t.Fatal(err)
	}
	_ = remote.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("owner shutdown leaked")
	}
}
func TestAlreadyCanceledCallDoesNotReachWorker(t *testing.T) {
	in, parentOut := io.Pipe()
	parentIn, out := io.Pipe()
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	calls := make(chan struct{}, 1)
	go func() {
		_ = Serve(owner, in, out, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			calls <- struct{}{}
			return json.RawMessage(`{}`), nil
		})
	}()
	client := NewClient(parentIn, parentOut)
	defer client.Close()
	ctx, cancel := context.WithCancel(owner)
	cancel()
	if err := client.Call(ctx, "state", nil, nil); err != context.Canceled {
		t.Fatal(err)
	}
	select {
	case <-calls:
		t.Fatal("already canceled call reached worker")
	case <-time.After(20 * time.Millisecond):
	}
}

type controlCancelEngine struct {
	pipeEngine
	method  string
	entered chan struct{}
}

func (e *controlCancelEngine) State(ctx context.Context) (json.RawMessage, error) {
	if e.method == "state" {
		close(e.entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return e.pipeEngine.State(ctx)
}
func (e *controlCancelEngine) WhoIs(ctx context.Context, ap netip.AddrPort) (string, error) {
	if e.method == "whois" {
		close(e.entered)
		<-ctx.Done()
		return "", ctx.Err()
	}
	return e.pipeEngine.WhoIs(ctx, ap)
}
func TestCanceledControlKeepsEstablishedStream(t *testing.T) {
	for _, method := range []string{"state", "whois"} {
		t.Run(method, func(t *testing.T) {
			in, parentOut := io.Pipe()
			parentIn, out := io.Pipe()
			owner, cancelOwner := context.WithCancel(context.Background())
			defer cancelOwner()
			e := &controlCancelEngine{method: method, entered: make(chan struct{})}
			go func() { _ = ServeEngine(owner, in, out, e) }()
			remote := NewRemoteEngine(owner, parentIn, parentOut)
			defer remote.Close()
			healthy, err := remote.DialIP(owner, "tcp", netip.MustParseAddrPort("100.64.0.2:1234"))
			if err != nil {
				t.Fatal(err)
			}
			defer healthy.Close()
			ctx, cancel := context.WithCancel(owner)
			done := make(chan error, 1)
			go func() {
				if method == "state" {
					_, err := remote.State(ctx)
					done <- err
				} else {
					_, err := remote.WhoIs(ctx, netip.MustParseAddrPort("100.64.0.2:1234"))
					done <- err
				}
			}()
			<-e.entered
			cancel()
			if err = <-done; err == nil {
				t.Fatal("canceled control succeeded")
			}
			written := make(chan error, 1)
			go func() { _, err := healthy.Write([]byte("kept")); written <- err }()
			got := make([]byte, 4)
			if _, err = io.ReadFull(healthy, got); err != nil || string(got) != "kept" {
				t.Fatal("control cancellation closed an unrelated flow", err)
			}
			if err = <-written; err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestCanceledCallsRetainBudgetUntilLateResponse(t *testing.T) {
	in, parentOut := io.Pipe()
	parentIn, out := io.Pipe()
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	limits := Limits{FrameBytes: 128 << 10, Requests: 8, Handles: 8}
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	go func() {
		_ = Serve(owner, in, out, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			entered <- struct{}{}
			<-release
			return json.RawMessage(`{}`), nil
		}, limits)
	}()
	client := NewClient(parentIn, parentOut, limits)
	defer client.Close()
	for i := 0; i < limits.dataSlots(); i++ {
		ctx, cancel := context.WithCancel(owner)
		done := make(chan error, 1)
		go func() { done <- client.Call(ctx, "work", nil, nil) }()
		<-entered
		cancel()
		if err := <-done; err == nil {
			t.Fatal("cancel succeeded")
		}
	}
	if err := client.Call(owner, "work", nil, nil); err != ErrBusy {
		t.Fatal("late work escaped the retained budget", err)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		client.mu.Lock()
		pending := client.dataPending
		client.mu.Unlock()
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("late responses did not retire request slots")
		}
		time.Sleep(time.Millisecond)
	}
}
