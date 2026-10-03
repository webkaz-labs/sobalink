package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/core"
)

func TestHumanStatusAndPeersShowRuntimeScopeWithoutPrivateBodies(t *testing.T) {
	raw := `{"processId":123,"self":{"name":"example-node","status":"running"},"settings":{"network":"tailnet"},"peers":[{"id":"peer-one","name":"相手","address":"100.64.0.1","verified":true,"online":true}],"services":[{"id":"saved-a","name":"API","peerId":"peer-one","network":"tcp","ports":"8080","status":"active","endpoint":"127.0.0.1:18080","owner":"test-owner","leaseSeconds":30,"leaseExpiresAt":"2026-10-03T00:00:30Z","lifetime":"finite","ttlSeconds":7200,"expiresAt":"2026-10-03T02:00:00Z"}],"messages":[{"text":"private-body"}],"transfers":[{"private":"private-filename"}]} `
	for _, locale := range []string{"en", "ja"} {
		for _, command := range []string{"status", "peers"} {
			var out bytes.Buffer
			err := runWith(context.Background(), []string{"--locale", locale, command}, &out, panicReader{}, func(_ context.Context, _ string, command string, result any) error {
				if command != "status" {
					t.Fatal(command)
				}
				return json.Unmarshal([]byte(raw), result)
			})
			if err != nil {
				t.Fatal(err)
			}
			got := out.String()
			if json.Valid(out.Bytes()) || strings.Contains(got, "private-body") || strings.Contains(got, "private-filename") {
				t.Fatal(got)
			}
			if !strings.Contains(got, "相手") || !strings.Contains(got, "peer-one") {
				t.Fatal(got)
			}
			if command == "status" {
				for _, value := range []string{"127.0.0.1:18080", "test-owner", "7200", "2026-10-03T00:00:30Z"} {
					if !strings.Contains(got, value) {
						t.Fatal(value, got)
					}
				}
			} else if strings.Contains(got, "test-owner") {
				t.Fatal("peers printed service state", got)
			}
			if locale == "ja" && (!strings.Contains(got, "相手") || strings.Contains(got, "Current peers")) {
				t.Fatal(got)
			}
		}
	}
}
func TestStatusJSONPreservesUnknownFieldsAndPeerJSONIsFocused(t *testing.T) {
	raw := `{"number":9007199254740993,"peers":[{"id":"peer-one","name":"日本語","unknown":9007199254740993}],"messages":[{"text":"private"}]}`
	for _, command := range []string{"status", "peers"} {
		var out bytes.Buffer
		err := snapshotCommand(context.Background(), command, []string{"--json"}, "", true, &out, func(_ context.Context, _ string, _ string, result any) error {
			return json.Unmarshal([]byte(raw), result)
		})
		if err != nil {
			t.Fatal(err)
		}
		got := out.String()
		if !json.Valid(out.Bytes()) || !strings.Contains(got, "9007199254740993") || !strings.Contains(got, "日本語") || strings.Contains(got, "\x1b") {
			t.Fatal(got)
		}
		if command == "peers" && strings.Contains(got, "private") {
			t.Fatal("peers JSON exposed unrelated state")
		}
	}
}
func TestHumanStatusReportsSavedWithoutInventingReadiness(t *testing.T) {
	var out bytes.Buffer
	writeHumanService(&out, true, humanService{ID: "saved", Name: "example", Status: "saved", Network: "tcp", Ports: "22", PeerID: "one", LocalPort: 2222, LoopbackHost: "127.0.0.1", Lifetime: "until-stopped"}, nil)
	got := out.String()
	if !strings.Contains(got, "保存済み") || !strings.Contains(got, "待受の準備は未確認") || strings.Contains(got, "実際の接続先") {
		t.Fatal(got)
	}
}
func TestServiceReferencesResolveNamesRejectAmbiguityAndDuplicateIdentity(t *testing.T) {
	services := []core.ServiceSpec{{ID: "id-one", Name: "日本語API"}, {ID: "id-two", Name: "api"}, {ID: "api", Name: "other"}}
	query := func(name string, _ any, out any) error {
		if name != "profile.export" {
			t.Fatal(name)
		}
		raw, _ := json.Marshal(map[string]any{"profile": core.DefinitionBundle{Services: services}})
		return json.Unmarshal(raw, out)
	}
	got, err := resolveServiceReferences([]string{"日本語API", "id-two"}, false, query)
	if err != nil || strings.Join(got.IDs, ",") != "id-one,id-two" {
		t.Fatal(got, err)
	}
	for _, refs := range [][]string{{"api"}, {"日本語API", "id-one"}, {"見つからない"}} {
		if _, err := resolveServiceReferences(refs, true, query); err == nil {
			t.Fatal(refs)
		}
	}
	if err := checkResolvedName(got, "id-one", "renamed", true); err == nil {
		t.Fatal("name changed during read")
	}
}
func TestNamedPeerFlagsResolveOnlyExactAuthenticatedNames(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		m := &mockCLI{state: map[string]any{"peers": []guidedPeer{{ID: "id-one", Name: "相手", Verified: true}}}}
		out, err := runMock(t, m, []string{"--locale", locale, "--dry-run", "connect", "--name", "api", "--ports", "8080", "--peer-name", "相手"}, "")
		if err != nil {
			t.Fatal(err)
		}
		payload := previewOf(t, out)
		if payload["peerId"] != "id-one" || payload["peerName"] != nil {
			t.Fatal(payload)
		}
		if len(m.commands) != 0 {
			t.Fatal("preview mutated state")
		}
	}
	for _, peers := range [][]guidedPeer{{{ID: "one", Name: "same", Verified: true}, {ID: "two", Name: "same", Verified: true}}, {{ID: "one", Name: "same", Verified: false}}} {
		m := &mockCLI{state: map[string]any{"peers": peers}}
		if _, err := runMock(t, m, []string{"connect", "--name", "api", "--ports", "8080", "--peer-name", "same"}, ""); err == nil || len(m.commands) != 0 {
			t.Fatal(err, m.commands)
		}
	}
	m := &mockCLI{}
	if _, err := runMock(t, m, []string{"connect", "--name", "api", "--ports", "8080", "--peer", "id-one", "--peer-name", "same"}, ""); err == nil || m.queries != 0 {
		t.Fatal("conflicting selectors reached network", err)
	}
}
func TestFriendlyServiceWorkflowKeepsResolvedIdentityAndMachineScope(t *testing.T) {
	m := newWorkflowMock()
	m.selection.Services[0].Name = "日本語API"
	w, out := testWorkflow(t, m)
	if err := w.services([]string{"start", "日本語API", "--json", "--owner", "fixture"}); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out.Bytes()) {
		t.Fatal(out.String())
	}
	calls := m.history()
	last := calls[len(calls)-1]
	if last.payload["ids"].([]any)[0] != "saved-a" {
		t.Fatal(last)
	}
	out.Reset()
	if err := w.services([]string{"wait", "日本語API", "--owner", "fixture"}); err != nil {
		t.Fatal(err)
	}
	if json.Valid(out.Bytes()) || !strings.Contains(out.String(), "日本語API") {
		t.Fatal(out.String())
	}
}
func TestGroupHumanListAndShareReviewUseNamesWithoutJSON(t *testing.T) {
	m := newWorkflowMock()
	m.selection.Services[0].Name = "日本語API"
	m.groups = []workflowGroup{{Name: "まとめ", ServiceIDs: []string{"saved-a"}}}
	w, out := testWorkflow(t, m)
	w.ja = true
	if err := w.group([]string{"list"}); err != nil {
		t.Fatal(err)
	}
	if json.Valid(out.Bytes()) || !strings.Contains(out.String(), "日本語API") || !strings.Contains(out.String(), "まとめ") {
		t.Fatal(out.String())
	}
	out.Reset()
	m.selection.Services[0].Direction = "share"
	m.selection.Services[0].PeerIDs = []string{"peer-fixture"}
	w.in = strings.NewReader("q\n")
	if err := w.services([]string{"start", "日本語API"}); err == nil {
		t.Fatal("canceled review started")
	}
	if strings.Contains(out.String(), `"peerIds"`) || !strings.Contains(out.String(), "対象の相手") || m.started {
		t.Fatal(out.String())
	}
}
func TestNamedServiceShowAndSettingsPreserveExplicitJSON(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runWith(context.Background(), append([]string{"--state-dir", dir, "--offline"}, args...), &out, panicReader{}, func(context.Context, string, string, any) error { return errors.New("unexpected IPC") })
		return out.String(), err
	}
	if _, err := run("service", "save", "connect", "--backend", "tailnet", "--name", "日本語API", "--peer", "peer-example", "--ports", "8080"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"service", "show", "日本語API"}, {"settings", "日本語API"}, {"service", "show", "日本語API", "--json"}, {"settings", "日本語API", "--json"}} {
		out, err := run(args...)
		if err != nil || !strings.Contains(out, "日本語API") {
			t.Fatal(args, err, out)
		}
		if json.Valid([]byte(out)) != hasFlag(args, "json") {
			t.Fatal(args, out)
		}
	}
}
func TestInvalidHumanStatusDoesNotQuery(t *testing.T) {
	var out bytes.Buffer
	if err := snapshotCommand(context.Background(), "status", []string{"invalid"}, "", false, &out, func(context.Context, string, string, any) error { t.Fatal("invalid args queried"); return nil }); err == nil {
		t.Fatal("invalid status accepted")
	}
}

