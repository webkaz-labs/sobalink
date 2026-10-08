package core

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Private source-only operations. No command, startup, runtime or application
// caller is installed. Callers must hold Core.op and exclusive profile ownership.
// A terminal marker denies the old binding; future re-pair/history is deferred.
type pairRecordOperation uint8

const (
	pairRecordMigrate pairRecordOperation = iota + 1
	pairRecordRevoke
)

type pairRecordInputs struct {
	operation pairRecordOperation
	peer      string
	republish bool
}

type pairRecordAdmission struct {
	core                                    *Core
	store                                   *directLANStore
	process, path, file, state, inputDigest string
	writeRevision                           uint64
	capacity                                lanStoreLimits
	inputs                                  pairRecordInputs
	target                                  endpointmeta.PeerRecord
	targetDigest, binding                   string
}

// This is a save outcome, never a context/application publication receipt.
// A model no-op is not durable. An explicitly requested republish can be.
type pairRecordSaveResult struct{ changed, published, durable bool }

// Err reads both concrete cancellation sources at the synchronous publisher's
// final admission. No owner, context attempt, callback or goroutine is created.
type pairRecordContext struct {
	context.Context
	core context.Context
}

func (c pairRecordContext) Err() error { return errors.Join(c.Context.Err(), c.core.Err()) }

