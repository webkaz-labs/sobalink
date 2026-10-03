package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/ranges"
	"github.com/webkaz-labs/sobalink/internal/servicepresets"
	"golang.org/x/term"
)

type guidedPeer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Verified bool   `json:"verified"`
	Online   bool   `json:"online"`
}
type guidedOffer struct {
	ID          string    `json:"id"`
	Revision    string    `json:"revision"`
	Name        string    `json:"name"`
	PeerID      string    `json:"peerId"`
	Network     string    `json:"network"`
	Ports       string    `json:"ports"`
	Purpose     string    `json:"purpose"`
	Lifetime    string    `json:"lifetime"`
	ExpiresAt   time.Time `json:"expiresAt"`
	CheckedAt   time.Time `json:"checkedAt"`
	Application string    `json:"application"`
}
type guidedSnapshot struct {
	Peers             []guidedPeer  `json:"peers"`
	AvailableServices []guidedOffer `json:"availableServices"`
}
type serviceGuide struct {
	ctx         context.Context
	p           *prompts
	ja          bool
	command     string
	dir         string
	query       stateQuery
	queryAction commandQuery
	spec        core.ServiceSpec
	selected    *guidedOffer
	peerNames   string
}

func wantsGuidedService(args []string, in io.Reader) bool {
	if hasFlag(args, "interactive") || hasFlag(args, "manual") {
		return true
	}
	input, ok := in.(*os.File)
	return len(args) == 0 && ok && term.IsTerminal(int(input.Fd()))
}
func cancelInput(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "q", "quit", "cancel", "取消", "中止", "取り消し", "キャンセル":
		return true
	}
	return false
}
func affirmativeInput(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "y", "yes", "はい":
		return true
	}
	return false
}

