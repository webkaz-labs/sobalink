package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
)

func TestResourceCatalogProcessTokenDomainAndRestart(t *testing.T) {
	if resourceCatalogProcessToken("") != "" || resourceCatalogProcessToken("not-an-owned-nonce") != "" {
		t.Fatal("fabricated process identity")
	}
	nonce := strings.Repeat("1", 32)
	one := resourceCatalogProcessToken(nonce)
	if !resource.ValidDigest(one) || one == nonce || one != resourceCatalogProcessToken(nonce) || one == resourceCatalogProcessToken(strings.Repeat("2", 32)) {
		t.Fatal("process domain/restart identity incorrect")
	}
	c := &Core{ctx: context.Background(), resourceNonce: nonce, resourceIdentity: strings.Repeat("3", 32)}
	if c.resourceCatalogProcessID() != "" {
		t.Fatal("unowned token exposed")
	}
}

// These owner tests use the existing fixture: private t.TempDir state and an
// actual local process lock only. No Core.Open, transport, listener or grant.
func TestResourceCatalogOriginalOwnerRejectsReplacement(t *testing.T) {
	for _, kind := range []string{"profile", "journal", "lock"} {
		t.Run(kind, func(t *testing.T) {
			c, lock := resourceFixture(t)
			owner, err := c.resourceCatalogOwnerLocked()
			if err != nil {
				t.Fatal(err)
			}
			defer owner.binding.close()
			if c.resourceCatalogOwnerCurrentLocked(owner) != nil {
				t.Fatal("unchanged owner rejected")
			}
			replaceResourceBinding(t, c.dir, kind, lock)
			if c.resourceCatalogOwnerCurrentLocked(owner) == nil {
				t.Fatal("replacement accepted as original owner")
			}
		})
	}
}

func TestResourceCatalogOwnerRejectsBootAndContextChange(t *testing.T) {
	c, _ := resourceFixture(t)
	owner, err := c.resourceCatalogOwnerLocked()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.binding.close()
	previous := c.resourceNonce
	c.resourceNonce = strings.Repeat("4", 32)
	if c.resourceCatalogOwnerCurrentLocked(owner) == nil {
		t.Fatal("new boot accepted")
	}
	c.resourceNonce = previous
	c.ctx = context.WithValue(c.ctx, struct{}{}, "synthetic-replacement")
	if c.resourceCatalogOwnerCurrentLocked(owner) == nil {
		t.Fatal("replacement context accepted")
	}
}

func TestResourceCatalogCommandFreshEpochAndDeclaredFailures(t *testing.T) {
	c, _ := resourceFixture(t)
	request := []byte(`{"schemaVersion":1,"sources":[{"kind":"local_service"}]}`)
	one, err := c.resourceCatalogCommand(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	two, err := c.resourceCatalogCommand(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	a, b := one.(resourceCatalogResponse), two.(resourceCatalogResponse)
	if !a.Snapshot.Complete || a.Snapshot.Sources[0].Selection.Epoch == b.Snapshot.Sources[0].Selection.Epoch || a.Snapshot.ScopeID == b.Snapshot.ScopeID {
		t.Fatal("refresh reused an epoch or lost empty completeness")
	}
	target := resource.Target{SchemaVersion: 1, ResourceID: strings.Repeat("9", 32)}
	raw, _ := json.Marshal(struct {
		SchemaVersion int   `json:"schemaVersion"`
		Sources       []any `json:"sources"`
	}{1, []any{struct {
		Kind   string          `json:"kind"`
		Target resource.Target `json:"target"`
	}{resourcecatalog.LocalSettings, target}, struct {
		Kind string `json:"kind"`
	}{resourcecatalog.LocalService}}})
	value, err := c.resourceCatalogCommand(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	result := value.(resourceCatalogResponse)
	if result.Snapshot.Complete || len(result.Snapshot.Sources) != 2 || result.Snapshot.Sources[0].Selection.Kind != resourcecatalog.LocalService || !result.Snapshot.Sources[0].Complete || result.Snapshot.Sources[1].State != "unavailable" || result.Snapshot.Sources[1].Selection.Target != target {
		t.Fatal("failure reduced declared scope or damaged independent source")
	}
}

func TestResourceCatalogCommandCancellationAndUnownedContext(t *testing.T) {
	request := []byte(`{"schemaVersion":1,"sources":[{"kind":"local_service"}]}`)
	c := &Core{ctx: context.Background()}
	if _, err := c.resourceCatalogCommand(context.Background(), request); err == nil {
		t.Fatal("unowned Core disclosed")
	}
	c, _ = resourceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.resourceCatalogCommand(ctx, request); err == nil {
		t.Fatal("cancelled request disclosed")
	}
}
