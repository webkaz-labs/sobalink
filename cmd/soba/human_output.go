package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
)

type humanPeer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Address   string `json:"address"`
	Online    bool   `json:"online"`
	Verified  bool   `json:"verified"`
	Trusted   bool   `json:"trusted"`
	Bridge    bool   `json:"bridge"`
	Discovery struct {
		State string `json:"state"`
		Code  string `json:"code"`
	} `json:"discovery"`
}
type humanService struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Direction      string   `json:"direction"`
	Network        string   `json:"network"`
	Ports          string   `json:"ports"`
	ExcludePorts   string   `json:"excludePorts"`
	PeerID         string   `json:"peerId"`
	PeerIDs        []string `json:"peerIds"`
	Endpoint       string   `json:"endpoint"`
	Status         string   `json:"status"`
	Lifetime       string   `json:"lifetime"`
	TTLSeconds     int      `json:"ttlSeconds"`
	LocalPort      int      `json:"localPort"`
	LoopbackHost   string   `json:"loopbackHost"`
	Owner          string   `json:"owner"`
	LeaseSeconds   int      `json:"leaseSeconds"`
	ExpiresAt      string   `json:"expiresAt"`
	LeaseExpiresAt string   `json:"leaseExpiresAt"`
	Error          string   `json:"error"`
	ErrorCode      string   `json:"errorCode"`
	LastFailure    *struct {
		Code      string            `json:"code"`
		At        string            `json:"at"`
		NextSteps map[string]string `json:"nextSteps"`
	} `json:"lastFailure"`
	Diagnostic *core.ServiceDiagnostic `json:"diagnostic"`
}
type humanSnapshot struct {
	LAN       *humanLANStatus `json:"lan"`
	State     string          `json:"state"`
	ProcessID int             `json:"processId"`
	Self      struct {
		Name      string `json:"name"`
		Status    string `json:"status"`
		Error     string `json:"error"`
		ErrorCode string `json:"errorCode"`
	} `json:"self"`
	Settings struct {
		Network string `json:"network"`
	} `json:"settings"`
	Peers     []humanPeer       `json:"peers"`
	Services  []humanService    `json:"services"`
	Shares    []humanService    `json:"shares"`
	Messages  []json.RawMessage `json:"messages"`
	Transfers []json.RawMessage `json:"transfers"`
	Proxies   []json.RawMessage `json:"proxies"`
}

