package resource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
)

func operationRequestFixture() ApplyRequest {
	id, nonce := strings.Repeat("a", 32), strings.Repeat("b", 32)
	return ApplyRequest{Target: Target{SchemaVersion, id}, OperationID: OperationID(id, nonce, math.MaxUint64), BaseRevision: strings.Repeat("c", 64), Revision: strings.Repeat("d", 64), Settings: Settings{capacity.Limited(capacity.MaxJSONInteger), capacity.Limited(capacity.MaxJSONInteger)}}
}
func operationRecordFixture() Record {
	request := operationRequestFixture()
	return Record{request, LocalActor, RequestHash(request), "result", Outcome{"applied", "durable", "succeeded", "succeeded"}}
}
func operationJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Mutate one object member at an arbitrary depth, keeping every other field
// intact. RawMessage preserves duplicate keys for the decoder's ambiguity tests.
func mutateOperationJSON(t *testing.T, data []byte, path []string, kind string) []byte {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	key := path[0]
	if len(path) > 1 {
		object[key] = mutateOperationJSON(t, object[key], path[1:], kind)
		return operationJSON(t, object)
	}
	original := object[key]
	if original == nil {
		t.Fatal("missing mutation target", key)
	}
	switch kind {
	case "null":
		object[key] = json.RawMessage(`null`)
	case "missing":
		delete(object, key)
	case "case":
		delete(object, key)
		object[strings.ToUpper(key[:1])+key[1:]] = original
	case "unknown":
		object["unexpectedField"] = json.RawMessage(`true`)
	case "duplicate":
		encoded := operationJSON(t, object)
		field := operationJSON(t, key)
		return append(append(append(append([]byte{'{'}, field...), ':'), append(original, ',')...), encoded[1:]...)
	default:
		t.Fatal("unknown mutation")
	}
	return operationJSON(t, object)
}
func operationFieldPaths(t *testing.T, data []byte, prefix []string) [][]string {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	var paths [][]string
	for key, value := range object {
		path := append(append([]string(nil), prefix...), key)
		paths = append(paths, path)
		if len(value) > 0 && value[0] == '{' {
			paths = append(paths, operationFieldPaths(t, value, path)...)
		}
	}
	return paths
}

func TestOperationStrictApplyAndRecordJSON(t *testing.T) {
	for _, kind := range []string{"apply", "record"} {
		t.Run(kind, func(t *testing.T) {
			request, record := operationRequestFixture(), operationRecordFixture()
			data := operationJSON(t, request)
			valid := func(data []byte) bool {
				var out ApplyRequest
				return Decode(data, 4096, &out) == nil && out.Validate() == nil
			}
			if kind == "record" {
				data = operationJSON(t, record)
				valid = func(data []byte) bool {
					var out Record
					return Decode(data, 4096, &out) == nil && out.Validate(request.ResourceID, math.MaxUint64) == nil
				}
			}
			if !valid(data) {
				t.Fatal("valid contract rejected")
			}
			for _, path := range operationFieldPaths(t, data, nil) {
				for _, mutation := range []string{"case", "duplicate", "null", "unknown", "missing"} {
					t.Run(strings.Join(path, "/")+"/"+mutation, func(t *testing.T) {
						if valid(mutateOperationJSON(t, data, path, mutation)) {
							t.Fatal("ambiguous or incomplete contract accepted")
						}
					})
				}
			}
			for _, bad := range [][]byte{nil, []byte("null"), []byte("[]"), append(append([]byte(nil), data...), []byte(" {}")...)} {
				if valid(bad) {
					t.Fatal("invalid envelope accepted")
				}
			}
		})
	}
}

