package directlan

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/tailscale/wireguard-go/device"
	"net"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const handshakeTimeout = 10 * time.Second

type peerState struct {
	peer          Peer
	session       *peerSession
	enginePeer    *device.OwnedPeer
	g             *runtimeGeneration
	binding       string                                // immutable; derived from the constructor context
	authenticated atomic.Pointer[managedAuthentication] // initially closed
}
type service struct {
	network string
	port    uint16
}
type wire struct {
	flow             *flow
	g                *runtimeGeneration
	work             *generationWork
	control          bool
	contextDeadline  time.Time
	contextArmCutoff uint64
	raw              net.Conn
	key              string
	peer             *peerState
	cancel           context.CancelFunc
	stopWatch        func()
	controlContext   context.Context
}

type pendingDial struct {
	g       *runtimeGeneration
	control bool
	key     string
	cancel  context.CancelFunc
}

type Node struct {
	mu                        sync.Mutex
	contextControl            bool // immutable; never grants application admission
	contextCompletion         ContextCompletion
	contextAttempts           map[contextAttemptKey]*ContextAttempt
	contextArmRevision        uint64
	cfg                       Config
	cert                      tls.Certificate
	peers                     map[string]*peerState
	invites                   map[string]pendingInvitation
	attempts                  map[string]context.CancelFunc
	wires                     map[*wire]struct{}
	dials                     map[*pendingDial]struct{}
	flows                     map[netip.AddrPort]map[*flow]struct{}
	listeners                 map[service]*listener
	fallback                  func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)
	underlay                  net.Listener
	started, closed, recovery bool
	nonTransportRecovery      bool
	closing                   atomic.Bool
	ctx                       context.Context
	cancel                    context.CancelFunc
	stop                      func() bool
	wg                        sync.WaitGroup
	nextPort                  uint16
	tunnel                    *userspaceTunnel
	engine                    *device.OwnedDevice
	generation                atomic.Pointer[runtimeGeneration]
	building                  atomic.Pointer[generationBuild]
	staged                    atomic.Pointer[PreparedTransport]
	bind                      *lanBind
	dispatchSlots             chan struct{}
}

