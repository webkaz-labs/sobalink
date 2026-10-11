package core

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// Storage fixtures are inert: no Core.Open, node, listener or peer traffic.
func catalogViewOwnedBinding(c *Core, fn func(*resourcePathBinding) error) error {
	return c.resourceLock.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
		b, err := openResourcePathBinding(c.dir, directory, lock)
		if err != nil {
			return err
		}
		defer b.close()
		return fn(b)
	})
}
func catalogViewSavedFixture(t *testing.T) (*Core, []byte) {
	t.Helper()
	c, peer := resourceGrantFixture(t)
	legacy := grantConfirm(t, c, grantPreview(t, c, peer)).Records[0]
	before, err := os.ReadFile(resourceGrantStatePath(c.dir))
	if err != nil {
		t.Fatal(err)
	}
	c.initializeCatalogViews()
	if c.catalogViews.frozen || !c.catalogViews.firstUse {
		t.Fatal("catalog first use unavailable")
	}
	next := c.catalogViews.state
	high := uint64(1)
	next.HighWater = &high
	next.Records = []resourcegrant.CatalogViewRecord{{ID: legacy.ID, Revision: 1, Target: legacy.Target, Relationship: legacy.Relationship, ServiceIDs: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, IssuedAt: legacy.IssuedAt, ExpiresAt: legacy.ExpiresAt, State: resourcegrant.Active}}
	err = catalogViewOwnedBinding(c, func(b *resourcePathBinding) error {
		if err := c.currentCatalogViewsBound(b); err != nil {
			return err
		}
		if err := c.initializeCatalogViewDirectoryBound(b); err != nil {
			return err
		}
		return c.writeCatalogViewsBound(next, b)
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, before
}
func TestCatalogViewStoreOwnedReopenPreservesLegacy(t *testing.T) {
	c, before := catalogViewSavedFixture(t)
	c.initializeCatalogViews()
	if c.catalogViews.frozen || c.catalogViews.firstUse || len(c.catalogViews.state.Records) != 1 || c.catalogViews.fence != nil || c.catalogViews.bootDeadline.IsZero() {
		t.Fatal("owned reopen lost dormant saved state or cutoff")
	}
	after, err := os.ReadFile(resourceGrantStatePath(c.dir))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("catalog persistence changed legacy state")
	}
	if err := catalogViewOwnedBinding(c, c.currentCatalogViewsBound); err != nil {
		t.Fatal(err)
	}
}
func TestCatalogViewStoreObservedMissingFreezes(t *testing.T) {
	c, _ := catalogViewSavedFixture(t)
	if err := os.Remove(resourceCatalogViewStatePath(c.dir)); err != nil {
		t.Fatal(err)
	}
	if err := catalogViewOwnedBinding(c, c.currentCatalogViewsBound); err == nil || !c.catalogViews.frozen || c.catalogViews.firstUse {
		t.Fatal("missing observed state became first use")
	}
}
func TestCatalogViewStoreFirstWriteFailureFreezes(t *testing.T) {
	c, _ := resourceGrantFixture(t)
	c.initializeCatalogViews()
	c.atomicWrite = func(string, []byte) error { return errors.New("synthetic storage failure") }
	err := catalogViewOwnedBinding(c, func(b *resourcePathBinding) error {
		if err := c.initializeCatalogViewDirectoryBound(b); err != nil {
			return err
		}
		return c.writeCatalogViewsBound(c.catalogViews.state, b)
	})
	if err == nil || !c.catalogViews.frozen || c.catalogViews.firstUse {
		t.Fatal("ambiguous first write remained eligible")
	}
}
func TestCatalogViewStoreBootDeadlineNeverExtends(t *testing.T) {
	c, _ := catalogViewSavedFixture(t)
	observed := time.Now()
	c.catalogViews.anchorBootDeadline(observed)
	cutoff := c.catalogViews.bootDeadline
	c.catalogViews.anchorBootDeadline(observed.Add(time.Hour))
	if c.catalogViews.bootDeadline != cutoff {
		t.Fatal("same grant deadline extended")
	}
}

func TestCatalogViewStoreCorruptOrLegacyEnvelopeFreezes(t *testing.T) {
	for _, kind := range []string{"corrupt", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			c, before := catalogViewSavedFixture(t)
			data := []byte(`{"unknown":true}`)
			if kind == "legacy" {
				data = before
			}
			if err := os.WriteFile(resourceCatalogViewStatePath(c.dir), data, 0600); err != nil {
				t.Fatal(err)
			}
			c.initializeCatalogViews()
			if !c.catalogViews.frozen || c.catalogViews.firstUse {
				t.Fatal("invalid catalog state accepted or migrated")
			}
		})
	}
}
