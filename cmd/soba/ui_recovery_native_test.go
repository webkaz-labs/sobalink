package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
)

// The native subprocess runs the production offline owner and protected IPC,
// including a real Windows named pipe on Windows. It uses a fresh synthetic
// profile, starts no network backend, and never opens a real desktop browser.
func TestUIRecoveryForegroundHelper(t *testing.T) {
	if os.Getenv("SOBA_TEST_UI_RECOVERY_CHILD") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			os.Exit(mainExitCode())
		}
	}
	os.Exit(90)
}

func TestUIRecoveryNativeStopRestart(t *testing.T) {
	out, _, _, opened := setupUI(t)
	dir := existingUIProfile(t)
	path := filepath.Join(dir, "sobalink.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	original = bytes.Replace(original, []byte(`"network": "none"`), []byte(`"network": "tailnet"`), 1)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	type child struct {
		cmd    *exec.Cmd
		exited chan struct{}
	}
	var children []child
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = control.Call(ctx, dir, "stop", nil)
		for _, child := range children {
			select {
			case <-child.exited:
			case <-time.After(6 * time.Second):
				_ = child.cmd.Process.Kill()
				<-child.exited
			}
		}
	})
	launcher := func(executable string, args []string, log *os.File) (backgroundProcess, error) {
		if len(args) != 7 || args[4] != "run" || args[5] != "--offline" || args[6] != "--existing-profile" {
			t.Fatal("unsafe native child invocation")
		}
		command := exec.Command(executable, append([]string{"-test.run=^TestUIRecoveryForegroundHelper$", "--"}, args...)...)
		command.Env = append(os.Environ(), "SOBA_TEST_UI_RECOVERY_CHILD=1", backgroundLogEnv+"="+log.Name())
		detach(command)
		if err := command.Start(); err != nil {
			return backgroundProcess{}, err
		}
		done := make(chan error, 1)
		exited := make(chan struct{})
		children = append(children, child{command, exited})
		go func() { done <- command.Wait(); close(exited) }()
		return backgroundProcess{PID: command.Process.Pid, Done: done}, nil
	}
	setUIRecoveryLauncher(t, func(ctx context.Context, got string, ja bool, call controlCaller) (bool, error) {
		return startUIOfflineWithLauncher(ctx, got, ja, call, launcher)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for cycle := 0; cycle < 2; cycle++ {
		*opened = ""
		out.Reset()
		if err := uiCommand(ctx, nil, dir, false, false, out, controlCall); err != nil {
			t.Fatal("native UI recovery failed", err)
		}
		if *opened == "" || len(children) != cycle+1 || !strings.Contains(out.String(), "Started local management only.") {
			t.Fatal("native recovery did not open once with its offline notice")
		}
		var status struct {
			ProcessID int `json:"processId"`
			Self      struct {
				Status   string `json:"status"`
				Guidance *struct {
					Code string `json:"code"`
				} `json:"guidance"`
			} `json:"self"`
			Settings struct {
				Network string `json:"network"`
			} `json:"settings"`
		}
		if err := controlCall(ctx, dir, "status", &status); err != nil {
			t.Fatal(err)
		}
		if status.ProcessID != children[cycle].cmd.Process.Pid || status.Self.Status != "idle" || status.Settings.Network != "tailnet" || status.Self.Guidance == nil || status.Self.Guidance.Code != "network_stopped" {
			t.Fatal("native recovery restarted saved networking or selected another owner")
		}
		// Existing-owner repeats obtain a new capability without a second process.
		out.Reset()
		if err := uiCommand(ctx, nil, dir, false, false, out, controlCall); err != nil {
			t.Fatal(err)
		}
		if len(children) != cycle+1 || strings.Contains(out.String(), "Started local management only.") {
			t.Fatal("existing owner was restarted")
		}
		if err := controlCall(ctx, dir, "stop", nil); err != nil {
			t.Fatal(err)
		}
		select {
		case <-children[cycle].exited:
		case <-ctx.Done():
			t.Fatal("native owner did not finish stopping")
		}
		err := control.Call(ctx, dir, "status", nil)
		if !control.Unavailable(err) {
			t.Fatal("closed native IPC did not classify as unavailable", err)
		}
	}
	after, err := os.ReadFile(path)
	var savedBefore, savedAfter core.Profile
	if err != nil || json.Unmarshal(original, &savedBefore) != nil || json.Unmarshal(after, &savedAfter) != nil || !reflect.DeepEqual(savedBefore.Settings, savedAfter.Settings) {
		t.Fatal("native offline recovery changed saved settings")
	}
	out.Reset()
	*opened = ""
	err = uiCommand(ctx, []string{"--json"}, dir, false, false, out, controlCall)
	if err == nil || len(children) != 2 || *opened != "" || out.Len() != 0 {
		t.Fatal("machine query unexpectedly started native owner")
	}
	var machine bytes.Buffer
	writeCommandError(&machine, &jsonCommandError{err})
	var failure struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(machine.Bytes(), &failure) != nil || failure.Code != "ui_agent_unavailable" {
		t.Fatal("native query failure contract")
	}
}
