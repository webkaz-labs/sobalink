package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRouteCLIInputAndExplicitApproval(t *testing.T) {
	peer := strings.Repeat("a", 64)
	id := strings.Repeat("b", 64)
	raw := `{"sender":"synthetic","box":"private-fixture"}`
	envelope, _ := json.Marshal(map[string]string{"update": raw})
	queries, requests := 0, 0
	var sent map[string]any
	expiry := time.Now().UTC().Add(time.Hour)
	query := func(name string, payload any, out any) error {
		queries++
		if name != "lan.routes.inspect" {
			t.Fatal(name)
		}
		b, _ := json.Marshal(map[string]any{"digest": "reviewed", "expires": expiry, "candidates": []map[string]string{{"candidateId": id}}})
		return json.Unmarshal(b, out)
	}
	request := func(name string, payload any) error {
		requests++
		if name != "lan.routes.apply" {
			t.Fatal(name)
		}
		sent = payload.(map[string]any)
		return nil
	}
	err := lanRoutesCommand(context.Background(), []string{"approve", peer, "--stdin", "--candidates", id}, false, false, io.Discard, bytes.NewReader(envelope), query, request)
	if err != nil || queries != 1 || requests != 1 {
		t.Fatal(err, queries, requests)
	}
	if sent["digest"] != "reviewed" || sent["update"] != raw || !sent["expires"].(time.Time).Equal(expiry) {
		t.Fatal("exact review or expiry not preserved")
	}
}
func TestRouteCLIRejectsImplicitGrantAndArgumentSecrets(t *testing.T) {
	peer := strings.Repeat("a", 64)
	calls := 0
	request := func(string, any) error { calls++; return nil }
	for _, args := range [][]string{{"approve", peer, "--stdin"}, {"inspect", peer, `{"box":"private"}`}, {"approve", peer, "--current", "--candidates", "bad"}, {"add", "--relay", "192.0.2.20:443", "--certificate", strings.Repeat("a", 64), "--scope", "local"}} {
		if err := lanRoutesCommand(context.Background(), args, false, false, io.Discard, strings.NewReader(`{"box":"private"}`), nil, request); err == nil {
			t.Fatal("bad input accepted", args[0])
		}
	}
	if calls != 0 {
		t.Fatal("invalid CLI mutated state")
	}
	if _, err := commandPayload(context.Background(), []string{"lan.routes.apply", `{"update":"private"}`}, strings.NewReader(""), false); err == nil {
		t.Fatal("generic route secret argument accepted")
	}
	preview := previewPayload("lan.routes.apply", json.RawMessage(`{"peerId":"synthetic","update":"private-fixture"}`))
	if strings.Contains(string(preview), "private-fixture") {
		t.Fatal("private update in dry-run output")
	}
}
func TestRouteCLIDryRunNeverQueriesAndRevocationIsExplicit(t *testing.T) {
	peer, id := strings.Repeat("a", 64), strings.Repeat("b", 64)
	query := func(string, any, any) error { t.Fatal("dry-run queried server"); return nil }
	var name string
	var payload any
	request := func(n string, p any) error { name, payload = n, p; return nil }
	if err := lanRoutesCommand(context.Background(), []string{"approve", peer, "--current", "--candidates", id}, true, true, io.Discard, strings.NewReader(""), query, request); err != nil {
		t.Fatal(err)
	}
	if name != "lan.routes.approve" {
		t.Fatal(name)
	}
	if err := lanRoutesCommand(context.Background(), []string{"revoke", peer}, false, false, io.Discard, nil, nil, request); err != nil {
		t.Fatal(err)
	}
	if name != "lan.routes.revoke" || payload.(map[string]any)["peerId"] != peer {
		t.Fatal("revocation changed peer")
	}
}
func TestRouteCLIBilingualHelpAndInputChoices(t *testing.T) {
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		if err := lanRoutesCommand(context.Background(), []string{"--help"}, ja, false, &out, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "--json-file") || !strings.Contains(out.String(), "--candidates") {
			t.Fatal("missing equivalent workflow")
		}
	}
	for _, tc := range []struct {
		path string
		pipe bool
	}{{"", false}, {"both", true}} {
		if _, err := readRouteInput(context.Background(), tc.path, tc.pipe, strings.NewReader("{}"), false); err == nil {
			t.Fatal("ambiguous private input")
		}
	}
}

func TestRouteCLIExplicitEmptyWithdrawal(t *testing.T) {
	peer := strings.Repeat("a", 64)
	calls := 0
	var sent map[string]any
	query := func(_ string, _ any, out any) error {
		b, _ := json.Marshal(map[string]any{"digest": "empty-proof", "expires": time.Now().Add(time.Hour), "candidates": []any{}})
		return json.Unmarshal(b, out)
	}
	request := func(name string, payload any) error {
		calls++
		sent = payload.(map[string]any)
		if name != "lan.routes.apply" && name != "lan.routes.export" {
			t.Fatal(name)
		}
		return nil
	}
	if err := lanRoutesCommand(context.Background(), []string{"approve", peer, "--stdin", "--withdrawal"}, false, false, io.Discard, strings.NewReader(`{"box":"synthetic"}`), query, request); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(sent["candidateIds"].([]string)) != 0 || !sent["expires"].(time.Time).IsZero() {
		t.Fatal("withdrawal granted permission")
	}
	if err := lanRoutesCommand(context.Background(), []string{"withdraw", peer}, false, false, io.Discard, nil, nil, request); err != nil {
		t.Fatal(err)
	}
	if sent["withdraw"] != true {
		t.Fatal("withdrawal export not explicit")
	}
	query = func(_ string, _ any, out any) error {
		b, _ := json.Marshal(map[string]any{"digest": "not-empty", "expires": time.Now().Add(time.Hour), "candidates": []map[string]string{{"candidateId": strings.Repeat("b", 64)}}})
		return json.Unmarshal(b, out)
	}
	before := calls
	if err := lanRoutesCommand(context.Background(), []string{"approve", peer, "--stdin", "--withdrawal"}, false, false, io.Discard, strings.NewReader(`{"box":"synthetic"}`), query, request); err == nil || calls != before {
		t.Fatal("nonemptyoffer accepted as withdrawal")
	}
}
