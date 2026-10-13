import type { Locale } from './api'
const en = {
  title: 'Outbox', filesTitle: 'Queued files', fileRequests: 'File requests', absent: 'The original request is not present in the healthy outbox. A new send still needs a new explicit review.', newReview: 'Review as a new request', immediate: 'Send now', queue: 'Queue for later', mode: 'File sending mode', deadline: 'Sending deadline', day: '24 hours', week: '7 days', hour: '1 hour',
  review: 'Prepare queue review', queueUntil: 'Queue until', prepared: 'Prepared locally', queued: 'Queued', waiting_for_peer: 'Waiting for device', sending: 'Sending', awaiting_receipt: 'Checking delivery', completed: 'Saved on receiving device', paused: 'Paused', blocked_permission: 'Permission needs review', blocked_recovery: 'Delivery needs review', cancel_requested: 'Stopping further sends', cancelled: 'Further sends stopped', expired: 'Sending deadline passed', discarded: 'Preparation discarded',
  not_attempted: 'Not attempted', unknown: 'May already have arrived', partial: 'Partly saved', confirmed: 'Save confirmed',
  consent: 'The exact selected files and devices will be retried automatically until this deadline, including after an application restart. A local staged copy is kept. Both devices must run Sobalink and become reachable. Receiving approval still applies.',
  localOnly: 'Queued on this device. Delivery is not yet confirmed.',
  check: 'Check original request', uncertain: 'Result not confirmed. Check the original request before starting another send.',
  stop: 'Stop further sends', stopHint: 'Files already saved on receiving devices remain. Stopping does not delete your original files.',
  discard: 'Discard preparation', empty: 'No queued files.', refresh: 'Refresh', details: 'Files and delivery details', request: 'Original request ID', delivery: 'Delivery evidence', work: 'Work status', recipients: 'Recipients', saved: 'Saved', waiting: 'Waiting', unknownCount: 'Unknown',
  unavailable: 'Queueing is unavailable for this device. Check its trust, permission and pause settings.',
  unsupported: 'This device needs a Sobalink upgrade for durable delivery. An immediate send is a separate choice.',
  capability: 'Waiting to check compatibility', expiredReview: 'This prepared review is no longer valid. No send is authorized. Check the original request.',
  staging: 'Preparing a local copy', error: 'Could not confirm the result. Check the original request.', view: 'View outbox', prepareOnly: 'Preparation does not authorize sending. Review the exact files, recipients and deadline before queuing.',
  receipt: 'A confirmed save means the receiver recorded the save durably. It does not mean someone read the file or that the file still exists.',
  pending: 'Pending requests', reason: 'Reason', autoRetry: 'Eligible retries keep the same delivery identities and stop at the original deadline.',
  outbox_storage_busy: 'Local storage is busy. Keep the original request, check its status, then retry the same operation if it is still valid.',
  outbox_evidence_capacity: 'Retained delivery evidence is full. Keep uncertain outcomes for review; clearing visible history does not guarantee more evidence space.',
  durable_evidence_capacity: 'The receiving device’s retained delivery evidence is full. Keep this original request and review capacity on that device; do not resend with a new ID.',
  outbox_retired: 'This original request ID can no longer be admitted. That does not prove non-delivery. Review the original outcome before any new send.', outbox_capacity: 'Outbox capacity is full. Review eligible old work or available staged-copy space; active work is never silently evicted.', outbox_recovery_required: 'Stored outbox evidence needs review. Check the original request and storage; do not replay it as a new send.',
  outbox_capability_unknown: 'Waiting to check compatibility', outbox_peer_upgrade_required: 'The receiving device needs an upgrade; no legacy fallback will run.', outbox_blocked_peer_upgrade_required: 'The receiving device needs an upgrade; no legacy fallback will run.',
  outbox_permission: 'Review the exact device identity and its current permission. A new recipient needs a new review.', outbox_peer_unavailable: 'Waiting for the selected device to become reachable.', outbox_receipt_unknown: 'Delivery evidence is uncertain. Check this original request; do not replay it as a new send.',
} as const
const ja: Record<keyof typeof en, string> = {
  title: '送信待ち', filesTitle: 'ファイルの送信待ち', fileRequests: 'ファイル要求', absent: '正常な送信待ちストアに元の要求はありません。新しく送る場合は、改めて明示的な確認が必要です。', newReview: '新しい要求として確認', immediate: '今すぐ送る', queue: '後で送るために予約', mode: 'ファイルの送信方法', deadline: '送信期限', day: '24時間', week: '7日間', hour: '1時間',
  review: '送信予約の確認を準備', queueUntil: 'まで送信を予約', prepared: 'ローカルに準備済み', queued: '送信待ち', waiting_for_peer: '相手の接続待ち', sending: '送信中', awaiting_receipt: '到着状況を確認中', completed: '相手の端末に保存済み', paused: '一時停止中', blocked_permission: '権限の確認が必要', blocked_recovery: '到着状況の確認が必要', cancel_requested: '追加の送信を停止中', cancelled: '追加の送信を停止済み', expired: '送信期限切れ', discarded: '準備を破棄済み',
  not_attempted: '未試行', unknown: 'すでに届いている可能性があります', partial: '一部を保存済み', confirmed: '保存を確認済み',
  consent: '選んだ正確なファイルと相手への送信を、この期限まで自動で再試行します。アプリの再起動後も再開します。ローカルに準備用コピーを保持します。両方の端末で Sobalink が起動し、接続できる必要があります。既存の受信承認も引き続き必要です。',
  localOnly: 'この端末の送信待ちに登録しました。相手への到着はまだ確認できていません。', check: '元の要求を確認', uncertain: '結果を確認できません。別の送信を始める前に、元の要求を確認してください。',
  stop: '追加の送信を停止', stopHint: '相手の端末に保存済みのファイルは残ります。元の入力ファイルも削除しません。',
  discard: '準備を破棄', empty: '送信待ちのファイルはありません。', refresh: '更新', details: 'ファイルと到着状況の詳細', request: '元の要求 ID', delivery: '到着の確認状況', work: '処理の状態', recipients: '宛先', saved: '保存済み', waiting: '待機中', unknownCount: '不明',
  unavailable: 'この相手には送信を予約できません。信頼・権限・一時停止の設定を確認してください。', unsupported: '永続的な送信には、相手の Sobalink の更新が必要です。今すぐ送る操作は別途選んでください。', capability: '対応状況の確認待ち', expiredReview: 'この準備内容の確認は無効になりました。送信は承認されていません。元の要求を確認してください。',
  staging: 'ローカルコピーを準備中', error: '結果を確認できません。元の要求を確認してください。', view: '送信待ちを表示', prepareOnly: '準備だけでは送信を承認しません。予約前に、正確なファイル・相手・期限を確認してください。',
  receipt: '保存を確認済みとは、相手が保存を永続的に記録したことです。既読や、現在もファイルが存在することは意味しません。', pending: '確認待ちの要求', reason: '理由', autoRetry: '再試行できる場合も同じ送信 ID を使い、元の期限で停止します。',
  outbox_storage_busy: 'ローカルの保存処理が使用中です。元の要求を保持して状態を確認し、有効なら同じ操作を再試行してください。',
  outbox_evidence_capacity: '到着記録の保持容量が上限です。不確かな結果は確認のため保持してください。表示履歴の整理で記録用の空きが増えるとは限りません。',
  durable_evidence_capacity: '相手側の到着記録の保持容量が上限です。元の要求を保持して相手側の容量を確認し、新しい ID で再送しないでください。',
  outbox_retired: 'この元の要求 ID は新規受付に使えません。不達の証明ではありません。新しく送る前に元の結果を確認してください。', outbox_capacity: '送信待ちの容量が不足しています。整理可能な過去の要求や準備コピー用の空きを確認してください。動作中の要求を勝手に削除しません。', outbox_recovery_required: '保存された送信待ちの記録を確認する必要があります。新しい送信として再送せず、元の要求とストレージを確認してください。',
  outbox_capability_unknown: '対応状況の確認待ち', outbox_peer_upgrade_required: '相手の更新が必要です。従来の送信には自動で切り替えません。', outbox_blocked_peer_upgrade_required: '相手の更新が必要です。従来の送信には自動で切り替えません。', outbox_permission: '正確な相手の識別情報と現在の権限を確認してください。別の相手には新しい確認が必要です。', outbox_peer_unavailable: '選んだ相手との接続を待っています。', outbox_receipt_unknown: '到着の記録を確認できません。新しい送信として再送せず、この元の要求を確認してください。',
}
export function outboxText(locale: Locale, key: keyof typeof en): string { return (locale === 'ja' ? ja : en)[key] }
export function outboxReason(locale: Locale, code?: string) { return code && code in en ? outboxText(locale, code as keyof typeof en) : code || '' }
export function outboxDeadline(value: string, locale: Locale) { return new Intl.DateTimeFormat(locale === 'ja' ? 'ja-JP' : 'en', { dateStyle: 'medium', timeStyle: 'long' }).format(new Date(value)) }
export function queueLabel(value: string, locale: Locale) { const date = outboxDeadline(value, locale); return locale === 'ja' ? `${date} ${ja.queueUntil}` : `${en.queueUntil} ${date}` }
