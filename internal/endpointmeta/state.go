package endpointmeta

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"time"
)

// Approval is local metadata. A received wire value cannot create one.
type Approval struct {
	Kind           string `json:"kind"`
	ProofDigest    string `json:"proof_digest"`
	Endpoint       string `json:"endpoint"`
	FollowRevision string `json:"follow_revision"`
	Granted        string `json:"granted"`
	Lifetime       string `json:"lifetime"`
	Expires        string `json:"expires"`
}

type FollowApproval struct {
	ScopeDigest string `json:"scope_digest"`
	Revision    string `json:"revision"`
	Granted     string `json:"granted"`
	Lifetime    string `json:"lifetime"`
	Expires     string `json:"expires"`
	Active      bool   `json:"active"`
}

type EndpointState struct {
	Version           int             `json:"version"`
	PairBinding       string          `json:"pair_binding"`
	IssuedVersion     int             `json:"issued_version"`
	IssuedHighwater   string          `json:"issued_highwater"`
	IssuedProof       *Envelope       `json:"issued_proof,omitempty"`
	ReceivedVersion   int             `json:"received_version"`
	ReceivedHighwater string          `json:"received_highwater"`
	ReceivedProof     *Envelope       `json:"received_proof,omitempty"`
	LastEndpoint      string          `json:"last_endpoint"`
	ReceiveStatus     string          `json:"receive_status"`
	AuthorityRevision string          `json:"authority_revision"`
	Approval          *Approval       `json:"approval,omitempty"`
	Follow            *FollowApproval `json:"follow,omitempty"`
}

// UpgradePending records an explicitly reviewed, unchanged peer snapshot. It
// is evidence only; neither its presence nor a prepared context enables use.
type UpgradePending struct {
	ReviewedPeer    PeerWire     `json:"reviewed_peer"`
	PeerRevision    string       `json:"peer_revision"`
	OwnNonce        string       `json:"own_nonce"`
	PrepareDeadline string       `json:"prepare_deadline"`
	Context         *PairContext `json:"context,omitempty"`
}

type PeerRecord struct {
	Peer             PeerWire        `json:"peer"`
	Revision         string          `json:"revision"`
	UpgradePending   *UpgradePending `json:"upgrade_pending,omitempty"`
	PairContext      *PairContext    `json:"pair_context,omitempty"`
	ContextConfirmed bool            `json:"context_confirmed"`
	EndpointState    *EndpointState  `json:"endpoint_state,omitempty"`
}

// Snapshot is an isolated data model, not the production profile/file schema.
// Identity secrets and application grants remain outside this model. No
// application loader or constructor consumes it. Its unused file adapter reads
// evidence only. All revision values are canonical decimal strings.
type Snapshot struct {
	Version               int            `json:"version"`
	Revision              string         `json:"revision"`
	LocalPeer             PeerWire       `json:"local_peer"`
	LocalScope            Scope          `json:"local_scope"`
	PreviousLocalEndpoint string         `json:"previous_local_endpoint"`
	ObservedAt            string         `json:"observed_at"`
	Peers                 []PeerRecord   `json:"peers"`
	PendingChange         *PendingChange `json:"pending_change,omitempty"`
}

type LegacySnapshot struct {
	Version    int
	LocalPeer  PeerWire
	LocalScope Scope
	Peers      []PeerWire
}

// MigrateLegacy converts only a synthetic v2 model. It neither reads an existing
// private file nor creates a context, approval, nonce, or application grant.
func MigrateLegacy(old LegacySnapshot, now time.Time) (Snapshot, error) {
	if old.Version != 2 || now.IsZero() {
		return Snapshot{}, ErrInvalid
	}
	s := Snapshot{Version: 3, Revision: "1", LocalPeer: old.LocalPeer, LocalScope: old.LocalScope, PreviousLocalEndpoint: old.LocalPeer.Endpoint, ObservedAt: now.UTC().Format(time.RFC3339Nano), Peers: make([]PeerRecord, 0, len(old.Peers))}
	for _, p := range old.Peers {
		s.Peers = append(s.Peers, PeerRecord{Peer: p, Revision: "1"})
	}
	if err := s.Validate(); err != nil {
		return Snapshot{}, err
	}
	return cloneSnapshot(s), nil
}

