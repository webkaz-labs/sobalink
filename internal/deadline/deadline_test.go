package deadline

import (
	"testing"
	"time"
)

func TestOptionalAndRequiredDeadlinesAtBoundary(t *testing.T) {
	start := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	until := start.Add(time.Minute)
	for _, tc := range []struct {
		name   string
		now    time.Time
		passed bool
	}{
		{"before", until.Add(-time.Nanosecond), false},
		{"equal", until, true},
		{"after", until.Add(time.Nanosecond), true},
		{"same-instant-other-zone", until.In(time.FixedZone("offset", 9*60*60)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if Passed(tc.now, until) != tc.passed || Active(tc.now, until) == tc.passed {
				t.Fatal("deadline boundary changed")
			}
		})
	}
	if Passed(start, time.Time{}) {
		t.Fatal("absent optional deadline was treated as expired")
	}
	if Active(start, time.Time{}) {
		t.Fatal("missing required expiry granted access")
	}
}

func TestEitherClockCanEndLifetime(t *testing.T) {
	base := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	until := base.Add(time.Minute)
	for _, tc := range []struct {
		name                string
		elapsedNow, wallNow time.Time
		passed              bool
	}{
		{"both-before", base.Add(30 * time.Second), base.Add(30 * time.Second), false},
		{"suspend-wall-reaches-deadline", base.Add(30 * time.Second), until, true},
		{"suspend-wall-past-deadline", base.Add(30 * time.Second), until.Add(time.Hour), true},
		{"wall-rollback-elapsed-reaches-deadline", until, base.Add(-time.Hour), true},
		{"wall-rollback-elapsed-past-deadline", until.Add(time.Second), base.Add(-time.Hour), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := passedByClocks(tc.elapsedNow, until, tc.wallNow, until); got != tc.passed {
				t.Fatalf("permission expiry with disagreeing clocks = %v; want %v", got, tc.passed)
			}
		})
	}
}
