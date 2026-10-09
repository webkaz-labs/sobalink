package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// These fixtures intentionally use production Core.Open, a real transfer
// manager and owned temporary persistence. They reuse the offline lifecycle
// helper: network:none, SkipNetworkStart, rejecting NodeFactory, and checked
// Core.Close. They never start IPC, Web, peer listeners or a child process.
func resourceOperationLifecycleOwner(t *testing.T) (string, *config.Lock) {
	t.Helper()
	dir := t.TempDir()
	owner, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Registered before any Core cleanup, so all maintenance owners are joined
	// before this actual lifecycle lock is released.
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	return dir, owner
}
func assertResourceOperationOffline(t *testing.T, c *Core) {
	t.Helper()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.profile.Settings.Network != "none" || c.node != nil || c.web != nil || c.peerServer != nil || c.contextControl != nil || c.transfers == nil {
		t.Fatal("fixture unexpectedly activated a network owner or omitted the real manager")
	}
}
func resourceOperationLifecycleReview(t *testing.T, c *Core, files, perPeer int64) resource.ApplyRequest {
	t.Helper()
	in := resource.PreviewRequest{Target: resourceTarget(c), Settings: resource.Settings{TransferConcurrentFiles: capacity.Limited(files), TransferConcurrentPerPeer: capacity.Limited(perPeer)}}
	preview := resourceCall(t, c, "resource.preview", in).(resource.Preview)
	return resource.ApplyRequest{Target: preview.Target, OperationID: preview.OperationID, BaseRevision: preview.BaseRevision, Revision: preview.Revision, Settings: preview.Requested}
}
func resourceOperationFileEvidence(t *testing.T, dir string) [2]string {
	t.Helper()
	var result [2]string
	for i, path := range []string{resourceStatePath(dir), filepath.Join(dir, capacityPolicyFile)} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result[i] = resourceDigest(string(data))
	}
	return result
}
func resourceOperationLegacyApply(t *testing.T, c *Core, files, perPeer int64) {
	t.Helper()
	proposed := resourceProjection(c.capacityPolicy(), resource.Settings{TransferConcurrentFiles: capacity.Limited(files), TransferConcurrentPerPeer: capacity.Limited(perPeer)})
	previewRaw, _ := json.Marshal(map[string]any{"policy": proposed})
	preview, err := c.Command(context.Background(), webui.Command{RequestID: "lifecycle-legacy-preview", Name: "policy.preview", Payload: previewRaw})
	if err != nil {
		t.Fatal(err)
	}
	applyRaw, _ := json.Marshal(map[string]any{"policy": proposed, "expectedRevision": preview.(map[string]any)["revision"]})
	if _, err := c.Command(context.Background(), webui.Command{RequestID: "lifecycle-legacy-apply", Name: "policy.apply", Payload: applyRaw}); err != nil {
		t.Fatal(err)
	}
}

func TestResourceOperationOwnedOpenApplyStatusReopen(t *testing.T) {
	dir, owner := resourceOperationLifecycleOwner(t)
	first, closeFirst := openResourceApplication(t, dir, owner)
	assertResourceOperationOffline(t, first)
	in := resourceOperationLifecycleReview(t, first, 3, 2)
	applied := resourceCall(t, first, "resource.apply", in).(resource.Operation)
	if applied.Outcome != (resource.Outcome{Status: "applied", Configuration: "durable", Accounting: "succeeded", Transfer: "succeeded"}) || !applied.EvidenceDurable {
		t.Fatal("real manager stages or terminal durability were not acknowledged", applied)
	}
	status := resourceCall(t, first, "resource.operation.status", resource.StatusRequest{Target: in.Target, OperationID: in.OperationID}).(resource.Operation)
	if status.Outcome != applied.Outcome || status.Current.Effective != (resource.Effective{TransferConcurrentFiles: 3, TransferConcurrentPerPeer: 2}) {
		t.Fatal(status)
	}
	closeFirst()
	if err := owner.WithOwnership(dir, func() error { return nil }); err != nil {
		t.Fatal("Core released the caller's ownership", err)
	}
	saved, err := readCapacityPolicy(dir)
	if err != nil || resourceDigest(resourceSettings(saved)) != resourceDigest(in.Settings) {
		t.Fatal("saved choices differ", saved, err)
	}
	second, closeSecond := openResourceApplication(t, dir, owner)
	assertResourceOperationOffline(t, second)
	reopened := resourceCall(t, second, "resource.operation.status", resource.StatusRequest{Target: in.Target, OperationID: in.OperationID}).(resource.Operation)
	if reopened.Outcome != applied.Outcome || !reopened.EvidenceDurable || reopened.Current.ResourceID != in.ResourceID || reopened.Current.Effective != status.Current.Effective || reopened.Current.Revision == status.Current.Revision {
		t.Fatal("reopen lost historical/current distinction", reopened)
	}
	state, err := readResourceEnvelope(resourceStatePath(dir))
	if err != nil || *state.HighWater != 1 || len(state.Records) != 1 || state.Records[0].Phase != "result" {
		t.Fatal(state, err)
	}
	closeSecond()
}

