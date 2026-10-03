package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/messageframe"
)

func TestMessageJSONInputAcceptsMaximumEscapedText(t *testing.T) {
	encoded, err := json.Marshal(map[string]string{"peerId": "fixture-peer", "text": strings.Repeat("&", messageframe.TextBytes)})
	if err != nil || len(encoded) <= 48<<10 {
		t.Fatal("fixture must exercise the larger valid message envelope")
	}
	path := filepath.Join(t.TempDir(), "message.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"message.send", "--stdin"}, {"message.send", "--json-file", path}, {"message.send", string(encoded)}} {
		got, err := commandPayload(context.Background(), args, strings.NewReader(string(encoded)), false)
		if err != nil || string(got) != string(encoded) {
			t.Fatalf("valid message envelope was rejected or changed: %v", err)
		}
	}
	if _, err := commandPayload(context.Background(), []string{"lan.join", "--stdin"}, strings.NewReader(string(encoded)), false); err == nil {
		t.Fatal("message-specific allowance widened private invitation input")
	}
	oversized := strings.Repeat(" ", messageframe.CommandBytes) + "{}"
	if _, err := commandPayload(context.Background(), []string{"message.send", "--stdin"}, strings.NewReader(oversized), false); err == nil {
		t.Fatal("message input lost its finite wire bound")
	}
}