func guidedServiceCommand(ctx context.Context, command string, args []string, dir string, ja, dryRun bool, out io.Writer, in io.Reader, query stateQuery, queryAction commandQuery, request actionRequest) error {
	if hasFlag(args, "json") {
		return errors.New(text(ja, "--interactive and --manual cannot be combined with --json; use explicit service flags for machine input", "--interactive・--manual と --json は併用できません。機械向け操作には各サービスの指定を使ってください"))
	}
	if os.Getenv("TEA_TRACE") != "" {
		return errors.New(text(ja, "Unset TEA_TRACE before guided input; input logging is not allowed", "案内付きの入力前に TEA_TRACE を解除してください。入力のログ記録はできません"))
	}
	g := &serviceGuide{ctx: ctx, p: newPrompts(in, out), ja: ja, command: command, dir: dir, query: query, queryAction: queryAction}
	g.p.ctx, g.p.ja = ctx, ja
	g.spec = core.ServiceSpec{Direction: "forward", Network: "tcp", Purpose: "custom", LoopbackHost: "127.0.0.1", Lifetime: "until-stopped"}
	if command == "share" {
		g.spec.Direction, g.spec.Lifetime, g.spec.TTLSeconds = "share", "finite", 3600
	}
	fmt.Fprintln(out, text(ja, "Choose a target, review the service and start explicitly. q cancels before applying.", "相手とサービスを選び、内容を確認して明示的に開始します。適用前は q で取り消せます。"))
	forwarded := []string{}
	for _, arg := range args {
		if arg != "--interactive" && arg != "--manual" {
			forwarded = append(forwarded, arg)
		}
	}
	if len(forwarded) != 0 {
		payload, err := servicePayload(command, forwarded, ja, out)
		if err != nil {
			return err
		}
		if err := resolveNamedPeerPayload(payload, ja, query); err != nil {
			return err
		}
		encoded, _ := json.Marshal(payload)
		if err = json.Unmarshal(encoded, &g.spec); err != nil {
			return err
		}
		g.spec.Direction = "forward"
		if command == "share" {
			g.spec.Direction = "share"
		}
		if serviceID, _ := payload["serviceId"].(string); serviceID != "" {
			return errors.New(text(ja, "For guided advertised selection, use connect --interactive without service flags", "案内付きで共有サービスを選ぶには、サービス指定なしで connect --interactive を使ってください"))
		}
	} else {
		if err := g.selectTarget(hasFlag(args, "manual")); err != nil {
			return g.localizeError(err)
		}
		if g.selected == nil {
			if err := g.choosePurpose(); err != nil {
				return g.localizeError(err)
			}
		}
		if err := g.editPorts(); err != nil {
			return g.localizeError(err)
		}
		if err := g.editLifetime(); err != nil {
			return g.localizeError(err)
		}
		if command == "share" {
			if err := g.editDiscovery(); err != nil {
				return g.localizeError(err)
			}
		}
		if err := g.suggestName(); err != nil {
			return err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		g.review()
		action, err := g.p.askChoice(text(ja, "y start; n name; e ports; l lifetime; p purpose; s/back target; d discovery; q cancel: ", "y 開始 / n 名前 / e ポート / l 期限 / p 用途 / s・back 相手 / d 共有表示 / q 取消: "), []promptChoice{{"y", text(ja, "Start reviewed service", "確認したサービスを開始")}, {"n", text(ja, "Edit saved name", "保存名を編集")}, {"e", text(ja, "Edit ports", "ポートを編集")}, {"l", text(ja, "Edit lifetime", "期限を編集")}, {"s", text(ja, "Back to selection", "選択に戻る")}, {"q", text(ja, "Cancel", "取消")}}, "")
		if err != nil {
			return g.localizeError(err)
		}
		switch strings.ToLower(action) {
		case "y", "yes", "はい":
			payload, err := g.payload()
			if err != nil {
				fmt.Fprintln(out, err)
				continue
			}
			if !dryRun {
				err = g.refreshSelected()
			}
			if err == nil {
				if g.selected != nil {
					payload["serviceRevision"] = g.selected.Revision
				}
				err = request("service."+command, payload)
			}
			if err == nil {
				if !dryRun {
					fmt.Fprintln(out, text(ja, "Service command accepted. Check current state before using the endpoint; application behavior remains unverified.", "サービスの操作を受け付けました。接続先を使う前に現在の状態を確認してください。アプリの動作は未確認です。"))
					var settings core.ClientSettingsView
					if readErr := queryAction("client.settings", core.ClientSettingsRequest{}, &settings); readErr == nil {
						for _, service := range settings.Services {
							if service.Name == g.spec.Name {
								writeClientServiceHints(out, ja, service)
							}
						}
						writeClientNotices(out, ja, settings.Notices)
					} else {
						fmt.Fprintln(out, text(ja, "Application settings could not be read; inspect settings before using the endpoint.", "アプリ設定を取得できませんでした。接続先を使う前に settings で確認してください。"))
					}
					fmt.Fprintf(out, "%s\n", cliCommandExample(dir, "status"))
				}
				return nil
			}
			fmt.Fprintf(out, "%s: %s\n", text(ja, "Could not apply; no successful start has been confirmed", "適用できませんでした。開始成功は確認できていません"), displayText(err.Error()))
			answer, promptErr := g.p.askChoice(text(ja, "r retry this reviewed request; e edit review; s choose target again; q cancel: ", "r 同じ内容で再試行 / e 確認画面で編集 / s 相手を再選択 / q 取消: "), []promptChoice{{"r", text(ja, "Retry", "再試行")}, {"e", text(ja, "Edit", "編集")}, {"s", text(ja, "Select again", "再選択")}, {"q", text(ja, "Cancel", "取消")}}, "")
			if promptErr != nil {
				return g.localizeError(promptErr)
			}
			if answer == "s" {
				if err := g.selectTarget(false); err != nil {
					return g.localizeError(err)
				}
			}
			// Retry always returns through review. A failure can be partial, and a
			// changed grant must be selected explicitly rather than rebound silently.
		case "n", "name", "名前":
			err = g.editName()
		case "e", "edit", "ports", "ポート":
			err = g.editPorts()
		case "l", "lifetime", "期限":
			err = g.editLifetime()
		case "p", "purpose", "用途":
			if g.selected != nil {
				fmt.Fprintln(out, text(ja, "Select a different advertised service or choose manual mode to change its purpose or remote ports.", "用途や接続先ポートを変えるには、別の共有サービスを選ぶか、手動設定を選んでください。"))
			} else {
				err = g.choosePurpose()
			}
		case "s", "back", "target", "戻る", "相手":
			err = g.selectTarget(false)
		case "d", "discovery", "共有表示":
			if command == "share" {
				err = g.editDiscovery()
			}
		default:
			fmt.Fprintln(out, text(ja, "Choose an action shown above; q cancels.", "表示した操作を選んでください。q で取り消せます。"))
		}
		if err != nil {
			return g.localizeError(err)
		}
	}
}

func (g *serviceGuide) localizeError(err error) error {
	if errors.Is(err, errPromptCanceled) {
		return fmt.Errorf("%s: %w", text(g.ja, "Canceled before further changes", "追加の変更前に取り消しました"), context.Canceled)
	}
	if g.ja {
		if translated, ok := terminalJapanese[err.Error()]; ok {
			return errors.New(translated)
		}
	}
	return err
}
func (g *serviceGuide) selectTarget(manual bool) error {
	for {
		var snapshot guidedSnapshot
		if err := g.query(&snapshot); err != nil {
			fmt.Fprintln(g.p.out, text(g.ja, "Could not read current peers or shares; start the agent and activate its network, then retry.", "現在の相手と共有を取得できません。本体とネットワークを起動してから再試行してください。"))
			answer, promptErr := g.p.ask(text(g.ja, "r retry / q cancel: ", "r 再試行 / q 取消: "))
			if promptErr != nil {
				return promptErr
			}
			if answer != "r" {
				continue
			}
			continue
		}
		peers := []guidedPeer{}
		for _, peer := range snapshot.Peers {
			if peer.Verified && config.ValidPeerID(peer.ID) {
				peers = append(peers, peer)
			}
		}
		sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })
		names := map[string]string{}
		for _, peer := range peers {
			names[peer.ID] = peer.Name
		}
		if g.command == "connect" && !manual {
			var refreshed struct {
				Services     []guidedOffer `json:"services"`
				Observations []struct {
					State string `json:"state"`
				} `json:"observations"`
				Partial bool `json:"partial"`
			}
			if err := g.queryAction("discovery.refresh", map[string]any{}, &refreshed); err != nil {
				snapshot.AvailableServices = nil
				fmt.Fprintln(g.p.out, text(g.ja, "Shared-service discovery is unavailable. Refresh to retry, or choose manual mode for a known service.", "共有サービスを確認できません。更新して再試行するか、既知のサービスには手動設定を選んでください。"))
			} else {
				snapshot.AvailableServices = refreshed.Services
				unconfirmed := refreshed.Partial
				for _, observation := range refreshed.Observations {
					unconfirmed = unconfirmed || observation.State != "confirmed"
				}
				if unconfirmed {
					fmt.Fprintln(g.p.out, text(g.ja, "Some peers could not be checked. This does not establish that their services are stopped.", "確認できない相手がいます。そのサービスが停止しているとは限りません。"))
				}
			}
			offers := []guidedOffer{}
			now := time.Now()
			for _, offer := range snapshot.AvailableServices {
				if _, ok := names[offer.PeerID]; ok && selectableGuidedOffer(offer, now) {
					offers = append(offers, offer)
				}
			}
			sort.Slice(offers, func(i, j int) bool {
				return names[offers[i].PeerID]+offers[i].Name+offers[i].ID < names[offers[j].PeerID]+offers[j].Name+offers[j].ID
			})
			fmt.Fprintln(g.p.out, text(g.ja, "Services shared with this node (recent authenticated observations):", "この端末に共有されているサービス（認証済みの最近の応答）:"))
			choices := []promptChoice{}
			for i, offer := range offers {
				label := fmt.Sprintf("%d. %s | %s | %s %s | %s", i+1, displayText(names[offer.PeerID]), displayText(offer.Name), offer.Network, offer.Ports, offer.Lifetime)
				fmt.Fprintln(g.p.out, label)
				choices = append(choices, promptChoice{strconv.Itoa(i + 1), label})
			}
			if len(offers) == 0 {
				fmt.Fprintln(g.p.out, text(g.ja, "No currently confirmed shares. The provider must start sharing with this node and allow discovery. Ordinary Tailscale services can use manual mode.", "現在確認できる共有はありません。提供側でこの端末への共有と共有情報の表示を開始してください。通常のTailscaleサービスには手動設定を使えます。"))
			}
			choices = append(choices, promptChoice{"r", text(g.ja, "Refresh", "更新")}, promptChoice{"m", text(g.ja, "Manual peer and ports", "相手とポートを手動で指定")}, promptChoice{"q", text(g.ja, "Cancel", "取消")})
			answer, err := g.p.askChoice(text(g.ja, "Service number / r refresh / m manual / q cancel: ", "サービス番号 / r 更新 / m 手動 / q 取消: "), choices, "")
			if err != nil {
				return err
			}
			if answer == "r" {
				continue
			}
			if answer == "m" {
				manual = true
				continue
			}
			number, err := strconv.Atoi(answer)
			if err != nil || number < 1 || number > len(offers) {
				fmt.Fprintln(g.p.out, text(g.ja, "Choose a displayed service number.", "表示したサービス番号を選んでください。"))
				continue
			}
			chosen := offers[number-1]
			g.selected = &chosen
			g.spec.PeerID, g.spec.PeerIDs, g.spec.Network, g.spec.Ports, g.spec.ExcludePorts, g.spec.Purpose = chosen.PeerID, nil, chosen.Network, chosen.Ports, "", chosen.Purpose
			g.spec.ServiceID = chosen.ID
			g.peerNames = names[chosen.PeerID]
			g.suggestLocalPort()
			return nil
		}
		fmt.Fprintln(g.p.out, text(g.ja, "Current authenticated peers:", "現在認証されている相手:"))
		choices := []promptChoice{}
		for i, peer := range peers {
			state := text(g.ja, "reachability unconfirmed", "到達未確認")
			if peer.Online {
				state = text(g.ja, "online", "オンライン")
			}
			label := fmt.Sprintf("%d. %s (%s) [%s]", i+1, displayText(peer.Name), peer.ID, state)
			fmt.Fprintln(g.p.out, label)
			choices = append(choices, promptChoice{strconv.Itoa(i + 1), label})
		}
		if len(peers) == 0 {
			fmt.Fprintln(g.p.out, text(g.ja, "No current peers. Check the network and sign-in, then refresh.", "現在の相手がありません。ネットワークとログインを確認して更新してください。"))
		}
		choices = append(choices, promptChoice{"r", text(g.ja, "Refresh", "更新")}, promptChoice{"back", text(g.ja, "Back to services", "共有サービスに戻る")}, promptChoice{"q", text(g.ja, "Cancel", "取消")})
		prompt := text(g.ja, "Peer number or exact name / r refresh / back / q cancel: ", "相手の番号・名前 / r 更新 / back 戻る / q 取消: ")
		if g.command == "share" {
			prompt = text(g.ja, "Allowed peer numbers or names (comma-separated) / r refresh / q cancel: ", "許可する相手の番号・名前（複数はカンマ区切り）/ r 更新 / q 取消: ")
		}
		answer, err := g.p.askChoice(prompt, choices, "")
		if err != nil {
			return err
		}
		if answer == "r" {
			continue
		}
		if answer == "back" {
			manual = false
			continue
		}
		selected, err := chooseGuidedPeers(peers, answer, g.command == "share")
		if err != nil {
			fmt.Fprintln(g.p.out, text(g.ja, "Choose distinct displayed numbers or unambiguous exact names.", "重複のない表示番号か、一意に決まる名前を選んでください。"))
			continue
		}
		ids, labels := []string{}, []string{}
		for _, peer := range selected {
			ids = append(ids, peer.ID)
			labels = append(labels, peer.Name)
		}
		g.selected = nil
		g.spec.ServiceID = ""
		g.peerNames = strings.Join(labels, ", ")
		if g.command == "share" {
			g.spec.PeerIDs = ids
		} else {
			g.spec.PeerID = ids[0]
			fmt.Fprintln(g.p.out, text(g.ja, "Manual service: the peer identity is checked; the remote application and port are not confirmed by discovery.", "手動のサービスです。相手のIDは確認しますが、接続先アプリとポートは共有情報では確認できていません。"))
		}
		return nil
	}
}
func chooseGuidedPeers(peers []guidedPeer, answer string, multiple bool) ([]guidedPeer, error) {
	tokens := strings.Split(answer, ",")
	if !multiple && len(tokens) != 1 {
		return nil, errors.New("choose one peer")
	}
	selected := []guidedPeer{}
	seen := map[string]bool{}
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		matches := []guidedPeer{}
		if n, err := strconv.Atoi(token); err == nil && n >= 1 && n <= len(peers) {
			matches = append(matches, peers[n-1])
		} else {
			for _, peer := range peers {
				if peer.Name == token || peer.ID == token {
					matches = append(matches, peer)
				}
			}
		}
		if len(matches) != 1 || seen[matches[0].ID] {
			return nil, errors.New("ambiguous peer selection")
		}
		seen[matches[0].ID] = true
		selected = append(selected, matches[0])
	}
	return selected, nil
}

