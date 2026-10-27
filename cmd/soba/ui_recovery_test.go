package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
)

func existingUIProfile(t *testing.T) string {
	t.Helper()
	dir := privateStateDir(t)
	if _, err := core.InitializeProfile(t.Context(), core.Options{Directory: dir}, "example-node"); err != nil {
		t.Fatal(err)
	}
	return dir
}

func setUIRecoveryLauncher(t *testing.T, launch func(context.Context, string, bool, controlCaller) (bool, error)) {
	t.Helper()
	old := launchUIOffline
	launchUIOffline = launch
	t.Cleanup(func() { launchUIOffline = old })
}

func TestUIStoppedProfileRecoversOfflineBeforeOpening(t *testing.T) {
	for _, ja := range []bool{false, true} {
		for _, manual := range []bool{false, true} {
			t.Run(map[bool]string{false: "en", true: "ja"}[ja]+map[bool]string{false: "-browser", true: "-manual"}[manual], func(t *testing.T) {
				out, success, _, opened := setupUI(t)
				dir := existingUIProfile(t)
				before, _ := os.ReadFile(filepath.Join(dir, "sobalink.json"))
				started, calls := false, 0
				client := func(ctx context.Context, got, raw string, result any) error {
					calls++
					if got != dir || raw != "ui" {
						t.Fatal("wrong profile or request")
					}
					if !started {
						return os.ErrNotExist
					}
					return success(ctx, got, raw, result)
				}
				setUIRecoveryLauncher(t, func(ctx context.Context, got string, gotJA bool, call controlCaller) (bool, error) {
					if got != dir || gotJA != ja || *opened != "" || calls != 1 {
						t.Fatal("startup order or selected profile changed")
					}
					started = true
					return true, nil
				})
				var args []string
				if manual {
					args = []string{"--no-browser"}
				}
				if err := uiCommand(t.Context(), args, dir, ja, false, out, client); err != nil {
					t.Fatal(err)
				}
				if !started || calls != 2 || (*opened == "") != manual {
					t.Fatal("recovery did not complete once")
				}
				notice := text(ja, "Started local management only.", "ローカル管理のみを起動しました。")
				if !strings.Contains(out.String(), notice) || (!manual && strings.Contains(out.String(), syntheticUICode)) {
					t.Fatal("missing offline notice or leaked code")
				}
				after, _ := os.ReadFile(filepath.Join(dir, "sobalink.json"))
				if !bytes.Equal(before, after) {
					t.Fatal("UI preflight rewrote saved profile")
				}
			})
		}
	}
}

func TestUIAbsentProfileOffersInitializationWithoutWriting(t *testing.T) {
	for _, ja := range []bool{false, true} {
		out, _, _, opened := setupUI(t)
		dir := filepath.Join(t.TempDir(), "not-created")
		setUIRecoveryLauncher(t, func(context.Context, string, bool, controlCaller) (bool, error) {
			t.Fatal("uninitialized UI launched")
			return false, nil
		})
		err := uiCommand(t.Context(), nil, dir, ja, false, out, func(context.Context, string, string, any) error { return os.ErrNotExist })
		var coded interface{ ErrorCode() string }
		if !errors.As(err, &coded) || coded.ErrorCode() != "ui_profile_uninitialized" || !strings.Contains(err.Error(), cliCommandExample(dir, "init")) || !strings.Contains(err.Error(), cliCommandExample(dir, "ui")) || *opened != "" {
			t.Fatal("missing safe first-use guidance", err)
		}
		if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("UI created absent state")
		}
	}
}

func TestUIRecoveryNeverStartsForOtherFailuresOrMachineOutput(t *testing.T) {
	for _, failure := range []error{os.ErrPermission, context.Canceled, context.DeadlineExceeded, io.EOF, errors.New("unexpected response")} {
		out, _, _, opened := setupUI(t)
		setUIRecoveryLauncher(t, func(context.Context, string, bool, controlCaller) (bool, error) {
			t.Fatal("uncertain failure launched")
			return false, nil
		})
		calls := 0
		err := uiCommand(t.Context(), nil, "unused", false, false, out, func(context.Context, string, string, any) error { calls++; return failure })
		if !errors.Is(err, failure) || calls != 1 || *opened != "" {
			t.Fatal("failure was reclassified or replayed", err)
		}
	}
	out, _, _, opened := setupUI(t)
	setUIRecoveryLauncher(t, func(context.Context, string, bool, controlCaller) (bool, error) {
		t.Fatal("machine output launched")
		return false, nil
	})
	err := uiCommand(t.Context(), []string{"--json"}, "selected-profile", false, false, out, func(context.Context, string, string, any) error { return os.ErrNotExist })
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) || coded.ErrorCode() != "ui_agent_unavailable" || !strings.Contains(err.Error(), "start --offline --background") || *opened != "" || out.Len() != 0 {
		t.Fatal("machine absence changed contract", err)
	}
	var encoded bytes.Buffer
	writeCommandError(&encoded, &jsonCommandError{err})
	var result struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(encoded.Bytes(), &result) != nil || result.Code != "ui_agent_unavailable" {
		t.Fatal("unstable machine failure")
	}
}

