package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// These are private local management inputs, never authenticated wire requests.
// Approval and Follow are explicit local choices, including their original
// granted time and lifetime; inspection/apply never manufacture a new deadline.
// A later CLI/Web review must present those choices before submitting them.
type directLANEndpointInput struct {
	PeerID           string                       `json:"peerId"`
	Action           string                       `json:"action,omitempty"`
	Update           string                       `json:"update,omitempty"`
	Approval         *endpointmeta.Approval       `json:"approval,omitempty"`
	Follow           *endpointmeta.FollowApproval `json:"follow,omitempty"`
	ExpectedRevision string                       `json:"expectedRevision,omitempty"`
	TransactionID    string                       `json:"transactionId,omitempty"`
	Cancel           bool                         `json:"cancel,omitempty"`
}

type directLANEndpointReview struct {
	Revision               string                       `json:"revision"`
	Action                 string                       `json:"action"`
	PeerID                 string                       `json:"peerId"`
	PairBinding            string                       `json:"pairBinding"`
	PreviousEndpoint       string                       `json:"previousEndpoint"`
	Endpoint               string                       `json:"endpoint"`
	Sequence               string                       `json:"sequence"`
	ProofDigest            string                       `json:"proofDigest,omitempty"`
	PeerRevision           string                       `json:"peerRevision"`
	AuthorityRevision      string                       `json:"authorityRevision"`
	Scope                  endpointmeta.Scope           `json:"scope"`
	ScopeDigest            string                       `json:"scopeDigest"`
	SignedPriorEndpoint    string                       `json:"signedPriorEndpoint,omitempty"`
	Issued                 string                       `json:"issued,omitempty"`
	Lifetime               string                       `json:"lifetime,omitempty"`
	Expires                string                       `json:"expires,omitempty"`
	Outcome                string                       `json:"outcome"`
	State                  string                       `json:"state"`
	Reason                 string                       `json:"reason,omitempty"`
	Approval               *endpointmeta.Approval       `json:"approval,omitempty"`
	Follow                 *endpointmeta.FollowApproval `json:"follow,omitempty"`
	EndpointUpdatesEnabled bool                         `json:"endpointUpdatesEnabled"`
}

func directLANEndpointReviewChanged() error {
	return &lanCommandError{"direct_lan_endpoint_review_changed", "the endpoint review or protected file changed; keep networking stopped and inspect the current saved state again"}
}

func directLANEndpointContextRequired() error {
	return &lanCommandError{"direct_lan_endpoint_context_required", "this saved pair has no confirmed matching endpoint context; context establishment is not available through these saved-state operations"}
}

func directLANEndpointError(err error) error {
	if err == nil {
		return nil
	}
	code, message := "direct_lan_endpoint_invalid", "the endpoint input is invalid; inspect its exact peer, proof and local authority before retrying"
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, endpointmeta.ErrRecovery), errors.Is(err, directlan.ErrRecovery):
		code, message = "direct_lan_recovery_required", "saved endpoint state requires explicit offline recovery; keep networking stopped and inspect the current protected state"
	case errors.Is(err, endpointmeta.ErrCapacity), errors.Is(err, directlan.ErrCapacity):
		code, message = "direct_lan_capacity", "the complete endpoint transaction exceeds a selected storage or revision limit"
	case errors.Is(err, endpointmeta.ErrStale):
		code, message = "direct_lan_endpoint_stale", "the endpoint proof is older than the highest saved proof"
	case errors.Is(err, endpointmeta.ErrConflict):
		code, message = "direct_lan_endpoint_conflict", "the endpoint sequence conflicts with the highest saved proof; preserve the protected state and review the pair"
	case errors.Is(err, endpointmeta.ErrExpired):
		code, message = "direct_lan_endpoint_expired", "the proposed endpoint proof or approval is no longer current; inspect the current saved proof and lifetime"
	case errors.Is(err, endpointmeta.ErrReview):
		code, message = "direct_lan_endpoint_review_required", "review the exact current endpoint proof and explicit local lifetime before applying this saved-state change"
	case errors.Is(err, endpointmeta.ErrIdentity):
		code, message = "direct_lan_endpoint_identity", "the endpoint proof or request does not match this saved pair and its confirmed context"
	case errors.Is(err, endpointmeta.ErrPolicy), errors.Is(err, directlan.ErrPolicy):
		code, message = "direct_lan_policy", "the proposed endpoint is outside the unchanged selected scope"
	default:
		var coded interface{ ErrorCode() string }
		if errors.As(err, &coded) {
			return err
		}
	}
	return privateAtomicError(&lanCommandError{code, message}, err)
}

func decodeDirectLANEndpointInput(raw json.RawMessage) (directLANEndpointInput, error) {
	var input directLANEndpointInput
	if len(raw) > 4*endpointmeta.MaxFrameBytes {
		return input, endpointmeta.ErrCapacity
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		return input, endpointmeta.ErrInvalid
	}
	return input, nil
}

