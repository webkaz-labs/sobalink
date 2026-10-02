package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/control"
	"github.com/webkaz-labs/tsnet-bridge/internal/webui"
)

func TestHelpAndVersionAreOffline(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}, {"version"}, {"--version"}, {"setup", "--help"}, {"share", "--help"}, {"connect", "-h"}, {"help", "message"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "absent")
			var out bytes.Buffer
			if err := run(context.Background(), append([]string{"--state-dir", dir}, args...), &out); err != nil {
				t.Fatal(err)
			}
			if out.Len() == 0 {
				t.Fatal("missing output")
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("help/version changed state: %v", err)
			}
		})
	}
}
func TestLocalePrecedence(t *testing.T) {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		t.Setenv(key, "")
	}
	t.Setenv("LANG", "ja_JP.UTF-8")
	if !japanese("auto") {
		t.Fatal("LANG ignored")
	}
	t.Setenv("LC_MESSAGES", "en_US.UTF-8")
	if japanese("auto") {
		t.Fatal("LC_MESSAGES ignored")
	}
	t.Setenv("LC_ALL", "ja-JP")
	if !japanese("auto") || japanese("en") || !japanese("ja") {
		t.Fatal("override ignored")
	}
	for _, value := range []string{"C", "xx_XX", "jargon", "japanese"} {
		t.Setenv("LC_ALL", value)
		if japanese("auto") {
			t.Fatalf("invalid locale %q selected Japanese", value)
		}
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"--locale", "ja", "--help"}, &out); err != nil || !strings.Contains(out.String(), "ローカル画面") {
		t.Fatalf("localized help: %v %s", err, out.String())
	}
}
func TestBadArgumentsDoNotContactAgent(t *testing.T) {
	for _, args := range [][]string{{"login", "extra"}, {"peers", "extra"}, {"status", "extra"}, {"setup", "extra"}, {"message", "one"}, {"share", "--name", "demo", "--ports", "80"}, {"connect", "--name", "demo", "--ports", "80"}, {"share", "--name", "demo", "--ports", "80", "--peers", "peer", "--ttl", "500ms"}, {"share", "--name", "demo", "--ports", "80", "--peers", "peer,,other"}, {"command", "x", "{"}, {"help", "x"}, {"--locale", "xx", "help"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out bytes.Buffer
			err := run(context.Background(), append([]string{"--state-dir", filepath.Join(t.TempDir(), "absent")}, args...), &out)
			if err == nil || strings.Contains(err.Error(), "Command failed") {
				t.Fatalf("invalid arguments reached IPC or succeeded: %v", err)
			}
		})
	}
}
func privateStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "soba-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}
func TestPrivateIPCCommandPayloadsAndStableJSON(t *testing.T) {
	dir := privateStateDir(t)
	if err := config.SecureDir(dir); err != nil {
		t.Fatal(err)
	}
	requests := make(chan string, 32)
	server, err := control.Serve(context.Background(), dir, func(_ context.Context, raw string) (any, error) {
		requests <- raw
		return json.RawMessage(`{"number":9007199254740993,"value":"日本語"}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	cases := []struct {
		args    []string
		name    string
		payload string
	}{
		{[]string{"setup", "--network", "none", "--name", "example"}, "network.configure", `{"mode":"none","hostname":"example"}`},
		{[]string{"login"}, "network.login", `{}`},
		{[]string{"trust", "peer"}, "peer.trust", `{"peerId":"peer","trusted":true}`},
		{[]string{"revoke", "peer"}, "peer.trust", `{"peerId":"peer","trusted":false}`},
		{[]string{"message", "peer", "日本語"}, "message.send", `{"peerId":"peer","text":"日本語"}`},
		{[]string{"send", "peer", "one", "two"}, "transfer.send", `{"peerId":"peer","paths":["one","two"]}`},
		{[]string{"accept", "batch", "directory"}, "transfer.accept", `{"transferId":"batch","destination":"directory"}`},
		{[]string{"cancel", "batch"}, "transfer.cancel", `{"transferId":"batch"}`},
		{[]string{"retry", "batch"}, "transfer.retry", `{"transferId":"batch"}`},
		{[]string{"forget", "batch"}, "transfer.forget", `{"transferId":"batch"}`},
		{[]string{"stop-service", "service"}, "service.stop", `{"id":"service"}`},
		{[]string{"share", "--name", "example", "--network", "udp", "--ports", "10000-10010", "--exclude", "10005", "--peers", "first, second", "--ttl", "2h", "--discoverable"}, "service.share", `{"name":"example","network":"udp","ports":"10000-10010","excludePorts":"10005","peerIds":["first","second"],"ttlSeconds":7200,"localPort":0,"purpose":"custom","discoverable":true}`},
		{[]string{"connect", "--name", "example", "--ports", "80", "--peer", "peer", "--local-port", "8080"}, "service.connect", `{"name":"example","network":"tcp","ports":"80","excludePorts":"","peerId":"peer","ttlSeconds":3600,"localPort":8080,"purpose":"custom","discoverable":false}`},
		{[]string{"command", "custom.action", `{"number":9007199254740993}`}, "custom.action", `{"number":9007199254740993}`},
	}
	for _, tt := range cases {
		for _, locale := range []string{"en", "ja"} {
			var out bytes.Buffer
			err := run(context.Background(), append([]string{"--state-dir", dir, "--locale", locale}, tt.args...), &out)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "9007199254740993") || !strings.Contains(out.String(), "日本語") {
				t.Fatalf("JSON changed: %s", out.String())
			}
			var cmd webui.Command
			if err := json.Unmarshal([]byte(<-requests), &cmd); err != nil {
				t.Fatal(err)
			}
			if cmd.Name != tt.name || cmd.RequestID == "" {
				t.Fatalf("bad command %+v", cmd)
			}
			var got, want bytes.Buffer
			if err := json.Compact(&got, cmd.Payload); err != nil {
				t.Fatal(err)
			}
			// Compare decoded values except the deliberately large numeric fixture.
			if tt.name == "transfer.send" {
				a, _ := filepath.Abs("one")
				b, _ := filepath.Abs("two")
				raw, _ := json.Marshal(map[string]any{"peerId": "peer", "paths": []string{a, b}})
				tt.payload = string(raw)
			}
			if tt.name == "transfer.accept" {
				d, _ := filepath.Abs("directory")
				raw, _ := json.Marshal(map[string]any{"transferId": "batch", "destination": d})
				tt.payload = string(raw)
			}
			var a, b any
			decoder := json.NewDecoder(bytes.NewReader(got.Bytes()))
			decoder.UseNumber()
			if err := decoder.Decode(&a); err != nil {
				t.Fatal(err)
			}
			decoder = json.NewDecoder(strings.NewReader(tt.payload))
			decoder.UseNumber()
			if err := decoder.Decode(&b); err != nil {
				t.Fatal(err)
			}
			ga, _ := json.Marshal(a)
			gb, _ := json.Marshal(b)
			want.Write(gb)
			if !bytes.Equal(ga, want.Bytes()) {
				t.Fatalf("%s got %s want %s", tt.name, ga, gb)
			}
		}
	}
	for _, name := range []string{"status", "ui", "stop", "peers"} {
		var out bytes.Buffer
		if err := run(context.Background(), []string{"--state-dir", dir, name}, &out); err != nil {
			t.Fatal(err)
		}
		expected := name
		if name == "peers" {
			expected = "status"
		}
		if raw := <-requests; raw != expected {
			t.Fatalf("got %s", raw)
		}
	}
}
func TestForegroundStartsOfflineAndStopsCleanly(t *testing.T) {
	dir := privateStateDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() { done <- run(ctx, []string{"--state-dir", dir, "start"}, &out) }()
	var status json.RawMessage
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := control.Call(ctx, dir, "status", &status)
		if err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("start failed: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("private IPC never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !bytes.Contains(status, []byte(`"network":"none"`)) && !bytes.Contains(status, []byte(`"mode":"none"`)) {
		t.Fatalf("fresh start must be offline: %s", status)
	}
	var stopped any
	if err := control.Call(ctx, dir, "stop", &stopped); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("stop did not finish")
	}
	if strings.Contains(out.String(), "One-time code") {
		t.Fatal("redirected startup leaked one-time code")
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal("shutdown retained profile lock:", err)
	}
	lock.Close()
	if _, err := os.Stat(filepath.Join(dir, "identity")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("offline start created identity: %v", err)
	}
}
