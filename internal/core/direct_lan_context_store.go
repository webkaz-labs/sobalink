package core

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// All helpers require Core.op, exclusive profile ownership and store.mu. There
// is no network wait, second persistence owner, or recovery override here.
// The concrete completion owner supplies terminal signals, never a callback.
// The final publisher reads them synchronously: watcher scheduling cannot make
// a stop that already completed invisible to publication admission.
type contextSaveLiveness struct {
	ctx                  context.Context
	owner                *contextControlOwner
	attempt              *directlan.ContextAttempt
	cancelledBeforeWrite bool
}

func (l *contextSaveLiveness) err() error {
	if l == nil {
		return nil
	}
	if err := l.ctx.Err(); err != nil {
		return err
	}
	if l.owner != nil {
		if l.owner.stopping.Load() {
			return context.Canceled
		}
		select {
		case <-l.owner.ctx.Done():
			return context.Canceled
		default:
		}
	}
	if l.attempt != nil && l.attempt.Cancelled() {
		return context.Canceled
	}
	return nil
}

type contextAdmission struct {
	store                                       *directLANStore
	process, path, file, state, proposal        string
	snapshotRevision, peerRevision, inputDigest string
	writeRevision                               uint64
	budget                                      int64
	inputs                                      contextInputs
}

type contextPreparationWindow struct {
	pair, proposal, nonce, absolute string
	cutoff                          time.Time
}

type contextPublicationReceipt struct {
	store                      *directLANStore
	process, path, file, state string
	writeRevision              uint64
}

// This describes only a local metadata operation. Observations have durable
// false even if an earlier operation saved the same phase. The scoped exchange
// owner must independently obtain a current receipt-backed response slot.
type contextSaveResult struct {
	changed, published, durable bool
	phase                       string
}

func contextProposalDigest(m endpointmeta.Snapshot, r endpointmeta.PeerRecord) string {
	return privateRevision(struct {
		Local endpointmeta.PeerWire
		Scope endpointmeta.Scope
		Peer  endpointmeta.PeerRecord
	}{m.LocalPeer, m.LocalScope, r})
}

func contextPairDigest(m endpointmeta.Snapshot, r endpointmeta.PeerRecord) string {
	return privateRevision(struct {
		Local, Remote endpointmeta.PeerWire
		Scope         endpointmeta.Scope
		Revision      string
	}{m.LocalPeer, r.Peer, m.LocalScope, r.Revision})
}

func contextWindowDigest(pair, nonce, deadline string) string {
	return privateRevision(struct{ Pair, Nonce, Deadline string }{pair, nonce, deadline})
}

func (s *directLANStore) captureContextAdmissionLocked(process string, input contextInputs, now time.Time) (contextAdmission, error) {
	if process == "" {
		return contextAdmission{}, endpointmeta.ErrReview
	}
	m, err := s.endpointModelLocked(now, false)
	if err != nil {
		return contextAdmission{}, err
	}
	i, err := contextPeer(*m, input.PeerKey)
	if err != nil {
		return contextAdmission{}, err
	}
	s.pruneContextWindowsLocked(*m)
	if input.Operation == contextPrepare && input.OwnNonce == "" && input.PeerRevision == "" {
		// Only the exact local preparation choices may ask the owner to mint
		// a nonce. Other fields are rejected before replacement of the input.
		if !reflect.DeepEqual(input, contextInputs{Operation: contextPrepare, PeerKey: input.PeerKey, Deadline: input.Deadline}) {
			return contextAdmission{}, endpointmeta.ErrInvalid
		}
		input, err = s.contextPreparationInputsLocked(*m, input.PeerKey, input.Deadline)
		if err != nil {
			return contextAdmission{}, err
		}
	}
	in, err := input.frozen()
	if err != nil {
		return contextAdmission{}, err
	}
	switch in.Operation {
	case contextPrepare, contextResume, contextConfirmUpdate, contextRepublish, contextRepublishPrepared:
		if _, _, err := s.stateWithContextMetadataLocked(in, contextTranscript{}, now); err != nil {
			return contextAdmission{}, err
		}
	default:
		// Remote request/reply data does not exist yet. Bind the complete
		// current local proposal now; only completion may invoke its reducer.
		if m.Peers[i].UpgradePending == nil && m.Peers[i].PairContext == nil {
			return contextAdmission{}, endpointmeta.ErrReview
		}
	}
	a := contextAdmission{store: s, process: process, path: s.path, file: s.fileDigest, state: privateRevision(s.state),
		proposal: contextProposalDigest(*m, m.Peers[i]), snapshotRevision: m.Revision, peerRevision: m.Peers[i].Revision,
		inputDigest: privateRevision(in), writeRevision: s.reviewRevision, budget: s.currentCapacity().bytes, inputs: in}
	return a, nil
}