func TestOperationIDsAndRequestValidation(t *testing.T) {
	request := operationRequestFixture()
	id, nonce := request.ResourceID, strings.Repeat("b", 32)
	for _, seq := range []uint64{1, math.MaxUint64 - 1, math.MaxUint64} {
		t.Run(strconv.FormatUint(seq, 10), func(t *testing.T) {
			text := OperationID(id, nonce, seq)
			gotID, gotNonce, gotSeq, err := ParseOperationID(text)
			if err != nil || gotID != id || gotNonce != nonce || gotSeq != seq {
				t.Fatal("operation ID did not round trip", err)
			}
		})
	}
	for _, decimal := range []string{"", "0", "00", "01", "+1", "-1", "1.0", "1e1", " 1", "1 ", "18446744073709551616", "１", "0x1"} {
		t.Run("decimal/"+decimal, func(t *testing.T) {
			if _, _, _, err := ParseOperationID(id + ":" + nonce + ":" + decimal); err == nil {
				t.Fatal("malformed decimal accepted")
			}
		})
	}
	for _, bad := range []string{id, id + ":" + nonce, id + ":" + nonce + ":1:2", strings.ToUpper(id) + ":" + nonce + ":1", id + ":" + strings.ToUpper(nonce) + ":1", id + ":short:1", strings.Repeat("z", 32) + ":" + nonce + ":1"} {
		if _, _, _, err := ParseOperationID(bad); err == nil {
			t.Fatal("malformed ID accepted")
		}
	}
	mutations := map[string]func(*ApplyRequest){
		"target-scope":      func(r *ApplyRequest) { r.ResourceID = strings.Repeat("e", 32) },
		"schema":            func(r *ApplyRequest) { r.SchemaVersion++ },
		"base-revision":     func(r *ApplyRequest) { r.BaseRevision = strings.Repeat("A", 64) },
		"revision":          func(r *ApplyRequest) { r.Revision = "short" },
		"setting-unlimited": func(r *ApplyRequest) { r.Settings.TransferConcurrentFiles = capacity.Unlimited() },
		"setting-too-large": func(r *ApplyRequest) {
			r.Settings.TransferConcurrentPerPeer = capacity.Limited(capacity.MaxJSONInteger + 1)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			r := request
			mutate(&r)
			if r.Validate() == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

func TestOperationRequestHashCanonicalActorAndInputs(t *testing.T) {
	request := operationRequestFixture()
	canonical := []byte(`{"actor":"local-control","action":"apply","request":` + string(operationJSON(t, request)) + `}`)
	sum := sha256.Sum256(canonical)
	want := hex.EncodeToString(sum[:])
	if LocalActor != "local-control" || RequestHash(request) != want || !ValidDigest(want) {
		t.Fatal("canonical actor/action hash changed")
	}
	for name, mutate := range map[string]func(*ApplyRequest){
		"files":         func(r *ApplyRequest) { r.Settings.TransferConcurrentFiles = capacity.Default() },
		"per-peer":      func(r *ApplyRequest) { r.Settings.TransferConcurrentPerPeer = capacity.Limited(1) },
		"base-revision": func(r *ApplyRequest) { r.BaseRevision = strings.Repeat("e", 64) },
		"revision":      func(r *ApplyRequest) { r.Revision = strings.Repeat("f", 64) },
		"nonce": func(r *ApplyRequest) {
			r.OperationID = OperationID(r.ResourceID, strings.Repeat("f", 32), math.MaxUint64)
		},
		"sequence": func(r *ApplyRequest) { r.OperationID = OperationID(r.ResourceID, strings.Repeat("b", 32), 1) },
		"scope": func(r *ApplyRequest) {
			r.ResourceID = strings.Repeat("f", 32)
			r.OperationID = OperationID(r.ResourceID, strings.Repeat("b", 32), math.MaxUint64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := request
			mutate(&r)
			if r.Validate() != nil || RequestHash(r) == want {
				t.Fatal("hash did not bind changed input")
			}
		})
	}
	var reordered map[string]json.RawMessage
	if err := json.Unmarshal(operationJSON(t, request), &reordered); err != nil {
		t.Fatal(err)
	}
	var roundtrip ApplyRequest
	if err := Decode(operationJSON(t, reordered), 4096, &roundtrip); err != nil || RequestHash(roundtrip) != want {
		t.Fatal("field order changed hash", err)
	}
}

func validOperationOutcomes() []Outcome {
	outcomes := []Outcome{UnknownOutcome(), {"failed", "not_attempted", "not_attempted", "not_attempted"}, {"failed", "not_published", "not_attempted", "not_attempted"}, {"canceled", "not_attempted", "not_attempted", "not_attempted"}, {"saved_not_applied", "durable", "failed", "not_attempted"}, {"unknown", "uncertain", "failed", "not_attempted"}}
	for _, accounting := range []string{"succeeded", "not_required"} {
		for _, transfer := range []string{"succeeded", "not_required"} {
			outcomes = append(outcomes, Outcome{"applied", "durable", accounting, transfer}, Outcome{"unknown", "uncertain", accounting, transfer})
		}
		outcomes = append(outcomes, Outcome{"saved_not_applied", "durable", accounting, "failed"}, Outcome{"unknown", "uncertain", accounting, "failed"})
	}
	return outcomes
}

func TestOperationOutcomeMatrixAndRecordBound(t *testing.T) {
	valid := map[Outcome]bool{}
	for _, outcome := range validOperationOutcomes() {
		valid[outcome] = true
	}
	if len(valid) != 18 {
		t.Fatal("unexpected frozen outcome count", len(valid))
	}
	statuses := []string{"applied", "failed", "canceled", "saved_not_applied", "unknown", "", "other"}
	configurations := []string{"durable", "not_attempted", "not_published", "uncertain", "unobserved", "", "other"}
	stages := []string{"succeeded", "not_required", "failed", "not_attempted", "unobserved", "", "other"}
	combinations, maxBytes := 0, 0
	for _, status := range statuses {
		for _, configuration := range configurations {
			for _, accounting := range stages {
				for _, transfer := range stages {
					outcome := Outcome{status, configuration, accounting, transfer}
					combinations++
					if (outcome.Validate() == nil) != valid[outcome] {
						t.Fatalf("outcome contract mismatch: %+v", outcome)
					}
					if !valid[outcome] {
						continue
					}
					record := operationRecordFixture()
					record.Outcome = outcome
					if err := record.Validate(record.Request.ResourceID, math.MaxUint64); err != nil {
						t.Fatal("valid result rejected", err)
					}
					encoded := operationJSON(t, record)
					maxBytes = max(maxBytes, len(encoded))
					if len(encoded) > 2048 {
						t.Fatal("serialized record exceeds reserved bound", len(encoded))
					}
					var decoded Record
					if err := Decode(encoded, 2048, &decoded); err != nil || !reflect.DeepEqual(decoded, record) {
						t.Fatal("record round trip failed", err)
					}
				}
			}
		}
	}
	t.Logf("validated %d enum combinations, %d valid outcomes; maximum compact record %d bytes (bound 2048)", combinations, len(valid), maxBytes)
}

func TestOperationRecordPhaseIdentityAndHash(t *testing.T) {
	record := operationRecordFixture()
	for _, phase := range []string{"intent", "result", "", "other"} {
		for _, outcome := range validOperationOutcomes() {
			t.Run(fmt.Sprintf("%s/%s/%s/%s/%s", phase, outcome.Status, outcome.Configuration, outcome.Accounting, outcome.Transfer), func(t *testing.T) {
				r := record
				r.Phase = phase
				r.Outcome = outcome
				valid := phase == "result" || phase == "intent" && outcome == UnknownOutcome()
				if (r.Validate(r.Request.ResourceID, math.MaxUint64) == nil) != valid {
					t.Fatal("phase/outcome mismatch")
				}
				if valid && r.Pinned() != (phase == "intent" || outcome.Status == "unknown") {
					t.Fatal("pinning contract mismatch")
				}
			})
		}
	}
	for name, mutate := range map[string]func(*Record){
		"actor":   func(r *Record) { r.Actor = "other-actor" },
		"hash":    func(r *Record) { r.RequestHash = strings.Repeat("f", 64) },
		"request": func(r *Record) { r.Request.Settings.TransferConcurrentFiles = capacity.Default() },
	} {
		t.Run(name, func(t *testing.T) {
			r := record
			mutate(&r)
			if r.Validate(r.Request.ResourceID, math.MaxUint64) == nil {
				t.Fatal("altered record accepted")
			}
		})
	}
	if record.Validate(record.Request.ResourceID, math.MaxUint64-1) == nil || record.Validate(strings.Repeat("e", 32), math.MaxUint64) == nil {
		t.Fatal("record escaped journal scope or high water")
	}
}
