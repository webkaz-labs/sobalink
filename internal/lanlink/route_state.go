package lanlink

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
	"math"
	"net"
	"slices"
	"sort"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

const RouteStateVersion = 2

var ErrRoutePermission = errors.New("no locally approved current route")

// RouteApproval is independent local permission for one exact endpoint,
// certificate and scope. Permanent permission requires an explicit lifetime;
// omitted lifetime is finite only when reading original version-1 state.
type RouteApproval struct {
	CandidateID string    `json:"candidate_id"`
	Granted     time.Time `json:"granted"`
	Expires     time.Time `json:"expires"`
	Lifetime    string    `json:"lifetime,omitempty"`
}

func (a RouteApproval) EffectiveLifetime() string {
	if a.Lifetime == "" {
		return RouteLifetimeFinite
	}
	return a.Lifetime
}

func (a RouteApproval) active(now time.Time) bool {
	if a.Granted.IsZero() || a.Granted.After(now) {
		return false
	}
	switch a.EffectiveLifetime() {
	case RouteLifetimeFinite:
		return !a.Expires.IsZero() && a.Expires.After(now)
	case RouteLifetimeUntilRevoked:
		return a.Expires.IsZero()
	}
	return false
}

// RouteState is protected pairing state. ReceivedSequence and its authenticated
// proof survive expiry and withdrawal; deleting either would permit rollback.
// Nil state is the legacy singleton route. Issuing an update alone does not
// convert the local outgoing route into managed mode.
type RouteState struct {
	Version          int             `json:"version"`
	PairBinding      string          `json:"pair_binding"`
	IssuedSequence   uint64          `json:"issued_sequence"`
	IssuedVersion    int             `json:"issued_version,omitempty"`
	ReceivedSequence uint64          `json:"received_sequence"`
	Received         *RouteUpdate    `json:"received,omitempty"`
	ReceivedProof    []byte          `json:"received_proof,omitempty"`
	Approvals        []RouteApproval `json:"approvals,omitempty"`
}

// RouteReview is a read-only authenticated review. The digest binds Apply to
// the exact privately exchanged envelope the user reviewed, not just a peer or
// candidate list. It is not authorization and does not consume a sequence.
type RouteReview struct {
	Digest string      `json:"digest"`
	Update RouteUpdate `json:"update"`
}

// RouteSnapshot contains no sealed capability or private key. Legacy remains
// true until a received update has been explicitly applied.
type RouteSnapshot struct {
	Legacy           bool             `json:"legacy"`
	IssuedSequence   uint64           `json:"issued_sequence"`
	ReceivedSequence uint64           `json:"received_sequence"`
	Candidates       []RouteCandidate `json:"candidates"`
	Approvals        []RouteApproval  `json:"approvals"`
	Permitted        []RouteCandidate `json:"permitted"`
	Expires          time.Time        `json:"expires,omitempty"`
	Lifetime         string           `json:"lifetime,omitempty"`
	NextExpiry       time.Time        `json:"next_expiry,omitempty"`
	RecoveryRequired bool             `json:"recovery_required"`
}

func cloneRouteState(s *RouteState) *RouteState {
	if s == nil {
		return nil
	}
	out := *s
	out.ReceivedProof = slices.Clone(s.ReceivedProof)
	out.Approvals = slices.Clone(s.Approvals)
	if s.Received != nil {
		u := *s.Received
		u.Candidates = slices.Clone(s.Received.Candidates)
		out.Received = &u
	}
	return &out
}

// CloneRemotePeers keeps mutable route slices out of the Node and store's
// durable snapshots. Pair capabilities and private keys remain protected data.
func CloneRemotePeers(remotes []RemotePeer) []RemotePeer {
	out := slices.Clone(remotes)
	for i := range out {
		out[i].Routes = cloneRouteState(out[i].Routes)
	}
	return out
}

func newRouteState(identity Identity, remote RemotePeer) (*RouteState, error) {
	binding, err := PairRouteBinding(identity, remote)
	if err != nil {
		return nil, err
	}
	if remote.Routes != nil {
		out := cloneRouteState(remote.Routes)
		if out.Version == 1 {
			out.Version = RouteStateVersion
			if out.IssuedSequence != 0 {
				out.IssuedVersion = legacyRouteUpdateVersion
			}
			for i := range out.Approvals {
				out.Approvals[i].Lifetime = RouteLifetimeFinite
			}
		}
		return out, nil
	}
	return &RouteState{Version: RouteStateVersion, PairBinding: binding}, nil
}

