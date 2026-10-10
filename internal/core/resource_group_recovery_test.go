package core

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

// Pure receipt/copy and negative-admission tests: no owned profile, transport,
// clock, random identifier generation or persisted state is used here.
func groupSelectReceiptFixture(t *testing.T) (*resourceGroupPrepared, resourcegroup.SelectInput) {
	t.Helper()
	run := groupStoreRecordFixture(t, 1, 3)
	peers := append([]string{}, run.Review.ExecutionPeers[:2]...)
	input := resourcegroup.SelectInput{SchemaVersion: resourcegroup.SchemaVersion, ReviewID: run.RunID, ReviewRevision: run.Review.Revision, ExecutionPeers: peers}
	review, err := resourcegroup.BuildReview(run.Review.Selection, run.Review.Rows, peers)
	if err != nil {
		t.Fatal(err)
	}
	p := &resourceGroupPrepared{id: strings.Repeat("d", 32), review: review, state: resourcegroup.AdmissionPrepared}
	p.selectReceipt, err = newResourceGroupSelectReceipt(input, p)
	if err != nil {
		t.Fatal(err)
	}
	return p, input
}

func TestGroupSelectReceiptBindsCompleteCanonicalInput(t *testing.T) {
	p, input := groupSelectReceiptFixture(t)
	input.ExecutionPeers[0], input.ExecutionPeers[1] = input.ExecutionPeers[1], input.ExecutionPeers[0]
	if !resourceGroupSelectRepeatMatches(p, input) {
		t.Fatal("canonical exact input failed")
	}
	for _, field := range []string{"schema", "id", "revision", "subset", "duplicate", "nil"} {
		t.Run(field, func(t *testing.T) {
			changed := input
			changed.ExecutionPeers = append([]string{}, input.ExecutionPeers...)
			switch field {
			case "schema":
				changed.SchemaVersion++
			case "id":
				changed.ReviewID = strings.Repeat("e", 32)
			case "revision":
				changed.ReviewRevision = strings.Repeat("e", 64)
			case "subset":
				changed.ExecutionPeers = changed.ExecutionPeers[:1]
			case "duplicate":
				changed.ExecutionPeers[0] = changed.ExecutionPeers[1]
			case "nil":
				changed.ExecutionPeers = nil
			}
			if resourceGroupSelectRepeatMatches(p, changed) {
				t.Fatal("partial/expanded receipt matched")
			}
		})
	}
}

func TestGroupSelectReceiptOwnsIndependentBoundedInput(t *testing.T) {
	p, input := groupSelectReceiptFixture(t)
	before := append([]string{}, p.selectReceipt.input.ExecutionPeers...)
	input.ExecutionPeers[0] = strings.Repeat("f", 64)
	if !reflect.DeepEqual(before, p.selectReceipt.input.ExecutionPeers) {
		t.Fatal("receipt aliases caller input")
	}
	if len(groupStoreJSON(t, p.selectReceipt.input)) >= 2048 {
		t.Fatal("fixed receipt exceeded reviewed bound")
	}
	maximum := groupStoreRecordFixture(t, 1, resourcegroup.MaxMembers)
	maximumInput := resourcegroup.SelectInput{SchemaVersion: resourcegroup.SchemaVersion, ReviewID: maximum.RunID, ReviewRevision: maximum.Review.Revision, ExecutionPeers: maximum.Review.ExecutionPeers}
	if maximumInput.Validate() != nil || len(groupStoreJSON(t, maximumInput)) >= 2048 {
		t.Fatal("maximum receipt exceeded reviewed bound")
	}
	view, err := resourceGroupPreparedView(p)
	if err != nil {
		t.Fatal(err)
	}
	data := string(groupStoreJSON(t, view))
	for _, private := range []string{"selectReceipt", "resultID", "resultRevision"} {
		if strings.Contains(data, private) {
			t.Fatal("receipt leaked into public view")
		}
	}
}

