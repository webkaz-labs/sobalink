package resourcegroup

import (
	"reflect"
	"strings"
	"testing"
)

func TestGroupLocalInputsRequireExactConfirmationAndIdentifiers(t *testing.T) {
	review := groupReviewFixture(t, 2)
	id := strings.Repeat("a", 32)
	inputs := []struct {
		value  any
		decode func([]byte) error
	}{
		{PreviewInput{SchemaVersion: SchemaVersion, Selection: groupSelectionFixture(t, 2)}, func(data []byte) error { _, err := DecodePreviewInput(data); return err }},
		{SelectInput{SchemaVersion, id, review.Revision, review.ExecutionPeers}, func(data []byte) error { _, err := DecodeSelectInput(data); return err }},
		{ApplyInput{SchemaVersion, id, review.Revision, review.ExecutionPeers, true}, func(data []byte) error { _, err := DecodeApplyInput(data); return err }},
		{StatusInput{SchemaVersion, id}, func(data []byte) error { _, err := DecodeStatusInput(data); return err }},
		{RefreshInput{SchemaVersion, id, review.ExecutionPeers}, func(data []byte) error { _, err := DecodeRefreshInput(data); return err }},
		{CancelInput{SchemaVersion, id}, func(data []byte) error { _, err := DecodeCancelInput(data); return err }},
	}
	for _, tc := range inputs {
		good := groupJSON(t, tc.value)
		if tc.decode(good) != nil {
			t.Fatal("valid local input rejected")
		}
		for _, bad := range []string{
			string(good) + string(good),
			strings.Replace(string(good), `"schemaVersion":1`, `"SchemaVersion":1`, 1),
			strings.Replace(string(good), `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1),
			strings.Replace(string(good), `"schemaVersion":1`, `"schemaVersion":1,"origin":"invented"`, 1),
			strings.Replace(string(good), `"schemaVersion":1`, `"schemaVersion":1,"allowUnknown":true`, 1),
			"null", strings.Repeat(" ", MaxLocalInputBytes+1),
		} {
			if tc.decode([]byte(bad)) == nil {
				t.Fatal("expanded local command accepted")
			}
		}
	}
	apply := ApplyInput{SchemaVersion, id, review.Revision, review.ExecutionPeers, false}
	if apply.Validate() == nil {
		t.Fatal("apply without confirmation accepted")
	}
	apply.Confirm = true
	apply.ExecutionPeers = []string{}
	if apply.Validate() == nil {
		t.Fatal("empty apply admitted")
	}
	if (SelectInput{SchemaVersion, id, review.Revision, []string{}}).Validate() != nil {
		t.Fatal("empty non-executable review edit should be representable")
	}
	if (RefreshInput{SchemaVersion, id, []string{}}).Validate() == nil {
		t.Fatal("empty refresh accepted")
	}
	for _, peers := range [][]string{nil, {review.ExecutionPeers[0], review.ExecutionPeers[0]}, {"*"}, make([]string, MaxMembers+1)} {
		if (SelectInput{SchemaVersion, id, review.Revision, peers}).Validate() == nil {
			t.Fatal("ambiguous local peer set accepted")
		}
	}
}

func TestGroupLocalInputDecodeCopiesAndCanonicalizesSelection(t *testing.T) {
	review := groupReviewFixture(t, 2)
	input := ApplyInput{SchemaVersion, strings.Repeat("a", 32), review.Revision, []string{review.ExecutionPeers[1], review.ExecutionPeers[0]}, true}
	decoded, err := DecodeApplyInput(groupJSON(t, input))
	if err != nil || !reflect.DeepEqual(decoded.ExecutionPeers, review.ExecutionPeers) {
		t.Fatal("execution set was not canonicalized")
	}
	input.ExecutionPeers[0] = strings.Repeat("f", 64)
	if !reflect.DeepEqual(decoded.ExecutionPeers, review.ExecutionPeers) {
		t.Fatal("decoded input shares caller storage")
	}
	selection := groupSelectionFixture(t, 2)
	decodedPreview, err := DecodePreviewInput(groupJSON(t, PreviewInput{SchemaVersion: SchemaVersion, Selection: selection}))
	if err != nil {
		t.Fatal(err)
	}
	*selection.Template.Settings.TransferConcurrentPerPeer.Value = 29
	if *decodedPreview.Selection.Template.Settings.TransferConcurrentPerPeer.Value != 3 {
		t.Fatal("decoded preview shares choices")
	}
}

func TestGroupLocalPreparedViewRequiresExplicitStorageFlag(t *testing.T) {
	initializes := false
	view := PreparedView{SchemaVersion, strings.Repeat("a", 32), groupReviewFixture(t, 1), AdmissionPrepared, &initializes}
	data := groupJSON(t, view)
	decoded, err := DecodePreparedView(data)
	if err != nil || !reflect.DeepEqual(decoded, view) {
		t.Fatal("prepared view round trip")
	}
	for _, bad := range []string{
		strings.Replace(string(data), `,"initializesLocalEvidence":false`, "", 1),
		strings.Replace(string(data), `"initializesLocalEvidence":false`, `"initializesLocalEvidence":null`, 1),
		strings.Replace(string(data), `"admissionState":"prepared"`, `"admissionState":"approved"`, 1),
	} {
		if _, err := DecodePreparedView([]byte(bad)); err == nil {
			t.Fatal("missing or invented prepared authority accepted")
		}
	}
	view.Review, err = BuildReview(view.Review.Selection, view.Review.Rows, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if view.Validate() == nil {
		t.Fatal("empty execution view marked prepared")
	}
	view.AdmissionState = AdmissionUnavailable
	if view.Validate() != nil {
		t.Fatal("all-failed/unavailable review not viewable")
	}
}

func TestGroupLocalRunViewRequiresCompleteDerivedSummary(t *testing.T) {
	review, rows := groupPendingEvidence(t, 2)
	summary, err := ReduceReview(review, rows)
	if err != nil {
		t.Fatal(err)
	}
	view := RunView{SchemaVersion, strings.Repeat("a", 32), 1, review, EvidenceBody{SchemaVersion, rows}, summary, LocalDurable, ActivityApplying}
	decoded, err := DecodeRunView(groupJSON(t, view))
	if err != nil || !reflect.DeepEqual(view, decoded) {
		t.Fatal("run view round trip")
	}
	view.Summary.AllApplied = true
	if view.Validate() == nil {
		t.Fatal("fabricated all-applied summary accepted")
	}
	view.Summary = summary
	view.Evidence.Members = view.Evidence.Members[:1]
	if view.Validate() == nil {
		t.Fatal("omitted selected run member accepted")
	}
	decoded.Activity = "replaying"
	if decoded.Validate() == nil {
		t.Fatal("invented run activity accepted")
	}
}

func TestGroupLocalCancelReplyClosedUnion(t *testing.T) {
	flag := false
	prepared := PreparedView{SchemaVersion, strings.Repeat("a", 32), groupReviewFixture(t, 1), AdmissionCanceled, &flag}
	view := CancelView{SchemaVersion: SchemaVersion, Prepared: &prepared}
	if _, err := DecodeCancelView(groupJSON(t, view)); err != nil {
		t.Fatal(err)
	}
	prepared.AdmissionState = AdmissionPrepared
	if view.Validate() == nil {
		t.Fatal("cancel response retains prepared admission")
	}
	view.Prepared = nil
	if view.Validate() == nil {
		t.Fatal("empty cancel union accepted")
	}
	prepared.AdmissionState = AdmissionCanceled
	view.Prepared, view.Run = &prepared, &RunView{}
	if view.Validate() == nil {
		t.Fatal("expanded cancel union accepted")
	}
}

func TestGroupLocalSummaryRequiresZeroCountsAndFalseFlags(t *testing.T) {
	review, rows := groupPendingEvidence(t, 1)
	summary, err := ReduceReview(review, rows)
	if err != nil {
		t.Fatal(err)
	}
	view := RunView{SchemaVersion, strings.Repeat("a", 32), 1, review, EvidenceBody{SchemaVersion, rows}, summary, LocalDurable, ActivityApplying}
	cancel := CancelView{SchemaVersion: SchemaVersion, Run: &view}
	for _, field := range []string{"schemaVersion", "selected", "executable", "excluded", "reviewFailures", "notAttempted", "dispatching", "dispatchObserved", "dispatchUnknown", "applied", "failed", "canceled", "savedNotApplied", "targetUnknown", "targetUnobserved", "targetNonDurable", "localNonDurable", "statusFailures", "admissionFinished", "reconciliationRequired", "allApplied"} {
		if _, err := DecodeRunView(groupRemoveField(t, groupJSON(t, view), []string{"summary", field})); err == nil {
			t.Fatalf("omitted run summary field %s accepted", field)
		}
		if _, err := DecodeCancelView(groupRemoveField(t, groupJSON(t, cancel), []string{"run", "summary", field})); err == nil {
			t.Fatalf("omitted nested summary field %s accepted", field)
		}
	}
}

func TestGroupLocalViewsCanonicalizeEveryPeerArray(t *testing.T) {
	review, rows := groupPendingEvidence(t, 2)
	summary, err := ReduceReview(review, rows)
	if err != nil {
		t.Fatal(err)
	}
	view := RunView{SchemaVersion, strings.Repeat("a", 32), 1, review, EvidenceBody{SchemaVersion, rows}, summary, LocalDurable, ActivityApplying}
	view.Review.Rows[0], view.Review.Rows[1] = view.Review.Rows[1], view.Review.Rows[0]
	view.Review.ExecutionPeers[0], view.Review.ExecutionPeers[1] = view.Review.ExecutionPeers[1], view.Review.ExecutionPeers[0]
	view.Evidence.Members[0], view.Evidence.Members[1] = view.Evidence.Members[1], view.Evidence.Members[0]
	got, err := DecodeRunView(groupJSON(t, view))
	if err != nil {
		t.Fatal(err)
	}
	for i, member := range got.Review.Selection.Members {
		if got.Review.Rows[i].PeerKey != member.PeerKey || got.Evidence.Members[i].PeerKey != member.PeerKey || got.Review.ExecutionPeers[i] != member.PeerKey {
			t.Fatal("decoded arrays retain inconsistent input order")
		}
	}
	flag := false
	prepared := PreparedView{SchemaVersion, view.RunID, view.Review, AdmissionPrepared, &flag}
	decoded, err := DecodePreparedView(groupJSON(t, prepared))
	if err != nil || decoded.Review.Rows[0].PeerKey != decoded.Review.Selection.Members[0].PeerKey {
		t.Fatal("prepared view did not canonicalize")
	}
}
