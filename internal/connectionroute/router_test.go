package connectionroute

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type testRoute struct {
	id, backend string
	auth        error
	dial        func() (net.Conn, error)
	calls       atomic.Int32
}

func (r *testRoute) ID() string                                      { return r.id }
func (r *testRoute) Backend() string                                 { return r.backend }
func (r *testRoute) Authorize(context.Context, Request) error        { return r.auth }
func (r *testRoute) Dial(context.Context, Request) (net.Conn, error) { r.calls.Add(1); return r.dial() }
func testPolicy(ids ...string) Policy {
	return Policy{RouteIDs: ids, AttemptTimeout: time.Second, HoldDown: time.Minute, MaxFlows: 2}
}
func testRequest() Request {
	return Request{PeerID: "peer:synthetic", ResourceID: "resource:synthetic", Network: "tcp", Port: 1234}
}
func TestDeniedNeverFallsBack(t *testing.T) {
	a := &testRoute{id: "lan", backend: "direct-lan", auth: ErrDenied}
	b := &testRoute{id: "wan", backend: "wan"}
	r, e := New(testPolicy("lan", "wan"), []Route{a, b})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Dial(context.Background(), testRequest()); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if b.calls.Load() != 0 {
		t.Fatal("denied route fell back")
	}
}
func TestUnavailableFallbackAndRevoke(t *testing.T) {
	a := &testRoute{id: "lan", backend: "direct-lan", auth: ErrUnavailable}
	local, remote := net.Pipe()
	defer remote.Close()
	b := &testRoute{id: "wan", backend: "wan", dial: func() (net.Conn, error) { return local, nil }}
	r, _ := New(testPolicy("lan", "wan"), []Route{a, b})
	c, e := r.Dial(context.Background(), testRequest())
	if e != nil {
		t.Fatal(e)
	}
	r.Invalidate()
	if _, e = c.Write([]byte{1}); e == nil {
		t.Fatal("revoked flow stayed open")
	}
}
func TestStrictLANCannotSelectWAN(t *testing.T) {
	p := testPolicy("wan")
	p.StrictLAN = true
	if _, e := New(p, []Route{&testRoute{id: "wan", backend: "wan"}}); e == nil {
		t.Fatal("strict LAN widened")
	}
}
func TestRevokeDuringDial(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	local, remote := net.Pipe()
	defer remote.Close()
	a := &testRoute{id: "lan", backend: "direct-lan", dial: func() (net.Conn, error) { close(entered); <-release; return local, nil }}
	r, _ := New(testPolicy("lan"), []Route{a})
	result := make(chan error, 1)
	go func() { _, e := r.Dial(context.Background(), testRequest()); result <- e }()
	<-entered
	r.Invalidate()
	close(release)
	if e := <-result; !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
}
