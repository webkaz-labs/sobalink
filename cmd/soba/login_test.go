package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func loginFixture(t *testing.T, views ...core.LoginView) controlCaller {
	t.Helper()
	index := 0
	return func(ctx context.Context, _ string, raw string, result any) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var command webui.Command
		if err := json.Unmarshal([]byte(raw), &command); err != nil {
			return err
		}
		if command.Name != "network.login" && command.Name != "network.login.status" {
			t.Fatal("unexpected operation", command.Name)
		}
		if index >= len(views) {
			t.Fatal("unexpected repeated login request")
		}
		if index > 0 && command.Name != "network.login.status" {
			t.Fatal("poll restarted sign-in")
		}
		encoded, _ := json.Marshal(views[index])
		index++
		return json.Unmarshal(encoded, result)
	}
}

func withImmediateLoginPoll(t *testing.T) {
	old := loginPause
	t.Cleanup(func() { loginPause = old })
	loginPause = func(ctx context.Context) error { return ctx.Err() }
}

func TestLoginWaitKeepsMachineOutputAndPrivateLinkSeparate(t *testing.T) {
	withImmediateLoginPoll(t)
	var out bytes.Buffer
	err := runWith(t.Context(), []string{"login", "--wait"}, &out, strings.NewReader(""), loginFixture(t,
		core.LoginView{State: "waiting", AuthURL: "https://login.tailscale.com/a/fictional"}, core.LoginView{State: "connected"}))
	if err != nil || strings.Contains(out.String(), "fictional") || !strings.Contains(out.String(), `"state":"connected"`) {
		t.Fatal(out.String(), err)
	}
}

func TestBrowserLoginSuppressesPrivateLinkInRedirectedOutput(t *testing.T) {
	withImmediateLoginPoll(t)
	old := launchLoginBrowser
	t.Cleanup(func() { launchLoginBrowser = old })
	opened := false
	launchLoginBrowser = func(_ context.Context, url string) error {
		opened = core.ValidAuthURL(url)
		return errors.New("unavailable")
	}
	var out bytes.Buffer
	err := runWith(t.Context(), []string{"login", "--browser"}, &out, strings.NewReader(""), loginFixture(t,
		core.LoginView{State: "waiting", AuthURL: "https://login.tailscale.com/a/fictional"}, core.LoginView{State: "approval-required"}))
	if err != nil || !opened || strings.Contains(out.String(), "https://") || !strings.Contains(out.String(), "approve this node") {
		t.Fatal(out.String(), err)
	}
}

func TestQRLoginRefusesRedirectBeforeRequest(t *testing.T) {
	var out bytes.Buffer
	client := func(context.Context, string, string, any) error { t.Fatal("redirected QR contacted agent"); return nil }
	if err := runWith(t.Context(), []string{"login", "--qr"}, &out, strings.NewReader(""), client); err == nil || out.Len() != 0 {
		t.Fatal("redirected QR accepted")
	}
}

func TestLoginLinkExplicitAndUnsafeURLRejected(t *testing.T) {
	for _, valid := range []bool{false, true} {
		url := "https://other.invalid/private"
		if valid {
			url = "https://login.tailscale.com/a/fictional"
		}
		var out bytes.Buffer
		err := runWith(t.Context(), []string{"--locale", "ja", "login", "--link"}, &out, strings.NewReader(""), loginFixture(t, core.LoginView{State: "waiting", AuthURL: url}))
		if valid && (err != nil || !strings.Contains(out.String(), url)) {
			t.Fatal(out.String(), err)
		}
		if !valid && (err == nil || strings.Contains(out.String(), url)) {
			t.Fatal("unexpected address was exposed")
		}
	}
}

func TestLoginWaitPassesCancellationToClient(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	client := func(ctx context.Context, _ string, _ string, _ any) error { cancel(); <-ctx.Done(); return ctx.Err() }
	err := runWith(ctx, []string{"login", "--wait", "--timeout", "1h"}, io.Discard, strings.NewReader(""), client)
	if err == nil {
		t.Fatal("cancelled login succeeded")
	}
}

func TestLoginOptionsHaveNoArbitraryThirtyMinuteCeiling(t *testing.T) {
	var out bytes.Buffer
	err := runWith(t.Context(), []string{"--dry-run", "login", "--wait", "--timeout", (time.Hour).String()}, &out, strings.NewReader(""), nil)
	if err != nil || !strings.Contains(out.String(), `"applied": false`) {
		t.Fatal(out.String(), err)
	}
}
