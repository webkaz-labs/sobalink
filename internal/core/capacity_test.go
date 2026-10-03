package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
)

func TestCapacityPolicyApplyRequiresFreshReviewAndPreservesData(t *testing.T) {
	p := newCorePair(t)
	policy := capacity.Defaults()
	policy.Logical["savedServices"] = capacity.Unlimited()
	policy.Resources["materializedListeners"] = capacity.Limited(1024)
	preview := mustCommand(t, p.a, "policy.preview", map[string]any{"policy": policy}).(map[string]any)
	changed := policy.Clone()
	changed.Resources["materializedListeners"] = capacity.Limited(2048)
	changedRaw, _ := json.Marshal(map[string]any{"policy": changed, "expectedRevision": preview["revision"]})
	if _, err := p.a.capacityCommand("policy.apply", changedRaw); err == nil {
		t.Fatal("changed proposal reused another review")
	}
	before := p.a.profileCopy()
	mustCommand(t, p.a, "policy.apply", map[string]any{"policy": policy, "expectedRevision": preview["revision"]})
	if p.a.limit("resources", "materializedListeners") != 1024 || p.a.limit("logical", "savedServices") <= 64 {
		t.Fatal("choices not effective")
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(p.a.profileCopy())
	if string(a) != string(b) {
		t.Fatal("policy apply changed saved data")
	}
	raw, _ := json.Marshal(map[string]any{"policy": capacity.Defaults(), "expectedRevision": preview["revision"]})
	if _, err := p.a.capacityCommand("policy.apply", raw); err == nil {
		t.Fatal("stale policy review accepted")
	}
	loaded, err := readCapacityPolicy(p.a.dir)
	if err != nil || loaded.Number("resources", "materializedListeners") != 1024 {
		t.Fatal("choice not persisted", err)
	}
}

func TestDedicatedProfileStorageExceedsLegacyEnvelope(t *testing.T) {
	dir := t.TempDir()
	c := &Core{dir: dir, capacity: capacity.Defaults(), profile: Profile{Version: 1}}
	p := Profile{Version: 1, Settings: Settings{Network: "none", Locale: "auto", Theme: "system", Hostname: "node"}}
	for i := 0; i < 200; i++ {
		p.Services = append(p.Services, ServiceSpec{Name: strings.Repeat("a", 64), Purpose: strings.Repeat("x", 512)})
	}
	if err := c.writeProfile(p); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "sobalink.json"))
	if err != nil || info.Size() <= 64<<10 {
		t.Fatal("fixture did not exceed old envelope", err)
	}
	loaded, err := ReadProfile(filepath.Join(dir, "sobalink.json"))
	if err != nil || len(loaded.Services) != 200 {
		t.Fatal("larger profile unreadable", err)
	}
}

func TestServiceLifetimesAllowLongAndExplicitPermanentModes(t *testing.T) {
	for _, tc := range []struct {
		mode, direction string
		ttl             int
		ok              bool
	}{{"finite", "share", 7 * 24 * 60 * 60, true}, {"until-revoked", "share", 0, true}, {"until-stopped", "forward", 0, true}, {"until-revoked", "forward", 0, false}, {"until-stopped", "share", 0, false}, {"", "share", 0, false}, {"finite", "share", 0, false}, {"until-revoked", "share", 1, false}, {"finite", "share", int(capacity.MaxDurationSeconds) + 1, false}} {
		_, err := serviceLifetime(tc.mode, tc.ttl, tc.direction)
		if (err == nil) != tc.ok {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := activeService{ctx: ctx, spec: ServiceSpec{Direction: "share", Lifetime: "until-revoked"}}
	if !a.permissionActiveAt(time.Now().Add(365 * 24 * time.Hour)) {
		t.Fatal("permanent grant expired")
	}
	cancel()
	if a.permissionActiveAt(time.Now()) {
		t.Fatal("cancelled permanent grant active")
	}
}

func TestRemoteLifetimeJSONFailsClosed(t *testing.T) {
	base := `"id":"example","network":"tcp","ports":"8080","application":"unverified"`
	for _, suffix := range []string{`,"lifetime":"until-revoked","expiresAt":null`, `,"lifetime":"until-revoked","expiresAt":"0001-01-01T00:00:00Z"`, `,"lifetime":"finite"`, `,"lifetime":null`, `,"lifetime":"unknown"`, `,"lifetime":"finite","lifetime":"until-revoked"`} {
		var service RemoteService
		if json.Unmarshal([]byte("{"+base+suffix+"}"), &service) == nil {
			t.Fatalf("accepted %s", suffix)
		}
	}
	for _, suffix := range []string{`,"lifetime":"until-revoked"`, `,"lifetime":"finite","expiresAt":"` + time.Now().Add(7*24*time.Hour).UTC().Format(time.RFC3339) + `"`} {
		var service RemoteService
		if err := json.Unmarshal([]byte("{"+base+suffix+"}"), &service); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRaisedServiceCountAndLoweringPreserveActiveGrants(t *testing.T) {
	p := newCorePair(t)
	policy := capacity.Defaults()
	policy.Logical["savedServices"] = capacity.Unlimited()
	policy.Logical["rangePolicies"] = capacity.Limited(128)
	apply := func(next capacity.Policy) {
		preview := mustCommand(t, p.b, "policy.preview", map[string]any{"policy": next}).(map[string]any)
		mustCommand(t, p.b, "policy.apply", map[string]any{"policy": next, "expectedRevision": preview["revision"]})
	}
	apply(policy)
	for i := 0; i < 65; i++ {
		mustCommand(t, p.b, "service.share", map[string]any{"name": fmt.Sprintf("service-%d", i), "network": "tcp", "ports": fmt.Sprint(8000 + i), "peerIds": []string{"peer-a"}, "lifetime": "until-revoked"})
	}
	if len(p.b.profileCopy().Services) != 65 || p.b.materializedCount() != 0 {
		t.Fatal("raised logical count failed or compact TCP allocated listeners")
	}
	policy.Logical["savedServices"] = capacity.Limited(1)
	policy.Resources["tcpConnections"] = capacity.Limited(1)
	apply(policy)
	if len(p.b.active) != 65 {
		t.Fatal("lower policy killed existing grants")
	}
	_, err := command(p.b, randomID(), "service.share", map[string]any{"name": "blocked-new", "network": "tcp", "ports": "9000", "peerIds": []string{"peer-a"}, "lifetime": "until-revoked"})
	if err == nil || len(p.b.profileCopy().Services) != 65 {
		t.Fatal("lower policy did not block new admission")
	}
}
