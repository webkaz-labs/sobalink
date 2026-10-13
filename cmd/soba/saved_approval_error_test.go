package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/control"
)

func savedApprovalCLIConflictArgs(code string) []string {
	if code == "startup_revision_conflict" {
		return []string{"startup", "save", "--name", "synthetic-startup", "--ids", "service-a", "--review", "scope-review", "--store-review", "store-review"}
	}
	return []string{"proxy", "saved-start", "synthetic-proxy", "--review", "scope-review"}
}

func TestSavedApprovalCLIConflictLocalization(t *testing.T) {
	for _, code := range []string{"startup_revision_conflict", "proxy_saved_revision_conflict"} {
		for _, locale := range []string{"en", "ja"} {
			cause := &control.RemoteError{Code: code, Message: "synthetic private detail"}
			calls := 0
			client := func(context.Context, string, string, any) error { calls++; return cause }
			var out bytes.Buffer
			args := append([]string{"--state-dir", t.TempDir(), "--locale", locale}, savedApprovalCLIConflictArgs(code)...)
			err := runWith(context.Background(), args, &out, strings.NewReader(""), client)
			var coded interface{ ErrorCode() string }
			if calls != 1 || err == nil || !errors.As(err, &coded) || coded.ErrorCode() != code || !errors.Is(err, cause) || out.Len() != 0 || strings.Contains(err.Error(), cause.Message) {
				t.Fatal("conflict repeated a command, lost code/cause or exposed detail")
			}
			ja := locale == "ja"
			required := []string{text(ja, "peer approval", "相手の承認"), text(ja, "separate approval", "別の許可"), text(ja, "explicitly", "明示的")}
			if code == "startup_revision_conflict" {
				required = append(required, "startup list", "startup preview")
			} else {
				required = append(required, "proxy saved", "proxy preview")
			}
			for _, value := range required {
				if !strings.Contains(err.Error(), value) {
					t.Fatalf("missing %q in %s guidance", value, locale)
				}
			}
		}
	}
	ordinary := errors.New("synthetic ordinary error")
	unknown := &control.RemoteError{Code: "proxy_listener_unavailable", Message: "synthetic existing listener error"}
	for _, ja := range []bool{false, true} {
		for _, err := range []error{nil, ordinary, unknown} {
			if localizeSavedApprovalError(ja, err) != err {
				t.Fatal("unrelated error was rewritten")
			}
		}
	}
}

func TestSavedApprovalCLIMachineJSONUnchanged(t *testing.T) {
	for _, code := range []string{"startup_revision_conflict", "proxy_saved_revision_conflict"} {
		previous := ""
		for _, locale := range []string{"en", "ja"} {
			var out, stderr bytes.Buffer
			client := func(context.Context, string, string, any) error {
				return &control.RemoteError{Code: code, Message: "Synthetic saved-scope conflict"}
			}
			args := append([]string{"--state-dir", t.TempDir(), "--locale", locale, "--json-errors"}, savedApprovalCLIConflictArgs(code)...)
			err := runWith(context.Background(), args, &out, strings.NewReader(""), client)
			if err == nil || out.Len() != 0 {
				t.Fatal("conflict became success")
			}
			writeCommandError(&stderr, err)
			var result struct {
				Code  string `json:"code"`
				Error string `json:"error"`
			}
			if json.Unmarshal(stderr.Bytes(), &result) != nil || result.Code != code || result.Error != "Synthetic saved-scope conflict" || previous != "" && previous != stderr.String() {
				t.Fatal("machine error contract localized or changed")
			}
			previous = stderr.String()
		}
	}
	for _, args := range [][]string{{"startup", "list"}, {"proxy", "saved"}} {
		previous := ""
		for _, locale := range []string{"en", "ja"} {
			var out bytes.Buffer
			client := func(_ context.Context, _, _ string, target any) error {
				return json.Unmarshal([]byte(`{"entries":[{"name":"synthetic","valid":false,"state":"stale"}],"revision":"unchanged"}`), target)
			}
			if err := runWith(context.Background(), append([]string{"--state-dir", t.TempDir(), "--locale", locale}, args...), &out, strings.NewReader(""), client); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), `"valid": false`) || !strings.Contains(out.String(), `"state": "stale"`) || previous != "" && previous != out.String() {
				t.Fatal("stable saved-scope list was translated or rewritten")
			}
			previous = out.String()
		}
	}
}

func TestSavedApprovalCLIHelpRequiresSeparateReview(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		for _, args := range [][]string{{"startup", "--help"}, {"proxy", "saved-help"}} {
			var out bytes.Buffer
			client := func(context.Context, string, string, any) error { t.Fatal("help contacted the agent"); return nil }
			if err := runWith(context.Background(), append([]string{"--state-dir", t.TempDir(), "--locale", locale}, args...), &out, strings.NewReader(""), client); err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{text(locale == "ja", "Peer approval changes", "相手の承認変更"), text(locale == "ja", "review", "確認"), text(locale == "ja", "silently", "暗黙")} {
				if !strings.Contains(out.String(), value) {
					t.Fatalf("missing help boundary %q", value)
				}
			}
		}
	}
}
