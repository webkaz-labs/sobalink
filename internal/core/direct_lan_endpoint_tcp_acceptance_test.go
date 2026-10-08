//go:build endpoint_following_acceptance

package core

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// These are new source-only acceptance assertions on the current production
// fixture. Their invariant overlaps a historical denied LIVE scenario whose
// exact command/rationale remain unrecovered. This is not execution clearance.
// No historical harness, transport replacement or authority injection is used.
type endpointTCPObservation struct {
	baseline bool
	extra    int
	terminal bool
}
type endpointTCPProbe struct {
	mu               sync.Mutex
	listener         *net.TCPListener
	connections      []net.Conn
	accepted         int
	observed         [2]endpointTCPObservation
	finished         [2]bool
	closing          bool
	done             chan struct{}
	records          chan endpointTCPObservation
	once             sync.Once
	closeErr         error
	workerErr        error
	reportedCloseErr error
}

func (p *endpointTCPProbe) retain(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.connections = append(p.connections, c)
	return !p.closing
}
func (p *endpointTCPProbe) closeClient(c net.Conn) error {
	err := c.Close()
	if !endpointTCPClosed(err) {
		p.mu.Lock()
		p.reportedCloseErr = errors.Join(p.reportedCloseErr, err)
		p.mu.Unlock()
	}
	return err
}
func (p *endpointTCPProbe) noteWorkerClose(err error) {
	if !endpointTCPClosed(err) {
		p.mu.Lock()
		p.workerErr = errors.Join(p.workerErr, err)
		p.mu.Unlock()
	}
}

// A local generation join does not certify that the independent remote Core's
// target socket closed. Observe the baseline stream without closing it.
func (p *endpointTCPProbe) requireNoExtra(f *endpointAcceptanceFixture) {
	f.t.Helper()
	p.mu.Lock()
	record, finished := p.observed[0], p.finished[0]
	p.mu.Unlock()
	if !record.baseline || record.extra != 0 || finished && !record.terminal {
		f.t.Fatal("owned target observed extra bytes or invalid baseline/terminal outcome")
	}
	f.t.Logf("remote target extra bytes=0 terminal_observed=%t", finished && record.terminal)
}
func (p *endpointTCPProbe) requireTerminalAt(f *endpointAcceptanceFixture, index int) {
	f.t.Helper()
	for {
		p.mu.Lock()
		record, finished := p.observed[index], p.finished[index]
		p.mu.Unlock()
		if finished {
			if !record.baseline || record.extra != 0 || !record.terminal {
				f.t.Fatal("explicit target connection did not finish cleanly")
			}
			return
		}
		select {
		case <-p.records:
		case <-f.ctx.Done():
			f.t.Fatal("explicit target connection did not finish")
		}
	}
}

