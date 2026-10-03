package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/autostart"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestLifecycleHelpAndLogoutRoutingAreOfflineAndStable(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		for _, args := range [][]string{{"run", "--help"}, {"start", "--help"}, {"autostart", "--help"}, {"logout", "--help"}} {
			var out bytes.Buffer
			dir := filepath.Join(t.TempDir(), "absent")
			client := func(context.Context, string, string, any) error { t.Fatal("help called IPC"); return nil }
			if err := runWith(context.Background(), append([]string{"--state-dir", dir, "--locale", locale}, args...), &out, strings.NewReader(""), client); err != nil || out.Len() == 0 {
				t.Fatal(err, out.String())
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("help wrote state", err)
			}
		}
		var out bytes.Buffer
		client := func(_ context.Context, _, raw string, out any) error {
			var command webui.Command
			if err := json.Unmarshal([]byte(raw), &command); err != nil || command.Name != "network.logout" || string(command.Payload) != "{}" {
				t.Fatal("wrong logout envelope", raw, err)
			}
			*(out.(*json.RawMessage)) = json.RawMessage(`{"state":"logged-out","logoutConfirmed":true,"localTrafficStopped":true,"applicationState":"stopping"}`)
			return nil
		}
		if err := runWith(context.Background(), []string{"--state-dir", t.TempDir(), "--locale", locale, "logout"}, &out, strings.NewReader(""), client); err != nil || !strings.Contains(out.String(), `"logoutConfirmed": true`) {
			t.Fatal(err, out.String())
		}
	}
}

func TestBackgroundStartupReadinessAndOfflineArguments(t *testing.T) {
	for _, offline := range []bool{true, false} {
		dir := filepath.Join(t.TempDir(), "private")
		var out bytes.Buffer
		launched := false
		client := func(_ context.Context, got, command string, result any) error {
			if got != dir || command != "status" {
				t.Fatal(got, command)
			}
			if !launched {
				return errors.New("absent")
			}
			result.(*processStatus).ProcessID = 123
			return nil
		}
		launch := func(executable string, args []string, log *os.File) (backgroundProcess, error) {
			launched = true
			want := []string{"--state-dir", dir, "--locale", "ja", "run"}
			if offline {
				want = append(want, "--offline")
			}
			if !filepath.IsAbs(executable) || !reflect.DeepEqual(args, want) {
				t.Fatal(executable, args)
			}
			info, err := log.Stat()
			if err != nil || !info.Mode().IsRegular() {
				t.Fatal(err)
			}
			return backgroundProcess{PID: 123, Done: make(chan error)}, nil
		}
		if err := startBackground(context.Background(), dir, "ja", offline, true, &out, client, launch, time.Second); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `"state":"ready"`) || !strings.Contains(out.String(), `"startupApplied":true`) {
			t.Fatal(out.String())
		}
		before, _ := os.Stat(filepath.Join(dir, "startup.log"))
		out.Reset()
		if err := startBackground(context.Background(), dir, "ja", !offline, true, &out, client, func(string, []string, *os.File) (backgroundProcess, error) {
			t.Fatal("repeated startup launched again")
			return backgroundProcess{}, nil
		}, time.Second); err != nil {
			t.Fatal(err)
		}
		after, _ := os.Stat(filepath.Join(dir, "startup.log"))
		if !os.SameFile(before, after) || !strings.Contains(out.String(), `"startupApplied":false`) {
			t.Fatal("repeated startup changed state", out.String())
		}
	}
}

