package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestOutboxCLIBusyAndEvidenceGuidance(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, code := range []string{"outbox_storage_busy", "outbox_evidence_capacity", "durable_evidence_capacity"} {
			err := outboxCLIControlError(ja, &control.RemoteError{Code: code, Message: "synthetic-private-cause"})
			var coded interface{ ErrorCode() string }
			if !errors.As(err, &coded) || coded.ErrorCode() != code || strings.Contains(err.Error(), "synthetic-private-cause") {
				t.Fatal("guidance changed machine code or exposed private cause")
			}
			wants := []string{code, text(ja, "original request", "元の要求")}
			if code == "outbox_storage_busy" {
				wants = append(wants, text(ja, "still prepared", "準備済み"), "commit", text(ja, "deadline", "期限"), text(ja, "nothing was sent", "何も送られていない"))
			} else {
				wants = append(wants, text(ja, "separate", "別"), text(ja, "physical", "実際"), text(ja, "history cleanup may not free", "整理で"), text(ja, "uncertain evidence", "結果不明の記録"))
			}
			for _, want := range wants {
				if !strings.Contains(err.Error(), want) || !strings.Contains(outboxCLIReason(ja, code), want) {
					t.Fatalf("missing %q for %s", want, code)
				}
			}
			r := outboxCLITestRecord()
			r.ReasonCode = code
			var out bytes.Buffer
			if err := outboxCLIShow(&out, "profile", ja, r); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{r.OutboxID, code, "--state-dir", "profile", "outbox show"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("guidance lost original lookup %q", want)
				}
			}
		}
	}
}
func TestOutboxCLIBusySameConfirmationRetry(t *testing.T) {
	for _, ja := range []bool{false, true} {
		f := &outboxCLIFixture{t: t, record: outboxCLITestRecord()}
		original := f.record
		var attempts []webui.Command
		client := func(ctx context.Context, dir, wire string, target any) error {
			var command webui.Command
			if json.Unmarshal([]byte(wire), &command) != nil {
				t.Fatal("unexpected process lookup")
			}
			if command.Name == "outbox.commit" {
				attempts = append(attempts, command)
				if len(attempts) == 1 {
					return &control.RemoteError{Code: "outbox_storage_busy"}
				}
			}
			return f.call(ctx, dir, wire, target)
		}
		args := []string{"commit", original.OutboxID, "--process", original.ProcessID, "--revision", original.ReviewRevision, "--confirm", "--json"}
		var out bytes.Buffer
		if err := outboxCommandCLI(context.Background(), args, "profile", ja, false, &out, client); err == nil || !strings.Contains(out.String(), original.OutboxID) || !strings.Contains(out.String(), `"code":"outbox_storage_busy"`) {
			t.Fatal("busy reply lost original request")
		}
		if len(attempts) != 1 || len(f.calls) != 0 {
			t.Fatal("automatic replacement or retry")
		}
		if err := outboxCommandCLI(context.Background(), []string{"show", original.OutboxID, "--json"}, "profile", ja, false, io.Discard, client); err != nil {
			t.Fatal(err)
		}
		if err := outboxCommandCLI(context.Background(), args, "profile", ja, false, io.Discard, client); err != nil {
			t.Fatal(err)
		}
		if len(attempts) != 2 || string(attempts[0].Payload) != string(attempts[1].Payload) || !outboxCLISameReview(original, f.record) || !reflect.DeepEqual(f.calls, []string{"status", "commit"}) {
			t.Fatal("retry changed original authority")
		}
	}
}
func TestOutboxCLIEvidenceJSONNotLocalized(t *testing.T) {
	for _, code := range []string{"outbox_storage_busy", "outbox_evidence_capacity", "durable_evidence_capacity"} {
		var previous string
		for _, ja := range []bool{false, true} {
			r := outboxCLITestRecord()
			r.ReasonCode = code
			f := &outboxCLIFixture{t: t, record: r}
			var out bytes.Buffer
			if err := outboxCommandCLI(context.Background(), []string{"show", r.OutboxID, "--json"}, "profile", ja, false, &out, f.call); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), `"reasonCode":"`+code+`"`) || previous != "" && previous != out.String() {
				t.Fatal("machine evidence result localized")
			}
			previous = out.String()
		}
	}
}