func endpointRecord(m endpointmeta.Snapshot, key string) (*endpointmeta.PeerRecord, error) {
	for i := range m.Peers {
		if m.Peers[i].Peer.Key == key {
			r := &m.Peers[i]
			if r.PairContext == nil || r.EndpointState == nil || !r.ContextConfirmed {
				return nil, directLANEndpointContextRequired()
			}
			return r, nil
		}
	}
	return nil, endpointmeta.ErrIdentity
}

// Returns the existing reducer's candidate, not a second authoritative model.
// Invalid input never changes recovery, high-water, follow consent or authority.
func inspectDirectLANEndpoint(m endpointmeta.Snapshot, input directLANEndpointInput, now time.Time) (directLANEndpointReview, endpointmeta.Mutation, error) {
	r, err := endpointRecord(m, input.PeerID)
	if err != nil {
		return directLANEndpointReview{}, endpointmeta.Mutation{}, err
	}
	state := r.EndpointState
	review := directLANEndpointReview{Action: input.Action, PeerID: r.Peer.Key, PairBinding: state.PairBinding, PreviousEndpoint: r.Peer.Endpoint, Endpoint: r.Peer.Endpoint, Sequence: state.ReceivedHighwater, PeerRevision: r.Revision, AuthorityRevision: state.AuthorityRevision, Scope: m.LocalScope, Approval: state.Approval, Follow: state.Follow}
	review.State = state.ReceiveStatus
	review.ScopeDigest, _ = m.LocalScope.Digest()
	setProof := func(envelope endpointmeta.Envelope) {
		review.Sequence = envelope.Update.Sequence
		review.ProofDigest, _ = envelope.Digest()
		review.SignedPriorEndpoint = envelope.Update.PriorEndpoint
		review.Issued, review.Lifetime, review.Expires = envelope.Update.Issued, envelope.Update.Lifetime, envelope.Update.Expires
	}
	if state.ReceivedProof != nil {
		setProof(*state.ReceivedProof)
	}
	var mutation endpointmeta.Mutation
	if input.Action != "receive" && input.Update != "" || input.Action != "receive" && input.Action != "reapprove" && input.Approval != nil || input.Action != "grant-follow" && input.Follow != nil {
		return review, mutation, endpointmeta.ErrInvalid
	}
	switch input.Action {
	case "receive":
		var envelope endpointmeta.Envelope
		envelope, err = endpointmeta.ParseUpdateText(input.Update)
		if err == nil && envelope.Update.Operation == "withdraw" && input.Approval != nil {
			err = endpointmeta.ErrInvalid
		}
		if err == nil {
			mutation, review.Outcome, err = endpointmeta.ProposeReceive(m, input.PeerID, envelope, input.Approval, now)
		}
		if err == nil {
			setProof(envelope)
			if envelope.Update.Operation == "set" {
				review.Endpoint = envelope.Update.Endpoint
			}
		}
	case "reapprove":
		if input.Approval == nil {
			err = endpointmeta.ErrReview
		} else {
			mutation, err = endpointmeta.ProposeReapproval(m, state.PairBinding, *input.Approval, now)
			review.Outcome = "candidate"
		}
	case "grant-follow":
		if input.Follow == nil {
			err = endpointmeta.ErrReview
		} else {
			mutation, err = endpointmeta.ProposeFollow(m, state.PairBinding, *input.Follow, now)
			review.Outcome = "candidate"
		}
	case "revoke", "disable-follow", "expire":
		var reduction endpointmeta.ReductionResult
		reduction, err = endpointmeta.ProposeReduction(m, state.PairBinding, input.Action, now)
		mutation, review.Reason = reduction.Mutation, reduction.Reason
		review.Outcome = "unchanged"
		if reduction.Changed {
			review.Outcome = "candidate"
		}
	default:
		err = endpointmeta.ErrInvalid
	}
	if err == nil && mutation.State != nil {
		review.Approval, review.Follow = mutation.State.Approval, mutation.State.Follow
		review.State = mutation.State.ReceiveStatus
	}
	return review, mutation, err
}

func (s *directLANStore) endpointReviewRevisionLocked(process string, input directLANEndpointInput, outcome any) string {
	input.ExpectedRevision = ""
	return privateRevision(struct {
		Purpose, Process, FileDigest string
		StoreRevision                uint64
		State                        directLANState
		Input                        directLANEndpointInput
		Outcome                      any
	}{"direct-lan-endpoint-review-v1", process, s.fileDigest, s.reviewRevision, s.state, input, outcome})
}

