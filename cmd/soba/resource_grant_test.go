package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func cliGrantReview() resourcegrant.GrantReview {
	return resourcegrant.GrantReview{Grant: resourcegrant.Record{ID: resourceTestID, Revision: 1, Target: resource.Target{SchemaVersion: 1, ResourceID: resourceTestID}, ResourceType: resource.Type, Relationship: resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: strings.Repeat("a", 64), PeerKey: strings.Repeat("b", 64), PairBinding: strings.Repeat("c", 64)}, Actions: []string{resourcegrant.Inspect}, Fields: []string{resourcegrant.FilesField, resourcegrant.PerPeerField}, IssuedAt: 1800000000, ExpiresAt: 1800003600, State: resourcegrant.Active}, BaseRevision: strings.Repeat("d", 64), ReviewRevision: strings.Repeat("e", 64), InitializesState: true}
}
func cliGrantReviewFile(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(cliGrantReview())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func cliGrantArgs(action, file string) []string {
	switch action {
	case "preview":
		return []string{"grant", "preview", "--id", resourceTestID, "--peer", strings.Repeat("b", 64), "--expires-at", "2030-01-01T00:00:00Z"}
	case "confirm":
		return []string{"grant", "confirm", "--review-file", file, "--confirm"}
	case "inspect":
		return []string{"grant", "inspect", "--id", resourceTestID}
	default:
		return []string{"grant", "revoke", "--id", resourceTestID, "--grant-id", resourceTestID, "--grant-revision", "1", "--confirm"}
	}
}
func TestResourceGrantCLIReviewedCommandsAndStableJSON(t *testing.T) {
	file := cliGrantReviewFile(t)
	for _, locale := range []string{"en", "ja"} {
		for _, action := range []string{"preview", "confirm", "inspect", "revoke"} {
			args := append([]string{"--locale", locale, "--state-dir", t.TempDir(), "resource"}, cliGrantArgs(action, file)...)
			calls := 0
			var out bytes.Buffer
			client := func(_ context.Context, _ string, raw string, target any) error {
				calls++
				var command webui.Command
				if err := json.Unmarshal([]byte(raw), &command); err != nil {
					t.Fatal(err)
				}
				if command.Name != "resource.grant."+action || command.RequestID == "" {
					t.Fatal("wrong local command")
				}
				switch action {
				case "preview":
					var in resourcegrant.GrantInputs
					if resource.Decode(command.Payload, 4096, &in) != nil || in.PeerKey != strings.Repeat("b", 64) || !reflect.DeepEqual(in.Actions, []string{resourcegrant.Inspect}) || !reflect.DeepEqual(in.Fields, []string{resourcegrant.FilesField, resourcegrant.PerPeerField}) {
						t.Fatal("preview scope widened")
					}
				case "confirm":
					var in resourcegrant.GrantConfirmation
					if resource.Decode(command.Payload, 8192, &in) != nil || !in.Confirm || !reflect.DeepEqual(in.Review, cliGrantReview()) {
						t.Fatal("review changed before confirmation")
					}
				case "revoke":
					var in resourcegrant.GrantRevoke
					if resource.Decode(command.Payload, 4096, &in) != nil || !in.Confirm || in.GrantID != resourceTestID || in.GrantRevision != 1 {
						t.Fatal("revoke scope changed")
					}
				}
				return json.Unmarshal([]byte(`{"activation":"not_started","listenerReady":false}`), target)
			}
			if err := runWith(t.Context(), args, &out, strings.NewReader(""), client); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !json.Valid(out.Bytes()) || !strings.Contains(out.String(), `"not_started"`) {
				t.Fatal("machine response changed")
			}
		}
	}
}
func TestResourceGrantCLIRejectsIncompleteAndAmbiguousReview(t *testing.T) {
	noCall := func(context.Context, string, string, any) error { t.Fatal("invalid input contacted agent"); return nil }
	for _, ja := range []bool{false, true} {
		cases := [][]string{{"grant", "confirm"}, {"grant", "confirm", "--review-file", cliGrantReviewFile(t)}, {"grant", "inspect"}, {"grant", "preview", "--id", resourceTestID, "--peer", strings.Repeat("b", 64), "--expires-at", "2030-01-01"}, {"grant", "revoke", "--id", resourceTestID, "--grant-id", resourceTestID, "--grant-revision", "1"}, {"grant", "apply"}, {"grant", "inspect", "--id", resourceTestID, "--offline"}}
		for _, number := range []string{"0", "01", "9007199254740992", "-1"} {
			cases = append(cases, []string{"grant", "revoke", "--id", resourceTestID, "--grant-id", resourceTestID, "--grant-revision", number, "--confirm"})
		}
		for _, args := range cases {
			if err := resourceCLI(t.Context(), args, t.TempDir(), ja, false, io.Discard, noCall); err == nil {
				t.Fatal("incomplete input accepted")
			}
		}
	}
	data, _ := json.Marshal(cliGrantReview())
	for _, bad := range []string{"null", string(data) + "{}", strings.Repeat(" ", 8193), strings.Replace(string(data), `"initializesState":true`, `"initializesState":true,"actor":"local-control"`, 1), strings.Replace(string(data), `"initializesState":true`, `"initializesState":true,"initializesState":false`, 1), strings.Replace(string(data), `,"initializesState":true`, ``, 1)} {
		file := filepath.Join(t.TempDir(), "review.json")
		if err := os.WriteFile(file, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		err := resourceCLI(t.Context(), []string{"grant", "confirm", "--review-file", file, "--confirm"}, t.TempDir(), false, false, io.Discard, noCall)
		if err == nil || strings.Contains(err.Error(), file) {
			t.Fatal("ambiguous review accepted or private filename leaked")
		}
	}
}
func TestResourceGrantCLIHelpDryRunAndAutomaticLocale(t *testing.T) {
	noCall := func(context.Context, string, string, any) error {
		t.Fatal("help or dry run contacted agent")
		return nil
	}
	for _, environment := range []string{"ja_JP.UTF-8", "en_US.UTF-8", "zz_ZZ.UTF-8"} {
		t.Setenv("LC_ALL", environment)
		for _, locale := range []string{"auto", "ja", "en"} {
			var out bytes.Buffer
			if err := runWith(t.Context(), []string{"--locale", locale, "resource", "grant", "--help"}, &out, strings.NewReader(""), noCall); err != nil {
				t.Fatal(err)
			}
			japanese := locale == "ja" || locale == "auto" && strings.HasPrefix(environment, "ja")
			if strings.Contains(out.String(), "管理対象の相手1台への参照のみの許可") != japanese || !strings.Contains(out.String(), "listenerReady") {
				t.Fatal("locale or listener status missing")
			}
		}
	}
	file := cliGrantReviewFile(t)
	for _, action := range []string{"preview", "confirm", "inspect", "revoke"} {
		var out bytes.Buffer
		args := append([]string{"--dry-run", "--state-dir", t.TempDir(), "resource"}, cliGrantArgs(action, file)...)
		if err := runWith(t.Context(), args, &out, strings.NewReader(""), noCall); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `"applied": false`) || !strings.Contains(out.String(), `"validation": "local-input-only"`) {
			t.Fatal("dry run claimed authority")
		}
	}
}
func TestResourceGrantCLIErrorLocalizationAndPrivacy(t *testing.T) {
	for _, code := range []string{"resource_grant_revoke_uncertain", "resource_grant_unavailable", "resource_grant_invalid", "resource_grant_stale", "resource_grant_conflict"} {
		original := &control.RemoteError{Code: code, Message: "/synthetic-private/agent.sock 192.0.2.18"}
		for _, machine := range []bool{false, true} {
			args := []string{"--locale", "ja", "--state-dir", t.TempDir()}
			if machine {
				args = append(args, "--json-errors")
			}
			args = append(args, "resource", "grant", "inspect", "--id", resourceTestID)
			err := runWith(t.Context(), args, io.Discard, strings.NewReader(""), func(context.Context, string, string, any) error { return original })
			var coded interface{ ErrorCode() string }
			if !errors.As(err, &coded) || coded.ErrorCode() != code || strings.Contains(err.Error(), "synthetic-private") || strings.Contains(err.Error(), "192.0.2.18") {
				t.Fatal("error identity or privacy changed")
			}
			if machine && err.Error() != resourceControlError(original).Error() || !machine && err.Error() == resourceControlError(original).Error() {
				t.Fatal("human/machine locale contract changed")
			}
		}
	}
}
