package backendworker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestRejectOversizeBeforeAllocation(t *testing.T) {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(MaxFrame+1))
	if _, e := readMessage(&b); !errors.Is(e, ErrProtocol) {
		t.Fatal(e)
	}
}
func TestConcurrentOwnerPipeRequests(t *testing.T) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, serverIn, serverOut, func(_ context.Context, m string, b json.RawMessage) (json.RawMessage, error) {
			if m != "echo" {
				return nil, ErrProtocol
			}
			return b, nil
		})
	}()
	client := NewClient(clientIn, clientOut)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var got int
			if e := client.Call(ctx, "echo", i, &got); e != nil || got != i {
				t.Errorf("got %d, %v", got, e)
			}
		}(i)
	}
	wg.Wait()
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not exit on owner EOF")
	}
}
func TestCancellationPreservesOtherRequests(t *testing.T) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, retired := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, serverIn, serverOut, func(ctx context.Context, method string, body json.RawMessage) (json.RawMessage, error) {
			if method == "read" {
				close(entered)
				<-ctx.Done()
				close(retired)
				return nil, ctx.Err()
			}
			return body, nil
		})
	}()
	client := NewClient(clientIn, clientOut)
	callctx, stop := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { result <- client.Call(callctx, "read", nil, nil) }()
	<-entered
	stop()
	select {
	case e := <-result:
		if e == nil {
			t.Fatal("cancel returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("call leaked")
	}
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("child request not canceled")
	}
	var got string
	if e := client.Call(ctx, "echo", "still-alive", &got); e != nil || got != "still-alive" {
		t.Fatal(got, e)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("owner EOF did not end worker")
	}
}

func TestCompletedCallCancellationDoesNotRetireWorker(t *testing.T) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Serve(ctx, serverIn, serverOut, func(_ context.Context, _ string, b json.RawMessage) (json.RawMessage, error) { return b, nil })
	}()
	client := NewClient(clientIn, clientOut)
	defer client.Close()
	for i := 0; i < 200; i++ {
		callctx, stop := context.WithCancel(ctx)
		var result int
		if e := client.Call(callctx, "echo", i, &result); e != nil {
			stop()
			t.Fatal(e)
		}
		stop()
		if result != i {
			t.Fatal(result)
		}
	}
}
