package directlan

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tailscale/wireguard-go/device"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

var ErrManagementConflict = errors.New("management port already has an owner")

const managementIOTimeout = 5 * time.Second
const (
	managementNew uint32 = iota
	managementNegotiating
	managementReady
	managementClosed
)

// ManagementListener is the distinct opt-in v2 entrance. It cannot replace an
// existing inspection or service listener on the reserved userspace TCP port.
// Only the owned, explicitly confirmed management grant coordinator creates it.
type ManagementListener struct {
	owner *listener
	done  <-chan struct{}
}

func (n *Node) ListenManagement(ctx context.Context) (*ManagementListener, error) {
	if n == nil || ctx == nil {
		return nil, ErrUnavailable
	}
	done := ctx.Done()
	raw, err := n.listenPeer(ctx, "tcp", ResourceInspectPort, ErrManagementConflict)
	if err != nil {
		return nil, err
	}
	owner, ok := raw.(*listener)
	if !ok {
		_ = raw.Close()
		return nil, ErrUnavailable
	}
	return &ManagementListener{owner: owner, done: done}, nil
}

func (l *ManagementListener) Close() error {
	if l == nil || l.owner == nil {
		return nil
	}
	return l.owner.Close()
}

func (l *ManagementListener) Accept() (*ManagedManagement, error) {
	if l == nil || l.owner == nil {
		return nil, ErrUnavailable
	}
	raw, err := l.owner.Accept()
	if err != nil {
		return nil, err
	}
	capability, ok := captureManagedManagement(raw, l)
	if !ok {
		_ = raw.Close()
		return nil, ErrUntrusted
	}
	return capability, nil
}

// ManagedManagement is minted only from an owned authenticated inbound flow.
// Snapshot and decoded DTOs do not convey authority. There is no raw connection,
// arbitrary writer, callback admission, or transferable provider token.
type ManagedManagement struct {
	flow           *flow
	owner          *listener
	authentication managedAuthentication
	relationship   resourcegrant.Relationship
	epoch          managementEpochSlot
	peerAddress    netip.Addr
	nodeDone       <-chan struct{}
	runtimeDone    <-chan struct{}
	done           chan struct{}
	phase          atomic.Uint32
	requestRead    atomic.Bool
	requestReady   atomic.Bool
	request        resourcegrant.ManagementRequest // immutable after requestReady
	requestDigest  [sha256.Size]byte

	// The following fields use Node.mu. Cancellation channels are captured
	// before transport locking; no context methods run inside admission.
	contextBound bool
	requestDone  <-chan struct{}
	stopContext  func() bool
	providerUsed bool
	replyUsed    bool
}

func captureManagedManagement(connection net.Conn, entrance *ManagementListener) (*ManagedManagement, bool) {
	f, ok := connection.(*flow)
	if !ok || f == nil || f.n == nil || f.n.ctx == nil || f.g == nil || f.g.bind == nil || f.c == nil || f.w == nil || f.w.peer == nil || entrance == nil || entrance.owner == nil {
		return nil, false
	}
	owner, p := entrance.owner, f.w.peer
	if owner.n != f.n || owner.service != (service{"tcp", ResourceInspectPort}) || !f.inbound || f.network != "tcp" || f.local.Port() != ResourceInspectPort || p.session == nil || p.binding == "" {
		return nil, false
	}
	// Randomness, key conversion, identity validation and Done capture precede
	// all Node/WG/generation locks. RNG failure cannot select a constant epoch.
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, false
	}
	candidate := hex.EncodeToString(random[:])
	address, err := OverlayAddress(p.peer.Key)
	if err != nil {
		return nil, false
	}
	pair, paired := f.g.cfg.PairContexts[p.peer.Key]
	pairBinding, bindingErr := pair.Binding()
	authenticated := p.authenticated.Load()
	if !paired || bindingErr != nil || authenticated == nil || authenticated.binding != pairBinding || p.binding != pairBinding {
		return nil, false
	}
	relationship := resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: f.g.cfg.Identity.PublicKey(), PeerKey: f.w.key, PairBinding: authenticated.binding}
	if relationship.Validate() != nil {
		return nil, false
	}
	capability := &ManagedManagement{
		flow: f, owner: owner, authentication: *authenticated, relationship: relationship,
		peerAddress: address, nodeDone: f.n.ctx.Done(), runtimeDone: entrance.done, done: make(chan struct{}),
	}
	f.n.mu.Lock()
	defer f.n.mu.Unlock()
	if f.resourceInspectionCaptured || f.resourceManagementCaptured {
		return nil, false
	}
	slot, ok := selectManagementEpoch(p.managementEpoch, capability.authentication, candidate)
	if !ok {
		return nil, false
	}
	if !f.g.admit(func() bool {
		if !capability.transportLeafLocked() {
			return false
		}
		p.managementEpoch = slot
		capability.epoch = slot
		f.resourceManagementCaptured = true
		return true
	}) {
		return nil, false
	}
	return capability, true
}

