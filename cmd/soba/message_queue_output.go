package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
)

const textQueueHelpEN = `Send text now or explicitly queue it for one recipient

  soba message PEER_ID TEXT                         Send immediately
  soba message --queue --peer PEER_ID [--expires-in 24h] [--confirm] [--json] TEXT
  soba message --queue --peer-name NAME [--confirm] [--json] TEXT
  soba message queue list [--cursor ID --revision PAGE_REVISION] [--json]
  soba message queue show OUTBOX_ID [--json]
  soba message queue show --request REQUEST_ID [--json]
  soba message queue commit OUTBOX_ID --process PROCESS_ID --revision REVIEW --confirm [--json]
  soba message queue cancel OUTBOX_ID [--json]
  soba message queue discard OUTBOX_ID --process PROCESS_ID [--json]

Queueing is explicit. Review the exact recipient, immutable text, local retention and
absolute sending deadline. Default: 24h; minimum: 1m; maximum: 168h (seven days).
Put options before TEXT; use -- before text beginning with a dash. Quote text for your shell.
--peer-name resolves one unique verified name; --peer selects an exact identity.
--json without --confirm prepares only, never prompts or sends. --confirm explicitly
approves the selected text and deadline; otherwise human output asks y/N.
Prepared reviews last only in the current agent process. Declining discards the preparation.
Enqueued records survive restart; use their original ID for show/cancel without a process ID.
An offline recipient is supported. The local backend must be running with a known identity;
a stopped local backend or unknown mixed-network identity cannot safely prepare a send.
Both devices must run Sobalink and become reachable. Permission, pause and identity checks apply.
Queued means accepted locally. Confirmed means receiver-save evidence, never a read receipt.
Cancel stops further sending; text already saved on the recipient is not recalled.
Text has no files or receive folders. History cleanup does not erase deduplication evidence
or necessarily free evidence capacity. Keep unknown outcomes; never erase them to make room.
A lost or storage_busy response is unconfirmed: show the ORIGINAL mt1_ ID first. If still
prepared and the review remains valid, retry its exact commit. Never create a replacement,
change text, or reset the deadline automatically. A lost preparation after restart needs
an explicit fresh review; the previous default deadline and review did not survive.
--dry-run checks local inputs only: zero agent calls, no name/identity resolution or sending.
This development version uses profile format 3. Older profiles and v0.4.0-alpha.4 are
incompatible. Keep old data unchanged; select a separate empty --state-dir. No migration/wipe.`

const textQueueHelpJA = `テキストを今すぐ送る、または相手1台への送信を明示的に予約する

  soba message PEER_ID TEXT                         即時送信
  soba message --queue --peer PEER_ID [--expires-in 24h] [--confirm] [--json] TEXT
  soba message --queue --peer-name NAME [--confirm] [--json] TEXT
  soba message queue list [--cursor ID --revision PAGE_REVISION] [--json]
  soba message queue show OUTBOX_ID [--json]
  soba message queue show --request REQUEST_ID [--json]
  soba message queue commit OUTBOX_ID --process PROCESS_ID --revision REVIEW --confirm [--json]
  soba message queue cancel OUTBOX_ID [--json]
  soba message queue discard OUTBOX_ID --process PROCESS_ID [--json]

予約は明示的な選択です。正確な宛先・変更しない本文・ローカル保持・絶対日時の送信期限を
確認してください。既定は 24h、最短は 1m、最長は 168h（7日）です。
オプションは TEXT より前に置き、ハイフンで始まる本文の前には -- を置いてください。
本文はシェルに合わせて引用します。--peer-name は一意の確認済みの名前、--peer は正確な ID を選びます。
--confirm のない --json は準備だけを行い、質問も送信も行いません。--confirm は選んだ本文と
期限の明示承認です。通常表示では --confirm がなければ y/N で確認します。
未確定の準備は本体の現在の起動中のみ有効です。承認を断ると準備を破棄します。
受付済みの記録は再起動後も残ります。元の ID で照会・停止でき、以前の起動 ID は不要です。
オフラインの相手にも予約できます。ただしローカルの接続機能が既知の識別情報で起動している
必要があり、ローカル接続の停止中や混在ネットワークで識別情報が不明な場合は準備できません。
両端末で Sobalink が起動して接続可能になる必要があり、権限・一時停止・識別情報の確認も続きます。
予約済みはローカル受付を示します。到着確認は受信側での保存の記録で、既読の証明ではありません。
cancel は追加の送信を停止します。相手に保存済みの本文は回収しません。
テキストにはファイルや受信フォルダーはありません。履歴整理でも重複防止記録は残り、記録の
容量が空くとは限りません。結果不明の記録を保管し、空きを作るために消さないでください。
応答消失・storage_busy は結果未確認です。まず元の mt1_ ID を照会し、準備済みで確認がまだ
有効な場合にだけ同じ commit を再試行してください。代わりの要求・本文の変更・期限の延長を
自動で行いません。再起動で準備を失った場合は新たな明示確認が必要です。以前の既定期限と
確認内容が残ったとは扱いません。
--dry-run はローカル入力検査のみです。本体への接続・名前や識別情報の解決・送信は行いません。
この開発版は profile 形式 3 を使い、旧形式と v0.4.0-alpha.4 とは互換性がありません。
旧データをそのまま保管し、別の空の --state-dir を選んでください。移行・初期化による消去はしません。`