func (g *serviceGuide) choosePurpose() error {
	choices := []promptChoice{}
	for _, preset := range servicepresets.All() {
		choices = append(choices, promptChoice{preset.ID, text(g.ja, preset.Label.EN, preset.Label.JA)})
	}
	choices = append(choices, promptChoice{"custom", text(g.ja, "Custom ports", "任意のポート")})
	for {
		answer, err := g.p.askChoice(text(g.ja, "Purpose: web / ssh / postgres / local-ai / custom [custom]: ", "用途: web / ssh / postgres / local-ai / custom [custom]: "), choices, "")
		if err != nil {
			return err
		}
		if answer == "" || answer == "custom" {
			g.spec.Purpose = "custom"
			return nil
		}
		preset, ok := servicepresets.Find(answer)
		if !ok {
			fmt.Fprintln(g.p.out, text(g.ja, "Choose one of the purposes shown above.", "表示した用途から選んでください。"))
			continue
		}
		g.spec.Purpose, g.spec.Network, g.spec.Ports = preset.Purpose, preset.Network, strconv.Itoa(preset.Port)
		if g.command == "connect" {
			g.spec.LocalPort = preset.LocalPort
		}
		fmt.Fprintln(g.p.out, text(g.ja, "This preset is an editable example. Confirm the application actually uses these ports.", "この候補は編集できる例です。アプリが実際に使うポートを確認してください。"))
		return nil
	}
}
func (g *serviceGuide) suggestLocalPort() {
	ports, err := ranges.Parse(g.spec.Ports)
	if err != nil || ports.Empty() {
		return
	}
	first := int(ports.Intervals()[0].First)
	g.spec.LocalPort = first
	if first < 1024 {
		g.spec.LocalPort = 10000 + first
	}
	if g.spec.Purpose == "ssh" {
		g.spec.LocalPort = 2222
	}
}
func (g *serviceGuide) askValue(label, current string, valid func(string) bool) (string, error) {
	for {
		message := label
		if current != "" {
			message += " [" + displayText(current) + "]"
		}
		answer, err := g.p.ask(message + ": ")
		if err != nil {
			return "", err
		}
		if answer == "" {
			answer = current
		}
		if valid(answer) {
			return answer, nil
		}
		fmt.Fprintln(g.p.out, text(g.ja, "Invalid value; correct this field or q to cancel.", "値が正しくありません。この項目を修正するか q で取り消してください。"))
	}
}
func (g *serviceGuide) editPorts() error {
	if g.selected == nil {
		network, err := g.askValue(text(g.ja, "Protocol tcp/udp", "プロトコル tcp/udp"), g.spec.Network, func(s string) bool { return s == "tcp" || s == "udp" })
		if err != nil {
			return err
		}
		g.spec.Network = network
		ports, err := g.askValue(text(g.ja, "Remote ports (share: exposed ports), e.g. 8080 or 8000-8010", "接続先ポート（共有では公開ポート）、例: 8080、8000-8010"), g.spec.Ports, func(s string) bool { p, e := ranges.Parse(s); return e == nil && !p.Empty() })
		if err != nil {
			return err
		}
		g.spec.Ports = ports
		excluded, err := g.askValue(text(g.ja, "Excluded ports (use - for none)", "除外ポート（なしは -）"), emptyDash(g.spec.ExcludePorts), func(s string) bool {
			if s == "-" {
				return true
			}
			p, e := ranges.Parse(s)
			if e != nil || p.Empty() {
				return false
			}
			selected, e := ranges.Parse(g.spec.Ports)
			if e != nil {
				return false
			}
			effective, e := selected.Excluding(p)
			return e == nil && !effective.Empty()
		})
		if err != nil {
			return err
		}
		if excluded == "-" {
			excluded = ""
		}
		g.spec.ExcludePorts = excluded
	}
	if g.command == "connect" && g.spec.LocalPort == 0 {
		g.suggestLocalPort()
	}
	label := text(g.ja, "Local entry port (1024..65535; 0 mirrors remote ports)", "ローカル入口ポート（1024〜65535、0は接続先と同じ）")
	if g.command == "share" {
		label = text(g.ja, "Application port (0 uses the exposed ports; mapping requires one port)", "アプリ側ポート（0は公開ポートと同じ。変更は単一ポートのみ）")
	}
	local, err := g.askValue(label, strconv.Itoa(g.spec.LocalPort), func(s string) bool {
		n, e := strconv.Atoi(s)
		if e != nil || n < 0 || n > 65535 {
			return false
		}
		selected, e := ranges.Parse(g.spec.Ports)
		if e != nil {
			return false
		}
		excluded, _ := ranges.Parse(g.spec.ExcludePorts)
		effective, e := selected.Excluding(excluded)
		if e != nil || effective.Empty() {
			return false
		}
		if g.command == "share" {
			return n == 0 || effective.Count() == 1 && (n < 54543 || n > 54545)
		}
		return n == 0 && effective.Intervals()[0].First >= 1024 || n >= 1024 && uint32(n)+effective.Count()-1 <= 65535
	})
	if err != nil {
		return err
	}
	g.spec.LocalPort, _ = strconv.Atoi(local)
	host, err := g.askValue(text(g.ja, "Loopback host", "ループバックのアドレス"), g.spec.LoopbackHost, func(s string) bool { return s == "127.0.0.1" || s == "::1" })
	if err != nil {
		return err
	}
	g.spec.LoopbackHost = host
	return nil
}
func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
func (g *serviceGuide) editLifetime() error {
	permanent := "until-stopped"
	if g.command == "share" {
		permanent = "until-revoked"
	}
	lifetime, err := g.askValue(text(g.ja, "Lifetime: finite or "+permanent, "期限: finite または "+permanent), g.spec.Lifetime, func(s string) bool { return s == "finite" || s == permanent })
	if err != nil {
		return err
	}
	g.spec.Lifetime = lifetime
	if lifetime != "finite" {
		g.spec.TTLSeconds = 0
		return nil
	}
	current := time.Duration(g.spec.TTLSeconds) * time.Second
	if current <= 0 {
		current = time.Hour
	}
	duration, err := g.askValue(text(g.ja, "Duration, e.g. 30m or 72h", "有効期間、例: 30m、72h"), current.String(), func(s string) bool {
		d, e := time.ParseDuration(s)
		return e == nil && validServiceLifetime("finite", d, g.command)
	})
	if err != nil {
		return err
	}
	d, _ := time.ParseDuration(duration)
	g.spec.TTLSeconds = int(d.Seconds())
	return nil
}
func (g *serviceGuide) editDiscovery() error {
	current := "n"
	if g.spec.Discoverable {
		current = "y"
	}
	answer, err := g.askValue(text(g.ja, "Advertise minimal service metadata only to selected peers? y/n", "選択した相手だけに最小限の共有情報を表示しますか？ y/n"), current, func(s string) bool { return affirmativeInput(s) || s == "n" || s == "no" || s == "いいえ" })
	if err != nil {
		return err
	}
	g.spec.Discoverable = affirmativeInput(answer)
	return nil
}
func (g *serviceGuide) suggestName() error {
	base := g.spec.Purpose + "-" + g.peerNames
	var clean []rune
	for _, r := range base {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' || r == '_' {
			clean = append(clean, r)
		} else if len(clean) > 0 && clean[len(clean)-1] != '-' {
			clean = append(clean, '-')
		}
		if len(clean) >= 40 {
			break
		}
	}
	base = strings.Trim(string(clean), "-_")
	if base == "" {
		base = g.command + "-service"
	}
	name, err := unusedServiceName(base, g.query)
	if err != nil {
		return err
	}
	g.spec.Name = name
	return g.editName()
}
func (g *serviceGuide) editName() error {
	name, err := g.askValue(text(g.ja, "Saved name (letters including Japanese, digits, - and _)", "保存名（日本語などの文字・数字・-・_）"), g.spec.Name, config.ValidName)
	if err != nil {
		return err
	}
	g.spec.Name = name
	return nil
}
func (g *serviceGuide) payload() (map[string]any, error) {
	name := g.spec.Name
	if name == "" {
		name = "review-pending"
	}
	args := []string{"--name", name, "--network", g.spec.Network, "--ports", g.spec.Ports, "--exclude", g.spec.ExcludePorts, "--local-port", strconv.Itoa(g.spec.LocalPort), "--loopback-host", g.spec.LoopbackHost, "--lifetime", g.spec.Lifetime}
	if g.spec.Lifetime == "finite" {
		args = append(args, "--ttl", fmt.Sprintf("%ds", g.spec.TTLSeconds))
	}
	if g.command == "share" {
		args = append(args, "--peers", strings.Join(g.spec.PeerIDs, ","), "--discoverable="+strconv.FormatBool(g.spec.Discoverable))
	} else {
		args = append(args, "--peer", g.spec.PeerID)
	}
	if g.selected != nil {
		args = append(args, "--service-id", g.selected.ID, "--service-revision", g.selected.Revision)
	}
	payload, err := servicePayload(g.command, args, g.ja, io.Discard)
	if err != nil {
		return nil, err
	}
	payload["purpose"] = g.spec.Purpose
	return payload, nil
}
func (g *serviceGuide) review() {
	fmt.Fprintln(g.p.out, text(g.ja, "Review before starting:", "開始前の確認:"))
	fmt.Fprintf(g.p.out, "  %s: %s\n", text(g.ja, "Saved name", "保存名"), displayText(g.spec.Name))
	fmt.Fprintf(g.p.out, "  %s: %s\n", text(g.ja, "Peer", "相手"), displayText(g.peerNames))
	fmt.Fprintf(g.p.out, "  %s: %s / %s %s; %s: %s\n", text(g.ja, "Service", "サービス"), displayText(g.spec.Purpose), g.spec.Network, g.spec.Ports, text(g.ja, "excluded", "除外"), emptyDash(g.spec.ExcludePorts))
	fmt.Fprintf(g.p.out, "  %s: %s; %s: %s", text(g.ja, "Planned local mapping", "予定のローカル接続先"), plannedLocalMapping(g.spec, g.command == "share"), text(g.ja, "lifetime", "期限"), g.spec.Lifetime)
	if g.spec.Lifetime == "finite" {
		fmt.Fprintf(g.p.out, " (%s)", (time.Duration(g.spec.TTLSeconds) * time.Second).String())
	}
	fmt.Fprintln(g.p.out)
	if g.command == "share" {
		fmt.Fprintf(g.p.out, "  %s: %s; %s: %t\n", text(g.ja, "Allowed IDs", "許可するID"), strings.Join(g.spec.PeerIDs, ", "), text(g.ja, "discovery to these peers only", "この相手だけに共有情報を表示"), g.spec.Discoverable)
	}
	if g.selected != nil {
		fmt.Fprintf(g.p.out, "  %s: %s; %s: %s\n", text(g.ja, "Advertised grant", "選択した共有"), g.selected.ID, text(g.ja, "checked", "確認時刻"), g.selected.CheckedAt.Format(time.RFC3339))
		if !g.selected.ExpiresAt.IsZero() {
			fmt.Fprintf(g.p.out, "  %s: %s\n", text(g.ja, "Remote grant expires", "相手の共有期限"), g.selected.ExpiresAt.Format(time.RFC3339))
		}
	}
	fmt.Fprintln(g.p.out, text(g.ja, "  The definition will be saved and started. Only a separately approved startup selection may start it on a future launch. Application success is unverified.", "  設定を保存して開始します。次回以降の起動で開始するのは、別途起動を許可した対象だけです。アプリの動作成功は未確認です。"))
}

