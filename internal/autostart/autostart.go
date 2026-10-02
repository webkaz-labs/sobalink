// Package autostart prepares optional per-user startup for an idle v2 node.
// Planning never writes files or invokes a service manager. Shares and forwards
// are never included in startup arguments; every runtime start stays explicit.
package autostart

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/config"
)

type Plan struct {
	OS       string     `json:"os"`
	Action   string     `json:"action"`
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	Content  string     `json:"content,omitempty"`
	Commands [][]string `json:"commands,omitempty"`
	Note     string     `json:"note"`
}

type Options struct{ OS, Home, ConfigDir, StateDir, Executable, UserID, Action string }

func Build(o Options) (Plan, error) {
	p := Plan{OS: o.OS, Action: o.Action}
	if o.Action != "enable" && o.Action != "disable" {
		return p, errors.New("autostart action must be enable or disable")
	}
	for _, s := range []string{o.Home, o.ConfigDir, o.StateDir, o.Executable} {
		if !filepath.IsAbs(s) || strings.ContainsAny(s, "\r\n\x00") {
			return p, errors.New("autostart paths must be absolute, single-line paths")
		}
	}
	digest := sha256.Sum256([]byte(filepath.Clean(o.StateDir)))
	p.Name = "tsnet-bridge-" + hex.EncodeToString(digest[:6])
	p.Note = "User-level registration only. Starts an idle version 2 node at a future sign-in; no rules or shares resume. Registration changes do not stop an already running node."
	switch o.OS {
	case "linux":
		p.Path = filepath.Join(o.ConfigDir, "systemd", "user", p.Name+".service")
		p.Content = "[Unit]\nDescription=tsnet-bridge idle node\n\n[Service]\nType=simple\nExecStart=" + systemdQuote(o.Executable) + " --state-dir " + systemdQuote(o.StateDir) + " run --idle\n\n[Install]\nWantedBy=default.target\n"
		p.Commands = [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", o.Action, p.Name + ".service"}}
		if o.Action == "disable" {
			p.Commands = [][]string{{"systemctl", "--user", "disable", p.Name + ".service"}}
		}
	case "darwin":
		p.Path = filepath.Join(o.Home, "Library", "LaunchAgents", p.Name+".plist")
		p.Content = `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n<plist version=\"1.0\"><dict><key>Label</key><string>" + xmlText(p.Name) + "</string><key>ProgramArguments</key><array><string>" + xmlText(o.Executable) + "</string><string>--state-dir</string><string>" + xmlText(o.StateDir) + "</string><string>run</string><string>--idle</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><false/></dict></plist>\n"
	case "windows":
		if o.UserID == "" || strings.ContainsAny(o.UserID, "\r\n\x00") {
			return p, errors.New("current Windows user SID is required")
		}
		p.Path = filepath.Join(o.StateDir, "autostart.task.xml")
		p.Content = `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + xmlText(o.UserID) + `</UserId></LogonTrigger></Triggers><Principals><Principal id="User"><UserId>` + xmlText(o.UserID) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals><Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><ExecutionTimeLimit>PT0S</ExecutionTimeLimit></Settings><Actions Context="User"><Exec><Command>` + xmlText(o.Executable) + `</Command><Arguments>` + xmlText("--state-dir "+windowsQuote(o.StateDir)+" run --idle") + `</Arguments></Exec></Actions></Task>` + "\n"
		p.Commands = [][]string{{"schtasks.exe", "/Create", "/TN", p.Name, "/XML", p.Path}}
		if o.Action == "disable" {
			p.Commands = [][]string{{"schtasks.exe", "/Delete", "/TN", p.Name, "/F"}}
		}
	default:
		return p, fmt.Errorf("user-level autostart is unsupported on %s", o.OS)
	}
	return p, nil
}
func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func systemdQuote(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "%", "%%", "$", "$$")
	return "\"" + r.Replace(s) + "\""
}

// windowsQuote applies CommandLineToArgvW-compatible quoting, not shell syntax.
func windowsQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range s {
		if r == '\\' {
			slashes++
			continue
		}
		if r == '"' {
			b.WriteString(strings.Repeat("\\", slashes*2+1))
			b.WriteRune(r)
		} else {
			b.WriteString(strings.Repeat("\\", slashes))
			b.WriteRune(r)
		}
		slashes = 0
	}
	b.WriteString(strings.Repeat("\\", slashes*2))
	b.WriteByte('"')
	return b.String()
}

type Runner func(context.Context, string, ...string) error

// Apply performs only the already displayed per-user registration plan.
// No elevation, login, enrollment or immediate service start is requested.
func Apply(ctx context.Context, p Plan, run Runner) error {
	if p.Action != "enable" && p.Action != "disable" {
		return errors.New("invalid autostart plan")
	}
	if p.OS != "linux" && p.OS != "darwin" && p.OS != "windows" {
		return errors.New("unsupported autostart plan")
	}
	if !filepath.IsAbs(p.Path) {
		return errors.New("autostart destination must be absolute")
	}
	if len(p.Commands) > 0 && run == nil {
		return errors.New("service-manager runner unavailable")
	}
	if p.Action == "enable" {
		if existing, err := os.Lstat(p.Path); err == nil {
			if !existing.Mode().IsRegular() {
				return errors.New("autostart destination is not a regular file")
			}
			content, err := os.ReadFile(p.Path)
			if err != nil {
				return err
			}
			if string(content) != p.Content {
				return errors.New("existing autostart file differs; remove it deliberately before replacement")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := config.AtomicWritePrivate(p.Path, []byte(p.Content)); err != nil {
			return err
		}
		for _, command := range p.Commands {
			if len(command) == 0 {
				return errors.New("empty autostart command")
			}
			if err := run(ctx, command[0], command[1:]...); err != nil {
				return fmt.Errorf("registration failed; planned file remains at %s: %w", p.Path, err)
			}
		}
		return nil
	}
	// Refuse to unregister a path whose content was replaced by another program.
	if info, err := os.Lstat(p.Path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("autostart destination is not a regular file")
		}
		content, e := os.ReadFile(p.Path)
		if e != nil {
			return e
		}
		if string(content) != p.Content {
			return errors.New("autostart file differs from this profile's plan; review it manually before removal")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, command := range p.Commands {
		if len(command) == 0 {
			return errors.New("empty autostart command")
		}
		if err := run(ctx, command[0], command[1:]...); err != nil {
			return fmt.Errorf("unregistration failed; file retained: %w", err)
		}
	}
	if info, err := os.Lstat(p.Path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("autostart destination is not a regular file")
		}
		if err = os.Remove(p.Path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if p.OS == "linux" {
		return run(ctx, "systemctl", "--user", "daemon-reload")
	}
	return nil
}