func TestHumanStatusShowsLocalizedFailureHistoryAndNativeNetworkState(t *testing.T) {
	var service humanService
	if err := json.Unmarshal([]byte(`{"id":"example","name":"Example","status":"failed","lastFailure":{"code":"listener_unavailable","at":"2026-10-03T00:00:00Z","nextSteps":{"en":"Review the local port.","ja":"入口ポートを確認してください。"}},"diagnostic":{"code":"tcp_unreachable","transport":"unconfirmed","checkedAt":"2026-10-03T00:01:00Z","nextSteps":{"en":"Check the application.","ja":"アプリを確認してください。"}}}`), &service); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	writeHumanService(&out, true, service, nil)
	for _, value := range []string{"直近の失敗の記録", "listener_unavailable", "入口ポートを確認", "直近の明示的な確認", "アプリを確認"} {
		if !strings.Contains(out.String(), value) {
			t.Fatal(out.String())
		}
	}
	if got := humanNetworkState(true, "Running"); !strings.Contains(got, "稼働中") {
		t.Fatal(got)
	}
	out.Reset()
	if err := snapshotCommand(context.Background(), "status", nil, "/tmp/example", true, &out, func(_ context.Context, _ string, _ string, result any) error {
		return json.Unmarshal([]byte(`{"processId":1,"self":{"status":"NeedsLogin"}}`), result)
	}); err != nil || !strings.Contains(out.String(), "ログインが必要") || !strings.Contains(out.String(), "login --link --wait") {
		t.Fatal(err, out.String())
	}
}
