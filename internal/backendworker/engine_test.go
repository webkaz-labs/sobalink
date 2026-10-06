package backendworker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

type pipeEngine struct {
	mu    sync.Mutex
	conns []net.Conn
}

func (*pipeEngine) Start() error { return nil }
func (*pipeEngine) State(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"running":true}`), nil
}
func (*pipeEngine) Login(context.Context) error  { return nil }
func (*pipeEngine) Logout(context.Context) error { return nil }
func (e *pipeEngine) DialIP(ctx context.Context, network string, ap netip.AddrPort) (net.Conn, error) {
	a, b := net.Pipe()
	e.mu.Lock()
	e.conns = append(e.conns, a, b)
	e.mu.Unlock()
	go func() { defer b.Close(); _, _ = io.Copy(b, b) }()
	return a, nil
}
func (*pipeEngine) Listen(string, string) (net.Listener, error) { return nil, errors.New("unused") }
func (*pipeEngine) ListenPacket(string, string) (net.PacketConn, error) {
	return nil, errors.New("unused")
}
func (*pipeEngine) WhoIs(context.Context, netip.AddrPort) (string, error) {
	return "synthetic-backend-id", nil
}
func (e *pipeEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.conns {
		_ = c.Close()
	}
	return nil
}
func TestEnginePipeDataAndOwnerEOF(t *testing.T) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeEngine(ctx, serverIn, serverOut, &pipeEngine{}) }()
	remote := NewRemoteEngine(ctx, clientIn, clientOut)
	c, e := remote.DialIP(ctx, "tcp", netip.MustParseAddrPort("100.64.0.1:1234"))
	if e != nil {
		t.Fatal(e)
	}
	writeDone := make(chan error, 1)
	go func() { _, e := c.Write([]byte("hello")); writeDone <- e }()
	got := make([]byte, 5)
	if _, e = io.ReadFull(c, got); e != nil || string(got) != "hello" {
		t.Fatal(string(got), e)
	}
	if e = <-writeDone; e != nil {
		t.Fatal(e)
	}
	readDone := make(chan error, 1)
	go func() { _, e := c.Read(got); readDone <- e }()
	_ = remote.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("remote read leaked")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("engine leaked after owner EOF")
	}
}
