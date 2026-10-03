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

func TestSavedProxyCLIPrivateInputsAndRedaction(t *testing.T) {
	input := `{"scope":{"name":"example"},"username":"fixture-private-user","password":"fixture-private-password","expectedRevision":"revision","expectedStoreRevision":"store","startOnLaunch":false}`
	for _, action := range []string{"save", "generate"} {
		for _, args := range [][]string{{"--dry-run", "proxy", action, "--stdin"}, {"--dry-run", "command", "proxy." + action, "--stdin"}} {
			m := &mockCLI{}
			out, err := runMock(t, m, args, input)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out, "fixture-private") || len(m.commands) > 0 {
				t.Fatal("dry-run leaked private input", out)
			}
		}
		for _, args := range [][]string{{"proxy", action, input}, {"command", "proxy." + action, input}} {
			m := &mockCLI{}
			out, err := runMock(t, m, args, "")
			if err == nil || len(m.commands) > 0 || strings.Contains(out, "fixture-private") {
				t.Fatal("credential argv bypass")
			}
		}
		path := filepath.Join(t.TempDir(), "private.json")
		if err := config.AtomicWrite(path, []byte(input)); err != nil {
			t.Fatal(err)
		}
		m := &mockCLI{}
		if _, err := runMock(t, m, []string{"proxy", action, "--json-file", path}, ""); err != nil || len(m.commands) != 1 {
			t.Fatal("private input failed", err)
		}
	}
	for _, args := range [][]string{{"proxy", "reveal", "example", "--review", "revision"}, {"command", "proxy.reveal", `{"name":"example","expectedRevision":"revision"}`}} {
		m := &mockCLI{}
		if _, err := runMock(t, m, args, ""); err == nil || len(m.commands) > 0 {
			t.Fatal("reveal bypassed private destination")
		}
	}
}
func TestSavedProxyRevealWritesPrivateFileOnly(t *testing.T) {
	target := filepath.Join(t.TempDir(), "credentials.json")
	query := func(name string, input any, output any) error {
		if name != "proxy.reveal" {
			t.Fatal("unexpected query")
		}
		encoded, _ := json.Marshal(core.SavedProxyCredentials{Username: "fixture-private-user", Password: "fixture-private-password"})
		return json.Unmarshal(encoded, output)
	}
	var out bytes.Buffer
	handled, err := savedProxyCLI(context.Background(), []string{"reveal", "example", "--review", "revision", "--private-file", target}, t.TempDir(), false, false, &out, strings.NewReader(""), query, func(string, any) error { t.Fatal("unexpected generic output"); return nil })
	if !handled || err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "fixture-private") {
		t.Fatal("reveal printed credentials")
	}
	file, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := checkProxyPrivateFile(file); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(data), "fixture-private-password") {
		t.Fatal("private output missing")
	}
}
func TestSavedProxyRevealDryRunDoesNotReadSecrets(t *testing.T) {
	var out bytes.Buffer
	handled, err := savedProxyCLI(context.Background(), []string{"reveal", "example", "--review", "revision", "--private-file", filepath.Join(t.TempDir(), "private.json")}, t.TempDir(), true, true, &out, strings.NewReader(""), func(string, any, any) error { t.Fatal("dry run read credentials"); return nil }, nil)
	if !handled || err != nil || !strings.Contains(out.String(), `"applied":false`) {
		t.Fatal("dry run reveal", err, out.String())
	}
}
