package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/directlan"
)

func directLANCLI(ctx context.Context, args []string, ja bool, out io.Writer, stdin io.Reader, dryRun bool, query commandQuery, request actionRequest) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		_, err := fmt.Fprintln(out, text(ja, directLANHelpEN, directLANHelpJA))
		return err
	}
	operation, rest := args[0], args[1:]
	switch operation {
	case "configure":
		return directLANConfigureCLI(rest, ja, out, request)
	case "status", "identity":
		structured := len(rest) == 1 && rest[0] == "--json"
		if structured {
			rest = nil
		}
		if len(rest) != 0 {
			return usageError(ja, "direct-lan "+operation+" [--json]")
		}
		if operation == "status" && !structured && !dryRun {
			var state humanDirectLANStatus
			if err := query("direct-lan.status", map[string]any{}, &state); err != nil {
				return err
			}
			writeHumanDirectLANStatus(out, ja, &state)
			return nil
		}
		return request("direct-lan."+operation, map[string]any{})
	case "invite":
		f := commandFlags("direct-lan invite", ja, out)
		recipient := f.String("to", "", text(ja, "recipient's exact public direct LAN identity", "相手の正確なdirect LAN公開ID"))
		name := f.String("name", "device", text(ja, "recipient display name", "相手の表示名"))
		ttl := f.Duration("ttl", 5*time.Minute, text(ja, "invitation lifetime, 1s to 10m", "招待の有効期間（1秒〜10分）"))
		if err := parseFlags(f, rest, ja); err != nil {
			return err
		}
		key, err := hex.DecodeString(*recipient)
		if err != nil || len(key) != 32 || hex.EncodeToString(key) != *recipient || *recipient == strings.Repeat("0", 64) {
			return errors.New(text(ja, "--to requires the recipient's exact 64-character public identity", "--to に相手の正確な64文字の公開IDを指定してください"))
		}
		if strings.TrimSpace(*name) != *name || *name == "" || len(*name) > 128 || (!utf8.ValidString(*name) || strings.IndexFunc(*name, unicode.IsControl) >= 0) {
			return errors.New(text(ja, "--name requires a nonempty display name of at most 128 bytes", "--name に空白だけでない128バイト以下の表示名を指定してください"))
		}
		if *ttl < time.Second || *ttl > 10*time.Minute || *ttl%time.Second != 0 {
			return errors.New(text(ja, "--ttl must be whole seconds from 1s to 10m", "--ttl は1秒〜10分の整数秒で指定してください"))
		}
		return request("direct-lan.invite", map[string]any{"recipientPublicKey": *recipient, "name": *name, "ttlSeconds": int(ttl.Seconds())})
	case "inspect", "join", "cancel":
		if len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h") {
			return usageHelp(out, ja, "direct-lan "+operation+" --json-file INVITATION_FILE | --stdin")
		}
		if len(rest) == 0 || (rest[0] != "--json-file" && rest[0] != "--stdin") {
			return errors.New(text(ja, "Private invitations require --json-file PATH or --stdin; do not paste them as command arguments", "機密の招待には --json-file PATH または --stdin を使ってください。コマンド引数に貼り付けないでください"))
		}
		raw, err := commandPayload(ctx, append([]string{"direct-lan." + operation}, rest...), stdin, ja)
		if err != nil {
			return err
		}
		var envelope struct {
			Invitation string `json:"invitation"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.Invitation == "" {
			return errors.New(text(ja, "Use the JSON invitation response saved by direct-lan invite", "direct-lan invite のJSON出力を保存した招待ファイルを使ってください"))
		}
		if _, err := directlan.ParseInvitation(envelope.Invitation); err != nil {
			return errors.New(text(ja, "Invitation is invalid or expired; request a new one", "招待が無効または期限切れです。新しい招待を依頼してください"))
		}
		return request("direct-lan."+operation, map[string]string{"invitation": envelope.Invitation})
	case "revoke":
		if len(rest) != 1 {
			return usageError(ja, "direct-lan revoke PEER_ID")
		}
		b, err := hex.DecodeString(rest[0])
		if err != nil || len(b) != 32 || hex.EncodeToString(b) != rest[0] || rest[0] == strings.Repeat("0", 64) {
			return errors.New(text(ja, "Use the exact paired public identity", "ペアリング済みの正確な公開IDを指定してください"))
		}
		return request("direct-lan.revoke", map[string]string{"peerId": rest[0]})
	default:
		return errors.New(text(ja, "Unknown direct LAN action; use soba direct-lan --help", "不明なdirect LAN操作です。soba direct-lan --help を参照してください"))
	}
}
func directLANConfigureCLI(args []string, ja bool, out io.Writer, request actionRequest) error {
	f := commandFlags("direct-lan configure", ja, out)
	listen := f.String("listen", "", text(ja, "exact private or loopback IP:port for the encrypted tunnel", "暗号化トンネルの正確なプライベート・ループバックIP:ポート"))
	name := f.String("name", "", text(ja, "node name; omit to keep current name", "端末名（省略時は現在の名前を維持）"))
	var prefixes policyPrefixes
	f.Var(&prefixes, "prefix", text(ja, "explicit allowed private/loopback CIDR, repeatable", "許可するプライベート・ループバックCIDR（複数可）"))
	if err := parseFlags(f, args, ja); err != nil {
		return err
	}
	selection := core.DirectLANSelection{Listen: *listen, Prefixes: prefixes}
	if err := core.ValidateDirectLANSelection(selection); err != nil {
		return errors.New(text(ja, "Choose an exact private or loopback IP:port, a high port excluding 54543..54545, and canonical --prefix ranges containing it", "正確なプライベート・ループバックIP:ポート、54543〜54545を除く高位ポート、そのIPを含む正規表記の --prefix 範囲を選んでください"))
	}
	return request("network.configure", map[string]any{"mode": "direct-lan", "hostname": *name, "directLAN": selection})
}

const directLANHelpEN = `Direct LAN: encrypted peer applications through a userspace WireGuard/netstack tunnel.

  soba direct-lan configure --listen IP:PORT --prefix CIDR [--prefix CIDR]
  soba direct-lan status [--json]
  soba direct-lan identity [--json]
  soba direct-lan invite --to PUBLIC_KEY [--name device] [--ttl 5m]
  soba direct-lan inspect --json-file INVITATION_FILE
  soba direct-lan join --json-file INVITATION_FILE
  soba direct-lan cancel --json-file INVITATION_FILE
  soba direct-lan revoke PEER_ID

Run soba start --offline first. Configure explicitly generates and saves this mode's
private identity. Repeat configuration preserves it. Use exact numeric private
or loopback addresses and explicitly chosen prefixes; no relay, public DNS,
STUN, router changes or system-wide tunnel is implied. JSON output is stable.
Invitations are private, short-lived and recipient-bound; keep them out of
shell history and logs. --stdin accepts the same invitation JSON by pipe.
Pairing does not grant messages, file reception or service access. Approve
application trust and each scoped service separately. Revoke closes active
peer flows; changed endpoints/prefixes require stop, offline restart and re-pairing.`
const directLANHelpJA = `Direct LAN: ユーザー空間のWireGuard/netstackトンネルでアプリ通信を暗号化します。

  soba direct-lan configure --listen IP:PORT --prefix CIDR [--prefix CIDR]
  soba direct-lan status [--json]
  soba direct-lan identity [--json]
  soba direct-lan invite --to PUBLIC_KEY [--name device] [--ttl 5m]
  soba direct-lan inspect --json-file INVITATION_FILE
  soba direct-lan join --json-file INVITATION_FILE
  soba direct-lan cancel --json-file INVITATION_FILE
  soba direct-lan revoke PEER_ID

先に soba start --offline で起動してください。configure の明示操作でこのモードの
秘密IDを生成して保存します。同じ設定を繰り返してもIDは変わりません。
数値のプライベート・ループバックアドレスと許可範囲を明示してください。
リレー・公開DNS・STUN・ルーター変更・OS全体のトンネルは暗黙に利用しません。
JSON出力は全言語で同じです。招待は宛先限定・短時間・一回用の機密情報です。
履歴やログに残さず交換してください。--stdin で招待JSONのパイプ入力もできます。
ペアリングだけではメッセージ・受信・サービス接続を許可しません。アプリの信頼と
サービスごとの範囲は別途承認します。revoke は通信中の接続も終了します。
接続先・許可範囲の変更は停止・オフライン起動・ペアリングのやり直しが必要です。`

// A saved endpoint is distinct from an active listener and application success.
type humanDirectLANStatus struct {
	Configured       bool     `json:"configured"`
	ListenerReady    bool     `json:"listenerReady"`
	RecoveryRequired bool     `json:"recoveryRequired"`
	PublicKey        string   `json:"publicKey"`
	Endpoint         string   `json:"endpoint"`
	Prefixes         []string `json:"prefixes"`
}

func writeHumanDirectLANStatus(out io.Writer, ja bool, state *humanDirectLANStatus) {
	if state == nil {
		return
	}
	saved := text(ja, "not configured", "未設定")
	if state.Configured {
		saved = text(ja, "configured", "設定済み")
	}
	ready := text(ja, "stopped", "停止中")
	if state.ListenerReady {
		ready = text(ja, "ready", "準備完了")
	}
	fmt.Fprintf(out, "%s: %s; %s: %s\n", text(ja, "Direct LAN saved configuration", "direct LAN保存状態"), saved, text(ja, "encrypted tunnel listener", "暗号化トンネル待受"), ready)
	if state.Endpoint != "" {
		fmt.Fprintf(out, "%s: %s\n", text(ja, "Tunnel endpoint", "トンネル接続先"), displayText(state.Endpoint))
	}
	if len(state.Prefixes) != 0 {
		fmt.Fprintf(out, "%s: %s\n", text(ja, "Allowed prefixes", "許可範囲"), displayText(strings.Join(state.Prefixes, ", ")))
	}
	if state.PublicKey != "" {
		fmt.Fprintf(out, "%s: %s\n", text(ja, "Public identity", "公開ID"), displayText(state.PublicKey))
	}
	if state.RecoveryRequired {
		fmt.Fprintln(out, text(ja, "Private-state recovery required. Stop and inspect saved approvals before reopening.", "秘密設定の復旧確認が必要です。停止し、保存済みの許可を確認してから再起動してください。"))
	}
}
