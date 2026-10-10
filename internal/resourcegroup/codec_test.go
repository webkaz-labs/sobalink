package resourcegroup

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func TestGroupStrictSelectionCodec(t *testing.T) {
	good := string(groupJSON(t, groupSelectionFixture(t, 2)))
	bad := []string{
		"", "null", "[]", good + good,
		strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":2`, 1),
		strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1),
		strings.Replace(good, `"schemaVersion":1`, `"SchemaVersion":1`, 1),
		strings.Replace(good, `"members":[`, `"members":null,"unused":[`, 1),
		strings.Replace(good, `"peerKey":`, `"override":null,"peerKey":`, 1),
		strings.Replace(good, `"peerKey":`, `"override":{},"peerKey":`, 1),
		strings.Replace(good, `"template":`, `"overrides":[],"template":`, 1),
		strings.Replace(good, `"template":`, `"command":"apply","template":`, 1),
		strings.Replace(good, `"settings":{`, `"settings":{"otherField":{"mode":"default"},`, 1),
		strings.Replace(good, `"settings":{`, `"settings":{"transferConcurrentFiles":{"mode":"default"},`, 1),
		strings.Replace(good, `"mode":"default"`, `"mode":"default","mode":"default"`, 1),
		strings.Replace(good, `"mode":"default"`, `"Mode":"default"`, 1),
		strings.Replace(good, `"mode":"default"`, `"mode":"unlimited"`, 1),
		strings.Replace(good, `"value":3`, `"value":null`, 1),
		strings.Replace(good, `"value":3`, `"value":0`, 1),
		strings.Replace(good, `"value":3`, `"value":-1`, 1),
		strings.Replace(good, `"value":3`, `"value":1.5`, 1),
		strings.Replace(good, `"value":3`, `"value":NaN`, 1),
		strings.Replace(good, `"value":3`, `"value":9007199254740992`, 1),
		strings.Replace(good, `"value":3`, `"value":9223372036854775808`, 1),
		strings.Repeat(" ", MaxSelectionBytes+1),
		`{"schemaVersion":1,"template":` + strings.Repeat(`{"settings":`, 14) + `{}` + strings.Repeat("}", 14) + `}`,
	}
	for i, input := range bad {
		if got, err := DecodeSelection([]byte(input)); err == nil || !reflect.DeepEqual(got, Selection{}) {
			t.Fatalf("invalid selection JSON %d accepted", i)
		}
	}
	for _, path := range [][]string{
		{"schemaVersion"}, {"template"}, {"members"},
		{"template", "schemaVersion"}, {"template", "revision"}, {"template", "settings"},
		{"template", "settings", "transferConcurrentFiles"}, {"template", "settings", "transferConcurrentPerPeer"},
	} {
		input := groupRemoveField(t, []byte(good), path)
		if _, err := DecodeSelection(input); err == nil {
			t.Fatalf("missing selection field %v accepted", path)
		}
	}
}

// Test-only malformed input construction; production DTOs have no map payload.
func groupRemoveField(t *testing.T, data []byte, path []string) []byte {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	if len(path) == 1 {
		delete(object, path[0])
	} else {
		object[path[0]] = groupRemoveField(t, object[path[0]], path[1:])
	}
	return groupJSON(t, object)
}

func TestGroupStrictReviewAndEvidenceCodecs(t *testing.T) {
	r, rows := groupPendingEvidence(t, 2)
	rows[0] = groupObserve(t, rows[0], groupAppliedOutcome(), true)
	e := EvidenceBody{SchemaVersion: SchemaVersion, Members: rows}
	for _, tc := range []struct {
		name   string
		data   []byte
		limit  int
		decode func([]byte) error
	}{
		{"review", groupJSON(t, r), MaxReviewBytes, func(data []byte) error { _, err := DecodeReview(data); return err }},
		{"evidence", groupJSON(t, e), MaxEvidenceBytes, func(data []byte) error { _, err := DecodeEvidence(data); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			good := string(tc.data)
			for _, bad := range []string{
				good + ` {}`, `null`, `[]`,
				strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":9`, 1),
				strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1),
				strings.Replace(good, `"schemaVersion":1`, `"SchemaVersion":1`, 1),
				strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":1,"confirm":true`, 1),
				strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":1,"origin":"invented"`, 1),
				strings.Repeat(" ", tc.limit+1),
			} {
				if tc.decode([]byte(bad)) == nil {
					t.Fatal("expanded or malformed JSON accepted")
				}
			}
		})
	}
	for _, path := range [][]string{{"revision"}, {"selection"}, {"rows"}, {"executionPeers"}} {
		if _, err := DecodeReview(groupRemoveField(t, groupJSON(t, r), path)); err == nil {
			t.Fatalf("missing review field %v accepted", path)
		}
	}
	good := string(groupJSON(t, e))
	for _, bad := range []string{
		strings.Replace(good, `,"evidenceDurable":true`, "", 1),
		strings.Replace(good, `"evidenceDurable":true`, `"evidenceDurable":null`, 1),
		strings.Replace(good, `"dispatch":"observed"`, `"dispatch":"offline"`, 1),
		strings.Replace(good, `"localDurability":"durable"`, `"localDurability":"unknown"`, 1),
		strings.Replace(good, `"admissionStop":"none"`, `"admissionStop":"rolled_back"`, 1),
		strings.Replace(good, `"request":{`, `"request":{"confirm":true,`, 1),
		strings.Replace(good, `"target":{`, `"target":{"provider":"invented",`, 1),
	} {
		if got, err := DecodeEvidence([]byte(bad)); err == nil || !reflect.DeepEqual(got, EvidenceBody{}) {
			t.Fatal("expanded or missing evidence accepted")
		}
	}
}

