package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func rustDeskCLIArgs() []string {
	return []string{"setup", "--backend", "tailnet", "--name", "desk", "--id-peer", "id-peer", "--relay-peer", "relay-peer", "--key", base64.StdEncoding.EncodeToString(make([]byte, 32)), "--id-port", "21216", "--relay-port", "21317", "--local-id-port", "32216", "--local-relay-port", "32317"}
}

func runRustDeskCLI(t *testing.T, dir string, ja, dry bool, args []string, client controlCaller) (string, error) {
	t.Helper()
	var out bytes.Buffer
	handled, err := clientHelperCLI(context.Background(), "rustdesk", args, dir, ja, dry, &out, client)
	if !handled {
		t.Fatal("RustDesk helper was not dispatched")
	}
	return out.String(), err
}

func TestRustDeskCLIReviewedAtomicSaveSettingsAndJSONLocale(t *testing.T) {
	dir := t.TempDir()
	var names []string
	client := func(ctx context.Context, dir, raw string, result any) error {
		var command webui.Command
		if err := json.Unmarshal([]byte(raw), &command); err != nil {
			return err
		}
		names = append(names, command.Name)
		return offlineDefinitionCall(ctx, dir, raw, result)
	}
	args := append(rustDeskCLIArgs(), "--json")
	en, err := runRustDeskCLI(t, dir, false, false, args, client)
	if err != nil {
		t.Fatal(err)
	}
	ja, err := runRustDeskCLI(t, dir, true, false, args, client)
	if err != nil || en != ja {
		t.Fatal("machine contract changed with locale", err)
	}
	var preview core.RustDeskSetupReview
	if err := json.Unmarshal([]byte(en), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Saved || len(preview.Services) != 4 {
		t.Fatal("preview saved or omitted roles")
	}
	names = nil
	saved, err := runRustDeskCLI(t, dir, false, false, append(args, "--apply", "--review", preview.Revision), client)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"rustdesk.preview", "rustdesk.save"}) {
		t.Fatalf("unexpected mutation calls: %v", names)
	}
	var result core.RustDeskSetupReview
	if err := json.Unmarshal([]byte(saved), &result); err != nil || !result.Saved || !result.Applied {
		t.Fatal("reviewed save failed", err)
	}
	for _, ja := range []bool{false, true} {
		output, err := runRustDeskCLI(t, dir, ja, false, []string{"settings", "--group", "desk"}, client)
		if err != nil {
			t.Fatal(err)
		}
		for _, literal := range []string{"127.0.0.1:32216", "127.0.0.1:32317", "id-peer:21215", "id-peer:21216", "relay-peer:21317", "remote-ID/r", result.ClientSettings.PublicKey} {
			if !strings.Contains(output, literal) {
				t.Fatalf("client setting missing: %s", literal)
			}
		}
		for _, literal := range []string{text(ja, "Proxy: blank", "プロキシ: 空欄"), text(ja, "UDP: enabled", "UDP: 有効"), text(ja, "unverified", "未検証"), text(ja, "saved / stopped", "保存済み・停止中")} {
			if !strings.Contains(output, literal) {
				t.Fatalf("localized state/caution missing: %s", literal)
			}
		}
	}
}

func TestRustDeskCLIMissingReviewDryRunAndCanceledSave(t *testing.T) {
	for _, suffix := range [][]string{{"--apply"}, {"--apply", "--review", strings.Repeat("a", 64)}, {"--review", strings.Repeat("a", 64)}} {
		dir := t.TempDir()
		var calls []string
		client := func(ctx context.Context, dir, raw string, result any) error {
			var cmd webui.Command
			_ = json.Unmarshal([]byte(raw), &cmd)
			calls = append(calls, cmd.Name)
			return offlineDefinitionCall(ctx, dir, raw, result)
		}
		if _, err := runRustDeskCLI(t, dir, false, false, append(rustDeskCLIArgs(), suffix...), client); err == nil {
			t.Fatal("unreviewed save accepted")
		}
		for _, name := range calls {
			if name != "rustdesk.preview" {
				t.Fatalf("review failure applied %s", name)
			}
		}
	}
	dir := t.TempDir()
	var calls []string
	client := func(ctx context.Context, dir, raw string, result any) error {
		var cmd webui.Command
		_ = json.Unmarshal([]byte(raw), &cmd)
		calls = append(calls, cmd.Name)
		return offlineDefinitionCall(ctx, dir, raw, result)
	}
	if _, err := runRustDeskCLI(t, dir, false, true, append(rustDeskCLIArgs(), "--apply"), client); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"rustdesk.preview"}) {
		t.Fatal("dry run saved state")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	handled, err := clientHelperCLI(canceled, "rustdesk", rustDeskCLIArgs(), dir, false, false, io.Discard, client)
	if !handled || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled helper did not stop", err)
	}
}

func TestRustDeskCLIInputAndHelpAreInertBilingual(t *testing.T) {
	deny := func(context.Context, string, string, any) error {
		t.Fatal("invalid/help input contacted Core")
		return nil
	}
	for _, ja := range []bool{false, true} {
		for _, args := range [][]string{{}, {"--help"}, {"setup", "--help"}, {"settings", "--help"}} {
			output, err := runRustDeskCLI(t, t.TempDir(), ja, false, args, deny)
			if err != nil && !errors.Is(err, flag.ErrHelp) {
				t.Fatal(err)
			}
			if output == "" {
				t.Fatal("empty helper help")
			}
		}
		for _, extra := range [][]string{{"--local-id-port", "0"}, {"--relay-port", "65536"}, {"--ttl", "0s"}, {"--ttl", "1.5s"}, {"--lifetime", "until-stopped", "--ttl", "1h"}} {
			if _, err := runRustDeskCLI(t, t.TempDir(), ja, false, append(rustDeskCLIArgs(), extra...), deny); err == nil {
				t.Fatalf("invalid helper options accepted: %v", extra)
			}
		}
	}
	handled, err := clientHelperCLI(context.Background(), "settings", nil, t.TempDir(), false, false, io.Discard, deny)
	if handled || err != nil {
		t.Fatal("helper shadowed general settings")
	}
}

func TestRustDeskCLIFiniteLifetimeAndSafeFollowupProfile(t *testing.T) {
	dir := t.TempDir()
	out, err := runRustDeskCLI(t, dir, false, false, append(rustDeskCLIArgs(), "--ttl", "72h", "--json"), offlineDefinitionCall)
	if err != nil {
		t.Fatal(err)
	}
	var review core.RustDeskSetupReview
	if err := json.Unmarshal([]byte(out), &review); err != nil {
		t.Fatal(err)
	}
	for _, s := range review.Services {
		if s.Lifetime != "finite" || s.TTLSeconds != 259200 {
			t.Fatal("finite role lifetime lost")
		}
	}
	weird := "/tmp/path with $(untrusted) 'quotes'"
	example := rustDeskCommandExample(weird, "--dry-run", "group", "start", "desk")
	if !strings.HasPrefix(example, "argv: ") {
		t.Fatal("unsafe path was presented as shell syntax")
	}
	var argv []string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(example, "argv: ")), &argv); err != nil || argv[2] != weird || argv[3] != "--dry-run" {
		t.Fatal("profile path or review step lost", err)
	}
}
