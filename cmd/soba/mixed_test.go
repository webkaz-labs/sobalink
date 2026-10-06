package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMixedCLIExplicitSetupAndDryRun(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		var out bytes.Buffer
		called := false
		e := runWith(context.Background(), []string{"--locale", locale, "--dry-run", "mixed", "setup", "--backends", "direct-lan,tailnet"}, &out, strings.NewReader(""), func(context.Context, string, string, any) error { called = true; return nil })
		if e != nil || called {
			t.Fatal(e, called)
		}
		var got map[string]any
		if e = json.Unmarshal(out.Bytes(), &got); e != nil || got["applied"] != false || got["command"] != "network.configure" {
			t.Fatal(out.String(), e)
		}
	}
}
func TestMixedCLINeverSelectsBackendsImplicitly(t *testing.T) {
	for _, args := range [][]string{{"setup"}, {"setup", "--backends", "tailnet"}, {"setup", "--backends", "lan,lan"}, {"bind", "--peer", "synthetic-one"}} {
		var out bytes.Buffer
		called := false
		e := mixedCLI(args, false, false, &out, nil, func(string, any) error { called = true; return nil })
		if e == nil || called {
			t.Fatal(args, e, called)
		}
	}
}

func TestMixedCLIAvailabilityClassesAndRestart(t *testing.T) {
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		query := func(_ string, _ any, dst any) error {
			raw := []byte(`{"configured":true,"backends":["direct-lan","tailnet"],"backendStates":[{"backend":"direct-lan","availability":"confirmed-unavailable","restartRequired":true},{"backend":"tailnet","availability":"readiness-unconfirmed"}]}`)
			return json.Unmarshal(raw, dst)
		}
		if e := mixedCLI([]string{"show"}, ja, false, &out, query, nil); e != nil {
			t.Fatal(e)
		}
		for _, want := range []string{text(ja, "Confirmed unavailable", "利用不可を確認済み"), text(ja, "Readiness unconfirmed", "準備状態を未確認"), text(ja, "restart soba", "soba を再起動")} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("missing %s: %s", want, out.String())
			}
		}
	}
}

func TestMixedCLIUnconfirmedRuntimeStatus(t *testing.T) {
	for _, ja := range []bool{false, true} {
		query := func(_ string, _ any, dst any) error {
			return json.Unmarshal([]byte(`{"configured":true,"backendStatusAvailable":false,"backends":["direct-lan","tailnet"]}`), dst)
		}
		var out bytes.Buffer
		if e := mixedCLI([]string{"show"}, ja, false, &out, query, nil); e != nil {
			t.Fatal(e)
		}
		for _, want := range []string{text(ja, "Backend status could not be confirmed", "接続方式の状態を確認できません"), "soba mixed show", text(ja, "active traffic route are not confirmed", "現在の通信経路は確認できていません")} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("missing %q: %s", want, out.String())
			}
		}
		out.Reset()
		if e := mixedCLI([]string{"show", "--json"}, ja, false, &out, query, nil); e != nil {
			t.Fatal(e)
		}
		var state map[string]any
		if e := json.Unmarshal(out.Bytes(), &state); e != nil || state["backendStatusAvailable"] != false {
			t.Fatalf("machine status changed: %s %v", out.String(), e)
		}
	}
}
