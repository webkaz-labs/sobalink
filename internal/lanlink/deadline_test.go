package lanlink

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"
)

func TestInvitationRejectsExactAndMissingExpiry(t *testing.T) {
	start := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		at       time.Time
		missing  bool
		accepted bool
	}{
		{"before", start.Add(time.Minute - time.Nanosecond), false, true},
		{"exact", start.Add(time.Minute), false, false},
		{"missing", start, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			book := NewBook()
			token, err := book.Issue(context.Background(), peer(), cfg(), start, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if tc.missing {
				hash := sha256.Sum256([]byte(token))
				invitation := book.invites[hash]
				invitation.expires = time.Time{}
				book.invites[hash] = invitation
			}
			err = book.Redeem(context.Background(), token, peer().Key, cfg().Listen, tc.at)
			if (err == nil) != tc.accepted {
				t.Fatalf("invitation boundary acceptance = %v; want %v", err == nil, tc.accepted)
			}
			_, err = book.Epoch(peer().Key)
			if (err == nil) != tc.accepted {
				t.Fatal("denied invitation published peer approval")
			}
		})
	}
}
