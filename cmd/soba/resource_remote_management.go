package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

const resourceRemoteManagementHelpEN = `Manage one explicitly permitted paired peer resource

  soba resource remote manage inspect --peer PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N [--json]
  soba resource remote manage preview --peer PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N --concurrent-files default|N --concurrent-per-peer default|N [--json]
  soba resource remote manage apply --peer PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N --operation-id OPERATION_ID --base-revision BASE --revision REVIEW --concurrent-files default|N --concurrent-per-peer default|N --confirm [--json]
  soba resource remote manage status --peer PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N --operation-id OPERATION_ID [--json]

The target must explicitly confirm a management grant for this paired device.
Use the exact target, grant and current revision from its grant inspect output.
Management uses protocol v2; both agents need a management-capable build.
An inspect-only grant never permits management. There is no protocol or backend fallback.
Preview changes no settings and reserves no operation. Review both requested and effective choices.
Apply requires --confirm and the exact operationId, baseRevision, reviewRevision and both choices
from preview. --revision supplies reviewRevision. Apply never creates a replacement preview.
The first accepted fresh remote apply upgrades the operation journal to v2 while preserving existing
evidence. Older v1-only agents cannot read that journal; do not delete state to downgrade.
An uncertain reply does not prove whether settings changed. Query status with the same opaque ID;
never invent a replacement operation or blindly apply again. Matching retained replays do not execute again.
Status reports historical outcome and evidenceDurable, not current settings or transfer completion.
Unavailable status does not distinguish missing, evicted or differently scoped evidence.
Revocation prevents new admission; work already admitted may finish and its reply may be withheld.
Each exchange has one application request and a fixed 15-second total budget including recovery.
Output is stable JSON. --offline is unsupported. --dry-run validates and prints local input only.`

const resourceRemoteManagementHelpJA = `明示的に許可されたペアの相手1台のリソースを管理

  soba resource remote manage inspect --peer PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N [--json]
  soba resource remote manage preview --peer PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N --concurrent-files default|N --concurrent-per-peer default|N [--json]
  soba resource remote manage apply --peer PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N --operation-id OPERATION_ID --base-revision BASE --revision REVIEW --concurrent-files default|N --concurrent-per-peer default|N --confirm [--json]
  soba resource remote manage status --peer PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N --operation-id OPERATION_ID [--json]

対象端末で、このペアの端末への管理許可を明示的に確定してください。
grant inspect が返した正確な対象・許可・現在の変更番号を指定します。
管理には通信方式 v2 を使い、両端末に管理対応版が必要です。
参照のみの許可では管理できません。別の通信方式や接続方式へ自動で切り替えません。
preview は設定を変更せず、操作枠も予約しません。両項目の指定値と実効値を確認してください。
apply には --confirm と、preview が返した正確な operationId・baseRevision・reviewRevision・両項目が必要です。
--revision は reviewRevision を指定します。apply は新しい preview を勝手に作りません。
初めて受理した新規の遠隔適用は、既存の記録を保持したまま操作記録を v2 に更新します。
v1 のみを扱う旧版本体では読めなくなります。旧版へ戻すために保存状態を削除しないでください。
応答が不明でも、設定が変更されていないとは判断できません。同じ不透明な ID で status を確認し、
別の操作を勝手に作ったり、確認せず適用し直したりしないでください。保持済みの同一要求は再実行しません。
status は過去の結果と evidenceDurable を返します。現在の設定やファイル転送の完了を示すものではありません。
記録を取得できない場合、未作成・削除済み・別範囲の記録を区別しません。
失効後は新たな実行を受け付けません。受理済みの作業は完了する場合があり、応答が開示されない場合もあります。
1回につきアプリケーション要求は1件で、接続の復旧を含め合計15秒の期限があります。
出力は安定した JSON です。--offline は使えません。--dry-run は入力検査とローカル表示のみです。`

func resourceRemoteManagementCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, client controlCaller) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") || len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		_, err := fmt.Fprintln(out, text(ja, resourceRemoteManagementHelpEN, resourceRemoteManagementHelpJA))
		return err
	}
	action := args[0]
	if action != "inspect" && action != "preview" && action != "apply" && action != "status" {
		return usageError(ja, "resource remote manage inspect|preview|apply|status --help")
	}
	flags := commandFlags("resource remote manage "+action, ja, out)
	flags.Bool("json", false, text(ja, "stable JSON output", "安定した JSON 出力"))
	var peer, id, grant, grantRevision, operation, base, review, files, perPeer string
	var confirm bool
	flags.StringVar(&peer, "peer", "", text(ja, "exact paired peer key", "ペアの相手の正確な鍵"))
	flags.StringVar(&id, "id", "", text(ja, "exact remote resource ID", "相手側の正確なリソース ID"))
	flags.StringVar(&grant, "grant-id", "", text(ja, "exact management grant ID", "正確な管理許可 ID"))
	flags.StringVar(&grantRevision, "grant-revision", "", text(ja, "current management grant revision", "現在の管理許可の変更番号"))
	if action == "preview" || action == "apply" {
		flags.StringVar(&files, "concurrent-files", "", text(ja, "reviewed concurrent files: default or positive integer", "確認済みの同時ファイル数: default または正の整数"))
		flags.StringVar(&perPeer, "concurrent-per-peer", "", text(ja, "reviewed concurrency per peer: default or positive integer", "確認済みの相手ごとの同時数: default または正の整数"))
	}
	if action == "apply" || action == "status" {
		flags.StringVar(&operation, "operation-id", "", text(ja, "exact opaque operation ID", "正確な不透明な操作 ID"))
	}
	if action == "apply" {
		flags.StringVar(&base, "base-revision", "", text(ja, "exact baseRevision from preview", "preview の正確な baseRevision"))
		flags.StringVar(&review, "revision", "", text(ja, "exact reviewRevision from preview", "preview の正確な reviewRevision"))
		flags.BoolVar(&confirm, "confirm", false, text(ja, "confirm both reviewed changes and journal migration if needed", "確認済みの両項目の変更と必要な操作記録の形式更新を承認"))
	}
	if err := parseFlags(flags, args[1:], ja); err != nil {
		return err
	}
	revision, err := strconv.ParseUint(grantRevision, 10, 64)
	if err != nil {
		return errors.New(text(ja, "Provide the current finite grant revision", "現在の有限の許可の変更番号を指定してください"))
	}
	wireAction := action
	if action == "status" {
		wireAction = resourcegrant.StatusAction
	}
	request := resourcegrant.ManagementRequest{ManagementSelector: resourcegrant.ManagementSelector{ProtocolVersion: resourcegrant.ManagementProtocolVersion, Target: resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: id}, GrantID: grant, GrantRevision: revision}, Action: wireAction}
	if action == "preview" || action == "apply" {
		fc, err := resourceCLIChoice(files, ja)
		if err != nil {
			return err
		}
		pc, err := resourceCLIChoice(perPeer, ja)
		if err != nil {
			return err
		}
		settings := resource.Settings{TransferConcurrentFiles: fc, TransferConcurrentPerPeer: pc}
		if action == "preview" {
			request.Preview = &resourcegrant.ManagementPreviewRequest{Settings: settings}
		} else {
			request.Apply = &resourcegrant.ManagementApplyRequest{OperationID: operation, BaseRevision: base, ReviewRevision: review, Settings: settings}
		}
	}
	if action == "status" {
		request.Status = &resourcegrant.ManagementStatusRequest{OperationID: operation}
	}
	input := resourcegrant.RemoteManagementInput{PeerKey: peer, Request: request, Confirm: confirm}
	if input.Validate() != nil {
		return errors.New(text(ja, "Provide the exact peer, target and current management grant; apply also requires the unchanged preview and --confirm", "正確な相手・対象・現在の管理許可を指定してください。apply には変更していない確認内容と --confirm も必要です"))
	}
	name := "resource.remote.management." + wireAction
	write := func(v any) error { e := json.NewEncoder(out); e.SetIndent("", "  "); return e.Encode(v) }
	if dryRun {
		return write(map[string]any{"applied": false, "command": name, "payload": input, "validation": "local-input-only"})
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	command, err := json.Marshal(webui.Command{RequestID: newCLIRequestID("cli-resource-management"), Name: name, Payload: payload})
	if err != nil {
		return err
	}
	var response json.RawMessage
	if err := client(ctx, dir, string(command), &response); err != nil {
		return resourceControlError(err)
	}
	return write(response)
}
