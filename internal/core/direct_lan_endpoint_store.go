package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"math"
	"reflect"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// This bounded process projection contains no writable endpoint authority.
// Proof bounds survive local reapproval; unchanged exact approvals and follow
// revisions keep their first monotonic cutoff. Only a new explicit local grant
// can create a new local bound. None of these time.Time values is serialized.
type directLANEndpointDeadlineKey struct{ binding, kind, revision string }
type directLANEndpointDeadline struct {
	abs       string
	monotonic time.Time
	expired   bool
}

func endpointDeadlineKeys(e *endpointmeta.EndpointState) map[directLANEndpointDeadlineKey]string {
	keys := make(map[directLANEndpointDeadlineKey]string, 3)
	if e == nil {
		return keys
	}
	if p := e.ReceivedProof; p != nil && p.Update.Operation == "set" && p.Update.Lifetime == "finite" {
		digest, _ := p.Digest()
		keys[directLANEndpointDeadlineKey{e.PairBinding, "proof", digest}] = p.Update.Expires
	}
	if a := e.Approval; a != nil && a.Kind == "exact" && a.Lifetime == "finite" {
		keys[directLANEndpointDeadlineKey{e.PairBinding, "approval", privateRevision(*a)}] = a.Expires
	}
	if f := e.Follow; f != nil && f.Lifetime == "finite" {
		keys[directLANEndpointDeadlineKey{e.PairBinding, "follow", f.Revision}] = f.Expires
	}
	return keys
}

// Keep at most three deadlines per current peer state plus three for the one
// pending candidate. Invalid/uncommitted imported previews cannot grow this map.
func (s *directLANStore) observeEndpointDeadlinesLocked(m endpointmeta.Snapshot, now time.Time) {
	next := make(map[directLANEndpointDeadlineKey]directLANEndpointDeadline)
	observe := func(e *endpointmeta.EndpointState) {
		for key, abs := range endpointDeadlineKeys(e) {
			entry, ok := s.endpointDeadlines[key]
			deadline, _ := time.Parse(time.RFC3339Nano, abs) // snapshot was validated
			if !ok || entry.abs != abs {
				entry = directLANEndpointDeadline{abs: abs, monotonic: now.Add(deadline.Sub(now.UTC()))}
			}
			entry.expired = entry.expired || !now.UTC().Before(deadline) || !now.Before(entry.monotonic)
			next[key] = entry
		}
	}
	for _, r := range m.Peers {
		observe(r.EndpointState)
	}
	if m.PendingChange != nil {
		observe(m.PendingChange.Mutation.State)
	}
	s.endpointDeadlines = next
}

// The pure reducers use UTC timestamps only. If a monotonic bound expired
// while UTC still says it is current, do not forge a future fence time or save
// eligible authority. Keep this owner blocked until clock/state is reviewed.
// Clearing an approval or disabling follow removes that authority from checks;
// a fresh exact reapproval still cannot reset its unchanged remote proof bound.
func (s *directLANStore) endpointDeadlineErrorLocked(e *endpointmeta.EndpointState, kind string, now time.Time) error {
	if e == nil {
		return nil
	}
	// Reductions never depend on retained independent follow consent. They
	// must save withdrawal/revocation/cancellation evidence even when that
	// consent is blocked. Check only authority this operation grants or uses.
	if kind == "withdraw" || kind == "revoke" || kind == "disable-follow" || kind == "expire" {
		return nil
	}
	for key, abs := range endpointDeadlineKeys(e) {
		if key.kind == "proof" && e.Approval == nil || key.kind == "follow" && !e.Follow.Active {
			continue
		}
		if kind == "grant-follow" && key.kind != "follow" {
			continue
		}
		if key.kind == "follow" && kind != "status" && kind != "grant-follow" && (e.Approval == nil || e.Approval.Kind != "follow") {
			continue
		}
		entry, ok := s.endpointDeadlines[key]
		if !ok {
			continue
		}
		deadline, _ := time.Parse(time.RFC3339Nano, abs)
		if entry.expired || !now.Before(entry.monotonic) {
			if now.UTC().Before(deadline) {
				return &lanCommandError{"direct_lan_endpoint_clock_review_required", "a saved endpoint authority reached its process deadline while the wall clock still reports it valid; review the clock and saved authority before continuing"}
			}
		}
	}
	return nil
}

