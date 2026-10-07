package directlan

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// ContextControlConfig admits only exact saved peers to a separately owned TCP
// listener. This owner never constructs a WG device, netstack, or application
// listener. Construction validates and copies policy without network I/O.
type ContextControlConfig struct {
	Identity        Identity
	Listen          netip.AddrPort
	AllowedPrefixes []netip.Prefix
	Peers           []Peer
	ControlLimit    int
	Completion      ContextCompletion
}

// A fresh registration is allocated for each saved peer in each generation.
// It is neither a WG peer registration nor a transferable authentication token.
type contextPeerRegistration struct {
	g    *runtimeGeneration
	peer Peer
}

func NewContextControl(cfg ContextControlConfig) (*Node, error) {
	policy := Config{Identity: cfg.Identity, Listen: cfg.Listen,
		AllowedPrefixes: append([]netip.Prefix(nil), cfg.AllowedPrefixes...),
		Peers:           append([]Peer(nil), cfg.Peers...), ControlLimit: cfg.ControlLimit}.withDefaults()
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	cert, err := certificate(policy.Identity, time.Now())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Node{cfg: policy, cert: cert, contextControl: true,
		contextCompletion: cfg.Completion, contextAttempts: make(map[contextAttemptKey]*ContextAttempt),
		peers: map[string]*peerState{}, wires: map[*wire]struct{}{},
		dials: map[*pendingDial]struct{}{}, ctx: ctx, cancel: cancel}, nil
}

func (n *Node) startReadyLocked() error {
	if n.contextControl {
		return n.controlReadyLocked()
	}
	return n.readyLocked()
}

func (n *Node) controlReadyLocked() error {
	if !n.contextControl {
		return ErrUnavailable
	}
	if n.closed || n.closing.Load() {
		return net.ErrClosed
	}
	if n.recovery {
		return ErrRecovery
	}
	g := n.generation.Load()
	if !n.started || g == nil || !g.controlOpen.Load() || !g.open() {
		return ErrUnavailable
	}
	return nil
}

// ControlReady reports only local control readiness, never application Running.
func (n *Node) ControlReady() error {
	n.mu.Lock()
	err := n.controlReadyLocked()
	g := n.generation.Load()
	n.mu.Unlock()
	if err != nil {
		return err
	}
	return localAddressReady(g.cfg.Listen.Addr())
}

func (n *Node) buildContextGeneration(b *generationBuild, cfg Config) (*runtimeGeneration, error) {
	// Allocate the lifecycle before the first resource. Even a failed listen
	// therefore retains a real cleanup owner; optional resources stay absent.
	g := newRuntimeGeneration(n, nil, nil)
	g.cfg = cloneGenerationConfig(cfg)
	g.controlPeers = make(map[string]*contextPeerRegistration, len(cfg.Peers))
	g.failedControl = make(map[*controlStream]error)
	for _, peer := range cfg.Peers {
		g.controlPeers[peer.Key] = &contextPeerRegistration{g: g, peer: peer}
	}
	b.generation.Store(g)
	go g.supervise()
	defer close(g.built)
	network := "tcp6"
	if cfg.Listen.Addr().Is4() {
		network = "tcp4"
	}
	ln, err := (&net.ListenConfig{}).Listen(b.ctx, network, cfg.Listen.String())
	if err != nil {
		g.requestStop(err)
		return g, err
	}
	g.underlay = newGenerationUnderlay(ln)
	b.underlay.Store(g.underlay)
	if err := b.ctx.Err(); err != nil {
		g.requestStop(err)
		return g, err
	}
	g.underlay.start(n, g)
	return g, nil
}

// watchAttempt owns cancellation during dialing, TLS and completion. The old
// epoch stops owning cancellation after a successful claim; the response epoch
// has its own watcher. Joining this watcher precedes releasing its reservation.
func watchAttempt(ctx context.Context, cancel context.CancelFunc, a *ContextAttempt, epoch *ContextEpoch) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-a.cell.cancelled:
			cancel()
			return
		case <-epoch.cell.done:
			if !a.cell.claimed.Load() {
				cancel()
				return
			}
		}
		select {
		case <-stop:
		case <-ctx.Done():
		case <-a.cell.cancelled:
			cancel()
		}
	}()
	return func() { close(stop); <-done }
}

