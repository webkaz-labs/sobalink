package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func clientHelperCLI(ctx context.Context, command string, args []string, dir string, ja, dryRun bool, out io.Writer, client controlCaller) (bool, error) {
	if command != "rustdesk" {
		return false, nil
	}
	if len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(out, text(ja, rustDeskHelpEN, rustDeskHelpJA))
		return true, err
	}
	query := func(name string, payload, result any) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		command, err := json.Marshal(webui.Command{RequestID: fmt.Sprintf("cli-client-helper-%d", time.Now().UnixNano()), Name: name, Payload: raw})
		if err != nil {
			return err
		}
		if err = client(ctx, dir, string(command), result); err != nil {
			return localizeRustDeskError(ja, err)
		}
		return nil
	}
	if args[0] == "setup" {
		return true, rustDeskSetupCLI(args[1:], dir, ja, dryRun, out, query)
	}
	if args[0] == "settings" {
		f := commandFlags("rustdesk settings [--group NAME] [--json]", ja, out)
		group := f.String("group", "rustdesk", text(ja, "saved RustDesk group", "保存済み RustDesk グループ"))
		machine := f.Bool("json", false, text(ja, "stable machine-readable settings", "機械処理用の共通 JSON"))
		if err := parseFlags(f, args[1:], ja); err != nil {
			return true, err
		}
		if *group == "" {
			return true, errors.New(text(ja, "Choose a saved --group", "保存済みの --group を指定してください"))
		}
		var settings core.RustDeskClientSettings
		if err := query("rustdesk.settings", map[string]string{"group": *group}, &settings); err != nil {
			return true, err
		}
		if *machine {
			return true, writeRustDeskJSON(out, settings)
		}
		return true, writeRustDeskSettings(out, ja, settings)
	}
	return true, usageError(ja, "rustdesk setup|settings [options]")
}

