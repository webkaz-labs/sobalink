// Package lanlink implements explicit trusted-relay pairing and a userspace
// application data plane. Direct peer paths and selected-relay diagnostics may
// use the network; this is not a strict LAN-only egress sandbox.
package lanlink

import (
	"errors"
	"net/netip"
)

var ErrBackendUnavailable = errors.New("trusted-relay data-plane backend is unavailable or not configured")

// RelayConfig is explicit: no wildcard, default map, DNS name or public relay.
// A loopback address is useful for local-only operation and isolated tests.
// AllowedPrefixes describe the exact selected LAN, not every private network.
type RelayConfig struct {
	Listen            netip.AddrPort
	AllowedPrefixes   []netip.Prefix
	CertificateSHA256 string
}

func (c RelayConfig) Validate() error {
	a := c.Listen.Addr()
	if !c.Listen.IsValid() || c.Listen.Port() < 1024 || a.IsUnspecified() || a.IsMulticast() || a.Zone() != "" {
		return errors.New("explicit numeric LAN address and unprivileged relay port required")
	}
	if !a.IsPrivate() && !a.IsLoopback() {
		return errors.New("relay address must be private or loopback")
	}
	if !validKey(c.CertificateSHA256) {
		return errors.New("explicit relay certificate SHA-256 pin required")
	}
	if len(c.AllowedPrefixes) == 0 || len(c.AllowedPrefixes) > 8 {
		return errors.New("one to eight explicit LAN prefixes required")
	}
	covered := false
	for _, p := range c.AllowedPrefixes {
		if !p.IsValid() || p != p.Masked() || p.Bits() == 0 || p.Addr().Is4() != a.Is4() {
			return errors.New("invalid LAN prefix")
		}
		last := prefixLast(p)
		if !(p.Addr().IsPrivate() && last.IsPrivate()) && !(p.Addr().IsLoopback() && last.IsLoopback()) {
			return errors.New("LAN prefix must remain wholly private or loopback")
		}
		covered = covered || p.Contains(a)
	}
	if !covered {
		return errors.New("relay address is outside the selected LAN")
	}
	return nil
}
func (c RelayConfig) PermitsUnderlay(ap netip.AddrPort) bool {
	if c.Validate() != nil || !ap.IsValid() || ap.Port() == 0 || ap.Addr().Zone() != "" {
		return false
	}
	for _, p := range c.AllowedPrefixes {
		if p.Contains(ap.Addr()) {
			return true
		}
	}
	return false
}

func prefixLast(p netip.Prefix) netip.Addr {
	b := p.Addr().AsSlice()
	for bit := p.Bits(); bit < len(b)*8; bit++ {
		b[bit/8] |= byte(1 << (7 - bit%8))
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}
