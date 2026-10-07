//go:build directlan_context_fixture

package directlan

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

const contextFixtureBound = 3 * time.Second

// ErrContextFixtureCleanup marks a failed cleanup bound, never an expected
// transport rejection. Callers must fail the case even if a later join succeeds.
var ErrContextFixtureCleanup = errors.New("context fixture cleanup remains unjoined")

// ContextFixtureReadGate is the single optional fixture barrier. Reached is
// closed by the first server Read, after production accept records its arm
// cutoff. Listener handoff alone is not evidence that this cutoff was captured.
// The gate has no callback and is never installed inside a production mutex.
type ContextFixtureReadGate struct {
	reached, release chan struct{}
	releaseOnce      sync.Once
	claimed          atomic.Bool
}

func NewContextFixtureReadGate() *ContextFixtureReadGate {
	return &ContextFixtureReadGate{reached: make(chan struct{}), release: make(chan struct{})}
}

func (g *ContextFixtureReadGate) Reached() <-chan struct{} { return g.reached }
func (g *ContextFixtureReadGate) Release() {
	g.releaseOnce.Do(func() { close(g.release) })
}

// ContextFixtureBridge owns one in-memory connection for one saved loopback
// peer. It exposes no connection, listener, proof, authentication override or
// generation replacement. The dedicated tag must remain absent from product
// build commands. Construction and all TLS work use only in-memory transport.
type ContextFixtureBridge struct {
	node              *Node
	listener          *contextFixtureListener
	server, client    *contextFixtureConnection
	clientCert        tls.Certificate
	mu                sync.Mutex
	submitted, closed bool
	exchangeDone      chan struct{}
	closeOnce         sync.Once
	closeDone         chan struct{}
	closeErr          error
}

func NewContextFixtureBridge(cfg ContextControlConfig, remote Identity, gate *ContextFixtureReadGate) (*ContextFixtureBridge, error) {
	// Ordinary validation below remains authoritative. These extra restrictions
	// close the fixture surface to explicit synthetic loopback configurations.
	if cfg.Completion == nil || cfg.ControlLimit <= 0 || !cfg.Listen.Addr().IsLoopback() || remote.Validate() != nil {
		return nil, ErrPolicy
	}
	for _, prefix := range cfg.AllowedPrefixes {
		if !prefix.IsValid() || !prefix.Addr().IsLoopback() || !prefixLast(prefix).IsLoopback() {
			return nil, ErrPolicy
		}
	}
	var selected *Peer
	for i := range cfg.Peers {
		peer := &cfg.Peers[i]
		if !peer.Endpoint.Addr().IsLoopback() {
			return nil, ErrPolicy
		}
		if peer.Key == remote.PublicKey() && peer.TunnelKey == remote.TunnelKey() {
			selected = peer
		}
	}
	if selected == nil {
		return nil, ErrUntrusted
	}
	cert, err := certificate(remote, time.Now())
	if err != nil {
		return nil, err
	}
	if gate != nil && (gate.reached == nil || gate.release == nil || !gate.claimed.CompareAndSwap(false, true)) {
		return nil, ErrUnavailable
	}
	n, err := NewContextControl(cfg)
	if err != nil {
		return nil, err
	}
	serverRaw, clientRaw := net.Pipe()
	local, peer := net.TCPAddrFromAddrPort(cfg.Listen), net.TCPAddrFromAddrPort(selected.Endpoint)
	f := &ContextFixtureBridge{node: n, clientCert: cert, exchangeDone: make(chan struct{}), closeDone: make(chan struct{})}
	f.server = &contextFixtureConnection{Conn: serverRaw, local: local, remote: peer, closed: make(chan struct{}), gate: gate}
	f.client = &contextFixtureConnection{Conn: clientRaw, local: peer, remote: local, closed: make(chan struct{})}
	f.listener = &contextFixtureListener{address: local, handoff: make(chan net.Conn), closed: make(chan struct{})}

	// Only the generation's existing supervisor and accept loop own production
	// work. No wire is assembled or registered here. Every saved peer receives
	// its exact generation-specific registration before publication.
	g := newRuntimeGeneration(n, nil, nil)
	g.controlPeers = make(map[string]*contextPeerRegistration, len(n.cfg.Peers))
	g.failedControl = make(map[*controlStream]error)
	for _, saved := range n.cfg.Peers {
		g.controlPeers[saved.Key] = &contextPeerRegistration{g: g, peer: saved}
	}
	g.underlay = newGenerationUnderlay(f.listener)
	go g.supervise()
	g.underlay.start(n, g)
	n.mu.Lock()
	// Match initial publication's Node -> generation admission order. Nothing
	// exposes this Node until its complete, immutable control owner is ready.
	g.admit(func() bool {
		n.peers = g.peers
		n.underlay = g.underlay.listener
		n.generation.Store(g)
		n.started = true
		g.controlOpen.Store(true)
		close(g.published)
		return true
	})
	n.mu.Unlock()
	// Cleanup reads these resources only after the full construction boundary.
	close(g.built)
	return f, nil
}

func (f *ContextFixtureBridge) Node() *Node { return f.node }

