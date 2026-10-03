package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/webui"
)

type mockCLI struct {
	state    any
	saved    serviceConfiguration
	queries  int
	commands []webui.Command
}

func (m *mockCLI) call(_ context.Context, _ string, raw string, result any) error {
	var response any = map[string]any{"ok": true}
	if raw == "status" {
		m.queries++
		response = m.state
	} else {
		var command webui.Command
		if err := json.Unmarshal([]byte(raw), &command); err != nil {
			return err
		}
		if command.Name == "profile.export" {
			response = map[string]any{"profile": map[string]any{"services": []savedService{m.saved.Configuration}}}
		} else if command.Name == "service.config" {
			response = m.saved
			m.queries++
		} else {
			m.commands = append(m.commands, command)
		}
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, result)
}
func runMock(t *testing.T, m *mockCLI, args []string, stdin string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runWith(context.Background(), append([]string{"--state-dir", filepath.Join(t.TempDir(), "unused")}, args...), &out, strings.NewReader(stdin), m.call)
	return out.String(), err
}
func payloadOf(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
func previewOf(t *testing.T, out string) map[string]any {
	t.Helper()
	plan := payloadOf(t, []byte(out))
	if plan["applied"] != false || plan["validation"] != "local-input-only" {
		t.Fatalf("not an honest preview: %s", out)
	}
	value, ok := plan["payload"].(map[string]any)
	if !ok {
		t.Fatalf("bad preview: %s", out)
	}
	return value
}
func TestMockServicePresetsPreviewAndUnusedNames(t *testing.T) {
	m := &mockCLI{}
	out, err := runMock(t, m, []string{"--dry-run", "connect", "--preset", "ssh", "--peer", "peer-123", "--name", "ssh-entry"}, "")
	if err != nil {
		t.Fatal(err)
	}
	payload := previewOf(t, out)
	if payload["ports"] != "22" || payload["localPort"] != float64(2222) || payload["ttlSeconds"] != float64(0) || payload["lifetime"] != "until-stopped" || payload["peerId"] != "peer-123" {
		t.Fatalf("bad safe preset: %v", payload)
	}
	if m.queries != 0 || len(m.commands) != 0 {
		t.Fatal("pure preview contacted agent")
	}
	args := []string{"share", "--preset", "web", "--peers", "peer-123"}
	base, err := servicePayload("share", args[1:], false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	name := base["name"].(string)
	m.state = map[string]any{"shares": []any{map[string]string{"name": name}}, "services": []any{map[string]string{"name": name + "-2"}}}
	out, err = runMock(t, m, append([]string{"--dry-run"}, args...), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := previewOf(t, out)["name"]; got != name+"-3" {
		t.Fatalf("reused a saved name: %v", got)
	}
	if m.queries != 1 || len(m.commands) != 0 {
		t.Fatal("dry-run applied service")
	}
}
func TestMockInvalidServiceInputStopsBeforeIPC(t *testing.T) {
	cases := [][]string{
		{"share", "--ports", "80-22", "--peers", "peer"},
		{"share", "--ports", "0", "--peers", "peer"},
		{"share", "--ports", "80", "--exclude", "80", "--peers", "peer"},
		{"share", "--ports", "54543-54545", "--peers", "peer"},
		{"share", "--ports", "8000", "--peers", "peer,peer"},
		{"share", "--ports", "8000", "--peers", strings.Repeat("peer,", 33)},
		{"share", "--preset", "unknown", "--peers", "peer"},
		{"connect", "--ports", "22", "--peer", "peer"},
		{"connect", "--ports", "8000-8010", "--peer", "peer", "--local-port", "65530"},
		{"connect", "--preset", "ssh", "--peer", "peer", "--local-port", "100"},
		{"share", "--ports", "8000", "--network", "udp", "--peers", "peer", "--ttl", "500ms"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			m := &mockCLI{}
			if _, err := runMock(t, m, args, ""); err == nil {
				t.Fatal("invalid input accepted")
			}
			if m.queries != 0 || len(m.commands) != 0 {
				t.Fatal("invalid input reached agent")
			}
		})
	}
}
func TestMockPreferenceActionsNeverOverwriteUnrequestedFields(t *testing.T) {
	cases := []struct {
		args []string
		name string
		want map[string]any
	}{
		{[]string{"autosave", "peer-123", "--off"}, "peer.autosave", map[string]any{"peerId": "peer-123", "enabled": false}},
		{[]string{"autosave", "peer-123", "--on"}, "peer.autosave", map[string]any{"peerId": "peer-123", "enabled": true}},
		{[]string{"pause", "peer-123"}, "peer.autosave", map[string]any{"peerId": "peer-123", "paused": true}},
		{[]string{"resume", "peer-123"}, "peer.autosave", map[string]any{"peerId": "peer-123", "paused": false}},
		{[]string{"reconnect", "peer-123"}, "peer.reconnect", map[string]any{"peerId": "peer-123"}},
		{[]string{"receive-dir", "--clear"}, "settings.update", map[string]any{"receiveDirectory": ""}},
	}
	for _, test := range cases {
		m := &mockCLI{}
		if _, err := runMock(t, m, test.args, ""); err != nil {
			t.Fatal(err)
		}
		if m.queries != 0 || len(m.commands) != 1 {
			t.Fatalf("unexpected read-modify-write: %+v", m)
		}
		command := m.commands[0]
		if command.Name != test.name || !reflect.DeepEqual(payloadOf(t, command.Payload), test.want) {
			t.Fatalf("unrequested fields sent: %+v", command)
		}
	}
	for _, command := range []string{"receive-dir", "autosave"} {
		m := &mockCLI{}
		args := []string{command, "./incoming"}
		if command == "autosave" {
			args = []string{command, "peer-123", "--on", "--directory", "./incoming"}
		}
		if _, err := runMock(t, m, args, ""); err != nil {
			t.Fatal(err)
		}
		payload := payloadOf(t, m.commands[0].Payload)
		field := "receiveDirectory"
		if command == "autosave" {
			field = "directory"
		}
		expected, _ := filepath.Abs("./incoming")
		if payload[field] != expected {
			t.Fatalf("relative directory not resolved: %v", payload)
		}
		if _, ok := payload["paused"]; ok {
			t.Fatal("directory edit changed pause")
		}
	}
}
func TestMockLANPrivateInputAndPreviewRedaction(t *testing.T) {
	secret := "fixture-private-invitation"
	invitation, _ := json.Marshal(map[string]string{"token": secret})
	envelope, _ := json.Marshal(map[string]any{"invitation": string(invitation), "expires": "2030-01-01"})
	for _, input := range []string{string(invitation), string(envelope)} {
		m := &mockCLI{}
		out, err := runMock(t, m, []string{"--dry-run", "lan", "join", "--stdin"}, input)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, secret) || len(m.commands) != 0 {
			t.Fatal("preview leaked private input or paired")
		}
		payload := previewOf(t, out)
		if payload["invitation"] != "[private input omitted]" {
			t.Fatal("missing clear redaction")
		}
		if _, err = runMock(t, m, []string{"lan", "inspect", "--stdin"}, input); err != nil {
			t.Fatal(err)
		}
		got := payloadOf(t, m.commands[0].Payload)
		if got["invitation"] != string(invitation) {
			t.Fatal("private envelope not unwrapped exactly")
		}
	}
	m := &mockCLI{}
	_, err := runMock(t, m, []string{"lan", "join", string(invitation)}, "")
	if err == nil || strings.Contains(err.Error(), secret) || len(m.commands) != 0 {
		t.Fatal("private command-line argument accepted or echoed")
	}
	for _, args := range [][]string{
		{"setup", "--network", "lan", "--host", "0.0.0.0:54546"},
		{"setup", "--network", "lan", "--relay", "relay.example:443", "--certificate", strings.Repeat("a", 64)},
		{"setup", "--network", "tailnet", "--host", "192.168.20.10:54546"},
		{"setup", "--network", "lan", "--host", "192.168.20.10:80"},
		{"setup", "--network", "lan", "--relay", "192.168.20.10:54546"},
	} {
		if _, err = runMock(t, m, args, ""); err == nil {
			t.Fatal("invalid LAN setup accepted")
		}
	}
	out, err := runMock(t, m, []string{"--dry-run", "setup", "--network", "lan", "--host", "192.168.20.10:54546"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if previewOf(t, out)["lan"].(map[string]any)["kind"] != "host" {
		t.Fatal("wrong LAN mode")
	}
}
func TestMockSavedServiceCopyRestartAndGuardedScope(t *testing.T) {
	config := savedService{ID: "saved-id", Backend: "tailnet", Name: "example-web", Direction: "share", Network: "tcp", Ports: "8000-8010", ExcludePorts: "8005", PeerIDs: []string{"peer-123"}, TTLSeconds: 1800, Discoverable: true, Purpose: "web"}
	saved := serviceConfiguration{Configuration: config, Revision: strings.Repeat("a", 64)}
	for _, operation := range []string{"copy", "restart"} {
		m := &mockCLI{saved: saved, state: map[string]any{"shares": []any{map[string]string{"name": "example-web-copy"}}}}
		out, err := runMock(t, m, []string{"--dry-run", "service", operation, "saved-id"}, "")
		if err != nil {
			t.Fatal(err)
		}
		payload := previewOf(t, out)
		if payload["ports"] != "8000-8010" || payload["excludePorts"] != "8005" || payload["ttlSeconds"] != float64(1800) || payload["discoverable"] != true || payload["backend"] != "tailnet" {
			t.Fatalf("saved configuration lost: %v", payload)
		}
		if operation == "restart" {
			if payload["replaceId"] != "saved-id" || payload["expectedRevision"] != saved.Revision {
				t.Fatal("replacement lost concurrency guard")
			}
		} else {
			if payload["name"] != "example-web-copy-2" {
				t.Fatal("copy reused name")
			}
			if _, ok := payload["replaceId"]; ok {
				t.Fatal("copy became replacement")
			}
		}
		if _, ok := payload["id"]; ok {
			t.Fatal("internal saved ID sent as ordinary payload")
		}
		if len(m.commands) != 0 {
			t.Fatal("preview started a service")
		}
	}
	for _, edit := range []func(*mockCLI){
		func(m *mockCLI) { m.saved.Active = true },
		func(m *mockCLI) { m.saved.Configuration.Backend = "" },
		func(m *mockCLI) { m.saved.Revision = "" },
	} {
		m := &mockCLI{saved: saved}
		edit(m)
		if _, err := runMock(t, m, []string{"service", "restart", "saved-id"}, ""); err == nil || len(m.commands) != 0 {
			t.Fatal("unguarded restart accepted")
		}
	}
	m := &mockCLI{saved: saved}
	if _, err := runMock(t, m, []string{"service", "restart", "saved-id", "--backend", "lan"}, ""); err == nil {
		t.Fatal("known backend switched")
	}
}

func TestMockDiscoveredServiceCopyPreservesAuthenticatedScope(t *testing.T) {
	saved := serviceConfiguration{Configuration: savedService{ID: "saved-id", Backend: "lan", Name: "shared-api", Direction: "forward", Network: "tcp", Ports: "8000-8002", PeerID: "peer-123", ServiceID: "advertised-id", TTLSeconds: 1800, LocalPort: 18000}, Revision: strings.Repeat("a", 64)}
	for _, overrides := range [][]string{{"--ports", "8000"}, {"--peer", "peer-456"}, {"--network", "udp"}, {"--exclude", "8001"}} {
		m := &mockCLI{saved: saved}
		args := append([]string{"service", "copy", "saved-id"}, overrides...)
		if _, err := runMock(t, m, args, ""); err == nil || len(m.commands) != 0 {
			t.Fatal("changed scope silently retained or dropped its service identity")
		}
	}
	m := &mockCLI{saved: saved}
	out, err := runMock(t, m, []string{"--dry-run", "service", "copy", "saved-id", "--local-port", "19000"}, "")
	if err != nil {
		t.Fatal(err)
	}
	payload := previewOf(t, out)
	if payload["serviceId"] != "advertised-id" || payload["ports"] != "8000-8002" || payload["localPort"] != float64(19000) {
		t.Fatal("local mapping edit lost the authenticated target scope")
	}
}
func TestMockHelpIsOfflineAndBilingual(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		for _, args := range [][]string{{"start", "--help"}, {"autosave", "--help"}, {"lan", "--help"}, {"lan", "invite", "--help"}, {"service", "--help"}, {"service", "copy", "--help"}, {"help", "examples"}, {"help", "upgrade"}} {
			var out bytes.Buffer
			client := func(context.Context, string, string, any) error { return errors.New("help contacted IPC") }
			err := runWith(context.Background(), append([]string{"--locale", locale}, args...), &out, strings.NewReader(""), client)
			if err != nil {
				t.Fatal(err)
			}
			if out.Len() == 0 {
				t.Fatal("empty help")
			}
			if args[0] == "start" && !strings.Contains(out.String(), "offline") {
				t.Fatal("offline recovery hidden from contextual help")
			}
			if args[0] == "autosave" && !strings.Contains(out.String(), "PEER_ID") {
				t.Fatal("required peer hidden from autosave help")
			}
		}
	}
}