func plannedLocalMapping(spec core.ServiceSpec, share bool) string {
	ports, err := ranges.ParseWithLimit(spec.Ports, 65535)
	if err != nil {
		return "—"
	}
	if spec.ExcludePorts != "" {
		excluded, err := ranges.ParseWithLimit(spec.ExcludePorts, 65535)
		if err != nil {
			return "—"
		}
		ports, err = ports.Excluding(excluded)
		if err != nil {
			return "—"
		}
	}
	if share {
		reserved, _ := ranges.Parse("54543-54545")
		ports, err = ports.Excluding(reserved)
		if err != nil {
			return "—"
		}
	}
	if ports.Empty() {
		return "—"
	}
	local := ports.String()
	if spec.LocalPort != 0 {
		local = strconv.Itoa(spec.LocalPort)
		if ports.Count() > 1 {
			local += "-" + strconv.Itoa(spec.LocalPort+int(ports.Count())-1)
		}
	}
	host := spec.LoopbackHost
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, local)
}

func discoveryCommand(args []string, ja bool, out io.Writer, request actionRequest) error {
	f := commandFlags("discover", ja, out)
	peer := f.String("peer", "", text(ja, "refresh only this authenticated peer identity", "この認証済みの相手だけを更新"))
	f.Bool("json", false, text(ja, "machine-readable authenticated observations", "認証済みの応答を機械向けに表示"))
	if err := parseFlags(f, args, ja); err != nil {
		return err
	}
	if *peer != "" && !config.ValidPeerID(*peer) {
		return errors.New(text(ja, "Choose a valid --peer ID from status", "status に表示された有効な --peer ID を指定してください"))
	}
	payload := map[string]string{}
	if *peer != "" {
		payload["peerId"] = *peer
	}
	return request("discovery.refresh", payload)
}

