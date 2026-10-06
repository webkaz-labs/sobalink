package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
)

func TestRelayResourceCLIReviewsOnlyExplicitEdits(t *testing.T) {
	for _, ja := range []bool{false, true} {
		current := capacity.Defaults()
		current.Resources["workerRequests"] = capacity.Limited(256)
		queries := 0
		applied := false
		query := func(name string, payload any, out any) error {
			queries++
			var value any
			switch name {
			case "policy.config":
				value = map[string]any{"requested": current, "effective": current, "relayResourceEditable": true}
			case "policy.preview":
				p := payload.(map[string]any)["policy"].(capacity.Policy)
				if p.Number("resources", "workerRequests") != 256 || p.Number("resources", "relayTLSConnections") != 128 {
					t.Fatal("unrelated setting changed or choice lost")
				}
				value = map[string]any{"revision": "reviewed"}
			default:
				t.Fatal("unexpected query", name)
			}
			b, _ := json.Marshal(value)
			return json.Unmarshal(b, out)
		}
		request := func(name string, payload any) error {
			if name != "policy.apply" || payload.(map[string]any)["expectedRevision"] != "reviewed" {
				t.Fatal("unreviewed budget apply")
			}
			applied = true
			return nil
		}
		if err := lanResourcesCommand([]string{"set", "--tls-connections", "128", "--admission-connections", "32"}, ja, false, io.Discard, query, request); err != nil || queries != 2 || !applied {
			t.Fatal(err, queries, applied)
		}
		var out bytes.Buffer
		if err := lanResourcesCommand([]string{"set", "--presence-connections", "8"}, ja, true, &out, func(string, any, any) error { t.Fatal("dry run queried"); return nil }, func(string, any) error { t.Fatal("dry run applied"); return nil }); err != nil || !strings.Contains(out.String(), `"applied":false`) {
			t.Fatal(err, out.String())
		}
	}
}
func TestRelayResourceCLIRejectsInvalidAndShowsEffectiveValues(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, args := range [][]string{{"set"}, {"set", "--tls-connections", "unlimited"}, {"set", "--admission-connections", "0"}, {"set", "--candidate-attempts", "65536"}, {"set", "--presence-connections", "-1"}} {
			if err := lanResourcesCommand(args, ja, true, io.Discard, nil, nil); err == nil {
				t.Fatal("invalid resource accepted", args)
			}
		}
		for _, structured := range []bool{false, true} {
			var out bytes.Buffer
			args := []string{"show"}
			if structured {
				args = append(args, "--json")
			}
			err := lanResourcesCommand(args, ja, false, &out, func(name string, _ any, target any) error {
				if name != "policy.config" {
					t.Fatal(name)
				}
				v := map[string]any{"requested": capacity.Defaults(), "effective": capacity.Defaults(), "relayResourceEditable": true}
				b, _ := json.Marshal(v)
				return json.Unmarshal(b, target)
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if structured {
				var result map[string]any
				if json.Unmarshal(out.Bytes(), &result) != nil || result["restartRequired"] != true {
					t.Fatal("machine contract changed")
				}
			} else if ja && !strings.Contains(out.String(), "次回") {
				t.Fatal("missing Japanese next-start guidance")
			}
		}
	}
}
func TestRouteCLIAllowsMoreThanFourExactIDs(t *testing.T) {
	var ids []string
	for _, v := range []string{"a", "b", "c", "d", "e", "f"} {
		ids = append(ids, strings.Repeat(v, 64))
	}
	got, err := routeIDs(strings.Join(ids, ","))
	if err != nil || len(got) != 6 {
		t.Fatal("CLI retained four-ID ceiling", err)
	}
}
