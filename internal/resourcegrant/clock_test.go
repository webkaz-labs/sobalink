package resourcegrant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
)

func TestGrantCheckpointStrictPresenceAndBounds(t *testing.T) {
	e := fixtureEnvelope()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(data); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		strings.Replace(string(data), `"clock":{"seconds":100,"nanoseconds":0,"uncertain":false}`, `"clock":null`, 1),
		strings.Replace(string(data), `"nanoseconds":0,`, ``, 1),
		strings.Replace(string(data), `,"uncertain":false`, ``, 1),
		strings.Replace(string(data), `"seconds":100`, `"seconds":0`, 1),
		strings.Replace(string(data), `"nanoseconds":0`, `"nanoseconds":1000000000`, 1),
		strings.Replace(string(data), `"version":2`, `"version":1`, 1),
	} {
		if _, err := Decode([]byte(input)); err == nil {
			t.Fatal("ambiguous checkpoint accepted")
		}
	}
}

func TestGrantCheckpointBackwardTimeIsTerminal(t *testing.T) {
	w := WallCheckpoint{Seconds: 100, Nanoseconds: 20}
	next, err := w.Observe(time.Unix(100, 19))
	if err != nil || !next.Uncertain || next.Seconds != 100 || next.Nanoseconds != 20 {
		t.Fatal("rollback lost checkpoint or certainty")
	}
	later, err := next.Observe(time.Unix(110, 0))
	if err != nil || !later.Uncertain || later.Seconds != 110 {
		t.Fatal("later clock reset uncertainty")
	}
	if w.Uncertain {
		t.Fatal("pure observation mutated original")
	}
}

func TestGrantExpiryReductionPreservesOriginalLifetime(t *testing.T) {
	e := fixtureEnvelope()
	next, err := ObserveEnvelope(e, time.Unix(200, 0))
	if err != nil {
		t.Fatal(err)
	}
	got, old := next.Records[0], e.Records[0]
	if got.State != Expired || got.Revision != old.Revision+1 || *next.HighWater != got.Revision || got.ID != old.ID || got.IssuedAt != old.IssuedAt || got.ExpiresAt != old.ExpiresAt {
		t.Fatal("expiry changed original lifetime or lost reduction")
	}
	if old.State != Active || *e.HighWater != 1 {
		t.Fatal("observation mutated source")
	}
	again, err := ObserveEnvelope(next, time.Unix(250, 0))
	if err != nil || *again.HighWater != *next.HighWater {
		t.Fatal("terminal observation renewed revision")
	}
	e.HighWater = revision(uint64(capacity.MaxJSONInteger))
	e.Records[0].Revision = *e.HighWater
	if _, err := ObserveEnvelope(e, time.Unix(200, 0)); err == nil {
		t.Fatal("exhausted revision wrapped")
	}
}

func TestGrantMonotonicReductionKeepsAbsoluteExpiry(t *testing.T) {
	state := fixtureEnvelope()
	next, err := ExpireEnvelope(state)
	if err != nil {
		t.Fatal(err)
	}
	if next.Records[0].State != Expired || next.Records[0].ExpiresAt != state.Records[0].ExpiresAt || next.Clock.Seconds != state.Clock.Seconds || *next.HighWater != *state.HighWater+1 {
		t.Fatal("monotonic expiry changed absolute lifetime")
	}
	if state.Records[0].State != Active {
		t.Fatal("source mutated")
	}
}
