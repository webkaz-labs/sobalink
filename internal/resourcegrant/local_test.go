package resourcegrant

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLocalGrantReviewRequiresExplicitInitialization(t *testing.T) {
	review := GrantReview{Grant: fixtureRecord(), BaseRevision: strings.Repeat("a", 64), ReviewRevision: strings.Repeat("b", 64), InitializesState: false}
	data, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	var decoded GrantReview
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.InitializesState {
		t.Fatal("explicit false rejected")
	}
	missing := strings.Replace(string(data), `,"initializesState":false`, "", 1)
	if missing == string(data) {
		t.Fatal("fixture field was not removed")
	}
	if err := json.Unmarshal([]byte(missing), &decoded); err == nil {
		t.Fatal("missing false decision accepted")
	}
	var confirmation GrantConfirmation
	if err := json.Unmarshal([]byte(`{"review":`+missing+`,"confirm":true}`), &confirmation); err == nil {
		t.Fatal("nested confirmation inferred missing decision")
	}
}
