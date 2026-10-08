//go:build managed_restart_native && (linux || darwin || windows)

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/control"
)

// These acceptance tests use only synthetic owners. No Core is opened, no
// network backend or browser is started, and no existing profile is inspected.
// The only subprocess is this test executable with one exact helper selector.
const restartFixtureCode = "synthetic-private-restart-code"
const restartFixtureURL = "http://127.0.0.1:12345/"

type restartCloser func() error

func (f restartCloser) Close() error { return f() }

// TestManagedRestartChildFixture is inert unless started by this file's parent.
func TestManagedRestartChildFixture(t *testing.T) {
	if os.Getenv("SOBALINK_RESTART_FIXTURE") != "1" {
		return
	}
	dir, mode := os.Getenv("SOBALINK_RESTART_DIR"), os.Getenv("SOBALINK_RESTART_MODE")
	if !filepath.IsAbs(dir) || !strings.HasPrefix(filepath.Base(dir), "sr-") {
		t.Fatal("invalid synthetic profile")
	}
	// A hard bound applies even if a test unexpectedly deadlocks. It exits only
	// this synthetic child; it is not a product force-kill fallback.
	watchdog := time.AfterFunc(20*time.Second, func() { os.Exit(92) })
	defer watchdog.Stop()
	quit := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(quit) }()
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	lockOpen := true
	defer func() {
		if lockOpen {
			_ = lock.Close()
		}
	}()
	life, err := newUpgradeLifecycle(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	exitBeforeReady := make(chan struct{}, 1)
	stopHandler := life.handler(nil) // Only the stop route is delegated; it never dereferences Core.
	server, err := control.Serve(context.Background(), dir, func(ctx context.Context, raw string) (any, error) {
		switch raw {
		case lifecycleIdentityCommand:
			return life.identity, nil
		case "status":
			if mode == "exit-before-ready" {
				select {
				case exitBeforeReady <- struct{}{}:
				default:
				}
				return nil, errors.New("synthetic startup failure")
			}
			if mode == "not-ready" {
				return nil, errors.New("synthetic startup pending")
			}
			return processStatus{ProcessID: os.Getpid()}, nil
		case "ui":
			return map[string]string{"url": restartFixtureURL, "code": restartFixtureCode}, nil
		}
		if strings.HasPrefix(raw, lifecycleStopPrefix) {
			return stopHandler(ctx, raw)
		}
		return nil, errors.New("unsupported synthetic command")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	select {
	case <-quit:
		return
	case <-exitBeforeReady:
		return
	case request := <-life.stop:
		// The actual IPC server and profile lock participate in the same ordered
		// shutdown helper as production. Failure injection is confined to this test.
		ipc := restartCloser(func() error {
			err := server.Close()
			if mode == "ipc-error" {
				return errors.Join(err, errors.New("synthetic IPC close error"))
			}
			return err
		})
		app := restartCloser(func() error {
			if mode == "core-error" {
				return errors.New("synthetic Core close error")
			}
			return nil
		})
		lease := restartCloser(func() error {
			if mode == "lock-error" {
				return errors.New("synthetic lock retained")
			}
			lockOpen = false
			return lock.Close()
		})
		closeErr, lockErr := shutdownManagedOwners(ipc, app, lease)
		if runtime.GOOS != "windows" && mode != "cancel" {
			for path, want := range map[string]os.FileMode{request.AcknowledgementDir: 0700, filepath.Join(request.AcknowledgementDir, "control.sock"): 0600} {
				st, e := os.Stat(path)
				if e != nil || st.Mode().Perm() != want {
					t.Fatal("unprotected acknowledgement endpoint", e)
				}
			}
		}
		if mode == "lost-ack" {
			return
		}
		if mode == "wrong-ack" {
			bad := upgradeClosed{ProcessID: os.Getpid(), Instance: request.Instance, Token: strings.Repeat("0", 64), Closed: true, LockReleased: true}
			raw, _ := json.Marshal(bad)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err := control.Call(ctx, request.AcknowledgementDir, string(raw), nil)
			cancel()
			if err == nil {
				t.Fatal("forged acknowledgement accepted")
			}
			if err := os.WriteFile(filepath.Join(dir, "rejected-ack"), []byte("rejected"), 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err := acknowledgeUpgradeShutdown(request, closeErr, lockErr); err != nil {
			// Cancellation is an intentional loss of the acknowledgement listener.
			if mode != "cancel" {
				t.Fatal(err)
			}
		}
		if mode == "no-exit" || mode == "lock-error" {
			<-quit
		}
	}
}

type restartChild struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	done   chan struct{}
	err    error
	output bytes.Buffer
	once   sync.Once
}

func restartProfile(t *testing.T) string {
	t.Helper()
	// Short names keep the protected acknowledgement's Unix socket under the
	// platform path bound. This is always a newly created, private directory.
	dir, err := os.MkdirTemp("", "sr-")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Protect(dir, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func startRestartChild(t *testing.T, dir, mode string) *restartChild {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &restartChild{done: make(chan struct{})}
	p.cmd = exec.Command(exe, "-test.run=^TestManagedRestartChildFixture$", "-test.count=1")
	// Do not inherit credentials, production logging markers or user settings.
	p.cmd.Env = []string{"GORACE=halt_on_error=1 exitcode=66 atexit_sleep_ms=0", "SOBALINK_RESTART_FIXTURE=1", "SOBALINK_RESTART_DIR=" + dir, "SOBALINK_RESTART_MODE=" + mode, "HOME=" + dir, "USERPROFILE=" + dir, "TMPDIR=" + dir, "TMP=" + dir, "TEMP=" + dir}
	if runtime.GOOS == "windows" {
		p.cmd.Env = append(p.cmd.Env, "SystemRoot="+os.Getenv("SystemRoot"))
	}
	p.cmd.Dir = dir
	p.cmd.Stdout = &p.output
	p.cmd.Stderr = &p.output
	p.input, err = p.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = p.cmd.Start(); err != nil {
		_ = p.input.Close()
		t.Fatal(err)
	}
	go func() { p.err = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() { p.finish(t) })
	return p
}

func (p *restartChild) finish(t *testing.T) {
	t.Helper()
	p.once.Do(func() { _ = p.input.Close() }) // Cooperative fixture-only stop.
	select {
	case <-p.done:
		if p.err != nil {
			t.Errorf("synthetic child failed: %v; output: %s", p.err, p.output.String())
		}
		if strings.Contains(p.output.String(), restartFixtureCode) {
			t.Error("private UI code leaked into child output")
		}
	case <-time.After(25 * time.Second):
		t.Error("synthetic child exceeded self-exit watchdog")
	}
}

func restartIdentity(t *testing.T, dir string, p *restartChild) upgradeIdentity {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		var id upgradeIdentity
		probe, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		err := control.Call(probe, dir, lifecycleIdentityCommand, &id)
		stop()
		if err == nil {
			if id.ProcessID != p.cmd.Process.Pid || !sameUpgradeExecutable(p.cmd.Path, id.Executable) || id.Instance == "" {
				t.Fatal("unbound synthetic identity")
			}
			return id
		}
		select {
		case <-p.done:
			t.Fatalf("child exited before identity: %v", p.err)
		case <-ctx.Done():
			t.Fatal("child identity timeout")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func assertNoRestartAcks(t *testing.T, dir string) {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, ".upgrade-*"))
	if err != nil || len(names) != 0 {
		t.Fatalf("acknowledgement endpoint not cleaned: %v %v", names, err)
	}
}

func TestManagedRestartNativeCleanSuccessor(t *testing.T) {
	dir := restartProfile(t)
	oldChild := startRestartChild(t, dir, "clean")
	old := restartIdentity(t, dir, oldChild)
	if lock, err := config.AcquireLock(dir); err == nil {
		_ = lock.Close()
		t.Fatal("live child did not own profile lock")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var successor *restartChild
	next, err := managedUpgradeRestartSequence(old, func() error {
		return stopForManagedUpgrade(ctx, dir, old, control.Call)
	}, func() (upgradeIdentity, error) {
		// The native stop observer already confirmed exit. Also require the
		// test harness to reap that exact child before launching a replacement.
		select {
		case <-oldChild.done:
		case <-time.After(time.Second):
			t.Fatal("old process was not reaped")
		}
		if oldChild.err != nil {
			t.Fatal(oldChild.err)
		}
		lock, err := config.AcquireLock(dir)
		if err != nil {
			return upgradeIdentity{}, err
		}
		if err = lock.Close(); err != nil {
			return upgradeIdentity{}, err
		}
		successor = startRestartChild(t, dir, "clean")
		return restartIdentity(t, dir, successor), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.ProcessID != successor.cmd.Process.Pid || next.Instance == old.Instance || !next.Offline || next.AttemptedNetwork || next.NetworkReady {
		t.Fatal("successor not fresh/offline")
	}
	assertNoRestartAcks(t, dir)
	// The successor still holds its own real lock until cooperative cleanup.
	if lock, err := config.AcquireLock(dir); err == nil {
		_ = lock.Close()
		t.Fatal("successor lacks profile lock")
	}
}

func TestManagedRestartNativeShutdownFailures(t *testing.T) {
	for _, mode := range []string{"ipc-error", "core-error", "lock-error", "lost-ack", "wrong-ack", "no-exit", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			dir := restartProfile(t)
			child := startRestartChild(t, dir, mode)
			old := restartIdentity(t, dir, child)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			client := controlCaller(control.Call)
			if mode == "cancel" {
				client = func(ctx context.Context, dir, raw string, result any) error {
					err := control.Call(ctx, dir, raw, result)
					cancel()
					return err
				}
			}
			launched := false
			_, err := managedUpgradeRestartSequence(old, func() error { return stopForManagedUpgrade(ctx, dir, old, client) }, func() (upgradeIdentity, error) { launched = true; return upgradeIdentity{}, nil })
			if err == nil || launched {
				t.Fatal("unconfirmed shutdown permitted launch")
			}
			switch mode {
			case "ipc-error", "core-error", "lock-error":
				if !strings.Contains(err.Error(), "incomplete shutdown") {
					t.Fatal(err)
				}
			case "lost-ack", "wrong-ack", "no-exit":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			if mode == "lock-error" {
				if lock, e := config.AcquireLock(dir); e == nil {
					_ = lock.Close()
					t.Fatal("injected lock failure did not retain lock")
				}
			}
			if mode == "wrong-ack" {
				if _, e := os.Stat(filepath.Join(dir, "rejected-ack")); e != nil {
					t.Fatal("invalid acknowledgement was not exercised", e)
				}
			}
			if mode == "no-exit" {
				select {
				case <-child.done:
					t.Fatal("exit was not actually pending")
				default:
				}
			}
			assertNoRestartAcks(t, dir)
			child.finish(t)
			lock, e := config.AcquireLock(dir)
			if e != nil {
				t.Fatal("fixture cleanup did not release profile", e)
			}
			_ = lock.Close()
		})
	}
}

func TestManagedRestartNativeLostStopReplyStillRequiresAckAndExit(t *testing.T) {
	dir := restartProfile(t)
	child := startRestartChild(t, dir, "clean")
	old := restartIdentity(t, dir, child)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := func(ctx context.Context, dir, raw string, result any) error {
		if err := control.Call(ctx, dir, raw, result); err != nil {
			return err
		}
		return errors.New("synthetic lost accepted stop response")
	}
	if err := stopForManagedUpgrade(ctx, dir, old, client); err != nil {
		t.Fatal(err)
	}
	child.finish(t)
	assertNoRestartAcks(t, dir)
}

func TestManagedRestartNativeRejectsWrongOwner(t *testing.T) {
	dir := restartProfile(t)
	child := startRestartChild(t, dir, "clean")
	old := restartIdentity(t, dir, child)
	old.Instance = "wrong-synthetic-instance"
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := stopForManagedUpgrade(ctx, dir, old, control.Call); err == nil {
		t.Fatal("wrong instance accepted")
	}
	select {
	case <-child.done:
		t.Fatal("wrong instance stopped owner")
	default:
	}
	_ = restartIdentity(t, dir, child)
	assertNoRestartAcks(t, dir)
}

func TestManagedRestartNativeUIPrivateBoundary(t *testing.T) {
	dir := restartProfile(t)
	child := startRestartChild(t, dir, "clean")
	_ = restartIdentity(t, dir, child)
	oldPrivate, oldBrowser := loginPrivateTerminal, launchLoginBrowser
	defer func() { loginPrivateTerminal, launchLoginBrowser = oldPrivate, oldBrowser }()
	var private bytes.Buffer
	var opened string
	loginPrivateTerminal = func(w io.Writer) bool { return w == &private }
	launchLoginBrowser = func(_ context.Context, url string) error { opened = url; return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := reopenUpgradeUI(ctx, dir, false, &private, control.Call); err != nil {
		t.Fatal(err)
	}
	if opened != restartFixtureURL || strings.Contains(opened, restartFixtureCode) || !strings.Contains(private.String(), restartFixtureCode) {
		t.Fatal("private presentation boundary failed")
	}
	if strings.Contains(strings.Join(child.cmd.Args, " "), restartFixtureCode) {
		t.Fatal("code leaked into argv")
	}
	var redirected bytes.Buffer
	if err := reopenUpgradeUI(ctx, dir, false, &redirected, func(context.Context, string, string, any) error {
		t.Fatal("redirected output requested code")
		return nil
	}); err == nil {
		t.Fatal("redirected output accepted")
	}
	if redirected.Len() != 0 {
		t.Fatal("private data written to redirected output")
	}
	child.finish(t)
}

func TestManagedRestartNativeStartupOutcomes(t *testing.T) {
	for _, mode := range []string{"clean", "not-ready", "cancel", "exit-before-ready"} {
		t.Run(mode, func(t *testing.T) {
			dir := restartProfile(t)
			var child *restartChild
			launches := 0
			var result bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			err := startBackground(ctx, dir, "en", true, false, &result, control.Call, func(exe string, args []string, log *os.File) (backgroundProcess, error) {
				launches++
				if !sameUpgradeExecutable(exe, os.Args[0]) || len(args) != 6 || args[5] != "--offline" || strings.Contains(strings.Join(args, " "), restartFixtureCode) {
					t.Fatal("unsafe successor arguments")
				}
				fixtureMode := mode
				if mode == "cancel" {
					fixtureMode = "not-ready"
				}
				child = startRestartChild(t, dir, fixtureMode)
				_ = restartIdentity(t, dir, child)
				done := make(chan error, 1)
				go func() { <-child.done; done <- child.err }()
				if mode == "cancel" {
					cancel()
				}
				return backgroundProcess{PID: child.cmd.Process.Pid, Done: done}, nil
			}, 300*time.Millisecond)
			if launches != 1 {
				t.Fatal("unexpected retry", launches)
			}
			switch mode {
			case "clean":
				var got struct {
					State string `json:"state"`
					PID   int    `json:"pid"`
				}
				if err != nil || json.Unmarshal(result.Bytes(), &got) != nil || got.State != "ready" || got.PID != child.cmd.Process.Pid {
					t.Fatalf("successor readiness mismatch: %v %s", err, result.String())
				}
			case "not-ready", "cancel":
				if err == nil || !strings.Contains(err.Error(), "may still be running") || result.Len() != 0 {
					t.Fatalf("startup uncertainty not preserved: %v", err)
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				select {
				case <-child.done:
					t.Fatal("uncertain startup killed synthetic successor")
				default:
				}
			case "exit-before-ready":
				if err == nil || !strings.Contains(err.Error(), "exited before local readiness") || result.Len() != 0 {
					t.Fatalf("startup failure not preserved: %v", err)
				}
			}
			raw, e := os.ReadFile(filepath.Join(dir, "startup.log"))
			if e != nil {
				t.Fatal(e)
			}
			if bytes.Contains(raw, []byte(restartFixtureCode)) {
				t.Fatal("code leaked into startup log")
			}
			child.finish(t)
		})
	}
}