// transportLeafLocked requires Node.mu -> g.admit (WG owner -> generation.mu).
// It is deliberately independent of currentLocked/validFlowLocked/sessionIdentity
// and endpoint.borrow, all of which can recursively enter g.admit. Only bounded
// map/atomic/channel predicates are permitted here; no I/O or context methods.
func (c *ManagedManagement) transportLeafLocked() bool {
	f, a := c.flow, c.authentication
	n, g, p, endpoint := f.n, f.g, f.w.peer, f.c
	if n.closed || n.closing.Load() || n.recovery || !n.started || n.contextControl || n.generation.Load() != g || g.n != n || !g.traffic.Load() ||
		a.generation != g || a.peer != p || a.policy == nil || a.registration == 0 || a.binding == "" || p.g != g || p.binding != a.binding ||
		n.peers[f.w.key] != p || g.peers[f.w.key] != p || p.peer.Key != f.w.key || f.w.g != g || f.w.flow != f || f.w.raw != endpoint ||
		g.bind == nil || g.bind.policy.Load() != a.policy || a.policy.generations[c.peerAddress] != p || p.session == nil || p.session.registration.Load() != a.registration ||
		g.peerRegistrations[device.PeerRegistration(a.registration)] != p || n.listeners[c.owner.service] != c.owner || f.listenerIdentity.Load() != c.owner ||
		endpoint.g != g || endpoint.ep == nil || g.live[endpoint.ep] != endpoint || endpoint.raw == nil || endpoint.closing ||
		c.phase.Load() == managementClosed {
		return false
	}
	if _, exists := g.cfg.PairContexts[f.w.key]; !exists {
		return false
	}
	if current := p.authenticated.Load(); current == nil || *current != a {
		return false
	}
	if _, exists := n.wires[f.w]; !exists {
		return false
	}
	if _, exists := n.flows[f.remote][f]; !exists {
		return false
	}
	if _, exists := g.work[f.work]; !exists {
		return false
	}
	select {
	case <-c.nodeDone:
		return false
	case <-c.runtimeDone:
		return false
	case <-c.requestDone:
		return false
	case <-c.owner.done:
		return false
	case <-c.done:
		return false
	case <-g.stop:
		return false
	case <-endpoint.closeRequested:
		return false
	default:
		return true
	}
}

func (c *ManagedManagement) currentLeafLocked() bool {
	return c.transportLeafLocked() && c.contextBound && c.flow.resourceManagementCaptured &&
		c.flow.w.peer.managementEpoch == c.epoch && c.epoch.identity == c.authentication
}

