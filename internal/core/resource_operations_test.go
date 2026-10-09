package core

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func resourceApplyRequest(t *testing.T, c *Core, files int64) resource.ApplyRequest {
	t.Helper()
	settings := resource.Settings{TransferConcurrentFiles: capacity.Limited(files), TransferConcurrentPerPeer: capacity.Default()}
	preview := resourceCall(t, c, "resource.preview", resource.PreviewRequest{Target: resourceTarget(c), Settings: settings}).(resource.Preview)
	return resource.ApplyRequest{Target: preview.Target, OperationID: preview.OperationID, BaseRevision: preview.BaseRevision, Revision: preview.Revision, Settings: preview.Requested}
}
func resourceCommandError(t *testing.T, c *Core, name string, request any, code string) {
	t.Helper()
	raw, _ := json.Marshal(request)
	_, err := c.Command(context.Background(), webui.Command{RequestID: "same-request", Name: name, Payload: raw})
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) || coded.ErrorCode() != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}
func TestResourceOperationReplayHistoricalAndCurrent(t *testing.T) {
	c, lock := resourceFixture(t)
	in := resourceApplyRequest(t, c, 3)
	result := resourceCall(t, c, "resource.apply", in).(resource.Operation)
	if result.Outcome.Status != "applied" || !result.EvidenceDurable || result.Current.Effective.TransferConcurrentFiles != 3 {
		t.Fatal(result)
	}
	state, err := readResourceEnvelope(resourceStatePath(c.dir))
	if err != nil || *state.HighWater != 1 || len(state.Records) != 1 {
		t.Fatal(state, err)
	}
	changed := c.capacityPolicy()
	changed.Resources["transferConcurrentFiles"] = capacity.Limited(7)
	if err := c.applyCapacityPolicy(changed).legacyError(); err != nil {
		t.Fatal(err)
	}
	c.initializeResourceIdentity(lock)
	writes := 0
	c.atomicWrite = func(string, []byte) error { writes++; return errors.New("must not replay") }
	replay := resourceCall(t, c, "resource.apply", in).(resource.Operation)
	status := resourceCall(t, c, "resource.operation.status", resource.StatusRequest{Target: in.Target, OperationID: in.OperationID}).(resource.Operation)
	if replay.Outcome != result.Outcome || !replay.EvidenceDurable || replay.Current.Effective.TransferConcurrentFiles != 7 || status.Current.Effective.TransferConcurrentFiles != 7 || writes != 0 || len(c.requests) != 0 {
		t.Fatal("historical evidence confused with current state", replay, status, writes)
	}
	in.Settings.TransferConcurrentFiles = capacity.Limited(8)
	resourceCommandError(t, c, "resource.apply", in, "resource_operation_mismatch")
}
func TestResourceOperationFirstPreviewWinsAndABA(t *testing.T) {
	c, lock := resourceFixture(t)
	one, two := resourceApplyRequest(t, c, 3), resourceApplyRequest(t, c, 4)
	if one.OperationID != two.OperationID {
		t.Fatal("preview allocated sequence")
	}
	resourceCall(t, c, "resource.apply", one)
	resourceCommandError(t, c, "resource.apply", two, "resource_operation_mismatch")
	next := resourceApplyRequest(t, c, 5)
	before := c.capacityPolicy()
	other := before.Clone()
	other.Resources["transferConcurrentFiles"] = capacity.Limited(9)
	for _, policy := range []capacity.Policy{other, before} {
		if err := c.applyCapacityPolicy(policy).legacyError(); err != nil {
			t.Fatal(err)
		}
	}
	resourceCommandError(t, c, "resource.apply", next, "resource_revision_conflict")
	next = resourceApplyRequest(t, c, 5)
	c.initializeResourceIdentity(lock)
	resourceCommandError(t, c, "resource.apply", next, "resource_revision_conflict")
}
func TestResourceOperationPersistenceBoundaries(t *testing.T) {
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
func TestResourceOperationCancellationEvidence(t *testing.T) {
	for _, afterProvider := range []bool{false, true} {
		t.Run("cancel", func(t *testing.T) {
			c, _ := resourceFixture(t)
			in := resourceApplyRequest(t, c, 3)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writes, provider := 0, false
			c.atomicWrite = func(path string, data []byte) error {
				writes++
				if filepath.Base(path) == capacityPolicyFile {
					provider = true
				}
				if err := config.AtomicWrite(path, data); err != nil {
					return err
				}
				if writes == 1 && !afterProvider || provider && afterProvider {
					cancel()
				}
				return nil
			}
			raw, _ := json.Marshal(in)
			value, err := c.Command(ctx, webui.Command{RequestID: "cancel", Name: "resource.apply", Payload: raw})
			if err != nil {
				t.Fatal(err)
			}
			result := value.(resource.Operation)
			want := "canceled"
			if afterProvider {
				want = "applied"
			}
			if result.Outcome.Status != want || !result.EvidenceDurable || provider != afterProvider {
				t.Fatal(result, provider)
			}
			state, err := readResourceEnvelope(resourceStatePath(c.dir))
			if err != nil || state.Records[0].Phase != "result" {
				t.Fatal(state, err)
			}
		})
	}
}
func TestResourceOperationRecoveredIntentPinnedAndCertified(t *testing.T) {
	c, lock := resourceFixture(t)
	in := resourceApplyRequest(t, c, 4)
	record := resource.Record{Request: in, Actor: resource.LocalActor, RequestHash: resource.RequestHash(in), Phase: "intent", Outcome: resource.UnknownOutcome()}
	next, err := c.resourceState.withIntent(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.writeResourceEnvelope(next); err != nil {
		t.Fatal(err)
	}
	// Matching current values are deliberately untrustworthy (legacy ABA).
	c.capacity = resourceProjection(c.capacity, in.Settings)
	republish := 0
	c.atomicWrite = func(path string, data []byte) error {
		republish++
		var state resourceEnvelope
		if err := resource.Decode(data, resourceStateMaxBytes, &state); err != nil {
			t.Fatal(err)
		}
		if state.Records[0].Phase != "result" || state.Records[0].Outcome.Status != "unknown" {
			t.Fatal("recovery was not persisted")
		}
		return config.AtomicWrite(path, data)
	}
	c.initializeResourceIdentity(lock)
	if republish != 1 || c.resourceState.Records[0].Outcome.Status != "unknown" || !c.resourceState.Records[0].Pinned() {
		t.Fatal(c.resourceState)
	}
	c.atomicWrite = func(string, []byte) error { t.Fatal("unknown replayed"); return nil }
	result := resourceCall(t, c, "resource.apply", in).(resource.Operation)
	if result.Outcome.Status != "unknown" || !result.EvidenceDurable {
		t.Fatal(result)
	}
}
func TestResourceOperationRetentionAndSequenceBounds(t *testing.T) {
	c, _ := resourceFixture(t)
	original := resourceApplyRequest(t, c, 3)
	makeRecord := func(sequence uint64, pinned bool) resource.Record {
		request := original
		request.OperationID = resource.OperationID(c.resourceIdentity, c.resourceNonce, sequence)
		out := resource.Outcome{Status: "applied", Configuration: "durable", Accounting: "not_required", Transfer: "not_required"}
		if pinned {
			out = resource.UnknownOutcome()
		}
		return resource.Record{Request: request, Actor: resource.LocalActor, RequestHash: resource.RequestHash(request), Phase: "result", Outcome: out}
	}
	state := c.resourceState.clone()
	*state.HighWater = resourceStateMaxRecords
	for n := uint64(1); n <= resourceStateMaxRecords; n++ {
		state.Records = append(state.Records, makeRecord(n, true))
	}
	latest := makeRecord(resourceStateMaxRecords+1, false)
	latest.Phase = "intent"
	latest.Outcome = resource.UnknownOutcome()
	if _, err := state.withIntent(latest); err == nil {
		t.Fatal("pinned journal evicted")
	}
	state.Records[0] = makeRecord(1, false)
	next, err := state.withIntent(latest)
	if err != nil || len(next.Records) != resourceStateMaxRecords {
		t.Fatal(next, err)
	}
	if _, found := next.find(original.OperationID); found {
		t.Fatal("terminal not evicted")
	}
	c.resourceState = next
	resourceCommandError(t, c, "resource.apply", original, "resource_operation_not_retained")
	resourceCommandError(t, c, "resource.operation.status", resource.StatusRequest{Target: original.Target, OperationID: original.OperationID}, "resource_operation_not_retained")
	// Maximum uint64 is valid retained evidence, but never wraps a new ID.
	state = c.resourceState.clone()
	state.Records = []resource.Record{makeRecord(math.MaxUint64, true)}
	*state.HighWater = math.MaxUint64
	c.resourceState = state
	resourceCommandError(t, c, "resource.preview", resource.PreviewRequest{Target: resourceTarget(c), Settings: original.Settings}, "resource_sequence_exhausted")
	if state.validate() != nil {
		t.Fatal("max high-water invalid")
	}
}
func TestResourceOperationProviderStages(t *testing.T) {
	failure := errors.New("private synthetic error")
	for _, tc := range []struct {
		out    capacityApplyOutcome
		status string
	}{
		{capacityApplyOutcome{Err: failure}, "failed"},
		{capacityApplyOutcome{SaveAttempted: true, SaveErr: failure}, "failed"},
		{capacityApplyOutcome{SaveAttempted: true, Published: true}, "applied"},
		{capacityApplyOutcome{SaveAttempted: true, Published: true, AccountingAttempted: true, AccountingErr: failure}, "saved_not_applied"},
		{capacityApplyOutcome{SaveAttempted: true, Published: true, AccountingAttempted: true, TransferAttempted: true, TransferErr: failure}, "saved_not_applied"},
		{capacityApplyOutcome{SaveAttempted: true, Published: true, SaveErr: config.ErrAtomicCommitted, AccountingAttempted: true, AccountingErr: failure}, "unknown"},
		{capacityApplyOutcome{SaveAttempted: true, Published: true, SaveErr: config.ErrAtomicCommitted, AccountingAttempted: true, TransferAttempted: true, TransferErr: failure}, "unknown"},
	} {
		got := resourceProviderOutcome(tc.out)
		if got.Status != tc.status || got.Validate() != nil {
			t.Fatal(got)
		}
		data, _ := json.Marshal(got)
		if strings.Contains(string(data), "private") {
			t.Fatal("private error leaked")
		}
	}
}
func TestResourceOperationCloseWaitsForFinalEvidence(t *testing.T) {
	// Audited bare Core.close path: all listeners/controllers/maps are nil;
	// inert manager Close only locks/sets closed. No Open or background work.
	c, _ := resourceFixture(t)
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.transfers = &transfer.Manager{}
	in := resourceApplyRequest(t, c, 4)
	resultEntered, release := make(chan struct{}), make(chan struct{})
	count := 0
	c.atomicWrite = func(path string, data []byte) error {
		count++
		if count == 3 {
			close(resultEntered)
			<-release
		}
		return config.AtomicWrite(path, data)
	}
	done := make(chan error, 1)
	raw, _ := json.Marshal(in)
	go func() {
		value, err := c.Command(context.Background(), webui.Command{RequestID: "close", Name: "resource.apply", Payload: raw})
		if err == nil && value.(resource.Operation).Outcome.Status != "applied" {
			err = errors.New("not applied")
		}
		done <- err
	}()
	<-resultEntered
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	<-c.ctx.Done()
	select {
	case <-closed:
		t.Fatal("Close did not wait for evidence")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	state, err := readResourceEnvelope(resourceStatePath(c.dir))
	if err != nil || state.Records[0].Outcome.Status != "applied" {
		t.Fatal(state, err)
	}
}
func TestResourceOperationConcurrentApplyAndStatus(t *testing.T) {
	c, _ := resourceFixture(t)
	in := resourceApplyRequest(t, c, 4)
	var wg sync.WaitGroup
	errorsSeen := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, _ := json.Marshal(in)
			_, err := c.Command(context.Background(), webui.Command{RequestID: "same", Name: "resource.apply", Payload: raw})
			errorsSeen <- err
		}()
	}
	wg.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if *c.resourceState.HighWater != 1 || len(c.requests) != 0 {
		t.Fatal("re-executed or cached")
	}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, _ := json.Marshal(resource.StatusRequest{Target: in.Target, OperationID: in.OperationID})
			_, err := c.Command(context.Background(), webui.Command{RequestID: "same", Name: "resource.operation.status", Payload: raw})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
func TestResourceOperationMalformedJournalAndBudget(t *testing.T) {
	c, lock := resourceFixture(t)
	in := resourceApplyRequest(t, c, 4)
	resourceCall(t, c, "resource.apply", in)
	data, err := os.ReadFile(resourceStatePath(c.dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(data), `"phase":"result"`, `"phase":"future"`, 1),
		strings.Replace(string(data), `"actor":"local-control"`, `"actor":"client"`, 1),
		strings.Replace(string(data), `"highWater":1`, `"highWater":0`, 1),
		strings.Replace(string(data), `"status":"applied"`, `"status":"unknown"`, 1),
		strings.Replace(string(data), `"phase":"result"`, `"Phase":"result"`, 1),
		strings.Replace(string(data), `"accounting":"not_required"`, `"accounting":null`, 1),
	} {
		if err := os.WriteFile(resourceStatePath(c.dir), []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		writes := 0
		c.atomicWrite = func(string, []byte) error { writes++; return nil }
		c.initializeResourceIdentity(lock)
		if c.resourceIdentity != "" || writes != 0 {
			t.Fatal("bad evidence trusted or rewritten")
		}
	}
	// Maximum request values plus longest valid outcome fit reserved record size.
	in.OperationID = resource.OperationID(in.ResourceID, strings.Repeat("f", 32), math.MaxUint64)
	in.Settings = resource.Settings{TransferConcurrentFiles: capacity.Limited(capacity.MaxJSONInteger), TransferConcurrentPerPeer: capacity.Limited(capacity.MaxJSONInteger)}
	record := resource.Record{Request: in, Actor: resource.LocalActor, RequestHash: resource.RequestHash(in), Phase: "result", Outcome: resource.Outcome{Status: "saved_not_applied", Configuration: "durable", Accounting: "succeeded", Transfer: "failed"}}
	encoded, _ := json.Marshal(record)
	if len(encoded) > resourceRecordMaxBytes {
		t.Fatal("completion reservation too small", len(encoded))
	}
}

func TestResourceOperationQueuedCancellationDoesNotConsumeSlot(t *testing.T) {
	c, _ := resourceFixture(t)
	in := resourceApplyRequest(t, c, 3)
	ctx, cancel := context.WithCancel(context.Background())
	c.op.Lock()
	raw, _ := json.Marshal(in)
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, err := c.Command(ctx, webui.Command{RequestID: "queued", Name: "resource.apply", Payload: raw})
		done <- err
	}()
	<-started
	cancel()
	c.op.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if *c.resourceState.HighWater != 0 || len(c.resourceState.Records) != 0 {
		t.Fatal("canceled request consumed slot")
	}
}
func TestResourceOperationRuntimeFailureAndUncertainSave(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run("accounting", func(t *testing.T) {
			c, _ := resourceFixture(t)
			c.transfers = &transfer.Manager{}
			in := resourceApplyRequest(t, c, 3)
			if err := c.transfers.Close(); err != nil {
				t.Fatal(err)
			}
			c.atomicWrite = func(path string, data []byte) error {
				if err := config.AtomicWrite(path, data); err != nil {
					return err
				}
				if uncertain && filepath.Base(path) == capacityPolicyFile {
					return config.ErrAtomicCommitted
				}
				return nil
			}
			got := resourceCall(t, c, "resource.apply", in).(resource.Operation)
			status := "saved_not_applied"
			if uncertain {
				status = "unknown"
			}
			if got.Outcome.Status != status || got.Outcome.Accounting != "failed" || got.Outcome.Transfer != "not_attempted" || !got.EvidenceDurable || got.Current.Effective.TransferConcurrentFiles != 3 {
				t.Fatal(got)
			}
		})
	}
}
func TestResourceOperationRecoveryRepublishFailureDisablesOnlyResource(t *testing.T) {
	for _, intent := range []bool{false, true} {
		for _, committed := range []bool{false, true} {
			t.Run("republish", func(t *testing.T) {
				c, lock := resourceFixture(t)
				in := resourceApplyRequest(t, c, 3)
				resourceCall(t, c, "resource.apply", in)
				if intent {
					state := c.resourceState.clone()
					state.Records[0].Phase = "intent"
					state.Records[0].Outcome = resource.UnknownOutcome()
					if err := c.writeResourceEnvelope(state); err != nil {
						t.Fatal(err)
					}
				}
				id := c.resourceIdentity
				c.atomicWrite = func(path string, data []byte) error {
					if committed {
						if err := config.AtomicWrite(path, data); err != nil {
							return err
						}
						return config.ErrAtomicCommitted
					}
					return errors.New("synthetic disk full")
				}
				c.initializeResourceIdentity(lock)
				if c.resourceIdentity != "" {
					t.Fatal("uncertified journal exposed")
				}
				resourceCommandError(t, c, "resource.apply", in, "resource_unavailable")
				if _, err := c.capacityCommand("policy.config", json.RawMessage(`{}`)); err != nil {
					t.Fatal("legacy disabled", err)
				}
				c.atomicWrite = nil
				c.initializeResourceIdentity(lock)
				if c.resourceIdentity != id {
					t.Fatal("identity replaced")
				}
				got := resourceCall(t, c, "resource.apply", in).(resource.Operation)
				want := "applied"
				if intent {
					want = "unknown"
				}
				if got.Outcome.Status != want || !got.EvidenceDurable {
					t.Fatal(got)
				}
			})
		}
	}
}
func TestResourceOperationUnsafeStateDirectoryAndFile(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run("symlink", func(t *testing.T) {
			c, owner := resourceFixture(t)
			in := resourceApplyRequest(t, c, 3)
			path := resourceStatePath(c.dir)
			outside := t.TempDir()
			target := filepath.Join(outside, "state.json")
			original, _ := os.ReadFile(path)
			if err := os.WriteFile(target, original, 0600); err != nil {
				t.Fatal(err)
			}
			if directory {
				if err := os.Rename(filepath.Dir(path), filepath.Join(c.dir, "old-resource-state")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Dir(path)); err != nil {
					t.Skip("symlink unavailable")
				}
			} else {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skip("symlink unavailable")
				}
			}
			code := "resource_journal_write_failed"
			if directory {
				code = "resource_unavailable"
			}
			resourceCommandError(t, c, "resource.apply", in, code)
			if *c.resourceState.HighWater != 0 {
				t.Fatal("unsafe target consumed sequence")
			}
			c.initializeResourceIdentity(owner)
			if c.resourceIdentity != "" {
				t.Fatal("unsafe state reopened")
			}
			after, _ := os.ReadFile(target)
			if string(after) != string(original) {
				t.Fatal("symlink target changed")
			}
		})
	}
}

