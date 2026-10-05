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
func TestCancelClosesGeneration(t *testing.T) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, serverIn, serverOut, func(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
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
			t.Fatal("cancel succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("call leaked")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker handler leaked")
	}
	if e := client.Call(ctx, "echo", nil, nil); !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
}
