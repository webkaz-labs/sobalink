import type { Locale } from './api'
const en = {
  title: 'Upgrade saved Direct LAN pair', intro: 'Review one saved peer and its exact endpoints. This attempt expires within five minutes; it does not change endpoints or grant application access.',
  peer: 'Saved peer', choose: 'Choose a peer', review: 'Review upgrade', apply: 'Apply reviewed upgrade', back: 'Back', refresh: 'Check upgrade status', cancel: 'Cancel upgrade attempt',
  local: 'Local endpoint', remote: 'Peer endpoint', scope: 'Scope', deadline: 'Attempt deadline', revision: 'Review revision', expired: 'This review expired or changed. Review again before applying.',
  invalid: 'The upgrade response could not be verified. Check status before trying again.', unknown: 'The request outcome is unknown. Check status before retrying; closing this panel does not cancel an attempt.',
  preparing: 'Preparing the reviewed pair', exchanging: 'Exchanging pair context', confirming: 'Confirming local saved context', connecting: 'Starting the ordinary connection', connected: 'Ordinary backend started. Network and application readiness require separate checks.', 'network-started': 'Ordinary backend started. Network and application readiness require separate checks.', resume: 'Resume the saved preparation with the newly reviewed deadline', previous: 'Previous preparation deadline',
  failed: 'Upgrade failed. Check the failure code and review again before retrying.', cancelled: 'Upgrade attempt cancelled. Already saved confirmation is not undone.', 'local-confirmed': 'Local context confirmed; ordinary connection is not yet ready.', 'restart-required': 'A fresh process is required before this upgrade can continue.', idle: 'No upgrade attempt is running.',
  restart: 'This browser cannot safely restart the running process. Use the managed upgrade command in a terminal for this same profile. Connections briefly stop, management reopens, and a fresh local sign-in may be required.',
  code: 'Failure code', currentPeer: 'Attempt peer', status: 'Upgrade progress', cancelHint: 'Cancellation stops this attempt. It does not revoke a saved pair or undo a confirmation already saved.',
}
const ja: typeof en = {
  title: '保存済みの直接LANペアをアップグレード', intro: '保存済みの相手と正確なエンドポイントを確認します。試行の有効期限は5分以内です。エンドポイントの変更やアプリへのアクセス許可は行いません。',
  peer: '保存済みの相手', choose: '相手を選択', review: 'アップグレードを確認', apply: '確認した内容でアップグレード', back: '戻る', refresh: 'アップグレードの状態を確認', cancel: 'アップグレードの試行を中止',
  local: 'この端末のエンドポイント', remote: '相手のエンドポイント', scope: '対象範囲', deadline: '試行の期限', revision: '確認のリビジョン', expired: '確認の期限が切れたか、内容が変わりました。適用前にもう一度確認してください。',
  invalid: 'アップグレードの応答を検証できませんでした。再試行の前に状態を確認してください。', unknown: '要求の結果は不明です。再試行の前に状態を確認してください。この画面を閉じても試行は中止されません。',
  preparing: '確認したペアを準備中', exchanging: 'ペアのコンテキストを交換中', confirming: 'この端末に保存したコンテキストを確認中', connecting: '通常接続を開始中', connected: '通常接続のバックエンドを開始しました。ネットワークとアプリの準備状態は別途確認が必要です。', 'network-started': '通常接続のバックエンドを開始しました。ネットワークとアプリの準備状態は別途確認が必要です。', resume: '新たに確認した期限で保存済みの準備を再開します', previous: '以前の準備期限',
  failed: 'アップグレードに失敗しました。エラーコードを確認し、再試行の前に内容を再確認してください。', cancelled: 'アップグレードの試行を中止しました。保存済みの確認は取り消されません。', 'local-confirmed': 'この端末のコンテキストを確認済みです。通常接続はまだ準備できていません。', 'restart-required': 'アップグレードを続けるには新しいプロセスが必要です。', idle: '実行中のアップグレードはありません。',
  restart: 'このブラウザーから実行中のプロセスを安全に再起動することはできません。同じプロファイルのターミナルで管理されたアップグレードコマンドを使用してください。接続が一時停止し、管理画面が開き直されます。ローカルの再サインインが必要な場合があります。',
  code: 'エラーコード', currentPeer: '試行対象の相手', status: 'アップグレードの進行状況', cancelHint: '中止するのはこの試行です。保存済みペアの失効や、保存済みの確認の取り消しは行いません。',
}
export function upgradeText(locale: Locale, key: keyof typeof en) { return (locale === 'ja' ? ja : en)[key] }
