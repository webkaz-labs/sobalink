package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

const resourceGroupHelpEN = `Review and apply one fixed resource selection (source build)

  soba resource group preview --member PEER_KEY,RESOURCE_ID,GRANT_ID,GRANT_REVISION [--member ...] --concurrent-files default|N --concurrent-per-peer default|N [--json]
  soba resource group select --review-id REVIEW_ID --revision REVIEW_REVISION --execute-peer PEER_KEY [--execute-peer ...] [--json]
  soba resource group apply --review-id REVIEW_ID --revision REVIEW_REVISION --execute-peer PEER_KEY [--execute-peer ...] --confirm [--json]
  soba resource group review current [--json]
  soba resource group status --run-id RUN_ID [--json]
  soba resource group refresh --run-id RUN_ID --peer PEER_KEY [--peer ...] [--json]
  soba resource group cancel --review-id REVIEW_OR_RUN_ID [--json]

Preview retains every selected peer, including failures. Review requested and effective choices.
At most 16 exact peers; no wildcard, dynamic group, remote command or automatic splitting.
The prepared review exists only in the current owning agent process. It is not approval.
Use --replace-review-id to replace an existing prepared review explicitly.
Use select to review a smaller ready subset. Optional --review-file REVIEW.json replaces both
review identity flags and checks preserved rows against saved preview --json output.
--exclude-all explicitly creates an empty, non-executable subset instead of --execute-peer.
A changed subset returns a new review ID/revision; an unchanged subset keeps both.
Repeat the identical select input explicitly to recover its lost current replacement reply.
review current reads the one unused review in this agent, including one created by another local client.
It may discard canceled or stale unused in-memory admission; saved outcome uncertainty remains.
No unused review does not mean nothing was executed. It does not list historical runs.
Recovering a review never applies it. Review every row again before explicit confirmation.
Apply must use the exact returned review and subset.
Apply requires --confirm. It may initialize private local evidence and may upgrade each target's
operation journal to v2. Older v1-only agents cannot read that journal; never delete it to downgrade.
There is no rollback, atomic multi-peer transaction, automatic retry or restored runnable job.
status reads local historical evidence without network calls. refresh explicitly queries only
selected peers of a known run, preserving original operation IDs; it never dispatches unsent work.
Unknown outcomes remain barriers even after an unavailable query. Target evidence durability and
local record durability are independent. Cancel stops further admission; admitted work may finish.
Advanced preview: --selection-file FILE replaces all --member and transfer-choice flags. This is
bounded data-only Selection JSON with optional per-peer overrides, never a command or grant.
--dry-run contacts no agent and writes no profile. It cannot establish that a review still exists.
--json is language independent. Human follow-up examples retain the selected --state-dir.`

