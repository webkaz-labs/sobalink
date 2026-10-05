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

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/messageframe"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"golang.org/x/term"
)

var version = "0.0.0-dev"

func main() {
	os.Exit(mainExitCode())
}

func mainExitCode() int {
	out, errorOut, closeOutput, err := backgroundCommandOutput(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		writeCommandError(os.Stderr, err)
		return 1
	}
	defer closeOutput()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := run(ctx, os.Args[1:], out); e != nil {
		writeCommandError(errorOut, e)
		return 1
	}
	return 0
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
	return runWith(ctx, args, out, os.Stdin, controlCall)
}

func runWith(ctx context.Context, args []string, out io.Writer, stdin io.Reader, client controlCaller) (err error) {
	var jsonErrors bool
	locale := "auto"
	// Help is a successful, side-effect-free action at every command level.
	defer func() {
		if errors.Is(err, flag.ErrHelp) {
			err = nil
		} else if err != nil && jsonErrors {
			err = &jsonCommandError{err}
		} else if err != nil {
			err = localizeRouteRecoveryError(japanese(locale), localizeDiskSpaceError(japanese(locale), err))
		}
	}()
	var dir string
	global := flag.NewFlagSet("soba", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	global.StringVar(&dir, "state-dir", "", "private state directory")
	global.StringVar(&locale, "locale", locale, "auto, ja or en")
	global.BoolVar(&jsonErrors, "json-errors", false, "write stable JSON errors to stderr")
	dryRun := global.Bool("dry-run", false, "preview the command without applying it")
	offlineDefinitions := global.Bool("offline", false, "edit saved definitions while the agent is stopped; no network")
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
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") && command != "startup" && command != "proxy" && command != "doctor" && command != "login" && command != "start" && command != "run" && command != "autostart" && command != "setup" && command != "share" && command != "connect" && command != "autosave" && command != "lan" && command != "service" && command != "profile" && command != "group" && command != "services" && command != "task" && command != "wait-ready" && command != "stop-shares" && command != "rules" && command != "settings" && command != "discover" && command != "init" && command != "rustdesk" {
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
	if command == "init" {
		return initializeCommand(ctx, args, dir, ja, *dryRun, out)
	}
	if *offlineDefinitions {
		if !offlineDefinitionCLIAllowed(command, args) {
			return errors.New(text(ja, "--offline supports rules/settings, service save/show/delete, group list/save and profile export/import; use the running agent for other actions", "--offline は rules/settings、service save/show/delete、group list/save、profile export/import に対応しています。他の操作は稼働中の本体で実行してください"))
		}
		client = offlineDefinitionCall
	}
	if handled, err := clientHelperCLI(ctx, command, args, dir, ja, *dryRun, out, client); handled {
		return err
	}
	if handled, err := definitionCLI(ctx, command, args, dir, ja, *dryRun, out, client); handled {
		return err
	}
	if handled, err := workflowCommand(ctx, command, args, dir, ja, *dryRun, out, stdin, client); handled {
		return err
	}
	if command == "start" || command == "run" {
		return startCommand(ctx, command, dir, locale, args, ja, *dryRun, out, client)
	}
	if command == "autostart" {
		return autostartCommand(ctx, dir, args, ja, *dryRun, out, currentAutostartEnvironment, runAutostartManager)
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
	if command == "status" || command == "peers" || command == "stop" {
		if *dryRun && command == "stop" {
			return errors.New(text(ja, "--dry-run is not available for stop", "stop は --dry-run に対応していません"))
		}
		return snapshotCommand(ctx, command, args, dir, ja, out, client)
	}
	if command == "ui" {
		if len(args) > 0 {
			return usageError(ja, "ui")
		}
		if *dryRun {
			return errors.New(text(ja, "--dry-run is not available for ui", "ui は --dry-run に対応していません"))
		}
		return call("ui")
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
	case "receive":
		return receiveRecoveryCommand(args, dir, ja, *dryRun, out, queryAction)
	case "startup":
		return startupCommand(args, ja, out, request)
	case "proxy":
		if handled, err := savedProxyCLI(ctx, args, dir, ja, *dryRun, out, stdin, queryAction, request); handled {
			return err
		}
		return proxyCommand(ctx, args, dir, ja, out, stdin, request)
	case "doctor":
		return doctorCommand(args, ja, out, request)
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
		if len(args) > 0 && args[0] == "routes" {
			return lanRoutesCommand(ctx, args[1:], ja, *dryRun, out, stdin, queryAction, request)
		}
		return lanCommand(ctx, args, ja, out, stdin, request)
	case "login":
		if len(args) == 0 {
			return request("network.login", map[string]string{})
		}
		return loginCommand(ctx, args, ja, *dryRun, out, dir, client, request)
	case "logout":
		if len(args) != 0 {
			return usageError(ja, command)
		}
		return request("network."+command, map[string]string{})
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
	case "discover":
		return discoveryCommand(args, ja, out, request)
	case "rules", "settings":
		return savedRulesCommand(command, args, dir, ja, out, queryAction)
	case "share", "connect":
		if wantsGuidedService(args, stdin) {
			return guidedServiceCommand(ctx, command, args, dir, ja, *dryRun, out, stdin, query, queryAction, request)
		}
		payload, e := servicePayload(command, args, ja, out)
		if e != nil {
			return e
		}
		if err := resolveNamedPeerPayload(payload, ja, query); err != nil {
			return err
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
		resolved, err := resolveServiceReferences(args, ja, queryAction)
		if err != nil {
			return err
		}
		return request("service.stop", map[string]string{"id": resolved.IDs[0]})
	case "command":
		payload, e := commandPayload(ctx, args, stdin, ja, dir)
		if e != nil {
			return e
		}
		return request(args[0], payload)
	default:
		return fmt.Errorf("%s: %s", text(ja, "Unknown command; use soba help", "不明なコマンドです。soba help を参照してください"), command)
	}
}

var commandUsage = map[string]string{
	"profile": "profile export [--output FILE] | profile import FILE [--apply --review REVISION]",
	"start":   "start [--offline] [--background]", "run": "run [--offline]", "autostart": "autostart [enable|disable] [--startup saved|offline] [--json] [--apply --review TOKEN]", "status": "status [--json]", "peers": "peers [--json]", "ui": "ui", "stop": "stop [--json]",
	"receive":     "receive recovery confirm [--reviewed] [--json]",
	"receive-dir": "receive-dir [DIRECTORY | --clear]", "autosave": "autosave PEER_ID --on [--directory DIR] | autosave PEER_ID --off",
	"pause": "pause PEER_ID", "resume": "resume PEER_ID", "reconnect": "reconnect PEER_ID",
	"login": "login [--qr|--link|--browser] [--wait] [--refresh] [--timeout 5m]", "logout": "logout", "trust": "trust PEER_ID", "revoke": "revoke PEER_ID",
	"message": "message PEER_ID TEXT", "send": "send PEER_ID PATH...",
	"accept": "accept TRANSFER_ID DIRECTORY", "cancel": "cancel TRANSFER_ID",
	"retry": "retry TRANSFER_ID", "forget": "forget TRANSFER_ID",
	"rules": "rules [--json]", "settings": "settings [SERVICE_ID... | --service ID[,ID] | --group NAME] [--json]",
	"stop-service": "stop-service SERVICE_ID", "command": "command NAME JSON_PAYLOAD | command NAME --json-file PATH | command NAME --stdin",
}

func commandPayload(ctx context.Context, args []string, stdin io.Reader, ja bool, profileDir ...string) (json.RawMessage, error) {
	if len(args) < 2 {
		return nil, usageError(ja, commandUsage["command"])
	}
	if args[0] == "proxy.reveal" {
		return nil, errors.New(text(ja, "Use proxy reveal with --private-file; generic command output cannot reveal credentials", "認証情報の確認には proxy reveal --private-file を使ってください。汎用コマンドでは認証情報を出力できません"))
	}
	if (args[0] == "lan.routes.inspect" || args[0] == "lan.routes.apply") && args[1] != "--stdin" && args[1] != "--json-file" {
		return nil, errors.New(text(ja, "Private route updates require --json-file or --stdin", "機密の経路更新には --json-file または --stdin を使ってください"))
	}
	if privateProxyCommand(args[0]) {
		if err := validatePrivateProxyInput(args, ja); err != nil {
			return nil, err
		}
	}
	limit := int64(48 << 10)
	if args[0] == "message.send" {
		// Preserve the decoded text policy even when valid JSON escaping expands
		// it beyond the smaller private-invitation input allowance.
		limit = int64(messageframe.CommandBytes)
	}
	if len(profileDir) > 0 {
		var err error
		limit, err = core.ReadCommandInputBytes(profileDir[0], args[0])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", text(ja, "Could not read selected capacity settings", "選択中の容量設定を読み込めませんでした"), err)
		}
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
		if privateProxyCommand(args[0]) {
			if err := privateProxyFileCheck(f, ja); err != nil {
				f.Close()
				return nil, err
			}
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

const helpEN = `sobalink — Close, even from afar.

  soba                         Start the local Web UI and agent
  soba init                    Create inert local metadata without a network
  soba run                     Run in the foreground (Ctrl+C stops)
  soba start --background      Start a detached local application
  soba autostart --help        Review optional startup at user sign-in
  soba start --offline         Open settings without starting the saved network
  soba ui                      Get the local URL and a fresh one-time code
  soba status                  Show current state (--json for machines)
  soba setup --network tailnet Activate the existing Tailscale connection
  soba login                   Request an official interactive sign-in link
  soba logout                  Stop traffic, log out of Tailnet and exit
  soba trust PEER_ID            Allow messages and batch offers from this identity
  soba message PEER_ID TEXT     Send text explicitly
  soba send PEER_ID PATH...     Offer files or folders as one batch
  soba accept ID DIRECTORY      Accept an incoming batch into this directory
  soba receive recovery confirm [--reviewed]  Review old receive leftovers
  soba receive-dir DIRECTORY   Save the preferred receive folder
  soba autosave --help          Explicit per-peer automatic saving
  soba pause|resume PEER_ID     Pause/resume messages and file transfers
  soba reconnect PEER_ID        Refresh this peer's reachability and services
  soba lan --help               Public identity and private pairing invitations
  soba service --help           Save, inspect, delete and restart service settings
  soba profile --help           Export/import stopped service definitions
  soba rustdesk --help          Review and save RustDesk client settings
  soba group --help             Save and start service groups
  soba task --help              Run a command with owned temporary services
  soba stop-shares              Stop every share; keep the node running
  soba share --help             Scoped TCP/UDP service sharing
  soba connect --help           Guided or explicit local entries for a peer
  soba discover                Refresh authenticated advertised services
  soba stop                    Stop the agent and revoke active shares

Run soba connect or soba share in a terminal for guided selection and review.
Use --interactive to explicitly select the guided flow; --json never prompts.
soba --offline rules lists saved definitions with the agent stopped.
soba settings [SERVICE_ID] shows saved endpoints and application hints.
Use soba help examples for short workflows; soba help upgrade for safe updates.
Global options: --state-dir DIR --locale auto|ja|en --dry-run --json-errors (before command)
--offline edits saved service/group/profile definitions with the agent stopped.
--json-errors writes {code,error} to stderr on failure; success JSON is unchanged.
--dry-run previews action JSON and validates local inputs without applying it.
status, peers and service/group workflows default to human output; --json stays stable.
Configuration actions and --dry-run previews keep their existing JSON output.
Optional tools: soba proxy --help (scoped TCP proxy), soba doctor --help (transport checks)
Advanced: command NAME JSON_PAYLOAD, revoke PEER_ID, stop-service ID,
cancel ID, retry ID (whole unfinished files), forget ID (history and retained sending copies)
For private invitations: command NAME --json-file PATH or --stdin (pipe only)

The Web UI and CLI use the same permission checks. The target application
must already be running; ordinary Tailscale service targets need no sobalink.
Files are never automatically opened or executed. Saving is receiver-approved.`
const helpJA = `sobalink — 離れていても、すぐそばに。

  soba                         ローカル画面と本体を起動
  soba init                    ネットワークを開始せずローカル設定を作成
  soba run                     フォアグラウンドで実行（Ctrl+C で停止）
  soba start --background      本体をバックグラウンドで起動
  soba autostart --help        サインイン時の起動を確認して任意登録
  soba start --offline         保存済みネットワークを開始せず設定画面を開く
  soba ui                      画面URLと新しい一回用コードを表示
  soba status                  現在の状態を表示（機械向けは --json）
  soba setup --network tailnet 既存のTailscale接続を有効化
  soba login                   公式の対話型ログインを開始
  soba logout                  通信を停止し、Tailnet からログアウトして終了
  soba trust PEER_ID            この端末からのメッセージ・転送申込みを許可
  soba message PEER_ID TEXT     文字を明示的に送信
  soba send PEER_ID PATH...     ファイル・フォルダーを一括で送信
  soba accept ID DIRECTORY      指定フォルダーへ一括受信を承認
  soba receive recovery confirm [--reviewed]  旧受信の残骸を確認して再開
  soba receive-dir DIRECTORY   既定の受信フォルダーを保存
  soba autosave --help          相手を指定した自動保存
  soba pause|resume PEER_ID     メッセージ・ファイル転送を一時停止／再開
  soba reconnect PEER_ID        相手の到達状態と共有一覧を再確認
  soba lan --help               公開IDと機密のペアリング招待
  soba service --help           設定の保存・確認・削除・再開始
  soba profile --help           停止状態のサービス設定を書き出し・読込み
  soba rustdesk --help          RustDeskの接続設定を確認・保存
  soba group --help             サービスをまとめて保存・開始
  soba task --help              所有する一時サービスでコマンドを実行
  soba stop-shares              本体を維持してすべての共有を停止
  soba share --help             範囲を指定したTCP/UDP共有
  soba connect --help           案内付き・明示指定で相手へつなぐ入口
  soba discover                認証済みの共有情報を更新
  soba stop                    本体を停止して稼働中の共有を取り消す

端末で soba connect または soba share を実行すると、選択と確認を案内します。
--interactive は案内付き操作を明示します。--json は対話入力を求めません。
soba --offline rules は本体を停止したまま保存済み設定を一覧表示します。
soba settings [SERVICE_ID] は保存済み接続先とアプリの設定例を表示します。
短い操作例は soba help examples、更新手順は soba help upgrade で表示します。
共通指定: --state-dir DIR --locale auto|ja|en --dry-run --json-errors（コマンドより前）
--offline は本体を起動せず保存済みサービス・グループ・プロファイルを編集します。
--json-errors は失敗時に {code,error} のJSONを標準エラーへ出します。成功時のJSONは同じです。
--dry-run はローカル入力を検証し、変更を適用せず操作JSONを表示します。
status・peers・サービス/グループの操作は人向け表示が基本です。--json は全言語で同じです。
設定操作と --dry-run のプレビューは従来のJSON出力を維持します。
任意の詳細機能: soba proxy --help（範囲を限定したTCPプロキシ）、soba doctor --help（通信の確認）
詳細操作: command NAME JSON_PAYLOAD、revoke PEER_ID、stop-service ID、
cancel ID、retry ID（未完了ファイルを先頭から）、forget ID（履歴と送信用の一時コピー）
機密の招待入力: command NAME --json-file PATH または --stdin（パイプ入力）

画面とCLIは同じ許可判定を使います。接続先では対象アプリの起動が必要です。
通常のTailscale接続先ではsobalinkは不要です。
受信ファイルを自動で開いたり実行したりしません。保存は受信側が承認します。`
