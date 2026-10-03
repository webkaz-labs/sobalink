package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
)

func savedRulesCommand(command string, args []string, dir string, ja bool, out io.Writer, query commandQuery) error {
	if command == "settings" {
		return clientSettingsCommand(args, dir, ja, out, query)
	}
	f := commandFlags(command, ja, out)
	structured := f.Bool("json", false, text(ja, "machine-readable saved definitions", "保存済み設定の機械向け出力"))
	positional, err := workflowFlags(f, args, ja)
	if err != nil {
		return err
	}
	if len(positional) > 1 || command == "rules" && len(positional) != 0 {
		return usageError(ja, commandUsage[command])
	}
	var saved struct {
		Profile  core.DefinitionBundle `json:"profile"`
		Revision string                `json:"revision"`
	}
	if err := query("profile.export", map[string]any{}, &saved); err != nil {
		return err
	}
	services := saved.Profile.Services
	if len(positional) != 0 {
		services = nil
		for _, service := range saved.Profile.Services {
			if service.ID == positional[0] {
				services = append(services, service)
			}
		}
		if len(services) == 0 {
			return fmt.Errorf("%s: %s", text(ja, "Saved service not found; use soba rules", "保存済みサービスが見つかりません。soba rules で確認してください"), positional[0])
		}
	}
	if services == nil {
		services = []core.ServiceSpec{}
	}
	if *structured {
		return json.NewEncoder(out).Encode(map[string]any{"services": services, "revision": saved.Revision, "state": "saved", "runtimeChecked": false, "application": "unverified"})
	}
	fmt.Fprintln(out, text(ja, "Saved definitions; running state has not been checked.", "保存済み設定です。現在の稼働状態は確認していません。"))
	if len(services) == 0 {
		fmt.Fprintln(out, text(ja, "No saved services. Use soba connect or soba share to create one.", "保存済みサービスはありません。soba connect または soba share で作成できます。"))
	}
	for _, service := range services {
		fmt.Fprintf(out, "%s (%s) | %s | %s %s | %s\n", displayText(service.Name), service.ID, text(ja, service.Direction, map[string]string{"forward": "接続", "share": "共有"}[service.Direction]), service.Network, service.Ports, service.Lifetime)
		if service.ExcludePorts != "" {
			fmt.Fprintf(out, "  %s: %s\n", text(ja, "Excluded ports", "除外ポート"), service.ExcludePorts)
		}
		if service.PeerID != "" {
			fmt.Fprintf(out, "  %s: %s\n", text(ja, "Peer", "相手"), service.PeerID)
		}
		if len(service.PeerIDs) > 0 {
			fmt.Fprintf(out, "  %s: %s\n", text(ja, "Allowed peers", "許可した相手"), strings.Join(service.PeerIDs, ", "))
		}
	}
	fmt.Fprintf(out, "%s: %s\n", text(ja, "Check current state", "現在の状態を確認"), cliCommandExample(dir, "status"))
	return nil
}

func cliCommandExample(dir string, args ...string) string {
	argv := []string{"soba"}
	if dir != "" {
		argv = append(argv, "--state-dir", dir)
	}
	argv = append(argv, args...)
	for _, value := range argv {
		if value == "" || strings.ContainsFunc(value, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-:=", r))
		}) {
			encoded, _ := json.Marshal(argv)
			return "argv: " + string(encoded)
		}
	}
	return strings.Join(argv, " ")
}

