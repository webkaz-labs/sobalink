package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestWANCandidatesCLIDispatchDryRun(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		var out bytes.Buffer
		args := []string{"--locale", locale, "--dry-run", "lan", "wan", "set", "--stun", "192.0.2.1:3478"}
		if err := runWith(context.Background(), args, &out, strings.NewReader(""), nil); err != nil {
			t.Fatal(err)
		}
		var preview struct {
			Command string
			Applied bool
			Payload struct {
				Enabled       bool
				STUNEndpoints []string
				AdvertiseIPv6 bool
			}
		}
		if json.Unmarshal(out.Bytes(), &preview) != nil || preview.Command != "wan.candidates.set" || preview.Applied || !preview.Payload.Enabled || len(preview.Payload.STUNEndpoints) != 1 || preview.Payload.AdvertiseIPv6 {
			t.Fatal("WAN dry-run widened configuration or started network")
		}
	}
}

func TestWANCandidatesCLIExplicitAndLocalized(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, args := range [][]string{{"set"}, {"set", "--stun", "stun.example.invalid:3478"}, {"set", "--stun", "192.0.2.1:0"}, {"set", "--stun", "192.0.2.1:3478", "--stun", "192.0.2.1:3478"}, {"disable", "--stun", "192.0.2.1:3478"}} {
			called := false
			err := wanCandidatesCommand(args, ja, false, io.Discard, nil, func(string, any) error { called = true; return nil })
			if err == nil || called {
				t.Fatal("invalid WAN settings reached control")
			}
		}
		var sent string
		var payload map[string]any
		err := wanCandidatesCommand([]string{"set", "--stun", "[2001:db8::1]:3478", "--stun", "192.0.2.1:3478", "--advertise-ipv6"}, ja, true, io.Discard, nil, func(name string, value any) error { sent = name; payload = value.(map[string]any); return nil })
		if err != nil || sent != "wan.candidates.set" || payload["enabled"] != true || payload["advertiseIPv6"] != true || len(payload["stunEndpoints"].([]string)) != 2 {
			t.Fatal("wrong WAN payload", err)
		}
		if err := wanCandidatesCommand([]string{"disable"}, ja, true, io.Discard, nil, func(name string, value any) error {
			if name != "wan.candidates.set" || value.(map[string]any)["enabled"] != false || len(value.(map[string]any)) != 1 {
				t.Fatal("disable retained authority")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := wanCandidatesCommand([]string{"--help"}, ja, false, &out, nil, nil); err != nil {
			t.Fatal(err)
		}
		if ja && (!strings.Contains(out.String(), "管理者権限") || !strings.Contains(out.String(), "実機")) || !ja && (!strings.Contains(out.String(), "elevated") || !strings.Contains(out.String(), "Real-device")) {
			t.Fatal("localized safety boundaries missing")
		}
	}
}

func TestWANCandidatesCLIShowStableJSON(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, structured := range []bool{false, true} {
			args := []string{"show"}
			if structured {
				args = append(args, "--json")
			}
			var out bytes.Buffer
			err := wanCandidatesCommand(args, ja, false, &out, func(name string, payload any, target any) error {
				if name != "wan.candidates.get" || len(payload.(map[string]any)) != 0 {
					t.Fatal("show changed settings")
				}
				return json.Unmarshal([]byte(`{"ok":true,"result":{"enabled":true,"stunEndpoints":["192.0.2.1:3478"],"advertiseIPv6":true,"editable":false,"restartRequired":true}}`), target)
			}, func(string, any) error { t.Fatal("show called mutation path"); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if structured {
				var got humanWANCandidates
				if json.Unmarshal(out.Bytes(), &got) != nil || !got.Enabled || got.STUNEndpoints[0] != "192.0.2.1:3478" {
					t.Fatal("JSON localized or modified")
				}
			} else if ja && !strings.Contains(out.String(), "保存済み") {
				t.Fatal("missing Japanese status")
			}
		}
	}
}

func TestWANCandidatesCLIConfigurableProbeBudgetAndMetadata(t *testing.T) {
	for _, ja := range []bool{false, true} {
		args := []string{"set", "--probe-budget", "2"}
		for port := 30000; port < 30012; port++ {
			args = append(args, "--stun", fmt.Sprintf("192.0.2.1:%d", port))
		}
		if err := wanCandidatesCommand(args, ja, true, io.Discard, nil, func(name string, value any) error {
			payload := value.(map[string]any)
			if name != "wan.candidates.set" || payload["probeBudget"] != 2 || len(payload["stunEndpoints"].([]string)) != 12 {
				t.Fatal("CLI capped metadata or omitted probe resource budget")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"0", "-1", "65536", "unbounded"} {
			if err := wanCandidatesCommand([]string{"set", "--advertise-ipv6", "--probe-budget", bad}, ja, false, io.Discard, nil, func(string, any) error { t.Fatal("invalid budget reached control"); return nil }); err == nil {
				t.Fatal("invalid budget accepted")
			}
		}
	}
}