// One exact owned-target sanity connection, not a peer/authentication path.
func (p *endpointTCPProbe) verifyLocalTarget(f *endpointAcceptanceFixture) {
	f.t.Helper()
	address := p.listener.Addr().(*net.TCPAddr).AddrPort()
	if address.Addr() != netip.MustParseAddr("127.0.0.1") {
		f.t.Fatal("local sanity target left owned loopback")
	}
	local, err := (&net.Dialer{}).DialContext(f.ctx, "tcp4", address.String())
	if err != nil {
		f.t.Fatal("owned application target no longer available")
	}
	if !p.retain(local) {
		_ = p.closeClient(local)
		f.t.Fatal("target probe closed")
	}
	deadline, _ := f.ctx.Deadline()
	if local.SetDeadline(deadline) != nil {
		f.t.Fatal("local target deadline failed")
	}
	if n, err := local.Write([]byte("NEW!")); n != 4 || err != nil {
		f.t.Fatal("owned target sanity write failed")
	}
	var reply [4]byte
	if n, err := io.ReadFull(local, reply[:]); n != 4 || err != nil || string(reply[:]) != "NEW!" {
		f.t.Fatal("owned target sanity response failed")
	}
	if err := p.closeClient(local); !endpointTCPClosed(err) {
		f.t.Fatal("local sanity connection close failed")
	}
	p.requireTerminalAt(f, 1)
	p.mu.Lock()
	accepted := p.accepted
	p.mu.Unlock()
	if accepted != 2 {
		f.t.Fatal("unexpected target connection beyond explicit local sanity check")
	}
	p.requireNoExtra(f)
}
func endpointTCPClosed(err error) bool { return err == nil || errors.Is(err, net.ErrClosed) }
func (p *endpointTCPProbe) Close() error {
	p.once.Do(func() {
		p.mu.Lock()
		p.closing = true
		connections := append([]net.Conn(nil), p.connections...)
		p.mu.Unlock()
		// Initiate all independent closes before waiting for any. Worker goroutines
		// report only through buffered channels, never through testing.T.
		results := make(chan error, len(connections)+1)
		go func() { results <- p.listener.Close() }()
		for _, c := range connections {
			go func(c net.Conn) { results <- c.Close() }(c)
		}
		for i := 0; i < len(connections)+1; i++ {
			if err := <-results; !endpointTCPClosed(err) {
				p.closeErr = errors.Join(p.closeErr, err)
			}
		}
		<-p.done
		p.mu.Lock()
		p.closeErr = errors.Join(p.closeErr, p.workerErr, p.reportedCloseErr)
		// A worker can publish evidence after a testcase snapshot. After the
		// actual worker join, any extra byte remains a sticky test failure.
		for _, observation := range p.observed {
			if observation.extra != 0 {
				p.closeErr = errors.Join(p.closeErr, errors.New("owned target received unexpected extra bytes"))
			}
		}
		p.mu.Unlock()
	})
	return p.closeErr
}
func newEndpointTCPProbe(f *endpointAcceptanceFixture, a, b *Core) *endpointTCPProbe {
	f.t.Helper()
	raw, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		f.t.Fatal("owned application target bind failed")
	}
	p := &endpointTCPProbe{listener: raw.(*net.TCPListener), done: make(chan struct{}), records: make(chan endpointTCPObservation, 2)}
	f.resources = append(f.resources, p)
	deadline, _ := f.ctx.Deadline()
	// At most two accepts and two finite workers. Independent reads let the
	// second explicit local sanity connection complete while a remote target
	// from an independently live Core remains idle. No unbounded copy loop.
	go func() {
		var workers sync.WaitGroup
		defer func() { workers.Wait(); close(p.done) }()
		if p.listener.SetDeadline(deadline) != nil {
			return
		}
		for i := 0; i < 2; i++ {
			conn, err := p.listener.AcceptTCP()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.accepted++
			p.mu.Unlock()
			if !p.retain(conn) {
				p.noteWorkerClose(conn.Close())
				return
			}
			workers.Add(1)
			go func(index int, conn *net.TCPConn) {
				defer workers.Done()
				record := endpointTCPObservation{}
				if conn.SetDeadline(deadline) == nil {
					var baseline [4]byte
					n, err := io.ReadFull(conn, baseline[:])
					expected := "PRE!"
					if index == 1 {
						expected = "NEW!"
					}
					record.baseline = n == 4 && err == nil && string(baseline[:]) == expected
					if record.baseline {
						p.mu.Lock()
						p.observed[index] = record
						p.mu.Unlock()
						n, err = conn.Write(baseline[:])
						if n == 4 && err == nil {
							var extra [1]byte
							record.extra, err = conn.Read(extra[:])
							var timeout net.Error
							record.terminal = err != nil && !(errors.As(err, &timeout) && timeout.Timeout())
						} else {
							record.baseline = false
						}
					}
				}
				// Publish received-byte evidence before any potentially delayed
				// physical Close; cleanup cannot hide an observed extra byte.
				p.mu.Lock()
				p.observed[index] = record
				p.mu.Unlock()
				if err := conn.Close(); !endpointTCPClosed(err) {
					p.noteWorkerClose(err)
					record.terminal = false
				}
				p.mu.Lock()
				p.observed[index] = record
				p.finished[index] = true
				p.mu.Unlock()
				p.records <- record
			}(i, conn)
		}
	}()
	endpoint := p.listener.Addr().(*net.TCPAddr).AddrPort()
	if endpoint.Addr() != netip.MustParseAddr("127.0.0.1") || endpoint.Port() < 1024 {
		f.t.Fatal("application target left owned numeric loopback")
	}
	key := b.directLANStoreCopy().copy().Identity.PublicKey()
	f.command(a, "service.share", map[string]any{"name": "synthetic-tcp-boundary", "network": "tcp", "ports": "32101", "localPort": int(endpoint.Port()), "loopbackHost": "127.0.0.1", "peerIds": []string{key}, "ttlSeconds": 120})
	return p
}
func (p *endpointTCPProbe) exchange(f *endpointAcceptanceFixture, from, to *Core, text string) net.Conn {
	f.t.Helper()
	key := to.directLANStoreCopy().copy().Identity.PublicKey()
	conn, err := from.dial(f.ctx, key, "tcp", 32101)
	if err != nil {
		f.t.Fatal("scoped production TCP dial failed")
	}
	if !p.retain(conn) {
		_ = conn.Close()
		f.t.Fatal("application probe already closed")
	}
	deadline, _ := f.ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		f.t.Fatal("application deadline failed")
	}
	if n, err := conn.Write([]byte(text)); err != nil || n != 4 {
		f.t.Fatal("bounded baseline write failed")
	}
	var reply [4]byte
	if n, err := io.ReadFull(conn, reply[:]); err != nil || n != 4 || string(reply[:]) != text {
		f.t.Fatal("bounded baseline exchange failed")
	}
	return conn
}
func (p *endpointTCPProbe) requireTerminal(f *endpointAcceptanceFixture) {
	f.t.Helper()
	select {
	case result := <-p.records:
		if !result.baseline || result.extra != 0 || !result.terminal {
			f.t.Fatal("old target stream received additional data or lacked actual terminal close")
		}
	case <-f.ctx.Done():
		f.t.Fatal("owned target stream did not finish")
	}
}