func (s *directLANStore) matchContextAdmissionLocked(process string, a contextAdmission, now time.Time) error {
	if process == "" || a.store != s || a.process != process || a.path != s.path || a.file != s.fileDigest ||
		a.writeRevision != s.reviewRevision || a.budget != s.currentCapacity().bytes || a.state != privateRevision(s.state) || a.inputDigest != privateRevision(a.inputs) {
		return endpointmeta.ErrReview
	}
	m, err := s.endpointModelLocked(now, false)
	if err != nil {
		return err
	}
	i, err := contextPeer(*m, a.inputs.PeerKey)
	if err != nil {
		return err
	}
	if a.snapshotRevision != m.Revision || a.peerRevision != m.Peers[i].Revision || a.proposal != contextProposalDigest(*m, m.Peers[i]) {
		return endpointmeta.ErrReview
	}
	return nil
}

// Bound both provisional and saved slots together by existing eligible peers.
// Pending absence is not a pruning reason: the first save may have failed.
func (s *directLANStore) pruneContextWindowsLocked(m endpointmeta.Snapshot) {
	for key, w := range s.contextWindows {
		i, err := contextPeer(m, key)
		if err != nil || m.Peers[i].ContextConfirmed || contextPairDigest(m, m.Peers[i]) != w.pair {
			delete(s.contextWindows, key)
		}
	}
}

func (s *directLANStore) contextWindowLocked(m endpointmeta.Snapshot, key string) endpointmeta.PreparationWindow {
	i, err := contextPeer(m, key)
	if err != nil || m.Peers[i].UpgradePending == nil {
		return endpointmeta.PreparationWindow{}
	}
	p := m.Peers[i].UpgradePending
	w, ok := s.contextWindows[key]
	pair := contextPairDigest(m, m.Peers[i])
	if !ok || w.pair != pair || w.proposal != contextWindowDigest(pair, p.OwnNonce, p.PrepareDeadline) {
		// In particular, loading a saved deadline never reconstructs a window.
		return endpointmeta.PreparationWindow{}
	}
	return endpointmeta.PreparationWindow{PeerKey: key, OwnNonce: w.nonce, Deadline: w.cutoff}
}

func (s *directLANStore) reserveContextWindowLocked(in contextInputs, next endpointmeta.Snapshot, now time.Time) error {
	if s.state.Metadata == nil || in.Operation != contextPrepare && in.Operation != contextResume {
		return endpointmeta.ErrReview
	}
	saved, err := contextPeer(*s.state.Metadata, in.PeerKey)
	if err != nil || s.state.Metadata.Peers[saved].ContextConfirmed {
		return endpointmeta.ErrReview
	}
	i, err := contextPeer(next, in.PeerKey)
	if err != nil || next.Peers[i].UpgradePending == nil || next.Peers[i].ContextConfirmed {
		return endpointmeta.ErrReview
	}
	r, p := next.Peers[i], next.Peers[i].UpgradePending
	pair := contextPairDigest(next, r)
	if pair != contextPairDigest(*s.state.Metadata, s.state.Metadata.Peers[saved]) {
		return endpointmeta.ErrIdentity
	}
	proposal := contextWindowDigest(pair, p.OwnNonce, p.PrepareDeadline)
	if w, exists := s.contextWindows[in.PeerKey]; exists {
		if w.pair != pair || w.nonce != p.OwnNonce {
			return endpointmeta.ErrReview
		}
		if w.proposal == proposal {
			if !now.Before(w.cutoff) {
				return endpointmeta.ErrExpired
			}
			return nil // preserve the original monotonic cutoff on exact retry
		}
		if in.Operation != contextResume {
			return endpointmeta.ErrReview
		}
	}
	until, err := time.Parse(time.RFC3339Nano, p.PrepareDeadline)
	if err != nil || !now.Before(until) {
		return endpointmeta.ErrExpired
	}
	cutoff := now.Add(until.Sub(now.UTC()))
	if cutoff.UTC().Format(time.RFC3339Nano) != p.PrepareDeadline {
		return endpointmeta.ErrInvalid // no saturated duration or changed deadline
	}
	if s.contextWindows == nil {
		s.contextWindows = make(map[string]contextPreparationWindow)
	}
	s.contextWindows[in.PeerKey] = contextPreparationWindow{pair: pair, proposal: proposal, nonce: p.OwnNonce, absolute: p.PrepareDeadline, cutoff: cutoff}
	return nil
}