// NewNode validates all policy and saved state before doing any network I/O.
func NewNode(cfg Config) (*Node, error) {
	cfg = cfg.withDefaults()
	cfg = cloneGenerationConfig(cfg)
	if e := cfg.Validate(); e != nil {
		return nil, e
	}
	cert, e := certificate(cfg.Identity, time.Now())
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	n := &Node{cfg: cfg, cert: cert, peers: map[string]*peerState{}, invites: map[string]pendingInvitation{}, attempts: map[string]context.CancelFunc{}, wires: map[*wire]struct{}{}, dials: map[*pendingDial]struct{}{}, flows: map[netip.AddrPort]map[*flow]struct{}{}, listeners: map[service]*listener{}, ctx: ctx, cancel: cancel, nextPort: 1024, dispatchSlots: make(chan struct{}, cfg.FlowLimit)}
	for _, p := range cfg.Peers {
		n.peers[p.Key] = n.newPeerState(p)
	}
	return n, nil
}
func (n *Node) PublicKey() string       { return n.cfg.Identity.PublicKey() }
func (n *Node) OverlayAddr() netip.Addr { a, _ := OverlayAddress(n.PublicKey()); return a }
func (n *Node) Endpoint() netip.AddrPort {
	if g := n.generation.Load(); g != nil {
		return g.cfg.Listen
	}
	return n.cfg.Listen
}
func (n *Node) Peers() []Peer { n.mu.Lock(); defer n.mu.Unlock(); return n.snapshotLocked() }
func (n *Node) snapshotLocked() []Peer {
	out := make([]Peer, 0, len(n.peers))
	for _, p := range n.peers {
		out = append(out, p.peer)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
func (n *Node) readyLocked() error {
	if n.contextControl {
		return ErrUnavailable
	}
	if n.closed || n.closing.Load() {
		return net.ErrClosed
	}
	if n.recovery {
		return ErrRecovery
	}
	if g := n.generation.Load(); g != nil && !g.trafficOpen() {
		return ErrRecovery
	}
	if !n.started {
		return ErrUnavailable
	}
	return nil
}

// Start opens exactly the selected numeric TCP tunnel endpoint. Failure is
// returned without changing its address, touching firewall policy or fallback.
func (n *Node) Start(ctx context.Context) error {
	return n.start(ctx, localAddressReady)
}

func (n *Node) start(ctx context.Context, checkAddress func(netip.Addr) error) error {
	n.mu.Lock()
	if n.closed || n.closing.Load() {
		n.mu.Unlock()
		return net.ErrClosed
	}
	if n.recovery {
		n.mu.Unlock()
		return ErrRecovery
	}
	if n.started {
		err := n.startReadyLocked()
		n.mu.Unlock()
		return err
	}
	if previous := n.building.Load(); previous != nil {
		n.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-previous.done:
			if previous.err != nil {
				return previous.err
			}
			n.mu.Lock()
			err := n.startReadyLocked()
			n.mu.Unlock()
			return err
		}
	}
	if n.generation.Load() != nil {
		n.mu.Unlock()
		return ErrRecovery
	}
	if err := ctx.Err(); err != nil {
		n.mu.Unlock()
		return err
	}
	if err := checkAddress(n.cfg.Listen.Addr()); err != nil {
		n.mu.Unlock()
		return err
	}
	cfg := cloneGenerationConfig(n.cfg)
	if !n.contextControl {
		cfg.Peers = n.snapshotLocked()
	}
	build := newGenerationBuild(ctx)
	n.building.Store(build)
	if n.closing.Load() {
		build.RequestStop()
	}
	n.mu.Unlock()

	// Stop can reach this unpublished owner while setup is outside Node.mu.
	callbackDone := make(chan struct{})
	stopCallback := context.AfterFunc(build.ctx, func() {
		build.signalResources()
		close(callbackDone)
	})
	var g *runtimeGeneration
	var err error
	if n.contextControl {
		g, err = n.buildContextGeneration(build, cfg)
	} else {
		g, err = n.buildTransportGeneration(build, cfg)
	}
	n.mu.Lock()
	if err == nil && (n.closed || n.closing.Load()) {
		err = net.ErrClosed
	}
	if err == nil {
		err = build.ctx.Err()
	}
	if err == nil {
		buildDone := build.ctx.Done()
		// Only initial Start may publish. There is no replacement entrypoint.
		if !g.admit(func() bool {
			if n.closing.Load() {
				return false
			}
			select {
			case <-buildDone:
				return false
			default:
			}
			n.peers = g.peers
			n.bind, n.tunnel, n.engine = g.bind, g.tunnel, g.engine.Load()
			n.underlay = g.underlay.listener
			n.generation.Store(g)
			n.started = true
			if n.contextControl {
				g.controlOpen.Store(true)
			} else {
				g.traffic.Store(true)
			}
			close(g.published)
			return true
		}) {
			err = ErrRecovery
		}
	}
	if err == nil {
		n.stop = context.AfterFunc(ctx, func() { n.Close() })
	} else if g != nil || build.cleanupErr != nil {
		n.recovery = true
		n.nonTransportRecovery = true
		if g != nil {
			g.requestStop(err)
		}
	}
	n.mu.Unlock()
	if !stopCallback() {
		<-callbackDone
	}
	build.cancel()
	build.err = err
	n.mu.Lock()
	// A failed live construction remains retained until actual cleanup joins.
	// A pre-generation error has already closed its only staged TCP resource.
	if err == nil || g == nil && build.cleanupErr == nil {
		n.building.CompareAndSwap(build, nil)
	}
	close(build.done)
	n.mu.Unlock()
	return err
}

type request struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
	Network   string `json:"network,omitempty"`
	Port      uint16 `json:"port,omitempty"`
	Token     string `json:"token,omitempty"`
	Peer      *Peer  `json:"peer,omitempty"`
}
type response struct {
	Version int    `json:"version"`
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Peer    *Peer  `json:"peer,omitempty"`
}

func (n *Node) handle(w *wire) {
	retained := false
	defer func() {
		if !retained {
			n.removeWire(w)
		}
	}()
	deadline := w.contextDeadline
	if deadline.IsZero() {
		deadline = time.Now().Add(handshakeTimeout)
	}
	c := tls.Server(w.raw, n.ordinaryServerTLS(w.g))
	if c.SetDeadline(deadline) != nil {
		return
	}
	ctx, cancel := context.WithDeadline(n.ctx, deadline)
	defer cancel()
	stop := watchConnection(ctx, w.raw)
	defer stop()
	if c.HandshakeContext(ctx) != nil {
		return
	}
	key, e := certificateKey([][]byte{c.ConnectionState().PeerCertificates[0].Raw}, time.Now())
	if e != nil || key == n.PublicKey() {
		return
	}
	n.mu.Lock()
	if n.readyLocked() != nil || n.generation.Load() != w.g {
		n.mu.Unlock()
		return
	}
	w.key = key
	w.peer = n.peers[key]
	managed := n.managedKey(key)
	if managed && (w.peer == nil || c.ConnectionState().NegotiatedProtocol != contextProtocolName) || !managed && c.ConnectionState().NegotiatedProtocol != protocolName {
		n.mu.Unlock()
		return
	}
	n.mu.Unlock()
	if managed {
		n.handleManagedSession(ctx, c, w)
		return
	}
	var req request
	if readJSON(c, &req) != nil || req.Version != 1 {
		return
	}
	if req.Operation == "session" {
		if req.Network != "" || req.Port != 0 || req.Token != "" || req.Peer != nil || w.peer == nil {
			return
		}
		if e := n.initiateSession(w.peer); e != nil {
			writeJSON(c, response{Version: 1, Code: "session_rejected"})
			return
		}
		writeJSON(c, response{Version: 1, OK: true})
		return
	}
	if req.Operation == "pair" {
		if req.Network != "" || req.Port != 0 || req.Peer == nil || req.Peer.Key != key {
			return
		}
		if n.acceptPair(ctx, w, req) != nil {
			writeJSON(c, response{Version: 1, Code: "pair_rejected"})
			return
		}
		own := Peer{Key: n.PublicKey(), Endpoint: w.g.cfg.Listen, TunnelKey: w.g.cfg.Identity.TunnelKey()}
		writeJSON(c, response{Version: 1, OK: true, Peer: &own})
		return
	}

	// TLS underlay is pairing/control only. Application data uses WireGuard netstack.
}