func endpointSavedResult(saved endpointmeta.SaveResolution, transaction string, changed bool) map[string]any {
	return map[string]any{"saved": saved.Durable, "changed": changed, "transactionId": transaction, "storeRevision": saved.Snapshot.Revision, "outcome": "saved_only", "peers": endpointPeerViews(saved.Snapshot), "endpointUpdatesEnabled": false}
}

// Only presentation metadata leaves the store. Raw proofs, invitation material
// and private identity bytes are excluded; endpoint is last-known saved data,
// never a claim that a route or application listener is active.
func endpointPeerViews(m endpointmeta.Snapshot) []map[string]any {
	peers := make([]map[string]any, 0, len(m.Peers))
	for _, r := range m.Peers {
		view := map[string]any{"peerId": r.Peer.Key, "name": r.Peer.Name, "endpoint": r.Peer.Endpoint, "revision": r.Revision, "contextConfirmed": r.ContextConfirmed, "state": "context_required"}
		if r.PairContext != nil && r.EndpointState != nil {
			e := r.EndpointState
			view["state"], view["pairBinding"] = e.ReceiveStatus, e.PairBinding
			view["receivedHighwater"], view["issuedHighwater"] = e.ReceivedHighwater, e.IssuedHighwater
			view["authorityRevision"] = e.AuthorityRevision
			if e.Approval != nil {
				view["approval"] = *e.Approval
			}
			if e.Follow != nil {
				view["follow"] = *e.Follow
			}
			if !r.ContextConfirmed {
				view["state"] = "context_waiting_for_peer"
			}
			if e.ReceivedProof != nil {
				view["proofDigest"], _ = e.ReceivedProof.Digest()
				view["operation"], view["issued"], view["lifetime"], view["expires"] = e.ReceivedProof.Update.Operation, e.ReceivedProof.Update.Issued, e.ReceivedProof.Update.Lifetime, e.ReceivedProof.Update.Expires
			}
		}
		peers = append(peers, view)
	}
	return peers
}

