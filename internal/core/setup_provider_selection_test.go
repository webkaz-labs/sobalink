package core

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type setupTimedDiscovery struct {
	setupFakeDiscovery
	now time.Time
}

func (d *setupTimedDiscovery) Lookup(id string) (SetupCandidate, error) {
	if !d.now.Before(d.candidate.ExpiresAt) {
		return SetupCandidate{}, errors.New("expired")
	}
	return d.setupFakeDiscovery.Lookup(id)
}

func TestSetupLiveDiscoverySupportsComparisonNearOriginalDeadline(t *testing.T) {
	for _, cause := range []string{"record_removed", "query_failed"} {
		t.Run(cause, func(t *testing.T) {
			a, b, now := setupTestSessions(t)
			setupTestPlan(t, a, b, now)
			d := &setupTimedDiscovery{setupFakeDiscovery: setupFakeDiscovery{candidate: SetupCandidate{CandidateID: strings.Repeat("c", 64), Contact: *a.selected, ObservedAt: now, ExpiresAt: a.plan.Expires}}, now: a.plan.Expires.Add(-time.Second)}
			a.discovery, a.selectedManual, a.candidateID = d, false, d.candidate.CandidateID
			a.localApproved = true
			if !a.selectionCurrentLocked(d.now) {
				t.Fatal("live bounded discovery interrupted one-sided identity comparison")
			}
			// Providers surface removal and query failure as an unavailable exact
			// lookup. One-sided approval must not convert either into authority.
			d.invalid = true
			if a.selectionCurrentLocked(d.now) || a.remoteApproved || a.paired {
				t.Fatal("unavailable discovery survived unfinished bilateral approval")
			}
			d.invalid = false
			d.candidate.CandidateID = strings.Repeat("d", 64)
			if a.selectionCurrentLocked(d.now) {
				t.Fatal("a replacement hint revived the original selected ID")
			}
			d.candidate.CandidateID = a.candidateID
			d.now = a.plan.Expires
			if a.selectionCurrentLocked(d.now) {
				t.Fatal("discovery extended its original deadline")
			}
		})
	}
}

func TestSetupCachedHintCannotReviveExpiredRemoteSession(t *testing.T) {
	a, b, now := setupTestSessions(t)
	a.local.Expires = now.Add(120 * time.Second)
	b.local.Expires = now.Add(10 * time.Second)
	b.selected.ExpiresAt = a.local.Expires
	a.selected.ExpiresAt = b.local.Expires
	w, err := b.wireLocked(now)
	if err != nil {
		t.Fatal(err)
	}
	// A still-present hint cannot extend the authenticated participant's own
	// expiry, even if its locally observed contact claims a later deadline.
	a.selected.ExpiresAt = a.local.Expires
	for i := 0; i < 2; i++ {
		if err := a.acceptWireLocked(w, false, now.Add(11*time.Second)); err == nil {
			t.Fatal("cached hint revived the expired remote participant")
		}
		if a.peer != nil || a.plan != nil || a.localApproved || a.remoteApproved || a.paired {
			t.Fatal("expired remote session published pairing authority")
		}
	}
}
