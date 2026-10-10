package core

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

func groupStoreRecordFixture(t *testing.T, sequence uint64, count int) resourceGroupRecord {
	t.Helper()
	settings := resource.Settings{TransferConcurrentFiles: capacity.Limited(capacity.MaxJSONInteger), TransferConcurrentPerPeer: capacity.Limited(capacity.MaxJSONInteger)}
	template, err := resourcegroup.NewTemplate(settings)
	if err != nil {
		t.Fatal(err)
	}
	selection := resourcegroup.Selection{SchemaVersion: resourcegroup.SchemaVersion, Template: template, Members: make([]resourcegroup.Member, count)}
	origins := make([]resourceGroupOrigin, count)
	for i := range selection.Members {
		peer := fmt.Sprintf("%064x", i+1)
		files, perPeer := capacity.Limited(capacity.MaxJSONInteger), capacity.Limited(capacity.MaxJSONInteger)
		selection.Members[i] = resourcegroup.Member{PeerKey: peer, Selector: resourcegrant.ManagementSelector{ProtocolVersion: resourcegrant.ManagementProtocolVersion, Target: resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: fmt.Sprintf("%032x", i+1)}, GrantID: strings.Repeat("a", 32), GrantRevision: uint64(capacity.MaxJSONInteger)}, Override: &resourcegroup.Override{TransferConcurrentFiles: &files, TransferConcurrentPerPeer: &perPeer}}
		origins[i] = resourceGroupOrigin{PeerKey: peer, State: resourceGroupOriginCaptured, Relationship: &resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: strings.Repeat("f", 64), PeerKey: peer, PairBinding: strings.Repeat("e", 64)}}
	}
	resolved, err := resourcegroup.ResolveSelection(selection)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]resourcegroup.ReviewRow, count)
	peers := make([]string, count)
	for i, member := range resolved.Members {
		peers[i] = member.PeerKey
		rows[i] = resourcegroup.ReviewRow{SchemaVersion: resourcegroup.SchemaVersion, PeerKey: member.PeerKey, State: resourcegroup.ReviewReady, Reply: &resourcegrant.ManagementReply{ManagementSelector: member.Selector, Action: resourcegrant.PreviewAction, Preview: &resourcegrant.ManagementPreview{OperationID: strings.Repeat("b", 64), BaseRevision: strings.Repeat("c", 64), ReviewRevision: strings.Repeat("d", 64), Requested: member.Requested, Effective: resource.Effective{TransferConcurrentFiles: capacity.MaxJSONInteger, TransferConcurrentPerPeer: capacity.MaxJSONInteger}}}}
	}
	review, err := resourcegroup.BuildReview(resolved, rows, peers)
	if err != nil {
		t.Fatal(err)
	}
	run, err := newResourceGroupRecord(fmt.Sprintf("%032x", sequence), review, origins, sequence, resourcegroup.MaxObservationTime)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func groupStoreEnvelopeFixture(t *testing.T, count int, unresolved bool) resourceGroupEnvelope {
	t.Helper()
	e, err := newResourceGroupEnvelope(strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		run := groupStoreRecordFixture(t, uint64(i+1), 1)
		row := &run.Evidence.Members[0]
		row.Dispatch = resourcegroup.DispatchObserved
		durable := !unresolved
		outcome := resource.Outcome{Status: "applied", Configuration: "durable", Accounting: "not_required", Transfer: "not_required"}
		if unresolved {
			outcome = resource.UnknownOutcome()
		}
		row.Target = &resourcegrant.ManagementOperation{OperationID: row.Request.Apply.OperationID, Outcome: outcome, EvidenceDurable: &durable}
		run.Admission, run.UpdateSequence = resourceGroupAdmissionFinished, 3
		e.Runs = append(e.Runs, run)
		*e.HighWater = run.AcceptedSequence
	}
	if e.validate() != nil {
		t.Fatal("invalid fixture envelope")
	}
	return e
}

func groupStoreJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGroupStoreCodecControllerBindingAndStrictBounds(t *testing.T) {
	e := groupStoreEnvelopeFixture(t, 1, false)
	good := string(groupStoreJSON(t, e))
	decoded, err := decodeResourceGroupEnvelope([]byte(good))
	if err != nil || !reflect.DeepEqual(e, decoded) {
		t.Fatal("store round trip")
	}
	for _, bad := range []string{
		good + good, "null", "[]",
		strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":2`, 1),
		strings.Replace(good, `"schemaVersion":1`, `"SchemaVersion":1`, 1),
		strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1),
		strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":1,"prepared":true`, 1),
		strings.Replace(good, `"highWater":1`, `"highWater":null`, 1),
		strings.Replace(good, `"highWater":1`, `"highWater":9007199254740992`, 1),
		strings.Replace(good, `"controllerResourceId":"`+e.ControllerResourceID+`"`, `"controllerResourceId":"invalid"`, 1),
		strings.Replace(good, `"state":"captured"`, `"state":"trusted"`, 1),
		strings.Replace(good, `,"evidenceDurable":true`, "", 1),
		strings.Repeat(" ", resourceGroupMaxStoreBytes+1),
	} {
		if got, err := decodeResourceGroupEnvelope([]byte(bad)); err == nil || !reflect.DeepEqual(got, resourceGroupEnvelope{}) {
			t.Fatal("expanded or malformed store accepted")
		}
	}
	*e.HighWater = 2
	if e.validate() == nil {
		t.Fatal("high-water hole accepted")
	}
}

func TestGroupStoreUnavailableOriginNeverBecomesExecutable(t *testing.T) {
	run := groupStoreRecordFixture(t, 1, 2)
	run.Review.Rows[1].State, run.Review.Rows[1].Reply = resourcegroup.ReviewUnavailable, nil
	review, err := resourcegroup.BuildReview(run.Review.Selection, run.Review.Rows, run.Review.ExecutionPeers[:1])
	if err != nil {
		t.Fatal(err)
	}
	run.Origins[1] = resourceGroupOrigin{PeerKey: review.Rows[1].PeerKey, State: resourceGroupOriginUnavailable}
	run, err = newResourceGroupRecord(run.RunID, review, run.Origins, 1, 1)
	if err != nil || run.validate() != nil {
		t.Fatal("failed selected peer requires invented relationship")
	}
	run.Origins[0].State, run.Origins[0].Relationship = resourceGroupOriginUnavailable, nil
	if run.validate() == nil {
		t.Fatal("ready peer lacks captured origin")
	}
}

func TestGroupStoreOriginalRequestsAndDeepCopies(t *testing.T) {
	e := groupStoreEnvelopeFixture(t, 1, false)
	copy, err := cloneResourceGroupEnvelope(e)
	if err != nil {
		t.Fatal(err)
	}
	copy.Runs[0].Origins[0].Relationship.PairBinding = strings.Repeat("1", 64)
	*copy.Runs[0].Evidence.Members[0].Target.EvidenceDurable = false
	*copy.Runs[0].Review.Selection.Template.Settings.TransferConcurrentFiles.Value = 2
	if e.Runs[0].Origins[0].Relationship.PairBinding != strings.Repeat("e", 64) || !*e.Runs[0].Evidence.Members[0].Target.EvidenceDurable || *e.Runs[0].Review.Selection.Template.Settings.TransferConcurrentFiles.Value != capacity.MaxJSONInteger {
		t.Fatal("clone aliases owned evidence")
	}
	if copy.validate() == nil {
		t.Fatal("changed original request/template accepted")
	}
	copy, _ = cloneResourceGroupEnvelope(e)
	copy.Runs[0].Evidence.Members[0].Request.Apply.ReviewRevision = strings.Repeat("f", 64)
	if copy.validate() == nil {
		t.Fatal("original per-peer token substitution accepted")
	}
}

