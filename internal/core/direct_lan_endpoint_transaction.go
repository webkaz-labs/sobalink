package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// EndpointTransaction is an opaque, retained Core-owned endpoint transaction.
// It has no public constructor, activation method, receipt setter or config
// export. The later staged-publication coordinator must consume this exact
// pointer under Core.op after checking endpointTransactionCurrentLocked.
// Ordinary dispatch delegates only authenticated observations to the Core job.
type EndpointTransaction struct {
	deliveries                                  map[string]*directlan.EndpointDelivery // exact published generation targets, Core.op
	core                                        *Core
	backend                                     *directLANBackend
	root                                        NetworkBackend
	store                                       *directLANStore
	process, path, file, state, profile, denied string
	revision, trust                             uint64
	policyRevision                              uint64
	baseFile, baseState                         string
	baseRevision                                uint64
	limits                                      lanStoreLimits
	limitsSource                                *lanStoreLimits
	previous, receipt                           *contextPublicationReceipt
	old                                         endpointOldOwner
	origin                                      *transportorigin.Token
	fence                                       endpointmeta.Snapshot
	deadlines                                   map[directLANEndpointDeadlineKey]directLANEndpointDeadline
	proposalCutoff                              time.Time
	joined, durable                             bool
	failure                                     error
	candidate                                   *directlan.PreparedTransport
	staging, published, constructionReturned    bool
}

// The only production implementation captures a concrete Node and its exact
// origin. Tests may substitute an inert owner, never a transport simulation.
// Begin is signal-only; Wait must include all existing origin participants.
type endpointOldOwner interface {
	identity() *transportorigin.Token
	begin() error
	wait(context.Context) error
}
type endpointNodeOwner struct {
	node           *directlan.Node
	origin         transportorigin.Origin
	retired        *directlan.TransportRetirement
	observed       time.Time // first accepted input observation, never reset after wire release
	proposalCutoff time.Time // exact pre-signed outgoing batch freshness, if present
}

func (o *endpointNodeOwner) identity() *transportorigin.Token { return o.origin.Identity() }
func (o *endpointNodeOwner) begin() error {
	r, err := o.node.BeginTransportRetirement(o.origin)
	if err == nil {
		if o.retired != nil && o.retired != r {
			return directlan.ErrUntrusted
		}
		o.retired = r
	}
	return err
}
func (o *endpointNodeOwner) wait(ctx context.Context) error {
	if o.retired == nil {
		return directlan.ErrRetirementIncomplete
	}
	return o.retired.Wait(ctx)
}

// Direct callers MUST release all counted old-generation work before calling
// it, including any origin lease and synchronous control/callback ownership.
// Core.Close may hold Core.op while joining that work; waiting to reacquire op
// from inside a counted callback would deadlock shutdown.
// It acquires Core.op itself; no caller may hold Core/store/Node locks.
func (c *Core) saveEndpointTransaction(ctx context.Context, b *directLANBackend, mutation endpointmeta.Mutation) (*EndpointTransaction, error) {
	if b == nil || b.Node == nil {
		return nil, endpointmeta.ErrReview
	}
	origin, err := b.Node.CaptureTransportOrigin()
	if err != nil {
		return nil, err
	}
	return c.saveEndpointTransactionWithOwner(ctx, b, mutation, &endpointNodeOwner{node: b.Node, origin: origin}, time.Now)
}

