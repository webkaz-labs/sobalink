package core

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

// This cohort uses the existing synthetic profile/process-lock fixture. It
// does not call Core.Open, start a backend or exchange with a peer. Positive
// prepared rows intentionally omit captured origins to isolate local receipt
// and storage decisions; they are not proof of positive transport authority.
// Changed-select coverage additionally uses the existing OS random ID source.
func groupOwnedRecoveryFixture(t *testing.T) (*Core, *resourceGroupPrepared, resourcegroup.SelectInput) {
	t.Helper()
	c, s := groupOwnedStoreFixture(t) // Core.op remains held until cleanup.
	c.ctx, c.resourceNonce = context.Background(), strings.Repeat("b", 32)
	p, input := groupSelectReceiptFixture(t)
	p.controllerID, p.bootNonce, p.storeRevision, p.firstUse = c.resourceIdentity, c.resourceNonce, resourceDigest(s.state), s.firstUse
	c.resourceGroups = &resourceGroupCoordinator{store: s, prepared: p}
	return c, p, input
}

func groupRecoverySelectUnlocked(c *Core, ctx context.Context, input resourcegroup.SelectInput) (any, error) {
	c.op.Unlock()
	defer c.op.Lock()
	return c.resourceGroupSelect(ctx, input)
}

func TestGroupOwnedRecoverySelectReturnsSameCurrentWithoutWrites(t *testing.T) {
	c, p, input := groupOwnedRecoveryFixture(t)
	before := groupStoreJSON(t, c.resourceGroups.store.state)
	want, err := resourceGroupPreparedView(p)
	if err != nil {
		t.Fatal(err)
	}
	receipt := p.selectReceipt
	for i := 0; i < 2; i++ {
		result, err := groupRecoverySelectUnlocked(c, context.Background(), input)
		if err != nil || !reflect.DeepEqual(result, want) || c.resourceGroups.prepared != p || p.selectReceipt != receipt {
			t.Fatal("repeat changed identity, tokens or admission", err)
		}
		current, err := c.resourceGroupCurrentReviewLocked(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		currentView := current.(resourcegroup.CurrentReviewView)
		if currentView.State != resourcegroup.CurrentReviewCurrent || !reflect.DeepEqual(*currentView.Prepared, want) {
			t.Fatal("current lookup changed identity or tokens")
		}
	}
	if string(before) != string(groupStoreJSON(t, c.resourceGroups.store.state)) {
		t.Fatal("repeat mutated retained evidence")
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "process.lock" {
		t.Fatal("recovery allocated persistent state")
	}
}

func TestGroupOwnedRecoveryNoopPreservesReceiptAndNextSelectReplacesIt(t *testing.T) {
	c, p, input := groupOwnedRecoveryFixture(t)
	noop := resourcegroup.SelectInput{SchemaVersion: resourcegroup.SchemaVersion, ReviewID: p.id, ReviewRevision: p.review.Revision, ExecutionPeers: append([]string{}, p.review.ExecutionPeers...)}
	if _, err := groupRecoverySelectUnlocked(c, context.Background(), noop); err != nil || c.resourceGroups.prepared != p || !resourceGroupSelectRepeatMatches(p, input) {
		t.Fatal("no-op consumed recovery receipt", err)
	}
	next := noop
	next.ExecutionPeers = []string{noop.ExecutionPeers[0]}
	result, err := groupRecoverySelectUnlocked(c, context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	replacement := c.resourceGroups.prepared
	if replacement == p || replacement.id == p.id || !resourceGroupSelectRepeatMatches(replacement, next) || resourceGroupSelectRepeatMatches(replacement, input) {
		t.Fatal("changed select retained a forwarding chain")
	}
	recovered, err := groupRecoverySelectUnlocked(c, context.Background(), next)
	if err != nil || !reflect.DeepEqual(result, recovered) {
		t.Fatal("new one-edge receipt could not recover reply", err)
	}
	if rows, err := groupRecoverySelectUnlocked(c, context.Background(), input); err == nil || rows != nil {
		t.Fatal("two-hop predecessor recovered later review")
	}
	if canceled, _ := c.reduceResourceGroupCancelLocked(p.id); canceled != nil || replacement.state != resourcegroup.AdmissionPrepared {
		t.Fatal("replace-before-exact-cancel affected replacement")
	}
}

func TestGroupOwnedCurrentReviewIsIndependentAndDoesNotClearHistory(t *testing.T) {
	c, p, _ := groupOwnedRecoveryFixture(t)
	groupOwnedAcceptedFixture(t, c, c.resourceGroups.store)
	p.storeRevision, p.firstUse = resourceDigest(c.resourceGroups.store.state), c.resourceGroups.store.firstUse
	overlay := groupStoreEnvelopeFixture(t, 1, true).Runs[0]
	c.resourceGroups.overlay = &overlay
	before := groupStoreJSON(t, c.resourceGroups.store.state)
	uncertain := groupStoreJSON(t, overlay)
	result, err := c.resourceGroupCurrentReviewLocked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	view, ok := result.(resourcegroup.CurrentReviewView)
	if !ok || view.State != resourcegroup.CurrentReviewCurrent || view.Prepared.ReviewID != p.id {
		t.Fatal("current review identity changed")
	}
	*view.Prepared.InitializesLocalEvidence = !p.firstUse
	*view.Prepared.Review.Selection.Template.Settings.TransferConcurrentFiles.Value = 1
	if *p.review.Selection.Template.Settings.TransferConcurrentFiles.Value == 1 {
		t.Fatal("current view aliases admission")
	}
	c.resourceGroups.prepared = nil // models consumed admission; no reconstruction.
	result, err = c.resourceGroupCurrentReviewLocked(context.Background())
	if err != nil || result.(resourcegroup.CurrentReviewView).State != resourcegroup.CurrentReviewNone {
		t.Fatal("consumed review surfaced as prepared", err)
	}
	if string(before) != string(groupStoreJSON(t, c.resourceGroups.store.state)) || string(uncertain) != string(groupStoreJSON(t, c.resourceGroups.overlay)) {
		t.Fatal("current read reduced saved uncertainty")
	}
}

func TestGroupOwnedCurrentCanceledCleanupRequiresOwnedDisclosure(t *testing.T) {
	for _, frozen := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "frozen"}[frozen], func(t *testing.T) {
			c, p, input := groupOwnedRecoveryFixture(t)
			c.reduceResourceGroupCancelLocked(p.id)
			c.resourceGroups.store.frozen = frozen
			result, err := c.resourceGroupCurrentReviewLocked(context.Background())
			if c.resourceGroups.prepared != nil || resourceGroupSelectRepeatMatches(p, input) {
				t.Fatal("canceled admission survived cleanup")
			}
			if frozen {
				if err == nil || result != nil {
					t.Fatal("frozen owner disclosed none")
				}
			} else if err != nil || result.(resourcegroup.CurrentReviewView).State != resourcegroup.CurrentReviewNone {
				t.Fatal("canceled unused review remained an invisible blocker", err)
			}
		})
	}
}

