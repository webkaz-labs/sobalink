package core

import (
	"fmt"
	"testing"
	"time"
)

func TestPeerRefreshBackoffScheduleAndReset(t *testing.T) {
	s := newPeerRefreshScheduler()
	base := time.Unix(100, 0)
	now := base
	for index, delay := range []time.Duration{2, 4, 8, 16, 32, 60, 60} {
		s.beginBatch()
		ticket := s.admit("peer", now)
		if ticket == nil {
			t.Fatalf("failure %d was not admitted", index+1)
		}
		s.complete(ticket, now, false)
		entry := s.entries["peer"]
		if got := entry.nextAttempt.Sub(now); got != delay*time.Second {
			t.Fatalf("failure %d delay = %s, want %s", index+1, got, delay*time.Second)
		}
		if s.admit("peer", entry.nextAttempt.Add(-time.Nanosecond)) != nil {
			t.Fatalf("failure %d admitted before deadline", index+1)
		}
		now = entry.nextAttempt
	}
	s.beginBatch()
	ticket := s.admit("peer", now)
	if ticket == nil {
		t.Fatal("peer not admitted after retry deadline")
	}
	s.complete(ticket, now, true)
	if _, exists := s.entries["peer"]; exists {
		t.Fatal("success did not reset failure state")
	}
	if s.admit("peer", now) == nil {
		t.Fatal("successful peer was not eligible after the normal confirmation interval")
	}
}

func TestPeerRefreshSchedulerBoundsAndPrunes(t *testing.T) {
	s := newPeerRefreshScheduler()
	now := time.Unix(200, 0)
	for batch := 0; batch < peerRefreshCacheLimit/peerRefreshOverflowPerBatch; batch++ {
		s.beginBatch()
		for i := batch * peerRefreshOverflowPerBatch; i < (batch+1)*peerRefreshOverflowPerBatch; i++ {
			id := peerRefreshTestID(i)
			ticket := s.admit(id, now)
			if ticket == nil {
				t.Fatalf("initial peer %s was not admitted", id)
			}
			s.complete(ticket, now, false)
		}
	}
	s.beginBatch()
	protected := s.entries[peerRefreshTestID(0)]
	for i := peerRefreshCacheLimit; i < 400; i++ {
		id := peerRefreshTestID(i)
		if ticket := s.admit(id, now); ticket != nil {
			s.complete(ticket, now, false)
		}
	}
	if len(s.entries) != peerRefreshCacheLimit || s.entries[peerRefreshTestID(0)] != protected {
		t.Fatal("cache pressure evicted protected backoff state or exceeded its bound")
	}
	active := map[string]struct{}{peerRefreshTestID(0): {}}
	s.prune(active, now)
	if len(s.entries) != 1 {
		t.Fatalf("prune retained %d absent entries", len(s.entries))
	}
	s.prune(active, now.Add(peerRefreshRecordLifetime))
	if len(s.entries) != 0 {
		t.Fatal("expired retry record was not pruned")
	}
}

func TestPeerRefreshSchedulerRateLimitsOnlyOverflow(t *testing.T) {
	s := newPeerRefreshScheduler()
	now := time.Unix(300, 0)
	s.beginBatch()
	for i := 0; i < peerRefreshCacheLimit; i++ {
		ticket := s.admit(peerRefreshTestID(i), now)
		if ticket == nil || !ticket.retained {
			t.Fatalf("initial peer %d was not admitted with capacity", i)
		}
		s.complete(ticket, now, false)
	}
	protected := s.entries[peerRefreshTestID(0)]
	for i := 0; i < peerRefreshOverflowPerBatch+5; i++ {
		ticket := s.admit(peerRefreshTestID(peerRefreshCacheLimit+i), now)
		if (ticket != nil) != (i < peerRefreshOverflowPerBatch) {
			t.Fatalf("unexpected overflow admission %d", i)
		}
		if ticket != nil {
			if ticket.retained {
				t.Fatal("overflow reserved protected state")
			}
			s.complete(ticket, now, false)
		}
	}
	if len(s.entries) != peerRefreshCacheLimit || s.entries[peerRefreshTestID(0)] != protected {
		t.Fatal("overflow changed protected retry state")
	}
	s.beginBatch()
	ticket := s.admit("peer-next-batch", now)
	if ticket == nil {
		t.Fatal("overflow admission budget did not replenish")
	}
	s.cancel(ticket)
}

