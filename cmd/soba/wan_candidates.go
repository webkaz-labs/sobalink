package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/routecat"
)

const wanCandidatesHelpEN = `WAN address candidates (explicit opt-in)

  soba lan wan show [--json]
  soba lan wan set --stun 192.0.2.10:3478 --advertise-ipv6 --probe-budget 4
  soba lan wan set --advertise-ipv6
  soba lan wan disable

The STUN address above is fictional. Choose exact numeric IP:port endpoints
you trust; repeat --stun for more. There is no built-in/default STUN server.
--probe-budget is an adjustable resource limit (default 4 destinations per
netcheck run); successive runs rotate through stored endpoints with bounded retries.
Metadata fits the configurable lanStateBytes budget and nonzero 16-bit discovery IDs.
An IPv6-only setting sends no STUN. STUN servers observe your source IP and port.
Stop soba and start --offline before changes. A certificate-pinned relay must
already be selected and the destination policy must be trusted-relay.
Allowed LAN destinations reject these settings. No router changes or elevated
permissions are required. Saving does not start the network. Direct WAN paths
depend on the actual NAT/firewall; the selected encrypted relay remains fallback.
Each device chooses its own settings. Real-device compatibility needs testing.`

const wanCandidatesHelpJA = `WANアドレス候補（明示的に選択）

  soba lan wan show [--json]
  soba lan wan set --stun 192.0.2.10:3478 --advertise-ipv6 --probe-budget 4
  soba lan wan set --advertise-ipv6
  soba lan wan disable

上のSTUNアドレスは架空の例です。信頼する相手の数値IP:portを明示してください。
--stun は繰り返し指定できます。既定のSTUNサーバーはありません。
--probe-budget は調整可能な資源上限です（既定は診断1回につき4接続先）。
保存済みの接続先を順に選び、各診断の再試行数を制限します。メタデータは調整可能な
lanStateBytes と、ゼロを除く16ビットの診断ID空間に収まる必要があります。
IPv6だけの設定ではSTUN通信を行いません。STUNサーバーには接続元IPとポートが伝わります。
変更前に soba を停止し、start --offline で起動してください。証明書を固定した
リレーを選択済みで、接続先ポリシーが trusted-relay である必要があります。
allowed-lan-destinations では使用できません。ルーター変更や管理者権限は不要です。
保存だけではネットワークを開始しません。WANの直接接続可否は実際のNATや
ファイアウォールに依存し、選択済みの暗号化リレーを代替経路として保持します。
各端末で個別に設定します。実機での互換性確認は別途必要です。`

type humanWANCandidates struct {
	Enabled         bool     `json:"enabled"`
	STUNEndpoints   []string `json:"stunEndpoints"`
	AdvertiseIPv6   bool     `json:"advertiseIPv6"`
	ProbeBudget     int      `json:"probeBudget"`
	Editable        bool     `json:"editable"`
	RestartRequired bool     `json:"restartRequired"`
}

func wanCandidatesCommand(args []string, ja, dryRun bool, out io.Writer, query commandQuery, request actionRequest) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(out, text(ja, wanCandidatesHelpEN, wanCandidatesHelpJA)+"\n")
		return err
	}
	switch args[0] {
	case "show":
		f := commandFlags("lan wan show", ja, out)
		structured := f.Bool("json", false, text(ja, "stable machine-readable settings", "安定した機械向け設定出力"))
		if err := parseFlags(f, args[1:], ja); err != nil {
			return err
		}
		if dryRun {
			return request("wan.candidates.get", map[string]any{})
		}
		var response humanWANCandidates
		if err := query("wan.candidates.get", map[string]any{}, &response); err != nil {
			return err
		}

		if *structured {
			return json.NewEncoder(out).Encode(response)
		}
		if !response.Enabled {
			fmt.Fprintln(out, text(ja, "WAN candidate extensions: disabled (existing defaults)", "WAN候補拡張: 無効（従来の既定動作）"))
			return nil
		}
		fmt.Fprintln(out, text(ja, "Saved WAN candidate extensions: enabled", "保存済みWAN候補拡張: 有効"))
		fmt.Fprintf(out, "%s: %d\n", text(ja, "STUN destination budget per netcheck", "診断1回あたりのSTUN接続先の資源上限"), response.ProbeBudget)
		for _, ep := range response.STUNEndpoints {
			fmt.Fprintf(out, "  STUN: %s\n", displayText(ep))
		}
		fmt.Fprintf(out, "%s: %t\n", text(ja, "Native IPv6 candidates", "ネイティブIPv6候補"), response.AdvertiseIPv6)
		if !response.Editable {
			fmt.Fprintln(out, text(ja, "Stop soba and start --offline before edits.", "編集前に soba を停止し、start --offline で起動してください。"))
		}
		fmt.Fprintln(out, text(ja, "Saved settings do not prove a direct WAN path. The selected encrypted relay remains fallback.", "保存済み設定はWAN直接接続の成立を示しません。選択済みの暗号化リレーを代替経路として保持します。"))
		return nil
	case "set":
		f := commandFlags("lan wan set", ja, out)
		var endpoints policyPrefixes
		f.Var(&endpoints, "stun", text(ja, "exact numeric STUN IP:port, repeatable", "STUNの数値IP:port（繰り返し指定可）"))
		budget := f.Int("probe-budget", routecat.DefaultWANProbeBudget, text(ja, "STUN destinations per netcheck run (adjustable resource budget)", "診断1回あたりのSTUN接続先数（調整可能な資源上限）"))
		ipv6 := f.Bool("advertise-ipv6", false, text(ja, "advertise native IPv6 interface candidates", "ネイティブIPv6インターフェースの候補を通知"))
		if err := parseFlags(f, args[1:], ja); err != nil {
			return err
		}
		if *budget < 1 {
			return errors.New(text(ja, "Choose a positive --probe-budget", "--probe-budget は正の整数で指定してください"))
		}
		canonical, err := (core.WANCandidateConfig{STUNEndpoints: endpoints, AdvertiseIPv6: *ipv6, ProbeBudget: *budget}).Canonical()
		if err != nil {
			return errors.New(text(ja, "Choose distinct canonical numeric --stun IP:port endpoints or --advertise-ipv6 and a finite positive --probe-budget; no default STUN server is selected", "重複しない正規表記の数値IP:portを --stun で指定するか、--advertise-ipv6 を選び、有限の正の --probe-budget を指定してください。既定のSTUNサーバーはありません"))
		}
		return request("wan.candidates.set", map[string]any{"enabled": true, "stunEndpoints": append([]string{}, canonical.STUNEndpoints...), "advertiseIPv6": canonical.AdvertiseIPv6, "probeBudget": canonical.ProbeBudget})
	case "disable":
		f := commandFlags("lan wan disable", ja, out)
		if err := parseFlags(f, args[1:], ja); err != nil {
			return err
		}
		return request("wan.candidates.set", map[string]any{"enabled": false})
	default:
		return usageError(ja, "lan wan show|set|disable|--help")
	}
}
