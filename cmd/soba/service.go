package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
)

type savedService struct {
	ID, Backend, Name, Direction, Network           string
	Ports, ExcludePorts, PeerID, Purpose, ServiceID string
	Lifetime, LoopbackHost, ServiceRevision         string
	PeerIDs                                         []string
	LocalPort, TTLSeconds                           int
	Discoverable                                    bool
}

type serviceConfiguration struct {
	Configuration savedService
	Revision      string
	Active        bool
}
type commandQuery func(string, any, any) error

func serviceCommand(args []string, ja bool, out io.Writer, state stateQuery, query commandQuery, request actionRequest) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		_, err := io.WriteString(out, text(ja, serviceHelpEN, serviceHelpJA)+"\n")
		return err
	}
	operation := args[0]
	if operation != "show" && operation != "copy" && operation != "restart" {
		return errors.New(text(ja, "Use service show, copy or restart", "service show、copy、restart を使ってください"))
	}
	if len(args) < 2 {
		return usageError(ja, "service "+operation+" SERVICE_ID [OPTIONS]")
	}
	if args[1] == "--help" || args[1] == "-h" {
		return usageHelp(out, ja, "service "+operation+" SERVICE_ID [--name NAME] [--network tcp|udp] [--ports PORTS] [--exclude PORTS] [--peer ID | --peers IDS] [--local-port PORT] [--loopback-host 127.0.0.1|::1] [--lifetime MODE] [--ttl DURATION] [--discoverable=true|false] [--backend tailnet|lan]")
	}
	if operation == "show" {
		f := commandFlags("service show NAME_OR_ID", ja, out)
		structured := f.Bool("json", false, text(ja, "machine-readable saved definition", "保存済み定義の機械向け出力"))
		if err := parseFlags(f, args[2:], ja); err != nil {
			return err
		}
		resolved, err := resolveServiceReferences([]string{args[1]}, ja, query)
		if err != nil {
			return err
		}
		var raw json.RawMessage
		if err = query("service.config", map[string]string{"id": resolved.IDs[0]}, &raw); err != nil {
			return err
		}
		var view struct {
			Configuration core.ServiceSpec `json:"configuration"`
			Active        bool             `json:"active"`
		}
		if err = json.Unmarshal(raw, &view); err != nil {
			return err
		}
		if err = checkResolvedName(resolved, view.Configuration.ID, view.Configuration.Name, ja); err != nil {
			return err
		}
		if *structured {
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			return encoder.Encode(raw)
		}
		fmt.Fprintln(out, text(ja, "Saved definition; inspect settings for current endpoints and readiness.", "保存済み定義です。現在の接続先と準備状況は settings で確認してください。"))
		writeHumanService(out, ja, humanFromSpec(view.Configuration), nil)
		fmt.Fprintf(out, "%s: %s\n", text(ja, "Active runtime record", "稼働中の記録"), humanYesNo(ja, view.Active))
		return nil
	}

	// Parse overrides before reading state; even invalid commands remain offline.
	f := commandFlags("service "+operation+" SERVICE_ID", ja, out)
	values := map[string]*string{}
	for _, name := range []string{"name", "network", "ports", "exclude", "peer", "peers", "backend", "loopback-host", "lifetime"} {
		values[name] = f.String(name, "", text(ja, "override the saved "+name, "保存済みの "+name+" を変更"))
	}
	local := f.Int("local-port", 0, text(ja, "override the saved listener or application port", "保存済みの入口またはアプリ側ポートを変更"))
	ttl := f.Duration("ttl", 0, text(ja, "override the saved permission lifetime", "保存済みの許可期間を変更"))
	discover := f.Bool("discoverable", false, text(ja, "override discovery visibility (true or false)", "共有情報の表示を変更（true または false）"))
	if err := parseFlags(f, args[2:], ja); err != nil {
		return err
	}
	changed := map[string]bool{}
	f.Visit(func(value *flag.Flag) { changed[value.Name] = true })
	resolved, err := resolveServiceReferences([]string{args[1]}, ja, query)
	if err != nil {
		return err
	}
	args = append([]string(nil), args...)
	args[1] = resolved.IDs[0]
	var snapshot serviceConfiguration
	if err := query("service.config", map[string]string{"id": args[1]}, &snapshot); err != nil {
		return err
	}
	if err := checkResolvedName(resolved, snapshot.Configuration.ID, snapshot.Configuration.Name, ja); err != nil {
		return err
	}
	config := snapshot.Configuration
	if config.ID != args[1] || (config.Direction != "share" && config.Direction != "forward") || len(snapshot.Revision) != 64 {
		return errors.New(text(ja, "Saved configuration is incomplete; refresh service show before retrying", "保存済み設定が不完全です。service show で再確認してください"))
	}
	if operation == "restart" && snapshot.Active {
		return errors.New(text(ja, "Service is active; stop it explicitly with soba stop-service before restarting", "サービスは稼働中です。再開始の前に soba stop-service で明示的に停止してください"))
	}
	backend := config.Backend
	if changed["backend"] {
		selected := *values["backend"]
		if backend != "" && selected != backend {
			return errors.New(text(ja, "A saved service cannot change network backends; create a fresh share or connection", "保存済みサービスのネットワークは変更できません。新しい共有・接続を作成してください"))
		}
		backend = selected
	}
	if backend != "tailnet" && backend != "lan" {
		return errors.New(text(ja, "This saved service has no known backend; review it and specify --backend tailnet|lan", "この保存済みサービスはネットワークが不明です。確認して --backend tailnet|lan を指定してください"))
	}
	for key, value := range map[string]*string{"name": &config.Name, "network": &config.Network, "ports": &config.Ports, "exclude": &config.ExcludePorts, "peer": &config.PeerID, "loopback-host": &config.LoopbackHost, "lifetime": &config.Lifetime} {
		if changed[key] {
			*value = *values[key]
		}
	}
	if changed["peers"] {
		config.PeerIDs = strings.Split(*values["peers"], ",")
	}
	if changed["local-port"] {
		config.LocalPort = *local
	}
	if changed["lifetime"] && config.Lifetime != "finite" && !changed["ttl"] {
		config.TTLSeconds = 0
	}
	if changed["ttl"] {
		if *ttl%time.Second != 0 {
			return errors.New(text(ja, "--ttl must use whole seconds", "--ttl は整数秒で指定してください"))
		}
		config.TTLSeconds = int(ttl.Seconds())
		if !changed["lifetime"] {
			config.Lifetime = "finite"
		}
	}
	if changed["discoverable"] {
		config.Discoverable = *discover
	}
	if config.ServiceID != "" && (config.Network != snapshot.Configuration.Network || config.Ports != snapshot.Configuration.Ports || config.ExcludePorts != snapshot.Configuration.ExcludePorts || config.PeerID != snapshot.Configuration.PeerID) {
		return errors.New(text(ja, "This saved connection follows an advertised service; keep its peer, protocol and complete ports, or create an explicit manual connection with soba connect", "この接続は共有サービスに対応しています。相手・プロトコル・ポート全体を維持するか、soba connect で明示的な手動接続を作成してください"))
	}
	command := "share"
	if config.Direction == "forward" {
		command = "connect"
	}
	if (command == "share" && changed["peer"]) || (command == "connect" && (changed["peers"] || changed["discoverable"])) {
		return errors.New(text(ja, "Override flags do not match this service direction; use service show first", "変更する指定がこのサービスの種類と合いません。先に service show を確認してください"))
	}
	if operation == "copy" && !changed["name"] {
		base := config.Name
		if len(base) > 40 {
			base = base[:40]
		}
		name, err := unusedServiceName(base+"-copy", state)
		if err != nil {
			return err
		}
		config.Name = name
	}
	if config.Lifetime == "" {
		config.Lifetime = "finite"
	}
	if config.LoopbackHost == "" {
		config.LoopbackHost = "127.0.0.1"
	}
	inputs := []string{"--name", config.Name, "--network", config.Network, "--ports", config.Ports, "--exclude", config.ExcludePorts, "--ttl", fmt.Sprintf("%ds", config.TTLSeconds), "--lifetime", config.Lifetime, "--loopback-host", config.LoopbackHost, "--local-port", strconv.Itoa(config.LocalPort)}
	if command == "share" {
		inputs = append(inputs, "--peers", strings.Join(config.PeerIDs, ","), "--discoverable="+strconv.FormatBool(config.Discoverable))
	} else {
		inputs = append(inputs, "--peer", config.PeerID)
	}
	payload, err := servicePayload(command, inputs, ja, out)
	if err != nil {
		return err
	}
	payload["backend"] = backend
	payload["purpose"] = config.Purpose
	if config.ServiceID != "" {
		payload["serviceId"] = config.ServiceID
		payload["serviceRevision"] = config.ServiceRevision
	}
	if operation == "restart" {
		payload["replaceId"] = config.ID
		payload["expectedRevision"] = snapshot.Revision
	}
	return request("service."+command, payload)
}
