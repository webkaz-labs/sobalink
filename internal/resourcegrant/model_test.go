package resourcegrant

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

func fixtureRecord() Record {
	return Record{
		ID: strings.Repeat("a", 32), Revision: 1,
		Target: resource.Target{SchemaVersion: 1, ResourceID: strings.Repeat("b", 32)}, ResourceType: resource.Type,
		Relationship: Relationship{Backend: Backend, TargetKey: strings.Repeat("c", 64), PeerKey: strings.Repeat("d", 64), PairBinding: strings.Repeat("e", 64)},
		Actions:      []string{Inspect}, Fields: []string{FilesField, PerPeerField}, IssuedAt: 100, ExpiresAt: 200, State: Active,
	}
}
func revision(n uint64) *uint64 { return &n }

func fixtureEnvelope() Envelope {
	r := fixtureRecord()
	return Envelope{Version: Version, Target: r.Target, HighWater: revision(1), Records: []Record{r}, Clock: &WallCheckpoint{Seconds: 100}}
}

func TestGrantRecordExactInspectScope(t *testing.T) {
	if fixtureRecord().Validate() != nil {
		t.Fatal("valid fixture rejected")
	}
	cases := []struct {
		name   string
		change func(*Record)
	}{
		{"write", func(r *Record) { r.Actions = []string{"apply"} }},
		{"extra action", func(r *Record) { r.Actions = []string{Inspect, "preview"} }},
		{"missing action", func(r *Record) { r.Actions = nil }},
		{"wildcard", func(r *Record) { r.Fields = []string{"*"} }},
		{"missing field", func(r *Record) { r.Fields = []string{FilesField} }},
		{"duplicate field", func(r *Record) { r.Fields = []string{FilesField, FilesField} }},
		{"order", func(r *Record) { r.Fields = []string{PerPeerField, FilesField} }},
		{"backend", func(r *Record) { r.Relationship.Backend = "tailnet" }},
		{"self", func(r *Record) { r.Relationship.PeerKey = r.Relationship.TargetKey }},
		{"unbound", func(r *Record) { r.Relationship.PairBinding = "" }},
		{"schema", func(r *Record) { r.Target.SchemaVersion++ }},
		{"type", func(r *Record) { r.ResourceType = "profile" }},
		{"zero revision", func(r *Record) { r.Revision = 0 }},
		{"revision overflow", func(r *Record) { r.Revision = uint64(capacity.MaxJSONInteger) + 1 }},
		{"no expiry", func(r *Record) { r.ExpiresAt = 0 }},
		{"expired at issuance", func(r *Record) { r.ExpiresAt = r.IssuedAt }},
		{"negative issuance", func(r *Record) { r.IssuedAt = -1 }},
		{"unsupported time", func(r *Record) { r.ExpiresAt = maxTimestamp + 1 }},
		{"unbounded duration", func(r *Record) { r.ExpiresAt = r.IssuedAt + capacity.MaxDurationSeconds + 1 }},
		{"unknown state", func(r *Record) { r.State = "enabled" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := fixtureRecord()
			tc.change(&r)
			if r.Validate() == nil {
				t.Fatal("invalid scope accepted")
			}
		})
	}
	r := fixtureRecord()
	r.State = Revoked
	if r.Validate() != nil {
		t.Fatal("tombstone rejected")
	}
}