func TestGroupOwnedRecoveryInvalidationReturnsNoRows(t *testing.T) {
	for _, mode := range []string{"controller", "boot", "store_revision", "first_use", "origin", "frozen", "uncertain", "active"} {
		for _, current := range []bool{false, true} {
			name := mode + "/select"
			if current {
				name = mode + "/current"
			}
			t.Run(name, func(t *testing.T) {
				c, p, input := groupOwnedRecoveryFixture(t)
				switch mode {
				case "controller":
					p.controllerID = strings.Repeat("f", 32)
				case "boot":
					p.bootNonce = strings.Repeat("f", 32)
				case "store_revision":
					p.storeRevision = strings.Repeat("f", 64)
				case "first_use":
					p.firstUse = !p.firstUse
				case "origin":
					p.origins = []resourceGroupCapturedOrigin{{peerKey: p.review.ExecutionPeers[0], origin: &resourceManagementOrigin{}}}
				case "frozen":
					c.resourceGroups.store.frozen = true
				case "uncertain":
					c.resourceGroups.store.frozen, c.resourceGroups.store.uncertain = true, true
				case "active":
					c.resourceGroups.active = &resourceGroupActivity{}
				}
				var result any
				var err error
				if current {
					result, err = c.resourceGroupCurrentReviewLocked(context.Background())
				} else {
					result, err = c.recoverResourceGroupSelectLocked(context.Background(), p, input)
				}
				if err == nil || result != nil {
					t.Fatal("invalidated recovery disclosed rows")
				}
				if mode != "active" && c.resourceGroups.prepared != nil {
					t.Fatal("stale unused admission remained recoverable")
				}
			})
		}
	}
}

// Cancels synchronously at a later Err check without a goroutine or timer.
// The embedded ordinary context retains its normal Done/Err relationship.
type groupRecoveryCancelOnCheck struct {
	context.Context
	cancel context.CancelFunc
	checks int
	after  int
}

func (ctx *groupRecoveryCancelOnCheck) Err() error {
	ctx.checks++
	if ctx.checks > ctx.after {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestGroupOwnedRecoveryFinalLivenessPreventsDisclosure(t *testing.T) {
	for _, current := range []bool{false, true} {
		c, p, input := groupOwnedRecoveryFixture(t)
		base, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		ctx := &groupRecoveryCancelOnCheck{Context: base, cancel: cancel, after: 1}
		var result any
		var err error
		if current {
			result, err = c.resourceGroupCurrentReviewLocked(ctx)
		} else {
			result, err = c.recoverResourceGroupSelectLocked(ctx, p, input)
		}
		if err == nil || result != nil || ctx.checks < 2 {
			t.Fatal("canceled final read disclosed prepared rows")
		}
		if c.resourceGroups.prepared != p {
			t.Fatal("cancellation unexpectedly renewed or replaced admission")
		}
	}
}
