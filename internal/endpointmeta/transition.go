package endpointmeta

import (
	"bytes"
	"encoding/json"
	"time"
)

// Mutation is a proposed local model change, never a received control message.
type Mutation struct {
	Kind          string         `json:"kind"`
	PairBinding   string         `json:"pair_binding"`
	State         *EndpointState `json:"state,omitempty"`
	LocalEndpoint string         `json:"local_endpoint"`
}

type PendingChange struct {
	TransactionID string   `json:"transaction_id"`
	FencedAt      string   `json:"fenced_at"`
	PairBindings  []string `json:"pair_bindings"`
	BaseRevision  string   `json:"base_revision"`
	BaseDigest    string   `json:"base_digest"`
	ReviewDigest  string   `json:"review_digest"`
	Mutation      Mutation `json:"mutation"`
}

func modelDigest(v any) string {
	b, _ := json.Marshal(v)
	return digest("sobalink directlan model review v1", b)
}

func findRecord(s Snapshot, binding string) (int, error) {
	for i, r := range s.Peers {
		if r.EndpointState != nil && r.EndpointState.PairBinding == binding {
			return i, nil
		}
	}
	return -1, ErrIdentity
}

func recordForKey(s Snapshot, key string) (int, error) {
	for i, r := range s.Peers {
		if r.Peer.Key == key {
			return i, nil
		}
	}
	return -1, ErrIdentity
}

// ProposeReceive creates an inert candidate. Exact approval is a separate local
// input; nil means only existing follow consent can admit a new set. Duplicate
// proofs are classified before current-time freshness and never refresh state.
func ProposeReceive(s Snapshot, remoteKey string, e Envelope, exact *Approval, now time.Time) (Mutation, string, error) {
	if err := s.ValidateAt(now); err != nil {
		return Mutation{}, "", err
	}
	if s.PendingChange != nil {
		return Mutation{}, "", ErrRecovery
	}
	i, err := recordForKey(s, remoteKey)
	if err != nil {
		return Mutation{}, "", err
	}
	r := s.Peers[i]
	if r.PairContext == nil || r.EndpointState == nil || !r.ContextConfirmed {
		return Mutation{}, "", ErrReview
	}
	if err := Verify(e, *r.PairContext, s.LocalPeer.Key); err != nil {
		return Mutation{}, "", err
	}
	state := cloneState(*r.EndpointState)
	h, _ := sequence(state.ReceivedHighwater, true)
	n, _ := sequence(e.Update.Sequence, false)
	if n < h {
		return Mutation{}, "", ErrStale
	}
	if n == h {
		if state.ReceivedProof != nil && *state.ReceivedProof == e {
			return Mutation{}, "already_applied", nil
		}
		return Mutation{}, "", ErrConflict
	}
	if err := Inspect(e, *r.PairContext, s.LocalPeer.Key, now); err != nil {
		return Mutation{}, "", err
	}
	u := e.Update
	if u.Operation == "set" && !s.LocalScope.Contains(u.Endpoint) {
		return Mutation{}, "", ErrPolicy
	}
	var approval *Approval
	if u.Operation == "set" {
		hash, _ := e.Digest()
		if exact != nil {
			a := *exact
			if a.Kind != "exact" || a.ProofDigest != hash || a.Endpoint != u.Endpoint || a.FollowRevision != "" || validity(a.Granted, a.Lifetime, a.Expires) != nil || !approvalWithin(a, u) || !current(a.Granted, a.Lifetime, a.Expires, now) {
				return Mutation{}, "", ErrReview
			}
			approval = &a
		} else if f := state.Follow; f != nil && f.Active && current(f.Granted, f.Lifetime, f.Expires, now) {
			approval = &Approval{Kind: "follow", ProofDigest: hash, Endpoint: u.Endpoint, FollowRevision: f.Revision, Granted: f.Granted, Lifetime: f.Lifetime, Expires: f.Expires}
		} else {
			return Mutation{}, "review_required", nil
		}
	}
	state.ReceivedVersion = 1
	state.ReceivedHighwater = u.Sequence
	state.ReceivedProof = &e
	state.Approval = approval
	state.AuthorityRevision, err = nextCounter(state.AuthorityRevision)
	if err != nil {
		return Mutation{}, "", err
	}
	if u.Operation == "withdraw" {
		state.ReceiveStatus = "withdrawn"
	} else {
		state.ReceiveStatus = "eligible"
		state.LastEndpoint = u.Endpoint
	}
	if err := state.Validate(*r.PairContext, s.LocalPeer.Key); err != nil {
		return Mutation{}, "", err
	}
	return Mutation{Kind: u.Operation, PairBinding: state.PairBinding, State: &state}, "candidate", nil
}

