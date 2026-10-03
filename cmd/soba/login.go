package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"golang.org/x/term"
)

var loginPrivateTerminal = func(out io.Writer) bool {
	f, ok := out.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

var launchLoginBrowser = func(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "open", url)
	case "windows":
		command = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.CommandContext(ctx, "xdg-open", url)
	}
	command.WaitDelay = 2 * time.Second
	return command.Run()
}

var loginPause = func(ctx context.Context) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func loginCommand(ctx context.Context, args []string, ja, dryRun bool, out io.Writer, dir string, client controlCaller, request func(string, any) error) error {
	flags := commandFlags("login", ja, out)
	qr := flags.Bool("qr", false, text(ja, "show a locally generated QR in a private terminal and wait", "非公開の端末にローカル生成QRを表示し、完了を待つ"))
	link := flags.Bool("link", false, text(ja, "explicitly output the private sign-in link", "非公開のサインインリンクを明示的に出力"))
	browser := flags.Bool("browser", false, text(ja, "open the official sign-in link and wait", "公式のサインインリンクをブラウザーで開き、完了を待つ"))
	wait := flags.Bool("wait", false, text(ja, "wait for sign-in or device approval", "サインイン完了または端末承認待ちまで待機"))
	refresh := flags.Bool("refresh", false, text(ja, "explicitly request a new sign-in flow", "新しいサインイン手続きを明示的に開始"))
	format := flags.String("qr-format", "small", text(ja, "small or large terminal QR", "QRの大きさ: small または large"))
	timeout := flags.Duration("timeout", 5*time.Minute, text(ja, "local wait duration; does not change the server link expiry", "ローカルの待機時間。リンクの有効期限は変更しません"))
	if err := parseFlags(flags, args, ja); err != nil {
		return err
	}
	if flags.NArg() != 0 || *timeout <= 0 || (*format != "small" && *format != "large") || (*qr && *browser) {
		return usageError(ja, "login [--qr|--link|--browser] [--wait] [--refresh] [--qr-format small|large] [--timeout 5m]")
	}
	if *qr && !loginPrivateTerminal(out) {
		return errors.New(text(ja, "QR needs a private terminal; use --link explicitly for redirected output", "QRには非公開の端末が必要です。リダイレクトする場合は --link を明示してください"))
	}
	payload := map[string]bool{}
	if *refresh {
		payload["refresh"] = true
	}
	if dryRun {
		return request("network.login", payload)
	}
	if !*qr && !*link && !*browser && !*wait {
		return request("network.login", payload)
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	query := func(name string, payload any, out any) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		command, err := json.Marshal(webui.Command{RequestID: fmt.Sprintf("login-%d", time.Now().UnixNano()), Name: name, Payload: raw})
		if err != nil {
			return err
		}
		return client(ctx, dir, string(command), out)
	}
	var view core.LoginView
	if err := query("network.login", payload, &view); err != nil {
		if ctx.Err() != nil {
			return loginWaitError(ctx.Err(), ja)
		}
		return err
	}
	human := *qr || *link || *browser
	lastURL := ""
	for {
		if err := ctx.Err(); err != nil {
			return loginWaitError(err, ja)
		}
		if view.State == "connected" || view.State == "approval-required" {
			if !human {
				return json.NewEncoder(out).Encode(view)
			}
			if view.State == "connected" {
				_, err := fmt.Fprintln(out, text(ja, "Sign-in complete. The node is connected; application readiness is separate.", "サインイン完了。ノードは接続済みです。アプリの動作は別途確認してください。"))
				return err
			}
			_, err := fmt.Fprintln(out, text(ja, "Account sign-in is complete; approve this node in the Tailnet administration console.", "アカウントのサインインは完了しています。Tailnet管理画面で端末を承認してください。"))
			return err
		}
		if view.AuthURL != "" && view.AuthURL != lastURL {
			if !core.ValidAuthURL(view.AuthURL) {
				return errors.New(text(ja, "Unexpected sign-in address was refused", "想定外のサインイン先を拒否しました"))
			}
			if lastURL != "" && human {
				fmt.Fprintln(out, text(ja, "Use the updated sign-in link or QR below.", "以下の更新されたリンク・QRを使ってください。"))
			}
			lastURL = view.AuthURL
			if human {
				fmt.Fprintln(out, text(ja, "This private link authorizes this node. Keep it private.", "この非公開リンクは端末の参加を許可します。共有しないでください。"))
			}
			if *link || human && loginPrivateTerminal(out) {
				fmt.Fprintln(out, view.AuthURL)
			}
			if *qr {
				if err := renderLoginQR(out, view.AuthURL, *format); err != nil {
					fmt.Fprintln(out, text(ja, "QR unavailable; use the private link above.", "QRを表示できません。上の非公開リンクを使ってください。"))
				}
			}
			if *browser {
				if err := launchLoginBrowser(ctx, view.AuthURL); err != nil {
					fmt.Fprintln(out, text(ja, "Browser could not open. Use login --link or login --qr in a private terminal.", "ブラウザーを開けませんでした。非公開の端末で login --link または login --qr を使ってください。"))
				}
			}
			if *link && !*wait && !*qr && !*browser {
				return nil
			}
		}
		if err := loginPause(ctx); err != nil {
			return loginWaitError(err, ja)
		}
		view = core.LoginView{}
		if err := query("network.login.status", map[string]bool{}, &view); err != nil {
			if ctx.Err() != nil {
				return loginWaitError(ctx.Err(), ja)
			}
			return err
		}
		if view.AuthURL == "" && lastURL != "" && view.State == "waiting" {
			if human {
				fmt.Fprintln(out, text(ja, "The sign-in link is no longer available; checking completion.", "サインインリンクは表示できなくなりました。完了状態を確認しています。"))
			}
			lastURL = ""
		}
	}
}

func loginWaitError(err error, ja bool) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", text(ja, "Sign-in wait cancelled. The node may still run and the displayed link is not revoked; use soba stop to stop the application", "サインイン待機を取消しました。端末の動作や表示済みリンクは失効しません。本体の停止には soba stop を使ってください"), err)
	}
	return fmt.Errorf("%s: %w", text(ja, "Sign-in wait timed out. Server link expiry is unknown; retry login or use --refresh explicitly", "サインイン待機が時間切れになりました。リンク側の期限は不明です。login を再実行するか --refresh を明示してください"), err)
}
