import type { Locale } from './api'
const en = {
  review_required: 'Local approval required', candidate: 'Reviewed candidate; not yet applied', unchanged: 'Unchanged', already_applied: 'Already applied; no renewal', accepted_inactive: 'Accepted but inactive', saved_pending_activation: 'Saved; activation must be checked separately', withdrawn: 'Endpoint withdrawn', applied: 'Peer reports update applied',
  waiting: 'Waiting for this operation. Transport may be interrupted; completion is not yet confirmed.',
  title: 'Signed endpoint updates', intro: 'Review exact endpoints, peers and lifetime before applying. Scope stays unchanged. A move can interrupt existing connections; reconnecting does not mean application success.',
  move: 'Move local endpoint', export: 'Export signed update', reexport: 'Copy existing signed update', delivery: 'Deliver / retry existing update', import: 'Import signed update', reapprove: 'Approve current signed endpoint', follow: 'Allow signed endpoint following', 'disable-follow': 'Stop following', revoke: 'Revoke endpoint approval', recover: 'Offline recovery',
  action: 'Action', peer: 'Peer', choose: 'Choose a peer', endpoint: 'New local endpoint', update: 'Private signed update', lifetime: 'Lifetime', finite: 'Until the exact expiry', permanent: 'No expiry', expires: 'Expiry (RFC3339)', granted: 'Approval starts (RFC3339)',
  review: 'Review changes', apply: 'Apply this exact review', back: 'Back / discard review', refresh: 'Refresh endpoint status', status: 'Endpoint status', consent: 'I approve the exact targets, scope and lifetime shown above.',
  unknown: 'Completion is unknown. Refresh status before reviewing again. Nothing will retry automatically.', invalid: 'The response no longer matches this selection. Refresh status and review again.',
  impact: 'Applying may interrupt transport. Existing TCP connections may close. Saved configuration, active transport and delivery confirmation are separate results. A lost reply is unconfirmed; retry requires a new explicit review and keeps the original proof lifetime.',
  deliveryChoice: 'Deliver the new signed update to this peer after moving', noDelivery: 'Leave unchecked to move without sending an update.', approval: 'Approve this exact signed endpoint with the lifetime below', inspect: 'Inspect signed proof first', inspectHint: 'Inspection alone grants no authority. After inspection, choose approval and review again.',
  operation: 'Signed operation', set: 'Set endpoint', withdraw: 'Withdraw endpoint', cancelRecovery: 'Cancel pending change only where recovery permits', recovery: 'Recovery requires an offline process. Inspect the retained transaction; no automatic rollback or restart is performed.',
  fields: 'Exact review details', result: 'Last result', private: 'This proof is private to the selected peer. Copy it only to that peer.',
  active: 'Active transport', reconnecting: 'Reconnecting: temporary transport interruption', saved_unavailable: 'Saved but unavailable: inspect status before proceeding', recovery_required: 'Recovery required', saved_only: 'Saved only; no active transport confirmed', unconfirmed: 'Delivery unconfirmed',
  stale: 'Status is stale or the connection is unavailable. Refresh before continuing.', noStatus: 'Refresh status to inspect saved peers and authority.',
} as const
const ja: Record<keyof typeof en, string> = {
  review_required: 'ローカルの承認が必要', candidate: '確認候補・未適用', unchanged: '変更なし', already_applied: '適用済み・期限の更新なし', accepted_inactive: '受け入れ済み・未稼働', saved_pending_activation: '保存済み・稼働は別途確認', withdrawn: '接続先を撤回済み', applied: '相手が更新の適用を報告',
  waiting: '操作の完了を待っています。通信が中断する場合があります。完了はまだ確認できていません。',
  title: '署名付き接続先の更新', intro: '適用前に正確な接続先・相手・有効期間を確認します。許可範囲は変更しません。移動中は接続が中断する場合があり、再接続中はアプリの成功を意味しません。',
  move: 'ローカル接続先を移動', export: '署名付き更新を出力', reexport: '既存の署名付き更新をコピー', delivery: '既存の更新を送信・再送', import: '署名付き更新を取り込む', reapprove: '現在の署名付き接続先を承認', follow: '署名付き接続先への追従を許可', 'disable-follow': '追従を停止', revoke: '接続先の承認を取り消す', recover: 'オフライン復旧',
  action: '操作', peer: '相手', choose: '相手を選択', endpoint: '新しいローカル接続先', update: '非公開の署名付き更新', lifetime: '有効期間', finite: '指定した期限まで', permanent: '期限なし', expires: '期限（RFC3339）', granted: '承認開始日時（RFC3339）',
  review: '変更内容を確認', apply: 'この確認内容を適用', back: '戻る・確認内容を破棄', refresh: '接続先の状態を更新', status: '接続先の状態', consent: '上記の正確な対象・範囲・有効期間を承認します。',
  unknown: '完了状態は不明です。状態を更新してから再度確認してください。自動再試行はしません。', invalid: '応答が現在の選択内容と一致しません。状態を更新して再確認してください。',
  impact: '適用中は通信が中断し、既存のTCP接続が切れる場合があります。保存・通信の稼働・配送確認は別の結果です。応答が失われた場合は配送未確認です。再送には再確認が必要で、元の署名の期限を維持します。',
  deliveryChoice: '移動後にこの相手へ新しい署名付き更新を送る', noDelivery: '選択しなければ更新を送信せずに移動します。', approval: '以下の有効期間で、この署名付き接続先を承認', inspect: '先に署名を検査', inspectHint: '検査のみでは権限を付与しません。検査後、承認を選んで再確認してください。',
  operation: '署名する操作', set: '接続先を設定', withdraw: '接続先を撤回', cancelRecovery: '復旧で許可される場合のみ保留中の変更を取り消す', recovery: '復旧にはオフラインのプロセスが必要です。保留中の処理を確認してください。自動の巻き戻しや再起動は行いません。',
  fields: '正確な確認内容', result: '直近の結果', private: 'この署名は選択した相手専用の非公開情報です。その相手にだけ渡してください。',
  active: '通信が稼働中', reconnecting: '再接続中：通信は一時中断', saved_unavailable: '保存済み・利用不可：状態を確認してください', recovery_required: '復旧が必要', saved_only: '保存のみ・通信の稼働は未確認', unconfirmed: '配送未確認',
  stale: '状態が古いか、接続を利用できません。更新してから続けてください。', noStatus: '状態を更新して、保存済みの相手と権限を確認してください。',
}
export function endpointText(locale: Locale, key: keyof typeof en): string { return (locale === 'ja' ? ja : en)[key] }
export function endpointStateText(locale: Locale, value: unknown): string { return typeof value === 'string' && value in en ? endpointText(locale, value as keyof typeof en) : String(value ?? '') }

