package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

type workflowCall struct {
	name    string
	payload map[string]any
}

type workflowMock struct {
	mu        sync.Mutex
	calls     []workflowCall
	selection workflowSelection
	groups    []workflowGroup
	owner     string
	started   bool
	onCall    func(context.Context, string, map[string]any) (any, error, bool)
}

func newWorkflowMock() *workflowMock {
	return &workflowMock{selection: workflowSelection{
		Services: []core.ServiceSpec{{ID: "saved-a", Backend: "tailnet", Name: "fixture", Direction: "forward", Network: "tcp", Ports: "8080", LocalPort: 18080, LoopbackHost: "127.0.0.1", Lifetime: "until-stopped", PeerID: "peer-fixture", Purpose: "custom"}},
		Revision: strings.Repeat("a", 64),
	}}
}

func (m *workflowMock) call(ctx context.Context, _ string, raw string, out any) error {
	var request webui.Command
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Payload, &payload); err != nil {
		return err
	}
	if request.Name == "profile.export" {
		m.mu.Lock()
		defer m.mu.Unlock()
		return workflowDecode(map[string]any{"profile": core.DefinitionBundle{Version: 1, Services: m.selection.Services}}, out)
	}
	m.mu.Lock()
	m.calls = append(m.calls, workflowCall{request.Name, payload})
	onCall := m.onCall
	m.mu.Unlock()
	if onCall != nil {
		value, err, handled := onCall(ctx, request.Name, payload)
		if handled {
			if err != nil {
				return err
			}
			return workflowDecode(value, out)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	selection := m.selection
	if group, _ := payload["group"].(string); group != "" {
		selection.Group = group
	}
	switch request.Name {
	case "service.selection":
		return workflowDecode(selection, out)
	case "group.list":
		return workflowDecode(map[string]any{"groups": m.groups, "revision": strings.Repeat("b", 64)}, out)
	case "services.start":
		m.owner, _ = payload["owner"].(string)
		m.started = true
	case "services.stop":
		m.started = false
	case "services.ready", "services.renew", "group.save", "service.stop-shares":
	default:
		return errors.New("unexpected fixture command: " + request.Name)
	}
	selection.States = nil
	selection.Ready = m.started
	for _, service := range selection.Services {
		state := "saved"
		if m.started {
			state = "active"
		}
		selection.States = append(selection.States, workflowState{ID: service.ID, Status: state, Owner: m.owner})
	}
	return workflowDecode(selection, out)
}

func workflowDecode(value, out any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, out)
}

func (m *workflowMock) history() []workflowCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]workflowCall(nil), m.calls...)
}

func (m *workflowMock) names() []string {
	var names []string
	for _, call := range m.history() {
		names = append(names, call.name)
	}
	return names
}

func testWorkflow(t *testing.T, m *workflowMock) (*workflowCLI, *bytes.Buffer) {
	t.Helper()
	out := new(bytes.Buffer)
	return &workflowCLI{ctx: context.Background(), dir: t.TempDir(), out: out, in: strings.NewReader(""), client: m.call, runProcess: func(context.Context, []string, io.Reader, io.Writer) error { return nil }, poll: time.Millisecond, renewEvery: time.Hour, cleanupTimeout: time.Second}, out
}

func TestWorkflowHelpIsBilingualAndOffline(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, command := range []string{"group", "services", "wait-ready", "task", "stop-shares"} {
			var out bytes.Buffer
			handled, err := workflowCommand(context.Background(), command, []string{"--help"}, "unused", ja, false, &out, strings.NewReader(""), func(context.Context, string, string, any) error { t.Fatal("help contacted Core"); return nil })
			if !handled || err != nil && !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("%s: %v", command, err)
			}
			if ja && !strings.Contains(out.String(), "使い方") || !ja && !strings.Contains(out.String(), "Usage:") {
				t.Fatalf("unlocalized help: %s", out.String())
			}
		}
	}
}

