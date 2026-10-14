package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestNearbyWizardExplicitRoleAndPortCorrection(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, tc := range []struct {
			name, input      string
			host             bool
			bootstrap, relay uint16
		}{
			{"explicit-decline", "Device A\n\nunknown\nn\ny\n", false, 54546, 0},
			{"explicit-host", "Device A\n\ny\ny\n", true, 54546, 54547},
			{"correct-host-ports", "Device A\ny\np\n80\n54544\n55000\n55000\n55001\ny\n", true, 55000, 55001},
			{"correct-no-host-port", "Device A\nn\np\n55002\ny\n", false, 55002, 0},
		} {
			t.Run(text(ja, "en/", "ja/")+tc.name, func(t *testing.T) {
				var out bytes.Buffer
				var calls []webui.Command
				c := nearbyCLI{ctx: context.Background(), ja: ja, dir: "/synthetic/profile", out: &out, client: nearbyFixtureClient(t, &calls, []nearbyScope{nearbyTestScope()}, nearbyTestSnapshot("idle"))}
				input, err := c.wizardStart(newPrompts(strings.NewReader(tc.input), &out))
				if err != nil || input.AllowHost != tc.host || input.BootstrapPort != tc.bootstrap || input.RelayPort != tc.relay || !input.Consent {
					t.Fatal(err, input)
				}
				if len(calls) != 1 || calls[0].Name != "setup.scopes" {
					t.Fatal("review granted network access", calls)
				}
				for _, required := range []string{"120", "soba --state-dir /synthetic/profile stop", text(ja, "not yet confirmed", "まだ未確認"), "192.168.70.0/24"} {
					if !strings.Contains(out.String(), required) {
						t.Fatalf("missing permission explanation: %s", required)
					}
				}
				if !tc.host && !strings.Contains(out.String(), text(ja, "other device needs", "相手側で中継")) {
					t.Fatal("missing decline consequence")
				}
			})
		}
	}
}

func TestNearbyWizardRoleOrPortCancellationNeverStarts(t *testing.T) {
	for _, input := range []string{"Device A\n\n", "Device A\nq\n", "Device A\ny\np\nq\n", "Device A\ny\np\n55000\nq\n", "Device A\ny\np\n55000\n55001\nn\n"} {
		var out bytes.Buffer
		var calls []webui.Command
		c := nearbyCLI{ctx: context.Background(), out: &out, client: nearbyFixtureClient(t, &calls, []nearbyScope{nearbyTestScope()}, nearbyTestSnapshot("idle"))}
		got, err := c.wizardStart(newPrompts(strings.NewReader(input), &out))
		if !errors.Is(err, errPromptCanceled) || got.Consent || len(calls) != 1 {
			t.Fatal(err, got, calls)
		}
	}
}

func TestNearbyWizardCleanFailedRetryRequiresFreshConsent(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		for _, allow := range []bool{false, true} {
			t.Run(locale+text(allow, "/decline", "/allow"), func(t *testing.T) {
				failed := nearbyTestSnapshot("failed")
				failed.ReasonCode, failed.NextAction = "setup_no_permitted_host", "try_again"
				var calls []webui.Command
				var out bytes.Buffer
				client := func(_ context.Context, _ string, raw string, target any) error {
					var cmd webui.Command
					if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
						t.Fatal(err)
					}
					calls = append(calls, cmd)
					var response any = failed
					if cmd.Name == "setup.scopes" {
						response = nearbyScopes{"sobalink-setup-scopes", 1, []nearbyScope{nearbyTestScope()}}
					}
					if cmd.Name == "setup.start" {
						var got nearbyStartInput
						if err := json.Unmarshal(cmd.Payload, &got); err != nil {
							t.Fatal(err)
						}
						if got.Alias != "New alias" || !got.Consent || got.AllowHost != allow || got.RelayPort != map[bool]uint16{true: 54547, false: 0}[allow] {
							t.Fatal("reused old permission", got)
						}
						response = nearbyTestSnapshot("discovering")
					}
					value, _ := json.Marshal(response)
					return json.Unmarshal(value, target)
				}
				input := "y\nNew alias\n" + text(allow, "n", "y") + "\ny\nx\n"
				if err := runWith(context.Background(), []string{"--locale", locale, "nearby", "--legacy-interactive"}, &out, strings.NewReader(input), client); err != nil {
					t.Fatal(err)
				}
				if len(calls) != 3 || calls[0].Name != "setup.status" || calls[1].Name != "setup.scopes" || calls[2].Name != "setup.start" {
					t.Fatal(calls)
				}
				if !strings.Contains(out.String(), text(locale == "ja", "If both devices declined", "両方で許可しなかった場合")) {
					t.Fatal("missing no-host next step")
				}
			})
		}
	}
}

func TestNearbyWizardDoesNotRetryUnsafeOrDeclinedFailure(t *testing.T) {
	for _, tc := range []struct {
		state, reason, action, process string
		paired                         bool
	}{
		{"failed", "setup_no_permitted_host", "review_existing_network", "ready", false},
		{"failed", "setup_existing_anchor_conflict", "review_existing_network", "ready", false},
		{"recovery_required", "setup_cleanup_unconfirmed", "review_recovery", "ready", false},
		{"failed", "setup_no_permitted_host", "try_again", "needs_restart", false},
		{"failed", "setup_no_permitted_host", "try_again", "ready", true},
		{"failed", "setup_no_permitted_host", "try_again", "ready", false},
	} {
		s := nearbyTestSnapshot(tc.state)
		s.ReasonCode, s.NextAction, s.Process.State, s.Paired = tc.reason, tc.action, tc.process, tc.paired
		var calls []webui.Command
		var out bytes.Buffer
		if err := runWith(context.Background(), []string{"nearby", "--legacy-interactive"}, &out, strings.NewReader("n\n"), nearbyFixtureClient(t, &calls, nil, s)); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || calls[0].Name != "setup.status" {
			t.Fatal("unsafe or declined retry", tc, calls)
		}
	}
}

func TestNearbyWizardRetryLostStartNeverReplays(t *testing.T) {
	failed := nearbyTestSnapshot("failed")
	failed.NextAction = "try_again"
	var calls []webui.Command
	var out bytes.Buffer
	fixture := nearbyFixtureClient(t, &calls, []nearbyScope{nearbyTestScope()}, failed)
	client := func(ctx context.Context, dir, raw string, target any) error {
		var command webui.Command
		if err := json.Unmarshal([]byte(raw), &command); err != nil {
			t.Fatal(err)
		}
		if command.Name == "setup.start" {
			calls = append(calls, command)
			return errors.New("synthetic lost response")
		}
		return fixture(ctx, dir, raw, target)
	}
	err := runWith(context.Background(), []string{"--state-dir", "/synthetic/profile", "nearby", "--legacy-interactive"}, &out, strings.NewReader("y\nNew alias\ny\ny\ny\n"), client)
	if err == nil || len(calls) != 3 || calls[2].Name != "setup.start" {
		t.Fatal(err, calls)
	}
	if !strings.HasSuffix(strings.TrimSpace(out.String()), "soba --state-dir /synthetic/profile nearby status") {
		t.Fatal("did not recover current fresh attempt")
	}
}
