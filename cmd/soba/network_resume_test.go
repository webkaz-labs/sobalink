package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestNetworkResumeCLIReviewDoesNotApplyAndKeepsProfile(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		var out bytes.Buffer
		calls := 0
		caller := func(_ context.Context, dir, raw string, value any) error {
			calls++
			if dir != "synthetic-profile" {
				t.Fatal("profile lost")
			}
			var cmd webui.Command
			if json.Unmarshal([]byte(raw), &cmd) != nil || cmd.Name != "network.resume.preview" {
				t.Fatal("review mutated")
			}
			view := value.(*core.NetworkResumeReview)
			*view = core.NetworkResumeReview{Mode: "tailnet", Hostname: "synthetic", Revision: "synthetic-revision", Backends: []string{"tailnet"}, StartupSuppressed: true, SavedProxiesSuppressed: true}
			return nil
		}
		if err := runWith(context.Background(), []string{"--state-dir", "synthetic-profile", "--locale", locale, "network", "resume"}, &out, strings.NewReader(""), caller); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || !strings.Contains(out.String(), "--expected-revision") || !strings.Contains(out.String(), "synthetic-profile") {
			t.Fatal("missing exact follow-up")
		}
	}
}
func TestNetworkResumeCLIApplyDryRunAndHelp(t *testing.T) {
	for _, args := range [][]string{{"network", "--help"}, {"network", "resume", "--help"}, {"--dry-run", "network", "resume"}, {"--dry-run", "network", "resume", "--expected-revision", "synthetic"}} {
		var out bytes.Buffer
		if err := runWith(context.Background(), args, &out, strings.NewReader(""), func(context.Context, string, string, any) error { t.Fatal("offline request made IPC"); return nil }); err != nil {
			t.Fatal(err)
		}
		if out.Len() == 0 {
			t.Fatal("missing guidance")
		}
	}
	var out bytes.Buffer
	calls := 0
	if err := runWith(context.Background(), []string{"network", "resume", "--expected-revision", "synthetic"}, &out, strings.NewReader(""), func(_ context.Context, _, raw string, value any) error {
		calls++
		var cmd webui.Command
		if json.Unmarshal([]byte(raw), &cmd) != nil || cmd.Name != "network.resume" || string(cmd.Payload) != `{"expectedRevision":"synthetic"}` {
			t.Fatal("changed apply payload")
		}
		*value.(*json.RawMessage) = json.RawMessage(`{"state":"starting","mode":"tailnet"}`)
		return nil
	}); err != nil || calls != 1 {
		t.Fatal("apply failed", err)
	}
}
