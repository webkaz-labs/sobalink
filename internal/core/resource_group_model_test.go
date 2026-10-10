package core

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

func TestGroupPreviewFailedFinalOwnershipReturnsNoRows(t *testing.T) {
	run := groupStoreRecordFixture(t, 1, 1)
	p := &resourceGroupPrepared{id: run.RunID, review: run.Review, state: resourcegroup.AdmissionUnavailable}
	c := &Core{ctx: context.Background(), resourceGroups: &resourceGroupCoordinator{}}
	for _, stop := range []resourcegroup.StopReason{resourcegroup.StopNone, resourcegroup.StopUserCanceled} {
		result, err := c.finishResourceGroupPreviewLocked(context.Background(), &resourceGroupActivity{stop: stop}, p, run.Review)
		if err == nil || result != nil || c.resourceGroups.prepared != nil {
			t.Fatal("invalidated owned preview disclosed ready rows or restored admission")
		}
	}
}

func TestGroupPreviewRepeatBindsCompleteCanonicalInputAndCannotRestore(t *testing.T) {
	run := groupStoreRecordFixture(t, 1, 2)
	selection := resourcegroup.Selection{SchemaVersion: resourcegroup.SchemaVersion, Template: run.Review.Selection.Template, Members: make([]resourcegroup.Member, 2)}
	for i, member := range run.Review.Selection.Members {
		selection.Members[i] = resourcegroup.Member{PeerKey: member.PeerKey, Selector: member.Selector, Override: member.Override}
	}
	input := resourcegroup.PreviewInput{SchemaVersion: resourcegroup.SchemaVersion, Selection: selection, ReplaceReviewID: strings.Repeat("d", 32)}
	hash, err := resourceGroupPreviewInputHash(input)
	if err != nil {
		t.Fatal(err)
	}
	p := &resourceGroupPrepared{id: run.RunID, review: run.Review, previewInputHash: hash, state: resourcegroup.AdmissionPrepared}
	input.Selection.Members[0], input.Selection.Members[1] = input.Selection.Members[1], input.Selection.Members[0]
	same, err := resourceGroupPreviewInputHash(input)
	if err != nil || same != hash || !resourceGroupPreviewRepeatMatches(p, same) {
		t.Fatal("canonical exact retry changed identity")
	}
	view, err := resourceGroupPreparedView(p)
	if err != nil || view.ReviewID != p.id || view.Review.Revision != p.review.Revision {
		t.Fatal("same view read generated new identity")
	}
	input.ReplaceReviewID = ""
	different, _ := resourceGroupPreviewInputHash(input)
	if resourceGroupPreviewRepeatMatches(p, different) {
		t.Fatal("replacement predecessor omitted from repeat binding")
	}
	input.ReplaceReviewID = strings.Repeat("d", 32)
	input.Selection.Members[0].Override = nil
	different, _ = resourceGroupPreviewInputHash(input)
	if resourceGroupPreviewRepeatMatches(p, different) {
		t.Fatal("explicit override omitted from repeat binding")
	}
	if resourceGroupPreviewRepeatMatches(nil, hash) {
		t.Fatal("consumed record recreated by hash")
	}
	p.state = resourcegroup.AdmissionCanceled
	if resourceGroupPreviewRepeatMatches(p, hash) {
		t.Fatal("canceled review recreated by exact input")
	}
	p.state = resourcegroup.AdmissionPrepared
	c := &Core{ctx: context.Background(), resourceGroups: &resourceGroupCoordinator{prepared: p}}
	result, err := c.recoverResourceGroupPreviewLocked(context.Background(), p)
	if err == nil || result != nil {
		t.Fatal("changed owned context disclosed repeated review")
	}
}

