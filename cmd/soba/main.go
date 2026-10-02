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

	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/control"
	"github.com/webkaz-labs/tsnet-bridge/internal/core"
	"github.com/webkaz-labs/tsnet-bridge/internal/webui"
	assets "github.com/webkaz-labs/tsnet-bridge/web"
	"golang.org/x/term"
)

var version = "0.0.0-dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := run(ctx, os.Args[1:], os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, "soba:", e)
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

func run(ctx context.Context, args []string, out io.Writer) (err error) {
	// Help is a successful, side-effect-free action at every command level.
	defer func() {
		if errors.Is(err, flag.ErrHelp) {
			err = nil
		}
	}()
	var dir string
	locale := "auto"
	global := flag.NewFlagSet("soba", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	global.StringVar(&dir, "state-dir", "", "private state directory")
	global.StringVar(&locale, "locale", locale, "auto, ja or en")
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
			return run(ctx, []string{"--locale", locale, args[0], "--help"}, out)
		}
		_, e := fmt.Fprintln(out, text(ja, helpEN, helpJA))
		return e
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") && command != "setup" && command != "share" && command != "connect" {
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
		if len(args) > 0 {
			return usageError(ja, "[--state-dir DIR] start")
		}
		lock, e := config.AcquireLock(dir)
		if e != nil {
			return e
		}
		defer func() { err = errors.Join(err, lock.Close()) }()
		app, e := core.Open(ctx, core.Options{Directory: dir, Version: version})
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
		e := control.Call(ctx, dir, raw, &formatted)
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
		id := fmt.Sprintf("cli-%d", time.Now().UnixNano())
		request, e := json.Marshal(webui.Command{RequestID: id, Name: name, Payload: raw})
		if e != nil {
			return e
		}
		return call(string(request))
	}
	switch command {
	case "setup":
		f := commandFlags(command, ja, out)
		network := f.String("network", "tailnet", text(ja, "tailnet, lan or none", "tailnet、lan、none"))
		hostname := f.String("name", "", text(ja, "node name", "端末名"))
		if e := parseFlags(f, args, ja); e != nil {
			return e
		}
		return request("network.configure", map[string]string{"mode": *network, "hostname": *hostname})
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
		f := commandFlags(command, ja, out)
		name := f.String("name", "", text(ja, "service name", "共有・接続の名前"))
		network := f.String("network", "tcp", text(ja, "tcp or udp", "tcp または udp"))
		ports := f.String("ports", "", text(ja, "port list or inclusive ranges", "ポートの一覧または範囲"))
		exclude := f.String("exclude", "", text(ja, "excluded ports", "除外するポート"))
		var peer, peers string
		var local int
		var discover bool
		if command == "share" {
			f.StringVar(&peers, "peers", "", text(ja, "comma-separated allowed peer IDs", "許可する相手のID（カンマ区切り）"))
			f.BoolVar(&discover, "discoverable", false, text(ja, "advertise minimal metadata to allowed peers", "許可した相手に最小限の共有情報を表示"))
		} else {
			f.StringVar(&peer, "peer", "", text(ja, "current peer ID", "現在の相手のID"))
			f.IntVar(&local, "local-port", 0, text(ja, "local starting port; default same port", "ローカル入口の先頭ポート（既定は同じ番号）"))
		}
		ttl := f.Duration("ttl", time.Hour, text(ja, "permission lifetime, maximum 24h", "許可の有効期間（最大24時間）"))
		if e := parseFlags(f, args, ja); e != nil {
			return e
		}
		if *name == "" || *ports == "" {
			return errors.New(text(ja, "--name and --ports are required", "--name と --ports を指定してください"))
		}
		if *ttl < time.Second || *ttl > 24*time.Hour || *ttl%time.Second != 0 {
			return errors.New(text(ja, "--ttl must be a whole number of seconds from 1s to 24h", "--ttl は1秒〜24時間の整数秒で指定してください"))
		}
		if *network != "tcp" && *network != "udp" {
			return errors.New(text(ja, "--network must be tcp or udp", "--network は tcp または udp を指定してください"))
		}
		if local < 0 || local > 65535 {
			return errors.New(text(ja, "--local-port must be 0..65535", "--local-port は 0〜65535 で指定してください"))
		}
		payload := map[string]any{"name": *name, "network": *network, "ports": *ports, "excludePorts": *exclude, "localPort": local, "ttlSeconds": int(ttl.Seconds()), "purpose": "custom", "discoverable": discover}
		if command == "share" {
			if peers == "" {
				return errors.New(text(ja, "--peers is required", "--peers を指定してください"))
			}
			ids := strings.Split(peers, ",")
			for i := range ids {
				ids[i] = strings.TrimSpace(ids[i])
				if ids[i] == "" {
					return errors.New(text(ja, "--peers contains an empty peer ID", "--peers に空のIDが含まれています"))
				}
			}
			payload["peerIds"] = ids
		} else {
			if peer == "" {
				return errors.New(text(ja, "--peer is required", "--peer を指定してください"))
			}
			payload["peerId"] = peer
		}
		return request("service."+command, payload)
	case "stop-service":
		if len(args) != 1 {
			return usageError(ja, "stop-service SERVICE_ID")
		}
		return request("service.stop", map[string]string{"id": args[0]})
	case "command":
		if len(args) != 2 {
			return usageError(ja, "command NAME JSON_PAYLOAD")
		}
		payload := json.RawMessage(args[1])
		if !json.Valid(payload) {
			return errors.New(text(ja, "Invalid JSON payload", "JSONの形式が正しくありません"))
		}
		return request(args[0], payload)
	default:
		return fmt.Errorf("%s: %s", text(ja, "Unknown command; use soba help", "不明なコマンドです。soba help を参照してください"), command)
	}
}

