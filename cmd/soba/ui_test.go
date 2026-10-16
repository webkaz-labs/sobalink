package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
)

const syntheticUICode = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func setupUI(t *testing.T) (*bytes.Buffer, controlCaller, *int, *string) {
	t.Helper()
	oldTerminal, oldDesktop, oldOpen := uiPrivateTerminal, uiDesktopAvailable, launchUIBrowser
	t.Cleanup(func() { uiPrivateTerminal, uiDesktopAvailable, launchUIBrowser = oldTerminal, oldDesktop, oldOpen })
	uiPrivateTerminal = func(_ io.Writer) bool { return true }
	uiDesktopAvailable = func() bool { return true }
	opened := ""
	calls := 0
	launchUIBrowser = func(_ context.Context, target string) error { opened = target; return nil }
	client := func(_ context.Context, _, raw string, result any) error {
		calls++
		if raw != "ui" {
			t.Fatal("unexpected IPC")
		}
		return json.Unmarshal([]byte(`{"url":"http://127.0.0.1:12345","code":"`+syntheticUICode+`"}`), result)
	}
	return new(bytes.Buffer), client, &calls, &opened
}
func TestUIBrowserLaunchKeepsCapabilityOutOfOutput(t *testing.T) {
	out, client, calls, opened := setupUI(t)
	if err := uiCommand(t.Context(), nil, "unused", false, false, out, client); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || *opened != "http://127.0.0.1:12345/#handoff="+syntheticUICode || strings.Contains(out.String(), syntheticUICode) {
		t.Fatal("wrong launch or private output")
	}
}
func TestUIExplicitManualModesDoNotLaunch(t *testing.T) {
	for _, mode := range []string{"--no-browser", "--json"} {
		t.Run(mode, func(t *testing.T) {
			out, client, _, opened := setupUI(t)
			if err := uiCommand(t.Context(), []string{mode}, "unused", true, false, out, client); err != nil {
				t.Fatal(err)
			}
			if *opened != "" || !strings.Contains(out.String(), syntheticUICode) {
				t.Fatal("manual mode failed")
			}
			if mode == "--json" && !json.Valid(out.Bytes()) {
				t.Fatal("invalid JSON")
			}
		})
	}
}
func TestUIRedirectRequiresExplicitJSON(t *testing.T) {
	out, client, calls, opened := setupUI(t)
	uiPrivateTerminal = func(_ io.Writer) bool { return false }
	if err := uiCommand(t.Context(), nil, "unused", false, false, out, client); err == nil || *calls != 0 || *opened != "" {
		t.Fatal("implicit redirected capability")
	}
	if err := uiCommand(t.Context(), []string{"--json"}, "unused", false, false, out, client); err != nil || !json.Valid(out.Bytes()) {
		t.Fatal("explicit machine mode failed")
	}
}
func TestUIHeadlessRequiresExplicitManualFallback(t *testing.T) {
	out, client, calls, opened := setupUI(t)
	uiDesktopAvailable = func() bool { return false }
	err := uiCommand(t.Context(), nil, "unused", false, false, out, client)
	if err == nil || *calls != 0 || *opened != "" || strings.Contains(out.String(), syntheticUICode) || !strings.Contains(err.Error(), "--no-browser") {
		t.Fatal("headless requested or exposed a capability")
	}
	if err := uiCommand(t.Context(), []string{"--no-browser"}, "unused", false, false, out, client); err != nil || !strings.Contains(out.String(), syntheticUICode) {
		t.Fatal("explicit headless fallback failed")
	}
}

func TestUIOpenerErrorsNeverRevealArguments(t *testing.T) {
	out, client, _, _ := setupUI(t)
	launchUIBrowser = func(_ context.Context, target string) error { return errors.New(target) }
	err := uiCommand(t.Context(), nil, "unused", false, false, out, client)
	if err == nil || strings.Contains(err.Error()+out.String(), syntheticUICode) || !strings.Contains(err.Error(), "--no-browser") {
		t.Fatal("opener failure leaked or lacked recovery")
	}
}
func TestUICancelAndInvalidArgumentsDoNotLaunch(t *testing.T) {
	out, client, _, opened := setupUI(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := uiCommand(ctx, nil, "unused", false, false, out, client); err == nil || *opened != "" {
		t.Fatal("cancelled launch")
	}
	for _, args := range [][]string{{"unexpected"}, {"--unknown"}, {"--json", "--no-browser"}} {
		if err := uiCommand(t.Context(), args, "unused", false, false, out, client); err == nil {
			t.Fatal("invalid arguments accepted")
		}
	}
}
func TestUIHandoffURLRejectsUntrustedVariants(t *testing.T) {
	for _, raw := range []string{"http://localhost:12345", "http://127.0.0.2:12345", "http://[::1]:12345", "https://127.0.0.1:12345", "http://127.0.0.1:0", "http://127.0.0.1:0123", "http://127.0.0.1:12345?", "http://127.0.0.1:12345#", "http://127.0.0.1:12345?code=secret", "http://127.0.0.1:12345/path", "http://user@127.0.0.1:12345", "http://192.0.2.1:12345"} {
		if _, err := uiHandoffURL(raw, syntheticUICode); err == nil {
			t.Fatal("unsafe URL accepted")
		}
	}
	for _, code := range []string{"", "short", strings.Repeat("A", 33), strings.Repeat("!", 32)} {
		if _, err := uiHandoffURL("http://127.0.0.1:12345", code); err == nil {
			t.Fatal("invalid code accepted")
		}
	}
}

func TestUIDesktopDetectionRejectsSSHAndMissingLinuxDisplay(t *testing.T) {
	for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		t.Setenv(key, "")
	}
	t.Setenv("DISPLAY", ":synthetic")
	t.Setenv("WAYLAND_DISPLAY", "")
	if !uiDesktopAvailable() {
		t.Fatal("desktop hint ignored")
	}
	for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		t.Setenv(key, "synthetic")
		if uiDesktopAvailable() {
			t.Fatal("SSH attempted a local desktop launch")
		}
		t.Setenv(key, "")
	}
	t.Setenv("DISPLAY", "")
	if runtime.GOOS == "linux" && uiDesktopAvailable() {
		t.Fatal("headless Linux attempted browser launch")
	}
}

func TestUIRecoveryKeepsSelectedProfile(t *testing.T) {
	out, client, _, _ := setupUI(t)
	launchUIBrowser = func(context.Context, string) error { return errors.New("unavailable") }
	err := uiCommand(t.Context(), nil, "example-profile", false, false, out, client)
	if err == nil || !strings.Contains(err.Error(), "soba --state-dir example-profile ui --no-browser") {
		t.Fatal("recovery changed the selected profile")
	}
}
