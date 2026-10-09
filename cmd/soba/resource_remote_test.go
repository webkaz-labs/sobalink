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

func remoteInspectionArgs() []string {
	return []string{"remote", "inspect", "--peer", strings.Repeat("a", 64), "--id", resourceTestID, "--grant-id", resourceTestID, "--grant-revision", "1"}
}
func TestResourceRemoteCLIExactTypedRequest(t *testing.T) {
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		calls := 0
		client := func(_ context.Context, _ string, raw string, target any) error {
			calls++
			var command webui.Command
			if json.Unmarshal([]byte(raw), &command) != nil || command.Name != "resource.remote.inspect" {
				t.Fatal("wrong command")
			}
			var input resourcegrant.RemoteInspectInput
			if resource.Decode(command.Payload, 4096, &input) != nil || input.Validate() != nil || input.PeerKey != strings.Repeat("a", 64) || input.Request.Target.ResourceID != resourceTestID || input.Request.GrantID != resourceTestID || input.Request.GrantRevision != 1 {
				t.Fatal("scope changed")
			}
			return json.Unmarshal([]byte(`{"protocolVersion":1,"target":{"schemaVersion":1,"resourceId":"0123456789abcdef0123456789abcdef"},"requested":{},"effective":{}}`), target)
		}
		if err := resourceCLI(t.Context(), remoteInspectionArgs(), "unused", ja, false, &out, client); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || !json.Valid(out.Bytes()) {
			t.Fatal("request/output missing")
		}
	}
}
func TestResourceRemoteCLIValidationLocaleAndPrivacy(t *testing.T) {
	noCall := func(context.Context, string, string, any) error {
		t.Fatal("local-only path contacted agent")
		return nil
	}
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		if err := resourceCLI(t.Context(), []string{"remote", "--help"}, "unused", ja, false, &out, noCall); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "明示的に許可された") != ja {
			t.Fatal("wrong language")
		}
		out.Reset()
		if err := resourceCLI(t.Context(), remoteInspectionArgs(), "unused", ja, true, &out, noCall); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "local-input-only") {
			t.Fatal("dry run missing")
		}
		for _, args := range [][]string{{"remote", "apply"}, {"remote", "inspect"}, append(remoteInspectionArgs(), "--offline"), append(remoteInspectionArgs(), "--grant-revision", "0"), append(remoteInspectionArgs(), "--command", "resource.list")} {
			if err := resourceCLI(t.Context(), args, "unused", ja, false, io.Discard, noCall); err == nil {
				t.Fatal("invalid remote request accepted")
			}
		}
		for _, code := range []string{"resource_remote_unavailable", "resource_remote_unsupported"} {
			original := &control.RemoteError{Code: code, Message: "/synthetic-private/target.sock 192.0.2.19"}
			err := resourceCLI(t.Context(), remoteInspectionArgs(), "unused", ja, false, io.Discard, func(context.Context, string, string, any) error { return original })
			err = localizeResourceError(ja, err)
			var coded interface{ ErrorCode() string }
			if !errors.As(err, &coded) || coded.ErrorCode() != code || strings.Contains(err.Error(), "synthetic-private") || strings.Contains(err.Error(), "192.0.2.19") {
				t.Fatal("private error escaped")
			}
		}
	}
}