// executeCommand holds Core.op. This entrypoint deliberately admits only a
// stopped/offline Core and never calls Node retirement or replacement. A future
// live/control entrypoint must phase its ownership and release op before joins.
func (c *Core) directLANEndpointCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	input, err := decodeDirectLANEndpointInput(raw)
	if err != nil {
		return nil, directLANEndpointError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.RLock()
	stopped := c.node == nil && c.attemptedNetwork == "" && !c.closing && c.ctx.Err() == nil
	process, store := c.lanStartNonce, c.directLAN
	c.mu.RUnlock()
	if !stopped {
		return nil, &lanCommandError{"network_restart_required", "stop soba and start with --offline to inspect or change saved endpoint metadata; live endpoint following is not enabled"}
	}
	if store == nil {
		return nil, &lanCommandError{"direct_lan_setup_required", "configure direct LAN before inspecting its saved endpoint metadata"}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := time.Now()
	if name == "direct-lan.endpoint.status" {
		if input != (directLANEndpointInput{}) {
			return nil, directLANEndpointError(endpointmeta.ErrInvalid)
		}
		if err := store.endpointFileCurrentLocked(); err != nil {
			return nil, directLANEndpointError(err)
		}
		_ = store.observeEndpointTimeLocked(now)
		view := map[string]any{"stateVersion": store.state.Version, "endpoint": store.state.Selection.Listen, "recoveryRequired": store.recovery, "endpointUpdatesEnabled": false}
		if m := store.state.Metadata; m != nil {
			if m.ValidateAt(now) != nil {
				store.recovery = true
			}
			store.observeEndpointDeadlinesLocked(*m, now)
			view["recoveryRequired"], view["storeRevision"], view["peers"] = store.recovery, m.Revision, endpointPeerViews(*m)
			clockReview := false
			for _, r := range m.Peers {
				clockReview = clockReview || store.endpointDeadlineErrorLocked(r.EndpointState, "status", now) != nil
			}
			view["authorityClockReviewRequired"] = clockReview
			if p := m.PendingChange; p != nil {
				view["pendingTransactionId"], view["pendingAction"] = p.TransactionID, p.Mutation.Kind
			}
		}
		return view, nil
	}
	if name == "direct-lan.endpoint.recovery.inspect" || name == "direct-lan.endpoint.recovery.apply" {
		result, err := store.endpointRecoveryCommandLocked(ctx, process, name, input, now)
		return result, directLANEndpointError(err)
	}
	if input.TransactionID != "" || input.Cancel {
		return nil, directLANEndpointError(endpointmeta.ErrInvalid)
	}
	m, err := store.endpointModelLocked(now, false)
	if err != nil {
		return nil, directLANEndpointError(err)
	}
	preview := name == "direct-lan.endpoint.inspect" || name == "direct-lan.endpoint.follow.preview"
	want := ""
	switch name {
	case "direct-lan.endpoint.inspect":
		if input.Action == "" {
			input.Action = "receive"
		}
	case "direct-lan.endpoint.accept":
		want = "receive"
	case "direct-lan.endpoint.reapprove-current":
		want = "reapprove"
	case "direct-lan.endpoint.revoke":
		want = "revoke"
	case "direct-lan.endpoint.expire":
		want = "expire"
	case "direct-lan.endpoint.follow.preview", "direct-lan.endpoint.follow.apply":
		if input.Action == "disable-follow" {
			want = "disable-follow"
		} else {
			want = "grant-follow"
		}
	default:
		return nil, directLANEndpointError(endpointmeta.ErrInvalid)
	}
	if want != "" {
		if input.Action != "" && input.Action != want {
			return nil, directLANEndpointError(endpointmeta.ErrInvalid)
		}
		input.Action = want
	}
	review, mutation, err := inspectDirectLANEndpoint(*m, input, now)
	if err != nil {
		return nil, directLANEndpointError(err)
	}
	deadlineState := mutation.State
	deadlineKind := mutation.Kind
	if deadlineState == nil && input.Action == "expire" {
		if r, err := endpointRecord(*m, input.PeerID); err == nil {
			deadlineState = r.EndpointState
			deadlineKind = "status"
		}
	}
	if err := store.endpointDeadlineErrorLocked(deadlineState, deadlineKind, now); err != nil {
		return nil, directLANEndpointError(err)
	}
	review.Revision = store.endpointReviewRevisionLocked(process, input, review)
	if preview {
		return review, nil
	}
	if input.ExpectedRevision == "" || input.ExpectedRevision != review.Revision {
		return nil, directLANEndpointReviewChanged()
	}
	if review.Outcome != "candidate" {
		// Identical proofs and reductions already performed remain observational.
		// In particular, a duplicate cannot revive expired/revoked authority.
		return map[string]any{"saved": false, "changed": false, "outcome": review.Outcome, "state": review.State, "reason": review.Reason, "endpointUpdatesEnabled": false}, nil
	}
	saved, transaction, err := store.applyEndpointMutationLocked(ctx, mutation, now)
	if err != nil {
		return nil, directLANEndpointError(err)
	}
	return endpointSavedResult(saved, transaction, true), nil
}

func (s *directLANStore) endpointRecoveryCommandLocked(ctx context.Context, process, name string, input directLANEndpointInput, now time.Time) (any, error) {
	if input.PeerID != "" || input.Action != "" || input.Update != "" || input.Approval != nil || input.Follow != nil {
		return nil, endpointmeta.ErrInvalid
	}
	m, err := s.endpointModelLocked(now, true)
	if err != nil {
		return nil, err
	}
	pending := m.PendingChange
	if input.TransactionID == "" && name == "direct-lan.endpoint.recovery.inspect" {
		input.TransactionID = pending.TransactionID
	}
	if input.TransactionID != pending.TransactionID {
		return nil, directLANEndpointReviewChanged()
	}
	budget, err := s.endpointBudgetLocked()
	if err != nil {
		return nil, err
	}
	final, err := endpointmeta.FinishPending(*m, input.Cancel, now, budget)
	if err != nil {
		return nil, err
	}
	for _, r := range final.Peers {
		if r.EndpointState != nil && r.EndpointState.PairBinding == pending.Mutation.PairBinding {
			if err := s.endpointDeadlineErrorLocked(r.EndpointState, pending.Mutation.Kind, now); err != nil {
				return nil, err
			}
		}
	}
	if err := s.preflightEndpointSnapshotLocked(final, true); err != nil {
		return nil, err
	}
	// Bind the actual reviewed final authority, including any expired approval
	// or follow reduction. Only the volatile completion timestamp is omitted.
	semanticFinal := final
	semanticFinal.ObservedAt = ""
	revision := s.endpointReviewRevisionLocked(process, input, semanticFinal)
	if name == "direct-lan.endpoint.recovery.inspect" {
		return map[string]any{"revision": revision, "transactionId": pending.TransactionID, "action": pending.Mutation.Kind, "pairBindings": append([]string{}, pending.PairBindings...), "previousLocalEndpoint": m.LocalPeer.Endpoint, "localEndpoint": final.LocalPeer.Endpoint, "before": endpointPeerViews(*m), "after": endpointPeerViews(final), "cancel": input.Cancel, "canCancel": pending.Mutation.Kind == "set", "endpointUpdatesEnabled": false}, nil
	}
	if input.ExpectedRevision == "" || input.ExpectedRevision != revision {
		return nil, directLANEndpointReviewChanged()
	}
	saved, err := s.reconcileEndpointPendingLocked(ctx, input.TransactionID, input.Cancel, now)
	if err != nil {
		return nil, err
	}
	return endpointSavedResult(saved, input.TransactionID, true), nil
}
