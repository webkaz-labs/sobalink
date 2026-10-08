package endpointmeta

func (s Snapshot) measure(w *wireSizer) bool {
	return s.measureReplacing(w, -1, nil, false)
}

// measureReplacing sizes a candidate without allocating or copying its peer
// slice. The input may be larger than the replacement being measured.
func (s Snapshot) measureReplacing(w *wireSizer, replacementIndex int, replacement *PeerRecord, clearUpgrades bool) bool {
	if !w.reserve(len(`{"version":,"revision":,"local_peer":,"local_scope":,"previous_local_endpoint":,"observed_at":,"peers":}`)) || !w.integer(s.Version) || !w.str(s.Revision) || !s.LocalPeer.measure(w) || !s.LocalScope.measure(w) || !w.strings(s.PreviousLocalEndpoint, s.ObservedAt) {
		return false
	}
	if s.Peers == nil {
		if !w.reserve(4) {
			return false
		}
	} else {
		if !w.reserve(2) || len(s.Peers) > 0 && !w.reserve(len(s.Peers)-1) {
			return false
		}
		for i, r := range s.Peers {
			if replacement != nil && i == replacementIndex {
				r = *replacement
			}
			if clearUpgrades && r.PairRevocation == nil {
				r.UpgradePending = nil
			}
			if !r.measure(w) {
				return false
			}
		}
	}
	return s.PendingChange == nil || w.reserve(len(`,"pending_change":`)) && s.PendingChange.measure(w)
}

func (r PeerRecord) measure(w *wireSizer) bool {
	return w.reserve(len(`{"peer":,"revision":,"context_confirmed":}`)) && r.Peer.measure(w) && w.str(r.Revision) && w.boolean(r.ContextConfirmed) &&
		(r.UpgradePending == nil || w.reserve(len(`,"upgrade_pending":`)) && r.UpgradePending.measure(w)) &&
		(r.PairContext == nil || w.reserve(len(`,"pair_context":`)) && r.PairContext.measure(w)) &&
		(r.EndpointState == nil || w.reserve(len(`,"endpoint_state":`)) && r.EndpointState.measure(w)) &&
		(r.PairRevocation == nil || w.reserve(len(`,"pair_revocation":`)) && r.PairRevocation.measure(w))
}

func (u UpgradePending) measure(w *wireSizer) bool {
	return w.reserve(len(`{"reviewed_peer":,"peer_revision":,"own_nonce":,"prepare_deadline":}`)) && u.ReviewedPeer.measure(w) && w.strings(u.PeerRevision, u.OwnNonce, u.PrepareDeadline) &&
		(u.Context == nil || w.reserve(len(`,"context":`)) && u.Context.measure(w))
}

func (s EndpointState) measure(w *wireSizer) bool {
	return w.reserve(len(`{"version":,"pair_binding":,"issued_version":,"issued_highwater":,"received_version":,"received_highwater":,"last_endpoint":,"receive_status":,"authority_revision":}`)) && w.integer(s.Version) && w.str(s.PairBinding) && w.integer(s.IssuedVersion) && w.str(s.IssuedHighwater) && w.integer(s.ReceivedVersion) && w.strings(s.ReceivedHighwater, s.LastEndpoint, s.ReceiveStatus, s.AuthorityRevision) &&
		(s.IssuedProof == nil || w.reserve(len(`,"issued_proof":`)) && s.IssuedProof.measure(w)) &&
		(s.ReceivedProof == nil || w.reserve(len(`,"received_proof":`)) && s.ReceivedProof.measure(w)) &&
		(s.Approval == nil || w.reserve(len(`,"approval":`)) && s.Approval.measure(w)) &&
		(s.Follow == nil || w.reserve(len(`,"follow":`)) && s.Follow.measure(w))
}

func (a Approval) measure(w *wireSizer) bool {
	return w.reserve(len(`{"kind":,"proof_digest":,"endpoint":,"follow_revision":,"granted":,"lifetime":,"expires":}`)) && w.strings(a.Kind, a.ProofDigest, a.Endpoint, a.FollowRevision, a.Granted, a.Lifetime, a.Expires)
}

func (f FollowApproval) measure(w *wireSizer) bool {
	return w.reserve(len(`{"scope_digest":,"revision":,"granted":,"lifetime":,"expires":,"active":}`)) && w.strings(f.ScopeDigest, f.Revision, f.Granted, f.Lifetime, f.Expires) && w.boolean(f.Active)
}

func (p PendingChange) measure(w *wireSizer) bool {
	if !w.reserve(len(`{"transaction_id":,"fenced_at":,"pair_bindings":,"base_revision":,"base_digest":,"review_digest":,"mutation":}`)) || !w.strings(p.TransactionID, p.FencedAt, p.BaseRevision, p.BaseDigest, p.ReviewDigest) || !p.Mutation.measure(w) {
		return false
	}
	if p.PairBindings == nil {
		return w.reserve(4)
	}
	return w.reserve(2) && (len(p.PairBindings) == 0 || w.reserve(len(p.PairBindings)-1)) && w.strings(p.PairBindings...)
}

func (m Mutation) measure(w *wireSizer) bool {
	return w.reserve(len(`{"kind":,"pair_binding":,"local_endpoint":}`)) && w.strings(m.Kind, m.PairBinding, m.LocalEndpoint) &&
		(m.State == nil || w.reserve(len(`,"state":`)) && m.State.measure(w))
}

func (p PairRevocation) measure(w *wireSizer) bool {
	return w.reserve(len(`{"revision":,"revoked_at":}`)) && w.strings(p.Revision, p.RevokedAt) &&
		(p.PairBinding == "" || w.reserve(len(`,"pair_binding":`)) && w.str(p.PairBinding)) &&
		(p.Kind == "" || w.reserve(len(`,"kind":`)) && w.str(p.Kind)) &&
		(p.PeerKey == "" || w.reserve(len(`,"peer_key":`)) && w.str(p.PeerKey)) &&
		(p.RecordRevision == "" || w.reserve(len(`,"record_revision":`)) && w.str(p.RecordRevision)) &&
		(p.RecordDigest == "" || w.reserve(len(`,"record_digest":`)) && w.str(p.RecordDigest))
}