func TestGroupCompletionSaveFailurePreservesTargetDurabilitySeparately(t *testing.T) {
	envelope, _ := newResourceGroupEnvelope(strings.Repeat("a", 32))
	intent := groupStoreRecordFixture(t, 1, 2)
	intent.Evidence.Members[0].Dispatch = resourcegroup.Dispatching
	intent.UpdateSequence = 2
	envelope.Runs = []resourceGroupRecord{intent}
	*envelope.HighWater = 1
	completed, _ := cloneResourceGroupRecord(intent)
	completed.UpdateSequence++
	first := completed.Evidence.Members[0]
	durable := true
	target := &resourcegrant.ManagementOperation{OperationID: first.Request.Apply.OperationID, Outcome: groupStoreEnvelopeFixture(t, 1, false).Runs[0].Evidence.Members[0].Target.Outcome, EvidenceDurable: &durable}
	reply := resourcegrant.ManagementReply{ManagementSelector: first.Request.ManagementSelector, Action: resourcegrant.ApplyAction, Operation: target}
	completed.Evidence.Members[0], _ = resourcegroup.ObserveApply(first, reply, resourcegroup.LocalDurable)
	g := &resourceGroupCoordinator{store: &resourceGroupStore{state: envelope, frozen: true, uncertain: true}}
	g.retainFailedResourceGroupPublication(completed)
	if g.overlay == nil || g.overlay.Evidence.Members[0].LocalDurability != resourcegroup.LocalUncertain || !*g.overlay.Evidence.Members[0].Target.EvidenceDurable || g.store.state.Runs[0].Evidence.Members[0].Target != nil {
		t.Fatal("terminal save failure erased target evidence or promoted local durability")
	}
	*target.EvidenceDurable = false
	if !*g.overlay.Evidence.Members[0].Target.EvidenceDurable {
		t.Fatal("reply alias changed retained target evidence")
	}
}

func TestGroupCoordinatorViewsAreIndependentCopies(t *testing.T) {
	run := groupStoreRecordFixture(t, 1, 2)
	p := &resourceGroupPrepared{id: run.RunID, review: run.Review, state: resourcegroup.AdmissionPrepared, firstUse: true}
	view, err := resourceGroupPreparedView(p)
	if err != nil {
		t.Fatal(err)
	}
	*view.InitializesLocalEvidence = false
	*view.Review.Selection.Template.Settings.TransferConcurrentFiles.Value = 1
	view.Review.ExecutionPeers[0] = strings.Repeat("f", 64)
	if !p.firstUse || *p.review.Selection.Template.Settings.TransferConcurrentFiles.Value != capacity.MaxJSONInteger || p.review.ExecutionPeers[0] == view.Review.ExecutionPeers[0] {
		t.Fatal("prepared display mutated owned admission")
	}
	runView, err := resourceGroupRunView(run, resourcegroup.ActivityIdle)
	if err != nil {
		t.Fatal(err)
	}
	*runView.Evidence.Members[0].Request.Apply.Settings.TransferConcurrentFiles.Value = 1
	if *run.Evidence.Members[0].Request.Apply.Settings.TransferConcurrentFiles.Value != capacity.MaxJSONInteger {
		t.Fatal("status display changed original request")
	}
	data, _ := json.Marshal(runView)
	if strings.Contains(string(data), "pairBinding") || strings.Contains(string(data), "controllerID") || strings.Contains(string(data), "bootNonce") {
		t.Fatal("public view disclosed private correlation")
	}
}

func TestGroupCoordinatorExactReviewRequiresOwnedPreparedState(t *testing.T) {
	run := groupStoreRecordFixture(t, 1, 2)
	p := &resourceGroupPrepared{id: run.RunID, review: run.Review, state: resourcegroup.AdmissionPrepared}
	in := run.acceptedInput()
	if !resourceGroupSameSelection(in, p) || resourceGroupSameSelection(in, nil) {
		t.Fatal("exact prepared comparison")
	}
	for _, field := range []string{"confirm", "id", "revision", "subset", "canceled", "unavailable"} {
		t.Run(field, func(t *testing.T) {
			changed, prepared := in, *p
			switch field {
			case "confirm":
				changed.Confirm = false
			case "id":
				changed.ReviewID = strings.Repeat("f", 32)
			case "revision":
				changed.ReviewRevision = strings.Repeat("f", 64)
			case "subset":
				changed.ExecutionPeers = changed.ExecutionPeers[:1]
			case "canceled":
				prepared.state = resourcegroup.AdmissionCanceled
			case "unavailable":
				prepared.state = resourcegroup.AdmissionUnavailable
			}
			if resourceGroupSameSelection(changed, &prepared) {
				t.Fatal("imported or changed review admitted", field)
			}
		})
	}
}

