package directlan

import (
	"context"
	"net"
	"sync"

	"github.com/tailscale/wireguard-go/device"
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// PeerCapability freezes one old peer registration. It retains only the
// detachable origin and an inert registration number, never a raw Node/G/peer
// graph. The old G registry retains the exact selected peerState until cleanup.
type PeerCapability struct {
	origin       *generationOrigin
	registration device.PeerRegistration
}

func (n *Node) CapturePeer(key string) (*PeerCapability, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if e := n.readyLocked(); e != nil {
		return nil, e
	}
	g, p := n.generation.Load(), n.peers[key]
	if g == nil || p == nil || p.g != g || p.enginePeer == nil || !g.open() {
		return nil, ErrUntrusted
	}
	registration := p.enginePeer.Registration()
	g.mu.Lock()
	exact := registration != 0 && g.peerRegistrations[registration] == p
	g.mu.Unlock()
	if !exact {
		return nil, ErrUntrusted
	}
	return &PeerCapability{origin: g.origin, registration: registration}, nil
}
func (c *PeerCapability) Origin() transportorigin.Origin {
	if c == nil || c.origin == nil {
		return nil
	}
	return c.origin
}
func (c *PeerCapability) DialPeer(ctx context.Context, network string, port uint16) (net.Conn, error) {
	if c == nil || c.origin == nil || c.registration == 0 || ctx == nil {
		return nil, transportorigin.ErrMissingOrigin
	}
	g, e := c.origin.capture()
	if e != nil {
		return nil, e
	}
	defer c.origin.callDone()
	run, cancel := context.WithCancel(ctx)
	work, e := g.acquireWork(cancel, false)
	if e != nil {
		cancel()
		return nil, e
	}
	defer work.finish()
	g.mu.Lock()
	peer := g.peerRegistrations[c.registration]
	g.mu.Unlock()
	if peer == nil {
		return nil, ErrUntrusted
	}
	return g.n.dialCapturedPeer(run, g, peer, network, port)
}

// CaptureDial fixes network/port before the caller exposes a request. Capturing
// does no network work and grants no new permission. The only later Dial uses
// this immutable destination and the exact old registration above.
func (c *PeerCapability) CaptureDial(network string, port uint16) (transportorigin.DialCapability, error) {
	if c == nil || c.origin == nil || c.registration == 0 {
		return nil, transportorigin.ErrMissingOrigin
	}
	if !validService(network, port) {
		return nil, ErrUnavailable
	}
	return &peerDialCapability{peer: c, network: network, port: port}, nil
}
func (n *Node) CaptureDial(key, network string, port uint16) (transportorigin.DialCapability, error) {
	peer, e := n.CapturePeer(key)
	if e != nil {
		return nil, e
	}
	return peer.CaptureDial(network, port)
}

type peerDialCapability struct {
	peer    *PeerCapability
	network string
	port    uint16
}

func (c *peerDialCapability) Origin() transportorigin.Origin {
	if c == nil {
		return nil
	}
	return c.peer.Origin()
}
func (c *peerDialCapability) Dial(ctx context.Context) (net.Conn, error) {
	if c == nil || c.peer == nil {
		return nil, transportorigin.ErrMissingOrigin
	}
	return c.peer.DialPeer(ctx, c.network, c.port)
}

var _ transportorigin.DialCapability = (*peerDialCapability)(nil)

func (f *flow) TransportOrigin() transportorigin.Origin { return f.g.origin }
func (f *flow) WaitClosed(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.c.done:
		return nil
	}
}

// generationOrigin is the single attribution facade for G. Detachment drops G
// after cleanup and joins calls that already captured it; identity remains inert.
type generationOrigin struct {
	mu       sync.Mutex
	g        *runtimeGeneration
	identity *transportorigin.Token
	stop     <-chan struct{}
	calls    int
	changed  chan struct{}
}

func newGenerationOrigin(g *runtimeGeneration) *generationOrigin {
	return &generationOrigin{g: g, identity: transportorigin.NewToken(), stop: g.stop, changed: make(chan struct{})}
}
func (o *generationOrigin) Identity() *transportorigin.Token {
	if o == nil {
		return nil
	}
	return o.identity
}

var originStopped = func() <-chan struct{} { ch := make(chan struct{}); close(ch); return ch }()
var originClosedContext = func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }()

func (o *generationOrigin) StopRequested() <-chan struct{} {
	if o == nil {
		return originStopped
	}
	return o.stop
}
func (o *generationOrigin) capture() (*runtimeGeneration, error) {
	if o == nil {
		return nil, transportorigin.ErrMissingOrigin
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.g == nil {
		return nil, transportorigin.ErrStopped
	}
	o.calls++
	return o.g, nil
}
func (o *generationOrigin) callDone() {
	o.mu.Lock()
	o.calls--
	close(o.changed)
	o.changed = make(chan struct{})
	o.mu.Unlock()
}
func (o *generationOrigin) Acquire(ctx context.Context) (transportorigin.Lease, error) {
	return o.acquire(ctx, false)
}
func (o *generationOrigin) AcquirePublication(ctx context.Context) (transportorigin.Lease, error) {
	return o.acquire(ctx, true)
}
func (o *generationOrigin) acquire(ctx context.Context, publication bool) (transportorigin.Lease, error) {
	if ctx == nil {
		return nil, transportorigin.ErrMissingOrigin
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	g, e := o.capture()
	if e != nil {
		return nil, e
	}
	defer o.callDone()
	run, cancel := context.WithCancel(ctx)
	var stop context.CancelFunc
	if !publication {
		stop = cancel
	}
	work, e := g.acquireWork(stop, false)
	if e != nil {
		cancel()
		return nil, e
	}
	if e := run.Err(); e != nil {
		cancel()
		work.finish()
		return nil, e
	}
	return &originLease{ctx: run, cancel: cancel, work: work}, nil
}
func (o *generationOrigin) detach() {
	o.mu.Lock()
	o.g = nil
	for o.calls != 0 {
		changed := o.changed
		o.mu.Unlock()
		<-changed
		o.mu.Lock()
	}
	o.mu.Unlock()
}

// An old retained Lease becomes an inert object on release. Cancellation and
// completion run outside its mutex; only its real caller releases the work.
type originLease struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	work   *generationWork
}

func (l *originLease) Context() context.Context {
	if l == nil {
		return originClosedContext
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx == nil {
		return originClosedContext
	}
	return l.ctx
}
func (l *originLease) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	cancel, work := l.cancel, l.work
	l.ctx, l.cancel, l.work = nil, nil, nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	work.finish()
}

var _ transportorigin.Origin = (*generationOrigin)(nil)
var _ transportorigin.Lease = (*originLease)(nil)
var _ transportorigin.TerminalConnection = (*flow)(nil)

// TransportRecovering identifies only a started generation whose terminal
// admission fence has closed. It does not certify cleanup or a replacement,
// and does not reinterpret a stopped Node or an unrelated persistence error.
func (n *Node) TransportRecovering() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	g := n.generation.Load()
	return !n.closed && !n.closing.Load() && n.started && !n.nonTransportRecovery && g != nil && !g.open()
}