func TestPeerRefreshSchedulerReservesConcurrentCacheCapacity(t *testing.T) {
	s := newPeerRefreshScheduler()
	now := time.Unix(400, 0)
	for i := 0; i < peerRefreshCacheLimit-1; i++ {
		id := peerRefreshTestID(i)
		if i%peerRefreshOverflowPerBatch == 0 {
			s.beginBatch()
		}
		ticket := s.admit(id, now)
		if ticket == nil {
			t.Fatalf("peer %s was not admitted", id)
		}
		s.complete(ticket, now, false)
	}
	s.beginBatch()
	one, two := s.admit("reserved-one", now), s.admit("reserved-two", now)
	if one == nil || two == nil || !one.retained || two.retained {
		t.Fatal("concurrent admissions overcommitted the final cache slot")
	}
	s.complete(two, now, false)
	s.complete(one, now, false)
	if len(s.entries) != peerRefreshCacheLimit || len(s.reservations) != 0 {
		t.Fatalf("cache or reservations have wrong bounds: entries=%d reservations=%d", len(s.entries), len(s.reservations))
	}
}

func peerRefreshTestID(i int) string { return fmt.Sprintf("peer-%04d", i) }

func TestPeerRefreshSchedulerStaleCompletionAfterForget(t *testing.T) {
	s := newPeerRefreshScheduler()
	now := time.Unix(500, 0)
	for i := 0; i < peerRefreshCacheLimit; i++ {
		s.beginBatch()
		id := peerRefreshTestID(i)
		ticket := s.admit(id, now)
		if ticket == nil {
			t.Fatal("initial admission failed")
		}
		s.complete(ticket, now, false)
	}
	id := peerRefreshTestID(0)
	now = now.Add(2 * time.Second)
	ticket := s.admit(id, now)
	if ticket == nil {
		t.Fatal("cached retry was not admitted")
	}
	release, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		<-release
		s.complete(ticket, now, false)
	}()
	s.forget(id)
	s.beginBatch()
	replacement := s.admit("replacement", now)
	if replacement == nil {
		t.Fatal("freed cache slot was not reusable")
	}
	s.complete(replacement, now, false)
	close(release)
	<-done
	if _, restored := s.entries[id]; restored || len(s.entries) != peerRefreshCacheLimit {
		t.Fatalf("stale completion restored=%v, cache size=%d, want %d", restored, len(s.entries), peerRefreshCacheLimit)
	}
}

func TestPeerRefreshSchedulerInvalidationRejectsOldTickets(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, invalidate := range []string{"forget", "absent", "expired"} {
			if invalidate == "expired" && !cached {
				continue
			}
			for _, outcome := range []string{"failure", "success", "cancel"} {
				t.Run(fmt.Sprintf("cached=%t/%s/%s", cached, invalidate, outcome), func(t *testing.T) {
					s := newPeerRefreshScheduler()
					now := time.Unix(600, 0)
					if cached {
						s.complete(s.admit("peer", now), now, false)
						now = now.Add(2 * time.Second)
					}
					old := s.admit("peer", now)
					if old == nil || s.admit("peer", now) != nil {
						t.Fatal("live admission was missing or duplicated")
					}
					switch invalidate {
					case "forget":
						s.forget("peer")
					case "absent":
						s.prune(map[string]struct{}{}, now)
					case "expired":
						now = now.Add(peerRefreshRecordLifetime)
						s.prune(map[string]struct{}{"peer": {}}, now)
					}
					fresh := s.admit("peer", now)
					if fresh == nil {
						t.Fatal("invalidated reservation was not released")
					}
					switch outcome {
					case "cancel":
						s.cancel(old)
					default:
						s.complete(old, now, outcome == "success")
					}
					if s.live["peer"] != fresh || len(s.reservations) != 1 || len(s.entries) != 0 {
						t.Fatal("stale outcome altered the new live admission")
					}
					s.complete(fresh, now, false)
					if s.entries["peer"].failures != 1 || len(s.live) != 0 || len(s.reservations) != 0 {
						t.Fatal("fresh failure did not start a clean retry sequence")
					}
				})
			}
		}
	}
}
