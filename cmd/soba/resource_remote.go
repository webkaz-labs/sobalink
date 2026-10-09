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

const resourceRemoteHelpEN = `Inspect one explicitly permitted managed peer resource

  soba resource remote inspect --peer PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N [--json]

The target must explicitly confirm an inspect-only grant for this device first.
Use the exact resource ID, grant ID and current revision from the target's grant inspect output.
Only the two transfer choices and effective values are returned. No catalog, history or remote writes.
Both agents must use the managed DirectLAN backend. There is no protocol or backend fallback.
The fixed hello checks peer-authenticated protocol compatibility, not service/process attestation.
The exchange has a 15-second total deadline, including bounded managed-session recovery.
An unavailable result does not reveal whether a resource or grant exists.
Output is stable JSON. --offline is unsupported. --dry-run only validates and prints local input.`

const resourceRemoteHelpJA = `明示的に許可された管理対象の相手1台のリソースを参照

  soba resource remote inspect --peer PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N [--json]

先に対象の端末で、この端末への参照のみの許可を明示的に確定してください。
対象端末の grant inspect が返した正確なリソース ID・許可 ID・現在の変更番号を指定します。
取得するのは転送の両項目と実効値だけです。一覧・履歴・遠隔での変更操作はありません。
両端末とも管理対象の DirectLAN 接続を使います。別の通信方式へ自動で切り替えません。
固定の hello は認証済みの相手との通信方式の互換性を確認します。サービスやプロセス自体の証明ではありません。
接続の復旧を含め、1回の参照には合計15秒の期限があります。
取得できなかった場合、リソースや許可の存在の有無は示しません。
出力は安定した JSON です。--offline は使えません。--dry-run は入力検査とローカル表示だけを行います。`

func resourceRemoteCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, client controlCaller) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") || len(args) == 2 && args[0] == "inspect" && (args[1] == "--help" || args[1] == "-h") {
		_, err := fmt.Fprintln(out, text(ja, resourceRemoteHelpEN, resourceRemoteHelpJA))
		return err
	}
	if args[0] != "inspect" {
		return usageError(ja, "resource remote inspect --help")
	}
	flags := commandFlags("resource remote inspect", ja, out)
	flags.Bool("json", false, text(ja, "stable JSON output", "安定した JSON 出力"))
	var peer, id, grant, revision string
	flags.StringVar(&peer, "peer", "", text(ja, "exact managed peer key", "管理対象の相手の正確な鍵"))
	flags.StringVar(&id, "id", "", text(ja, "exact remote resource ID", "相手側の正確なリソース ID"))
	flags.StringVar(&grant, "grant-id", "", text(ja, "exact grant ID", "正確な許可 ID"))
	flags.StringVar(&revision, "grant-revision", "", text(ja, "current grant revision", "現在の許可の変更番号"))
	if err := parseFlags(flags, args[1:], ja); err != nil {
		return err
	}
	number, err := strconv.ParseUint(revision, 10, 64)
	input := resourcegrant.RemoteInspectInput{PeerKey: peer, Request: resourcegrant.InspectRequest{ProtocolVersion: resourcegrant.ProtocolVersion, Target: resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: id}, GrantID: grant, GrantRevision: number}}
	if err != nil || input.Validate() != nil {
		return errors.New(text(ja, "Provide the exact peer, resource ID, grant ID and current finite revision", "正確な相手・リソース ID・許可 ID・現在の有限の変更番号を指定してください"))
	}
	write := func(v any) error {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(v)
	}
	if dryRun {
		return write(map[string]any{"applied": false, "command": "resource.remote.inspect", "payload": input, "validation": "local-input-only"})
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	command, err := json.Marshal(webui.Command{RequestID: newCLIRequestID("cli-resource-remote"), Name: "resource.remote.inspect", Payload: raw})
	if err != nil {
		return err
	}
	var response json.RawMessage
	if err := client(ctx, dir, string(command), &response); err != nil {
		return resourceControlError(err)
	}
	return write(response)
}
