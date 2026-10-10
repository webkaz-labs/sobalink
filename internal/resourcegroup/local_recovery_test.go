package resourcegroup

import (
	"reflect"
	"strings"
	"testing"
)

func currentReviewFixture(t *testing.T) CurrentReviewView {
	t.Helper()
	firstUse := false
	prepared := &PreparedView{SchemaVersion: SchemaVersion, ReviewID: strings.Repeat("a", 32), Review: groupReviewFixture(t, 2), AdmissionState: AdmissionPrepared, InitializesLocalEvidence: &firstUse}
	return CurrentReviewView{SchemaVersion: SchemaVersion, State: CurrentReviewCurrent, Prepared: prepared}
}

func TestGroupCurrentReviewInputClosedSchema(t *testing.T) {
	if _, err := DecodeCurrentReviewInput([]byte(`{"schemaVersion":1}`)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{`{}`, `null`, `[]`, `{"schemaVersion":0}`, `{"schemaVersion":null}`, `{"SchemaVersion":1}`, `{"schemaVersion":1,"schemaVersion":1}`, `{"schemaVersion":1,"confirm":true}`, `{"schemaVersion":1,"reviewId":"x"}`, `{"schemaVersion":1}{}`, strings.Repeat(" ", MaxLocalInputBytes+1)} {
		if _, err := DecodeCurrentReviewInput([]byte(invalid)); err == nil {
			t.Fatal("expanded current-review input accepted")
		}
	}
}

func TestGroupCurrentReviewViewClosedUnion(t *testing.T) {
	current := currentReviewFixture(t)
	none := CurrentReviewView{SchemaVersion: SchemaVersion, State: CurrentReviewNone}
	for _, good := range []CurrentReviewView{current, none} {
		decoded, err := DecodeCurrentReviewView(groupJSON(t, good))
		if err != nil || !reflect.DeepEqual(decoded, good) {
			t.Fatal("valid current-review result rejected", err)
		}
	}
	for _, mutate := range []func(*CurrentReviewView){
		func(v *CurrentReviewView) { v.SchemaVersion = 0 },
		func(v *CurrentReviewView) { v.State = "active" },
		func(v *CurrentReviewView) { v.State = CurrentReviewNone },
		func(v *CurrentReviewView) { v.Prepared = nil },
		func(v *CurrentReviewView) { v.Prepared.AdmissionState = AdmissionCanceled },
	} {
		bad := currentReviewFixture(t)
		mutate(&bad)
		if _, err := DecodeCurrentReviewView(groupJSON(t, bad)); err == nil {
			t.Fatal("expanded union or canceled admission accepted")
		}
	}
	current.Prepared.AdmissionState = AdmissionUnavailable
	if _, err := DecodeCurrentReviewView(groupJSON(t, current)); err != nil {
		t.Fatal("non-executable current view rejected", err)
	}
}

func TestGroupCurrentReviewViewRequiresCompleteNestedShape(t *testing.T) {
	good := string(groupJSON(t, currentReviewFixture(t)))
	for _, invalid := range []string{
		`{"schemaVersion":1}`, `{"schemaVersion":1,"state":"none","prepared":null}`,
		`{"schemaVersion":1,"state":"none","run":{}}`, good + good,
		strings.Replace(good, `"state":"current"`, `"state":"current","state":"current"`, 1),
		strings.Replace(good, `"initializesLocalEvidence":false`, `"initializesLocalEvidence":null`, 1),
		strings.Replace(good, `,"initializesLocalEvidence":false`, "", 1),
		strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":1,"origin":"invented"`, 1),
	} {
		if _, err := DecodeCurrentReviewView([]byte(invalid)); err == nil {
			t.Fatal("incomplete/expanded current view accepted")
		}
	}
}

func TestGroupCurrentReviewViewCanonicalIndependentCopies(t *testing.T) {
	view := currentReviewFixture(t)
	review := &view.Prepared.Review
	review.Rows[0], review.Rows[1] = review.Rows[1], review.Rows[0]
	review.Selection.Members[0], review.Selection.Members[1] = review.Selection.Members[1], review.Selection.Members[0]
	review.ExecutionPeers[0], review.ExecutionPeers[1] = review.ExecutionPeers[1], review.ExecutionPeers[0]
	decoded, err := DecodeCurrentReviewView(groupJSON(t, view))
	if err != nil || decoded.Prepared.Review.Rows[0].PeerKey >= decoded.Prepared.Review.Rows[1].PeerKey {
		t.Fatal("current view was not canonical", err)
	}
	*decoded.Prepared.InitializesLocalEvidence = true
	*decoded.Prepared.Review.Selection.Template.Settings.TransferConcurrentPerPeer.Value = 7
	if *view.Prepared.InitializesLocalEvidence || *view.Prepared.Review.Selection.Template.Settings.TransferConcurrentPerPeer.Value != 3 {
		t.Fatal("decoded current view retained nested aliases")
	}
}

func TestGroupCurrentReviewViewByteBound(t *testing.T) {
	data := groupJSON(t, currentReviewFixture(t))
	oversized := append(data, []byte(strings.Repeat(" ", MaxLocalResponseBytes))...)
	if _, err := DecodeCurrentReviewView(oversized); err == nil {
		t.Fatal("oversized recovery response accepted")
	}
}
