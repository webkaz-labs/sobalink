//go:build resource_group_catalog_native && resource_management_native && resource_inspection_native && directlan_activation_native

package core

import (
	"context"
	"errors"
	"flag"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourceacceptance"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

// SOURCE ONLY until a separate exact build and native execution gate.
// This is ordinary joined Core.Close/Open, never an OS-process restart.
const resourceGroupRestartNativeSelector = "^TestResourceGroupNativePostGroupCoreReopenNoReplay$"

func newResourceGroupRestartNative(t *testing.T) *resourceGroupCatalogNative {
	t.Helper()
	// Independent N4 guard: no allocations, goroutines, ports or Core yet.
	if os.Getenv("SOBALINK_RUN_RESOURCE_GROUP_RESTART_NATIVE") != "reviewed-post-group-core-reopen-v1" {
		t.Skip("requires separately reviewed post-group Core reopen execution")
	}
	if os.Getenv("SOBALINK_RUN_ACTIVATION_NATIVE") != "1" || !directLANNetstackReady {
		t.Fatal("explicit native activation prerequisite is missing")
	}
	run := flag.Lookup("test.run")
	if run == nil || run.Value.String() != resourceGroupRestartNativeSelector {
		t.Fatal("exact single native group-reopen selector is required")
	}
	deadline, ok := t.Deadline()
	if !ok || time.Until(deadline) > 4*time.Minute || time.Until(deadline) < 200*time.Second {
		t.Fatal("native group reopen requires the reviewed four-minute test budget")
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "TS_PROXY"} {
		if os.Getenv(name) != "" {
			t.Fatal("native group reopen requires an isolated proxy-free environment")
		}
	}
	return newResourceGroupCatalogNativeAfterGuard(t)
}

func resourceGroupRestartSelection(t *testing.T, f *resourceGroupCatalogNative, grants [2]resourcegrant.Record, wanted [2]resource.Settings) resourcegroup.Selection {
	t.Helper()
	template, err := resourcegroup.NewTemplate(wanted[0])
	if err != nil {
		t.Fatal("synthetic fixed template invalid")
	}
	selection := resourcegroup.Selection{SchemaVersion: 1, Template: template, Members: []resourcegroup.Member{}}
	for i, grant := range grants {
		member := resourcegroup.Member{PeerKey: f.identities[i+1].PublicKey(), Selector: managementSelector(grant)}
		if i == 1 {
			member.Override = &resourcegroup.Override{TransferConcurrentFiles: &wanted[i].TransferConcurrentFiles, TransferConcurrentPerPeer: &wanted[i].TransferConcurrentPerPeer}
		}
		selection.Members = append(selection.Members, member)
	}
	return selection
}

func (f *resourceGroupCatalogNative) restartPrepared(selection resourcegroup.Selection, wanted [2]resource.Settings, first bool) (resourcegroup.PreparedView, resourcegroup.ApplyInput) {
	f.t.Helper()
	value := f.group(resourcegroup.LocalPreviewCommand, resourcegroup.PreviewInput{SchemaVersion: 1, Selection: selection})
	prepared, ok := value.(resourcegroup.PreparedView)
	decoded, err := resourcegroup.DecodePreparedView(resourceGroupCatalogReplyJSON(f.t, value))
	if !ok || err != nil || !reflect.DeepEqual(decoded, prepared) || prepared.AdmissionState != resourcegroup.AdmissionPrepared || prepared.InitializesLocalEvidence == nil || *prepared.InitializesLocalEvidence != first || len(prepared.Review.Rows) != 2 || len(prepared.Review.ExecutionPeers) != 2 {
		f.t.Fatal("two-target prepared review was not exact and complete")
	}
	for i, row := range prepared.Review.Rows {
		if row.State != resourcegroup.ReviewReady || row.PeerKey != selection.Members[i].PeerKey || prepared.Review.ExecutionPeers[i] != row.PeerKey || row.Reply == nil || row.Reply.Preview == nil || row.Reply.ManagementSelector != selection.Members[i].Selector || !reflect.DeepEqual(row.Reply.Preview.Requested, wanted[i]) {
			f.t.Fatal("prepared row lost its actual target, grant or settings")
		}
	}
	apply := resourcegroup.ApplyInput{SchemaVersion: 1, ReviewID: prepared.ReviewID, ReviewRevision: prepared.Review.Revision, ExecutionPeers: append([]string{}, prepared.Review.ExecutionPeers...), Confirm: true}
	return prepared, apply
}

func (f *resourceGroupCatalogNative) reopenControllerAfterGroup() {
	f.t.Helper()
	old, owner := f.cores[0], f.owners[0]
	backend := old.nodeCopy().(*directLANBackend)
	oldNode, completion := backend.Node, backend.currentCompletion()
	profile, state := old.profileCopy(), old.directLAN.copy()
	resourceID, resourceNonce, lanNonce, processID := old.resourceIdentity, old.resourceNonce, old.lanStartNonce, old.resourceCatalogProcessID()
	// The pre-consent setup owner already joined successfully. Retain this
	// measured old controller until final cleanup, without stopping observation.
	owner.startClose()
	wait, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	select {
	case <-owner.done:
		if owner.err != nil {
			f.t.Fatal("post-group controller close failed; no reopen attempted")
		}
	case <-wait.Done():
		f.t.Fatal("post-group controller close did not join; no reopen attempted")
	}
	f.retired = owner
	lock, err := config.AcquireLock(old.dir)
	if err != nil {
		f.t.Fatal("same-directory lifecycle ownership could not be reacquired")
	}
	freshOwner := &resourceInspectionNativeOwner{lock: lock, done: make(chan struct{})}
	f.owners[0], f.cores[0] = freshOwner, nil
	if !resourceacceptance.ArmControllerReopen(resourceacceptance.Owner{Core: old, Node: oldNode}, lock) {
		f.t.Fatal("continuous one-shot controller observation could not be armed")
	}
	fresh, err := Open(f.ctx, Options{Directory: old.dir, Version: "synthetic-group-catalog", LifecycleLock: lock, EnableResourceInspection: true,
		NodeFactory: func(string, string) (NetworkBackend, error) {
			return nil, errors.New("non-direct synthetic backend is forbidden")
		}})
	freshOwner.core, f.cores[0] = fresh, fresh
	if err != nil || fresh == nil {
		f.t.Fatal("normal post-group controller Open failed")
	}
	if fresh == old || fresh.resourceIdentity != resourceID || fresh.resourceNonce == resourceNonce || fresh.lanStartNonce == lanNonce || fresh.resourceCatalogProcessID() == processID || !resourceInspectionNativeSameProfile(profile, fresh.profileCopy()) || !resourceInspectionNativeSameAuthority(state, fresh.directLAN.copy()) {
		f.t.Fatal("post-group reopen changed retained identity/authority or reused a process token")
	}
	fresh.op.Lock()
	current, ok := fresh.nodeCopy().(*directLANBackend)
	valid := ok && current != nil && current != backend && current.Node != nil && current.Node != oldNode && current.currentCompletion() != nil && current.currentCompletion() != completion
	g := fresh.resourceGroups
	valid = valid && g != nil && g.prepared == nil && g.active == nil && g.store != nil && g.store.certified && !g.store.firstUse && !g.store.frozen && !g.store.uncertain && len(g.store.state.Runs) == 1
	fresh.op.Unlock()
	if !valid || resourceacceptance.RolloverSnapshot().Stage != resourceacceptance.RolloverObserving || resourceacceptance.RolloverSnapshot().Invalid {
		f.t.Fatal("fresh ordinary owner or uninterrupted startup observation is missing")
	}
	f.assertOrdinary(0)
	f.assertPair(1)
	f.assertPair(2)
}

func (f *resourceGroupCatalogNative) observeRestartMaintenance(want resourceacceptance.View) {
	f.t.Helper()
	f.assertOrdinary(0)
	view := resourceacceptance.RolloverSnapshot()
	if view.Invalid || view.Stage != resourceacceptance.RolloverObserving || view.MaintenancePasses > resourceacceptance.MaxMaintenancePasses-2 {
		f.t.Fatal("post-reopen observation has no bounded ordinary-owner interval")
	}
	// Two later tails ensure at least one whole iteration began after this
	// boundary even if one earlier iteration was already underway. No product
	// startup is delayed; these are ordinary two-second maintenance passes.
	until := view.MaintenancePasses + 2
	ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
	defer cancel()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		view = resourceacceptance.RolloverSnapshot()
		if view.Invalid || view.Stage != resourceacceptance.RolloverObserving || resourceacceptance.Snapshot() != want {
			f.t.Fatal("startup or maintenance replayed a management action")
		}
		if view.MaintenancePasses >= until {
			break
		}
		select {
		case <-ctx.Done():
			f.t.Fatal("ordinary post-reopen maintenance did not complete in the original budget")
		case <-tick.C:
		}
	}
	f.assertOrdinary(0) // A completed iteration alone is not a ready-network proof.
}

