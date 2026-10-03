package main

import (
	"errors"
	"io"
	"strings"
)

func startupCommand(args []string, ja bool, out io.Writer, request actionRequest) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		_, err := io.WriteString(out, text(ja, startupHelpEN, startupHelpJA)+"\n")
		return err
	}
	name := args[0]
	if name == "list" {
		if len(args) != 1 {
			return usageError(ja, "startup list")
		}
		return request("startup.list", map[string]any{})
	}
	if name != "preview" && name != "save" && name != "disable" {
		return errors.New(text(ja, "Unknown startup action; use soba startup --help", "不明な起動設定の操作です。soba startup --help を参照してください"))
	}
	f := commandFlags("startup "+name, ja, out)
	selectedName := f.String("name", "", text(ja, "startup selection name", "起動対象の名前"))
	ids := f.String("ids", "", text(ja, "comma-separated saved service IDs", "保存済みサービス ID（カンマ区切り）"))
	group := f.String("group", "", text(ja, "saved outbound group", "保存済みの接続グループ"))
	review := f.String("review", "", text(ja, "exact preview revision", "確認結果の revision"))
	store := f.String("store-review", "", text(ja, "private startup settings revision", "非公開の起動設定の revision"))
	if err := parseFlags(f, args[1:], ja); err != nil {
		return err
	}
	if *selectedName == "" {
		return errors.New(text(ja, "Specify --name", "--name を指定してください"))
	}
	payload := map[string]any{"name": *selectedName}
	if name == "disable" {
		if *ids != "" || *group != "" || *review != "" || *store == "" {
			return usageError(ja, "startup disable --name NAME --store-review REVISION")
		}
		payload["expectedStoreRevision"] = *store
	} else {
		if (*ids == "") == (*group == "") {
			return errors.New(text(ja, "Select --ids or --group", "--ids または --group の一方を指定してください"))
		}
		if *ids != "" {
			payload["ids"] = strings.Split(*ids, ",")
		} else {
			payload["group"] = *group
		}
		if name == "save" {
			if *review == "" || *store == "" {
				return errors.New(text(ja, "Review first, then provide --review and --store-review", "先に確認し、--review と --store-review を指定してください"))
			}
			payload["expectedRevision"] = *review
			payload["expectedStoreRevision"] = *store
		} else if *review != "" || *store != "" {
			return usageError(ja, "startup preview --name NAME --ids ID,ID | --group GROUP")
		}
	}
	return request("startup."+name, payload)
}

const startupHelpEN = `Optional outbound startup selections

  soba startup preview --name NAME --ids ID,ID | --group GROUP
  soba startup save --name NAME --ids ID,ID | --group GROUP
       --review REVISION --store-review STORE_REVISION
  soba startup list
  soba startup disable --name NAME --store-review STORE_REVISION

Preview the exact saved definitions, then use its revision and storeRevision.
Saving enables that exact outbound scope on future online process launches.
It does not start anything now. New profiles have no startup selections.
Inbound shares are rejected. Edited/imported definitions and changed groups
require review again. Offline launches suppress all startup selections.
Finite lifetimes begin anew on an explicitly requested process restart;
transport recovery preserves the existing expiry. Stopping never restarts a
selection in the current process. Disable prevents future launch starts.
Startup approval and private proxy credentials are excluded from profile export.`
const startupHelpJA = `任意の起動時接続

  soba startup preview --name NAME --ids ID,ID | --group GROUP
  soba startup save --name NAME --ids ID,ID | --group GROUP
       --review REVISION --store-review STORE_REVISION
  soba startup list
  soba startup disable --name NAME --store-review STORE_REVISION

保存済みの正確な定義を確認し、結果の revision と storeRevision を指定します。
保存すると、次回以降のオンライン起動時にその接続範囲だけを開始します。
保存操作自体では開始しません。新規プロファイルではすべて無効です。
受信共有は対象にできません。定義の編集・取込みやグループ変更後は再確認が
必要です。オフライン起動では起動時接続をすべて抑止します。
明示的な本体の再起動では有限の有効期間を新たに開始します。通信の復旧では
既存の期限を維持します。停止後、同じ本体内で自動的に再開始しません。
disable は次回以降の自動開始を無効にします。起動許可とプロキシ認証情報は
プロファイルの書き出しに含まれません。`