func TestUIRecoveryGatesBeforeIPCAndStartup(t *testing.T) {
	for _, scenario := range []string{"redirected", "headless", "dry-run", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			out, _, _, opened := setupUI(t)
			setUIRecoveryLauncher(t, func(context.Context, string, bool, controlCaller) (bool, error) {
				t.Fatal("protected UI launched")
				return false, nil
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "redirected" {
				uiPrivateTerminal = func(io.Writer) bool { return false }
			}
			if scenario == "headless" {
				uiDesktopAvailable = func() bool { return false }
			}
			if scenario == "cancelled" {
				cancel()
			}
			err := uiCommand(ctx, nil, "unused", false, scenario == "dry-run", out, func(context.Context, string, string, any) error { t.Fatal("protected UI called IPC"); return nil })
			if err == nil || *opened != "" {
				t.Fatal("missing protection")
			}
		})
	}
}

func TestUIRecoveryDoesNotReplayFailedStartupOrPostStartRequest(t *testing.T) {
	for _, failStartup := range []bool{false, true} {
		out, success, _, opened := setupUI(t)
		dir := existingUIProfile(t)
		calls, launches := 0, 0
		failure := errors.New("readiness is unconfirmed")
		setUIRecoveryLauncher(t, func(context.Context, string, bool, controlCaller) (bool, error) {
			launches++
			if failStartup {
				return false, failure
			}
			return false, nil
		})
		err := uiCommand(t.Context(), nil, dir, false, false, out, func(ctx context.Context, d, raw string, result any) error {
			calls++
			if calls == 1 {
				return os.ErrNotExist
			}
			if calls == 2 {
				return io.EOF
			}
			return success(ctx, d, raw, result)
		})
		wantCalls := 2
		if failStartup {
			wantCalls = 1
		}
		if err == nil || launches != 1 || calls != wantCalls || *opened != "" || out.Len() != 0 {
			t.Fatal("uncertain outcome replayed or offline startup claimed", err)
		}
	}
}

func TestUIRecoveryBackgroundRequiresExactOfflineArgumentsAndReadiness(t *testing.T) {
	dir := existingUIProfile(t)
	launched := false
	var out bytes.Buffer
	client := func(_ context.Context, got, command string, v any) error {
		if got != dir || command != "status" {
			t.Fatal("wrong probe")
		}
		if !launched {
			return os.ErrNotExist
		}
		v.(*processStatus).ProcessID = 123
		return nil
	}
	launch := func(_ string, args []string, _ *os.File) (backgroundProcess, error) {
		expected := []string{"--state-dir", dir, "--locale", "en", "run", "--offline", "--existing-profile"}
		if strings.Join(args, "\x00") != strings.Join(expected, "\x00") || !backgroundLogInvocation(args, filepath.Join(dir, "startup.log")) {
			t.Fatal("unsafe child invocation", args)
		}
		launched = true
		return backgroundProcess{PID: 123, Done: make(chan error)}, nil
	}
	if err := startBackgroundWithProfile(t.Context(), dir, "en", true, true, false, &out, client, launch, time.Second); err != nil {
		t.Fatal(err)
	}
	if !launched || !strings.Contains(out.String(), `"state":"ready"`) {
		t.Fatal("readiness missing")
	}
}

func TestUIRecoveryBackgroundRefusesUnexpectedProbeWithoutWriting(t *testing.T) {
	dir := existingUIProfile(t)
	launch := func(string, []string, *os.File) (backgroundProcess, error) {
		t.Fatal("permission failure launched")
		return backgroundProcess{}, nil
	}
	err := startBackgroundWithProfile(t.Context(), dir, "en", true, true, false, io.Discard, func(context.Context, string, string, any) error { return os.ErrPermission }, launch, time.Second)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatal("permission failure hidden", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "startup.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed preflight wrote startup log")
	}
}

func TestUIRecoveryChildRefusesDisappearedProfile(t *testing.T) {
	dir := existingUIProfile(t)
	if err := core.CheckExistingProfile(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "sobalink.json")); err != nil {
		t.Fatal(err)
	}
	err := runWith(t.Context(), []string{"--state-dir", dir, "run", "--offline", "--existing-profile"}, io.Discard, strings.NewReader(""), func(context.Context, string, string, any) error { t.Fatal("child called client IPC"); return nil })
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disappeared profile initialized", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sobalink.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("profile recreated")
	}
}

