package main

import (
	"fmt"
	"io"
	"reflect"
	"strconv"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

func resourceGroupResponse(out io.Writer, dir string, ja, machine bool, name string, input any, previous *resourcegroup.PreparedView, raw []byte) error {
	invalid := func() error { return resourceCollectionError("resource_group_response_invalid", nil) }
	switch name {
	case resourcegroup.LocalPreviewCommand, resourcegroup.LocalSelectCommand:
		view, err := resourceGroupDecodePrepared(raw)
		if err != nil {
			return invalid()
		}
		if name == resourcegroup.LocalPreviewCommand {
			in := input.(resourcegroup.PreviewInput)
			resolved, err := resourcegroup.ResolveSelection(in.Selection)
			if err != nil || !reflect.DeepEqual(view.Review.Selection, resolved) || view.ReviewID == in.ReplaceReviewID || view.AdmissionState == resourcegroup.AdmissionCanceled {
				return invalid()
			}
			ready := []string{}
			for _, row := range view.Review.Rows {
				if row.State == resourcegroup.ReviewReady {
					ready = append(ready, row.PeerKey)
				}
			}
			if !resourceGroupSamePeers(ready, view.Review.ExecutionPeers) {
				return invalid()
			}
		} else {
			in := input.(resourcegroup.SelectInput)
			if (view.ReviewID == in.ReviewID) != (view.Review.Revision == in.ReviewRevision) || view.AdmissionState == resourcegroup.AdmissionCanceled || !resourceGroupSamePeers(view.Review.ExecutionPeers, in.ExecutionPeers) {
				return invalid()
			}
			if previous != nil && (!reflect.DeepEqual(view.Review.Selection, previous.Review.Selection) || !reflect.DeepEqual(view.Review.Rows, previous.Review.Rows) || *view.InitializesLocalEvidence != *previous.InitializesLocalEvidence) {
				return invalid()
			}
		}
		if machine {
			return resourceCollectionJSON(out, view)
		}
		return writeResourceGroupPrepared(out, dir, ja, view)
	case resourcegroup.LocalApplyCommand, resourcegroup.LocalStatusCommand, resourcegroup.LocalRefreshCommand:
		view, err := resourcegroup.DecodeRunView(raw)
		if err != nil {
			return invalid()
		}
		switch in := input.(type) {
		case resourcegroup.ApplyInput:
			if view.RunID != in.ReviewID || view.Review.Revision != in.ReviewRevision || !resourceGroupSamePeers(view.Review.ExecutionPeers, in.ExecutionPeers) {
				return invalid()
			}
		case resourcegroup.StatusInput:
			if view.RunID != in.RunID {
				return invalid()
			}
		case resourcegroup.RefreshInput:
			if view.RunID != in.RunID {
				return invalid()
			}
			for _, peer := range in.Peers {
				found := false
				for _, row := range view.Evidence.Members {
					if row.PeerKey == peer {
						found = true
					}
				}
				if !found {
					return invalid()
				}
			}
		default:
			return invalid()
		}
		if machine {
			return resourceCollectionJSON(out, view)
		}
		return writeResourceGroupRun(out, dir, ja, view)
	case resourcegroup.LocalCancelCommand:
		view, err := resourcegroup.DecodeCancelView(raw)
		if err != nil {
			return invalid()
		}
		id := input.(resourcegroup.CancelInput).ReviewID
		if view.Prepared != nil && view.Prepared.ReviewID != id || view.Run != nil && view.Run.RunID != id {
			return invalid()
		}
		if machine {
			return resourceCollectionJSON(out, view)
		}
		if view.Prepared != nil {
			view.Prepared.Review, _ = resourcegroup.CloneReview(view.Prepared.Review)
			return writeResourceGroupPrepared(out, dir, ja, *view.Prepared)
		}
		return writeResourceGroupRun(out, dir, ja, *view.Run)
	}
	return invalid()
}

func resourceGroupDecodePrepared(raw []byte) (resourcegroup.PreparedView, error) {
	view, err := resourcegroup.DecodePreparedView(raw)
	if err != nil {
		return resourcegroup.PreparedView{}, err
	}
	view.Review, err = resourcegroup.CloneReview(view.Review)
	return view, err
}

func resourceGroupChoice(c capacity.Choice) string {
	if c.Mode == "default" {
		return "default"
	}
	if c.Value != nil {
		return strconv.FormatInt(*c.Value, 10)
	}
	return "?"
}
func writeResourceGroupPrepared(out io.Writer, dir string, ja bool, view resourcegroup.PreparedView) error {
	var b resourceCollectionText
	b.line("%s", text(ja, "Prepared review; current owning agent process only. Displayed data does not authorize execution.", "確認内容は現在の本体プロセス内でのみ有効です。表示データは実行を許可しません。"))
	b.line("reviewId: %s\nrevision: %s\n%s: %s", view.ReviewID, view.Review.Revision, text(ja, "Admission", "受付状態"), resourceCollectionState(ja, string(view.AdmissionState)))
	b.line(text(ja, "Selected: %d; execution subset: %d; excluded: %d", "選択: %d台、実行対象: %d台、除外: %d台"), len(view.Review.Rows), len(view.Review.ExecutionPeers), len(view.Review.Rows)-len(view.Review.ExecutionPeers))
	for i, row := range view.Review.Rows {
		member := view.Review.Selection.Members[i]
		selected := false
		for _, peer := range view.Review.ExecutionPeers {
			if row.PeerKey == peer {
				selected = true
			}
		}
		b.line("%s | %s | %s", row.PeerKey, resourceCollectionState(ja, string(row.State)), text(ja, map[bool]string{true: "execution selected", false: "excluded"}[selected], map[bool]string{true: "実行対象", false: "除外"}[selected]))
		b.line("  %s: %s; grantId: %s; grantRevision: %d", text(ja, "Resource", "リソース"), member.Selector.Target.ResourceID, member.Selector.GrantID, member.Selector.GrantRevision)
		b.line("  %s: concurrent-files=%s, concurrent-per-peer=%s", text(ja, "Requested", "指定値"), resourceGroupChoice(member.Requested.TransferConcurrentFiles), resourceGroupChoice(member.Requested.TransferConcurrentPerPeer))
		if member.Override != nil {
			b.line("  %s", text(ja, "Explicit per-peer override retained.", "相手ごとの明示的な上書きを保持しています。"))
		}
		if row.Reply != nil {
			b.line("  %s: concurrent-files=%d, concurrent-per-peer=%d", text(ja, "Effective at preview", "確認時の実効値"), row.Reply.Preview.Effective.TransferConcurrentFiles, row.Reply.Preview.Effective.TransferConcurrentPerPeer)
		}
	}
	if *view.InitializesLocalEvidence {
		b.line("%s", text(ja, "Apply will initialize private local group evidence storage.", "apply はローカルの非公開グループ記録を初期化します。"))
	}
	b.line("%s", text(ja, "Apply may upgrade target operation journals to v2. Older v1-only agents cannot read them. No rollback or all-peer transaction.", "apply は対象の操作記録を v2 に更新する場合があります。v1 専用の旧版は読めません。復元や全相手の一括トランザクションはありません。"))
	if view.AdmissionState == resourcegroup.AdmissionPrepared {
		args := []string{"resource", "group", "apply", "--review-id", view.ReviewID, "--revision", view.Review.Revision}
		for _, peer := range view.Review.ExecutionPeers {
			args = append(args, "--execute-peer", peer)
		}
		args = append(args, "--confirm")
		b.line("%s: %s", text(ja, "After reviewing every row and storage change", "全行と保存状態の変更を確認してから"), cliCommandExample(dir, args...))
		b.line("%s: %s", text(ja, "Cancel this review", "この確認を取り消す"), cliCommandExample(dir, "resource", "group", "cancel", "--review-id", view.ReviewID))
	} else {
		b.line("%s", text(ja, "This view cannot be applied. Inspect failed peers or create a fresh explicit preview.", "この表示からは適用できません。失敗した相手を確認するか、明示的に新しい preview を作成してください。"))
	}
	return b.write(out)
}

func writeResourceGroupRun(out io.Writer, dir string, ja bool, view resourcegroup.RunView) error {
	var b resourceCollectionText
	b.line("%s", text(ja, "Historical run evidence; this does not describe current settings or transfer completion.", "過去の実行記録です。現在の設定やファイル転送の完了を示すものではありません。"))
	b.line("runId: %s\nrevision: %s", view.RunID, view.Review.Revision)
	b.line("%s: %s | %s: %s | %s: %s", text(ja, "Accepted", "受付時刻"), time.Unix(view.AcceptedAt, 0).UTC().Format(time.RFC3339), text(ja, "Activity", "処理状態"), resourceCollectionState(ja, string(view.Activity)), text(ja, "Local durability", "ローカル記録の永続性"), resourceCollectionState(ja, string(view.LocalDurability)))
	s := view.Summary
	b.line(text(ja, "Selected: %d; execution subset: %d; excluded: %d; preview failures: %d", "選択: %d台、実行対象: %d台、除外: %d台、確認失敗: %d台"), s.Selected, s.Executable, s.Excluded, s.ReviewFailures)
	b.line(text(ja, "Applied: %d; failed: %d; not attempted: %d; dispatch unknown: %d; target unknown: %d", "適用済み: %d台、失敗: %d台、未試行: %d台、送信結果不明: %d台、相手結果不明: %d台"), s.Applied, s.Failed, s.NotAttempted, s.DispatchUnknown, s.TargetUnknown)
	b.line(text(ja, "Admission finished: %t; all selected execution rows durably applied: %t; reconciliation required: %t", "受付処理終了: %t、実行対象の全行に永続的な適用記録あり: %t、照合が必要: %t"), s.AdmissionFinished, s.AllApplied, s.ReconciliationRequired)
	refreshPeers := []string{}
	for _, row := range view.Evidence.Members {
		b.line("%s | %s | %s | %s: %s", row.PeerKey, resourceCollectionState(ja, string(row.Execution)), resourceCollectionState(ja, string(row.Dispatch)), text(ja, "Local durability", "ローカル永続性"), resourceCollectionState(ja, string(row.LocalDurability)))
		if row.Target != nil {
			b.line("  %s: %s | %s: %t", text(ja, "Historical target outcome", "過去の相手側結果"), resourceCollectionState(ja, row.Target.Outcome.Status), text(ja, "Target evidence durable", "相手の操作記録が永続化済み"), *row.Target.EvidenceDurable)
		}
		b.line("  %s: %s | %s: %s", text(ja, "Last status query", "最後の状態照会"), resourceCollectionState(ja, string(row.Status.State)), text(ja, "Admission stop", "受付停止理由"), resourceCollectionState(ja, string(row.AdmissionStop)))
		if row.Status.ObservedAt > 0 {
			b.line("  %s: %s", text(ja, "Observed locally", "ローカル観測時刻"), time.Unix(row.Status.ObservedAt, 0).UTC().Format(time.RFC3339))
		}
		if row.Execution == resourcegroup.ExecutionSelected && row.Dispatch != resourcegroup.DispatchNotAttempted {
			refreshPeers = append(refreshPeers, row.PeerKey)
		}
	}
	if s.ReconciliationRequired {
		b.line("%s", text(ja, "Unknown or non-durable evidence remains a barrier. An unavailable query does not clear it. Do not invent a replacement operation or blindly apply again.", "結果不明または永続性未確認の記録は新規操作を遮断します。取得不能な照会で解除されません。別の操作を作ったり、確認せず再適用したりしないでください。"))
	}
	b.line("%s: %s", text(ja, "Read this local record without network", "通信せずローカル記録を読む"), cliCommandExample(dir, "resource", "group", "status", "--run-id", view.RunID))
	if len(refreshPeers) > 0 {
		args := []string{"resource", "group", "refresh", "--run-id", view.RunID}
		for _, peer := range refreshPeers {
			args = append(args, "--peer", peer)
		}
		b.line("%s: %s", text(ja, "Explicit read-only query of attempted peers", "試行済みの相手を明示的に読み取り専用で照会"), cliCommandExample(dir, args...))
	}
	b.line("%s", text(ja, "Cancellation stops further admission; already admitted work may finish. Records never become runnable jobs after restart.", "取り消しは新たな受付を止めます。受理済みの処理は完了する場合があります。再起動後に記録から自動実行することはありません。"))
	return b.write(out)
}

type resourceCollectionText struct{ lines []string }

func (b *resourceCollectionText) line(format string, args ...any) {
	b.lines = append(b.lines, fmt.Sprintf(format, args...))
}
func (b *resourceCollectionText) write(out io.Writer) error {
	for _, s := range b.lines {
		if _, err := fmt.Fprintln(out, s); err != nil {
			return err
		}
	}
	return nil
}

// Human labels only: never translate fields or user values in machine output.
func resourceCollectionState(ja bool, state string) string {
	if !ja {
		return state
	}
	translations := map[string]string{
		"prepared": "確認済み・未適用", "canceled": "取り消し済み", "unavailable": "取得不能", "idle": "待機中", "applying": "適用中", "refreshing": "照会中",
		"ready": "実行候補", "unsupported": "非対応", "invalid_reply": "応答不正", "canceled_before_preview": "確認前に取り消し", "selected": "実行対象", "excluded": "除外",
		"not_attempted": "未試行", "dispatching": "送信中", "observed": "観測済み", "unknown": "結果不明", "not_saved": "未保存", "durable": "永続化済み", "uncertain": "永続性不明",
		"not_queried": "未照会", "query_failed": "照会失敗", "none": "なし", "user_canceled": "ユーザーによる取り消し", "budget_exhausted": "上限到達", "context_changed": "実行環境の変更", "persistence_uncertain": "保存結果不明", "restarted": "再起動",
		"applied": "適用済み", "failed": "失敗", "saved_not_applied": "保存済み・未適用", "unconfirmed": "未確認", "current": "観測時点で確認済み", "stale": "古い観測", "invalid": "不正", "limited": "上限による一部表示",
		"persistent": "保存中のみ有効な識別子", "activation": "今回の有効化中のみ有効な識別子", "process": "現在のプロセス内のみ有効な識別子", "saved": "保存済み", "starting": "開始中", "active": "有効", "reconnecting": "再接続中", "expired": "期限切れ", "stopped": "停止中",
		"awaiting-acceptance": "受領確認待ち", "queued": "待機列", "transferring": "転送中", "saving": "保存中", "completed": "完了", "cancelled": "取り消し済み", "declined": "辞退",
	}
	if translated, ok := translations[state]; ok {
		return translated
	}
	return state
}
