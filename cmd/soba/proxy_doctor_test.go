package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
)

func TestProxyPrivateInputUsesSelectedProfileBudget(t *testing.T) {
	targets := make([]core.ProxyTarget, 1200)
	for i := range targets {
		targets[i] = core.ProxyTarget{PeerID: "peer-" + strings.Repeat("x", 120), Port: i + 1}
	}
	input, err := json.Marshal(map[string]any{"scope": core.ProxyScope{Name: "large-scope", Backend: "tailnet", LoopbackHost: "127.0.0.1", LocalPort: 1080, Lifetime: "until-stopped", Targets: targets}, "expectedRevision": "fixture-review", "username": "fixture-user", "password": "fixture-password"})
	if err != nil || len(input) <= 48<<10 {
		t.Fatal("fixture does not exceed legacy ceiling")
	}
	path := filepath.Join(t.TempDir(), "private-proxy.json")
	if err := os.WriteFile(path, input, 0600); err != nil {
		t.Fatal(err)
	}
	if err := config.Protect(path, false); err != nil {
		t.Fatal(err)
	}
	for _, permitted := range []bool{false, true} {
		dir := t.TempDir()
		selected := capacity.Defaults()
		selected.Resources["profileBytes"] = capacity.Limited(1024)
		selected.Resources["transferManifestBytes"] = capacity.Limited(1024)
		if permitted {
			selected.Resources["profileBytes"] = capacity.Limited(512 << 10)
		}
		if err := config.WriteJSON(filepath.Join(dir, "capacity.json"), selected); err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"preview", "start"} {
			m := &mockCLI{}
			_, err := runMock(t, m, []string{"--state-dir", dir, "proxy", action, "--json-file", path}, "")
			if permitted && (err != nil || len(m.commands) != 1) {
				t.Fatal("selected larger budget rejected", err)
			}
			if !permitted && (err == nil || len(m.commands) != 0) {
				t.Fatal("selected smaller budget was bypassed")
			}
		}
	}
}

