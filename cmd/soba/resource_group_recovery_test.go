package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestResourceGroupCLICurrentReviewOneReadAndStrictArms(t *testing.T) {
	prepared := resourceGroupCLITestPrepared(t, resourceGroupCLITestSelection(t, 1))
	for _, present := range []bool{false, true} {
		for _, ja := range []bool{false, true} {
			for _, machine := range []bool{false, true} {
				response := map[string]any{"schemaVersion": 1, "state": "none"}
				if present {
					response["state"], response["prepared"] = "current", prepared
				}
				calls := 0
				client := func(_ context.Context, dir, raw string, target any) error {
					calls++
					var cmd webui.Command
					if dir != "synthetic-profile" || json.Unmarshal([]byte(raw), &cmd) != nil || cmd.Name != resourcegroup.LocalCurrentReviewCommand || cmd.RequestID == "" {
						t.Fatal("unexpected command identity")
					}
					if _, err := resourcegroup.DecodeCurrentReviewInput(cmd.Payload); err != nil {
						t.Fatal(err)
					}
					return json.Unmarshal(resourceCollectionTestJSON(t, response), target)
				}
				args := []string{"review", "current"}
				if machine {
					args = append(args, "--json")
				}
				var out bytes.Buffer
				if err := resourceGroupCLI(t.Context(), args, "synthetic-profile", ja, false, &out, client); err != nil || calls != 1 {
					t.Fatalf("one explicit read: calls=%d err=%v", calls, err)
				}
				if machine {
					if _, err := resourcegroup.DecodeCurrentReviewView(out.Bytes()); err != nil {
						t.Fatal(err)
					}
				} else if present && !strings.Contains(out.String(), prepared.ReviewID) {
					t.Fatal("recovered identity omitted")
				} else if !present && !strings.Contains(out.String(), text(ja, "does not establish", "分かりません")) {
					t.Fatal("none overclaimed nonexecution")
				}
			}
		}
	}
}

func TestResourceGroupCLICurrentReviewDryRunAndInvalidAreZeroCall(t *testing.T) {
	noCall := func(context.Context, string, string, any) error { t.Fatal("unexpected command"); return nil }
	var out bytes.Buffer
	if err := resourceGroupCLI(t.Context(), []string{"review", "current", "--json"}, "synthetic-profile", false, true, &out, noCall); err != nil {
		t.Fatal(err)
	}
	var dry struct {
		Command string          `json:"command"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(out.Bytes(), &dry) != nil || dry.Command != resourcegroup.LocalCurrentReviewCommand {
		t.Fatal("dry-run command")
	}
	if _, err := resourcegroup.DecodeCurrentReviewInput(dry.Payload); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"review"}, {"review", "list"}, {"review", "current", "--confirm"}, {"review", "current", "--review-id", strings.Repeat("a", 32)}, {"review", "current", "extra"}} {
		out.Reset()
		if err := resourceGroupCLI(t.Context(), args, "synthetic-profile", false, false, &out, noCall); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	for _, ja := range []bool{false, true} {
		out.Reset()
		if err := resourceGroupCLI(t.Context(), []string{"--help"}, "synthetic-profile", ja, false, &out, noCall); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "review current") || !strings.Contains(out.String(), text(ja, "stale unused in-memory", "古いメモリ内")) {
			t.Fatal("reduction help omitted")
		}
	}
}

func TestResourceGroupCLICurrentReviewRejectsMalformedReplyWithoutRetry(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"schemaVersion":1,"state":"none","prepared":null}`, `{"schemaVersion":1,"state":"current"}`, `{"schemaVersion":1,"state":"none","runs":[]}`} {
		calls := 0
		client := func(_ context.Context, _, _ string, target any) error {
			calls++
			return json.Unmarshal([]byte(raw), target)
		}
		var out bytes.Buffer
		if err := resourceGroupCLI(t.Context(), []string{"review", "current", "--json"}, "synthetic-profile", false, false, &out, client); err == nil || calls != 1 || out.Len() != 0 {
			t.Fatal("malformed reply accepted or retried")
		}
	}
}
