package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
)

func TestDirectLANCLILocalizedHelpAndStablePreview(t *testing.T) {
	var baseline string
	for _, locale := range []string{"en", "ja"} {
		var out bytes.Buffer
		if err := runWith(context.Background(), []string{"--locale", locale, "direct-lan", "--help"}, &out, strings.NewReader(""), nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "--listen") || !strings.Contains(out.String(), "--prefix") {
			t.Fatal(out.String())
		}
		out.Reset()
		args := []string{"--locale", locale, "--dry-run", "direct-lan", "configure", "--listen", "127.0.0.1:55446", "--prefix", "127.0.0.0/8"}
		if err := runWith(context.Background(), args, &out, strings.NewReader(""), nil); err != nil {
			t.Fatal(err)
		}
		var v struct {
			Command string
			Applied bool
			Payload struct {
				Mode      string
				DirectLAN struct {
					Listen   string
					Prefixes []string
				}
			}
		}
		if json.Unmarshal(out.Bytes(), &v) != nil || v.Command != "network.configure" || v.Applied || v.Payload.Mode != "direct-lan" || v.Payload.DirectLAN.Listen != "127.0.0.1:55446" {
			t.Fatal(out.String())
		}
		if baseline != "" && baseline != out.String() {
			t.Fatal("locale changed machine JSON")
		}
		baseline = out.String()
	}
}
func TestDirectLANCLIRejectsImplicitScopeAndPrivateArguments(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		for _, args := range [][]string{
			{"direct-lan", "configure", "--listen", "127.0.0.1:55446"},
			{"direct-lan", "configure", "--listen", "localhost:55446", "--prefix", "127.0.0.0/8"},
			{"direct-lan", "configure", "--listen", "127.0.0.1:54544", "--prefix", "127.0.0.0/8"},
			{"direct-lan", "join", "private-token"},
			{"command", "direct-lan.join", `{"invitation":"private-token"}`},
			{"direct-lan", "invite", "--to", strings.Repeat("a", 64), "--ttl", "601s"},
			{"setup", "--network", "tailnet", "--listen", "127.0.0.1:55446"},
			{"setup", "--network", "direct-lan", "--host", "127.0.0.1:55446"},
		} {
			called := false
			err := runWith(context.Background(), append([]string{"--locale", locale}, args...), io.Discard, strings.NewReader(""), func(context.Context, string, string, any) error { called = true; return nil })
			if err == nil || called {
				t.Fatal(args, err, called)
			}
		}
	}
}
func TestDirectLANCLIInvitationPreviewRedactsCapability(t *testing.T) {
	host, _ := directlan.GenerateIdentity()
	recipient, _ := directlan.GenerateIdentity()
	inv := directlan.Invitation{Version: 1, Host: directlan.Peer{Key: host.PublicKey(), TunnelKey: host.TunnelKey(), Name: "test-host", Endpoint: netip.MustParseAddrPort("127.0.0.1:55446")}, RecipientKey: recipient.PublicKey(), Token: strings.Repeat("A", 43), Expires: time.Now().Add(time.Minute)}
	encoded, err := inv.Encode()
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"invitation": encoded})
	for _, action := range []string{"inspect", "join", "cancel"} {
		var out bytes.Buffer
		err := runWith(context.Background(), []string{"--dry-run", "direct-lan", action, "--stdin"}, &out, strings.NewReader(string(input)), nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), encoded) || !strings.Contains(out.String(), "private input omitted") {
			t.Fatal("invitation leaked", out.String())
		}
	}
}

func TestDirectLANHumanStatusShowsSavedAndLiveSeparately(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		var out bytes.Buffer
		err := runWith(context.Background(), []string{"--locale", locale, "direct-lan", "status"}, &out, strings.NewReader(""), func(_ context.Context, _ string, raw string, value any) error {
			var request struct{ Name string }
			if err := json.Unmarshal([]byte(raw), &request); err != nil {
				return err
			}
			if request.Name != "direct-lan.status" {
				t.Fatalf("unexpected %s", request.Name)
			}
			return json.Unmarshal([]byte(`{"configured":true,"listenerReady":false,"endpoint":"127.0.0.1:55446","prefixes":["127.0.0.0/8"],"publicKey":"synthetic-test-key","recoveryRequired":true}`), value)
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, piece := range []string{"127.0.0.1:55446", "127.0.0.0/8", "synthetic-test-key"} {
			if !strings.Contains(out.String(), piece) {
				t.Fatal(out.String())
			}
		}
		if locale == "ja" {
			if !strings.Contains(out.String(), "停止中") || !strings.Contains(out.String(), "復旧確認") {
				t.Fatal(out.String())
			}
		} else if !strings.Contains(out.String(), "stopped") || !strings.Contains(out.String(), "recovery required") {
			t.Fatal(out.String())
		}
	}
}

func TestDirectLANSetupCanReuseSavedSelection(t *testing.T) {
	var out bytes.Buffer
	if err := runWith(context.Background(), []string{"--dry-run", "setup", "--network", "direct-lan"}, &out, strings.NewReader(""), nil); err != nil {
		t.Fatal(err)
	}
	var v struct{ Payload map[string]json.RawMessage }
	if json.Unmarshal(out.Bytes(), &v) != nil {
		t.Fatal(out.String())
	}
	if _, ok := v.Payload["directLAN"]; ok {
		t.Fatal("saved setup invented a new selection", out.String())
	}
}
