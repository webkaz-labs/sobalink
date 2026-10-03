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

func TestUntilStoppedCannotAuthorizeInboundOrContradictoryPermission(t *testing.T) {
	for _, spec := range []ServiceSpec{
		{Direction: "share", Lifetime: "until-stopped"},
		{Direction: "forward", Lifetime: "until-stopped", TTLSeconds: 1},
		{Direction: "forward", Lifetime: "invalid"},
		{Direction: "forward", Lifetime: "finite"},
		{Direction: "forward"},
	} {
		service := &activeService{spec: spec, ctx: context.Background()}
		service.ready.Store(true)
		if service.guard() == nil {
			t.Fatalf("invalid missing-expiry permission became unbounded: %+v", spec)
		}
	}
}