func TestResourceOperationOwnedOpenReplayAfterLegacyChange(t *testing.T) {
	dir, owner := resourceOperationLifecycleOwner(t)
	first, closeFirst := openResourceApplication(t, dir, owner)
	in := resourceOperationLifecycleReview(t, first, 3, 2)
	applied := resourceCall(t, first, "resource.apply", in).(resource.Operation)
	resourceOperationLegacyApply(t, first, 7, 4)
	closeFirst()
	second, closeSecond := openResourceApplication(t, dir, owner)
	assertResourceOperationOffline(t, second)
	before := resourceOperationFileEvidence(t, dir)
	authorityBefore := second.lanStartWriteRevision.Load()
	replay := resourceCall(t, second, "resource.apply", in).(resource.Operation)
	status := resourceCall(t, second, "resource.operation.status", resource.StatusRequest{Target: in.Target, OperationID: in.OperationID}).(resource.Operation)
	if replay.Outcome != applied.Outcome || !replay.EvidenceDurable || replay.Current.Effective != (resource.Effective{TransferConcurrentFiles: 7, TransferConcurrentPerPeer: 4}) || status.Outcome != replay.Outcome || status.Current.Effective != replay.Current.Effective {
		t.Fatal("historical replay replaced current policy", replay, status)
	}
	if resourceOperationFileEvidence(t, dir) != before || second.lanStartWriteRevision.Load() != authorityBefore {
		t.Fatal("retained operation performed another publication")
	}
	if len(second.requests) != 0 || *second.resourceState.HighWater != 1 {
		t.Fatal("replay was cached or consumed another operation")
	}
	closeSecond()
}

func TestResourceOperationOwnedOpenRecoversIntentWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		name           string
		files, perPeer int64
	}{{"matching", 3, 2}, {"newer", 7, 4}} {
		t.Run(tc.name, func(t *testing.T) {
			dir, owner := resourceOperationLifecycleOwner(t)
			first, closeFirst := openResourceApplication(t, dir, owner)
			in := resourceOperationLifecycleReview(t, first, 3, 2)
			observed := resourceProjection(first.capacityPolicy(), resource.Settings{TransferConcurrentFiles: capacity.Limited(tc.files), TransferConcurrentPerPeer: capacity.Limited(tc.perPeer)})
			closeFirst()
			// Seed a validated interrupted-intent fixture only after joining Core.
			// This exercises real startup recovery, not process-kill/power-loss proof.
			if err := owner.WithOwnershipInfo(dir, func(directory, lock os.FileInfo) error {
				state, err := readResourceEnvelope(resourceStatePath(dir))
				if err != nil {
					return err
				}
				*state.HighWater = 1
				state.Records = []resource.Record{{Request: in, Actor: resource.LocalActor, RequestHash: resource.RequestHash(in), Phase: "intent", Outcome: resource.UnknownOutcome()}}
				if err := state.validate(); err != nil {
					return err
				}
				binding, err := openResourcePathBinding(dir, directory, lock)
				if err != nil {
					return err
				}
				defer binding.close()
				policyData, err := json.Marshal(observed)
				if err != nil {
					return err
				}
				if err := binding.write(filepath.Join(dir, capacityPolicyFile), policyData, nil); err != nil {
					return err
				}
				journalData, err := json.Marshal(state)
				if err != nil {
					return err
				}
				return binding.write(resourceStatePath(dir), journalData, nil)
			}); err != nil {
				t.Fatal(err)
			}
			second, closeSecond := openResourceApplication(t, dir, owner)
			assertResourceOperationOffline(t, second)
			status := resourceCall(t, second, "resource.operation.status", resource.StatusRequest{Target: in.Target, OperationID: in.OperationID}).(resource.Operation)
			if status.Outcome != resource.UnknownOutcome() || !status.EvidenceDurable || status.Current.Effective != (resource.Effective{TransferConcurrentFiles: tc.files, TransferConcurrentPerPeer: tc.perPeer}) {
				t.Fatal("startup inferred or replayed intent", status)
			}
			persisted, err := readResourceEnvelope(resourceStatePath(dir))
			if err != nil || persisted.Records[0].Phase != "result" || !persisted.Records[0].Pinned() || persisted.Records[0].Outcome != resource.UnknownOutcome() {
				t.Fatal("unknown recovery was not durably republished", persisted, err)
			}
			before := resourceOperationFileEvidence(t, dir)
			replay := resourceCall(t, second, "resource.apply", in).(resource.Operation)
			if replay.Outcome != resource.UnknownOutcome() || !replay.EvidenceDurable || resourceOperationFileEvidence(t, dir) != before {
				t.Fatal("unknown intent was replayed", replay)
			}
			closeSecond()
		})
	}
}
