package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/webkaz-labs/sobalink/internal/core"
	"io"
	"strings"
)

const mixedHelpEN = `Mixed connections

  soba mixed setup --backends direct-lan,tailnet
  soba mixed show [--json]
  soba mixed bind --peer PEER_ID --peer PEER_ID
  soba mixed unbind --peer LOGICAL_PEER_ID

Prepare each backend separately, then stop and start --offline before setup.
The listed order is preferred for new connections. Selecting Tailnet permits
its external control traffic; strict LAN mode never enables it automatically.
Binding verifies the same application key through each authenticated backend.
Names and IP addresses are not proof. Old approvals are paused, not migrated;
review a new application/service approval for the logical peer after binding.
A failed authorization never falls back. Existing TCP streams are not migrated
or replayed, and an unavailable path may require the application to reconnect.`
const mixedHelpJA = `複数方式の接続

  soba mixed setup --backends direct-lan,tailnet
  soba mixed show [--json]
  soba mixed bind --peer PEER_ID --peer PEER_ID
  soba mixed unbind --peer LOGICAL_PEER_ID

各方式を個別に設定し、停止後に start --offline で起動してから設定します。
指定した順序で新しい接続の経路を選びます。Tailnetを選ぶと外部の制御通信を
許可します。LAN限定から自動で外部通信を追加することはありません。
関連付けでは、各方式の認証済み経路で同じアプリの鍵を確認します。
名前やIPだけでは同一端末と判断しません。既存の許可は移行せず一時停止します。
関連付け後に、相手と共有サービスへの許可を改めて確認してください。
認可拒否を別経路で回避しません。既存TCP接続の移行やデータ再送は行わず、
経路が利用できない場合はアプリからの再接続が必要になることがあります。`

func mixedCLI(args []string, ja, dryRun bool, out io.Writer, query commandQuery, request func(string, any) error) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, text(ja, mixedHelpEN, mixedHelpJA))
		return nil
	}
	switch args[0] {
	case "show":
		f := commandFlags("mixed show", ja, out)
		asJSON := f.Bool("json", false, "JSON")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if f.NArg() != 0 {
			return errors.New(text(ja, "Unexpected argument", "余分な引数があります"))
		}
		if dryRun {
			return request("mixed.status", map[string]any{})
		}
		var state map[string]any
		if e := query("mixed.status", map[string]any{}, &state); e != nil {
			return e
		}

		if *asJSON {
			return json.NewEncoder(out).Encode(state)
		}
		if failure, _ := state["error"].(string); failure != "" {
			return errors.New(text(ja, "Saved mixed configuration is unavailable. Stop soba and inspect saved settings before restarting; do not treat this as a new profile.", "保存済みの複数方式設定を確認できません。soba を停止し、保存設定を確認・修正してから再起動してください。未設定として扱わないでください。"))
		}
		if configured, _ := state["configured"].(bool); !configured {
			fmt.Fprintln(out, text(ja, "Mixed connections are not configured", "複数方式の接続は未設定です"))
			return nil
		}
		fmt.Fprintln(out, text(ja, "Mixed connection settings", "複数方式の接続設定"))
		fmt.Fprintf(out, "%s: %v\n", text(ja, "Backends", "接続方式"), state["backends"])
		fmt.Fprintf(out, "%s: %v\n", text(ja, "Stable identity", "アプリの識別子"), state["identity"])
		if resources, ok := state["workerResources"]; ok {
			fmt.Fprintf(out, "%s: %v\n", text(ja, "Worker resource allocation", "ワーカーのリソース割り当て"), resources)
		}
		if restart, _ := state["resourceRestartRequired"].(bool); restart {
			fmt.Fprintln(out, text(ja, "Restart soba to apply changed worker resource budgets.", "変更したワーカーのリソース予算を適用するには、soba を再起動してください。"))
		}
		fmt.Fprintln(out, text(ja, "For new connections only. Application approvals remain separate.", "新しい接続が対象です。アプリへの許可は別途必要です。"))
		return nil
	case "setup":
		f := commandFlags("mixed setup", ja, out)
		backends := f.String("backends", "", text(ja, "ordered comma-separated backends", "接続方式を優先順にカンマ区切り"))
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if f.NArg() != 0 || *backends == "" {
			return errors.New(text(ja, "Choose --backends explicitly", "--backends を明示してください"))
		}
		names := strings.Split(*backends, ",")
		if len(names) < 2 || len(names) > 3 {
			return errors.New(text(ja, "Choose two or three backends", "接続方式を2つまたは3つ選んでください"))
		}
		seen := map[string]bool{}
		for _, n := range names {
			if (n != "tailnet" && n != "lan" && n != "direct-lan") || seen[n] {
				return errors.New(text(ja, "Invalid or duplicate backend", "接続方式が無効または重複しています"))
			}
			seen[n] = true
		}
		return request("network.configure", map[string]any{"mode": "mixed", "mixed": core.MixedSelection{Backends: names}})
	case "bind":
		f := commandFlags("mixed bind", ja, out)
		var peers policyPrefixes
		f.Var(&peers, "peer", text(ja, "authenticated peer route ID; repeat for each backend", "認証済みの相手ID（方式ごとに繰り返し指定）"))
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if f.NArg() != 0 || len(peers) < 2 || len(peers) > 3 {
			return errors.New(text(ja, "Select two or three exact --peer routes", "--peer で2つまたは3つの経路を選んでください"))
		}
		return request("mixed.bind", map[string]any{"peers": []string(peers)})
	case "unbind":
		f := commandFlags("mixed unbind", ja, out)
		peer := f.String("peer", "", text(ja, "logical peer ID", "関連付けた相手のID"))
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if f.NArg() != 0 || *peer == "" {
			return errors.New(text(ja, "Choose --peer explicitly", "--peer を明示してください"))
		}
		return request("mixed.unbind", map[string]any{"peerId": *peer})
	default:
		return errors.New(text(ja, "Use soba mixed help", "soba mixed help を参照してください"))
	}
}
