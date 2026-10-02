package main

import (
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
	if e := run(ctx, os.Args[1:], newProcessInput(ctx, os.Stdin), os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, "tsnet-bridge:", e)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	cleaned, display, translate, err := prepareLocale(args, out)
	if err != nil {
		return err
	}
	err = runCommand(ctx, cleaned, in, display)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return translate(err)
}
func runCommand(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
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
	global.Usage = func() { fmt.Fprintln(out, help) }
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
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return commandHelp(cmd, out)
	}
	switch cmd {
	case "version", "--version":
		fmt.Fprintln(out, "tsnet-bridge", version)
		return nil
	case "help":
		if len(args) > 1 {
			return errors.New("usage: help [COMMAND|all]")
		}
		if len(args) == 1 {
			return commandHelp(args[0], out)
		}
		fmt.Fprintln(out, help)
		return nil
	case "init":
		return initRules(dir, args, out)
	case "peers":
		return printPeers(ctx, dir, args, out)
	case "connect":
		return configureRule(ctx, dir, "forward", args, in, out)
	case "share":
		return configureRule(ctx, dir, "share", args, in, out)
	case "rules":
		return listRules(ctx, dir, args, out, false)
	case "shares":
		return listRules(ctx, dir, args, out, true)
	case "group":
		return groupCommand(ctx, dir, args, in, out)
	case "stop-shares":
		return namedAction(ctx, dir, "stop-shares", args, in, out)
	case "wait-ready":
		return waitCommand(ctx, dir, args, out)
	case "task":
		return taskCommand(ctx, dir, args, in, out)
	case "migrate":
		return migrateCommand(dir, args, in, out)
	case "export":
		return exportCommand(dir, args, in, out)
	case "import":
		return importCommand(ctx, dir, args, in, out)
	case "autostart":
		return autostartCommand(ctx, dir, args, in, out)
	case "setup":
		return setup(dir, args, in, out)
	case "status", "doctor", "stop", "reconnect", "logout":
		if cmd == "stop" && len(args) > 0 && !(len(args) == 1 && args[0] == "--json") {
			return namedAction(ctx, dir, "stop", args, in, out)
		}
		if len(args) > 1 || (len(args) == 1 && args[0] != "--json") {
			return errors.New("only --json is accepted")
		}
		var s app.Status
		e = call(ctx, dir, cmd, &s)
		if e != nil && cmd == "stop" {
			if control.Unavailable(e) {
				if len(args) == 1 {
					return printStatus(out, app.Status{State: "stopped", Reason: "No reachable process", RustDesk: "unverified"}, true, dir)
				}
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
		return printStatus(out, s, len(args) > 0, dir)
	case "settings":
		return settings(dir, args, out)
	case "login":
		options, e := parseLoginOptions(args, out)
		if e != nil {
			return e
		}
		if options.QR {
			if e = checkPrivateTerminal(out); e != nil {
				return e
			}
		}
		if e = start(ctx, dir, out); e != nil {
			return e
		}
		return loginWithOptions(ctx, dir, out, options)
	case "run":
		idleOnly := len(args) == 1 && args[0] == "--idle"
		if len(args) != 0 && !idleOnly {
			return errors.New("usage: run [--idle]; --idle requires a version 2 profile")
		}
		c, e := config.Load(dir)
		if e != nil {
			return fmt.Errorf("profile unavailable: run init (or setup for legacy RustDesk) first: %w", e)
		}
		if idleOnly && c.Version != 2 {
			return errors.New("idle startup requires a version 2 profile; refusing legacy automatic forwarding")
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
		jsonOutput := len(args) == 1 && args[0] == "--json"
		if len(args) != 0 && !jsonOutput {
			return namedAction(ctx, dir, "start", args, in, out)
		}
		startupOut := out
		if jsonOutput {
			startupOut = io.Discard
		}
		if _, e = config.Load(dir); os.IsNotExist(e) {
			if e = initRules(dir, nil, startupOut); e != nil {
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
		if s.State == "needs-login" && !jsonOutput {
			fmt.Fprintf(out, "Tailscale sign-in is required. Run: %s login\nEnrollment creates a separate node in the selected tailnet.\n", commandPrefix(dir))
		}
		return printStatus(out, s, jsonOutput, dir)
	default:
		return fmt.Errorf("unknown command %q; use tsnet-bridge help", cmd)
	}
}

const helpAll = `tsnet-bridge: experimental application-scoped tailnet bridge

Basic named-rule workflow (version 2):
  init                     Save an idle profile; no networking or enrollment
  login                    Browser sign-in; --qr for phone, --link for private URL
  peers                    Choose from current tailnet peers; --json available
  connect                  Choose shared service -> preview -> start
  connect --manual         Advanced peer, purpose and port configuration
  share                    Choose peers -> local service -> lifetime -> confirm
  connect/share --save-only Save disabled; never connects or shares
  rules                    List saved rules; --json available
  start NAME...            Explicitly start selected saved rules
  stop NAME...             Stop selected manually owned rules
  group save NAME RULE...  Save a group; add --confirm before names for scripts
  group start/stop NAME    Operate one group; --ttl is mandatory for shares
  shares                   Show sharing state, targets and expiry
  stop-shares              Immediately stop all shares, including task shares
  wait-ready NAME...       Wait for transport readiness, with timeout/cancel
  task --rules N -- CMD    Lease named rules to a directly executed command
  settings                 Show connection endpoints and verification cautions
  export                   Preview a credential-free disabled profile
  import FILE              Preview an import; --confirm applies it
  migrate                  Preview a v1 forward migration; keeps original backup
  autostart                Preview optional user-level idle-node startup

Node and existing RustDesk workflow:
  setup                    Create legacy v1 RustDesk profile (no networking)
  start                    Start background node; v2 starts NO saved rules
  run                      Run in foreground; Ctrl+C closes all connections
  status / doctor          Current state and diagnostics; --json available
  stop                     Stop node and connections; retain saved login
  reconnect                Recheck identity and connections; no expired restart
  logout                   Stop connections and log out; node must be running
  version

Flags precede positional names. Global --state-dir PATH precedes the command.
Language is automatic; --lang ja|en|auto before COMMAND overrides it.
Share start requires --ttl 1s..24h and confirmation (--confirm for scripts).
Privileged/conflicting local ports require an explicitly chosen alternative.
No OS VPN, route or DNS changes. TLS/SSH checks are never disabled.
Readiness is not application verification or remote-job completion.
RustDesk remains experimental and not yet end-to-end verified.`

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
	ask := newPrompts(in, out).ask
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
	if c.Version == 2 {
		return ruleSettings(c, out, dir)
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
		return fmt.Errorf("run init (or setup for legacy RustDesk) first: %w", e)
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
	return fmt.Errorf("background startup did not become reachable; run %s run for a direct error (state retained)", commandPrefix(dir))
}
func running(ctx context.Context, dir string) bool {
	var s app.Status
	return call(ctx, dir, "status", &s) == nil
}
func call(ctx context.Context, dir, command string, v any) error {
	c, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	return request(c, dir, command, v)
}
func printStatus(out io.Writer, s app.Status, j bool, profileDirs ...string) error {
	if j {
		return json.NewEncoder(out).Encode(s)
	}
	if len(profileDirs) > 0 {
		switch s.Reason {
		case "No reachable process; run tsnet-bridge to start":
			s.Reason = fmt.Sprintf("No reachable process; run %s start to start", commandPrefix(profileDirs[0]))
		case "Run tsnet-bridge login to sign in":
			s.Reason = fmt.Sprintf("Run %s login to sign in", commandPrefix(profileDirs[0]))
		}
	}
	fmt.Fprintf(out, "%s: %s\nTailnet: %s\n", s.State, s.Reason, s.Backend)
	if s.Discovery == "unavailable" {
		fmt.Fprintln(out, "Service discovery is unavailable for this node. Sharing may still work with manual configuration; check the discovery listener and tailnet permissions.")
	}
	if s.Mode != "rules" {
		fmt.Fprintln(out, "RustDesk screen/control:", s.RustDesk)
	}
	for _, r := range s.Rules {
		fmt.Fprintf(out, "%s: %s [%s/%s]\n", r.Name, r.State, r.Direction, r.Network)
		if r.ListenAddress != "" {
			fmt.Fprintf(out, "  Connect: %s -> %s\n", r.ListenAddress, r.Target)
		} else {
			fmt.Fprintln(out, "  Target:", r.Target)
		}
		if r.State != "ready" || r.ReasonCode == "tcp-reachable" {
			fmt.Fprintf(out, "  %s (%s)\n", r.Reason, r.ReasonCode)
		}
		for _, peer := range r.AllowedPeers {
			fmt.Fprintln(out, "  Allowed peer:", peer.Host)
		}
		if r.Owner != "" {
			fmt.Fprintln(out, "  Task:", r.Owner)
		}
		if !r.ExpiresAt.IsZero() {
			remaining := time.Until(r.ExpiresAt).Round(time.Second)
			if remaining < 0 {
				remaining = 0
			}
			fmt.Fprintf(out, "  Expires: %s (%s remaining)\n", r.ExpiresAt.Format(time.RFC3339), remaining)
		}
	}
	for _, a := range s.Listeners {
		if len(s.Rules) > 0 {
			break
		}
		label := "Loopback:"
		if s.Mode == "rules" {
			label = "Listener:"
		}
		fmt.Fprintln(out, label, a)
	}
	return nil
}
func validAuthURL(s string) bool {
	u, e := url.Parse(s)
	if e != nil || len(s) > 2048 || u.Scheme != "https" || u.Host != "login.tailscale.com" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || !strings.HasPrefix(u.Path, "/a/") {
		return false
	}
	token := strings.TrimPrefix(u.Path, "/a/")
	if token == "" {
		return false
	}
	for _, c := range token {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
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
