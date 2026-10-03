package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/autostart"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestAutostartReviewBindsExplicitLaunchScopeWithoutPrivateCredentials(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	service := core.ServiceSpec{ID: "saved-api", Name: "api", Backend: "tailnet", Direction: "forward", PeerID: "peer-example", Network: "tcp", Ports: "8080", LocalPort: 18080, LoopbackHost: "127.0.0.1", Lifetime: "until-stopped", Purpose: "web"}
	profile := core.Profile{Version: 1, Settings: core.Settings{Network: "tailnet", Hostname: "example-node", Locale: "auto", Theme: "system"}, Peers: []core.Trust{}, Services: []core.ServiceSpec{service}}
	if err := config.WriteJSON(filepath.Join(dir, "sobalink.json"), profile); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"ids": []string{service.ID}})
	selected, err := core.OfflineDefinitionCommand(context.Background(), core.Options{Directory: dir, SkipNetworkStart: true}, webui.Command{RequestID: "fixture-selection", Name: "service.selection", Payload: raw})
	if err != nil {
		t.Fatal(err)
	}
	revision := selected.(map[string]any)["revision"].(string)
	startup := core.StartupEntry{Name: "daily-api", IDs: []string{service.ID}, Services: []core.ServiceSpec{service}, SelectionRevision: revision, Network: "tailnet", Hostname: "example-node", Enabled: true, Revision: "startup-revision", PeerEpochs: map[string]string{"peer-example": ""}}
	if err := config.WriteJSON(filepath.Join(dir, "startup.json"), map[string]any{"version": 1, "entries": []core.StartupEntry{startup}}); err != nil {
		t.Fatal(err)
	}
	scope := core.ProxyScope{Name: "private-proxy", Backend: "tailnet", LoopbackHost: "127.0.0.1", LocalPort: 1080, Lifetime: "finite", TTLSeconds: 3600, Targets: []core.ProxyTarget{{PeerID: "peer-example", Port: 443}}}
	proxy := map[string]any{"scope": scope, "hostname": "example-node", "username": "synthetic-user-marker", "password": "synthetic-password-marker", "revision": "proxy-one", "startOnLaunch": true, "peerEpochs": map[string]string{"peer-example": ""}}
	writeProxy := func() {
		t.Helper()
		if err := config.WriteJSON(filepath.Join(dir, "saved-proxies.json"), map[string]any{"version": 1, "entries": []any{proxy}}); err != nil {
			t.Fatal(err)
		}
	}
	writeProxy()
	env := func() (autostart.Options, error) {
		return autostart.Options{OS: "linux", Home: root, ConfigDir: filepath.Join(root, "config"), Executable: filepath.Join(root, "soba")}, nil
	}
	ran := false
	runner := func(context.Context, string, ...string) error { ran = true; return nil }
	preview := func(mode string, ja bool) (autostartReview, string) {
		t.Helper()
		var out bytes.Buffer
		if err := autostartCommand(context.Background(), dir, []string{"--startup", mode, "--json"}, ja, false, &out, env, runner); err != nil {
			t.Fatal(err)
		}
		var review autostartReview
		if err := json.Unmarshal(out.Bytes(), &review); err != nil {
			t.Fatal(err)
		}
		return review, out.String()
	}
	online, encoded := preview("saved", false)
	if !online.Startup.NetworkStarts || !online.Startup.ServicesRestart || !online.Startup.ProxiesRestart || online.Startup.TransfersRestart || online.Startup.Launch == nil {
		t.Fatal(online)
	}
	for _, secret := range []string{"synthetic-user-marker", "synthetic-password-marker", `"username"`, `"password"`, `"passwordHash"`} {
		if strings.Contains(encoded, secret) {
			t.Fatal("private material in review")
		}
	}
	for _, field := range []string{"daily-api", "private-proxy", "peer-example", "18080", "1080"} {
		if !strings.Contains(encoded, field) {
			t.Fatal("scope not disclosed", field)
		}
	}
	if ran {
		t.Fatal("preview invoked OS manager")
	}
	if _, err := os.Stat(online.Plan.Path); !os.IsNotExist(err) {
		t.Fatal("preview wrote registration")
	}
	offline, _ := preview("offline", true)
	if offline.Startup.Launch != nil || offline.Startup.ServicesRestart || offline.Startup.ProxiesRestart || offline.Startup.NetworkStarts || offline.Startup.TransfersRestart {
		t.Fatal("offline effects not suppressed", offline)
	}
	proxy["revision"] = "proxy-two"
	proxy["password"] = "rotated-synthetic-password"
	writeProxy()
	changed, _ := preview("saved", false)
	if changed.ReviewToken == online.ReviewToken {
		t.Fatal("credential rotation revision not bound")
	}
	var out bytes.Buffer
	if err := autostartCommand(context.Background(), dir, []string{"--apply", "--review", online.ReviewToken}, false, false, &out, env, runner); err == nil || ran {
		t.Fatal("stale launch approval reached OS manager", err)
	}
	if err := config.WriteJSON(filepath.Join(dir, "startup-revocations.json"), map[string]string{"peer-example": "revoked"}); err != nil {
		t.Fatal(err)
	}
	revoked, _ := preview("saved", false)
	if revoked.ReviewToken == changed.ReviewToken || revoked.Startup.ServicesRestart || revoked.Startup.ProxiesRestart {
		t.Fatal("revocation did not alter projected effects", revoked)
	}
}
