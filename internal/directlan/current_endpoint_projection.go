package directlan

import (
	"crypto/sha256"
	"encoding/json"
	"net/netip"
	"reflect"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// currentEndpointAuthority is copied constructor/generation input, never a
// publication receipt. Only ProjectCurrentEndpointConfig can derive it from a
// complete validated snapshot. No caller-set boolean relaxes pair validation.
// NewNode rejects this input. Only the Core transaction seam supplies
// the receipt, original deadlines and exact replacement ownership.
type currentEndpointAuthority struct {
	policy                [32]byte
	observed              time.Time
	localEndpoint         string
	previousLocalEndpoint string
	peers                 map[string]currentEndpointPeer
	inactive              []string
}

// The deadline below is an inert wall-derived observation, not a runtime
// deadline grant. A future lifecycle owner must carry forward the store-owned
// first monotonic cutoff; reprojecting must never reset that cutoff.
type currentEndpointPeer struct {
	binding           string
	endpoint          string
	highwater         string
	authorityRevision string
	proofDigest       string
	deadline          time.Time
}

// ProjectCurrentEndpointConfig produces inert configuration data from the full
// retained peer list. It performs no I/O, publication, receipt reconstruction,
// session authentication or activation. The caller must independently observe
// the protected file, recovery state and process deadlines before using it.
// A retained local move uses the same immutable pair identities and scopes. The
// copied current/previous endpoints describe that model; they certify neither
// its publication nor a completed retirement of the previous local listener.
func ProjectCurrentEndpointConfig(base Config, model endpointmeta.Snapshot, now time.Time) (Config, error) {
	if err := base.Validate(); err != nil {
		return Config{}, err
	}
	if base.currentEndpoints != nil || len(base.PairContexts) != 0 || len(base.DeniedPeerKeys) != 0 {
		return Config{}, ErrIdentity
	}
	if err := model.ValidateAt(now); err != nil {
		return Config{}, err
	}
	if model.PendingChange != nil {
		return Config{}, ErrRecovery
	}
	if model.LocalPeer.Key != base.Identity.PublicKey() || model.LocalPeer.TunnelKey != base.Identity.TunnelKey() || model.LocalPeer.Endpoint != base.Listen.String() || len(base.Peers) != len(model.Peers) {
		return Config{}, ErrIdentity
	}
	scope, err := configEndpointScope(base)
	if err != nil || !reflect.DeepEqual(scope, model.LocalScope) {
		return Config{}, ErrPolicy
	}
	cfg := cloneGenerationConfig(base)
	cfg.Peers = make([]Peer, 0, len(base.Peers))
	cfg.PairContexts = make(map[string]endpointmeta.PairContext)
	authority := &currentEndpointAuthority{observed: now, localEndpoint: model.LocalPeer.Endpoint, previousLocalEndpoint: model.PreviousLocalEndpoint, peers: make(map[string]currentEndpointPeer)}
	for i, record := range model.Peers {
		peer := base.Peers[i]
		if record.Peer.Key != peer.Key || record.Peer.Name != peer.Name || record.Peer.TunnelKey != peer.TunnelKey || record.Peer.Endpoint != peer.Endpoint.String() {
			return Config{}, ErrIdentity
		}
		if record.PairRevocation != nil {
			cfg.DeniedPeerKeys = append(cfg.DeniedPeerKeys, peer.Key)
			continue
		}
		if record.PairContext == nil && record.UpgradePending == nil && record.EndpointState == nil && !record.ContextConfirmed {
			cfg.Peers = append(cfg.Peers, peer)
			continue
		}
		if record.PairContext == nil || !record.ContextConfirmed || record.UpgradePending != nil || record.EndpointState == nil {
			return Config{}, ErrUnavailable
		}
		pair, state := *record.PairContext, record.EndpointState
		localKey, localTunnel, localScope := pair.HostKey, pair.HostTunnelKey, pair.HostScope
		remoteKey, remoteTunnel, remoteEndpoint := pair.JoinerKey, pair.JoinerTunnelKey, pair.JoinerEndpoint
		if model.LocalPeer.Key == pair.JoinerKey {
			localKey, localTunnel, localScope = pair.JoinerKey, pair.JoinerTunnelKey, pair.JoinerScope
			remoteKey, remoteTunnel, remoteEndpoint = pair.HostKey, pair.HostTunnelKey, pair.HostEndpoint
		}
		if localKey != model.LocalPeer.Key || localTunnel != model.LocalPeer.TunnelKey || remoteKey != peer.Key || remoteTunnel != peer.TunnelKey {
			return Config{}, ErrIdentity
		}
		if !reflect.DeepEqual(localScope, model.LocalScope) || !pair.HostScope.Contains(model.LocalPeer.Endpoint) || !pair.JoinerScope.Contains(model.LocalPeer.Endpoint) || !pair.HostScope.Contains(model.PreviousLocalEndpoint) || !pair.JoinerScope.Contains(model.PreviousLocalEndpoint) {
			return Config{}, ErrPolicy
		}
		projected := currentEndpointPeer{binding: state.PairBinding, endpoint: state.LastEndpoint, highwater: state.ReceivedHighwater, authorityRevision: state.AuthorityRevision}
		if state.ReceivedProof == nil {
			// Initial remote authority survives unrelated follow/issued history, but
			// can never substitute a different address for the original pair endpoint.
			if state.ReceiveStatus != "initial" || peer.Endpoint.String() != remoteEndpoint {
				return Config{}, ErrIdentity
			}
		} else {
			if !endpointmeta.SavedEligibility(model, state.PairBinding, now) {
				authority.inactive = append(authority.inactive, peer.Key)
				continue
			}
			proof := state.ReceivedProof
			if proof.Update.Operation != "set" || proof.Update.Sequence != state.ReceivedHighwater || proof.Update.Endpoint != peer.Endpoint.String() {
				return Config{}, ErrIdentity
			}
			projected.proofDigest, err = proof.Digest()
			if err != nil {
				return Config{}, err
			}
			for _, bound := range []struct{ lifetime, expires string }{{proof.Update.Lifetime, proof.Update.Expires}, {state.Approval.Lifetime, state.Approval.Expires}} {
				if bound.lifetime == "finite" {
					absolute, err := time.Parse(time.RFC3339Nano, bound.expires)
					if err != nil {
						return Config{}, err
					}
					deadline := now.Add(absolute.Sub(now.UTC()))
					if projected.deadline.IsZero() || deadline.Before(projected.deadline) {
						projected.deadline = deadline
					}
				}
			}
		}
		cfg.Peers = append(cfg.Peers, peer)
		cfg.PairContexts[peer.Key] = pair
		authority.peers[peer.Key] = projected
	}
	if len(cfg.PairContexts) == 0 && len(cfg.DeniedPeerKeys) == 0 && len(authority.inactive) == 0 && model.PreviousLocalEndpoint == model.LocalPeer.Endpoint {
		return cloneGenerationConfig(base), nil
	}
	cfg.PairContexts = clonePairContexts(cfg.PairContexts)
	cfg.currentEndpoints = authority
	authority.policy = currentEndpointPolicy(cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// InactiveEndpointPeerKeys identifies retained, nonterminal managed records that
// have no current endpoint authority. A later complete, approved projection may
// restore them; they are not unknown legacy peers or permanent pair denials.
func (c Config) InactiveEndpointPeerKeys() []string {
	if c.currentEndpoints == nil {
		return nil
	}
	return append([]string(nil), c.currentEndpoints.inactive...)
}

func cloneCurrentEndpointAuthority(input *currentEndpointAuthority) *currentEndpointAuthority {
	if input == nil {
		return nil
	}
	out := *input
	out.inactive = append([]string(nil), input.inactive...)
	out.peers = make(map[string]currentEndpointPeer, len(input.peers))
	for key, peer := range input.peers {
		out.peers[key] = peer
	}
	return &out
}

func currentEndpointPolicy(c Config) [32]byte {
	// Bind all projected membership/identity/endpoint/scope data. Resource limits
	// and ownership callbacks remain independently supplied by the future owner.
	localEndpoint, previousLocalEndpoint := "", ""
	if c.currentEndpoints != nil {
		localEndpoint, previousLocalEndpoint = c.currentEndpoints.localEndpoint, c.currentEndpoints.previousLocalEndpoint
	}
	data, _ := json.Marshal(struct {
		LocalKey, LocalTunnel                string
		LocalEndpoint, PreviousLocalEndpoint string
		Listen                               netip.AddrPort
		Prefixes                             []netip.Prefix
		Peers                                []Peer
		Pairs                                map[string]endpointmeta.PairContext
		Denied                               []string
	}{c.Identity.PublicKey(), c.Identity.TunnelKey(), localEndpoint, previousLocalEndpoint, c.Listen, c.AllowedPrefixes, c.Peers, c.PairContexts, c.DeniedPeerKeys})
	return sha256.Sum256(data)
}