// All methods in this file require Core.op, the exclusive profile lifecycle,
// and store.mu. They never enter a Node, create a second file, or activate a
// transport. The stopped predicate is checked by the sole command entrypoint.
func (s *directLANStore) endpointFileCurrentLocked() error {
	current, digest, err := readDirectLANFile(s.path, s.currentCapacity().bytes)
	if err != nil || digest != s.fileDigest || !reflect.DeepEqual(current, s.state) {
		s.recovery = true
		return directLANEndpointReviewChanged()
	}
	return nil
}

func (s *directLANStore) observeEndpointTimeLocked(now time.Time) error {
	// UTC strips time.Time's process monotonic reading: compare actual wall
	// observations so a backward clock change cannot hide behind monotonic time.
	wall := now.UTC()
	if now.IsZero() || !s.endpointObservedAt.IsZero() && wall.Before(s.endpointObservedAt) {
		s.recovery = true
		return directlan.ErrRecovery
	}
	s.endpointObservedAt = wall
	return nil
}

func (s *directLANStore) endpointModelLocked(now time.Time, pending bool) (*endpointmeta.Snapshot, error) {
	if !directLANActiveMetadataSchema(s.state) {
		return nil, directLANEndpointContextRequired()
	}
	if err := s.endpointFileCurrentLocked(); err != nil {
		return nil, err
	}
	if err := s.observeEndpointTimeLocked(now); err != nil {
		return nil, err
	}
	m := s.state.Metadata
	if err := m.ValidateAt(now); err != nil {
		s.recovery = true
		return nil, directlan.ErrRecovery
	}
	s.observeEndpointDeadlinesLocked(*m, now)
	if pending {
		// This is not a general recovery override. Only a valid saved fence is
		// reviewable here; uncertain final writes without a fence stay blocked.
		if m.PendingChange == nil {
			return nil, directlan.ErrRecovery
		}
	} else if s.recovery || m.PendingChange != nil {
		return nil, directlan.ErrRecovery
	}
	return cloneDirectLANMetadata(m), nil
}

func (s *directLANStore) endpointBudgetLocked() (int, error) {
	budget := s.currentCapacity().bytes
	if budget <= 0 {
		return 0, endpointmeta.ErrCapacity
	}
	return int(min(budget, int64(math.MaxInt))), nil
}

// Derive the legacy public DTO and selected endpoint from the one accepted
// metadata snapshot. Context/proof/high-water records are never reconstructed
// from DTOs; identity, allowed prefixes and application grants are untouched.
func (s *directLANStore) stateWithEndpointMetadataLocked(m endpointmeta.Snapshot) (directLANState, error) {
	before := s.state
	if !directLANActiveMetadataSchema(before) || m.Version != before.Version ||
		m.LocalPeer.Key != before.Metadata.LocalPeer.Key || m.LocalPeer.TunnelKey != before.Metadata.LocalPeer.TunnelKey ||
		!reflect.DeepEqual(m.LocalScope, before.Metadata.LocalScope) || len(m.Peers) != len(before.Metadata.Peers) {
		return directLANState{}, endpointmeta.ErrIdentity
	}
	if err := validateEndpointProjection(*before.Metadata, m); err != nil {
		return directLANState{}, err
	}
	next := cloneDirectLANState(before)
	next.Selection.Listen = m.LocalPeer.Endpoint
	next.Peers = make([]directlan.Peer, 0, len(m.Peers))
	for i, r := range m.Peers {
		old := before.Metadata.Peers[i]
		if !reflect.DeepEqual(r.PairRevocation, old.PairRevocation) || old.PairRevocation != nil && !reflect.DeepEqual(r, old) {
			return directLANState{}, endpointmeta.ErrIdentity
		}
		// Endpoint transactions do not establish/confirm contexts, remove
		// pairs, change tunnel identity or migrate application authority.
		if r.Peer.Key != old.Peer.Key || r.Peer.TunnelKey != old.Peer.TunnelKey || r.Peer.Name != old.Peer.Name ||
			r.ContextConfirmed != old.ContextConfirmed || !reflect.DeepEqual(r.PairContext, old.PairContext) {
			return directLANState{}, endpointmeta.ErrIdentity
		}
		peer, err := directLANPeerDTO(r.Peer)
		if err != nil {
			return directLANState{}, err
		}
		next.Peers = append(next.Peers, peer)
	}
	next.Metadata = cloneDirectLANMetadata(&m)
	if err := validateDirectLANState(next); err != nil {
		return directLANState{}, err
	}
	return next, nil
}