// Invoke the selected reducer here rather than accepting caller-made snapshots.
// Then independently constrain its projection to that operation's field set.
func (s *directLANStore) stateWithContextMetadataLocked(in contextInputs, transcript contextTranscript, now time.Time) (directLANState, endpointmeta.ContextTransition, error) {
	before := s.state.Metadata
	if s.state.Version != directLANMetadataStateVersion || before == nil {
		return directLANState{}, endpointmeta.ContextTransition{}, directLANEndpointContextRequired()
	}
	budget, err := s.endpointBudgetLocked()
	if err != nil {
		return directLANState{}, endpointmeta.ContextTransition{}, err
	}
	transcript, err = transcript.frozen(in)
	if err != nil {
		return directLANState{}, endpointmeta.ContextTransition{}, err
	}
	transition, err := s.reduceContextLocked(in, transcript, *before, now, budget)
	if err != nil {
		return directLANState{}, transition, err
	}
	if err := validateContextProjection(*before, transition, in, now); err != nil {
		return directLANState{}, transition, err
	}
	if (transition.Changed || in.Operation == contextRepublish || in.Operation == contextRepublishPrepared) && s.reviewRevision == ^uint64(0) {
		return directLANState{}, transition, endpointmeta.ErrCapacity
	}
	next := cloneDirectLANState(s.state)
	next.Metadata = cloneDirectLANMetadata(&transition.Snapshot)
	if err := validateDirectLANState(next); err != nil {
		return directLANState{}, transition, err
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return directLANState{}, transition, err
	}
	if int64(len(data))+1 > s.currentCapacity().bytes {
		return directLANState{}, transition, endpointmeta.ErrCapacity
	}
	return next, transition, nil
}

