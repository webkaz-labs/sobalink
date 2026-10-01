package transport

import (
	"context"
	"net"
	"testing"
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
