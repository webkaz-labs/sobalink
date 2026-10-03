package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/messageframe"
)

func TestCommandPayloadUsesSelectedFiniteBudgets(t *testing.T) {
	dir := t.TempDir()
	policy := capacity.Defaults()
	policy.Logical["messageBytes"] = capacity.Unlimited()
	policy.Resources["messageTextBytes"] = capacity.Limited(128 << 10)
	policy.Resources["profileBytes"] = capacity.Limited(8 << 20)
	if err := config.WriteJSON(filepath.Join(dir, "capacity.json"), policy); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"peerId": "peer", "text": strings.Repeat("&", 128<<10)})
	if len(payload) <= messageframe.CommandBytes {
		t.Fatal("fixture is not larger than legacy envelope")
	}
	path := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"message.send", string(payload)}, {"message.send", "--stdin"}, {"message.send", "--json-file", path}, {"profile.import.preview", "--stdin"}, {"service.save", "--stdin"}} {
		got, err := commandPayload(t.Context(), args, strings.NewReader(string(payload)), false, dir)
		if err != nil || string(got) != string(payload) {
			t.Fatalf("selected input rejected for %s: %v", args[0], err)
		}
	}
	limit, err := core.ReadCommandInputBytes(dir, "message.send")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commandPayload(t.Context(), []string{"message.send", "--stdin"}, strings.NewReader(strings.Repeat(" ", int(limit))+"{}"), false, dir); err == nil {
		t.Fatal("selected finite input limit lost")
	}
	if _, err := commandPayload(t.Context(), []string{"lan.join", "--stdin"}, strings.NewReader(string(payload)), false, dir); err == nil {
		t.Fatal("invitation protocol allowance widened")
	}
}

func TestCommandPayloadRejectsInvalidCapacityWithoutReadingInput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "capacity.json"), []byte(`{"version":1,"resources":{"messageTextBytes":{"mode":"unlimited"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := commandPayload(context.Background(), []string{"message.send", "--stdin"}, failIfRead{t}, false, dir); err == nil {
		t.Fatal("invalid policy silently fell back")
	}
}

type failIfRead struct{ t *testing.T }

func (r failIfRead) Read([]byte) (int, error) {
	r.t.Fatal("read payload before validating selected limits")
	return 0, nil
}