func clientSettingsCommand(args []string, dir string, ja bool, out io.Writer, query commandQuery) error {
	f := commandFlags("settings", ja, out)
	structured := f.Bool("json", false, text(ja, "machine-readable application settings", "アプリ設定の機械向け出力"))
	group := f.String("group", "", text(ja, "saved group name", "保存済みのグループ名"))
	service := f.String("service", "", text(ja, "saved service IDs, separated by commas", "保存済みサービスID（カンマ区切り）"))
	positional, err := workflowFlags(f, args, ja)
	if err != nil {
		return err
	}
	ids, err := workflowIDs(*service, positional, ja)
	if err != nil {
		return err
	}
	if *group != "" && (len(ids) != 0 || !config.ValidName(*group)) {
		return usageError(ja, "settings [SERVICE_ID... | --service ID[,ID] | --group NAME] [--json]")
	}
	resolved, err := resolveServiceReferences(ids, ja, query)
	if err != nil {
		return err
	}
	ids = resolved.IDs
	var view core.ClientSettingsView
	if err := query("client.settings", core.ClientSettingsRequest{IDs: ids, Group: *group}, &view); err != nil {
		return err
	}
	if *structured {
		return json.NewEncoder(out).Encode(view)
	}
	if len(view.Services) == 0 {
		fmt.Fprintln(out, text(ja, "No saved services. Use soba connect or soba share to create one.", "保存済みサービスはありません。soba connect または soba share で作成できます。"))
	}
	for _, service := range view.Services {
		writeClientServiceHints(out, ja, service)
	}
	for _, settings := range view.RustDesk {
		fmt.Fprintf(out, "RustDesk: %s\n%s: %s\n%s: %s\n%s: %s\nUDP: %s; %s: remote-ID%s\n%s: %s\n", displayText(settings.Group), text(ja, "ID server", "IDサーバー"), settings.IDServer, text(ja, "Relay server", "リレーサーバー"), settings.RelayServer, text(ja, "Proxy", "プロキシ"), settings.Proxy, humanYesNo(ja, settings.UDPEnabled), text(ja, "connection", "接続"), settings.RemoteIDSuffix, text(ja, "Public key", "公開鍵"), displayText(settings.PublicKey))
		writeClientNotices(out, ja, settings.Notices)
	}
	writeClientNotices(out, ja, view.Notices)
	fmt.Fprintf(out, "%s: %s\n", text(ja, "Check current state", "現在の状態を確認"), cliCommandExample(dir, "status"))
	return nil
}
func writeClientServiceHints(out io.Writer, ja bool, service core.ClientServiceSettings) {
	fmt.Fprintf(out, "%s (%s) | %s %s | %s; %s: %s\n", displayText(service.Name), service.ID, service.Purpose, service.Network, humanServiceState(ja, service.Status), text(ja, "listener ready", "待受準備"), humanYesNo(ja, service.ListenerReady))
	if service.LocalEndpoint != "" {
		fmt.Fprintf(out, "  %s: %s\n", text(ja, "Local endpoint", "ローカル接続先"), service.LocalEndpoint)
	}
	if service.PeerID != "" {
		fmt.Fprintf(out, "  %s: %s\n", text(ja, "Peer", "相手"), service.PeerID)
	}
	if len(service.AllowedPeerIDs) > 0 {
		fmt.Fprintf(out, "  %s: %s\n", text(ja, "Allowed peers", "許可した相手"), strings.Join(service.AllowedPeerIDs, ", "))
	}
	for _, mapping := range service.Mappings {
		fmt.Fprintf(out, "  %s %d-%d -> %s %d-%d (%s)\n", text(ja, "local", "ローカル"), mapping.LocalFirst, mapping.LocalLast, text(ja, "remote", "接続先"), mapping.RemoteFirst, mapping.RemoteLast, service.Network)
	}
	for _, host := range service.RemoteHosts {
		fmt.Fprintf(out, "  %s: %s\n", text(ja, "Remote host (ports above)", "相手のアドレス（ポートは上記）"), host)
	}
	for _, endpoint := range service.RemoteEndpoints {
		fmt.Fprintf(out, "  %s: %s\n", text(ja, "Remote endpoint", "相手側の接続先"), endpoint)
	}
	fmt.Fprintf(out, "  %s: %s", text(ja, "Lifetime", "期限"), service.Lifetime)
	if service.Lifetime == "finite" {
		fmt.Fprintf(out, " (%ds)", service.TTLSeconds)
	}
	fmt.Fprintln(out)
	if service.SSH != nil {
		fmt.Fprintf(out, "  SSH: %s\n", service.SSH.Command)
	}
	if service.HTTPCandidate != "" {
		fmt.Fprintf(out, "  %s: %s\n", text(ja, "HTTP candidate", "HTTPの候補"), service.HTTPCandidate)
	}
	writeClientNotices(out, ja, service.Notices)
}
func writeClientNotices(out io.Writer, ja bool, notices []core.ClientNotice) {
	for _, notice := range notices {
		fmt.Fprintln(out, "  "+text(ja, notice.Message, notice.MessageJA))
	}
}

// Names are untrusted display data. Keep terminal control characters out of
// human output without altering the original values in JSON or commands.
func displayText(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || r >= 0x80 && r <= 0x9f {
			return ' '
		}
		return r
	}, value)
}

func humanYesNo(ja, yes bool) string {
	if yes {
		return text(ja, "yes", "はい")
	}
	return text(ja, "no", "いいえ")
}
func humanServiceState(ja bool, state string) string {
	if ja {
		if value, ok := map[string]string{"active": "稼働中", "saved": "保存済み", "stopped": "停止済み", "expired": "期限切れ", "failed": "失敗", "reconnecting": "再接続中", "planned": "未保存の予定"}[state]; ok {
			return value + " (" + state + ")"
		}
	}
	return displayText(state)
}
