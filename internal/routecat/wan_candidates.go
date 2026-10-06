// Copyright (c) 2026 sobalink contributors
// SPDX-License-Identifier: MIT

package routecat

import (
	"errors"
	"net/netip"
	"slices"

	"tailscale.com/feature/buildfeatures"
)

// STUNRegionNamespaceSize is the inherent nonzero 16-bit discovery-ID space.
// Persisted metadata is additionally bounded by the configurable LAN-state byte budget.
const STUNRegionNamespaceSize = 1<<16 - 1

// DefaultWANProbeBudget is an adjustable resource default, not a metadata cap.
const DefaultWANProbeBudget = 4

// WANConfig opts one engine into additional WAN address discovery. It is local
// configuration, never learned from a peer capability or default relay map.
// STUN observes an address mapping; it does not authenticate a peer, guarantee a
// direct path, or replace the required certificate-pinned encrypted relay.
// Neither option creates router mappings or requires privileged networking.
type WANConfig struct {
	// STUNEndpoints contains exact numeric UDP destinations. Empty means no
	// STUN discovery. There is no DNS or implicit/default STUN server.
	STUNEndpoints []netip.AddrPort
	// ProbeBudget bounds selected STUN destinations per netcheck run. Zero uses
	// DefaultWANProbeBudget for compatibility. Repeated runs rotate through all
	// metadata; the upstream retry count per selected destination stays bounded.
	ProbeBudget int
	// AdvertiseIPv6 includes available global IPv6 interface candidates with
	// their actual IPv6 socket port. This is a candidate, not a reachability claim.
	AdvertiseIPv6 bool
}

var ErrWANCandidates = errors.New("WAN candidates require distinct numeric UDP STUN endpoints or explicit IPv6 advertisement within the discovery-ID namespace and a finite probe budget")
var ErrWANRestricted = errors.New("WAN candidates are incompatible with private-only or selected LAN destination transport")
var ErrWANBuild = errors.New("WAN candidates require UDP transport and NAT traversal support")

// ValidateWANConfig returns an owned canonical copy without creating sockets.
// A nil configuration preserves existing behavior. An empty non-nil destination
// policy is still restricted, so it cannot silently enable WAN discovery.
func ValidateWANConfig(config *WANConfig, privateOnly bool, destinationPrefixes []netip.Prefix) (*WANConfig, error) {
	if config == nil {
		return nil, nil
	}
	if privateOnly || destinationPrefixes != nil {
		return nil, ErrWANRestricted
	}
	if !buildfeatures.HasUDPTransport || !buildfeatures.HasNATTraversal {
		return nil, ErrWANBuild
	}
	if len(config.STUNEndpoints) > STUNRegionNamespaceSize || config.ProbeBudget < 0 || config.ProbeBudget > STUNRegionNamespaceSize || len(config.STUNEndpoints) == 0 && !config.AdvertiseIPv6 {
		return nil, ErrWANCandidates
	}
	out := &WANConfig{STUNEndpoints: slices.Clone(config.STUNEndpoints), AdvertiseIPv6: config.AdvertiseIPv6, ProbeBudget: config.ProbeBudget}
	if out.ProbeBudget == 0 {
		out.ProbeBudget = DefaultWANProbeBudget
	}
	for _, endpoint := range out.STUNEndpoints {
		ip := endpoint.Addr()
		if !endpoint.IsValid() || endpoint.Port() == 0 || ip.Is4In6() || ip.Zone() != "" || (!ip.IsGlobalUnicast() && !ip.IsLoopback()) {
			return nil, ErrWANCandidates
		}
	}
	slices.SortFunc(out.STUNEndpoints, func(a, b netip.AddrPort) int { return a.Compare(b) })
	for i := 1; i < len(out.STUNEndpoints); i++ {
		if out.STUNEndpoints[i] == out.STUNEndpoints[i-1] {
			return nil, ErrWANCandidates
		}
	}
	return out, nil
}
