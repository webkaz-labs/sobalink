package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

const resourceGrantHelpEN = `Inspect-only grants for one managed peer

  soba resource grant preview --id RESOURCE_ID --peer PEER_KEY --expires-at RFC3339 [--json]
  soba resource grant confirm --review-file REVIEW.json --confirm [--json]
  soba resource grant inspect --id RESOURCE_ID [--json]
  soba resource grant revoke --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N --confirm [--json]

Preview does not change saved state. Review the verified peer/target keys, relationship,
resource, both transfer fields, inspect-only action, expiry and initialization flag.
Save the complete preview JSON unchanged to a private file, then explicitly confirm.
Keep that file private; the CLI validates regular bounded JSON, not its access permissions.
If initializesState is true, you are approving first-use grant-state initialization.
Missing whole state cannot prove that no older grants existed; do not delete state as recovery.
Confirm rejects stale or changed reviews and never creates a replacement preview.
A confirmed grant can start the scoped listener; check activation and listenerReady.
Without an eligible saved grant, starting the agent creates no permission and starts no inspection listener.
Saved permission survives restart only until its original expiry, with the same managed relationship.
Port conflicts leave permission saved but inactive and never displace an existing service.
Detected clock rollback or uncertain private state denies activation. Lifetime across downtime relies
on the target clock: complete state rollback or unobserved clock changes cannot be detected reliably.
Revoke needs the current ID/revision from inspect;
it remains available without a connected peer or valid grant time.
Inspection permission is separate from message/file permission. General trust never creates or restores it.
The broad peer revoke action also revokes inspection; message/file pause and stopping service shares do not.
Removing the managed pair denies inspection, even if its saved grant record still says active.
A failed revoke may not survive a crash; do not treat a persistence error as durable success.
Output is stable JSON in either language. --offline is unsupported.
Global --dry-run prints validated local input without contacting the agent.`

const resourceGrantHelpJA = `管理対象の相手1台への参照のみの許可

  soba resource grant preview --id RESOURCE_ID --peer PEER_KEY --expires-at RFC3339 [--json]
  soba resource grant confirm --review-file REVIEW.json --confirm [--json]
  soba resource grant inspect --id RESOURCE_ID [--json]
  soba resource grant revoke --id RESOURCE_ID --grant-id GRANT_ID --grant-revision N --confirm [--json]

preview は保存状態を変更しません。検証済みの相手・対象の鍵、接続関係、
リソース、転送の両項目、参照のみの操作、有効期限、初期化の有無を確認してください。
preview の JSON 全体を変更せず非公開ファイルに保存し、明示的に confirm してください。
ファイルは非公開で保管してください。CLI は通常ファイルの JSON とサイズを検査しますが、アクセス権は検査しません。
initializesState が true の場合は、参照許可の保存領域の初期化も承認します。
保存状態全体がないことから、過去の許可がなかったとは判断できません。復旧目的で削除しないでください。
confirm は古い・変更された確認内容を拒否し、新しい preview を勝手に作りません。
確定した許可で専用の接続口を開始できます。activation と listenerReady を確認してください。
有効な保存済み許可がなければ、本体の起動で許可を作成したり参照用の接続口を開始したりしません。
保存済み許可は、同じ管理対象の接続関係と元の有効期限を保って再起動後も維持します。
ポートが使用中の場合は保存済み・未稼働となり、既存のサービスを置き換えません。
時計の巻き戻りや保存状態の不確実性を検出すると有効化を拒否します。停止中の期限管理は対象端末の時計に依存します。
保存状態全体の巻き戻りや未観測の時計変更は確実には検出できません。
revoke には inspect で確認した現在の ID・変更番号を指定します。
相手が未接続の場合や許可の期限が切れた場合も失効できます。
参照許可はメッセージ・ファイルの許可とは別です。通常の許可操作で参照許可を作成・復元しません。
相手への許可全体を取り消す revoke は参照許可も失効させます。メッセージ・ファイルの停止やサービス共有の停止は対象外です。
管理対象のペアを削除すると、保存した許可が active と表示されていても参照を拒否します。
失効の保存が失敗すると、再起動後の失効は保証できません。保存エラーを成功として扱わないでください。
出力は言語によらず安定した JSON です。--offline は使えません。
共通オプション --dry-run は本体に接続せず、検証済みの入力だけを表示します。`

func resourceGrantCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, client controlCaller) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(out, text(ja, resourceGrantHelpEN, resourceGrantHelpJA))
		return err
	}
	action := args[0]
	if action != "preview" && action != "confirm" && action != "inspect" && action != "revoke" {
		return usageError(ja, "resource grant preview|confirm|inspect|revoke --help")
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		_, err := fmt.Fprintln(out, text(ja, resourceGrantHelpEN, resourceGrantHelpJA))
		return err
	}
	flags := commandFlags("resource grant "+action, ja, out)
	flags.Bool("json", false, text(ja, "stable JSON output (also the default)", "機械処理用の安定した JSON 出力（既定）"))
	var id, peer, expiry, reviewFile, grantID, grantRevision string
	var confirm bool
	if action != "confirm" {
		flags.StringVar(&id, "id", "", text(ja, "exact resource ID", "正確なリソース ID"))
	}
	if action == "preview" {
		flags.StringVar(&peer, "peer", "", text(ja, "exact verified managed peer key", "検証済みの管理対象の相手の正確な鍵"))
		flags.StringVar(&expiry, "expires-at", "", text(ja, "finite RFC3339 expiry with timezone", "タイムゾーン付き RFC3339 形式の有限の有効期限"))
	}
	if action == "confirm" {
		flags.StringVar(&reviewFile, "review-file", "", text(ja, "private file containing the unchanged preview JSON", "変更していない preview JSON を含む非公開ファイル"))
	}
	if action == "revoke" {
		flags.StringVar(&grantID, "grant-id", "", text(ja, "exact grant ID from inspect", "inspect で確認した正確な許可 ID"))
		flags.StringVar(&grantRevision, "grant-revision", "", text(ja, "current grant revision from inspect", "inspect で確認した現在の許可の変更番号"))
	}
	if action == "confirm" || action == "revoke" {
		flags.BoolVar(&confirm, "confirm", false, text(ja, "explicitly confirm the reviewed grant action", "確認済みの許可操作を明示的に承認する"))
	}
	if err := parseFlags(flags, args[1:], ja); err != nil {
		return err
	}
	invalid := func() error {
		return errors.New(text(ja, "Provide the exact grant scope and an explicit confirmation for changes; see resource grant --help", "正確な参照範囲を指定し、変更には明示的な承認を付けてください。resource grant --help を参照してください"))
	}
	var payload any
	if action == "confirm" {
		if !confirm || reviewFile == "" {
			return invalid()
		}
		review, err := readResourceGrantReview(reviewFile)
		if err != nil {
			return errors.New(text(ja, "Could not read a valid bounded grant review; use the unchanged private preview JSON file", "有効なサイズ制限内の確認内容を読み込めません。変更していない非公開の preview JSON ファイルを指定してください"))
		}
		payload = resourcegrant.GrantConfirmation{Review: review, Confirm: true}
	} else {
		target := resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: id}
		if target.Validate() != nil {
			return invalid()
		}
		switch action {
		case "inspect":
			payload = struct {
				Target resource.Target `json:"target"`
			}{target}
		case "preview":
			deadline, err := time.Parse(time.RFC3339, expiry)
			if err != nil || deadline.Unix() <= 0 || !resource.ValidDigest(peer) {
				return invalid()
			}
			payload = resourcegrant.GrantInputs{Target: target, PeerKey: peer, ExpiresAt: deadline.Unix(), Actions: []string{resourcegrant.Inspect}, Fields: []string{resourcegrant.FilesField, resourcegrant.PerPeerField}}
		case "revoke":
			revision, err := strconv.ParseUint(grantRevision, 10, 64)
			if !confirm || !resource.ValidID(grantID) || err != nil || revision == 0 || revision > uint64(capacity.MaxJSONInteger) || strconv.FormatUint(revision, 10) != grantRevision {
				return invalid()
			}
			payload = resourcegrant.GrantRevoke{Target: target, GrantID: grantID, GrantRevision: revision, Confirm: true}
		}
	}
	write := func(value any) error {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	name := "resource.grant." + action
	if dryRun {
		return write(map[string]any{"applied": false, "command": name, "payload": payload, "validation": "local-input-only"})
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := json.Marshal(webui.Command{RequestID: newCLIRequestID("cli-resource-grant"), Name: name, Payload: raw})
	if err != nil {
		return err
	}
	var result json.RawMessage
	if err := client(ctx, dir, string(request), &result); err != nil {
		return resourceControlError(err)
	}
	return write(result)
}

func readResourceGrantReview(path string) (resourcegrant.GrantReview, error) {
	var review resourcegrant.GrantReview
	file, err := (transfer.Source{LocalPath: path}).Open()
	if err != nil {
		return review, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8192 {
		return review, resourcegrant.ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || resource.Decode(data, 8192, &review) != nil || review.Grant.Validate() != nil || !resource.ValidDigest(review.BaseRevision) || !resource.ValidDigest(review.ReviewRevision) {
		return resourcegrant.GrantReview{}, resourcegrant.ErrInvalid
	}
	return review, nil
}
