package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"strings"
	"time"
)

func setupPayload(args []string, ja bool, out io.Writer) (map[string]any, error) {
	f := commandFlags("setup", ja, out)
	network := f.String("network", "tailnet", text(ja, "tailnet, lan or none", "tailnet、lan、none"))
	hostname := f.String("name", "", text(ja, "node name; omit to keep the current name", "端末名（省略時は現在の名前を維持）"))
	host := f.String("host", "", text(ja, "explicit private local relay IP:port (LAN mode)", "明示するプライベートIP:ポートのローカル中継（LAN用）"))
	relay := f.String("relay", "", text(ja, "explicit trusted relay IP:port (LAN mode)", "明示する信頼済み中継のIP:ポート（LAN用）"))
	certificate := f.String("certificate", "", text(ja, "relay SHA-256 certificate fingerprint, 64 hexadecimal characters", "中継証明書のSHA-256指紋（16進64文字）"))
	if err := parseFlags(f, args, ja); err != nil {
		return nil, err
	}
	if *network != "tailnet" && *network != "lan" && *network != "none" {
		return nil, errors.New(text(ja, "--network must be tailnet, lan or none", "--network は tailnet、lan、none から選んでください"))
	}
	payload := map[string]any{"mode": *network, "hostname": *hostname}
	if *host == "" && *relay == "" && *certificate == "" {
		return payload, nil
	}
	if *network != "lan" || (*host != "" && *relay != "") || (*host != "" && *certificate != "") || (*relay == "" && *host == "") {
		return nil, errors.New(text(ja, "LAN setup requires either --host IP:PORT or --relay IP:PORT --certificate SHA256", "LAN設定では --host IP:PORT または --relay IP:PORT --certificate SHA256 のどちらかを指定してください"))
	}
	endpoint := *relay
	kind := "relay"
	if *host != "" {
		endpoint = *host
		kind = "host"
	}
	address, err := netip.ParseAddrPort(endpoint)
	if err != nil || address.Port() == 0 || address.Addr().IsUnspecified() || address.Addr().IsMulticast() || address.Addr().Zone() != "" || address.String() != endpoint {
		return nil, errors.New(text(ja, "Use an exact numeric IP:port (IPv6 addresses need brackets)", "正確な数値IP:ポートを指定してください（IPv6は角括弧で囲みます）"))
	}
	selection := map[string]string{"kind": kind, "address": endpoint}
	if kind == "host" {
		if address.Port() < 1024 || (!address.Addr().IsPrivate() && !address.Addr().IsLoopback()) || (address.Port() >= 54543 && address.Port() <= 54545) {
			return nil, errors.New(text(ja, "Choose a private local address and port 1024..65535, excluding 54543..54545", "プライベートなローカルアドレスと、54543〜54545を除く1024〜65535のポートを選んでください"))
		}
	} else {
		if len(*certificate) != 64 {
			return nil, errors.New(text(ja, "--certificate requires the relay's 64-character SHA-256 fingerprint", "--certificate に中継証明書のSHA-256指紋（64文字）を指定してください"))
		}
		if _, err := hex.DecodeString(*certificate); err != nil {
			return nil, errors.New(text(ja, "--certificate must contain hexadecimal characters", "--certificate は16進文字で指定してください"))
		}
		selection["certificateSHA256"] = strings.ToLower(*certificate)
	}
	payload["lan"] = selection
	return payload, nil
}

func lanCommand(ctx context.Context, args []string, ja bool, out io.Writer, stdin io.Reader, request actionRequest) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		_, err := io.WriteString(out, text(ja, lanHelpEN, lanHelpJA)+"\n")
		return err
	}
	operation, rest := args[0], args[1:]
	switch operation {
	case "identity", "addresses":
		if len(rest) != 0 {
			return usageError(ja, "lan "+operation)
		}
		return request("lan."+operation, map[string]any{})
	case "invite":
		f := commandFlags("lan invite", ja, out)
		recipient := f.String("to", "", text(ja, "recipient's exact public identity from soba lan identity", "soba lan identity で取得した相手の正確な公開ID"))
		name := f.String("name", "device", text(ja, "recipient display name", "相手の表示名"))
		ttl := f.Duration("ttl", 5*time.Minute, text(ja, "invitation lifetime, 1s to 10m", "招待の有効期間（1秒〜10分）"))
		if err := parseFlags(f, rest, ja); err != nil {
			return err
		}
		if len(*recipient) != 64 || *recipient != strings.ToLower(*recipient) || *recipient == strings.Repeat("0", 64) {
			return errors.New(text(ja, "--to requires the recipient's exact 64-character public identity", "--to に相手の正確な64文字の公開IDを指定してください"))
		}
		if _, err := hex.DecodeString(*recipient); err != nil {
			return errors.New(text(ja, "--to must be a hexadecimal public identity", "--to は16進文字の公開IDを指定してください"))
		}
		if *ttl < time.Second || *ttl > 10*time.Minute || *ttl%time.Second != 0 {
			return errors.New(text(ja, "--ttl must be whole seconds from 1s to 10m", "--ttl は1秒〜10分の整数秒で指定してください"))
		}
		return request("lan.invite", map[string]any{"recipientPublicKey": *recipient, "name": *name, "ttlSeconds": int(ttl.Seconds())})
	case "inspect", "join", "cancel":
		if len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h") {
			return usageHelp(out, ja, "lan "+operation+" --json-file INVITATION_FILE | --stdin")
		}
		if len(rest) == 0 || (rest[0] != "--json-file" && rest[0] != "--stdin") {
			return errors.New(text(ja, "Private invitations require --json-file PATH or --stdin; do not paste them as command arguments", "機密の招待には --json-file PATH または --stdin を使ってください。コマンド引数に貼り付けないでください"))
		}
		raw, err := commandPayload(ctx, append([]string{"lan." + operation}, rest...), stdin, ja)
		if err != nil {
			return err
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal(raw, &envelope) != nil {
			return errors.New(text(ja, "Invitation file must contain a JSON object", "招待ファイルにはJSONオブジェクトが必要です"))
		}
		invitation := string(raw)
		if wrapped, ok := envelope["invitation"]; ok {
			if json.Unmarshal(wrapped, &invitation) != nil || !json.Valid([]byte(invitation)) {
				return errors.New(text(ja, "Invitation envelope is invalid", "招待の内容が正しくありません"))
			}
		}
		return request("lan."+operation, map[string]string{"invitation": invitation})
	case "revoke":
		if len(rest) != 1 {
			return usageError(ja, "lan revoke PEER_ID")
		}
		return request("lan.revoke", map[string]string{"peerId": rest[0]})
	default:
		return errors.New(text(ja, "Unknown LAN action; use soba lan --help", "不明なLAN操作です。soba lan --help を参照してください"))
	}
}
func usageHelp(out io.Writer, ja bool, usage string) error {
	_, err := io.WriteString(out, text(ja, "Usage: soba ", "使い方: soba ")+usage+"\n")
	return err
}
