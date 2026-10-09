package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func cliManagementReview() resourcegrant.ManagementGrantReview {
	old := cliGrantReview()
	old.Grant.Actions = []string{resourcegrant.Inspect, resourcegrant.PreviewAction, resourcegrant.ApplyAction, resourcegrant.StatusAction}
	return resourcegrant.ManagementGrantReview{Grant: resourcegrant.ManagementRecord{Scope: resourcegrant.Management, Record: old.Grant}, BaseRevision: old.BaseRevision, ReviewRevision: old.ReviewRevision, InitializesState: true, UpgradesFormat: true}
}
func cliManagementReviewFile(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(cliManagementReview())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "management-review.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestManagementGrantCLIExplicitTypedPreviewConfirm(t *testing.T) {
	file := cliManagementReviewFile(t)
	for _, locale := range []string{"en", "ja"} {
		for _, action := range []string{"preview", "confirm"} {
			args := append([]string{"--locale", locale, "--state-dir", t.TempDir(), "resource"}, append(cliGrantArgs(action, file), "--management")...)
			calls := 0
			var out bytes.Buffer
			client := func(_ context.Context, _ string, raw string, target any) error {
				calls++
				var command webui.Command
				if json.Unmarshal([]byte(raw), &command) != nil {
					t.Fatal("invalid command")
				}
				if command.Name != "resource.grant.management."+action {
					t.Fatal("legacy route reused")
				}
				if action == "preview" {
					var in resourcegrant.ManagementGrantInputs
					if resource.Decode(command.Payload, 4096, &in) != nil || in.Scope != resourcegrant.Management || (resourcegrant.ManagementScope{Actions: in.Inputs.Actions, Fields: in.Inputs.Fields}).Validate() != nil {
						t.Fatal("scope differs")
					}
				} else {
					var in resourcegrant.ManagementGrantConfirmation
					if resource.Decode(command.Payload, 8192, &in) != nil || !in.Confirm || !reflect.DeepEqual(in.Review, cliManagementReview()) {
						t.Fatal("review altered")
					}
				}
				return json.Unmarshal([]byte(`{"activation":"not_started","listenerReady":false}`), target)
			}
			if err := runWith(t.Context(), args, &out, strings.NewReader(""), client); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !json.Valid(out.Bytes()) || !strings.Contains(out.String(), `"not_started"`) {
				t.Fatal("CLI outcome misreported")
			}
		}
	}
}
func TestManagementGrantCLIRejectsScopeReinterpretation(t *testing.T) {
	noCall := func(context.Context, string, string, any) error { t.Fatal("invalid input contacted agent"); return nil }
	legacy := cliGrantReviewFile(t)
	managed := cliManagementReviewFile(t)
	for _, ja := range []bool{false, true} {
		for _, args := range [][]string{append(cliGrantArgs("confirm", legacy), "--management"), cliGrantArgs("confirm", managed), {"grant", "confirm", "--management", "--review-file", managed}, {"grant", "inspect", "--id", resourceTestID, "--management"}} {
			if err := resourceCLI(t.Context(), args, t.TempDir(), ja, false, io.Discard, noCall); err == nil {
				t.Fatal("scope/confirmation ambiguity accepted")
			}
		}
	}
	data, _ := json.Marshal(cliManagementReview())
	for _, bad := range []string{strings.Replace(string(data), `,"upgradesFormat":true`, "", 1), strings.Replace(string(data), `"scope":"management"`, `"scope":"inspect"`, 1), strings.Replace(string(data), `"upgradesFormat":true`, `"upgradesFormat":true,"actor":"local-control"`, 1)} {
		file := filepath.Join(t.TempDir(), "review.json")
		if err := os.WriteFile(file, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if err := resourceCLI(t.Context(), append(cliGrantArgs("confirm", file), "--management"), t.TempDir(), false, false, io.Discard, noCall); err == nil {
			t.Fatal("malformed management review accepted")
		}
	}
}
func TestManagementGrantCLIHelpAndDryRunTruth(t *testing.T) {
	noCall := func(context.Context, string, string, any) error { t.Fatal("help/dry run contacted agent"); return nil }
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		if err := resourceCLI(t.Context(), []string{"grant", "--help"}, t.TempDir(), ja, false, &out, noCall); err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{"--management", "operation.status", "resource remote manage", "upgradesFormat", "listenerReady"} {
			if !strings.Contains(out.String(), required) {
				t.Fatal("management consequence missing")
			}
		}
		out.Reset()
		if err := resourceCLI(t.Context(), append(cliGrantArgs("preview", ""), "--management"), t.TempDir(), ja, true, &out, noCall); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `"applied": false`) || !strings.Contains(out.String(), `"resource.grant.management.preview"`) {
			t.Fatal("dry run claimed admission")
		}
	}
}
