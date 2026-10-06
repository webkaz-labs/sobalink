// Package lanpolicy validates an explicitly chosen underlay destination policy.
// Prefix membership is not proof of a physical interface or same-link routing.
package lanpolicy

import (
	"errors"
	"net/netip"
	"slices"
)

const TrustedRelay = "trusted-relay"
const AllowedLANDestinations = "allowed-lan-destinations"

var ErrPolicy = errors.New("choose trusted-relay without prefixes, or allowed-lan-destinations with explicit private or loopback network prefixes")
var ErrDestination = errors.New("selected relay is outside the explicitly allowed LAN destinations")

// Config is public policy, independently selected on each device. The zero value
// preserves the existing trusted-relay behavior when reading older state.
type Config struct {
	Mode     string   `json:"mode"`
	Prefixes []string `json:"prefixes,omitempty"`
}

var localRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"),
}

// Canonical returns a detached, deterministic representation. It rejects broad
// prefixes containing public addresses, link-local zones and IPv4-mapped IPv6.
func (c Config) Canonical() (Config, error) {
	if c.Mode == "" {
		c.Mode = TrustedRelay
	}
	switch c.Mode {
	case TrustedRelay:
		if len(c.Prefixes) != 0 {
			return Config{}, ErrPolicy
		}
		return Config{Mode: TrustedRelay}, nil
	case AllowedLANDestinations:
		if len(c.Prefixes) == 0 {
			return Config{}, ErrPolicy
		}
	default:
		return Config{}, ErrPolicy
	}
	out := Config{Mode: c.Mode, Prefixes: make([]string, 0, len(c.Prefixes))}
	for _, raw := range c.Prefixes {
		p, err := netip.ParsePrefix(raw)
		if err != nil || p.Addr().Is4In6() || p != p.Masked() || p.String() != raw {
			return Config{}, ErrPolicy
		}
		local := false
		for _, r := range localRanges {
			if r.Addr().BitLen() == p.Addr().BitLen() && p.Bits() >= r.Bits() && r.Contains(p.Addr()) {
				local = true
				break
			}
		}
		if !local {
			return Config{}, ErrPolicy
		}
		out.Prefixes = append(out.Prefixes, raw)
	}
	slices.Sort(out.Prefixes)
	out.Prefixes = slices.Compact(out.Prefixes)
	return out, nil
}

func (c Config) Strict() bool { return c.Mode == AllowedLANDestinations }

// Parsed requires already valid policy but still fails closed on malformed data.
func (c Config) Parsed() ([]netip.Prefix, error) {
	canonical, err := c.Canonical()
	if err != nil {
		return nil, err
	}
	out := make([]netip.Prefix, 0, len(canonical.Prefixes))
	for _, raw := range canonical.Prefixes {
		p, _ := netip.ParsePrefix(raw)
		out = append(out, p)
	}
	return out, nil
}

func (c Config) CheckRelay(address netip.AddrPort) error {
	prefixes, err := c.Parsed()
	if err != nil {
		return err
	}
	if !address.IsValid() || address.Port() == 0 || address.Addr().Is4In6() || address.Addr().Zone() != "" {
		return ErrDestination
	}
	if !c.Strict() {
		return nil
	}
	for _, p := range prefixes {
		if p.Contains(address.Addr()) {
			return nil
		}
	}
	return ErrDestination
}
