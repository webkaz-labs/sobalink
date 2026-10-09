package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func remoteManagementArgs(action string) []string {
	args := []string{"remote", "manage", action, "--peer", strings.Repeat("a", 64), "--id", resourceTestID, "--grant-id", resourceTestID, "--grant-revision", "1"}
	if action == "preview" || action == "apply" {
		args = append(args, "--concurrent-files", "default", "--concurrent-per-peer", "2")
	}
	if action == "apply" || action == "status" {
		args = append(args, "--operation-id", strings.Repeat("b", 64))
	}
	if action == "apply" {
		args = append(args, "--base-revision", strings.Repeat("c", 64), "--revision", strings.Repeat("d", 64), "--confirm")
	}
	return args
}

func TestRemoteManagementCLIExactTypedActions(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, action := range []string{"inspect", "preview", "apply", "status"} {
			calls := 0
			var out bytes.Buffer
			client := func(_ context.Context, _ string, raw string, target any) error {
				calls++
				var command webui.Command
				if json.Unmarshal([]byte(raw), &command) != nil {
					t.Fatal("invalid local command")
				}
				wireAction := action
				if action == "status" {
					wireAction = resourcegrant.StatusAction
				}
				if command.Name != "resource.remote.management."+wireAction {
					t.Fatal("wrong typed command")
				}
				var input resourcegrant.RemoteManagementInput
				if resource.Decode(command.Payload, 4096, &input) != nil || input.Validate() != nil || input.Request.Action != wireAction || input.Confirm != (action == "apply") {
					t.Fatal("invalid typed payload")
				}
				if input.Request.ProtocolVersion != 2 || input.Request.GrantRevision != 1 || input.Request.Target.ResourceID != resourceTestID || input.PeerKey != strings.Repeat("a", 64) {
					t.Fatal("selector changed")
				}
				if action == "apply" && (input.Request.Apply.OperationID != strings.Repeat("b", 64) || input.Request.Apply.BaseRevision != strings.Repeat("c", 64) || input.Request.Apply.ReviewRevision != strings.Repeat("d", 64)) {
					t.Fatal("review changed")
				}
				return json.Unmarshal([]byte(`{"result":"synthetic"}`), target)
			}
			if err := resourceCLI(t.Context(), remoteManagementArgs(action), "unused", ja, false, &out, client); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !json.Valid(out.Bytes()) {
				t.Fatal("request repeated or output invalid")
			}
		}
	}
}

func TestRemoteManagementCLIRejectsIncompleteOrExpandedInput(t *testing.T) {
	noCall := func(context.Context, string, string, any) error { t.Fatal("invalid input contacted agent"); return nil }
	apply := remoteManagementArgs("apply")
	for _, ja := range []bool{false, true} {
		for _, args := range [][]string{
			apply[:len(apply)-1], append(append([]string{}, apply...), "--confirm=false"),
			append(remoteManagementArgs("inspect"), "--confirm"), append(remoteManagementArgs("status"), "--concurrent-files", "1"),
			append(remoteManagementArgs("preview"), "--concurrent-per-peer", "0"), append(remoteManagementArgs("apply"), "--operation-id", "bad"),
			append(remoteManagementArgs("inspect"), "--offline"), append(remoteManagementArgs("inspect"), "--command", "resource.list"),
			append(remoteManagementArgs("inspect"), "--grant-revision", "0"), {"remote", "manage", "apply"},
		} {
			if err := resourceCLI(t.Context(), args, "unused", ja, false, io.Discard, noCall); err == nil {
				t.Fatal("invalid request accepted")
			}
		}
	}
}

func TestRemoteManagementCLIHelpDryRunAndPrivacy(t *testing.T) {
	noCall := func(context.Context, string, string, any) error { t.Fatal("local path contacted agent"); return nil }
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		if err := resourceCLI(t.Context(), []string{"remote", "manage", "--help"}, "unused", ja, false, &out, noCall); err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{"--confirm", "reviewRevision", "evidenceDurable", "15", "v2"} {
			if !strings.Contains(out.String(), required) {
				t.Fatal("consequence missing")
			}
		}
		if strings.Contains(out.String(), "明示的に許可された") != ja {
			t.Fatal("wrong locale")
		}
		for _, action := range []string{"inspect", "preview", "apply", "status"} {
			out.Reset()
			if err := resourceCLI(t.Context(), remoteManagementArgs(action), "unused", ja, true, &out, noCall); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), `"applied": false`) || !strings.Contains(out.String(), "local-input-only") {
				t.Fatal("dry run claimed execution")
			}
		}
		for _, code := range []string{"resource_management_remote_unavailable", "resource_management_remote_unsupported"} {
			original := &control.RemoteError{Code: code, Message: "/synthetic-private/state.sock 192.0.2.19"}
			err := resourceCLI(t.Context(), remoteManagementArgs("inspect"), "unused", ja, false, io.Discard, func(context.Context, string, string, any) error { return original })
			err = localizeResourceError(ja, err)
			var coded interface{ ErrorCode() string }
			if !errors.As(err, &coded) || coded.ErrorCode() != code || strings.Contains(err.Error(), "synthetic-private") || strings.Contains(err.Error(), "192.0.2.19") {
				t.Fatal("private error escaped")
			}
		}
	}
}
