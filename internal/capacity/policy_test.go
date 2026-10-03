package capacity

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestChoicesRejectAmbiguity(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"mode":""}`, `{"mode":"limited"}`, `{"mode":"limited","value":0}`, `{"mode":"limited","value":-1}`, `{"mode":"limited","value":1.5}`, `{"mode":"limited","value":null}`, `{"mode":"limited","value":9007199254740992}`, `{"mode":"default","value":1}`, `{"mode":"unlimited","value":null}`, `{"mode":"unlimited","value":1}`, `{"mode":"default","mode":"unlimited"}`, `{"mode":"default","other":1}`} {
		var choice Choice
		if err := json.Unmarshal([]byte(raw), &choice); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`{"mode":"default"}`, `{"mode":"limited","value":1}`, `{"mode":"unlimited"}`} {
		var choice Choice
		if err := json.Unmarshal([]byte(raw), &choice); err != nil {
			t.Errorf("rejected %s: %v", raw, err)
		}
	}
}

func TestPolicySeparatesLogicalAndResourceLimits(t *testing.T) {
	p := Defaults()
	p.Logical["savedServices"] = Unlimited()
	p.Resources["materializedListeners"] = Limited(1024)
	r, err := p.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if r.Number("logical", "savedServices") != math.MaxInt64 || r.Number("resources", "materializedListeners") != 1024 || r.Number("logical", "messageBytes") != 16<<10 {
		t.Fatal("incorrect effective limits")
	}
	p.Resources["materializedListeners"] = Unlimited()
	if p.Validate() == nil {
		t.Fatal("unbounded physical resource admitted")
	}
	delete(p.Resources, "materializedListeners")
	p.Logical["unknown"] = Limited(1)
	if p.Validate() == nil {
		t.Fatal("unknown policy accepted")
	}
}

func TestDurationUsesRepresentationBoundary(t *testing.T) {
	if d, err := Duration(7 * 24 * 60 * 60); err != nil || d != 7*24*time.Hour {
		t.Fatal("custom duration rejected")
	}
	if _, err := Duration(MaxDurationSeconds); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int64{0, -1, MaxDurationSeconds + 1, math.MaxInt64} {
		if _, err := Duration(n); err == nil {
			t.Fatalf("accepted %d", n)
		}
	}
}

func TestPathChoicesExposeDefaultsAndAllowUnlimited(t *testing.T) {
	for key, definition := range map[string]Definition{"pathDepth": {16, "levels"}, "pathBytes": {4096, "bytes"}} {
		if Catalog()["logical"][key] != definition || Defaults().Number("logical", key) != definition.Default {
			t.Fatalf("incorrect %s catalog/default", key)
		}
		for _, choice := range []Choice{Limited(definition.Default + 1), Unlimited()} {
			p := Defaults()
			p.Logical[key] = choice
			data, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Policy
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Number("logical", key) != p.Number("logical", key) {
				t.Fatalf("%s did not round-trip", key)
			}
		}
	}
}
