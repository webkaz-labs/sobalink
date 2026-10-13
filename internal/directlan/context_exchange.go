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

// ContextClaimSite is a fixed diagnostic label, never an error payload.
type ContextClaimSite string

// ContextClaimObservation is diagnostic data only. Its caller owns it for one
// synchronous claim and may read it only after return. It contains no capability,
// identifier or callback, and is never consulted for admission.
type ContextClaimObservation struct{ Site ContextClaimSite }

func (o *ContextClaimObservation) record(site ContextClaimSite) {
	if o != nil {
		o.Site = site
	}
}

func (v VerifiedContextExchange) TryClaimFor(a *ContextAttempt) (ContextExchangeTranscript, error) {
	return v.TryClaimForObserved(a, nil)
}

// TryClaimForObserved performs the same one-shot claim as TryClaimFor. A nil
// observation does no diagnostic sampling or allocation. Sites are closed
// literals at the actual return checks, never a later recheck of live state.
func (v VerifiedContextExchange) TryClaimForObserved(a *ContextAttempt, observation *ContextClaimObservation) (ContextExchangeTranscript, error) {
	if v.cell == nil {
		observation.record("proof-empty")
		return ContextExchangeTranscript{}, ErrUntrusted
	}
	if !v.cell.used.CompareAndSwap(false, true) {
		observation.record("proof-used")
		return ContextExchangeTranscript{}, ErrUntrusted
	}
	proof := v.cell.value.Swap(nil)
	if proof == nil {
		observation.record("proof-missing")
		return ContextExchangeTranscript{}, ErrUntrusted
	}
	if proof.attempt != a {
		observation.record("proof-attempt")
		return ContextExchangeTranscript{}, ErrUntrusted
	}
	if a.data() != proof.data {
		observation.record("proof-data")
		return ContextExchangeTranscript{}, ErrUntrusted
	}
	if !proof.pinned {
		observation.record("proof-unpinned")
		return ContextExchangeTranscript{}, ErrUntrusted
	}
	d := proof.data
	if proof.transcript.operation != d.key.operation {
		observation.record("transcript-operation")
		a.Cancel()
		return ContextExchangeTranscript{}, ErrUntrusted
	}
	if proof.transcript.direction != d.direction {
		observation.record("transcript-direction")
		a.Cancel()
		return ContextExchangeTranscript{}, ErrUntrusted
	}
	if d.direction == ContextOutbound {
		if !bytes.Equal(proof.transcript.request, d.expected) {
			observation.record("outbound-request")
			a.Cancel()
			return ContextExchangeTranscript{}, ErrUntrusted
		}
		if !contextReplyMatches(d, proof.transcript.request, proof.transcript.reply) {
			observation.record("outbound-reply")
			a.Cancel()
			return ContextExchangeTranscript{}, ErrUntrusted
		}
	}
	if d.direction == ContextInbound {
		if len(proof.transcript.reply) != 0 {
			observation.record("inbound-reply")
			a.Cancel()
			return ContextExchangeTranscript{}, ErrUntrusted
		}
		if !contextInboundRequestMatches(d, proof.transcript.request) {
			observation.record("inbound-request")
			a.Cancel()
			return ContextExchangeTranscript{}, ErrUntrusted
		}
	}
	if !tryContextCurrentObserved(a, d, d.epoch, proof.deadline, proof.done, false, observation) {
		a.Cancel()
		return ContextExchangeTranscript{}, ErrUnavailable
	}
	// Old arm invalidation must still reject a future claim, but the accepted
	// callback now owns completion. Its own durable write invalidates the old
	// epoch and installs a distinct response epoch; it cannot roll back a save.
	if !claimContextEpochCurrent(a, d.epoch, observation) {
		return ContextExchangeTranscript{}, ErrUnavailable
	}
	observation.record("claimed")
	return ContextExchangeTranscript{request: bytes.Clone(proof.transcript.request), reply: bytes.Clone(proof.transcript.reply), operation: d.key.operation, direction: d.direction}, nil
}

