package resourcegrant

import (
	"strings"
	"testing"
	"time"
)

func TestCatalogViewFenceFinalDisclosureAndTerminalTiming(t *testing.T) {
	record := fixtureCatalogView().Records[0]
	now := time.Unix(150, 0)
	cutoff := time.Unix(175, 0)
	newFence := func() *CatalogViewFence {
		f, err := newCatalogViewFence(record, cutoff, now)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	f := newFence()
	if !f.admit(record.Selector(), record.Relationship, record.ServiceIDs, func() time.Time { return now }) {
		t.Fatal("exact disclosure rejected")
	}
	wrong := record.Relationship
	wrong.PairBinding = strings.Repeat("a", 64)
	if f.admit(record.Selector(), wrong, record.ServiceIDs, func() time.Time { return now }) || f.admit(record.Selector(), record.Relationship, []string{strings.Repeat("a", 32)}, func() time.Time { return now }) {
		t.Fatal("different pair or service admitted")
	}
	selector := record.Selector()
	selector.GrantRevision++
	if f.admit(selector, record.Relationship, record.ServiceIDs, func() time.Time { return now }) {
		t.Fatal("different grant admitted")
	}
	f.Close()
	if f.admit(record.Selector(), record.Relationship, record.ServiceIDs, func() time.Time { return now }) {
		t.Fatal("revoked fence admitted disclosure")
	}
	for _, tc := range []struct {
		name               string
		at                 time.Time
		expired, uncertain bool
	}{
		{"boot deadline", cutoff, true, false},
		{"absolute expiry", time.Unix(record.ExpiresAt, 0), true, false},
		{"backward time", time.Unix(record.IssuedAt-1, 0), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFence()
			if f.admit(record.Selector(), record.Relationship, record.ServiceIDs, func() time.Time { return tc.at }) {
				t.Fatal("terminal deadline admitted")
			}
			expired, uncertain := f.TimingObservation(record.Selector())
			if expired != tc.expired || uncertain != tc.uncertain {
				t.Fatal("terminal timing lost")
			}
			if f.admit(record.Selector(), record.Relationship, record.ServiceIDs, func() time.Time { return now }) {
				t.Fatal("terminal observation revived")
			}
		})
	}
}

func TestCatalogViewObserveIndependentExpiry(t *testing.T) {
	e := fixtureCatalogView()
	next, err := ObserveCatalogViewEnvelope(e, time.Unix(200, 0))
	if err != nil || next.ActiveRecord().ID != "" || len(next.Records) != 1 || next.Records[0].State != Expired || *next.HighWater != 2 {
		t.Fatal("expiry did not reduce catalog authority", err)
	}
	if e.Records[0].State != Active || *e.HighWater != 1 {
		t.Fatal("input state mutated")
	}
	backward, err := ObserveCatalogViewEnvelope(next, time.Unix(150, 0))
	if err != nil || !backward.Clock.Uncertain || backward.ActiveRecord().ID != "" {
		t.Fatal("backward time revived expired authority")
	}
}