// ValidateRouteState validates saved protected state without granting expired
// permissions or resetting high-water marks. Proof is rechecked at its signed
// issue time so reopening an expired or withdrawn offer remains possible.
func ValidateRouteState(identity Identity, remote RemotePeer) error {
	s := remote.Routes
	if s == nil {
		return nil
	}
	binding, err := PairRouteBinding(identity, remote)
	if err != nil || (s.Version != 1 && s.Version != RouteStateVersion) || s.PairBinding != binding || len(s.Approvals) > tailcat.RelayRegionNamespace {
		return ErrRouteUpdate
	}
	if s.Version == 1 {
		if s.IssuedVersion != 0 || s.Received != nil && s.Received.Version != legacyRouteUpdateVersion {
			return ErrRouteUpdate
		}
	} else if s.IssuedSequence == 0 && s.IssuedVersion != 0 || s.IssuedSequence != 0 && s.IssuedVersion != legacyRouteUpdateVersion && s.IssuedVersion != RouteUpdateVersion {
		return ErrRouteUpdate
	}
	if s.Received == nil {
		if s.ReceivedSequence != 0 || len(s.ReceivedProof) != 0 || len(s.Approvals) != 0 {
			return ErrRouteUpdate
		}
		return nil
	}
	if s.ReceivedSequence == 0 || s.ReceivedSequence != s.Received.Sequence {
		return ErrRouteUpdate
	}
	verified, err := OpenRouteUpdate(identity, remote, s.ReceivedProof, s.ReceivedSequence-1, s.Received.Issued)
	if err != nil {
		return ErrRouteUpdate
	}
	a, _ := json.Marshal(verified)
	b, _ := json.Marshal(s.Received)
	if !bytes.Equal(a, b) {
		return ErrRouteUpdate
	}
	candidates := make(map[string]bool, len(verified.Candidates))
	for _, c := range verified.Candidates {
		candidates[c.ID()] = true
	}
	seen := make(map[string]bool, len(s.Approvals))
	for _, approval := range s.Approvals {
		if !candidates[approval.CandidateID] || seen[approval.CandidateID] || approval.Granted.Before(verified.Issued) || approval.Granted.IsZero() {
			return ErrRouteUpdate
		}
		if s.Version == 1 && approval.Lifetime != "" || s.Version == RouteStateVersion && approval.Lifetime == "" {
			return ErrRouteUpdate
		}
		if err := validateRouteApproval(verified, approval.EffectiveLifetime(), approval.Granted, approval.Expires); err != nil {
			return ErrRouteUpdate
		}
		seen[approval.CandidateID] = true
	}
	return nil
}

func routeReviewDigest(raw []byte) string {
	return digest(append([]byte("sobalink route review v1\x00"), raw...))
}

// routePeerLocked requires n.mu; callers serialize writes with pairMu first.
func (n *Node) routePeerLocked(peer string) (*remoteClient, error) {
	if n.pairingRecovery {
		return nil, config.ErrAtomicRecovery
	}
	if n.closed {
		return nil, net.ErrClosed
	}
	r := n.clients[peer]
	if r == nil {
		return nil, ErrUntrusted
	}
	if _, err := n.cfg.Trust.Epoch(peer); err != nil {
		return nil, err
	}
	return r, nil
}

// persistRouteLocked stages the full private snapshot. n.pairMu and n.mu must
// be held. Nothing changes on prepublication failure; uncertain durability
// freezes all later snapshot writers and outgoing reconnects until reopen.
func (n *Node) persistRouteLocked(remote RemotePeer) error {
	if n.cfg.Persist == nil {
		return errors.New("durable route persistence required")
	}
	records := n.remoteSnapshotLocked()
	for i := range records {
		if records[i].Peer.Key == remote.Peer.Key {
			records[i] = CloneRemotePeers([]RemotePeer{remote})[0]
		}
	}
	b := n.cfg.Trust
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.peers[remote.Peer.Key] == nil {
		return ErrUntrusted
	}
	snapshot := Snapshot{Version: 1, Peers: make([]Peer, 0, len(b.peers))}
	for _, approval := range b.peers {
		snapshot.Peers = append(snapshot.Peers, approval.peer)
	}
	sort.Slice(snapshot.Peers, func(i, j int) bool { return snapshot.Peers[i].Key < snapshot.Peers[j].Key })
	if err := n.cfg.Persist(snapshot, records); err != nil {
		if errors.Is(err, config.ErrAtomicCommitted) || errors.Is(err, config.ErrAtomicRecovery) {
			n.pairingRecovery = true
		}
		return fmt.Errorf("route state durable save unconfirmed: %w", err)
	}
	return nil
}

