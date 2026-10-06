package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/webkaz-labs/sobalink/internal/capacity"
)

var relayResourceCLI = []struct{ flag, key, en, ja string }{
	{"presence-connections", "relayPresenceConnections", "Active relay presence connections", "同時に維持するリレー接続数"},
	{"candidate-attempts", "relayCandidateAttempts", "Sequential relay attempts per connection", "接続1回ごとのリレー候補試行数"},
	{"tls-connections", "relayTLSConnections", "Hosted relay TLS connections", "ホストするリレーのTLS接続数"},
	{"admission-connections", "relayAdmissionConnections", "Hosted relay admission connections", "ホストするリレーの入場確認接続数"},
}

const relayResourcesHelpEN = `Relay resource budgets

  soba lan resources show [--json]
  soba lan resources set --presence-connections 8 --candidate-attempts 8
  soba lan resources set --tls-connections 128 --admission-connections 32
  soba lan resources set --tls-connections default

Defaults: 4 active relay-presence connections, 4 sequential candidates per dial,
64 hosted TLS connections and 16 admission connections. These are adjustable
finite resource budgets, not NAT constraints or a four-candidate permission limit.
Stop soba and start --offline before changing them; they apply at next network start.
Unspecified choices and unrelated capacity settings are preserved. Use a positive
integer or default. Presence/attempt budgets fit the DERP ID namespace (1..65535).
--dry-run previews only the requested edits, without contacting the local agent.
Existing pairs, routes, lifetimes and application permissions do not change.`
const relayResourcesHelpJA = `リレーのリソース予算

  soba lan resources show [--json]
  soba lan resources set --presence-connections 8 --candidate-attempts 8
  soba lan resources set --tls-connections 128 --admission-connections 32
  soba lan resources set --tls-connections default

既定値は同時リレー接続4、接続1回ごとの候補試行4、ホストTLS接続64、入場確認接続16です。
変更可能な有限のリソース予算であり、NATの制約や候補の許可数4件の制約ではありません。
変更前に soba を停止し start --offline で起動します。次回のネットワーク開始時に反映します。
未指定の項目と他の容量設定は維持します。正の整数または default を指定してください。
同時リレー・試行予算はDERP識別子空間（1〜65535）に収まる必要があります。
--dry-run は稼働中の本体に接続せず、指定した変更だけを表示します。
既存のペア、経路、有効期間、アプリの許可は変えません。`

func lanResourcesCommand(args []string, ja, dryRun bool, out io.Writer, query commandQuery, request actionRequest) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		_, err := io.WriteString(out, text(ja, relayResourcesHelpEN, relayResourcesHelpJA)+"\n")
		return err
	}
	if args[0] != "show" && args[0] != "set" {
		return usageError(ja, "lan resources show|set|--help")
	}
	f := commandFlags("lan resources "+args[0], ja, out)
	var structured *bool
	values := map[string]*string{}
	if args[0] == "show" {
		structured = f.Bool("json", false, text(ja, "stable machine-readable budgets", "機械向けの安定した予算出力"))
	} else {
		for _, item := range relayResourceCLI {
			values[item.key] = f.String(item.flag, "", text(ja, item.en+": positive integer or default", item.ja+": 正の整数または default"))
		}
	}
	if err := parseFlags(f, args[1:], ja); err != nil {
		return err
	}
	changes := map[string]capacity.Choice{}
	for _, item := range relayResourceCLI {
		value := values[item.key]
		if value == nil || *value == "" {
			continue
		}
		choice := capacity.Default()
		if *value != "default" {
			n, err := strconv.ParseInt(*value, 10, 64)
			if err != nil || n < 1 || n > capacity.MaxJSONInteger || (item.key == "relayPresenceConnections" || item.key == "relayCandidateAttempts") && n > 65535 {
				return errors.New(text(ja, "Use positive finite relay budgets; presence/attempts are 1..65535", "正の有限整数を指定してください。同時リレー・試行は1〜65535です"))
			}
			choice = capacity.Limited(n)
		}
		changes[item.key] = choice
	}
	if args[0] == "set" && len(changes) == 0 {
		return errors.New(text(ja, "Choose at least one relay resource flag", "リレーの予算項目を1つ以上指定してください"))
	}
	if dryRun {
		return json.NewEncoder(out).Encode(map[string]any{"applied": false, "operation": "lan.resources." + args[0], "requestedResources": changes, "validation": "local-input-only"})
	}
	var config struct {
		Requested capacity.Policy `json:"requested"`
		Effective capacity.Policy `json:"effective"`
		Editable  bool            `json:"relayResourceEditable"`
	}
	if err := query("policy.config", map[string]any{}, &config); err != nil {
		return err
	}
	if err := config.Requested.Validate(); err != nil {
		return err
	}
	if args[0] == "show" {
		effective := map[string]int64{}
		for _, item := range relayResourceCLI {
			effective[item.key] = config.Effective.Number("resources", item.key)
		}
		if *structured {
			return json.NewEncoder(out).Encode(map[string]any{"effective": effective, "editable": config.Editable, "restartRequired": config.Editable})
		}
		for _, item := range relayResourceCLI {
			fmt.Fprintf(out, "%s: %d\n", text(ja, item.en, item.ja), effective[item.key])
		}
		fmt.Fprintln(out, text(ja, "Change relay budgets while offline; they apply at next network start.", "予算はオフラインで変更し、次回のネットワーク開始時に反映します。"))
		return nil
	}
	proposed := config.Requested.Clone()
	for key, choice := range changes {
		proposed.Resources[key] = choice
	}
	var preview struct {
		Revision string `json:"revision"`
	}
	if err := query("policy.preview", map[string]any{"policy": proposed}, &preview); err != nil {
		return err
	}
	if preview.Revision == "" {
		return errors.New(text(ja, "The relay budget review was not confirmed", "リレー予算の確認が完了しませんでした"))
	}
	return request("policy.apply", map[string]any{"policy": proposed, "expectedRevision": preview.Revision})
}