func validService(network string, port uint16) bool {
	return (network == "tcp" || network == "udp") && port != 0 && port != 54545
}

func (n *Node) removeWire(w *wire) error {
	closeErr := w.raw.Close()
	if w.stopWatch != nil {
		w.stopWatch()
	}
	if w.cancel != nil {
		w.cancel()
	}
	n.mu.Lock()
	delete(n.wires, w)
	n.mu.Unlock()
	w.work.finish()
	return closeErr
}
func (n *Node) connect(ctx context.Context, p Peer, expected *peerState) (*tls.Conn, *wire, error) {
	bounded, cancel := context.WithTimeout(ctx, handshakeTimeout)
	n.mu.Lock()
	if e := n.readyLocked(); e != nil {
		n.mu.Unlock()
		cancel()
		return nil, nil, e
	}
	if expected != nil && n.peers[p.Key] != expected || n.managedKey(p.Key) && (expected == nil || expected.binding == "") {
		n.mu.Unlock()
		cancel()
		return nil, nil, ErrUntrusted
	}
	g := n.generation.Load()
	if g == nil || !g.cfg.permits(p.Endpoint, true) {
		n.mu.Unlock()
		cancel()
		return nil, nil, ErrPolicy
	}
	if n.controlUsageLocked() >= n.cfg.ControlLimit {
		n.mu.Unlock()
		cancel()
		return nil, nil, ErrCapacity
	}
	work, e := g.acquireWork(cancel, true)
	if e != nil {
		n.mu.Unlock()
		cancel()
		return nil, nil, e
	}
	pending := &pendingDial{key: p.Key, cancel: cancel, control: true, g: g}
	n.dials[pending] = struct{}{}
	n.mu.Unlock()
	transferred := false
	defer func() {
		n.mu.Lock()
		delete(n.dials, pending)
		n.mu.Unlock()
		if !transferred {
			work.finish()
		}
	}()
	// Bind to the user's selected interface address. No resolver or proxy can
	// influence this exact numeric peer-tunnel dial; no application address enters.
	d := net.Dialer{LocalAddr: net.TCPAddrFromAddrPort(netip.AddrPortFrom(g.cfg.Listen.Addr(), 0))}
	network := "tcp6"
	if p.Endpoint.Addr().Is4() {
		network = "tcp4"
	}
	raw, e := d.DialContext(bounded, network, p.Endpoint.String())
	if e != nil {
		e = closeUnadmittedControl(g, raw, e)
		cancel()
		return nil, nil, e
	}
	owned := newControlStream(g, raw)
	w := &wire{raw: owned, key: p.Key, peer: expected, cancel: cancel, control: true, g: g, work: work, controlContext: bounded}
	n.mu.Lock()
	delete(n.dials, pending)
	if n.readyLocked() != nil || n.generation.Load() != g || bounded.Err() != nil || (expected != nil && n.peers[p.Key] != expected) || n.controlUsageLocked() >= n.cfg.ControlLimit {
		n.mu.Unlock()
		closeErr := closeUnadmittedControl(g, owned, ErrUntrusted)
		cancel()
		return nil, nil, closeErr
	}
	n.wires[w] = struct{}{}
	transferred = true
	delete(n.dials, pending)
	n.mu.Unlock()
	stop := watchConnection(bounded, owned)
	protocol := protocolName
	if n.managedKey(p.Key) {
		protocol = contextProtocolName
	}
	c := tls.Client(owned, tlsConfigProtocol(n.cert, p.Key, false, protocol))
	deadline, _ := bounded.Deadline()
	w.contextDeadline = deadline
	if e = c.SetDeadline(deadline); e == nil {
		e = c.HandshakeContext(bounded)
	}
	if e != nil {
		stop()
		closeErr := n.removeWire(w)
		return nil, nil, errors.Join(e, closeErr)
	}
	// One bounded cancellation owner covers dial, TLS and the complete frame exchange.
	w.stopWatch = stop
	return c, w, nil
}

