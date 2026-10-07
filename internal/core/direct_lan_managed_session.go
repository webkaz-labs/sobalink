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
	state := s.state
	if state.Version == directLANStateVersion && state.Metadata == nil {
		// endpointModelLocked intentionally accepts only v3. A genuine v2 file
		// has no contexts to project, but still needs current-file, clock and
		// recovery checks; do not migrate or manufacture metadata for it.
		if err := s.endpointFileCurrentLocked(); err != nil {
			return nil, err
		}
		if err := s.observeEndpointTimeLocked(now); err != nil {
			return nil, err
		}
		if s.recovery {
			return nil, directlan.ErrRecovery
		}
	} else {
		m, err := s.endpointModelLocked(now, false)
		if err != nil {
			return nil, err
		}
		state.Metadata = m
	}
	return projectManagedFixedEndpointContexts(state)
}

// projectManagedFixedEndpointContexts is structural validation only. Its result
// is copied data, never evidence of publication, peer authentication or grants.
// Unsupported managed records reject the entire projection, not just one peer.
func projectManagedFixedEndpointContexts(state directLANState) (map[string]endpointmeta.PairContext, error) {
	if state.Version != directLANStateVersion && state.Version != directLANMetadataStateVersion {
		return nil, directLANMetadataUnavailable()
	}
	// This also validates directLANConfig and the exact identity, selected
	// scope and Peer DTO projection against the single metadata model.
	if err := validateDirectLANState(state); err != nil {
		return nil, err
	}
	contexts := make(map[string]endpointmeta.PairContext)
	m := state.Metadata
	if m == nil { // validateDirectLANState permits only genuine legacy v2 here.
		return contexts, nil
	}
	if m.PendingChange != nil {
		return nil, directlan.ErrRecovery
	}
	if m.PreviousLocalEndpoint != m.LocalPeer.Endpoint {
		return nil, directLANMetadataUnavailable()
	}
	for _, record := range m.Peers {
		if record.PairContext == nil && record.UpgradePending == nil && record.EndpointState == nil && !record.ContextConfirmed {
			continue // Genuine legacy record; no managed classification to erase.
		}
		if record.PairContext == nil || !record.ContextConfirmed || record.UpgradePending != nil || record.EndpointState == nil {
			return nil, directLANMetadataUnavailable()
		}
		pair := *record.PairContext
		localKey, localTunnel, localEndpoint, localScope := pair.HostKey, pair.HostTunnelKey, pair.HostEndpoint, pair.HostScope
		remoteKey, remoteTunnel, remoteEndpoint := pair.JoinerKey, pair.JoinerTunnelKey, pair.JoinerEndpoint
		if m.LocalPeer.Key == pair.JoinerKey {
			localKey, localTunnel, localEndpoint, localScope = pair.JoinerKey, pair.JoinerTunnelKey, pair.JoinerEndpoint, pair.JoinerScope
			remoteKey, remoteTunnel, remoteEndpoint = pair.HostKey, pair.HostTunnelKey, pair.HostEndpoint
		}
		if m.LocalPeer.Key != localKey || m.LocalPeer.TunnelKey != localTunnel || record.Peer.Key != remoteKey || record.Peer.TunnelKey != remoteTunnel {
			return nil, endpointmeta.ErrIdentity
		}
		if m.LocalPeer.Endpoint != localEndpoint || record.Peer.Endpoint != remoteEndpoint || !reflect.DeepEqual(m.LocalScope, localScope) {
			return nil, endpointmeta.ErrPolicy
		}
		// InitialState validates the canonical binding through PairContext.Binding.
		// Full equality excludes every proof, high-water, approval, follow and
		// reduction field, even when the saved numeric endpoint has not moved.
		initial, err := endpointmeta.InitialState(pair, m.LocalPeer.Key)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(*record.EndpointState, initial) {
			return nil, directLANMetadataUnavailable()
		}
		pair.HostScope.Prefixes = append([]string(nil), pair.HostScope.Prefixes...)
		pair.JoinerScope.Prefixes = append([]string(nil), pair.JoinerScope.Prefixes...)
		contexts[record.Peer.Key] = pair
	}
	return contexts, nil
}
