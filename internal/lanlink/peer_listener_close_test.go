package lanlink

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countedPeerCloseListener struct {
	calls    atomic.Int32
	called   chan struct{}
	firstErr error
	release  <-chan struct{}
}

func (l *countedPeerCloseListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (l *countedPeerCloseListener) Addr() net.Addr            { return &net.TCPAddr{} }
func (l *countedPeerCloseListener) Close() error {
	if l.calls.Add(1) != 1 {
		return net.ErrClosed
	}
	close(l.called)
	if l.release != nil {
		<-l.release
	}
	return l.firstErr
}
func TestPeerListenerContextCloseSharesResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := &countedPeerCloseListener{called: make(chan struct{})}
	listener := newPeerListener(ctx, nil, raw)
	cancel()
	select {
	case <-raw.called:
	case <-time.After(time.Second):
		t.Fatal("context callback did not close listener")
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("context and explicit close do not share ownership: closed=%t", err == net.ErrClosed)
	}
	if raw.calls.Load() != 1 {
		t.Fatal("listener closed physically more than once")
	}
}

func TestPeerListenerClosePreservesFirstResultForEveryOrder(t *testing.T) {
	sentinel := errors.New("synthetic listener close failure")
	for _, mode := range []string{"callback_first", "explicit_first", "already_canceled"} {
		for i, first := range []error{nil, net.ErrClosed, sentinel, errors.Join(net.ErrClosed, sentinel)} {
			t.Run(mode+string(rune('0'+i)), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "already_canceled" {
					cancel()
				}
				raw := &countedPeerCloseListener{called: make(chan struct{}), firstErr: first}
				listener := newPeerListener(ctx, nil, raw)
				if mode != "explicit_first" {
					cancel()
					select {
					case <-raw.called:
					case <-time.After(time.Second):
						t.Fatal("context close did not start")
					}
				}
				for j := 0; j < 3; j++ {
					if got := listener.Close(); got != first {
						t.Fatal("first physical close result changed")
					}
				}
				cancel()
				if raw.calls.Load() != 1 {
					t.Fatal("context and explicit owners physically reclosed")
				}
			})
		}
	}
}

func TestPeerListenerConcurrentCloseJoinsContextOwner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := errors.Join(net.ErrClosed, errors.New("synthetic genuine close failure"))
	release := make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	raw := &countedPeerCloseListener{called: make(chan struct{}), release: release, firstErr: first}
	listener := newPeerListener(ctx, nil, raw)
	cancel()
	select {
	case <-raw.called:
	case <-time.After(time.Second):
		t.Fatal("context close did not start")
	}
	started := make(chan struct{}, 16)
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func() { started <- struct{}{}; results <- listener.Close() }()
	}
	for i := 0; i < 16; i++ {
		<-started
	}
	select {
	case <-results:
		t.Fatal("explicit close returned before physical context close completed")
	default:
	}
	unblock.Do(func() { close(release) })
	for i := 0; i < 16; i++ {
		select {
		case got := <-results:
			if got != first {
				t.Fatal("concurrent owner lost first genuine close error")
			}
		case <-time.After(time.Second):
			t.Fatal("explicit close did not join context owner")
		}
	}
	if raw.calls.Load() != 1 {
		t.Fatal("concurrent owners physically reclosed")
	}
}
