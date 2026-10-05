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
