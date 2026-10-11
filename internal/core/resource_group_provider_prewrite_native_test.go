//go:build resource_group_catalog_native && resource_management_native && resource_inspection_native && directlan_activation_native

package core

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/operationjournal"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourceacceptance"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

// SOURCE ONLY until a separate exact build and native execution gate.
// The actual canonical provider refuses publication after real controller and
// target intents. This is not a terminal-journal failure, lost reply, canceled
// pre-send intent, post-rename uncertainty, interrupted restart or power loss.
const resourceGroupProviderPrewriteNativeSelector = "^TestResourceGroupNativeSecondTargetProviderPrewriteRefusal$"

func newResourceGroupProviderPrewriteNative(t *testing.T) *resourceGroupCatalogNative {
	t.Helper()
	// Independent guard: no fixture state, reservation, goroutine or Core yet.
	if os.Getenv("SOBALINK_RUN_RESOURCE_GROUP_PROVIDER_PREWRITE_NATIVE") != "reviewed-second-target-provider-prewrite-refusal-v1" {
		t.Skip("requires separately reviewed second-target provider prewrite refusal execution")
	}
	if os.Getenv("SOBALINK_RUN_ACTIVATION_NATIVE") != "1" || !directLANNetstackReady {
		t.Fatal("explicit native activation prerequisite is missing")
	}
	run := flag.Lookup("test.run")
	if run == nil || run.Value.String() != resourceGroupProviderPrewriteNativeSelector {
		t.Fatal("exact single native provider-prewrite selector is required")
	}
	deadline, ok := t.Deadline()
	if !ok || time.Until(deadline) > 4*time.Minute || time.Until(deadline) < 200*time.Second {
		t.Fatal("native provider prewrite refusal requires the reviewed four-minute test budget")
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "TS_PROXY"} {
		if os.Getenv(name) != "" {
			t.Fatal("native provider prewrite refusal requires an isolated proxy-free environment")
		}
	}
	return newResourceGroupCatalogNativeAfterGuard(t)
}

