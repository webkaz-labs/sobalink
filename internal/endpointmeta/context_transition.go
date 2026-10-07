package endpointmeta

import "time"

// ContextTransition is an inert candidate, not a saved or authenticated result.
// A production owner must separately compare its captured full-store admission,
// authenticate/claim exchange evidence, and durably publish the candidate before
// replying. None of these reducers authenticates caller-supplied request JSON.
type ContextTransition struct {
	Snapshot Snapshot
	Changed  bool
	Phase    string
}

// PreparationWindow is nonpersistent input from the exclusive process owner.
// Deadline must retain that owner's original monotonic cutoff for this saved
// peer/nonce/UTC deadline. Constructing this value is not authentication or proof
// of its history; the future owner must not recreate it on ordinary retries.
type PreparationWindow struct {
	PeerKey  string
	OwnNonce string
	Deadline time.Time
}

// ContextResumeReview binds only model data. Core's separate process/file/store
// admission remains required; this is not a production approval token.
type ContextResumeReview struct {
	PeerKey        string
	PeerRevision   string
	ProposalDigest string
	OldDeadline    string
	NewDeadline    string
	Revision       string
}

func contextRecord(s Snapshot, peerKey string, now time.Time) (int, error) {
	if s.PendingChange != nil {
		return -1, ErrRecovery
	}
	if err := s.ValidateAt(now); err != nil {
		return -1, err
	}
	return recordForKey(s, peerKey)
}

func contextPhase(r PeerRecord) string {
	if r.ContextConfirmed {
		return "confirmed"
	}
	if r.PairContext != nil {
		return "committed"
	}
	if r.UpgradePending != nil && r.UpgradePending.Context != nil {
		return "prepared"
	}
	return "reviewed"
}

func contextUnchanged(s Snapshot, r PeerRecord, budget int) (ContextTransition, error) {
	if _, err := EncodeSnapshot(s, budget); err != nil {
		return ContextTransition{}, err
	}
	return ContextTransition{Snapshot: cloneSnapshot(s), Phase: contextPhase(r)}, nil
}

func contextChanged(s Snapshot, i int, r PeerRecord, confirm bool, now time.Time, budget int) (ContextTransition, error) {
	revision, err := nextCounter(s.Revision)
	if err != nil {
		return ContextTransition{}, err
	}
	if confirm {
		r.Revision = revision
	}
	next := s
	next.Revision, next.ObservedAt = revision, now.UTC().Format(time.RFC3339Nano)
	w := wireSizer{left: budget}
	if budget <= 0 || !next.measureReplacing(&w, i, &r, false) {
		return ContextTransition{}, ErrCapacity
	}
	next.Peers = append([]PeerRecord(nil), s.Peers...)
	next.Peers[i] = r
	// Clone the complete output, including every mutable scope slice. No input
	// request, reply or snapshot reference becomes a writable output alias.
	next = cloneSnapshot(next)
	if _, err := EncodeSnapshot(next, budget); err != nil {
		return ContextTransition{}, err
	}
	return ContextTransition{Snapshot: next, Changed: true, Phase: contextPhase(r)}, nil
}

func currentPreparation(r PeerRecord, window PreparationWindow, now time.Time) error {
	p := r.UpgradePending
	if p == nil {
		return ErrReview
	}
	deadline, err := instant(p.PrepareDeadline)
	if err != nil {
		return err
	}
	if window.PeerKey != r.Peer.Key || window.OwnNonce != p.OwnNonce || window.Deadline.IsZero() || window.Deadline.UTC().Format(time.RFC3339Nano) != p.PrepareDeadline {
		return ErrReview
	}
	if !now.Before(deadline) || !now.Before(window.Deadline) {
		return ErrExpired
	}
	return nil
}

