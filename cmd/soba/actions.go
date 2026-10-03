package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/ranges"
	"github.com/webkaz-labs/sobalink/internal/servicepresets"
)

type actionRequest func(string, any) error
type stateQuery func(any) error

type preferenceState struct {
	Settings struct {
		ReceiveDirectory string `json:"receiveDirectory"`
	} `json:"settings"`
	Peers []struct {
		ID       string `json:"id"`
		Trusted  bool   `json:"trusted"`
		Autosave struct {
			Enabled   bool   `json:"enabled"`
			Paused    bool   `json:"paused"`
			Directory string `json:"directory"`
		} `json:"autosave"`
	} `json:"peers"`
}

func preferenceCommand(command string, args []string, ja bool, out io.Writer, query stateQuery, request actionRequest) error {
	if command == "receive-dir" {
		if len(args) > 1 {
			return usageError(ja, commandUsage[command])
		}
		if len(args) == 0 {
			var state preferenceState
			if err := query(&state); err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(map[string]string{"receiveDirectory": state.Settings.ReceiveDirectory})
		}
		directory := ""
		if args[0] != "--clear" {
			if strings.HasPrefix(args[0], "--") {
				return usageError(ja, commandUsage[command])
			}
			var err error
			directory, err = filepath.Abs(args[0])
			if err != nil {
				return err
			}
		}
		return request("settings.update", map[string]string{"receiveDirectory": directory})
	}
	if command == "reconnect" {
		if len(args) != 1 {
			return usageError(ja, commandUsage[command])
		}
		return request("peer.reconnect", map[string]string{"peerId": args[0]})
	}
	var enable, disable bool
	var directory string
	if command == "autosave" {
		f := commandFlags("autosave PEER_ID", ja, out)
		f.BoolVar(&enable, "on", false, text(ja, "allow automatic saving from this approved peer", "許可済みの相手からの自動保存を有効にする"))
		f.BoolVar(&disable, "off", false, text(ja, "require manual acceptance; preserve any peer pause", "手動承認に戻す（相手の一時停止は維持）"))
		f.StringVar(&directory, "directory", "", text(ja, "receive folder; defaults to the saved peer folder or receive-dir", "保存先（省略時は相手の保存先または receive-dir の設定）"))
		if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
			return f.Parse(args)
		}
		if len(args) < 1 {
			return usageError(ja, commandUsage[command])
		}
		if err := parseFlags(f, args[1:], ja); err != nil {
			return err
		}
		if enable == disable || (disable && directory != "") {
			return usageError(ja, commandUsage[command])
		}
	} else if len(args) != 1 {
		return usageError(ja, commandUsage[command])
	}
	payload := map[string]any{"peerId": args[0]}
	switch command {
	case "pause":
		payload["paused"] = true
	case "resume":
		payload["paused"] = false
	case "autosave":
		payload["enabled"] = enable
		if directory != "" {
			absolute, err := filepath.Abs(directory)
			if err != nil {
				return err
			}
			payload["directory"] = absolute
		}
	}
	return request("peer.autosave", payload)
}

