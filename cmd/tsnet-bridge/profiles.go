package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/webkaz-labs/tsnet-bridge/internal/autostart"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
)

func ruleSettings(c config.Config, out io.Writer, dir string) error {
	fmt.Fprintf(out, "Saved configuration. Check running state with: %s status\n", commandPrefix(dir))
	for _, r := range c.Rules {
		fmt.Fprintf(out, "%s (%s, saved configuration):\n", r.Name, r.Purpose)
		if r.Direction == "share" {
			if r.Discoverable {
				fmt.Fprintln(out, "  Service discovery: enabled while sharing (allowed peers only).")
			} else {
				fmt.Fprintln(out, "  Service discovery: disabled.")
			}
			fmt.Fprintf(out, "  Remote peers connect to this node's tailnet address, port %d (%s).\n  Local target: %s\n", r.ListenPort, r.Network, config.Address(r.TargetHost, r.TargetPort))
			for _, p := range r.AllowedPeers {
				fmt.Fprintf(out, "  Allowed peer: %s (%s)\n", p.Host, p.ID)
			}
			continue
		}
		fmt.Fprintf(out, "  Local application endpoint: %s (%s)\n  Actual remote destination: %s, pinned peer %s\n", config.Loopback(r.ListenPort), r.Network, config.Address(r.TargetHost, r.TargetPort), r.PeerID)
		if r.Purpose == "ssh" {
			fmt.Fprintf(out, "  SSH: ssh -o HostKeyAlias=%s -p %d <user>@127.0.0.1\n", r.TargetHost, r.ListenPort)
		}
		if r.Purpose == "web" {
			fmt.Fprintf(out, "  HTTP candidate: http://127.0.0.1:%d/\n  For HTTPS, preserve the original TLS name, origin and certificate validation.\n", r.ListenPort)
		}
	}
	if c.PublicKey != "" {
		fmt.Fprintln(out, "Migrated RustDesk profile: all four fixed rules and the original relay ports remain required. Keep the same local relay port at participating endpoints.\nPublic key:", c.PublicKey)
	}
	fmt.Fprintln(out, "These are service endpoints, not SOCKS proxy addresses. Application compatibility is unverified. Never disable TLS certificate or SSH host-key verification.")
	return nil
}

func migrateCommand(dir string, args []string, in io.Reader, out io.Writer) error {
	f := flags("migrate", out)
	confirm := f.Bool("confirm", false, "apply the displayed migration and retain original backup")
	idPeer := f.String("id-peer-id", "", "pinned ID of the current ID-server peer (from peers)")
	relayPeer := f.String("relay-peer-id", "", "pinned relay peer ID (defaults to ID peer for the same host)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: migrate [--confirm --id-peer-id ID --relay-peer-id ID]")
	}
	c, err := config.Load(dir)
	if err != nil {
		return err
	}
	if c.Version != 1 {
		return errors.New("profile is already version 2; nothing to migrate")
	}
	previewID := *idPeer
	if previewID == "" {
		previewID = "choose-current-peer-ID"
	}
	previewRelay := *relayPeer
	if previewRelay == "" && !strings.EqualFold(strings.TrimSuffix(c.IDHost, "."), strings.TrimSuffix(c.RelayHost, ".")) {
		previewRelay = "choose-current-relay-peer-ID"
	}
	next, err := config.MigrateV1(c, previewID, previewRelay)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Migration preview: four disabled fixed RustDesk forwarding rules and the rustdesk group. No rule will start.\nThe original profile will be retained as profile.v1.backup.json. Keep relay ports consistent at every participating endpoint.")
	if err = json.NewEncoder(out).Encode(next); err != nil {
		return err
	}
	if !*confirm {
		fmt.Fprintln(out, "To apply: stop the node, then use migrate --confirm --id-peer-id ID [--relay-peer-id ID]. Use peers while signed in to identify the current peers. No files changed.")
		return nil
	}
	if *idPeer == "" {
		return errors.New("migration requires an explicit pinned --id-peer-id from the current peer list")
	}
	next, err = config.MigrateV1(c, *idPeer, *relayPeer)
	if err != nil {
		return err
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		return fmt.Errorf("stop the node before migrating: %w", err)
	}
	defer lock.Close()
	latest, err := config.Load(dir)
	if err != nil {
		return err
	}
	left, _ := json.Marshal(c)
	right, _ := json.Marshal(latest)
	if string(left) != string(right) {
		return errors.New("profile changed after preview; preview again")
	}
	backup, err := config.SaveMigration(dir, next)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Migration saved, all rules disabled. Original retained:", backup)
	return nil
}

const exportWarning = "Privacy review: exported profiles include node names, peer identities, service addresses and ports. Credentials, login state and task owners are excluded. All rules are disabled. Review and redact identifying details before sharing; export does not upload anything."

