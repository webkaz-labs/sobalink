package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
	assets "github.com/webkaz-labs/sobalink/web"
	"golang.org/x/term"
)

func startCommand(ctx context.Context, command, dir, locale string, args []string, ja, dryRun bool, out io.Writer, client controlCaller) error {
	f := commandFlags(command, ja, out)
	offline := f.Bool("offline", false, text(ja, "open local management without starting the saved network", "保存済みのネットワークを開始せず、ローカル管理画面を開く"))
	background := f.Bool("background", false, text(ja, "detach the application and wait for local management readiness", "本体をバックグラウンドで起動し、ローカル管理機能の準備を待つ"))
	if err := parseFlags(f, args, ja); err != nil {
		return err
	}
	if command == "run" && *background {
		return errors.New(text(ja, "run stays in the foreground; use start --background", "run はフォアグラウンド専用です。start --background を使ってください"))
	}
	if dryRun {
		return errors.New(text(ja, "Use --dry-run with a configuration or action command; start launches the local agent", "--dry-run は設定・操作コマンドと使ってください。start は本体を起動します"))
	}
	if *background {
		return startBackground(ctx, dir, locale, *offline, ja, out, client, launchBackground, 10*time.Second)
	}
	return runForeground(ctx, dir, *offline, ja, out)
}

func runForeground(ctx context.Context, dir string, offline, ja bool, out io.Writer) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	app, err := core.Open(ctx, core.Options{Directory: dir, Version: version, SkipNetworkStart: offline})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, app.Close()) }()
	files, err := assets.Assets()
	if err != nil {
		return err
	}
	url, code, err := app.StartWeb(files)
	if err != nil {
		return err
	}
	ipc, err := control.ServeWithLimits(ctx, dir, app.IPC, app.LocalControlLimits)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, ipc.Close()) }()
	fmt.Fprintln(out, text(ja, "Local UI:", "ローカル画面:"), url)
	if file, ok := out.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprintln(out, text(ja, "One-time code (5 minutes):", "一回用コード（5分）:"), code)
	} else {
		fmt.Fprintln(out, text(ja, "Use soba ui in a terminal for a one-time sign-in code.", "soba ui で一回用ログインコードを表示できます。"))
	}
	fmt.Fprintln(out, text(ja, "Keep this process running. Ctrl+C stops connections and shares.", "このプロセスを起動したまま使います。Ctrl+C で接続と共有を停止します。"))
	select {
	case <-ctx.Done():
	case <-app.Done():
	}
	return nil
}

type backgroundProcess struct {
	PID  int
	Done <-chan error
}

type backgroundLauncher func(string, []string, *os.File) (backgroundProcess, error)

type processStatus struct {
	ProcessID int `json:"processId"`
}

func launchBackground(executable string, args []string, log *os.File) (backgroundProcess, error) {
	cmd := exec.Command(executable, args...)
	cmd.Stdout, cmd.Stderr = log, log
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return backgroundProcess{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return backgroundProcess{PID: cmd.Process.Pid, Done: done}, nil
}

func startBackground(ctx context.Context, dir, locale string, offline, ja bool, out io.Writer, client controlCaller, launch backgroundLauncher, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	ready := func() (int, bool) {
		probe, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		var snapshot processStatus
		err := client(probe, dir, "status", &snapshot)
		return snapshot.ProcessID, err == nil
	}
	writeResult := func(state string, pid int, logPath string) error {
		return json.NewEncoder(out).Encode(struct {
			State          string `json:"state"`
			PID            int    `json:"pid,omitempty"`
			Log            string `json:"log,omitempty"`
			StartupApplied bool   `json:"startupApplied"`
		}{state, pid, logPath, state == "ready"})
	}
	if _, ok := ready(); ok {
		return writeResult("already-running", 0, "")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := config.SecureDir(dir); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(dir, "startup.log")
	if info, err := os.Lstat(logPath); err == nil && !info.Mode().IsRegular() {
		return errors.New(text(ja, "startup.log must be a regular private file", "startup.log には通常の非公開ファイルが必要です"))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	if err := config.Protect(logPath, false); err != nil {
		return err
	}
	args := []string{"--state-dir", dir, "--locale", locale, "run"}
	if offline {
		args = append(args, "--offline")
	}
	process, err := launch(exe, args, log)
	if err != nil {
		return fmt.Errorf("%s: %w", text(ja, "Could not launch the background application", "バックグラウンド起動に失敗しました"), err)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if pid, ok := ready(); ok {
			if pid == process.PID && pid > 0 {
				return writeResult("ready", process.PID, logPath)
			}
			return writeResult("already-running", 0, "")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", text(ja, "Readiness check cancelled; the launched application may still be running. Use soba status or soba stop with this state directory", "準備確認を中断しました。起動した本体が動作している場合があります。同じ状態ディレクトリで soba status または soba stop を使ってください"), ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("%s: %s", text(ja, "Local readiness is unconfirmed; check this private log or run soba run with the same state directory. The process may still be running", "ローカル管理機能の準備を確認できません。この非公開ログを確認するか、同じ状態ディレクトリで soba run を使ってください。本体が動作している場合があります"), logPath)
		case <-process.Done:
			return fmt.Errorf("%s: %s", text(ja, "The background application exited before local readiness; inspect this private log or run soba run with the same state directory", "ローカル管理機能の準備前に本体が終了しました。この非公開ログを確認するか、同じ状態ディレクトリで soba run を使ってください"), logPath)
		case <-tick.C:
		}
	}
}
