package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/webkaz-labs/tsnet-bridge/internal/app"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/control"
	"github.com/webkaz-labs/tsnet-bridge/internal/identity"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var version = "0.0.0-dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := run(ctx, os.Args[1:], os.Stdin, os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, "tsnet-bridge:", e)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 1 {
		switch args[0] {
		case "--version":
			fmt.Fprintln(out, "tsnet-bridge", version)
			return nil
		case "--help", "-h":
			fmt.Fprintln(out, help)
			return nil
		}
	}
	dir, e := config.DefaultDir()
	if e != nil {
		return e
	}
	global := flag.NewFlagSet("tsnet-bridge", flag.ContinueOnError)
	global.SetOutput(out)
	global.StringVar(&dir, "state-dir", dir, "private profile directory (put before command)")
	if e = global.Parse(args); e != nil {
		return e
	}
	args = global.Args()
	cmd := "start"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "version", "--version":
		fmt.Fprintln(out, "tsnet-bridge", version)
		return nil
	case "help":
		fmt.Fprintln(out, help)
		return nil
	case "setup":
		return setup(dir, args, in, out)
	case "status", "doctor", "stop", "reconnect", "logout":
		if len(args) > 1 || (len(args) == 1 && args[0] != "--json") {
			return errors.New("only --json is accepted")
		}
		var s app.Status
		e = call(ctx, dir, cmd, &s)
		if e != nil && cmd == "stop" {
			if control.Unavailable(e) {
				fmt.Fprintln(out, "Stopped (no process)")
				return nil
			}
		}
		if e != nil && cmd == "status" && control.Unavailable(e) {
			s = app.Status{State: "stopped", Reason: "No reachable process; run tsnet-bridge to start", RustDesk: "unverified"}
			e = nil
		}
		if e != nil {
			return e
		}
		return printStatus(out, s, len(args) > 0)
	case "settings":
		return settings(dir, args, out)
	case "login":
		noBrowser := len(args) == 1 && args[0] == "--no-browser"
		if len(args) > 0 && !noBrowser {
			return errors.New("usage: login [--no-browser]")
		}
		if e = start(ctx, dir, out); e != nil {
			return e
		}
		return login(ctx, dir, out, noBrowser)
	case "run":
		if len(args) != 0 {
			return errors.New("run takes no arguments")
		}
		c, e := config.Load(dir)
		if e != nil {
			return fmt.Errorf("profile unavailable: run setup first: %w", e)
		}
		if e = app.Preflight(c); e != nil {
			return e
		}
		if c.Mode == "socks" {
			if e = app.EnsureCredentials(dir); e != nil {
				return e
			}
		}
		n, e := identity.New(dir, c.Hostname)
		if e != nil {
			return e
		}
		s := &app.Service{Dir: dir, Config: c, Node: n}
		return s.Run(ctx)
	case "start":
		if len(args) != 0 {
			return errors.New("start takes no arguments")
		}
		if _, e = config.Load(dir); os.IsNotExist(e) {
			if e = setup(dir, nil, in, out); e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
		if e = start(ctx, dir, out); e != nil {
			return e
		}
		var s app.Status
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			if e = call(ctx, dir, "status", &s); e != nil {
				return e
			}
			if s.State != "starting" {
				break
			}
			if e = pause(ctx, 300*time.Millisecond); e != nil {
				return e
			}
		}
		if s.State == "needs-login" {
			fmt.Fprintln(out, "Tailscale sign-in is required. Run: tsnet-bridge login\nEnrollment creates a separate node in the selected tailnet.")
		}
		return printStatus(out, s, false)
	default:
		return fmt.Errorf("unknown command %q; use tsnet-bridge help", cmd)
	}
}

const help = `tsnet-bridge: experimental application-scoped tailnet bridge

  tsnet-bridge setup        Create a profile (no network activity)
  tsnet-bridge              Start in background, or show current status
  tsnet-bridge run          Run in foreground; Ctrl+C stops forwarding
  tsnet-bridge login        Open interactive Tailscale sign-in
  tsnet-bridge status       Show status; --json for structured output
  tsnet-bridge doctor       Recheck identity, allowlist and TCP reachability
  tsnet-bridge settings     Show values to enter in RustDesk
  tsnet-bridge stop         Stop forwarding; retain saved login
  tsnet-bridge reconnect    Recreate local forwarding; retain saved login
  tsnet-bridge logout       Stop forwarding and log out; must be running
  tsnet-bridge version

Global --state-dir PATH goes before the command. No OS VPN, route or DNS changes.
RustDesk remote-control compatibility is experimental, not yet E2E verified.
Use a dedicated profile; keep a backup of existing RustDesk settings.`

