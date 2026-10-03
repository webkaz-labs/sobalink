package autostart

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestSobaPlansSavedAndOffline(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, mode := range []string{"saved", "offline"} {
			t.Run(goos+"/"+mode, func(t *testing.T) {
				o := SobaOptions{Options: testOptions(t, goos), StartupMode: mode}
				// Exercise every escaping layer with generic paths.
				o.Executable = filepath.Join(o.Home, `soba & "$binary%`)
				o.StateDir = filepath.Join(o.Home, `state & "$profile%`)
				p, err := BuildSoba(o)
				if err != nil {
					t.Fatal(err)
				}
				// Deliberately malformed path characters exercise escaping even on
				// hosts where that path cannot exist. Inspect the real fixture root
				// rather than confusing ERROR_INVALID_NAME with a created file.
				if entries, err := os.ReadDir(o.Home); err != nil || len(entries) != 0 {
					t.Fatalf("planning changed the fixture directory: %v (%d entries)", err, len(entries))
				}
				if !strings.HasPrefix(p.Name, "sobalink-") || strings.Contains(p.Content, "tsnet-bridge") || strings.Contains(p.Content, "--idle") {
					t.Fatalf("not an independent sobalink registration: %+v", p)
				}
				args := []string{"--state-dir", o.StateDir, "run"}
				if mode == "offline" {
					args = append(args, "--offline")
					for _, text := range []string{"idle local application", "saved network is not started", "no shares, forwards or file autosave reception are active"} {
						if !strings.Contains(p.Note, text) {
							t.Fatalf("offline scope not disclosed: %q", p.Note)
						}
					}
				} else {
					for _, text := range []string{"Starts the saved network", "restores existing peer and file autosave approvals", "No shares, forwards or outgoing transfers start automatically"} {
						if !strings.Contains(p.Note, text) {
							t.Fatalf("saved scope not disclosed: %q", p.Note)
						}
					}
				}
				switch goos {
				case "linux":
					want := "ExecStart=" + systemdQuote(o.Executable) + " --state-dir " + systemdQuote(o.StateDir) + " run"
					if mode == "offline" {
						want += " --offline"
					}
					if !strings.Contains(p.Content, want+"\n") || p.Path != filepath.Join(o.ConfigDir, "systemd", "user", p.Name+".service") {
						t.Fatalf("unexpected systemd plan: %+v", p)
					}
				case "darwin":
					var plist struct {
						Args []string `xml:"dict>array>string"`
					}
					if err := xml.Unmarshal([]byte(p.Content), &plist); err != nil {
						t.Fatal(err)
					}
					if want := append([]string{o.Executable}, args...); !reflect.DeepEqual(plist.Args, want) {
						t.Fatalf("launch arguments = %q, want %q", plist.Args, want)
					}
					if p.Path != filepath.Join(o.Home, "Library", "LaunchAgents", p.Name+".plist") || len(p.Commands) != 0 {
						t.Fatalf("unexpected LaunchAgent plan: %+v", p)
					}
				case "windows":
					var task struct {
						Command   string `xml:"Actions>Exec>Command"`
						Arguments string `xml:"Actions>Exec>Arguments"`
						TriggerID string `xml:"Triggers>LogonTrigger>UserId"`
						UserID    string `xml:"Principals>Principal>UserId"`
						LogonType string `xml:"Principals>Principal>LogonType"`
						RunLevel  string `xml:"Principals>Principal>RunLevel"`
					}
					if err := xml.Unmarshal([]byte(p.Content), &task); err != nil {
						t.Fatal(err)
					}
					want := "--state-dir " + windowsQuote(o.StateDir) + " run"
					if mode == "offline" {
						want += " --offline"
					}
					if task.Command != o.Executable || task.Arguments != want || task.TriggerID != o.UserID || task.UserID != o.UserID || task.LogonType != "InteractiveToken" || task.RunLevel != "LeastPrivilege" {
						t.Fatalf("unexpected task fields: %+v", task)
					}
					if p.Path != filepath.Join(o.StateDir, "sobalink-autostart.task.xml") {
						t.Fatalf("unexpected task path: %q", p.Path)
					}
				}
				for _, command := range p.Commands {
					for _, arg := range command {
						if arg == "--now" || arg == "/Run" || arg == "sudo" || arg == "bootstrap" || arg == "load" {
							t.Fatalf("plan starts immediately or elevates: %q", command)
						}
					}
				}
			})
		}
	}
}

func TestSobaDefaultModeAndLegacyPlanStayDistinct(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		o := SobaOptions{Options: testOptions(t, goos)}
		defaultPlan, err := BuildSoba(o)
		if err != nil {
			t.Fatal(err)
		}
		o.StartupMode = "saved"
		savedPlan, err := BuildSoba(o)
		if err != nil || !reflect.DeepEqual(defaultPlan, savedPlan) {
			t.Fatalf("default differs from saved on %s: %v", goos, err)
		}
		legacy, err := Build(o.Options)
		if err != nil || !strings.HasPrefix(legacy.Name, "tsnet-bridge-") || !strings.Contains(legacy.Content, "--idle") || strings.Contains(legacy.Content, "--offline") || legacy.Path == savedPlan.Path {
			t.Fatalf("legacy plan changed on %s: %+v, %v", goos, legacy, err)
		}
		o.StartupMode = "automatic"
		if _, err := BuildSoba(o); err == nil {
			t.Fatal("unknown startup mode accepted")
		}
	}
}

