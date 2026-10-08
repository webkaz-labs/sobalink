//go:build directlan_managed_session_tls

package directlan

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// Opt-in synthetic cleanup coverage. These fixtures have no OS sockets,
// resolver, dialer, WireGuard engine or transport startup. Helper cases below
// do not execute net.Dialer.DialContext or prove its real return behavior.
type managedCleanupAddr string

func (a managedCleanupAddr) Network() string { return "tcp" }
func (a managedCleanupAddr) String() string  { return string(a) }

type managedCleanupConn struct {
	closeErr, deadlineErr error
	remote                net.Addr
	closes                atomic.Int32
	deadlines             atomic.Int32
	reads                 atomic.Int32
	writes                atomic.Int32
}

func (c *managedCleanupConn) Read([]byte) (int, error) {
	c.reads.Add(1)
	return 0, net.ErrClosed
}
func (c *managedCleanupConn) Write([]byte) (int, error) {
	c.writes.Add(1)
	return 0, net.ErrClosed
}
func (c *managedCleanupConn) Close() error {
	c.closes.Add(1)
	return c.closeErr
}
func (c *managedCleanupConn) LocalAddr() net.Addr { return managedCleanupAddr("127.0.0.1:45101") }
func (c *managedCleanupConn) RemoteAddr() net.Addr {
	if c.remote != nil {
		return c.remote
	}
	return managedCleanupAddr("127.0.0.1:45102")
}
func (c *managedCleanupConn) SetDeadline(time.Time) error {
	c.deadlines.Add(1)
	return c.deadlineErr
}
func (c *managedCleanupConn) SetReadDeadline(d time.Time) error  { return c.SetDeadline(d) }
func (c *managedCleanupConn) SetWriteDeadline(d time.Time) error { return c.SetDeadline(d) }

type managedCleanupListener struct {
	conn    net.Conn
	err     error
	accepts atomic.Int32
	closes  atomic.Int32
}

func (l *managedCleanupListener) Accept() (net.Conn, error) {
	if l.accepts.Add(1) == 1 {
		return l.conn, l.err
	}
	return nil, net.ErrClosed
}
func (l *managedCleanupListener) Close() error {
	l.closes.Add(1)
	return nil
}
func (l *managedCleanupListener) Addr() net.Addr { return managedCleanupAddr("127.0.0.1:45101") }

func assertManagedCleanupRetained(t *testing.T, g *runtimeGeneration, raw *managedCleanupConn, failure error) {
	t.Helper()
	g.mu.Lock()
	count, work, sealed := g.controlCount, len(g.work), g.sealed
	retained := len(g.failedControl)
	var exact bool
	for owned, err := range g.failedControl {
		exact = owned.raw == raw && errors.Is(err, failure)
	}
	g.mu.Unlock()
	if !sealed || count != 1 || work != 0 || retained != 1 || !exact || raw.closes.Load() != 1 {
		t.Fatalf("cleanup ownership mismatch: sealed=%v charge=%d work=%d retained=%d exact=%v closes=%d", sealed, count, work, retained, exact, raw.closes.Load())
	}
	if _, err := g.acquireWork(nil, true); !errors.Is(err, ErrRecovery) {
		t.Fatalf("failed owner admitted new control work: %v", err)
	}
}

// This calls the shared production cleanup helper used by error+nonnil and
// stale/late dial branches. Branch selection and actual OS dialing remain
// source-inspection obligations, not behavior exercised by this fixture.
func TestManagedUnadmittedDialCleanupHelper(t *testing.T) {
	for _, kind := range []string{"error-with-connection", "late-after-stop", "stale-owner-result"} {
		t.Run(kind, func(t *testing.T) {
			n, g, _, _ := managedFixtureOwner(t)
			work, err := g.acquireWork(nil, true)
			if err != nil {
				t.Fatal(err)
			}
			cause := errors.New("synthetic dial failure")
			switch kind {
			case "late-after-stop":
				g.requestStop(ErrRecovery)
				cause = ErrUntrusted
			case "stale-owner-result":
				n.generation.Store(nil)
				cause = ErrUntrusted
			}
			failure := errors.New("synthetic unadmitted close failure")
			raw := &managedCleanupConn{closeErr: failure}
			var result error
			if kind == "error-with-connection" {
				result = closeUnadmittedControl(g, raw, cause)
			} else {
				result = closeUnadmittedControl(g, newControlStream(g, raw), cause)
			}
			work.finish()
			if !errors.Is(result, cause) || !errors.Is(result, failure) {
				t.Fatalf("cleanup discarded a cause: %v", result)
			}
			assertManagedCleanupRetained(t, g, raw, failure)
		})
	}
}