// BindContext narrows the capability to one owned handler's cancellation. It
// must precede negotiation and is single-use. It can never extend a deadline.
func (c *ManagedManagement) BindContext(ctx context.Context) error {
	if c == nil || c.flow == nil || ctx == nil {
		return c.fail(ErrUntrusted)
	}
	done := ctx.Done()
	n := c.flow.n
	n.mu.Lock()
	bound := c.phase.Load() == managementNew && !c.contextBound
	if bound {
		c.contextBound, c.requestDone = true, done
	}
	n.mu.Unlock()
	if !bound {
		return c.fail(ErrUntrusted)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	n.mu.Lock()
	closed := c.phase.Load() == managementClosed
	if !closed {
		c.stopContext = stop
	}
	n.mu.Unlock()
	if closed {
		stop()
		return ErrUntrusted
	}
	if !c.current() {
		return c.fail(ErrUntrusted)
	}
	return nil
}

// Snapshot is immutable data, never proof of current transport/grant authority.
func (c *ManagedManagement) Snapshot() (resourcegrant.Relationship, string, bool) {
	if c == nil || c.flow == nil || c.authentication.peer == nil || c.epoch.digest == "" {
		return resourcegrant.Relationship{}, "", false
	}
	return c.relationship, c.epoch.digest, true
}

func (c *ManagedManagement) current() bool {
	if c == nil || c.flow == nil || c.flow.n == nil || c.flow.g == nil {
		return false
	}
	c.flow.n.mu.Lock()
	defer c.flow.n.mu.Unlock()
	return c.flow.g.admit(func() bool { return c.currentLeafLocked() })
}

// Current verifies this exact immutable captured request, not a caller-supplied
// relationship or replacement DTO. Core must additionally certify owned grants.
func (c *ManagedManagement) Current(request resourcegrant.ManagementRequest) bool {
	if c == nil || c.flow == nil || !c.requestReady.Load() {
		return false
	}
	frame, err := resourcegrant.ManagementRequestFrame(request)
	if err != nil || sha256.Sum256(frame) != c.requestDigest {
		return false
	}
	c.flow.n.mu.Lock()
	defer c.flow.n.mu.Unlock()
	return c.flow.g.admit(func() bool {
		return c.phase.Load() == managementReady && c.currentLeafLocked()
	})
}

func (c *ManagedManagement) Close() error {
	if c == nil || c.flow == nil || c.flow.n == nil {
		return nil
	}
	c.flow.n.mu.Lock()
	var stop func() bool
	if c.phase.Load() != managementClosed {
		c.phase.Store(managementClosed)
		if c.done != nil {
			close(c.done)
		}
		stop, c.stopContext = c.stopContext, nil
	}
	c.flow.n.mu.Unlock()
	if stop != nil {
		stop()
	}
	return c.flow.Close()
}

func (c *ManagedManagement) fail(err error) error { _ = c.Close(); return err }

// borrow pins only the already captured authentication. It never substitutes a
// newer authentication tuple while carrying application bytes on an old flow.
func (c *ManagedManagement) borrow(deadline bool) (net.Conn, bool) {
	if c == nil || c.flow == nil {
		return nil, false
	}
	var raw net.Conn
	f := c.flow
	f.n.mu.Lock()
	ok := f.g.admit(func() bool {
		if !c.currentLeafLocked() {
			return false
		}
		f.c.borrows++
		if deadline {
			f.c.deadlineSetters++
		}
		raw = f.c.raw
		return true
	})
	f.n.mu.Unlock()
	return raw, ok
}

func (c *ManagedManagement) setDeadline(deadline time.Time, writeOnly bool) error {
	raw, ok := c.borrow(true)
	if !ok {
		return ErrUntrusted
	}
	defer c.flow.c.release(true)
	var err error
	if writeOnly {
		err = raw.SetWriteDeadline(deadline)
	} else {
		err = raw.SetDeadline(deadline)
	}
	if err != nil {
		return resourcegrant.ErrTransport
	}
	return nil
}

// managementReader is private: callers receive no generic transport capability.
type managementReader struct{ capability *ManagedManagement }

func (r managementReader) Read(data []byte) (int, error) {
	c := r.capability
	raw, ok := c.borrow(false)
	if !ok {
		return 0, ErrUntrusted
	}
	n, err := raw.Read(data)
	c.flow.c.release(false)
	if !c.current() {
		clear(data[:n])
		return 0, ErrUntrusted
	}
	if err != nil && err != io.EOF {
		return n, resourcegrant.ErrTransport
	}
	return n, err
}

// Negotiate sends only fixed support bytes. The management-only listener
// explicitly rejects v1 before reading selectors and never falls back to v1.
func (c *ManagedManagement) Negotiate() error {
	if c == nil || c.flow == nil || !c.phase.CompareAndSwap(managementNew, managementNegotiating) {
		return c.fail(ErrUntrusted)
	}
	if err := c.setDeadline(time.Now().Add(managementIOTimeout), false); err != nil {
		return c.fail(err)
	}
	version, err := resourcegrant.ReadHelloRequest(managementReader{c})
	if err != nil {
		return c.fail(err)
	}
	reply, supported, err := resourcegrant.ManagementListenerHelloResponse(version)
	if err != nil {
		return c.fail(err)
	}
	raw, ok := c.borrow(false)
	if !ok {
		return c.fail(ErrUntrusted)
	}
	n, err := raw.Write(reply[:])
	c.flow.c.release(false)
	if err != nil || n != len(reply) {
		return c.fail(resourcegrant.ErrTransport)
	}
	if !supported {
		return c.fail(resourcegrant.ErrUnsupported)
	}
	c.flow.n.mu.Lock()
	ready := c.flow.g.admit(func() bool {
		return c.currentLeafLocked() && c.phase.CompareAndSwap(managementNegotiating, managementReady)
	})
	c.flow.n.mu.Unlock()
	if !ready {
		return c.fail(ErrUntrusted)
	}
	return nil
}

// ReadRequest reads one bounded frame and retains a separate deep copy. Nested
// choice pointers returned to Core cannot mutate the request being admitted.
func (c *ManagedManagement) ReadRequest() (resourcegrant.ManagementRequest, error) {
	var empty resourcegrant.ManagementRequest
	if c == nil || c.flow == nil || c.phase.Load() != managementReady || !c.requestRead.CompareAndSwap(false, true) {
		return empty, c.fail(ErrUntrusted)
	}
	if err := c.setDeadline(time.Now().Add(managementIOTimeout), false); err != nil {
		return empty, c.fail(err)
	}
	request, err := resourcegrant.ReadManagementRequest(managementReader{c})
	if err != nil {
		if err != resourcegrant.ErrTransport {
			err = resourcegrant.ErrProtocol
		}
		return empty, c.fail(err)
	}
	frame, err := resourcegrant.ManagementRequestFrame(request)
	if err != nil {
		return empty, c.fail(resourcegrant.ErrProtocol)
	}
	owned, err := resourcegrant.DecodeManagementRequest(frame[4:])
	if err != nil {
		return empty, c.fail(resourcegrant.ErrProtocol)
	}
	digest := sha256.Sum256(frame)
	c.flow.n.mu.Lock()
	ready := c.flow.g.admit(func() bool {
		if c.phase.Load() != managementReady || !c.currentLeafLocked() {
			return false
		}
		c.request, c.requestDigest = owned, digest
		c.requestReady.Store(true)
		return true
	})
	c.flow.n.mu.Unlock()
	if !ready {
		return empty, c.fail(ErrUntrusted)
	}
	return request, nil
}

// AdmitProvider is the one-shot ordering point immediately after Core's durable
// intent and owned recertification. It invokes no provider, borrows no endpoint,
// and returns with every transport lock released. Only captured apply can pass.
func (c *ManagedManagement) AdmitProvider(fence *resourcegrant.ManagementFence) bool {
	if c == nil || c.flow == nil || !c.requestReady.Load() || c.request.Action != resourcegrant.ApplyAction || c.request.Apply == nil {
		return false
	}
	c.flow.n.mu.Lock()
	defer c.flow.n.mu.Unlock()
	return c.flow.g.admit(func() bool {
		if c.phase.Load() != managementReady || !c.currentLeafLocked() || c.providerUsed || c.replyUsed ||
			!fence.Admit(c.request.ManagementSelector, c.request.Action, c.relationship) {
			return false
		}
		c.providerUsed = true
		return true
	})
}

// managementReplyMatches adds request correspondence to the strict reply union.
// This pure check is always performed before acquiring transport locks.
func managementReplyMatches(request resourcegrant.ManagementRequest, reply resourcegrant.ManagementReply) bool {
	if request.Validate() != nil || reply.Validate() != nil || request.ManagementSelector != reply.ManagementSelector || request.Action != reply.Action {
		return false
	}
	switch request.Action {
	case resourcegrant.ApplyAction:
		return reply.Operation != nil && reply.Operation.OperationID == request.Apply.OperationID
	case resourcegrant.StatusAction:
		return reply.Operation != nil && reply.Operation.OperationID == request.Status.OperationID ||
			reply.Unavailable != nil && reply.Unavailable.OperationID == request.Status.OperationID
	default:
		return true
	}
}

// PrepareReply freezes one allowlisted frame, installs its absolute finite
// deadline, and independently admits disclosure. Core freshly recertifies the
// owned grant/clock immediately before this call, then releases Core.op and its
// lifecycle callback before response.Write. No endpoint borrow crosses provider.
func (c *ManagedManagement) PrepareReply(fence *resourcegrant.ManagementFence, reply resourcegrant.ManagementReply) (*ManagementResponse, error) {
	if c == nil || c.flow == nil || !c.requestReady.Load() || !managementReplyMatches(c.request, reply) {
		return nil, c.fail(ErrUntrusted)
	}
	frame, err := resourcegrant.ManagementReplyFrame(reply)
	if err != nil {
		return nil, c.fail(resourcegrant.ErrProtocol)
	}
	deadline := time.Now().Add(managementIOTimeout)
	if err := c.setDeadline(deadline, true); err != nil {
		return nil, c.fail(err)
	}
	response := &ManagementResponse{capability: c, endpoint: c.flow.c, frame: frame, deadline: deadline}
	c.flow.n.mu.Lock()
	admitted := c.flow.g.admit(func() bool {
		if c.phase.Load() != managementReady || !c.currentLeafLocked() || c.replyUsed ||
			!fence.Admit(c.request.ManagementSelector, c.request.Action, c.relationship) {
			return false
		}
		c.replyUsed = true
		c.flow.c.borrows++
		response.raw = c.flow.c.raw
		return true
	})
	c.flow.n.mu.Unlock()
	if !admitted {
		return nil, c.fail(ErrUntrusted)
	}
	return response, nil
}

// ManagementResponse owns only a frozen bounded frame and one endpoint borrow.
// Close during Write requests cancellation but keeps the borrow until Write
// returns. Close before Write releases immediately. No sync.Once callback waits
// for or reenters another Close; one mutex protects this small ownership state.
type ManagementResponse struct {
	mu         sync.Mutex
	capability *ManagedManagement
	endpoint   *liveEndpoint
	raw        net.Conn
	frame      []byte
	deadline   time.Time
	writing    bool
	written    bool
	closed     bool
	released   bool
}

func (r *ManagementResponse) Write() error {
	if r == nil {
		return ErrUntrusted
	}
	r.mu.Lock()
	if r.closed || r.written || r.raw == nil || r.capability == nil || !time.Now().Before(r.deadline) {
		r.mu.Unlock()
		_ = r.Close()
		return ErrUntrusted
	}
	r.writing, r.written = true, true
	raw, frame := r.raw, r.frame
	r.mu.Unlock()
	defer r.finishWrite()
	// Cancellation before actual I/O must not use the retained raw handle.
	if !r.capability.current() {
		return ErrUntrusted
	}
	n, err := raw.Write(frame)
	if err != nil || n != len(frame) {
		return resourcegrant.ErrTransport
	}
	return nil
}

func (r *ManagementResponse) finishWrite() {
	r.mu.Lock()
	r.writing, r.closed = false, true
	release := !r.released
	r.released = true
	endpoint, capability := r.endpoint, r.capability
	r.raw, r.frame = nil, nil
	r.mu.Unlock()
	if release && endpoint != nil {
		endpoint.release(false)
	}
	if capability != nil {
		_ = capability.Close()
	}
}

func (r *ManagementResponse) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.closed = true
	release := !r.writing && !r.released
	if release {
		r.released = true
		r.raw, r.frame = nil, nil
	}
	endpoint, capability := r.endpoint, r.capability
	r.mu.Unlock()
	if capability != nil {
		_ = capability.Close()
	}
	if release && endpoint != nil {
		endpoint.release(false)
	}
	return nil
}
