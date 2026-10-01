package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
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
