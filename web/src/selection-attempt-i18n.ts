import type { Locale } from './api'

export const selectionAttemptEnglish = {
  start: 'Start not confirmed', stop: 'Stop not confirmed',
  explanation: 'These are observed states for the reviewed services. A stopped state alone does not tell us whether a service failed, was rolled back, or was never attempted. Review the selection again before starting or stopping.',
  atReview: 'At review', afterRefresh: 'At result refresh',
  refreshing: 'Refreshing the reviewed services’ states…',
  unavailable: 'The state refresh did not complete. Current member states are unknown.',
  unknown: 'Unknown', refresh: 'Refresh member states', dismiss: 'Dismiss result',
} as const

export const selectionAttemptJapanese: Record<keyof typeof selectionAttemptEnglish, string> = {
  start: '開始結果を確認できません', stop: '停止結果を確認できません',
  explanation: '確認したサービスごとに、観測できた状態を表示します。停止状態だけでは、失敗・開始後の取り消し・未実行のどれかは分かりません。開始・停止する前に対象をもう一度確認してください。',
  atReview: '操作前の確認時', afterRefresh: '結果の状態更新時',
  refreshing: '確認したサービスの状態を更新しています…',
  unavailable: '状態の更新が完了しなかったため、各サービスの現在の状態は不明です。',
  unknown: '不明', refresh: '各サービスの状態を更新', dismiss: '結果を閉じる',
}

export function selectionAttemptText(locale: Locale, key: keyof typeof selectionAttemptEnglish) {
  return (locale === 'ja' ? selectionAttemptJapanese : selectionAttemptEnglish)[key]
}
