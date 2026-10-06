package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestCLIReadAndWriteIDsAreIndependentOfClockResolution(t *testing.T) {
	seen := make(map[string]bool)
	client := func(ctx context.Context, dir, raw string, target any) error {
		var command webui.Command
		if err := json.Unmarshal([]byte(raw), &command); err != nil {
			return err
		}
		prefix := "cli-"
		if command.Name == "mixed.status" {
			prefix = "cli-read-"
		}
		if !strings.HasPrefix(command.RequestID, prefix) || len(strings.TrimPrefix(command.RequestID, prefix)) < 26 {
			t.Errorf("request identifier lacks independent random suffix: %q", command.RequestID)
		}
		if seen[command.RequestID] {
			t.Errorf("request identifier was reused")
		}
		seen[command.RequestID] = true
		data := []byte(`{"configured":false}`)
		return json.Unmarshal(data, target)
	}
	for range 256 {
		for _, args := range [][]string{{"--locale", "en", "mixed", "show", "--json"}, {"--locale", "en", "lan", "wan", "disable"}} {
			var out bytes.Buffer
			if err := runWith(context.Background(), args, &out, strings.NewReader(""), client); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(seen) != 512 {
		t.Fatalf("got %d distinct commands, want512", len(seen))
	}
}
