package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
)

type loginTestBackend struct {
	NetworkBackend
	state  identity.State
	logins int
}

func (b *loginTestBackend) State(context.Context) (identity.State, error) { return b.state, nil }
func (b *loginTestBackend) Login(context.Context) error                   { b.logins++; return nil }

func TestExplicitLoginPreservesCompletedAndPendingAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name, backend, url, want string
		running                  bool
		calls                    int
	}{
		{"connected", "Running", "", "connected", true, 0},
		{"approval", "NeedsMachineAuth", "", "approval-required", false, 0},
		{"existing-link", "NeedsLogin", "https://login.tailscale.com/a/fictional", "waiting", false, 0},
		{"request", "NeedsLogin", "", "waiting", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &loginTestBackend{state: identity.State{Backend: tc.backend, AuthURL: tc.url, Snapshot: policy.Snapshot{Running: tc.running}}}
			c := &Core{ctx: t.Context(), profile: Profile{Settings: Settings{Network: "tailnet"}}, node: backend}
			view, err := c.loginCommand(t.Context(), true, json.RawMessage(`{}`))
			if err != nil || view.State != tc.want || backend.logins != tc.calls {
				t.Fatalf("state=%s calls=%d err=%v", view.State, backend.logins, err)
			}
			if view.State != "waiting" && (view.AuthURL != "" || view.QR != nil) {
				t.Fatal("completed sign-in retained a private link")
			}
		})
	}
}

func TestLoginQRIsExplicitLocalAndStrictlyValidated(t *testing.T) {
	backend := &loginTestBackend{state: identity.State{Backend: "NeedsLogin", AuthURL: "https://login.tailscale.com/a/fictional"}}
	c := &Core{ctx: t.Context(), profile: Profile{Settings: Settings{Network: "tailnet"}}, node: backend}
	view, err := c.loginCommand(t.Context(), false, json.RawMessage(`{"qr":true}`))
	if err != nil || len(view.QR) == 0 || backend.logins != 0 {
		t.Fatal("read-only QR failed", err)
	}
	if _, err := c.loginCommand(t.Context(), false, json.RawMessage(`{"refresh":true}`)); err == nil {
		t.Fatal("status restarted authentication")
	}
	if _, err := c.loginCommand(t.Context(), true, json.RawMessage(`{"refresh":true}`)); err != nil || backend.logins != 1 {
		t.Fatal("explicit refresh failed", err)
	}
	for _, bad := range []string{"http://login.tailscale.com/a/example", "https://login.tailscale.com.evil.invalid/a/example", "https://login.tailscale.com/a/example?token=x", "https://login.tailscale.com/a/example#fragment", "https://login.tailscale.com/a/%65xample", "https://login.tailscale.com/a/", "https://login.tailscale.com/a/example/more"} {
		backend.state.AuthURL = bad
		view, err = c.loginCommand(t.Context(), false, json.RawMessage(`{"qr":true}`))
		if err == nil || view.AuthURL != "" || view.QR != nil || strings.Contains(err.Error(), bad) {
			t.Fatal("unsafe private URL leaked or was accepted")
		}
	}
}