func TestBackgroundStartupDoesNotClaimAnotherProcessMode(t *testing.T) {
	launched := false
	client := func(_ context.Context, _, _ string, result any) error {
		if !launched {
			return errors.New("absent")
		}
		result.(*processStatus).ProcessID = 999
		return nil
	}
	launch := func(string, []string, *os.File) (backgroundProcess, error) {
		launched = true
		done := make(chan error, 1)
		done <- errors.New("another process owns this profile")
		return backgroundProcess{PID: 123, Done: done}, nil
	}
	var out bytes.Buffer
	if err := startBackground(context.Background(), t.TempDir(), "en", true, false, &out, client, launch, time.Second); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"startupApplied":false`) || !strings.Contains(out.String(), `"state":"already-running"`) || strings.Contains(out.String(), `"pid"`) {
		t.Fatal("another process was reported as the requested offline child", out.String())
	}
}

func TestBackgroundStartupEarlyExitCancellationAndSymlink(t *testing.T) {
	for _, scenario := range []string{"exit", "timeout", "cancel", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "symlink" {
				target := filepath.Join(t.TempDir(), "preserved")
				if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(dir, "startup.log")); err != nil {
					t.Skip("symlink unavailable", err)
				}
				defer func() {
					data, _ := os.ReadFile(target)
					if string(data) != "keep" {
						t.Fatal("symlink target modified")
					}
				}()
			}
			launch := func(string, []string, *os.File) (backgroundProcess, error) {
				if scenario == "symlink" {
					t.Fatal("launched with symlink log")
				}
				done := make(chan error, 1)
				if scenario == "exit" {
					done <- errors.New("exit")
				}
				if scenario == "cancel" {
					cancel()
				}
				return backgroundProcess{PID: 123, Done: done}, nil
			}
			err := startBackground(ctx, dir, "en", true, false, &bytes.Buffer{}, func(context.Context, string, string, any) error { return errors.New("absent") }, launch, time.Millisecond)
			if err == nil {
				t.Fatal("unconfirmed readiness accepted")
			}
			if scenario == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestAutostartRequiresExactReviewAndShowsSavedAutosave(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	profile := core.Profile{Version: 1, Settings: core.Settings{Network: "tailnet", Hostname: "example", Locale: "auto", Theme: "system"}, Peers: []core.Trust{{ID: "peer-a", Network: "tailnet", Generation: 1, Autosave: true, Directory: filepath.Join(root, "received")}}}
	if err := config.WriteJSON(filepath.Join(dir, "sobalink.json"), profile); err != nil {
		t.Fatal(err)
	}
	environment := func() (autostart.Options, error) {
		return autostart.Options{OS: "linux", Home: root, ConfigDir: filepath.Join(root, "config"), Executable: filepath.Join(root, "soba")}, nil
	}
	var commands []string
	runner := func(_ context.Context, executable string, args ...string) error {
		commands = append(commands, executable+" "+strings.Join(args, " "))
		return nil
	}
	var out bytes.Buffer
	preview := func(mode string) autostartReview {
		out.Reset()
		if err := autostartCommand(context.Background(), dir, []string{"--startup", mode, "--json"}, false, false, &out, environment, runner); err != nil {
			t.Fatal(err)
		}
		var review autostartReview
		if err := json.Unmarshal(out.Bytes(), &review); err != nil {
			t.Fatal(err)
		}
		return review
	}
	review := preview("saved")
	if !review.Startup.NetworkStarts || len(review.Startup.AutosaveReceivers) != 1 || review.Startup.ServicesRestart || review.Startup.TransfersRestart || len(commands) != 0 {
		t.Fatal(review, commands)
	}
	if _, err := os.Stat(review.Plan.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview changed registration", err)
	}
	offline := preview("offline")
	if offline.Startup.NetworkStarts || len(offline.Startup.AutosaveReceivers) != 0 || offline.ReviewToken == review.ReviewToken {
		t.Fatal(offline)
	}
	apply := func(token string) error {
		return autostartCommand(context.Background(), dir, []string{"--apply", "--review", token, "--json"}, false, false, &out, environment, runner)
	}
	if err := apply(""); err == nil || len(commands) != 0 {
		t.Fatal("unreviewed plan applied")
	}
	profile.Peers[0].Directory = filepath.Join(root, "other")
	if err := config.WriteJSON(filepath.Join(dir, "sobalink.json"), profile); err != nil {
		t.Fatal(err)
	}
	if err := apply(review.ReviewToken); err == nil || len(commands) != 0 {
		t.Fatal("stale review applied")
	}
	review = preview("saved")
	if err := apply(review.ReviewToken); err != nil || len(commands) != 2 {
		t.Fatal(err, commands)
	}
	data, err := os.ReadFile(review.Plan.Path)
	if err != nil || string(data) != review.Plan.Content {
		t.Fatal("reviewed plan not applied", err)
	}
}