func TestResourceGroupNativeSecondTargetProviderPrewriteRefusal(t *testing.T) {
	f := newResourceGroupProviderPrewriteNative(t)
	var grants [2]resourcegrant.Record
	for i := range grants {
		f.prewriteAdmit(32 * time.Second)
		grants[i] = f.confirm(i + 1)
	}
	policies := [2]capacity.Policy{f.cores[1].capacityPolicy(), f.cores[2].capacityPolicy()}
	wanted := [2]resource.Settings{resourceManagementNativeSettings(3, 1), resourceManagementNativeSettings(4, 2)}
	selection := resourceGroupRestartSelection(t, f, grants, wanted)
	before := [3][3]resourceGroupCatalogFile{f.files(0), f.files(1), f.files(2)}
	// Read existing authority only. The fault must not become a pair/profile/grant
	// change, and its profile-wide write refusal must not be described as a
	// capacity-only injected writer.
	authorityFiles := func(i int) [3]resourceGroupCatalogFile {
		t.Helper()
		var files [3]resourceGroupCatalogFile
		for j, path := range []string{filepath.Join(f.cores[i].dir, "sobalink.json"), filepath.Join(f.cores[i].dir, "direct-lan.json"), resourceGrantStatePath(f.cores[i].dir)} {
			data, err := os.ReadFile(path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal("synthetic original authority could not be read")
			}
			files[j] = resourceGroupCatalogFile{present: err == nil, data: data}
		}
		return files
	}
	originalAuthority := [3][3]resourceGroupCatalogFile{authorityFiles(0), authorityFiles(1), authorityFiles(2)}
	for i, c := range f.cores {
		f.assertOrdinary(i)
		c.op.Lock()
		valid := c.atomicWrite == nil && !c.resourceFrozen && c.resourceState.validate() == nil && c.resourceState.scoped == nil && c.resourceState.HighWater != nil && *c.resourceState.HighWater == 0 && len(c.resourceState.Records) == 0
		c.op.Unlock()
		if !valid {
			t.Fatal("fresh owner lacks unmodified production writer or pristine operation history")
		}
	}
	f.prewriteAdmit(8 * time.Second)
	bBefore := f.descriptor(2)
	if reflect.DeepEqual(bBefore.Requested, wanted[1]) {
		t.Fatal("second-target requested values do not distinguish a failed publication")
	}
	b := f.cores[2]
	bRevision := b.lanStartWriteRevision.Load()
	bNonce := b.resourceNonce
	if b.lanStartUncertain.Load() {
		t.Fatal("second target already has uncertain canonical publication")
	}
	f.prewriteAdmit(30 * time.Second)
	prepared, apply := f.restartPrepared(selection, wanted, true)
	for i := range f.cores {
		f.assertFiles(i, before[i])
	}
	f.prewriteAdmit(8 * time.Second)
	currentValue := f.local(0, resourcegroup.LocalCurrentReviewCommand, resourcegroup.CurrentReviewInput{SchemaVersion: 1})
	current, ok := currentValue.(resourcegroup.CurrentReviewView)
	if !ok || current.State != resourcegroup.CurrentReviewCurrent || current.Prepared == nil || !reflect.DeepEqual(*current.Prepared, prepared) {
		t.Fatal("fresh exact prepared review was not retained before provider refusal")
	}
	previewTrace := resourceacceptance.Snapshot()
	if previewTrace.InvalidOrOverflow || previewTrace.Count != 6 {
		t.Fatal("fresh preview has unexpected resource effects")
	}
	controller := f.cores[0]
	controller.op.Lock()
	g := controller.resourceGroups
	ready := g != nil && g.prepared != nil && g.prepared.id == prepared.ReviewID && g.active == nil && g.overlay == nil && g.store != nil && g.store.firstUse && g.store.certified && !g.store.frozen && !g.store.uncertain && len(g.store.state.Runs) == 0
	var origins []resourceGroupOrigin
	if ready {
		origins = resourceGroupPersistedOrigins(g.prepared.origins)
	}
	controller.op.Unlock()
	if !ready {
		t.Fatal("fresh review lacks idle certified first-use coordinator ownership")
	}

	// Real setup has already written this profile's private root namespace.
	// Do not precreate one, acquire a writer to manufacture an error, or modify
	// the distinct resource-state/resource-grants/group evidence namespaces.
	var profileInfo os.FileInfo
	if err := f.owners[2].lock.WithOwnershipInfo(b.dir, func(profile, lock os.FileInfo) error {
		if profile == nil || lock == nil || b.resourceLock != f.owners[2].lock {
			return errors.New("synthetic target ownership is unavailable")
		}
		profileInfo = profile
		return nil
	}); err != nil {
		t.Fatal("second-target profile ownership was not certified")
	}
	atomicDirectory := filepath.Join(b.dir, ".sobalink-atomic-v1")
	atomicInfo, err := os.Lstat(atomicDirectory)
	if err != nil || !atomicInfo.IsDir() || atomicInfo.Mode()&os.ModeSymlink != 0 || atomicInfo.Mode().Perm() != 0700 {
		t.Fatal("existing private target-root atomic namespace unavailable")
	}
	entries, err := os.ReadDir(atomicDirectory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "owner.lock" {
		t.Fatal("second-target root inventory was not healthy before insertion")
	}
	ownerInfo, err := os.Lstat(filepath.Join(atomicDirectory, "owner.lock"))
	if err != nil || !ownerInfo.Mode().IsRegular() || ownerInfo.Mode().Perm() != 0600 {
		t.Fatal("second-target root inventory lacks its private owner marker")
	}
	blockerPath := filepath.Join(atomicDirectory, "provider-prewrite-refusal-fixture")
	blockerData := []byte("synthetic provider prewrite refusal\n")
	blocker, err := os.OpenFile(blockerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("exclusive synthetic provider refusal file creation failed")
	}
	written, writeErr := blocker.Write(blockerData)
	blockerInfo, statErr := blocker.Stat()
	closeErr := blocker.Close()
	if writeErr != nil || written != len(blockerData) || statErr != nil || closeErr != nil || !blockerInfo.Mode().IsRegular() || blockerInfo.Mode().Perm() != 0600 || blockerInfo.Size() != int64(len(blockerData)) {
		t.Fatal("synthetic provider refusal file was not completed exactly")
	}
	assertBlocker := func() {
		t.Helper()
		profile, profileErr := os.Lstat(b.dir)
		directory, directoryErr := os.Lstat(atomicDirectory)
		info, infoErr := os.Lstat(blockerPath)
		data, readErr := os.ReadFile(blockerPath)
		owner, ownerErr := os.Lstat(filepath.Join(atomicDirectory, "owner.lock"))
		inventory, inventoryErr := os.ReadDir(atomicDirectory)
		if profileErr != nil || !profile.IsDir() || profile.Mode()&os.ModeSymlink != 0 || !os.SameFile(profile, profileInfo) || directoryErr != nil || !directory.IsDir() || directory.Mode().Perm() != 0700 || !os.SameFile(directory, atomicInfo) || infoErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !os.SameFile(info, blockerInfo) || info.Size() != int64(len(blockerData)) || readErr != nil || !bytes.Equal(data, blockerData) || ownerErr != nil || !owner.Mode().IsRegular() || !os.SameFile(owner, ownerInfo) || inventoryErr != nil || len(inventory) != 2 || inventory[0].Name() != "owner.lock" || inventory[1].Name() != "provider-prewrite-refusal-fixture" {
			t.Fatal("synthetic provider refusal inventory or bound profile changed")
		}
	}
	assertBlocker()
	for i := range f.cores {
		f.assertFiles(i, before[i])
		f.assertOrdinary(i)
		if !reflect.DeepEqual(authorityFiles(i), originalAuthority[i]) {
			t.Fatal("blocker insertion changed original authority")
		}
	}
	if resourceacceptance.Snapshot() != previewTrace || b.lanStartWriteRevision.Load() != bRevision || b.lanStartUncertain.Load() {
		t.Fatal("blocker insertion performed provider work or changed canonical authority")
	}

	// Both per-peer group intents and each real target journal intent precede
	// ProviderAdmitted in the unchanged production path. B's profile-root lease
	// then rejects this inventory; its separate journal can save the true failure.
	// No independent transient target-write event or crash durability is claimed.
	f.prewriteAdmit(30 * time.Second)
	value := f.group(resourcegroup.LocalApplyCommand, apply)
	run, ok := value.(resourcegroup.RunView)
	decoded, err := resourcegroup.DecodeRunView(resourceGroupCatalogReplyJSON(t, value))
	wantSummary := resourcegroup.Summary{SchemaVersion: 1, Selected: 2, Executable: 2, DispatchObserved: 2, Applied: 1, Failed: 1, AdmissionFinished: true}
	if !ok || err != nil || !reflect.DeepEqual(decoded, run) || run.RunID != prepared.ReviewID || !reflect.DeepEqual(run.Review, prepared.Review) || run.Activity != resourcegroup.ActivityIdle || run.LocalDurability != resourcegroup.LocalDurable || run.Summary != wantSummary || len(run.Evidence.Members) != 2 {
		t.Fatal("actual group did not retain exact mixed durable terminal evidence")
	}
	outcomes := [2]resource.Outcome{
		{Status: "applied", Configuration: "durable", Accounting: "succeeded", Transfer: "succeeded"},
		{Status: "failed", Configuration: "not_published", Accounting: "not_attempted", Transfer: "not_attempted"},
	}
	expectedRows, err := resourcegroup.NewEvidence(prepared.Review)
	if err != nil || len(expectedRows) != 2 {
		t.Fatal("actual review cannot produce complete original request evidence")
	}
	for i := range expectedRows {
		row := &expectedRows[i]
		durable := true
		row.Dispatch, row.LocalDurability = resourcegroup.DispatchObserved, resourcegroup.LocalDurable
		row.Target = &resourcegrant.ManagementOperation{OperationID: row.Request.Apply.OperationID, Outcome: outcomes[i], EvidenceDurable: &durable}
	}
	if !reflect.DeepEqual(run.Evidence, resourcegroup.EvidenceBody{SchemaVersion: 1, Members: expectedRows}) {
		t.Fatal("provider refusal changed original requests, stages or independent durability")
	}
	f.prewriteAdmit(8 * time.Second)
	f.assertApplied(1, wanted[0], policies[0], run.Evidence.Members[0])
	f.prewriteAdmit(8 * time.Second)
	bAfter := f.descriptor(2)
	if bAfter.Target != bBefore.Target || !reflect.DeepEqual(bAfter.Requested, bBefore.Requested) || bAfter.Effective != bBefore.Effective || bAfter.Revision == bBefore.Revision || !capacityJSONEqual(b.capacityPolicy(), policies[1]) || b.lanStartWriteRevision.Load() != bRevision+1 || b.lanStartUncertain.Load() {
		t.Fatal("second-target refusal changed settings or lost its single authority-write attempt")
	}
	completed := [3][3]resourceGroupCatalogFile{f.files(0), f.files(1), f.files(2)}
	if !reflect.DeepEqual(completed[0][0], before[0][0]) || !reflect.DeepEqual(completed[0][1], before[0][1]) || !completed[0][2].present || !reflect.DeepEqual(completed[2][0], before[2][0]) || !reflect.DeepEqual(completed[1][2], before[1][2]) || !reflect.DeepEqual(completed[2][2], before[2][2]) {
		t.Fatal("canonical refusal changed unrelated settings or target group history")
	}
	for i, grant := range grants {
		journal, readErr := readResourceEnvelope(resourceStatePath(f.cores[i+1].dir))
		if readErr != nil || journal.scoped == nil || *journal.HighWater != 1 || len(journal.scoped.Records) != 1 {
			t.Fatal("actual target journal lacks exactly one scoped operation")
		}
		tagged := journal.scoped.Records[0]
		record := tagged.Remote
		wantScope := operationjournal.RemoteOperationScope{Target: grant.Target, Relationship: grant.Relationship, GrantID: grant.ID, ScopeVersion: operationjournal.ScopeVersion}
		if tagged.Kind != operationjournal.RemoteKind || tagged.Local != nil || record == nil || record.Sequence != 1 || record.Phase != "result" || record.Scope != wantScope || record.AuthorizingRevision != grant.Revision || record.IssuanceNonce != f.cores[i+1].resourceNonce || !reflect.DeepEqual(record.Request, *run.Evidence.Members[i].Request.Apply) || record.Outcome != outcomes[i] || tagged.Pinned() {
			t.Fatal("actual terminal journal differs from original scope, request or provider outcome")
		}
		c := f.cores[i+1]
		c.op.Lock()
		valid := c.atomicWrite == nil && !c.resourceFrozen && !c.resourceRemoteEvidenceDenied && c.resourceUncertainWrite == nil && reflect.DeepEqual(c.resourceState, journal)
		c.op.Unlock()
		if !valid {
			t.Fatal("known provider refusal became uncertain target evidence or substituted persistence")
		}
	}
	if b.resourceNonce != bNonce {
		t.Fatal("provider refusal replaced the original target owner")
	}
	saved, err := decodeResourceGroupEnvelope(completed[0][2].data)
	if err != nil || len(saved.Runs) != 1 || *saved.HighWater != 1 {
		t.Fatal("controller journal lacks the single real accepted run")
	}
	savedRun := saved.Runs[0]
	if savedRun.RunID != run.RunID || savedRun.AcceptedAt != run.AcceptedAt || savedRun.AcceptedSequence != 1 || savedRun.UpdateSequence != 6 || savedRun.Admission != resourceGroupAdmissionFinished || savedRun.InputHash != resourceGroupInputHash(apply) || !reflect.DeepEqual(savedRun.Review, prepared.Review) || !reflect.DeepEqual(savedRun.Origins, origins) || !reflect.DeepEqual(savedRun.Evidence, run.Evidence) || savedRun.pinned() {
		t.Fatal("controller journal lost exact completed mixed history")
	}
	trace := f.assertEvents(run)
	if !reflect.DeepEqual(trace.Events[:previewTrace.Count], previewTrace.Events[:previewTrace.Count]) {
		t.Fatal("post-intent provider refusal rewrote the real preview trace")
	}
	stateFile, err := os.Lstat(resourceGroupStatePath(controller.dir))
	if err != nil || !stateFile.Mode().IsRegular() {
		t.Fatal("controller terminal snapshot identity unavailable")
	}
	assertUnchanged := func() {
		t.Helper()
		assertBlocker()
		for i, c := range f.cores {
			f.assertFiles(i, completed[i])
			f.assertOrdinary(i)
			if !reflect.DeepEqual(authorityFiles(i), originalAuthority[i]) {
				t.Fatal("provider refusal or historical access changed original authority")
			}
			c.op.Lock()
			productionWriter := c.atomicWrite == nil
			c.op.Unlock()
			if !productionWriter {
				t.Fatal("historical access replaced a production writer")
			}
		}
		for i, grant := range grants {
			f.assertGrant(i+1, grant)
			f.assertPair(i + 1)
		}
		controller.op.Lock()
		store := g.store
		valid := controller.resourceGroups == g && g.active == nil && g.prepared == nil && g.overlay == nil && store != nil && store.certified && !store.firstUse && !store.frozen && !store.uncertain && store.before == nil && store.attempted == nil && os.SameFile(store.file, stateFile) && store.digest == sha256.Sum256(completed[0][2].data) && store.size == len(completed[0][2].data) && reflect.DeepEqual(store.state, saved) && store.current(controller) == nil
		controller.op.Unlock()
		if !valid || resourceacceptance.Snapshot() != trace || b.lanStartWriteRevision.Load() != bRevision+1 || b.lanStartUncertain.Load() || !capacityJSONEqual(b.capacityPolicy(), policies[1]) || f.ctx.Err() != nil || time.Now().Unix() >= f.expires {
			t.Fatal("historical access lost certified mixed results, replayed work or exceeded lifetime")
		}
	}
	assertUnchanged()
	f.prewriteAdmit(8 * time.Second)
	if status := f.local(0, resourcegroup.LocalStatusCommand, resourcegroup.StatusInput{SchemaVersion: 1, RunID: run.RunID}); !reflect.DeepEqual(status, run) {
		t.Fatal("local status changed the completed provider-failure history")
	}
	assertUnchanged()
	f.prewriteAdmit(30 * time.Second)
	if repeated := f.group(resourcegroup.LocalApplyCommand, apply); !reflect.DeepEqual(repeated, run) {
		t.Fatal("exact provider-failure repeat did not remain historical")
	}
	assertUnchanged()
	f.prewriteAdmit(8 * time.Second)
	currentValue = f.local(0, resourcegroup.LocalCurrentReviewCommand, resourcegroup.CurrentReviewInput{SchemaVersion: 1})
	current, ok = currentValue.(resourcegroup.CurrentReviewView)
	if !ok || current.State != resourcegroup.CurrentReviewNone || current.Prepared != nil {
		t.Fatal("completed mixed run restored unused admission")
	}
	assertUnchanged()
	// Keep the blocker throughout every live owner. Unchanged fixture cleanup
	// joins all owners/reservations before whole-root removal on overall success;
	// failure retains protected evidence and does not repair this namespace.
	t.Log("actual second-target canonical publication refused after real intents and provider admission; first-target success and both durable terminal results retained without replay; no uncertainty, interrupted-restart or power-loss claim")
}