// A long human review may outlive the observation window. Refresh only the
// already reviewed grant, accepting a later expiry but never another scope.
func (g *serviceGuide) refreshSelected() error {
	if g.selected == nil {
		return nil
	}
	var refreshed struct {
		Services []guidedOffer `json:"services"`
	}
	if err := g.queryAction("discovery.refresh", map[string]string{"peerId": g.selected.PeerID}, &refreshed); err != nil {
		return err
	}
	old := *g.selected
	now := time.Now()
	for _, current := range refreshed.Services {
		if current.ID == old.ID && current.PeerID == old.PeerID && current.Network == old.Network && current.Ports == old.Ports && current.Purpose == old.Purpose && current.Lifetime == old.Lifetime && !current.ExpiresAt.Before(old.ExpiresAt) && selectableGuidedOffer(current, now) {
			g.selected = &current
			return nil
		}
	}
	return errors.New(text(g.ja, "The reviewed shared service changed or could not be confirmed. Choose s to select it again.", "確認した共有サービスが変わったか、確認できませんでした。s で選び直してください。"))
}

func selectableGuidedOffer(offer guidedOffer, now time.Time) bool {
	if offer.Revision == "" || !config.ValidPeerID(offer.ID) || !config.ValidPeerID(offer.PeerID) || (offer.Network != "tcp" && offer.Network != "udp") || offer.Application != "unverified" || offer.CheckedAt.IsZero() || now.Sub(offer.CheckedAt) > 15*time.Second || offer.CheckedAt.After(now.Add(5*time.Second)) {
		return false
	}
	if !((offer.Lifetime == "until-revoked" && offer.ExpiresAt.IsZero()) || (offer.Lifetime == "finite" || offer.Lifetime == "") && offer.ExpiresAt.After(now)) {
		return false
	}
	ports, err := ranges.Parse(offer.Ports)
	return err == nil && !ports.Empty()
}
