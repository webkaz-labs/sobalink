package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/operationjournal"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// These tests use only the existing inert Core fixture and owned TempDir.
// No Core.Open, manager, network, listener, external path or process is used.
func scopedStorageBinding(c *Core, owner *config.Lock, fn func(*resourcePathBinding) error) error {
	return owner.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
		b, err := openResourcePathBinding(c.dir, directory, lock)
		if err != nil {
			return err
		}
		defer b.close()
		return fn(b)
	})
}
func scopedStorageJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func scopedStorageV2(t *testing.T, c *Core) operationjournal.EnvelopeV2 {
	t.Helper()
	if c.resourceState.scoped != nil {
		return *c.resourceState.clone().scoped
	}
	old := c.resourceState
	e, err := operationjournal.ConvertV1(operationjournal.LegacyEnvelope{SchemaVersion: old.SchemaVersion, ResourceID: old.ResourceID, HighWater: old.HighWater, Records: old.Records})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func scopedStorageRemote(t *testing.T, e operationjournal.EnvelopeV2) operationjournal.TaggedRecord {
	t.Helper()
	binding := operationjournal.CurrentBinding{Scope: operationjournal.RemoteOperationScope{Target: resource.Target{SchemaVersion: 1, ResourceID: e.ResourceID}, Relationship: resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: strings.Repeat("b", 64), PeerKey: strings.Repeat("c", 64), PairBinding: strings.Repeat("d", 64)}, GrantID: strings.Repeat("e", 32), ScopeVersion: operationjournal.ScopeVersion}, AuthorizingRevision: 1, IssuanceNonce: strings.Repeat("f", 32), ManagedGeneration: strings.Repeat("1", 64)}
	id, err := operationjournal.CurrentRemoteID(e, binding)
	if err != nil {
		t.Fatal(err)
	}
	record, err := operationjournal.NewRemoteIntent(binding, *e.HighWater+1, resourcegrant.ManagementApplyRequest{OperationID: id, BaseRevision: strings.Repeat("2", 64), ReviewRevision: strings.Repeat("3", 64), Settings: resource.Settings{TransferConcurrentFiles: capacity.Limited(3), TransferConcurrentPerPeer: capacity.Default()}})
	if err != nil {
		t.Fatal(err)
	}
	return record
}
func scopedStorageInstall(t *testing.T, c *Core, owner *config.Lock, e operationjournal.EnvelopeV2) {
	t.Helper()
	if err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error { return c.writeResourceEnvelopeBound(scopedResourceEnvelope(e), b) }); err != nil {
		t.Fatal(err)
	}
	c.initializeResourceIdentity(owner)
	if c.resourceIdentity == "" {
		t.Fatal("reopen unavailable")
	}
}
func TestScopedStorageV1RemainsV1(t *testing.T) {
	c, owner := resourceFixture(t)
	in := resourceApplyRequest(t, c, 3)
	resourceCall(t, c, "resource.inspect", in.Target)
	resourceCall(t, c, "resource.apply", in)
	before := scopedStorageJSON(t, c.resourceState)
	c.initializeResourceIdentity(owner)
	resourceCall(t, c, "resource.operation.status", resource.StatusRequest{Target: in.Target, OperationID: in.OperationID})
	after, err := os.ReadFile(resourceStatePath(c.dir))
	if err != nil || !bytes.Equal(before, after) || c.resourceState.SchemaVersion != 1 || c.resourceState.scoped != nil {
		t.Fatal("implicit migration", err)
	}
}
func TestScopedStorageMigrationPreservesAndSeparatesIntent(t *testing.T) {
	c, owner := resourceFixture(t)
	in := resourceApplyRequest(t, c, 4)
	result := resourceCall(t, c, "resource.apply", in).(resource.Operation)
	old := c.resourceState.clone()
	next := scopedStorageRemote(t, scopedStorageV2(t, c))
	writes := 0
	c.atomicWrite = func(path string, data []byte) error { writes++; return config.AtomicWrite(path, data) }
	if err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error { return c.migrateResourceJournalBound(next, b) }); err != nil {
		t.Fatal(err)
	}
	if writes != 1 || *c.resourceState.HighWater != *old.HighWater || len(c.resourceState.scoped.Records) != len(old.Records) || !reflect.DeepEqual(*c.resourceState.scoped.Records[0].Local, old.Records[0]) {
		t.Fatal("migration changed evidence or admitted intent")
	}
	got := resourceCall(t, c, "resource.apply", in).(resource.Operation)
	if got.Outcome != result.Outcome || !got.EvidenceDurable || writes != 1 {
		t.Fatal("local replay changed")
	}
	c.atomicWrite = nil
	c.initializeResourceIdentity(owner)
	if c.resourceIdentity != old.ResourceID || c.resourceState.SchemaVersion != 2 {
		t.Fatal("identity or format lost")
	}
}
func TestScopedStorageReopenNormalizesBothArms(t *testing.T) {
	c, owner := resourceFixture(t)
	in := resourceApplyRequest(t, c, 3)
	local := resource.Record{Request: in, Actor: resource.LocalActor, RequestHash: resource.RequestHash(in), Phase: "intent", Outcome: resource.UnknownOutcome()}
	legacy, err := c.resourceState.withIntent(local)
	if err != nil {
		t.Fatal(err)
	}
	c.resourceState = legacy
	e := scopedStorageV2(t, c)
	remote := scopedStorageRemote(t, e)
	e, err = operationjournal.WithIntent(e, remote)
	if err != nil {
		t.Fatal(err)
	}
	scopedStorageInstall(t, c, owner, e)
	for _, r := range c.resourceState.scoped.Records {
		if !r.Pinned() || r.Local != nil && (r.Local.Phase != "result" || r.Local.Outcome != resource.UnknownOutcome()) || r.Remote != nil && (r.Remote.Phase != "result" || r.Remote.Outcome != resource.UnknownOutcome()) {
			t.Fatal("intent not normalized")
		}
	}
	data, _ := os.ReadFile(resourceStatePath(c.dir))
	if !bytes.Equal(data, scopedStorageJSON(t, c.resourceState)) || *c.resourceState.HighWater != 2 {
		t.Fatal("normalization not recertified")
	}
}
func TestScopedStorageV2LocalApplyAndRemoteIsolation(t *testing.T) {
	c, owner := resourceFixture(t)
	e := scopedStorageV2(t, c)
	remote := scopedStorageRemote(t, e)
	e, err := operationjournal.WithIntent(e, remote)
	if err != nil {
		t.Fatal(err)
	}
	scopedStorageInstall(t, c, owner, e)
	before := scopedStorageJSON(t, c.resourceState.scoped.Records[0])
	in := resourceApplyRequest(t, c, 7)
	_, _, seq, _ := resource.ParseOperationID(in.OperationID)
	if seq != 2 {
		t.Fatal("local slot ignored remote high-water")
	}
	result := resourceCall(t, c, "resource.apply", in).(resource.Operation)
	if result.Outcome.Status != "applied" || !result.EvidenceDurable || result.Journal.Records != 2 {
		t.Fatal(result)
	}
	if !bytes.Equal(before, scopedStorageJSON(t, c.resourceState.scoped.Records[0])) {
		t.Fatal("local completion replaced remote evidence")
	}
	resourceCommandError(t, c, "resource.operation.status", resource.StatusRequest{Target: in.Target, OperationID: remote.Remote.Request.OperationID}, "resource_invalid")
	projected := scopedStorageJSON(t, result)
	if bytes.Contains(projected, []byte(remote.Remote.Request.OperationID)) || bytes.Contains(projected, []byte(remote.Remote.Scope.Relationship.PairBinding)) {
		t.Fatal("remote identity escaped local projection")
	}
	if _, ok := c.resourceState.find(remote.Remote.Request.OperationID); ok {
		t.Fatal("remote evidence found by local lookup")
	}
	c.initializeResourceIdentity(owner)
	replay := resourceCall(t, c, "resource.apply", in).(resource.Operation)
	if replay.Outcome != result.Outcome || !replay.EvidenceDurable {
		t.Fatal("v2 local replay failed")
	}
}
func TestScopedStorageMigrationFailureBoundaries(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unpublished", true: "uncertain"}[committed], func(t *testing.T) {
			c, owner := resourceFixture(t)
			in := resourceApplyRequest(t, c, 3)
			resourceCall(t, c, "resource.apply", in)
			before := scopedStorageJSON(t, c.resourceState)
			candidate := scopedStorageRemote(t, scopedStorageV2(t, c))
			c.atomicWrite = func(path string, data []byte) error {
				if committed {
					if err := config.AtomicWrite(path, data); err != nil {
						return err
					}
					return config.ErrAtomicCommitted
				}
				return errors.New("synthetic unpublished write")
			}
			err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error { return c.migrateResourceJournalBound(candidate, b) })
			if err == nil || c.resourceFrozen != committed || (c.resourceState.scoped != nil) != committed {
				t.Fatal("wrong migration uncertainty", err)
			}
			data, _ := os.ReadFile(resourceStatePath(c.dir))
			if !committed && (!bytes.Equal(before, data) || !bytes.Equal(before, scopedStorageJSON(t, c.resourceState))) {
				t.Fatal("unpublished conversion changed v1")
			}
			if *c.resourceState.HighWater != 1 {
				t.Fatal("migration consumed intent slot")
			}
			if committed {
				resourceCommandError(t, c, "resource.apply", resourceApplyRequest(t, c, 5), "resource_journal_uncertain")
			}
			old := resourceCall(t, c, "resource.operation.status", resource.StatusRequest{Target: in.Target, OperationID: in.OperationID}).(resource.Operation)
			if !old.EvidenceDurable {
				t.Fatal("prior durable evidence lost")
			}
			c.atomicWrite = nil
			c.initializeResourceIdentity(owner)
			if c.resourceIdentity != in.ResourceID || c.resourceFrozen {
				t.Fatal("reopen failed")
			}
		})
	}
}
func TestScopedStorageReopenRecertificationFailure(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unpublished", true: "uncertain"}[committed], func(t *testing.T) {
			c, owner := resourceFixture(t)
			e := scopedStorageV2(t, c)
			r := scopedStorageRemote(t, e)
			e, err := operationjournal.WithIntent(e, r)
			if err != nil {
				t.Fatal(err)
			}
			scopedStorageInstall(t, c, owner, e)
			id := c.resourceIdentity
			c.atomicWrite = func(path string, data []byte) error {
				if committed {
					if err := config.AtomicWrite(path, data); err != nil {
						return err
					}
					return config.ErrAtomicCommitted
				}
				return errors.New("synthetic unavailable write")
			}
			c.initializeResourceIdentity(owner)
			if c.resourceIdentity != "" || c.resourceLock != nil {
				t.Fatal("uncertified evidence exposed")
			}
			c.atomicWrite = nil
			c.initializeResourceIdentity(owner)
			if c.resourceIdentity != id || c.resourceState.SchemaVersion != 2 {
				t.Fatal("identity recreated or downgraded")
			}
		})
	}
}
func TestScopedStorageStrictVersionDispatch(t *testing.T) {
	for _, data := range []string{
		`{"schemaVersion":3,"resourceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","highWater":0,"records":[]}`,
		`{"schemaVersion":2,"resourceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","highWater":1,"records":[{}]}`,
		`{"schemaVersion":2,"schemaVersion":1,"resourceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","highWater":0,"records":[]}`,
		`{"schemaVersion":2,"resourceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","highWater":0,"records":[],"unexpected":true}`,
	} {
		t.Run(data, func(t *testing.T) {
			c, owner := resourceFixture(t)
			if err := os.WriteFile(resourceStatePath(c.dir), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			c.initializeResourceIdentity(owner)
			after, _ := os.ReadFile(resourceStatePath(c.dir))
			if c.resourceIdentity != "" || string(after) != data {
				t.Fatal("invalid version fell back or recreated state")
			}
		})
	}
}
func TestScopedStorageCandidateIsolationAndValidation(t *testing.T) {
	c, _ := resourceFixture(t)
	e := scopedStorageV2(t, c)
	r := scopedStorageRemote(t, e)
	e, err := operationjournal.WithIntent(e, r)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := scopedResourceEnvelope(e)
	before := scopedStorageJSON(t, wrapped)
	*e.HighWater = 99
	*e.Records[0].Remote.Request.Settings.TransferConcurrentFiles.Value = 19
	clone := wrapped.clone()
	*clone.HighWater = 99
	*clone.scoped.Records[0].Remote.Request.Settings.TransferConcurrentFiles.Value = 20
	if !bytes.Equal(before, scopedStorageJSON(t, wrapped)) || clone.validate() == nil {
		t.Fatal("alias or mismatched mirrors accepted")
	}
	ambiguous := wrapped.clone()
	ambiguous.Records = []resource.Record{}
	if ambiguous.validate() == nil {
		t.Fatal("both version arms accepted")
	}
	if _, err := wrapped.withLocalResult(resource.Record{}, resource.UnknownOutcome()); err == nil {
		t.Fatal("local completion accepted remote last record")
	}
}
func TestScopedStorageMigrationPreflightPinnedFullAndExhausted(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(map[bool]string{false: "pinned_full", true: "exhausted"}[exhausted], func(t *testing.T) {
			c, owner := resourceFixture(t)
			candidate := scopedStorageRemote(t, scopedStorageV2(t, c))
			n := uint64(resourceStateMaxRecords)
			if exhausted {
				n = math.MaxUint64
			}
			s := c.resourceState.clone()
			s.Records = []resource.Record{}
			start := uint64(1)
			if exhausted {
				start = n
			}
			for seq := start; ; seq++ {
				req := resource.ApplyRequest{Target: resourceTarget(c), OperationID: resource.OperationID(c.resourceIdentity, c.resourceNonce, seq), BaseRevision: strings.Repeat("2", 64), Revision: strings.Repeat("3", 64), Settings: resource.Settings{TransferConcurrentFiles: capacity.Default(), TransferConcurrentPerPeer: capacity.Default()}}
				s.Records = append(s.Records, resource.Record{Request: req, Actor: resource.LocalActor, RequestHash: resource.RequestHash(req), Phase: "result", Outcome: resource.UnknownOutcome()})
				if seq == n {
					break
				}
			}
			*s.HighWater = n
			c.resourceState = s
			if !exhausted {
				candidate = scopedStorageRemote(t, scopedStorageV2(t, c))
			}
			before := scopedStorageJSON(t, s)
			writes := 0
			c.atomicWrite = func(string, []byte) error { writes++; return nil }
			err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error { return c.migrateResourceJournalBound(candidate, b) })
			if err == nil || writes != 0 || !bytes.Equal(before, scopedStorageJSON(t, c.resourceState)) {
				t.Fatal("preflight changed old evidence", err, writes)
			}
		})
	}
}
func TestScopedStorageMigrationRejectsInvalidBindingAndLocalCandidate(t *testing.T) {
	c, owner := resourceFixture(t)
	candidate := scopedStorageRemote(t, scopedStorageV2(t, c))
	writes := 0
	c.atomicWrite = func(string, []byte) error { writes++; return nil }
	if err := c.migrateResourceJournalBound(candidate, nil); err == nil {
		t.Fatal("nil binding accepted")
	}
	if err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error {
		return c.migrateResourceJournalBound(operationjournal.TaggedRecord{Kind: operationjournal.LocalKind}, b)
	}); err == nil {
		t.Fatal("local migration candidate accepted")
	}
	saved := c.resourceDirectoryIdentity
	c.resourceDirectoryIdentity = nil
	if err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error { return c.migrateResourceJournalBound(candidate, b) }); err == nil {
		t.Fatal("uncertified directory accepted")
	}
	c.resourceDirectoryIdentity = saved
	if writes != 0 {
		t.Fatal("rejected migration wrote")
	}
}