// SavedEligibility is a data-only predicate, not a transport activation gate.
// It cannot account for runtime generations, workers, monotonic process clocks,
// local address ownership, or current application permissions.
func SavedEligibility(s Snapshot, binding string, now time.Time) bool {
	if s.ValidateAt(now) != nil {
		return false
	}
	if s.PendingChange != nil {
		for _, affected := range s.PendingChange.PairBindings {
			if affected == binding {
				return false
			}
		}
	}
	i, err := findRecord(s, binding)
	if err != nil {
		return false
	}
	r := s.Peers[i]
	if !r.ContextConfirmed {
		return false
	}
	e := r.EndpointState
	if e.ReceiveStatus != "eligible" || e.ReceivedProof == nil || e.Approval == nil {
		return false
	}
	u := e.ReceivedProof.Update
	a := e.Approval
	return s.LocalScope.Contains(u.Endpoint) && current(u.Issued, u.Lifetime, u.Expires, now) && current(a.Granted, a.Lifetime, a.Expires, now)
}

// ProposeReapproval is the local-only equal-sequence operation. It cannot select
// an older proof or a withdrawal and does not change the remote issue/deadline.
func ProposeReapproval(s Snapshot, binding string, approval Approval, now time.Time) (Mutation, error) {
	if err := s.ValidateAt(now); err != nil {
		return Mutation{}, err
	}
	if s.PendingChange != nil {
		return Mutation{}, ErrRecovery
	}
	i, err := findRecord(s, binding)
	if err != nil {
		return Mutation{}, err
	}
	r := s.Peers[i]
	state := cloneState(*r.EndpointState)
	if !r.ContextConfirmed || state.ReceivedProof == nil || state.ReceivedProof.Update.Operation != "set" {
		return Mutation{}, ErrReview
	}
	u := state.ReceivedProof.Update
	hash, _ := state.ReceivedProof.Digest()
	if Inspect(*state.ReceivedProof, *r.PairContext, s.LocalPeer.Key, now) != nil || !s.LocalScope.Contains(u.Endpoint) || approval.Kind != "exact" || approval.ProofDigest != hash || approval.Endpoint != u.Endpoint || approval.FollowRevision != "" || validity(approval.Granted, approval.Lifetime, approval.Expires) != nil || !approvalWithin(approval, u) || !current(approval.Granted, approval.Lifetime, approval.Expires, now) {
		return Mutation{}, ErrReview
	}
	state.Approval = &approval
	state.ReceiveStatus = "eligible"
	state.AuthorityRevision, err = nextCounter(state.AuthorityRevision)
	if err != nil {
		return Mutation{}, err
	}
	return Mutation{Kind: "reapprove", PairBinding: binding, State: &state}, nil
}

type ReductionResult struct {
	Changed  bool
	State    string
	Reason   string
	Mutation Mutation
}

func ProposeReduction(s Snapshot, binding, kind string, now time.Time) (ReductionResult, error) {
	if err := s.ValidateAt(now); err != nil {
		return ReductionResult{}, err
	}
	if s.PendingChange != nil {
		return ReductionResult{}, ErrRecovery
	}
	i, err := findRecord(s, binding)
	if err != nil {
		return ReductionResult{}, err
	}
	state := cloneState(*s.Peers[i].EndpointState)
	changed := false
	switch kind {
	case "revoke":
		if state.ReceivedProof == nil {
			return ReductionResult{State: "initial", Reason: "no_managed_endpoint_approval"}, nil
		}
		if state.Approval != nil {
			state.Approval = nil
			state.ReceiveStatus = "locally_revoked"
			changed = true
		}
	case "disable-follow":
		if state.Follow != nil && state.Follow.Active {
			state.Follow.Active = false
			changed = true
		}
		if state.Approval != nil && state.Approval.Kind == "follow" {
			state.Approval = nil
			state.ReceiveStatus = "locally_revoked"
			changed = true
		}
	case "expire":
		if state.Follow != nil && state.Follow.Active && !current(state.Follow.Granted, state.Follow.Lifetime, state.Follow.Expires, now) {
			state.Follow.Active = false
			changed = true
		}
		if state.Approval != nil && !SavedEligibility(s, binding, now) {
			state.Approval = nil
			state.ReceiveStatus = "expired"
			changed = true
		}
	default:
		return ReductionResult{}, ErrInvalid
	}
	result := ReductionResult{Changed: changed, State: state.ReceiveStatus}
	if !changed {
		return result, nil
	}
	state.AuthorityRevision, err = nextCounter(state.AuthorityRevision)
	if err != nil {
		return ReductionResult{}, err
	}
	result.Mutation = Mutation{Kind: kind, PairBinding: binding, State: &state}
	return result, nil
}