func TestGroupSelectReceiptCannotForwardOrRestoreAdmission(t *testing.T) {
	p, input := groupSelectReceiptFixture(t)
	for _, field := range []string{"nil", "missing_receipt", "canceled", "result_id", "result_revision", "changed_subset", "preview_replacement"} {
		t.Run(field, func(t *testing.T) {
			candidate := *p
			r := *p.selectReceipt
			candidate.selectReceipt = &r
			switch field {
			case "nil":
				if resourceGroupSelectRepeatMatches(nil, input) {
					t.Fatal("consumed admission recreated")
				}
				return
			case "missing_receipt", "preview_replacement":
				candidate.selectReceipt = nil
			case "canceled":
				candidate.state = resourcegroup.AdmissionCanceled
			case "result_id":
				r.resultID = strings.Repeat("e", 32)
			case "result_revision":
				r.resultRevision = strings.Repeat("e", 64)
			case "changed_subset":
				var err error
				candidate.review, err = resourcegroup.BuildReview(p.review.Selection, p.review.Rows, []string{})
				if err != nil {
					t.Fatal(err)
				}
			}
			if resourceGroupSelectRepeatMatches(&candidate, input) {
				t.Fatal("unowned or changed result matched")
			}
		})
	}
	second := resourcegroup.SelectInput{SchemaVersion: resourcegroup.SchemaVersion, ReviewID: p.id, ReviewRevision: p.review.Revision, ExecutionPeers: []string{p.review.ExecutionPeers[0]}}
	next := *p
	next.id = strings.Repeat("e", 32)
	var err error
	next.review, err = resourcegroup.BuildReview(p.review.Selection, p.review.Rows, second.ExecutionPeers)
	if err != nil {
		t.Fatal(err)
	}
	next.selectReceipt, err = newResourceGroupSelectReceipt(second, &next)
	if err != nil || !resourceGroupSelectRepeatMatches(&next, second) || resourceGroupSelectRepeatMatches(&next, input) {
		t.Fatal("receipt forwarded beyond immediate predecessor", err)
	}
}

func TestGroupSelectReceiptEmptySubsetStaysUnavailable(t *testing.T) {
	p, input := groupSelectReceiptFixture(t)
	input.ExecutionPeers = []string{}
	var err error
	p.review, err = resourcegroup.BuildReview(p.review.Selection, p.review.Rows, input.ExecutionPeers)
	if err != nil {
		t.Fatal(err)
	}
	p.state = resourcegroup.AdmissionUnavailable
	p.selectReceipt, err = newResourceGroupSelectReceipt(input, p)
	if err != nil || !resourceGroupSelectRepeatMatches(p, input) {
		t.Fatal("exact empty subset recovery rejected", err)
	}
	view, err := resourceGroupPreparedView(p)
	if err != nil || view.AdmissionState != resourcegroup.AdmissionUnavailable || len(view.Review.ExecutionPeers) != 0 {
		t.Fatal("empty subset became executable")
	}
}

func TestGroupRecoveryWithoutCurrentOwnershipReturnsNoRows(t *testing.T) {
	for _, mode := range []string{"absent_store", "active", "consumed", "canceled_context", "stopped_core"} {
		t.Run(mode, func(t *testing.T) {
			p, input := groupSelectReceiptFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := &Core{ctx: context.Background(), resourceGroups: &resourceGroupCoordinator{prepared: p}}
			switch mode {
			case "active":
				c.resourceGroups.active = &resourceGroupActivity{}
			case "consumed":
				c.resourceGroups.prepared = nil
			case "canceled_context":
				cancel()
			case "stopped_core":
				c.ctx = ctx
				cancel()
			}
			if result, err := c.recoverResourceGroupSelectLocked(ctx, p, input); err == nil || result != nil {
				t.Fatal("select recovery disclosed unowned rows")
			}
			if result, err := c.resourceGroupCurrentReviewLocked(ctx); err == nil || result != nil {
				t.Fatal("current recovery disclosed unowned rows")
			}
		})
	}
}

func TestGroupRecoveryExactCancelClearsOnlyMatchingReceipt(t *testing.T) {
	p, input := groupSelectReceiptFixture(t)
	c := &Core{resourceGroups: &resourceGroupCoordinator{prepared: p}}
	if canceled, _ := c.reduceResourceGroupCancelLocked(input.ReviewID); canceled != nil || !resourceGroupSelectRepeatMatches(p, input) {
		t.Fatal("old exact ID canceled replacement")
	}
	if canceled, _ := c.reduceResourceGroupCancelLocked(p.id); canceled != p || p.selectReceipt != nil || resourceGroupSelectRepeatMatches(p, input) {
		t.Fatal("exact cancellation retained recoverable admission")
	}
}