func TestGrantEnvelopeBoundsAndHighWater(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Envelope)
	}{
		{"version", func(e *Envelope) { e.Version++ }},
		{"missing high water", func(e *Envelope) { e.HighWater = nil }},
		{"missing records", func(e *Envelope) { e.Records = nil; e.HighWater = revision(0) }},
		{"regressed high water", func(e *Envelope) { e.HighWater = revision(0) }},
		{"unrepresented high water", func(e *Envelope) { e.HighWater = revision(2) }},
		{"wrong resource", func(e *Envelope) { e.Target.ResourceID = strings.Repeat("f", 32) }},
		{"duplicate ID", func(e *Envelope) {
			r := e.Records[0]
			r.Revision = 2
			r.State = Revoked
			e.Records = append(e.Records, r)
			e.HighWater = revision(2)
		}},
		{"two active", func(e *Envelope) {
			r := fixtureRecord()
			r.ID = strings.Repeat("f", 32)
			r.Revision = 2
			e.Records = append(e.Records, r)
			e.HighWater = revision(2)
		}},
		{"too many", func(e *Envelope) { e.Records = make([]Record, MaxRecords+1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := fixtureEnvelope()
			tc.change(&e)
			if e.Validate() == nil {
				t.Fatal("invalid envelope accepted")
			}
		})
	}
	e := fixtureEnvelope()
	e.Records = []Record{}
	e.HighWater = revision(0)
	if e.Validate() != nil {
		t.Fatal("explicit empty envelope rejected")
	}
	e = fixtureEnvelope()
	e.Records[0].State = Revoked
	r := fixtureRecord()
	r.ID = strings.Repeat("f", 32)
	r.Revision = 2
	e.Records = append(e.Records, r)
	e.HighWater = revision(2)
	if e.Validate() != nil {
		t.Fatal("retained tombstone plus new grant rejected")
	}
}

func TestGrantStrictCodec(t *testing.T) {
	raw, err := json.Marshal(fixtureEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(raw); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		`{"version":2,"target":{"schemaVersion":1,"resourceId":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"records":[],"clock":{"seconds":100,"nanoseconds":0,"uncertain":false}}`,
		"null", "[]", "", string(raw) + "{}", strings.Repeat(" ", MaxBytes+1),
		strings.Replace(string(raw), `"version":2`, `"version":2,"version":2`, 1),
		strings.Replace(string(raw), `"version":2`, `"Version":2`, 1),
		strings.Replace(string(raw), `"version":2`, `"version":2,"actor":"local-control"`, 1),
		strings.Replace(string(raw), `"actions":["inspect"]`, `"actions":null`, 1),
		strings.Replace(string(raw), `"actions":["inspect"]`, `"actions":[null]`, 1),
		strings.Replace(string(raw), `"backend":"directlan-managed"`, `"backend":"directlan-managed","address":"127.0.0.1"`, 1),
	}
	for i, input := range bad {
		if _, err := Decode([]byte(input)); err == nil {
			t.Fatalf("bad input %d accepted", i)
		}
	}
}

func TestInspectCodecProjection(t *testing.T) {
	r := fixtureRecord()
	req := InspectRequest{ProtocolVersion: ProtocolVersion, Target: r.Target, GrantID: r.ID, GrantRevision: r.Revision}
	data, _ := json.Marshal(req)
	if _, err := DecodeInspectRequest(data); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{`"actor":"local-control"`, `"command":"resource.apply"`, `"pairBinding":"forged"`, `"operationId":"guessed"`} {
		injected := strings.TrimSuffix(string(data), "}") + "," + extra + "}"
		if _, err := DecodeInspectRequest([]byte(injected)); err == nil {
			t.Fatal("injected authority accepted")
		}
	}
	response := Inspection{ProtocolVersion: ProtocolVersion, Target: r.Target, Requested: resource.Settings{TransferConcurrentFiles: capacity.Default(), TransferConcurrentPerPeer: capacity.Limited(2)}, Effective: resource.Effective{TransferConcurrentFiles: 4, TransferConcurrentPerPeer: 2}}
	data, _ = json.Marshal(response)
	if _, err := DecodeInspection(data); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"authority", "provider", "revision", "operationId", "journal", "relationship", "peerKey", "grantId"} {
		if strings.Contains(string(data), `"`+name+`"`) {
			t.Fatalf("private field %s leaked", name)
		}
	}
	response.Effective.TransferConcurrentFiles = 0
	if response.Validate() == nil {
		t.Fatal("invalid effective value accepted")
	}
	response.Effective.TransferConcurrentFiles = 4
	response.Requested.TransferConcurrentFiles = capacity.Unlimited()
	if response.Validate() == nil {
		t.Fatal("unlimited resource accepted")
	}
}