// Linux-only IP .1 -> .2 transition under two upfront /32 scopes. Binding .2
// must already work: no alias creation, settings change, probing or fallback.
// Only writes ATTEMPTED AFTER successful publication and exact physical join
// are tested. Baseline bytes are fully exchanged/drained before movement;
// this makes no assertion about retroactive erasure or queued pre-seal bytes.
func TestIntegratedEndpointIPMoveRejectsStaleTCP(t *testing.T) {
	var probe *endpointTCPProbe
	var stale net.Conn
	var staleTerminal transportorigin.TerminalConnection
	f, a, b := runIntegratedEndpointMoveDeliveryAt(t, netip.MustParseAddr("127.0.0.2"), func(f *endpointAcceptanceFixture, a, b *Core) {
		probe = newEndpointTCPProbe(f, a, b)
		stale = probe.exchange(f, b, a, "PRE!")
		var ok bool
		staleTerminal, ok = stale.(transportorigin.TerminalConnection)
		if !ok {
			t.Fatal("retained TCP stream lost production terminal ownership")
		}
		b.op.Lock()
		backend := b.endpointBackendLocked()
		origin, err := backend.Node.CaptureTransportOrigin()
		b.op.Unlock()
		if err != nil || staleTerminal.TransportOrigin() == nil || staleTerminal.TransportOrigin().Identity() != origin.Identity() {
			t.Fatal("retained TCP stream is not attributed to exact old generation")
		}
	})
	// The shared fixture has already required both real transaction receipts,
	// exact old retirement identities/physical joins and distinct fresh sessions.
	// Native physical joins are already proven by the shared fixture. Do not
	// close the policy wrapper before testing the stale underlying flow: that
	// would manufacture rejection. Its own WaitClosed requires explicit Close.
	if n, err := stale.Write([]byte("OLD!")); n != 0 || err == nil {
		t.Fatal("stale TCP handle accepted a post-publication write")
	}
	if err := probe.closeClient(stale); !endpointTCPClosed(err) {
		t.Fatal("stale policy wrapper cleanup failed")
	}
	if staleTerminal.WaitClosed(f.ctx) != nil {
		t.Fatal("stale policy wrapper cleanup did not join")
	}
	probe.requireTerminal(f)
	a.op.Lock()
	endpoint := a.endpointBackendLocked().Node.Endpoint()
	a.op.Unlock()
	if endpoint.Addr() != netip.MustParseAddr("127.0.0.2") {
		t.Fatal("fixture proved only a port change")
	}
	fresh := probe.exchange(f, b, a, "NEW!")
	carrier, ok := fresh.(transportorigin.Carrier)
	if !ok || carrier.TransportOrigin() == nil || carrier.TransportOrigin().Identity() == staleTerminal.TransportOrigin().Identity() {
		t.Fatal("new TCP stream reused old generation attribution")
	}
	if err := probe.closeClient(fresh); !endpointTCPClosed(err) {
		t.Fatal("fresh TCP close failed")
	}
	probe.requireTerminal(f)
	t.Log("verified Linux loopback IP movement and rejected post-publication stale-handle writes; queued pre-seal bytes not tested")
}