const fields: Record<string, [string, string]> = {
  revision: ['Review revision', '確認リビジョン'], peerId: ['Peer identity', '相手のID'], endpoint: ['Endpoint', '接続先'], signedEndpoint: ['Signed proposal endpoint', '署名された提案接続先'], previousEndpoint: ['Previous endpoint', '変更前の接続先'], priorEndpoint: ['Prior endpoint', '元の接続先'], destination: ['Delivery destination', '配送先'], destinations: ['Delivery destinations', '配送先一覧'], deliveries: ['Explicit deliveries', '明示的な配送'], scope: ['Unchanged scope', '変更しない許可範囲'], recipientScope: ['Recipient scope', '相手の許可範囲'], family: ['Address family', 'アドレス種別'], prefixes: ['Allowed prefixes', '許可プレフィックス'], scopeDigest: ['Scope digest', '許可範囲のダイジェスト'], scope_digest: ['Scope digest', '許可範囲のダイジェスト'], proofDigest: ['Signed proof digest', '署名のダイジェスト'], proof_digest: ['Signed proof digest', '署名のダイジェスト'], pairBinding: ['Pair binding', 'ペアの結び付け'], action: ['Action', '操作'], operation: ['Signed operation', '署名された操作'], sequence: ['Sequence', '連番'], lifetime: ['Lifetime', '有効期間'], expires: ['Exact expiry', '正確な期限'], granted: ['Approval start', '承認開始日時'], issued: ['Signed time', '署名日時'], approval: ['Exact endpoint approval', '接続先の承認'], follow: ['Following approval', '追従の承認'], active: ['Active', '稼働'], saved: ['Durably saved', '永続保存済み'], state: ['State', '状態'], outcome: ['Outcome', '結果'], peers: ['Saved peers', '保存済みの相手'], authorityRevision: ['Authority revision', '権限リビジョン'], recoveryRequired: ['Recovery required', '復旧が必要'], cleanupPending: ['Cleanup unresolved', '終了処理が未解決'], transactionId: ['Transaction', 'トランザクション'], pendingTransactionId: ['Pending transaction', '保留中のトランザクション'], before: ['Before recovery', '復旧前'], after: ['After recovery', '復旧後'], cancel: ['Cancel pending change', '保留中変更の取消'], canCancel: ['Cancellation supported', '取消可能'], endpointUpdatesEnabled: ['Endpoint update runtime enabled', '接続先更新機能が有効'],
}
export function endpointFieldLabel(locale: Locale, key: string): string { return fields[key]?.[locale === 'ja' ? 1 : 0] ?? key }