func rustDeskSetupCLI(args []string, dir string, ja, dryRun bool, out io.Writer, query commandQuery) error {
	f := commandFlags("rustdesk setup --backend tailnet|lan --id-peer ID --key PUBLIC_KEY [options]", ja, out)
	var setup core.RustDeskSetup
	f.StringVar(&setup.Name, "name", "rustdesk", text(ja, "saved group name", "保存するグループ名"))
	f.StringVar(&setup.Backend, "backend", "", text(ja, "explicit tailnet or lan backend", "tailnet または lan を明示"))
	f.StringVar(&setup.IDPeerID, "id-peer", "", text(ja, "immutable ID-server peer ID", "ID サーバーの不変な相手 ID"))
	f.StringVar(&setup.RelayPeerID, "relay-peer", "", text(ja, "immutable relay peer ID; defaults to ID peer", "リレーの不変な相手 ID（省略時は ID サーバーと同じ）"))
	f.StringVar(&setup.PublicKey, "key", "", text(ja, "RustDesk public key (base64, 32 bytes)", "RustDesk 公開鍵（32 バイトの Base64）"))
	f.IntVar(&setup.IDPort, "id-port", 21116, text(ja, "remote ID port; NAT uses one less", "接続先の ID ポート（NAT はその1つ前）"))
	f.IntVar(&setup.RelayPort, "relay-port", 21117, text(ja, "remote relay port", "接続先のリレーポート"))
	f.IntVar(&setup.LocalIDPort, "local-id-port", 32116, text(ja, "local ID port; NAT uses one less", "ローカルの ID ポート（NAT はその1つ前）"))
	f.IntVar(&setup.LocalRelayPort, "local-relay-port", 32117, text(ja, "same local relay port on every participating endpoint", "参加するすべての端末で共通のローカルリレーポート"))
	f.StringVar(&setup.LoopbackHost, "loopback-host", "127.0.0.1", text(ja, "127.0.0.1 or ::1", "127.0.0.1 または ::1"))
	f.StringVar(&setup.Lifetime, "lifetime", "until-stopped", text(ja, "until-stopped or finite", "until-stopped または finite"))
	ttl := f.Duration("ttl", 0, text(ja, "finite lifetime in whole seconds; implies finite", "整数秒の有効期間。finite を指定"))
	apply := f.Bool("apply", false, text(ja, "save all four reviewed rules and public key; no start", "確認済みの4設定と公開鍵を保存（開始は別の操作）"))
	review := f.String("review", "", text(ja, "revision returned by the setup preview", "設定プレビューが返したリビジョン"))
	machine := f.Bool("json", false, text(ja, "stable machine-readable review", "機械処理用の共通 JSON"))
	if err := parseFlags(f, args, ja); err != nil {
		return err
	}
	explicit := map[string]bool{}
	f.Visit(func(item *flag.Flag) { explicit[item.Name] = true })
	if explicit["ttl"] && !explicit["lifetime"] {
		setup.Lifetime = "finite"
	}
	if !validServiceLifetime(setup.Lifetime, *ttl, "connect") {
		return errors.New(text(ja, "Use --lifetime until-stopped without --ttl, or a positive whole-second --ttl for finite lifetime", "--lifetime until-stopped は --ttl なしで、有限の期間は正の整数秒の --ttl を指定してください"))
	}
	setup.TTLSeconds = int(*ttl / time.Second)
	if setup.Name == "" || setup.Backend != "tailnet" && setup.Backend != "lan" || setup.IDPeerID == "" || setup.PublicKey == "" {
		return errors.New(text(ja, "Specify a name, --backend tailnet|lan, --id-peer ID and --key PUBLIC_KEY", "名前、--backend tailnet|lan、--id-peer ID、--key PUBLIC_KEY を指定してください"))
	}
	// Zero is meaningful only as an omitted Core default, not an explicitly
	// selected CLI port. Catch it before a preview could silently default it.
	if setup.IDPort < 1025 || setup.IDPort > 65535 || setup.LocalIDPort < 1025 || setup.LocalIDPort > 65535 || setup.RelayPort < 1024 || setup.RelayPort > 65535 || setup.LocalRelayPort < 1024 || setup.LocalRelayPort > 65535 {
		return errors.New(text(ja, "ID ports must be 1025..65535 and relay ports 1024..65535", "ID ポートは 1025〜65535、リレーポートは 1024〜65535 を指定してください"))
	}
	if *review != "" && !*apply {
		return errors.New(text(ja, "--review requires --apply", "--review には --apply が必要です"))
	}
	var result core.RustDeskSetupReview
	payload := core.RustDeskSetupRequest{Configuration: setup}
	if err := query("rustdesk.preview", payload, &result); err != nil {
		return err
	}
	if len(result.Revision) != 64 || len(result.Services) != 4 || result.Group.Name != setup.Name || result.ClientSettings.PublicKey != setup.PublicKey {
		return errors.New(text(ja, "RustDesk preview is incomplete; refresh before saving", "RustDesk のプレビューが不完全です。保存前に再読込みしてください"))
	}
	if *apply && !dryRun {
		if *review == "" || *review != result.Revision {
			return errors.New(text(ja, "Preview all four rules and key first, then repeat with --apply --review REVISION from that preview", "4つの設定と公開鍵を確認し、表示された --apply --review REVISION を付けて再実行してください"))
		}
		payload.ExpectedRevision = *review
		if err := query("rustdesk.save", payload, &result); err != nil {
			return err
		}
	}
	if *machine {
		return writeRustDeskJSON(out, result)
	}
	if result.Saved {
		fmt.Fprintln(out, text(ja, "RustDesk group saved. Starting it requires a separate group review.", "RustDesk グループを保存しました。開始には別のグループ確認操作が必要です。"))
	} else {
		fmt.Fprintln(out, text(ja, "RustDesk setup preview. Saving will store these four stopped rules and the public key atomically.", "RustDesk 設定のプレビューです。保存すると、停止中の4つの設定と公開鍵がまとめて保存されます。"))
	}
	fmt.Fprintln(out, text(ja, "Review revision:", "確認用リビジョン:"), result.Revision)
	if err := writeRustDeskSettings(out, ja, result.ClientSettings); err != nil {
		return err
	}
	if !result.Saved {
		fmt.Fprintln(out, text(ja, "To save, repeat the same setup options with --apply --review", "保存するには同じ設定に --apply --review を追加してください:"), result.Revision)
	}
	fmt.Fprintln(out, text(ja, "Next, review the exact group before starting (keep the selected profile):", "次に、開始前に対象グループを確認してください（同じ保存先を使用）:"))
	// For arbitrary profile paths, use an argv list instead of unsafe shell
	// quoting; simple paths can use a directly readable command example.
	_, err := fmt.Fprintln(out, rustDeskCommandExample(dir, "--dry-run", "group", "start", result.Group.Name))
	return err
}

func writeRustDeskJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeRustDeskSettings(out io.Writer, ja bool, settings core.RustDeskClientSettings) error {
	if _, err := fmt.Fprintf(out, "%s %s\n%s %s\n%s %s\n%s %s\n%s\n%s\n%s remote-ID%s\n", text(ja, "Group:", "グループ:"), settings.Group, text(ja, "ID server:", "ID サーバー:"), settings.IDServer, text(ja, "Relay server:", "リレーサーバー:"), settings.RelayServer, text(ja, "Public key:", "公開鍵:"), settings.PublicKey, text(ja, "Proxy: blank", "プロキシ: 空欄"), text(ja, "UDP: enabled", "UDP: 有効"), text(ja, "Connection:", "接続:"), settings.RemoteIDSuffix); err != nil {
		return err
	}
	for _, role := range settings.Roles {
		if _, err := fmt.Fprintf(out, "  %s (%s): %s -> %s:%d; %s=%s; %s=%s\n", role.Role, role.Network, role.LocalEndpoint, role.PeerID, role.RemotePort, text(ja, "lifetime", "有効期間"), role.Lifetime, text(ja, "state", "状態"), rustDeskStateText(ja, role.Status)); err != nil {
			return err
		}
		if role.TTLSeconds > 0 {
			if _, err := fmt.Fprintf(out, "    TTL: %d %s\n", role.TTLSeconds, text(ja, "seconds", "秒")); err != nil {
				return err
			}
		}
	}
	for _, notice := range settings.Notices {
		if _, err := fmt.Fprintln(out, text(ja, notice.Message, notice.MessageJA)); err != nil {
			return err
		}
	}
	return nil
}