// ExportRouteUpdate publishes only after the issued sequence is durably saved.
// It neither changes local outgoing permissions nor contacts any destination.
func (n *Node) ExportRouteUpdate(peer string, candidates []RouteCandidate, expires time.Time) ([]byte, error) {
	return n.exportRouteUpdate(peer, candidates, expires, time.Time{})
}
func (n *Node) exportRouteUpdate(peer string, candidates []RouteCandidate, expires, now time.Time) ([]byte, error) {
	return n.exportRouteUpdateWithLifetime(peer, candidates, legacyRouteUpdateVersion, "", expires, now)
}

func (n *Node) ExportRouteUpdateWithLifetime(peer string, candidates []RouteCandidate, lifetime string, expires time.Time) ([]byte, error) {
	return n.exportRouteUpdateWithLifetime(peer, candidates, RouteUpdateVersion, lifetime, expires, time.Time{})
}

func (n *Node) exportRouteUpdateWithLifetime(peer string, candidates []RouteCandidate, version int, lifetime string, expires, now time.Time) ([]byte, error) {
	n.pairMu.Lock()
	defer n.pairMu.Unlock()
	n.mu.Lock()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	defer n.mu.Unlock()
	r, err := n.routePeerLocked(peer)
	if err != nil {
		return nil, err
	}
	s, err := newRouteState(n.cfg.Identity, r.remote)
	if err != nil || s.IssuedSequence == math.MaxUint64 || version < s.IssuedVersion {
		return nil, ErrRouteUpdate
	}
	frame, err := sealRouteUpdate(n.cfg.Identity, r.remote, s.IssuedSequence+1, candidates, version, lifetime, now, expires)
	if err != nil {
		return nil, err
	}
	s.IssuedSequence++
	s.IssuedVersion = version
	next := r.remote
	next.Routes = s
	if err := n.persistRouteLocked(next); err != nil {
		return nil, err
	}
	r.remote = next
	return frame, nil
}

func (n *Node) InspectRouteUpdate(peer string, raw []byte) (RouteReview, error) {
	return n.inspectRouteUpdate(peer, raw, time.Time{})
}
func (n *Node) inspectRouteUpdate(peer string, raw []byte, now time.Time) (RouteReview, error) {
	n.mu.Lock()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	defer n.mu.Unlock()
	r, err := n.routePeerLocked(peer)
	if err != nil {
		return RouteReview{}, err
	}
	var highest uint64
	if r.remote.Routes != nil {
		highest = r.remote.Routes.ReceivedSequence
	}
	u, err := OpenRouteUpdate(n.cfg.Identity, r.remote, raw, highest, now)
	if err != nil {
		return RouteReview{}, err
	}
	if r.remote.Routes != nil && r.remote.Routes.Received != nil && u.Version < r.remote.Routes.Received.Version {
		return RouteReview{}, ErrRouteUpdate
	}
	return RouteReview{Digest: routeReviewDigest(raw), Update: u}, nil
}

// ApplyRouteUpdate re-verifies an exact reviewed envelope under the save lock
// and replaces the local approved selection. Empty selectedIDs explicitly
// records the authenticated offer without granting any route. Approvals are
// never inherited from a removed candidate, a previous offer or a former pair.
func (n *Node) ApplyRouteUpdate(peer string, raw []byte, reviewedDigest string, selectedIDs []string, approvalExpiry time.Time) error {
	return n.applyRouteUpdate(peer, raw, reviewedDigest, selectedIDs, approvalExpiry, time.Time{})
}
func (n *Node) applyRouteUpdate(peer string, raw []byte, reviewedDigest string, selectedIDs []string, approvalExpiry, now time.Time) error {
	return n.applyRouteUpdateWithLifetime(peer, raw, reviewedDigest, selectedIDs, RouteLifetimeFinite, approvalExpiry, now)
}

