package lanlink

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Route updates describe reachability, never application or relay-use permission.
// They are sealed to an existing pair and must be committed by the caller before
// activation. This contract performs no network I/O or persistence.
const (
	RouteUpdateVersion     = 1
	MaxRouteCandidates     = 4
	MaxRouteUpdateLifetime = 30 * 24 * time.Hour
	routeUpdateDomain      = "sobalink paired route update v1"
)

var ErrRouteUpdate = errors.New("route update is invalid, stale, expired or belongs to another pairing")

// RouteCandidate is one exact relay destination. Local describes the candidate
// address only; a separate enforced transport policy is needed for LAN-only
// egress. A certificate change creates a different candidate identity.
type RouteCandidate struct {
	Relay TrustedRelay `json:"relay"`
	Scope string       `json:"scope"`
}

func (c RouteCandidate) Validate() error {
	if c.Relay.Validate() != nil {
		return ErrRouteUpdate
	}
	switch c.Scope {
	case "local":
		if a := c.Relay.Address.Addr(); !a.IsPrivate() && !a.IsLoopback() {
			return ErrRouteUpdate
		}
	case "external":
	default:
		return ErrRouteUpdate
	}
	return nil
}

func (c RouteCandidate) ID() string {
	return digest([]byte("sobalink route candidate v1\x00" + c.Scope + "\x00" + c.Relay.Address.String() + "\x00" + c.Relay.CertificateSHA256))
}

type RouteUpdate struct {
	Version     int              `json:"version"`
	Domain      string           `json:"domain"`
	Issuer      string           `json:"issuer"`
	Recipient   string           `json:"recipient"`
	PairBinding string           `json:"pair_binding"`
	Sequence    uint64           `json:"sequence"`
	Issued      time.Time        `json:"issued"`
	Expires     time.Time        `json:"expires"`
	Candidates  []RouteCandidate `json:"candidates"`
}

// PairRouteBinding changes after re-pairing even if both device keys survive.
// Role public keys are authenticated by the original pairing transcript.
func PairRouteBinding(identity Identity, remote RemotePeer) (string, error) {
	if identity.Validate() != nil || !validPeer(remote.Peer) || remote.ClientPrivate.IsZero() || !validKey(remote.IncomingClientKey) {
		return "", ErrRouteUpdate
	}
	devices := []string{identity.PublicKey(), remote.Peer.Key}
	roles := []string{keyString(remote.ClientPrivate.Public()), remote.IncomingClientKey}
	used := map[string]bool{}
	for _, k := range append(slices.Clone(devices), roles...) {
		if used[k] {
			return "", ErrRouteUpdate
		}
		used[k] = true
	}
	bindings := []string{devices[0] + "=" + roles[0], devices[1] + "=" + roles[1]}
	slices.Sort(bindings)
	return digest([]byte("sobalink paired routes v1\x00" + strings.Join(bindings, "\x00"))), nil
}

func validateRouteUpdate(update RouteUpdate, issuer, recipient, binding string, now time.Time) error {
	if !validKey(issuer) || !validKey(recipient) || issuer == recipient || !validKey(binding) || update.Version != RouteUpdateVersion || update.Domain != routeUpdateDomain || update.Issuer != issuer || update.Recipient != recipient || update.PairBinding != binding || update.Sequence == 0 {
		return ErrRouteUpdate
	}
	// Reject future claims rather than increasing the lifetime after a clock jump.
	if update.Issued.IsZero() || update.Issued.After(now) || !update.Expires.After(now) || !update.Expires.After(update.Issued) || update.Expires.Sub(update.Issued) > MaxRouteUpdateLifetime {
		return ErrRouteUpdate
	}
	if len(update.Candidates) > MaxRouteCandidates {
		return ErrRouteUpdate
	}
	seen := map[string]bool{}
	endpoints := map[string]bool{}
	for _, candidate := range update.Candidates {
		if candidate.Validate() != nil || seen[candidate.ID()] || endpoints[candidate.Relay.Address.String()] {
			return ErrRouteUpdate
		}
		seen[candidate.ID()] = true
		endpoints[candidate.Relay.Address.String()] = true
	}
	return nil
}