func TestWorkflowRejectsBadInputsBeforeQuery(t *testing.T) {
	for _, input := range []struct {
		command string
		args    []string
	}{
		{"services", []string{"start"}},
		{"services", []string{"start", "saved-a", "saved-a"}},
		{"services", []string{"start", "saved-a", "--group", "fixture"}},
		{"services", []string{"wait", "saved-a", "--timeout", "0s"}},
		{"services", []string{"wait", "saved-a", "--timeout", "25h"}},
		{"services", []string{"wait", "saved-a", "--unknown"}},
		{"group", []string{"save", "bad/name", "saved-a"}},
		{"group", []string{"save", "fixture"}},
		{"group", []string{"start", "fixture", "second"}},
		{"task", []string{"--services", "saved-a", "command"}},
		{"task", []string{"--services", "saved-a", "--"}},
		{"task", []string{"--services", "saved-a", "--group", "fixture", "--", "command"}},
		{"task", []string{"--services", "saved-a", "--timeout", "-1s", "--", "command"}},
		{"task", []string{"--services", "saved-a,", "--", "command"}},
	} {
		t.Run(input.command+strings.Join(input.args, "_"), func(t *testing.T) {
			_, err := workflowCommand(context.Background(), input.command, input.args, "unused", false, false, io.Discard, strings.NewReader(""), func(context.Context, string, string, any) error { t.Fatal("invalid input contacted Core"); return nil })
			if err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestGroupSaveUsesProfileRevisionAndExplicitReplacement(t *testing.T) {
	m := newWorkflowMock()
	w, _ := testWorkflow(t, m)
	m.groups = []workflowGroup{{Name: "fixture", ServiceIDs: []string{"saved-a"}}}
	if err := w.group([]string{"save", "fixture", "saved-a"}); err == nil || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("replacement accepted: %v", err)
	}
	if !reflect.DeepEqual(m.names(), []string{"group.list"}) {
		t.Fatalf("unexpected calls: %v", m.names())
	}
	if err := w.group([]string{"save", "fixture", "saved-a", "--replace"}); err != nil {
		t.Fatal(err)
	}
	calls := m.history()
	last := calls[len(calls)-1]
	if last.name != "group.save" || last.payload["expectedRevision"] != strings.Repeat("b", 64) {
		t.Fatalf("group used wrong revision: %#v", last)
	}
	group := last.payload["group"].(map[string]any)
	if group["name"] != "fixture" || !reflect.DeepEqual(group["serviceIds"], []any{"saved-a"}) {
		t.Fatalf("group changed: %#v", group)
	}
}

func TestWorkflowDryRunIsStableAndPreservesSavedScope(t *testing.T) {
	var outputs []map[string]any
	for _, ja := range []bool{false, true} {
		m := newWorkflowMock()
		m.selection.Services[0].Direction, m.selection.Services[0].Lifetime = "share", "until-revoked"
		m.selection.Services[0].PeerID, m.selection.Services[0].PeerIDs = "", []string{"peer-fixture"}
		w, out := testWorkflow(t, m)
		w.dryRun, w.ja = true, ja
		if err := w.services([]string{"start", "saved-a"}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(m.names(), []string{"service.selection"}) {
			t.Fatalf("preview mutated: %v", m.names())
		}
		var result map[string]any
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, result)
		selection := result["selection"].(map[string]any)
		service := selection["services"].([]any)[0].(map[string]any)
		if service["lifetime"] != "until-revoked" || service["localPort"] != float64(18080) || service["ttlSeconds"] != float64(0) {
			t.Fatalf("saved scope changed: %v", service)
		}
	}
	if !reflect.DeepEqual(outputs[0], outputs[1]) {
		t.Fatal("machine contract changed with locale")
	}
}

func TestWorkflowShareConfirmationAndGroupSelection(t *testing.T) {
	m := newWorkflowMock()
	m.selection.Services[0].Direction = "share"
	w, out := testWorkflow(t, m)
	if err := w.services([]string{"start", "saved-a", "--json"}); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("JSON share accepted without confirmation: %v", err)
	}
	if len(out.Bytes()) != 0 {
		t.Fatalf("JSON mode wrote a prompt: %s", out)
	}
	if err := w.group([]string{"start", "fixture", "--confirm", "--json"}); err != nil {
		t.Fatal(err)
	}
	last := m.history()[len(m.history())-1]
	if last.name != "services.start" || last.payload["group"] != "fixture" || last.payload["ids"] != nil || last.payload["expectedRevision"] != m.selection.Revision {
		t.Fatalf("group scope lost: %+v", last)
	}
}

func TestWorkflowSelectionRejectsChangedOrIncompleteScope(t *testing.T) {
	for _, mutate := range []func(*workflowMock){
		func(m *workflowMock) { m.selection.Services = nil },
		func(m *workflowMock) { m.selection.Revision = "" },
		func(m *workflowMock) { m.selection.Services[0].ID = "saved-other" },
		func(m *workflowMock) { m.selection.Services[0].Direction = "unknown" },
	} {
		m := newWorkflowMock()
		mutate(m)
		w, _ := testWorkflow(t, m)
		if err := w.services([]string{"start", "saved-a", "--confirm"}); err == nil {
			t.Fatal("incomplete selection accepted")
		}
		if !reflect.DeepEqual(m.names(), []string{"service.selection"}) {
			t.Fatalf("changed selection mutated: %v", m.names())
		}
	}
}

func TestTaskRunsExactArgumentsAndPreservesInputAfterConfirmation(t *testing.T) {
	m := newWorkflowMock()
	m.selection.Services[0].Direction = "share"
	w, _ := testWorkflow(t, m)
	w.in = strings.NewReader("yes\nchild input\n")
	argv := []string{"fixture-command", "one argument", "$(literal)", ";", "--help"}
	w.runProcess = func(_ context.Context, received []string, in io.Reader, _ io.Writer) error {
		if !reflect.DeepEqual(received, argv) {
			t.Fatalf("argv changed: %q", received)
		}
		input, err := io.ReadAll(in)
		if err != nil || string(input) != "child input\n" {
			t.Fatalf("input lost: %q %v", input, err)
		}
		if !reflect.DeepEqual(m.names(), []string{"service.selection", "services.start", "services.ready"}) {
			t.Fatalf("process ran before readiness: %v", m.names())
		}
		return nil
	}
	if err := w.task(append([]string{"--group", "fixture", "--"}, argv...)); err != nil {
		t.Fatal(err)
	}
	calls := m.history()
	start, stop := calls[1], calls[len(calls)-1]
	owner, _ := start.payload["owner"].(string)
	if !strings.HasPrefix(owner, "task-") || len(owner) != 37 || start.payload["leaseSeconds"] != float64(30) {
		t.Fatalf("invalid task lease: %v", start.payload)
	}
	if start.payload["group"] != "fixture" || stop.payload["group"] != nil || stop.payload["owner"] != owner || !reflect.DeepEqual(stop.payload["ids"], []any{"saved-a"}) {
		t.Fatalf("cleanup not pinned to owned IDs: %v", stop.payload)
	}
}

func TestTaskDryRunDoesNotExecuteOrAcquireLease(t *testing.T) {
	m := newWorkflowMock()
	w, out := testWorkflow(t, m)
	w.dryRun = true
	w.runProcess = func(context.Context, []string, io.Reader, io.Writer) error { t.Fatal("dry run executed"); return nil }
	if err := w.task([]string{"--services", "saved-a", "--", "fixture-command", "literal;value"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.names(), []string{"service.selection"}) {
		t.Fatalf("dry run mutated: %v", m.names())
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["applied"] != false || result["start"].(map[string]any)["owner"] != nil || !reflect.DeepEqual(result["argv"], []any{"fixture-command", "literal;value"}) {
		t.Fatalf("invalid preview: %v", result)
	}
}

func TestTaskCleanupAfterStartOrChildFailure(t *testing.T) {
	failure := errors.New("fixture failure")
	for _, failStart := range []bool{false, true} {
		m := newWorkflowMock()
		w, _ := testWorkflow(t, m)
		if failStart {
			m.onCall = func(_ context.Context, name string, _ map[string]any) (any, error, bool) {
				return nil, failure, name == "services.start"
			}
		}
		w.runProcess = func(context.Context, []string, io.Reader, io.Writer) error {
			if failStart {
				t.Fatal("child ran after uncertain start")
			}
			return failure
		}
		if err := w.task([]string{"--services", "saved-a", "--", "fixture-command"}); !errors.Is(err, failure) {
			t.Fatalf("failure lost: %v", err)
		}
		calls := m.history()
		if last := calls[len(calls)-1]; last.name != "services.stop" || last.payload["owner"] != calls[1].payload["owner"] {
			t.Fatalf("cleanup missing or wrong owner: %v", calls)
		}
	}
}

func TestTaskTimeoutAndReadinessFailureNeverRunChild(t *testing.T) {
	for _, status := range []string{"reconnecting", "failed", "missing", "wrong-owner"} {
		t.Run(status, func(t *testing.T) {
			m := newWorkflowMock()
			w, _ := testWorkflow(t, m)
			m.onCall = func(_ context.Context, name string, payload map[string]any) (any, error, bool) {
				if name != "services.ready" {
					return nil, nil, false
				}
				owner, _ := payload["owner"].(string)
				state := workflowState{ID: "saved-a", Status: status, Owner: owner}
				if status == "wrong-owner" {
					state.Status, state.Owner = "active", "task-other"
				}
				states := []workflowState{state}
				if status == "missing" {
					states = nil
				}
				return workflowSelection{Ready: false, States: states}, nil, true
			}
			w.runProcess = func(context.Context, []string, io.Reader, io.Writer) error {
				t.Fatal("child ran without readiness")
				return nil
			}
			err := w.task([]string{"--services", "saved-a", "--timeout", "5ms", "--", "fixture-command"})
			if err == nil {
				t.Fatal("readiness failure ignored")
			}
			if status == "reconnecting" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline lost: %v", err)
			}
			calls := m.history()
			if calls[len(calls)-1].name != "services.stop" {
				t.Fatal("failure skipped cleanup")
			}
		})
	}
}

func TestTaskRenewsAndStopsOnLostLease(t *testing.T) {
	m := newWorkflowMock()
	w, _ := testWorkflow(t, m)
	w.renewEvery = time.Millisecond
	lost := errors.New("fixture lease lost")
	m.onCall = func(_ context.Context, name string, payload map[string]any) (any, error, bool) {
		if name != "services.renew" {
			return nil, nil, false
		}
		if payload["leaseSeconds"] != float64(30) || !strings.HasPrefix(payload["owner"].(string), "task-") || !reflect.DeepEqual(payload["ids"], []any{"saved-a"}) {
			t.Errorf("renewal changed lease: %v", payload)
		}
		return nil, lost, true
	}
	w.runProcess = func(ctx context.Context, _ []string, _ io.Reader, _ io.Writer) error { <-ctx.Done(); return ctx.Err() }
	if err := w.task([]string{"--services", "saved-a", "--", "fixture-command"}); !errors.Is(err, lost) {
		t.Fatalf("lease failure lost: %v", err)
	}
	calls := m.history()
	if calls[len(calls)-1].name != "services.stop" {
		t.Fatal("lost lease skipped cleanup")
	}
}

func TestTaskCancellationUsesFreshBoundedCleanupContext(t *testing.T) {
	m := newWorkflowMock()
	w, _ := testWorkflow(t, m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.ctx = ctx
	cleanupCalled := false
	m.onCall = func(ctx context.Context, name string, _ map[string]any) (any, error, bool) {
		if name != "services.stop" {
			return nil, nil, false
		}
		cleanupCalled = true
		deadline, bounded := ctx.Deadline()
		if ctx.Err() != nil || !bounded || time.Until(deadline) > time.Second {
			t.Error("cleanup did not use a fresh bounded context")
		}
		return nil, nil, false
	}
	w.runProcess = func(ctx context.Context, _ []string, _ io.Reader, _ io.Writer) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}
	if err := w.task([]string{"--services", "saved-a", "--", "fixture-command"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if !cleanupCalled {
		t.Fatal("cancellation skipped cleanup")
	}
}

func TestTaskCleanupFailurePreservesProcessFailure(t *testing.T) {
	m := newWorkflowMock()
	w, _ := testWorkflow(t, m)
	childFailure, cleanupFailure := errors.New("fixture child failure"), errors.New("fixture cleanup failure")
	m.onCall = func(_ context.Context, name string, _ map[string]any) (any, error, bool) {
		return nil, cleanupFailure, name == "services.stop"
	}
	w.runProcess = func(context.Context, []string, io.Reader, io.Writer) error { return childFailure }
	err := w.task([]string{"--services", "saved-a", "--", "fixture-command"})
	if !errors.Is(err, childFailure) || !errors.Is(err, cleanupFailure) || !strings.Contains(err.Error(), "30 seconds") {
		t.Fatalf("failure detail lost: %v", err)
	}
}

func TestWorkflowConfirmationCancellation(t *testing.T) {
	reader, writer := net.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := workflowConfirmation(ctx, reader); result <- err }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation ignored: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("confirmation blocked after cancellation")
	}
}

func TestWorkflowProcessPreservesArgumentAndInputBytes(t *testing.T) {
	if os.Getenv("SOBA_WORKFLOW_TEST_HELPER") == "1" {
		separator := -1
		for i, arg := range os.Args {
			if arg == "--" {
				separator = i
				break
			}
		}
		if separator < 0 {
			os.Exit(2)
		}
		input, _ := io.ReadAll(os.Stdin)
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"args": os.Args[separator+1:], "input": string(input)})
		os.Exit(0)
	}
	t.Setenv("SOBA_WORKFLOW_TEST_HELPER", "1")
	args := []string{"two words", "$(literal)", ";", "日本語", "--flag=value"}
	argv := append([]string{os.Args[0], "-test.run=^TestWorkflowProcessPreservesArgumentAndInputBytes$", "--"}, args...)
	var out bytes.Buffer
	if err := runWorkflowProcess(context.Background(), argv, strings.NewReader("fixture input\n"), &out); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args  []string
		Input string
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid child output: %q %v", out.String(), err)
	}
	if !reflect.DeepEqual(got.Args, args) || got.Input != "fixture input\n" {
		t.Fatalf("process bytes changed: %+v", got)
	}
}

