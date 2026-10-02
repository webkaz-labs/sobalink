package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/webkaz-labs/sobalink/internal/app"
)

const sampleAuthURL = "https://login.tailscale.com/a/example-not-a-real-login"

func TestLoginOptions(t *testing.T) {
	for _, args := range [][]string{{"--qr"}, {"--link"}, {"--no-browser"}} {
		o, e := parseLoginOptions(args, &bytes.Buffer{})
		if e != nil || !o.NoBrowser {
			t.Fatal(args, o, e)
		}
	}
	for _, args := range [][]string{{"--qr-format", "invalid"}, {"--timeout", "0s"}, {"unexpected"}} {
		if _, e := parseLoginOptions(args, &bytes.Buffer{}); e == nil {
			t.Fatal("invalid login options accepted")
		}
	}
}
func TestQRPrivateTerminalRejectsRedirect(t *testing.T) {
	f, e := os.CreateTemp(t.TempDir(), "qr-log")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if checkPrivateTerminal(f) == nil {
		t.Fatal("QR may leak to redirected log")
	}
}
func TestQRRenderMatchesEncoder(t *testing.T) {
	q, e := qrcode.New(sampleAuthURL, qrcode.Medium)
	if e != nil {
		t.Fatal(e)
	}
	bits := q.Bitmap()
	for _, format := range []string{"small", "large"} {
		var out bytes.Buffer
		if e = renderLoginQR(&out, sampleAuthURL, format); e != nil {
			t.Fatal(e)
		}
		rows := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		step := 2
		if format == "large" {
			step = 1
		}
		if len(rows) != (len(bits)+step-1)/step {
			t.Fatal("QR height changed")
		}
		for y, line := range rows {
			line = strings.TrimPrefix(line, "\x1b[30;47m")
			line = strings.TrimSuffix(line, "\x1b[0m")
			cells := []rune(line)
			wantWidth := len(bits)
			if step == 1 {
				wantWidth *= 2
			}
			if len(cells) != wantWidth {
				t.Fatal("QR width changed")
			}
			for x := range bits[0] {
				if step == 1 {
					for _, v := range cells[2*x : 2*x+2] {
						if (v == '█') != bits[y][x] {
							t.Fatal("large QR pixel changed")
						}
					}
				} else {
					top := cells[x] == '█' || cells[x] == '▀'
					bottom := cells[x] == '█' || cells[x] == '▄'
					if top != bits[y*2][x] || (y*2+1 < len(bits) && bottom != bits[y*2+1][x]) {
						t.Fatal("small QR pixel changed")
					}
				}
			}
		}
	}
	for _, bad := range []string{"https://example.invalid/a/example", "https://login.tailscale.com/a/", "http://login.tailscale.com/a/example", "https://login.tailscale.com/a/example?token=x"} {
		var out bytes.Buffer
		if renderLoginQR(&out, bad, "small") == nil || out.Len() != 0 {
			t.Fatal("untrusted URL rendered")
		}
	}
}
func TestLoginPhoneWaitsForCompletionWithoutOpeningBrowser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		browser := 0
		old := launchLoginBrowser
		launchLoginBrowser = func(context.Context, string) error { browser++; return nil }
		defer func() { launchLoginBrowser = old }()
		installRequest(t, func(_ context.Context, _, command string, v any) error {
			switch command {
			case "login":
				return nil
			case "auth-url":
				return assign(v, map[string]string{"url": sampleAuthURL})
			case "status":
				calls++
				if calls >= 3 {
					return assign(v, app.Status{State: "idle", Backend: "Running", Mode: "rules"})
				}
				return assign(v, app.Status{State: "needs-login", Backend: "NeedsLogin"})
			}
			return errors.New("unexpected command")
		})
		var out bytes.Buffer
		e := loginWithOptions(t.Context(), "unused", &out, loginOptions{QR: true, NoBrowser: true, QRFormat: "small", Timeout: 5 * time.Second})
		if e != nil || browser != 0 || !strings.Contains(out.String(), "Sign-in complete.") || strings.Count(out.String(), sampleAuthURL) != 1 {
			t.Fatal("phone flow failed", e, browser)
		}
	})
}
func TestLoginBrowserFallbackAndDeviceApproval(t *testing.T) {
	old := launchLoginBrowser
	launchLoginBrowser = func(context.Context, string) error { return errors.New("no browser") }
	defer func() { launchLoginBrowser = old }()
	calls := 0
	installRequest(t, func(_ context.Context, _, command string, v any) error {
		switch command {
		case "login":
			return nil
		case "auth-url":
			return assign(v, map[string]string{"url": sampleAuthURL})
		case "status":
			calls++
			if calls > 1 {
				return assign(v, app.Status{State: "approval-required", Backend: "NeedsMachineAuth"})
			}
			return assign(v, app.Status{State: "needs-login", Backend: "NeedsLogin"})
		}
		return nil
	})
	var out bytes.Buffer
	if e := loginWithOptions(t.Context(), "unused", &out, loginOptions{Timeout: time.Minute}); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), "Browser could not be opened") || !strings.Contains(out.String(), "still needs approval") {
		t.Fatal("missing next action")
	}
}
func TestLoginTimeoutDoesNotInventServerExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installRequest(t, func(_ context.Context, _, command string, v any) error {
			if command == "auth-url" {
				return assign(v, map[string]string{"url": sampleAuthURL})
			}
			if command == "status" {
				return assign(v, app.Status{State: "needs-login", Backend: "NeedsLogin"})
			}
			return nil
		})
		var out bytes.Buffer
		e := loginWithOptions(t.Context(), "unused", &out, loginOptions{NoBrowser: true, Timeout: 2 * time.Second})
		if e == nil || !strings.Contains(e.Error(), "server expiry is not known") {
			t.Fatal(e)
		}
	})
}
func TestLoginRejectsChangedUntrustedAuthURL(t *testing.T) {
	installRequest(t, func(_ context.Context, _, command string, v any) error {
		if command == "auth-url" {
			return assign(v, map[string]string{"url": "https://example.invalid/a/stolen"})
		}
		if command == "status" {
			return assign(v, app.Status{Backend: "NeedsLogin"})
		}
		return nil
	})
	var out bytes.Buffer
	if e := loginWithOptions(t.Context(), "unused", &out, loginOptions{QR: true, NoBrowser: true, Timeout: time.Minute}); e == nil {
		t.Fatal("untrusted auth accepted")
	}
	if strings.Contains(out.String(), "stolen") {
		t.Fatal("untrusted URL printed")
	}
}