func textQueueCLIStatusLabel(ja bool, status string) string {
	labels := map[string][2]string{
		"prepared": {"Prepared; confirmation required", "準備済み・承認待ち"}, "discarded": {"Preparation discarded", "準備を破棄済み"},
		"queued": {"Accepted locally", "ローカル受付済み"}, "waiting_peer": {"Waiting for the recipient", "相手への接続待ち"}, "delivering": {"Sending; save not yet confirmed", "送信中・保存は未確認"},
		"completed": {"Receiver save confirmed", "受信側での保存を確認済み"}, "cancel_requested": {"Stopping further sends", "追加の送信を停止中"}, "cancelled": {"Further sends stopped", "追加の送信を停止済み"},
		"expired": {"Sending deadline passed", "送信期限切れ"}, "paused": {"Paused", "一時停止中"}, "blocked": {"Blocked; review required", "停止中・確認が必要"}, "needs_review": {"Delivery evidence needs review", "到着の記録の確認が必要"},
	}
	label, ok := labels[status]
	if !ok {
		return status
	}
	return text(ja, label[0], label[1])
}
func textQueueCLIReason(ja bool, code string) string {
	var en, jp string
	switch code {
	case "message_storage_busy", "message_store_busy", "busy":
		en, jp = "Storage is busy. Check the original text request first; if it is prepared and still valid, retry the exact same commit. Do not replace it or assume nothing was sent.", "保存処理が使用中です。まず元のテキスト要求を照会し、準備済みで確認がまだ有効なら同じ commit を再試行してください。要求を置換したり、何も送られていないと決めつけないでください。"
	case "message_identity_unavailable", "message_stopping":
		en, jp = "The local backend identity is unavailable. Start the selected backend and check its exact identity. An offline recipient is allowed, but a stopped local backend or unknown mixed-network identity cannot prepare text.", "ローカルの接続機能の識別情報を取得できません。選択した接続機能を起動し、正確な識別情報を確認してください。相手がオフラインでも予約できますが、ローカル接続の停止中や混在ネットワークで識別情報が不明な場合は準備できません。"
	case "message_stale", "message_review_required", "message_conflict", "message_invalid":
		en, jp = "The original review is unavailable, stale or changed. Check the original ID, recipient, text and absolute deadline. A preparation lost after restart requires an explicit fresh review; do not assume its old deadline survived.", "元の確認内容が取得不能・期限切れ・変更済みです。元の ID・宛先・本文・絶対期限を確認してください。再起動で準備を失った場合は新たな明示確認が必要で、以前の期限が残ったとは扱いません。"
	case "message_not_found", "message_retired":
		en, jp = "The original request is absent or retired in this profile. Verify the original ID and selected profile, and review any delivery evidence before a new send. This is not proof of non-delivery.", "このプロファイルに元の要求がないか、有効期間が終了しています。新しい送信の前に元の ID と選択中のプロファイル、および到着の記録を確認してください。未到着の証明ではありません。"
	case "message_storage_uncertain", "message_store_storage_uncertain", "message_store_recovery_required", "message_store_unavailable", "message_unknown_lease", "message_receipt_invalid":
		en, jp = "Stored delivery evidence needs review. Keep the original request and profile data; text may already be saved on the recipient. Do not erase evidence or retry under a new ID.", "保存された到着の記録の確認が必要です。相手が本文を保存済みの可能性があるため、元の要求とプロファイルを保管してください。記録の消去や新しい ID での再送はしないでください。"
	case "message_evidence_capacity":
		en, jp = "The retained evidence budget is full. Existing status and cancellation remain available. Visible history cleanup may not free this budget; never remove unknown evidence to admit a new send.", "保持する到着・重複防止記録の容量が満杯です。既存の状態照会と停止は引き続き可能です。表示履歴の整理ではこの容量が空かない場合があり、新しい送信のために結果不明の記録を消さないでください。"
	case "message_active_capacity", "message_peer_capacity", "message_too_large":
		en, jp = "The text or active queue exceeds a configured limit. Review the text size, existing requests and available capacity; existing work is not automatically removed.", "本文または進行中の予約が設定上限を超えています。本文量・既存の要求・空き容量を確認してください。既存の処理は自動で削除されません。"
	case "message_permission", "message_identity_changed":
		en, jp = "Check the exact local and recipient identities, trust and pause settings. Do not silently substitute another identity or grant permission.", "自分と相手の正確な識別情報・信頼・一時停止設定を確認してください。別の ID への差し替えや権限付与を自動で行わないでください。"
	case "message_peer_upgrade_required":
		en, jp = "The recipient cannot confirm durable text support. Check its version; no fallback to immediate sending occurs.", "相手の永続テキスト送信への対応を確認できません。相手のバージョンを確認してください。即時送信へ自動では切り替えません。"
	case "message_clock", "message_clock_rollback", "message_store_clock_rollback":
		en, jp = "Check the device clock and original absolute deadline. Do not extend the deadline or replace the request automatically.", "端末の時計と元の絶対期限を確認してください。期限の延長や要求の置換を自動で行わないでください。"
	case "message_peer_offline", "message_receipt_pending":
		en, jp = "Waiting for the recipient or exact save evidence. Keep the original request; a missing reply does not prove failure.", "相手への接続または正確な保存の記録を待っています。元の要求を保ち、応答がないことを失敗の証明にしないでください。"
	case "message_cancelled", "message_receiver_cancelled":
		en, jp = "Further sending is stopped. Previously saved text remains on the recipient; check delivery evidence separately.", "追加の送信は停止済みです。相手に保存済みの本文は残るため、到着の記録は別に確認してください。"
	case "message_expired":
		en, jp = "The original sending deadline has passed. Check existing save evidence; expiry does not recall saved text.", "元の送信期限を過ぎました。既存の保存の記録を確認してください。期限切れでも保存済みの本文は回収しません。"
	case "page_changed":
		en, jp = "The list changed. Reload the first page; do not combine pages from different revisions.", "一覧が変わりました。最初のページを取り直し、異なるリビジョンのページを混ぜないでください。"
	case "page_capacity":
		en, jp = "The configured page budget is too small. Review its finite limit; original status and cancellation use the original ID.", "設定されたページ容量が小さすぎます。有限の上限を確認してください。元の ID による状態照会と停止は別に行えます。"
	case "message_already_enqueued":
		en, jp = "This request was already accepted locally. Use its original ID to check status or cancel further sending; it cannot be discarded as a preparation.", "この要求はローカル受付済みです。元の ID で状態照会または追加送信の停止を行ってください。未確定の準備としては破棄できません。"
	default:
		en, jp = "Result not confirmed. Check the original text request and keep its ID, text and deadline unchanged before any further action.", "結果を確認できません。次の操作の前に元のテキスト要求を照会し、ID・本文・期限を変更せず保持してください。"
	}
	return text(ja, en, jp) + " (" + code + ")"
}
func textQueueCLIShow(out io.Writer, dir string, ja bool, r core.TextOutboxRecord, list bool) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n%s: %s\n%s: %s\n%s: %s\n%s: %s\n", text(ja, "Text queue request", "テキスト送信待ち ID"), r.OutboxID, text(ja, "Exact recipient identity", "正確な宛先 ID"), displayText(r.PeerID), text(ja, "Work status", "処理状態"), textQueueCLIStatusLabel(ja, r.Status), text(ja, "Delivery evidence", "到着の記録"), outboxCLIOutcomeLabel(ja, r.DeliveryOutcome), text(ja, "Sending deadline", "送信期限"), r.ExpiresAt.Local().Format(time.RFC3339Nano))
	if r.ReasonCode != "" {
		fmt.Fprintln(&b, textQueueCLIReason(ja, r.ReasonCode))
	}
	fmt.Fprintf(&b, "%s: %d bytes; SHA-256: %s\n", text(ja, "Text", "本文"), r.TextBytes, r.SHA256)
	if r.Text != "" {
		fmt.Fprintf(&b, "%s: %s\n", text(ja, "Text (quoted; escapes show control characters)", "本文（引用表記・制御文字はエスケープ）"), strconv.Quote(r.Text))
	}
	if r.Status == "prepared" {
		if list {
			fmt.Fprintln(&b, text(ja, "Open this original record to review the body before committing.", "確定する前に、この元の記録を開いて本文を確認してください。"))
		} else {
			fmt.Fprintln(&b, text(ja, "Prepared locally; sending is not authorized yet. Confirmation retains the text locally and permits automatic attempts, including after restart, until this exact deadline. Both devices must run Sobalink and become reachable. This text has no files or receive folder.", "ローカルに準備済みで、送信はまだ承認されていません。承認すると本文をローカルに保持し、再起動後もこの正確な期限まで自動で送信を試みます。両端末で Sobalink が起動して接続可能になる必要があります。このテキストにはファイルや受信フォルダーはありません。"))
			fmt.Fprintln(&b, cliCommandExample(dir, "message", "queue", "commit", r.OutboxID, "--process", r.ProcessID, "--revision", r.ReviewRevision, "--confirm"))
			fmt.Fprintln(&b, cliCommandExample(dir, "message", "queue", "discard", r.OutboxID, "--process", r.ProcessID))
		}
	} else if r.Admission == "enqueued" {
		fmt.Fprintln(&b, text(ja, "Accepted locally; receiver-save completion is shown separately in delivery evidence. Cancel stops further sending and does not recall text already saved on the recipient.", "ローカル受付済みです。受信側での保存完了は到着の記録で別に確認します。停止は追加の送信を止め、相手に保存済みの本文を回収しません。"))
		if r.Receipt != nil {
			fmt.Fprintf(&b, "%s: %s\n", text(ja, "Receiver saved at", "受信側の保存日時"), r.Receipt.SavedAt.Format(time.RFC3339Nano))
		}
		if r.Status != "completed" && r.Status != "cancelled" && r.Status != "expired" {
			fmt.Fprintln(&b, cliCommandExample(dir, "message", "queue", "cancel", r.OutboxID))
		}
	} else {
		fmt.Fprintln(&b, text(ja, "Preparation discarded. No sending was authorized.", "準備を破棄しました。送信は承認されていません。"))
	}
	fmt.Fprintln(&b, cliCommandExample(dir, "message", "queue", "show", r.OutboxID))
	n, err := io.WriteString(out, b.String())
	if err == nil && n != b.Len() {
		return io.ErrShortWrite
	}
	return err
}