func validateContextProjection(before endpointmeta.Snapshot, transition endpointmeta.ContextTransition, in contextInputs, now time.Time) error {
	next := transition.Snapshot
	if !transition.Changed {
		if !reflect.DeepEqual(before, next) {
			return endpointmeta.ErrIdentity
		}
		return nil
	}
	i, err := contextPeer(before, in.PeerKey)
	if err != nil || len(before.Peers) != len(next.Peers) {
		return endpointmeta.ErrIdentity
	}
	revision, err := strconv.ParseUint(before.Revision, 10, 64)
	if err != nil || revision == ^uint64(0) || next.Revision != strconv.FormatUint(revision+1, 10) || next.ObservedAt != now.UTC().Format(time.RFC3339Nano) {
		return endpointmeta.ErrCapacity
	}
	old, got := before.Peers[i], next.Peers[i]
	want := old
	switch in.Operation {
	case contextPrepare:
		if old.UpgradePending != nil || old.PairContext != nil || old.EndpointState != nil || old.ContextConfirmed {
			return endpointmeta.ErrReview
		}
		want.UpgradePending = &endpointmeta.UpgradePending{ReviewedPeer: old.Peer, PeerRevision: old.Revision, OwnNonce: in.OwnNonce, PrepareDeadline: in.Deadline}
	case contextResume, contextRecordInbound, contextRecordOutbound:
		if old.UpgradePending == nil || got.UpgradePending == nil {
			return endpointmeta.ErrReview
		}
		pending := *old.UpgradePending
		if in.Operation == contextResume {
			pending.PrepareDeadline = in.Resume.NewDeadline
		} else {
			if pending.Context != nil {
				return endpointmeta.ErrIdentity
			}
			pending.Context = got.UpgradePending.Context
		}
		want.UpgradePending = &pending
	case contextCommit:
		if old.PairContext != nil || old.EndpointState != nil || old.UpgradePending == nil || old.UpgradePending.Context == nil {
			return endpointmeta.ErrReview
		}
		initial, err := endpointmeta.InitialState(*old.UpgradePending.Context, before.LocalPeer.Key)
		if err != nil {
			return err
		}
		want.PairContext, want.EndpointState = old.UpgradePending.Context, &initial
	case contextConfirmCommit, contextConfirmStatus, contextConfirmUpdate:
		if old.ContextConfirmed || old.PairContext == nil || old.EndpointState == nil {
			return endpointmeta.ErrReview
		}
		want.ContextConfirmed, want.UpgradePending, want.Revision = true, nil, next.Revision
	default:
		return endpointmeta.ErrInvalid
	}
	if !reflect.DeepEqual(want, got) {
		return endpointmeta.ErrIdentity
	}
	// Comparing the complete remainder preserves local identity/scope/endpoint,
	// selection-derived data, fences, history and every unrelated peer record.
	expected := *cloneDirectLANMetadata(&before)
	expected.Revision, expected.ObservedAt, expected.Peers[i] = next.Revision, next.ObservedAt, want
	if !reflect.DeepEqual(expected, next) {
		return endpointmeta.ErrIdentity
	}
	return nil
}

func (s *directLANStore) contextPublicationCurrentLocked(process string) bool {
	r := s.contextPublication
	return r != nil && !s.recovery && r.store == s && process != "" && r.process == process && r.path == s.path &&
		r.file == s.fileDigest && r.state == privateRevision(s.state) && r.writeRevision == s.reviewRevision
}

func (s *directLANStore) contextEpochLocked() *directlan.ContextEpoch {
	if s.contextEpoch == nil || !s.contextEpoch.Valid() {
		s.contextEpoch = directlan.NewContextEpoch()
	}
	return s.contextEpoch
}

// A canonical reply requires both the just-claimed exact request and a current
// process publication receipt. A duplicate can qualify without a new write;
// reopening alone, an inert reducer result, or a receipt alone cannot qualify.
func (s *directLANStore) contextReplyLocked(process string, in contextInputs, transcript contextTranscript, now time.Time) (endpointmeta.Reply, error) {
	if !s.contextPublicationCurrentLocked(process) {
		return nil, endpointmeta.ErrReview
	}
	if _, _, err := s.stateWithContextMetadataLocked(in, transcript, now); err != nil {
		return nil, err
	}
	i, err := contextPeer(*s.state.Metadata, in.PeerKey)
	if err != nil {
		return nil, err
	}
	r := s.state.Metadata.Peers[i]
	p := contextSavedPair(r)
	if p == nil {
		return nil, endpointmeta.ErrReview
	}
	binding, err := p.Binding()
	if err != nil {
		return nil, err
	}
	var reply endpointmeta.Reply
	switch in.Operation {
	case contextRecordInbound:
		// Clone scopes with the complete saved record; do not expose a mutable
		// alias into the private state through an interface-valued reply.
		copy := cloneDirectLANMetadata(s.state.Metadata)
		reply = endpointmeta.PrepareReply{Version: 2, Operation: "pair-context-prepare", OK: true, PairContext: *contextSavedPair(copy.Peers[i]), PairBinding: binding}
	case contextCommit, contextStatusInbound:
		if in.Bound.PairBinding != binding || transcript.Bound != in.Bound {
			return nil, endpointmeta.ErrIdentity
		}
		state := "prepared"
		if r.PairContext != nil {
			state = "committed"
		}
		reply = endpointmeta.ContextReply{Version: 2, Operation: in.Bound.Operation, OK: true, PairBinding: binding, State: state}
	default:
		return nil, endpointmeta.ErrInvalid
	}
	if _, err := endpointmeta.Encode(reply); err != nil {
		return nil, err
	}
	return reply, nil
}

