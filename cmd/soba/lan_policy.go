package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
)

const lanPolicyHelpEN = `Allowed LAN destinations (explicit opt-in)

  soba lan policy show [--json]
  soba lan policy set --mode allowed-lan-destinations --prefix 192.168.50.0/24
  soba lan policy set --mode trusted-relay

The prefix above is fictional. Inspect soba lan addresses and explicitly choose
only intended canonical private/ULA or loopback CIDRs. Repeat --prefix for more.
Stop soba and start --offline before edits. Include the selected and prepared
relay addresses. Peer pairing, application trust and route grants stay separate.
Prefix membership does not prove physical LAN or VPN isolation.
Explicitly choosing trusted-relay permits ordinary direct peer destinations and
selected-relay diagnostics again, including approved external relay addresses.
Saving does not start the network. Review status, then start the saved LAN setup.`
const lanPolicyHelpJA = `LAN接続先の許可範囲（明示的に選択）

  soba lan policy show [--json]
  soba lan policy set --mode allowed-lan-destinations --prefix 192.168.50.0/24
  soba lan policy set --mode trusted-relay

上の範囲は架空の例です。soba lan addresses を確認し、意図した正規表記の
プライベート・ULA・ループバックCIDRだけを明示的に選びます。--prefix は複数指定できます。
編集前に soba を停止して start --offline で起動します。選択済み・追加候補の
リレーを範囲に含めてください。ペアリング・アプリの信頼・経路許可は別々です。
アドレス範囲への一致は、物理LANやVPNの隔離を証明しません。
trusted-relay を明示的に選ぶと、通常の直接接続先や選択済みリレーの診断通信を
再び許可します。許可済みの外部リレー宛ても含みます。
保存だけではネットワークを開始しません。状態を確認し、保存済みLAN設定を開始してください。`

type policyPrefixes []string

func (p *policyPrefixes) String() string         { return strings.Join(*p, ",") }
func (p *policyPrefixes) Set(value string) error { *p = append(*p, value); return nil }

type humanLANPolicy struct {
	Mode            string   `json:"mode"`
	Prefixes        []string `json:"prefixes"`
	Editable        bool     `json:"editable"`
	RestartRequired bool     `json:"restartRequired"`
}

func lanPolicyCommand(args []string, ja, dryRun bool, out io.Writer, query commandQuery, request actionRequest) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		_, err := io.WriteString(out, text(ja, lanPolicyHelpEN, lanPolicyHelpJA)+"\n")
		return err
	}
	switch args[0] {
	case "show":
		f := commandFlags("lan policy show", ja, out)
		structured := f.Bool("json", false, text(ja, "stable machine-readable policy", "安定した機械向けポリシー出力"))
		if err := parseFlags(f, args[1:], ja); err != nil {
			return err
		}
		if dryRun {
			return request("lan.policy.get", map[string]any{})
		}
		var response struct {
			OK     bool           `json:"ok"`
			Result humanLANPolicy `json:"result"`
		}
		if err := query("lan.policy.get", map[string]any{}, &response); err != nil {
			return err
		}
		if !response.OK {
			return errors.New(text(ja, "Policy status was not confirmed", "ポリシー状態を確認できませんでした"))
		}
		if *structured {
			return json.NewEncoder(out).Encode(response.Result)
		}
		fmt.Fprintf(out, "%s: %s\n", text(ja, "Destination policy", "接続先ポリシー"), displayText(response.Result.Mode))
		for _, prefix := range response.Result.Prefixes {
			fmt.Fprintf(out, "  %s\n", displayText(prefix))
		}
		if !response.Result.Editable {
			fmt.Fprintln(out, text(ja, "Stop soba and start --offline before changing this policy.", "この設定を変更するには、soba を停止して start --offline で起動してください。"))
		}
		fmt.Fprintln(out, text(ja, "Prefix membership does not prove physical LAN or VPN isolation.", "アドレス範囲への一致は、物理LANやVPNの隔離を証明しません。"))
		return nil
	case "set":
		f := commandFlags("lan policy set", ja, out)
		mode := f.String("mode", "", text(ja, "allowed-lan-destinations or trusted-relay", "allowed-lan-destinations または trusted-relay"))
		var prefixes policyPrefixes
		f.Var(&prefixes, "prefix", text(ja, "explicit canonical private/ULA or loopback CIDR; repeatable", "明示する正規表記のプライベート・ULA・ループバックCIDR（複数可）"))

		if err := parseFlags(f, args[1:], ja); err != nil {
			return err
		}
		if *mode == "" {
			return errors.New(text(ja, "Choose --mode explicitly", "--mode を明示的に選んでください"))
		}

		policy, err := (lanpolicy.Config{Mode: *mode, Prefixes: prefixes}).Canonical()
		if err != nil {
			return errors.New(text(ja, "Choose trusted-relay without prefixes, or allowed-lan-destinations with canonical private/ULA or loopback CIDRs", "trusted-relay は範囲を指定せず、allowed-lan-destinations は正規表記のプライベート・ULA・ループバックCIDRを指定してください"))
		}
		return request("lan.policy.set", map[string]any{"mode": policy.Mode, "prefixes": append([]string{}, policy.Prefixes...)})
	default:
		return usageError(ja, "lan policy show|set|--help")
	}
}
