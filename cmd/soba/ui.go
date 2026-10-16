package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"runtime"
	"strings"
)

var uiPrivateTerminal = loginPrivateTerminal
var launchUIBrowser = func(ctx context.Context, target string) error { return launchLoginBrowser(ctx, target) }
var uiDesktopAvailable = func() bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_CLIENT") != "" || os.Getenv("SSH_TTY") != "" {
		return false
	}
	return runtime.GOOS != "linux" || os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func uiCommand(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, client controlCaller) error {
	f := commandFlags("ui", ja, out)
	manual := f.Bool("no-browser", false, text(ja, "show the URL and private one-time code without opening a browser", "ブラウザーを開かず、URLと非公開の一回用コードを表示"))
	machine := f.Bool("json", false, text(ja, "write the URL and private code as JSON; do not open a browser", "URLと非公開コードをJSONで出力し、ブラウザーは開かない"))
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *manual && *machine {
		return usageError(ja, "ui [--no-browser|--json]")
	}
	if dryRun {
		return errors.New(text(ja, "--dry-run is not available for ui", "ui は --dry-run に対応していません"))
	}
	if !*machine && !uiPrivateTerminal(out) {
		return fmt.Errorf(text(ja, "Use a private terminal, or explicitly choose %s for private machine output", "非公開の対話型端末で実行してください。機械向けの非公開出力には明示的に %s を指定してください"), cliCommandExample(dir, "ui", "--json"))
	}
	if !*machine && !*manual && !uiDesktopAvailable() {
		return fmt.Errorf(text(ja, "No local desktop browser detected. In a private terminal use %s; for private machine output use %s", "同じ端末のデスクトップブラウザーを確認できません。非公開の端末では %s、非公開の機械向け出力には %s を使ってください"), cliCommandExample(dir, "ui", "--no-browser"), cliCommandExample(dir, "ui", "--json"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if *machine {
		var raw json.RawMessage
		if err := client(ctx, dir, "ui", &raw); err != nil {
			return err
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(raw)
	}
	var access struct {
		URL  string `json:"url"`
		Code string `json:"code"`
	}
	if err := client(ctx, dir, "ui", &access); err != nil {
		return err
	}
	target, err := uiHandoffURL(access.URL, access.Code)
	if err != nil {
		return errors.New(text(ja, "Invalid local UI response; browser was not opened", "ローカル画面の応答が不正なため、ブラウザーを開きませんでした"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if *manual {
		fmt.Fprintln(out, text(ja, "Open this exact URL on the same device. The private code expires in five minutes; do not share or record it.", "同じ端末でこの正確なURLを開いてください。非公開コードは5分で失効します。共有・記録しないでください。"))
		fmt.Fprintln(out, access.URL)
		fmt.Fprintln(out, text(ja, "One-time code:", "一回用コード:"), access.Code)
		return nil
	}
	if err := launchUIBrowser(ctx, target); err != nil {
		// Opener errors may contain command arguments, including the private fragment.
		return fmt.Errorf(text(ja, "Could not open the browser. In a private terminal retry %s", "ブラウザーを開けませんでした。非公開の端末で %s を実行してください"), cliCommandExample(dir, "ui", "--no-browser"))
	}
	fmt.Fprintln(out, text(ja, "Opened the local UI in your browser:", "ローカル画面をブラウザーで開きました:"), access.URL)
	return nil
}

func uiHandoffURL(raw, code string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || !validUpgradeUIURL(raw) || u.Hostname() != "127.0.0.1" || strings.Contains(raw, "#") {
		return "", errors.New("invalid local UI URL")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil || len(decoded) != 24 || base64.RawURLEncoding.EncodeToString(decoded) != code {
		return "", errors.New("invalid local UI code")
	}
	u.Path = "/"
	u.Fragment = "handoff=" + code
	return u.String(), nil
}