func TestWorkflowProcessCancellation(t *testing.T) {
	if os.Getenv("SOBA_WORKFLOW_CANCEL_HELPER") == "1" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	t.Setenv("SOBA_WORKFLOW_CANCEL_HELPER", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := runWorkflowProcess(ctx, []string{os.Args[0], "-test.run=^TestWorkflowProcessCancellation$"}, strings.NewReader(""), io.Discard)
	var exit *exec.ExitError
	if !errors.Is(err, context.DeadlineExceeded) && !errors.As(err, &exit) {
		t.Fatalf("process cancellation failed: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("child stopped before cancellation")
	}
}

func TestWorkflowCommandsAreIntegrated(t *testing.T) {
	for _, command := range [][]string{
		{"group", "list", "--json"},
		{"services", "start", "saved-a", "--json"},
		{"stop-shares", "--json"},
		{"--dry-run", "task", "--services", "saved-a", "--", "fixture-command"},
	} {
		m := newWorkflowMock()
		var out bytes.Buffer
		args := append([]string{"--locale", "en", "--state-dir", t.TempDir()}, command...)
		if err := runWith(context.Background(), args, &out, strings.NewReader(""), m.call); err != nil {
			t.Fatalf("%v: %v", command, err)
		}
		if !json.Valid(out.Bytes()) {
			t.Fatalf("%v did not return JSON: %s", command, out.String())
		}
	}
	for _, command := range []string{"group", "services", "task", "wait-ready", "stop-shares"} {
		for _, args := range [][]string{{"help", command}, {command, "--help"}} {
			var out bytes.Buffer
			if err := runWith(context.Background(), append([]string{"--locale", "ja"}, args...), &out, strings.NewReader(""), func(context.Context, string, string, any) error { t.Fatal("help contacted Core"); return nil }); err != nil {
				t.Fatalf("%v: %v", args, err)
			}
			if !strings.Contains(out.String(), "使い方") {
				t.Fatalf("help locale lost: %s", out.String())
			}
		}
	}
}

func TestWorkflowReadinessPreservesEndpointAndVerificationFields(t *testing.T) {
	m := newWorkflowMock()
	w, out := testWorkflow(t, m)
	m.onCall = func(_ context.Context, name string, _ map[string]any) (any, error, bool) {
		if name != "services.ready" {
			return nil, nil, false
		}
		return map[string]any{"ready": true, "application": "unverified", "states": []map[string]any{{"id": "saved-a", "status": "active", "owner": "task-fixture", "endpoint": "127.0.0.1:18080", "leaseExpiresAt": "2026-01-01T00:00:30Z"}}}, nil, true
	}
	if err := w.services([]string{"wait", "saved-a", "--json"}); err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(out.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	state := output["states"].([]any)[0].(map[string]any)
	if output["application"] != "unverified" || state["endpoint"] != "127.0.0.1:18080" || state["leaseExpiresAt"] != "2026-01-01T00:00:30Z" {
		t.Fatalf("readiness fields discarded: %v", output)
	}
}

func TestTaskRenewalAlsoRunsWhileWaitingForReadiness(t *testing.T) {
	m := newWorkflowMock()
	w, _ := testWorkflow(t, m)
	w.renewEvery = time.Millisecond
	renewed := make(chan struct{})
	var once sync.Once
	m.onCall = func(ctx context.Context, name string, payload map[string]any) (any, error, bool) {
		if name == "services.renew" {
			once.Do(func() { close(renewed) })
			return nil, nil, false
		}
		if name != "services.ready" {
			return nil, nil, false
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err(), true
		case <-renewed:
		}
		return workflowSelection{Ready: true, States: []workflowState{{ID: "saved-a", Status: "active", Owner: payload["owner"].(string)}}}, nil, true
	}
	if err := w.task([]string{"--services", "saved-a", "--timeout", "1s", "--", "fixture-command"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.names(), "services.renew") {
		t.Fatal("readiness wait was not renewed")
	}
}
