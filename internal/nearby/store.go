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
// no history grows with attacker-controlled IDs. A monotonically increasing
// revision prevents a replay after expiry/replacement from reviving an old ID.
type candidateStore struct {
	scope      Scope
	generation string
	revision   uint64
	items      map[string]Candidate
}

func newStore(s Scope) candidateStore {
	return candidateStore{scope: s, items: make(map[string]Candidate)}
}
func observationKey(a Advertisement, source netip.AddrPort) string {
	return a.SessionID + "/" + source.String()
}
func (s *candidateStore) prune(now time.Time) {
	for key, c := range s.items {
		if !now.Before(c.ExpiresAt) {
			delete(s.items, key)
		}
	}
}
func (s *candidateStore) observe(a Advertisement, source netip.AddrPort, ttl time.Duration, now time.Time) {
	s.prune(now)
	key := observationKey(a, source)
	prior, exists := s.items[key]
	if ttl <= 0 {
		if exists && prior.Advertisement == a {
			delete(s.items, key)
		}
		return
	}
	if ttl > CandidateTTL {
		ttl = CandidateTTL
	}
	if exists && prior.Advertisement == a {
		prior.ObservedAt, prior.ExpiresAt = now, now.Add(ttl)
		s.items[key] = prior
		return
	}
	if exists {
		delete(s.items, key)
	}
	if len(s.items) >= MaxCandidates {
		return
	}
	s.revision++
	// Every string is length delimited by %q and every structural value explicit.
	binding := fmt.Sprintf("%q|%d|%s|%s|%s|%q|%q|%q|%q|%s|%d", s.generation, s.scope.InterfaceIndex, s.scope.LocalAddress, s.scope.Prefix, source, a.SessionID, a.Alias, a.Protocol, a.Capability, a.Bootstrap, s.revision)
	digest := sha256.Sum256([]byte(binding))
	s.items[key] = Candidate{ID: hex.EncodeToString(digest[:]), Advertisement: a, Scope: s.scope, Source: source, ObservedAt: now, ExpiresAt: now.Add(ttl)}
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