func (a *ContextAttempt) Exchange(ctx context.Context, completion ContextCompletion) (result error) {
	d := a.data()
	if d == nil || ctx == nil || completion == nil || d.direction != ContextOutbound ||
		!a.cell.started.CompareAndSwap(false, true) {
		return ErrUnavailable
	}
	var work *generationWork
	defer func() {
		a.finish(d)
		if work != nil {
			work.finish()
		}
	}()
	deadline := time.Now().Add(handshakeTimeout)
	run, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	n, g := d.n, d.g
	n.mu.Lock()
	if n.controlReadyLocked() != nil || n.generation.Load() != g || n.contextAttempts[d.key] != a ||
		g.controlPeers[d.key.peer] != d.registration || !d.epoch.Valid() || a.cancelled() || run.Err() != nil {
		n.mu.Unlock()
		return ErrUnavailable
	}
	var err error
	work, err = g.acquireWork(cancel, true)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	pending := &pendingDial{g: g, key: d.key.peer, cancel: cancel, control: true}
	n.dials[pending] = struct{}{}
	n.mu.Unlock()
	stopAttempt := watchAttempt(run, cancel, a, d.epoch)
	// One owner carries this work from dial through response/callback/close.
	// Failed Close is separately retained/charged, never left as fictitious work.
	var raw net.Conn
	var c *tls.Conn
	var w *wire
	var stopConnection func()
	defer func() {
		if c != nil {
			_ = c.Close()
		}
		if raw != nil {
			result = errors.Join(result, raw.Close())
		}
		if stopConnection != nil {
			stopConnection()
		}
		stopAttempt()
		n.mu.Lock()
		delete(n.dials, pending)
		if w != nil {
			delete(n.wires, w)
		}
		n.mu.Unlock()
	}()
	peer := d.registration.peer
	dialer := net.Dialer{LocalAddr: net.TCPAddrFromAddrPort(netip.AddrPortFrom(g.cfg.Listen.Addr(), 0))}
	network := "tcp6"
	if peer.Endpoint.Addr().Is4() {
		network = "tcp4"
	}
	connected, err := dialer.DialContext(run, network, peer.Endpoint.String())
	if connected != nil {
		raw = newControlStream(g, connected)
	}
	if err != nil {
		return err
	}
	n.mu.Lock()
	delete(n.dials, pending)
	if n.controlReadyLocked() != nil || n.generation.Load() != g || n.contextAttempts[d.key] != a ||
		g.controlPeers[d.key.peer] != d.registration || !d.epoch.Valid() || a.cancelled() || run.Err() != nil {
		n.mu.Unlock()
		return ErrUnavailable
	}
	w = &wire{raw: raw, key: peer.Key, cancel: cancel, control: true, g: g, work: work, contextDeadline: deadline}
	n.wires[w] = struct{}{}
	n.mu.Unlock()
	stopConnection = watchConnection(run, raw)
	c = tls.Client(raw, tlsConfigProtocol(n.cert, peer.Key, false, contextProtocolName))
	if err := c.SetDeadline(deadline); err != nil {
		return err
	}
	if err := c.HandshakeContext(run); err != nil {
		return err
	}
	if !tryContextCurrent(a, d, d.epoch, deadline, run.Done(), false) {
		return ErrUnavailable
	}
	if err := writeFrame(c, d.expected, endpointmeta.MaxFrameBytes); err != nil {
		return err
	}
	reply, err := readFrame(c, endpointmeta.MaxFrameBytes)
	if err != nil {
		return err
	}
	if !contextReplyMatches(d, d.expected, reply) {
		return ErrUntrusted
	}
	verified := mintContextExchange(a, d, run, d.expected, reply)
	defer verified.cell.value.Store(nil)
	response, err := completion(run, a, verified)
	if err != nil {
		return err
	}
	if !a.cell.claimed.Load() || response.Reply != nil || response.Epoch != nil || response.Admit != nil {
		return ErrUntrusted
	}
	// The completion may have durably changed the store, invalidating its
	// original admission. Its old epoch is not a post-save success predicate.
	if run.Err() != nil || a.cancelled() || n.closing.Load() {
		return ErrUnavailable
	}
	return nil
}