// Signal-only final check, separated so its invalidation boundary can be
// exercised without manufacturing authenticated evidence.
func claimContextEpochCurrent(a *ContextAttempt, epoch *ContextEpoch, observation *ContextClaimObservation) bool {
	a.cell.claimed.Store(true)
	if !epoch.Valid() {
		observation.record("post-claim-epoch")
		a.Cancel()
		return false
	}
	return true
}

// No engine.Admit, open(), external callback, context method or blocking lock
// occurs inside this gate. Contention consumes/rejects rather than waiting.
func tryContextCurrent(a *ContextAttempt, d *contextAttemptData, epoch *ContextEpoch, deadline time.Time, done <-chan struct{}, response bool) bool {
	return tryContextCurrentObserved(a, d, epoch, deadline, done, response, nil)
}

func tryContextCurrentObserved(a *ContextAttempt, d *contextAttemptData, epoch *ContextEpoch, deadline time.Time, done <-chan struct{}, response bool, observation *ContextClaimObservation) bool {
	if a == nil {
		observation.record("initial-attempt-missing")
		return false
	}
	if d == nil {
		observation.record("initial-data-missing")
		return false
	}
	if !epoch.Valid() {
		observation.record("initial-epoch")
		return false
	}
	if channelClosed(done) {
		observation.record("initial-exchange-done")
		return false
	}
	if a.cancelled() {
		observation.record("initial-attempt-cancelled")
		return false
	}
	if !time.Now().Before(deadline) {
		observation.record("initial-deadline")
		return false
	}
	n, g := d.n, d.g
	if !n.mu.TryLock() {
		observation.record("node-lock")
		return false
	}
	defer n.mu.Unlock()
	if !g.mu.TryLock() {
		observation.record("generation-lock")
		return false
	}
	defer g.mu.Unlock()
	return contextCurrentLockedObserved(a, d, epoch, deadline, done, response, observation)
}

// The caller holds the same Node and generation locks acquired above.
func contextCurrentLockedObserved(a *ContextAttempt, d *contextAttemptData, epoch *ContextEpoch, deadline time.Time, done <-chan struct{}, response bool, observation *ContextClaimObservation) bool {
	n, g := d.n, d.g
	if !n.contextControl {
		observation.record("node-mode")
		return false
	}
	if n.closed {
		observation.record("node-closed")
		return false
	}
	if n.closing.Load() {
		observation.record("node-closing")
		return false
	}
	if n.recovery {
		observation.record("node-recovery")
		return false
	}
	if !n.started {
		observation.record("node-not-started")
		return false
	}
	if n.generation.Load() != g {
		observation.record("generation-identity")
		return false
	}
	if g.sealed {
		observation.record("generation-sealed")
		return false
	}
	if g.engine.Load() != nil {
		observation.record("generation-engine")
		return false
	}
	if g.bind != nil {
		observation.record("generation-bind")
		return false
	}
	if g.tunnel != nil {
		observation.record("generation-tunnel")
		return false
	}
	if !g.controlOpen.Load() {
		observation.record("generation-control-closed")
		return false
	}
	if g.origin != d.origin {
		observation.record("origin-identity")
		return false
	}
	if d.registration == nil {
		observation.record("registration-missing")
		return false
	}
	if d.registration.g != g {
		observation.record("registration-generation")
		return false
	}
	if g.controlPeers[d.key.peer] != d.registration {
		observation.record("registration-identity")
		return false
	}
	if d.registration.peer.Key != d.key.peer {
		observation.record("registration-peer")
		return false
	}
	if n.contextAttempts[d.key] != a {
		observation.record("attempt-identity")
		return false
	}
	if a.data() != d {
		observation.record("attempt-data")
		return false
	}
	if !epoch.Valid() {
		observation.record("locked-epoch")
		return false
	}
	if channelClosed(done) {
		observation.record("locked-exchange-done")
		return false
	}
	if a.cancelled() {
		observation.record("locked-attempt-cancelled")
		return false
	}
	if channelClosed(d.nodeDone) {
		observation.record("node-done")
		return false
	}
	if !time.Now().Before(deadline) {
		observation.record("locked-deadline")
		return false
	}
	if response && !a.cell.claimed.Load() {
		observation.record("response-unclaimed")
		return false
	}
	observation.record("current")
	return true
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
