package operationjournal

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func fixtureBinding() CurrentBinding {
	return CurrentBinding{Scope: RemoteOperationScope{Target: resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: strings.Repeat("a", 32)}, Relationship: resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: strings.Repeat("b", 64), PeerKey: strings.Repeat("c", 64), PairBinding: strings.Repeat("d", 64)}, GrantID: strings.Repeat("e", 32), ScopeVersion: ScopeVersion}, AuthorizingRevision: uint64(capacity.MaxJSONInteger), IssuanceNonce: strings.Repeat("f", 32), ManagedGeneration: strings.Repeat("1", 64)}
}
func fixtureSettings() resource.Settings {
	return resource.Settings{TransferConcurrentFiles: capacity.Limited(capacity.MaxJSONInteger), TransferConcurrentPerPeer: capacity.Limited(capacity.MaxJSONInteger)}
}
func fixtureEmpty() EnvelopeV2 {
	high := uint64(0)
	return EnvelopeV2{FormatVersion, fixtureBinding().Scope.Target.ResourceID, &high, []TaggedRecord{}}
}
func fixtureRemote(t *testing.T, seq uint64, b CurrentBinding) TaggedRecord {
	t.Helper()
	id, err := operationID(b, seq)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRemoteIntent(b, seq, resourcegrant.ManagementApplyRequest{OperationID: id, BaseRevision: strings.Repeat("2", 64), ReviewRevision: strings.Repeat("3", 64), Settings: fixtureSettings()})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func fixtureLocal(seq uint64) TaggedRecord {
	b := fixtureBinding()
	request := resource.ApplyRequest{Target: b.Scope.Target, OperationID: resource.OperationID(b.Scope.Target.ResourceID, b.IssuanceNonce, seq), BaseRevision: strings.Repeat("2", 64), Revision: strings.Repeat("3", 64), Settings: fixtureSettings()}
	record := resource.Record{Request: request, Actor: resource.LocalActor, RequestHash: resource.RequestHash(request), Phase: "intent", Outcome: resource.UnknownOutcome()}
	return TaggedRecord{Kind: LocalKind, Local: &record}
}
func encoded(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func fixtureEnvelope(t *testing.T, records ...TaggedRecord) EnvelopeV2 {
	t.Helper()
	e := fixtureEmpty()
	e.Records = records
	if len(records) > 0 {
		*e.HighWater = records[len(records)-1].sequence()
	}
	if e.Validate() != nil {
		t.Fatal("invalid fixture")
	}
	return e
}
func fixtureOutcome(status, configuration, accounting, transfer string) resource.Outcome {
	return resource.Outcome{Status: status, Configuration: configuration, Accounting: accounting, Transfer: transfer}
}
func fixtureOutcomes() []resource.Outcome {
	result := []resource.Outcome{resource.UnknownOutcome(), fixtureOutcome("failed", "not_attempted", "not_attempted", "not_attempted"), fixtureOutcome("failed", "not_published", "not_attempted", "not_attempted"), fixtureOutcome("canceled", "not_attempted", "not_attempted", "not_attempted"), fixtureOutcome("saved_not_applied", "durable", "failed", "not_attempted"), fixtureOutcome("unknown", "uncertain", "failed", "not_attempted")}
	for _, a := range []string{"succeeded", "not_required"} {
		for _, tr := range []string{"succeeded", "not_required"} {
			result = append(result, fixtureOutcome("applied", "durable", a, tr), fixtureOutcome("unknown", "uncertain", a, tr))
		}
		result = append(result, fixtureOutcome("saved_not_applied", "durable", a, "failed"), fixtureOutcome("unknown", "uncertain", a, "failed"))
	}
	return result
}

func terminal(r TaggedRecord, outcome resource.Outcome) TaggedRecord {
	r = cloneTagged(r)
	if r.Local != nil {
		r.Local.Phase = "result"
		r.Local.Outcome = outcome
	} else {
		r.Remote.Phase = "result"
		r.Remote.Outcome = outcome
	}
	return r
}
func applied() resource.Outcome {
	return fixtureOutcome("applied", "durable", "succeeded", "succeeded")
}

func TestJournalCanonicalIdentityAndHistoricalHash(t *testing.T) {
	b := fixtureBinding()
	r := fixtureRemote(t, math.MaxUint64, b)
	// Independently spell the full canonical HMAC input. Do not marshal the
	// production input type: field-order/tag regressions must break this vector.
	canonical := `{"domain":"sobalink/resource-operation/remote-management/v1","scope":{"target":{"schemaVersion":1,"resourceId":"` + b.Scope.Target.ResourceID + `"},"relationship":{"backend":"directlan-managed","targetKey":"` + b.Scope.Relationship.TargetKey + `","peerKey":"` + b.Scope.Relationship.PeerKey + `","pairBinding":"` + b.Scope.Relationship.PairBinding + `"},"grantId":"` + b.Scope.GrantID + `","scopeVersion":1},"authorizingRevision":9007199254740991,"managedGeneration":"` + b.ManagedGeneration + `","sequence":18446744073709551615}`
	salt, _ := hex.DecodeString(b.IssuanceNonce)
	mac := hmac.New(sha256.New, salt)
	_, _ = mac.Write([]byte(canonical))
	if r.Remote.Request.OperationID != hex.EncodeToString(mac.Sum(nil)) || !resource.ValidDigest(r.Remote.Request.OperationID) {
		t.Fatal("canonical identifier drift")
	}
	scopeBytes := encoded(t, b.Scope)
	requestBytes := encoded(t, r.Remote.Request)
	hashInput := `{"domain":"sobalink/resource-operation/remote-request/v1","sequence":18446744073709551615,"scope":` + string(scopeBytes) + `,"authorizingRevision":9007199254740991,"issuanceNonce":"` + b.IssuanceNonce + `","managedGeneration":"` + b.ManagedGeneration + `","action":"apply","request":` + string(requestBytes) + `}`
	sum := sha256.Sum256([]byte(hashInput))
	if r.Remote.RequestHash != hex.EncodeToString(sum[:]) {
		t.Fatal("canonical replay hash drift")
	}
	mutations := []func(*RemoteOperationRecord){func(r *RemoteOperationRecord) { r.Sequence-- }, func(r *RemoteOperationRecord) { r.Scope.GrantID = strings.Repeat("9", 32) }, func(r *RemoteOperationRecord) { r.Scope.Relationship.PairBinding = strings.Repeat("9", 64) }, func(r *RemoteOperationRecord) { r.AuthorizingRevision-- }, func(r *RemoteOperationRecord) { r.IssuanceNonce = strings.Repeat("9", 32) }, func(r *RemoteOperationRecord) { r.ManagedGeneration = strings.Repeat("9", 64) }, func(r *RemoteOperationRecord) { r.Request.BaseRevision = strings.Repeat("9", 64) }, func(r *RemoteOperationRecord) { r.Request.ReviewRevision = strings.Repeat("9", 64) }, func(r *RemoteOperationRecord) { r.Request.Settings.TransferConcurrentFiles = capacity.Default() }}
	for i, mutate := range mutations {
		candidate := cloneTagged(r)
		mutate(candidate.Remote)
		if remoteRequestHash(*candidate.Remote) == r.Remote.RequestHash || candidate.Validate(b.Scope.Target.ResourceID, math.MaxUint64) == nil {
			t.Fatalf("immutable mutation %d accepted", i)
		}
	}
	for _, outcome := range fixtureOutcomes() {
		candidate := terminal(r, outcome)
		if candidate.Validate(b.Scope.Target.ResourceID, math.MaxUint64) != nil || remoteRequestHash(*candidate.Remote) != r.Remote.RequestHash {
			t.Fatal("terminal changed immutable binding")
		}
	}
}

func TestJournalFullTaggedRecordBounds(t *testing.T) {
	for _, record := range []TaggedRecord{fixtureLocal(math.MaxUint64), fixtureRemote(t, math.MaxUint64, fixtureBinding())} {
		maxSize := 0
		for _, outcome := range fixtureOutcomes() {
			r := terminal(record, outcome)
			data := encoded(t, r)
			if len(data) > maxSize {
				maxSize = len(data)
			}
			if len(data) > MaxRecordBytes || r.Validate(fixtureBinding().Scope.Target.ResourceID, math.MaxUint64) != nil {
				t.Fatalf("full tagged %s record exceeds bound: %d", r.Kind, len(data))
			}
			e := fixtureEnvelope(t, r)
			decoded, err := Decode(encoded(t, e))
			if err != nil || !reflect.DeepEqual(decoded.Journal, &e) {
				t.Fatal("maximal terminal round trip")
			}
		}
		t.Logf("maximum full tagged %s record: %d of %d bytes", record.Kind, maxSize, MaxRecordBytes)
	}
}

// Walk every existing JSON member including nested records/choices. These
// fixtures contain every non-optional field in both tagged union arms.
func fieldPaths(t *testing.T, data []byte, prefix []string) [][]string {
	t.Helper()
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) == nil && object != nil {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var paths [][]string
		for _, key := range keys {
			path := append(append([]string{}, prefix...), key)
			paths = append(paths, path)
			paths = append(paths, fieldPaths(t, object[key], path)...)
		}
		return paths
	}
	var array []json.RawMessage
	if json.Unmarshal(data, &array) == nil && array != nil {
		var paths [][]string
		for i, item := range array {
			paths = append(paths, fieldPaths(t, item, append(append([]string{}, prefix...), strconv.Itoa(i)))...)
		}
		return paths
	}
	return nil
}
func mutateJSON(t *testing.T, data []byte, path []string, kind string) []byte {
	t.Helper()
	var array []json.RawMessage
	if len(data) > 0 && data[0] == '[' {
		if json.Unmarshal(data, &array) != nil {
			t.Fatal("array mutation")
		}
		i, err := strconv.Atoi(path[0])
		if err != nil {
			t.Fatal(err)
		}
		array[i] = mutateJSON(t, array[i], path[1:], kind)
		return encoded(t, array)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil {
		t.Fatal("object mutation")
	}
	key := path[0]
	if len(path) > 1 {
		object[key] = mutateJSON(t, object[key], path[1:], kind)
		return encoded(t, object)
	}
	original := object[key]
	switch kind {
	case "null":
		object[key] = json.RawMessage("null")
	case "missing":
		delete(object, key)
	case "alias":
		delete(object, key)
		object[strings.ToUpper(key[:1])+key[1:]] = original
	case "unknown":
		object["unexpected"] = json.RawMessage("true")
	case "duplicate":
		rest := encoded(t, object)
		return append(append(append(append([]byte{'{'}, encoded(t, key)...), ':'), append(original, ',')...), rest[1:]...)
	default:
		t.Fatal("invalid mutation")
	}
	return encoded(t, object)
}
func TestJournalStrictTaggedVersionDispatch(t *testing.T) {
	legacy := LegacyEnvelope{resource.SchemaVersion, fixtureEmpty().ResourceID, new(uint64), []resource.Record{*fixtureLocal(1).Local}}
	*legacy.HighWater = 1
	fixtures := [][]byte{encoded(t, legacy), encoded(t, fixtureEnvelope(t, fixtureLocal(1), fixtureRemote(t, 2, fixtureBinding())))}
	for _, data := range fixtures {
		if _, err := Decode(data); err != nil {
			t.Fatal(err)
		}
		for _, path := range fieldPaths(t, data, nil) {
			for _, kind := range []string{"null", "missing", "alias", "unknown", "duplicate"} {
				if _, err := Decode(mutateJSON(t, data, path, kind)); err == nil {
					t.Fatalf("accepted %s at %v", kind, path)
				}
			}
		}
		for _, bad := range [][]byte{append(append([]byte{}, data...), data...), append(append([]byte{}, data...), []byte(" true")...), bytes.Repeat([]byte(" "), MaxBytes+1), []byte("[]"), []byte("null"), []byte("{\"schemaVersion\":99}"), nil} {
			if _, err := Decode(bad); err == nil {
				t.Fatal("malformed version dispatch accepted")
			}
		}
	}
	e := fixtureEnvelope(t, fixtureLocal(1))
	e.Records[0].Remote = fixtureRemote(t, 1, fixtureBinding()).Remote
	if _, err := Decode(encoded(t, e)); err == nil {
		t.Fatal("two tagged arms accepted")
	}
	e.Records[0].Local = nil
	e.Records[0].Kind = RemoteKind
	e.SchemaVersion = resource.SchemaVersion
	if _, err := Decode(encoded(t, e)); err == nil {
		t.Fatal("v1 error fell back to v2")
	}
	nested := `{"schemaVersion":2,"resourceId":"` + fixtureEmpty().ResourceID + `","highWater":0,"records":[` + strings.Repeat("[", 14) + "0" + strings.Repeat("]", 14) + "]}"
	if _, err := Decode([]byte(nested)); err == nil {
		t.Fatal("excess depth accepted")
	}
}

func TestJournalConversionPreservesLocalAndRejectsOverflow(t *testing.T) {
	first := terminal(fixtureLocal(4), applied())
	last := fixtureLocal(9)
	high := uint64(9)
	old := LegacyEnvelope{resource.SchemaVersion, fixtureEmpty().ResourceID, &high, []resource.Record{*first.Local, *last.Local}}
	before := encoded(t, old)
	next, err := ConvertV1(old)
	if err != nil || *next.HighWater != high || len(next.Records) != len(old.Records) {
		t.Fatal("conversion failed", err)
	}
	for i, record := range old.Records {
		if !reflect.DeepEqual(*next.Records[i].Local, record) || next.Records[i].Kind != LocalKind {
			t.Fatal("conversion changed local evidence")
		}
	}
	if !bytes.Equal(before, encoded(t, old)) {
		t.Fatal("conversion mutated input")
	}
	// Actual maximal valid v1 fits today's cap; a lower internal limit exercises
	// the fail-closed conversion branch without inventing an invalid v1 record.
	convertedBytes := encoded(t, next)
	if _, err := convertV1(old, len(convertedBytes)-1); err != ErrFull || !bytes.Equal(before, encoded(t, old)) {
		t.Fatal("conversion evicted or mutated on overflow")
	}
	*next.HighWater = 100
	*next.Records[0].Local.Request.Settings.TransferConcurrentFiles.Value = 1
	if !bytes.Equal(before, encoded(t, old)) {
		t.Fatal("converted candidate aliases input")
	}
	empty := LegacyEnvelope{resource.SchemaVersion, fixtureEmpty().ResourceID, new(uint64), []resource.Record{}}
	if got, err := ConvertV1(empty); err != nil || len(got.Records) != 0 || got.Records == nil {
		t.Fatal("empty conversion")
	}
}

func TestJournalBoundedScopedReplayAndCurrentToken(t *testing.T) {
	b := fixtureBinding()
	original := fixtureRemote(t, 2, b)
	e := fixtureEnvelope(t, terminal(fixtureLocal(1), applied()), terminal(original, applied()))
	later := b
	later.AuthorizingRevision--
	later.IssuanceNonce = strings.Repeat("8", 32)
	later.ManagedGeneration = strings.Repeat("7", 64)
	retained, fresh := MatchApply(e, later, original.Remote.Request)
	if !retained || fresh {
		t.Fatal("original historical binding overwritten by current selector")
	}
	conflicting := cloneApply(original.Remote.Request)
	conflicting.Settings.TransferConcurrentFiles = capacity.Default()
	if a, b := MatchApply(e, later, conflicting); a || b {
		t.Fatal("conflicting replay accepted")
	}
	currentID, err := CurrentRemoteID(e, later)
	if err != nil {
		t.Fatal(err)
	}
	request := cloneApply(original.Remote.Request)
	request.OperationID = currentID
	if a, b := MatchApply(e, later, request); a || !b {
		t.Fatal("exact current next token rejected")
	}
	variants := []CurrentBinding{b, later, later, later, later}
	variants[1].Scope.GrantID = strings.Repeat("9", 32)
	variants[2].Scope.Relationship.PairBinding = strings.Repeat("9", 64)
	variants[3].Scope.Relationship.PeerKey = strings.Repeat("9", 64)
	variants[4].Scope.Relationship.TargetKey = strings.Repeat("9", 64)
	for _, other := range variants {
		foreign, _ := operationID(other, 3)
		candidate := request
		candidate.OperationID = foreign
		if a, b := MatchApply(e, later, candidate); a || b {
			t.Fatal("old boot or other scope became fresh")
		}
	}
	for _, seq := range []uint64{1, 2, 4, math.MaxUint64} {
		id, _ := operationID(later, seq)
		candidate := request
		candidate.OperationID = id
		if a, b := MatchApply(e, later, candidate); a || b {
			t.Fatal("noncurrent sequence became executable")
		}
	}
	foreign := later.Scope
	foreign.GrantID = strings.Repeat("9", 32)
	for _, id := range []string{original.Remote.Request.OperationID, strings.Repeat("0", 64), e.Records[0].Local.Request.OperationID} {
		if _, ok := FindRemote(e, foreign, id); ok {
			t.Fatal("foreign/local history disclosed")
		}
	}
	copy, ok := FindRemote(e, b.Scope, original.Remote.Request.OperationID)
	if !ok {
		t.Fatal("retained absent")
	}
	*copy.Request.Settings.TransferConcurrentFiles.Value = 1
	if *e.Records[1].Remote.Request.Settings.TransferConcurrentFiles.Value != capacity.MaxJSONInteger {
		t.Fatal("lookup alias")
	}
	exhausted := fixtureEnvelope(t, fixtureRemote(t, math.MaxUint64, b))
	if _, err := CurrentRemoteID(exhausted, b); err != ErrExhausted {
		t.Fatal("sequence wrapped")
	}
	// At maximum retained capacity, lookup still finds the last scoped record.
	records := make([]TaggedRecord, MaxRecords)
	for i := range records {
		records[i] = fixtureRemote(t, uint64(i+1), b)
	}
	full := fixtureEnvelope(t, records...)
	if _, ok := FindRemote(full, b.Scope, records[MaxRecords-1].Remote.Request.OperationID); !ok {
		t.Fatal("bounded last-slot lookup")
	}
}

func TestJournalReservationEvictionAndPinnedUnknown(t *testing.T) {
	b := fixtureBinding()
	records := make([]TaggedRecord, MaxRecords)
	for i := range records {
		if i%2 == 0 {
			records[i] = fixtureLocal(uint64(i + 1))
		} else {
			records[i] = fixtureRemote(t, uint64(i+1), b)
		}
		records[i] = terminal(records[i], applied())
	}
	records[0] = terminal(records[0], resource.UnknownOutcome())
	records[1] = fixtureRemote(t, 2, b)
	e := fixtureEnvelope(t, records...)
	before := encoded(t, e)
	intent := fixtureRemote(t, MaxRecords+1, b)
	next, err := WithIntent(e, intent)
	if err != nil || len(next.Records) != MaxRecords || next.Records[0].sequence() != 1 || next.Records[1].sequence() != 2 || next.Records[2].sequence() != 4 {
		t.Fatal("did not evict oldest unpinned", err)
	}
	if _, ok := FindRemote(next, b.Scope, records[1].Remote.Request.OperationID); !ok {
		t.Fatal("pinned retained record lost")
	}
	oldScopeRecord := fixtureRemote(t, 3, b)
	if a, fresh := MatchApply(next, b, oldScopeRecord.Remote.Request); a || fresh {
		t.Fatal("absent older operation became executable")
	}
	data := encoded(t, next)
	if len(data)+MaxRecordBytes-len(encoded(t, intent)) > MaxBytes {
		t.Fatal("terminal reservation missing")
	}
	if !bytes.Equal(before, encoded(t, e)) {
		t.Fatal("intent changed original")
	}
	*intent.Remote.Request.Settings.TransferConcurrentFiles.Value = 1
	if *next.Records[MaxRecords-1].Remote.Request.Settings.TransferConcurrentFiles.Value != capacity.MaxJSONInteger {
		t.Fatal("admitted record aliases input")
	}
	for i := range e.Records {
		if i%2 == 0 {
			e.Records[i] = terminal(e.Records[i], resource.UnknownOutcome())
		} else {
			if e.Records[i].Remote != nil {
				e.Records[i].Remote.Phase = "intent"
				e.Records[i].Remote.Outcome = resource.UnknownOutcome()
			}
		}
	}
	before = encoded(t, e)
	if _, err := WithIntent(e, fixtureRemote(t, MaxRecords+1, b)); err != ErrFull || !bytes.Equal(before, encoded(t, e)) {
		t.Fatal("pinned evidence evicted or mutated")
	}
	empty := fixtureEmpty()
	if _, err := WithIntent(empty, fixtureRemote(t, 2, b)); err == nil {
		t.Fatal("sequence hole admitted")
	}
	if _, err := WithIntent(empty, terminal(fixtureRemote(t, 1, b), applied())); err == nil {
		t.Fatal("terminal admitted as intent")
	}
	exhausted := fixtureEnvelope(t, fixtureLocal(math.MaxUint64))
	if _, err := WithIntent(exhausted, fixtureLocal(1)); err != ErrExhausted {
		t.Fatal("intent sequence wrapped")
	}
}

func TestJournalCompletionAndNormalizationKeepBindings(t *testing.T) {
	for _, intent := range []TaggedRecord{fixtureLocal(2), fixtureRemote(t, 2, fixtureBinding())} {
		e := fixtureEnvelope(t, fixtureLocal(1), intent)
		before := encoded(t, e)
		for _, outcome := range fixtureOutcomes() {
			next, err := Complete(e, intent, outcome)
			if err != nil || *next.HighWater != 2 || !reflect.DeepEqual(next.Records[0], e.Records[0]) {
				t.Fatal("completion failed", err)
			}
			expected := terminal(intent, outcome)
			if !reflect.DeepEqual(next.Records[1], expected) {
				t.Fatal("completion replaced immutable fields")
			}
			if _, err := Complete(next, expected, applied()); err == nil {
				t.Fatal("terminal completion accepted")
			}
		}
		if !bytes.Equal(before, encoded(t, e)) {
			t.Fatal("completion mutated original")
		}
		wrong := cloneTagged(intent)
		if wrong.Local != nil {
			wrong.Local.Request.BaseRevision = strings.Repeat("8", 64)
			wrong.Local.RequestHash = resource.RequestHash(wrong.Local.Request)
		} else {
			wrong.Remote.Request.BaseRevision = strings.Repeat("8", 64)
			wrong.Remote.RequestHash = remoteRequestHash(*wrong.Remote)
		}
		if _, err := Complete(e, wrong, applied()); err == nil {
			t.Fatal("changed accepted intent completed")
		}
		normalized, err := NormalizeIntents(e)
		if err != nil {
			t.Fatal(err)
		}
		for i, record := range normalized.Records {
			if !reflect.DeepEqual(record, terminal(e.Records[i], resource.UnknownOutcome())) || !record.Pinned() {
				t.Fatal("normalization changed binding or unpinned UNKNOWN")
			}
		}
		again, err := NormalizeIntents(normalized)
		if err != nil || !reflect.DeepEqual(normalized, again) {
			t.Fatal("normalization not idempotent")
		}
		if !bytes.Equal(before, encoded(t, e)) {
			t.Fatal("normalization mutated input")
		}
	}
}

func TestJournalEnvelopeRejectsInvalidOrderingAndBindings(t *testing.T) {
	good := fixtureEnvelope(t, fixtureLocal(1), fixtureRemote(t, 2, fixtureBinding()))
	changes := []func(*EnvelopeV2){func(e *EnvelopeV2) { e.SchemaVersion = 1 }, func(e *EnvelopeV2) { e.HighWater = nil }, func(e *EnvelopeV2) { *e.HighWater = 3 }, func(e *EnvelopeV2) { e.Records = nil }, func(e *EnvelopeV2) { e.Records[0], e.Records[1] = e.Records[1], e.Records[0] }, func(e *EnvelopeV2) { e.Records[1].Remote.Scope.ScopeVersion = 2 }, func(e *EnvelopeV2) { e.Records[1].Remote.AuthorizingRevision = 0 }, func(e *EnvelopeV2) { e.Records[1].Remote.AuthorizingRevision = math.MaxUint64 }, func(e *EnvelopeV2) { e.Records[1].Remote.IssuanceNonce = strings.Repeat("F", 32) }, func(e *EnvelopeV2) { e.Records[1].Remote.ManagedGeneration = strings.Repeat("G", 64) }, func(e *EnvelopeV2) { e.Records[1].Remote.Phase = "pending" }, func(e *EnvelopeV2) { e.Records[1].Remote.Outcome = applied() }, func(e *EnvelopeV2) { e.Records[1].Kind = "other" }, func(e *EnvelopeV2) { e.Records[1].Remote = nil }, func(e *EnvelopeV2) { e.Records[0].Remote = fixtureRemote(t, 1, fixtureBinding()).Remote }, func(e *EnvelopeV2) { e.Records = append(e.Records, e.Records[1]) }}
	for i, change := range changes {
		candidate := good.clone()
		change(&candidate)
		if candidate.Validate() == nil {
			t.Fatalf("invalid mutation %d accepted", i)
		}
		if _, err := Encode(candidate); err == nil {
			t.Fatal("invalid encoded")
		}
	}
	for _, b := range []CurrentBinding{{}, func() CurrentBinding {
		b := fixtureBinding()
		b.Scope.Target.ResourceID = strings.Repeat("9", 32)
		return b
	}()} {
		if _, err := CurrentRemoteID(fixtureEmpty(), b); err == nil {
			t.Fatal("invalid/wrong resource fresh binding")
		}
	}
}