// PrepareContextUpgrade accepts an already reviewed unchanged legacy record.
// The caller supplies fresh 32-byte CSPRNG output and an explicit finite UTC
// deadline, then retains the corresponding process bound before fallible save.
func PrepareContextUpgrade(s Snapshot, peerKey, expectedPeerRevision, ownNonce, deadline string, now time.Time, budget int) (ContextTransition, error) {
	i, err := contextRecord(s, peerKey, now)
	if err != nil {
		return ContextTransition{}, err
	}
	r := s.Peers[i]
	if r.Revision != expectedPeerRevision || r.ContextConfirmed {
		return ContextTransition{}, ErrReview
	}
	if _, err := rawBytes(ownNonce, 32); err != nil {
		return ContextTransition{}, err
	}
	until, err := instant(deadline)
	if err != nil {
		return ContextTransition{}, err
	}
	if p := r.UpgradePending; p != nil {
		if p.OwnNonce != ownNonce || p.PrepareDeadline != deadline {
			return ContextTransition{}, ErrReview
		}
		// An exact retry observes the existing phase, including an expired
		// preparation. It grants no renewed deadline or first-commit permission.
		return contextUnchanged(s, r, budget)
	}
	if r.PairContext != nil || r.EndpointState != nil {
		return ContextTransition{}, ErrReview
	}
	if !now.Before(until) {
		return ContextTransition{}, ErrExpired
	}
	r.UpgradePending = &UpgradePending{ReviewedPeer: r.Peer, PeerRevision: r.Revision, OwnNonce: ownNonce, PrepareDeadline: deadline}
	return contextChanged(s, i, r, false, now, budget)
}

func expectedPrepare(s Snapshot, r PeerRecord, local bool) PrepareRequest {
	request := PrepareRequest{Version: 2, Operation: "pair-context-prepare"}
	if local {
		request.Sender, request.Recipient = s.LocalPeer.Key, r.Peer.Key
		request.SenderTunnelKey, request.RecipientTunnelKey = s.LocalPeer.TunnelKey, r.Peer.TunnelKey
		request.SenderEndpoint, request.RecipientEndpoint = s.LocalPeer.Endpoint, r.Peer.Endpoint
		request.SenderNonce, request.SenderScope = r.UpgradePending.OwnNonce, s.LocalScope
	} else {
		request.Sender, request.Recipient = r.Peer.Key, s.LocalPeer.Key
		request.SenderTunnelKey, request.RecipientTunnelKey = r.Peer.TunnelKey, s.LocalPeer.TunnelKey
		request.SenderEndpoint, request.RecipientEndpoint = r.Peer.Endpoint, s.LocalPeer.Endpoint
	}
	return request
}

func upgradeContext(s Snapshot, r PeerRecord, remoteNonce string, remoteScope Scope) PairContext {
	p := PairContext{Version: 1, HostKey: s.LocalPeer.Key, JoinerKey: r.Peer.Key,
		HostTunnelKey: s.LocalPeer.TunnelKey, JoinerTunnelKey: r.Peer.TunnelKey,
		HostNonce: r.UpgradePending.OwnNonce, JoinerNonce: remoteNonce,
		HostEndpoint: s.LocalPeer.Endpoint, JoinerEndpoint: r.Peer.Endpoint,
		HostScope: s.LocalScope, JoinerScope: remoteScope}
	if p.HostKey > p.JoinerKey {
		p.HostKey, p.JoinerKey = p.JoinerKey, p.HostKey
		p.HostTunnelKey, p.JoinerTunnelKey = p.JoinerTunnelKey, p.HostTunnelKey
		p.HostNonce, p.JoinerNonce = p.JoinerNonce, p.HostNonce
		p.HostEndpoint, p.JoinerEndpoint = p.JoinerEndpoint, p.HostEndpoint
		p.HostScope, p.JoinerScope = p.JoinerScope, p.HostScope
	}
	return p
}

func recordPreparedContext(s Snapshot, i int, p PairContext, window PreparationWindow, now time.Time, budget int) (ContextTransition, error) {
	r := s.Peers[i]
	if r.UpgradePending == nil || r.ContextConfirmed {
		return ContextTransition{}, ErrReview
	}
	if err := p.validate(); err != nil {
		return ContextTransition{}, err
	}
	if existing := r.UpgradePending.Context; existing != nil {
		if modelDigest(*existing) != modelDigest(p) {
			return ContextTransition{}, ErrIdentity
		}
		return contextUnchanged(s, r, budget)
	}
	if r.PairContext != nil || r.EndpointState != nil {
		return ContextTransition{}, ErrReview
	}
	if err := currentPreparation(r, window, now); err != nil {
		return ContextTransition{}, err
	}
	pending := *r.UpgradePending
	pending.Context = &p
	r.UpgradePending = &pending
	return contextChanged(s, i, r, false, now, budget)
}