func TestGroupCoordinatorFailedPublicationKeepsLastConfirmedEvidence(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		run := groupStoreRecordFixture(t, 1, 2)
		envelope, _ := newResourceGroupEnvelope(strings.Repeat("a", 32))
		envelope, err := withResourceGroupRun(envelope, run)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := cloneResourceGroupEnvelope(envelope)
		g := &resourceGroupCoordinator{store: &resourceGroupStore{state: envelope, frozen: true, uncertain: uncertain}, prepared: &resourceGroupPrepared{id: run.RunID}}
		intent, _ := cloneResourceGroupRecord(run)
		intent.UpdateSequence++
		intent.Evidence.Members[0].Dispatch = resourcegroup.Dispatching
		g.retainFailedResourceGroupPublication(intent)
		if g.prepared != nil || !reflect.DeepEqual(before, g.store.state) || g.overlay == nil {
			t.Fatal("failure restored approval or promoted attempted state")
		}
		wantDispatch, wantLocal := resourcegroup.DispatchNotAttempted, resourcegroup.LocalNotSaved
		if uncertain {
			wantDispatch, wantLocal = resourcegroup.DispatchUnknown, resourcegroup.LocalUncertain
		}
		if g.overlay.Evidence.Members[0].Dispatch != wantDispatch || g.overlay.Evidence.Members[0].LocalDurability != wantLocal || g.overlay.Evidence.Members[1].AdmissionStop != resourcegroup.StopPersistenceUncertain {
			t.Fatal("failure evidence conflated publication with remote outcome")
		}
		if _, err := resourceGroupRunView(*g.overlay, resourcegroup.ActivityIdle); err != nil {
			t.Fatal("bounded failure overlay cannot be shown", err)
		}
	}
}

func TestGroupCoordinatorStoppedAdmissionPreservesEarlierTargetResult(t *testing.T) {
	run := groupStoreRecordFixture(t, 1, 3)
	durable := true
	first := &run.Evidence.Members[0]
	first.Dispatch = resourcegroup.DispatchObserved
	first.Target = &resourcegrant.ManagementOperation{OperationID: first.Request.Apply.OperationID, Outcome: groupStoreEnvelopeFixture(t, 1, false).Runs[0].Evidence.Members[0].Target.Outcome, EvidenceDurable: &durable}
	run.Evidence.Members[1].Dispatch = resourcegroup.Dispatching
	next, err := resourceGroupStoppedRecord(run, resourcegroup.StopUserCanceled)
	if err != nil || !reflect.DeepEqual(next.Evidence.Members[0].Target, first.Target) || next.Evidence.Members[1].Dispatch != resourcegroup.DispatchUnknown || next.Evidence.Members[2].Dispatch != resourcegroup.DispatchNotAttempted || next.Evidence.Members[2].AdmissionStop != resourcegroup.StopUserCanceled || next.Admission != resourceGroupAdmissionFinished {
		t.Fatal("local stop inferred remote cancellation or erased prior evidence", err)
	}
	if run.Evidence.Members[1].Dispatch != resourcegroup.Dispatching || run.Evidence.Members[2].AdmissionStop != resourcegroup.StopNone {
		t.Fatal("stopped reduction changed prior snapshot")
	}
}

func TestGroupStatusOriginalSelectorCannotBeSubstituted(t *testing.T) {
	run := groupStoreEnvelopeFixture(t, 1, true).Runs[0]
	row := run.Evidence.Members[0]
	request, err := resourceGroupOriginalStatusRequest(row)
	if err != nil || request.Action != resourcegrant.StatusAction || request.ManagementSelector != row.Request.ManagementSelector || request.Status.OperationID != row.Request.Apply.OperationID || request.Apply != nil {
		t.Fatal("status substituted selector or action", err)
	}
	request.GrantRevision--
	request.Status.OperationID = strings.Repeat("f", 64)
	if row.Request.GrantRevision != uint64(capacity.MaxJSONInteger) || row.Request.Apply.OperationID == request.Status.OperationID {
		t.Fatal("status request mutated original apply evidence")
	}
}

func TestGroupCoordinatorRefreshRejectsUnsentOrExpandedPeers(t *testing.T) {
	run := groupStoreRecordFixture(t, 1, 2)
	peers := run.Review.ExecutionPeers
	if _, err := resourceGroupRefreshIndices(run, peers); err == nil {
		t.Fatal("unsent rows became status candidates")
	}
	run.Evidence.Members[0].Dispatch = resourcegroup.DispatchUnknown
	indices, err := resourceGroupRefreshIndices(run, peers[:1])
	if err != nil || !reflect.DeepEqual(indices, []int{0}) {
		t.Fatal("exact possible-dispatch query rejected")
	}
	for _, invalid := range [][]string{nil, {}, peers, {peers[0], peers[0]}, {strings.Repeat("f", 64)}} {
		if _, err := resourceGroupRefreshIndices(run, invalid); err == nil {
			t.Fatal("expanded, duplicate or unsent query accepted")
		}
	}
}