var commandUsage = map[string]string{
	"start": "start", "status": "status", "peers": "peers", "ui": "ui", "stop": "stop",
	"login": "login", "trust": "trust PEER_ID", "revoke": "revoke PEER_ID",
	"message": "message PEER_ID TEXT", "send": "send PEER_ID PATH...",
	"accept": "accept TRANSFER_ID DIRECTORY", "cancel": "cancel TRANSFER_ID",
	"retry": "retry TRANSFER_ID", "forget": "forget TRANSFER_ID",
	"stop-service": "stop-service SERVICE_ID", "command": "command NAME JSON_PAYLOAD",
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
  soba ui                      Get the local URL and a fresh one-time code
  soba status                  Show machine-readable state
  soba setup --network tailnet Activate the existing Tailscale connection
  soba login                   Request an official interactive sign-in link
  soba trust PEER_ID            Allow messages and batch offers from this identity
  soba message PEER_ID TEXT     Send text explicitly
  soba send PEER_ID PATH...     Offer files or folders as one batch
  soba accept ID DIRECTORY      Accept an incoming batch into this directory
  soba share --help             Scoped TCP/UDP service sharing
  soba connect --help           Local entry ports for a selected peer
  soba stop                    Stop the agent and revoke active shares

Global options: --state-dir DIR --locale auto|ja|en (before the command)
Advanced: command NAME JSON_PAYLOAD, revoke PEER_ID, stop-service ID,
cancel ID, retry ID (whole unfinished files), forget ID (history only)

The Web UI and CLI use the same permission checks. The target application
must already be running; ordinary Tailscale service targets need no sobalink.
Files are never automatically opened or executed. Saving is receiver-approved.`
const helpJA = `sobalink — 離れた端末を、そばに

  soba                         ローカル画面と本体を起動
  soba ui                      画面URLと新しい一回用コードを表示
  soba status                  状態を機械向けJSONで表示
  soba setup --network tailnet 既存のTailscale接続を有効化
  soba login                   公式の対話型ログインを開始
  soba trust PEER_ID            この端末からのメッセージ・転送申込みを許可
  soba message PEER_ID TEXT     文字を明示的に送信
  soba send PEER_ID PATH...     ファイル・フォルダーを一括で送信
  soba accept ID DIRECTORY      指定フォルダーへ一括受信を承認
  soba share --help             範囲を指定したTCP/UDP共有
  soba connect --help           相手へつなぐローカル入口
  soba stop                    本体を停止して稼働中の共有を取り消す

共通指定: --state-dir DIR --locale auto|ja|en（コマンドより前）
詳細操作: command NAME JSON_PAYLOAD、revoke PEER_ID、stop-service ID、
cancel ID、retry ID（未完了ファイルを先頭から）、forget ID（履歴のみ）

画面とCLIは同じ許可判定を使います。接続先では対象アプリの起動が必要です。
通常のTailscale接続先ではsobalinkは不要です。
受信ファイルを自動で開いたり実行したりしません。保存は受信側が承認します。`
