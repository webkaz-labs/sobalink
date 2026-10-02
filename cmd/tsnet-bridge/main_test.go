package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/webkaz-labs/sobalink/internal/app"
	"github.com/webkaz-labs/sobalink/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineFlags(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"--help"}, {"version"}, {"help"}} {
		var out bytes.Buffer
		if e := run(context.Background(), args, strings.NewReader(""), &out); e != nil {
			t.Fatal(args, e)
		}
		if !strings.Contains(out.String(), "tsnet-bridge") {
			t.Fatal(out.String())
		}
	}
}
func TestAuthURL(t *testing.T) {
	if !validAuthURL("https://login.tailscale.com/a/example") {
		t.Fatal("valid URL rejected")
	}
	for _, u := range []string{"http://login.tailscale.com/a/x", "https://login.tailscale.com.evil.example/a/x", "https://login.tailscale.com:443/a/x", "https://user@login.tailscale.com/a/x", "file:///a/x", "https://login.tailscale.com/other"} {
		if validAuthURL(u) {
			t.Fatalf("accepted %s", u)
		}
	}
}
func TestSettingsHideCredentials(t *testing.T) {
	d := t.TempDir()
	c, e := config.New("server", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	c.Mode = "socks"
	if e = config.Save(d, c); e != nil {
		t.Fatal(e)
	}
	secret := "not-for-status-test-value"
	config.WriteJSON(filepath.Join(d, "credentials.json"), config.Credentials{Username: "test-user", Password: secret})
	var out bytes.Buffer
	if e = settings(d, nil, &out); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(out.String(), secret) {
		t.Fatal("leaked credentials")
	}
	out.Reset()
	if e = settings(d, []string{"--show-secrets"}, &out); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), secret) {
		t.Fatal("explicit display missing")
	}
}
func TestSetupCancellationDoesNotSave(t *testing.T) {
	d := t.TempDir()
	var out bytes.Buffer
	if e := setup(d, nil, strings.NewReader(""), &out); e == nil {
		t.Fatal("did not cancel")
	}
	if _, e := config.Load(d); e == nil {
		t.Fatal("saved incomplete profile")
	}
}

func TestStatusKeepsProfileAndSuccessfulDiagnostic(t *testing.T) {
	dir := t.TempDir()
	for _, reason := range []string{"No reachable process; run tsnet-bridge to start", "Run tsnet-bridge login to sign in"} {
		var out bytes.Buffer
		s := app.Status{State: "stopped", Reason: reason}
		if e := printStatus(&out, s, false, dir); e != nil || !strings.Contains(out.String(), commandPrefix(dir)) {
			t.Fatal(out.String(), e)
		}
		var a, b bytes.Buffer
		printStatus(&a, s, true, dir)
		printStatus(&b, s, true)
		if a.String() != b.String() {
			t.Fatal("profile hint changed machine JSON")
		}
	}
	s := app.Status{Mode: "rules", State: "ready", Rules: []app.RuleStatus{{Name: "web", State: "ready", Direction: "forward", Network: "tcp", ReasonCode: "tcp-reachable", Reason: "TCP accepted a connection; protocol/TLS/application behavior remains unverified"}}}
	for _, localized := range []bool{false, true} {
		var out bytes.Buffer
		var writer interface{ Write([]byte) (int, error) } = &out
		if localized {
			writer = &localeWriter{out: &out, language: "ja"}
		}
		printStatus(writer, s, false, dir)
		if !strings.Contains(out.String(), "tcp-reachable") {
			t.Fatal("successful doctor result hidden", out.String())
		}
	}
}

func TestStopJSONNamedRoutingAndNoProcess(t *testing.T) {
	for _, selection := range [][]string{{"--json", "web"}, {"--json", "--group", "work"}} {
		t.Run(strings.Join(selection, "-"), func(t *testing.T) {
			dir := saveRules(t, testRule("web"))
			fake := newFake(t, dir)
			var out bytes.Buffer
			args := append([]string{"--lang", "ja", "--state-dir", dir, "stop"}, selection...)
			if e := run(t.Context(), args, strings.NewReader(""), &out); e != nil || !json.Valid(out.Bytes()) {
				t.Fatal(e, out.String())
			}
			if len(fake.commands) != 1 || fake.commands[0].Action != "stop" {
				t.Fatal(fake.commands)
			}
		})
	}
	// Use the native missing-file errno: Windows IPC intentionally recognizes
	// ERROR_FILE_NOT_FOUND/ERROR_PATH_NOT_FOUND rather than a generic sentinel.
	_, missing := os.Open(filepath.Join(t.TempDir(), "missing"))
	if missing == nil {
		t.Fatal("missing-file fixture unexpectedly exists")
	}
	installRequest(t, func(context.Context, string, string, any) error { return missing })
	var out bytes.Buffer
	if e := run(t.Context(), []string{"--lang", "ja", "--state-dir", t.TempDir(), "stop", "--json"}, strings.NewReader(""), &out); e != nil || !json.Valid(out.Bytes()) {
		t.Fatal(e, out.String())
	}
}
func TestNodeStartJSONHasNoHumanPreamble(t *testing.T) {
	dir := t.TempDir()
	installRequest(t, func(_ context.Context, got, command string, v any) error {
		if got != dir || command != "status" {
			t.Fatal(got, command)
		}
		return assign(v, app.Status{State: "needs-login", Reason: "Run tsnet-bridge login to sign in"})
	})
	var out bytes.Buffer
	if e := run(t.Context(), []string{"--lang", "ja", "--state-dir", dir, "start", "--json"}, strings.NewReader(""), &out); e != nil || !json.Valid(out.Bytes()) {
		t.Fatal(e, out.String())
	}
}
