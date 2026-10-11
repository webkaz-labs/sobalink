package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/ranges"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func catalogCommandFixture(t *testing.T) (*Core, string, *activeService) {
	t.Helper()
	c, peer := resourceGrantFixture(t)
	ports, err := ranges.Parse("8080")
	if err != nil {
		t.Fatal(err)
	}
	a := &activeService{discoveryID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ctx: context.Background(), effective: ports, spec: ServiceSpec{Direction: "share", Discoverable: true, Network: "tcp", Ports: "8080", Lifetime: "until-revoked"}}
	a.ready.Store(true)
	c.active = map[string]*activeService{"synthetic-service": a}
	c.initializeCatalogViews()
	if c.catalogViews.frozen || !c.catalogViews.firstUse {
		t.Fatal("first use unavailable")
	}
	return c, peer, a
}
func catalogCall(t *testing.T, c *Core, name string, input any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	c.op.Lock()
	defer c.op.Unlock()
	return c.catalogViewCommand(context.Background(), name, raw)
}
func catalogPreview(t *testing.T, c *Core, peer string, a *activeService) resourcegrant.CatalogViewReview {
	t.Helper()
	got, err := catalogCall(t, c, "resource.catalog.view.preview", resourcegrant.CatalogViewInputs{Target: resourceTarget(c), PeerKey: peer, ServiceIDs: []string{a.discoveryID}, ExpiresAt: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return got.(resourcegrant.CatalogViewReview)
}
func catalogConfirm(t *testing.T, c *Core, r resourcegrant.CatalogViewReview) resourcegrant.CatalogViewLocalView {
	t.Helper()
	got, err := catalogCall(t, c, "resource.catalog.view.confirm", resourcegrant.CatalogViewConfirmation{Review: r, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	return got.(resourcegrant.CatalogViewLocalView)
}

func TestCatalogViewCommandsRevokeClosesPreparedDisclosureBeforeFailedSave(t *testing.T) {
	c, peer, a := catalogCommandFixture(t)
	r := catalogPreview(t, c, peer, a)
	catalogConfirm(t, c, r)
	c.op.Lock()
	var fence *resourcegrant.CatalogViewFence
	err := c.withCatalogViewState(false, func(*resourcePathBinding) error {
		_, f, err := c.prepareCatalogView(r.Grant.Selector(), peer)
		fence = f
		return err
	})
	c.op.Unlock()
	if err != nil || fence == nil {
		t.Fatal("could not prepare exact disclosure", err)
	}
	entered, finish := make(chan struct{}), make(chan struct{})
	c.atomicWrite = func(string, []byte) error {
		close(entered)
		<-finish
		return errors.New("synthetic revoke save failure")
	}
	raw, _ := json.Marshal(resourcegrant.GrantRevoke{Target: r.Grant.Target, GrantID: r.Grant.ID, GrantRevision: r.Grant.Revision, Confirm: true})
	done := make(chan error, 1)
	go func() {
		c.op.Lock()
		defer c.op.Unlock()
		_, err := c.catalogViewCommand(context.Background(), "resource.catalog.view.revoke", raw)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(finish)
		t.Fatal("revocation did not reach save")
	}
	admitted := fence.Admit(r.Grant.Selector(), r.Grant.Relationship, r.Grant.ServiceIDs)
	close(finish)
	if err := <-done; err == nil {
		t.Fatal("failed revoke persistence reported success")
	}
	if admitted || !c.catalogViews.frozen {
		t.Fatal("pending or failed revoke permitted prepared disclosure")
	}
}
func TestCatalogViewCommandsExplicitReviewDurability(t *testing.T) {
	c, peer, a := catalogCommandFixture(t)
	r := catalogPreview(t, c, peer, a)
	if !r.InitializesState {
		t.Fatal("initialization not reviewed")
	}
	if _, err := os.Stat(filepath.Dir(resourceCatalogViewStatePath(c.dir))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview created storage")
	}
	got := catalogConfirm(t, c, r)
	if len(got.Records) != 1 || c.catalogViews.firstUse || c.catalogViews.fence != nil {
		t.Fatal("saved state activated or missing")
	}
	if _, err := catalogCall(t, c, "resource.catalog.view.confirm", resourcegrant.CatalogViewConfirmation{Review: r, Confirm: true}); err == nil {
		t.Fatal("replay accepted")
	}
}
func TestCatalogViewCommandsRejectChangedReviewAndShare(t *testing.T) {
	for _, kind := range []string{"consent", "peer", "service", "expiry", "activation", "stopped", "relationship"} {
		t.Run(kind, func(t *testing.T) {
			c, peer, a := catalogCommandFixture(t)
			r := catalogPreview(t, c, peer, a)
			in := resourcegrant.CatalogViewConfirmation{Review: r, Confirm: true}
			switch kind {
			case "consent":
				in.Confirm = false
			case "peer":
				in.Review.Grant.Relationship.PeerKey = in.Review.Grant.Relationship.TargetKey
			case "service":
				in.Review.Grant.ServiceIDs = []string{"cccccccccccccccccccccccccccccccc"}
			case "expiry":
				in.Review.Grant.ExpiresAt++
			case "activation":
				a.discoveryID = "cccccccccccccccccccccccccccccccc"
			case "stopped":
				a.ready.Store(false)
			case "relationship":
				c.directLAN = nil
			}
			if _, err := catalogCall(t, c, "resource.catalog.view.confirm", in); err == nil {
				t.Fatal("changed review accepted")
			}
			if _, err := os.Stat(filepath.Dir(resourceCatalogViewStatePath(c.dir))); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid consent created storage")
			}
		})
	}
}
func TestCatalogViewCommandsPreserveLegacyGrants(t *testing.T) {
	for _, kind := range []string{"inspect", "management"} {
		t.Run(kind, func(t *testing.T) {
			c, peer, a := catalogCommandFixture(t)
			if kind == "inspect" {
				grantConfirm(t, c, grantPreview(t, c, peer))
			} else {
				managementConfirm(t, c, managementPreview(t, c, peer))
			}
			before, err := os.ReadFile(resourceGrantStatePath(c.dir))
			if err != nil {
				t.Fatal(err)
			}
			r := catalogPreview(t, c, peer, a)
			got := catalogConfirm(t, c, r)
			_, err = catalogCall(t, c, "resource.catalog.view.revoke", resourcegrant.GrantRevoke{Target: r.Grant.Target, GrantID: r.Grant.ID, GrantRevision: got.Records[0].Revision, Confirm: true})
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(resourceGrantStatePath(c.dir))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("catalog changed legacy grant bytes")
			}
		})
	}
}
func TestCatalogViewCommandsSaveFailureFreezes(t *testing.T) {
	c, peer, a := catalogCommandFixture(t)
	r := catalogPreview(t, c, peer, a)
	c.atomicWrite = func(string, []byte) error { return errors.New("synthetic save failure") }
	if _, err := catalogCall(t, c, "resource.catalog.view.confirm", resourcegrant.CatalogViewConfirmation{Review: r, Confirm: true}); err == nil || !c.catalogViews.frozen || c.catalogViews.fence != nil {
		t.Fatal("failed save eligible")
	}
}
func TestCatalogViewCommandsRevokeAfterRelationshipLoss(t *testing.T) {
	c, peer, a := catalogCommandFixture(t)
	r := catalogPreview(t, c, peer, a)
	catalogConfirm(t, c, r)
	c.directLAN = nil
	c.profile.Settings.Network = "none"
	got, err := catalogCall(t, c, "resource.catalog.view.revoke", resourcegrant.GrantRevoke{Target: r.Grant.Target, GrantID: r.Grant.ID, GrantRevision: r.Grant.Revision, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.(resourcegrant.CatalogViewLocalView).Records[0].State != resourcegrant.Revoked {
		t.Fatal("relationship loss stranded revoke")
	}
}

func TestCatalogViewCommandsWholePeerRemovalReducesCatalogOnly(t *testing.T) {
	c, peer, a := catalogCommandFixture(t)
	r := catalogPreview(t, c, peer, a)
	catalogConfirm(t, c, r)
	c.op.Lock()
	if err := c.revokeCatalogViewPeerPermission(context.Background(), "other-peer"); err != nil || c.catalogViews.state.ActiveRecord().ID == "" {
		c.op.Unlock()
		t.Fatal("other peer reduced catalog")
	}
	err := c.revokeCatalogViewPeerPermission(context.Background(), peer)
	c.op.Unlock()
	if err != nil || c.catalogViews.state.ActiveRecord().ID != "" || c.catalogViews.state.Records[0].State != resourcegrant.Revoked {
		t.Fatal("whole peer removal retained catalog eligibility", err)
	}
}
func TestCatalogViewCommandsDisclosureDoesNotGrantConnection(t *testing.T) {
	c, peer, a := catalogCommandFixture(t)
	r := catalogPreview(t, c, peer, a)
	catalogConfirm(t, c, r)
	extra := &activeService{discoveryID: "cccccccccccccccccccccccccccccccc", ctx: context.Background(), effective: a.effective, spec: a.spec}
	extra.ready.Store(true)
	c.active["unselected"] = extra
	selector := resourcegrant.CatalogViewSelector{ProtocolVersion: resourcegrant.ProtocolVersion, Target: r.Grant.Target, GrantID: r.Grant.ID, GrantRevision: r.Grant.Revision}
	c.op.Lock()
	got, fence, err := c.prepareCatalogView(selector, peer)
	c.op.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	if len(got.Services) != 1 || got.Services[0].Service.ID != a.discoveryID || got.Services[0].ConnectAllowed || len(a.spec.PeerIDs) != 0 {
		t.Fatal("viewing expanded service or connection scope")
	}
	a.discoveryID = "dddddddddddddddddddddddddddddddd"
	c.op.Lock()
	_, _, err = c.prepareCatalogView(selector, peer)
	c.op.Unlock()
	if err == nil {
		t.Fatal("old activation disclosed")
	}
}

func TestCatalogViewCommandsPreviewRequiresExactSelection(t *testing.T) {
	for _, kind := range []string{"target", "peer", "services", "expiry", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			c, peer, a := catalogCommandFixture(t)
			in := resourcegrant.CatalogViewInputs{Target: resourceTarget(c), PeerKey: peer, ServiceIDs: []string{a.discoveryID}, ExpiresAt: time.Now().Add(time.Hour).Unix()}
			switch kind {
			case "target":
				in.Target.ResourceID = ""
			case "peer":
				in.PeerKey = ""
			case "services":
				in.ServiceIDs = nil
			case "expiry":
				in.ExpiresAt = 0
			case "duplicate":
				in.ServiceIDs = append(in.ServiceIDs, a.discoveryID)
			}
			if _, err := catalogCall(t, c, "resource.catalog.view.preview", in); err == nil {
				t.Fatal("incomplete selection accepted")
			}
			if c.catalogViews.frozen || !c.catalogViews.firstUse || len(c.catalogViews.state.Records) != 0 {
				t.Fatal("invalid selection changed store")
			}
		})
	}
}
