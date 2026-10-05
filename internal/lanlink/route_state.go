package lanlink

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"slices"
	"sort"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

const RouteStateVersion = 1

var ErrRoutePermission = errors.New("no locally approved current route")

// RouteApproval is an independent, finite local permission for one exact
// endpoint, certificate and scope. A remote update can never extend it.
type RouteApproval struct {
	CandidateID string    `json:"candidate_id"`
	Granted     time.Time `json:"granted"`
	Expires     time.Time `json:"expires"`
}

// RouteState is protected pairing state. ReceivedSequence and its authenticated
// proof survive expiry and withdrawal; deleting either would permit rollback.
// Nil state is the legacy singleton route. Issuing an update alone does not
// convert the local outgoing route into managed mode.
type RouteState struct {
	Version          int             `json:"version"`
	PairBinding      string          `json:"pair_binding"`
	IssuedSequence   uint64          `json:"issued_sequence"`
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
		return cloneRouteState(remote.Routes), nil
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
	if err != nil || s.Version != RouteStateVersion || s.PairBinding != binding || len(s.Approvals) > MaxRouteCandidates {
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
		if !candidates[approval.CandidateID] || seen[approval.CandidateID] || approval.Granted.Before(verified.Issued) || approval.Granted.IsZero() || !approval.Expires.After(approval.Granted) || approval.Expires.After(verified.Expires) || approval.Expires.Sub(approval.Granted) > MaxRouteUpdateLifetime {
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
		if errors.Is(err, config.ErrAtomicCommitted) {
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
	if err != nil || s.IssuedSequence == math.MaxUint64 {
		return nil, ErrRouteUpdate
	}
	frame, err := SealRouteUpdate(n.cfg.Identity, r.remote, s.IssuedSequence+1, candidates, now, expires)
	if err != nil {
		return nil, err
	}
	s.IssuedSequence++
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
	if len(raw) == 0 || len(raw) > maxPairMessage || !validKey(reviewedDigest) || reviewedDigest != routeReviewDigest(raw) || len(selectedIDs) > MaxRouteCandidates {
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
	if err != nil {
		n.mu.Unlock()
		return err
	}
	approvals, err := selectedApprovals(u, selectedIDs, approvalExpiry, now)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	s.ReceivedSequence, s.Received, s.ReceivedProof, s.Approvals = u.Sequence, &u, slices.Clone(raw), approvals
	next := r.remote
	next.Routes = s
	if err := n.persistRouteLocked(next); err != nil {
		n.mu.Unlock()
		return err
	}
	n.clients[peer] = &remoteClient{remote: next, address: r.address}
	n.mu.Unlock()
	return r.shutdown()
}

func selectedApprovals(u RouteUpdate, ids []string, expiry, now time.Time) ([]RouteApproval, error) {
	if len(ids) > MaxRouteCandidates || len(ids) > 0 && (expiry.IsZero() || !expiry.After(now) || expiry.Sub(now) > MaxRouteUpdateLifetime || expiry.After(u.Expires)) {
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
		approvals = append(approvals, RouteApproval{id, now.UTC(), expiry.UTC()})
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
	approvals, err := selectedApprovals(review.Update, selectedIDs, expiry, now)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	next := r.remote
	next.Routes = cloneRouteState(r.remote.Routes)
	next.Routes.Approvals = approvals
	if err := n.persistRouteLocked(next); err != nil {
		n.mu.Unlock()
		return err
	}
	n.clients[peer] = &remoteClient{remote: next, address: r.address}
	n.mu.Unlock()
	return r.shutdown()
}

// RevokeRoutes removes exact local permissions without changing application
// trust or role keys. Empty IDs revoke all managed routes. Local outgoing work
// is stopped before persistence, even when the save fails. A failed save is
// surfaced and the reduced in-memory authority stays in force for a retry.
func (n *Node) RevokeRoutes(peer string, ids []string) error {
	if len(ids) > MaxRouteCandidates {
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
	n.clients[peer] = &remoteClient{remote: next, address: r.address}
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
	var ids []string
	if s.Received.Expires.After(now) {
		out.NextExpiry = s.Received.Expires
		for _, a := range s.Approvals {
			if !a.Granted.After(now) && a.Expires.After(now) {
				ids = append(ids, a.CandidateID)
				if a.Expires.Before(out.NextExpiry) {
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
