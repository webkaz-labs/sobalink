//go:build resource_group_catalog_native && resource_management_native && resource_inspection_native && directlan_activation_native

package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourceacceptance"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

// SOURCE ONLY until a separate exact build and native execution gate.
// The second actual acceptance is refused before durable acceptance or intent.
// This does not cover cancellation after intent, post-rename uncertainty,
// target result-save failure, first-ever acceptance failure or OS-process restart.
const resourceGroupPrewriteNativeSelector = "^TestResourceGroupNativeAcceptedPrewriteRefusal$"

func newResourceGroupPrewriteNative(t *testing.T) *resourceGroupCatalogNative {
	t.Helper()
	// Independent guard: no temp state, reservation, goroutine or Core yet.
	if os.Getenv("SOBALINK_RUN_RESOURCE_GROUP_PREWRITE_NATIVE") != "reviewed-accepted-prewrite-refusal-v1" {
		t.Skip("requires separately reviewed accepted-run prewrite refusal execution")
	}
	if os.Getenv("SOBALINK_RUN_ACTIVATION_NATIVE") != "1" || !directLANNetstackReady {
		t.Fatal("explicit native activation prerequisite is missing")
	}
	run := flag.Lookup("test.run")
	if run == nil || run.Value.String() != resourceGroupPrewriteNativeSelector {
		t.Fatal("exact single native prewrite-refusal selector is required")
	}
	deadline, ok := t.Deadline()
	if !ok || time.Until(deadline) > 4*time.Minute || time.Until(deadline) < 200*time.Second {
		t.Fatal("native prewrite refusal requires the reviewed four-minute test budget")
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "TS_PROXY"} {
		if os.Getenv(name) != "" {
			t.Fatal("native prewrite refusal requires an isolated proxy-free environment")
		}
	}
	return newResourceGroupCatalogNativeAfterGuard(t)
}

func (f *resourceGroupCatalogNative) prewriteAdmit(watchdog time.Duration) {
	f.t.Helper()
	cutoff, ok := f.ctx.Deadline()
	if !ok || f.ctx.Err() != nil || watchdog <= 0 || !time.Now().Add(watchdog).Before(cutoff) || time.Now().Unix() >= f.expires {
		f.t.Fatal("full unchanged call watchdog does not fit the original fixture lifetime")
	}
}

