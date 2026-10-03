package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestDefinitionCLISaveOfflinePreviewAndExplicitReplacement(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		m := &mockCLI{}
		out, err := runMock(t, m, []string{"--locale", locale, "--dry-run", "service", "save", "share", "--backend", "tailnet", "--name", "example", "--ports", "8080", "--peers", "peer-example"}, "")
		if err != nil {
			t.Fatal(err)
		}
		input := previewOf(t, out)
		configuration := input["configuration"].(map[string]any)
		if configuration["direction"] != "share" || configuration["backend"] != "tailnet" || len(m.commands) != 0 || m.queries != 0 {
			t.Fatal("save-only preview activated or queried a network")
		}
		if _, err := runMock(t, m, []string{"service", "save", "connect", "--ports", "8080", "--peer", "peer-example"}, ""); err == nil {
			t.Fatal("offline save inferred missing backend")
		}
		m.saved = serviceConfiguration{Configuration: savedService{ID: "example-id"}, Revision: strings.Repeat("a", 64)}
		out, err = runMock(t, m, []string{"--dry-run", "service", "save", "connect", "--replace", "example-id", "--backend", "tailnet", "--ports", "8080", "--peer", "peer-example"}, "")
		if err != nil {
			t.Fatal(err)
		}
		input = previewOf(t, out)
		if input["expectedRevision"] != m.saved.Revision || input["configuration"].(map[string]any)["id"] != "example-id" {
			t.Fatal("replacement lost review guard")
		}
	}
}

func TestProfileCLIExportUsesPrivateNewFileAndImportReview(t *testing.T) {
	state := t.TempDir()
	bundle := core.DefinitionBundle{Version: 1, Services: []core.ServiceSpec{{ID: "example-id", Backend: "tailnet", Name: "example", Direction: "share", Network: "tcp", Ports: "8080", PeerIDs: []string{"peer-example"}, Lifetime: "until-revoked"}}}
	revision := strings.Repeat("b", 64)
	var commands []webui.Command
	client := func(_ context.Context, _ string, raw string, result any) error {
		var command webui.Command
		if err := json.Unmarshal([]byte(raw), &command); err != nil {
			return err
		}
		commands = append(commands, command)
		response := map[string]any{"profile": bundle, "revision": revision, "disabled": true, "preservesIdentity": true, "replacesServices": 0}
		b, _ := json.Marshal(response)
		return json.Unmarshal(b, result)
	}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runWith(context.Background(), append([]string{"--state-dir", state}, args...), &out, strings.NewReader(""), client)
		return out.String(), err
	}
	path := filepath.Join(t.TempDir(), "example.json")
	if _, err := run("profile", "export", "--output", path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatal("export was not private", err)
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(b), "revision") || strings.Contains(string(b), "settings") {
		t.Fatal("export included local state envelope", err)
	}
	if _, err := run("profile", "export", "--output", path); err == nil {
		t.Fatal("existing output silently replaced")
	}
	if _, err := run("profile", "export", "--output", filepath.Join(state, "unused.json")); err == nil {
		t.Fatal("export wrote into application private state")
	}
	commands = nil
	if _, err := run("profile", "import", path); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Name != "profile.import.preview" {
		t.Fatal("import preview wrote definitions")
	}
	if _, err := run("profile", "import", path, "--apply", "--review", strings.Repeat("c", 64)); err == nil {
		t.Fatal("stale import review applied")
	}
	if _, err := run("profile", "import", path, "--apply", "--review", revision); err != nil {
		t.Fatal(err)
	}
	if commands[len(commands)-1].Name != "profile.import" {
		t.Fatal("reviewed import missing")
	}
}

func TestDefinitionCLIDeletePreviewsEveryGroupEffect(t *testing.T) {
	revision := strings.Repeat("c", 64)
	var commands []webui.Command
	client := func(_ context.Context, _ string, raw string, result any) error {
		var command webui.Command
		if err := json.Unmarshal([]byte(raw), &command); err != nil {
			return err
		}
		commands = append(commands, command)
		var response any = map[string]any{"deleted": true}
		switch command.Name {
		case "service.config":
			response = serviceConfiguration{Configuration: savedService{ID: "example-id"}, Revision: strings.Repeat("a", 64), Active: true}
		case "group.list":
			response = map[string]any{"groups": []core.ServiceGroup{{Name: "example", ServiceIDs: []string{"example-id"}}}, "revision": revision}
		}
		b, _ := json.Marshal(response)
		return json.Unmarshal(b, result)
	}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runWith(context.Background(), append([]string{"--state-dir", t.TempDir(), "service", "delete", "example-id"}, args...), &out, strings.NewReader(""), client)
		return out.String(), err
	}
	out, err := run("--stop-active", "--remove-from-groups")
	if err != nil || !strings.Contains(out, `"active": true`) || !strings.Contains(out, "example") {
		t.Fatal("missing effects preview", err, out)
	}
	for _, command := range commands {
		if command.Name == "service.delete" {
			t.Fatal("preview deleted definition")
		}
	}
	if _, err := run("--stop-active", "--remove-from-groups", "--apply", "--review", revision); err != nil {
		t.Fatal(err)
	}
	last := commands[len(commands)-1]
	payload := payloadOf(t, last.Payload)
	if last.Name != "service.delete" || payload["expectedProfileRevision"] != revision || payload["stopActive"] != true || payload["removeFromGroups"] != true {
		t.Fatal("reviewed effects were lost")
	}
}