// RecordInboundContextPrepare checks the remote request against admitted local
// model data. The peerKey argument is not proof of TLS pinning or authentication.
func RecordInboundContextPrepare(s Snapshot, peerKey string, request PrepareRequest, window PreparationWindow, now time.Time, budget int) (ContextTransition, error) {
	i, err := contextRecord(s, peerKey, now)
	if err != nil {
		return ContextTransition{}, err
	}
	r := s.Peers[i]
	if r.UpgradePending == nil {
		return ContextTransition{}, ErrReview
	}
	if _, err := Encode(request); err != nil {
		return ContextTransition{}, err
	}
	expected := expectedPrepare(s, r, false)
	expected.SenderNonce, expected.SenderScope = request.SenderNonce, request.SenderScope
	if modelDigest(request) != modelDigest(expected) {
		return ContextTransition{}, ErrIdentity
	}
	return recordPreparedContext(s, i, upgradeContext(s, r, request.SenderNonce, request.SenderScope), window, now, budget)
}

// RecordOutboundContextPrepare requires the exact original outbound request and
// its operation-specific reply. Matching their bytes is not authentication.
func RecordOutboundContextPrepare(s Snapshot, peerKey string, request PrepareRequest, reply PrepareReply, window PreparationWindow, now time.Time, budget int) (ContextTransition, error) {
	i, err := contextRecord(s, peerKey, now)
	if err != nil {
		return ContextTransition{}, err
	}
	r := s.Peers[i]
	if r.UpgradePending == nil {
		return ContextTransition{}, ErrReview
	}
	if _, err := Encode(request); err != nil {
		return ContextTransition{}, err
	}
	if _, err := Encode(reply); err != nil {
		return ContextTransition{}, err
	}
	if modelDigest(request) != modelDigest(expectedPrepare(s, r, true)) {
		return ContextTransition{}, ErrIdentity
	}
	remoteNonce, remoteScope := reply.PairContext.JoinerNonce, reply.PairContext.JoinerScope
	if peerKey == reply.PairContext.HostKey {
		remoteNonce, remoteScope = reply.PairContext.HostNonce, reply.PairContext.HostScope
	}
	expected := upgradeContext(s, r, remoteNonce, remoteScope)
	if modelDigest(reply.PairContext) != modelDigest(expected) {
		return ContextTransition{}, ErrIdentity
	}
	return recordPreparedContext(s, i, expected, window, now, budget)
}

func CommitPreparedContext(s Snapshot, peerKey, binding string, window PreparationWindow, now time.Time, budget int) (ContextTransition, error) {
	i, err := contextRecord(s, peerKey, now)
	if err != nil {
		return ContextTransition{}, err
	}
	r := s.Peers[i]
	if r.PairContext != nil {
		current, _ := r.PairContext.Binding()
		if current != binding {
			return ContextTransition{}, ErrIdentity
		}
		return contextUnchanged(s, r, budget)
	}
	if r.UpgradePending == nil || r.UpgradePending.Context == nil || r.EndpointState != nil {
		return ContextTransition{}, ErrReview
	}
	p := *r.UpgradePending.Context
	want, _ := p.Binding()
	if binding != want {
		return ContextTransition{}, ErrIdentity
	}
	if err := currentPreparation(r, window, now); err != nil {
		return ContextTransition{}, err
	}
	state, err := InitialState(p, s.LocalPeer.Key)
	if err != nil {
		return ContextTransition{}, err
	}
	r.PairContext, r.EndpointState = &p, &state
	return contextChanged(s, i, r, false, now, budget)
}

func confirmContext(s Snapshot, i int, now time.Time, budget int) (ContextTransition, error) {
	r := s.Peers[i]
	if r.PairContext == nil || r.EndpointState == nil {
		return ContextTransition{}, ErrReview
	}
	if r.ContextConfirmed {
		return contextUnchanged(s, r, budget)
	}
	if r.UpgradePending == nil || r.UpgradePending.Context == nil || modelDigest(*r.UpgradePending.Context) != modelDigest(*r.PairContext) {
		return ContextTransition{}, ErrReview
	}
	// Preparation expiry cannot erase an already committed context. Fresh
	// evidence validity is an owner precondition; preserve all endpoint state.
	r.ContextConfirmed, r.UpgradePending = true, nil
	return contextChanged(s, i, r, true, now, budget)
}

