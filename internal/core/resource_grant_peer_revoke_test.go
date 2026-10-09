package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// Empty in-memory transfer manager only: no stores, destinations, batches,
// workers or network. It allows the real peer.trust=false teardown path.
func resourceGrantRemovalFixture(t *testing.T) (*Core, string, resourceGrantReview, *resourcegrant.DisclosureFence) {
	t.Helper()
	c, peer := resourceGrantFixture(t)
	review := grantPreview(t, c, peer)
	grantConfirm(t, c, review)
	manager, err := transfer.NewManager(transfer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.transfers = manager
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	c.confirmed = map[string]time.Time{peer: time.Now()}
	fence, err := resourcegrant.NewDisclosureFence(review.Grant)
	if err != nil {
		t.Fatal(err)
	}
	c.resourceGrants.fence = fence
	return c, peer, review, fence
}
func peerPermissionCommand(c *Core, id string, trusted bool, requestID string) error {
	raw, _ := json.Marshal(map[string]any{"peerId": id, "trusted": trusted})
	_, err := c.Command(context.Background(), webui.Command{RequestID: requestID, Name: "peer.trust", Payload: raw})
	return err
}
func grantRemovalRequest(review resourceGrantReview) resourcegrant.InspectRequest {
	return resourcegrant.InspectRequest{ProtocolVersion: resourcegrant.ProtocolVersion, Target: review.Grant.Target, GrantID: review.Grant.ID, GrantRevision: review.Grant.Revision}
}
func TestResourceGrantPeerRemovalCascadesWithoutOrdinaryTrust(t *testing.T) {
	c, peer, review, fence := resourceGrantRemovalFixture(t)
	if _, ok := c.trust(peer); ok {
		t.Fatal("fixture unexpectedly has ordinary Trust")
	}
	if err := peerPermissionCommand(c, peer, false, "remove-one"); err != nil {
		t.Fatal(err)
	}
	record := c.resourceGrants.state.Records[0]
	if record.State != resourcegrant.Revoked || record.Revision != review.Grant.Revision+1 || fence.Admit(grantRemovalRequest(review), review.Grant.Relationship) {
		t.Fatal("no-Trust fast path left inspection active")
	}
	if _, ok := c.confirmed[peer]; ok {
		t.Fatal("ordinary teardown skipped")
	}
	if err := peerPermissionCommand(c, peer, false, "remove-two"); err != nil {
		t.Fatal(err)
	}
	if c.resourceGrants.state.Records[0].Revision != record.Revision {
		t.Fatal("repeated removal renewed grant revision")
	}
	// Disconnected general approval cannot revive a separately revoked grant.
	_ = peerPermissionCommand(c, peer, true, "ordinary-approval")
	if c.resourceGrants.state.Records[0].State != resourcegrant.Revoked || c.resourceGrants.fence != fence {
		t.Fatal("general trust recreated inspect permission")
	}
	c.initializeResourceGrants()
	if c.resourceGrants.state.Records[0].State != resourcegrant.Revoked {
		t.Fatal("reopen revived broad removal")
	}
}
func TestResourceGrantPeerRemovalFailureStillDeniesAndTearsDown(t *testing.T) {
	c, peer, review, fence := resourceGrantRemovalFixture(t)
	c.atomicWrite = func(path string, data []byte) error {
		if path == resourceGrantStatePath(c.dir) {
			if fence.Admit(grantRemovalRequest(review), review.Grant.Relationship) {
				t.Fatal("fallible write preceded runtime denial")
			}
			return errors.New("synthetic grant write failure")
		}
		if path != filepath.Join(c.dir, "startup-revocations.json") && path != filepath.Join(c.dir, "startup.json") {
			t.Fatal("unexpected write path")
		}
		return config.AtomicWritePrivate(path, data)
	}
	err := peerPermissionCommand(c, peer, false, "remove-failure")
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) || coded.ErrorCode() != "resource_grant_revoke_uncertain" || !c.resourceGrants.frozen || strings.Contains(err.Error(), "synthetic grant") {
		t.Fatal("failed saved revocation falsely reported or leaked")
	}
	if _, ok := c.confirmed[peer]; ok {
		t.Fatal("grant error skipped ordinary teardown")
	}
	if c.startup.Revocations[peer] == "" || fence.Admit(grantRemovalRequest(review), review.Grant.Relationship) {
		t.Fatal("denial or startup revocation skipped")
	}
}
func TestResourceGrantPeerRemovalStartupFailureDoesNotSkipCascade(t *testing.T) {
	c, peer, review, fence := resourceGrantRemovalFixture(t)
	c.atomicWrite = func(path string, data []byte) error {
		if path == resourceGrantStatePath(c.dir) {
			return config.AtomicWritePrivate(path, data)
		}
		if path != filepath.Join(c.dir, "startup-revocations.json") && path != filepath.Join(c.dir, "startup.json") {
			t.Fatal("unexpected write path")
		}
		return errors.New("synthetic startup write failure")
	}
	if err := peerPermissionCommand(c, peer, false, "startup-failure"); err == nil {
		t.Fatal("startup failure reported full success")
	}
	if c.resourceGrants.state.Records[0].State != resourcegrant.Revoked || fence.Admit(grantRemovalRequest(review), review.Grant.Relationship) {
		t.Fatal("startup failure skipped inspect revocation")
	}
	if _, ok := c.confirmed[peer]; ok {
		t.Fatal("startup failure skipped ordinary teardown")
	}
}
func TestResourceGrantPeerRemovalDoesNotRevokeAnotherPeer(t *testing.T) {
	c, peer, review, fence := resourceGrantRemovalFixture(t)
	other := strings.Repeat("f", 64)
	if other == peer {
		other = strings.Repeat("e", 64)
	}
	before, err := os.ReadFile(resourceGrantStatePath(c.dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := peerPermissionCommand(c, other, false, "remove-other"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(resourceGrantStatePath(c.dir))
	if err != nil || string(before) != string(after) {
		t.Fatal("another peer removal changed grant bytes")
	}
	if !fence.Admit(grantRemovalRequest(review), review.Grant.Relationship) {
		t.Fatal("another peer removal closed this grant")
	}
}