func TestScopedStorageV2LocalPersistenceBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name             string
		failAt           int
		committed        bool
		status, reopened string
		provider         bool
		frozen           bool
	}{
		{"before_intent", 1, false, "", "", false, false},
		{"uncertain_intent", 1, true, "unknown", "unknown", false, true},
		{"provider_unpublished", 2, false, "failed", "failed", true, false},
		{"provider_uncertain", 2, true, "unknown", "unknown", true, false},
		{"result_unpublished", 3, false, "unknown", "unknown", true, true},
		{"result_uncertain", 3, true, "unknown", "applied", true, true},
		{"durable_result", 0, false, "applied", "applied", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, lock := resourceFixture(t)
			scopedStorageInstall(t, c, lock, scopedStorageV2(t, c))
			in := resourceApplyRequest(t, c, 6)
			writes, provider := 0, false
			c.atomicWrite = func(path string, data []byte) error {
				writes++
				if filepath.Base(path) == capacityPolicyFile {
					provider = true
				}
				if writes == tc.failAt && !tc.committed {
					return errors.New("synthetic disk full private filename")
				}
				if err := config.AtomicWrite(path, data); err != nil {
					return err
				}
				if writes == tc.failAt {
					return config.ErrAtomicCommitted
				}
				return nil
			}
			if tc.status == "" {
				resourceCommandError(t, c, "resource.apply", in, "resource_journal_write_failed")
			} else {
				got := resourceCall(t, c, "resource.apply", in).(resource.Operation)
				if got.Outcome.Status != tc.status || (tc.frozen && got.EvidenceDurable) {
					t.Fatal(got)
				}
				replay := resourceCall(t, c, "resource.apply", in).(resource.Operation)
				if replay.Outcome != got.Outcome || replay.EvidenceDurable != got.EvidenceDurable {
					t.Fatal("failed result leaked from memory", replay)
				}
			}
			if provider != tc.provider || c.resourceFrozen != tc.frozen {
				t.Fatal("wrong stage/freeze", provider, c.resourceFrozen)
			}
			if tc.frozen {
				next := resourceApplyRequest(t, c, 8)
				resourceCommandError(t, c, "resource.apply", next, "resource_journal_uncertain")
			}
			c.atomicWrite = nil
			c.initializeResourceIdentity(lock)
			if c.resourceIdentity == "" {
				t.Fatal("reopen unavailable")
			}
			if tc.reopened != "" {
				got := resourceCall(t, c, "resource.apply", in).(resource.Operation)
				if got.Outcome.Status != tc.reopened || !got.EvidenceDurable {
					t.Fatal("wrong recertified evidence", got)
				}
			} else {
				if *c.resourceState.HighWater != 0 {
					t.Fatal("failed intent consumed sequence")
				}
				resourceCommandError(t, c, "resource.apply", in, "resource_revision_conflict")
			}
		})
	}
}

func TestScopedStorageMigrationRejectsReplacedDirectory(t *testing.T) {
	c, owner := resourceFixture(t)
	candidate := scopedStorageRemote(t, scopedStorageV2(t, c))
	writes := 0
	c.atomicWrite = func(string, []byte) error { writes++; return nil }
	replaceResourceBinding(t, c.dir, "journal", owner)
	err := scopedStorageBinding(c, owner, func(b *resourcePathBinding) error { return c.migrateResourceJournalBound(candidate, b) })
	if err == nil || writes != 0 {
		t.Fatal("replacement journal accepted", err, writes)
	}
	if _, err := os.Stat(resourceStatePath(c.dir)); !os.IsNotExist(err) {
		t.Fatal("replacement journal received migration", err)
	}
}
