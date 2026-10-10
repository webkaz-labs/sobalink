package main

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
)

func writeResourceCatalogHuman(out io.Writer, dir string, ja bool, response core.ResourceCatalogResponse) error {
	return writeResourceCatalogHumanAt(out, dir, ja, response, time.Now().UnixMilli())
}
func writeResourceCatalogHumanAt(out io.Writer, dir string, ja bool, response core.ResourceCatalogResponse, now int64) error {
	var b resourceCollectionText
	snapshot := response.Snapshot
	b.line("%s", text(ja, "Local catalog observation. Each source has its own observed time; this is not continuous monitoring or an atomic cross-source read.", "ローカルのカタログ観測です。情報源ごとに観測時刻が異なり、常時監視や全情報源の同時読み取りではありません。"))
	b.line("%s: %s\n%s: %s", text(ja, "Scope", "範囲"), displayText(snapshot.ScopeID), text(ja, "Observation", "観測 ID"), displayText(snapshot.ID))
	b.line("%s: %t", text(ja, "Complete for exactly the selected sources", "明示した情報源の範囲内で完全"), snapshot.Complete)
	if !snapshot.Complete {
		b.line("%s", text(ja, "Partial, limited or unresolved sources remain visible. Missing rows do not prove deletion or absence.", "一部取得・上限到達・未解決の情報源も表示します。行がないことは削除や不存在の証明ではありません。"))
	}
	b.line("%s", text(ja, "Rows show observation state and identity lifetime. Existing workflows obtain a fresh review before any change.", "行は観測時の状態と識別子の有効期間を示します。変更前には既存の操作で新たに確認します。"))
	for _, source := range snapshot.Sources {
		b.line("%s | %s | %s: %t | %s: %d", resourceCatalogKind(ja, source.Selection.Kind), resourceCollectionState(ja, source.State), text(ja, "Complete", "完全"), source.Complete, text(ja, "Visible rows", "表示行数"), len(source.Rows))
		if source.Selection.PeerKey != "" {
			b.line("  %s: %s", text(ja, "Exact peer", "正確な相手"), displayText(source.Selection.PeerKey))
		}
		if source.Selection.ProcessID != "" {
			b.line("  %s: %s", text(ja, "Process-local identity scope", "現在のプロセス内の識別範囲"), displayText(source.Selection.ProcessID))
		}
		if source.CheckedAt > 0 {
			b.line("  %s: %s", text(ja, "Observed", "観測時刻"), time.UnixMilli(source.CheckedAt).UTC().Format(time.RFC3339Nano))
		} else {
			b.line("  %s", text(ja, "No successful source observation is available.", "利用できる情報源の観測はまだありません。"))
		}
		if len(source.Rows) == 0 && source.Complete && source.State == "current" {
			b.line("  %s", text(ja, "Successfully observed an empty source.", "空の情報源であることを観測しました。"))
		}
		if source.Selection.Kind == resourcecatalog.RemoteService && source.State == "stale" {
			b.line("  %s", text(ja, "Cached discovery remains non-actionable in this source build. Continue through the existing discovery/connect workflow.", "今回のソースでは発見キャッシュから操作できません。既存のサービス発見・接続の手順を使ってください。"))
		}
		for _, row := range source.Rows {
			b.line("  %s | %s", displayText(row.Identity.ID), resourceCollectionState(ja, row.Identity.Lifetime))
			switch {
			case row.LocalSettings != nil:
				value := row.LocalSettings
				b.line("    %s: concurrent-files=%s, concurrent-per-peer=%s", text(ja, "Requested", "指定値"), resourceGroupChoice(value.Requested.TransferConcurrentFiles), resourceGroupChoice(value.Requested.TransferConcurrentPerPeer))
				b.line("    %s: concurrent-files=%d, concurrent-per-peer=%d", text(ja, "Effective", "実効値"), value.Effective.TransferConcurrentFiles, value.Effective.TransferConcurrentPerPeer)
			case row.RemoteSettingsV1 != nil || row.RemoteSettingsV2 != nil:
				value := row.RemoteSettingsV1
				if value == nil {
					value = row.RemoteSettingsV2
				}
				b.line("    %s: concurrent-files=%s, concurrent-per-peer=%s", text(ja, "Requested", "指定値"), resourceGroupChoice(value.Requested.TransferConcurrentFiles), resourceGroupChoice(value.Requested.TransferConcurrentPerPeer))
				b.line("    %s: concurrent-files=%d, concurrent-per-peer=%d", text(ja, "Effective", "実効値"), value.Effective.TransferConcurrentFiles, value.Effective.TransferConcurrentPerPeer)
			case row.LocalService != nil:
				value := row.LocalService
				b.line("    %s | %s | %s %s | %s | %s", displayText(value.Name), text(ja, value.Direction, map[string]string{"share": "共有", "forward": "接続"}[value.Direction]), value.Network, value.Ports, resourceCatalogLifetime(ja, value.Lifetime), resourceCollectionState(ja, value.State))
				b.line("    %s", text(ja, "Application success remains unverified.", "アプリケーションでの利用成功は未確認です。"))
			case row.RemoteService != nil:
				value := row.RemoteService
				b.line("    %s | %s %s | %s", displayText(value.Purpose), value.Network, value.Ports, resourceCatalogLifetime(ja, value.Lifetime))
				if value.ExpiresAt > 0 {
					b.line("    %s: %s", text(ja, "Expires", "有効期限"), time.UnixMilli(value.ExpiresAt).UTC().Format(time.RFC3339Nano))
				}
				b.line("    %s", text(ja, "Application success remains unverified. Discovery IDs last only for this activation.", "アプリケーションでの利用成功は未確認です。発見 ID は今回の有効化中のみ有効です。"))
			case row.TransferActivity != nil:
				value := row.TransferActivity
				b.line("    %s | %s | %s: %s | %d/%d %s", text(ja, row.Identity.Direction, map[string]string{"incoming": "受信", "outgoing": "送信"}[row.Identity.Direction]), resourceCollectionState(ja, value.State), text(ja, "Peer", "相手"), displayText(value.PeerID), value.CompletedBytes, value.TotalBytes, text(ja, "bytes", "バイト"))
				b.line("    %s", text(ja, "Process-local batch activity; no durable file share or file contents are exposed.", "現在のプロセスのバッチ状況です。永続的なファイル共有や内容は公開しません。"))
			}
			workflows := resourcecatalog.Workflows(source, row, now, response.Limits.CatalogLimits())
			if len(workflows) == 0 {
				b.line("    %s", text(ja, "Refresh this source explicitly before choosing its existing workflow.", "既存の操作を選ぶ前に、この情報源を明示的に更新してください。"))
				continue
			}
			switch source.Selection.Kind {
			case resourcecatalog.LocalSettings:
				b.line("    %s: %s", text(ja, "Inspect before a fresh settings review", "新たな設定確認の前に参照"), cliCommandExample(dir, "resource", "inspect", "--id", row.Identity.ID))
			case resourcecatalog.RemoteSettingsV1, resourcecatalog.RemoteSettingsV2:
				args := []string{"resource", "remote"}
				if source.Selection.Kind == resourcecatalog.RemoteSettingsV2 {
					args = append(args, "manage")
				}
				args = append(args, "inspect", "--peer", source.Selection.PeerKey, "--id", row.Identity.ID, "--grant-id", source.Selection.GrantID, "--grant-revision", strconv.FormatUint(source.Selection.GrantRevision, 10))
				b.line("    %s: %s", text(ja, "Fresh authorized inspection", "新たに許可を検査して参照"), cliCommandExample(dir, args...))
			case resourcecatalog.LocalService:
				b.line("    %s: %s", text(ja, "Review exact saved service", "正確な保存済みサービスを確認"), cliCommandExample(dir, "service", "show", row.Identity.ID))
				for _, workflow := range workflows {
					if workflow.Kind == "review_service_start" {
						b.line("    %s: %s", text(ja, "Preview the existing start workflow", "既存の開始操作を事前確認"), cliCommandExample(dir, "--dry-run", "services", "start", row.Identity.ID))
					}
					if workflow.Kind == "review_service_stop" {
						b.line("    %s: %s", text(ja, "Preview the existing stop workflow", "既存の停止操作を事前確認"), cliCommandExample(dir, "--dry-run", "services", "stop", row.Identity.ID))
					}
				}
			case resourcecatalog.RemoteService:
				b.line("    %s: %s", text(ja, "Refresh discovery before reviewing an advertised connection", "公開された接続を確認する前にサービス発見を更新"), cliCommandExample(dir, "discover", "--peer", source.Selection.PeerKey))
				b.line("    %s", text(ja, "Then choose this exact activation in the existing connect workflow and review local port and lifetime.", "その後、既存の接続操作でこの正確な有効化を選び、ローカルポートと有効期間を確認してください。"))
			case resourcecatalog.TransferActivity:
				b.line("    %s: %s", text(ja, "Open the existing transfer view for this peer, direction and original batch ID", "この相手・方向・元のバッチ ID に対応する既存の転送表示を開く"), cliCommandExample(dir, "ui"))
			}
		}
		if !source.Complete || source.State != "current" {
			if source.Selection.Kind == resourcecatalog.RemoteService {
				b.line("  %s: %s", text(ja, "Explicit discovery refresh", "明示的なサービス発見の更新"), cliCommandExample(dir, "discover", "--peer", source.Selection.PeerKey))
			}
		}
	}
	b.line("%s", text(ja, "No operation was applied by catalog. Fresh provider review and authorization remain required; prior observations do not confer permission.", "カタログによる適用操作はありません。情報源での新たな確認と許可検査が必要です。過去の観測は許可を与えません。"))
	return b.write(out)
}
func resourceCatalogKind(ja bool, kind string) string {
	if !ja {
		return kind
	}
	return map[string]string{resourcecatalog.LocalSettings: "ローカル設定", resourcecatalog.LocalService: "保存済みサービス", resourcecatalog.TransferActivity: "転送状況", resourcecatalog.RemoteService: "相手のサービス発見", resourcecatalog.RemoteSettingsV1: "相手の設定（参照 v1）", resourcecatalog.RemoteSettingsV2: "相手の設定（管理 v2 による参照）"}[kind]
}
func resourceCatalogLifetime(ja bool, value string) string {
	if value == "" {
		return text(ja, "provider-defined lifetime", "情報源の規定の有効期間")
	}
	if !ja {
		return value
	}
	if translated, ok := map[string]string{"finite": "期限あり", "until-revoked": "失効まで", "until-stopped": "停止まで"}[value]; ok {
		return translated
	}
	return fmt.Sprintf("%s", displayText(value))
}
