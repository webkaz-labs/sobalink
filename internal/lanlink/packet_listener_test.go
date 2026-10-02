package lanlink

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"tailscale.com/types/key"
	"testing"
	"time"
)

type fakeListener struct {
	accept chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newFakeListener() *fakeListener {
	return &fakeListener{accept: make(chan net.Conn, 4), closed: make(chan struct{})}
}
func (l *fakeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.accept:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *fakeListener) Close() error { l.once.Do(func() { close(l.closed) }); return nil }
func (l *fakeListener) Addr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("fd7a:115c:a1e0::1"), Port: 1234}
}

type fakeDatagrams struct {
	recv, sent chan []byte
	closed     chan struct{}
	once       sync.Once
	remote     net.Addr
}

func newFakeDatagrams(port int) *fakeDatagrams {
	return &fakeDatagrams{recv: make(chan []byte, 256), sent: make(chan []byte, 4), closed: make(chan struct{}), remote: &net.UDPAddr{IP: net.ParseIP("fd7a:115c:a1e0::2"), Port: port}}
}
func (c *fakeDatagrams) Read(b []byte) (int, error) {
	select {
	case data := <-c.recv:
		return copy(b, data), nil
	case <-c.closed:
		return 0, net.ErrClosed
	}
}
func (c *fakeDatagrams) Write(b []byte) (int, error) {
	select {
	case c.sent <- append([]byte(nil), b...):
		return len(b), nil
	case <-c.closed:
		return 0, net.ErrClosed
	}
}
func (c *fakeDatagrams) Close() error { c.once.Do(func() { close(c.closed) }); return nil }
func (c *fakeDatagrams) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("fd7a:115c:a1e0::1"), Port: 1234}
}
func (c *fakeDatagrams) RemoteAddr() net.Addr             { return c.remote }
func (c *fakeDatagrams) SetDeadline(time.Time) error      { return nil }
func (c *fakeDatagrams) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeDatagrams) SetWriteDeadline(time.Time) error { return nil }
func (c *fakeDatagrams) ReadFrom(b []byte) (int, net.Addr, error) {
	n, e := c.Read(b)
	return n, c.remote, e
}
func (c *fakeDatagrams) WriteTo(b []byte, a net.Addr) (int, error) {
	if a.String() != c.remote.String() {
		return 0, ErrUntrusted
	}
	return c.Write(b)
}
func TestUDPMuxBoundariesAndReply(t *testing.T) {
	ln := newFakeListener()
	p := newPacketListener(context.Background(), ln)
	defer p.Close()
	c := newFakeDatagrams(5555)
	ln.accept <- c
	c.recv <- []byte("one")
	c.recv <- []byte("two")
	p.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 32)
	for _, want := range []string{"one", "two"} {
		n, addr, e := p.ReadFrom(buf)
		if e != nil || string(buf[:n]) != want || addr.String() != c.remote.String() {
			t.Fatal("datagram boundary", e)
		}
	}
	if _, e := p.WriteTo([]byte("reply"), c.remote); e != nil {
		t.Fatal(e)
	}
	if got := <-c.sent; string(got) != "reply" {
		t.Fatal("reply payload")
	}
	if _, e := p.WriteTo([]byte("forbidden"), newFakeDatagrams(9999).remote); !errors.Is(e, ErrUntrusted) {
		t.Fatal("unobserved destination accepted", e)
	}
	if _, e := p.WriteTo(make([]byte, maxPacketSize+1), c.remote); e == nil {
		t.Fatal("oversized UDP accepted")
	}
	p.Close()
	select {
	case <-c.closed:
	default:
		t.Fatal("flow not closed")
	}
}
func TestUDPMuxDeadlineChangesAndCancelledStart(t *testing.T) {
	p := newPacketListener(context.Background(), newFakeListener())
	defer p.Close()
	done := make(chan error, 1)
	go func() { _, _, e := p.ReadFrom(make([]byte, 10)); done <- e }()
	p.SetReadDeadline(time.Now().Add(-time.Second))
	select {
	case e := <-done:
		if !errors.Is(e, os.ErrDeadlineExceeded) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("deadline did not wake read")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	other := newPacketListener(ctx, newFakeListener())
	if e := other.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestFailedRevokeSaveDoesNotDeadlockQueuedPair(t *testing.T) {
	host, client, _, role, frame, _, _ := pairFixture(t)
	req, peer, _, e := host.readRequest(frame, keyString(role.Public()))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &PairingServer{listener: newFakeListener(), node: host, ctx: ctx, cancel: cancel, conns: make(map[net.Conn]string), slots: make(chan struct{}, 8)}
	host.pairing = p
	entered := make(chan struct{})
	queued := make(chan struct{})
	host.cfg.Persist = func(Snapshot, []RemotePeer) error { close(entered); <-queued; return errors.New("save failed") }
	revokeDone := make(chan error, 1)
	go func() { revokeDone <- host.Revoke(client.PublicKey()) }()
	<-entered
	p.wg.Add(1)
	commitDone := make(chan error, 1)
	go func() {
		defer p.wg.Done()
		close(queued)
		commitDone <- host.commitPair(context.Background(), RemotePeer{Peer: peer, Address: req.Address, ClientPrivate: key.NewNode(), IncomingClientKey: req.RoleKey}, req.Token, 0)
	}()
	select {
	case e := <-revokeDone:
		if e == nil {
			t.Fatal("save failure hidden")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revoke shutdown deadlocked")
	}
	select {
	case e := <-commitDone:
		if e == nil {
			t.Fatal("late commit succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("pair handler did not stop")
	}
}

type deadlineWriter struct {
	*fakeDatagrams
	entered, expired      chan struct{}
	enterOnce, expireOnce sync.Once
}

func (c *deadlineWriter) Write(b []byte) (int, error) {
	c.enterOnce.Do(func() { close(c.entered) })
	select {
	case <-c.expired:
		return 0, os.ErrDeadlineExceeded
	case <-c.closed:
		return 0, net.ErrClosed
	}
}
func (c *deadlineWriter) SetWriteDeadline(t time.Time) error {
	if !t.IsZero() && !time.Now().Before(t) {
		c.expireOnce.Do(func() { close(c.expired) })
	}
	return nil
}
func TestChangedDeadlineInterruptsPendingUDPWrite(t *testing.T) {
	ln := newFakeListener()
	p := newPacketListener(context.Background(), ln)
	defer p.Close()
	c := &deadlineWriter{fakeDatagrams: newFakeDatagrams(6000), entered: make(chan struct{}), expired: make(chan struct{})}
	ln.accept <- c
	c.recv <- []byte("ready")
	p.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, e := p.ReadFrom(make([]byte, 10)); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := p.WriteTo([]byte("blocked"), c.remote); done <- e }()
	<-c.entered
	p.SetWriteDeadline(time.Now().Add(-time.Second))
	select {
	case e := <-done:
		if !errors.Is(e, os.ErrDeadlineExceeded) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("pending UDP write ignored changed deadline")
	}
}
func TestQueuedUDPDiscardedAfterRevocation(t *testing.T) {
	n := testNode()
	epoch := approve(t, n.cfg.Trust)
	raw := newFakeDatagrams(6100)
	wrapped, e := n.track(peer().Key, epoch, raw)
	if e != nil {
		t.Fatal(e)
	}
	ln := newFakeListener()
	p := newPacketListener(context.Background(), ln)
	defer p.Close()
	ln.accept <- wrapped
	raw.recv <- []byte("queued")
	packet := <-p.packets
	p.packets <- packet
	n.cfg.Trust.Revoke(peer().Key)
	p.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if count, _, e := p.ReadFrom(make([]byte, 20)); count != 0 || !errors.Is(e, os.ErrDeadlineExceeded) {
		t.Fatal("revoked queued datagram delivered", count, e)
	}
}