func TestManagedUnadmittedCleanupSuccessAndNil(t *testing.T) {
	_, g, _, _ := managedFixtureOwner(t)
	cause := errors.New("synthetic dial failure")
	if got := closeUnadmittedControl(g, nil, cause); got != cause {
		t.Fatal("nil connection changed the original error")
	}
	raw := &managedCleanupConn{}
	if got := closeUnadmittedControl(g, raw, cause); !errors.Is(got, cause) {
		t.Fatal("successful close discarded the original error")
	}
	if raw.closes.Load() != 1 || !g.open() || g.controlCount != 0 || len(g.failedControl) != 0 {
		t.Fatal("successful cleanup retained a failure or sealed the owner")
	}
}

func TestManagedRejectedAcceptCleanupBeforeIdentity(t *testing.T) {
	for _, withAcceptError := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid-address", true: "accept-error-with-connection"}[withAcceptError], func(t *testing.T) {
			n, g, _, _ := managedFixtureOwner(t)
			failure := errors.New("synthetic rejected close failure")
			raw := &managedCleanupConn{closeErr: failure, remote: managedCleanupAddr("invalid")}
			listener := &managedCleanupListener{conn: raw}
			if withAcceptError {
				listener.err = errors.New("synthetic accept failure")
			}
			u := newGenerationUnderlay(listener)
			t.Cleanup(func() {
				u.RequestClose()
				cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := u.WaitClosed(cleanup); !errors.Is(err, failure) {
					t.Errorf("rejected underlay cleanup incomplete or error lost: %v", err)
				}
			})
			g.underlay = u
			close(g.published)
			u.start(n, g)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := u.WaitClosed(ctx); !errors.Is(err, failure) {
				t.Fatalf("rejection close error lost: %v", err)
			}
			assertManagedCleanupRetained(t, g, raw, failure)
			if u.rejected != raw || listener.accepts.Load() != 1 || listener.closes.Load() != 1 || len(n.wires) != 0 || raw.reads.Load() != 0 {
				t.Fatal("rejected socket advanced to another accept or identity I/O")
			}
		})
	}
}

func TestManagedDeadlineFailureRetainsThroughGenerationCompletion(t *testing.T) {
	n, g, _, _ := managedFixtureOwner(t)
	failure := errors.New("synthetic close after deadline failure")
	deadlineErr := errors.New("synthetic deadline failure")
	raw := &managedCleanupConn{closeErr: failure, deadlineErr: deadlineErr}
	work, err := g.acquireWork(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	w := &wire{raw: newControlStream(g, raw), control: true, g: g, work: work, contextDeadline: time.Now().Add(time.Second)}
	n.wires[w] = struct{}{}
	// Exercise the production inbound SetDeadline rejection and removeWire
	// defers. No TLS handshake can start after the synthetic deadline failure.
	n.handle(w)
	assertManagedCleanupRetained(t, g, raw, failure)
	if raw.deadlines.Load() != 1 || raw.reads.Load() != 0 || raw.writes.Load() != 0 || len(n.wires) != 0 {
		t.Fatal("deadline failure did not stop before TLS I/O and release the wire")
	}
	// Only the ownership supervisor runs. The fixture deliberately has no
	// physical bind, underlay, tunnel or WG engine to start or stop.
	g.bind = nil
	close(g.built)
	go g.supervise()
	t.Cleanup(func() {
		g.requestStop(ErrRecovery)
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := g.wait(cleanup); !errors.Is(err, failure) {
			t.Errorf("supervisor cleanup incomplete or error lost: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if result := g.wait(ctx); !errors.Is(result, failure) {
		t.Fatalf("generation completion discarded retained close failure: %v", result)
	}
	if result := g.wait(ctx); !errors.Is(result, failure) {
		t.Fatalf("repeated completion discarded retained close failure: %v", result)
	}
	assertManagedCleanupRetained(t, g, raw, failure)
	g.origin.mu.Lock()
	retained := g.origin.g == g
	g.origin.mu.Unlock()
	if !retained {
		t.Fatal("failed cleanup detached the owner as if cleanup succeeded")
	}
}