func setup(dir string, args []string, in io.Reader, out io.Writer) error {
	f := flag.NewFlagSet("setup", flag.ContinueOnError)
	f.SetOutput(out)
	host := f.String("id-host", "", "tailnet ID-server hostname or IP")
	relay := f.String("relay-host", "", "relay host (defaults to ID host)")
	key := f.String("key", "", "RustDesk public key")
	mode := f.String("mode", "forward", "forward or socks")
	id := f.Int("id-port", 21116, "remote ID port")
	rp := f.Int("relay-port", 21117, "remote relay port")
	lp := f.Int("local-id-port", 32116, "local ID port")
	lr := f.Int("local-relay-port", 32117, "same local relay port on EVERY participating endpoint")
	sp := f.Int("socks-port", 1080, "authenticated SOCKS port")
	if e := f.Parse(args); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected setup arguments")
	}
	if _, e := os.Lstat(filepath.Join(dir, "profile.json")); e == nil {
		return errors.New("profile already exists; stop the tool and edit the profile deliberately (keep relay ports consistent)")
	}
	scan := bufio.NewScanner(in)
	ask := func(prompt string) (string, error) {
		fmt.Fprint(out, prompt)
		if !scan.Scan() {
			return "", errors.New("setup canceled; pass --id-host and --key for noninteractive setup")
		}
		return strings.TrimSpace(scan.Text()), scan.Err()
	}
	var e error
	if *host == "" {
		*host, e = ask("Tailnet ID-server name or IP: ")
		if e != nil {
			return e
		}
	}
	if *key == "" {
		*key, e = ask("RustDesk public key: ")
		if e != nil {
			return e
		}
	}
	c, e := config.New(*host, *key)
	if e != nil {
		return e
	}
	c.Mode = *mode
	c.IDPort = *id
	c.RelayPort = *rp
	c.LocalIDPort = *lp
	c.LocalRelayPort = *lr
	c.SOCKSPort = *sp
	if *relay != "" {
		c.RelayHost = *relay
	}
	if e = c.Validate(); e != nil {
		return e
	}
	lock, e := config.AcquireLock(dir)
	if e != nil {
		return e
	}
	defer lock.Close()
	if _, err := os.Lstat(filepath.Join(dir, "profile.json")); err == nil {
		return errors.New("profile was created by another process; reload it before changing anything")
	}
	if e = app.Preflight(c); e != nil {
		return e
	}
	if e = app.EnsureCredentials(dir); e != nil {
		return e
	}
	if e = config.Save(dir, c); e != nil {
		return e
	}
	fmt.Fprintln(out, "Profile saved. No tailnet enrollment has occurred.")
	if c.Mode == "forward" {
		fmt.Fprintln(out, "Experimental RustDesk profile: all participating endpoints need the same local relay address. Keep proxy blank and UDP enabled. Use remote-ID/r; mixed profiles are unverified.")
	} else {
		fmt.Fprintln(out, "SOCKS CONNECT-only mode cannot register a controlled RustDesk 1.4.9 endpoint with OSS server 1.1.16. Use only for separately validated client roles.")
	}
	return settings(dir, nil, out)
}
func settings(dir string, args []string, out io.Writer) error {
	secrets := len(args) == 1 && args[0] == "--show-secrets"
	if len(args) > 0 && !secrets {
		return errors.New("usage: settings [--show-secrets]")
	}
	c, e := config.Load(dir)
	if e != nil {
		return e
	}
	fmt.Fprintln(out, "Back up the existing RustDesk server and proxy settings before changing them.")
	if c.Mode == "forward" {
		fmt.Fprintf(out, "ID server: %s\nRelay server: %s\nProxy: blank\nUDP: enabled\nConnection: remote-ID/r\n", config.Loopback(c.LocalIDPort), config.Loopback(c.LocalRelayPort))
	} else {
		fmt.Fprintf(out, "ID server: %s\nRelay server: %s\nSOCKS5 proxy: %s\n", config.Address(c.IDHost, c.IDPort), config.Address(c.RelayHost, c.RelayPort), config.Loopback(c.SOCKSPort))
		if secrets {
			var v config.Credentials
			if e = config.ReadJSON(filepath.Join(dir, "credentials.json"), &v); e != nil {
				return e
			}
			fmt.Fprintf(out, "Username: %s\nPassword: %s\n", v.Username, v.Password)
		} else {
			fmt.Fprintln(out, "Credentials hidden. Use settings --show-secrets only in a private terminal.")
		}
	}
	fmt.Fprintln(out, "Key:", c.PublicKey, "\nRustDesk screen/control: unverified. Stopping this tool does not restore RustDesk settings.")
	return nil
}
func start(ctx context.Context, dir string, out io.Writer) error {
	if running(ctx, dir) {
		return nil
	}
	if _, e := config.Load(dir); e != nil {
		return fmt.Errorf("run setup first: %w", e)
	}
	if e := config.SecureDir(dir); e != nil {
		return e
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	logPath := filepath.Join(dir, "startup.log")
	if s, e := os.Lstat(logPath); e == nil && !s.Mode().IsRegular() {
		return errors.New("startup log must be a regular file")
	}
	log, e := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if e != nil {
		return e
	}
	defer log.Close()
	if e = config.Protect(logPath, false); e != nil {
		return e
	}
	c := exec.Command(exe, "--state-dir", dir, "run")
	c.Stdout = log
	c.Stderr = log
	c.Stdin = nil
	detach(c)
	if e = c.Start(); e != nil {
		return e
	}
	_ = c.Process.Release()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if running(ctx, dir) {
			return nil
		}
		if e = pause(ctx, 100*time.Millisecond); e != nil {
			return e
		}
	}
	return errors.New("background startup did not become reachable; run tsnet-bridge run for a direct error (state retained)")
}
func running(ctx context.Context, dir string) bool {
	var s app.Status
	return call(ctx, dir, "status", &s) == nil
}
func call(ctx context.Context, dir, command string, v any) error {
	c, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	return control.Call(c, dir, command, v)
}
func printStatus(out io.Writer, s app.Status, j bool) error {
	if j {
		return json.NewEncoder(out).Encode(s)
	}
	fmt.Fprintf(out, "%s: %s\nTailnet: %s\nRustDesk screen/control: %s\n", s.State, s.Reason, s.Backend, s.RustDesk)
	for _, a := range s.Listeners {
		fmt.Fprintln(out, "Loopback:", a)
	}
	return nil
}
func login(ctx context.Context, dir string, out io.Writer, noBrowser bool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var s app.Status
	if e := call(ctx, dir, "status", &s); e != nil {
		return e
	}
	if s.Backend == "Running" {
		return printStatus(out, s, false)
	}
	if e := call(ctx, dir, "login", nil); e != nil {
		return e
	}
	fmt.Fprintln(out, "Waiting for interactive sign-in. Ctrl+C cancels this wait; saved profile is retained.")
	deadline := time.Now().Add(5 * time.Minute)
	last := ""
	for time.Now().Before(deadline) {
		var auth struct {
			URL string `json:"url"`
		}
		if e := call(ctx, dir, "auth-url", &auth); e != nil {
			return e
		}
		if auth.URL != "" && auth.URL != last {
			if !validAuthURL(auth.URL) {
				return errors.New("unexpected login URL; refusing to open browser")
			}
			last = auth.URL
			fmt.Fprintln(out, "Private sign-in URL (do not share):", auth.URL)
			if !noBrowser {
				if e := openBrowser(ctx, auth.URL); e != nil {
					fmt.Fprintln(out, "Browser could not be opened. Open the private URL above manually.")
				}
			}
		}
		if e := call(ctx, dir, "status", &s); e != nil {
			return e
		}
		if s.Backend == "Running" || s.State == "approval-required" {
			return printStatus(out, s, false)
		}
		if e := pause(ctx, time.Second); e != nil {
			return e
		}
	}
	return errors.New("sign-in timed out; run login to retry, or stop to close the process")
}
func validAuthURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && u.Scheme == "https" && u.Host == "login.tailscale.com" && u.User == nil && strings.HasPrefix(u.Path, "/a/")
}
func openBrowser(ctx context.Context, u string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.CommandContext(ctx, "open", u)
	case "windows":
		c = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", u)
	default:
		c = exec.CommandContext(ctx, "xdg-open", u)
	}
	c.WaitDelay = 2 * time.Second
	return c.Run()
}
func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