const resourceGroupHelpJA = `固定したリソース選択を確認して適用（ソースビルド）

  soba resource group preview --member PEER_KEY,RESOURCE_ID,GRANT_ID,GRANT_REVISION [--member ...] --concurrent-files default|N --concurrent-per-peer default|N [--json]
  soba resource group select --review-id REVIEW_ID --revision REVIEW_REVISION --execute-peer PEER_KEY [--execute-peer ...] [--json]
  soba resource group apply --review-id REVIEW_ID --revision REVIEW_REVISION --execute-peer PEER_KEY [--execute-peer ...] --confirm [--json]
  soba resource group review current [--json]
  soba resource group status --run-id RUN_ID [--json]
  soba resource group refresh --run-id RUN_ID --peer PEER_KEY [--peer ...] [--json]
  soba resource group cancel --review-id REVIEW_OR_RUN_ID [--json]

preview は失敗した相手も省略しません。指定値と実効値を確認してください。
正確な相手を最大16台指定できます。ワイルドカード・動的なグループ・遠隔コマンド・自動分割はありません。
確認内容は現在の本体プロセス内でのみ有効です。表示や JSON 自体は実行許可になりません。
既存の確認を置き換えるには --replace-review-id を明示します。
select で実行可能な相手の一部を確認できます。任意の --review-file REVIEW.json は両確認識別フラグを
置き換え、保存した preview --json の出力と保持された行の一致も検査します。
--execute-peer の代わりに --exclude-all を指定すると、実行できない空の選択を明示できます。
相手の選択が変わると新しい確認 ID・変更番号が返り、変更しない場合は両方を維持します。
select の応答を失った場合、同一の入力を明示的に再送すると現在の置換後の確認を取得できます。
review current は本体内の未使用の確認を1件読みます。別のローカルクライアントの確認も対象です。
取消済み・古いメモリ内の実行受付を破棄する場合があります。保存された結果不明の遮断は維持します。
未使用の確認がないことは、過去に未実行だった証拠にはなりません。過去の実行一覧は取得しません。
確認の取得では適用しません。全行を再確認し、改めて明示的に承認してください。
apply は返された正確な確認と相手の一覧を使います。
apply には --confirm が必要です。ローカルの非公開記録を初期化し、対象の操作記録を v2 に
更新する場合があります。v1 専用の旧版は読めなくなります。旧版へ戻すために記録を削除しないでください。
全相手の一括トランザクション・取り消しによる復元・自動再試行・再起動後の自動実行はありません。
status は通信せずローカルの過去の記録を読みます。refresh は既知の実行に含まれる相手だけを
元の操作 ID で明示的に照会します。未実行の操作は開始しません。
取得不能な照会後も結果不明の状態は新規操作を遮断します。相手側とローカルの記録の永続性は別です。
cancel は新たな実行開始を止めます。受理済みの処理は完了する場合があります。
高度な preview: --selection-file FILE は --member と両設定フラグをすべて置き換えます。
ファイルは上限付きの Selection JSON で、相手ごとの上書きを含められます。コマンドや許可にはなりません。
--dry-run は本体に接続せず保存状態を書き換えません。確認内容が現存するかは検証できません。
--json は言語によらず同じ形式です。案内する次のコマンドは選択中の --state-dir を維持します。`

type resourceGroupValues []string

func (v *resourceGroupValues) String() string { return strings.Join(*v, ",") }
func (v *resourceGroupValues) Set(s string) error {
	if len(*v) >= resourcegroup.MaxMembers || len(s) > 512 {
		return resourceCollectionError("resource_group_invalid", nil)
	}
	*v = append(*v, s)
	return nil
}

func resourceGroupCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, client controlCaller) error {
	if len(args) == 0 || len(args) == 1 && resourceCollectionHelp(args[0]) || len(args) == 2 && resourceCollectionHelp(args[1]) {
		_, err := fmt.Fprintln(out, text(ja, resourceGroupHelpEN, resourceGroupHelpJA))
		return err
	}
	if args[0] == "review" {
		return resourceGroupCurrentReviewCLI(ctx, args[1:], dir, ja, dryRun, out, client)
	}
	action := args[0]
	switch action {
	case "preview", "select", "apply", "status", "refresh", "cancel":
	default:
		return resourceCollectionError("resource_group_invalid", nil)
	}
	f := commandFlags("resource group "+action, ja, out)
	machine := f.Bool("json", false, text(ja, "stable machine JSON", "機械処理用の安定した JSON"))
	var members, execution, peers resourceGroupValues
	var files, perPeer, selectionPath, reviewPath, replaceID, reviewID, revision, runID string
	var confirm, excludeAll bool
	switch action {
	case "preview":
		f.Var(&members, "member", text(ja, "repeat exact peer,resource,grant,revision tuple", "相手,リソース,許可,変更番号の正確な組を繰り返し指定"))
		f.StringVar(&files, "concurrent-files", "", text(ja, "template files: default or positive integer", "共通の同時ファイル数: default または正の整数"))
		f.StringVar(&perPeer, "concurrent-per-peer", "", text(ja, "template per-peer files: default or positive integer", "共通の相手ごとの同時数: default または正の整数"))
		f.StringVar(&selectionPath, "selection-file", "", text(ja, "advanced bounded Selection JSON file", "高度な上限付き Selection JSON ファイル"))
		f.StringVar(&replaceID, "replace-review-id", "", text(ja, "explicit current review to replace", "置き換える現在の確認 ID を明示"))
	case "select":
		f.StringVar(&reviewID, "review-id", "", text(ja, "exact current review ID", "現在の正確な確認 ID"))
		f.StringVar(&revision, "revision", "", text(ja, "exact reviewed revision", "確認した正確な変更番号"))
		f.StringVar(&reviewPath, "review-file", "", text(ja, "optional saved preview response instead of identity flags", "確認識別フラグの代わりとなる任意の保存済み preview 応答"))
		f.Var(&execution, "execute-peer", text(ja, "repeat exact ready subset peer", "実行可能な相手の正確な鍵を繰り返し指定"))
		f.BoolVar(&excludeAll, "exclude-all", false, text(ja, "explicitly select an empty, non-executable subset", "空の実行不能な選択を明示"))
	case "apply":
		f.StringVar(&reviewID, "review-id", "", text(ja, "exact current review ID", "現在の正確な確認 ID"))
		f.StringVar(&revision, "revision", "", text(ja, "exact reviewed revision", "確認した正確な変更番号"))
		f.Var(&execution, "execute-peer", text(ja, "repeat every reviewed execution peer", "確認済みの実行対象を省略せず繰り返し指定"))
		f.BoolVar(&confirm, "confirm", false, text(ja, "confirm exact subset, settings and evidence/journal creation or migration", "正確な相手・設定・記録の作成や形式更新を承認"))
	case "status", "refresh":
		f.StringVar(&runID, "run-id", "", text(ja, "exact known historical run ID", "既知の過去の実行の正確な ID"))
		if action == "refresh" {
			f.Var(&peers, "peer", text(ja, "repeat exact run peers to query read-only", "読み取り専用で照会する実行内の相手を繰り返し指定"))
		}
	case "cancel":
		f.StringVar(&reviewID, "review-id", "", text(ja, "known review or run ID to stop", "停止する既知の確認または実行 ID"))
	}
	if err := parseFlags(f, args[1:], ja); err != nil {
		return err
	}
	var payload any
	var previous *resourcegroup.PreparedView
	name := ""
	invalid := func() error { return resourceCollectionError("resource_group_invalid", nil) }
	switch action {
	case "preview":
		var selection resourcegroup.Selection
		if selectionPath != "" {
			if resourceCollectionFlagSet(f, "member", "concurrent-files", "concurrent-per-peer") {
				return invalid()
			}
			data, err := resourceGroupReadFile(ctx, selectionPath, resourcegroup.MaxSelectionBytes)
			if err != nil {
				return err
			}
			selection, err = resourcegroup.DecodeSelection(data)
			if err != nil {
				return invalid()
			}
		} else {
			if resourceCollectionFlagSet(f, "selection-file") {
				return invalid()
			}
			fc, err := resourceCLIChoice(files, false)
			if err != nil {
				return invalid()
			}
			pc, err := resourceCLIChoice(perPeer, false)
			if err != nil {
				return invalid()
			}
			template, err := resourcegroup.NewTemplate(resource.Settings{TransferConcurrentFiles: fc, TransferConcurrentPerPeer: pc})
			if err != nil {
				return invalid()
			}
			selection = resourcegroup.Selection{SchemaVersion: resourcegroup.SchemaVersion, Template: template, Members: []resourcegroup.Member{}}
			for _, value := range members {
				fields := strings.Split(value, ",")
				if len(fields) != 4 {
					return invalid()
				}
				grantRevision, err := strconv.ParseUint(fields[3], 10, 64)
				if err != nil {
					return invalid()
				}
				selection.Members = append(selection.Members, resourcegroup.Member{PeerKey: fields[0], Selector: resourcegrant.ManagementSelector{ProtocolVersion: resourcegrant.ManagementProtocolVersion, Target: resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: fields[1]}, GrantID: fields[2], GrantRevision: grantRevision}})
			}
		}
		in := resourcegroup.PreviewInput{SchemaVersion: resourcegroup.SchemaVersion, Selection: selection, ReplaceReviewID: replaceID}
		raw, _ := json.Marshal(in)
		canonical, err := resourcegroup.DecodePreviewInput(raw)
		if err != nil {
			return invalid()
		}
		payload, name = canonical, resourcegroup.LocalPreviewCommand
	case "select":
		if excludeAll == (len(execution) != 0) {
			return invalid()
		}
		if resourceCollectionFlagSet(f, "review-file") {
			if reviewPath == "" || resourceCollectionFlagSet(f, "review-id", "revision") {
				return invalid()
			}
			data, err := resourceGroupReadFile(ctx, reviewPath, resourcegroup.MaxLocalResponseBytes)
			if err != nil {
				return err
			}
			view, err := resourceGroupDecodePrepared(data)
			if err != nil || view.AdmissionState != resourcegroup.AdmissionPrepared {
				return invalid()
			}
			previous, reviewID, revision = &view, view.ReviewID, view.Review.Revision
		}
		in := resourcegroup.SelectInput{SchemaVersion: resourcegroup.SchemaVersion, ReviewID: reviewID, ReviewRevision: revision, ExecutionPeers: append([]string{}, execution...)}
		raw, _ := json.Marshal(in)
		canonical, err := resourcegroup.DecodeSelectInput(raw)
		if err != nil {
			return invalid()
		}
		if previous != nil {
			if _, err := resourcegroup.BuildReview(previous.Review.Selection, previous.Review.Rows, canonical.ExecutionPeers); err != nil {
				return invalid()
			}
		}
		payload, name = canonical, resourcegroup.LocalSelectCommand
	case "apply":
		in := resourcegroup.ApplyInput{SchemaVersion: resourcegroup.SchemaVersion, ReviewID: reviewID, ReviewRevision: revision, ExecutionPeers: append([]string{}, execution...), Confirm: confirm}
		raw, _ := json.Marshal(in)
		canonical, err := resourcegroup.DecodeApplyInput(raw)
		if err != nil {
			return invalid()
		}
		payload, name = canonical, resourcegroup.LocalApplyCommand
	case "status":
		in := resourcegroup.StatusInput{SchemaVersion: resourcegroup.SchemaVersion, RunID: runID}
		if in.Validate() != nil {
			return invalid()
		}
		payload, name = in, resourcegroup.LocalStatusCommand
	case "refresh":
		in := resourcegroup.RefreshInput{SchemaVersion: resourcegroup.SchemaVersion, RunID: runID, Peers: append([]string{}, peers...)}
		raw, _ := json.Marshal(in)
		canonical, err := resourcegroup.DecodeRefreshInput(raw)
		if err != nil {
			return invalid()
		}
		payload, name = canonical, resourcegroup.LocalRefreshCommand
	case "cancel":
		in := resourcegroup.CancelInput{SchemaVersion: resourcegroup.SchemaVersion, ReviewID: reviewID}
		if in.Validate() != nil {
			return invalid()
		}
		payload, name = in, resourcegroup.LocalCancelCommand
	}
	if dryRun {
		return resourceCollectionJSON(out, map[string]any{"applied": false, "command": name, "payload": payload, "validation": "local-input-only"})
	}
	raw, err := resourceCollectionCall(ctx, dir, name, payload, client)
	if err != nil {
		return err
	}
	return resourceGroupResponse(out, dir, ja, *machine, name, payload, previous, raw)
}

