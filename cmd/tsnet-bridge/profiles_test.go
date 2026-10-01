package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/webkaz-labs/tsnet-bridge/internal/app"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
)

func TestMigrationPreviewConfirmAndBackup(t *testing.T) {
	d := t.TempDir()
	c, e := config.New("server.example.ts.net", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	if e = config.Save(d, c); e != nil {
		t.Fatal(e)
	}
	original, _ := os.ReadFile(filepath.Join(d, "profile.json"))
	var out bytes.Buffer
	if e = migrateCommand(d, nil, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(filepath.Join(d, "profile.json"))
	if !bytes.Equal(after, original) {
		t.Fatal("preview changed original")
	}
	if _, e = os.Stat(filepath.Join(d, "profile.v1.backup.json")); !os.IsNotExist(e) {
		t.Fatal("preview created backup")
	}
	if e = migrateCommand(d, []string{"--confirm"}, strings.NewReader(""), &out); e == nil {
		t.Fatal("migration accepted missing pin")
	}
	if e = migrateCommand(d, []string{"--confirm", "--id-peer-id", "node-server"}, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	after, _ = os.ReadFile(filepath.Join(d, "profile.v1.backup.json"))
	if !bytes.Equal(after, original) {
		t.Fatal("backup was not exact")
	}
	got, e := config.Load(d)
	if e != nil || got.Version != 2 || len(got.Rules) != 4 {
		t.Fatal(got, e)
	}
	for _, r := range got.Rules {
		if r.Enabled {
			t.Fatal("migration activated rule")
		}
	}
}
func TestExportPrivacyDisabledAndNoCredentials(t *testing.T) {
	d := saveRules(t, testRule("web"))
	credentials := filepath.Join(d, "credentials.json")
	if e := config.WriteJSON(credentials, config.Credentials{Username: "not-exported-user", Password: "never-export-this-value"}); e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(t.TempDir(), "export.json")
	var out bytes.Buffer
	if e := exportCommand(d, []string{"--output", dest}, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(dest); !os.IsNotExist(e) {
		t.Fatal("preview wrote file")
	}
	if !strings.Contains(out.String(), "Privacy review") || strings.Contains(out.String(), "never-export-this-value") {
		t.Fatal(out.String())
	}
	if e := exportCommand(d, []string{"--output", dest, "--confirm"}, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	var c config.Config
	if e := config.ReadJSON(dest, &c); e != nil {
		t.Fatal(e)
	}
	if c.Rules[0].Enabled {
		t.Fatal("active export")
	}
	b, _ := os.ReadFile(dest)
	if strings.Contains(string(b), "never-export-this-value") || strings.Contains(string(b), "password") {
		t.Fatal("credential leaked")
	}
	if runtime.GOOS != "windows" {
		i, _ := os.Stat(dest)
		if i.Mode().Perm() != 0600 {
			t.Fatal(i.Mode())
		}
	}
	if e := exportCommand(d, []string{"--output", filepath.Join(d, "profile.json"), "--confirm"}, strings.NewReader(""), &out); e == nil {
		t.Fatal("export replaced active profile")
	}
}
func TestImportPreviewThenDisabledSave(t *testing.T) {
	source := saveRules(t, testRule("web"))
	c, e := config.Load(source)
	if e != nil {
		t.Fatal(e)
	}
	c.Rules[0].Enabled = true
	c.Hostname = "source-node"
	input := filepath.Join(t.TempDir(), "incoming.json")
	if e = config.WriteJSON(input, c); e != nil {
		t.Fatal(e)
	}
	destination := t.TempDir()
	var out bytes.Buffer
	if e = importCommand(context.Background(), destination, []string{input}, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	if _, e = config.Load(destination); !os.IsNotExist(e) {
		t.Fatal("preview changed destination", e)
	}
	if e = importCommand(context.Background(), destination, []string{"--confirm", input}, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	got, e := config.Load(destination)
	if e != nil || got.Hostname == "source-node" || got.Rules[0].Enabled {
		t.Fatal(got, e)
	}
	if e = importCommand(context.Background(), destination, []string{"--confirm", input}, strings.NewReader(""), &out); e == nil {
		t.Fatal("silently replaced existing profile")
	}
	hostname := got.Hostname
	if e = importCommand(context.Background(), destination, []string{"--replace", "--confirm", input}, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	got, _ = config.Load(destination)
	if got.Hostname != hostname || got.Rules[0].Enabled {
		t.Fatal(got)
	}
}
func TestProfileWritesRefuseActiveLock(t *testing.T) {
	d := saveRules(t, testRule("web"))
	source := saveRules(t, testRule("other"))
	lock, e := config.AcquireLock(d)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	var out bytes.Buffer
	if e = importCommand(context.Background(), d, []string{"--replace", "--confirm", filepath.Join(source, "profile.json")}, strings.NewReader(""), &out); e == nil {
		t.Fatal("import wrote around running daemon lock")
	}
	got, _ := config.Load(d)
	if got.Rules[0].Name != "web" {
		t.Fatal(got.Rules)
	}
}
func TestGroupFailedStartReportsAllRulesAndReturnsError(t *testing.T) {
	d := saveRules(t, testRule("web"), testRule("database"), testRule("unrelated"))
	c, _ := config.Load(d)
	c.Groups = []config.Group{{Name: "dev", Rules: []string{"web", "database"}}}
	if e := config.Save(d, c); e != nil {
		t.Fatal(e)
	}
	installRequest(t, func(_ context.Context, _ string, command string, v any) error {
		if command == "status" {
			return assign(v, app.Status{State: "idle"})
		}
		var q app.RuleCommand
		if e := json.Unmarshal([]byte(strings.TrimPrefix(command, "rules:")), &q); e != nil {
			t.Fatal(e)
		}
		if q.Action != "start" || q.Group != "dev" {
			t.Fatal(q)
		}
		return assign(v, app.Status{Mode: "rules", State: "partial", Rules: []app.RuleStatus{{Name: "web", State: "stopped", ReasonCode: "rolled-back", Reason: "new listener closed"}, {Name: "database", State: "failed", ReasonCode: "start-failed", Reason: "port unavailable"}, {Name: "unrelated", State: "ready", Owner: "another-task"}}})
	})
	var out bytes.Buffer
	if e := groupCommand(context.Background(), d, []string{"start", "dev"}, strings.NewReader(""), &out); e == nil {
		t.Fatal("partial group failure reported success")
	}
	for _, text := range []string{"rolled-back", "database", "port unavailable", "another-task"} {
		if !strings.Contains(out.String(), text) {
			t.Fatal("missing detail", text, out.String())
		}
	}
}
func TestStopSharesRejectsMisleadingScope(t *testing.T) {
	d := saveRules(t, testRule("web"))
	newFake(t, d)
	var out bytes.Buffer
	for _, args := range [][]string{{"web"}, {"--group", "dev"}, {"--owner", "mine"}, {"--ttl", "1h"}} {
		if e := namedAction(context.Background(), d, "stop-shares", args, strings.NewReader(""), &out); e == nil {
			t.Fatal("accepted scoped global stop", args)
		}
	}
}
func TestGroupSaveReplaceRequiresExplicitFlag(t *testing.T) {
	d := saveRules(t, testRule("web"))
	installRequest(t, func(context.Context, string, string, any) error { return os.ErrNotExist })
	var out bytes.Buffer
	args := []string{"save", "--confirm", "dev", "web"}
	if e := groupCommand(context.Background(), d, args, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	c, _ := config.Load(d)
	if !reflect.DeepEqual(c.Groups, []config.Group{{Name: "dev", Rules: []string{"web"}}}) {
		t.Fatal(c.Groups)
	}
	if e := groupCommand(context.Background(), d, args, strings.NewReader(""), &out); e == nil {
		t.Fatal("silently replaced group")
	}
	if e := groupCommand(context.Background(), d, []string{"save", "--confirm", "--replace", "dev", "web"}, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
}
func TestAutostartDefaultsToPlanOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("current real user SID varies; Windows plans tested independently")
	}
	d := saveRules(t)
	old := autostartRun
	autostartRun = func(context.Context, string, ...string) error { t.Fatal("preview invoked service manager"); return nil }
	defer func() { autostartRun = old }()
	var out bytes.Buffer
	if e := autostartCommand(context.Background(), d, []string{"--json"}, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), "idle version 2 node") {
		t.Fatal(out.String())
	}
}

func TestAutostartIdleRunRefusesLegacyBeforeNetwork(t *testing.T) {
	d := t.TempDir()
	c, _ := config.New("server", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e := config.Save(d, c); e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	e := run(context.Background(), []string{"--state-dir", d, "run", "--idle"}, strings.NewReader(""), &out)
	if e == nil || !strings.Contains(e.Error(), "refusing legacy automatic forwarding") {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(d, "tsnet")); !os.IsNotExist(e) {
		t.Fatal("attempted node creation")
	}
}

func TestNamedShareJSONRemainsStructured(t *testing.T) {
	r := config.Rule{Name: "shared", Purpose: "web", Direction: "share", Network: "tcp", ListenPort: 8080, TargetHost: "127.0.0.1", TargetPort: 8080, AllowedPeers: []config.PeerRef{{ID: "node-server", Host: "server.example.ts.net"}}}
	d := saveRules(t, r)
	newFake(t, d)
	var out bytes.Buffer
	if e := namedAction(context.Background(), d, "start", []string{"--ttl", "1h", "--confirm", "--json", "shared"}, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	var status app.Status
	if e := json.Unmarshal(out.Bytes(), &status); e != nil {
		t.Fatal("JSON polluted by preview", e, out.String())
	}
}
