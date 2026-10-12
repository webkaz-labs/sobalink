package core

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOutboxHealthySenderStillReportsBlockedProfileGuard(t *testing.T) {
	for _, tc := range []struct{ phase, failure, want string }{
		{"initializing", "", "outbox_recovery_required"},
		{"ready", "durable_recovery_required", "durable_recovery_required"},
	} {
		t.Run(tc.phase+tc.failure, func(t *testing.T) {
			c := outboxFixture(t)
			c.mu.Lock()
			c.profile.DurableFiles.Phase = tc.phase
			c.durableProfileError = tc.failure
			c.mu.Unlock()
			value, err := c.outboxCommand(context.Background(), "outbox.list", "list-readiness", json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			list := value.(map[string]any)
			if list["status"] != "blocked_recovery" || list["reasonCode"] != tc.want {
				t.Fatalf("misleading list readiness: %#v", list)
			}
			eligibility := c.outboxEligibility("peer-b")
			if eligibility["allowed"] != false || eligibility["reasonCode"] != tc.want {
				t.Fatalf("misleading peer eligibility: %#v", eligibility)
			}
			if c.outbox.frozen != "" {
				t.Fatal("diagnostic read froze healthy sender storage")
			}
		})
	}
}