func TestGroupStoreReservationIncludesFutureRefreshGrowth(t *testing.T) {
	run := groupStoreRecordFixture(t, 1, resourcegroup.MaxMembers)
	worst, err := resourceGroupWorstRecord(run)
	if err != nil || len(groupStoreJSON(t, worst)) > resourceGroupMaxRunBytes || len(groupStoreJSON(t, worst)) <= len(groupStoreJSON(t, run)) {
		t.Fatal("maximum complete run reservation")
	}
	e, _ := newResourceGroupEnvelope(strings.Repeat("a", 32))
	e, err = withResourceGroupRun(e, run)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := resourceGroupReservedBytes(e)
	if err != nil || reserved < len(groupStoreJSON(t, e)) || reserved > resourceGroupMaxStoreBytes {
		t.Fatal("envelope reservation excludes future growth")
	}
	// Every currently valid terminal stage is no longer than the reserved
	// canceled/not_attempted shape; false evidence flags reserve the extra byte.
	for _, outcome := range []resource.Outcome{
		{Status: "applied", Configuration: "durable", Accounting: "not_required", Transfer: "not_required"},
		{Status: "failed", Configuration: "not_published", Accounting: "not_attempted", Transfer: "not_attempted"},
		{Status: "failed", Configuration: "not_attempted", Accounting: "not_attempted", Transfer: "not_attempted"},
		{Status: "saved_not_applied", Configuration: "durable", Accounting: "not_required", Transfer: "failed"},
		{Status: "saved_not_applied", Configuration: "durable", Accounting: "failed", Transfer: "not_attempted"},
		{Status: "unknown", Configuration: "uncertain", Accounting: "not_required", Transfer: "not_required"},
		resource.UnknownOutcome(),
	} {
		candidate, _ := cloneResourceGroupRecord(worst)
		for i := range candidate.Evidence.Members {
			candidate.Evidence.Members[i].Target.Outcome = outcome
			candidate.Evidence.Members[i].Status.Operation.Outcome = outcome
		}
		if candidate.validate() != nil || len(groupStoreJSON(t, candidate)) > len(groupStoreJSON(t, worst)) {
			t.Fatal("terminal outcome outgrows reservation")
		}
	}
	// Non-active retained runs also reserve complete later status observations.
	e = groupStoreEnvelopeFixture(t, 2, true)
	reserved, err = resourceGroupReservedBytes(e)
	if err != nil || reserved <= len(groupStoreJSON(t, e)) {
		t.Fatal("retained unknown records lack refresh reservation")
	}
}

func TestGroupStoreAllPinnedRefusesAdmission(t *testing.T) {
	e := groupStoreEnvelopeFixture(t, resourceGroupMaxRuns, true)
	before := string(groupStoreJSON(t, e))
	if _, err := withResourceGroupRun(e, groupStoreRecordFixture(t, resourceGroupMaxRuns+1, 1)); err != errResourceGroupCapacity {
		t.Fatal("all-pinned store did not refuse admission")
	}
	if string(groupStoreJSON(t, e)) != before {
		t.Fatal("failed reservation mutated retained evidence")
	}
}

func TestGroupStoreEvictionCannotRestorePreparedAdmission(t *testing.T) {
	e := groupStoreEnvelopeFixture(t, resourceGroupMaxRuns, false)
	old := e.Runs[0].acceptedInput()
	run := groupStoreRecordFixture(t, resourceGroupMaxRuns+1, 1)
	next, err := withResourceGroupRun(e, run)
	if err != nil || len(next.Runs) != resourceGroupMaxRuns || next.Runs[0].AcceptedSequence != 2 {
		t.Fatal("oldest resolved eviction was not deterministic")
	}
	if _, found, err := matchResourceGroupRun(next, old); err != nil || found {
		t.Fatal("evicted ID became admitted or replayable")
	}
	copy, found, err := matchResourceGroupRun(next, run.acceptedInput())
	if err != nil || !found || !reflect.DeepEqual(copy, run) {
		t.Fatal("retained exact apply did not return copied evidence")
	}
	changed := run.acceptedInput()
	changed.ReviewRevision = strings.Repeat("f", 64)
	if _, _, err := matchResourceGroupRun(next, changed); err != errResourceGroupReview {
		t.Fatal("changed body reused retained ID")
	}
}