// ProposeFollow records an explicitly chosen local lifetime. It does not change
// the current endpoint approval or revive a stopped managed endpoint.
func ProposeFollow(s Snapshot, binding string, follow FollowApproval, now time.Time) (Mutation, error) {
	if err := s.ValidateAt(now); err != nil {
		return Mutation{}, err
	}
	if s.PendingChange != nil {
		return Mutation{}, ErrRecovery
	}
	i, err := findRecord(s, binding)
	if err != nil {
		return Mutation{}, err
	}
	r := s.Peers[i]
	state := cloneState(*r.EndpointState)
	if !r.ContextConfirmed || state.Approval != nil && state.Approval.Kind == "follow" {
		return Mutation{}, ErrReview
	}
	rev, err := nextCounter(state.AuthorityRevision)
	if err != nil {
		return Mutation{}, err
	}
	_, scope, _ := pairSides(*r.PairContext, s.LocalPeer.Key)
	scopeDigest, _ := scope.Digest()
	if follow.Revision != rev || !follow.Active || follow.ScopeDigest != scopeDigest || validity(follow.Granted, follow.Lifetime, follow.Expires) != nil || !current(follow.Granted, follow.Lifetime, follow.Expires, now) {
		return Mutation{}, ErrReview
	}
	state.AuthorityRevision = rev
	state.Follow = &follow
	return Mutation{Kind: "grant-follow", PairBinding: binding, State: &state}, nil
}

func PreviewMutation(s Snapshot, m Mutation) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if s.PendingChange != nil {
		return "", ErrRecovery
	}
	if err := validateMutation(s, m); err != nil {
		return "", err
	}
	return modelDigest(struct {
		Snapshot Snapshot
		Mutation Mutation
	}{s, m}), nil
}