func TestReviewDigestBindsEveryPlanField(t *testing.T) {
	o := SobaOptions{Options: testOptions(t, "linux"), StartupMode: "offline"}
	p, err := BuildSoba(o)
	if err != nil {
		t.Fatal(err)
	}
	base := ReviewDigest(p)
	if len(base) != 64 || base != ReviewDigest(p) {
		t.Fatalf("digest not deterministic: %q", base)
	}
	mutations := map[string]func(*Plan){
		"os":      func(p *Plan) { p.OS = "darwin" },
		"action":  func(p *Plan) { p.Action = "disable" },
		"name":    func(p *Plan) { p.Name += "-other" },
		"path":    func(p *Plan) { p.Path += ".other" },
		"content": func(p *Plan) { p.Content += "\n" },
		"command": func(p *Plan) { p.Commands = [][]string{{"systemctl", "--user", "disable", p.Name + ".service"}} },
		"note":    func(p *Plan) { p.Note += " other" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := p
			mutate(&changed)
			if ReviewDigest(changed) == base {
				t.Fatal("changed plan retained approval digest")
			}
		})
	}
	for name, mutate := range map[string]func(*SobaOptions){
		"executable": func(o *SobaOptions) { o.Executable += "-other" },
		"state":      func(o *SobaOptions) { o.StateDir += "-other" },
		"mode":       func(o *SobaOptions) { o.StartupMode = "saved" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := o
			mutate(&changed)
			plan, err := BuildSoba(changed)
			if err != nil || ReviewDigest(plan) == base {
				t.Fatalf("changed input retained approval digest: %v", err)
			}
		})
	}
}

func TestSobaApplyUsesExactlyReviewedCommands(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			o := SobaOptions{Options: testOptions(t, goos), StartupMode: "offline"}
			for _, action := range []string{"enable", "disable"} {
				o.Action = action
				p, err := BuildSoba(o)
				if err != nil {
					t.Fatal(err)
				}
				var commands [][]string
				run := func(_ context.Context, name string, args ...string) error {
					command := append([]string{name}, args...)
					commands = append(commands, command)
					if action == "disable" && isDaemonReload(command) {
						if _, err := os.Stat(p.Path); !os.IsNotExist(err) {
							t.Fatalf("daemon reload preceded removal: %v", err)
						}
					}
					return nil
				}
				if err := Apply(context.Background(), p, run); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(commands, p.Commands) {
					t.Fatalf("ran %q; reviewed %q", commands, p.Commands)
				}
				if action == "enable" {
					info, err := os.Stat(p.Path)
					if err != nil {
						t.Fatal(err)
					}
					if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
						t.Fatalf("registration is not private: %v", info.Mode())
					}
				} else if _, err := os.Stat(p.Path); !os.IsNotExist(err) {
					t.Fatalf("registration remains after disable: %v", err)
				}
			}
		})
	}
}

func TestDisableRetainsFileReplacedDuringUnregister(t *testing.T) {
	o := SobaOptions{Options: testOptions(t, "linux"), StartupMode: "offline"}
	p, err := BuildSoba(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), p, func(context.Context, string, ...string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	o.Action = "disable"
	p, err = BuildSoba(o)
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	err = Apply(context.Background(), p, func(_ context.Context, name string, args ...string) error {
		called++
		return os.WriteFile(p.Path, []byte("another application's registration"), 0600)
	})
	if err == nil || called != 1 {
		t.Fatalf("replacement not rejected before reload: calls=%d, error=%v", called, err)
	}
	content, err := os.ReadFile(p.Path)
	if err != nil || string(content) != "another application's registration" {
		t.Fatalf("replacement removed or changed: %q, %v", content, err)
	}
}

func TestApplyRejectsMalformedCommandsBeforeWriting(t *testing.T) {
	p, err := BuildSoba(SobaOptions{Options: testOptions(t, "linux")})
	if err != nil {
		t.Fatal(err)
	}
	p.Commands = append(p.Commands, nil)
	if err := Apply(context.Background(), p, func(context.Context, string, ...string) error {
		t.Fatal("ran command before validating complete plan")
		return nil
	}); err == nil {
		t.Fatal("malformed command accepted")
	}
	if _, err := os.Stat(p.Path); !os.IsNotExist(err) {
		t.Fatalf("invalid plan wrote file: %v", err)
	}
}

func TestSobaCancelledApplyDoesNotRegister(t *testing.T) {
	p, err := BuildSoba(SobaOptions{Options: testOptions(t, "darwin")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Apply(ctx, p, nil); err != context.Canceled {
		t.Fatal("cancelled registration was not stopped", err)
	}
	if _, err := os.Stat(p.Path); !os.IsNotExist(err) {
		t.Fatal("cancelled registration wrote a file", err)
	}
}
