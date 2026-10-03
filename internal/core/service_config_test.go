package core

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func savedService(t *testing.T, c *Core, id string) SavedServiceConfiguration {
	t.Helper()
	return mustCommand(t, c, "service.config", map[string]string{"id": id}).(SavedServiceConfiguration)
}

func serviceConfigArgs(name string) map[string]any {
	return map[string]any{"name": name, "network": "tcp", "ports": "8000-8010", "excludePorts": "8002,8004", "peerIds": []string{"peer-a"}, "ttlSeconds": 137, "purpose": "review", "discoverable": true}
}

func TestSavedServiceConfigurationIsExactLocalAndDoesNotRenew(t *testing.T) {
	p := newCorePair(t)
	args := serviceConfigArgs("full-rule")
	value := mustCommand(t, p.b, "service.share", args).(map[string]any)
	id := value["id"].(string)
	before, _ := os.ReadFile(filepath.Join(p.b.dir, "sobalink.json"))
	saved := savedService(t, p.b, id)
	spec := saved.Configuration
	if !saved.Active || len(saved.Revision) != 64 || spec.Backend != "tailnet" || spec.Direction != "share" || spec.ExcludePorts != "8002,8004" || spec.Ports != "8000-8010" || spec.TTLSeconds != 137 || spec.Purpose != "review" || !spec.Discoverable || !reflect.DeepEqual(spec.PeerIDs, []string{"peer-a"}) {
		t.Fatalf("incomplete saved configuration: %+v", saved)
	}
	again := savedService(t, p.b, id)
	after, _ := os.ReadFile(filepath.Join(p.b.dir, "sobalink.json"))
	if !reflect.DeepEqual(saved, again) || !bytes.Equal(before, after) || p.b.serviceViews()[0]["expiresAt"] != value["expiresAt"] {
		t.Fatal("reading saved configuration changed persistence or active lifetime")
	}
	public, _ := json.Marshal(p.b.permittedServices("peer-a"))
	for _, local := range []string{"backend", "excludePorts", "ttlSeconds", "purpose", "discoverable", "revision", "peerIds"} {
		if strings.Contains(string(public), local) {
			t.Fatalf("local saved field %s leaked through peer discovery", local)
		}
	}
	if _, err := command(p.b, randomID(), "service.config", map[string]string{"id": "missing"}); networkErrorCode(err) != "service_not_found" {
		t.Fatal("missing saved configuration was not explicit")
	}
}

func TestServiceCreateReplaceAndCopyRequireExactReview(t *testing.T) {
	p := newCorePair(t)
	args := serviceConfigArgs("reviewed-rule")
	id := mustCommand(t, p.b, "service.share", args).(map[string]any)["id"].(string)
	saved := savedService(t, p.b, id)
	args["replaceId"], args["expectedRevision"], args["backend"] = id, saved.Revision, "tailnet"
	if _, err := command(p.b, randomID(), "service.share", args); networkErrorCode(err) != "service_active" || !savedService(t, p.b, id).Active {
		t.Fatal("editing an active rule stopped or replaced it")
	}
	mustCommand(t, p.b, "service.stop", map[string]string{"id": id})
	if _, err := command(p.b, randomID(), "service.share", serviceConfigArgs("reviewed-rule")); networkErrorCode(err) != "service_name_conflict" {
		t.Fatal("ordinary create silently replaced a stopped rule")
	}
	args["name"] = "revised-rule"
	args["ttlSeconds"] = 241
	value := mustCommand(t, p.b, "service.share", args).(map[string]any)
	if value["id"] != id {
		t.Fatal("explicit replace lost its saved identity")
	}
	updated := savedService(t, p.b, id)
	if updated.Revision == saved.Revision || updated.Configuration.TTLSeconds != 241 {
		t.Fatal("replacement revision did not cover the changed lifetime")
	}
	mustCommand(t, p.b, "service.stop", map[string]string{"id": id})
	if _, err := command(p.b, randomID(), "service.share", args); networkErrorCode(err) != "service_revision_conflict" {
		t.Fatal("stale configuration review replaced newer saved fields")
	}
	if savedService(t, p.b, id).Active {
		t.Fatal("stale edit activated the rule")
	}
	copyArgs := serviceConfigArgs("copied-rule")
	copyID := mustCommand(t, p.b, "service.share", copyArgs).(map[string]any)["id"].(string)
	if copyID == id || savedService(t, p.b, id).Revision != updated.Revision {
		t.Fatal("create-copy overwrote its source")
	}
}

func TestServiceBackendContextIsPreservedAndLegacyIsExplicit(t *testing.T) {
	for _, original := range []string{"lan", ""} {
		t.Run("original-"+original, func(t *testing.T) {
			p := newCorePair(t)
			args := serviceConfigArgs("backend-rule")
			id := mustCommand(t, p.b, "service.share", args).(map[string]any)["id"].(string)
			mustCommand(t, p.b, "service.stop", map[string]string{"id": id})
			p.b.mu.Lock()
			p.b.profile.Services[0].Backend = original
			p.b.mu.Unlock()
			saved := savedService(t, p.b, id)
			if saved.Configuration.Backend != original {
				t.Fatal("saved backend context was inferred from the active network")
			}
			args["replaceId"], args["expectedRevision"] = id, saved.Revision
			if _, err := command(p.b, randomID(), "service.share", args); networkErrorCode(err) != "service_backend_required" {
				t.Fatal("replacement did not require explicit backend review")
			}
			args["backend"] = "tailnet"
			_, err := command(p.b, randomID(), "service.share", args)
			if original == "lan" {
				if networkErrorCode(err) != "service_backend_mismatch" || savedService(t, p.b, id).Active {
					t.Fatal("known different backend was silently reassigned")
				}
			} else if err != nil || savedService(t, p.b, id).Configuration.Backend != "tailnet" {
				t.Fatal("explicit legacy backend review could not start the rule", err)
			}
		})
	}
}

func TestConcurrentServiceCreatesCannotOverwriteByName(t *testing.T) {
	p := newCorePair(t)
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := command(p.b, randomID(), "service.share", serviceConfigArgs("same-generated-name"))
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	success, conflict := 0, 0
	for err := range errors {
		if err == nil {
			success++
		} else if networkErrorCode(err) == "service_name_conflict" {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 || len(p.b.profileCopy().Services) != 1 {
		t.Fatal("concurrent creates were not serialized with the name guard")
	}
}

func TestSavedForwardConfigurationRetainsSourceAcrossOfflineReopen(t *testing.T) {
	c := openLANTestCore(t)
	p := c.profileCopy()
	spec := ServiceSpec{ID: "saved-forward", Backend: "lan", Name: "forward-example", Direction: "forward", Network: "tcp", Ports: "8000-8010", ExcludePorts: "8005", LocalPort: 18000, PeerID: strings.Repeat("b", 64), TTLSeconds: 321, Purpose: "custom", ServiceID: "reviewed-remote-service"}
	p.Services = []ServiceSpec{spec}
	if err := c.saveProfile(p); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Options{Directory: c.dir, SkipNetworkStart: true, NodeFactory: func(string, string) (NetworkBackend, error) {
		t.Fatal("saved configuration read tried to activate a backend")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved := savedService(t, reopened, spec.ID)
	if saved.Active || !reflect.DeepEqual(saved.Configuration, spec) {
		t.Fatal("offline config read dropped original backend, exclusions, target service or lifetime")
	}
}