func (n *Node) handleContext(w *wire) {
	run, cancel := context.WithDeadline(n.ctx, w.contextDeadline)
	defer cancel()
	c := tls.Server(w.raw, tlsConfigProtocol(n.cert, "", true, contextProtocolName))
	stopConnection := watchConnection(run, w.raw)
	var stopAttempt, stopResponse func()
	var selected *ContextAttempt
	var data *contextAttemptData
	defer func() {
		_ = c.Close()
		_ = w.raw.Close()
		if stopResponse != nil {
			stopResponse()
		}
		if stopAttempt != nil {
			stopAttempt()
		}
		stopConnection()
		if selected != nil {
			selected.finish(data)
		}
		n.removeWire(w)
	}()
	if c.SetDeadline(w.contextDeadline) != nil || c.HandshakeContext(run) != nil {
		return
	}
	state := c.ConnectionState()
	if len(state.PeerCertificates) != 1 {
		return
	}
	key, err := certificateKey([][]byte{state.PeerCertificates[0].Raw}, time.Now())
	if err != nil || key == n.PublicKey() || state.NegotiatedProtocol != contextProtocolName {
		return
	}

	// Authentication and exact registration precede the first frame read.
	// Capture only already-armed pointers; parsing cannot look up a later arm.
	// The acceptance cutoff also excludes arms created during TLS handshake:
	// application bytes buffered by TLS cannot gain a later local review.
	var captured [3]*ContextAttempt
	n.mu.Lock()
	g := w.g
	if n.controlReadyLocked() != nil || n.generation.Load() != g || g.controlPeers[key] == nil || n.contextCompletion == nil {
		n.mu.Unlock()
		return
	}
	registration := g.controlPeers[key]
	w.key = key
	for op := ContextPrepare; op <= ContextStatus; op++ {
		a := n.contextAttempts[contextAttemptKey{key, op}]
		d := a.data()
		if d != nil && d.g == g && d.registration == registration && d.direction == ContextInbound && d.armRevision <= w.contextArmCutoff &&
			d.epoch.Valid() && !a.cancelled() && !a.cell.started.Load() {
			captured[int(op)-1] = a
		}
	}
	n.mu.Unlock()
	requestBytes, err := readFrame(c, endpointmeta.MaxFrameBytes)
	if err != nil {
		return
	}
	request, err := endpointmeta.ParseRequest(requestBytes)
	if err != nil {
		return
	}
	op, err := contextRequestOperation(request)
	if err != nil {
		return
	} // reject pair/session/update before any callback
	a := captured[int(op)-1]
	d := a.data()
	if d == nil || d.registration != registration || !contextInboundRequestMatches(d, requestBytes) ||
		!a.cell.started.CompareAndSwap(false, true) {
		return
	}
	selected, data = a, d
	stopAttempt = watchAttempt(run, cancel, a, d.epoch)
	if !tryContextCurrent(a, d, d.epoch, w.contextDeadline, run.Done(), false) {
		return
	}
	verified := mintContextExchange(a, d, run, requestBytes, nil)
	defer verified.cell.value.Store(nil)
	response, err := n.contextCompletion(run, a, verified)
	if err != nil || !a.cell.claimed.Load() || response.Reply == nil || response.Admit == nil || !response.Epoch.Valid() {
		return
	}
	reply, err := endpointmeta.Encode(response.Reply)
	if err != nil || !contextReplyMatches(d, requestBytes, reply) {
		return
	}
	stopResponse = watchContextEpoch(response.Epoch, a, false, w.raw)
	// Core's one response admission runs with no DirectLAN lock. Repeat the
	// current nonwaiting Node→generation check after that callback and before
	// framed TLS I/O. Later invalidation closes the wire; sent bytes cannot be
	// recalled and a lost reply requires a fresh authenticated exchange.
	if !response.Admit() || !tryContextCurrent(a, d, response.Epoch, w.contextDeadline, run.Done(), true) {
		return
	}
	_ = writeFrame(c, reply, endpointmeta.MaxFrameBytes)
}