func exportCommand(dir string, args []string, in io.Reader, out io.Writer) error {
	f := flags("export", out)
	path := f.String("output", "", "local export destination")
	confirm := f.Bool("confirm", false, "write the reviewed export to --output")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: export [--output FILE --confirm]")
	}
	c, err := config.Load(dir)
	if err != nil {
		return err
	}
	if c.Version != 2 {
		return errors.New("safe named-rule export requires a version 2 profile; v1 credentials and login state are never exported")
	}
	c = c.Disabled()
	fmt.Fprintln(out, exportWarning)
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(out, string(b))
	if !*confirm {
		fmt.Fprintln(out, "Preview only. Use --output FILE --confirm to write a private local copy.")
		return nil
	}
	if *path == "" {
		return errors.New("--confirm requires --output FILE")
	}
	absolute, err := filepath.Abs(*path)
	if err != nil {
		return err
	}
	profile, err := filepath.Abs(filepath.Join(dir, "profile.json"))
	if err != nil {
		return err
	}
	if absolute == profile {
		return errors.New("export must not replace the active profile")
	}
	// A chosen local output is written privately and never uploaded.
	if err = config.AtomicWritePrivate(absolute, append(b, '\n')); err != nil {
		return err
	}
	fmt.Fprintln(out, "Private disabled export written:", absolute)
	return nil
}
func importCommand(ctx context.Context, dir string, args []string, in io.Reader, out io.Writer) error {
	f := flags("import", out)
	confirm := f.Bool("confirm", false, "apply reviewed disabled profile")
	replace := f.Bool("replace", false, "replace the existing idle profile after preview")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 1 {
		return errors.New("usage: import [--replace] [--confirm] FILE")
	}
	var incoming config.Config
	if err := config.ReadJSON(f.Arg(0), &incoming); err != nil {
		return err
	}
	if incoming.Version != 2 {
		return errors.New("import requires version 2; use migration for a legacy profile")
	}
	if err := incoming.Validate(); err != nil {
		return err
	}
	incoming = incoming.Disabled()
	existing, loadErr := config.Load(dir)
	if loadErr != nil && !os.IsNotExist(loadErr) {
		return loadErr
	}
	if loadErr == nil {
		if !*replace {
			return errors.New("profile exists; use --replace to preview replacing its saved rules")
		}
		if existing.Version != 2 {
			return errors.New("legacy profile must be migrated or imported into a separate state directory")
		}
		incoming.Hostname = existing.Hostname
	} else {
		fresh, err := config.NewRules()
		if err != nil {
			return err
		}
		incoming.Hostname = fresh.Hostname
	}
	fmt.Fprintln(out, "Import preview. All rules are disabled; existing node identity is retained when replacing. No credentials or login state are imported.\n"+exportWarning)
	if err := json.NewEncoder(out).Encode(incoming); err != nil {
		return err
	}
	if !*confirm {
		fmt.Fprintln(out, "No files changed. Add --confirm to apply the reviewed import.")
		return nil
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		return fmt.Errorf("stop the node before importing: %w", err)
	}
	defer lock.Close()
	latest, e := config.Load(dir)
	if os.IsNotExist(loadErr) {
		if !os.IsNotExist(e) {
			return errors.New("destination changed after preview; preview again")
		}
	} else {
		if e != nil {
			return e
		}
		a, _ := json.Marshal(existing)
		b, _ := json.Marshal(latest)
		if string(a) != string(b) {
			return errors.New("destination changed after preview; preview again")
		}
	}
	if err = config.Save(dir, incoming); err != nil {
		return err
	}
	fmt.Fprintln(out, "Import saved. All rules are disabled; explicitly start only the rules you need.")
	return nil
}

var autostartRun = func(ctx context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func autostartCommand(ctx context.Context, dir string, args []string, in io.Reader, out io.Writer) error {
	action := "enable"
	if len(args) > 0 && (args[0] == "enable" || args[0] == "disable") {
		action = args[0]
		args = args[1:]
	}
	f := flags("autostart", out)
	apply := f.Bool("apply", false, "apply the displayed per-user registration plan")
	j := f.Bool("json", false, "show structured plan")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: autostart [enable|disable] [--json] [--apply]")
	}
	c, err := config.Load(dir)
	if err != nil {
		return err
	}
	if c.Version != 2 {
		return errors.New("autostart supports idle version 2 nodes only; legacy automatic forwarding is not registered")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	state, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	uid := ""
	if runtime.GOOS == "windows" {
		u, e := user.Current()
		if e != nil {
			return e
		}
		uid = u.Uid
	}
	plan, err := autostart.Build(autostart.Options{OS: runtime.GOOS, Home: home, ConfigDir: cfg, StateDir: state, Executable: exe, UserID: uid, Action: action})
	if err != nil {
		return err
	}
	if *j {
		if err = json.NewEncoder(out).Encode(plan); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out, "Autostart %s preview\nFile: %s\n%s\n", action, plan.Path, plan.Note)
		if plan.Content != "" && action == "enable" {
			fmt.Fprintln(out, plan.Content)
		}
		for _, cmd := range plan.Commands {
			fmt.Fprintf(out, "Command arguments: %q\n", cmd)
		}
	}
	if !*apply {
		if !*j {
			fmt.Fprintln(out, "No registration changed. Add --apply only after reviewing this plan.")
		}
		return nil
	}
	if err = autostart.Apply(ctx, plan, autostartRun); err != nil {
		return err
	}
	if !*j {
		fmt.Fprintln(out, "User-level autostart registration updated. No rule or share was started.")
	}
	return nil
}