func TestResourceGroupNativeAcceptedPrewriteRefusal(t *testing.T) {
	f := newResourceGroupPrewriteNative(t)
	var grants [2]resourcegrant.Record
	for i := range grants {
		// The unchanged helper has descriptor, preview, confirmation and one
		// shared readiness interval, each with its original eight-second cap.
		f.prewriteAdmit(32 * time.Second)
		grants[i] = f.confirm(i + 1)
	}
	policies := [2]capacity.Policy{f.cores[1].capacityPolicy(), f.cores[2].capacityPolicy()}
	wanted := [2]resource.Settings{resourceManagementNativeSettings(3, 1), resourceManagementNativeSettings(4, 2)}
	selection := resourceGroupRestartSelection(t, f, grants, wanted)
	before := [3][3]resourceGroupCatalogFile{f.files(0), f.files(1), f.files(2)}
	f.prewriteAdmit(30 * time.Second)
	prepared, apply := f.restartPrepared(selection, wanted, true)
	for i := range f.cores {
		f.assertFiles(i, before[i])
	}
	f.prewriteAdmit(30 * time.Second)
	value := f.group(resourcegroup.LocalApplyCommand, apply)
	run, ok := value.(resourcegroup.RunView)
	decoded, err := resourcegroup.DecodeRunView(resourceGroupCatalogReplyJSON(t, value))
	if !ok || err != nil || !reflect.DeepEqual(decoded, run) || run.RunID != prepared.ReviewID || !reflect.DeepEqual(run.Review, prepared.Review) || run.Activity != resourcegroup.ActivityIdle || run.LocalDurability != resourcegroup.LocalDurable || !run.Summary.AllApplied || run.Summary.Applied != 2 || run.Summary.Executable != 2 || len(run.Evidence.Members) != 2 {
		t.Fatal("real completed two-target baseline lacks exact durable evidence")
	}
	for i, row := range run.Evidence.Members {
		f.prewriteAdmit(8 * time.Second)
		f.assertApplied(i+1, wanted[i], policies[i], row)
		f.assertGrant(i+1, grants[i])
	}
	completed := [3][3]resourceGroupCatalogFile{f.files(0), f.files(1), f.files(2)}
	if !reflect.DeepEqual(completed[0][0], before[0][0]) || !reflect.DeepEqual(completed[0][1], before[0][1]) || !completed[0][2].present {
		t.Fatal("controller settings changed or completed baseline history is absent")
	}
	saved, err := decodeResourceGroupEnvelope(completed[0][2].data)
	if err != nil || len(saved.Runs) != 1 || *saved.HighWater != 1 || saved.Runs[0].RunID != run.RunID || saved.Runs[0].Admission != resourceGroupAdmissionFinished || !reflect.DeepEqual(saved.Runs[0].Review, run.Review) || !reflect.DeepEqual(saved.Runs[0].Evidence, run.Evidence) {
		t.Fatal("completed controller journal does not match the actual baseline")
	}
	baseline := f.assertEvents(run)
	newWanted := [2]resource.Settings{resourceManagementNativeSettings(5, 2), resourceManagementNativeSettings(6, 3)}
	newSelection := resourceGroupRestartSelection(t, f, grants, newWanted)
	f.prewriteAdmit(30 * time.Second)
	fresh, newApply := f.restartPrepared(newSelection, newWanted, false)
	if fresh.ReviewID == run.RunID {
		t.Fatal("fresh prepared review reused the completed run identity")
	}
	f.prewriteAdmit(8 * time.Second)
	currentValue := f.local(0, resourcegroup.LocalCurrentReviewCommand, resourcegroup.CurrentReviewInput{SchemaVersion: 1})
	current, ok := currentValue.(resourcegroup.CurrentReviewView)
	if !ok || current.State != resourcegroup.CurrentReviewCurrent || current.Prepared == nil || !reflect.DeepEqual(*current.Prepared, fresh) {
		t.Fatal("fresh actual prepared review was not retained before refusal")
	}
	trace := resourceacceptance.Snapshot()
	if trace.InvalidOrOverflow || trace.Count != baseline.Count+6 || !reflect.DeepEqual(trace.Events[:baseline.Count], baseline.Events[:baseline.Count]) {
		t.Fatal("fresh review altered baseline events or performed extra work")
	}
	var previews [3]int
	for i, event := range trace.Events[baseline.Count:trace.Count] {
		if event.Sequence != baseline.Count+uint16(i)+1 || event.Role != resourceacceptance.Controller || event.Action != resourceacceptance.Preview || event.RunID != "" || event.OperationID != "" {
			t.Fatal("fresh review performed a mutation or changed event identity")
		}
		switch event.Kind {
		case resourceacceptance.ManagementClientInvoked:
			previews[0]++
		case resourceacceptance.ManagementFrameAttempted:
			previews[1]++
		case resourceacceptance.ManagementFrameWritten:
			previews[2]++
		default:
			t.Fatal("fresh review admitted an operation")
		}
	}
	if previews != [3]int{2, 2, 2} {
		t.Fatal("fresh review did not make exactly one preview per target")
	}
	for i := range f.cores {
		f.assertFiles(i, completed[i])
	}
	var grantBytes [2][]byte
	for i := range grants {
		grantBytes[i], err = os.ReadFile(resourceGrantStatePath(f.cores[i+1].dir))
		if err != nil {
			t.Fatal("original grant bytes unavailable before refusal")
		}
	}
	controller := f.cores[0]
	statePath := resourceGroupStatePath(controller.dir)
	confirmedFile, err := os.Lstat(statePath)
	if err != nil || !confirmedFile.Mode().IsRegular() {
		t.Fatal("completed controller inode unavailable before refusal")
	}
	controller.op.Lock()
	g := controller.resourceGroups
	if g == nil || g.store == nil || g.prepared == nil || g.prepared.id != fresh.ReviewID || g.active != nil || g.overlay != nil {
		controller.op.Unlock()
		t.Fatal("fresh review has no exact idle owned coordinator")
	}
	store := g.store
	origins := resourceGroupPersistedOrigins(g.prepared.origins)
	validStore := store.certified && !store.firstUse && !store.frozen && !store.uncertain && store.before == nil && store.attempted == nil && os.SameFile(confirmedFile, store.file) && store.digest == sha256.Sum256(completed[0][2].data) && store.size == len(completed[0][2].data) && reflect.DeepEqual(store.state, saved)
	confirmedDirectory := store.directory
	controller.op.Unlock()
	if !validStore {
		t.Fatal("baseline store is not the exact certified durable snapshot")
	}

	// Add exactly one new private regular file to the already-owned atomic
	// namespace. No writer hook, repaired inventory or synthesized authority is
	// involved. The blocker survives every assertion and joined cleanup owns it.
	groupDirectory, err := os.Lstat(filepath.Dir(statePath))
	if err != nil || !groupDirectory.IsDir() || !os.SameFile(groupDirectory, confirmedDirectory) {
		t.Fatal("completed controller group directory changed before refusal")
	}
	atomicDirectory := filepath.Join(filepath.Dir(statePath), ".sobalink-atomic-v1")
	atomicInfo, err := os.Lstat(atomicDirectory)
	if err != nil || !atomicInfo.IsDir() || atomicInfo.Mode()&os.ModeSymlink != 0 || atomicInfo.Mode().Perm() != 0700 {
		t.Fatal("existing private atomic namespace unavailable before refusal")
	}
	blockerPath := filepath.Join(atomicDirectory, "acceptance-refusal-fixture")
	blockerData := []byte("synthetic acceptance refusal\n")
	blocker, err := os.OpenFile(blockerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("exclusive synthetic refusal file creation failed")
	}
	written, writeErr := blocker.Write(blockerData)
	blockerInfo, statErr := blocker.Stat()
	closeErr := blocker.Close()
	if writeErr != nil || written != len(blockerData) || statErr != nil || closeErr != nil || !blockerInfo.Mode().IsRegular() || blockerInfo.Mode().Perm() != 0600 || blockerInfo.Size() != int64(len(blockerData)) {
		t.Fatal("exclusive synthetic refusal file was not completed exactly")
	}
	assertBlocker := func() {
		t.Helper()
		directory, directoryErr := os.Lstat(atomicDirectory)
		info, infoErr := os.Lstat(blockerPath)
		data, readErr := os.ReadFile(blockerPath)
		if directoryErr != nil || !directory.IsDir() || !os.SameFile(directory, atomicInfo) || infoErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !os.SameFile(info, blockerInfo) || info.Size() != int64(len(blockerData)) || readErr != nil || !bytes.Equal(data, blockerData) {
			t.Fatal("synthetic refusal file or its owned namespace changed")
		}
	}
	assertBlocker()
	f.prewriteAdmit(30 * time.Second)
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	attemptedAt := time.Now().Unix()
	value, err = f.call(ctx, 0, resourcegroup.LocalApplyCommand, newApply)
	cancel()
	if value != nil || networkErrorCode(err) != "resource_group_storage_unavailable" {
		t.Fatal("actual prewrite acceptance refusal did not return exact storage_unavailable")
	}
	if resourceacceptance.Snapshot() != trace {
		t.Fatal("refused acceptance emitted an admission, intent or remote work event")
	}
	controller.op.Lock()
	joined := controller.resourceGroups == g && g.prepared == nil && g.active == nil && g.overlay != nil
	var refused resourceGroupRecord
	if joined {
		refused, err = cloneResourceGroupRecord(*g.overlay)
	}
	controller.op.Unlock()
	if !joined || err != nil || refused.RunID != fresh.ReviewID || refused.AcceptedSequence != 2 || refused.UpdateSequence != 1 || refused.AcceptedAt < attemptedAt || refused.AcceptedAt > time.Now().Unix() || refused.Admission != resourceGroupAdmissionFinished || refused.InputHash != resourceGroupInputHash(newApply) || !reflect.DeepEqual(refused.Origins, origins) || !reflect.DeepEqual(refused.Review, fresh.Review) {
		t.Fatal("refused acceptance did not consume admission and join with exact historical overlay")
	}
	// AcceptedAt/AcceptedSequence above belong only to this unsaved attempt.
	// The confirmed envelope remains at sequence one with no second run.
	expectedRows, err := resourcegroup.NewEvidence(fresh.Review)
	if err != nil || len(expectedRows) != 2 {
		t.Fatal("fresh review cannot produce exact original request evidence")
	}
	for i := range expectedRows {
		expectedRows[i].AdmissionStop = resourcegroup.StopPersistenceUncertain
		row := expectedRows[i]
		if row.Execution != resourcegroup.ExecutionSelected || row.Dispatch != resourcegroup.DispatchNotAttempted || row.LocalDurability != resourcegroup.LocalNotSaved || row.Target != nil || row.Status != (resourcegroup.StatusObservation{State: resourcegroup.StatusNotQueried}) || row.Request == nil || row.Request.Apply == nil || row.Request.ManagementSelector != newSelection.Members[i].Selector || row.Request.Apply.OperationID == run.Evidence.Members[i].Request.Apply.OperationID || !reflect.DeepEqual(row.Request.Apply.Settings, newWanted[i]) {
			t.Fatal("refusal oracle does not retain the fresh unattempted request identities")
		}
	}
	expectedEvidence := resourcegroup.EvidenceBody{SchemaVersion: resourcegroup.SchemaVersion, Members: expectedRows}
	if !reflect.DeepEqual(refused.Evidence, expectedEvidence) {
		t.Fatal("failed overlay fabricated dispatch, target outcome or local durability")
	}
	assertUnchanged := func() {
		t.Helper()
		assertBlocker()
		for i := range f.cores {
			f.assertFiles(i, completed[i])
			f.assertOrdinary(i)
		}
		for i, grant := range grants {
			f.assertGrant(i+1, grant)
			data, readErr := os.ReadFile(resourceGrantStatePath(f.cores[i+1].dir))
			if readErr != nil || !bytes.Equal(data, grantBytes[i]) {
				t.Fatal("refusal changed original grant bytes or finite authority")
			}
		}
		file, fileErr := os.Lstat(statePath)
		if fileErr != nil || !file.Mode().IsRegular() || !os.SameFile(file, confirmedFile) {
			t.Fatal("refusal replaced the original confirmed controller inode")
		}
		controller.op.Lock()
		valid := controller.resourceGroups == g && g.store == store && g.prepared == nil && g.active == nil && g.overlay != nil && reflect.DeepEqual(*g.overlay, refused) && store.certified && !store.firstUse && store.frozen && !store.uncertain && store.attempted == nil && store.before != nil && os.SameFile(store.before.file, confirmedFile) && store.before.digest == sha256.Sum256(completed[0][2].data) && store.before.size == len(completed[0][2].data) && os.SameFile(store.file, confirmedFile) && os.SameFile(store.directory, confirmedDirectory) && store.digest == sha256.Sum256(completed[0][2].data) && store.size == len(completed[0][2].data) && reflect.DeepEqual(store.state, saved)
		controller.op.Unlock()
		if !valid || resourceacceptance.Snapshot() != trace || f.ctx.Err() != nil || time.Now().Unix() >= f.expires {
			t.Fatal("refusal or historical access changed confirmed state, resumed work or exceeded lifetime")
		}
	}
	assertUnchanged()

	// Current-review is an admission-context read and must reject the frozen
	// store. The nil prepared pointer above separately proves consumption.
	f.prewriteAdmit(8 * time.Second)
	ctx, cancel = context.WithTimeout(f.ctx, 8*time.Second)
	value, err = f.call(ctx, 0, resourcegroup.LocalCurrentReviewCommand, resourcegroup.CurrentReviewInput{SchemaVersion: 1})
	cancel()
	if value != nil || networkErrorCode(err) != "resource_group_storage_unavailable" {
		t.Fatal("frozen admission context was exposed as a reusable current review")
	}
	assertUnchanged()
	f.prewriteAdmit(8 * time.Second)
	status := f.local(0, resourcegroup.LocalStatusCommand, resourcegroup.StatusInput{SchemaVersion: 1, RunID: fresh.ReviewID})
	failedView, ok := status.(resourcegroup.RunView)
	decoded, err = resourcegroup.DecodeRunView(resourceGroupCatalogReplyJSON(t, status))
	wantSummary := resourcegroup.Summary{SchemaVersion: resourcegroup.SchemaVersion, Selected: 2, Executable: 2, NotAttempted: 2, TargetUnobserved: 2, LocalNonDurable: 2, AdmissionFinished: true}
	if !ok || err != nil || !reflect.DeepEqual(decoded, failedView) || failedView.RunID != fresh.ReviewID || failedView.AcceptedAt != refused.AcceptedAt || failedView.Activity != resourcegroup.ActivityIdle || failedView.LocalDurability != resourcegroup.LocalNotSaved || !reflect.DeepEqual(failedView.Review, fresh.Review) || !reflect.DeepEqual(failedView.Evidence, expectedEvidence) || failedView.Summary != wantSummary {
		t.Fatal("failed-run status did not disclose exact idle unsaved historical evidence")
	}
	assertUnchanged()
	f.prewriteAdmit(30 * time.Second)
	if repeated := f.group(resourcegroup.LocalApplyCommand, newApply); !reflect.DeepEqual(repeated, failedView) {
		t.Fatal("exact refused-run repeat did not remain historical")
	}
	assertUnchanged()
	f.prewriteAdmit(8 * time.Second)
	if status := f.local(0, resourcegroup.LocalStatusCommand, resourcegroup.StatusInput{SchemaVersion: 1, RunID: run.RunID}); !reflect.DeepEqual(status, run) {
		t.Fatal("refusal changed original completed status")
	}
	assertUnchanged()
	f.prewriteAdmit(30 * time.Second)
	if repeated := f.group(resourcegroup.LocalApplyCommand, apply); !reflect.DeepEqual(repeated, run) {
		t.Fatal("original completed apply repeat did not remain historical")
	}
	assertUnchanged()
	for i, row := range run.Evidence.Members {
		f.prewriteAdmit(8 * time.Second)
		f.assertApplied(i+1, wanted[i], policies[i], row)
	}
	assertUnchanged()
	t.Log("actual acceptance prewrite refusal retained certified baseline and idle unsaved history without new dispatch; no post-intent, post-rename, target-save or OS-process claim")
}
