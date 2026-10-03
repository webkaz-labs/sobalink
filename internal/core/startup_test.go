package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func startupFixture(t *testing.T) (*Core, ServiceSpec) {
	t.Helper()
	c, err := Open(context.Background(), Options{Directory: t.TempDir(), SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	p := c.profileCopy()
	p.Settings.Network = "tailnet"
	spec := ServiceSpec{ID: "fixture-service", Name: "example", Backend: "tailnet", Direction: "forward", Network: "tcp", Ports: "443", LocalPort: 8443, LoopbackHost: "127.0.0.1", Lifetime: "finite", TTLSeconds: 3600, PeerID: "peer-b"}
	p.Services = []ServiceSpec{spec}
	if err := c.saveProfile(p); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.profile = p
	c.mu.Unlock()
	return c, spec
}
func saveStartupFixture(t *testing.T, c *Core, s ServiceSpec) StartupReview {
	t.Helper()
	review := mustCommand(t, c, "startup.preview", map[string]any{"name": "example", "ids": []string{s.ID}}).(StartupReview)
	mustCommand(t, c, "startup.save", map[string]any{"name": "example", "ids": []string{s.ID}, "expectedRevision": review.Revision, "expectedStoreRevision": review.StoreRevision})
	return review
}
func TestStartupPrivateOptInAndExactSelection(t *testing.T) {
	c, spec := startupFixture(t)
	if len(c.startup.Entries) != 0 || len(c.startupPending) != 0 {
		t.Fatal("fresh profile opted in")
	}
	review := saveStartupFixture(t, c, spec)
	if len(c.startupPending) != 0 {
		t.Fatal("saving queued an immediate start")
	}
	exported := mustCommand(t, c, "profile.export", map[string]any{})
	data, _ := json.Marshal(exported)
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	if _, ok := fields["startup"]; ok {
		t.Fatal("approval exported")
	}
	if err := c.loadStartup(false); err != nil {
		t.Fatal(err)
	}
	called := 0
	start := func(_ context.Context, name string, raw json.RawMessage) (any, error) {
		called++
		var in serviceSelection
		if err := json.Unmarshal(raw, &in); err != nil {
			t.Fatal(err)
		}
		if name != "services.start" || in.ExpectedRevision != review.SelectionRevision || len(in.IDs) != 1 || in.IDs[0] != spec.ID {
			t.Fatal("scope changed")
		}
		return map[string]any{"ready": true}, nil
	}
	c.runStartupSelections(context.Background(), start)
	c.runStartupSelections(context.Background(), start)
	if called != 1 {
		t.Fatal("launch not exactly once", called)
	}
	if err := c.loadStartup(false); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.profile.Services[0].Ports = "443,8443"
	c.mu.Unlock()
	c.runStartupSelections(context.Background(), start)
	if called != 1 || c.startupStates["example"] != "stale" {
		t.Fatal("edited scope silently expanded")
	}
}
func TestStartupStaleReviewDisableAndOffline(t *testing.T) {
	c, spec := startupFixture(t)
	review := saveStartupFixture(t, c, spec)
	if _, err := command(c, randomID(), "startup.save", map[string]any{"name": "example", "ids": []string{spec.ID}, "expectedRevision": review.Revision, "expectedStoreRevision": review.StoreRevision}); networkErrorCode(err) != "startup_revision_conflict" {
		t.Fatal("stale store accepted", err)
	}
	if err := c.loadStartup(true); err != nil {
		t.Fatal(err)
	}
	c.runStartupSelections(context.Background(), func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("offline launch started")
		return nil, nil
	})
	if err := c.loadStartup(false); err != nil {
		t.Fatal(err)
	}
	revision := c.startupView()["revision"]
	mustCommand(t, c, "startup.disable", map[string]any{"name": "example", "expectedStoreRevision": revision})
	if err := c.loadStartup(false); err != nil {
		t.Fatal(err)
	}
	if len(c.startupPending) != 0 {
		t.Fatal("disable failed to persist")
	}
}
func TestStartupRejectsSharesAndChangedGroups(t *testing.T) {
	c, spec := startupFixture(t)
	c.mu.Lock()
	c.profile.Groups = []ServiceGroup{{Name: "pair", ServiceIDs: []string{spec.ID}}}
	c.mu.Unlock()
	review := mustCommand(t, c, "startup.preview", map[string]any{"name": "pair", "group": "pair"}).(StartupReview)
	mustCommand(t, c, "startup.save", map[string]any{"name": "pair", "group": "pair", "expectedRevision": review.Revision, "expectedStoreRevision": review.StoreRevision})
	if err := c.loadStartup(false); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	other := spec
	other.ID = "other"
	other.Direction = "share"
	c.profile.Services = append(c.profile.Services, other)
	c.profile.Groups[0].ServiceIDs = append(c.profile.Groups[0].ServiceIDs, other.ID)
	c.mu.Unlock()
	if _, err := command(c, randomID(), "startup.preview", map[string]any{"name": "pair", "group": "pair"}); networkErrorCode(err) != "startup_outbound_only" {
		t.Fatal("share accepted", err)
	}
	c.runStartupSelections(context.Background(), func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("changed group started")
		return nil, nil
	})
}
func TestStartupFailureAndStopNeverRestart(t *testing.T) {
	c, spec := startupFixture(t)
	saveStartupFixture(t, c, spec)
	if err := c.loadStartup(false); err != nil {
		t.Fatal(err)
	}
	called := 0
	start := func(context.Context, string, json.RawMessage) (any, error) {
		called++
		return nil, errors.New("synthetic listener failure")
	}
	c.runStartupSelections(context.Background(), start)
	c.runStartupSelections(context.Background(), start)
	if called != 1 || c.startupStates["example"] != "failed" {
		t.Fatal("failed launch retried")
	}
	if err := c.loadStartup(false); err != nil {
		t.Fatal(err)
	}
	c.cancelStartupServices([]string{spec.ID})
	c.runStartupSelections(context.Background(), start)
	if called != 1 {
		t.Fatal("stopped selection restarted")
	}
}
func TestStartupAtomicFailureLeavesApprovalUnchanged(t *testing.T) {
	c, spec := startupFixture(t)
	saveStartupFixture(t, c, spec)
	before := privateRevision(c.startup)
	path := filepath.Join(c.dir, "startup.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := command(c, randomID(), "startup.disable", map[string]any{"name": "example", "expectedStoreRevision": before}); err == nil {
		t.Fatal("write failure accepted")
	}
	if privateRevision(c.startup) != before {
		t.Fatal("write failure mutated memory")
	}
}
