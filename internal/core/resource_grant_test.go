package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// These fixtures never call Core.Open, a node constructor, a listener or a
// process. They use existing inert models and only owned temporary files.
func resourceGrantFixture(t *testing.T) (*Core, string) {
	t.Helper()
	c, _ := resourceFixture(t)
	state, _ := pairRecordFixture(t)
	state.Metadata.ObservedAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.dir, "direct-lan.json")
	if err := config.AtomicWritePrivate(path, data); err != nil {
		t.Fatal(err)
	}
	c.directLAN, err = readDirectLANStore(path, 1<<20, 16)
	if err != nil {
		t.Fatal(err)
	}
	c.profile.Settings.Network = "direct-lan"
	c.initializeResourceGrants()
	if c.resourceGrants.frozen || !c.resourceGrants.firstUse {
		t.Fatal("first-use fixture unavailable")
	}
	return c, state.Peers[0].Key
}
func grantCall(t *testing.T, c *Core, name string, payload any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	c.op.Lock()
	defer c.op.Unlock()
	return c.resourceGrantCommand(context.Background(), name, raw)
}
func grantPreview(t *testing.T, c *Core, peer string) resourceGrantReview {
	t.Helper()
	value, err := grantCall(t, c, "resource.grant.preview", resourceGrantInputs{Target: resourceTarget(c), PeerKey: peer, ExpiresAt: time.Now().Add(time.Hour).Unix(), Actions: []string{resourcegrant.Inspect}, Fields: []string{resourcegrant.FilesField, resourcegrant.PerPeerField}})
	if err != nil {
		t.Fatal(err)
	}
	return value.(resourceGrantReview)
}
func grantConfirm(t *testing.T, c *Core, review resourceGrantReview) resourceGrantLocalView {
	t.Helper()
	value, err := grantCall(t, c, "resource.grant.confirm", resourceGrantConfirmation{Review: review, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	return value.(resourceGrantLocalView)
}
func grantInspectInput(c *Core) map[string]any { return map[string]any{"target": resourceTarget(c)} }

func TestResourceGrantOwnedReviewDurabilityAndDormantReopen(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	review := grantPreview(t, c, peer)
	if !review.InitializesState {
		t.Fatal("initialization not explicitly reviewed")
	}
	if _, err := os.Stat(filepath.Dir(resourceGrantStatePath(c.dir))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview created state")
	}
	result := grantConfirm(t, c, review)
	if result.Activation != "backend_unavailable" || result.ListenerReady || len(result.Records) != 1 || c.resourceGrants.fence != nil {
		t.Fatal("saved grant silently activated")
	}
	if _, err := grantCall(t, c, "resource.grant.confirm", resourceGrantConfirmation{Review: review, Confirm: true}); err == nil {
		t.Fatal("stale confirmation accepted")
	}
	c.initializeResourceGrants()
	if c.resourceGrants.frozen || c.resourceGrants.firstUse || c.resourceGrants.fence != nil || len(c.resourceGrants.state.Records) != 1 {
		t.Fatal("owned reopen changed dormant grant authority")
	}
}

func TestResourceGrantConfirmationRejectsChangedScope(t *testing.T) {
	for _, kind := range []string{"consent", "peer", "expiry", "fields", "initialize", "revision"} {
		t.Run(kind, func(t *testing.T) {
			c, peer := resourceGrantFixture(t)
			review := grantPreview(t, c, peer)
			in := resourceGrantConfirmation{Review: review, Confirm: true}
			switch kind {
			case "consent":
				in.Confirm = false
			case "peer":
				in.Review.Grant.Relationship.PeerKey = in.Review.Grant.Relationship.TargetKey
			case "expiry":
				in.Review.Grant.ExpiresAt++
			case "fields":
				in.Review.Grant.Fields = []string{resourcegrant.FilesField}
			case "initialize":
				in.Review.InitializesState = false
			case "revision":
				in.Review.Grant.Revision++
			}
			if _, err := grantCall(t, c, "resource.grant.confirm", in); err == nil {
				t.Fatal("changed review accepted")
			}
			if _, err := os.Stat(filepath.Dir(resourceGrantStatePath(c.dir))); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid review created state")
			}
		})
	}
}

func TestResourceGrantRevokeSurvivesLostEligibilityAndClockAnomaly(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	review := grantPreview(t, c, peer)
	grantConfirm(t, c, review)
	c.directLAN = nil
	c.profile.Settings.Network = "none"
	if err := c.resourceGrants.observe(time.Unix(1, 0)); err == nil {
		t.Fatal("clock anomaly accepted")
	}
	value, err := grantCall(t, c, "resource.grant.inspect", grantInspectInput(c))
	if err != nil || !value.(resourceGrantLocalView).TimeUncertain {
		t.Fatal("local saved inspection stranded revoke")
	}
	_, err = grantCall(t, c, "resource.grant.revoke", resourceGrantRevoke{Target: review.Grant.Target, GrantID: review.Grant.ID, GrantRevision: review.Grant.Revision, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.resourceGrants.state.Records[0].State != resourcegrant.Revoked || *c.resourceGrants.state.HighWater != 2 {
		t.Fatal("tombstone or high-water lost")
	}
}

func TestResourceGrantMissingObservedStateFreezes(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	grantConfirm(t, c, grantPreview(t, c, peer))
	if err := os.Remove(resourceGrantStatePath(c.dir)); err != nil {
		t.Fatal(err)
	}
	if _, err := grantCall(t, c, "resource.grant.inspect", grantInspectInput(c)); err == nil {
		t.Fatal("missing observed state accepted")
	}
	if !c.resourceGrants.frozen || c.resourceGrants.firstUse {
		t.Fatal("deleted state became first use")
	}
}

func TestResourceGrantUncertainPublicationStaysDormant(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	review := grantPreview(t, c, peer)
	c.atomicWrite = func(path string, data []byte) error {
		if path != resourceGrantStatePath(c.dir) {
			t.Fatal("unexpected write")
		}
		if err := config.AtomicWritePrivate(path, data); err != nil {
			return err
		}
		return config.ErrAtomicCommitted
	}
	if _, err := grantCall(t, c, "resource.grant.confirm", resourceGrantConfirmation{Review: review, Confirm: true}); err == nil {
		t.Fatal("uncertain publication reported success")
	}
	if !c.resourceGrants.frozen || c.resourceGrants.fence != nil {
		t.Fatal("uncertain publication admitted disclosure")
	}
	c.atomicWrite = nil
	c.initializeResourceGrants()
	if c.resourceGrants.frozen || c.resourceGrants.fence != nil {
		t.Fatal("owned reopen activated uncertain grant")
	}
}

func TestResourceGrantFailedRevokeClosesFence(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	review := grantPreview(t, c, peer)
	grantConfirm(t, c, review)
	fence, err := resourcegrant.NewDisclosureFence(review.Grant)
	if err != nil {
		t.Fatal(err)
	}
	c.resourceGrants.fence = fence
	c.atomicWrite = func(string, []byte) error { return errors.New("synthetic persistence failure") }
	_, err = grantCall(t, c, "resource.grant.revoke", resourceGrantRevoke{Target: review.Grant.Target, GrantID: review.Grant.ID, GrantRevision: review.Grant.Revision, Confirm: true})
	if err == nil || !c.resourceGrants.frozen {
		t.Fatal("failed revocation did not freeze")
	}
	request := resourcegrant.InspectRequest{ProtocolVersion: 1, Target: review.Grant.Target, GrantID: review.Grant.ID, GrantRevision: review.Grant.Revision}
	if fence.Admit(request, review.Grant.Relationship) {
		t.Fatal("failed durable revoke reopened runtime authority")
	}
}

func TestResourceGrantDefaultOffAndAppearingDirectory(t *testing.T) {
	c, _ := resourceFixture(t)
	if _, err := grantCall(t, c, "resource.grant.inspect", grantInspectInput(c)); err == nil {
		t.Fatal("default-off command enabled")
	}
	if _, err := os.Stat(filepath.Dir(resourceGrantStatePath(c.dir))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("default-off grant I/O created state")
	}
	c, peer := resourceGrantFixture(t)
	review := grantPreview(t, c, peer)
	if err := os.Mkdir(filepath.Dir(resourceGrantStatePath(c.dir)), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := grantCall(t, c, "resource.grant.confirm", resourceGrantConfirmation{Review: review, Confirm: true}); err == nil {
		t.Fatal("appearing directory adopted")
	}
	if !c.resourceGrants.frozen {
		t.Fatal("raced initialization did not freeze")
	}
}

// Simulate already durable private bytes before a new owned reopen. This does
// not construct a Core, backend or connection and never activates authority.
func writeGrantReopenFixture(t *testing.T, c *Core, state resourcegrant.Envelope) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.AtomicWritePrivate(resourceGrantStatePath(c.dir), data); err != nil {
		t.Fatal(err)
	}
	c.initializeResourceGrants()
}

func TestResourceGrantReopenCheckpointsOriginalExpiry(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	review := grantPreview(t, c, peer)
	grantConfirm(t, c, review)
	before := c.resourceGrants.state
	oldFence, err := resourcegrant.NewDisclosureFence(review.Grant)
	if err != nil {
		t.Fatal(err)
	}
	c.resourceGrants.fence = oldFence
	c.initializeResourceGrants()
	request := resourcegrant.InspectRequest{ProtocolVersion: resourcegrant.ProtocolVersion, Target: review.Grant.Target, GrantID: review.Grant.ID, GrantRevision: review.Grant.Revision}
	if oldFence.Admit(request, review.Grant.Relationship) {
		t.Fatal("old coordinator fence survived reopen")
	}
	after := c.resourceGrants.state
	if c.resourceGrants.frozen || c.resourceGrants.timeUncertain || after.Records[0].ExpiresAt != review.Grant.ExpiresAt || after.Records[0].Revision != review.Grant.Revision {
		t.Fatal("reopen changed original permission")
	}
	if time.Unix(after.Clock.Seconds, int64(after.Clock.Nanoseconds)).Before(time.Unix(before.Clock.Seconds, int64(before.Clock.Nanoseconds))) {
		t.Fatal("checkpoint regressed")
	}
}

func TestResourceGrantReopenRollbackPersistsDenial(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	review := grantPreview(t, c, peer)
	grantConfirm(t, c, review)
	state := c.resourceGrants.state
	checkpoint, err := resourcegrant.Checkpoint(time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	state.Clock = &checkpoint
	writeGrantReopenFixture(t, c, state)
	if c.resourceGrants.frozen || !c.resourceGrants.timeUncertain || !c.resourceGrants.state.Clock.Uncertain || c.resourceGrants.fence != nil {
		t.Fatal("rollback restored authority")
	}
	c.initializeResourceGrants()
	if !c.resourceGrants.timeUncertain || !c.resourceGrants.state.Clock.Uncertain {
		t.Fatal("uncertainty was not durable")
	}
	if _, err := grantCall(t, c, "resource.grant.inspect", grantInspectInput(c)); err != nil {
		t.Fatal("local recovery inspection blocked", err)
	}
	if _, err := grantCall(t, c, "resource.grant.revoke", resourceGrantRevoke{Target: review.Grant.Target, GrantID: review.Grant.ID, GrantRevision: review.Grant.Revision, Confirm: true}); err != nil {
		t.Fatal("uncertain time stranded revoke", err)
	}
}

func TestResourceGrantReopenPersistsExpiredReduction(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	review := grantPreview(t, c, peer)
	grantConfirm(t, c, review)
	state := c.resourceGrants.state
	state.Records = append([]resourcegrant.Record{}, state.Records...)
	state.Records[0].IssuedAt = time.Now().Add(-time.Minute).Unix()
	state.Records[0].ExpiresAt = time.Now().Add(-time.Second).Unix()
	expiry := state.Records[0].ExpiresAt
	writeGrantReopenFixture(t, c, state)
	got := c.resourceGrants.state.Records[0]
	if c.resourceGrants.frozen || got.State != resourcegrant.Expired || got.ExpiresAt != expiry || got.Revision != review.Grant.Revision+1 || c.resourceGrants.fence != nil {
		t.Fatal("expired grant not terminally reduced")
	}
	c.initializeResourceGrants()
	if c.resourceGrants.state.Records[0].Revision != got.Revision {
		t.Fatal("reopen repeated terminal reduction")
	}
}

func TestResourceGrantInactiveRetryKeepsBootCutoff(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	review := grantPreview(t, c, peer)
	view := grantConfirm(t, c, review)
	deadline := c.resourceGrants.bootDeadline
	if deadline.IsZero() || view.Activation != "backend_unavailable" || view.ListenerReady {
		t.Fatal("inactive confirmation misreported")
	}
	c.op.Lock()
	c.reconcileResourceInspection()
	c.reconcileResourceInspection()
	c.op.Unlock()
	if c.resourceGrants.bootDeadline != deadline || c.resourceGrants.fence != nil || c.resourceGrants.state.Records[0].ExpiresAt != review.Grant.ExpiresAt {
		t.Fatal("retry renewed lifetime or minted authority without backend")
	}
}

func TestResourceGrantPreviewLeavesTerminalStateUnwritten(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	review := grantPreview(t, c, peer)
	grantConfirm(t, c, review)
	fence, err := resourcegrant.NewDisclosureFence(review.Grant)
	if err != nil {
		t.Fatal(err)
	}
	c.resourceGrants.fence = fence
	c.resourceGrants.bootDeadline = time.Now().Add(-time.Second)
	before, err := os.ReadFile(resourceGrantStatePath(c.dir))
	if err != nil {
		t.Fatal(err)
	}
	_, err = grantCall(t, c, "resource.grant.preview", resourceGrantInputs{Target: review.Grant.Target, PeerKey: peer, ExpiresAt: review.Grant.ExpiresAt, Actions: review.Grant.Actions, Fields: review.Grant.Fields})
	if err == nil {
		t.Fatal("expired saved active record silently replaced")
	}
	after, err := os.ReadFile(resourceGrantStatePath(c.dir))
	if err != nil || string(before) != string(after) {
		t.Fatal("preview wrote saved terminal state")
	}
	request := resourcegrant.InspectRequest{ProtocolVersion: resourcegrant.ProtocolVersion, Target: review.Grant.Target, GrantID: review.Grant.ID, GrantRevision: review.Grant.Revision}
	if fence.Admit(request, review.Grant.Relationship) {
		t.Fatal("preview left unsafe memory authority open")
	}
}

func TestResourceGrantRetainedCutoffPersistsReduction(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	review := grantPreview(t, c, peer)
	grantConfirm(t, c, review)
	c.resourceGrants.bootDeadline = time.Now().Add(-time.Second)
	c.op.Lock()
	c.reconcileResourceInspection()
	c.op.Unlock()
	record := c.resourceGrants.state.Records[0]
	if c.resourceGrants.frozen || record.State != resourcegrant.Expired || record.ExpiresAt != review.Grant.ExpiresAt || record.Revision != review.Grant.Revision+1 || c.resourceGrants.activation != "expired" {
		t.Fatal("retained cutoff not durably reduced")
	}
	c.initializeResourceGrants()
	if c.resourceGrants.state.Records[0].State != resourcegrant.Expired {
		t.Fatal("terminal cutoff revived after reopen")
	}
}
