import type { Locale } from './api'
const en = {
  title: 'Queued text', mode: 'Text sending mode', immediate: 'Send text now', queue: 'Queue text', review: 'Review queued text', recipient: 'Recipient', body: 'Exact message', deadline: 'Sending deadline', hour: '1 hour', day: '24 hours', week: '7 days',
  prepareOnly: 'Preparation does not send. Review this one device, the exact text and the deadline before queuing.',
  consent: 'I allow this device to keep this exact text locally and send it to the selected device until the deadline while the sending agent is running.',
  commit: 'Queue this text', discard: 'Discard this preparation', check: 'Check original text request', retryPrepare: 'Retry the same preparation', request: 'Original text request',
  uncertain: 'The result is unconfirmed. Keep this text and its original request ID. Check that request before any new send.',
  preparing: 'Preparing the text review', committing: 'Confirming local queue acceptance',
  localOnly: 'Accepted into the local queue. This does not confirm that the receiving device saved the text.',
  receipt: 'A receiver-saved receipt confirms storage on the selected device. It is not a read receipt.',
  stop: 'Stop further text sends', stopHint: 'Stopping prevents further sends. It cannot recall text already saved by the receiving device.',
  cleanup: 'Clearing visible message history does not erase retained delivery or duplicate-prevention evidence, or cancel queued text.',
  absent: 'The healthy store has no record of this original request. An uncommitted review may have ended with the previous process. A new send requires a fresh explicit review.',
  newReview: 'Start a new text review', expiredReview: 'This preparation is no longer valid in the current process. Check the original request before starting a new review.',
  unavailable: 'Text queuing needs this exact device’s current trust and an active local identity. Mixed identities are not supported. An offline recipient can still be queued.',
  unresolved: 'An earlier text request needs checking in the outbox before another text is queued.',
  empty: 'No queued text requests.', refresh: 'Refresh queued text', details: 'Inspect original text', more: 'Load more text requests', view: 'View text in outbox', work: 'Work status', outcome: 'Delivery evidence', bodyRemoved: 'The local message body is no longer retained.',
  prepared: 'Ready for review', discarded: 'Preparation discarded', queued: 'Queued locally', waiting_peer: 'Waiting for the selected device', delivering: 'Sending text', completed: 'Receiver saved the text', cancel_requested: 'Stopping further sends', cancelled: 'Further sends stopped', expired: 'Deadline passed', paused: 'Paused', blocked: 'Blocked', needs_review: 'Needs review', not_attempted: 'Not yet attempted', unknown: 'Unconfirmed', confirmed: 'Receiver-saved confirmation',
  storage_busy: 'Local storage is busy. Keep the draft and original request. Check its status, then retry the same operation if still valid.',
  message_invalid_response: 'The response did not match the original text request. Keep its ID and check the original status; do not create a replacement.',
  message_not_found: 'The original request was not found in the healthy store.', message_retired: 'This original ID is too old for admission. This does not prove non-delivery. Review its outcome before any new send.',
  message_stale: 'The process or review expired. Check the original request; a replacement requires a fresh explicit review.', message_conflict: 'The original ID is bound to different reviewed content. Keep its original text, recipient and deadline.',
  message_identity_unavailable: 'Start the selected local network so its device identity is available, then check the original request.', message_permission: 'Review the selected device’s identity and current permission. Queuing never expands permission.',
  message_peer_upgrade_required: 'The receiving device needs an upgrade. Text is not sent through the older immediate-message route.', message_peer_offline: 'Waiting for the selected receiving device to reconnect.',
  message_store_recovery_required: 'Local message storage needs review. Preserve the original request and its evidence.', message_storage_uncertain: 'Local storage could not confirm its write. Preserve the original request; do not send it again as a new message.',
  message_active_capacity: 'Active text work is at its limit. Review current work or wait for it to finish; retained delivery evidence is preserved.',
  message_peer_capacity: 'Active text work for this device is at its limit. Review current work for that device or wait for it to finish.',
  message_evidence_capacity: 'Retained delivery evidence is full. Preserve uncertain outcomes for review; clearing visible history does not guarantee more evidence space.',
  message_capacity: 'Message storage or admission capacity is full. Keep existing evidence; history cleanup does not promise to free evidence capacity.',
  message_too_large: 'The text exceeds the current message limit. Discard an existing preparation before reviewing changed text.', message_expired: 'The reviewed sending deadline has passed.',
  message_unknown_lease: 'The receiver’s original delivery evidence could not be matched. Keep this request for review.', message_receipt_invalid: 'The receipt did not match this original text. Its delivery remains unconfirmed.',
  message_already_enqueued: 'This text is already queued. Check the original request; discarding a preparation cannot cancel it.', message_stopping: 'The sending agent is stopping. Check this original request when it restarts.',
  error: 'Could not confirm this operation. Check the original text request.', page_changed: 'The queue changed while loading. Refresh the list from the beginning.',
} as const
const ja: Record<keyof typeof en, string> = {
  title: 'テキストの送信待ち', mode: 'テキストの送信方法', immediate: 'テキストを今すぐ送る', queue: 'テキストを送信待ちに登録', review: '送信待ちのテキストを確認', recipient: '送信先', body: '送信する本文', deadline: '送信期限', hour: '1時間', day: '24時間', week: '7日間',
  prepareOnly: '準備だけでは送信しません。相手1台、正確な本文、期限を確認してから登録してください。',
  consent: 'この送信元に正確な本文を保持し、送信元のエージェントが動作している間、期限まで選んだ相手への送信を許可します。',
  commit: 'このテキストを送信待ちに登録', discard: 'この準備を破棄', check: '元のテキスト要求を確認', retryPrepare: '同じ準備を再試行', request: '元のテキスト要求',
  uncertain: '結果を確認できません。本文と元の要求 ID を保持し、新しく送る前に元の要求を確認してください。',
  preparing: 'テキストの確認を準備中', committing: 'ローカルでの登録結果を確認中',
  localOnly: 'ローカルの送信待ちに登録しました。相手側での本文保存はまだ確認されていません。',
  receipt: '受信保存の確認は、選んだ相手の端末に保存されたことを示します。既読を示すものではありません。',
  stop: 'テキストの追加送信を停止', stopHint: '停止すると追加送信を止めます。相手がすでに保存した本文は回収できません。',
  cleanup: '表示履歴を整理しても、到着や重複防止の記録は消えず、送信待ちのテキストも取り消されません。',
  absent: '正常な保存領域に元の要求が見つかりません。未登録の確認は前のプロセス終了時に失われた可能性があります。新しい送信には、改めて明示的な確認が必要です。',
  newReview: '新しいテキストの確認を始める', expiredReview: 'この準備は現在のプロセスでは有効ではありません。新たな確認の前に、元の要求を照会してください。',
  unavailable: 'テキストの予約には、この相手への現在の信頼と動作中のローカル識別情報が必要です。複合識別情報には未対応です。オフラインの相手にも予約できます。',
  unresolved: '別のテキストを登録する前に、送信待ちで前のテキスト要求を確認してください。',
  empty: 'テキストの送信待ちはありません。', refresh: 'テキストの送信待ちを更新', details: '元のテキストを詳しく確認', more: 'テキスト要求の続きを表示', view: '送信待ちのテキストを見る', work: '処理状況', outcome: '到着の記録', bodyRemoved: 'ローカルの本文は保持されていません。',
  prepared: '確認待ち', discarded: '準備を破棄済み', queued: 'ローカルの送信待ち', waiting_peer: '選んだ相手との接続待ち', delivering: 'テキストを送信中', completed: '相手側で本文を保存済み', cancel_requested: '追加送信を停止中', cancelled: '追加送信を停止済み', expired: '期限切れ', paused: '一時停止中', blocked: '処理を停止', needs_review: '確認が必要', not_attempted: '未試行', unknown: '未確認', confirmed: '受信側の保存を確認済み',
  storage_busy: 'ローカルの保存処理が使用中です。下書きと元の要求を保持し、状態を確認した後、有効なら同じ操作を再試行してください。',
  message_invalid_response: '応答が元のテキスト要求と一致しません。ID を保持して元の状態を確認し、別の要求に置き換えないでください。',
  message_not_found: '正常な保存領域に元の要求が見つかりません。', message_retired: '元の ID は新規受付の期限を過ぎています。不達の証明ではありません。新しく送る前に結果を確認してください。',
  message_stale: 'プロセスまたは確認の有効期間が終了しました。元の要求を確認してください。新しい要求には改めて明示的な確認が必要です。', message_conflict: '元の ID は別の確認内容に結び付いています。元の本文、送信先、期限を保持してください。',
  message_identity_unavailable: '選んだローカルネットワークを起動して識別情報を利用できる状態にし、元の要求を確認してください。', message_permission: '選んだ相手の識別情報と現在の権限を確認してください。予約によって権限が広がることはありません。',
  message_peer_upgrade_required: '相手の更新が必要です。従来の即時メッセージ経路では送信しません。', message_peer_offline: '選んだ相手の再接続を待っています。',
  message_store_recovery_required: 'ローカルのメッセージ保存領域の確認が必要です。元の要求と記録を保持してください。', message_storage_uncertain: 'ローカルへの書き込みを確認できません。元の要求を保持し、新しいメッセージとして再送しないでください。',
  message_active_capacity: '動作中のテキスト要求が上限です。現在の処理を確認するか完了を待ってください。到着記録は保持します。',
  message_peer_capacity: 'この相手への動作中のテキスト要求が上限です。この相手への処理を確認するか完了を待ってください。',
  message_evidence_capacity: '到着記録の保持容量が上限です。不確かな結果は確認のため保持してください。表示履歴の整理で記録用の空きが増えるとは限りません。',
  message_capacity: 'メッセージの保存容量または受付件数が上限です。既存の記録を保持してください。表示履歴の整理で記録用の空きが増えるとは限りません。',
  message_too_large: '本文が現在の上限を超えています。準備済みの場合は、その準備を破棄してから変更後の本文を確認してください。', message_expired: '確認した送信期限が過ぎています。',
  message_unknown_lease: '相手側の元の到着記録と一致しません。この要求を保持して確認してください。', message_receipt_invalid: '受信保存の確認が元の本文と一致しません。到着は未確認です。',
  message_already_enqueued: 'この本文は登録済みです。元の要求を確認してください。準備の破棄では登録済みの送信を取り消せません。', message_stopping: '送信元のエージェントを停止中です。再起動後に元の要求を確認してください。',
  error: '操作の結果を確認できません。元のテキスト要求を確認してください。', page_changed: '表示中に送信待ちが変わりました。最初から一覧を更新してください。',
}
export function textQueueText(locale: Locale, key: keyof typeof en): string { return (locale === 'ja' ? ja : en)[key] }
export function textQueueReason(locale: Locale, code?: string) {
  if (!code) return ''
  if (['storage_busy', 'message_storage_busy', 'message_store_busy'].includes(code)) return textQueueText(locale, 'storage_busy')
  if (code === 'busy') return textQueueText(locale, 'storage_busy')
  if (code === 'message_cancelled') return textQueueText(locale, 'cancelled')
  if (code === 'message_paused') return textQueueText(locale, 'paused')
  if (code === 'message_identity_changed') return textQueueText(locale, 'message_permission')
  if (code === 'message_receipt_pending') return textQueueText(locale, 'unknown')
  if (code === 'message_invalid' || code === 'message_review_required') return textQueueText(locale, 'prepareOnly')
  if (code === 'message_peer_storage_unavailable' || code === 'message_history_unavailable') return textQueueText(locale, 'uncertain')
  if (['message_active_capacity', 'message_peer_capacity', 'message_evidence_capacity'].includes(code)) return textQueueText(locale, code as keyof typeof en)
  if (code.includes('capacity')) return textQueueText(locale, 'message_capacity')
  if (code.includes('storage_uncertain')) return textQueueText(locale, 'message_storage_uncertain')
  if (code.includes('clock') || code === 'message_store_unavailable') return textQueueText(locale, 'message_store_recovery_required')
  if (code in en) return textQueueText(locale, code as keyof typeof en)
  return `${textQueueText(locale, 'error')} (${code})`
}
