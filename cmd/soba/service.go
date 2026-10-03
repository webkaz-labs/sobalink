package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

type savedService struct {
	ID, Backend, Name, Direction, Network, Ports, ExcludePorts, PeerID, Purpose, ServiceID string
	PeerIDs                                                                                []string
	LocalPort, TTLSeconds                                                                  int
	Discoverable                                                                           bool
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
		return usageHelp(out, ja, "service "+operation+" SERVICE_ID [--name NAME] [--network tcp|udp] [--ports PORTS] [--exclude PORTS] [--peer ID | --peers IDS] [--local-port PORT] [--ttl DURATION] [--discoverable=true|false] [--backend tailnet|lan]")
	}
	if operation == "show" {
		if len(args) != 2 {
			return usageError(ja, "service show SERVICE_ID")
		}
		return request("service.config", map[string]string{"id": args[1]})
	}
	// Parse overrides before reading state; even invalid commands remain offline.
	f := commandFlags("service "+operation+" SERVICE_ID", ja, out)
	values := map[string]*string{}
	for _, name := range []string{"name", "network", "ports", "exclude", "peer", "peers", "backend"} {
		values[name] = f.String(name, "", text(ja, "override the saved "+name, "保存済みの "+name+" を変更"))
	}
	local := f.Int("local-port", 0, text(ja, "override the saved local starting port", "保存済みの入口先頭ポートを変更"))
	ttl := f.Duration("ttl", 0, text(ja, "override the saved permission lifetime", "保存済みの許可期間を変更"))
	discover := f.Bool("discoverable", false, text(ja, "override discovery visibility (true or false)", "共有情報の表示を変更（true または false）"))
	if err := parseFlags(f, args[2:], ja); err != nil {
		return err
	}
	changed := map[string]bool{}
	f.Visit(func(value *flag.Flag) { changed[value.Name] = true })
	var snapshot serviceConfiguration
	if err := query("service.config", map[string]string{"id": args[1]}, &snapshot); err != nil {
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
	for key, value := range map[string]*string{"name": &config.Name, "network": &config.Network, "ports": &config.Ports, "exclude": &config.ExcludePorts, "peer": &config.PeerID} {
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
	if changed["ttl"] {
		if *ttl%time.Second != 0 {
			return errors.New(text(ja, "--ttl must use whole seconds", "--ttl は整数秒で指定してください"))
		}
		config.TTLSeconds = int(ttl.Seconds())
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
	if (command == "share" && (changed["peer"] || changed["local-port"])) || (command == "connect" && (changed["peers"] || changed["discoverable"])) {
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
	inputs := []string{"--name", config.Name, "--network", config.Network, "--ports", config.Ports, "--exclude", config.ExcludePorts, "--ttl", fmt.Sprintf("%ds", config.TTLSeconds)}
	if command == "share" {
		inputs = append(inputs, "--peers", strings.Join(config.PeerIDs, ","), "--discoverable="+strconv.FormatBool(config.Discoverable))
	} else {
		inputs = append(inputs, "--peer", config.PeerID, "--local-port", strconv.Itoa(config.LocalPort))
	}
	payload, err := servicePayload(command, inputs, ja, out)
	if err != nil {
		return err
	}
	payload["backend"] = backend
	payload["purpose"] = config.Purpose
	if config.ServiceID != "" {
		payload["serviceId"] = config.ServiceID
	}
	if operation == "restart" {
		payload["replaceId"] = config.ID
		payload["expectedRevision"] = snapshot.Revision
	}
	return request("service."+command, payload)
}