func servicePayload(command string, args []string, ja bool, out io.Writer) (map[string]any, error) {
	f := commandFlags(command, ja, out)
	f.Bool("json", false, text(ja, "machine output; never prompt", "機械向け出力（対話なし）"))
	f.Bool("interactive", false, text(ja, "guided selection, editable review and explicit confirmation", "選択・編集・確認を案内する"))
	name := f.String("name", "", text(ja, "new service name; default is an unused name for the selected scope", "新しい保存名（省略時は対象とポートから未使用名を生成）"))
	preset := f.String("preset", "", servicePresetHelp(ja))
	purposeFlag := f.String("purpose", "", text(ja, "explicit purpose for a reviewed service (without changing ports)", "確認したサービスの用途（ポートは変えない）"))
	network := f.String("network", "tcp", text(ja, "tcp or udp", "tcp または udp"))
	ports := f.String("ports", "", text(ja, "port list or ranges; overrides a preset's target ports", "ポート一覧・範囲（プリセットより優先）"))
	exclude := f.String("exclude", "", text(ja, "excluded ports", "除外するポート"))
	var peer, peers, peerName, peerNames, serviceID, serviceRevision string
	local := f.Int("local-port", 0, text(ja, "local listener for connect; application target for a single-port share", "接続の入口、または単一ポート共有のアプリ側ポート"))
	loopback := f.String("loopback-host", "127.0.0.1", text(ja, "numeric loopback only: 127.0.0.1 or ::1", "数値のループバックのみ: 127.0.0.1 または ::1"))
	lifetime := f.String("lifetime", "", text(ja, "finite, until-stopped (connect), or until-revoked (share)", "finite、until-stopped（接続）、until-revoked（共有）"))
	var discover bool
	if command == "share" {
		f.StringVar(&peers, "peers", "", text(ja, "explicit allowed peer IDs, separated by commas", "許可する相手のID（カンマ区切り）"))
		f.StringVar(&peerNames, "peer-names", "", text(ja, "exact current peer names, separated by commas", "現在の相手の正確な名前（カンマ区切り）"))
		f.BoolVar(&discover, "discoverable", false, text(ja, "advertise minimal metadata only to allowed peers", "許可した相手にだけ最小限の共有情報を表示"))
	} else {
		f.StringVar(&peer, "peer", "", text(ja, "explicit target peer ID", "接続先の相手のID"))
		f.StringVar(&peerName, "peer-name", "", text(ja, "exact current peer name; ambiguous names are rejected", "現在の相手の正確な名前（一意でない名前は拒否）"))
		f.StringVar(&serviceID, "service-id", "", text(ja, "bind to this advertised service grant", "この共有サービスに接続を固定する"))
		f.StringVar(&serviceRevision, "service-revision", "", text(ja, "exact reviewed revision from availableServices", "availableServices で確認した正確な更新情報"))
	}
	ttl := f.Duration("ttl", time.Hour, text(ja, "finite duration in whole seconds, for example 30m or 72h; implies finite", "有限の有効期間（整数秒、例: 30m、72h）。finite を指定"))
	if err := parseFlags(f, args, ja); err != nil {
		return nil, err
	}
	explicit := map[string]bool{}
	f.Visit(func(flag *flag.Flag) { explicit[flag.Name] = true })
	if explicit["name"] && *name == "" {
		return nil, errors.New(text(ja, "--name cannot be empty; omit it to choose an unused name", "--name は空にできません。省略すると未使用の名前を選びます"))
	}
	if (serviceID == "") != (serviceRevision == "") || serviceID != "" && (!config.ValidPeerID(serviceID) || strings.ContainsAny(serviceRevision, "\r\n\t ")) {
		return nil, errors.New(text(ja, "Use --service-id and --service-revision together from the same reviewed availableServices row", "同じ availableServices の行で確認した --service-id と --service-revision を一緒に指定してください"))
	}
	if peerName != "" && peer != "" || peerNames != "" && peers != "" {
		return nil, errors.New(text(ja, "Choose explicit peer IDs or peer names, not both", "相手のIDと名前は同時に指定できません"))
	}
	if peerName != "" {
		peer = peerName
	}
	if peerNames != "" {
		peers = peerNames
	}
	purpose := "custom"
	if *preset != "" {
		item, ok := servicepresets.Find(*preset)
		if !ok {
			return nil, errors.New(text(ja, "Choose --preset web|ssh|postgres|local-ai", "--preset web|ssh|postgres|local-ai を指定してください"))
		}
		purpose = item.Purpose
		if !explicit["network"] {
			*network = item.Network
		}
		if !explicit["ports"] {
			*ports = strconv.Itoa(item.Port)
		}
		if command == "connect" && !explicit["local-port"] {
			*local = item.LocalPort
		}
	}
	if explicit["purpose"] {
		switch *purposeFlag {
		case "", "generic", "web", "ssh", "postgres", "local-ai", "rustdesk", "custom", "db", "ai", "desktop":
			purpose = *purposeFlag
		default:
			return nil, errors.New(text(ja, "--purpose must be web, ssh, postgres, local-ai, rustdesk or custom", "--purpose は web・ssh・postgres・local-ai・rustdesk・custom を指定してください"))
		}
	}
	if *ports == "" {
		return nil, errors.New(text(ja, "Specify --ports or choose --preset web|ssh|postgres|local-ai", "--ports または --preset web|ssh|postgres|local-ai を指定してください"))
	}
	if *loopback != "127.0.0.1" && *loopback != "::1" {
		return nil, errors.New(text(ja, "--loopback-host must be 127.0.0.1 or ::1", "--loopback-host は 127.0.0.1 または ::1 を指定してください"))
	}
	if !explicit["lifetime"] {
		*lifetime = "finite"
		if command == "connect" && !explicit["ttl"] {
			*lifetime = "until-stopped"
		}
	}
	if *lifetime != "finite" && !explicit["ttl"] {
		*ttl = 0
	}
	if !validServiceLifetime(*lifetime, *ttl, command) {
		return nil, errors.New(text(ja, "Choose a positive whole-second --ttl for finite lifetime, or use --lifetime until-stopped (connect) / until-revoked (share) without --ttl", "有限の期間には正の整数秒の --ttl を指定してください。無期限は --ttl なしで --lifetime until-stopped（接続）/ until-revoked（共有）を指定します"))
	}
	if *network != "tcp" && *network != "udp" {
		return nil, errors.New(text(ja, "--network must be tcp or udp", "--network は tcp または udp を指定してください"))
	}
	selected, err := ranges.Parse(*ports)
	if err != nil {
		return nil, errors.New(text(ja, "Invalid --ports; use 22,80 or 8000-8010 with ports 1..65535", "--ports は 22,80 や 8000-8010 の形式で、1〜65535を指定してください"))
	}
	var excluded ranges.Set
	if *exclude != "" {
		excluded, err = ranges.Parse(*exclude)
		if err != nil {
			return nil, errors.New(text(ja, "Invalid --exclude port list or range", "--exclude のポート一覧・範囲が正しくありません"))
		}
	}
	effective, err := selected.Excluding(excluded)
	if err != nil || effective.Empty() {
		return nil, errors.New(text(ja, "No ports remain after exclusions", "除外後にポートが残りません"))
	}
	if command == "share" {
		if *local < 0 || *local > 65535 || (*local != 0 && (effective.Count() != 1 || (*local >= 54543 && *local <= 54545))) {
			return nil, errors.New(text(ja, "A mapped share requires one exposed port and an application --local-port in 1..65535 outside reserved ports", "共有先を変える場合は公開ポートを1個にし、予約ポート以外の --local-port 1〜65535 を指定してください"))
		}
		reserved, _ := ranges.Parse("54543-54545")
		effective, err = effective.Excluding(reserved)
		if err != nil || effective.Empty() {
			return nil, errors.New(text(ja, "Selected ports are reserved; choose application ports", "選択したポートは予約されています。アプリのポートを指定してください"))
		}
	}
	if command == "connect" {
		if peer == "" {
			return nil, errors.New(text(ja, "--peer is required", "--peer を指定してください"))
		}
		if *local < 0 || *local > 65535 || (*local != 0 && (*local < 1024 || uint32(*local)+effective.Count()-1 > 65535)) {
			return nil, errors.New(text(ja, "Choose --local-port in 1024..65535 with room for every selected port", "--local-port は全ポートが収まる1024〜65535の範囲で指定してください"))
		}
		if *local == 0 && effective.Intervals()[0].First < 1024 {
			return nil, errors.New(text(ja, "Target ports below 1024 need --local-port 1024..65535; try --preset ssh or web", "1024未満の接続先には --local-port 1024..65535 が必要です。--preset ssh または web も使えます"))
		}
	}
	payload := map[string]any{"network": *network, "ports": selected.String(), "excludePorts": excluded.String(), "localPort": *local, "loopbackHost": *loopback, "lifetime": *lifetime, "ttlSeconds": int(ttl.Seconds()), "purpose": purpose, "discoverable": discover}
	if command == "share" {
		if peers == "" {
			return nil, errors.New(text(ja, "--peers is required", "--peers を指定してください"))
		}
		ids := strings.Split(peers, ",")
		seen := map[string]bool{}
		for i := range ids {
			ids[i] = strings.TrimSpace(ids[i])
			if ids[i] == "" || seen[ids[i]] {
				return nil, errors.New(text(ja, "--peers requires distinct, nonempty peer IDs", "--peers のIDは空欄や重複がないように指定してください"))
			}
			seen[ids[i]] = true
		}
		slices.Sort(ids)
		payload["peerIds"] = ids
	} else {
		payload["peerId"] = peer
		if serviceID != "" {
			payload["serviceId"], payload["serviceRevision"] = serviceID, serviceRevision
		}
	}
	if *name == "" {
		// Identity and scope are part of the default name, so changing a target does
		// not silently overwrite a different saved service. TTL is intentionally not
		// part of the base name. The caller chooses an unused suffix, and Core
		// rejects concurrent name conflicts; a new command never overwrites a rule.
		scope := map[string]any{"command": command, "network": *network, "ports": selected.String(), "exclude": excluded.String(), "localPort": *local, "loopbackHost": *loopback, "peerId": payload["peerId"], "peerIds": payload["peerIds"]}
		raw, _ := json.Marshal(scope)
		sum := sha256.Sum256(raw)
		*name = command + "-" + *network + "-" + hex.EncodeToString(sum[:6])
	}
	if !config.ValidName(*name) {
		return nil, errors.New(text(ja, "--name must use 1..64 Unicode letters, digits, hyphens or underscores", "--name は1〜64文字の日本語などの文字・数字・ハイフン・アンダースコアで指定してください"))
	}
	if peerName != "" {
		payload["peerName"] = peerName
	}
	if peerNames != "" {
		payload["peerNames"] = splitPeerNames(peerNames)
	}
	payload["name"] = *name
	return payload, nil
}

func hasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == "--"+name || arg == "-"+name || strings.HasPrefix(arg, "--"+name+"=") || strings.HasPrefix(arg, "-"+name+"=") {
			return true
		}
	}
	return false
}
func unusedServiceName(base string, query stateQuery) (string, error) {
	var snapshot struct{ Services, Shares []struct{ Name string } }
	if err := query(&snapshot); err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, items := range [][]struct{ Name string }{snapshot.Services, snapshot.Shares} {
		for _, item := range items {
			taken[item.Name] = true
		}
	}
	if !taken[base] {
		return base, nil
	}
	for suffix := 2; suffix <= 10000; suffix++ {
		candidate := fmt.Sprintf("%s-%d", base, suffix)
		if !taken[candidate] {
			return candidate, nil
		}
	}
	return "", errors.New("could not select an unused service name")
}

func validServiceLifetime(lifetime string, ttl time.Duration, command string) bool {
	if lifetime == "finite" {
		return ttl >= time.Second && ttl%time.Second == 0
	}
	return ttl == 0 && ((command == "connect" && lifetime == "until-stopped") || (command == "share" && lifetime == "until-revoked"))
}

func servicePresetHelp(ja bool) string {
	var examples []string
	for _, item := range servicepresets.All() {
		label := text(ja, item.Label.EN, item.Label.JA)
		examples = append(examples, fmt.Sprintf("%s: %s (%s %d; %s %d)", item.ID, label, strings.ToUpper(item.Network), item.Port, text(ja, "local", "入口"), item.LocalPort))
	}
	return text(ja, "editable examples only; verify your application: ", "入力用の例です。アプリの設定を確認してください: ") + strings.Join(examples, "; ")
}
