package nearby

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"time"
)

// candidateStore is confined by Session.mu. It retains at most 64 candidates;
// the assembler separately retains a bounded lifetime tombstone for each bundle.
// Revisions and a local owner generation bind immutable observation identities.
type candidateStore struct {
	scope      Scope
	generation string
	revision   uint64
	items      map[string]Candidate
}

func newStore(s Scope) candidateStore {
	return candidateStore{scope: s, items: make(map[string]Candidate)}
}
func (s *candidateStore) prune(now time.Time) {
	for key, c := range s.items {
		if !now.Before(c.ExpiresAt) {
			delete(s.items, key)
		}
	}
}
func (s *candidateStore) publish(key string, a Advertisement, kind observationKind, source netip.AddrPort, first, expiry time.Time) {
	if prior, ok := s.items[key]; ok && prior.Advertisement == a {
		prior.ExpiresAt = expiry
		s.items[key] = prior
		return
	}
	if len(s.items) >= MaxCandidates {
		return
	}
	provenance := RawPacketProvenance
	if kind == osDNSServiceObservation {
		provenance = OSDNSServiceProvenance
	}
	s.revision++
	binding := fmt.Sprintf("%q|%d|%s|%s|%s|%q|%q|%q|%q|%s|%q|%d", s.generation, s.scope.InterfaceIndex, s.scope.LocalAddress, s.scope.Prefix, source, a.SessionID, a.Alias, a.Protocol, a.Capability, a.Bootstrap, provenance, s.revision)
	digest := sha256.Sum256([]byte(binding))
	s.items[key] = Candidate{ID: hex.EncodeToString(digest[:]), Advertisement: a, Scope: s.scope, Source: source, Provenance: provenance, ObservedAt: first, ExpiresAt: expiry}
}

func (s *candidateStore) list(now time.Time) []Candidate {
	s.prune(now)
	out := make([]Candidate, 0, len(s.items))
	for _, c := range s.items {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (s *candidateStore) lookup(id string, now time.Time) (Candidate, error) {
	s.prune(now)
	for _, c := range s.items {
		if c.ID == id {
			return c, nil
		}
	}
	return Candidate{}, ErrCandidateUnavailable
}