// ConfirmContextCommit is a data-only reducer over the exact request and reply.
// An authenticated, current, one-use owner evidence claim is still required.
// An inbound commit request, prepared reply, session reply or bare binding is
// never accepted as the reply evidence variant by this API.
func ConfirmContextCommit(s Snapshot, peerKey string, request BoundRequest, reply ContextReply, now time.Time, budget int) (ContextTransition, error) {
	i, err := contextRecord(s, peerKey, now)
	if err != nil {
		return ContextTransition{}, err
	}
	if _, err := Encode(request); err != nil {
		return ContextTransition{}, err
	}
	if _, err := Encode(reply); err != nil {
		return ContextTransition{}, err
	}
	if request.Operation != "pair-context-commit" && request.Operation != "pair-context-status" ||
		reply.Operation != request.Operation || reply.State != "committed" || reply.PairBinding != request.PairBinding {
		return ContextTransition{}, ErrIdentity
	}
	r := s.Peers[i]
	if r.PairContext == nil {
		return ContextTransition{}, ErrReview
	}
	binding, _ := r.PairContext.Binding()
	if request.PairBinding != binding {
		return ContextTransition{}, ErrIdentity
	}
	return confirmContext(s, i, now, budget)
}

// ConfirmContextFromUpdate verifies cryptographic possession of the current
// context but neither accepts the endpoint offer nor consumes its sequence.
func ConfirmContextFromUpdate(s Snapshot, peerKey string, proof Envelope, now time.Time, budget int) (ContextTransition, error) {
	i, err := contextRecord(s, peerKey, now)
	if err != nil {
		return ContextTransition{}, err
	}
	r := s.Peers[i]
	if r.PairContext == nil || proof.Update.Issuer != peerKey {
		return ContextTransition{}, ErrReview
	}
	if err := Inspect(proof, *r.PairContext, s.LocalPeer.Key, now); err != nil {
		return ContextTransition{}, err
	}
	return confirmContext(s, i, now, budget)
}

func ReviewContextResume(s Snapshot, peerKey, newDeadline string, now time.Time) (ContextResumeReview, error) {
	i, err := contextRecord(s, peerKey, now)
	if err != nil {
		return ContextResumeReview{}, err
	}
	r := s.Peers[i]
	if r.UpgradePending == nil || r.ContextConfirmed {
		return ContextResumeReview{}, ErrReview
	}
	deadline, err := instant(newDeadline)
	if err != nil {
		return ContextResumeReview{}, err
	}
	if !now.Before(deadline) {
		return ContextResumeReview{}, ErrExpired
	}
	proposal := modelDigest(struct {
		Local PeerWire
		Scope Scope
		Peer  PeerRecord
	}{s.LocalPeer, s.LocalScope, r})
	review := ContextResumeReview{PeerKey: peerKey, PeerRevision: r.Revision, ProposalDigest: proposal,
		OldDeadline: r.UpgradePending.PrepareDeadline, NewDeadline: newDeadline}
	review.Revision = modelDigest(struct {
		Snapshot Snapshot
		Review   ContextResumeReview
	}{s, review})
	return review, nil
}

func ResumePreparedContext(s Snapshot, review ContextResumeReview, now time.Time, budget int) (ContextTransition, error) {
	expected, err := ReviewContextResume(s, review.PeerKey, review.NewDeadline, now)
	if err != nil {
		return ContextTransition{}, err
	}
	if review != expected {
		return ContextTransition{}, ErrReview
	}
	i, _ := recordForKey(s, review.PeerKey)
	r := s.Peers[i]
	if review.NewDeadline == review.OldDeadline {
		return contextUnchanged(s, r, budget)
	}
	pending := *r.UpgradePending
	pending.PrepareDeadline = review.NewDeadline
	r.UpgradePending = &pending
	return contextChanged(s, i, r, false, now, budget)
}
