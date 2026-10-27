package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestStopHumanOutputNeverPromotesRequestOrMissingEndpointToExit(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, scenario := range []string{"accepted", "absent", "unknown"} {
			t.Run(scenario, func(t *testing.T) {
				var out bytes.Buffer
				client := func(_ context.Context, _ string, command string, result any) error {
					if command != "stop" {
						t.Fatal("unexpected command")
					}
					if scenario == "absent" {
						return os.ErrNotExist
					}
					value := `{"state":"stopping"}`
					if scenario == "unknown" {
						value = `{"state":"unknown"}`
					}
					*result.(*json.RawMessage) = json.RawMessage(value)
					return nil
				}
				if err := snapshotCommand(context.Background(), "stop", nil, "synthetic-profile", ja, &out, client); err != nil {
					t.Fatal(err)
				}
				expected := text(ja, "Stop requested.", "停止を要求しました。")
				if scenario == "absent" {
					expected = text(ja, "endpoint is unavailable", "ローカル制御の入口がありません")
				}
				if scenario == "unknown" {
					expected = text(ja, "Stop completion is unconfirmed", "停止完了は未確認")
				}
				if !strings.Contains(out.String(), expected) || strings.Contains(out.String(), "Stopped.") || strings.Contains(out.String(), "停止しました。") {
					t.Fatal("unproven stop completion", out.String())
				}
			})
		}
	}
}