func rustDeskStateText(ja bool, state string) string {
	switch state {
	case "planned":
		return text(ja, "planned / not saved", "予定・未保存")
	case "saved":
		return text(ja, "saved / stopped", "保存済み・停止中")
	case "active":
		return text(ja, "listener ready", "待受準備完了")
	case "reconnecting":
		return text(ja, "reconnecting", "再接続中")
	case "failed":
		return text(ja, "failed", "失敗")
	case "stopped":
		return text(ja, "stopped", "停止中")
	case "expired":
		return text(ja, "expired", "期限切れ")
	}
	return state
}

func localizeRustDeskError(ja bool, err error) error {
	if !ja {
		return err
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		messages := map[string]string{"rustdesk_key_invalid": "公開鍵には32バイトを Base64 化した値を指定してください", "rustdesk_name_invalid": "グループ名を短くしてください。-heartbeat を付けた名前も有効である必要があります", "rustdesk_peer_invalid": "ID サーバーとリレーの不変な相手 ID を指定してください", "rustdesk_port_invalid": "ID ポートは1025〜65535、リレーポートは1024〜65535です。NAT は ID ポートの1つ前を使います", "rustdesk_port_overlap": "ID・NAT・リレーの TCP ポートが重複しています。別のポートを選んでください", "rustdesk_group_conflict": "同じ名前に異なる保存済みグループがあります。別名を選ぶか現在の設定を明示的に確認してください", "rustdesk_revision_conflict": "設定が変更されています。4つの設定と公開鍵をもう一度確認してください", "rustdesk_group_inconsistent": "RustDesk の4つの役割が不整合です。個別編集の前に公開鍵・役割の関連付けを明示的に解除してください", "rustdesk_group_required": "保存済みの RustDesk グループを選んでください", "group_not_found": "保存済みグループが見つかりません。グループ名を確認してください", "service_capacity": "保存数の上限を確認してください。RustDesk には4つのサービス設定が必要です", "profile_capacity": "保存先の容量上限を確認してください", "service_name_conflict": "同じ名前のサービス設定があります。別のグループ名を選んでください"}
		if message := messages[coded.ErrorCode()]; message != "" {
			return fmt.Errorf("%s: %w", message, err)
		}
	}
	return fmt.Errorf("%s: %w", text(ja, "Review setup and retry", "設定内容を確認して再実行してください"), err)
}

const rustDeskHelpEN = `RustDesk connection helper

  soba rustdesk setup --backend tailnet --id-peer PEER_ID --key PUBLIC_KEY
  soba rustdesk setup --help
  soba rustdesk settings [--group rustdesk] [--json]

setup previews ID TCP, ID UDP, NAT TCP (ID port minus one) and relay TCP.
ID and relay may use distinct immutable peer IDs and custom remote/local ports.
Repeat identical options with --apply --review REVISION to save all four rules
and the public key atomically. No listener or RustDesk setting is changed.
Use global --offline before rustdesk when soba is stopped. Then review and
start the saved group explicitly. Keep the same local relay address/port on
every participating endpoint, proxy blank and UDP enabled; use remote-ID/r.
Application screen sharing/input control remains unverified.`

const rustDeskHelpJA = `RustDesk 接続ヘルパー

  soba rustdesk setup --backend tailnet --id-peer PEER_ID --key PUBLIC_KEY
  soba rustdesk setup --help
  soba rustdesk settings [--group rustdesk] [--json]

setup は ID の TCP・UDP、NAT の TCP（ID ポートの1つ前）、リレーの TCP を
確認できます。ID とリレーは異なる不変の相手 ID と個別の接続先・ローカル
ポートを指定できます。同じ設定に --apply --review REVISION を付けて再実行
すると、4つの設定と公開鍵をまとめて保存します。待受や RustDesk 設定は
変更しません。soba 停止中は rustdesk の前にグローバルの --offline を指定し、
その後に保存したグループを確認して明示的に開始してください。
すべての端末で共通のローカルリレーアドレス・ポートを使い、プロキシは
空欄、UDP は有効にし、remote-ID/r で接続します。画面表示・操作は未検証です。`

func rustDeskCommandExample(dir string, args ...string) string {
	argv := append([]string{"soba", "--state-dir", dir}, args...)
	for _, arg := range argv {
		if arg == "" {
			raw, _ := json.Marshal(argv)
			return "argv: " + string(raw)
		}
		for _, r := range arg {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/_:.-", r) {
				continue
			}
			raw, _ := json.Marshal(argv)
			return "argv: " + string(raw)
		}
	}
	return strings.Join(argv, " ")
}