func TestResourceOperationLiveOwnershipRequired(t *testing.T) {
	for _, kind := range []string{"closed", "profile_replaced", "lock_replaced"} {
		t.Run(kind, func(t *testing.T) {
			c, owner := resourceFixture(t)
			in := resourceApplyRequest(t, c, 3)
			switch kind {
			case "closed":
				if err := owner.Close(); err != nil {
					t.Fatal(err)
				}
			case "profile_replaced":
				replaceResourceBinding(t, c.dir, "profile", owner)
			case "lock_replaced":
				replaceResourceBinding(t, c.dir, "lock", owner)
			}
			writes := 0
			c.atomicWrite = func(string, []byte) error { writes++; return nil }
			resourceCommandError(t, c, "resource.apply", in, "resource_unavailable")
			resourceCommandError(t, c, "resource.operation.status", resource.StatusRequest{Target: in.Target, OperationID: in.OperationID}, "resource_unavailable")
			if writes != 0 {
				t.Fatal("lost ownership allowed mutation")
			}
		})
	}
}
func TestResourceOperationLifecycleLockCloseJoinsEvidence(t *testing.T) {
	c, owner := resourceFixture(t)
	in := resourceApplyRequest(t, c, 3)
	entered, release := make(chan struct{}), make(chan struct{})
	writes := 0
	c.atomicWrite = func(path string, data []byte) error {
		writes++
		if writes == 3 {
			close(entered)
			<-release
		}
		return config.AtomicWrite(path, data)
	}
	done := make(chan error, 1)
	raw, _ := json.Marshal(in)
	go func() {
		_, err := c.Command(context.Background(), webui.Command{RequestID: "owner", Name: "resource.apply", Payload: raw})
		done <- err
	}()
	<-entered
	closeStarted, closed := make(chan struct{}), make(chan error, 1)
	go func() { close(closeStarted); closed <- owner.Close() }()
	<-closeStarted
	select {
	case <-closed:
		t.Fatal("ownership closed during final evidence")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	before := writes
	resourceCommandError(t, c, "resource.apply", in, "resource_unavailable")
	resourceCommandError(t, c, "resource.operation.status", resource.StatusRequest{Target: in.Target, OperationID: in.OperationID}, "resource_unavailable")
	if writes != before {
		t.Fatal("late write after ownership release")
	}
}

// Fixture-only state seeding still holds actual profile ownership and uses the
// production bound writer. There is no unowned journal write path in Core.
func (c *Core) writeResourceEnvelope(state resourceEnvelope) error {
	return c.resourceLock.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
		binding, err := openResourcePathBinding(c.dir, directory, lock)
		if err != nil {
			return err
		}
		defer binding.close()
		return c.writeResourceEnvelopeBound(state, binding)
	})
}