func TestResourceGroupNativePostGroupCoreReopenNoReplay(t *testing.T) {
	f := newResourceGroupRestartNative(t)
	grants := [2]resourcegrant.Record{f.confirm(1), f.confirm(2)}
	policies := [2]capacity.Policy{f.cores[1].capacityPolicy(), f.cores[2].capacityPolicy()}
	wanted := [2]resource.Settings{resourceManagementNativeSettings(3, 1), resourceManagementNativeSettings(4, 2)}
	selection := resourceGroupRestartSelection(t, f, grants, wanted)
	before := [3][3]resourceGroupCatalogFile{f.files(0), f.files(1), f.files(2)}
	prepared, apply := f.restartPrepared(selection, wanted, true)
	for i := range f.cores {
		f.assertFiles(i, before[i])
	}
	value := f.group(resourcegroup.LocalApplyCommand, apply)
	run, ok := value.(resourcegroup.RunView)
	decoded, err := resourcegroup.DecodeRunView(resourceGroupCatalogReplyJSON(t, value))
	if !ok || err != nil || !reflect.DeepEqual(decoded, run) || run.RunID != prepared.ReviewID || !reflect.DeepEqual(run.Review, prepared.Review) || run.Activity != resourcegroup.ActivityIdle || run.LocalDurability != resourcegroup.LocalDurable || !run.Summary.AllApplied || run.Summary.Applied != 2 || run.Summary.Executable != 2 || len(run.Evidence.Members) != 2 {
		t.Fatal("real completed two-target group lacks exact durable evidence")
	}
	for i, row := range run.Evidence.Members {
		f.assertApplied(i+1, wanted[i], policies[i], row)
		f.assertGrant(i+1, grants[i])
	}
	completed := [3][3]resourceGroupCatalogFile{f.files(0), f.files(1), f.files(2)}
	if !reflect.DeepEqual(completed[0][0], before[0][0]) || !reflect.DeepEqual(completed[0][1], before[0][1]) || !completed[0][2].present {
		t.Fatal("controller settings changed or completed history is absent")
	}
	saved, err := decodeResourceGroupEnvelope(completed[0][2].data)
	if err != nil || len(saved.Runs) != 1 || *saved.HighWater != 1 || saved.Runs[0].RunID != run.RunID || saved.Runs[0].Admission != resourceGroupAdmissionFinished || !reflect.DeepEqual(saved.Runs[0].Review, run.Review) || !reflect.DeepEqual(saved.Runs[0].Evidence, run.Evidence) {
		t.Fatal("completed controller journal does not match the actual run")
	}
	baseline := f.assertEvents(run)
	unusedWanted := [2]resource.Settings{resourceManagementNativeSettings(5, 2), resourceManagementNativeSettings(6, 3)}
	unusedSelection := resourceGroupRestartSelection(t, f, grants, unusedWanted)
	unused, staleApply := f.restartPrepared(unusedSelection, unusedWanted, false)
	if unused.ReviewID == run.RunID {
		t.Fatal("unused prepared review reused the completed run identity")
	}
	currentValue := f.local(0, resourcegroup.LocalCurrentReviewCommand, resourcegroup.CurrentReviewInput{SchemaVersion: 1})
	currentReview, ok := currentValue.(resourcegroup.CurrentReviewView)
	if !ok || currentReview.State != resourcegroup.CurrentReviewCurrent || currentReview.Prepared == nil || !reflect.DeepEqual(*currentReview.Prepared, unused) {
		t.Fatal("unused actual prepared review was not retained before reopen")
	}
	trace := resourceacceptance.Snapshot()
	if trace.InvalidOrOverflow || trace.Count != baseline.Count+6 || !reflect.DeepEqual(trace.Events[:baseline.Count], baseline.Events[:baseline.Count]) {
		t.Fatal("unused review altered earlier events or performed extra work")
	}
	var previews [3]int
	for i, event := range trace.Events[baseline.Count:trace.Count] {
		if event.Sequence != baseline.Count+uint16(i)+1 || event.Role != resourceacceptance.Controller || event.Action != resourceacceptance.Preview || event.RunID != "" || event.OperationID != "" {
			t.Fatal("unused review performed a mutation or changed event identity")
		}
		switch event.Kind {
		case resourceacceptance.ManagementClientInvoked:
			previews[0]++
		case resourceacceptance.ManagementFrameAttempted:
			previews[1]++
		case resourceacceptance.ManagementFrameWritten:
			previews[2]++
		default:
			t.Fatal("unused review admitted an operation")
		}
	}
	if previews != [3]int{2, 2, 2} {
		t.Fatal("unused review did not make exactly one preview per target")
	}
	for i := range f.cores {
		f.assertFiles(i, completed[i])
	}
	targetCores, targetOwners := [2]*Core{f.cores[1], f.cores[2]}, [2]*resourceInspectionNativeOwner{f.owners[1], f.owners[2]}
	targetNodes := [2]*directlan.Node{f.cores[1].nodeCopy().(*directLANBackend).Node, f.cores[2].nodeCopy().(*directLANBackend).Node}
	f.reopenControllerAfterGroup()
	f.observeRestartMaintenance(trace)
	if f.ctx.Err() != nil || time.Now().Unix() >= f.expires {
		t.Fatal("post-group reopen exceeded the original finite lifetime")
	}
	currentValue = f.local(0, resourcegroup.LocalCurrentReviewCommand, resourcegroup.CurrentReviewInput{SchemaVersion: 1})
	currentReview, ok = currentValue.(resourcegroup.CurrentReviewView)
	if !ok || currentReview.State != resourcegroup.CurrentReviewNone || currentReview.Prepared != nil {
		t.Fatal("reopen restored unused prepared admission")
	}
	status := f.local(0, resourcegroup.LocalStatusCommand, resourcegroup.StatusInput{SchemaVersion: 1, RunID: run.RunID})
	repeated := f.group(resourcegroup.LocalApplyCommand, apply)
	if !reflect.DeepEqual(status, run) || !reflect.DeepEqual(repeated, run) {
		t.Fatal("reopen lost original historical run or exact repeat semantics")
	}
	ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
	value, err = f.call(ctx, 0, resourcegroup.LocalApplyCommand, staleApply)
	cancel()
	if value != nil || networkErrorCode(err) != "resource_group_review_unavailable" {
		t.Fatal("pre-reopen unused review was not rejected as unavailable")
	}
	for i := range f.cores {
		f.assertFiles(i, completed[i])
		f.assertOrdinary(i)
	}
	for i, grant := range grants {
		f.assertGrant(i+1, grant)
		if f.cores[i+1] != targetCores[i] || f.owners[i+1] != targetOwners[i] || f.cores[i+1].nodeCopy().(*directLANBackend).Node != targetNodes[i] {
			t.Fatal("target owner changed during controller-only reopen")
		}
	}
	f.assertCatalog()
	if resourceacceptance.Snapshot() != trace || resourceacceptance.RolloverSnapshot().Invalid {
		t.Fatal("reopen, history, stale review or local catalog issued additional work")
	}
	t.Log("post-group joined Core.Close/Open retained history and rejected unused admission without replay across startup and ordinary maintenance; no OS-process acceptance")
}