func TestLoginBrowserRedirectSuppressesPrivateURL(t *testing.T) {
	f, e := os.CreateTemp(t.TempDir(), "browser-log")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	calls := 0
	opened := ""
	old := launchLoginBrowser
	launchLoginBrowser = func(_ context.Context, url string) error { opened = url; return nil }
	defer func() { launchLoginBrowser = old }()
	installRequest(t, func(_ context.Context, _, command string, v any) error {
		switch command {
		case "auth-url":
			return assign(v, map[string]string{"url": sampleAuthURL})
		case "status":
			calls++
			if calls > 1 {
				return assign(v, app.Status{Backend: "Running"})
			}
			return assign(v, app.Status{Backend: "NeedsLogin"})
		}
		return nil
	})
	if e = loginWithOptions(t.Context(), "unused", f, loginOptions{Timeout: time.Minute}); e != nil {
		t.Fatal(e)
	}
	f.Close()
	contents, e := os.ReadFile(f.Name())
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(contents), sampleAuthURL) || opened != sampleAuthURL {
		t.Fatal("redirected browser flow leaked or didn't open")
	}
}
func TestAuthURLRejectsPathAndEncodedControlVariants(t *testing.T) {
	for _, url := range []string{"https://login.tailscale.com/a/../admin", "https://login.tailscale.com/a/%2e%2e/admin", "https://login.tailscale.com/a/a/b", "https://login.tailscale.com/a/a%2fb", "https://login.tailscale.com/a/%0a", "https://login.tailscale.com/a/a?", "https://login.tailscale.com/a/a#fragment", "https://login.tailscale.com/a/a\\b"} {
		if validAuthURL(url) {
			t.Fatal("accepted", url)
		}
	}
}

func TestLoginApprovalPendingDoesNotRestartAuthentication(t *testing.T) {
	installRequest(t, func(_ context.Context, _, command string, v any) error {
		if command != "status" {
			t.Fatalf("unexpected authentication operation %s", command)
		}
		return assign(v, app.Status{State: "approval-required", Backend: "NeedsMachineAuth"})
	})
	var out bytes.Buffer
	if e := loginWithOptions(t.Context(), "unused", &out, loginOptions{Timeout: time.Minute}); e != nil || !strings.Contains(out.String(), "needs approval") {
		t.Fatal(e)
	}
}

func TestQRDisplaySetupFailureWritesNoEscapeOrCode(t *testing.T) {
	old := prepareLoginQRDisplay
	t.Cleanup(func() { prepareLoginQRDisplay = old })
	setupErr := errors.New("Terminal cannot display QR colors. Use the private link instead.")
	prepareLoginQRDisplay = func(io.Writer) (func(), error) { return nil, setupErr }
	var out bytes.Buffer
	if err := renderLoginQR(&out, sampleAuthURL, "small"); !errors.Is(err, setupErr) || out.Len() != 0 {
		t.Fatalf("unsupported terminal received QR data: err %v, bytes %d", err, out.Len())
	}
}

func TestQRDisplayModeRestoredAfterOutputFailure(t *testing.T) {
	old := prepareLoginQRDisplay
	t.Cleanup(func() { prepareLoginQRDisplay = old })
	for _, writer := range []io.Writer{promptFailWriter{io.ErrClosedPipe}, shortLocaleWriter{}} {
		restored := false
		prepareLoginQRDisplay = func(io.Writer) (func(), error) { return func() { restored = true }, nil }
		if err := renderLoginQR(writer, sampleAuthURL, "small"); err == nil || !restored {
			t.Fatalf("QR write failure did not restore display: err %v, restored %v", err, restored)
		}
	}
}