type ConnPacketConn interface {
	net.Conn
	net.PacketConn
}

// PeerKey accepts only addresses of live authenticated flows. Synthesizing an
// overlay IP from a known public key is never enough to obtain authorization.
func (n *Node) PeerKey(remote net.Addr) (string, bool) {
	if remote == nil {
		return "", false
	}
	ap, e := netip.ParseAddrPort(remote.String())
	if e != nil {
		return "", false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	key := ""
	for f := range n.flows[ap] {
		if n.validFlowLocked(f) {
			if key != "" && key != f.w.key {
				return "", false
			}
			key = f.w.key
		}
	}
	return key, key != ""
}
func (n *Node) RegisterTCPFallback(f func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error) {
	if f == nil {
		return nil, errors.New("scoped dispatcher required")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.contextControl {
		return nil, ErrUnavailable
	}
	if n.closed || n.closing.Load() {
		return nil, net.ErrClosed
	}
	if n.fallback != nil {
		return nil, errors.New("dispatcher already registered")
	}
	n.fallback = f
	var once sync.Once
	return func() { once.Do(func() { n.mu.Lock(); n.fallback = nil; n.mu.Unlock() }) }, nil
}
func (n *Node) Close() error {
	n.RequestClose()
	// Signal without Node.mu; a session initiator may own it while sending on
	// the old UDP socket. The supervisor supplies the wake before its snapshot.
	g := n.generation.Load()
	if g != nil {
		g.requestStop(net.ErrClosed)
	}
	n.mu.Lock()
	g = n.generation.Load()
	if g != nil {
		g.requestStop(net.ErrClosed)
	}
	build := n.building.Load()
	if build != nil {
		build.RequestStop()
	}
	staged := n.staged.Load()
	if staged != nil {
		staged.Abort()
	}
	if n.closed {
		n.mu.Unlock()
		var err error
		if build != nil {
			err = build.wait(context.Background())
		}
		if g != nil {
			err = errors.Join(err, g.wait(context.Background()))
		}
		if staged != nil {
			err = errors.Join(err, staged.WaitClosed(context.Background()))
		}
		return err
	}
	n.closed = true
	n.cancel()
	if n.stop != nil {
		n.stop()
	}
	var ws []*wire
	for w := range n.wires {
		ws = append(ws, w)
	}
	var ls []*listener
	for _, l := range n.listeners {
		ls = append(ls, l)
	}
	for p := range n.dials {
		p.cancel()
	}
	for _, cancel := range n.attempts {
		cancel()
	}
	for key, attempt := range n.contextAttempts {
		attempt.Cancel()
		attempt.cell.data.Store(nil)
		delete(n.contextAttempts, key)
	}
	n.invites = map[string]pendingInvitation{}
	n.mu.Unlock()
	for _, w := range ws {
		if w.flow != nil {
			w.flow.Close()
		} else {
			w.raw.Close()
		}
	}
	for _, l := range ls {
		l.Close()
	}
	var err error
	if build != nil {
		err = build.wait(context.Background())
	}
	if g != nil {
		err = errors.Join(err, g.wait(context.Background()))
	}
	if staged != nil {
		err = errors.Join(err, staged.WaitClosed(context.Background()))
	}
	n.wg.Wait()
	return err
}
func (n *Node) String() string { return fmt.Sprintf("directlan(%s)", n.Endpoint()) }

// Ready reports local listener/engine admission state without claiming remote
// reachability. Failed persistence or engine admission is reported as recovery.
func (n *Node) Ready() error {
	n.mu.Lock()
	e := n.readyLocked()
	g := n.generation.Load()
	n.mu.Unlock()
	if e != nil {
		return e
	}
	if g == nil {
		return ErrUnavailable
	}
	return localAddressReady(g.cfg.Listen.Addr())
}

func (n *Node) flowUsageLocked() int {
	count := 0
	for w := range n.wires {
		if !w.control {
			count++
		}
	}
	for p := range n.dials {
		if !p.control {
			count++
		}
	}
	return count
}
func (n *Node) controlUsageLocked() int {
	count := 0
	for w := range n.wires {
		if w.control {
			count++
		}
	}
	for p := range n.dials {
		if p.control {
			count++
		}
	}
	return count
}