func TestGroupStoreNormalizeStopsWithoutReplay(t *testing.T) {
	e, _ := newResourceGroupEnvelope(strings.Repeat("a", 32))
	run := groupStoreRecordFixture(t, 1, 2)
	var err error
	e, err = withResourceGroupRun(e, run)
	if err != nil {
		t.Fatal(err)
	}
	e.Runs[0].Evidence.Members[0].Dispatch = resourcegroup.Dispatching
	next, err := normalizeResourceGroupEnvelope(e)
	if err != nil || next.Runs[0].Admission != resourceGroupAdmissionFinished || next.Runs[0].Evidence.Members[0].Dispatch != resourcegroup.DispatchUnknown || next.Runs[0].Evidence.Members[1].Dispatch != resourcegroup.DispatchNotAttempted || next.Runs[0].Evidence.Members[1].AdmissionStop != resourcegroup.StopRestarted || !next.Runs[0].pinned() {
		t.Fatal("restart normalization created resume or false nonexecution")
	}
	if e.Runs[0].Evidence.Members[0].Dispatch != resourcegroup.Dispatching || e.Runs[0].Evidence.Members[1].AdmissionStop != resourcegroup.StopNone {
		t.Fatal("normalization mutated input before publication")
	}
}

func TestGroupStoreTransitionsRequireIntentAndRetainDurableEvidence(t *testing.T) {
	e, _ := newResourceGroupEnvelope(strings.Repeat("a", 32))
	run := groupStoreRecordFixture(t, 1, 1)
	e, err := withResourceGroupRun(e, run)
	if err != nil {
		t.Fatal(err)
	}
	next, _ := cloneResourceGroupRecord(run)
	next.UpdateSequence++
	next.Evidence.Members[0].Dispatch = resourcegroup.DispatchUnknown
	if _, err := withResourceGroupUpdate(e, next); err == nil {
		t.Fatal("possible execution appeared without recorded dispatch intent")
	}
	next.Evidence.Members[0].Dispatch = resourcegroup.Dispatching
	e, err = withResourceGroupUpdate(e, next)
	if err != nil {
		t.Fatal(err)
	}
	reset, _ := cloneResourceGroupRecord(next)
	reset.UpdateSequence++
	reset.Evidence.Members[0].Dispatch = resourcegroup.DispatchNotAttempted
	if _, err := withResourceGroupUpdate(e, reset); err == nil {
		t.Fatal("possible dispatch reset to unsent")
	}
	e = groupStoreEnvelopeFixture(t, 1, false)
	weaker, _ := cloneResourceGroupRecord(e.Runs[0])
	weaker.UpdateSequence++
	*weaker.Evidence.Members[0].Target.EvidenceDurable = false
	if _, err := withResourceGroupUpdate(e, weaker); err == nil {
		t.Fatal("stronger historical target result erased")
	}
}