func (n *Node) ApplyRouteUpdateWithLifetime(peer string, raw []byte, reviewedDigest string, selectedIDs []string, lifetime string, expiry time.Time) error {
	return n.applyRouteUpdateWithLifetime(peer, raw, reviewedDigest, selectedIDs, lifetime, expiry, time.Time{})
}

func (n *Node) applyRouteUpdateWithLifetime(peer string, raw []byte, reviewedDigest string, selectedIDs []string, lifetime string, approvalExpiry, now time.Time) error {
	if len(raw) == 0 || len(raw) > maxPairMessage || !validKey(reviewedDigest) || reviewedDigest != routeReviewDigest(raw) || len(selectedIDs) > tailcat.RelayRegionNamespace {
		return ErrRouteUpdate
	}
	n.pairMu.Lock()
	defer n.pairMu.Unlock()
	n.mu.Lock()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	r, err := n.routePeerLocked(peer)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	s, err := newRouteState(n.cfg.Identity, r.remote)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	u, err := OpenRouteUpdate(n.cfg.Identity, r.remote, raw, s.ReceivedSequence, now)
	if err != nil || s.Received != nil && u.Version < s.Received.Version {
		n.mu.Unlock()
		return ErrRouteUpdate
	}
	approvals, err := selectedApprovalsWithLifetime(u, selectedIDs, lifetime, approvalExpiry, now)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	s.ReceivedSequence, s.Received, s.ReceivedProof, s.Approvals = u.Sequence, &u, slices.Clone(raw), approvals
	next := r.remote
	next.Routes = s
	return n.commitRouteChangeLocked(peer, r, next, now)
}

func selectedApprovals(u RouteUpdate, ids []string, expiry, now time.Time) ([]RouteApproval, error) {
	return selectedApprovalsWithLifetime(u, ids, RouteLifetimeFinite, expiry, now)
}

func validateRouteApproval(u RouteUpdate, lifetime string, granted, expiry time.Time) error {
	switch lifetime {
	case RouteLifetimeFinite:
		if !finiteRouteLifetime(granted, expiry) || u.EffectiveLifetime() == RouteLifetimeFinite && expiry.After(u.Expires) || u.Version == legacyRouteUpdateVersion && expiry.Sub(granted) > MaxRouteUpdateLifetime {
			return ErrRouteUpdate
		}
	case RouteLifetimeUntilRevoked:
		if !expiry.IsZero() || u.Version != RouteUpdateVersion || u.EffectiveLifetime() != RouteLifetimeUntilRevoked {
			return ErrRouteUpdate
		}
	default:
		return ErrRouteUpdate
	}
	return nil
}

func selectedApprovalsWithLifetime(u RouteUpdate, ids []string, lifetime string, expiry, now time.Time) ([]RouteApproval, error) {
	if (lifetime != RouteLifetimeFinite && lifetime != RouteLifetimeUntilRevoked) || lifetime == RouteLifetimeUntilRevoked && !expiry.IsZero() || len(ids) > tailcat.RelayRegionNamespace || len(ids) > 0 && validateRouteApproval(u, lifetime, now, expiry) != nil {
		return nil, ErrRouteUpdate
	}
	available := make(map[string]bool, len(u.Candidates))
	for _, candidate := range u.Candidates {
		available[candidate.ID()] = true
	}
	seen := make(map[string]bool, len(ids))
	approvals := make([]RouteApproval, 0, len(ids))
	for _, id := range ids {
		if !available[id] || seen[id] {
			return nil, ErrRouteUpdate
		}
		seen[id] = true
		approvals = append(approvals, RouteApproval{CandidateID: id, Granted: now.UTC(), Expires: expiry.UTC(), Lifetime: lifetime})
	}
	return approvals, nil
}

// ReviewRoutes rechecks the saved current proof for an explicit local change.
// It does not import a replay or mutate the received high-water mark.
func (n *Node) ReviewRoutes(peer string) (RouteReview, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	r, err := n.routePeerLocked(peer)
	if err != nil {
		return RouteReview{}, err
	}
	return currentRouteReview(n.cfg.Identity, r.remote, time.Now().UTC())
}