func contextInboundRequestMatches(d *contextAttemptData, request []byte) bool {
	if d.key.operation != ContextPrepare {
		return bytes.Equal(d.expected, request)
	}
	expected, err := endpointmeta.ParseRequest(d.expected)
	if err != nil {
		return false
	}
	parsed, err := endpointmeta.ParseRequest(request)
	if err != nil {
		return false
	}
	local, lok := expected.(*endpointmeta.PrepareRequest)
	remote, rok := parsed.(*endpointmeta.PrepareRequest)
	return lok && rok && local.Sender == remote.Recipient && local.Recipient == remote.Sender &&
		local.SenderTunnelKey == remote.RecipientTunnelKey && local.RecipientTunnelKey == remote.SenderTunnelKey &&
		local.SenderEndpoint == remote.RecipientEndpoint && local.RecipientEndpoint == remote.SenderEndpoint
}

func contextReplyMatches(d *contextAttemptData, request, reply []byte) bool {
	r, err := endpointmeta.ParseReply(reply, contextOperationName(d.key.operation))
	if err != nil {
		return false
	}
	if d.key.operation != ContextPrepare {
		parsed, err := endpointmeta.ParseRequest(request)
		if err != nil {
			return false
		}
		bound, ok := parsed.(*endpointmeta.BoundRequest)
		response, rok := r.(*endpointmeta.ContextReply)
		return ok && rok && bound.PairBinding == response.PairBinding
	}
	response, ok := r.(*endpointmeta.PrepareReply)
	if !ok {
		return false
	}
	expected, err := endpointmeta.ParseRequest(d.expected)
	if err != nil {
		return false
	}
	local, ok := expected.(*endpointmeta.PrepareRequest)
	if !ok || !contextPrepareSideMatches(*local, response.PairContext, true) {
		return false
	}
	if d.direction == ContextInbound {
		parsed, err := endpointmeta.ParseRequest(request)
		if err != nil {
			return false
		}
		remote, ok := parsed.(*endpointmeta.PrepareRequest)
		return ok && contextPrepareSideMatches(*remote, response.PairContext, true)
	}
	return true
}
func contextPrepareSideMatches(r endpointmeta.PrepareRequest, p endpointmeta.PairContext, nonce bool) bool {
	key, other, tunnel, otherTunnel := p.HostKey, p.JoinerKey, p.HostTunnelKey, p.JoinerTunnelKey
	endpoint, otherEndpoint, ownNonce, scope := p.HostEndpoint, p.JoinerEndpoint, p.HostNonce, p.HostScope
	if r.Sender == p.JoinerKey {
		key, other, tunnel, otherTunnel = p.JoinerKey, p.HostKey, p.JoinerTunnelKey, p.HostTunnelKey
		endpoint, otherEndpoint, ownNonce, scope = p.JoinerEndpoint, p.HostEndpoint, p.JoinerNonce, p.JoinerScope
	}
	left, _ := endpointmeta.Encode(r.SenderScope)
	right, _ := endpointmeta.Encode(scope)
	return r.Sender == key && r.Recipient == other && r.SenderTunnelKey == tunnel && r.RecipientTunnelKey == otherTunnel &&
		r.SenderEndpoint == endpoint && r.RecipientEndpoint == otherEndpoint && (!nonce || r.SenderNonce == ownNonce) && bytes.Equal(left, right)
}