func TestProxyCLIReviewKeepsLifetimeAndExactScope(t *testing.T) {
	for _, ja := range []bool{false, true} {
		m := &mockCLI{}
		locale := "en"
		if ja {
			locale = "ja"
		}
		out, err := runMock(t, m, []string{"--locale", locale, "proxy", "preview", "--name", "example-proxy", "--peer", "peer-123", "--ports", "443,8443", "--ttl", "72h"}, "")
		if err != nil {
			t.Fatal(err, out)
		}
		if len(m.commands) != 1 || m.commands[0].Name != "proxy.preview" {
			t.Fatal("wrong proxy review command")
		}
		payload := payloadOf(t, m.commands[0].Payload)
		scope := payload["scope"].(map[string]any)
		if scope["ttlSeconds"] != float64(72*3600) || scope["lifetime"] != "finite" || scope["localPort"] != float64(1080) {
			t.Fatal("review lost selected lifetime or endpoint", scope)
		}
	}
	m := &mockCLI{}
	_, err := runMock(t, m, []string{"proxy", "preview", "--name", "example-proxy", "--peer", "peer-123", "--ports", "443"}, "")
	if err != nil {
		t.Fatal(err)
	}
	scope := payloadOf(t, m.commands[0].Payload)["scope"].(map[string]any)
	if scope["lifetime"] != "until-stopped" || scope["ttlSeconds"] != float64(0) {
		t.Fatal("default proxy unexpectedly expires")
	}
}
func TestProxyCLIPrivateInputsAndSecretRedaction(t *testing.T) {
	input := `{"scope":{"name":"example-proxy","backend":"tailnet","loopbackHost":"127.0.0.1","localPort":1080,"lifetime":"until-stopped","ttlSeconds":0,"targets":[{"peerId":"peer-123","port":443}]},"expectedRevision":"review-marker","username":"fixture-user-secret","password":"fixture-password-secret","unknownSecret":"fixture-extra-secret"}`
	for _, args := range [][]string{{"--dry-run", "proxy", "start", "--stdin"}, {"--dry-run", "command", "proxy.start", "--stdin"}} {
		m := &mockCLI{}
		out, err := runMock(t, m, args, input)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"fixture-user-secret", "fixture-password-secret", "fixture-extra-secret"} {
			if strings.Contains(out, secret) {
				t.Fatal("dry run exposed private proxy input", secret)
			}
		}
		if len(m.commands) != 0 || !strings.Contains(out, "example-proxy") {
			t.Fatal("private dry run mutated or hid review fields")
		}
	}
	for _, args := range [][]string{{"proxy", "start", input}, {"command", "proxy.start", input}, {"proxy", "start", "--password", "fixture-password-secret"}} {
		m := &mockCLI{}
		out, err := runMock(t, m, args, "")
		if err == nil || len(m.commands) != 0 || strings.Contains(out, "fixture-password-secret") || strings.Contains(err.Error(), "fixture-password-secret") {
			t.Fatal("literal credentials accepted or echoed", err)
		}
	}
	path := filepath.Join(t.TempDir(), "proxy-input.json")
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	if err := config.Protect(path, false); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"proxy", "start", "--json-file", path}, {"command", "proxy.start", "--json-file", path}} {
		m := &mockCLI{}
		out, err := runMock(t, m, args, "")
		if err != nil {
			t.Fatal(err, out)
		}
		if len(m.commands) != 1 || m.commands[0].Name != "proxy.start" {
			t.Fatal("private file did not reach explicit start")
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		m := &mockCLI{}
		_, err := runMock(t, m, []string{"proxy", "start", "--json-file", path}, "")
		if err == nil || len(m.commands) != 0 {
			t.Fatal("world-readable credentials accepted")
		}
	}
}
func TestProxyCLIRejectsInvalidScopesOffline(t *testing.T) {
	for _, extra := range [][]string{{"--loopback-host", "0.0.0.0"}, {"--local-port", "80"}, {"--ttl", "500ms"}, {"--lifetime", "until-revoked"}, {"--ports", "54544"}, {"--peer", ""}} {
		m := &mockCLI{}
		args := []string{"proxy", "preview", "--name", "example", "--peer", "peer-123", "--ports", "443"}
		args = append(args, extra...)
		_, err := runMock(t, m, args, "")
		if err == nil || len(m.commands) != 0 {
			t.Fatal("invalid CLI scope reached agent", extra)
		}
	}
}
func TestDoctorCLIRequiresExplicitTCPAndKeepsMachineJSON(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		m := &mockCLI{}
		_, err := runMock(t, m, []string{"--locale", locale, "doctor", "--service", "service-example", "--tcp", "--port", "8443"}, "")
		if err != nil {
			t.Fatal(err)
		}
		payload := payloadOf(t, m.commands[0].Payload)
		if m.commands[0].Name != "diagnostics.run" || payload["probeTCP"] != true || payload["port"] != float64(8443) {
			t.Fatal("localized doctor changed wire contract")
		}
	}
	for _, args := range [][]string{{"doctor", "--service", "example"}, {"doctor", "--tcp"}, {"doctor", "--port", "443"}, {"doctor", "--service", "example", "--tcp", "--port", "65536"}} {
		m := &mockCLI{}
		_, err := runMock(t, m, args, "")
		if err == nil || len(m.commands) != 0 {
			t.Fatal("invalid doctor request accepted", args)
		}
	}
	for _, topic := range []string{"proxy", "doctor"} {
		for _, locale := range []string{"en", "ja"} {
			m := &mockCLI{}
			out, err := runMock(t, m, []string{"--locale", locale, topic, "--help"}, "")
			if err != nil || len(m.commands) != 0 || !strings.Contains(out, "TCP") {
				t.Fatal("help contacted agent or omitted transport limit", err)
			}
		}
	}
}
func TestProxyPrivateFileRejectsSymlinkAndMalformedInput(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "private.json")
	if err := os.WriteFile(target, []byte(`{"password":"fixture-secret"`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := config.Protect(target, false); err != nil {
		t.Fatal(err)
	}
	_, err := commandPayload(context.Background(), []string{"proxy.start", "--json-file", target}, strings.NewReader(""), false)
	if err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("malformed private payload leaked")
	}
	if runtime.GOOS == "windows" {
		return
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err = commandPayload(context.Background(), []string{"proxy.start", "--json-file", link}, strings.NewReader(""), false)
	if err == nil {
		t.Fatal("symlink input accepted")
	}
}
