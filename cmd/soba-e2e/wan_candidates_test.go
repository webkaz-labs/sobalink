//go:build soba_e2e

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// The browser suite reuses the offline route fixture's selected pinned relay.
// Prove that WAN commands reach real private storage without a mock response,
// and that closing/reopening Core (not only React) retains the exact choice.
func TestWANCandidateBrowserFixturePersistsWithoutNetwork(t *testing.T) {
	// Match the existing route fixture's isolated environment. Production's
	// trusted-relay guard remains active and rejects inherited proxy overrides.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || upper == "TS_PROXY" || strings.HasPrefix(upper, "TS_") && upper != "TS_NO_LOGS_NO_SUPPORT" {
			t.Setenv(name, "")
		}
	}
	root := t.TempDir()
	directory := filepath.Join(root, "profile")
	if _, err := core.PrepareRouteBrowserFixture(directory, filepath.Join(root, "route-update.json")); err != nil {
		t.Fatal(err)
	}
	var factoryCalls atomic.Int32
	var current *core.Core
	open := func() *fixtureBackend {
		t.Helper()
		app, err := core.Open(context.Background(), core.Options{Directory: directory, SkipNetworkStart: true, NodeFactory: func(string, string) (core.NetworkBackend, error) {
			factoryCalls.Add(1)
			return nil, errors.New("network activation is forbidden in this fixture")
		}})
		if err != nil {
			t.Fatal(err)
		}
		current = app
		return &fixtureBackend{Backend: app, scenario: "routes"}
	}
	t.Cleanup(func() {
		if current != nil {
			if err := current.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	fixture := open()
	sequence := 0
	call := func(name, payload string) (any, error) {
		sequence++
		return fixture.Command(context.Background(), webui.Command{RequestID: fmt.Sprintf("wan-fixture-%d", sequence), Name: name, Payload: json.RawMessage(payload)})
	}
	check := func(enabled, ipv6 bool, endpoints []string, budget int) {
		t.Helper()
		result, err := call("wan.candidates.get", `{}`)
		if err != nil {
			t.Fatal(err)
		}
		view, ok := result.(map[string]any)
		if !ok || view["enabled"] != enabled || view["advertiseIPv6"] != ipv6 || view["probeBudget"] != budget || view["editable"] != true || view["restartRequired"] != true || !reflect.DeepEqual(view["stunEndpoints"], endpoints) {
			t.Fatal("WAN settings did not preserve the exact offline fixture configuration")
		}
		state, err := fixture.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		peers := state["peers"].([]map[string]any)
		if state["self"].(map[string]any)["status"] != "idle" || state["settings"].(core.Settings).Network != "lan" || len(peers) != 1 || peers[0]["online"] != false || peers[0]["trusted"] != false || len(state["services"].([]map[string]any)) != 0 || len(state["shares"].([]map[string]any)) != 0 || factoryCalls.Load() != 0 {
			t.Fatal("WAN settings activated a network or changed unrelated fixture state")
		}
	}
	reopen := func() {
		t.Helper()
		if err := current.Close(); err != nil {
			t.Fatal(err)
		}
		current = nil
		fixture = open()
	}
	check(false, false, []string{}, 4)
	before, err := os.ReadFile(filepath.Join(directory, "lan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call("wan.candidates.set", `{"enabled":true,"stunEndpoints":[],"advertiseIPv6":false,"probeBudget":4}`); err == nil {
		t.Fatal("empty candidates without IPv6 were accepted by Core")
	}
	after, err := os.ReadFile(filepath.Join(directory, "lan.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("rejected empty WAN configuration changed private storage")
	}
	if _, err := call("wan.candidates.set", `{"enabled":true,"stunEndpoints":["192.0.2.20:3478","[2001:db8::20]:3478"],"advertiseIPv6":false,"probeBudget":2}`); err != nil {
		t.Fatal(err)
	}
	check(true, false, []string{"192.0.2.20:3478", "[2001:db8::20]:3478"}, 2)
	if _, err := call("wan.candidates.set", `{"enabled":true,"stunEndpoints":[],"advertiseIPv6":true,"probeBudget":3}`); err != nil {
		t.Fatal(err)
	}
	check(true, true, []string{}, 3)
	reopen()
	check(true, true, []string{}, 3)
	for _, payload := range []string{`{"mode":"lan"}`, `{"mode":"tailnet"}`, `{"mode":"direct-lan"}`, `{"mode":"mixed"}`} {
		if _, err := call("network.configure", payload); err == nil {
			t.Fatal("the reused offline fixture admitted network activation")
		}
	}
	if _, err := call("wan.candidates.set", `{"enabled":false}`); err != nil {
		t.Fatal(err)
	}
	reopen()
	check(false, false, []string{}, 4)
}
