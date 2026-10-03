package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
)

func TestOfflineDefinitionCLIWithoutAgentOrNetwork(t *testing.T) {
	dir := t.TempDir()
	client := func(context.Context, string, string, any) error {
		t.Fatal("offline mode contacted a running agent")
		return nil
	}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runWith(context.Background(), append([]string{"--state-dir", dir, "--offline"}, args...), &out, strings.NewReader(""), client)
		return out.String(), err
	}
	output, err := run("service", "save", "share", "--backend", "tailnet", "--name", "offline-example", "--ports", "8080", "--peers", "peer-example")
	if err != nil {
		t.Fatal(err)
	}
	var saved core.SavedServiceConfiguration
	if err := json.Unmarshal([]byte(output), &saved); err != nil || saved.Configuration.ID == "" || saved.Active {
		t.Fatal("offline save failed", err, output)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	assertOnlyOwnedProfileMetadata(t, dir, files)
	if _, err := run("service", "show", saved.Configuration.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := run("group", "save", "offline-group", saved.Configuration.ID); err != nil {
		t.Fatal(err)
	}
	if output, err := run("group", "list"); err != nil || !strings.Contains(output, "offline-group") {
		t.Fatal("offline group unavailable", err, output)
	}
	path := filepath.Join(t.TempDir(), "definitions.json")
	if _, err := run("profile", "export", "--output", path); err != nil {
		t.Fatal(err)
	}
	output, err = run("profile", "import", path)
	if err != nil {
		t.Fatal(err)
	}
	var preview struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal([]byte(output), &preview); err != nil || len(preview.Revision) != 64 {
		t.Fatal("offline import review missing", err)
	}
	if _, err := run("profile", "import", path, "--apply", "--review", preview.Revision); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "sobalink.json"))
	for _, args := range [][]string{{"group", "start", "offline-group", "--confirm"}, {"service", "restart", saved.Configuration.ID}, {"login"}, {"setup", "--network", "tailnet"}} {
		if _, err := run(args...); err == nil {
			t.Fatalf("offline mode admitted active command: %v", args)
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, "sobalink.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("rejected active commands changed metadata")
	}
}

func TestOfflineDefinitionCLIHonorsRunningAgentLock(t *testing.T) {
	dir := t.TempDir()
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	var out bytes.Buffer
	err = runWith(context.Background(), []string{"--state-dir", dir, "--offline", "service", "save", "share", "--backend", "tailnet", "--ports", "8080", "--peers", "peer-example"}, &out, strings.NewReader(""), func(context.Context, string, string, any) error {
		t.Fatal("offline command fell back to agent")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "without --offline") {
		t.Fatal("lock contention lacked running-agent route", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sobalink.json")); !os.IsNotExist(err) {
		t.Fatal("contended edit wrote profile")
	}
}

func TestOfflineReadDoesNotReconcilePrivateIdentityOrTransferState(t *testing.T) {
	dir := t.TempDir()
	profile := core.Profile{Version: 1, Settings: core.Settings{Network: "none", Locale: "auto", Theme: "system", Hostname: "offline-example"}, Peers: []core.Trust{{ID: "peer-example", Name: "Example", Network: "lan", Generation: 1}}, Services: []core.ServiceSpec{}}
	if err := config.WriteJSON(filepath.Join(dir, "sobalink.json"), profile); err != nil {
		t.Fatal(err)
	}
	private := []byte("intentionally unreadable fixture")
	for _, name := range []string{"lan.json", "messages.json", "incoming.json", "outgoing.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), private, 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.ReadFile(filepath.Join(dir, "sobalink.json"))
	var out bytes.Buffer
	if err := runWith(context.Background(), []string{"--state-dir", dir, "--offline", "profile", "export"}, &out, strings.NewReader(""), nil); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "sobalink.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("offline read changed trust or identity metadata")
	}
	for _, name := range []string{"lan.json", "messages.json", "incoming.json", "outgoing.json"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, private) {
			t.Fatalf("offline read touched private runtime file %s", name)
		}
	}
}
