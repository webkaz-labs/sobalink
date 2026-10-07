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
	"github.com/webkaz-labs/sobalink/internal/core"
)

func portProposalCLI(t *testing.T, locale string, args []string, fail error) (string, error, []workflowCall) {
	t.Helper()
	m := newWorkflowMock()
	revision := strings.Repeat("a", 64)
	m.onCall = func(_ context.Context, name string, payload map[string]any) (any, error, bool) {
		switch name {
		case "service.config":
			return core.SavedServiceConfiguration{Configuration: m.selection.Services[0], Revision: revision}, nil, true
		case "service.connect":
			if fail != nil {
				return nil, fail, true
			}
			return map[string]any{"status": "active"}, nil, true
		case "service.ports":
			if fail != nil {
				return nil, fail, true
			}
			return core.ServicePortProposals{Configuration: m.selection.Services[0], Revision: revision, Code: "listener_conflict", ConflictPort: 18080, EffectivePorts: "8080", FromPort: 49152, RequestedCount: 3, AttemptBudget: 32, Attempts: 3, BindChecks: 4, StopReason: "requested_count", Proposals: []core.PortProposal{{LocalPort: 49152, LocalEnd: 49152}}}, nil, true
		}
		return nil, nil, false
	}
	var out bytes.Buffer
	err := runWith(context.Background(), append([]string{"--state-dir", "/tmp/proposal-fixture", "--locale", locale}, args...), &out, strings.NewReader(""), m.call)
	return out.String(), err, m.history()
}

func TestServicePortCLIExplicitCheckAndStableJSON(t *testing.T) {
	var jsonOutput string
	for _, locale := range []string{"en", "ja"} {
		out, err, calls := portProposalCLI(t, locale, []string{"service", "ports", "fixture", "--json", "--count", "2", "--attempts", "8"}, nil)
		if err != nil || len(calls) != 2 || calls[0].name != "service.config" || calls[1].name != "service.ports" {
			t.Fatal(err, calls)
		}
		want := map[string]any{"id": "saved-a", "expectedRevision": strings.Repeat("a", 64), "fromPort": float64(49152), "count": float64(2), "attempts": float64(8)}
		if !reflect.DeepEqual(want, calls[1].payload) {
			t.Fatal(calls[1].payload)
		}
		if jsonOutput != "" && jsonOutput != out {
			t.Fatal("locale changed JSON")
		}
		jsonOutput = out
		var result core.ServicePortProposals
		if err := json.Unmarshal([]byte(out), &result); err != nil || result.Reservation {
			t.Fatal(err, out)
		}
		human, err, _ := portProposalCLI(t, locale, []string{"service", "ports", "fixture"}, nil)
		if err != nil || !strings.Contains(human, "soba --state-dir /tmp/proposal-fixture service restart saved-a --local-port 49152 --expected-revision "+strings.Repeat("a", 64)) {
			t.Fatal(err, human)
		}
		if locale == "ja" && !strings.Contains(human, "候補は予約ではなく") {
			t.Fatal(human)
		}
	}
}

func TestServicePortCLIDryRunAndHelpNeverBind(t *testing.T) {
	out, err, calls := portProposalCLI(t, "en", []string{"--dry-run", "service", "ports", "fixture"}, nil)
	if err != nil || len(calls) != 1 || calls[0].name != "service.config" || !strings.Contains(out, `"command": "service.ports"`) {
		t.Fatal(err, out, calls)
	}
	for _, locale := range []string{"en", "ja"} {
		out, err, calls := portProposalCLI(t, locale, []string{"service", "ports", "--help"}, nil)
		if err != nil || len(calls) != 0 || !strings.Contains(out, "portProposalBinds") {
			t.Fatal(err, out, calls)
		}
	}
}

func TestServicePortCLIStaleReviewCannotRefreshIntoRestart(t *testing.T) {
	_, err, calls := portProposalCLI(t, "en", []string{"service", "restart", "fixture", "--local-port", "49152", "--expected-revision", strings.Repeat("b", 64)}, nil)
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) || coded.ErrorCode() != "service_revision_conflict" || len(calls) != 1 {
		t.Fatal("stale review refreshed silently", err, calls)
	}
	out, err, calls := portProposalCLI(t, "en", []string{"--dry-run", "service", "restart", "fixture", "--local-port", "49152", "--expected-revision", strings.Repeat("a", 64)}, nil)
	if err != nil || len(calls) != 1 {
		t.Fatal(err, calls)
	}
	payload := previewOf(t, out)
	if payload["localPort"] != float64(49152) || payload["ports"] != "8080" || payload["peerId"] != "peer-fixture" || payload["lifetime"] != "until-stopped" || payload["expectedRevision"] != strings.Repeat("a", 64) {
		t.Fatal("explicit choice changed unrelated scope", payload)
	}
	for _, args := range [][]string{{"service", "restart", "fixture", "--expected-revision", "bad"}, {"service", "copy", "fixture", "--expected-revision", strings.Repeat("a", 64)}} {
		_, err, calls := portProposalCLI(t, "en", args, nil)
		if err == nil || len(calls) != 0 {
			t.Fatal("invalid review reached IPC", err, calls)
		}
	}
}

