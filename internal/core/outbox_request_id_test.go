package core

import (
	"strings"
	"testing"
	"time"
)

func TestOutboxRequestIdentityCanonicalAndBounded(t *testing.T) {
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	id := newOutboxRequestID(now)
	created, ok := outboxRequestCreated(id)
	if !ok || !created.Equal(now) || !outboxRequestFresh(id, now) {
		t.Fatal("current identity rejected")
	}
	for _, bad := range []string{"", id + "_extra", strings.Replace(id, "ob1", "ob2", 1), "ob1_01760227200000_" + strings.Repeat("a", 32), "ob1_9999999999999_" + strings.Repeat("a", 32), "ob1_1760227200000_" + strings.Repeat("A", 32), "ob1_1760227200000_" + strings.Repeat("0", 31), "ob1_+760227200000_" + strings.Repeat("a", 32)} {
		if _, ok := outboxRequestCreated(bad); ok {
			t.Fatal("noncanonical or overflowing identity accepted")
		}
	}
	if !outboxRequestFresh(id, now.Add(5*time.Minute)) || outboxRequestFresh(id, now.Add(5*time.Minute+time.Millisecond)) || outboxRequestFresh(id, now.Add(-5*time.Minute-time.Millisecond)) {
		t.Fatal("freshness boundary changed")
	}
	horizon, ok := outboxRequestHorizon(id)
	if !ok || !horizon.Equal(now.Add(8*24*time.Hour+5*time.Minute)) || outboxRequestRetired(id, horizon.Add(-time.Millisecond)) || !outboxRequestRetired(id, horizon) {
		t.Fatal("retirement horizon changed")
	}
}