func resourceCollectionHelp(s string) bool { return s == "--help" || s == "-h" || s == "help" }
func resourceCollectionFlagSet(f *flag.FlagSet, names ...string) bool {
	found := false
	f.Visit(func(v *flag.Flag) {
		for _, name := range names {
			if v.Name == name {
				found = true
			}
		}
	})
	return found
}
func resourceCollectionJSON(out io.Writer, v any) error {
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
func resourceCollectionCall(ctx context.Context, dir, name string, payload any, client controlCaller) (json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := json.Marshal(webui.Command{RequestID: newCLIRequestID("cli-resource-collection"), Name: name, Payload: raw})
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	if err := client(ctx, dir, string(request), &result); err != nil {
		return nil, resourceCollectionControlError(err)
	}
	return result, nil
}

// Reuse the existing cross-platform no-follow, nonblocking regular-file opener.
// Both metadata and actual bytes are bounded; paths and OS errors stay private.
func resourceGroupReadFile(ctx context.Context, path string, limit int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := (transfer.Source{LocalPath: path}).Open()
	if err != nil {
		return nil, resourceCollectionError("resource_group_file_invalid", err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > int64(limit) {
		return nil, resourceCollectionError("resource_group_file_invalid", nil)
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	after, statErr := file.Stat()
	current, pathErr := os.Lstat(path)
	if err != nil || statErr != nil || pathErr != nil || len(data) > limit || !after.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(before, after) || !os.SameFile(after, current) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return nil, resourceCollectionError("resource_group_file_invalid", nil)
	}
	return data, nil
}

func resourceGroupSamePeers(a, b []string) bool {
	left, right := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(left)
	sort.Strings(right)
	return reflect.DeepEqual(left, right)
}