// The compact model budget is not the production file budget. Check the whole
// indented Core representation, including identity/selection and its newline.
func (s *directLANStore) preflightEndpointSnapshotLocked(m endpointmeta.Snapshot, final bool) error {
	return s.preflightEndpointSnapshotFromLocked(*s.state.Metadata, m, final)
}

func (s *directLANStore) preflightEndpointSnapshotFromLocked(before, m endpointmeta.Snapshot, final bool) error {
	// A final preflight may precede publication of its already-validated fence.
	// Use an inert value copy; never substitute prospective state in the owner.
	preview := &directLANStore{state: cloneDirectLANState(s.state)}
	preview.state.Metadata = cloneDirectLANMetadata(&before)
	next, err := preview.stateWithEndpointMetadataLocked(m)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	size := int64(len(data)) + 1
	if final {
		// FinishPending observes time again after the fence write. Fractional
		// timestamp width may grow. Expiry removes an approval, but changing
		// a retained follow flag from true to false adds one encoded byte.
		size += int64(max(0, len("2000-01-01T00:00:00.999999999Z")-len(m.ObservedAt)))
		for _, r := range m.Peers {
			if r.Revision == m.Revision && r.EndpointState != nil && r.EndpointState.Follow != nil && r.EndpointState.Follow.Active {
				size++
			}
		}
	}
	if size > s.currentCapacity().bytes {
		return endpointmeta.ErrCapacity
	}
	return nil
}

func (s *directLANStore) resolveEndpointWriteLocked(before, next endpointmeta.Snapshot, err error) endpointmeta.SaveResolution {
	result := endpointmeta.ResolveSave(before, next, atomicPublished(err), err)
	// The publisher already adopted the complete matching Core state if and
	// only if published. ResolveSave owns the metadata outcome classification.
	s.state.Metadata = cloneDirectLANMetadata(&result.Snapshot)
	if result.Recovery {
		s.recovery = true
	}
	return result
}

func (s *directLANStore) saveEndpointSnapshotLocked(next endpointmeta.Snapshot) (endpointmeta.SaveResolution, error) {
	before := *cloneDirectLANMetadata(s.state.Metadata)
	state, err := s.stateWithEndpointMetadataLocked(next)
	if err == nil {
		err = s.writeStateLocked(state)
	}
	return s.resolveEndpointWriteLocked(before, next, err), err
}

