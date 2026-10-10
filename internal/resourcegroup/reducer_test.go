package resourcegroup

import (
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func groupPendingEvidence(t *testing.T, count int) (ReviewBody, []MemberEvidence) {
	t.Helper()
	r := groupReviewFixture(t, count)
	rows, err := NewEvidence(r)
	if err != nil {
		t.Fatal(err)
	}
	return r, rows
}

func groupOperationReply(row MemberEvidence, action string, outcome resource.Outcome, durable bool) resourcegrant.ManagementReply {
	return resourcegrant.ManagementReply{
		ManagementSelector: row.Request.ManagementSelector, Action: action,
		Operation: &resourcegrant.ManagementOperation{OperationID: row.Request.Apply.OperationID, Outcome: outcome, EvidenceDurable: &durable},
	}
}

func groupAppliedOutcome() resource.Outcome {
	return resource.Outcome{Status: "applied", Configuration: "durable", Accounting: "succeeded", Transfer: "not_required"}
}

func groupObserve(t *testing.T, row MemberEvidence, outcome resource.Outcome, durable bool) MemberEvidence {
	t.Helper()
	row.Dispatch, row.LocalDurability = Dispatching, LocalDurable
	next, err := ObserveApply(row, groupOperationReply(row, resourcegrant.ApplyAction, outcome, durable), LocalDurable)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestGroupReducerPreservesOutcomeStagesAndDurability(t *testing.T) {
	outcomes := []resource.Outcome{
		groupAppliedOutcome(),
		{Status: "failed", Configuration: "not_published", Accounting: "not_attempted", Transfer: "not_attempted"},
		{Status: "canceled", Configuration: "not_attempted", Accounting: "not_attempted", Transfer: "not_attempted"},
		{Status: "saved_not_applied", Configuration: "durable", Accounting: "failed", Transfer: "not_attempted"},
		{Status: "saved_not_applied", Configuration: "durable", Accounting: "succeeded", Transfer: "failed"},
		resource.UnknownOutcome(),
		{Status: "unknown", Configuration: "uncertain", Accounting: "succeeded", Transfer: "failed"},
	}
	for _, outcome := range outcomes {
		for _, durable := range []bool{false, true} {
			t.Run(outcome.Status+"/"+outcome.Accounting+"/"+string(rune('0'+boolInt(durable))), func(t *testing.T) {
				r, rows := groupPendingEvidence(t, 1)
				rows[0] = groupObserve(t, rows[0], outcome, durable)
				s, err := ReduceReview(r, rows)
				if err != nil || rows[0].Target.Outcome != outcome || *rows[0].Target.EvidenceDurable != durable {
					t.Fatal("exact target outcome or durability lost")
				}
				wantApplied := durable && outcome.Status == "applied"
				wantReconcile := !durable || outcome.Status == "unknown"
				if s.AllApplied != wantApplied || s.ReconciliationRequired != wantReconcile || !s.AdmissionFinished || s.DispatchObserved != 1 {
					t.Fatalf("untruthful summary: %+v", s)
				}
			})
		}
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestGroupReducerKeepsMixedResultsIndependent(t *testing.T) {
	r, rows := groupPendingEvidence(t, 4)
	rows[0] = groupObserve(t, rows[0], groupAppliedOutcome(), true)
	rows[1] = groupObserve(t, rows[1], resource.Outcome{Status: "saved_not_applied", Configuration: "durable", Accounting: "succeeded", Transfer: "failed"}, true)
	rows[2].Dispatch, rows[2].LocalDurability = Dispatching, LocalDurable
	var err error
	rows[2], err = ObserveDispatchUnknown(rows[2], LocalDurable)
	if err != nil {
		t.Fatal(err)
	}
	rows[3].AdmissionStop, rows[3].LocalDurability = StopUserCanceled, LocalDurable
	s, err := ReduceReview(r, rows)
	if err != nil || s.Applied != 1 || s.SavedNotApplied != 1 || s.DispatchUnknown != 1 || s.NotAttempted != 1 || s.AllApplied || !s.AdmissionFinished || !s.ReconciliationRequired {
		t.Fatalf("mixed results collapsed: %+v, %v", s, err)
	}
	if rows[2].Target != nil || rows[2].Dispatch != DispatchUnknown || rows[3].Target != nil || rows[3].Dispatch != DispatchNotAttempted {
		t.Fatal("local cancellation or unavailable fabricated a target outcome")
	}
}

func TestGroupReducerStatusFailureRetainsDurableEvidence(t *testing.T) {
	r, rows := groupPendingEvidence(t, 1)
	rows[0] = groupObserve(t, rows[0], groupAppliedOutcome(), true)
	before := cloneEvidence(rows[0])
	for i, state := range []StatusState{StatusUnavailable, StatusUnsupported, StatusQueryFailed} {
		next, err := ObserveStatusFailure(rows[0], state, uint64(i+1), int64(i+1), LocalDurable)
		if err != nil || !reflect.DeepEqual(next.Target, before.Target) || next.Dispatch != DispatchObserved || next.Status.State != state {
			t.Fatal("query failure overwrote durable target result")
		}
		rows[0] = next
	}
	s, err := ReduceReview(r, rows)
	if err != nil || !s.AllApplied || s.StatusFailures != 1 || s.ReconciliationRequired {
		t.Fatal("later status error erased known historical application")
	}
	if before.Status.State != StatusNotQueried {
		t.Fatal("observation mutated original row")
	}
}

func TestGroupReducerLocalDurabilityNeverChangesTargetDurability(t *testing.T) {
	r, rows := groupPendingEvidence(t, 1)
	rows[0] = groupObserve(t, rows[0], groupAppliedOutcome(), false)
	s, err := ReduceReview(r, rows)
	if err != nil || s.AllApplied || s.TargetNonDurable != 1 || s.LocalNonDurable != 0 || !s.ReconciliationRequired {
		t.Fatal("local durable publication promoted target evidence")
	}
	rows[0] = groupObserve(t, mustFreshRow(t, r), groupAppliedOutcome(), true)
	rows[0].LocalDurability = LocalUncertain
	s, err = ReduceReview(r, rows)
	if err != nil || !s.AllApplied || s.TargetNonDurable != 0 || s.LocalNonDurable != 1 || !s.ReconciliationRequired {
		t.Fatal("target evidence falsely established local persistence")
	}
}

func mustFreshRow(t *testing.T, r ReviewBody) MemberEvidence {
	t.Helper()
	rows, err := NewEvidence(r)
	if err != nil {
		t.Fatal(err)
	}
	return rows[0]
}

func TestGroupReducerRequiresCompleteFrozenReviewCoverage(t *testing.T) {
	r, rows := groupPendingEvidence(t, 2)
	if ValidateEvidence(r, rows) != nil {
		t.Fatal("initial evidence does not match review")
	}
	for _, change := range []func([]MemberEvidence){
		func(rows []MemberEvidence) { rows[0].Request.GrantRevision++ },
		func(rows []MemberEvidence) { rows[0].Request.Apply.ReviewRevision = strings.Repeat("d", 64) },
		func(rows []MemberEvidence) { rows[0].Request.Apply.BaseRevision = strings.Repeat("d", 64) },
		func(rows []MemberEvidence) { rows[0].Request.Apply.OperationID = strings.Repeat("d", 64) },
		func(rows []MemberEvidence) {
			rows[0].Request.Apply.Settings.TransferConcurrentFiles = capacity.Limited(9)
		},
		func(rows []MemberEvidence) { rows[0].Execution = ExecutionExcluded },
		func(rows []MemberEvidence) { rows[0].PeerKey = strings.Repeat("d", 64) },
		func(rows []MemberEvidence) { rows[0].GroupRevision = strings.Repeat("d", 64) },
	} {
		copy, err := NewEvidence(r)
		if err != nil {
			t.Fatal(err)
		}
		change(copy)
		if ValidateEvidence(r, copy) == nil {
			t.Fatal("changed original review evidence accepted")
		}
	}
	if _, err := ReduceReview(r, rows[:1]); err == nil {
		t.Fatal("omitted selected row accepted")
	}
	rows[1] = rows[0]
	if _, err := Reduce(rows); err == nil {
		t.Fatal("duplicate row accepted")
	}
}

func TestGroupReducerExcludedRowsStayVisibleAndUnsent(t *testing.T) {
	r := groupReviewFixture(t, 3)
	r.Rows[2].State, r.Rows[2].Reply = ReviewUnavailable, nil
	r, err := BuildReview(r.Selection, r.Rows, r.ExecutionPeers[:1])
	if err != nil {
		t.Fatal(err)
	}
	rows, err := NewEvidence(r)
	if err != nil {
		t.Fatal(err)
	}
	rows[0] = groupObserve(t, rows[0], groupAppliedOutcome(), true)
	s, err := ReduceReview(r, rows)
	if err != nil || !s.AllApplied || s.Selected != 3 || s.Executable != 1 || s.Excluded != 2 || s.ReviewFailures != 1 {
		t.Fatal("successful explicit subset hid excluded members")
	}
	rows[1].Dispatch = DispatchUnknown
	if _, err := ReduceReview(r, rows); err == nil {
		t.Fatal("excluded ready row marked sent")
	}
}

func TestGroupStatusReconciliationPreservesDispatchHistory(t *testing.T) {
	r, rows := groupPendingEvidence(t, 1)
	rows[0].Dispatch, rows[0].LocalDurability = DispatchUnknown, LocalDurable
	reply := groupOperationReply(rows[0], resourcegrant.StatusAction, groupAppliedOutcome(), true)
	next, err := ObserveStatus(rows[0], reply, 1, 1, LocalDurable)
	if err != nil || next.Dispatch != DispatchUnknown || next.Target.Outcome != groupAppliedOutcome() || next.Status.State != StatusObserved {
		t.Fatal("status rewrote local dispatch history")
	}
	rows[0] = next
	s, err := ReduceReview(r, rows)
	if err != nil || !s.AllApplied || s.DispatchUnknown != 1 || s.ReconciliationRequired {
		t.Fatal("matching durable status did not reconcile target evidence")
	}
	*reply.Operation.EvidenceDurable = false
	if !*next.Target.EvidenceDurable || !*next.Status.Operation.EvidenceDurable {
		t.Fatal("reply pointer aliases retained evidence")
	}
	*next.Status.Operation.EvidenceDurable = false
	if !*next.Target.EvidenceDurable {
		t.Fatal("status observation aliases best target evidence")
	}
}

func TestGroupStatusWeakerObservationCannotEraseDurableTerminal(t *testing.T) {
	_, rows := groupPendingEvidence(t, 1)
	row := groupObserve(t, rows[0], groupAppliedOutcome(), true)
	weaker := groupOperationReply(row, resourcegrant.StatusAction, resource.UnknownOutcome(), false)
	next, err := ObserveStatus(row, weaker, 1, 1, LocalDurable)
	if err != nil || next.Target.Outcome != groupAppliedOutcome() || next.Status.Operation.Outcome != resource.UnknownOutcome() || !*next.Target.EvidenceDurable {
		t.Fatal("newer weak observation erased historical durable outcome")
	}
	conflict := groupOperationReply(row, resourcegrant.StatusAction, resource.Outcome{Status: "failed", Configuration: "not_attempted", Accounting: "not_attempted", Transfer: "not_attempted"}, true)
	if _, err := ObserveStatus(row, conflict, 2, 2, LocalDurable); err == nil {
		t.Fatal("conflicting durable terminal outcome accepted")
	}
}

func TestGroupEvidenceRejectsUnmatchedAndExpandedObservations(t *testing.T) {
	_, rows := groupPendingEvidence(t, 1)
	row := groupObserve(t, rows[0], groupAppliedOutcome(), true)
	for _, change := range []func(*resourcegrant.ManagementReply){
		func(r *resourcegrant.ManagementReply) { r.GrantRevision++ },
		func(r *resourcegrant.ManagementReply) { r.Operation.OperationID = strings.Repeat("d", 64) },
		func(r *resourcegrant.ManagementReply) { r.Action = resourcegrant.ApplyAction },
		func(r *resourcegrant.ManagementReply) { r.Operation.EvidenceDurable = nil },
		func(r *resourcegrant.ManagementReply) { r.Operation.Outcome.Status = "offline" },
	} {
		reply := groupOperationReply(row, resourcegrant.StatusAction, groupAppliedOutcome(), true)
		change(&reply)
		if _, err := ObserveStatus(row, reply, 1, 1, LocalDurable); err == nil {
			t.Fatal("unmatched status accepted")
		}
	}
	for _, state := range []StatusState{"offline", "conflict", "known_not_executed", StatusObserved, StatusNotQueried} {
		if _, err := ObserveStatusFailure(row, state, 1, 1, LocalDurable); err == nil {
			t.Fatal("invented query classification accepted")
		}
	}
	for _, bad := range []StatusObservation{
		{State: StatusUnavailable, Sequence: 0, ObservedAt: 1},
		{State: StatusUnavailable, Sequence: uint64(capacity.MaxJSONInteger) + 1, ObservedAt: 1},
		{State: StatusUnavailable, Sequence: 1, ObservedAt: 0},
		{State: StatusUnavailable, Sequence: 1, ObservedAt: MaxObservationTime + 1},
	} {
		if _, err := ObserveStatusFailure(row, bad.State, bad.Sequence, bad.ObservedAt, LocalDurable); err == nil {
			t.Fatal("unbounded or absent observation stamp accepted")
		}
	}
	next, err := ObserveStatusFailure(row, StatusUnavailable, 3, 1, LocalDurable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ObserveStatusFailure(next, StatusUnavailable, 3, 2, LocalDurable); err == nil {
		t.Fatal("observation sequence replay accepted")
	}
}
