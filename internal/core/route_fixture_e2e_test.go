//go:build soba_e2e

package core

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRouteBrowserFixtureUsesRealProofWithoutNetwork(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || upper == "TS_PROXY" || strings.HasPrefix(upper, "TS_") && upper != "TS_NO_LOGS_NO_SUPPORT" {
			t.Setenv(name, "")
		}
	}
	root := t.TempDir()
	dir := filepath.Join(root, "profile")
	file := filepath.Join(root, "route-update.json")
	peer, err := PrepareRouteBrowserFixture(dir, file)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Open(context.Background(), Options{Directory: dir, Version: "fixture-test", SkipNetworkStart: true, NodeFactory: func(string, string) (NetworkBackend, error) {
		t.Error("route fixture started network")
		return nil, errors.New("forbidden")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Update string `json:"update"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		t.Fatal("invalid private fixture")
	}
	review := mustCommand(t, c, "lan.routes.inspect", map[string]any{"peerId": peer, "update": envelope.Update}).(map[string]any)
	candidates := review["candidates"].([]map[string]any)
	if len(candidates) != 2 {
		t.Fatal("missing exact candidates")
	}
	applied := mustCommand(t, c, "lan.routes.apply", map[string]any{"peerId": peer, "update": envelope.Update, "digest": review["digest"], "candidateIds": []string{candidates[0]["candidateId"].(string)}, "expires": time.Now().Add(time.Hour)}).(map[string]any)
	if len(applied["permittedIds"].([]string)) != 1 || c.lanStoreCopy().copy().Version != 2 {
		t.Fatal("review did not reach real state")
	}
	snapshot, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	peers := snapshot["peers"].([]map[string]any)
	if len(peers) != 1 || peers[0]["trusted"] != false || peers[0]["online"] != false {
		t.Fatal("route approval altered application trust or status")
	}
	revoked := mustCommand(t, c, "lan.routes.revoke", map[string]any{"peerId": peer, "candidateIds": []string{}}).(map[string]any)
	if len(revoked["permittedIds"].([]string)) != 0 {
		t.Fatal("route revoke failed")
	}
	if c.nodeCopy() != nil {
		t.Fatal("fixture created network backend")
	}
}

func TestRouteOfflineFailedRevocationCannotReloadOldPermission(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || upper == "TS_PROXY" || strings.HasPrefix(upper, "TS_") && upper != "TS_NO_LOGS_NO_SUPPORT" {
			t.Setenv(name, "")
		}
	}
	root := t.TempDir()
	dir := filepath.Join(root, "profile")
	file := filepath.Join(root, "route-update.json")
	peer, err := PrepareRouteBrowserFixture(dir, file)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Open(context.Background(), Options{Directory: dir, Version: "fixture-test", SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raw, _ := os.ReadFile(file)
	var envelope struct {
		Update string `json:"update"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		t.Fatal("fixture")
	}
	review := mustCommand(t, c, "lan.routes.inspect", map[string]any{"peerId": peer, "update": envelope.Update}).(map[string]any)
	candidates := review["candidates"].([]map[string]any)
	mustCommand(t, c, "lan.routes.apply", map[string]any{"peerId": peer, "update": envelope.Update, "digest": review["digest"], "candidateIds": []string{candidates[0]["candidateId"].(string)}, "expires": time.Now().Add(time.Hour)})
	store := c.lanStoreCopy()
	store.write = func(string, []byte) error { return os.ErrPermission }
	payload, _ := json.Marshal(map[string]any{"peerId": peer, "candidateIds": []string{}})
	if _, err := c.lanRoutesCommand(context.Background(), "lan.routes.revoke", payload); !errors.Is(err, config.ErrAtomicRecovery) {
		t.Fatal("failed revoke did not latch Core recovery", err)
	}
	payload, _ = json.Marshal(map[string]any{"peerId": peer})
	if _, err := c.lanRoutesCommand(context.Background(), "lan.routes.list", payload); !errors.Is(err, config.ErrAtomicRecovery) {
		t.Fatal("offline command resurrected saved permission", err)
	}
	if _, err := c.newLANBackend(store); !errors.Is(err, config.ErrAtomicRecovery) {
		t.Fatal("same-process activation resurrected saved permission", err)
	}
}