func (s *directLANStore) applyContextTransitionLocked(ctx context.Context, process string, a contextAdmission, transcript contextTranscript, now time.Time) (contextSaveResult, error) {
	return s.saveContextTransitionLocked(ctx, process, a, transcript, now)
}

// This save boundary accepts only an earlier admission with its frozen closed
// inputs. No caller-supplied Snapshot or Core state reaches the publisher.
func (s *directLANStore) saveContextTransitionLocked(ctx context.Context, process string, a contextAdmission, transcript contextTranscript, now time.Time) (contextSaveResult, error) {
	return s.saveContextTransitionWithLivenessLocked(ctx, process, a, transcript, now, &contextSaveLiveness{ctx: ctx})
}

func (s *directLANStore) saveContextTransitionWithLivenessLocked(ctx context.Context, process string, a contextAdmission, transcript contextTranscript, now time.Time, live *contextSaveLiveness) (contextSaveResult, error) {
	if err := live.err(); err != nil {
		return contextSaveResult{}, err
	}
	if err := s.matchContextAdmissionLocked(process, a, now); err != nil {
		return contextSaveResult{}, err
	}
	in := a.inputs
	if (in.Operation == contextConfirmCommit || in.Operation == contextConfirmStatus || in.Operation == contextConfirmUpdate) && !s.contextPublicationCurrentLocked(process) {
		// A reopened file or observational duplicate cannot certify the local
		// committed state. Require an explicit fresh guarded publication first.
		return contextSaveResult{}, endpointmeta.ErrReview
	}
	next, transition, err := s.stateWithContextMetadataLocked(in, transcript, now)
	if err != nil {
		return contextSaveResult{}, err
	}
	if in.Operation == contextResume || in.Operation == contextPrepare && transition.Changed {
		// Reserve before the first fallible save and retain on every outcome.
		// Explicit resume is also required after reopen when its saved UTC
		// deadline is unchanged; such a resume still creates no durability.
		s.pruneContextWindowsLocked(*s.state.Metadata)
		if err := s.reserveContextWindowLocked(in, transition.Snapshot, now); err != nil {
			return contextSaveResult{}, err
		}
	}
	if !transition.Changed && in.Operation != contextRepublish && in.Operation != contextRepublishPrepared {
		return contextSaveResult{phase: transition.Phase}, nil
	}
	if s.reviewRevision == ^uint64(0) {
		return contextSaveResult{}, endpointmeta.ErrCapacity
	}
	if err := live.err(); err != nil {
		return contextSaveResult{}, err
	}
	before := *cloneDirectLANMetadata(s.state.Metadata)
	s.contextPublication = nil
	err = s.writeContextStateLocked(next, live)
	if live.cancelledBeforeWrite {
		// Publication admission was rejected before invoking the writer.
		// The epoch/revision remains invalidated, but no save uncertainty was
		// created and no previous recovery latch is cleared.
		return contextSaveResult{}, err
	}
	resolution := endpointmeta.ResolveSave(before, transition.Snapshot, atomicPublished(err), err)
	// The existing publisher adopts the whole Core file exactly on publication.
	// Retain that adoption on uncertainty; never roll it back or clear recovery.
	if resolution.Recovery {
		s.recovery = true
	}
	s.pruneContextWindowsLocked(*s.state.Metadata)
	if err != nil || !resolution.Durable {
		if err == nil {
			err = directlan.ErrRecovery
		}
		return contextSaveResult{published: resolution.Published}, err
	}
	s.contextPublication = &contextPublicationReceipt{store: s, process: process, path: s.path, file: s.fileDigest,
		state: privateRevision(s.state), writeRevision: s.reviewRevision}
	if err := live.err(); err != nil {
		return contextSaveResult{published: true}, err
	}
	return contextSaveResult{changed: transition.Changed, published: true, durable: true, phase: transition.Phase}, nil
}