func (c *Core) pairRecordOwnerLocked(ctx context.Context, s *directLANStore, process string) error {
	if ctx == nil || s == nil || process == "" {
		return endpointmeta.ErrReview
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.ctx == nil || c.closing || c.node != nil || c.attemptedNetwork != "" || c.contextControl != nil || c.directLAN != s || c.lanStartNonce != process {
		return endpointmeta.ErrReview
	}
	return c.ctx.Err()
}

// Passive v3/v4 observation is deliberately separate from endpointModelLocked.
// Preserve existing clock/expiry latches and preparation windows. Retained
// expired consent is evidence and cannot veto a terminal reduction.
func (s *directLANStore) pairRecordModelLocked(now time.Time) (*endpointmeta.Snapshot, error) {
	if (s.state.Version != directLANMetadataStateVersion && s.state.Version != directLANPairRecordStateVersion) || s.state.Metadata == nil {
		return nil, endpointmeta.ErrReview
	}
	if s.recovery || s.state.Metadata.PendingChange != nil {
		return nil, directlan.ErrRecovery
	}
	if err := s.endpointFileCurrentLocked(); err != nil {
		return nil, err
	}
	if err := s.observeEndpointTimeLocked(now); err != nil {
		return nil, err
	}
	if err := validateDirectLANState(s.state); err != nil {
		s.recovery = true
		return nil, directlan.ErrRecovery
	}
	if err := s.state.Metadata.ValidateAt(now); err != nil {
		s.recovery = true
		return nil, directlan.ErrRecovery
	}
	s.observeEndpointDeadlinesLocked(*s.state.Metadata, now)
	return cloneDirectLANMetadata(s.state.Metadata), nil
}

func pairRecordInputDigest(in pairRecordInputs) string {
	return privateRevision(struct {
		Operation pairRecordOperation
		Peer      string
		Republish bool
	}{in.operation, in.peer, in.republish})
}

// Requires store.mu as well as the lifecycle ownership documented above.
func (c *Core) capturePairRecordAdmissionLocked(ctx context.Context, s *directLANStore, process string, in pairRecordInputs, now time.Time) (pairRecordAdmission, error) {
	if err := c.pairRecordOwnerLocked(ctx, s, process); err != nil {
		return pairRecordAdmission{}, err
	}
	m, err := s.pairRecordModelLocked(now)
	if err != nil {
		return pairRecordAdmission{}, err
	}
	a := pairRecordAdmission{core: c, store: s, process: process, path: s.path, file: s.fileDigest, state: privateRevision(s.state), writeRevision: s.reviewRevision, capacity: *s.currentCapacity(), inputs: in, inputDigest: pairRecordInputDigest(in)}
	switch in.operation {
	case pairRecordMigrate:
		if in.peer != "" || in.republish || m.Version != directLANMetadataStateVersion {
			return pairRecordAdmission{}, endpointmeta.ErrReview
		}
	case pairRecordRevoke:
		if m.Version != directLANPairRecordStateVersion || in.peer == "" {
			return pairRecordAdmission{}, endpointmeta.ErrReview
		}
		i, err := contextPeer(*m, in.peer)
		if err != nil {
			return pairRecordAdmission{}, err
		}
		a.target = m.Peers[i] // m is already a deep copy, never a caller-owned record.
		if a.target.PairContext == nil || a.target.EndpointState == nil {
			return pairRecordAdmission{}, endpointmeta.ErrReview
		}
		a.binding, err = a.target.PairContext.Binding()
		if err != nil {
			return pairRecordAdmission{}, err
		}
		a.targetDigest = privateRevision(a.target)
	default:
		return pairRecordAdmission{}, endpointmeta.ErrInvalid
	}
	if _, _, err := s.pairRecordCandidateLocked(a, now); err != nil {
		return pairRecordAdmission{}, err
	}
	return a, nil
}

func (c *Core) matchPairRecordAdmissionLocked(ctx context.Context, s *directLANStore, process string, a pairRecordAdmission, now time.Time) error {
	if err := c.pairRecordOwnerLocked(ctx, s, process); err != nil {
		return err
	}
	if a.core != c || a.store != s || a.process != process || a.path != s.path || a.file != s.fileDigest || a.state != privateRevision(s.state) || a.writeRevision != s.reviewRevision || a.capacity != *s.currentCapacity() || a.inputDigest != pairRecordInputDigest(a.inputs) {
		return endpointmeta.ErrReview
	}
	m, err := s.pairRecordModelLocked(now)
	if err != nil {
		return err
	}
	if a.inputs.operation == pairRecordRevoke {
		i, err := contextPeer(*m, a.inputs.peer)
		if err != nil || a.targetDigest == "" || a.targetDigest != privateRevision(a.target) || !reflect.DeepEqual(a.target, m.Peers[i]) || a.target.PairContext == nil {
			return endpointmeta.ErrReview
		}
		binding, err := a.target.PairContext.Binding()
		if err != nil || binding != a.binding {
			return endpointmeta.ErrReview
		}
	} else if a.inputs.operation != pairRecordMigrate || a.inputs.peer != "" || a.inputs.republish || a.binding != "" || a.targetDigest != "" || !reflect.DeepEqual(a.target, endpointmeta.PeerRecord{}) {
		return endpointmeta.ErrReview
	}
	return nil
}

func (s *directLANStore) pairRecordCandidateLocked(a pairRecordAdmission, now time.Time) (directLANState, bool, error) {
	budget, err := s.endpointBudgetLocked()
	if err != nil {
		return directLANState{}, false, err
	}
	if s.state.Metadata == nil {
		return directLANState{}, false, endpointmeta.ErrReview
	}
	before := *s.state.Metadata
	var m endpointmeta.Snapshot
	changed := true
	switch a.inputs.operation {
	case pairRecordMigrate:
		if a.inputs.peer != "" || a.inputs.republish {
			return directLANState{}, false, endpointmeta.ErrInvalid
		}
		m, err = endpointmeta.MigrateManagedV4(before, now, budget)
	case pairRecordRevoke:
		if a.inputs.peer != a.target.Peer.Key {
			return directLANState{}, false, endpointmeta.ErrIdentity
		}
		m, changed, err = endpointmeta.RevokeManagedPairV4(before, a.target, now, budget)
	default:
		return directLANState{}, false, endpointmeta.ErrInvalid
	}
	if err != nil {
		return directLANState{}, false, err
	}
	next := cloneDirectLANState(s.state)
	next.Version, next.Metadata = directLANPairRecordStateVersion, cloneDirectLANMetadata(&m)
	if err := validatePairRecordDelta(s.state, next, a.inputs, changed, now); err != nil {
		return directLANState{}, false, err
	}
	if err := validateDirectLANState(next); err != nil {
		return directLANState{}, false, err
	}
	if (changed || a.inputs.republish) && s.reviewRevision == ^uint64(0) {
		return directLANState{}, false, endpointmeta.ErrCapacity
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return directLANState{}, false, err
	}
	if int64(len(data))+1 > s.currentCapacity().bytes {
		return directLANState{}, false, endpointmeta.ErrCapacity
	}
	return next, changed, nil
}

// Independent exact-delta check: no reducer result may change identity, DTOs,
// selection, evidence, peer revisions, unrelated records or retained authority.
func validatePairRecordDelta(before, next directLANState, in pairRecordInputs, changed bool, now time.Time) error {
	if before.Metadata == nil || next.Metadata == nil || before.Metadata.PendingChange != nil || next.Metadata.PendingChange != nil {
		return endpointmeta.ErrInvalid
	}
	expected := cloneDirectLANState(before)
	if !changed {
		if in.operation != pairRecordRevoke || before.Version != directLANPairRecordStateVersion {
			return endpointmeta.ErrInvalid
		}
		i, err := contextPeer(*before.Metadata, in.peer)
		if err != nil || before.Metadata.Peers[i].PairRevocation == nil {
			return endpointmeta.ErrReview
		}
		if !reflect.DeepEqual(expected, next) {
			return endpointmeta.ErrIdentity
		}
		return nil
	}
	revision, err := strconv.ParseUint(before.Metadata.Revision, 10, 64)
	if err != nil || revision == ^uint64(0) {
		return endpointmeta.ErrCapacity
	}
	expected.Version, expected.Metadata.Version = directLANPairRecordStateVersion, directLANPairRecordStateVersion
	expected.Metadata.Revision = strconv.FormatUint(revision+1, 10)
	expected.Metadata.ObservedAt = now.UTC().Format(time.RFC3339Nano)
	switch in.operation {
	case pairRecordMigrate:
		if before.Version != directLANMetadataStateVersion || in.peer != "" || in.republish {
			return endpointmeta.ErrReview
		}
	case pairRecordRevoke:
		if before.Version != directLANPairRecordStateVersion {
			return endpointmeta.ErrReview
		}
		i, err := contextPeer(*before.Metadata, in.peer)
		if err != nil {
			return err
		}
		r := &expected.Metadata.Peers[i]
		if r.PairContext == nil || r.EndpointState == nil || r.PairRevocation != nil {
			return endpointmeta.ErrReview
		}
		binding, err := r.PairContext.Binding()
		if err != nil {
			return err
		}
		r.PairRevocation = &endpointmeta.PairRevocation{PairBinding: binding, Revision: expected.Metadata.Revision, RevokedAt: expected.Metadata.ObservedAt}
	default:
		return endpointmeta.ErrInvalid
	}
	if !reflect.DeepEqual(expected, next) {
		return endpointmeta.ErrIdentity
	}
	return nil
}

func (c *Core) savePairRecordLocked(ctx context.Context, s *directLANStore, process string, a pairRecordAdmission, now time.Time) (pairRecordSaveResult, error) {
	if err := c.matchPairRecordAdmissionLocked(ctx, s, process, a, now); err != nil {
		return pairRecordSaveResult{}, err
	}
	next, changed, err := s.pairRecordCandidateLocked(a, now)
	if err != nil {
		return pairRecordSaveResult{}, err
	}
	if !changed && !a.inputs.republish {
		return pairRecordSaveResult{}, nil
	}
	// Repeat exact file/owner/time observations immediately before publication.
	// No new observation time is invented after candidate budget validation.
	if err := c.matchPairRecordAdmissionLocked(ctx, s, process, a, now); err != nil {
		return pairRecordSaveResult{}, err
	}
	// This is a ctx-only data guard, not a context owner or receipt-producing save.
	live := &contextSaveLiveness{ctx: pairRecordContext{Context: ctx, core: c.ctx}}
	return s.publishPairRecordLocked(next, changed, live)
}

// Narrow receipt-free result adapter for the existing single publisher. The
// enclosing save has already checked frozen admission and the exact delta.
func (s *directLANStore) publishPairRecordLocked(next directLANState, changed bool, live *contextSaveLiveness) (pairRecordSaveResult, error) {
	err := s.writeContextStateLocked(next, live)
	if live.cancelledBeforeWrite {
		// No writer ran. Publisher review revision/epoch/receipt were invalidated;
		// that invalidation remains, but no new uncertainty latch is manufactured.
		return pairRecordSaveResult{}, err
	}
	result := pairRecordSaveResult{changed: changed && atomicPublished(err), published: atomicPublished(err), durable: err == nil}
	// The sole publisher adopts only published outcomes and latches writer
	// failures. Late cancellation cannot retract a successful durable publication.
	return result, errors.Join(err, live.err())
}
