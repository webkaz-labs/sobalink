package transport

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

// These helpers are for synctest bubbles only: every wait uses in-bubble pipes
// and channels, so native I/O scheduling cannot consume a protocol deadline.
func virtualStreamClient(t *testing.T, listener *pipeListener) (net.Conn, <-chan struct{}) {
	t.Helper()
	local, client := net.Pipe()
	closed := make(chan struct{})
	listener.queue <- &trackedConn{Conn: local, closed: closed}
	return client, closed
}

func virtualStreamServer(t *testing.T, handler func(*Server, net.Conn)) (*Server, net.Conn, <-chan struct{}) {
	t.Helper()
	listener := &pipeListener{queue: make(chan net.Conn, 1), done: make(chan struct{})}
	s := startServer(context.Background(), listener, listener.Addr(), func(s *Server) { acceptConnections(s, listener, func(c net.Conn) { handler(s, c) }) })
	client, closed := virtualStreamClient(t, listener)
	return s, client, closed
}

func assertChannelOpen(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal(message)
	default:
	}
}

func assertChannelClosed(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	default:
		t.Fatal(message)
	}
}

func TestProcessStreamCapAndCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		if defaultController.Usage().TCPConnections != 0 {
			t.Fatal("existing stream slots")
		}
		var reservations []func()
		for range defaultTCPConnections - 1 {
			release, ok := AdmitTCP()
			if !ok {
				t.Fatal("stream budget unavailable")
			}
			reservations = append(reservations, release)
		}
		defer func() {
			for _, release := range reservations {
				release()
			}
		}()
		listener := &pipeListener{queue: make(chan net.Conn, 2), done: make(chan struct{})}
		var handlers atomic.Int32
		s := startServer(t.Context(), listener, listener.Addr(), func(s *Server) { acceptConnections(s, listener, func(c net.Conn) { handlers.Add(1); <-s.ctx.Done() }) })
		defer closeServer(t, s)
		first, _ := virtualStreamClient(t, listener)
		defer first.Close()
		synctest.Wait()
		second, closed := virtualStreamClient(t, listener)
		defer second.Close()
		synctest.Wait()
		assertChannelClosed(t, closed, "excess process stream admitted")
		if handlers.Load() != 1 {
			t.Fatal("stream cap not enforced")
		}
		closeServer(t, s)
		if defaultController.Usage().TCPConnections != defaultTCPConnections-1 {
			t.Fatal("stream slot leaked")
		}
	})
}
