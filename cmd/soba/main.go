package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/messageframe"
	"github.com/webkaz-labs/sobalink/internal/webui"
	assets "github.com/webkaz-labs/sobalink/web"
	"golang.org/x/term"
)

var version = "0.0.0-dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := run(ctx, os.Args[1:], os.Stdout); e != nil {
		writeCommandError(os.Stderr, e)
		os.Exit(1)
	}
}
func japanese(locale string) bool {
	if locale == "ja" {
		return true
	}
	if locale == "en" {
		return false
	}
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(key); value != "" {
			return isJapanese(value)
		}
	}
	return isJapanese(nativePreferredLanguage())
}
func isJapanese(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "ja" || strings.HasPrefix(value, "ja_") || strings.HasPrefix(value, "ja-") || strings.HasPrefix(value, "ja.") || strings.HasPrefix(value, "ja@")
}
func text(ja bool, en, jp string) string {
	if ja {
		return jp
	}
	return en
}

type controlCaller func(context.Context, string, string, any) error

func run(ctx context.Context, args []string, out io.Writer) error {
	return runWith(ctx, args, out, os.Stdin, control.Call)
}

func runWith(ctx context.Context, args []string, out io.Writer, stdin io.Reader, client controlCaller) (err error) {
	var jsonErrors bool
	// Help is a successful, side-effect-free action at every command level.
	defer func() {
		if errors.Is(err, flag.ErrHelp) {
			err = nil
		} else if err != nil && jsonErrors {
			err = &jsonCommandError{err}
		}
	}()
	var dir string
	locale := "auto"
	global := flag.NewFlagSet("soba", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	global.StringVar(&dir, "state-dir", "", "private state directory")
	global.StringVar(&locale, "locale", locale, "auto, ja or en")
	global.BoolVar(&jsonErrors, "json-errors", false, "write stable JSON errors to stderr")
	dryRun := global.Bool("dry-run", false, "preview the command without applying it")
	showVersion := global.Bool("version", false, "show version")
	global.Usage = func() { fmt.Fprintln(out, text(japanese(locale), helpEN, helpJA)) }
	if e := global.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return e
		}
		return fmt.Errorf("%s: %w", text(japanese(locale), "Invalid option; use soba help", "指定が正しくありません。soba help を参照してください"), e)
	}
	ja := japanese(locale)
	if locale != "auto" && locale != "ja" && locale != "en" {
		return errors.New(text(ja, "--locale must be auto, ja or en", "--locale は auto、ja、en から選んでください"))
	}
	args = global.Args()
	command := "start"
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}
	if *showVersion {
		command = "version"
	}
	if command == "version" {
		if len(args) != 0 {
			return usageError(ja, "version")
		}
		_, e := fmt.Fprintln(out, "sobalink", version, "(soba)")
		return e
	}
	if command == "help" {
		if len(args) > 1 {
			return usageError(ja, "help [COMMAND]")
		}
		if len(args) == 1 {
			if topic, ok := helpTopic(args[0], ja); ok {
				_, err := fmt.Fprintln(out, topic)
				return err
			}
			return runWith(ctx, []string{"--locale", locale, args[0], "--help"}, out, stdin, client)
		}
		_, e := fmt.Fprintln(out, text(ja, helpEN, helpJA))
		return e
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") && command != "start" && command != "setup" && command != "share" && command != "connect" && command != "autosave" && command != "lan" && command != "service" {
		usage, ok := commandUsage[command]
		if !ok {
			return fmt.Errorf("%s: %s", text(ja, "Unknown command; use soba help", "不明なコマンドです。soba help を参照してください"), command)
		}
		fmt.Fprintln(out, text(ja, "Usage: soba ", "使い方: soba ")+usage)
		return nil
	}
	if dir == "" {
		var e error
		dir, e = core.DefaultDir()
		if e != nil {
			return e
		}
	}
	if command == "start" {
		startFlags := commandFlags("start", ja, out)
		offline := startFlags.Bool("offline", false, text(ja, "open local management without starting the saved network", "保存済みのネットワークを開始せず、ローカル管理画面を開く"))
		if e := parseFlags(startFlags, args, ja); e != nil {
			return e
		}
		if *dryRun {
			return errors.New(text(ja, "Use --dry-run with a configuration or action command; start launches the local agent", "--dry-run は設定・操作コマンドと使ってください。start は本体を起動します"))
		}
		lock, e := config.AcquireLock(dir)
		if e != nil {
			return e
		}
		defer func() { err = errors.Join(err, lock.Close()) }()
		app, e := core.Open(ctx, core.Options{Directory: dir, Version: version, SkipNetworkStart: *offline})
		if e != nil {
			return e
		}
		defer func() { err = errors.Join(err, app.Close()) }()
		files, e := assets.Assets()
		if e != nil {
			return e
		}
		url, code, e := app.StartWeb(files)
		if e != nil {
			return e
		}
		ipc, e := control.Serve(ctx, dir, app.IPC)
		if e != nil {
			return e
		}
		defer func() { err = errors.Join(err, ipc.Close()) }()
		fmt.Fprintln(out, text(ja, "Local UI:", "ローカル画面:"), url)
		if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
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
	call := func(raw string) error {
		var formatted json.RawMessage
		e := client(ctx, dir, raw, &formatted)
		if e != nil {
			return fmt.Errorf("%s: %w", text(ja, "Command failed. Check that soba is running and review the error", "操作に失敗しました。soba の起動状態とエラーを確認してください"), e)
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(formatted)
	}
	if command == "status" || command == "ui" || command == "stop" {
		if len(args) > 0 {
			return usageError(ja, command)
		}
		if *dryRun && command != "status" {
			return errors.New(text(ja, "--dry-run is not available for ui or stop", "ui と stop は --dry-run に対応していません"))
		}
		return call(command)
	}
	if command == "peers" {
		if len(args) != 0 {
			return usageError(ja, command)
		}
		return call("status")
	}
	request := func(name string, payload any) error {
		raw, e := json.Marshal(payload)
		if e != nil {
			return e
		}
		if *dryRun {
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			return encoder.Encode(map[string]any{"applied": false, "command": name, "payload": previewPayload(name, raw), "validation": "local-input-only"})
		}
		id := fmt.Sprintf("cli-%d", time.Now().UnixNano())
		request, e := json.Marshal(webui.Command{RequestID: id, Name: name, Payload: raw})
		if e != nil {
			return e
		}
		return call(string(request))
	}
	query := func(v any) error { return client(ctx, dir, "status", v) }
	queryAction := func(name string, payload any, v any) error {
		raw, e := json.Marshal(payload)
		if e != nil {
			return e
		}
		command, e := json.Marshal(webui.Command{RequestID: fmt.Sprintf("cli-read-%d", time.Now().UnixNano()), Name: name, Payload: raw})
		if e != nil {
			return e
		}
		return client(ctx, dir, string(command), v)
	}
	switch command {
	case "receive-dir", "autosave", "pause", "resume", "reconnect":
		return preferenceCommand(command, args, ja, out, query, request)
	case "service":
		return serviceCommand(args, ja, out, query, queryAction, request)
	case "setup":
		payload, e := setupPayload(args, ja, out)
		if e != nil {
			return e
		}
		return request("network.configure", payload)
	case "lan":
		return lanCommand(ctx, args, ja, out, stdin, request)
	case "login":
		if len(args) != 0 {
			return usageError(ja, "login")
		}
		return request("network.login", map[string]string{})
	case "trust", "revoke":
		if len(args) != 1 {
			return usageError(ja, "trust|revoke PEER_ID")
		}
		return request("peer.trust", map[string]any{"peerId": args[0], "trusted": command == "trust"})
	case "message":
		if len(args) != 2 {
			return usageError(ja, "message PEER_ID TEXT")
		}
		return request("message.send", map[string]string{"peerId": args[0], "text": args[1]})
	case "send":
		if len(args) < 2 {
			return usageError(ja, "send PEER_ID PATH...")
		}
		paths := make([]string, len(args)-1)
		for i, path := range args[1:] {
			absolute, e := filepath.Abs(path)
			if e != nil {
				return e
			}
			paths[i] = absolute
		}
		return request("transfer.send", map[string]any{"peerId": args[0], "paths": paths})
	case "accept":
		if len(args) != 2 {
			return usageError(ja, "accept TRANSFER_ID DIRECTORY")
		}
		destination, e := filepath.Abs(args[1])
		if e != nil {
			return e
		}
		return request("transfer.accept", map[string]string{"transferId": args[0], "destination": destination})
	case "cancel", "retry", "forget":
		if len(args) != 1 {
			return usageError(ja, "cancel|retry|forget TRANSFER_ID")
		}
		return request("transfer."+command, map[string]string{"transferId": args[0]})
	case "share", "connect":
		payload, e := servicePayload(command, args, ja, out)
		if e != nil {
			return e
		}
		if !hasFlag(args, "name") {
			name, e := unusedServiceName(payload["name"].(string), query)
			if e != nil {
				return fmt.Errorf("%s: %w", text(ja, "Could not choose an unused name; start soba or supply an explicit --name", "未使用の名前を選べません。soba を起動するか --name を明示してください"), e)
			}
			payload["name"] = name
		}
		return request("service."+command, payload)
	case "stop-service":
		if len(args) != 1 {
			return usageError(ja, "stop-service SERVICE_ID")
		}
		return request("service.stop", map[string]string{"id": args[0]})
	case "command":
		payload, e := commandPayload(ctx, args, stdin, ja)
		if e != nil {
			return e
		}
		return request(args[0], payload)
	default:
		return fmt.Errorf("%s: %s", text(ja, "Unknown command; use soba help", "不明なコマンドです。soba help を参照してください"), command)
	}
}

var commandUsage = map[string]string{
	"start": "start [--offline]", "status": "status", "peers": "peers", "ui": "ui", "stop": "stop",
	"receive-dir": "receive-dir [DIRECTORY | --clear]", "autosave": "autosave PEER_ID --on [--directory DIR] | autosave PEER_ID --off",
	"pause": "pause PEER_ID", "resume": "resume PEER_ID", "reconnect": "reconnect PEER_ID",
	"login": "login", "trust": "trust PEER_ID", "revoke": "revoke PEER_ID",
	"message": "message PEER_ID TEXT", "send": "send PEER_ID PATH...",
	"accept": "accept TRANSFER_ID DIRECTORY", "cancel": "cancel TRANSFER_ID",
	"retry": "retry TRANSFER_ID", "forget": "forget TRANSFER_ID",
	"stop-service": "stop-service SERVICE_ID", "command": "command NAME JSON_PAYLOAD | command NAME --json-file PATH | command NAME --stdin",
}

func commandPayload(ctx context.Context, args []string, stdin io.Reader, ja bool) (json.RawMessage, error) {
	if len(args) < 2 {
		return nil, usageError(ja, commandUsage["command"])
	}
	limit := int64(48 << 10)
	if args[0] == "message.send" {
		// Preserve the decoded text policy even when valid JSON escaping expands
		// it beyond the smaller private-invitation input allowance.
		limit = int64(messageframe.CommandBytes)
	}
	var reader io.Reader
	var owned io.Closer
	switch {
	case len(args) == 2 && args[1] == "--stdin":
		if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			return nil, errors.New(text(ja, "Use a pipe, --json-file, or the local Web UI for private payloads", "機密の入力にはパイプ、--json-file、またはローカル画面を使ってください"))
		}
		reader = stdin
	case len(args) == 3 && args[1] == "--json-file":
		info, e := os.Lstat(args[2])
		if e != nil || !info.Mode().IsRegular() || info.Size() > limit {
			return nil, fmt.Errorf(text(ja, "JSON input must be a regular file of at most %d bytes", "JSON入力は%dバイト以下の通常ファイルを指定してください"), limit)
		}
		f, e := os.Open(args[2])
		if e != nil {
			return nil, errors.New(text(ja, "Could not open the JSON input file", "JSON入力ファイルを開けませんでした"))
		}
		opened, e := f.Stat()
		if e != nil || !os.SameFile(info, opened) {
			f.Close()
			return nil, errors.New(text(ja, "JSON input file changed while opening", "JSON入力ファイルが読込み中に変わりました"))
		}
		reader = f
		owned = f
	case len(args) == 2 && !strings.HasPrefix(args[1], "--"):
		reader = strings.NewReader(args[1])
	default:
		return nil, usageError(ja, commandUsage["command"])
	}
	if owned != nil {
		defer owned.Close()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	stop := func() bool { return false }
	if closer, ok := reader.(io.Closer); ok {
		stop = context.AfterFunc(ctx, func() { _ = closer.Close() })
	}
	defer stop()
	data, e := io.ReadAll(io.LimitReader(reader, limit+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if e != nil {
		return nil, errors.New(text(ja, "Could not read JSON input", "JSON入力を読み込めませんでした"))
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf(text(ja, "JSON payload exceeds the %d-byte input limit", "JSON入力が上限の%dバイトを超えています"), limit)
	}
	if !json.Valid(data) {
		return nil, errors.New(text(ja, "Invalid JSON payload", "JSONの形式が正しくありません"))
	}
	return json.RawMessage(data), nil
}

func usageError(ja bool, usage string) error {
	return errors.New(text(ja, "Usage: soba ", "使い方: soba ") + usage)
}
func commandFlags(command string, ja bool, out io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(out)
	f.Usage = func() {
		fmt.Fprintln(out, text(ja, "Usage: soba ", "使い方: soba ")+command+" [OPTIONS]")
		if !ja {
			f.PrintDefaults()
			return
		}
		var defaults bytes.Buffer
		f.SetOutput(&defaults)
		f.PrintDefaults()
		f.SetOutput(out)
		fmt.Fprint(out, strings.ReplaceAll(defaults.String(), "(default ", "(既定 "))
	}
	return f
}
func parseFlags(f *flag.FlagSet, args []string, ja bool) error {
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return fmt.Errorf("%s: %w", text(ja, "Invalid option", "指定が正しくありません"), err)
	}
	if f.NArg() != 0 {
		return usageError(ja, f.Name()+" [OPTIONS]")
	}
	return nil
}

const helpEN = `sobalink — nearby devices, one connection

  soba                         Start the local Web UI and agent
  soba start --offline         Open settings without starting the saved network
  soba ui                      Get the local URL and a fresh one-time code
  soba status                  Show machine-readable state
  soba setup --network tailnet Activate the existing Tailscale connection
  soba login                   Request an official interactive sign-in link
  soba trust PEER_ID            Allow messages and batch offers from this identity
  soba message PEER_ID TEXT     Send text explicitly
  soba send PEER_ID PATH...     Offer files or folders as one batch
  soba accept ID DIRECTORY      Accept an incoming batch into this directory
  soba receive-dir DIRECTORY   Save the preferred receive folder
  soba autosave --help          Explicit per-peer automatic saving
  soba pause|resume PEER_ID     Pause/resume messages and file transfers
  soba reconnect PEER_ID        Refresh this peer's reachability and services
  soba lan --help               Public identity and private pairing invitations\n  soba service --help           Inspect, copy and restart saved service settings
  soba share --help             Scoped TCP/UDP service sharing
  soba connect --help           Local entry ports for a selected peer
  soba stop                    Stop the agent and revoke active shares

For guided setup and picking peers, open the local URL from soba ui.
Use soba help examples for short workflows; soba help upgrade for safe updates.
Global options: --state-dir DIR --locale auto|ja|en --dry-run --json-errors (before command)
--json-errors writes {code,error} to stderr on failure; success JSON is unchanged.
--dry-run previews action JSON and validates local inputs without applying it.
Command results stay JSON in every locale; human guidance stays in help/errors.
Advanced: command NAME JSON_PAYLOAD, revoke PEER_ID, stop-service ID,
cancel ID, retry ID (whole unfinished files), forget ID (history only)
For private invitations: command NAME --json-file PATH or --stdin (pipe only)

The Web UI and CLI use the same permission checks. The target application
must already be running; ordinary Tailscale service targets need no sobalink.
Files are never automatically opened or executed. Saving is receiver-approved.`
const helpJA = `sobalink — 離れた端末を、そばに

  soba                         ローカル画面と本体を起動
  soba start --offline         保存済みネットワークを開始せず設定画面を開く
  soba ui                      画面URLと新しい一回用コードを表示
  soba status                  状態を機械向けJSONで表示
  soba setup --network tailnet 既存のTailscale接続を有効化
  soba login                   公式の対話型ログインを開始
  soba trust PEER_ID            この端末からのメッセージ・転送申込みを許可
  soba message PEER_ID TEXT     文字を明示的に送信
  soba send PEER_ID PATH...     ファイル・フォルダーを一括で送信
  soba accept ID DIRECTORY      指定フォルダーへ一括受信を承認
  soba receive-dir DIRECTORY   既定の受信フォルダーを保存
  soba autosave --help          相手を指定した自動保存
  soba pause|resume PEER_ID     メッセージ・ファイル転送を一時停止／再開
  soba reconnect PEER_ID        相手の到達状態と共有一覧を再確認
  soba lan --help               公開IDと機密のペアリング招待\n  soba service --help           保存済み設定の確認・コピー・再開始
  soba share --help             範囲を指定したTCP/UDP共有
  soba connect --help           相手へつなぐローカル入口
  soba stop                    本体を停止して稼働中の共有を取り消す

案内付きの設定や相手の選択には、soba ui のローカルURLを開いてください。
短い操作例は soba help examples、更新手順は soba help upgrade で表示します。
共通指定: --state-dir DIR --locale auto|ja|en --dry-run --json-errors（コマンドより前）
--json-errors は失敗時に {code,error} のJSONを標準エラーへ出します。成功時のJSONは同じです。
--dry-run はローカル入力を検証し、変更を適用せず操作JSONを表示します。
操作結果のJSONは言語で変わりません。案内やエラーは選択した言語を使います。
詳細操作: command NAME JSON_PAYLOAD、revoke PEER_ID、stop-service ID、
cancel ID、retry ID（未完了ファイルを先頭から）、forget ID（履歴のみ）
機密の招待入力: command NAME --json-file PATH または --stdin（パイプ入力）

画面とCLIは同じ許可判定を使います。接続先では対象アプリの起動が必要です。
通常のTailscale接続先ではsobalinkは不要です。
受信ファイルを自動で開いたり実行したりしません。保存は受信側が承認します。`