func TestGroupCodecRoundTripsDoNotRestoreAuthority(t *testing.T) {
	s := groupSelectionFixture(t, 2)
	decodedSelection, err := DecodeSelection(groupJSON(t, s))
	if err != nil || !reflect.DeepEqual(s, decodedSelection) {
		t.Fatal("selection round trip changed intent")
	}
	r, rows := groupPendingEvidence(t, 2)
	decodedReview, err := DecodeReview(groupJSON(t, r))
	if err != nil || !reflect.DeepEqual(r, decodedReview) {
		t.Fatal("review round trip changed exact values")
	}
	rows[0] = groupObserve(t, rows[0], resource.UnknownOutcome(), false)
	e := EvidenceBody{SchemaVersion: SchemaVersion, Members: rows}
	decodedEvidence, err := DecodeEvidence(groupJSON(t, e))
	if err != nil || !reflect.DeepEqual(e, decodedEvidence) || ValidateEvidence(r, decodedEvidence.Members) != nil {
		t.Fatal("evidence round trip changed false durability or frozen request")
	}
	for _, data := range [][]byte{groupJSON(t, s), groupJSON(t, r), groupJSON(t, e)} {
		for _, field := range []string{"confirm", "reviewId", "origin", "pairBinding", "authority", "command", "endpoint", "path"} {
			if bytes.Contains(data, []byte(`"`+field+`"`)) {
				t.Fatalf("unexpected authority/private field %s", field)
			}
		}
	}
}

func TestGroupMaximumBodiesBoundTerminalGrowth(t *testing.T) {
	s := groupSelectionFixture(t, MaxMembers)
	maxSettings := resource.Settings{TransferConcurrentFiles: capacity.Limited(capacity.MaxJSONInteger), TransferConcurrentPerPeer: capacity.Limited(capacity.MaxJSONInteger)}
	var err error
	s.Template, err = NewTemplate(maxSettings)
	if err != nil {
		t.Fatal(err)
	}
	for i := range s.Members {
		s.Members[i].Selector.GrantRevision = uint64(capacity.MaxJSONInteger)
		files, peer := capacity.Limited(capacity.MaxJSONInteger), capacity.Limited(capacity.MaxJSONInteger)
		s.Members[i].Override = &Override{TransferConcurrentFiles: &files, TransferConcurrentPerPeer: &peer}
	}
	resolved, err := ResolveSelection(s)
	if err != nil {
		t.Fatal(err)
	}
	previews := groupRowsFixture(resolved)
	execution := make([]string, MaxMembers)
	for i := range previews {
		previews[i].Reply.Preview.Effective = resource.Effective{TransferConcurrentFiles: capacity.MaxJSONInteger, TransferConcurrentPerPeer: capacity.MaxJSONInteger}
		execution[i] = previews[i].PeerKey
	}
	r, err := BuildReview(resolved, previews, execution)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := NewEvidence(r)
	if err != nil {
		t.Fatal(err)
	}
	outcome := resource.Outcome{Status: "saved_not_applied", Configuration: "durable", Accounting: "not_required", Transfer: "failed"}
	for i := range rows {
		rows[i] = groupObserve(t, rows[i], outcome, false)
		rows[i].AdmissionStop = StopPersistenceUncertain
		rows[i], err = ObserveStatus(rows[i], groupOperationReply(rows[i], resourcegrant.StatusAction, outcome, false), uint64(capacity.MaxJSONInteger), MaxObservationTime, LocalUncertain)
		if err != nil {
			t.Fatal(err)
		}
	}
	e := EvidenceBody{SchemaVersion: SchemaVersion, Members: rows}
	if len(groupJSON(t, s)) > MaxSelectionBytes || len(groupJSON(t, r)) > MaxReviewBytes || len(groupJSON(t, e)) > MaxEvidenceBytes || ValidateEvidence(r, rows) != nil {
		t.Fatal("maximum member shape exceeds a pure DTO bound")
	}
	// This checks only the pure review+evidence shape. The future owned store
	// must additionally reserve its origin binding and envelope overhead, enforce
	// aggregate retention and test all-pinned capacity before dispatch is wired.
	combined := groupJSON(t, struct {
		Review   ReviewBody   `json:"review"`
		Evidence EvidenceBody `json:"evidence"`
	}{r, e})
	if len(combined) > 64*1024 {
		t.Fatal("pure maximum body pair exceeds proposed per-run budget")
	}
	if _, err := DecodeReview(groupJSON(t, r)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEvidence(groupJSON(t, e)); err != nil {
		t.Fatal(err)
	}
	if _, err := Reduce(append(rows, rows[0])); err == nil {
		t.Fatal("evidence member overflow accepted")
	}
}