func TestGroupRetryBarrierRequiresDurableTerminalStatus(t *testing.T) {
	e := groupStoreEnvelopeFixture(t, 1, true)
	selection := e.Runs[0].Review.Selection
	blockers, err := resourceGroupUnresolved(e, selection)
	if err != nil || len(blockers) != 1 {
		t.Fatal("unknown target outcome did not block new operation")
	}
	row := &e.Runs[0].Evidence.Members[0]
	row.Status = resourcegroup.StatusObservation{State: resourcegroup.StatusUnavailable, Sequence: 1, ObservedAt: 1}
	if blockers, err := resourceGroupUnresolved(e, selection); err != nil || len(blockers) != 1 {
		t.Fatal("unavailable query cleared barrier")
	}
	row.Target.Outcome = resource.Outcome{Status: "applied", Configuration: "durable", Accounting: "not_required", Transfer: "not_required"}
	if blockers, err := resourceGroupUnresolved(e, selection); err != nil || len(blockers) != 1 {
		t.Fatal("non-durable applied observation cleared barrier")
	}
	*row.Target.EvidenceDurable = true
	row.Status = resourcegroup.StatusObservation{State: resourcegroup.StatusObserved, Sequence: 2, ObservedAt: 2, Operation: row.Target}
	if blockers, err := resourceGroupUnresolved(e, selection); err != nil || len(blockers) != 0 {
		t.Fatal("durable terminal query did not resolve possible execution")
	}
}

func TestGroupRetryBarrierCannotBeBypassedByGrantOrPairReplacement(t *testing.T) {
	e := groupStoreEnvelopeFixture(t, 1, true)
	review, err := resourcegroup.CloneReview(e.Runs[0].Review)
	if err != nil {
		t.Fatal(err)
	}
	selection := review.Selection
	selection.Members[0].Selector.GrantID = strings.Repeat("f", 32)
	selection.Members[0].Selector.GrantRevision = 1
	if blockers, err := resourceGroupUnresolved(e, selection); err != nil || len(blockers) != 1 {
		t.Fatal("replacement grant bypassed unknown operation")
	}
	e.Runs[0].Origins[0].Relationship.PairBinding = strings.Repeat("1", 64)
	if blockers, err := resourceGroupUnresolved(e, selection); err != nil || len(blockers) != 1 {
		t.Fatal("different pair scope erased same resource uncertainty")
	}
	selection.Members[0].Selector.Target.ResourceID = strings.Repeat("f", 32)
	if blockers, err := resourceGroupUnresolved(e, selection); err != nil || len(blockers) != 0 {
		t.Fatal("distinct resource identity inherited old scope")
	}
}

func TestGroupStoreCountersReserveCompletionAndRestartReduction(t *testing.T) {
	e, _ := newResourceGroupEnvelope(strings.Repeat("a", 32))
	run := groupStoreRecordFixture(t, 1, 1)
	e, err := withResourceGroupRun(e, run)
	if err != nil {
		t.Fatal(err)
	}
	e.Runs[0].UpdateSequence = uint64(capacity.MaxJSONInteger) - 2
	intent, _ := cloneResourceGroupRecord(e.Runs[0])
	intent.UpdateSequence++
	intent.Evidence.Members[0].Dispatch = resourcegroup.Dispatching
	if _, err := withResourceGroupUpdate(e, intent); err == nil {
		t.Fatal("dispatch intent consumed mandatory completion headroom")
	}
	e.Runs[0].UpdateSequence = uint64(capacity.MaxJSONInteger) - 1
	stopped, _ := cloneResourceGroupRecord(e.Runs[0])
	stopped.UpdateSequence++
	stopped.Admission = resourceGroupAdmissionFinished
	stopped.Evidence.Members[0].AdmissionStop = resourcegroup.StopBudgetExhausted
	final, err := withResourceGroupUpdate(e, stopped)
	if err != nil {
		t.Fatal("reserved final reduction refused")
	}
	if _, err := normalizeResourceGroupEnvelope(final); err != nil {
		t.Fatal("terminal maximum counter required an unnecessary update")
	}
	attempt, _ := cloneResourceGroupRecord(stopped)
	attempt.UpdateSequence++
	if _, err := withResourceGroupUpdate(final, attempt); err == nil {
		t.Fatal("exhausted status/update counter wrapped or advanced")
	}
	active := stopped
	active.Admission = resourceGroupAdmissionActive
	if active.validate() == nil {
		t.Fatal("maximum active record cannot normalize on restart")
	}
}