func (s *directLANStore) applyEndpointMutationLocked(ctx context.Context, m endpointmeta.Mutation, now time.Time) (endpointmeta.SaveResolution, string, error) {
	before := *cloneDirectLANMetadata(s.state.Metadata)
	budget, err := s.endpointBudgetLocked()
	if err != nil {
		return endpointmeta.SaveResolution{}, "", err
	}
	if s.recovery || before.PendingChange != nil {
		return endpointmeta.SaveResolution{}, "", directlan.ErrRecovery
	}
	// Two writes must fit the process-local review counter as well as the
	// reducer's persisted revision counters. Neither counter may wrap.
	if s.reviewRevision > ^uint64(0)-2 {
		return endpointmeta.SaveResolution{}, "", endpointmeta.ErrCapacity
	}
	review, err := endpointmeta.PreviewMutation(before, m)
	if err != nil {
		return endpointmeta.SaveResolution{}, "", err
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return endpointmeta.SaveResolution{}, "", err
	}
	id := base64.RawURLEncoding.EncodeToString(nonce[:])
	fence, err := endpointmeta.Fence(before, m, review, id, now, budget)
	if err != nil {
		return endpointmeta.SaveResolution{}, "", err
	}
	final, err := endpointmeta.FinishPending(fence, false, now, budget)
	if err != nil {
		return endpointmeta.SaveResolution{}, "", err
	}
	if err := s.preflightEndpointSnapshotLocked(fence, false); err != nil {
		return endpointmeta.SaveResolution{}, "", err
	}
	if err := s.preflightEndpointSnapshotFromLocked(fence, final, true); err != nil {
		return endpointmeta.SaveResolution{}, "", err
	}
	if err := ctx.Err(); err != nil {
		return endpointmeta.SaveResolution{}, "", err
	}
	// Stage the bounded pending deadlines before fallible persistence so its
	// write time cannot restart a finite offer/approval clock.
	s.observeEndpointDeadlinesLocked(fence, now)
	if err := s.endpointDeadlineErrorLocked(m.State, m.Kind, now); err != nil {
		s.observeEndpointDeadlinesLocked(before, now)
		return endpointmeta.SaveResolution{}, "", err
	}
	saved, err := s.saveEndpointSnapshotLocked(fence)
	if err != nil || !saved.Durable {
		s.observeEndpointDeadlinesLocked(*s.state.Metadata, time.Now())
		return saved, id, err
	}
	// Once a fence is durable, cancellation leaves explicit recovery evidence;
	// it never rolls back the previous file or acknowledges final acceptance.
	if err := ctx.Err(); err != nil {
		s.recovery = true
		return saved, id, err
	}
	finishTime := time.Now()
	err = s.observeEndpointTimeLocked(finishTime)
	if err == nil {
		final, err = endpointmeta.FinishPending(saved.Snapshot, false, finishTime, budget)
	}
	if err == nil {
		err = s.preflightEndpointSnapshotLocked(final, false)
	}
	if err == nil && m.State != nil {
		for _, r := range final.Peers {
			if r.EndpointState != nil && r.EndpointState.PairBinding == m.PairBinding {
				err = s.endpointDeadlineErrorLocked(r.EndpointState, m.Kind, finishTime)
				break
			}
		}
	}
	if err != nil {
		s.recovery = true
		return saved, id, err
	}
	saved, err = s.saveEndpointSnapshotLocked(final)
	s.observeEndpointDeadlinesLocked(*s.state.Metadata, time.Now())
	return saved, id, err
}

// The caller has just regenerated and matched the process/file/store/transaction
// review under the same locks. This sole exceptional publisher can finish only
// that valid persisted fence; it cannot accept an arbitrary state or clear an
// uncertain fence-less final write. No generic ignore-recovery switch exists.
func (s *directLANStore) reconcileEndpointPendingLocked(ctx context.Context, expectedTransaction string, cancel bool, now time.Time) (endpointmeta.SaveResolution, error) {
	m, err := s.endpointModelLocked(now, true)
	if err != nil {
		return endpointmeta.SaveResolution{}, err
	}
	if expectedTransaction == "" || m.PendingChange.TransactionID != expectedTransaction {
		return endpointmeta.SaveResolution{}, directLANEndpointReviewChanged()
	}
	budget, err := s.endpointBudgetLocked()
	if err != nil {
		return endpointmeta.SaveResolution{}, err
	}
	next, err := endpointmeta.FinishPending(*m, cancel, now, budget)
	if err != nil {
		return endpointmeta.SaveResolution{}, err
	}
	for _, r := range next.Peers {
		if r.EndpointState != nil && r.EndpointState.PairBinding == m.PendingChange.Mutation.PairBinding {
			if err := s.endpointDeadlineErrorLocked(r.EndpointState, m.PendingChange.Mutation.Kind, now); err != nil {
				return endpointmeta.SaveResolution{}, err
			}
		}
	}
	if err := s.preflightEndpointSnapshotLocked(next, false); err != nil {
		return endpointmeta.SaveResolution{}, err
	}
	state, err := s.stateWithEndpointMetadataLocked(next)
	if err != nil {
		return endpointmeta.SaveResolution{}, err
	}
	if err := ctx.Err(); err != nil {
		return endpointmeta.SaveResolution{}, err
	}
	err = s.publishStateLocked(state)
	result := s.resolveEndpointWriteLocked(*m, next, err)
	if result.Durable {
		s.recovery = false
	}
	s.observeEndpointDeadlinesLocked(*s.state.Metadata, time.Now())
	return result, err
}