// SealRouteUpdate requires the next monotonic sequence chosen by the caller's
// atomic store. An empty candidate set is an authenticated route withdrawal.
func SealRouteUpdate(identity Identity, remote RemotePeer, sequence uint64, candidates []RouteCandidate, issued, expires time.Time) ([]byte, error) {
	binding, err := PairRouteBinding(identity, remote)
	if err != nil {
		return nil, err
	}
	update := RouteUpdate{RouteUpdateVersion, routeUpdateDomain, identity.PublicKey(), remote.Peer.Key, binding, sequence, issued.UTC(), expires.UTC(), slices.Clone(candidates)}
	if err := validateRouteUpdate(update, identity.PublicKey(), remote.Peer.Key, binding, issued); err != nil {
		return nil, err
	}
	sealed, _, err := sealMessage(identity, remote.Peer.Key, update)
	return sealed, err
}

// OpenRouteUpdate verifies proof and freshness only. It does not authorize any
// newly offered destination, generate keys or contact a relay. The caller must
// obtain highestSequence from durable state, including withdrawn/expired routes.
func OpenRouteUpdate(identity Identity, remote RemotePeer, raw []byte, highestSequence uint64, now time.Time) (RouteUpdate, error) {
	binding, err := PairRouteBinding(identity, remote)
	if err != nil {
		return RouteUpdate{}, err
	}
	var update RouteUpdate
	plain, err := openMessage(identity, raw, remote.Peer.Key, &update)
	if err != nil {
		return RouteUpdate{}, ErrRouteUpdate
	}
	// The route protocol accepts only the exact canonical JSON emitted by the
	// producer, rejecting duplicate keys, case aliases and ambiguous encodings.
	canonical, err := json.Marshal(update)
	if err != nil || !bytes.Equal(plain, canonical) {
		return RouteUpdate{}, ErrRouteUpdate
	}
	if update.Sequence <= highestSequence || validateRouteUpdate(update, remote.Peer.Key, identity.PublicKey(), binding, now) != nil {
		return RouteUpdate{}, ErrRouteUpdate
	}
	return update, nil
}

// PermittedRoutes intersects a verified current offer with locally approved
// exact candidate identities. Authentication alone never adds permission. The
// local list must be stored separately from remotely controlled update content.
func PermittedRoutes(update RouteUpdate, approvedIDs []string, now time.Time) ([]RouteCandidate, error) {
	if len(approvedIDs) > MaxRouteCandidates || len(update.Candidates) > MaxRouteCandidates {
		return nil, ErrRouteUpdate
	}
	if !update.Expires.After(now) {
		return nil, nil
	}
	approved := make(map[string]bool, len(approvedIDs))
	for _, id := range approvedIDs {
		raw, err := hex.DecodeString(id)
		if err != nil || len(raw) != 32 || id != strings.ToLower(id) || approved[id] {
			return nil, ErrRouteUpdate
		}
		approved[id] = true
	}
	var result []RouteCandidate
	for _, candidate := range update.Candidates {
		if err := candidate.Validate(); err != nil {
			return nil, err
		}
		if approved[candidate.ID()] {
			result = append(result, candidate)
		}
	}
	// Stable LAN-first preference is deterministic; health/hysteresis belongs to
	// the transport coordinator and must not mutate permission or route identity.
	slices.SortStableFunc(result, func(a, b RouteCandidate) int {
		if a.Scope != b.Scope {
			if a.Scope == "local" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID(), b.ID())
	})
	return result, nil
}

func (u RouteUpdate) Summary() string {
	// Deliberately omit endpoint, peer key, capability and sealed bytes.
	return fmt.Sprintf("route update %d: %d candidates", u.Sequence, len(u.Candidates))
}
