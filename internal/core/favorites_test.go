package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func favoritesList(t *testing.T, c *Core) FavoritesView {
	t.Helper()
	return mustCommand(t, c, "favorites.list", struct{}{}).(FavoritesView)
}

func favoriteChange(t *testing.T, c *Core, action string, ref FavoriteReference, revision string) FavoritesView {
	t.Helper()
	return mustCommand(t, c, "favorites."+action, FavoriteChangeRequest{Reference: ref, ExpectedRevision: revision}).(FavoritesView)
}

func TestFavoritesInertRevisionRestartAndMissingReferences(t *testing.T) {
	c := offlineDefinitionCore(t)
	spec := saveDefinitionFixture(t, c, definitionFixture("example", "tcp", "8080"))
	mustCommand(t, c, "group.save", map[string]any{"group": ServiceGroup{Name: "セット", ServiceIDs: []string{spec.ID}}})
	before, err := os.ReadFile(filepath.Join(c.dir, "sobalink.json"))
	if err != nil {
		t.Fatal(err)
	}
	initial := favoritesList(t, c)
	if initial.Version != 1 || len(initial.Entries) != 0 || len(initial.Revision) != 64 || initial.DurabilityUncertain {
		t.Fatalf("initial: %+v", initial)
	}
	if favoritesList(t, c).Revision != initial.Revision {
		t.Fatal("missing file revision changed")
	}
	if _, err := os.Stat(filepath.Join(c.dir, "favorites.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read created preferences")
	}
	service := FavoriteReference{Kind: "service", ServiceID: spec.ID}
	group := FavoriteReference{Kind: "group", GroupName: "セット"}
	first := favoriteChange(t, c, "add", service, initial.Revision)
	if first.Revision == initial.Revision || !first.Entries[0].Available {
		t.Fatal("addition missing")
	}
	for _, revision := range []string{"", initial.Revision} {
		if _, err := command(c, randomID(), "favorites.add", FavoriteChangeRequest{Reference: group, ExpectedRevision: revision}); networkErrorCode(err) != "favorites_revision_conflict" {
			t.Fatal("stale or missing review accepted", err)
		}
	}
	if got := favoriteChange(t, c, "add", service, first.Revision); !reflect.DeepEqual(got, first) {
		t.Fatal("repeated favorite was not idempotent")
	}
	both := favoriteChange(t, c, "add", group, first.Revision)
	after, _ := os.ReadFile(filepath.Join(c.dir, "sobalink.json"))
	if string(before) != string(after) || len(c.active) != 0 || c.nodeCopy() != nil || savedService(t, c, spec.ID).Revision != serviceRevision(spec) {
		t.Fatal("favorite altered service scope or runtime")
	}
	for _, name := range []string{"startup.json", "saved-proxies.json", "startup-revocations.json", "direct-lan.json"} {
		if _, err := os.Stat(filepath.Join(c.dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("favorite created private authority", name, err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Options{Directory: c.dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := favoritesList(t, reopened); !reflect.DeepEqual(got, both) || len(reopened.active) != 0 {
		t.Fatal("restart changed or activated favorites")
	}
	mustCommand(t, reopened, "service.delete", map[string]any{"id": spec.ID, "expectedRevision": serviceRevision(spec), "expectedProfileRevision": definitionsRevision(reopened.profileCopy()), "removeFromGroups": true})
	missing := favoritesList(t, reopened)
	if missing.Revision != both.Revision || len(missing.Entries) != 2 || missing.Entries[0].Available || missing.Entries[1].Available {
		t.Fatal("deleted definitions were pruned or shown available")
	}
	if _, err := command(reopened, randomID(), "favorites.add", FavoriteChangeRequest{Reference: service, ExpectedRevision: missing.Revision}); networkErrorCode(err) != "favorites_target_missing" {
		t.Fatal("missing target re-added", err)
	}
	remaining := favoriteChange(t, reopened, "remove", service, missing.Revision)
	empty := favoriteChange(t, reopened, "remove", group, remaining.Revision)
	if len(empty.Entries) != 0 || empty.Revision == initial.Revision {
		t.Fatal("preference revision allowed ABA")
	}
}

func TestFavoritesRejectMalformedOversizedAndFutureStoreOnly(t *testing.T) {
	valid := favoritesStore{Version: 1, Revision: strings.Repeat("a", 64), Entries: []FavoriteReference{{Kind: "service", ServiceID: "missing-id"}}}
	encoded, _ := json.Marshal(valid)
	cases := map[string]string{
		"malformed": `{`, "empty": `{}`, "null": `null`, "future": strings.Replace(string(encoded), `"version":1`, `"version":2`, 1),
		"unknown":  strings.Replace(string(encoded), `"version":1`, `"version":1,"grant":true`, 1),
		"trailing": string(encoded) + ` {}`, "null-entries": `{"version":1,"revision":"` + valid.Revision + `","entries":null}`,
		"bad-revision":    strings.Replace(string(encoded), valid.Revision, "invalid", 1),
		"duplicate":       `{"version":1,"revision":"` + valid.Revision + `","entries":[{"kind":"service","serviceId":"a"},{"kind":"service","serviceId":"a"}]}`,
		"mixed-reference": `{"version":1,"revision":"` + valid.Revision + `","entries":[{"kind":"service","serviceId":"a","groupName":"g"}]}`,
		"invalid-kind":    `{"version":1,"revision":"` + valid.Revision + `","entries":[{"kind":"startup","serviceId":"a"}]}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "favorites.json"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
			if err != nil {
				t.Fatal("preferences blocked startup", err)
			}
			defer c.Close()
			if _, err := command(c, randomID(), "favorites.list", struct{}{}); networkErrorCode(err) != "favorites_unavailable" {
				t.Fatal("invalid preferences accepted", err)
			}
			if _, err := command(c, randomID(), "group.list", struct{}{}); err != nil {
				t.Fatal("preferences blocked unrelated read", err)
			}
			current, _ := os.ReadFile(filepath.Join(dir, "favorites.json"))
			if string(current) != data {
				t.Fatal("invalid preferences silently rewritten")
			}
		})
	}
	c := offlineDefinitionCore(t)
	c.mu.Lock()
	c.capacity.Resources["profileBytes"] = capacity.Limited(1024)
	c.mu.Unlock()
	path := filepath.Join(c.dir, "favorites.json")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", 1025)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := command(c, randomID(), "favorites.list", struct{}{}); networkErrorCode(err) != "favorites_unavailable" {
		t.Fatal("oversized preferences accepted", err)
	}
}

func TestFavoritesUncertainPublicationReconcilesActualFileAndRetainsWarning(t *testing.T) {
	c := offlineDefinitionCore(t)
	spec := saveDefinitionFixture(t, c, definitionFixture("example", "tcp", "8080"))
	ref := FavoriteReference{Kind: "service", ServiceID: spec.ID}
	initial := favoritesList(t, c)
	var calls int
	c.atomicWrite = func(path string, data []byte) error {
		calls++
		if filepath.Base(path) != "favorites.json" {
			t.Fatal("unexpected persistence target")
		}
		if err := config.AtomicWrite(path, data); err != nil {
			return err
		}
		return fmt.Errorf("%w: %w", config.ErrAtomicCommitted, &os.PathError{Op: "sync", Path: path, Err: os.ErrPermission})
	}
	value, err := command(c, randomID(), "favorites.add", FavoriteChangeRequest{Reference: ref, ExpectedRevision: initial.Revision})
	if networkErrorCode(err) != "favorites_persistence_uncertain" || !errors.Is(err, config.ErrAtomicCommitted) || strings.Contains(err.Error(), c.dir) {
		t.Fatal("publication outcome lost or path leaked", err)
	}
	view := value.(FavoritesView)
	if !view.DurabilityUncertain || len(view.Entries) != 1 || !reflect.DeepEqual(view, favoritesList(t, c)) {
		t.Fatal("published state rolled back")
	}
	c.atomicWrite = func(string, []byte) error { return config.ErrAtomicRecovery }
	_, err = command(c, randomID(), "favorites.remove", FavoriteChangeRequest{Reference: ref, ExpectedRevision: view.Revision})
	if !errors.Is(err, config.ErrAtomicRecovery) || !reflect.DeepEqual(view, favoritesList(t, c)) {
		t.Fatal("failed later write cleared publication uncertainty", err)
	}
	c.atomicWrite = nil
	confirmed := favoriteChange(t, c, "add", ref, view.Revision)
	if confirmed.DurabilityUncertain || confirmed.Revision == view.Revision || len(confirmed.Entries) != 1 || calls != 1 {
		t.Fatal("whole-store retry did not confirm publication")
	}
	// Inject a published value different from the attempted write: reconciliation
	// must use the file, not manufacture an in-memory success for the request.
	published := favoritesStore{Version: 1, Revision: strings.Repeat("b", 64), Entries: []FavoriteReference{}}
	c.atomicWrite = func(path string, _ []byte) error {
		data, _ := json.Marshal(published)
		if err := config.AtomicWrite(path, data); err != nil {
			return err
		}
		return config.ErrAtomicCommitted
	}
	value, err = command(c, randomID(), "favorites.remove", FavoriteChangeRequest{Reference: ref, ExpectedRevision: confirmed.Revision})
	if !errors.Is(err, config.ErrAtomicCommitted) || value.(FavoritesView).Revision != published.Revision || len(value.(FavoritesView).Entries) != 0 {
		t.Fatal("uncertain write was not reconciled from disk")
	}
}

func TestFavoritesConcurrentReviewAllowsOneMutation(t *testing.T) {
	c := offlineDefinitionCore(t)
	a := saveDefinitionFixture(t, c, definitionFixture("first", "tcp", "8080"))
	b := saveDefinitionFixture(t, c, definitionFixture("second", "tcp", "8081"))
	revision := favoritesList(t, c).Revision
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{a.ID, b.ID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := command(c, randomID(), "favorites.add", FavoriteChangeRequest{Reference: FavoriteReference{Kind: "service", ServiceID: id}, ExpectedRevision: revision})
			results <- err
		}(id)
	}
	wg.Wait()
	close(results)
	succeeded, conflicts := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if networkErrorCode(err) == "favorites_revision_conflict" {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicts != 1 || len(favoritesList(t, c).Entries) != 1 {
		t.Fatal("concurrent stale review accepted")
	}
}

func TestFavoritesCapacityNoFixedCountAndPrivateUsage(t *testing.T) {
	c := offlineDefinitionCore(t)
	store := emptyFavorites()
	for i := 0; i < 1000; i++ {
		store.Entries = append(store.Entries, FavoriteReference{Kind: "service", ServiceID: fmt.Sprintf("missing-%d", i)})
	}
	data, _ := json.Marshal(store)
	if err := config.AtomicWrite(filepath.Join(c.dir, "favorites.json"), append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	if len(favoritesList(t, c).Entries) != 1000 {
		t.Fatal("arbitrary favorite count cap")
	}
	usage := c.capacityUsage()
	if usage["favoritesBytes"] != int64(len(data)+1) || usage["profileBytes"] < usage["favoritesBytes"] {
		t.Fatal("favorite bytes not accounted")
	}
	policy := c.capacityPolicy()
	policy.Resources["profileBytes"] = capacity.Limited(1024)
	if _, err := command(c, randomID(), "policy.preview", map[string]any{"policy": policy}); networkErrorCode(err) != "policy_in_use" {
		t.Fatal("capacity reduction strands favorites", err)
	}
	c.mu.Lock()
	c.capacity.Resources["profileBytes"] = capacity.Limited(int64(len(data) + 1))
	c.mu.Unlock()
	view := favoritesList(t, c)
	spec := ServiceSpec{ID: "new-service"}
	c.mu.Lock()
	c.profile.Services = append(c.profile.Services, spec)
	c.mu.Unlock()
	if _, err := command(c, randomID(), "favorites.add", FavoriteChangeRequest{Reference: FavoriteReference{Kind: "service", ServiceID: spec.ID}, ExpectedRevision: view.Revision}); networkErrorCode(err) != "favorites_capacity" {
		t.Fatal("oversized write accepted", err)
	}
	if got := favoritesList(t, c); got.Revision != view.Revision || len(got.Entries) != 1000 {
		t.Fatal("rejected write changed preferences")
	}
}

func TestFavoritesInvalidRequestsCancelAndExactReferences(t *testing.T) {
	c := offlineDefinitionCore(t)
	spec := saveDefinitionFixture(t, c, definitionFixture("example", "tcp", "8080"))
	mustCommand(t, c, "group.save", map[string]any{"group": ServiceGroup{Name: "Exact", ServiceIDs: []string{spec.ID}}})
	view := favoritesList(t, c)
	for _, ref := range []FavoriteReference{
		{Kind: "service", ServiceID: spec.ID, GroupName: "Exact"}, {Kind: "service", ServiceID: " bad "},
		{Kind: "group", GroupName: ""}, {Kind: "peer", ServiceID: spec.ID}, {Kind: "group", GroupName: strings.Repeat("a", 65)},
	} {
		if _, err := command(c, randomID(), "favorites.add", FavoriteChangeRequest{Reference: ref, ExpectedRevision: view.Revision}); networkErrorCode(err) != "favorites_invalid_request" {
			t.Fatal("invalid reference accepted", err)
		}
	}
	for _, ref := range []FavoriteReference{{Kind: "service", ServiceID: "missing"}, {Kind: "group", GroupName: "exact"}} {
		if _, err := command(c, randomID(), "favorites.add", FavoriteChangeRequest{Reference: ref, ExpectedRevision: view.Revision}); networkErrorCode(err) != "favorites_target_missing" {
			t.Fatal("missing or case-folded target accepted", err)
		}
		if got := favoriteChange(t, c, "remove", ref, view.Revision); got.Revision != view.Revision || len(got.Entries) != 0 {
			t.Fatal("removing an absent preference was not idempotent")
		}
	}
	for _, raw := range []string{`null`, `{} {}`, `{"unexpected":true}`} {
		if _, err := c.favoritesCommand("favorites.list", json.RawMessage(raw)); networkErrorCode(err) != "favorites_invalid_request" {
			t.Fatal("invalid list input accepted", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, _ := json.Marshal(FavoriteChangeRequest{Reference: FavoriteReference{Kind: "service", ServiceID: spec.ID}, ExpectedRevision: view.Revision})
	if _, err := c.Command(ctx, webui.Command{RequestID: randomID(), Name: "favorites.add", Payload: raw}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled edit applied", err)
	}
	if got := favoritesList(t, c); !reflect.DeepEqual(got, view) {
		t.Fatal("invalid request altered preferences")
	}
}

func TestFavoritesCommittedReadFailureKeepsUncertainty(t *testing.T) {
	c := offlineDefinitionCore(t)
	spec := saveDefinitionFixture(t, c, definitionFixture("example", "tcp", "8080"))
	view := favoritesList(t, c)
	c.atomicWrite = func(path string, _ []byte) error {
		if err := config.AtomicWrite(path, []byte(`{"version":2}`)); err != nil {
			return err
		}
		return config.ErrAtomicCommitted
	}
	_, err := command(c, randomID(), "favorites.add", FavoriteChangeRequest{Reference: FavoriteReference{Kind: "service", ServiceID: spec.ID}, ExpectedRevision: view.Revision})
	if !errors.Is(err, config.ErrAtomicCommitted) || networkErrorCode(err) != "favorites_persistence_uncertain" || !c.favoritesUncertain {
		t.Fatal("reconciliation failure erased publication outcome", err)
	}
	// Explicitly repair only the test store, then verify read-only observation
	// does not certify a previous uncertain write as durable.
	data, _ := json.Marshal(emptyFavorites())
	if err := config.AtomicWrite(filepath.Join(c.dir, "favorites.json"), data); err != nil {
		t.Fatal(err)
	}
	if !favoritesList(t, c).DurabilityUncertain {
		t.Fatal("read silently cleared uncertainty")
	}
}

func TestFavoritesPreserveExistingStartupApprovalAndActiveLifetime(t *testing.T) {
	c, spec := startupFixture(t)
	saveStartupFixture(t, c, spec)
	before, err := os.ReadFile(filepath.Join(c.dir, "startup.json"))
	if err != nil {
		t.Fatal(err)
	}
	approvalBefore := c.startupView()
	profileBefore := c.profileCopy()
	deadline := time.Now().Add(time.Hour)
	lease := time.Now().Add(30 * time.Minute)
	active := &activeService{spec: spec, expires: deadline, leaseSeconds: 30, ctx: context.Background()}
	active.leaseExpires.Store(&lease)
	c.mu.Lock()
	c.active[spec.ID] = active
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.active, spec.ID); c.mu.Unlock() }()
	initial := favoritesList(t, c)
	ref := FavoriteReference{Kind: "service", ServiceID: spec.ID}
	marked := favoriteChange(t, c, "add", ref, initial.Revision)
	favoriteChange(t, c, "remove", ref, marked.Revision)
	after, err := os.ReadFile(filepath.Join(c.dir, "startup.json"))
	if err != nil || string(before) != string(after) || !reflect.DeepEqual(approvalBefore, c.startupView()) || !reflect.DeepEqual(profileBefore, c.profileCopy()) {
		t.Fatal("favorite mutated existing approval or scope", err)
	}
	c.mu.RLock()
	unchanged := c.active[spec.ID] == active && active.expires.Equal(deadline) && active.leaseExpires.Load().Equal(lease)
	c.mu.RUnlock()
	if !unchanged || len(c.startupPending) != 0 {
		t.Fatal("favorite changed active lifetime or queued startup")
	}
}

func TestFavoritesGroupReuseStillRequiresCurrentScopeReview(t *testing.T) {
	c := offlineDefinitionCore(t)
	a := saveDefinitionFixture(t, c, definitionFixture("first", "tcp", "8080"))
	b := saveDefinitionFixture(t, c, definitionFixture("second", "tcp", "8081"))
	mustCommand(t, c, "group.save", map[string]any{"group": ServiceGroup{Name: "example-set", ServiceIDs: []string{a.ID}}})
	selection := mustCommand(t, c, "service.selection", map[string]any{"group": "example-set"}).(map[string]any)
	favorite := favoriteChange(t, c, "add", FavoriteReference{Kind: "group", GroupName: "example-set"}, favoritesList(t, c).Revision)
	mustCommand(t, c, "group.save", map[string]any{"group": ServiceGroup{Name: "example-set", ServiceIDs: []string{b.ID}}, "expectedRevision": definitionsRevision(c.profileCopy())})
	if got := favoritesList(t, c); got.Revision != favorite.Revision || len(got.Entries) != 1 || !got.Entries[0].Available {
		t.Fatal("favorite copied old membership or pruned reused group")
	}
	for _, revision := range []any{selection["revision"], favorite.Revision} {
		if _, err := command(c, randomID(), "services.start", map[string]any{"group": "example-set", "expectedRevision": revision}); networkErrorCode(err) != "service_revision_conflict" {
			t.Fatal("favorite bypassed complete scope review", err)
		}
	}
	if len(c.active) != 0 || c.nodeCopy() != nil {
		t.Fatal("favorite activated reused group")
	}
}