func TestUIRecoveryWaitsForConcurrentOwnerAfterLosingChildExits(t *testing.T) {
	dir := existingUIProfile(t)
	probes, launches := 0, 0
	var out bytes.Buffer
	client := func(_ context.Context, _, _ string, v any) error {
		probes++
		if probes < 3 {
			return os.ErrNotExist
		}
		v.(*processStatus).ProcessID = 999
		return nil
	}
	launch := func(string, []string, *os.File) (backgroundProcess, error) {
		launches++
		done := make(chan error, 1)
		done <- errors.New("synthetic owner conflict")
		return backgroundProcess{PID: 123, Done: done}, nil
	}
	err := startBackgroundWithProfile(t.Context(), dir, "en", true, true, false, &out, client, launch, time.Second)
	if err != nil || launches != 1 || !strings.Contains(out.String(), `"state":"already-running"`) || !strings.Contains(out.String(), `"startupApplied":false`) {
		t.Fatal("concurrent winner was not reused without relaunch or mode claim", err, out.String())
	}
}

func TestUIRecoveryStartupFailureKeepsOfflineDiagnosticAndProfile(t *testing.T) {
	dir := existingUIProfile(t)
	var out bytes.Buffer
	launch := func(string, []string, *os.File) (backgroundProcess, error) {
		done := make(chan error, 1)
		done <- errors.New("synthetic startup failure")
		return backgroundProcess{PID: 123, Done: done}, nil
	}
	err := startBackgroundWithProfile(t.Context(), dir, "en", true, true, false, &out, func(context.Context, string, string, any) error { return os.ErrNotExist }, launch, 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), cliCommandExample(dir, "run", "--offline", "--existing-profile")) || !strings.Contains(err.Error(), cliCommandExample(dir, "status")) || out.Len() != 0 {
		t.Fatal("unsafe or missing offline failure recovery", err)
	}
}

func TestUIRecoveryCorruptProfileNeverLaunches(t *testing.T) {
	out, _, _, opened := setupUI(t)
	dir := existingUIProfile(t)
	if err := os.WriteFile(filepath.Join(dir, "sobalink.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	setUIRecoveryLauncher(t, func(context.Context, string, bool, controlCaller) (bool, error) {
		t.Fatal("corrupt profile launched")
		return false, nil
	})
	err := uiCommand(t.Context(), nil, dir, false, false, out, func(context.Context, string, string, any) error { return os.ErrNotExist })
	if err == nil || *opened != "" || out.Len() != 0 {
		t.Fatal("corrupt profile was treated as uninitialized or usable")
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) && coded.ErrorCode() == "ui_profile_uninitialized" {
		t.Fatal("corruption replaced with initialization advice")
	}
}

func TestUIRecoveryExplicitManualHeadlessKeepsMode(t *testing.T) {
	out, success, _, opened := setupUI(t)
	uiDesktopAvailable = func() bool { return false }
	dir := existingUIProfile(t)
	calls, launches := 0, 0
	setUIRecoveryLauncher(t, func(context.Context, string, bool, controlCaller) (bool, error) { launches++; return true, nil })
	err := uiCommand(t.Context(), []string{"--no-browser"}, dir, false, false, out, func(ctx context.Context, d, raw string, v any) error {
		calls++
		if calls == 1 {
			return os.ErrNotExist
		}
		return success(ctx, d, raw, v)
	})
	if err != nil || calls != 2 || launches != 1 || *opened != "" || !strings.Contains(out.String(), syntheticUICode) {
		t.Fatal("explicit manual recovery failed", err)
	}
	err = uiUnavailableError(os.ErrNotExist, dir, false, "--no-browser")
	if !strings.Contains(err.Error(), cliCommandExample(dir, "ui", "--no-browser")) {
		t.Fatal("manual recovery mode lost")
	}
}

func TestUIRecoveryPostLaunchPermissionDoesNotRetry(t *testing.T) {
	dir := existingUIProfile(t)
	probes, launches := 0, 0
	var out bytes.Buffer
	client := func(context.Context, string, string, any) error {
		probes++
		if probes == 1 {
			return os.ErrNotExist
		}
		return os.ErrPermission
	}
	launch := func(string, []string, *os.File) (backgroundProcess, error) {
		launches++
		return backgroundProcess{PID: 123, Done: make(chan error)}, nil
	}
	err := startBackgroundWithProfile(t.Context(), dir, "en", true, true, false, &out, client, launch, time.Second)
	if !errors.Is(err, os.ErrPermission) || probes != 2 || launches != 1 || out.Len() != 0 || !strings.Contains(err.Error(), cliCommandExample(dir, "status")) {
		t.Fatal("postlaunch permission hidden or retried", err)
	}
}
