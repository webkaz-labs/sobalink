package core

import (
	"reflect"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// managedFixedEndpointContextsLocked observes the current protected store and
// projects only confirmed, unchanged initial contexts. The caller must hold
// Core.op, the exclusive profile lifecycle and store.mu. This creates no runtime
// authority and performs no durable/model writes. The existing observation
// caches and recovery latch still advance through the store-owned checks.
func (s *directLANStore) managedFixedEndpointContextsLocked(now time.Time) (map[string]endpointmeta.PairContext, error) {
	projection, err := s.managedFixedEndpointProjectionLocked(now)
	if err != nil {
		return nil, err
	}
	return projection.PairContexts, nil
}

func (s *directLANStore) managedFixedEndpointProjectionLocked(now time.Time) (managedFixedEndpointProjection, error) {
	state := s.state
	if state.Version == directLANStateVersion && state.Metadata == nil {
		// endpointModelLocked intentionally accepts only v3/v4. A genuine v2 file
		// has no contexts to project, but still needs current-file, clock and
		// recovery checks; do not migrate or manufacture metadata for it.
		if err := s.endpointFileCurrentLocked(); err != nil {
			return managedFixedEndpointProjection{}, err
		}
		if err := s.observeEndpointTimeLocked(now); err != nil {
			return managedFixedEndpointProjection{}, err
		}
		if s.recovery {
			return managedFixedEndpointProjection{}, directlan.ErrRecovery
		}
	} else {
		m, err := s.endpointModelLocked(now, false)
		if err != nil {
			return managedFixedEndpointProjection{}, err
		}
		state.Metadata = m
	}
	return projectManagedFixedEndpoint(state)
}

// projectManagedFixedEndpointContexts is structural validation only. Its result
// is copied data, never evidence of publication, peer authentication or grants.
// Unsupported managed records reject the entire projection, not just one peer.
type managedFixedEndpointProjection struct {
	Peers          []directlan.Peer
	PairContexts   map[string]endpointmeta.PairContext
	DeniedPeerKeys []string
}

// projectActiveDirectLANPeers keeps every evidence DTO in the store and derives
// transport membership and negative identity classification in one observation.
func projectActiveDirectLANPeers(state directLANState) ([]directlan.Peer, []string, error) {
	if err := validateDirectLANState(state); err != nil {
		return nil, nil, err
	}
	peers := make([]directlan.Peer, 0, len(state.Peers))
	denied := make([]string, 0)
	for i, peer := range state.Peers {
		if state.Metadata != nil && state.Metadata.Peers[i].PairRevocation != nil {
			denied = append(denied, peer.Key)
		} else {
			peers = append(peers, peer)
		}
	}
	return peers, denied, nil
}

func projectManagedFixedEndpointContexts(state directLANState) (map[string]endpointmeta.PairContext, error) {
	projection, err := projectManagedFixedEndpoint(state)
	if err != nil {
		return nil, err
	}
	return projection.PairContexts, nil
}

func projectManagedFixedEndpoint(state directLANState) (managedFixedEndpointProjection, error) {
	if state.Version != directLANStateVersion && state.Version != directLANMetadataStateVersion && state.Version != directLANPairRecordStateVersion {
		return managedFixedEndpointProjection{}, directLANMetadataUnavailable()
	}
	// This also validates directLANConfig and the exact identity, selected
	// scope and Peer DTO projection against the single metadata model.
	if err := validateDirectLANState(state); err != nil {
		return managedFixedEndpointProjection{}, err
	}
	peers, denied, err := projectActiveDirectLANPeers(state)
	if err != nil {
		return managedFixedEndpointProjection{}, err
	}
	contexts := make(map[string]endpointmeta.PairContext)
	projection := managedFixedEndpointProjection{Peers: peers, PairContexts: contexts, DeniedPeerKeys: denied}
	m := state.Metadata
	if m == nil { // validateDirectLANState permits only genuine legacy v2 here.
		return projection, nil
	}
	if m.PendingChange != nil {
		return managedFixedEndpointProjection{}, directlan.ErrRecovery
	}
	if m.PreviousLocalEndpoint != m.LocalPeer.Endpoint {
		return managedFixedEndpointProjection{}, directLANMetadataUnavailable()
	}
	for _, record := range m.Peers {
		if record.PairRevocation != nil {
			continue // Retained evidence is never reinterpreted as active authority.
		}
		if record.PairContext == nil && record.UpgradePending == nil && record.EndpointState == nil && !record.ContextConfirmed {
			continue // Genuine legacy record; no managed classification to erase.
		}
		if record.PairContext == nil || !record.ContextConfirmed || record.UpgradePending != nil || record.EndpointState == nil {
			return managedFixedEndpointProjection{}, directLANMetadataUnavailable()
		}
		pair := *record.PairContext
		localKey, localTunnel, localEndpoint, localScope := pair.HostKey, pair.HostTunnelKey, pair.HostEndpoint, pair.HostScope
		remoteKey, remoteTunnel, remoteEndpoint := pair.JoinerKey, pair.JoinerTunnelKey, pair.JoinerEndpoint
		if m.LocalPeer.Key == pair.JoinerKey {
			localKey, localTunnel, localEndpoint, localScope = pair.JoinerKey, pair.JoinerTunnelKey, pair.JoinerEndpoint, pair.JoinerScope
			remoteKey, remoteTunnel, remoteEndpoint = pair.HostKey, pair.HostTunnelKey, pair.HostEndpoint
		}
		if m.LocalPeer.Key != localKey || m.LocalPeer.TunnelKey != localTunnel || record.Peer.Key != remoteKey || record.Peer.TunnelKey != remoteTunnel {
			return managedFixedEndpointProjection{}, endpointmeta.ErrIdentity
		}
		if m.LocalPeer.Endpoint != localEndpoint || record.Peer.Endpoint != remoteEndpoint || !reflect.DeepEqual(m.LocalScope, localScope) {
			return managedFixedEndpointProjection{}, endpointmeta.ErrPolicy
		}
		// InitialState validates the canonical binding through PairContext.Binding.
		// Full equality excludes every proof, high-water, approval, follow and
		// reduction field, even when the saved numeric endpoint has not moved.
		initial, err := endpointmeta.InitialState(pair, m.LocalPeer.Key)
		if err != nil {
			return managedFixedEndpointProjection{}, err
		}
		if !reflect.DeepEqual(*record.EndpointState, initial) {
			return managedFixedEndpointProjection{}, directLANMetadataUnavailable()
		}
		pair.HostScope.Prefixes = append([]string(nil), pair.HostScope.Prefixes...)
		pair.JoinerScope.Prefixes = append([]string(nil), pair.JoinerScope.Prefixes...)
		contexts[record.Peer.Key] = pair
	}
	return projection, nil
}