func snapshotCommand(ctx context.Context, command string, args []string, dir string, ja bool, out io.Writer, client controlCaller) error {
	f := commandFlags(command, ja, out)
	structured := f.Bool("json", false, text(ja, "stable machine-readable state", "安定した機械向けの状態出力"))
	if err := parseFlags(f, args, ja); err != nil {
		return err
	}
	var raw json.RawMessage
	ipc := command
	if command == "peers" {
		ipc = "status"
	}
	err := client(ctx, dir, ipc, &raw)
	if control.Unavailable(err) {
		raw = []byte(`{"state":"stopped","processId":0,"reason":"No reachable process","application":"unverified"}`)
	} else if err != nil {
		return err
	}
	var snapshot humanSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}
	if *structured {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if command == "peers" {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			peers := fields["peers"]
			if len(peers) == 0 || string(peers) == "null" {
				peers = []byte("[]")
			}
			return encoder.Encode(map[string]json.RawMessage{"peers": peers})
		}
		return encoder.Encode(raw)
	}
	if command == "peers" {
		writeHumanPeers(out, ja, snapshot.Peers)
		fmt.Fprintf(out, "%s: %s\n", text(ja, "Refresh shared services", "共有サービスを更新"), cliCommandExample(dir, "discover", "--json"))
		return nil
	}
	if command == "stop" {
		fmt.Fprintln(out, text(ja, "Stopped. Active connections and shares are no longer running.", "停止しました。稼働中の接続と共有は終了しています。"))
		return nil
	}
	state := snapshot.Self.Status
	if state == "" {
		state = snapshot.State
	}
	if state == "" {
		state = "unknown"
	}
	fmt.Fprintf(out, "%s: %s\n", text(ja, "State", "状態"), humanNetworkState(ja, state))
	if snapshot.Self.Name != "" {
		fmt.Fprintf(out, "%s: %s; PID: %d; %s: %s\n", text(ja, "Node", "端末"), displayText(snapshot.Self.Name), snapshot.ProcessID, text(ja, "network", "ネットワーク"), snapshot.Settings.Network)
	}
	if snapshot.Self.Error != "" {
		fmt.Fprintf(out, "%s: %s (%s)\n", text(ja, "Reported reason", "確認できた理由"), displayText(snapshot.Self.Error), snapshot.Self.ErrorCode)
	}
	fmt.Fprintf(out, "%s: %d; %s: %d; %s: %d\n", text(ja, "Peers", "相手"), len(snapshot.Peers), text(ja, "connections", "接続"), len(snapshot.Services), text(ja, "shares", "共有"), len(snapshot.Shares))
	writeHumanLANStatus(out, ja, snapshot.LAN)
	names := map[string]string{}
	for _, peer := range snapshot.Peers {
		names[peer.ID] = peer.Name
	}
	for _, service := range append(snapshot.Services, snapshot.Shares...) {
		writeHumanService(out, ja, service, names)
	}
	fmt.Fprintln(out, text(ja, "Listener readiness does not establish application success or remote-job completion.", "待受の準備完了は、アプリの動作成功や相手側ジョブの完了を意味しません。"))
	next := []string{"peers"}
	normalizedState := strings.ToLower(state)
	if normalizedState == "stopped" && snapshot.ProcessID == 0 {
		next = []string{"start", "--background"}
	} else if normalizedState == "idle" || normalizedState == "none" || normalizedState == "stopped" {
		next = []string{"setup", "--help"}
	} else if normalizedState == "needs-login" || normalizedState == "needslogin" || normalizedState == "needsmachineauth" {
		next = []string{"login", "--link", "--wait"}
	} else if normalizedState == "error" || normalizedState == "unavailable" {
		next = []string{"doctor"}
	}
	fmt.Fprintf(out, "%s: %s\n", text(ja, "Next", "次の操作"), cliCommandExample(dir, next...))
	return nil
}
func humanNetworkState(ja bool, state string) string {
	if ja {
		if value, ok := map[string]string{"stopped": "停止中", "running": "稼働中", "ready": "準備完了", "connected": "接続済み", "idle": "待機中", "starting": "起動中", "needs-login": "ログインが必要", "needslogin": "ログインが必要", "needsmachineauth": "端末の承認が必要", "unavailable": "状態を取得できません", "stopping": "停止処理中", "error": "エラー", "unknown": "不明", "none": "未選択"}[strings.ToLower(state)]; ok {
			return value + " (" + state + ")"
		}
	}
	return displayText(state)
}
func writeHumanPeers(out io.Writer, ja bool, peers []humanPeer) {
	fmt.Fprintln(out, text(ja, "Current peers:", "現在の相手:"))
	if len(peers) == 0 {
		fmt.Fprintln(out, text(ja, "No current peers. Start the network and sign in, then retry.", "現在の相手はありません。ネットワークを開始してログインしてから再確認してください。"))
		return
	}
	for i, peer := range peers {
		reachability := text(ja, "reachability unconfirmed", "到達未確認")
		if peer.Online {
			reachability = text(ja, "online", "オンライン")
		}
		fmt.Fprintf(out, "%d. %s (%s) | %s\n", i+1, displayText(peer.Name), peer.ID, reachability)
		fmt.Fprintf(out, "   %s: %s; %s: %s; %s: %s\n", text(ja, "address", "アドレス"), displayText(peer.Address), text(ja, "identity verified", "IDを確認済み"), humanYesNo(ja, peer.Verified), text(ja, "messages/files approved", "メッセージ・ファイルを許可"), humanYesNo(ja, peer.Trusted))
		if peer.Discovery.State != "" {
			fmt.Fprintf(out, "   %s: %s\n", text(ja, "Discovery observation", "共有情報の確認"), humanDiscoveryState(ja, peer.Discovery.State))
		}
	}
	fmt.Fprintln(out, text(ja, "Peer presence or online state does not prove that sobalink or an application is running.", "相手の登録やオンライン状態だけでは、sobalinkやアプリの起動は確認できません。"))
}
func humanDiscoveryState(ja bool, state string) string {
	if ja {
		if value, ok := map[string]string{"confirmed": "確認済み", "unconfirmed": "未確認", "unsupported": "未対応", "pending": "未確認", "stale": "古い確認情報", "limited": "確認範囲に上限あり"}[state]; ok {
			return value + " (" + state + ")"
		}
	}
	return displayText(state)
}
func humanPeerLabel(id string, names map[string]string) string {
	if name := names[id]; name != "" && name != id {
		return displayText(name) + " (" + id + ")"
	}
	return id
}
func writeHumanService(out io.Writer, ja bool, service humanService, names map[string]string) {
	state := service.Status
	if state == "" {
		state = "saved"
	}
	fmt.Fprintf(out, "%s (%s) | %s | %s %s\n", displayText(service.Name), service.ID, humanServiceState(ja, state), service.Network, service.Ports)
	peers := service.PeerIDs
	if service.PeerID != "" {
		peers = []string{service.PeerID}
	}
	labels := []string{}
	for _, id := range peers {
		labels = append(labels, humanPeerLabel(id, names))
	}
	fmt.Fprintf(out, "  %s: %s\n", text(ja, "Peer scope", "対象の相手"), strings.Join(labels, ", "))
	if service.ExcludePorts != "" {
		fmt.Fprintf(out, "  %s: %s\n", text(ja, "Excluded ports", "除外ポート"), service.ExcludePorts)
	}
	if service.Endpoint != "" {
		fmt.Fprintf(out, "  %s: %s\n", text(ja, "Reported endpoint", "実際の接続先"), displayText(service.Endpoint))
	} else if service.LocalPort != 0 {
		host := service.LoopbackHost
		if host == "" {
			host = "127.0.0.1"
		}
		fmt.Fprintf(out, "  %s: %s; %s\n", text(ja, "Saved local mapping", "保存済みのローカル設定"), net.JoinHostPort(host, strconv.Itoa(service.LocalPort)), text(ja, "listener readiness unconfirmed", "待受の準備は未確認"))
	} else {
		fmt.Fprintln(out, "  "+text(ja, "Saved local mapping uses the selected service ports; listener readiness unconfirmed.", "保存済みのローカル設定は選択したサービスポートと同じです。待受の準備は未確認です。"))
	}
	fmt.Fprintf(out, "  %s: %s", text(ja, "Lifetime", "期限"), service.Lifetime)
	if service.Lifetime == "finite" {
		fmt.Fprintf(out, " (%ds)", service.TTLSeconds)
	}
	if service.ExpiresAt != "" {
		fmt.Fprintf(out, "; %s: %s", text(ja, "expires", "終了予定"), service.ExpiresAt)
	}
	fmt.Fprintln(out)
	if service.Owner != "" {
		fmt.Fprintf(out, "  %s: %s", text(ja, "Owner", "所有者"), displayText(service.Owner))
		if service.LeaseSeconds > 0 {
			fmt.Fprintf(out, "; %s: %ds / %s", text(ja, "lease / expiry", "リース / 期限"), service.LeaseSeconds, service.LeaseExpiresAt)
		}
		fmt.Fprintln(out)
	}
	if service.Error != "" || service.ErrorCode != "" {
		fmt.Fprintf(out, "  %s: %s (%s)\n", text(ja, "Reported reason", "確認できた理由"), displayText(service.Error), service.ErrorCode)
	}
	if service.LastFailure != nil {
		fmt.Fprintf(out, "  %s: %s (%s)\n", text(ja, "Last recorded failure", "直近の失敗の記録"), displayText(service.LastFailure.Code), displayText(service.LastFailure.At))
		writeHumanNextSteps(out, ja, service.LastFailure.NextSteps)
	}
	if service.Diagnostic != nil {
		fmt.Fprintf(out, "  %s: %s / %s / %s\n", text(ja, "Last explicit check", "直近の明示的な確認"), displayText(service.Diagnostic.Code), displayText(service.Diagnostic.Transport), service.Diagnostic.CheckedAt.Format("2006-01-02T15:04:05Z07:00"))
		writeHumanNextSteps(out, ja, service.Diagnostic.NextSteps)
	}
}
func humanFromSpec(spec core.ServiceSpec) humanService {
	var result humanService
	raw, _ := json.Marshal(spec)
	_ = json.Unmarshal(raw, &result)
	return result
}
func writeHumanSelection(out io.Writer, ja bool, raw json.RawMessage) error {
	var value struct {
		Services []core.ServiceSpec `json:"services"`
		States   []json.RawMessage  `json:"states"`
		Ready    bool               `json:"ready"`
		Group    string             `json:"group"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value.Group != "" {
		fmt.Fprintf(out, "%s: %s\n", text(ja, "Group", "グループ"), displayText(value.Group))
	}
	byID := map[string]json.RawMessage{}
	for _, state := range value.States {
		var id struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(state, &id) == nil {
			byID[id.ID] = state
		}
	}
	for _, spec := range value.Services {
		service := humanFromSpec(spec)
		if state := byID[spec.ID]; state != nil {
			_ = json.Unmarshal(state, &service)
		}
		writeHumanService(out, ja, service, nil)
	}
	if len(value.Services) == 0 {
		for _, state := range value.States {
			var service humanService
			if json.Unmarshal(state, &service) == nil {
				writeHumanService(out, ja, service, nil)
			}
		}
	}
	fmt.Fprintf(out, "%s: %s. %s\n", text(ja, "Transport ready", "通信の準備"), humanYesNo(ja, value.Ready), text(ja, "Application success remains unverified.", "アプリの動作成功は未確認です。"))
	return nil
}

func writeHumanNextSteps(out io.Writer, ja bool, steps map[string]string) {
	language := "en"
	if ja {
		language = "ja"
	}
	value := steps[language]
	if value == "" {
		value = steps["en"]
	}
	if value != "" {
		fmt.Fprintf(out, "  %s: %s\n", text(ja, "Next", "次の操作"), displayText(value))
	}
}