func (c *Core) endpointTransactionOwnerCurrentLocked(ctx context.Context, t *EndpointTransaction) bool {
	if ctx == nil || ctx.Err() != nil || t == nil || t.core != c {
		return false
	}
	if owner, ok := t.old.(*endpointNodeOwner); ok && owner.node != t.backend.Node {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closing || c.ctx == nil || c.ctx.Err() != nil || c.directLAN != t.store || c.node != t.root ||
		c.lanStartNonce != t.process || t.process == "" || c.trustGeneration != t.trust ||
		c.lanStartWriteRevision.Load() != t.policyRevision || c.lanStartUncertain.Load() ||
		privateRevision(c.profile) != t.profile || privateRevision(c.managedDenied) != t.denied ||
		c.managedCleanupPending || c.managedCleanupError != nil || t.backend.store != t.store || t.backend.endpointStopped.Load() ||
		t.backend.endpointTransaction != t || t.old.identity() != t.origin {
		return false
	}
	switch root := c.node.(type) {
	case *directLANBackend:
		return root == t.backend
	case *mixedBackend:
		return root.nodes["direct-lan"] == t.backend
	default:
		return false
	}
}

func (s *directLANStore) matchEndpointTransactionLocked(t *EndpointTransaction, now time.Time) error {
	if t == nil || s.endpointTransaction != t || t.store != s || t.path != s.path || t.file != s.fileDigest ||
		t.state != privateRevision(s.state) || t.revision != s.reviewRevision || t.limits != *s.currentCapacity() ||
		t.limitsSource != s.limits.Load() || s.recovery {
		return endpointmeta.ErrReview
	}
	if !t.durable && s.contextPublication != nil {
		return endpointmeta.ErrReview
	}
	if err := s.endpointFileCurrentLocked(); err != nil {
		return err
	}
	if err := s.observeEndpointTimeLocked(now); err != nil {
		return err
	}
	// Never reconstruct or refresh the first process-monotonic cutoffs after a
	// join. Missing, changed or elapsed bounds fail closed, including wall rollback.
	for key, bound := range t.deadlines {
		got, ok := s.endpointDeadlines[key]
		if !ok || got.abs != bound.abs || !got.monotonic.Equal(bound.monotonic) || got.expired || bound.expired || !now.Before(bound.monotonic) {
			return endpointmeta.ErrExpired
		}
	}
	return nil
}

// This is the interface for the subsequent staged-publication increment. It
// returns only an error, not a transferable grant. Call with Core.op and store.mu
// held, after owner validation (which takes Core.mu before store.mu).
func (c *Core) endpointTransactionCurrentLocked(t *EndpointTransaction, now time.Time) error {
	if t == nil || t.core != c || t.failure != nil || !t.joined || !t.durable || t.receipt == nil {
		return endpointmeta.ErrReview
	}
	s := t.store
	if err := t.deadlineErrorAt(now); err != nil {
		return err
	}
	if err := s.matchEndpointTransactionLocked(t, now); err != nil {
		return err
	}
	if s.contextPublication != t.receipt || !s.contextPublicationCurrentLocked(t.process) {
		return endpointmeta.ErrReview
	}
	_, err := s.managedCurrentEndpointProjectionLocked(now)
	return err
}

// Immutable process cutoffs only. Safe for the writer's synchronous admission
// check: no locks, owner callbacks, wall-derived renewal or network work.
func (t *EndpointTransaction) deadlineErrorAt(now time.Time) error {
	if !t.proposalCutoff.IsZero() && !now.Before(t.proposalCutoff) {
		return endpointmeta.ErrExpired
	}
	for _, bound := range t.deadlines {
		if bound.expired || !now.Before(bound.monotonic) {
			return endpointmeta.ErrExpired
		}
	}
	return nil
}

func (t *EndpointTransaction) snapshotLocked() {
	t.file, t.state, t.revision = t.store.fileDigest, privateRevision(t.store.state), t.store.reviewRevision
}

func (c *Core) saveEndpointTransactionWithOwner(ctx context.Context, b *directLANBackend, mutation endpointmeta.Mutation, old endpointOldOwner, clock func() time.Time, checks ...func(*directLANStore) error) (*EndpointTransaction, error) {
	if ctx == nil || b == nil || old == nil || old.identity() == nil || clock == nil {
		return nil, endpointmeta.ErrReview
	}
	c.op.Lock()
	c.mu.RLock()
	s := c.directLAN
	t := &EndpointTransaction{core: c, backend: b, root: c.node, store: s, process: c.lanStartNonce,
		profile: privateRevision(c.profile), denied: privateRevision(c.managedDenied), trust: c.trustGeneration, policyRevision: c.lanStartWriteRevision.Load(), old: old, origin: old.identity()}
	c.mu.RUnlock()
	if s == nil || b.endpointTransaction != nil || b.store != s {
		c.op.Unlock()
		return nil, endpointmeta.ErrReview
	}
	// The provisional slot permits exact owner validation but is removed on any
	// pre-disruption rejection. No route/epoch is touched before full preflight.
	b.endpointTransaction = t
	reject := func(err error) (*EndpointTransaction, error) {
		b.endpointTransaction = nil
		c.op.Unlock()
		return nil, err
	}
	if !c.endpointTransactionOwnerCurrentLocked(ctx, t) {
		return reject(endpointmeta.ErrReview)
	}
	s.mu.Lock()
	admitted, err := func() (bool, error) {
		if s.endpointTransaction != nil || !s.contextPublicationCurrentLocked(t.process) {
			return false, endpointmeta.ErrReview
		}
		for _, check := range checks {
			if check == nil {
				return false, endpointmeta.ErrReview
			}
			if err := check(s); err != nil {
				return false, err
			}
		}
		if owner, ok := old.(*endpointNodeOwner); ok {
			completion := b.currentCompletion()
			if owner.node != b.Node || completion == nil || (completion.stopped.Load() && (owner.retired == nil || completion.deadline.IsZero() || time.Now().Before(completion.deadline))) || completion.core != c || completion.backend != b || completion.root != t.root || completion.store != s || completion.process != t.process || completion.receipt != s.contextPublication {
				return false, endpointmeta.ErrReview
			}
		}
		// Reject structurally incomplete direct proposals before reducers that
		// expect a validated approval for reapproval.
		if mutation.Kind == "reapprove" && (mutation.State == nil || mutation.State.Approval == nil) {
			return false, endpointmeta.ErrInvalid
		}
		now := clock()
		before, err := s.endpointModelLocked(now, false)
		if err != nil {
			return false, err
		}
		if s.reviewRevision > ^uint64(0)-2 {
			return false, endpointmeta.ErrCapacity
		}
		budget, err := s.endpointBudgetLocked()
		if err != nil {
			return false, err
		}
		review, err := endpointmeta.PreviewMutation(*before, mutation)
		if err != nil {
			return false, err
		}
		var nonce [32]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return false, err
		}
		fence, err := endpointmeta.Fence(*before, mutation, review, base64.RawURLEncoding.EncodeToString(nonce[:]), now, budget)
		if err != nil {
			return false, err
		}
		final, err := endpointmeta.FinishPending(fence, false, now, budget)
		if err != nil {
			return false, err
		}
		if err = s.preflightEndpointSnapshotLocked(fence, false); err != nil {
			return false, err
		}
		if err = s.preflightEndpointSnapshotFromLocked(fence, final, true); err != nil {
			return false, err
		}
		// Validate the complete prospective runtime projection before touching the
		// valid old route. This value is inert and never certifies publication.
		preview := &directLANStore{state: cloneDirectLANState(s.state)}
		preview.state.Metadata = cloneDirectLANMetadata(&fence)
		prospective, err := preview.stateWithEndpointMetadataLocked(final)
		if err != nil {
			return false, err
		}
		projection, err := projectManagedCurrentEndpoint(prospective, now)
		if err != nil {
			return false, err
		}
		if s.currentCapacity().peers > 0 && int64(len(projection.Peers)) > s.currentCapacity().peers {
			return false, directlan.ErrCapacity
		}

		s.observeEndpointDeadlinesLocked(fence, now)
		observed := now
		if owner, ok := old.(*endpointNodeOwner); ok {
			if !owner.observed.IsZero() {
				observed = owner.observed
			}
			t.proposalCutoff = owner.proposalCutoff
		}
		// Preserve the accepted observation's original cutoffs across the wire
		// release/job handoff. Existing earlier proof/follow bounds always win.
		for key, absolute := range endpointDeadlineKeys(mutation.State) {
			bound, ok := s.endpointDeadlines[key]
			if !ok {
				continue
			}
			expires, _ := time.Parse(time.RFC3339Nano, absolute)
			first := observed.Add(expires.Sub(observed.UTC()))
			if first.Before(bound.monotonic) {
				bound.monotonic = first
			}
			bound.expired = bound.expired || !now.Before(bound.monotonic)
			s.endpointDeadlines[key] = bound
		}
		if err = s.endpointDeadlineErrorLocked(mutation.State, mutation.Kind, now); err != nil {
			s.observeEndpointDeadlinesLocked(*before, now)
			return false, err
		}
		if err = (&contextSaveLiveness{ctx: ctx, core: c.ctx, endpoint: t}).err(); err != nil {
			s.observeEndpointDeadlinesLocked(*before, now)
			return false, err
		}
		t.path, t.previous, t.limits, t.limitsSource = s.path, s.contextPublication, *s.currentCapacity(), s.limits.Load()
		t.baseFile, t.baseState, t.baseRevision = s.fileDigest, privateRevision(s.state), s.reviewRevision
		t.fence = fence
		t.deadlines = make(map[directLANEndpointDeadlineKey]directLANEndpointDeadline)
		// Retain only bounds used by the final authority. Old replaced proof bounds
		// may legitimately disappear, but surviving proof/follow clocks never renew.
		for _, r := range final.Peers {
			// Terminal and inactive records retain their proof/approval history, but
			// cannot impose a runtime cutoff on unrelated positively projected peers.
			if _, active := projection.PairContexts[r.Peer.Key]; !active {
				continue
			}
			e := r.EndpointState
			if e == nil || e.Approval == nil {
				continue
			}
			for key := range endpointDeadlineKeys(e) {
				if key.kind == "follow" && e.Approval.Kind != "follow" {
					continue
				}
				if bound, ok := s.endpointDeadlines[key]; ok {
					t.deadlines[key] = bound
				}
			}
		}
		// A withdrawal proof also has an input freshness cutoff, although it
		// contributes no positive runtime authority in the final projection.
		if mutation.Kind == "set" || mutation.Kind == "withdraw" {
			proof := fence.PendingChange.Mutation.State.ReceivedProof
			if proof.Update.Lifetime == "finite" {
				absolute, _ := time.Parse(time.RFC3339Nano, proof.Update.Expires)
				t.proposalCutoff = observed.Add(absolute.Sub(observed.UTC()))
			}
		}
		// Reducer/signature/preflight work can consume a short offer's remaining
		// lifetime. Check the original bounds again immediately before disruption.
		admissionTime := clock()
		if err = s.observeEndpointTimeLocked(admissionTime); err == nil {
			err = t.deadlineErrorAt(admissionTime)
		}
		if err != nil {
			s.observeEndpointDeadlinesLocked(*before, admissionTime)
			return false, err
		}

		s.endpointTransaction = t
		// Signal-only: seals exact physical admissions before the fence publisher
		// invalidates the old receipt. Any failure now retains both owner and slot.
		b.replacing.Store(t)
		if err = old.begin(); err != nil {
			return true, err
		}
		if completion := b.currentCompletion(); completion != nil {
			completion.invalidate()
		}
		state, err := s.stateWithEndpointMetadataLocked(fence)
		if err == nil {
			err = s.writeContextStateLocked(state, &contextSaveLiveness{ctx: ctx, core: c.ctx, endpoint: t})
		}
		t.snapshotLocked()
		return true, err
	}()
	s.mu.Unlock()
	if !admitted {
		return reject(err)
	}
	if err != nil {
		t.failure = err
		c.op.Unlock()
		return t, err
	}
	c.op.Unlock()
	// Exact old generation and origin accounting only; never a current-backend
	// lookup. No operation, store, Core or Node lock is held during this join.
	joinErr := old.wait(ctx)
	c.op.Lock()
	defer c.op.Unlock()
	if joinErr != nil {
		t.failure = joinErr
		return t, joinErr
	}
	t.joined = true
	if !c.endpointTransactionOwnerCurrentLocked(ctx, t) {
		t.failure = endpointmeta.ErrReview
		return t, t.failure
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := clock()
	err = s.matchEndpointTransactionLocked(t, now)
	if err == nil && privateRevision(s.state.Metadata) != privateRevision(&t.fence) {
		err = endpointmeta.ErrReview
	}
	if err == nil {
		budget, e := s.endpointBudgetLocked()
		err = e
		if err == nil {
			final, e := endpointmeta.FinishPending(t.fence, false, now, budget)
			err = e
			if err == nil {
				err = s.preflightEndpointSnapshotLocked(final, false)
			}
			if err == nil {
				var next directLANState
				next, err = s.stateWithEndpointMetadataLocked(final)
				if err == nil {
					err = s.writeContextPublicationLocked(t.process, next, &contextSaveLiveness{ctx: ctx, core: c.ctx, endpoint: t})
				}
			}
		}
	}
	if err != nil {
		t.failure = err
		return t, err
	}
	t.snapshotLocked()
	t.receipt, t.durable = s.contextPublication, true
	// Final durability is not activation. Cancellation after successful save
	// retains saved-but-unavailable state and cannot roll it back.
	if err = ctx.Err(); err == nil {
		err = c.ctx.Err()
	}
	if err == nil && b.endpointStopped.Load() {
		err = context.Canceled
	}
	if err == nil {
		err = c.endpointTransactionCurrentLocked(t, clock())
	}
	if err != nil {
		t.failure = err
		return t, err
	}
	return t, nil
}