// Exchange sends exactly one copied frame over production-pinned TLS 1.3/v2.
// A nil request is the sole peer-close mode: complete TLS, then close without a
// frame. Every first call is terminal, including invalid or cancelled inputs.
// Return waits for raw closure, cancellation ownership and the real Node join.
// A cleanup timeout is an error and leaves the retained fixture owner unjoined;
// callers must fail without inspecting completion-owned observations afterward.
func (f *ContextFixtureBridge) Exchange(ctx context.Context, request []byte) (reply []byte, result error) {
	f.mu.Lock()
	if f.submitted || f.closed {
		f.mu.Unlock()
		return nil, ErrUnavailable
	}
	f.submitted = true
	f.mu.Unlock()
	defer close(f.exchangeDone)
	var cancel context.CancelFunc
	var stopConnection func()
	defer func() {
		_ = f.client.Close()
		_ = f.server.Close()
		if cancel != nil {
			cancel()
		}
		if stopConnection != nil {
			stopConnection()
		}
		f.shutdown()
		result = errors.Join(result, f.waitClosed(false))
		if result != nil {
			reply = nil
		} else {
			reply = append([]byte(nil), reply...)
		}
	}()
	if ctx == nil || len(request) > endpointmeta.MaxFrameBytes {
		return nil, ErrPolicy
	}
	frame := append([]byte(nil), request...)
	run, finish := context.WithTimeout(ctx, contextFixtureBound)
	cancel = finish
	f.server.run = run
	if err := run.Err(); err != nil {
		return nil, err
	}
	deadline, _ := run.Deadline()
	if err := f.client.SetDeadline(deadline); err != nil {
		return nil, err
	}
	stopConnection = watchConnection(run, f.client)
	select {
	case <-run.Done():
		return nil, run.Err()
	case <-f.listener.closed:
		return nil, net.ErrClosed
	case f.listener.handoff <- f.server:
		// Production accept owns the delivered end, including any rejection.
	}
	client := tls.Client(f.client, tlsConfigProtocol(f.clientCert, f.node.PublicKey(), false, contextProtocolName))
	if err := client.HandshakeContext(run); err != nil {
		return nil, err
	}
	if state := client.ConnectionState(); state.Version != tls.VersionTLS13 || state.NegotiatedProtocol != contextProtocolName {
		return nil, ErrUntrusted
	}
	if request == nil {
		return nil, io.EOF
	}
	if err := writeFrame(client, frame, endpointmeta.MaxFrameBytes); err != nil {
		return nil, err
	}
	return readFrame(client, endpointmeta.MaxFrameBytes)
}

// Close is registered by the caller immediately after construction, before
// assertions or arm setup. It is also safe before Exchange or concurrent with
// its client work. Completion callbacks must only signal Node.RequestClose;
// joining here from a completion callback would wait for that same callback.
func (f *ContextFixtureBridge) Close() error {
	f.shutdown()
	return f.waitClosed(true)
}

func (f *ContextFixtureBridge) shutdown() {
	f.closeOnce.Do(func() {
		f.mu.Lock()
		f.closed = true
		f.mu.Unlock()
		f.node.RequestClose()
		_ = f.client.Close()
		_ = f.server.Close()
		_ = f.listener.Close()
		go func() {
			f.closeErr = f.node.Close()
			// Node.Close has joined generation supervision and the accept loop.
			// No future accept can Add work now. Also join the handler epilogue
			// if another owner concurrently entered Node.Close's closed branch.
			f.node.wg.Wait()
			close(f.closeDone)
		}()
	})
}

func (f *ContextFixtureBridge) waitClosed(joinExchange bool) error {
	timer := time.NewTimer(contextFixtureBound)
	defer timer.Stop()
	select {
	case <-f.closeDone:
	case <-timer.C:
		return errors.Join(ErrContextFixtureCleanup, errors.New("Node cleanup deadline"))
	}
	if joinExchange {
		f.mu.Lock()
		submitted := f.submitted
		f.mu.Unlock()
		if submitted {
			select {
			case <-f.exchangeDone:
			case <-timer.C:
				return errors.Join(ErrContextFixtureCleanup, errors.New("client cleanup deadline"))
			}
		}
	}
	return f.closeErr
}

// The unbuffered handoff holds no pending slice or worker queue. Close wakes
// Accept idempotently. Racing delivery either transfers this one connection to
// production accept or fails; fixture shutdown closes both raw ends either way.
type contextFixtureListener struct {
	address net.Addr
	handoff chan net.Conn
	closed  chan struct{}
	once    sync.Once
}

func (l *contextFixtureListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case conn := <-l.handoff:
		return conn, nil
	}
}
func (l *contextFixtureListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}
func (l *contextFixtureListener) Addr() net.Addr { return l.address }

type contextFixtureConnection struct {
	net.Conn
	local, remote net.Addr
	closed        chan struct{}
	closeOnce     sync.Once
	closeErr      error
	gate          *ContextFixtureReadGate
	run           context.Context // assigned before the unbuffered handoff
	readOnce      sync.Once
	readErr       error
}

func (c *contextFixtureConnection) LocalAddr() net.Addr  { return c.local }
func (c *contextFixtureConnection) RemoteAddr() net.Addr { return c.remote }
func (c *contextFixtureConnection) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.closeErr = c.Conn.Close()
	})
	return c.closeErr
}
func (c *contextFixtureConnection) Read(p []byte) (int, error) {
	if c.gate != nil {
		c.readOnce.Do(func() {
			close(c.gate.reached)
			select {
			case <-c.gate.release:
			case <-c.closed:
				c.readErr = net.ErrClosed
			case <-c.run.Done():
				c.readErr = c.run.Err()
			}
		})
		if c.readErr != nil {
			return 0, c.readErr
		}
	}
	return c.Conn.Read(p)
}
