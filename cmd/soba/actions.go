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
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/ranges"
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
	name := f.String("name", "", text(ja, "new service name; default is an unused name for the selected scope", "新しい保存名（省略時は対象とポートから未使用名を生成）"))
	preset := f.String("preset", "", text(ja, "ssh (TCP 22; local 2222) or web (TCP 80; local 8080)", "ssh（TCP 22・入口2222）または web（TCP 80・入口8080）"))
	network := f.String("network", "tcp", text(ja, "tcp or udp", "tcp または udp"))
	ports := f.String("ports", "", text(ja, "port list or ranges; overrides a preset's target ports", "ポート一覧・範囲（プリセットより優先）"))
	exclude := f.String("exclude", "", text(ja, "excluded ports", "除外するポート"))
	var peer, peers string
	var local int
	var discover bool
	if command == "share" {
		f.StringVar(&peers, "peers", "", text(ja, "explicit allowed peer IDs, separated by commas", "許可する相手のID（カンマ区切り）"))
		f.BoolVar(&discover, "discoverable", false, text(ja, "advertise minimal metadata only to allowed peers", "許可した相手にだけ最小限の共有情報を表示"))
	} else {
		f.StringVar(&peer, "peer", "", text(ja, "explicit target peer ID", "接続先の相手のID"))
		f.IntVar(&local, "local-port", 0, text(ja, "first local port; default uses target ports or the chosen preset", "入口の先頭ポート（既定は接続先と同じ番号、またはプリセット）"))
	}
	ttl := f.Duration("ttl", time.Hour, text(ja, "permission lifetime, 1s to 24h", "許可の有効期間（1秒〜24時間）"))
	if err := parseFlags(f, args, ja); err != nil {
		return nil, err
	}
	explicit := map[string]bool{}
	f.Visit(func(flag *flag.Flag) { explicit[flag.Name] = true })
	if explicit["name"] && *name == "" {
		return nil, errors.New(text(ja, "--name cannot be empty; omit it to choose an unused name", "--name は空にできません。省略すると未使用の名前を選びます"))
	}
	purpose := "custom"
	switch *preset {
	case "":
	case "ssh", "web":
		purpose = *preset
		target, entry := "22", 2222
		if *preset == "web" {
			target, entry = "80", 8080
		}
		if !explicit["network"] {
			*network = "tcp"
		}
		if !explicit["ports"] {
			*ports = target
		}
		if command == "connect" && !explicit["local-port"] {
			local = entry
		}
	default:
		return nil, errors.New(text(ja, "--preset must be ssh or web", "--preset は ssh または web を指定してください"))
	}
	if *ports == "" {
		return nil, errors.New(text(ja, "Specify --ports or choose --preset ssh|web", "--ports または --preset ssh|web を指定してください"))
	}
	if *network != "tcp" && *network != "udp" {
		return nil, errors.New(text(ja, "--network must be tcp or udp", "--network は tcp または udp を指定してください"))
	}
	if *ttl < time.Second || *ttl > 24*time.Hour || *ttl%time.Second != 0 {
		return nil, errors.New(text(ja, "--ttl must be a whole number of seconds from 1s to 24h", "--ttl は1秒〜24時間の整数秒で指定してください"))
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
		reserved, _ := ranges.Parse("54543-54545")
		effective, err = effective.Excluding(reserved)
		if err != nil || effective.Empty() {
			return nil, errors.New(text(ja, "Selected ports are reserved; choose application ports", "選択したポートは予約されています。アプリのポートを指定してください"))
		}
	}
	if command == "connect" || *network == "udp" {
		if effective.Count() > ranges.MaxMaterializedListeners {
			return nil, errors.New(text(ja, "Connections and UDP shares support at most 64 effective ports; narrow --ports", "接続とUDP共有は最大64ポートです。--ports を絞ってください"))
		}
	}
	if command == "connect" {
		if peer == "" {
			return nil, errors.New(text(ja, "--peer is required", "--peer を指定してください"))
		}
		if local < 0 || local > 65535 || (local != 0 && (local < 1024 || uint32(local)+effective.Count()-1 > 65535)) {
			return nil, errors.New(text(ja, "Choose --local-port in 1024..65535 with room for every selected port", "--local-port は全ポートが収まる1024〜65535の範囲で指定してください"))
		}
		if local == 0 && effective.Intervals()[0].First < 1024 {
			return nil, errors.New(text(ja, "Target ports below 1024 need --local-port 1024..65535; try --preset ssh or web", "1024未満の接続先には --local-port 1024..65535 が必要です。--preset ssh または web も使えます"))
		}
	}
	payload := map[string]any{"network": *network, "ports": selected.String(), "excludePorts": excluded.String(), "localPort": local, "ttlSeconds": int(ttl.Seconds()), "purpose": purpose, "discoverable": discover}
	if command == "share" {
		if peers == "" {
			return nil, errors.New(text(ja, "--peers is required", "--peers を指定してください"))
		}
		ids := strings.Split(peers, ",")
		seen := map[string]bool{}
		if len(ids) > 32 {
			return nil, errors.New(text(ja, "Choose at most 32 allowed peers", "許可する相手は最大32端末です"))
		}
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
	}
	if *name == "" {
		// Identity and scope are part of the default name, so changing a target does
		// not silently overwrite a different saved service. TTL is intentionally not
		// part of the base name. The caller chooses an unused suffix, and Core
		// rejects concurrent name conflicts; a new command never overwrites a rule.
		scope := map[string]any{"command": command, "network": *network, "ports": selected.String(), "exclude": excluded.String(), "localPort": local, "peerId": payload["peerId"], "peerIds": payload["peerIds"]}
		raw, _ := json.Marshal(scope)
		sum := sha256.Sum256(raw)
		*name = command + "-" + *network + "-" + hex.EncodeToString(sum[:6])
	}
	if !config.ValidName(*name) {
		return nil, errors.New(text(ja, "--name must use 1..64 letters, digits, hyphens or underscores", "--name は1〜64文字の英数字・ハイフン・アンダースコアで指定してください"))
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
