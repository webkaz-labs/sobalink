package directlan

import (
	"bytes"
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// These closed variants describe data. They neither authenticate a caller nor
// grant application, pair, session, endpoint-update or publication authority.
type ContextOperation uint8

const (
	ContextPrepare ContextOperation = iota + 1
	ContextCommit
	ContextStatus
)

type ContextDirection uint8

const (
	ContextInbound ContextDirection = iota + 1
	ContextOutbound
)

// ContextEpoch is a bounded nonpersistent invalidation signal. Invalidate does
// no I/O, joins or callbacks, and is safe at a store's write-revision boundary.
// The zero value is invalid. An epoch is never evidence of authentication.
type contextEpochCell struct {
	done chan struct{}
	once sync.Once
}
type ContextEpoch struct{ cell *contextEpochCell }

func NewContextEpoch() *ContextEpoch {
	return &ContextEpoch{cell: &contextEpochCell{done: make(chan struct{})}}
}
func (e *ContextEpoch) Invalidate() {
	if e != nil && e.cell != nil {
		e.cell.once.Do(func() { close(e.cell.done) })
	}
}
func (e *ContextEpoch) Valid() bool {
	if e == nil || e.cell == nil {
		return false
	}
	select {
	case <-e.cell.done:
		return false
	default:
		return true
	}
}

// Completion executes synchronously while the wire, cancellation watchers and
// one ControlLimit reservation remain owned. No evidence escapes Exchange.
type ContextCompletion func(context.Context, *ContextAttempt, VerifiedContextExchange) (ContextResponse, error)

// ContextResponse is reply data with a one-shot synchronous owner recheck. It
// offers no raw send method or reusable response capability. Only the active
// inbound completion can return it to its still-owned exchange.
type ContextResponse struct {
	Reply endpointmeta.Reply
	Epoch *ContextEpoch
	Admit func() bool
}

type contextAttemptKey struct {
	peer      string
	operation ContextOperation
}
type contextAttemptData struct {
	n            *Node
	g            *runtimeGeneration
	origin       *generationOrigin
	registration *contextPeerRegistration
	key          contextAttemptKey
	direction    ContextDirection
	expected     []byte
	epoch        *ContextEpoch
	nodeDone     <-chan struct{}
	armRevision  uint64
}
type contextAttemptCell struct {
	original   *ContextAttempt
	data       atomic.Pointer[contextAttemptData]
	started    atomic.Bool
	claimed    atomic.Bool
	cancelled  chan struct{}
	cancelOnce sync.Once
}

// ContextAttempt is an opaque frozen association, not verified evidence.
// Copying its value never creates another accepted attempt pointer.
type ContextAttempt struct{ cell *contextAttemptCell }

func (a *ContextAttempt) Cancel() {
	if a != nil && a.cell != nil {
		a.cell.cancelOnce.Do(func() { close(a.cell.cancelled) })
	}
}

// Cancelled observes only terminal signals. It acquires no Node/generation
// mutex and invokes no callback, so the persistence owner can recheck it at
// publication admission. This observation grants no authentication authority.
func (a *ContextAttempt) Cancelled() bool {
	d := a.data()
	return d == nil || a.cancelled() || d.n.closing.Load() ||
		channelClosed(d.nodeDone) || channelClosed(d.g.stop)
}

func (a *ContextAttempt) data() *contextAttemptData {
	if a == nil || a.cell == nil || a.cell.original != a {
		return nil
	}
	return a.cell.data.Load()
}
func channelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
func (a *ContextAttempt) cancelled() bool {
	return a == nil || a.cell == nil || channelClosed(a.cell.cancelled)
}

func contextRequestOperation(request endpointmeta.Request) (ContextOperation, error) {
	switch r := request.(type) {
	case *endpointmeta.PrepareRequest:
		if r != nil {
			return ContextPrepare, nil
		}
	case endpointmeta.PrepareRequest:
		return ContextPrepare, nil
	case *endpointmeta.BoundRequest:
		if r != nil {
			return contextBoundOperation(*r)
		}
	case endpointmeta.BoundRequest:
		return contextBoundOperation(r)
	}
	return 0, ErrUnavailable
}
func contextBoundOperation(r endpointmeta.BoundRequest) (ContextOperation, error) {
	switch r.Operation {
	case "pair-context-commit":
		return ContextCommit, nil
	case "pair-context-status":
		return ContextStatus, nil
	}
	return 0, ErrUnavailable
}
func contextOperationName(op ContextOperation) string {
	switch op {
	case ContextPrepare:
		return "pair-context-prepare"
	case ContextCommit:
		return "pair-context-commit"
	case ContextStatus:
		return "pair-context-status"
	}
	return ""
}

func (n *Node) PrepareContextAttempt(peerKey string, request endpointmeta.Request, epoch *ContextEpoch) (*ContextAttempt, error) {
	return n.contextAttempt(peerKey, request, epoch, ContextOutbound)
}
func (n *Node) ArmContextAttempt(peerKey string, expected endpointmeta.Request, epoch *ContextEpoch) (*ContextAttempt, error) {
	return n.contextAttempt(peerKey, expected, epoch, ContextInbound)
}
func (n *Node) contextAttempt(peerKey string, request endpointmeta.Request, epoch *ContextEpoch, direction ContextDirection) (*ContextAttempt, error) {
	op, err := contextRequestOperation(request)
	if err != nil || !epoch.Valid() {
		return nil, ErrUnavailable
	}
	encoded, err := endpointmeta.Encode(request)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.controlReadyLocked(); err != nil {
		return nil, err
	}
	g := n.generation.Load()
	registration := g.controlPeers[peerKey]
	if registration == nil || !contextLocalRequest(g.cfg, registration.peer, encoded) {
		return nil, ErrUntrusted
	}
	if n.contextArmRevision == ^uint64(0) {
		return nil, ErrCapacity
	}
	n.contextArmRevision++
	key := contextAttemptKey{peerKey, op}
	if previous := n.contextAttempts[key]; previous != nil {
		previous.Cancel()
		// Active work retains its immutable local capture until real cleanup;
		// its old association is never relabeled as this replacement arm.
		previous.cell.data.Store(nil)
	}
	a := &ContextAttempt{cell: &contextAttemptCell{cancelled: make(chan struct{})}}
	a.cell.original = a
	a.cell.data.Store(&contextAttemptData{n: n, g: g, origin: g.origin, registration: registration,
		key: key, direction: direction, expected: bytes.Clone(encoded), epoch: epoch, nodeDone: n.ctx.Done(), armRevision: n.contextArmRevision})
	n.contextAttempts[key] = a
	return a, nil
}
func contextLocalRequest(cfg Config, peer Peer, data []byte) bool {
	r, err := endpointmeta.ParseRequest(data)
	if err != nil {
		return false
	}
	if p, ok := r.(*endpointmeta.PrepareRequest); ok {
		return p.Sender == cfg.Identity.PublicKey() && p.Recipient == peer.Key &&
			p.SenderTunnelKey == cfg.Identity.TunnelKey() && p.RecipientTunnelKey == peer.TunnelKey &&
			p.SenderEndpoint == cfg.Listen.String() && p.RecipientEndpoint == peer.Endpoint.String()
	}
	_, err = contextRequestOperation(r)
	return err == nil
}
func (a *ContextAttempt) finish(d *contextAttemptData) {
	a.Cancel()
	d.n.mu.Lock()
	if d.n.contextAttempts[d.key] == a {
		delete(d.n.contextAttempts, d.key)
	}
	a.cell.data.Store(nil)
	d.n.mu.Unlock()
}

// ContextExchangeTranscript contains copied data only, never a Node/generation
// capability. Each accessor decodes fresh owned slices from canonical bytes.
type ContextExchangeTranscript struct {
	request, reply []byte
	operation      ContextOperation
	direction      ContextDirection
}

func (t ContextExchangeTranscript) Request() endpointmeta.Request {
	r, _ := endpointmeta.ParseRequest(t.request)
	return r
}
func (t ContextExchangeTranscript) Reply() endpointmeta.Reply {
	r, _ := endpointmeta.ParseReply(t.reply, contextOperationName(t.operation))
	return r
}
func (t ContextExchangeTranscript) Operation() ContextOperation { return t.operation }
func (t ContextExchangeTranscript) Direction() ContextDirection { return t.direction }

type contextVerifiedData struct {
	attempt    *ContextAttempt
	data       *contextAttemptData
	transcript ContextExchangeTranscript
	deadline   time.Time
	done       <-chan struct{}
	pinned     bool
}
type contextClaimCell struct {
	used  atomic.Bool
	value atomic.Pointer[contextVerifiedData]
}

// Only a completed pinned v2 TLS path in this package can mint this wrapper.
// Copies share the single claim cell; zero values and wrong attempt pointers fail.
type VerifiedContextExchange struct{ cell *contextClaimCell }

func (v VerifiedContextExchange) TryClaimFor(a *ContextAttempt) (ContextExchangeTranscript, error) {
	if v.cell == nil || !v.cell.used.CompareAndSwap(false, true) {
		return ContextExchangeTranscript{}, ErrUntrusted
	}
	proof := v.cell.value.Swap(nil)
	if proof == nil || proof.attempt != a || a.data() != proof.data || !proof.pinned {
		return ContextExchangeTranscript{}, ErrUntrusted
	}
	d := proof.data
	if proof.transcript.operation != d.key.operation || proof.transcript.direction != d.direction ||
		(d.direction == ContextOutbound && (!bytes.Equal(proof.transcript.request, d.expected) || !contextReplyMatches(d, proof.transcript.request, proof.transcript.reply))) ||
		(d.direction == ContextInbound && (len(proof.transcript.reply) != 0 || !contextInboundRequestMatches(d, proof.transcript.request))) {
		a.Cancel()
		return ContextExchangeTranscript{}, ErrUntrusted
	}
	if !tryContextCurrent(a, d, d.epoch, proof.deadline, proof.done, false) {
		a.Cancel()
		return ContextExchangeTranscript{}, ErrUnavailable
	}
	// Old arm invalidation must still reject a future claim, but the accepted
	// callback now owns completion. Its own durable write invalidates the old
	// epoch and installs a distinct response epoch; it cannot roll back a save.
	a.cell.claimed.Store(true)
	if !d.epoch.Valid() {
		a.Cancel()
		return ContextExchangeTranscript{}, ErrUnavailable
	}
	return ContextExchangeTranscript{request: bytes.Clone(proof.transcript.request), reply: bytes.Clone(proof.transcript.reply), operation: d.key.operation, direction: d.direction}, nil
}

// No engine.Admit, open(), external callback, context method or blocking lock
// occurs inside this gate. Contention consumes/rejects rather than waiting.
func tryContextCurrent(a *ContextAttempt, d *contextAttemptData, epoch *ContextEpoch, deadline time.Time, done <-chan struct{}, response bool) bool {
	if a == nil || d == nil || !epoch.Valid() || channelClosed(done) || a.cancelled() || !time.Now().Before(deadline) {
		return false
	}
	n, g := d.n, d.g
	if !n.mu.TryLock() {
		return false
	}
	defer n.mu.Unlock()
	if !g.mu.TryLock() {
		return false
	}
	defer g.mu.Unlock()
	if !n.contextControl || n.closed || n.closing.Load() || n.recovery || !n.started || n.generation.Load() != g ||
		g.sealed || g.engine.Load() != nil || g.bind != nil || g.tunnel != nil || !g.controlOpen.Load() || g.origin != d.origin || d.registration == nil || d.registration.g != g ||
		g.controlPeers[d.key.peer] != d.registration || d.registration.peer.Key != d.key.peer ||
		n.contextAttempts[d.key] != a || a.data() != d || !epoch.Valid() || channelClosed(done) || a.cancelled() ||
		channelClosed(d.nodeDone) || !time.Now().Before(deadline) {
		return false
	}
	return !response || a.cell.claimed.Load()
}

func mintContextExchange(a *ContextAttempt, d *contextAttemptData, ctx context.Context, request, reply []byte) VerifiedContextExchange {
	deadline, ok := ctx.Deadline()
	if !ok {
		return VerifiedContextExchange{}
	}
	cell := &contextClaimCell{}
	cell.value.Store(&contextVerifiedData{attempt: a, data: d, deadline: deadline, done: ctx.Done(), pinned: true,
		transcript: ContextExchangeTranscript{request: bytes.Clone(request), reply: bytes.Clone(reply), operation: d.key.operation, direction: d.direction}})
	return VerifiedContextExchange{cell: cell}
}

// watchContextEpoch is owned by the same exchange reservation and always joined.
// Once evidence is claimed, its initial epoch no longer owns cancellation of
// the callback's write. The response has a separate current-revision watcher.
func watchContextEpoch(epoch *ContextEpoch, a *ContextAttempt, initial bool, c net.Conn) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-stop:
		case <-epoch.cell.done:
			if !initial || !a.cell.claimed.Load() {
				_ = c.Close()
			}
		}
	}()
	return func() { close(stop); <-done }
}
