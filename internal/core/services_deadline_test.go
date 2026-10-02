package core

import (
	"context"
	"testing"
	"time"
)

func TestServicePermissionRejectsExactAndMissingExpiry(t *testing.T) {
	until := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	service := &activeService{ctx: context.Background(), expires: until}
	service.ready.Store(true)
	if err := service.guardAt(until.Add(-time.Nanosecond)); err != nil {
		t.Fatalf("valid grant denied: %v", err)
	}
	if err := service.guardAt(until); err == nil {
		t.Fatal("grant remained active at its exact expiry")
	}
	service.expires = time.Time{}
	if err := service.guardAt(until); err == nil {
		t.Fatal("missing expiry enabled an unbounded service permission")
	}
}
