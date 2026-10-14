package nearby

import "net/netip"

// discoveryPolicy is passed by value, never supplied through Config or an
// environment setting. Every exported entry point uses the zero-value strict
// LAN policy. Only a constructor in _test.go selects the exact loopback seam
// needed to exercise the same socket, wire and session implementation safely.
// There is deliberately no general address-validation callback or allow-any IP.
type discoveryPolicy struct {
	isolatedLoopbackTest bool
}

func (p discoveryPolicy) validAddress(addr netip.Addr) bool {
	if p.isolatedLoopbackTest {
		return addr == netip.MustParseAddr("127.0.0.1")
	}
	return canonicalPrivate(addr)
}

func (p discoveryPolicy) validateScope(scope Scope) error {
	if !p.isolatedLoopbackTest {
		return scope.validateLAN()
	}
	if scope.InterfaceIndex <= 0 || scope.LocalAddress != netip.MustParseAddr("127.0.0.1") || scope.Prefix != netip.MustParsePrefix("127.0.0.1/32") {
		return ErrInvalidScope
	}
	return nil
}
