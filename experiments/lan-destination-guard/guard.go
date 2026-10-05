// Package guard is an isolated, socket-free design prototype, not production integration.
package guard

import (
	"errors"
	"net/netip"
	"sync"
)

var ErrDenied = errors.New("destination denied")

// Policy is deliberately numeric and explicit. Prefix membership is NOT route proof.
type Policy struct {
	Prefixes []netip.Prefix
	Relay    netip.AddrPort
}

func (p Policy) valid() bool {
	if len(p.Prefixes) == 0 || len(p.Prefixes) > 8 || !validDestination(p.Relay) {
		return false
	}
	covered := false
	for _, prefix := range p.Prefixes {
		if !prefix.IsValid() || prefix != prefix.Masked() || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return false
		}
		last := prefix.Addr().AsSlice()
		for bit := prefix.Bits(); bit < len(last)*8; bit++ {
			last[bit/8] |= 1 << (7 - bit%8)
		}
		end, _ := netip.AddrFromSlice(last)
		if !prefix.Addr().IsPrivate() || !end.IsPrivate() {
			return false
		}
		covered = covered || prefix.Contains(p.Relay.Addr())
	}
	return covered
}
func validDestination(ap netip.AddrPort) bool {
	a := ap.Addr()
	return ap.IsValid() && ap.Port() != 0 && a.Zone() == "" && !a.Is4In6() && a.IsPrivate() && !a.IsUnspecified() && !a.IsMulticast()
}
func (p Policy) permits(ap netip.AddrPort) bool {
	if !p.valid() || !validDestination(ap) {
		return false
	}
	for _, prefix := range p.Prefixes {
		if prefix.Contains(ap.Addr()) {
			return true
		}
	}
	return false
}

// Sink models the two distinct lower-layer write entrypoints and TCP connect.
// Implementations in this experiment only record calls; no real socket implementation exists.
type Sink interface {
	Write([]byte, netip.AddrPort) error
	Batch([][]byte, netip.AddrPort) error
	Connect(netip.AddrPort) error
}

// RouteProof is deliberately injectable and NOT implemented here. A real implementation
// needs selected-interface, on-link and route-change enforcement with OS-specific binding.
// Returning true without those properties reduces this to a CIDR-only guard.
type RouteProof func(netip.AddrPort) bool

type Guard struct {
	mu     sync.RWMutex
	policy Policy
	sink   Sink
	proof  RouteProof
}

func New(p Policy, sink Sink, proof RouteProof) *Guard {
	g := &Guard{sink: sink, proof: proof}
	g.Replace(p)
	return g
}

// Replace revokes old policy even if replacement is invalid: invalid means deny all.
// Lock spans writes, so when Replace returns, no older-policy call remains in progress.
func (g *Guard) Replace(p Policy) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p.Prefixes = append([]netip.Prefix(nil), p.Prefixes...)
	g.policy = p
}
func (g *Guard) permits(ap netip.AddrPort) bool {
	return g.sink != nil && g.policy.permits(ap) && g.proof != nil && g.proof(ap)
}
func (g *Guard) Write(b []byte, ap netip.AddrPort) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if !g.permits(ap) {
		return ErrDenied
	}
	return g.sink.Write(b, ap)
}
func (g *Guard) Batch(b [][]byte, ap netip.AddrPort) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if !g.permits(ap) {
		return ErrDenied
	}
	if len(b) == 0 {
		return nil
	}
	return g.sink.Batch(b, ap)
}
func (g *Guard) Connect(ap netip.AddrPort) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if ap != g.policy.Relay || !g.permits(ap) {
		return ErrDenied
	}
	return g.sink.Connect(ap)
}
