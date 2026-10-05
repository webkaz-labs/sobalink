package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestLANPolicyCLIValidatesWithoutImplicitScope(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		for _, args := range [][]string{
			{"--mode", "allowed-lan-destinations"},
			{"--mode", "allowed-lan-destinations", "--prefix", "0.0.0.0/0"},
			{"--mode", "allowed-lan-destinations", "--prefix", "192.168.50.10/24"},
			{"--mode", "trusted-relay", "--prefix", "192.168.50.0/24"},
			{"--prefix", "192.168.50.0/24"},
		} {
			called := false
			err := runWith(context.Background(), append([]string{"--locale", locale, "lan", "policy", "set"}, args...), io.Discard, strings.NewReader(""), func(context.Context, string, string, any) error { called = true; return nil })
			if err == nil || called {
				t.Fatal("invalid policy reached control", args, err)
			}
		}
		var out bytes.Buffer
		args := []string{"--locale", locale, "--dry-run", "lan", "policy", "set", "--mode", "allowed-lan-destinations", "--prefix", "192.168.50.0/24", "--prefix", "fd00::/64", "--prefix", "192.168.50.0/24"}
		err := runWith(context.Background(), args, &out, strings.NewReader(""), func(context.Context, string, string, any) error { t.Fatal("dry run contacted process"); return nil })
		if err != nil {
			t.Fatal(err)
		}
		var preview struct {
			Command string
			Applied bool
			Payload struct {
				Mode     string
				Prefixes []string
			}
		}
		if json.Unmarshal(out.Bytes(), &preview) != nil || preview.Applied || preview.Command != "lan.policy.set" || preview.Payload.Mode != "allowed-lan-destinations" || len(preview.Payload.Prefixes) != 2 {
			t.Fatal("wrong dry-run policy", out.String())
		}
		out.Reset()
		args = []string{"--locale", locale, "--dry-run", "lan", "policy", "set", "--mode", "trusted-relay"}
		if err := runWith(context.Background(), args, &out, strings.NewReader(""), nil); err != nil {
			t.Fatal("explicit trusted mode must not require redundant confirmation", err)
		}
	}
}

func TestLANInitialSetupPolicyPayloadAndValidation(t *testing.T) {
	for _, ja := range []bool{false, true} {
		payload, err := setupPayload([]string{"--network", "lan", "--host", "192.168.50.10:48443", "--policy-mode", "allowed-lan-destinations", "--prefix", "192.168.50.0/24"}, ja, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		policy := payload["lanPolicy"].(map[string]any)
		if policy["mode"] != "allowed-lan-destinations" || len(policy["prefixes"].([]string)) != 1 {
			t.Fatal("policy absent from atomic setup")
		}
		for _, args := range [][]string{{"--prefix", "192.168.50.0/24"}, {"--network", "lan", "--policy-mode", "allowed-lan-destinations"}, {"--network", "none", "--policy-mode", "trusted-relay"}, {"--network", "lan", "--policy-mode", "trusted-relay", "--prefix", "192.168.50.0/24"}} {
			if _, err := setupPayload(args, ja, io.Discard); err == nil {
				t.Fatal("invalid atomic policy accepted", args)
			}
		}
	}
}

func TestLANPolicyShowReadOnlyAndLocalized(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		for _, structured := range []bool{false, true} {
			var out bytes.Buffer
			args := []string{"--locale", locale, "lan", "policy", "show"}
			if structured {
				args = append(args, "--json")
			}
			err := runWith(context.Background(), args, &out, strings.NewReader(""), func(_ context.Context, _ string, request string, target any) error {
				var command struct {
					Name    string
					Payload map[string]any
				}
				if json.Unmarshal([]byte(request), &command) != nil || command.Name != "lan.policy.get" || len(command.Payload) != 0 {
					t.Fatal("show attempted mutation", request)
				}
				return json.Unmarshal([]byte(`{"mode":"allowed-lan-destinations","prefixes":["192.168.50.0/24"],"editable":false,"restartRequired":true}`), target)
			})
			if err != nil {
				t.Fatal(err)
			}
			if structured {
				var p humanLANPolicy
				if json.Unmarshal(out.Bytes(), &p) != nil || p.Mode != "allowed-lan-destinations" {
					t.Fatal("JSON localized or polluted", out.String())
				}
			} else if locale == "ja" && !strings.Contains(out.String(), "物理LAN") {
				t.Fatal("missing Japanese boundary", out.String())
			}
		}
	}
}
