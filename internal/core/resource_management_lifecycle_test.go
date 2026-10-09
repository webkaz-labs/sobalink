package core

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func managementFixtureFence(t *testing.T, c *Core, review resourcegrant.ManagementGrantReview) *resourcegrant.ManagementFence {
	t.Helper()
	fence, err := resourcegrant.NewManagementFenceBefore(review.Grant, c.resourceGrants.bootDeadline)
	if err != nil {
		t.Fatal(err)
	}
	c.resourceGrants.managementFence = fence
	return fence
}

func TestManagementBroadRevokeClosesDedicatedFence(t *testing.T) {
	c, peer := resourceGrantFixture(t)
	review := managementPreview(t, c, peer)
	managementConfirm(t, c, review)
	fence := managementFixtureFence(t, c, review)
	selector := managementSelector(review.Grant.Record)
	if !fence.Admit(selector, resourcegrant.Inspect, review.Grant.Record.Relationship) {
		t.Fatal("synthetic current fence rejected")
	}
	if err := c.revokeInspectionPeerPermission(context.Background(), peer); err != nil {
		t.Fatal(err)
	}
	if fence.Admit(selector, resourcegrant.ApplyAction, review.Grant.Record.Relationship) {
		t.Fatal("broad peer revoke retained management authority")
	}
}

func TestManagementClockAndStorageFailureCloseDedicatedFence(t *testing.T) {
	for _, reason := range []string{"clock", "storage"} {
		t.Run(reason, func(t *testing.T) {
			c, peer := resourceGrantFixture(t)
			review := managementPreview(t, c, peer)
			managementConfirm(t, c, review)
			fence := managementFixtureFence(t, c, review)
			if reason == "clock" {
				_ = c.resourceGrants.observe(time.Unix(1, 0))
			} else {
				c.resourceGrants.freeze()
			}
			if fence.Admit(managementSelector(review.Grant.Record), resourcegrant.StatusAction, review.Grant.Record.Relationship) {
				t.Fatal("terminal denial retained management fence")
			}
		})
	}
}

// A context method deliberately panics: the WG/generation callback must use
// only the cancellation signal captured outside that critical section.
type managementPoisonContext struct{ context.Context }

func (managementPoisonContext) Err() error { panic("context method under transport gate") }
func TestManagedAuthorityUsesCapturedCancellationSignal(t *testing.T) {
	done := make(chan struct{})
	c := &Core{ctx: managementPoisonContext{Context: context.Background()}}
	o := &managedCompletionOwner{core: c, coreDone: done}
	o.authority.Store(directlan.NewContextEpoch())
	if !o.authorityCurrent() {
		t.Fatal("live captured signal rejected")
	}
	close(done)
	if o.authorityCurrent() {
		t.Fatal("canceled captured signal admitted")
	}
}

func TestManagementPreparedResponseAbandonsOnOwnedPostcheckFailure(t *testing.T) {
	// Source ownership assertion complements transport response close/race
	// tests. An outer owned-path postcheck may fail after PrepareReply succeeds;
	// the returned response owner must be deferred regardless of that error.
	data, err := os.ReadFile("resource_management_runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	start := strings.Index(body, "func (c *Core) serveResourceManagement(")
	if start < 0 {
		t.Fatal("missing management handler")
	}
	body = body[start:]
	prepare := strings.Index(body, "response, err = capability.PrepareReply(fence, reply)")
	unlock := strings.LastIndex(body[:strings.Index(body, "writeErr := err")], "c.op.Unlock()")
	deferred := strings.Index(body, "defer response.Close()")
	write := strings.Index(body, "writeErr = response.Write()")
	if prepare < 0 || unlock <= prepare || deferred <= unlock || write <= deferred || !strings.Contains(body[unlock:deferred], "if response != nil") || !strings.Contains(body[deferred:write], "if writeErr == nil && response != nil") {
		t.Fatal("prepared response can escape or write after failed owned postcheck")
	}
	release := strings.Index(body[write:], "_ = response.Close()")
	timingLock := strings.Index(body[write:], "c.op.Lock()")
	if release < 0 || timingLock <= release {
		t.Fatal("prepared response borrow spans timing persistence lock")
	}
}

func TestManagementAuthorizationAndProviderSourceOrdering(t *testing.T) {
	data, err := os.ReadFile("resource_management_runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	start := strings.Index(body, "func (c *Core) serveResourceManagement(")
	if start < 0 {
		t.Fatal("missing handler")
	}
	body = body[start:]
	owned := strings.Index(body, "err = c.withResourceInspectionState(")
	first := strings.Index(body, "c.authorizeManagementBound(")
	history := strings.Index(body, "c.managementOperationBound(")
	final := strings.LastIndex(body, "c.authorizeManagementBound(")
	prepare := strings.Index(body, "capability.PrepareReply(")
	if owned < 0 || first <= owned || history <= first || final <= history || prepare <= final {
		t.Fatal("history or reply crossed owned authorization ordering")
	}
	data, err = os.ReadFile("resource_management_operations.go")
	if err != nil {
		t.Fatal(err)
	}
	body = string(data)
	start = strings.Index(body, "func (c *Core) finishManagementIntentBound(")
	end := strings.Index(body[start:], "func (c *Core) completeManagementBound(")
	body = body[start : start+end]
	admit := strings.Index(body, "capability.AdmitProvider(c.resourceGrants.managementFence)")
	provider := strings.Index(body, "c.applyCapacityPolicyBound(proposed, b)")
	completion := strings.Index(body, "c.completeManagementBound(request, intent, outcome, b)")
	if admit < 0 || provider <= admit || completion <= provider || strings.Contains(body[admit:provider], "func(") {
		t.Fatal("provider lacks concrete one-shot admission or synchronous completion")
	}
}
