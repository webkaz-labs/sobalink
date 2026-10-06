import type { Locale } from './api'
export const policyEnglish = {
  relayRestart: 'Stop networking before changing relay budgets. Changes take effect on the next network start.', attempts: 'attempts', policy_invalid: 'Choose positive finite relay budgets. Presence connections and candidate attempts must fit the nonzero 16-bit DERP identifier space (1–65535).', network_restart_required: 'Stop sobalink and reopen with start --offline before changing relay resource budgets.',
  title: 'Capacity and history', intro: 'Choose the limits for this device. Review changes before applying them.',
  common: 'Files and messages', history: 'Message history', advanced: 'More limits and resource budgets', logical: 'Usage limits', resources: 'Finite resource budgets',
  default: 'Use default', limited: 'Custom limit', unlimited: 'No policy limit', effective: 'Effective', value: 'Limit',
  resourceHint: 'No policy limit still uses finite resource budgets. Limits on the receiving device also apply. Lowering a budget affects new work; existing data and active work are kept. The device limit counts application permissions and LAN pairs separately; pairing does not grant application permission.',
  retentionHint: 'Retention settings select candidates for an explicit cleanup. Saving settings and receiving new messages never remove history.',
  review: 'Review changes', apply: 'Apply reviewed settings', back: 'Edit choices', applied: 'Capacity settings saved', reload: 'Reload saved settings',
  preview: 'Review effective settings', previewHint: 'These settings do not delete history. Existing data must still fit the storage budgets.',
  cleanupReview: 'Review history cleanup', cleanupHint: 'This review uses the saved retention settings for all devices.', cleanupTitle: 'Review permanent removal',
  remove: 'Messages to remove', retained: 'Messages to keep', cleanupWarning: 'Removing these messages permanently deletes their local history. This cannot be undone.', cleanupApply: 'Permanently remove reviewed messages', cleanupDone: 'Reviewed message history removed', nothing: 'No messages match the saved cleanup settings.',
  invalid: 'Choose a positive whole number within the supported range.', bytes: 'bytes', entries: 'entries', seconds: 'seconds', levels: 'levels', listeners: 'listeners', connections: 'connections', sessions: 'sessions', packets: 'packets', streams: 'streams',
  policy_revision_conflict: 'Settings or saved configuration changed. Review your choices again before applying.',
  history_revision_conflict: 'History or retention settings changed. Review cleanup again before removing messages.',
  policy_in_use: 'The budget cannot hold existing data. Increase it or review an explicit history cleanup.',
  policy_unsupported: 'This setting is not adjustable by the current backend. Reload the saved settings and review the details.',
} as const
export const policyJapanese: Record<keyof typeof policyEnglish, string> = {
  relayRestart: 'リレー予算を変更する前にネットワークを停止してください。次回のネットワーク起動時に反映します。', attempts: '試行', policy_invalid: 'リレー予算には正の有限整数を指定します。同時リレー接続数と候補試行数は、ゼロを除く16ビットのDERP識別子空間（1〜65535）以内です。', network_restart_required: 'リレーのリソース予算を変更するには、sobalink を停止して start --offline で開き直してください。',
  title: '容量と履歴', intro: 'この端末の上限を選び、変更内容を確認してから適用します。',
  common: 'ファイルとメッセージ', history: 'メッセージ履歴', advanced: 'その他の上限とリソース予算', logical: '利用上限', resources: '有限のリソース予算',
  default: '既定値を使う', limited: '上限を指定', unlimited: '利用上限なし', effective: '実効値', value: '上限',
  resourceHint: '利用上限なしでも、有限のリソース予算は適用されます。受信側の上限も適用されます。予算を下げると新しい処理に反映され、既存データや処理中の作業は維持されます。端末数の上限はアプリの通信許可とLANペアリングに別々に適用され、ペアリングだけでアプリの通信は許可されません。',
  retentionHint: '保持設定は、確認して整理する履歴の候補を選びます。設定の保存や新しいメッセージの受信では履歴を削除しません。',
  review: '変更を確認', apply: '確認した設定を適用', back: '選択を編集', applied: '容量設定を保存しました', reload: '保存済み設定を再読み込み',
  preview: '実効設定を確認', previewHint: 'この設定では履歴を削除しません。保存済みデータを保持できるリソース予算が必要です。',
  cleanupReview: '履歴の整理を確認', cleanupHint: 'すべてのデバイスについて、保存済みの保持設定で候補を確認します。', cleanupTitle: '完全削除する内容を確認',
  remove: '削除するメッセージ', retained: '残すメッセージ', cleanupWarning: 'この端末の履歴から対象メッセージを完全に削除します。元に戻すことはできません。', cleanupApply: '確認したメッセージを完全削除', cleanupDone: '確認したメッセージ履歴を削除しました', nothing: '保存済み設定で削除の対象になるメッセージはありません。',
  invalid: '対応する範囲内で正の整数を入力してください。', bytes: 'バイト', entries: '件', seconds: '秒', levels: '階層', listeners: '待受', connections: '接続', sessions: 'セッション', packets: 'パケット', streams: 'ストリーム',
  policy_revision_conflict: '設定または保存済みの構成が変わりました。選択内容をもう一度確認してから適用してください。',
  history_revision_conflict: '履歴または保持設定が変わりました。削除前に整理する内容をもう一度確認してください。',
  policy_in_use: '既存データを保持できない予算です。予算を増やすか、履歴の整理を確認してください。',
  policy_unsupported: '現在の接続方式では変更できない設定です。保存済み設定を再読み込みし、詳細を確認してください。',
}
const labels: Record<string, [string, string]> = {
 relayPresenceConnections: ['Active relay presence connections', '同時に維持するリレー接続数'], relayCandidateAttempts: ['Sequential relay attempts per connection', '接続1回ごとのリレー候補試行数'], relayTLSConnections: ['Hosted relay TLS connections', 'ホストするリレーのTLS接続数'], relayAdmissionConnections: ['Hosted relay admission connections', 'ホストするリレーの入場確認接続数'],
  savedServices: ['Saved services', '保存するサービス'], trustedPeers: ['Allowed devices', '許可する端末'], sharePeers: ['Devices per share', '共有ごとの端末'], rangePolicies: ['Port range rules', 'ポート範囲ルール'], portIntervals: ['Port intervals', 'ポート区間'], groups: ['Service groups', 'サービスグループ'], groupMembers: ['Services per group', 'グループ内のサービス'],
  pathDepth: ['Path depth', 'パスの階層数'], pathBytes: ['Path length', 'パスのバイト数'], batchEntries: ['Items per batch', 'バッチ内の項目数'], fileBytes: ['File size', 'ファイル単体の容量'], batchBytes: ['Batch size', 'バッチの容量'], messageBytes: ['Message size', 'メッセージの容量'], messageHistoryEntries: ['Messages to retain', '保持するメッセージ数'], messageHistoryBytes: ['History size to retain', '保持する履歴の容量'], messageHistoryAgeSeconds: ['History age to retain', '履歴の保持期間'], transferHistoryEntries: ['Transfer records to retain', '保持する転送記録数'], receiveWaitSeconds: ['Wait for file acceptance', 'ファイル受信許可の待ち時間'], fileTransferSeconds: ['File transfer duration', 'ファイル転送の時間'], stagingSeconds: ['File preparation duration', 'ファイル準備の時間'],
  workerFrameBytes: ['Worker frame memory', 'ワーカーフレームのメモリー'], workerRequests: ['Concurrent worker requests', 'ワーカーの同時要求数'], workerHandles: ['Active worker handles', 'ワーカーの有効ハンドル数'],
  lanStateBytes: ['LAN identity and pairing storage', 'LANの識別情報とペアリングの保存容量'], stagingInventoryEntries: ['Retained-file inspection entries', '保存済みファイルの検査項目数'], stagingInventoryDepth: ['Retained-file inspection depth', '保存済みファイルの検査階層'], messageStorageBytes: ['Message storage', 'メッセージ保存容量'], messageTextBytes: ['Message text memory', 'メッセージ本文のメモリー'], profileBytes: ['Saved configuration storage', '設定の保存容量'], discoveryBytes: ['Service discovery data', 'サービス検出データ容量'], materializedListeners: ['Local listeners', 'ローカルの待受数'], tcpConnections: ['TCP connections', 'TCP接続数'], tcpPerPolicy: ['TCP connections per rule', 'ルールごとのTCP接続数'], tcpPerPeer: ['TCP connections per device', '端末ごとのTCP接続数'], udpSessions: ['UDP sessions', 'UDPセッション数'], udpPerPolicy: ['UDP sessions per rule', 'ルールごとのUDPセッション数'], udpQueuedBytes: ['UDP queue memory', 'UDPキューの容量'], udpPolicyQueuedBytes: ['UDP queue memory per rule', 'ルールごとのUDPキュー容量'], udpQueuePackets: ['UDP queue packets', 'UDPキューのパケット数'], diskReserveBytes: ['Transfer free-space reserve', '転送時に残す空き容量'], transferSpoolBytes: ['Outgoing file staging storage', '送信準備用の保存容量'], receiveReservedBytes: ['Reserved receiving storage', '受信用に確保する容量'], transferManifestBytes: ['Batch file-list memory', 'バッチ内の一覧の容量'], transferMetadataBytes: ['Transfer record storage', '転送記録の保存容量'], transferPending: ['Pending transfers', '保留する転送数'], transferPendingPerPeer: ['Pending transfers per device', '端末ごとの保留転送数'], transferConcurrentFiles: ['Concurrent file transfers', '同時に転送するファイル数'], transferConcurrentPerPeer: ['Concurrent transfers per device', '端末ごとの同時転送数'], pageBytes: ['List page size', '一覧ページの容量'], pageEntries: ['List page entries', '一覧ページの項目数'],
}
export function policyText(locale: Locale, key: string): string { return (locale === 'ja' ? policyJapanese : policyEnglish)[key as keyof typeof policyEnglish] || '' }
export function policyLabel(locale: Locale, key: string) { return labels[key]?.[locale === 'ja' ? 1 : 0] || key }