func currentRouteReview(identity Identity, remote RemotePeer, now time.Time) (RouteReview, error) {
	s := remote.Routes
	if s == nil || s.Received == nil || s.ReceivedSequence == 0 {
		return RouteReview{}, ErrRoutePermission
	}
	u, err := OpenRouteUpdate(identity, remote, s.ReceivedProof, s.ReceivedSequence-1, now)
	if err != nil {
		return RouteReview{}, err
	}
	return RouteReview{Digest: routeReviewDigest(s.ReceivedProof), Update: u}, nil
}

// ApproveRoutes explicitly replaces local permissions for the exact current
// reviewed offer. It permits deliberate reapproval after a local revoke while
// keeping received-update replay rejection and remote high-water unchanged.
func (n *Node) ApproveRoutes(peer, reviewedDigest string, selectedIDs []string, expiry time.Time) error {
	return n.approveRoutes(peer, reviewedDigest, selectedIDs, expiry, time.Time{})
}
func (n *Node) approveRoutes(peer, reviewedDigest string, selectedIDs []string, expiry, now time.Time) error {
	return n.approveRoutesWithLifetime(peer, reviewedDigest, selectedIDs, RouteLifetimeFinite, expiry, now)
}

func (n *Node) ApproveRoutesWithLifetime(peer, reviewedDigest string, selectedIDs []string, lifetime string, expiry time.Time) error {
	return n.approveRoutesWithLifetime(peer, reviewedDigest, selectedIDs, lifetime, expiry, time.Time{})
}

func (n *Node) approveRoutesWithLifetime(peer, reviewedDigest string, selectedIDs []string, lifetime string, expiry, now time.Time) error {
	n.pairMu.Lock()
	defer n.pairMu.Unlock()
	n.mu.Lock()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	r, err := n.routePeerLocked(peer)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	review, err := currentRouteReview(n.cfg.Identity, r.remote, now)
	if err != nil || review.Digest != reviewedDigest {
		n.mu.Unlock()
		return ErrRouteUpdate
	}
	approvals, err := selectedApprovalsWithLifetime(review.Update, selectedIDs, lifetime, expiry, now)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	next := r.remote
	next.Routes, err = newRouteState(n.cfg.Identity, r.remote)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	next.Routes.Approvals = approvals
	return n.commitRouteChangeLocked(peer, r, next, now)
}

// routeAuthority contains only authority effective now, with its final deadline.
// A zero deadline is until-revoked, not an already expired finite permission.
func routeAuthority(remote RemotePeer, anchor TrustedRelay, now time.Time) map[string]time.Time {
	snapshot := routeSnapshot(remote, now)
	if snapshot.Legacy {
		return map[string]time.Time{legacyCandidate(anchor).ID(): {}}
	}
	out := make(map[string]time.Time, len(snapshot.Permitted))
	for _, candidate := range snapshot.Permitted {
		deadline := remote.Routes.Received.Expires
		for _, approval := range remote.Routes.Approvals {
			if approval.CandidateID == candidate.ID() && approval.active(now) {
				if !approval.Expires.IsZero() && (deadline.IsZero() || approval.Expires.Before(deadline)) {
					deadline = approval.Expires
				}
				out[candidate.ID()] = deadline
			}
		}
	}
	return out
}

func reducesRouteAuthority(before, after RemotePeer, anchor TrustedRelay, now time.Time) bool {
	next := routeAuthority(after, anchor, now)
	for id, oldDeadline := range routeAuthority(before, anchor, now) {
		deadline, exists := next[id]
		if !exists || !deadline.IsZero() && (oldDeadline.IsZero() || deadline.Before(oldDeadline)) {
			return true
		}
	}
	return false
}

