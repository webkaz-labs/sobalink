package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"strings"
	"testing"
)

func TestNearbyReenrollRequiresExactFreshConsentBeforeCore(t *testing.T) {
	key := strings.Repeat("a", 64)
	for _, args := range [][]string{{"reenroll", "--peer-key", key, "--generation", "1"}, {"reenroll", "--peer-key", key, "--consent"}, {"reenroll", "--peer-key", "name", "--generation", "1", "--consent"}, {"reenroll", "--peer-key", key, "--generation", "9007199254740992", "--consent"}} {
		var out bytes.Buffer
		if err := runWith(context.Background(), append([]string{"--dry-run", "nearby"}, args...), &out, nil, nearbyNoCalls(t)); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	var out bytes.Buffer
	if err := runWith(context.Background(), []string{"--dry-run", "nearby", "reenroll", "--peer-key", key, "--generation", "3", "--consent"}, &out, nil, nearbyNoCalls(t)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"expectedGeneration":3`) || !strings.Contains(out.String(), `"applied":false`) {
		t.Fatal(out.String())
	}
}
func TestNearbyReenrollWizardEOFNeverRestores(t *testing.T) {
	key := strings.Repeat("a", 64)
	for _, input := range []string{"", "1\n", "1\nn\n"} {
		var out bytes.Buffer
		calls := 0
		c := &nearbyCLI{ctx: context.Background(), out: &out, in: strings.NewReader(input), dir: "/synthetic/profile", client: func(_ context.Context, _ string, raw string, result any) error {
			calls++
			var command webui.Command
			json.Unmarshal([]byte(raw), &command)
			if command.Name != "setup.revoked" {
				t.Fatal("unexpected mutation", command.Name)
			}
			data, _ := json.Marshal(nearbyRevokedList{Kind: "sobalink-setup-revoked", Version: 1, Records: []nearbyRevokedRecord{{key, 3, "full-sha256"}}})
			return json.Unmarshal(data, result)
		}}
		if err := c.reenrollWizard(newPrompts(c.in, c.out)); err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("calls %d", calls)
		}
	}
}
func TestNearbyReenrollRejectsMissingOrFalseSuccessEvidence(t *testing.T) {
	key := strings.Repeat("a", 64)
	for _, mutation := range []string{"generation", "peerKey", "paired", "trusted", "missing"} {
		var out bytes.Buffer
		client := func(_ context.Context, _ string, raw string, result any) error {
			fields := map[string]any{"kind": "sobalink-setup-reenroll", "version": 1, "peerKey": key, "generation": 4, "verification": "full-sha256", "paired": false, "trusted": false}
			switch mutation {
			case "generation":
				fields["generation"] = 3
			case "peerKey":
				fields["peerKey"] = strings.Repeat("b", 64)
			case "paired":
				fields["paired"] = true
			case "trusted":
				fields["trusted"] = true
			case "missing":
				delete(fields, "paired")
			}
			data, _ := json.Marshal(fields)
			return json.Unmarshal(data, result)
		}
		if err := runWith(context.Background(), []string{"nearby", "reenroll", "--peer-key", key, "--generation", "3", "--consent"}, &out, nil, client); err == nil {
			t.Fatal("accepted", mutation)
		}
	}
}