func validateMutation(s Snapshot, m Mutation) error {
	if m.Kind == "local-endpoint" {
		if m.PairBinding != "" || m.State != nil || m.LocalEndpoint == s.LocalPeer.Endpoint || !s.LocalScope.Contains(m.LocalEndpoint) {
			return ErrPolicy
		}
		for _, r := range s.Peers {
			if r.PairContext != nil && (!r.PairContext.HostScope.Contains(m.LocalEndpoint) || !r.PairContext.JoinerScope.Contains(m.LocalEndpoint)) {
				return ErrPolicy
			}
		}
		return nil
	}
	if m.LocalEndpoint != "" || m.State == nil {
		return ErrInvalid
	}
	i, err := findRecord(s, m.PairBinding)
	if err != nil {
		return err
	}
	r := s.Peers[i]
	old := r.EndpointState
	next := m.State
	if err := next.Validate(*r.PairContext, s.LocalPeer.Key); err != nil {
		return err
	}
	if next.PairBinding != old.PairBinding || next.IssuedVersion != old.IssuedVersion || next.IssuedHighwater != old.IssuedHighwater || modelDigest(next.IssuedProof) != modelDigest(old.IssuedProof) {
		return ErrInvalid
	}
	if !s.LocalScope.Contains(next.LastEndpoint) {
		return ErrPolicy
	}
	ar, err := nextCounter(old.AuthorityRevision)
	if err != nil || next.AuthorityRevision != ar {
		return ErrInvalid
	}
	h, _ := sequence(old.ReceivedHighwater, true)
	n, _ := sequence(next.ReceivedHighwater, true)
	if n < h {
		return ErrStale
	}
	if n == h && modelDigest(old.ReceivedProof) != modelDigest(next.ReceivedProof) {
		return ErrConflict
	}
	if n == h && next.LastEndpoint != old.LastEndpoint {
		return ErrInvalid
	}
	if m.Kind != "grant-follow" && m.Kind != "disable-follow" && m.Kind != "expire" && modelDigest(old.Follow) != modelDigest(next.Follow) {
		return ErrInvalid
	}
	switch m.Kind {
	case "set":
		if n <= h || next.ReceiveStatus != "eligible" {
			return ErrInvalid
		}
	case "withdraw":
		if n <= h || next.ReceiveStatus != "withdrawn" || next.LastEndpoint != old.LastEndpoint {
			return ErrInvalid
		}
	case "reapprove":
		if n != h || next.ReceiveStatus != "eligible" || next.Approval.Kind != "exact" {
			return ErrInvalid
		}
	case "revoke":
		if n != h || next.ReceiveStatus != "locally_revoked" || next.Approval != nil {
			return ErrInvalid
		}
	case "disable-follow", "expire":
		if n != h || next.Approval != nil && modelDigest(next.Approval) != modelDigest(old.Approval) {
			return ErrInvalid
		}
		if next.Follow != nil {
			if old.Follow == nil || next.Follow.Active && (!old.Follow.Active || m.Kind == "disable-follow") {
				return ErrInvalid
			}
			a, b := *old.Follow, *next.Follow
			a.Active = b.Active
			if a != b {
				return ErrInvalid
			}
		} else if old.Follow != nil {
			return ErrInvalid
		}
		if old.Approval == nil || next.Approval != nil {
			if next.ReceiveStatus != old.ReceiveStatus {
				return ErrInvalid
			}
		} else if m.Kind == "disable-follow" {
			if old.Approval.Kind != "follow" || next.ReceiveStatus != "locally_revoked" {
				return ErrInvalid
			}
		} else if next.ReceiveStatus != "expired" {
			return ErrInvalid
		}
	case "grant-follow":
		if n != h || next.ReceiveStatus != old.ReceiveStatus || next.Follow == nil || !next.Follow.Active || next.Follow.Revision != ar || modelDigest(next.Approval) != modelDigest(old.Approval) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// Recompute a proposal at the fence time so callers cannot bypass a reducer by
// constructing Mutation directly. Pending validation repeats this check at the
// saved fence time; final reconciliation never renews that original proposal.
func validateMutationAt(s Snapshot, m Mutation, now time.Time) error {
	var want Mutation
	var err error
	switch m.Kind {
	case "local-endpoint":
		return validateMutation(s, m)
	case "set", "withdraw":
		var exact *Approval
		if m.State.Approval != nil && m.State.Approval.Kind == "exact" {
			exact = m.State.Approval
		}
		var outcome string
		want, outcome, err = ProposeReceive(s, m.State.ReceivedProof.Update.Issuer, *m.State.ReceivedProof, exact, now)
		if err == nil && outcome != "candidate" {
			return ErrReview
		}
	case "reapprove":
		want, err = ProposeReapproval(s, m.PairBinding, *m.State.Approval, now)
	case "grant-follow":
		want, err = ProposeFollow(s, m.PairBinding, *m.State.Follow, now)
	case "revoke", "disable-follow", "expire":
		var result ReductionResult
		result, err = ProposeReduction(s, m.PairBinding, m.Kind, now)
		if err == nil && !result.Changed {
			return ErrReview
		}
		want = result.Mutation
	default:
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	if modelDigest(m) != modelDigest(want) {
		return ErrReview
	}
	return nil
}

// Fence returns an in-memory candidate for the first durable save. Its presence
// represents blocked activation, not confirmation that a write succeeded.
func Fence(s Snapshot, m Mutation, reviewDigest, transactionID string, now time.Time, budget int) (Snapshot, error) {
	if s.PendingChange != nil {
		return Snapshot{}, ErrRecovery
	}
	if !fitsFence(s, m, reviewDigest, transactionID, now, budget) {
		return Snapshot{}, ErrCapacity
	}
	if err := s.ValidateAt(now); err != nil {
		return Snapshot{}, err
	}
	review, err := PreviewMutation(s, m)
	if err != nil {
		return Snapshot{}, err
	}
	if reviewDigest != review {
		return Snapshot{}, ErrReview
	}
	if err := validateMutationAt(s, m, now); err != nil {
		return Snapshot{}, err
	}
	if _, err := rawBytes(transactionID, 32); err != nil {
		return Snapshot{}, err
	}
	next := cloneSnapshot(s)
	revision, err := nextCounter(s.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	pairs := []string{}
	if m.Kind == "local-endpoint" {
		for _, r := range s.Peers {
			if r.EndpointState != nil {
				pairs = append(pairs, r.EndpointState.PairBinding)
			}
		}
	} else {
		pairs = append(pairs, m.PairBinding)
	}
	next.Revision = revision
	next.PendingChange = &PendingChange{TransactionID: transactionID, FencedAt: now.UTC().Format(time.RFC3339Nano), PairBindings: pairs, BaseRevision: s.Revision, BaseDigest: modelDigest(s), ReviewDigest: review, Mutation: m}
	next = cloneSnapshot(next)
	if _, err := EncodeSnapshot(next, budget); err != nil {
		return Snapshot{}, err
	}
	return next, nil
}

func (s Snapshot) validatePending() error {
	p := s.PendingChange
	if _, err := rawBytes(p.TransactionID, 32); err != nil {
		return err
	}
	fenced, err := instant(p.FencedAt)
	if err != nil {
		return err
	}
	next, err := nextCounter(p.BaseRevision)
	if err != nil || next != s.Revision {
		return ErrInvalid
	}
	base := s
	base.Revision = p.BaseRevision
	base.PendingChange = nil
	if modelDigest(base) != p.BaseDigest {
		return ErrInvalid
	}
	review, err := PreviewMutation(base, p.Mutation)
	if err != nil || review != p.ReviewDigest {
		return ErrInvalid
	}
	if err := base.ValidateAt(fenced); err != nil {
		return err
	}
	if err := validateMutationAt(base, p.Mutation, fenced); err != nil {
		return err
	}
	expected := []string{}
	if p.Mutation.Kind == "local-endpoint" {
		for _, r := range base.Peers {
			if r.EndpointState != nil {
				expected = append(expected, r.EndpointState.PairBinding)
			}
		}
	} else {
		expected = append(expected, p.Mutation.PairBinding)
	}
	a, _ := json.Marshal(expected)
	b, _ := json.Marshal(p.PairBindings)
	if !bytes.Equal(a, b) {
		return ErrInvalid
	}
	return nil
}

// FinishPending models explicit reconciliation of a confirmed saved fence.
// Cancellation is only valid for a new set and retains its received evidence.
// This returns no runtime activation result and executes no worker barrier.
func FinishPending(s Snapshot, cancel bool, now time.Time, budget int) (Snapshot, error) {
	if s.PendingChange == nil {
		return Snapshot{}, ErrReview
	}
	m := s.PendingChange.Mutation
	if cancel && m.Kind != "set" {
		return Snapshot{}, ErrReview
	}
	if m.Kind != "local-endpoint" {
		if _, err := findRecord(s, m.PairBinding); err != nil {
			return Snapshot{}, err
		}
		if m.State == nil {
			return Snapshot{}, ErrInvalid
		}
	}
	if !fitsFinish(s, cancel, now, budget) {
		return Snapshot{}, ErrCapacity
	}
	if err := s.ValidateAt(now); err != nil {
		return Snapshot{}, err
	}
	next := cloneSnapshot(s)
	next.PendingChange = nil
	revision, err := nextCounter(s.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	next.Revision = revision
	next.ObservedAt = now.UTC().Format(time.RFC3339Nano)
	if m.Kind == "local-endpoint" {
		next.PreviousLocalEndpoint = next.LocalPeer.Endpoint
		next.LocalPeer.Endpoint = m.LocalEndpoint
		for i := range next.Peers {
			next.Peers[i].UpgradePending = nil
		}
	} else {
		i, _ := findRecord(next, m.PairBinding)
		r := &next.Peers[i]
		state := cloneState(*m.State)
		if cancel {
			state.Approval = nil
			state.ReceiveStatus = "cancelled"
		}
		if state.Approval != nil {
			u := state.ReceivedProof.Update
			a := state.Approval
			if !current(u.Issued, u.Lifetime, u.Expires, now) || !current(a.Granted, a.Lifetime, a.Expires, now) {
				state.Approval = nil
				state.ReceiveStatus = "expired"
			}
		}
		if state.Follow != nil && state.Follow.Active && !current(state.Follow.Granted, state.Follow.Lifetime, state.Follow.Expires, now) {
			state.Follow.Active = false
		}
		r.EndpointState = &state
		r.Peer.Endpoint = state.LastEndpoint
		r.Revision = revision
		r.UpgradePending = nil
	}
	if _, err := EncodeSnapshot(next, budget); err != nil {
		return Snapshot{}, err
	}
	return next, nil
}

// SaveResolution records the AtomicWrite distinction without implementing a
// second persistence owner. A published-but-uncertain result adopts the candidate
// in memory and tells the caller to require recovery. Snapshot reducers do not
// perform persistence. A failed initial publication cannot claim a fence is on
// disk. Only Durable permits ExportModel to return newly issued bytes.
type SaveResolution struct {
	Snapshot  Snapshot
	Recovery  bool
	Published bool
	Durable   bool
}

func ResolveSave(before, candidate Snapshot, published bool, saveError error) SaveResolution {
	if saveError == nil {
		return SaveResolution{Snapshot: cloneSnapshot(candidate), Published: true, Durable: true}
	}
	s := before
	if published {
		s = candidate
	}
	return SaveResolution{Snapshot: cloneSnapshot(s), Recovery: true, Published: published}
}
