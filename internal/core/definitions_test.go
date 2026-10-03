package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
)

func offlineDefinitionCore(t *testing.T) *Core {
	t.Helper()
	c, err := Open(context.Background(), Options{Directory: t.TempDir(), SkipNetworkStart: true, NodeFactory: func(string, string) (NetworkBackend, error) {
		t.Fatal("offline definition activated a network")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func definitionFixture(name, protocol, ports string) ServiceSpec {
	return ServiceSpec{Backend: "tailnet", Name: name, Direction: "share", Network: protocol, Ports: ports, PeerIDs: []string{"peer-a"}, Lifetime: "until-revoked", LoopbackHost: "127.0.0.1", Purpose: "custom"}
}

func saveDefinitionFixture(t *testing.T, c *Core, spec ServiceSpec) ServiceSpec {
	t.Helper()
	return mustCommand(t, c, "service.save", map[string]any{"configuration": spec}).(SavedServiceConfiguration).Configuration
}

func TestDefinitionsSaveOfflineReopenAndReplaceRevision(t *testing.T) {
	c := offlineDefinitionCore(t)
	spec := saveDefinitionFixture(t, c, definitionFixture("offline-share", "tcp", "8080"))
	if len(c.active) != 0 || c.nodeCopy() != nil || spec.ID == "" {
		t.Fatal("save activated transport")
	}
	oldRevision := serviceRevision(spec)
	spec.Ports = "8081"
	if _, err := command(c, randomID(), "service.save", map[string]any{"configuration": spec}); err == nil {
		t.Fatal("unguarded replacement accepted")
	}
	updated := mustCommand(t, c, "service.save", map[string]any{"configuration": spec, "expectedRevision": oldRevision}).(SavedServiceConfiguration)
	if updated.Active || updated.Revision == oldRevision {
		t.Fatal("save replacement started or lost revision")
	}
	if _, err := command(c, randomID(), "service.save", map[string]any{"configuration": spec, "expectedRevision": oldRevision}); err == nil {
		t.Fatal("stale replacement accepted")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Options{Directory: c.dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := savedService(t, reopened, spec.ID); got.Active || !reflect.DeepEqual(got.Configuration, updated.Configuration) {
		t.Fatal("offline reopen changed or activated saved definition")
	}
}

func TestDefinitionExportImportExcludesPrivateStateAndRequiresExactReview(t *testing.T) {
	c := offlineDefinitionCore(t)
	spec := saveDefinitionFixture(t, c, definitionFixture("export-example", "tcp", "8080"))
	mustCommand(t, c, "group.save", map[string]any{"group": ServiceGroup{Name: "example-group", ServiceIDs: []string{spec.ID}}})
	export := mustCommand(t, c, "profile.export", map[string]any{}).(map[string]any)
	bundle := export["profile"].(DefinitionBundle)
	raw, _ := json.Marshal(bundle)
	for _, key := range []string{"hostname", "settings", "trusted", "receiveDirectory", "owner", "lease", "secret", "token", "generation"} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("private state leaked: %s", key)
		}
	}
	destination := offlineDefinitionCore(t)
	identity := destination.profileCopy().Settings
	preview := mustCommand(t, destination, "profile.import.preview", map[string]any{"profile": bundle}).(map[string]any)
	changed := bundle
	changed.Services = append([]ServiceSpec(nil), bundle.Services...)
	changed.Services[0].Ports = "9000"
	if _, err := command(destination, randomID(), "profile.import", map[string]any{"profile": changed, "expectedRevision": preview["revision"]}); err == nil {
		t.Fatal("changed incoming payload reused old review")
	}
	mustCommand(t, destination, "profile.import", map[string]any{"profile": bundle, "expectedRevision": preview["revision"]})
	if destination.profileCopy().Settings != identity || len(destination.active) != 0 || destination.nodeCopy() != nil || len(destination.profileCopy().Groups) != 1 {
		t.Fatal("import changed identity or activated services")
	}
	preview = mustCommand(t, destination, "profile.import.preview", map[string]any{"profile": bundle}).(map[string]any)
	saveDefinitionFixture(t, destination, definitionFixture("newer-example", "tcp", "8081"))
	if _, err := command(destination, randomID(), "profile.import", map[string]any{"profile": bundle, "expectedRevision": preview["revision"]}); err == nil {
		t.Fatal("changed destination accepted stale review")
	}
	privateInput := []byte(`{"profile":{"version":1,"services":[],"groups":[],"authKey":"example"}}`)
	if _, err := destination.profileDefinitionsCommand("profile.import.preview", privateInput); err == nil {
		t.Fatal("unknown credential field accepted")
	}
}

func TestDefinitionDeletionExplicitActiveAndGroupEffects(t *testing.T) {
	p := newCorePair(t)
	spec := saveDefinitionFixture(t, p.b, definitionFixture("delete-example", "tcp", "8080"))
	mustCommand(t, p.b, "group.save", map[string]any{"group": ServiceGroup{Name: "example-group", ServiceIDs: []string{spec.ID}}})
	selection := mustCommand(t, p.b, "service.selection", map[string]any{"ids": []string{spec.ID}}).(map[string]any)
	mustCommand(t, p.b, "services.start", map[string]any{"ids": []string{spec.ID}, "expectedRevision": selection["revision"]})
	input := map[string]any{"id": spec.ID, "expectedRevision": serviceRevision(spec)}
	if _, err := command(p.b, randomID(), "service.delete", input); err == nil {
		t.Fatal("active deletion silently stopped service")
	}
	input["stopActive"] = true
	if _, err := command(p.b, randomID(), "service.delete", input); err == nil {
		t.Fatal("group reference silently removed")
	}
	input["removeFromGroups"], input["expectedProfileRevision"] = true, definitionsRevision(p.b.profileCopy())
	mustCommand(t, p.b, "service.delete", input)
	if len(p.b.profileCopy().Services) != 0 || len(p.b.profileCopy().Groups) != 0 || len(p.b.active) != 0 || p.b.nodeCopy() == nil {
		t.Fatal("explicit deletion did not clean reference or stopped node")
	}
}

func TestGroupCapacityLoweringRetainsDefinitionsAndRejectsGrowth(t *testing.T) {
	c := offlineDefinitionCore(t)
	a := saveDefinitionFixture(t, c, definitionFixture("first", "tcp", "8080"))
	b := saveDefinitionFixture(t, c, definitionFixture("second", "tcp", "8081"))
	mustCommand(t, c, "group.save", map[string]any{"group": ServiceGroup{Name: "one", ServiceIDs: []string{a.ID, b.ID}}})
	policy := capacity.Defaults()
	policy.Logical["groups"] = capacity.Limited(1)
	policy.Logical["groupMembers"] = capacity.Limited(1)
	preview := mustCommand(t, c, "policy.preview", map[string]any{"policy": policy}).(map[string]any)
	mustCommand(t, c, "policy.apply", map[string]any{"policy": policy, "expectedRevision": preview["revision"]})
	if _, err := command(c, randomID(), "group.save", map[string]any{"group": ServiceGroup{Name: "two", ServiceIDs: []string{a.ID}}}); err == nil {
		t.Fatal("new group exceeded lower budget")
	}
	mustCommand(t, c, "group.save", map[string]any{"group": ServiceGroup{Name: "one", ServiceIDs: []string{b.ID, a.ID}}, "expectedRevision": definitionsRevision(c.profileCopy())})
	if len(c.profileCopy().Groups[0].ServiceIDs) != 2 {
		t.Fatal("lowering removed retained members")
	}
}

func TestDefinitionBundleReadRejectsUnknownAndOversizedInput(t *testing.T) {
	c := offlineDefinitionCore(t)
	path := filepath.Join(t.TempDir(), "definitions.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"services":[],"groups":[],"password":"example"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDefinitionBundle(path, c.dir); err == nil {
		t.Fatal("unrecognized state imported")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", int(c.limit("resources", "profileBytes"))+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDefinitionBundle(path, c.dir); err == nil {
		t.Fatal("oversized import read")
	}
}

func TestServiceGroupsRollbackKeepExistingAndTaskLeasesFailClosed(t *testing.T) {
	p := newCorePair(t)
	prior := saveDefinitionFixture(t, p.b, definitionFixture("existing", "tcp", "8070"))
	one := saveDefinitionFixture(t, p.b, definitionFixture("first", "tcp", "8080"))
	two := saveDefinitionFixture(t, p.b, definitionFixture("second", "udp", "8081"))
	start := func(ids []string, owner string) (any, error) {
		selection := mustCommand(t, p.b, "service.selection", map[string]any{"ids": ids}).(map[string]any)
		input := map[string]any{"ids": ids, "expectedRevision": selection["revision"], "owner": owner}
		if owner != "" {
			input["leaseSeconds"] = 30
		}
		return command(p.b, randomID(), "services.start", input)
	}
	if _, err := start([]string{prior.ID}, ""); err != nil {
		t.Fatal(err)
	}
	before := p.b.profileCopy()
	if _, err := start([]string{prior.ID, one.ID, two.ID}, ""); err == nil {
		t.Fatal("mock UDP failure did not fail group")
	}
	if len(p.b.active) != 1 || p.b.active[prior.ID] == nil || !reflect.DeepEqual(before, p.b.profileCopy()) {
		t.Fatal("rollback changed existing work or durable definitions")
	}
	value, err := start([]string{one.ID}, "task-example")
	if err != nil || value.(map[string]any)["ready"] != true {
		t.Fatal("owned start not ready", err)
	}
	active := p.b.active[one.ID]
	if active.owner != "task-example" || active.leaseExpires.Load() == nil || !active.permissionActiveAt(time.Now()) {
		t.Fatal("task grant missing lease")
	}
	if _, err := command(p.b, randomID(), "services.stop", map[string]any{"ids": []string{one.ID}}); err == nil {
		t.Fatal("manual selected stop took task ownership")
	}
	if _, err := command(p.b, randomID(), "services.renew", map[string]any{"ids": []string{one.ID}, "owner": "another-task", "leaseSeconds": 30}); err == nil {
		t.Fatal("wrong owner renewed task")
	}
	previousLease := *active.leaseExpires.Load()
	mustCommand(t, p.b, "services.renew", map[string]any{"ids": []string{one.ID}, "owner": "task-example", "leaseSeconds": 30})
	if active.leaseExpires.Load().Before(previousLease) {
		t.Fatal("renewal shortened lease")
	}
	expired := time.Now().Add(-time.Second)
	active.leaseExpires.Store(&expired)
	if active.permissionActiveAt(time.Now()) {
		t.Fatal("unlimited service ignored task expiry")
	}
	if _, err := command(p.b, randomID(), "services.renew", map[string]any{"ids": []string{one.ID}, "owner": "task-example", "leaseSeconds": 30}); err == nil {
		t.Fatal("expired task resurrected by renewal")
	}
	withServiceOperation(p.b, func() { p.b.expireServices() })
	if savedService(t, p.b, one.ID).Active || !savedService(t, p.b, prior.ID).Active {
		t.Fatal("abandonment cleanup affected unrelated work")
	}
	forward := definitionFixture("outbound", "tcp", "8090")
	forward.Direction, forward.Lifetime, forward.PeerID, forward.PeerIDs = "forward", "until-stopped", "peer-a", nil
	forward = saveDefinitionFixture(t, p.b, forward)
	forwardContext, cancelForward := context.WithCancel(p.b.ctx)
	defer cancelForward()
	// A no-socket transport stand-in isolates share-only revocation semantics.
	p.b.mu.Lock()
	p.b.active[forward.ID] = &activeService{spec: forward, ctx: forwardContext, cancel: cancelForward}
	p.b.mu.Unlock()
	result := mustCommand(t, p.b, "service.stop-shares", map[string]any{}).(map[string]any)
	if result["nodeRunning"] != true || len(p.b.active) != 1 || p.b.active[forward.ID] == nil || p.b.nodeCopy() == nil {
		t.Fatal("share stop closed node or outbound connection")
	}
}

func TestServiceSelectionStopReviewAndOwnedDeletedCleanup(t *testing.T) {
	p := newCorePair(t)
	a := saveDefinitionFixture(t, p.b, definitionFixture("first", "tcp", "8080"))
	b := saveDefinitionFixture(t, p.b, definitionFixture("second", "tcp", "8081"))
	mustCommand(t, p.b, "group.save", map[string]any{"group": ServiceGroup{Name: "example", ServiceIDs: []string{a.ID}}})
	review := mustCommand(t, p.b, "service.selection", map[string]string{"group": "example"}).(map[string]any)
	mustCommand(t, p.b, "services.start", map[string]any{"group": "example", "expectedRevision": review["revision"]})
	mustCommand(t, p.b, "group.save", map[string]any{"group": ServiceGroup{Name: "example", ServiceIDs: []string{b.ID}}, "expectedRevision": definitionsRevision(p.b.profileCopy())})
	if _, err := command(p.b, randomID(), "services.stop", map[string]any{"group": "example", "expectedRevision": review["revision"]}); err == nil || !savedService(t, p.b, a.ID).Active {
		t.Fatal("changed group redirected an old stop review")
	}
	selection := mustCommand(t, p.b, "service.selection", map[string]any{"ids": []string{b.ID}}).(map[string]any)
	mustCommand(t, p.b, "services.start", map[string]any{"ids": []string{b.ID}, "expectedRevision": selection["revision"], "owner": "task-example", "leaseSeconds": 30})
	if view := mustCommand(t, p.b, "services.ready", map[string]any{"ids": []string{b.ID}}).(map[string]any); view["ready"] != true {
		t.Fatal("read-only readiness could not observe a task-owned service")
	}
	export := mustCommand(t, p.b, "profile.export", map[string]any{}).(map[string]any)
	raw, _ := json.Marshal(export["profile"])
	if strings.Contains(string(raw), "task-example") || strings.Contains(string(raw), "lease") {
		t.Fatal("task runtime state entered an export")
	}
	mustCommand(t, p.b, "services.stop", map[string]any{"ids": []string{"deleted-example", b.ID}, "owner": "task-example"})
	if savedService(t, p.b, b.ID).Active || !savedService(t, p.b, a.ID).Active {
		t.Fatal("deleted entry prevented owned cleanup or stopped unrelated work")
	}
}