func InitialState(pair PairContext, localKey string) (EndpointState, error) {
	b, err := pair.Binding()
	if err != nil {
		return EndpointState{}, err
	}
	remote, _, err := pairSides(pair, localKey)
	if err != nil {
		return EndpointState{}, err
	}
	return EndpointState{Version: 1, PairBinding: b, IssuedHighwater: "0", ReceivedHighwater: "0", LastEndpoint: remote.Endpoint, ReceiveStatus: "initial", AuthorityRevision: "0"}, nil
}

func pairSides(p PairContext, localKey string) (PeerWire, Scope, error) {
	switch localKey {
	case p.HostKey:
		return PeerWire{Key: p.JoinerKey, Endpoint: p.JoinerEndpoint, TunnelKey: p.JoinerTunnelKey}, p.HostScope, nil
	case p.JoinerKey:
		return PeerWire{Key: p.HostKey, Endpoint: p.HostEndpoint, TunnelKey: p.HostTunnelKey}, p.JoinerScope, nil
	default:
		return PeerWire{}, Scope{}, ErrIdentity
	}
}

func (s EndpointState) Validate(pair PairContext, localKey string) error {
	b, err := pair.Binding()
	if err != nil {
		return err
	}
	remote, localScope, err := pairSides(pair, localKey)
	if err != nil {
		return err
	}
	if s.Version != 1 || s.PairBinding != b || !localScope.Contains(s.LastEndpoint) {
		return ErrInvalid
	}
	ar, err := sequence(s.AuthorityRevision, true)
	if err != nil {
		return err
	}
	for _, direction := range []struct {
		version   int
		highwater string
		proof     *Envelope
		recipient string
	}{{s.IssuedVersion, s.IssuedHighwater, s.IssuedProof, remote.Key}, {s.ReceivedVersion, s.ReceivedHighwater, s.ReceivedProof, localKey}} {
		h, err := sequence(direction.highwater, true)
		if err != nil {
			return err
		}
		if h == 0 {
			if direction.version != 0 || direction.proof != nil {
				return ErrInvalid
			}
			continue
		}
		if direction.version != 1 || direction.proof == nil || direction.proof.Update.Sequence != direction.highwater {
			return ErrInvalid
		}
		if err := Verify(*direction.proof, pair, direction.recipient); err != nil {
			return err
		}
	}
	if s.Follow != nil {
		f := s.Follow
		rev, err := sequence(f.Revision, false)
		expected, _ := localScope.Digest()
		if err != nil || rev > ar || f.ScopeDigest != expected || validity(f.Granted, f.Lifetime, f.Expires) != nil {
			return ErrInvalid
		}
	}
	if s.ReceivedProof == nil {
		if s.ReceiveStatus != "initial" || s.Approval != nil || s.LastEndpoint != remote.Endpoint {
			return ErrInvalid
		}
		return nil
	}
	if ar == 0 {
		return ErrInvalid
	}
	u := s.ReceivedProof.Update
	if u.Operation == "withdraw" {
		if s.ReceiveStatus != "withdrawn" || s.Approval != nil {
			return ErrInvalid
		}
		return nil
	}
	if s.LastEndpoint != u.Endpoint {
		return ErrInvalid
	}
	switch s.ReceiveStatus {
	case "eligible":
		if s.Approval == nil {
			return ErrInvalid
		}
	case "expired", "locally_revoked", "cancelled":
		if s.Approval != nil {
			return ErrInvalid
		}
		return nil
	default:
		return ErrInvalid
	}
	a := s.Approval
	hash, _ := s.ReceivedProof.Digest()
	if ar == 0 || a.ProofDigest != hash || a.Endpoint != u.Endpoint || validity(a.Granted, a.Lifetime, a.Expires) != nil {
		return ErrInvalid
	}
	switch a.Kind {
	case "exact":
		if a.FollowRevision != "" || !approvalWithin(*a, u) {
			return ErrInvalid
		}
	case "follow":
		f := s.Follow
		if f == nil || !f.Active || a.FollowRevision != f.Revision || a.Granted != f.Granted || a.Lifetime != f.Lifetime || a.Expires != f.Expires {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func approvalWithin(a Approval, u UpdateBody) bool {
	g, err := instant(a.Granted)
	if err != nil {
		return false
	}
	issued, _ := instant(u.Issued)
	if g.Before(issued) {
		return false
	}
	if u.Lifetime == "until-revoked" {
		return true
	}
	if a.Lifetime != "finite" {
		return false
	}
	d, err := instant(a.Expires)
	if err != nil {
		return false
	}
	remote, _ := instant(u.Expires)
	return !d.After(remote)
}

func (s Snapshot) Validate() error {
	if s.Version != 3 || s.LocalPeer.validate() != nil || !s.LocalScope.Contains(s.LocalPeer.Endpoint) || !s.LocalScope.Contains(s.PreviousLocalEndpoint) || s.Peers == nil {
		return ErrInvalid
	}
	revision, err := sequence(s.Revision, false)
	if err != nil {
		return err
	}
	if _, err := instant(s.ObservedAt); err != nil {
		return err
	}
	keys := map[string]bool{s.LocalPeer.Key: true}
	tunnels := map[string]bool{s.LocalPeer.TunnelKey: true}
	for _, r := range s.Peers {
		if r.Peer.validate() != nil || keys[r.Peer.Key] || tunnels[r.Peer.TunnelKey] || !s.LocalScope.Contains(r.Peer.Endpoint) {
			return ErrIdentity
		}
		keys[r.Peer.Key] = true
		tunnels[r.Peer.TunnelKey] = true
		rv, err := sequence(r.Revision, false)
		if err != nil || rv > revision {
			return ErrInvalid
		}
		if r.PairContext == nil {
			if r.ContextConfirmed || r.EndpointState != nil {
				return ErrInvalid
			}
		} else {
			p := *r.PairContext
			remote, _, err := pairSides(p, s.LocalPeer.Key)
			if err != nil {
				return err
			}
			localTunnel := p.HostTunnelKey
			if s.LocalPeer.Key == p.JoinerKey {
				localTunnel = p.JoinerTunnelKey
			}
			if p.validate() != nil || remote.Key != r.Peer.Key || remote.TunnelKey != r.Peer.TunnelKey || localTunnel != s.LocalPeer.TunnelKey || r.EndpointState == nil {
				return ErrIdentity
			}
			if err := r.EndpointState.Validate(p, s.LocalPeer.Key); err != nil {
				return err
			}
			if r.Peer.Endpoint != r.EndpointState.LastEndpoint {
				return ErrInvalid
			}
		}
		if u := r.UpgradePending; u != nil {
			if u.ReviewedPeer != r.Peer || u.PeerRevision != r.Revision {
				return ErrInvalid
			}
			if _, err := rawBytes(u.OwnNonce, 32); err != nil {
				return err
			}
			if _, err := instant(u.PrepareDeadline); err != nil {
				return err
			}
			if u.Context != nil {
				p := *u.Context
				remote, scope, err := pairSides(p, s.LocalPeer.Key)
				if err != nil || p.validate() != nil || p.HostKey >= p.JoinerKey || remote.Key != r.Peer.Key || remote.Endpoint != r.Peer.Endpoint || remote.TunnelKey != r.Peer.TunnelKey {
					return ErrIdentity
				}
				nonce, localEndpoint, localTunnel := p.HostNonce, p.HostEndpoint, p.HostTunnelKey
				if s.LocalPeer.Key == p.JoinerKey {
					nonce, localEndpoint, localTunnel = p.JoinerNonce, p.JoinerEndpoint, p.JoinerTunnelKey
				}
				a, _ := Encode(scope)
				b, _ := Encode(s.LocalScope)
				if nonce != u.OwnNonce || localEndpoint != s.LocalPeer.Endpoint || localTunnel != s.LocalPeer.TunnelKey || !bytes.Equal(a, b) {
					return ErrIdentity
				}
				if r.PairContext != nil {
					a, _ := Encode(*r.PairContext)
					b, _ := Encode(p)
					if !bytes.Equal(a, b) {
						return ErrIdentity
					}
				}
			}
		}
	}
	if s.PendingChange != nil {
		return s.validatePending()
	}
	return nil
}

// ValidateAt detects future saved claims and observed wall-clock regression.
// Expired proofs remain valid evidence; this function never refreshes a deadline.
func (s Snapshot) ValidateAt(now time.Time) error {
	if err := s.Validate(); err != nil {
		return err
	}
	observed, _ := instant(s.ObservedAt)
	if now.IsZero() || now.Before(observed) {
		return ErrReview
	}
	if s.PendingChange != nil {
		fenced, _ := instant(s.PendingChange.FencedAt)
		if now.Before(fenced) {
			return ErrReview
		}
	}
	states := make([]*EndpointState, 0, len(s.Peers)+1)
	for _, r := range s.Peers {
		states = append(states, r.EndpointState)
	}
	if s.PendingChange != nil {
		states = append(states, s.PendingChange.Mutation.State)
	}
	for _, e := range states {
		if e == nil {
			continue
		}
		for _, p := range []*Envelope{e.IssuedProof, e.ReceivedProof} {
			if p != nil {
				issued, _ := instant(p.Update.Issued)
				if now.Before(issued) {
					return ErrReview
				}
			}
		}
		if e.Follow != nil {
			g, _ := instant(e.Follow.Granted)
			if now.Before(g) {
				return ErrReview
			}
		}
		if e.Approval != nil {
			g, _ := instant(e.Approval.Granted)
			if now.Before(g) {
				return ErrReview
			}
		}
	}
	return nil
}

func cloneSnapshot(s Snapshot) Snapshot {
	b, _ := json.Marshal(s)
	var out Snapshot
	json.Unmarshal(b, &out)
	return out
}
func cloneState(s EndpointState) EndpointState {
	b, _ := json.Marshal(s)
	var out EndpointState
	json.Unmarshal(b, &out)
	return out
}

func nextCounter(s string) (string, error) {
	n, err := sequence(s, true)
	if err != nil {
		return "", err
	}
	if n == math.MaxUint64 {
		return "", ErrCapacity
	}
	return strconv.FormatUint(n+1, 10), nil
}

// EncodeSnapshot applies the caller's existing protected-store byte budget.
func EncodeSnapshot(s Snapshot, budget int) ([]byte, error) {
	w := wireSizer{left: budget}
	if budget <= 0 || !s.measure(&w) {
		return nil, ErrCapacity
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, ErrInvalid
	}
	if budget <= 0 || len(b) > budget {
		return nil, ErrCapacity
	}
	return b, nil
}

func ParseSnapshot(b []byte, budget int) (Snapshot, error) {
	var s Snapshot
	if budget <= 0 || len(b) > budget {
		return s, ErrCapacity
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil {
		return Snapshot{}, ErrInvalid
	}
	canonical, err := EncodeSnapshot(s, budget)
	if err != nil {
		return Snapshot{}, err
	}
	if !bytes.Equal(b, canonical) {
		return Snapshot{}, ErrInvalid
	}
	return s, nil
}
