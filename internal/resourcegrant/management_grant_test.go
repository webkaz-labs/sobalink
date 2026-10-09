package resourcegrant

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func managementGrantFixture() ManagementRecord {
	r := fixtureRecord()
	r.Actions = []string{Inspect, PreviewAction, ApplyAction, StatusAction}
	return ManagementRecord{Scope: Management, Record: r}
}
func managementEnvelopeFixture() Envelope {
	m := managementGrantFixture()
	return Envelope{Version: ManagementVersion, Target: m.Record.Target, HighWater: revision(1), Records: []Record{}, ManagementRecords: []ManagementRecord{m}, Clock: &WallCheckpoint{Seconds: 100}}
}
func TestManagementGrantStrictSeparateScope(t *testing.T) {
	m := managementGrantFixture()
	if m.Validate() != nil || m.Record.Validate() == nil {
		t.Fatal("scope validators overlap")
	}
	if _, err := newDisclosureFence(m.Record, time.Unix(110, 0)); err == nil {
		t.Fatal("management minted legacy fence")
	}
	for _, mutate := range []func(*ManagementRecord){
		func(m *ManagementRecord) { m.Scope = "" },
		func(m *ManagementRecord) { m.Scope = "inspect" },
		func(m *ManagementRecord) { m.Record.Actions = []string{Inspect} },
		func(m *ManagementRecord) {
			m.Record.Actions = []string{Inspect, ApplyAction, PreviewAction, StatusAction}
		},
		func(m *ManagementRecord) { m.Record.Fields = []string{FilesField} },
		func(m *ManagementRecord) { m.Record.Fields = []string{"*"} },
	} {
		changed := managementGrantFixture()
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatal("invalid management scope accepted")
		}
	}
}
func TestManagementGrantStrictVersionAndLegacyCodecs(t *testing.T) {
	legacy, _ := json.Marshal(fixtureEnvelope())
	for _, field := range []string{`,"managementRecords":null`, `,"managementRecords":[]`} {
		data := append(append([]byte{}, legacy[:len(legacy)-1]...), []byte(field+"}")...)
		if _, err := Decode(data); err == nil {
			t.Fatal("v2 new arm accepted")
		}
	}
	if e, err := Decode(legacy); err != nil || e.Version != Version || e.ManagementRecords != nil {
		t.Fatal("legacy changed")
	}
	current, _ := json.Marshal(managementEnvelopeFixture())
	for _, data := range [][]byte{[]byte(strings.Replace(string(current), `"version":3`, `"version":2`, 1)), []byte(strings.Replace(string(current), `"scope":"management"`, `"scope":""`, 1)), []byte(strings.Replace(string(current), `"scope":"management",`, "", 1)), []byte(strings.Replace(string(current), `"scope":"management"`, `"scope":"management","scope":"management"`, 1))} {
		if _, err := Decode(data); err == nil {
			t.Fatal("malformed tagged arm accepted")
		}
	}
	if _, err := Decode(current); err != nil {
		t.Fatal(err)
	}
	// Legacy review still rejects fields which never belonged to its Record.
	r := GrantReview{Grant: fixtureRecord(), InitializesState: false}
	data, _ := json.Marshal(r)
	data = []byte(strings.Replace(string(data), `"id":`, `"scope":"","id":`, 1))
	var decoded GrantReview
	if json.Unmarshal(data, &decoded) == nil {
		t.Fatal("legacy review allowlist expanded")
	}
}
func TestManagementGrantCombinedEnvelopeBounds(t *testing.T) {
	for _, mutate := range []func(*Envelope){
		func(e *Envelope) { e.ManagementRecords = nil },
		func(e *Envelope) { r := fixtureRecord(); r.State = Revoked; e.Records = []Record{r} },
		func(e *Envelope) {
			r := fixtureRecord()
			r.ID = strings.Repeat("f", 32)
			r.State = Revoked
			e.Records = []Record{r}
		},
		func(e *Envelope) {
			r := fixtureRecord()
			r.ID = strings.Repeat("f", 32)
			r.Revision = 2
			e.Records = []Record{r}
			e.HighWater = revision(2)
		},
		func(e *Envelope) { e.HighWater = revision(2) },
		func(e *Envelope) { e.ManagementRecords[0].Record.Target.ResourceID = strings.Repeat("f", 32) },
	} {
		e := managementEnvelopeFixture()
		mutate(&e)
		if e.Validate() == nil {
			t.Fatal("combined invariant accepted invalid state")
		}
	}
	e := managementEnvelopeFixture()
	e.ManagementRecords[0].Record.Revision = 2
	e.HighWater = revision(2)
	r := fixtureRecord()
	r.ID = strings.Repeat("f", 32)
	r.State = Revoked
	e.Records = []Record{r}
	if e.Validate() != nil {
		t.Fatal("ordered cross-arm history rejected")
	}
	for i := 0; i < MaxRecords; i++ {
		r.ID = strings.Repeat("0", 30) + string("abcdef0123456789"[i]) + "f"
		r.Revision = uint64(i + 3)
		e.Records = append(e.Records, r)
		e.HighWater = revision(r.Revision)
	}
	if e.Validate() == nil {
		t.Fatal("combined record cap exceeded")
	}
}
func TestManagementGrantLifetimeReductionPreservesHistory(t *testing.T) {
	e := managementEnvelopeFixture()
	m := e.ManagementRecords[0]
	next, err := ObserveEnvelope(e, time.Unix(200, 0))
	if err != nil || next.ManagementRecords[0].Record.State != Expired || *next.HighWater != 2 || next.ManagementRecords[0].Record.IssuedAt != m.Record.IssuedAt || next.ManagementRecords[0].Record.ExpiresAt != m.Record.ExpiresAt || !reflect.DeepEqual(e.ManagementRecords[0], m) {
		t.Fatal("expiry extended, mutated source or lost shared revision")
	}
	uncertain, err := ObserveEnvelope(e, time.Unix(90, 0))
	if err != nil || !uncertain.Clock.Uncertain {
		t.Fatal("rollback not terminal")
	}
	later, err := ObserveEnvelope(uncertain, time.Unix(120, 0))
	if err != nil || !later.Clock.Uncertain {
		t.Fatal("later wall time cleared uncertainty")
	}
}
