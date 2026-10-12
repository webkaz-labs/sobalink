//go:build directlan_integration && directlan_lifecycle

package directlan

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const lifecycleExpiryGrace = 5 * time.Second

type lifecycleExpirySample struct {
	before, after [2]time.Time
	ready         [2]bool
	observedAt    time.Time // taken before either readiness read, not after stats
}

// evaluateLifecycleExpiry never treats a completed handshake as key derivation.
// A stable overdue ready session fails immediately; only an unelapsed or changed
// deadline may defer the assertion. Renewal cannot extend the absolute budget.
func evaluateLifecycleExpiry(s lifecycleExpirySample, budget time.Time) (next time.Time, err error) {
	if !s.observedAt.Before(budget) {
		return time.Time{}, fmt.Errorf("idle expiry observation budget exhausted")
	}
	for i := range s.ready {
		if s.before[i].IsZero() || s.after[i].IsZero() {
			return time.Time{}, fmt.Errorf("side %d has no session expiry deadline", i)
		}
		due := s.after[i].Add(lifecycleExpiryGrace)
		if !s.before[i].Equal(s.after[i]) {
			// Retry a renewal that overlapped the sample, even if it is already
			// overdue. The small backoff avoids spinning; it grants no pass.
			if !s.observedAt.Before(due) {
				due = s.observedAt.Add(time.Millisecond)
			}
		} else if !s.observedAt.Before(due) {
			if s.ready[i] {
				return time.Time{}, fmt.Errorf("idle expired session remained ready: side=%d overdue=%s", i, s.observedAt.Sub(s.after[i]))
			}
			continue
		}
		if next.IsZero() || due.Before(next) {
			next = due
		}
	}
	if !next.IsZero() && !next.Before(budget) {
		return time.Time{}, fmt.Errorf("idle expiry deadline exceeds observation budget")
	}
	return next, nil
}

func lifecycleExpiryDeadlines(t *testing.T, nodes [2]*Node) (deadlines [2]time.Time) {
	t.Helper()
	for i, n := range nodes {
		// No Node.mu is held while entering the engine's observation API.
		stats, err := n.engine.LifecycleTestPeerStats()
		if err != nil || len(stats) != 1 {
			t.Fatal("no single fixture session expiry observation")
		}
		deadlines[i] = stats[0].SessionExpires
	}
	return deadlines
}

func awaitLifecycleExpiry(t *testing.T, a, b *Node) {
	t.Helper()
	nodes := [2]*Node{a, b}
	start := time.Now()
	// Retain the existing eight-minute test cap, reserving the existing
	// eight-second fresh-dial allowance rather than extending it for renewal.
	budget := start.Add(8 * time.Minute)
	if deadline, ok := t.Deadline(); ok && deadline.Before(budget) {
		budget = deadline
	}
	budget = budget.Add(-8 * time.Second)
	for {
		s := lifecycleExpirySample{before: lifecycleExpiryDeadlines(t, nodes)}
		s.observedAt = time.Now()
		for i, n := range nodes {
			n.mu.Lock()
			p := n.peers[nodes[1-i].PublicKey()]
			n.mu.Unlock()
			s.ready[i] = p.session.ready()
		}
		s.after = lifecycleExpiryDeadlines(t, nodes)
		next, err := evaluateLifecycleExpiry(s, budget)
		if err != nil {
			t.Fatal(err)
		}
		if next.IsZero() {
			t.Logf("idle expiry: elapsed=%s deadline_age_a=%s deadline_age_b=%s", time.Since(start), s.observedAt.Sub(s.after[0]), s.observedAt.Sub(s.after[1]))
			return
		}
		time.Sleep(time.Until(next))
	}
}

func testLifecycleExpiryOracle(t *testing.T) {
	// Value-only cases exercise the acceptance rule without sleeping, sending
	// traffic, changing engine clocks, or claiming a historical failure cause.
	start := time.Now()
	deadline := start.Add(180 * time.Second)
	stable := [2]time.Time{deadline, deadline}
	budget := start.Add(8 * time.Minute)
	for _, tc := range []struct {
		name       string
		before     [2]time.Time
		after      [2]time.Time
		ready      [2]bool
		observedAt time.Time
		next       time.Time
		failure    string
	}{
		{name: "expired", before: stable, after: stable, observedAt: start.Add(185 * time.Second)},
		{name: "overdue-ready", before: stable, after: stable, ready: [2]bool{true, false}, observedAt: start.Add(185 * time.Second), failure: "remained ready: side=0"},
		{name: "other-side-overdue-ready", before: stable, after: stable, ready: [2]bool{false, true}, observedAt: start.Add(185 * time.Second), failure: "remained ready: side=1"},
		{name: "late-derivation", before: [2]time.Time{deadline.Add(6 * time.Second), deadline}, after: [2]time.Time{deadline.Add(6 * time.Second), deadline}, ready: [2]bool{true, false}, observedAt: start.Add(185 * time.Second), next: start.Add(191 * time.Second)},
		{name: "renewal-during-sample", before: stable, after: [2]time.Time{deadline.Add(6 * time.Second), deadline}, ready: [2]bool{true, false}, observedAt: start.Add(185 * time.Second), next: start.Add(191 * time.Second)},
		{name: "both-peers-must-expire", before: [2]time.Time{deadline, deadline.Add(6 * time.Second)}, after: [2]time.Time{deadline, deadline.Add(6 * time.Second)}, observedAt: start.Add(185 * time.Second), next: start.Add(191 * time.Second)},
		{name: "earliest-pending-deadline", before: [2]time.Time{deadline, deadline.Add(6 * time.Second)}, after: [2]time.Time{deadline, deadline.Add(6 * time.Second)}, ready: [2]bool{true, true}, observedAt: start, next: start.Add(185 * time.Second)},
		{name: "renewal-does-not-hide-other-side-failure", before: stable, after: [2]time.Time{deadline.Add(6 * time.Second), deadline}, ready: [2]bool{true, true}, observedAt: start.Add(185 * time.Second), failure: "remained ready: side=1"},
		{name: "read-before-grace-boundary", before: stable, after: stable, ready: [2]bool{true, true}, observedAt: start.Add(185*time.Second - time.Nanosecond), next: start.Add(185 * time.Second)},
		{name: "missing-before", before: [2]time.Time{{}, deadline}, after: stable, observedAt: start, failure: "no session expiry"},
		{name: "missing-after", before: stable, after: [2]time.Time{deadline, {}}, observedAt: start, failure: "no session expiry"},
		{name: "budget-exhausted", before: stable, after: stable, observedAt: budget, failure: "budget exhausted"},
		{name: "renewal-exceeds-budget", before: stable, after: [2]time.Time{budget, deadline}, observedAt: start.Add(185 * time.Second), failure: "exceeds observation budget"},
		{name: "overdue-renewal-needs-new-sample", before: [2]time.Time{deadline.Add(-time.Second), deadline}, after: stable, ready: [2]bool{true, false}, observedAt: start.Add(185 * time.Second), next: start.Add(185*time.Second + time.Millisecond)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, err := evaluateLifecycleExpiry(lifecycleExpirySample{before: tc.before, after: tc.after, ready: tc.ready, observedAt: tc.observedAt}, budget)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("error=%v; want %q", err, tc.failure)
				}
				return
			}
			if err != nil || next != tc.next {
				t.Fatalf("next=%v error=%v; want next=%v", next, err, tc.next)
			}
		})
	}
}
