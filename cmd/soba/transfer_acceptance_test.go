package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestTransferAcceptCLIExplicitRetryKeepsSelection(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		destination := t.TempDir()
		var attempts []webui.Command
		client := func(_ context.Context, dir, wire string, value any) error {
			var cmd webui.Command
			if dir != "synthetic-profile" || json.Unmarshal([]byte(wire), &cmd) != nil || cmd.Name != "transfer.accept" {
				t.Fatal("acceptance changed profile or issued another operation")
			}
			attempts = append(attempts, cmd)
			if len(attempts) == 1 {
				return &control.RemoteError{Code: "transfer_accept_busy", Message: "synthetic-private-cause"}
			}
			return json.Unmarshal([]byte(`{"state":"accepted"}`), value)
		}
		args := []string{"--locale", locale, "--state-dir", "synthetic-profile", "accept", "original-transfer", destination}
		err := runWith(context.Background(), args, io.Discard, strings.NewReader(""), client)
		if err == nil || len(attempts) != 1 {
			t.Fatal("busy acceptance disappeared or triggered automatic retry")
		}
		var coded interface{ ErrorCode() string }
		if !errors.As(err, &coded) || coded.ErrorCode() != "transfer_accept_busy" || strings.Contains(err.Error(), "synthetic-private-cause") {
			t.Fatal("busy guidance lost code or exposed a private cause")
		}
		for _, want := range []string{text(locale == "ja", "same transfer ID, directory and profile", "同じ転送ID・保存先・プロファイル"), text(locale == "ja", "earlier acceptance may exist", "以前の承認が残っている可能性"), text(locale == "ja", "explicitly", "明示的")} {
			if !strings.Contains(err.Error(), want) {
				t.Fatal("explicit retry guidance lost its scope")
			}
		}
		if err := runWith(context.Background(), args, io.Discard, strings.NewReader(""), client); err != nil {
			t.Fatal("later explicit acceptance failed")
		}
		if len(attempts) != 2 || attempts[0].RequestID == "" || attempts[0].RequestID == attempts[1].RequestID || !bytes.Equal(attempts[0].Payload, attempts[1].Payload) {
			t.Fatal("explicit retry reused the cached invocation or changed selection")
		}
		var payload struct{ TransferID, Destination string }
		if json.Unmarshal(attempts[0].Payload, &payload) != nil || payload.TransferID != "original-transfer" || payload.Destination != destination || !filepath.IsAbs(payload.Destination) {
			t.Fatal("acceptance did not retain an explicit resolved destination")
		}
	}
}

func TestTransferAcceptCLIUnconfirmedNeverPromisesRetry(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		for _, code := range []string{"transfer_accept_unconfirmed", "command_failed", "busy", "durable_recovery_required", "disk_space_low_extra", "disk_space_unknown\nsynthetic", "unknown", ""} {
			calls := 0
			original := &control.RemoteError{Code: code, Message: "synthetic-private-cause"}
			err := runWith(context.Background(), []string{"--locale", locale, "accept", "original", t.TempDir()}, io.Discard, strings.NewReader(""), func(context.Context, string, string, any) error { calls++; return original })
			if err == nil || calls != 1 || !errors.Is(err, original) || strings.Contains(err.Error(), "synthetic-private-cause") {
				t.Fatal("unconfirmed acceptance lost its cause or changed invocation count")
			}
			if !strings.Contains(err.Error(), text(locale == "ja", "could not be confirmed", "確認できません")) || !strings.Contains(err.Error(), text(locale == "ja", "Before another action", "次の操作の前")) || strings.Contains(err.Error(), "run accept again") || strings.Contains(err.Error(), "もう一度") {
				t.Fatal("unconfirmed acceptance promised a retry or definite nonacceptance")
			}
		}
	}
}

func TestTransferAcceptCLIJSONErrorsRemainStable(t *testing.T) {
	for _, code := range []string{"transfer_accept_busy", "transfer_accept_unconfirmed", "disk_space_low", "disk_space_unknown"} {
		previous := ""
		for _, locale := range []string{"en", "ja"} {
			original := &control.RemoteError{Code: code, Message: "stable machine message"}
			err := runWith(context.Background(), []string{"--locale", locale, "--json-errors", "accept", "original", t.TempDir()}, io.Discard, strings.NewReader(""), func(context.Context, string, string, any) error { return original })
			var out bytes.Buffer
			writeCommandError(&out, err)
			if !strings.Contains(out.String(), `"code":"`+code+`"`) || !strings.Contains(out.String(), original.Message) || previous != "" && previous != out.String() {
				t.Fatal("machine acceptance code or message was localized")
			}
			previous = out.String()
		}
	}
}
