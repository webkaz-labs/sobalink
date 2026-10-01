package policy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type snapshotStore struct {
	mu       sync.RWMutex
	snapshot Snapshot
	failed   bool
	calls    atomic.Int64
}

func (s *snapshotStore) source(context.Context) (Snapshot, error) {
	s.calls.Add(1)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.failed {
		return Snapshot{}, errors.New("status unavailable")
	}
	out := s.snapshot
	out.Peers = append([]Peer(nil), s.snapshot.Peers...)
	for i := range out.Peers {
		out.Peers[i].IPs = append([]netip.Addr(nil), out.Peers[i].IPs...)
	}
	return out, nil
}

func (s *snapshotStore) change(fn func(*Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.snapshot)
}

type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }

type fakeConn struct {
	writes     atomic.Int64
	closes     atomic.Int64
	halfCloses atomic.Int64
	payload    []byte
}

func (c *fakeConn) Read(b []byte) (int, error) {
	if c.closes.Load() != 0 {
		return 0, net.ErrClosed
	}
	return copy(b, c.payload), nil
}
func (c *fakeConn) Write(b []byte) (int, error) {
	if c.closes.Load() != 0 {
		return 0, net.ErrClosed
	}
	c.writes.Add(1)
	return len(b), nil
}
func (c *fakeConn) Close() error                     { c.closes.Add(1); return nil }
func (c *fakeConn) CloseWrite() error                { c.halfCloses.Add(1); return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return fakeAddr("100.64.1.1:12345") }
func (c *fakeConn) RemoteAddr() net.Addr             { return fakeAddr("100.64.1.2:21116") }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