// commitRouteChangeLocked consumes n.mu; callers retain n.pairMu. Reductions
// retire the old generation before attempting a durable save. The interim
// state grants no outgoing route, including new grants in a mixed replacement.
// Pure grants still become effective only after confirmed persistence.
func (n *Node) commitRouteChangeLocked(peer string, old *remoteClient, next RemotePeer, now time.Time) error {
	withdrawal := next.Routes != nil && next.Routes.Received != nil && len(next.Routes.Received.Candidates) == 0
	if !withdrawal && !reducesRouteAuthority(old.remote, next, n.cfg.Relay, now) {
		if err := n.persistRouteLocked(next); err != nil {
			n.mu.Unlock()
			return err
		}
		n.replaceRemoteLocked(peer, old, next)
		n.mu.Unlock()
		return old.shutdown()
	}
	blocked := next
	blocked.Routes = cloneRouteState(next.Routes)
	blocked.Routes.Approvals = nil
	pending := n.replaceRemoteLocked(peer, old, blocked)
	n.mu.Unlock()
	stopErr := old.shutdown()
	n.mu.Lock()
	var err error
	if n.closed {
		err = net.ErrClosed
	} else if stopErr != nil {
		err = stopErr
	} else {
		err = n.persistRouteLocked(next)
	}
	if err != nil {
		n.pairingRecovery = true
		n.mu.Unlock()
		return errors.Join(err, stopErr, config.ErrAtomicRecovery)
	}
	n.replaceRemoteLocked(peer, pending, next)
	n.mu.Unlock()
	return pending.shutdown()
}

// RevokeRoutes removes exact local permissions without changing application
// trust or role keys. Empty IDs revoke all managed routes. Local outgoing work
// is stopped before persistence, even when the save fails. A failed save is
// surfaced and the reduced in-memory authority stays in force for a retry.
func (n *Node) RevokeRoutes(peer string, ids []string) error {
	if len(ids) > tailcat.RelayRegionNamespace {
		return ErrRouteUpdate
	}
	remove := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !validKey(id) || remove[id] {
			return ErrRouteUpdate
		}
		remove[id] = true
	}
	n.pairMu.Lock()
	defer n.pairMu.Unlock()
	n.mu.Lock()
	r := n.clients[peer]
	if r == nil {
		n.mu.Unlock()
		return ErrUntrusted
	}
	if r.remote.Routes == nil || r.remote.Routes.Received == nil {
		n.mu.Unlock()
		return ErrRoutePermission
	}
	available := make(map[string]bool, len(r.remote.Routes.Received.Candidates))
	for _, candidate := range r.remote.Routes.Received.Candidates {
		available[candidate.ID()] = true
	}
	for id := range remove {
		if !available[id] {
			n.mu.Unlock()
			return ErrRouteUpdate
		}
	}
	next := r.remote
	next.Routes = cloneRouteState(r.remote.Routes)
	next.Routes.Approvals = slices.DeleteFunc(next.Routes.Approvals, func(a RouteApproval) bool { return len(ids) == 0 || remove[a.CandidateID] })
	n.replaceRemoteLocked(peer, r, next)
	recovery, closed := n.pairingRecovery, n.closed
	n.mu.Unlock()
	stopErr := r.shutdown()
	if recovery {
		return errors.Join(config.ErrAtomicRecovery, stopErr)
	}
	if closed {
		return errors.Join(net.ErrClosed, stopErr)
	}
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return errors.Join(net.ErrClosed, stopErr)
	}
	err := n.persistRouteLocked(next)
	n.mu.Unlock()
	return errors.Join(err, stopErr)
}

func routeSnapshot(remote RemotePeer, now time.Time) RouteSnapshot {
	s := remote.Routes
	out := RouteSnapshot{Legacy: s == nil || s.Received == nil}
	if s == nil {
		return out
	}
	out.IssuedSequence, out.ReceivedSequence = s.IssuedSequence, s.ReceivedSequence
	out.Approvals = slices.Clone(s.Approvals)
	if s.Received == nil {
		return out
	}
	out.Candidates = slices.Clone(s.Received.Candidates)
	out.Expires = s.Received.Expires
	out.Lifetime = s.Received.EffectiveLifetime()
	var ids []string
	if s.Received.active(now) {
		out.NextExpiry = s.Received.Expires
		for _, a := range s.Approvals {
			if a.active(now) {
				ids = append(ids, a.CandidateID)
				if !a.Expires.IsZero() && (out.NextExpiry.IsZero() || a.Expires.Before(out.NextExpiry)) {
					out.NextExpiry = a.Expires
				}
			}
		}
	}
	out.Permitted, _ = PermittedRoutes(*s.Received, ids, now)
	return out
}

func (n *Node) RouteSnapshot(peer string) (RouteSnapshot, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	r := n.clients[peer]
	if r == nil {
		return RouteSnapshot{}, ErrUntrusted
	}
	out := routeSnapshot(r.remote, time.Now().UTC())
	out.RecoveryRequired = n.pairingRecovery
	return out, nil
}
