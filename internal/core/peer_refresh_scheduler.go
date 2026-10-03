package core

import (
	"sync"
	"time"
)

const (
	peerRefreshCacheLimit       = 128
	peerRefreshOverflowPerBatch = 4
	peerRefreshRecordLifetime   = 5 * time.Minute
)

var peerRefreshFailureDelays = [...]time.Duration{
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	32 * time.Second,
	60 * time.Second,
}

type peerRefreshRetry struct {
	failures    uint8
	nextAttempt time.Time
	expiresAt   time.Time
}

// peerRefreshScheduler stores retry eligibility only for failed background
// probes. First attempts reserve available cache capacity; only overflow probes
// are rate limited, and their failures never evict protected retry records.
// Each admission has an identity so invalidation cannot be undone by completion.
type peerRefreshScheduler struct {
	mu              sync.Mutex
	entries         map[string]peerRefreshRetry
	reservations    map[string]struct{}
	live            map[string]*peerRefreshTicket
	overflowStarted int
}

type peerRefreshTicket struct {
	id       string
	retained bool
}

func newPeerRefreshScheduler() *peerRefreshScheduler {
	return &peerRefreshScheduler{
		entries:      make(map[string]peerRefreshRetry, peerRefreshCacheLimit),
		reservations: make(map[string]struct{}, peerRefreshOverflowPerBatch),
		live:         make(map[string]*peerRefreshTicket, 4),
	}
}

func (s *peerRefreshScheduler) beginBatch() {
	s.mu.Lock()
	s.overflowStarted = 0
	s.mu.Unlock()
}

func (s *peerRefreshScheduler) admit(id string, now time.Time) *peerRefreshTicket {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live[id] != nil {
		return nil
	}
	retained := false
	if entry, ok := s.entries[id]; ok {
		if !now.Before(entry.expiresAt) {
			delete(s.entries, id)
		} else {
			if now.Before(entry.nextAttempt) {
				return nil
			}
			retained = true
		}
	}
	if !retained {
		if len(s.entries)+len(s.reservations) < peerRefreshCacheLimit {
			s.reservations[id] = struct{}{}
			retained = true
		} else {
			if s.overflowStarted >= peerRefreshOverflowPerBatch {
				return nil
			}
			s.overflowStarted++
		}
	}
	ticket := &peerRefreshTicket{id: id, retained: retained}
	s.live[id] = ticket
	return ticket
}

func (s *peerRefreshScheduler) complete(ticket *peerRefreshTicket, now time.Time, succeeded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ticket == nil || s.live[ticket.id] != ticket {
		return
	}
	id := ticket.id
	delete(s.live, id)
	delete(s.reservations, id)
	if succeeded {
		delete(s.entries, id)
		return
	}
	if !ticket.retained {
		return
	}
	entry := s.entries[id]
	if entry.failures < uint8(len(peerRefreshFailureDelays)) {
		entry.failures++
	}
	delay := peerRefreshFailureDelays[int(entry.failures)-1]
	entry.nextAttempt = now.Add(delay)
	if entry.expiresAt.IsZero() {
		entry.expiresAt = now.Add(peerRefreshRecordLifetime)
	}
	s.entries[id] = entry
}

func (s *peerRefreshScheduler) cancel(ticket *peerRefreshTicket) {
	s.mu.Lock()
	if ticket != nil && s.live[ticket.id] == ticket {
		delete(s.live, ticket.id)
		delete(s.reservations, ticket.id)
	}
	s.mu.Unlock()
}

// publish serializes background cache writes with ticket invalidation. Callers
// must not hold Core.mu while acquiring the scheduler lock.
func (s *peerRefreshScheduler) publish(ticket *peerRefreshTicket, write func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ticket != nil && s.live[ticket.id] == ticket {
		write()
	}
}

func (s *peerRefreshScheduler) prune(active map[string]struct{}, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, entry := range s.entries {
		if _, present := active[id]; !present || !now.Before(entry.expiresAt) {
			delete(s.entries, id)
			delete(s.live, id)
			delete(s.reservations, id)
		}
	}
	for id := range s.live {
		if _, present := active[id]; !present {
			delete(s.live, id)
			delete(s.reservations, id)
		}
	}
}

func (s *peerRefreshScheduler) forget(id string) {
	s.mu.Lock()
	delete(s.entries, id)
	delete(s.live, id)
	delete(s.reservations, id)
	s.mu.Unlock()
}

func (s *peerRefreshScheduler) full() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)+len(s.reservations) >= peerRefreshCacheLimit
}

func (s *peerRefreshScheduler) cached(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, cached := s.entries[id]
	return cached
}
