package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// Reuses inert owned temporary-file fixtures; no Core.Open, node, listener,
// provider, real profile, network, external process or actual grant is used.
func managementPreview(t *testing.T, c *Core, peer string) resourcegrant.ManagementGrantReview {
	t.Helper()
	input := resourcegrant.ManagementGrantInputs{Scope: resourcegrant.Management, Inputs: resourcegrant.GrantInputs{Target: resourceTarget(c), PeerKey: peer, ExpiresAt: time.Now().Add(time.Hour).Unix(), Actions: []string{resourcegrant.Inspect, resourcegrant.PreviewAction, resourcegrant.ApplyAction, resourcegrant.StatusAction}, Fields: []string{resourcegrant.FilesField, resourcegrant.PerPeerField}}}
	value, err := grantCall(t, c, "resource.grant.management.preview", input)
	if err != nil {
		t.Fatal(err)
	}
	return value.(resourcegrant.ManagementGrantReview)
}
func managementConfirm(t *testing.T, c *Core, review resourcegrant.ManagementGrantReview) resourceGrantLocalView {
	t.Helper()
	value, err := grantCall(t, c, "resource.grant.management.confirm", resourcegrant.ManagementGrantConfirmation{Review: review, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	return value.(resourceGrantLocalView)
}
func TestManagementGrantOwnedMigrationInactiveAndReopen(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	old := grantPreview(t, c, peer)
	grantConfirm(t, c, old)
	// Upgrade is never implicit while an old inspect grant is active.
	input := resourcegrant.ManagementGrantInputs{Scope: resourcegrant.Management, Inputs: resourcegrant.GrantInputs{Target: resourceTarget(c), PeerKey: peer, ExpiresAt: time.Now().Add(time.Hour).Unix(), Actions: []string{resourcegrant.Inspect, resourcegrant.PreviewAction, resourcegrant.ApplyAction, resourcegrant.StatusAction}, Fields: []string{resourcegrant.FilesField, resourcegrant.PerPeerField}}}
	activeBytes, err := os.ReadFile(resourceGrantStatePath(c.dir))
	if err != nil {
		t.Fatal(err)
	}
	_, previewErr := grantCall(t, c, "resource.grant.management.preview", input)
	var conflict interface{ ErrorCode() string }
	if !errors.As(previewErr, &conflict) || conflict.ErrorCode() != "resource_grant_conflict" {
		t.Fatal("active grant did not block otherwise valid management scope", previewErr)
	}
	unchanged, err := os.ReadFile(resourceGrantStatePath(c.dir))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(activeBytes, unchanged) || c.resourceGrants.state.Version != resourcegrant.Version {
		t.Fatal("conflicting preview changed saved grant")
	}
	if _, err := grantCall(t, c, "resource.grant.revoke", resourcegrant.GrantRevoke{Target: old.Grant.Target, GrantID: old.Grant.ID, GrantRevision: old.Grant.Revision, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	retained := c.resourceGrants.state.Records[0]
	before, err := os.ReadFile(resourceGrantStatePath(c.dir))
	if err != nil {
		t.Fatal(err)
	}
	review := managementPreview(t, c, peer)
	after, _ := os.ReadFile(resourceGrantStatePath(c.dir))
	if !reflect.DeepEqual(before, after) || !review.UpgradesFormat || review.InitializesState || c.resourceGrants.state.Version != resourcegrant.Version {
		t.Fatal("preview migrated or rewrote state")
	}
	view := managementConfirm(t, c, review)
	if view.Activation != "backend_unavailable" || view.ListenerReady || c.resourceGrants.fence != nil || c.resourceGrants.runtime != nil || c.node != nil || len(view.ManagementRecords) != 1 || !reflect.DeepEqual(c.resourceGrants.state.Records[0], retained) {
		t.Fatal("migration activated or changed history")
	}
	deadline := c.resourceGrants.bootDeadline
	c.initializeResourceGrants()
	c.reconcileResourceInspection()
	if c.resourceGrants.frozen || c.resourceGrants.state.Version != resourcegrant.ManagementVersion || c.resourceGrants.fence != nil || c.resourceGrants.runtime != nil || c.resourceGrants.activation != "backend_unavailable" || c.resourceGrants.state.ManagementRecords[0].Record.ExpiresAt != review.Grant.Record.ExpiresAt || c.resourceGrants.bootDeadline.After(deadline.Add(time.Millisecond)) {
		t.Fatal("reopen changed lifetime or activated")
	}
}
func TestManagementGrantReviewSeparationAndFirstUse(t *testing.T) {
	for _, kind := range []string{"missing_confirm", "changed_scope", "changed_upgrade", "changed_expiry", "legacy_route"} {
		t.Run(kind, func(t *testing.T) {
			c, peer := resourceGrantFixture(t)
			review := managementPreview(t, c, peer)
			in := resourcegrant.ManagementGrantConfirmation{Review: review, Confirm: true}
			name := "resource.grant.management.confirm"
			switch kind {
			case "missing_confirm":
				in.Confirm = false
			case "changed_scope":
				in.Review.Grant.Scope = ""
			case "changed_upgrade":
				in.Review.UpgradesFormat = false
			case "changed_expiry":
				in.Review.Grant.Record.ExpiresAt++
			case "legacy_route":
				name = "resource.grant.confirm"
			}
			if _, err := grantCall(t, c, name, in); err == nil {
				t.Fatal("changed/reinterpreted review accepted")
			}
			if _, err := os.Stat(filepath.Dir(resourceGrantStatePath(c.dir))); !os.IsNotExist(err) {
				t.Fatal("invalid review created storage")
			}
		})
	}
	c, peer := resourceGrantFixture(t)
	legacy := grantPreview(t, c, peer)
	if _, err := grantCall(t, c, "resource.grant.management.confirm", resourcegrant.GrantConfirmation{Review: legacy, Confirm: true}); err == nil {
		t.Fatal("legacy review became management")
	}
	review := managementPreview(t, c, peer)
	view := managementConfirm(t, c, review)
	if !review.InitializesState || !review.UpgradesFormat || view.Activation != "backend_unavailable" {
		t.Fatal("first-use decisions wrong")
	}
	if _, err := grantCall(t, c, "resource.grant.management.confirm", resourcegrant.ManagementGrantConfirmation{Review: review, Confirm: true}); err == nil {
		t.Fatal("stale review accepted")
	}
}
func TestManagementGrantBroadRevokeAndInspectReturn(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	review := managementPreview(t, c, peer)
	managementConfirm(t, c, review)
	if err := c.revokeInspectionPeerPermission(context.Background(), peer); err != nil {
		t.Fatal(err)
	}
	if c.resourceGrants.state.ManagementRecords[0].Record.State != resourcegrant.Revoked || *c.resourceGrants.state.HighWater != 2 {
		t.Fatal("broad revoke missed management")
	}
	legacy := grantPreview(t, c, peer)
	grantConfirm(t, c, legacy)
	if c.resourceGrants.state.Version != resourcegrant.ManagementVersion || len(c.resourceGrants.state.ManagementRecords) != 1 || legacy.Grant.Validate() != nil {
		t.Fatal("inspect regrant downgraded history")
	}
}
func TestManagementGrantOwnedMonotonicExpiryAndRollback(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	review := managementPreview(t, c, peer)
	managementConfirm(t, c, review)
	c.resourceGrants.bootDeadline = time.Now().Add(-time.Second)
	if _, err := grantCall(t, c, "resource.grant.inspect", grantInspectInput(c)); err != nil {
		t.Fatal(err)
	}
	if c.resourceGrants.state.ManagementRecords[0].Record.State != resourcegrant.Expired || *c.resourceGrants.state.HighWater != 2 {
		t.Fatal("monotonic expiry not durable")
	}
	// Check stored shape through strict decoder, not an authority constructor.
	data, err := os.ReadFile(resourceGrantStatePath(c.dir))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		t.Fatal("invalid persisted JSON")
	}
	if _, err := resourcegrant.Decode(data); err != nil {
		t.Fatal(err)
	}
}

func TestManagementGrantClockUncertaintyAndFailedPublication(t *testing.T) {
	for _, stage := range []string{"confirm", "revoke"} {
		t.Run(stage, func(t *testing.T) {
			c, peer := resourceGrantFixture(t)
			review := managementPreview(t, c, peer)
			if stage == "revoke" {
				managementConfirm(t, c, review)
			}
			c.atomicWrite = func(string, []byte) error { return errors.New("synthetic persistence failure") }
			var err error
			if stage == "confirm" {
				_, err = grantCall(t, c, "resource.grant.management.confirm", resourcegrant.ManagementGrantConfirmation{Review: review, Confirm: true})
			} else {
				_, err = grantCall(t, c, "resource.grant.revoke", resourcegrant.GrantRevoke{Target: review.Grant.Record.Target, GrantID: review.Grant.Record.ID, GrantRevision: review.Grant.Record.Revision, Confirm: true})
			}
			if err == nil || !c.resourceGrants.frozen || c.resourceGrants.fence != nil || c.resourceGrants.runtime != nil {
				t.Fatal("failed persistence admitted or claimed success")
			}
		})
	}
	c, peer := resourceGrantFixture(t)
	review := managementPreview(t, c, peer)
	managementConfirm(t, c, review)
	if c.resourceGrants.observe(time.Unix(1, 0)) == nil {
		t.Fatal("rollback accepted")
	}
	if _, err := grantCall(t, c, "resource.grant.inspect", grantInspectInput(c)); err != nil {
		t.Fatal(err)
	}
	c.initializeResourceGrants()
	c.reconcileResourceInspection()
	if !c.resourceGrants.timeUncertain || c.resourceGrants.fence != nil || c.resourceGrants.runtime != nil {
		t.Fatal("restart cleared uncertainty")
	}
	if _, err := grantCall(t, c, "resource.grant.revoke", resourcegrant.GrantRevoke{Target: review.Grant.Record.Target, GrantID: review.Grant.Record.ID, GrantRevision: review.Grant.Record.Revision, Confirm: true}); err != nil {
		t.Fatal("clock uncertainty stranded revoke", err)
	}
}

func TestManagementGrantBroadRevokeFailureStaysDenied(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	review := managementPreview(t, c, peer)
	managementConfirm(t, c, review)
	c.atomicWrite = func(string, []byte) error { return errors.New("synthetic persistence failure") }
	err := c.revokeInspectionPeerPermission(context.Background(), peer)
	var uncertain *resourceGrantRevokeUncertain
	if !errors.As(err, &uncertain) || !c.resourceGrants.frozen || c.resourceGrants.fence != nil || c.resourceGrants.runtime != nil {
		t.Fatal("broad revoke failure claimed success or remained eligible")
	}
}
