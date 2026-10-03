package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestReceiveRecoveryCLIExplicitReviewAndLocaleIndependentJSON(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		for _, mode := range []string{"preview", "confirm", "dry-run"} {
			t.Run(locale+"/"+mode, func(t *testing.T) {
				args := []string{"--locale", locale, "--state-dir", t.TempDir()}
				if mode == "dry-run" {
					args = append(args, "--dry-run")
				}
				args = append(args, "receive", "recovery", "confirm", "--json")
				if mode != "preview" {
					args = append(args, "--reviewed")
				}
				calls := 0
				var out bytes.Buffer
				caller := func(ctx context.Context, dir, raw string, result any) error {
					calls++
					var command webui.Command
					if err := json.Unmarshal([]byte(raw), &command); err != nil {
						t.Fatal(err)
					}
					if command.Name != "receive.recovery.confirm" {
						t.Fatal("wrong local operation")
					}
					var payload struct {
						Reviewed bool `json:"reviewed"`
					}
					if err := json.Unmarshal(command.Payload, &payload); err != nil {
						t.Fatal(err)
					}
					if payload.Reviewed != (mode == "confirm") {
						t.Fatal("review changed without explicit flag, or dry run applied")
					}
					data := []byte(`{"state":"blocked","code":"legacy_review_required","reservedBytes":null,"applied":false,"review":["previous_manual_destinations"]}`)
					return json.Unmarshal(data, result)
				}
				if err := runWith(context.Background(), args, &out, strings.NewReader(""), caller); err != nil {
					t.Fatal(err)
				}
				if calls != 1 {
					t.Fatalf("calls: %d", calls)
				}
				want := `{"state":"blocked","code":"legacy_review_required","reservedBytes":null,"applied":false,"review":["previous_manual_destinations"]}` + "\n"
				if out.String() != want {
					t.Fatalf("localized/unstable JSON: %s", out.String())
				}
			})
		}
	}
}

func TestReceiveRecoveryCLIExplainsReviewInBothLanguages(t *testing.T) {
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		query := func(name string, payload, result any) error {
			*(result.(*transfer.ReceiveRecoveryView)) = transfer.ReceiveRecoveryView{State: "blocked", Code: "legacy_review_required"}
			return nil
		}
		if err := receiveRecoveryCommand([]string{"recovery", "confirm"}, "/fictional/profile", ja, false, &out, query); err != nil {
			t.Fatal(err)
		}
		required := []string{"default", "per-peer", "manually", "unfinished", "saved", "--reviewed", "/fictional/profile", "no untracked partial data remains", "does not delete any files"}
		if ja {
			required = []string{"既定", "相手別", "手動", "途中保存", "保存済み", "--reviewed", "/fictional/profile", "未追跡の途中保存データが残らない", "ファイルを削除しません"}
		}
		for _, word := range required {
			if !strings.Contains(out.String(), word) {
				t.Fatalf("missing review guidance %q: %s", word, out.String())
			}
		}
	}
}

func TestReceiveRecoveryInvalidInputsAndHelpNeverConfirm(t *testing.T) {
	for _, args := range [][]string{
		{"receive", "recovery"}, {"receive", "recovery", "confirm", "--unknown"},
		{"receive", "recovery", "confirm", "--reviewed", "extra"},
		{"receive", "recovery", "confirm", "--reviewed", "--help"}, {"help", "receive"},
	} {
		var out bytes.Buffer
		calls := 0
		err := runWith(context.Background(), append([]string{"--state-dir", t.TempDir()}, args...), &out, strings.NewReader(""), func(context.Context, string, string, any) error { calls++; return nil })
		help := args[0] == "help" || args[len(args)-1] == "--help"
		if help && err != nil {
			t.Fatal(err)
		}
		if !help && err == nil {
			t.Fatalf("invalid input accepted: %v", args)
		}
		if calls != 0 {
			t.Fatalf("invalid/help input contacted local control: %v", args)
		}
	}
}
