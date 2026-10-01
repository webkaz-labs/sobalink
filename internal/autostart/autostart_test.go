package autostart

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testOptions(t *testing.T, goos string) Options {
	t.Helper()
	d := t.TempDir()
	return Options{OS: goos, Home: d, ConfigDir: filepath.Join(d, "config"), StateDir: filepath.Join(d, "state"), Executable: filepath.Join(d, "bridge executable"), UserID: "S-1-5-21-example", Action: "enable"}
}
func TestPlansOnlyIdleNodeAndUserLevel(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			o := testOptions(t, goos)
			p, e := Build(o)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = os.Stat(p.Path); !os.IsNotExist(e) {
				t.Fatal("planning wrote file")
			}
			if !strings.Contains(p.Content, "run") || !strings.Contains(p.Content, "--idle") || strings.Contains(p.Content, "stop-shares") || strings.Contains(p.Content, "start --") {
				t.Fatal(p.Content)
			}
			if strings.Contains(p.Content, "Administrator") || strings.Contains(p.Content, "HighestAvailable") {
				t.Fatal("privileged registration")
			}
			for _, c := range p.Commands {
				for _, arg := range c {
					if arg == "--now" || arg == "/Run" || arg == "sudo" {
						t.Fatal("immediate/elevated start", c)
					}
				}
			}
			if goos != "linux" {
				decoder := xml.NewDecoder(strings.NewReader(p.Content))
				for {
					_, e := decoder.Token()
					if e != nil {
						if e.Error() != "EOF" {
							t.Fatal(e)
						}
						break
					}
				}
			}
		})
	}
}
func TestApplyUsesMockServiceManager(t *testing.T) {
	o := testOptions(t, "linux")
	p, _ := Build(o)
	var commands []string
	run := func(_ context.Context, name string, args ...string) error {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil
	}
	if e := Apply(context.Background(), p, run); e != nil {
		t.Fatal(e)
	}
	if len(commands) != 2 || !strings.Contains(commands[1], "--user enable") {
		t.Fatal(commands)
	}
	b, e := os.ReadFile(p.Path)
	if e != nil || string(b) != p.Content {
		t.Fatal(e)
	}
	o.Action = "disable"
	p, _ = Build(o)
	if e = Apply(context.Background(), p, run); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(p.Path); !os.IsNotExist(e) {
		t.Fatal("disable did not remove planned file")
	}
}
func TestApplyFailureRetainsPlanAndRefusesDifferentFile(t *testing.T) {
	o := testOptions(t, "linux")
	p, _ := Build(o)
	sentinel := errors.New("service manager unavailable")
	if e := Apply(context.Background(), p, func(context.Context, string, ...string) error { return sentinel }); !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	if _, e := os.Stat(p.Path); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(p.Path, []byte("unrelated"), 0600)
	called := false
	if e := Apply(context.Background(), p, func(context.Context, string, ...string) error { called = true; return nil }); e == nil || called {
		t.Fatal("overwrote unrelated content")
	}
}
func TestEscaping(t *testing.T) {
	if got := systemdQuote(`/a/$b%"c`); got != `"/a/$$b%%\"c"` {
		t.Fatal(got)
	}
	if got := windowsQuote(`C:\space dir\`); got != `"C:\space dir\\"` {
		t.Fatal(got)
	}
	if got := xmlText(`<x a="&">`); strings.Contains(got, "<") {
		t.Fatal(got)
	}
}
func TestBadPathsAndUnsupportedOS(t *testing.T) {
	o := testOptions(t, "plan9")
	if _, e := Build(o); e == nil {
		t.Fatal("unknown OS accepted")
	}
	o.OS = "linux"
	o.Executable = "relative"
	if _, e := Build(o); e == nil {
		t.Fatal("relative executable")
	}
	o.Executable = "/x\nExecStart=bad"
	if _, e := Build(o); e == nil {
		t.Fatal("newline accepted")
	}
}

func TestDisableRefusesUnrelatedReplacement(t *testing.T) {
	o := testOptions(t, "linux")
	p, _ := Build(o)
	if e := os.MkdirAll(filepath.Dir(p.Path), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p.Path, []byte("unrelated service"), 0600); e != nil {
		t.Fatal(e)
	}
	o.Action = "disable"
	p, _ = Build(o)
	called := false
	if e := Apply(context.Background(), p, func(context.Context, string, ...string) error { called = true; return nil }); e == nil || called {
		t.Fatal("unregistered unrelated content")
	}
	if _, e := os.Stat(p.Path); e != nil {
		t.Fatal("deleted unrelated content")
	}
}