func liveFixture(network string) (*Policy, *snapshotStore, *fakeConn) {
	store := &snapshotStore{snapshot: Snapshot{Running: true, Peers: []Peer{
		{ID: "original", DNSName: "server.example.ts.net.", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.2")}},
		{ID: "other", DNSName: "other.example.ts.net.", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.3")}},
	}}}
	conn := &fakeConn{payload: []byte("payload")}
	p := &Policy{Rules: []Rule{{Host: "server.example.ts.net", Port: 21116, Network: network}}, Source: store.source,
		DialIP: func(context.Context, string, netip.AddrPort) (net.Conn, error) { return conn, nil }}
	return p, store, conn
}

func TestLiveIdentityRevalidation(t *testing.T) {
	for _, change := range []string{"rename", "address reassigned", "removed", "expired", "offline", "source failure"} {
		t.Run(change, func(t *testing.T) {
			p, store, raw := liveFixture("tcp")
			conn, err := p.Dial(context.Background(), "tcp", "server:21116")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := p.RevalidateActive(context.Background()); err != nil {
				t.Fatal(err)
			}
			store.change(func(s *Snapshot) {
				switch change {
				case "rename":
					s.Peers[0].DNSName = "renamed.example.ts.net."
					s.Peers[1].DNSName = "server.example.ts.net."
				case "address reassigned":
					s.Peers[0].ID = "replacement-identity"
				case "removed":
					s.Peers = s.Peers[1:]
				case "expired":
					s.Peers[0].Expired = true
				case "offline":
					s.Running = false
				case "source failure":
					store.failed = true
				}
			})
			if err := p.RevalidateActive(context.Background()); err == nil {
				t.Fatal("stale identity passed health check")
			}
			if raw.closes.Load() != 1 {
				t.Fatalf("socket close count %d", raw.closes.Load())
			}
			if _, err := conn.Write([]byte("late")); err == nil {
				t.Fatal("closed socket accepted traffic")
			}
			p.mu.Lock()
			active := len(p.active)
			p.mu.Unlock()
			if active != 0 {
				t.Fatal("closed socket remained tracked")
			}
		})
	}
}

func TestUDPPinnedValidationRejectsBothDirections(t *testing.T) {
	for _, direction := range []string{"write", "read"} {
		t.Run(direction, func(t *testing.T) {
			p, store, raw := liveFixture("udp")
			conn, err := p.Dial(context.Background(), "udp", "server:21116")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.Write([]byte("allowed")); err != nil {
				t.Fatal(err)
			}
			store.change(func(s *Snapshot) {
				s.Peers[0].DNSName = "renamed.example.ts.net."
				s.Peers[1].DNSName = "server.example.ts.net."
			})
			// The configured name is still valid, but the original connection is not.
			if err := p.Validate(context.Background(), "udp", "server:21116"); err != nil {
				t.Fatalf("replacement peer should resolve: %v", err)
			}
			if direction == "write" {
				if n, err := conn.Write([]byte("denied")); n != 0 || err == nil {
					t.Fatal("revoked UDP write escaped")
				}
				if raw.writes.Load() != 1 {
					t.Fatal("revoked bytes reached underlying Write")
				}
			} else {
				buffer := make([]byte, 7)
				if n, err := conn.Read(buffer); n != 0 || err == nil {
					t.Fatal("revoked UDP reply escaped")
				}
				for _, b := range buffer {
					if b != 0 {
						t.Fatal("revoked data remained in caller buffer")
					}
				}
			}
			if raw.closes.Load() != 1 {
				t.Fatal("revoked socket was not closed")
			}
		})
	}
}

func TestTCPWrapperPreservesHalfCloseAndAvoidsPerChunkQueries(t *testing.T) {
	p, store, raw := liveFixture("tcp")
	conn, err := p.Dial(context.Background(), "tcp", "server:21116")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	queries := store.calls.Load()
	for i := 0; i < 20; i++ {
		if _, err := conn.Write([]byte("data")); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Read(make([]byte, 20)); err != nil {
			t.Fatal(err)
		}
	}
	if store.calls.Load() != queries {
		t.Fatal("TCP performed per-chunk status queries")
	}
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if raw.halfCloses.Load() != 1 {
		t.Fatal("half-close was not forwarded")
	}
	if err := p.RevalidateActive(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.calls.Load() != queries+1 {
		t.Fatal("health check should obtain exactly one snapshot")
	}
}

func TestRevocationDuringDialClosesSocket(t *testing.T) {
	p, store, raw := liveFixture("tcp")
	p.DialIP = func(context.Context, string, netip.AddrPort) (net.Conn, error) {
		store.change(func(s *Snapshot) { s.Peers[0].ID = "replacement" })
		return raw, nil
	}
	if conn, err := p.Dial(context.Background(), "tcp", "server:21116"); conn != nil || err == nil {
		t.Fatal("revocation during dial escaped")
	}
	if raw.closes.Load() != 1 {
		t.Fatal("revoked dial leaked socket")
	}
}

func TestUDPValidationCanceledByClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, _, raw := liveFixture("udp")
		conn, err := p.Dial(context.Background(), "udp", "server:21116")
		if err != nil {
			t.Fatal(err)
		}
		started := make(chan struct{})
		p.Source = func(ctx context.Context) (Snapshot, error) {
			close(started)
			<-ctx.Done()
			return Snapshot{}, ctx.Err()
		}
		done := make(chan error, 1)
		go func() { _, err := conn.Write([]byte("waiting")); done <- err }()
		<-started
		conn.Close()
		synctest.Wait()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("write accepted after close")
			}
		default:
			t.Fatal("close did not cancel policy lookup")
		}
		if raw.writes.Load() != 0 {
			t.Fatal("blocked packet reached socket")
		}
	})
}

func TestConcurrentRevalidationAndClose(t *testing.T) {
	p, store, raw := liveFixture("udp")
	conn, err := p.Dial(context.Background(), "udp", "server:21116")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 25; j++ {
				conn.Write([]byte("data"))
				conn.Read(make([]byte, 10))
				p.RevalidateActive(context.Background())
			}
		}()
	}
	store.change(func(s *Snapshot) { s.Peers[0].Expired = true })
	conn.Close()
	workers.Wait()
	if raw.closes.Load() != 1 {
		t.Fatalf("non-idempotent close: %d", raw.closes.Load())
	}
}

func TestDialFailureDoesNotLeakReturnedConnection(t *testing.T) {
	p, _, raw := liveFixture("tcp")
	p.DialIP = func(context.Context, string, netip.AddrPort) (net.Conn, error) { return raw, io.ErrUnexpectedEOF }
	if _, err := p.Dial(context.Background(), "tcp", "server:21116"); err == nil {
		t.Fatal("dial error lost")
	}
	if raw.closes.Load() != 1 {
		t.Fatal("connection accompanying dial error leaked")
	}
}