func TestServicePortCLIErrorTaxonomyHumanAndMachine(t *testing.T) {
	for _, code := range []string{"listener_probe_timeout", "listener_probe_canceled", "listener_probe_invalid", "listener_conflict", "listener_capacity", "listener_permission_denied", "listener_address_unavailable", "listener_unavailable", "listener_mapping_invalid", "listener_probe_capacity", "listener_no_conflict", "listener_proposal_unsupported", "service_revision_conflict"} {
		remote := &control.RemoteError{Code: code, Message: "fixture stable English failure"}
		_, err, _ := portProposalCLI(t, "ja", []string{"service", "ports", "fixture"}, remote)
		var coded interface{ ErrorCode() string }
		if !errors.As(err, &coded) || coded.ErrorCode() != code || strings.Contains(err.Error(), "fixture stable") {
			t.Fatal("human error not localized or code lost", err)
		}
		var previous string
		for _, locale := range []string{"en", "ja"} {
			_, err, _ := portProposalCLI(t, locale, []string{"--json-errors", "service", "ports", "fixture", "--json"}, remote)
			var out bytes.Buffer
			writeCommandError(&out, err)
			if previous != "" && previous != out.String() {
				t.Fatal("JSON error changed with locale", out.String(), previous)
			}
			previous = out.String()
		}
	}
	if err := servicePortsCommand([]string{"--help"}, "", false, false, io.Discard, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestServicePortCLIFailedRestartPreservesSavedDisclosure(t *testing.T) {
	cause := &control.RemoteError{Code: "listener_conflict", Message: "saved but not started: the local port is already in use"}
	_, localized, calls := portProposalCLI(t, "ja", []string{"service", "restart", "fixture", "--local-port", "49152"}, cause)
	if len(calls) != 2 || calls[1].name != "service.connect" {
		t.Fatal("restart did not exercise actual request wrapper", calls)
	}
	if !strings.Contains(localized.Error(), "設定は保存されましたが、開始できませんでした") || !strings.Contains(localized.Error(), "使用中") {
		t.Fatal("Japanese output hid saved-but-not-started state", localized)
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(localized, &coded) || coded.ErrorCode() != "listener_conflict" {
		t.Fatal("failure code lost", localized)
	}
}

func TestServicePortCLIMalformedInputsHaveStableJSONErrors(t *testing.T) {
	for _, args := range [][]string{
		{"service", "ports", "fixture", "--from-port", "80"},
		{"service", "ports", "fixture", "--count", "-1"},
		{"service", "ports", "fixture", "--attempts", "-1"},
		{"service", "restart", "fixture", "--expected-revision", "bad"},
		{"service", "copy", "fixture", "--expected-revision", strings.Repeat("a", 64)},
	} {
		var previous string
		for _, locale := range []string{"en", "ja"} {
			_, err, calls := portProposalCLI(t, locale, append([]string{"--json-errors"}, args...), nil)
			if err == nil || len(calls) != 0 {
				t.Fatal("invalid input reached IPC", err, calls)
			}
			var out bytes.Buffer
			writeCommandError(&out, err)
			if strings.Contains(out.String(), `"code":"command_failed"`) || previous != "" && previous != out.String() {
				t.Fatal("malformed input changed machine error", out.String(), previous)
			}
			previous = out.String()
		}
	}
}

func TestServicePortCLIFailedRestartJSONIsLocaleIndependent(t *testing.T) {
	cause := &control.RemoteError{Code: "listener_conflict", Message: "saved but not started: the local port is already in use"}
	var previous string
	for _, locale := range []string{"en", "ja"} {
		_, err, _ := portProposalCLI(t, locale, []string{"--json-errors", "service", "restart", "fixture", "--local-port", "49152"}, cause)
		var out bytes.Buffer
		writeCommandError(&out, err)
		if !strings.Contains(out.String(), "saved but not started") || previous != "" && previous != out.String() {
			t.Fatal("failed restart changed machine JSON", out.String(), previous)
		}
		previous = out.String()
	}
}
