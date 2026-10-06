package routecat

import (
	"net/netip"
	"slices"

	"tailscale.com/net/underlayguard"
	"tailscale.com/tailcfg"
)

func newUnderlayPolicy(prefixes []netip.Prefix, regions []*tailcfg.DERPRegion) (*underlayguard.Policy, error) {
	if prefixes == nil {
		return nil, nil
	}
	relays := make([]netip.AddrPort, 0, len(regions))
	// Regions were canonicalized before this function. Reparse defensively so a
	// future caller cannot convert an invalid relay into an unrestricted dial.
	for _, region := range regions {
		if region == nil || len(region.Nodes) != 1 || region.Nodes[0] == nil {
			return nil, ErrExplicitRegions
		}
		node := region.Nodes[0]
		address, err := netip.ParseAddr(node.HostName)
		if err != nil || node.DERPPort < 1 || node.DERPPort > 65535 {
			return nil, ErrExplicitRegions
		}
		relays = append(relays, netip.AddrPortFrom(address, uint16(node.DERPPort)))
	}
	return underlayguard.New(underlayguard.Config{Prefixes: slices.Clone(prefixes), Relays: relays})
}

func selectedEndpoints(endpoints []netip.AddrPort, prefixes []netip.Prefix) []netip.AddrPort {
	if prefixes == nil {
		return slices.Clone(endpoints)
	}
	out := make([]netip.AddrPort, 0, len(endpoints))
	for _, ep := range endpoints {
		if !ep.IsValid() || ep.Addr().Is4In6() || ep.Addr().Zone() != "" || ep.Port() == 0 {
			continue
		}
		for _, prefix := range prefixes {
			if prefix.Contains(ep.Addr()) {
				out = append(out, ep)
				break
			}
		}
	}
	return out
}
