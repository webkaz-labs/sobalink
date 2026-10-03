package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestSelectionActivationGateWithholdsAuthorizationAndDiscovery(t *testing.T) {
	p := newCorePair(t)
	saved := mustCommand(t, p.b, "service.save", map[string]any{"configuration": ServiceSpec{Name: "gate", Backend: "tailnet", Direction: "share", Network: "tcp", Ports: "8080", PeerIDs: []string{"peer-a"}, Lifetime: "until-revoked", Discoverable: true}}).(SavedServiceConfiguration)
	gate := &atomic.Bool{}
	withServiceOperation(p.b, func() {
		if _, err := p.b.startServiceCommand(context.Background(), "service.share", savedStartPayload(saved.Configuration, "", 0), gate); err != nil {
			t.Fatal(err)
		}
	})
	a := p.b.active[saved.Configuration.ID]
	if !p.b.serviceTransportReady(a) || a.guard() == nil || len(p.b.permittedServices("peer-a")) != 0 {
		t.Fatal("staged group member authorized traffic or discovery before activation")
	}
	if !p.b.selectionPrepared([]ServiceSpec{saved.Configuration}) {
		t.Fatal("prepared gated member was not ready for atomic activation")
	}
	expires := time.Now().Add(-time.Second)
	a.leaseSeconds = 1
	a.leaseExpires.Store(&expires)
	if p.b.selectionPrepared([]ServiceSpec{saved.Configuration}) || gate.Load() {
		t.Fatal("expired prepared member opened group gate")
	}
	a.leaseSeconds = 0
	a.leaseExpires.Store(nil)
	gate.Store(true)
	if a.guard() != nil || len(p.b.permittedServices("peer-a")) != 1 {
		t.Fatal("complete group did not activate")
	}
	gate.Store(false)
	if a.guard() == nil || len(p.b.permittedServices("peer-a")) != 0 {
		t.Fatal("rollback gate retained authorization")
	}
}

func TestSelectionTemporaryLifetimePreservesSavedDefinitionAndDoesNotRenew(t *testing.T) {
	p := newCorePair(t)
	saved := mustCommand(t, p.b, "service.save", map[string]any{"configuration": ServiceSpec{Name: "scope", Backend: "tailnet", Direction: "share", Network: "tcp", Ports: "8080", PeerIDs: []string{"peer-a"}, Lifetime: "finite", TTLSeconds: 3600}}).(SavedServiceConfiguration)
	selection := mustCommand(t, p.b, "service.selection", map[string]any{"ids": []string{saved.Configuration.ID}}).(map[string]any)
	input := map[string]any{"ids": []string{saved.Configuration.ID}, "expectedRevision": selection["revision"], "owner": "manual-owner", "lifetime": "finite", "ttlSeconds": 120}
	result := mustCommand(t, p.b, "services.start", input).(map[string]any)
	if result["ready"] != true {
		t.Fatal("temporary selection not ready", result)
	}
	a := p.b.active[saved.Configuration.ID]
	if a.spec.TTLSeconds != 120 || a.leaseExpires.Load() != nil || !a.permissionActiveAt(time.Now()) {
		t.Fatal("owner-only temporary lifetime was not applied")
	}
	before := a.expires
	retained := savedService(t, p.b, saved.Configuration.ID)
	if retained.Configuration.TTLSeconds != 3600 || retained.Revision != saved.Revision {
		t.Fatal("temporary start rewrote saved lifetime")
	}
	mustCommand(t, p.b, "services.start", input)
	if !a.expires.Equal(before) {
		t.Fatal("idempotent start renewed the lifetime")
	}
	input["ttlSeconds"] = 180
	if _, err := command(p.b, randomID(), "services.start", input); networkErrorCode(err) != "service_lifetime_conflict" {
		t.Fatal("active lifetime changed without stop", err)
	}
	if _, err := command(p.b, randomID(), "services.renew", map[string]any{"ids": []string{saved.Configuration.ID}, "owner": "manual-owner", "leaseSeconds": 30}); err == nil {
		t.Fatal("owner-only permission became a renewable lease")
	}
	if _, err := command(p.b, randomID(), "services.stop", map[string]any{"ids": []string{saved.Configuration.ID}, "owner": "another-owner"}); err == nil {
		t.Fatal("different owner stopped permission")
	}
	mustCommand(t, p.b, "services.stop", map[string]any{"ids": []string{saved.Configuration.ID}, "owner": "manual-owner"})
	if a.permissionActiveAt(time.Now()) {
		t.Fatal("owned stop left permission active")
	}
}

func TestSelectionRejectsInvalidRuntimeOverrideBeforeStartingAnyMember(t *testing.T) {
	p := newCorePair(t)
	saved := mustCommand(t, p.b, "service.save", map[string]any{"configuration": ServiceSpec{Name: "scope", Backend: "tailnet", Direction: "share", Network: "tcp", Ports: "8080", PeerIDs: []string{"peer-a"}, Lifetime: "until-revoked"}}).(SavedServiceConfiguration)
	selection := mustCommand(t, p.b, "service.selection", map[string]any{"ids": []string{saved.Configuration.ID}}).(map[string]any)
	for _, mode := range []string{"until-stopped", "finite", "invalid"} {
		if _, err := command(p.b, randomID(), "services.start", map[string]any{"ids": []string{saved.Configuration.ID}, "expectedRevision": selection["revision"], "lifetime": mode, "ttlSeconds": 0}); err == nil {
			t.Fatal("invalid temporary lifetime accepted", mode)
		}
	}
	if len(p.b.active) != 0 || savedService(t, p.b, saved.Configuration.ID).Revision != saved.Revision {
		t.Fatal("invalid override mutated permission or definition")
	}
}
